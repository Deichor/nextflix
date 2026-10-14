package subtitles

import (
	"context"
	"math"
	"math/rand"
	"testing"
)

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
// impaired when another one is there.
func TestOwnTrackChoice(t *testing.T) {
	tracks := []OwnTrack{
		{Stream: 2, Lang: "eng", Codec: "hdmv_pgs_subtitle"},
		{Stream: 3, Lang: "eng", Codec: "subrip", Forced: true},
		{Stream: 4, Lang: "tur", Codec: "subrip"},
		{Stream: 5, Lang: "eng", Title: "English SDH", Codec: "subrip"},
		{Stream: 6, Lang: "eng", Codec: "ass"},
	}
	if got := pickOwnTrack(tracks, -1); got != 6 {
		t.Fatalf("picked %d", got)
	}
	if got := pickOwnTrack(tracks, 4); got != 4 {
		t.Fatalf("picked %d, the probe's is 4", got)
	}
	if got := pickOwnTrack(tracks[:2], -1); got != -1 {
		t.Fatalf("picked %d of a picture and a forced track", got)
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
	svc.sync.refitFile(svc.sync.liveFile(syncHash, 1))
	a, _ := svc.FileFor(ctx, id, 0)
	got := vttCues(a.VTT)
	if a.Sync != syncStateSynced || len(got) != len(file) {
		t.Fatalf("sync %q, %d cues", a.Sync, len(got))
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
