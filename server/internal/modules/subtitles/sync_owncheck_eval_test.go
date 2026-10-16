package subtitles

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Calibration of the own-track check (sync_owncheck.go) on real data: NFX_TESTDATA (the
// snapshot: read windows and the played speech) and NFX_GT (whole own tracks as cue times, read
// from the files for the evaluation). Every file's own track against the speech the server has
// for the same stretches: its read own windows where played audio covers them, and with a whole
// track every played window and every read speech window.

var ownCheckCases = []struct{ file, times, note string }{
	{"a9de75c7", "s03e02_pgs.times", "Mentalist S03E02 d3g PGS (timed, tight)"},
	{"1f3cf24f", "s03e01_pgs.times", "Mentalist S03E01 d3g PGS (another cut)"},
	{"555f0556", "s02e22_own3.times", "Mentalist S02E22 NF track 3 (roll-up captions)"},
	{"555f0556", "s02e22_own4.times", "Mentalist S02E22 NF track 4 (roll-up captions)"},
	{"75c5beae", "s02e16_own2.times", "Mentalist S02E16 Code Red ASS (another cut)"},
}

// speechAt is the played speech of [start, start+frames) from the live grid windows, when they
// cover all of it.
func speechAt(live []refWindow, start float64, frames int) ([]byte, bool) {
	out := make([]byte, frames)
	have := make([]bool, frames)
	a := int(math.Round(start * syncRate))
	for _, w := range live {
		b := int(math.Round(w.Start * syncRate))
		for i, v := range w.Speech {
			if j := b + i - a; j >= 0 && j < frames {
				out[j], have[j] = v, true
			}
		}
	}
	for _, h := range have {
		if !h {
			return nil, false
		}
	}
	return out, true
}

// tapOwnPairs are the cue tap's own windows of the snapshot (synclive "own") with the speech
// heard with them.
func tapOwnPairs(t *testing.T, f *snapFile) []ownPair {
	b, err := os.ReadFile(filepath.Join(snapshotDir(t), "synclive", f.name+".json"))
	if err != nil {
		return nil
	}
	var s liveSaved
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	lf := &liveFile{done: s.Windows, ownDone: s.Own}
	return ownPairsOf(syncRef{}, lf)
}

// snapOwnPairs pairs an own track (whole, or nil) and read own windows with the file's speech:
// read speech windows (as a read with the own track pairs them) and played windows.
func snapOwnPairs(f *snapFile, track []span) []ownPair {
	var pairs []ownPair
	for _, w := range f.ref.Windows {
		switch {
		case w.Kind == "subs":
			if sp, ok := speechAt(f.live, w.Start, len(w.Speech)); ok {
				pairs = append(pairs, ownPair{w.Start, w.Speech, sp})
			}
		case track != nil:
			own := ownWindows(track, []float64{w.Start})[0].Speech[:len(w.Speech)]
			pairs = append(pairs, ownPair{w.Start, own, w.Speech})
		}
	}
	if track != nil {
		for _, w := range f.live {
			own := ownWindows(track, []float64{w.Start})[0].Speech[:len(w.Speech)]
			pairs = append(pairs, ownPair{w.Start, own, w.Speech})
		}
	}
	return pairs
}

func logCheck(t *testing.T, note string, pairs []ownPair) ownCheck {
	c := checkOwn(pairs)
	t.Logf("%-50s %-5s tight=%-5v near %3.0f%% of %4d starts, %2d stretches, fit %s (score %.2f over %.2f, peak %.2f)", note, c.State,
		c.Tight, 100*c.Near, c.Matched, c.Pairs, segText(c.Fit), c.Fit.Score, c.Fit.Base, c.Fit.Peak)
	if os.Getenv("NFX_EVAL") == "verbose" {
		var parts []string
		for i, w := range c.Fit.windows {
			if i < len(c.Fit.windowOffsets) {
				parts = append(parts, fmt.Sprintf("%.0f:%+.2f", w.Start, c.Fit.windowOffsets[i]))
			}
		}
		for i := 0; i < len(parts); i += 12 {
			t.Logf("   %s", strings.Join(parts[i:min(i+12, len(parts))], " "))
		}
		// tightness at other lags
		if c.Fit.Scale != 0 {
			for _, lag := range []float64{0, 0.1, 0.2, 0.3} {
				near, n := nearShare(distinctPairs(pairs), lag)
				t.Logf("   lag %+.1f: near %3.0f%% of %d", lag, 100*near, n)
			}
		}
	}
	return c
}

func TestSnapshotOwnCheck(t *testing.T) {
	files, _ := loadSnapshot(t)
	for _, c := range ownCheckCases {
		f := findFile(files, c.file)
		track := loadFullOwn(t, c.times)
		if f == nil || len(track) == 0 {
			t.Logf("%s: no data", c.note)
			continue
		}
		pairs := snapOwnPairs(f, track)
		logCheck(t, c.note, pairs)
		// as the evidence comes: the first n stretches played (and the read ones)
		if os.Getenv("NFX_EVAL") == "verbose" {
			for _, n := range []int{3, 5, 8, 12} {
				if n < len(pairs) {
					logCheck(t, fmt.Sprintf("   first %d", n), pairs[:n])
				}
			}
		}
	}
	// Every other file: read own windows that played audio covers, and the cue tap's windows
	for _, f := range files {
		if pairs := append(snapOwnPairs(f, nil), tapOwnPairs(t, f)...); len(pairs) > 0 {
			logCheck(t, f.label(), pairs)
		}
	}
}
