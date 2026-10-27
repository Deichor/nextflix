package subtitles

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests of when a fit is accepted (real data from sync_accept_eval_test.go's
// fixtures, played from the file's start through decide).

// loadAcceptFixture is loadFixture with the speech read with the own-track windows.
func loadAcceptFixture(t *testing.T, name string) (*snapFile, []span) {
	t.Helper()
	f, sub := loadFixture(t, name)
	b, err := os.ReadFile(filepath.Join("testdata", "sync", name))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	for _, w := range fx.Speech {
		f.ref.Speech = append(f.ref.Speech, w.window())
	}
	f.ref.Paired = true
	return f, sub
}

// servedOffsetAt is the offset the step serves at subtitle time u (NaN while untimed).
func servedOffsetAt(st heardStep, u float64) float64 {
	if !st.fits {
		return math.NaN()
	}
	return offsetAt(st.served.Segments, u*st.served.Scale)
}

// The Mentalist S03E20 (testdata/sync/mentalist_s03e20.json: 6 read windows of an own track made
// for another cut, with their speech; 18 played minutes): the Turkish subtitle of the season pack
// was made for another cut, with offset steps at act breaks, and the speech fits it just under the
// gain bar (0.13-0.15). The fit accepted at 470 s heard put -3.67 s from 768 s where -1.85 s holds
// from 714 s to 1417 s; the re-fits found the step at 890 s heard (gain 0.146), but only one at
// 930 s cleared the bar, when the viewer (220 s behind on the TV) was at 708 s: 79 cues moved
// +1.82 s, the one on screen 0.56 s. Now the re-fit from 890 s replaces the stale offsets.
func TestHeldGainTakesTheActBreakStep(t *testing.T) {
	f, sub := loadAcceptFixture(t, "mentalist_s03e20.json")
	first := func() float64 {
		for _, st := range replayHeard(t, f, sub, 10, 940, 0, false) {
			if st.h >= 800 && math.Abs(servedOffsetAt(st, 760)+1.85) < 0.15 {
				return st.h
			}
		}
		return -1
	}
	if h := first(); h < 0 || h > 900 {
		t.Fatalf("the step at 714 s served from %.0f s heard (want by 900 s)", h)
	}
	saved := syncHeldGain
	syncHeldGain = 1
	defer func() { syncHeldGain = saved }()
	if h := first(); h >= 0 && h < 930 {
		t.Fatalf("without the held bar the step came at %.0f s heard (the server's 930 s)", h)
	}
}

// Avatar: Seven Havens S01E07 (testdata/sync/avatar_s01e07.json): the Turkish subtitle is 5.9 s
// late, -9.6 s after 865 s. From the cue tap's first three minutes of the own track the right
// offset ties with one 39 s further (-45.3 s; the speech of those minutes has the same second
// peak), so nothing is accepted; with the first four read windows the one change fits under its
// peak bar, and the log names it; the fourth played minute takes it over the bar.
func TestAvatarFirstMinutesWaitForEvidence(t *testing.T) {
	f, sub := loadAcceptFixture(t, "avatar_s01e07.json")
	for _, st := range replayHeard(t, f, sub, 10, 190, -1, true) {
		if st.fits {
			t.Fatalf("%.0f s heard: accepted %s from the three own-track minutes", st.h, describeFit(st.served))
		}
	}
	steps := replayHeard(t, f, sub, 10, 220, 170, true)
	for _, st := range steps {
		switch {
		case st.h == 180 && (st.fits || st.meta == nil || !strings.Contains(st.meta.Fit.missedBars(), "one change")):
			t.Fatalf("180 s heard: fits=%v, missed %q", st.fits, st.meta.Fit.missedBars())
		case st.h >= 200 && (!st.fits || math.Abs(servedOffsetAt(st, 100)+5.9) > 0.1):
			t.Fatalf("%.0f s heard: fits=%v %s", st.h, st.fits, describeFit(st.served))
		}
	}
}
