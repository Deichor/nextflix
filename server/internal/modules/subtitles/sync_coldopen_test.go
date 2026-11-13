package subtitles

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The Mentalist S03E02 with its English subf2m subtitle (testdata/sync/mentalist_s03e02_en.json:
// its cue times; the file's windows of mentalist_s03e02.json). Its offsets by the speech
// (scripts/subtitle-truth, en_lines of 2026-10-16), on the subtitle's own times.
var mentalistS03E02EnglishTruth = []struct{ from, to, offset float64 }{
	{41, 106.2, 1.39}, {125.5, 798.4, 3.26}, {819.4, 1277, 5.28}, {1286.9, 2022.6, 7.32}, {2047.8, 2550.7, 9.33},
}

func loadEnglishS03E02(t *testing.T) []span {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sync", "mentalist_s03e02_en.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	var out []span
	for _, c := range fx.Sub {
		out = append(out, span{c[0], c[1]})
	}
	return out
}

// Its cold open has 8 lines in two windows: at +1.39 s they matched 4.2 lines more than at the
// +3.30 s after the title sequence, under the 4.95 a change of 1.9 s cost, so the teaser was
// served 1.9 s late. A change at the title sequence's pause (19 s without lines) in the first
// minutes costs syncOpenPenalty: the teaser is timed on its own once its windows were heard.
func TestMentalistS03E02EnglishColdOpen(t *testing.T) {
	f, _ := loadFixture(t, "mentalist_s03e02.json")
	en := loadEnglishS03E02(t)
	steps := replayLive(t, f, en, true, nil)
	last := steps[len(steps)-1].served
	for _, p := range mentalistS03E02EnglishTruth {
		for u := p.from; u <= p.to; u += 5 {
			if e := last.mapTime(u) - (u + p.offset); math.Abs(e) > 0.15 {
				t.Fatalf("at %.0f s %+.2f s off: %s", u, e, describeFit(last))
			}
		}
	}
	// from the first fit on, the teaser has its own offset
	for _, st := range steps {
		if st.fits {
			if e := st.served.mapTime(70) - (70 + 1.39); math.Abs(e) > 0.15 {
				t.Fatalf("minute %d: the teaser %+.2f s off: %s", st.minute, e, describeFit(st.served))
			}
		}
	}
}

func TestOpenChange(t *testing.T) {
	// lines every 3 s, a 15 s pause at 100-115 s and at 500-515 s, 6 s at 200-206 s
	var cues []span
	for s := 0.0; s < 700; s += 3 {
		if s > 100 && s < 115 || s > 500 && s < 515 || s > 200 && s < 206 {
			continue
		}
		cues = append(cues, span{s, s + 2})
	}
	for _, c := range []struct {
		lo, hi float64
		want   bool
	}{
		{60, 180, true},   // the pause lies in the stretch
		{109, 109, true},  // a change inside it
		{150, 260, false}, // only a short pause
		{460, 580, false}, // past the opening minutes
		{0, 90, false},    // before it
		{20, 480, false},  // a stretch reaching past the opening minutes
	} {
		if got := openChange(cues, 1, c.lo, c.hi); got != c.want {
			t.Errorf("openChange %v-%v = %v", c.lo, c.hi, got)
		}
	}
}
