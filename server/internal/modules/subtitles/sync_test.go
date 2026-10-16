package subtitles

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const syncHash = "73ec17eb899cf91f2ca248fdd65a55e25c30478f"

func TestSyncIDRoundTrip(t *testing.T) {
	inner := encodeID(SourceTurkceAltyazi, "tr", "301213|/sub/301213/the-mentalist.html|2|7|1")
	id := wrapSyncID(inner, syncHash, 1)
	in, hash, index, ok := unwrapSyncID(id)
	if !ok || in != inner || hash != syncHash || index != 1 {
		t.Fatalf("unwrap %q %q %d %v", in, hash, index, ok)
	}
	if _, _, _, ok := unwrapSyncID(inner); ok {
		t.Fatal("a plain id is not a file id")
	}
	if _, _, _, err := decodeID(id); err == nil {
		t.Fatal("a file id is not a plain id")
	}
}

func TestWrongEpisode(t *testing.T) {
	q := Query{MediaType: "tv", Season: 2, Episode: 7}
	cases := map[string]bool{
		"The.Mentalist.S02E07.720p.WEB-DL.DD5.1.H.264-EbP":    false,
		"The.Mentalist.S02E06.720p.WEB-DL.DD5.1.H.264-EbP":    true,
		"The Mentalist - [2x07] - Red Bulls":                  false,
		"Mentalist - 2x08 - Red Bulls":                        true,
		"The.Mentalist.S02E06E07.HDTV":                        false,
		"The.Mentalist.S02E06-1080p.WEB":                      true,
		"S02 (pack) HDTV":                                     false,
		"Season 2/The.Mentalist.S02E05.srt":                   true,
		"The.Mentalist.S02E07.1920x1080.WEB":                  false,
		"[720pMkv.Com]_The.Mentalist.S02E07.480p.WEB.DL.x264": false,
	}
	for name, want := range cases {
		if got := wrongEpisode(candidate{Sub: Sub{Release: name}}, q); got != want {
			t.Errorf("%s: wrong=%v want %v", name, got, want)
		}
	}
	if wrongEpisode(candidate{Sub: Sub{Release: "S02E01"}, Pack: true}, q) {
		t.Error("a pack is never the wrong episode")
	}
	if wrongEpisode(candidate{Sub: Sub{Release: "S02E01"}}, Query{MediaType: "movie"}) {
		t.Error("movies have no episodes")
	}
}

func vttOf(cues []span) []byte {
	lc := make([]liveCue, len(cues))
	for i, c := range cues {
		lc[i] = liveCue{start: c.start, end: c.end, text: fmt.Sprintf("line %d", i)}
	}
	return renderVTT(lc, false)
}

// newSyncTest is a service whose file reads come from the given true cues of the file: its own
// track's windows, and the speech read with them (the same lines; not counted in reads).
func newSyncTest(t *testing.T, file []span) (*Service, *int) {
	saved := syncReadDelay
	syncReadDelay = 0
	t.Cleanup(func() { syncReadDelay = saved })
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver"})
	reads := 0
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"}, nil
	}
	svc.sync.read = readTruth(file, &reads)
	return svc, &reads
}

// readTruth reads windows of a file whose own track has the given cues: own windows, or with
// no own track asked for the speech of the same lines (reads counts the own ones).
func readTruth(file []span, reads *int) syncWindowReader {
	return func(_ context.Context, _ string, p mediaProbe, start, _ float64) (refWindow, error) {
		w := truthWindows(file, []float64{start}, 0.1, 0, int64(start))[0]
		if p.OwnSub < 0 {
			w.Kind = "audio"
			for i, v := range w.Speech {
				w.Speech[i] = min(v, 1)
			}
			return w, nil
		}
		*reads++
		return w, nil
	}
}

func cacheProviderSub(t *testing.T, svc *Service, ref string, cues []span) string {
	id := encodeID(SourceTurkceAltyazi, "tr", ref)
	if err := svc.files.write(SourceTurkceAltyazi, SourceTurkceAltyazi+"|"+ref, ".vtt", vttOf(cues)); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServeTimesTheSubtitle(t *testing.T) {
	file := dialogue(11, episode)
	svc, reads := newSyncTest(t, file)
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 1300 {
			return 0
		}
		return -4
	})
	inner := cacheProviderSub(t, svc, "1|a|2|7|1", sub)
	id := wrapSyncID(inner, syncHash, 1)

	a, err := svc.FileFor(context.Background(), id, 0)
	if err != nil || a.Partial || a.Sync != syncStateSynced {
		t.Fatalf("answer partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	got := vttCues(a.VTT)
	if len(got) != len(file) {
		t.Fatalf("%d cues, want %d", len(got), len(file))
	}
	on := 0
	for i := range got {
		if math.Abs(got[i].start-file[i].start) <= 0.05 {
			on++
		}
	}
	if share := float64(on) / float64(len(got)); share < 0.97 {
		t.Fatalf("only %.2f of the lines on time", share)
	}
	n := *reads
	// cached: no new reads, same answer
	b, _ := svc.FileFor(context.Background(), id, 0)
	if *reads != n || string(b.VTT) != string(a.VTT) || b.Sync != syncStateSynced {
		t.Fatalf("second answer reads %d->%d sync %q", n, *reads, b.Sync)
	}
	// the list now counts it as synced
	if svc.sync.fitOf(inner, syncHash, 1) != 1 {
		t.Fatal("fit not recorded")
	}
}

// A result of an older timing (final ones are never re-fitted otherwise) is served as it is while
// it is timed again, then replaced.
func TestServeRetimesAnOlderVersion(t *testing.T) {
	file := dialogue(12, episode)
	svc, _ := newSyncTest(t, file)
	inner := cacheProviderSub(t, svc, "1|old|2|7|1", subtitleFor(file, 1, func(float64) float64 { return -3 }))
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	if a, err := svc.FileFor(context.Background(), id, 0); err != nil || a.Sync != syncStateSynced {
		t.Fatalf("first answer: sync %q err %v", a.Sync, err)
	}
	// What an older version stored: 1 s off, no version
	old := *svc.sync.meta(key)
	old.Version, old.Fit.Segments = 0, []syncSegment{{Offset: old.Fit.Segments[0].Offset + 1}}
	b, _ := json.Marshal(old)
	raw, ok := svc.files.read(SourceTurkceAltyazi, SourceTurkceAltyazi+"|1|old|2|7|1", ".vtt", 0)
	if !ok {
		t.Fatal("raw subtitle")
	}
	cues := applyFit(vttCues(raw), old.Fit)
	if svc.files.write("synced", key, ".json", b) != nil || svc.files.write("synced", key, ".vtt", renderVTT(cues, false)) != nil {
		t.Fatal("write")
	}
	svc.sync.mu.Lock()
	svc.sync.metas = map[string]*syncMeta{}
	svc.sync.mu.Unlock()

	a, err := svc.FileFor(context.Background(), id, 0)
	if err != nil || !a.Partial || a.Sync != syncStateSynced || math.Abs(vttCues(a.VTT)[10].start-file[10].start-1) > 0.05 {
		t.Fatalf("stale answer: partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	for deadline := time.Now().Add(10 * time.Second); svc.flights.running("sync|" + key); {
		if time.Now().After(deadline) {
			t.Fatal("timing again did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a, err = svc.FileFor(context.Background(), id, 0)
	if err != nil || a.Partial || a.Sync != syncStateSynced || math.Abs(vttCues(a.VTT)[10].start-file[10].start) > 0.05 {
		t.Fatalf("timed again: partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	if m := svc.sync.meta(key); m.Version != syncVersion {
		t.Fatalf("version %d", m.Version)
	}
}

func TestServeReplacesASubtitleThatDoesNotFit(t *testing.T) {
	file := dialogue(21, episode)
	svc, _ := newSyncTest(t, file)
	bad := cacheProviderSub(t, svc, "1|bad|2|7|1", dialogue(99, episode))
	good := cacheProviderSub(t, svc, "2|good|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2.5 }))
	svc.sync.listed([]Sub{
		{ID: bad, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: good, Source: SourceTurkceAltyazi, Language: "tr"},
	}, map[string]bool{}, syncHash, 1)

	a, err := svc.FileFor(context.Background(), wrapSyncID(bad, syncHash, 1), 0)
	if err != nil || a.Sync != syncStateReplaced {
		t.Fatalf("sync %q err %v", a.Sync, err)
	}
	if got := vttCues(a.VTT); len(got) != len(file) || math.Abs(got[10].start-file[10].start) > 0.05 {
		t.Fatal("not the fitting subtitle's timed cues")
	}
	if svc.sync.fitOf(bad, syncHash, 1) != -1 || svc.sync.fitOf(good, syncHash, 1) != 1 {
		t.Fatal("fits not recorded")
	}
	// Ranked again: the fitting one first, shown as synced
	subs := rankCandidates([]candidate{
		{Sub: Sub{ID: bad, Source: SourceTurkceAltyazi, Language: "tr", Downloads: 30000}, AutoSync: -1},
		{Sub: Sub{ID: good, Source: SourceTurkceAltyazi, Language: "tr"}, AutoSync: 1},
	}, Query{Langs: []string{"tr"}})
	if subs[0].ID != good || !subs[0].Synced || subs[1].Synced {
		t.Fatalf("ranked %+v", subs)
	}
}

func TestServeRetimesAFallbackWhoseCuesAreGone(t *testing.T) {
	file := dialogue(23, episode)
	svc, _ := newSyncTest(t, file)
	bad := cacheProviderSub(t, svc, "1|bad|2|7|1", dialogue(98, episode))
	good := cacheProviderSub(t, svc, "2|good|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2.5 }))
	svc.sync.listed([]Sub{
		{ID: bad, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: good, Source: SourceTurkceAltyazi, Language: "tr"},
	}, map[string]bool{}, syncHash, 1)
	if a, err := svc.FileFor(context.Background(), wrapSyncID(good, syncHash, 1), 0); err != nil || a.Sync != syncStateSynced {
		t.Fatalf("good: sync %q err %v", a.Sync, err)
	}
	// The cache sweep removed the timed cues; the result entry is still known
	goodKey, _ := syncKey(good, syncHash, 1)
	if err := os.Remove(svc.files.path("synced", goodKey, ".vtt")); err != nil {
		t.Fatal(err)
	}

	a, err := svc.FileFor(context.Background(), wrapSyncID(bad, syncHash, 1), 0)
	if err != nil || a.Sync != syncStateReplaced {
		t.Fatalf("sync %q err %v", a.Sync, err)
	}
	if got := vttCues(a.VTT); len(got) != len(file) || math.Abs(got[10].start-file[10].start) > 0.05 {
		t.Fatal("served the fallback untimed")
	}
}

func TestServePendingIsPartial(t *testing.T) {
	file := dialogue(31, episode)
	svc, reads := newSyncTest(t, file)
	block := make(chan struct{})
	read := readTruth(file, reads)
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return read(ctx, url, p, start, length)
	}
	saved := syncFirstWait
	syncFirstWait = 0
	inner := cacheProviderSub(t, svc, "1|p|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 1 }))
	a, err := svc.FileFor(context.Background(), wrapSyncID(inner, syncHash, 1), 0)
	if err != nil || !a.Partial || a.Sync != syncStatePending || len(vttCues(a.VTT)) != len(file) {
		t.Fatalf("partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	// released: the next request joins the running timing and gets its result
	close(block)
	syncFirstWait = saved
	if b, _ := svc.FileFor(context.Background(), wrapSyncID(inner, syncHash, 1), 0); b.Partial || b.Sync != syncStateSynced {
		t.Fatalf("after: partial=%v sync=%q", b.Partial, b.Sync)
	}
}

func TestTorrentSubForFile(t *testing.T) {
	const gb = 2 << 30
	cases := []struct {
		name       string
		files      []torrFile
		sub, video int
		want       bool
	}{
		{"named after the file", []torrFile{{1, "Movie.One.mkv", gb}, {2, "Movie.One.en.srt", 1}, {3, "Movie.Two.mkv", gb}}, 2, 1, true},
		{"named after another file", []torrFile{{1, "Movie.One.mkv", gb}, {2, "Movie.Two.srt", 1}, {3, "Movie.Two.mkv", gb}}, 2, 1, false},
		{"sequel name", []torrFile{{1, "Toy Story.mkv", gb}, {2, "Toy Story 2.srt", 1}, {3, "Toy Story 2.mkv", gb}}, 2, 1, false},
		{"own folder", []torrFile{{1, "C/One/One.mkv", gb}, {2, "C/One/Subs/English.srt", 1}, {3, "C/Two/Two.mkv", gb}}, 2, 1, true},
		{"other folder", []torrFile{{1, "C/One/One.mkv", gb}, {2, "C/Two/Subs/English.srt", 1}, {3, "C/Two/Two.mkv", gb}}, 2, 1, false},
		{"sample is not another file", []torrFile{{1, "M/M.mkv", gb}, {2, "M/Subs/English.srt", 1}, {3, "M/Sample/s.mkv", 50 << 20}}, 2, 1, true},
		{"episode folder", []torrFile{{1, "S/Show.S01E01.mkv", gb}, {2, "S/Subs/Show.S01E02/2_English.srt", 1}, {3, "S/Show.S01E02.mkv", gb}}, 2, 3, true},
		{"other episode folder", []torrFile{{1, "S/Show.S01E01.mkv", gb}, {2, "S/Subs/Show.S01E02/2_English.srt", 1}, {3, "S/Show.S01E02.mkv", gb}}, 2, 1, false},
		{"pack subtitle naming no episode", []torrFile{{1, "S/E01.mkv", gb}, {2, "S/Subs/English.srt", 1}, {3, "S/E02.mkv", gb}}, 2, 1, false},
		{"unknown file", []torrFile{{1, "M.mkv", gb}}, 2, 1, false},
	}
	for _, c := range cases {
		if got := torrentSubForFile(c.files, c.sub, c.video); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFullReferenceOnlyFromTheFilesSubtitle(t *testing.T) {
	files := []torrFile{
		{1, "Collection/Movie.One.2001/Movie.One.2001.mkv", 2 << 30},
		{2, "Collection/Movie.One.2001/Subs/English.vtt", 80_000},
		{3, "Collection/Movie.Two.2003/Movie.Two.2003.mkv", 2 << 30},
	}
	srv := fakeTorrServer(t, files, map[int][]byte{2: vttOf(dialogue(41, episode))})
	defer srv.Close()
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: srv.URL})
	sub := Sub{ID: encodeID(SourceTorrent, "en", testHash+"|2|vtt"), Source: SourceTorrent, Language: "en", Synced: true}
	ctx := context.Background()

	svc.sync.listed([]Sub{sub}, map[string]bool{}, testHash, 3)
	if got := svc.sync.fullReference(ctx, testHash, 3); got != nil {
		t.Fatalf("another movie's subtitle used as the reference (%d cues)", len(got))
	}
	svc.sync.listed([]Sub{sub}, map[string]bool{}, testHash, 1)
	if got := svc.sync.fullReference(ctx, testHash, 1); len(got) == 0 {
		t.Fatal("the file's own subtitle not used")
	}
}

func TestWeakReferenceIsNotKept(t *testing.T) {
	file := dialogue(51, episode)
	svc, _ := newSyncTest(t, file)
	n := 0
	svc.sync.read = func(_ context.Context, _ string, _ mediaProbe, start, _ float64) (refWindow, error) {
		if n++; n > 2 {
			return refWindow{}, errors.New("timed out")
		}
		return truthWindows(file, []float64{start}, 0.1, 0, int64(start))[0], nil
	}
	if r, err := svc.sync.reference(context.Background(), syncHash, 1); err == nil {
		t.Fatalf("a reference of %d windows accepted", len(r.Windows))
	}
	if svc.files.has("syncref", syncFileKey(syncHash, 1), ".json") {
		t.Fatal("weak reference saved")
	}
}

// failingSpeech reads a file's own track (counted in reads) while every speech read fails, as
// for audio the server cannot decode (counted in speech).
func failingSpeech(file []span, reads, speech *int) syncWindowReader {
	own := readTruth(file, reads)
	return func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		if p.OwnSub < 0 {
			*speech++
			return refWindow{}, errors.New("audio: decoder failed")
		}
		return own(ctx, url, p, start, length)
	}
}

// Speech reads that always fail: a text track is used as before the own-track check (the review
// saw such a file never timed), and the reference is not read again by every request (it was:
// own reads 6, 12, 18, 24 over 4 requests).
func TestSpeechReadsThatFailAreTriedOnce(t *testing.T) {
	file := dialogue(41, episode)
	svc, reads := newSyncTest(t, file)
	speech := 0
	svc.sync.read = failingSpeech(file, reads, &speech)
	inner := cacheProviderSub(t, svc, "1|sf|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	id := wrapSyncID(inner, syncHash, 1)
	if a, err := svc.FileFor(context.Background(), id, 0); err != nil || a.Sync != syncStateSynced {
		t.Fatalf("a file with an exact own text track: sync %q, %v", a.Sync, err)
	}
	n, s := *reads, speech
	for range 3 {
		svc.FileFor(context.Background(), id, 0)
	}
	if *reads != n || speech != s || n == 0 || s != n {
		t.Fatalf("own reads %d -> %d, speech reads %d -> %d", n, *reads, s, speech)
	}
	// A PGS track whose speech cannot be read times nothing: the job fails (10 minutes) instead
	// of being started again by every poll
	svc, reads = newSyncTest(t, file)
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "hdmv_pgs_subtitle"}, nil
	}
	speech = 0
	svc.sync.read = failingSpeech(file, reads, &speech)
	inner = cacheProviderSub(t, svc, "1|pf|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	id = wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	if a, _ := svc.FileFor(context.Background(), id, 0); a.Sync != syncStateFailed || !svc.sync.recentlyFailed(key) {
		t.Fatalf("PGS without its speech: sync %q, failed %v", a.Sync, svc.sync.recentlyFailed(key))
	}
	n = *reads
	for range 3 {
		svc.FileFor(context.Background(), id, 0)
	}
	if *reads != n || svc.flights.running("sync|"+key) {
		t.Fatalf("polls read again: %d -> %d", n, *reads)
	}
}

// writeOldRef saves a reference as the version before the own-track check did: own-track
// windows without their speech.
func writeOldRef(t *testing.T, svc *Service, file []span) []float64 {
	t.Helper()
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"}
	starts := windowStarts(episode, syncWindowLen, syncMaxWindows)
	b, _ := json.Marshal(syncRef{Probe: probe, Windows: truthWindows(file, starts, 0, 0, 1), MaxWindows: syncMaxWindows})
	if err := svc.files.write("syncref", syncFileKey(syncHash, 1), ".json", b); err != nil {
		t.Fatal(err)
	}
	return starts
}

// A reference saved before the own-track check (34 of 50 stored ones) times at once; then only
// the speech at its windows is read, once. When those reads fail the saved reference stays (the
// job failed and threw it away: "only 0 of 6 reference windows could be read").
func TestOldReferenceKeptWhenItsPairReadsFail(t *testing.T) {
	file := dialogue(43, episode)
	svc, _ := newSyncTest(t, file)
	starts := writeOldRef(t, svc, file)
	own, speech := 0, 0
	svc.sync.read = func(_ context.Context, _ string, p mediaProbe, _, _ float64) (refWindow, error) {
		if p.OwnSub >= 0 {
			own++
		} else {
			speech++
		}
		return refWindow{}, errors.New("window read timed out")
	}
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|old|2|7|1", subtitleFor(file, 1, func(float64) float64 { return -3 }))
	key, _ := syncKey(inner, syncHash, 1)
	if a, err := svc.FileFor(ctx, wrapSyncID(inner, syncHash, 1), 0); err != nil || a.Sync != syncStateSynced {
		t.Fatalf("the saved reference was not used: %q %v", a.Sync, err)
	}
	waitFlight(t, svc, "sync|"+key)
	r, err := svc.sync.reference(ctx, syncHash, 1)
	if own != 0 || speech != len(starts) || err != nil || len(r.Windows) != len(starts) || !r.Paired || r.unpaired() {
		t.Fatalf("own reads %d, speech reads %d of %d; reference %d windows, paired %v, %v", own, speech, len(starts),
			len(r.Windows), r.Paired, err)
	}
	// another subtitle of the file reads nothing
	other := cacheProviderSub(t, svc, "2|old|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 1 }))
	if a, _ := svc.FileFor(ctx, wrapSyncID(other, syncHash, 1), 0); a.Sync != syncStateSynced || own+speech != len(starts) {
		t.Fatalf("second subtitle: %q, %d more reads", a.Sync, own+speech-len(starts))
	}
}

// While the speech of an old reference's windows is read, the reference is not locked: re-fits
// get it (not the probe-only stand-in), and the subtitle is served timed from it meanwhile.
func TestOldReferencePairReadsLeaveItUsable(t *testing.T) {
	file := dialogue(44, episode)
	svc, _ := newSyncTest(t, file)
	starts := writeOldRef(t, svc, file)
	block := make(chan struct{})
	truth := readTruth(file, new(int))
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		if p.OwnSub >= 0 {
			t.Errorf("own window at %.0f read again", start)
		}
		select {
		case <-block:
		case <-ctx.Done():
		}
		return truth(ctx, url, p, start, length)
	}
	saved := syncFirstWait
	syncFirstWait = 200 * time.Millisecond
	t.Cleanup(func() { syncFirstWait = saved })
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|use|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	key, _ := syncKey(inner, syncHash, 1)
	if a, _ := svc.FileFor(ctx, wrapSyncID(inner, syncHash, 1), 0); a.Sync != syncStateSynced {
		t.Fatalf("not timed from the saved reference at once: %q", a.Sync)
	}
	if r := svc.sync.liveRef(ctx, syncHash, 1); r.Busy || len(r.Windows) != len(starts) || r.kind() != "subs" {
		t.Fatalf("while the speech is read: busy %v, %d windows (%s)", r.Busy, len(r.Windows), r.kind())
	}
	close(block)
	waitFlight(t, svc, "sync|"+key)
	if r, _ := svc.sync.reference(ctx, syncHash, 1); len(r.Speech) != len(starts) || !r.Paired {
		t.Fatalf("%d speech windows of %d, paired %v", len(r.Speech), len(starts), r.Paired)
	}
	if m := svc.sync.meta(key); m == nil || !m.fits() {
		t.Fatalf("after the pairs: %+v", m)
	}
}

// A first read cut by the job's time keeps the windows it read when they are enough (it threw
// all of them away).
func TestCutReadKeepsItsWindows(t *testing.T) {
	file := dialogue(45, episode)
	svc, reads := newSyncTest(t, file)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	truth := readTruth(file, reads)
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		if p.OwnSub >= 0 && *reads == 3 {
			cancel() // out of time after this window's reads
		}
		return truth(ctx, url, p, start, length)
	}
	r, err := svc.sync.reference(ctx, syncHash, 1)
	if err != nil || len(r.Windows) != 4 || len(r.Speech) != 4 || !svc.files.has("syncref", syncFileKey(syncHash, 1), ".json") {
		t.Fatalf("cut read: %d windows, %d speech, %v", len(r.Windows), len(r.Speech), err)
	}
}

// The speech read with each own-track window counts against the file's byte budget: once a
// window's reads would pass it, the own track is read alone.
func TestPairedReadsKeepTheByteBudget(t *testing.T) {
	file := dialogue(46, episode)
	svc, reads := newSyncTest(t, file)
	truth := readTruth(file, reads)
	speech := 0
	svc.sync.read = func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
		if p.OwnSub < 0 {
			speech++
		}
		w, err := truth(ctx, url, p, start, length)
		w.Read = 150 << 20
		return w, err
	}
	r, err := svc.sync.reference(context.Background(), syncHash, 1)
	// 6 windows of 150 MB: two with their speech (600 MB), then a pair would pass 800 MB
	if err != nil || len(r.Windows) != 6 || speech != 2 || len(r.Speech) != 2 {
		t.Fatalf("%d windows, %d speech reads, %v", len(r.Windows), speech, err)
	}
}

func TestRefStateEvictionKeepsUsedStates(t *testing.T) {
	y := NewService(nil, Config{CacheDir: t.TempDir()}).sync
	held, release := y.refState("held")
	for i := range 40 {
		_, r := y.refState(strconv.Itoa(i))
		r()
	}
	st, r := y.refState("held")
	r()
	release()
	if st != held {
		t.Fatal("a state in use was evicted")
	}
}

func TestAddWindowsAfterEviction(t *testing.T) {
	file := dialogue(61, episode)
	svc, _ := newSyncTest(t, file)
	ctx := context.Background()
	ref, err := svc.sync.reference(ctx, syncHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		_, r := svc.sync.refState(strconv.Itoa(i))
		r()
	}
	more, err := svc.sync.addWindows(ctx, syncHash, 1, []float64{1000})
	if err != nil || len(more.Windows) != len(ref.Windows)+1 {
		t.Fatalf("%d -> %d windows: %v", len(ref.Windows), len(more.Windows), err)
	}
}

// TestSyncWindowFFmpeg reads windows of a generated MKV through the byte cap, with a text track
// and with a PGS one: cues come at the file's times, and a subtitle read in a stretch without
// lines (which ffmpeg would continue to the end of the file) stops at the cap. It needs ffmpeg
// (in the server image, not in plain golang images) and writes ~300 MB to the temp dir.
func TestSyncWindowFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	var srt strings.Builder
	for i := 0; i < 20; i++ { // lines at 1, 4, ... 58 s; none after
		s := 3*i + 1
		fmt.Fprintf(&srt, "%d\n00:00:%02d,000 --> 00:00:%02d,500\nLine %d\n\n", i+1, s, s+1, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.srt"), []byte(srt.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(dir, "f.mkv")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=s=320x180:d=180:r=24",
		"-f", "lavfi", "-i", "sine=frequency=300:duration=180", "-i", filepath.Join(dir, "s.srt"),
		"-map", "0", "-map", "1", "-map", "2", "-vf", "noise=alls=60:allf=t", "-c:v", "mpeg4", "-q:v", "4", "-g", "240",
		"-c:a", "aac", "-c:s", "srt", "-metadata:s:s:0", "language=eng", mkv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(mkv)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.mkv", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	ctx := context.Background()
	p, err := probeMedia(ctx, srv.URL)
	if err != nil || p.OwnSub < 0 || p.Duration < 179 || p.Bitrate <= 0 {
		t.Fatalf("probe %+v %v", p, err)
	}
	limit := windowByteCap(p, 20) + 64<<10

	w, err := ffmpegSyncWindow(ctx, srv.URL, p, 20.5, 20)
	if err != nil || w.Kind != "subs" {
		t.Fatalf("window %v kind %q", err, w.Kind)
	}
	var at []int
	for i, v := range w.Speech {
		if v == 2 {
			at = append(at, i)
		}
	}
	// lines start at 22, 25, ... 40 (from the file's start time: AAC priming moves them ~20 ms)
	if len(at) != 7 || at[0] < 148 || at[0] > 154 || at[6]-at[0] != 1800 || w.Read > limit {
		t.Fatalf("line starts at frames %v, read %d (cap %d)", at, w.Read, limit)
	}
	// No lines after 58 s: the subtitle read would go on to the end; the cap stops it
	w, _ = ffmpegSyncWindow(ctx, srv.URL, p, 100, 20)
	if w.Read > limit {
		t.Fatalf("silent stretch read %d (cap %d, file %d)", w.Read, limit, len(data))
	}
	t.Logf("file %d MB, cap per 20 s window %d MB", len(data)>>20, limit>>20)

	// The same lines as a PGS track instead: cue times from its packets
	var lines []span
	for i := 0; i < 20; i++ {
		lines = append(lines, span{float64(3*i + 1), float64(3*i) + 2.5})
	}
	sup, pgs := filepath.Join(dir, "s.sup"), filepath.Join(dir, "p.mkv")
	writeSup(t, sup, lines)
	cmd = exec.Command("ffmpeg", "-v", "error", "-i", mkv, "-i", sup, "-map", "0:v", "-map", "0:a", "-map", "1",
		"-c", "copy", "-metadata:s:s:0", "language=eng", pgs)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	pgsData, err := os.ReadFile(pgs)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "p.mkv", time.Time{}, bytes.NewReader(pgsData))
	}))
	defer srv2.Close()
	if p, err = probeMedia(ctx, srv2.URL); err != nil || p.OwnSub != 2 || !p.picture() {
		t.Fatalf("PGS probe %+v %v", p, err)
	}
	w, err = ffmpegSyncWindow(ctx, srv2.URL, p, 20.5, 20)
	if err != nil || w.Kind != "subs" {
		t.Fatalf("PGS window %v kind %q", err, w.Kind)
	}
	at = at[:0]
	for i, v := range w.Speech {
		if v == 2 {
			at = append(at, i)
		}
	}
	if len(at) != 7 || at[0] < 148 || at[0] > 154 || at[6]-at[0] != 1800 || w.Speech[at[0]+100] != 1 || w.Speech[at[0]+160] != 0 {
		t.Fatalf("PGS line starts at frames %v", at)
	}
	if w, _ = ffmpegSyncWindow(ctx, srv2.URL, p, 100, 20); w.Read > limit {
		t.Fatalf("PGS: silent stretch read %d (cap %d)", w.Read, limit)
	}
}

// writeSup writes cues as a PGS .sup: per cue a display set with a picture (PCS with one object,
// WDS, PDS, ODS of a 100x20 striped picture, END) and one that clears the screen; a clear at 0
// first (the stream starts at 0).
func writeSup(t *testing.T, file string, cues []span) {
	t.Helper()
	var b []byte
	seg := func(at float64, typ byte, data []byte) {
		b = append(b, 'P', 'G')
		b = binary.BigEndian.AppendUint32(b, uint32(math.Round(at*90000)))
		b = binary.BigEndian.AppendUint32(b, 0)
		b = append(b, typ)
		b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
		b = append(b, data...)
	}
	window := []byte{1, 0, 0, 0, 0, 10, 0, 100, 0, 20}
	var rle []byte
	for range 20 {
		for x := range 100 {
			rle = append(rle, byte(1+x%2))
		}
		rle = append(rle, 0, 0)
	}
	clear := func(at float64, n byte) {
		seg(at, 0x16, []byte{0x01, 0x40, 0, 0xb4, 0x10, 0, n, 0, 0, 0, 0})
		seg(at, 0x17, window)
		seg(at, 0x80, nil)
	}
	clear(0, 0)
	for i, c := range cues {
		n := byte(2*i + 1)
		seg(c.start, 0x16, []byte{0x01, 0x40, 0, 0xb4, 0x10, 0, n, 0x80, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 10})
		seg(c.start, 0x17, window)
		seg(c.start, 0x14, []byte{0, 0, 1, 235, 128, 128, 255, 2, 16, 128, 128, 255})
		ods := []byte{0, 0, 0, 0xc0, byte((len(rle) + 4) >> 16), byte((len(rle) + 4) >> 8), byte(len(rle) + 4), 0, 100, 0, 20}
		seg(c.start, 0x15, append(ods, rle...))
		seg(c.start, 0x80, nil)
		clear(c.end, n+1)
	}
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
