package subtitles

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
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
	{"21b66acc", "s03e09_own3.vtt", "Mentalist S03E09 NF d1fa8946 track 3 (on the file, 0.53 s early, loose)"},
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

// checkChanges counts what a viewer would get from the check's results as the stretches come:
// decided states that change (to not known among them) and tightness switched on or off.
type checkChanges struct {
	steps                   []string
	flips, unknown, toggles int
}

func (c *checkChanges) note(k int, prev, cur ownCheck) {
	if cur.State == prev.State && cur.Tight == prev.Tight {
		return
	}
	state := cur.State
	if state == "" {
		state = "-"
	}
	c.steps = append(c.steps, fmt.Sprintf("%d:%s/%v(%.0f%%)", k, state, cur.Tight, 100*cur.Near))
	if prev.State != "" && cur.State != prev.State {
		c.flips++
		if cur.State == "" {
			c.unknown++
		}
	}
	if prev.State == ownTimed && cur.State == ownTimed && cur.Tight != prev.Tight {
		c.toggles++
	}
}

// TestSnapshotOwnCheckSequence replays every file's stretches in the order they come (read ones,
// then the played ones in time order) through the check as the re-fits run it: before (every
// new result replaced the last; 6 of 34 files changed a decided state or tightness) and now
// (checkOwnCached, settleOwn).
func TestSnapshotOwnCheckSequence(t *testing.T) {
	files, _ := loadSnapshot(t)
	var before, now checkChanges
	files0, changed0, changed1 := 0, 0, 0
	run := func(label string, pairs []ownPair) {
		var read, live []ownPair
		for _, p := range pairs {
			if p.start == math.Floor(p.start/syncWindowLen)*syncWindowLen {
				live = append(live, p)
			} else {
				read = append(read, p)
			}
		}
		sort.Slice(live, func(i, j int) bool { return live[i].start < live[j].start })
		seq := append(read, live...)
		var b, a checkChanges
		var prev ownCheck
		n := 0
		for k := 1; k <= len(seq); k++ { // the check before: every result replaced the last
			if k == n || prev.State != "" && k < n+max(1, n/3) {
				continue
			}
			c := checkOwn(seq[:k])
			b.note(k, prev, c)
			prev, n = c, k
		}
		f := newLiveFile(label, 1)
		prev = ownCheck{}
		for k := 1; k <= len(seq); k++ {
			c := checkOwnCached(f, seq[:k], true, "", 1)
			a.note(k, prev, c)
			prev = c
		}
		files0++
		if b.flips+b.toggles > 0 {
			changed0++
		}
		if a.flips+a.toggles > 0 {
			changed1++
		}
		before.flips, before.unknown, before.toggles = before.flips+b.flips, before.unknown+b.unknown, before.toggles+b.toggles
		now.flips, now.unknown, now.toggles = now.flips+a.flips, now.unknown+a.unknown, now.toggles+a.toggles
		all := checkOwn(seq)
		t.Logf("%-46s %3d stretches | before %s | now %s | all at once: %s/%v(%.0f%%)", label, len(seq), strings.Join(b.steps, " "),
			strings.Join(a.steps, " "), all.State, all.Tight, 100*all.Near)
		if a.unknown > 0 {
			t.Errorf("%s: a decided check went back to not known", label)
		}
	}
	for _, c := range ownCheckCases {
		f := findFile(files, c.file)
		track := loadFullOwn(t, c.times)
		if f == nil || len(track) == 0 {
			continue
		}
		run(c.note, snapOwnPairs(f, track))
	}
	for _, f := range files {
		if pairs := append(snapOwnPairs(f, nil), tapOwnPairs(t, f)...); len(pairs) > 0 {
			run(f.label(), pairs)
		}
	}
	t.Logf("%d sequences; changed a decided state or tightness: before %d (%d flips, %d to not known, %d tight toggles), now %d (%d, %d, %d)",
		files0, changed0, before.flips, before.unknown, before.toggles, changed1, now.flips, now.unknown, now.toggles)
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

// The timed checks kept with the snapshot's live windows: one kept before its offsets were is
// checked again with the next fit, from the file's stretches as a fit pairs them.
func TestSnapshotKeptChecks(t *testing.T) {
	files, _ := loadSnapshot(t)
	refs := map[string]syncRef{}
	for _, f := range files {
		refs[f.name] = f.ref
	}
	lives, _ := filepath.Glob(filepath.Join(snapshotDir(t), "synclive", "*.json"))
	timed, old, offsets := 0, 0, 0
	for _, p := range lives {
		b, _ := os.ReadFile(p)
		var s liveSaved
		if json.Unmarshal(b, &s) != nil || s.Check == nil || s.Check.State != ownTimed {
			continue
		}
		timed++
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		kept, n, _ := s.Check.check()
		f := &liveFile{done: s.Windows, ownDone: s.Own, check: kept, checkN: n}
		pairs := ownPairsOf(refs[name], f)
		c := checkOwnCached(f, pairs, true, name, 1)
		if len(s.Check.Offsets) == 0 {
			old++
			if n != 0 {
				t.Errorf("%s: kept without offsets, counted as done from %d stretches", name[:8], n)
			}
		}
		if len(c.Fit.Segments) > 0 {
			offsets++
		}
		t.Logf("%s: kept %s tight=%-5v from %2d stretches, offsets %-14s -> %s tight=%-5v from %2d, offsets %s (%d local)",
			name[:8], kept.State, kept.Tight, kept.Pairs, segText(kept.Fit), c.State, c.Tight, c.Pairs, segText(c.Fit), len(c.local))
	}
	t.Logf("%d timed checks kept, %d without offsets; %d with offsets after the next fit", timed, old, offsets)
}
