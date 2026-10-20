package subtitles

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"math/rand"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The start of a viewing: untimed subtitles are re-fitted sooner, the next episode is timed
// before it opens, a switch to a file of the same cut keeps the timing, and moves near the
// viewer are logged.

// newPreTest is a service with live timing whose file reads come from its true cues.
func newPreTest(t *testing.T, file []span) (*Service, *int) {
	svc, reads := newSyncTest(t, file)
	svc.cfg.LiveTiming = true
	svc.sync.onGrow = func(*liveFile) {}
	return svc, reads
}

// waitFlight waits until the background job key ended.
func waitFlight(t *testing.T, svc *Service, key string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for svc.flights.running(key) {
		if time.Now().After(deadline) {
			t.Fatalf("%s still runs", key)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startsNear(cues []liveCue, want []span, i int) bool {
	return len(cues) == len(want) && math.Abs(cues[i].start-want[i].start) < 0.15
}

// A subtitle first served untimed is re-fitted every 10 s of audio (30 s once it is timed).
func TestUntimedViewingRefitsSooner(t *testing.T) {
	svc := newLiveTest(t, episode)
	grows := 0
	svc.sync.onGrow = func(*liveFile) { grows++ }
	file := dialogue(47, episode)
	inner := cacheProviderSub(t, svc, "1|soon|2|10|1", subtitleFor(file, 1, func(float64) float64 { return 1.5 }))
	id := wrapSyncID(inner, syncHash, 1)
	if a, _ := svc.FileFor(context.Background(), id, 0); a.Sync != syncStatePending {
		t.Fatalf("first: %q", a.Sync)
	}
	f := svc.sync.liveFile(syncHash, 1)
	if !f.pending {
		t.Fatal("an untimed first answer does not mark the file")
	}
	s := play(t, svc, file, 0, 60)
	defer s.Close()
	if grows < 5 {
		t.Fatalf("%d re-fits asked for in 60 s of audio, want one per 10 s", grows)
	}
	injectLive(f, file, []int{0, 1, 2, 3})
	svc.sync.refitFile(f)
	if a, _ := svc.FileFor(context.Background(), id, 0); a.Sync != syncStateLive || f.pending {
		t.Fatalf("timed: %q, pending %v", a.Sync, f.pending)
	}
	grows = 0
	feed(s, speechPCM(file, 60, 60, 2), 60*2*syncSampleRate)
	if grows > 2 {
		t.Fatalf("%d re-fits asked for in 60 s once timed", grows)
	}
}

// The first request of a viewing re-fits at once when the file was heard before it.
func TestFirstRequestKicksARefit(t *testing.T) {
	svc := newLiveTest(t, episode)
	kicks := 0
	svc.sync.onGrow = func(*liveFile) { kicks++ }
	file := dialogue(53, episode)
	injectLive(svc.sync.liveFile(syncHash, 1), file, []int{0, 1})
	inner := cacheProviderSub(t, svc, "1|kick|2|10|1", subtitleFor(file, 1, func(float64) float64 { return -2 }))
	id := wrapSyncID(inner, syncHash, 1)
	svc.FileFor(context.Background(), id, 130)
	if kicks != 1 {
		t.Fatalf("%d re-fits asked for by the first request", kicks)
	}
	svc.FileFor(context.Background(), id, 145) // the same viewing
	if kicks != 1 {
		t.Fatalf("%d re-fits asked for by a poll", kicks)
	}
}

// A file with an untimed subtitle waits the short gap between re-fits, also when it became so
// during a long wait.
func TestRefitWaitFollowsPending(t *testing.T) {
	svc := newLiveTest(t, episode)
	y := svc.sync
	y.refitDelay, y.refitGap, y.pendingGap = 0, time.Hour, 10*time.Millisecond
	f := y.liveFile(syncHash, 1)
	f.mu.Lock()
	f.lastFit = time.Now()
	f.mu.Unlock()
	done := make(chan struct{})
	go func() {
		y.waitRefit(f)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("did not wait the gap")
	case <-time.After(50 * time.Millisecond):
	}
	f.setPending(true)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an untimed subtitle waits the long gap")
	}
}

// roomFake is a ReadRoom with a fixed answer (bytes/s).
type roomFake float64

func (r roomFake) RoomToRead(string) float64 { return float64(r) }

// roomVar is a ReadRoom whose answer changes (bytes/s).
type roomVar struct {
	mu   sync.Mutex
	rate float64
}

func (r *roomVar) RoomToRead(string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rate
}

func (r *roomVar) set(rate float64) {
	r.mu.Lock()
	r.rate = rate
	r.mu.Unlock()
}

// The next episode's subtitle is timed before it opens without counting as a viewing; when it
// opens, the fit shows once a re-fit with the played audio backed it.
func TestPreTimingIsNotAViewing(t *testing.T) {
	file := dialogue(41, episode)
	svc, reads := newPreTest(t, file)
	sub := subtitleFor(file, 1, func(float64) float64 { return 2.5 })
	inner := cacheProviderSub(t, svc, "1|pre|2|8|1", sub)
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	ctx := context.Background()

	if state, err := svc.PreTime(ctx, id); err != nil || state != syncStatePending {
		t.Fatalf("pre-timing: %q %v", state, err)
	}
	waitFlight(t, svc, "sync|"+key)
	m := svc.sync.meta(key)
	if m == nil || !m.fits() || !m.Pre || !m.Live || *reads == 0 {
		t.Fatalf("pre-timed: %+v, %d reads", m, *reads)
	}
	svc.sync.mu.Lock()
	_, noted := svc.sync.served[key]
	svc.sync.mu.Unlock()
	if noted {
		t.Fatal("pre-timing counted as a viewing")
	}
	n := *reads
	if state, _ := svc.PreTime(ctx, id); state != syncStateSynced || *reads != n {
		t.Fatalf("asked again: %q, %d more reads", state, *reads-n)
	}

	// The episode opens: the subtitle as it is until the played audio backs the fit
	a, _ := svc.FileFor(ctx, id, 0)
	if a.Sync != syncStatePending || !a.Partial || !startsNear(vttCues(a.VTT), sub, 40) {
		t.Fatalf("open: %q partial %v", a.Sync, a.Partial)
	}
	f := svc.sync.liveFile(syncHash, 1)
	injectLive(f, file, []int{0, 1, 2})
	svc.sync.refitFile(f)
	a, _ = svc.FileFor(ctx, id, 30)
	if a.Sync != syncStateLive || !a.Partial || !startsNear(vttCues(a.VTT), file, 40) || svc.sync.meta(key).Pre {
		t.Fatalf("backed: %q partial %v", a.Sync, a.Partial)
	}
}

// Pre-timing reads a file only while the playing streams leave room (a TorBox file too: the same
// home line), no slower than the file plays, and each read keeps to that room. Without room it
// gives up after the wait, and the episode's own timing is not held back by a failure.
func TestPreTimingWaitsForRoom(t *testing.T) {
	file := dialogue(43, episode)
	svc, reads := newPreTest(t, file)
	svc.sync.roomWait, svc.sync.roomPoll = 50*time.Millisecond, 10*time.Millisecond
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip", Bitrate: 1 << 20}, nil
	}
	debridHook.Store(&DebridSource{FileURL: func(h string, i int) string {
		if h == syncHash && i == 1 {
			return "http://127.0.0.1:9/f/" + h + "/1"
		}
		return ""
	}})
	t.Cleanup(func() { debridHook.Store(nil) })
	var mu sync.Mutex
	var rates []float64
	truth := svc.sync.read
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		mu.Lock()
		rates = append(rates, readRate(ctx))
		mu.Unlock()
		return truth(ctx, url, p, start, length)
	}
	inner := cacheProviderSub(t, svc, "1|room|2|8|1", subtitleFor(file, 1, func(float64) float64 { return -1 }))
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	ctx := context.Background()
	for _, room := range []float64{0, 1 << 19} { // none; less than the file's bitrate
		svc.cfg.ReadRoom = roomFake(room)
		if state, _ := svc.PreTime(ctx, id); state != syncStatePending {
			t.Fatalf("room %.0f: %q", room, state)
		}
		waitFlight(t, svc, "sync|"+key)
		if len(rates) != 0 || svc.sync.meta(key) != nil || svc.sync.recentlyFailed(key) {
			t.Fatalf("room %.0f: %d reads, meta %+v, failed %v", room, len(rates), svc.sync.meta(key), svc.sync.recentlyFailed(key))
		}
	}
	svc.cfg.ReadRoom = roomFake(3 << 20)
	svc.PreTime(ctx, id)
	waitFlight(t, svc, "sync|"+key)
	if m := svc.sync.meta(key); m == nil || !m.Pre || *reads == 0 {
		t.Fatalf("not pre-timed: %+v", m)
	}
	for _, r := range rates {
		if r != 3<<20 {
			t.Fatalf("a read at %.0f B/s", r)
		}
	}
}

// The playing stream takes the line once the pre-timing read its first window: the next read
// (the speech read with that own-track window) waits, and the job gives up without marking the
// timing failed (before, the room was checked once and 11 more reads went on).
func TestPreTimingChecksRoomBeforeEveryRead(t *testing.T) {
	file := dialogue(71, episode)
	svc, _ := newPreTest(t, file)
	svc.sync.roomWait, svc.sync.roomPoll = 50*time.Millisecond, 10*time.Millisecond
	room := &roomVar{rate: 1 << 20}
	svc.cfg.ReadRoom = room
	truth := svc.sync.read
	var mu sync.Mutex
	all := 0
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		mu.Lock()
		all++
		mu.Unlock()
		room.set(0)
		return truth(ctx, url, p, start, length)
	}
	inner := cacheProviderSub(t, svc, "1|flip|2|8|1", subtitleFor(file, 1, func(float64) float64 { return 1 }))
	key, _ := syncKey(inner, syncHash, 1)
	if _, err := svc.PreTime(context.Background(), wrapSyncID(inner, syncHash, 1)); err != nil {
		t.Fatal(err)
	}
	waitFlight(t, svc, "sync|"+key)
	mu.Lock()
	defer mu.Unlock()
	if all != 1 || svc.sync.meta(key) != nil || svc.sync.recentlyFailed(key) {
		t.Fatalf("%d reads, meta %+v, failed %v", all, svc.sync.meta(key), svc.sync.recentlyFailed(key))
	}
}

// Once the episode opens (its subtitle is asked for in a viewing) the pre-timing's waiting reads
// go on like any timing's, unthrottled.
func TestPreTimingReadsOnceTheFilePlays(t *testing.T) {
	file := dialogue(73, episode)
	svc, reads := newPreTest(t, file)
	svc.sync.roomPoll = 10 * time.Millisecond
	svc.cfg.ReadRoom = roomFake(0)
	inner := cacheProviderSub(t, svc, "1|open|2|8|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	ctx := context.Background()
	svc.PreTime(ctx, id)
	time.Sleep(100 * time.Millisecond)
	if *reads != 0 || !svc.flights.running("sync|"+key) {
		t.Fatalf("no room: %d reads, waiting %v", *reads, svc.flights.running("sync|"+key))
	}
	svc.FileFor(ctx, id, 0)
	waitFlight(t, svc, "sync|"+key)
	if m := svc.sync.meta(key); m == nil || !m.fits() || *reads == 0 {
		t.Fatalf("opened: %+v, %d reads", m, *reads)
	}
}

// bigStartFile is an episode whose busy dialogue starts after a 100 s intro, with a title line
// at 60 s.
func bigStartFile(seed int64) []span {
	rng := rand.New(rand.NewSource(seed))
	out := []span{{60, 63}}
	for t := 100.0; t < episode-60; {
		d := 1 + rng.Float64()*1.5
		out = append(out, span{t, t + d})
		t += d + 0.8 + rng.Float64()*1.2
		if rng.Float64() < 0.03 {
			t += 20
		}
	}
	return out
}

// newBigPreTest is a service with live timing whose file is above the byte budget (4K: 2.8 MB/s,
// with a PGS track); its reads come from the file's true lines and are recorded.
func newBigPreTest(t *testing.T, file []span) (*Service, *[]mediaProbe, *[]float64) {
	svc, _ := newPreTest(t, file)
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, Center: true, OwnSub: 2, SubCodec: "hdmv_pgs_subtitle",
			Bitrate: 2.8 * (1 << 20)}, nil
	}
	var mu sync.Mutex
	var probes []mediaProbe
	var starts []float64
	svc.sync.read = func(_ context.Context, _ string, p mediaProbe, start, _ float64) (refWindow, error) {
		mu.Lock()
		probes, starts = append(probes, p), append(starts, start)
		mu.Unlock()
		w := liveWindowsOf(file, []float64{start}, 0, 0)[0]
		w.Kind = "audio"
		return w, nil
	}
	return svc, &probes, &starts
}

// The next episode's file above the byte budget: its pre-timing reads the speech of 3 minutes
// from just before the subtitle's dialogue (not the own track, which waits for its check) and
// the subtitle is pre-timed from them; it shows once the played audio backs it. Nothing is read
// again, and a timing that is not a pre-timing never reads such a file.
func TestPreTimingReadsTheStartOfABigFile(t *testing.T) {
	file := bigStartFile(89)
	svc, probes, starts := newBigPreTest(t, file)
	ctx := context.Background()
	sub := subtitleFor(file, 1, func(float64) float64 { return 0.8 })
	inner := cacheProviderSub(t, svc, "1|big|2|8|1", sub)
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)

	if state, err := svc.PreTime(ctx, id); err != nil || state != syncStatePending {
		t.Fatalf("pre-timing: %q %v", state, err)
	}
	waitFlight(t, svc, "sync|"+key)
	want := []float64{44, 104, 164} // the title line at 60 s - 0.8 s starts the first 5 lines within a minute
	if !slices.Equal(*starts, want) {
		t.Fatalf("read at %v, want %v", *starts, want)
	}
	for _, p := range *probes {
		if p.OwnSub >= 0 {
			t.Fatal("the own track was read")
		}
	}
	m := svc.sync.meta(key)
	if m == nil || !m.fits() || !m.Pre || !m.Live || math.Abs(m.Fit.Segments[0].Offset-0.8) > 0.15 {
		t.Fatalf("not pre-timed: %+v", m)
	}
	fk := syncFileKey(syncHash, 1)
	st, release := svc.sync.refState(fk)
	st.mu.Lock()
	ref := *st.ref
	st.mu.Unlock()
	release()
	if !ref.Skipped || !ref.Start || len(ref.Windows) != 3 {
		t.Fatalf("reference: skipped %v start %v, %d windows", ref.Skipped, ref.Start, len(ref.Windows))
	}

	// Another subtitle's pre-timing and the viewing's own timing read nothing more
	other := cacheProviderSub(t, svc, "1|big2|2|8|1", subtitleFor(file, 1, func(float64) float64 { return -1.5 }))
	otherKey, _ := syncKey(other, syncHash, 1)
	svc.PreTime(ctx, wrapSyncID(other, syncHash, 1))
	waitFlight(t, svc, "sync|"+otherKey)
	if len(*starts) != 3 {
		t.Fatalf("read again: %v", *starts)
	}

	// The episode opens: the subtitle as it is until the played audio backs the fit
	a, _ := svc.FileFor(ctx, id, 0)
	if a.Sync != syncStatePending || !startsNear(vttCues(a.VTT), sub, 40) {
		t.Fatalf("open: %q", a.Sync)
	}
	f := svc.sync.liveFile(syncHash, 1)
	injectLive(f, file, []int{2})
	svc.sync.refitFile(f)
	a, _ = svc.FileFor(ctx, id, 100)
	if a.Sync != syncStateLive || !startsNear(vttCues(a.VTT), file, 40) || svc.sync.meta(key).Pre {
		t.Fatalf("backed: %q", a.Sync)
	}
	if len(*starts) != 3 {
		t.Fatalf("read while it plays: %v", *starts)
	}
}

// A first timing that is not a pre-timing does not read the start of a file above the byte budget
// (a read beside its own playing stream would take its line); a later pre-timing still does.
func TestOnlyPreTimingReadsTheStartOfABigFile(t *testing.T) {
	file := bigStartFile(97)
	svc, _, starts := newBigPreTest(t, file)
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|first|2|8|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	key, _ := syncKey(inner, syncHash, 1)
	base, _ := svc.File(ctx, inner)
	svc.sync.startTiming(inner, syncHash, 1, base, false)
	waitFlight(t, svc, "sync|"+key)
	if len(*starts) != 0 || svc.sync.meta(key) != nil {
		t.Fatalf("read at %v, meta %+v", *starts, svc.sync.meta(key))
	}
	svc.PreTime(ctx, wrapSyncID(inner, syncHash, 1))
	waitFlight(t, svc, "sync|"+key)
	if len(*starts) != 3 {
		t.Fatalf("pre-timing read at %v", *starts)
	}
}

// The start reads stop once the file plays: its tap hears that stretch, and a read beside its own
// stream would take its line. The pass is not tried again.
func TestStartReadsStopWhenTheFilePlays(t *testing.T) {
	file := bigStartFile(101)
	svc, _, starts := newBigPreTest(t, file)
	svc.sync.roomPoll = 10 * time.Millisecond
	svc.cfg.ReadRoom = roomFake(0)
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|stop|2|8|1", subtitleFor(file, 1, func(float64) float64 { return 1 }))
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	svc.PreTime(ctx, id)
	time.Sleep(100 * time.Millisecond)
	if len(*starts) != 0 || !svc.flights.running("sync|"+key) {
		t.Fatalf("no room: read at %v, waiting %v", *starts, svc.flights.running("sync|"+key))
	}
	svc.FileFor(ctx, id, 0)
	waitFlight(t, svc, "sync|"+key)
	if len(*starts) != 0 {
		t.Fatalf("read at %v while the file plays", *starts)
	}
	st, release := svc.sync.refState(syncFileKey(syncHash, 1))
	defer release()
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.ref.Start || len(st.ref.Windows) != 0 {
		t.Fatalf("reference: start %v, %d windows", st.ref.Start, len(st.ref.Windows))
	}
}

// A start read cut when the playing streams take the line keeps its windows; the next pre-timing
// reads only the rest.
func TestCutStartReadGoesOnNextTime(t *testing.T) {
	file := bigStartFile(103)
	svc, _, starts := newBigPreTest(t, file)
	svc.sync.roomWait, svc.sync.roomPoll = 50*time.Millisecond, 10*time.Millisecond
	room := &roomVar{rate: 4 << 20}
	svc.cfg.ReadRoom = room
	truth := svc.sync.read
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		room.set(0)
		return truth(ctx, url, p, start, length)
	}
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|cut|2|8|1", subtitleFor(file, 1, func(float64) float64 { return 1 }))
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	svc.PreTime(ctx, id)
	waitFlight(t, svc, "sync|"+key)
	if len(*starts) != 1 || svc.sync.recentlyFailed(key) {
		t.Fatalf("cut: read at %v, failed %v", *starts, svc.sync.recentlyFailed(key))
	}
	room.set(4 << 20)
	svc.sync.read = truth
	svc.PreTime(ctx, id)
	waitFlight(t, svc, "sync|"+key)
	if len(*starts) != 3 || (*starts)[1] == (*starts)[0] || (*starts)[2] == (*starts)[0] {
		t.Fatalf("next time: read at %v", *starts)
	}
	if m := svc.sync.meta(key); m == nil || !m.fits() || !m.Pre {
		t.Fatalf("not pre-timed: %+v", m)
	}
}

// The start windows begin just before the subtitle's first stretch of dialogue (a lone credit
// line before it does not count), as many as the byte budget allows, none below what a live fit
// needs.
func TestStartWindows(t *testing.T) {
	cues := func(starts ...float64) []liveCue {
		var out []liveCue
		for _, s := range starts {
			out = append(out, liveCue{start: s, end: s + 2})
		}
		return out
	}
	fourK := mediaProbe{Duration: 3393, Bitrate: 2.82 * (1 << 20)}
	for _, c := range []struct {
		name string
		p    mediaProbe
		cues []liveCue
		want []float64
	}{
		{"after the intro", fourK, cues(90.6, 94, 108.2, 113.5, 119.9, 123, 127), []float64{75, 135, 195}},
		{"a credit line first", fourK, cues(1, 100, 104, 108, 112, 116), []float64{85, 145, 205}},
		{"from the start", fourK, cues(3, 5, 8, 11, 14), []float64{0, 60, 120}},
		{"near the end", mediaProbe{Duration: 400, Bitrate: 2.82 * (1 << 20)}, cues(300, 302, 304, 306, 308), []float64{220, 280, 340}},
		{"two fit the budget", mediaProbe{Duration: 3393, Bitrate: 5 * (1 << 20)}, cues(10, 12, 14, 16, 18), []float64{0, 60}},
		{"one fits: none", mediaProbe{Duration: 3393, Bitrate: 8 * (1 << 20)}, cues(10, 12, 14, 16, 18), nil},
		{"no dialogue", fourK, cues(10, 100, 200, 300, 400, 500), nil},
	} {
		if got := startWindows(c.p, c.cues); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A switch to another file of the same cut serves the fit the subtitle has on the first file at
// once; a file of another length starts from the subtitle as it is.
func TestSameCutKeepsTheFit(t *testing.T) {
	file := dialogue(59, episode)
	svc, _ := newPreTest(t, file)
	const same, other = "5c8f6d1e2a3b4c5d6e7f8091a2b3c4d5e6f70812", "6d9f7e2f3b4c5d6e7f8091a2b3c4d5e6f7081923"
	length := map[string]float64{syncHash: episode, same: episode + 0.02, other: episode + 0.4}
	svc.sync.probe = func(_ context.Context, url string) (mediaProbe, error) {
		for h, d := range length {
			if strings.Contains(url, h) {
				return mediaProbe{Duration: d, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}, nil
			}
		}
		return mediaProbe{}, errors.New("unknown file")
	}
	block := make(chan struct{})
	truth := readTruth(file, new(int))
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		if !strings.Contains(url, syncHash) { // the new files' own reads take long
			select {
			case <-block:
			case <-ctx.Done():
			}
		}
		return truth(ctx, url, p, start, length)
	}
	sub := subtitleFor(file, 1, func(float64) float64 { return 2.5 })
	inner := cacheProviderSub(t, svc, "1|cut|2|7|1", sub)
	ctx := context.Background()
	saved := syncFirstWait
	syncFirstWait = 500 * time.Millisecond
	t.Cleanup(func() { syncFirstWait = saved })

	tap := svc.OpenSpeech(syncHash, 1, 0) // the first file plays
	defer tap.Close()
	if a, _ := svc.FileFor(ctx, wrapSyncID(inner, syncHash, 1), 300); a.Sync != syncStateLive || !startsNear(vttCues(a.VTT), file, 40) {
		t.Fatalf("first file: %q", a.Sync)
	}
	a, _ := svc.FileFor(ctx, wrapSyncID(inner, same, 1), 330)
	key, _ := syncKey(inner, same, 1)
	m := svc.sync.meta(key)
	if a.Sync != syncStateLive || !startsNear(vttCues(a.VTT), file, 40) || m == nil || m.SameCut != syncFileKey(syncHash, 1) || !m.Live {
		t.Fatalf("same cut: %q %+v", a.Sync, m)
	}
	if m.RateWindows != 0 {
		t.Fatalf("the copy keeps the first file's rate search (%d windows)", m.RateWindows)
	}
	if a, _ := svc.FileFor(ctx, wrapSyncID(inner, other, 1), 330); a.Sync != syncStatePending {
		t.Fatalf("other cut: %q", a.Sync)
	}
	close(block)
	sameKey, _ := syncKey(inner, same, 1)
	otherKey, _ := syncKey(inner, other, 1)
	waitFlight(t, svc, "sync|"+sameKey)
	waitFlight(t, svc, "sync|"+otherKey)
	// The new file's own windows back the copy: it stays timed
	if m := svc.sync.meta(sameKey); m == nil || !m.fits() || !startsNear(vttCues(func() []byte { a, _ := svc.sync.result(sameKey, nil); return a.VTT }()), file, 40) {
		t.Fatalf("same cut after its own windows: %+v", m)
	}
}

// A subtitle that fits file A is asked for on file B of the same length and rate whose dialogue
// is another episode's (a wrong pick, or another cut): B gets A's fit at once, but once B's own
// windows say it does not fit them the copy goes (review: it stayed served as timed until the
// file was covered).
func TestSameCutGivesWayToTheFilesWindows(t *testing.T) {
	file := dialogue(59, episode)
	otherEp := dialogue(83, episode)
	svc, _ := newPreTest(t, file)
	const b = "5c8f6d1e2a3b4c5d6e7f8091a2b3c4d5e6f70812"
	svc.sync.probe = func(_ context.Context, url string) (mediaProbe, error) {
		d := episode
		if strings.Contains(url, b) {
			d = episode + 0.02
		}
		return mediaProbe{Duration: d, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}, nil
	}
	block := make(chan struct{})
	truthA, truthB := readTruth(file, new(int)), readTruth(otherEp, new(int))
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		if strings.Contains(url, b) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return truthB(ctx, url, p, start, length)
		}
		return truthA(ctx, url, p, start, length)
	}
	sub := subtitleFor(file, 1, func(float64) float64 { return 2.5 })
	inner := cacheProviderSub(t, svc, "1|cut|2|7|1", sub)
	ctx := context.Background()
	saved := syncFirstWait
	syncFirstWait = 500 * time.Millisecond
	t.Cleanup(func() { syncFirstWait = saved })
	tap := svc.OpenSpeech(syncHash, 1, 0) // file A plays
	defer tap.Close()
	if a, _ := svc.FileFor(ctx, wrapSyncID(inner, syncHash, 1), 300); a.Sync != syncStateLive {
		t.Fatalf("A: %q", a.Sync)
	}
	keyB, _ := syncKey(inner, b, 1)
	if a, _ := svc.FileFor(ctx, wrapSyncID(inner, b, 1), 330); a.Sync != syncStateLive || svc.sync.meta(keyB).SameCut == "" {
		t.Fatalf("B before its own windows: %q", a.Sync)
	}
	close(block)
	waitFlight(t, svc, "sync|"+keyB)
	f := svc.sync.liveFile(b, 1)
	injectLive(f, otherEp, []int{5, 6, 7, 8, 9, 10, 11, 12})
	svc.sync.refitFile(f)
	m := svc.sync.meta(keyB)
	a, _ := svc.FileFor(ctx, wrapSyncID(inner, b, 1), 400)
	if m.fits() || m.SameCut != "" || a.Sync == syncStateLive || startsNear(vttCues(a.VTT), file, 40) {
		t.Fatalf("A's fit stays on B although the subtitle does not fit B: %q %+v", a.Sync, m)
	}
}

// Only a viewing of the last minutes lends its fit (a step down or sidegrade during playback).
func TestSameCutOnlyFromARecentViewing(t *testing.T) {
	file := dialogue(61, episode)
	svc, _ := newPreTest(t, file)
	const b = "5c8f6d1e2a3b4c5d6e7f8091a2b3c4d5e6f70812"
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}, nil
	}
	inner := cacheProviderSub(t, svc, "1|recent|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 1.5 }))
	ctx := context.Background()
	tap := svc.OpenSpeech(syncHash, 1, 0)
	defer tap.Close()
	if a, _ := svc.FileFor(ctx, wrapSyncID(inner, syncHash, 1), 300); a.Sync != syncStateLive {
		t.Fatalf("A: %q", a.Sync)
	}
	keyA, _ := syncKey(inner, syncHash, 1)
	keyB, _ := syncKey(inner, b, 1)
	base, _ := svc.File(ctx, inner)
	svc.sync.mu.Lock()
	svc.sync.served[keyA].last = time.Now().Add(-liveServedFor - time.Minute)
	svc.sync.mu.Unlock()
	svc.sync.sameCut(ctx, inner, b, 1, base)
	if m := svc.sync.meta(keyB); m != nil {
		t.Fatalf("an old viewing lent its fit: %+v", m)
	}
	svc.sync.mu.Lock()
	svc.sync.served[keyA].last = time.Now()
	svc.sync.mu.Unlock()
	svc.sync.sameCut(ctx, inner, b, 1, base)
	if m := svc.sync.meta(keyB); m == nil || m.SameCut == "" {
		t.Fatalf("a recent viewing did not lend its fit: %+v", m)
	}
}

// While an untimed viewing re-fits every 5 s in its first minutes, the subtitle's alternatives
// (up to 3, each at every frame rate) are fitted at most every refitGap; what is served stays.
func TestPendingRefitsFitAlternativesLessOften(t *testing.T) {
	svc := newLiveTest(t, episode)
	file := dialogue(67, episode)
	bad := cacheProviderSub(t, svc, "1|bad|2|10|1", dialogue(97, episode))
	good := cacheProviderSub(t, svc, "2|good|2|10|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	svc.sync.listed([]Sub{{ID: bad, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: good, Source: SourceTurkceAltyazi, Language: "tr"}}, map[string]bool{}, syncHash, 1)
	key, _ := syncKey(bad, syncHash, 1)
	goodKey, _ := syncKey(good, syncHash, 1)
	svc.FileFor(context.Background(), wrapSyncID(bad, syncHash, 1), 0)
	waitFlight(t, svc, "sync|"+key)
	f := svc.sync.liveFile(syncHash, 1)
	refit := func(w int) (*syncMeta, *syncMeta) {
		injectLive(f, file, []int{w})
		svc.sync.refitFile(f)
		return svc.sync.meta(key), svc.sync.meta(goodKey)
	}
	injectLive(f, file, []int{0, 1, 2, 3, 4, 5})
	if m, g := refit(6); m == nil || m.State != syncStateReplaced || g == nil || g.LiveWindows != 7 {
		t.Fatalf("first re-fit: %+v / %+v", m, g)
	}
	if m, g := refit(7); m.State != syncStateReplaced || g.LiveWindows != 7 {
		t.Fatalf("a re-fit soon after: served %q, the alternative fitted with %d windows", m.State, g.LiveWindows)
	}
	svc.sync.refitGap = 0 // the gap passed
	if m, g := refit(8); m.State != syncStateReplaced || g.LiveWindows != 9 {
		t.Fatalf("after the gap: served %q, the alternative fitted with %d windows", m.State, g.LiveWindows)
	}
}

func TestMovesNear(t *testing.T) {
	before := []liveCue{{10, 11, "a"}, {20, 21, "b"}, {30, 31, "c"}, {400, 401, "d"}, {25, 26, "a"}}
	after := []liveCue{{10, 11, "a"}, {21.5, 22, "b"}, {29, 30, "c"}, {402, 403, "d"}, {25.05, 26, "a"}, {40, 41, "x"}}
	mv := movesNear(before, after, 0, 300)
	if mv.cues != 5 || mv.moved != 2 || mv.other != 1 || mv.max != 1.5 || mv.median != 1.5 {
		t.Fatalf("%+v", mv)
	}
	// A cue that moved into the range counts too
	if mv := movesNear([]liveCue{{299.5, 300, "e"}}, []liveCue{{301, 302, "e"}}, 0, 300); mv.moved != 1 {
		t.Fatalf("%+v", mv)
	}
}

// A re-fit that moves cues ahead of the viewer says so in the log, with the position the player
// sent.
func TestLogNearTheViewer(t *testing.T) {
	svc := newLiveTest(t, episode)
	inner := cacheProviderSub(t, svc, "1|near|2|10|1", dialogue(61, episode))
	key, _ := syncKey(inner, syncHash, 1)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	before := []liveCue{{1000, 1001, "a"}, {1100, 1101, "b"}, {2000, 2001, "c"}}
	after := []liveCue{{1001.5, 1002, "a"}, {1100, 1101, "b"}, {2003, 2004, "c"}}
	svc.sync.logNear(key, inner, syncHash, 1, before, after) // no position known
	svc.sync.noteServed(key, inner, syncHash, 1, 990)
	svc.sync.logNear(key, inner, syncHash, 1, before, after)
	out := buf.String()
	if strings.Count(out, "of the viewer") != 1 || !strings.Contains(out, "moved 1 of 2 cues within 300 s of the viewer at 990s: max +1.50s") {
		t.Fatalf("log: %s", out)
	}
}
