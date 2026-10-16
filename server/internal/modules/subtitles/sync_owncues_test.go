package subtitles

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"
)

// gridIndexes are the grid windows of [from, to).
func gridIndexes(from, to float64) []int {
	var out []int
	for w := int(from / syncWindowLen); float64(w)*syncWindowLen < to; w++ {
		out = append(out, w)
	}
	return out
}

// playOwn opens a cue tap at from and passes the file's lines of [from, to), reading on in
// 0.5 s steps like a play stream.
func playOwn(t *testing.T, svc *Service, file []span, from, to float64) OwnCueSink {
	t.Helper()
	_, s, ok := svc.OpenOwnCues(syncHash, 1, from, []OwnTrack{{Stream: 3, Lang: "eng", Codec: "subrip"}})
	if !ok {
		t.Fatalf("no cue tap at %.0f", from)
	}
	j := 0
	for at := from; at < to; at += 0.5 {
		next := math.Min(at+0.5, to)
		for ; j < len(file) && file[j].start < next; j++ {
			if file[j].start >= from-1 {
				s.Cue(file[j].start, file[j].end)
			}
		}
		s.Heard(next)
	}
	return s
}

// Cue taps of several play streams (overlapping, out of order) merge on the grid and are kept.
func TestOwnCuesMerge(t *testing.T) {
	svc := newLiveTest(t, 600)
	file := dialogue(5, 600)
	a := playOwn(t, svc, file, 100, 230)
	b := playOwn(t, svc, file, 50, 130)
	a.Close()
	b.Close()
	f := svc.sync.liveFile(syncHash, 1)
	if len(f.ownDone) != 2 || f.ownDone[1].Frames != liveWindowFrames || f.ownDone[2].Frames != liveWindowFrames {
		t.Fatalf("finished %v", f.ownDone)
	}
	ws, _ := f.windows()
	// finished: 60-120, 120-180; the part read of 180-240 (50 s); 50-60 is too short
	if len(ws) != 3 || ws[0].Start != 60 || ws[2].Start != 180 || len(ws[2].Speech) != 50*syncRate {
		t.Fatalf("windows %+v", ws)
	}
	got := ownStartsOf(ws)
	var want []float64
	for _, c := range file {
		if c.start >= 60 && c.start < 230 && math.Round(c.start*syncRate) < 230*syncRate {
			want = append(want, math.Round(c.start*syncRate)/syncRate)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%d line starts, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-6 {
			t.Fatalf("line start %.2f, want %.2f", got[i], want[i])
		}
	}
	for _, w := range ws {
		if w.Kind != "subs" {
			t.Fatalf("window at %.0f is %q", w.Start, w.Kind)
		}
	}
	// Kept on disk and read back by a new process
	svc.sync.saveLive(f)
	again := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true})
	g := again.sync.liveFile(syncHash, 1)
	if len(g.ownDone) != 2 || string(g.ownDone[2].Bits) != string(f.ownDone[2].Bits) ||
		len(g.ownDone[2].Starts) != len(f.ownDone[2].Starts) {
		t.Fatalf("reloaded %d windows", len(g.ownDone))
	}
}

// The tap reads the track the reference windows read: English, not forced, not for the hearing
// impaired when another one is there; a text track before a PGS one, and a PGS track only when
// it has lines all through.
func TestOwnTrackChoice(t *testing.T) {
	tracks := []OwnTrack{
		{Stream: 2, Lang: "eng", Codec: "hdmv_pgs_subtitle"},
		{Stream: 3, Lang: "eng", Codec: "subrip", Forced: true},
		{Stream: 4, Lang: "tur", Codec: "subrip"},
		{Stream: 5, Lang: "eng", Title: "English SDH", Codec: "subrip"},
		{Stream: 6, Lang: "eng", Codec: "ass"},
	}
	if got := pickOwnTrack(tracks, -1, 0); got != 6 {
		t.Fatalf("picked %d", got)
	}
	if got := pickOwnTrack(tracks, 4, 0); got != 4 {
		t.Fatalf("picked %d, the probe's is 4", got)
	}
	if got := pickOwnTrack(tracks[:2], -1, 0); got != 2 {
		t.Fatalf("picked %d of a PGS and a forced track", got)
	}
	pgs := []OwnTrack{
		{Stream: 2, Lang: "eng", Title: "English SDH", Codec: "hdmv_pgs_subtitle", Frames: 1940},
		{Stream: 3, Lang: "eng", Codec: "hdmv_pgs_subtitle", Frames: 40}, // signs only, not marked forced
		{Stream: 4, Lang: "eng", Codec: "dvd_subtitle"},
	}
	if got := pickOwnTrack(pgs, -1, 2595); got != 2 {
		t.Fatalf("picked %d of PGS tracks", got)
	}
	if got := pickOwnTrack(pgs[1:], -1, 2595); got != -1 {
		t.Fatalf("picked %d of a forced-only PGS and a VobSub track", got)
	}
}

// A window read of a PGS track: The Mentalist S03E02 (d3g WEBRip) from 1500 s with -copyts as
// recorded (times and sizes, checksums zeroed). Each line is the extracted track's to the ms;
// the line shown at the start began before the keyframe (only its clear at 1501.456 comes), the
// last one still shows at the window's end.
func TestPictureWindow(t *testing.T) {
	data, err := os.ReadFile("testdata/sync/pgs_window_1500.framecrc")
	if err != nil {
		t.Fatal(err)
	}
	got := pictureCues(framePackets(data), 1560)
	want := []span{{1502.196, 1504.111}, {1504.198, 1507.680}, {1507.767, 1510.900}, {1512.162, 1513.120},
		{1514.556, 1516.427}, {1516.515, 1517.907}, {1517.994, 1518.908}, {1529.876, 1530.790}, {1535.751, 1538.449},
		{1538.537, 1540.626}, {1540.713, 1542.236}, {1542.323, 1545.413}, {1545.500, 1547.763}, {1547.850, 1551.506},
		{1551.593, 1554.248}, {1554.335, 1555.466}, {1555.554, 1557.164}, {1557.251, 1558.295}, {1558.382, 1559.862},
		{1559.949, 1560}}
	if len(got) != len(want) {
		t.Fatalf("%d cues: %v", len(got), got)
	}
	for i := range want {
		if math.Abs(got[i].start-want[i].start) > 1e-6 || math.Abs(got[i].end-want[i].end) > 1e-6 {
			t.Fatalf("cue %d: %v, want %v", i, got[i], want[i])
		}
	}
	w, ok := spanWindow(got, 1500, liveWindowFrames)
	if starts := ownStartsOf([]refWindow{w}); !ok || w.Kind != "subs" || len(starts) != len(want) {
		t.Fatalf("window ok=%v kind %q, %d starts", ok, w.Kind, len(starts))
	}
	// Fades: palette updates change the line showing; a picture replaces it; a display set kept
	// as a packet per segment counts as one
	ps := []framePacket{{10, 9000}, {10.04, 120}, {10.08, 1300}, {12, 12000}, {13, 30}, {13.5, 39}, {14, 800}, {15, 30},
		{16, 22}, {16, 13}, {16, 85}, {16, 4000}, {16, 3}, {17, 14}, {17, 13}, {17, 3}}
	if got := pictureCues(ps, 60); fmt.Sprint(got) != fmt.Sprint([]span{{10, 12}, {12, 13}, {14, 15}, {16, 17}}) {
		t.Fatalf("updates: %v", got)
	}
	// A fade-out whose small step reads as a clear and the next one as a picture: no line of
	// 0.1 s (the line ends at the step)
	ps = []framePacket{{20, 9000}, {21.9, 300}, {21.94, 50}, {21.98, 120}, {22.08, 30}, {23, 5000}, {25, 30}}
	if got := pictureCues(ps, 60); fmt.Sprint(got) != fmt.Sprint([]span{{20, 21.94}, {23, 25}}) {
		t.Fatalf("fade: %v", got)
	}
}

// A file above the byte budget whose reference was saved before picture tracks counted: the
// early read probes it again and reads its PGS track.
func TestEarlyReadsPictureTrack(t *testing.T) {
	svc := newLiveTest(t, episode)
	y := svc.sync
	y.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "hdmv_pgs_subtitle", Bitrate: 2 << 20}, nil
	}
	file := dialogue(1100, episode)
	got := make(chan mediaProbe, 4)
	y.readSubs = func(_ context.Context, _ string, p mediaProbe, start, _ float64) (refWindow, error) {
		got <- p
		return truthWindows(file, []float64{start}, 0, 0, 1)[0], nil
	}
	grown := make(chan struct{}, 4)
	y.onGrow = func(*liveFile) { grown <- struct{}{} }
	fk := syncFileKey(syncHash, 1)
	old := []byte(`{"probe":{"duration":2560,"fps":23.976,"audio":1,"center":true,"text_sub":-1,"bitrate":2097152},` +
		`"windows":null,"full":false,"max_windows":5,"skipped":true}`)
	if err := svc.files.write("syncref", fk, ".json", old); err != nil {
		t.Fatal(err)
	}
	tap := y.openSpeech(syncHash, 1, 300.4)
	select {
	case <-grown: // the early windows are in
	case <-time.After(5 * time.Second):
		t.Fatal("no early read")
	}
	tap.Close()
	if p := <-got; !p.picture() || p.OwnSub != 2 {
		t.Fatalf("early read with %+v", p)
	}
	b, _ := svc.files.read("syncref", fk, ".json", 0)
	if !strings.Contains(string(b), `"sub_codec":"hdmv_pgs_subtitle"`) || !strings.Contains(string(b), `"kind":"subs"`) {
		t.Fatalf("saved %s", b[:min(len(b), 300)])
	}
}

// A reference saved before picture tracks counted (speech windows, no sub_codec, like The
// Mentalist S03E02's) is probed again and its PGS track read once at its windows (their speech,
// read before, is the pair: no audio is read again); one of a file without any own track only
// gets its probe brought up to date.
func TestOldReferenceReadsPictureTrack(t *testing.T) {
	file := dialogue(11, episode)
	for _, codec := range []string{"hdmv_pgs_subtitle", "none"} {
		svc, _ := newSyncTest(t, file)
		y := svc.sync
		y.probe = func(context.Context, string) (mediaProbe, error) {
			p := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: codec}
			if codec != "none" {
				p.OwnSub = 2
			}
			return p, nil
		}
		y.read = func(_ context.Context, _ string, _ mediaProbe, start, _ float64) (refWindow, error) {
			t.Errorf("%s: window at %.0f read again", codec, start)
			return refWindow{}, errors.New("no")
		}
		var at []float64
		y.readSubs = func(_ context.Context, _ string, p mediaProbe, start, _ float64) (refWindow, error) {
			if !p.picture() || p.OwnSub != 2 {
				t.Errorf("own read at %.0f with %+v", start, p)
			}
			at = append(at, start)
			return truthWindows(file, []float64{start}, 0, 0, 1)[0], nil
		}
		fk := syncFileKey(syncHash, 1)
		old := []byte(`{"probe":{"duration":2560,"fps":23.976,"audio":1,"center":true,"text_sub":-1,"bitrate":360178},` +
			`"windows":[{"start":20,"kind":"audio","speech":"AQE="},{"start":500,"kind":"audio","speech":"AQE="}],"full":false,"max_windows":12}`)
		if err := svc.files.write("syncref", fk, ".json", old); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		r, err := y.reference(ctx, syncHash, 1)
		if err != nil || r.Probe.SubCodec != codec || len(r.Windows) != 2 || r.kind() != "audio" {
			t.Fatalf("%s: reference %v, probe %+v, %d windows", codec, err, r.Probe, len(r.Windows))
		}
		if r.unpaired() != (codec != "none") {
			t.Fatalf("%s: unpaired %v", codec, r.unpaired())
		}
		if codec != "none" {
			p, ok := y.pairRef(ctx, syncHash, 1)
			if !ok || len(at) != 2 || at[0] != 20 || at[1] != 500 || p.kind() != "subs" || len(p.Speech) != 2 ||
				p.Speech[0].Kind != "audio" || !p.Paired || p.unpaired() {
				t.Fatalf("PGS: own reads at %v, reference %s, %d speech windows, paired %v", at, p.kind(), len(p.Speech), p.Paired)
			}
		}
		b, _ := svc.files.read("syncref", fk, ".json", 0)
		if !strings.Contains(string(b), `"sub_codec":"`+codec+`"`) {
			t.Fatalf("%s: saved %s", codec, b[:min(len(b), 200)])
		}
		// once: a new process loads it as it is
		again := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, TorrServerURL: "http://torrserver"})
		again.sync.probe = func(context.Context, string) (mediaProbe, error) {
			t.Error("probed again")
			return mediaProbe{}, errors.New("no")
		}
		if r2, err := again.sync.reference(ctx, syncHash, 1); err != nil || r2.Probe.SubCodec != codec || r2.unpaired() {
			t.Fatalf("%s: again %v %+v, unpaired %v", codec, err, r2.Probe, r2.unpaired())
		}
	}
}

// A loose subtitle (single lines up to 0.45 s off, a stretch 2 s off) played with the cue tap
// is served on the file's own lines.
func TestOwnCuesRefineServed(t *testing.T) {
	const length = 900.0
	svc := newLiveTest(t, length)
	file := dialogue(9, length)
	rng := rand.New(rand.NewSource(3))
	sub := make([]span, len(file))
	for i, c := range file {
		d := -1.5 // the subtitle's offset
		if c.start > 400 && c.start < 470 {
			d += 2
		}
		if i%3 == 1 {
			d += (rng.Float64()*2 - 1) * 0.45
		}
		sub[i] = span{c.start - d, c.end - d}
	}
	inner := cacheProviderSub(t, svc, "1|own|2|10|1", sub)
	id := wrapSyncID(inner, syncHash, 1)
	ctx := context.Background()
	if _, err := svc.FileFor(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	playOwn(t, svc, file, 0, length).Close()
	f := svc.sync.liveFile(syncHash, 1)
	injectLive(f, file, gridIndexes(0, length)) // the speech heard with them checks the track
	svc.sync.refitFile(f)
	a, _ := svc.FileFor(ctx, id, 0)
	got := vttCues(a.VTT)
	if a.Sync != syncStateSynced || len(got) != len(file) {
		t.Fatalf("sync %q, %d cues", a.Sync, len(got))
	}
	if c := f.check; c.State != ownTimed || !c.Tight {
		t.Fatalf("own track %+v", c)
	}
	on := 0
	for i, c := range got {
		if math.Abs(c.start-file[i].start) <= 0.02 {
			on++
		}
	}
	if share := float64(on) / float64(len(file)); share < 0.9 {
		t.Fatalf("only %.2f of the lines on the file's own lines", share)
	}
}
