package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureViewing is a testdata/sync pair loaded into a replay service, its live windows put aside
// for playWindows.
type fixtureViewing struct {
	svc     *Service
	ref     syncRef
	inner   string
	key     string
	base    []byte
	done    map[int]liveWin
	ownDone map[int]liveWin
	last    int
}

func loadFixtureViewing(t *testing.T, name string) *fixtureViewing {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sync", name))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	codec := fx.Codec
	if codec == "" {
		codec = "subrip"
	}
	ref := syncRef{Probe: mediaProbe{Duration: fx.Duration, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: codec}}
	for _, w := range fx.Subs {
		ref.Windows = append(ref.Windows, w.window())
	}
	for _, w := range fx.Speech {
		ref.Speech = append(ref.Speech, w.window())
	}
	svc := replayService(t.TempDir(), ref)
	var sub []span
	for _, c := range fx.Sub {
		sub = append(sub, span{c[0], c[1]})
	}
	v := &fixtureViewing{svc: svc, ref: ref, done: map[int]liveWin{}, ownDone: map[int]liveWin{}}
	v.inner = cacheProviderSub(t, svc, "1|fixture|1|1|1", sub)
	v.key, _ = syncKey(v.inner, syncHash, 1)
	if v.base, err = svc.File(context.Background(), v.inner); err != nil {
		t.Fatal(err)
	}
	for _, l := range fx.Live {
		g := int(math.Round(l.Start / syncWindowLen))
		v.done[g] = liveWin{Bits: l.Bits, Frames: l.N}
		v.last = max(v.last, g)
	}
	for _, fw := range fx.Own {
		w := fw.window()
		var starts []int
		for i, s := range w.Speech {
			if s == 2 {
				starts = append(starts, i)
			}
		}
		g := int(math.Round(w.Start / syncWindowLen))
		v.ownDone[g] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech), Starts: starts}
		v.last = max(v.last, g)
	}
	lf := svc.sync.liveFile(syncHash, 1)
	lf.setDuration(fx.Duration)
	saved := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(saved) })
	return v
}

// playWindows makes the grid windows up to g heard and re-fits, the viewer fbViewerLag behind them.
func (v *fixtureViewing) playWindows(g int) *syncMeta {
	y := v.svc.sync
	lf := y.liveFile(syncHash, 1)
	lf.mu.Lock()
	lf.done, lf.ownDone = map[int]liveWin{}, map[int]liveWin{}
	for w, d := range v.done {
		if w <= g {
			lf.done[w] = d
		}
	}
	for w, d := range v.ownDone {
		if w <= g {
			lf.ownDone[w] = d
		}
	}
	lf.complete = false
	lf.checkComplete()
	lf.mu.Unlock()
	y.noteServed(v.key, v.inner, syncHash, 1, math.Max(float64(g+1)*syncWindowLen-fbViewerLag, 1))
	y.decide(context.Background(), v.inner, syncHash, 1, v.base, v.ref, false)
	return y.meta(v.key)
}

func logState(m *syncMeta) string {
	if m == nil {
		return "none"
	}
	return m.State + " " + offsetsText(m.Fit)
}

func (v *fixtureViewing) served(m *syncMeta) []liveCue {
	return v.svc.sync.servedCues(v.key, v.base, m)
}

// Avatar: Seven Havens S01E12 (real data, testdata/sync/avatar_s01e12.json): the Turkish subtitle's
// last line reads "00:22:06.125 --> 00:44:12.000", past the end of the 22:44 file. Every fit (+0.3 s,
// score 0.50 over 0.13, peak 0.33) was rejected as another cut, and the episode was served raw and
// 0.3 s early from start to end.
func TestBrokenCueEndKeepsTheFit(t *testing.T) {
	v := loadFixtureViewing(t, "avatar_s01e12.json")
	m := v.playWindows(v.last)
	if m == nil || !m.fits() {
		t.Fatalf("not fitted: %s", logState(m))
	}
	if off := m.Fit.Segments[0].Offset; len(m.Fit.Segments) != 1 || off < 0.2 || off > 0.4 {
		t.Errorf("offsets %s", offsetsText(m.Fit))
	}
	cues := v.served(m)
	if end := cues[len(cues)-1].end; end > v.ref.Probe.Duration {
		t.Errorf("the last line ends at %.1f s", end)
	}
	if s := string(v.base); strings.Contains(s, "00:44:12") {
		t.Error("the broken end is still served")
	}
}

// The Mentalist (real data, testdata/sync/mentalist_steps.json, a season pack's Turkish subtitle
// with four offset steps down of 2-4 s): lines over a second on top of each other at three of
// them, one out of order. The line starts stay where the fit put them.
func TestNoLinesStackedAtOffsetSteps(t *testing.T) {
	v := loadFixtureViewing(t, "mentalist_steps.json")
	m := v.playWindows(v.last)
	if m == nil || !m.fits() || len(m.Fit.Segments) < 4 {
		t.Fatalf("not fitted with steps: %s", logState(m))
	}
	cues := v.served(m)
	if n := servedStacks(cues); n > 0 {
		t.Errorf("%d lines stacked", n)
	}
	if n := servedInverted(vttCues(v.base), cues); n > 0 {
		t.Errorf("%d lines out of order", n)
	}
}

func TestUnstackSteps(t *testing.T) {
	f := syncFit{Scale: 1, Segments: []syncSegment{{Offset: 0}, {From: 100, Offset: -4}}}
	clock := []float64{90, 97, 99, 103, 110}
	cues := []liveCue{
		{90, 92, "a"},
		{97, 100, "b"}, // ends after the first line of the step
		{99, 101.5, "c"},
		{99, 101, "d"}, // starts with c, though it comes after it
		{106, 108, "e"},
	}
	got := unstackSteps(cues, clock, f)
	if got[1].end != 99 {
		t.Errorf("the line before the step ends at %.2f", got[1].end)
	}
	if got[2].text != "c" || got[2].start != 99 || got[3].start != 99 {
		t.Errorf("out of order lines: %+v", got[2:4])
	}
	if got[2].end != 100 {
		t.Errorf("a line starting with the next one shows until %.2f, want the least a line shows", got[2].end)
	}
	if got[0].end != 92 || got[4].end != 108 {
		t.Errorf("lines away from the step changed: %+v", got)
	}
	// A step up leaves a pause: nothing to do
	up := syncFit{Scale: 1, Segments: []syncSegment{{Offset: 0}, {From: 100, Offset: 4}}}
	same := []liveCue{{97, 100, "a"}, {104, 106, "b"}}
	if out := unstackSteps(same, []float64{97, 100}, up); out[0].end != 100 {
		t.Errorf("step up: %+v", out)
	}
}
