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
	alt     string
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
		ref.Paired = true
	}
	svc := replayService(t.TempDir(), ref)
	var sub []span
	for _, c := range fx.Sub {
		sub = append(sub, span{c[0], c[1]})
	}
	v := &fixtureViewing{svc: svc, ref: ref, done: map[int]liveWin{}, ownDone: map[int]liveWin{}}
	v.inner = cacheProviderSub(t, svc, "1|fixture|1|1|1", sub)
	v.key, _ = syncKey(v.inner, syncHash, 1)
	if len(fx.Alt) > 0 {
		var alt []span
		for _, c := range fx.Alt {
			alt = append(alt, span{c[0], c[1]})
		}
		v.alt = cacheProviderSub(t, svc, "2|alt|1|1|1", alt)
		svc.sync.listed([]Sub{{ID: v.inner, Source: SourceTurkceAltyazi, Language: "tr"}, {ID: v.alt, Source: SourceTurkceAltyazi, Language: "tr"}},
			nil, syncHash, 1)
	}
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
	if fx.Check != nil {
		if c, n, ok := fx.Check.check(); ok {
			lf.mu.Lock()
			lf.check, lf.checkN = c, n
			lf.mu.Unlock()
		}
	}
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

// Westworld S02E04 (real data, testdata/sync/westworld_s02e04.json, a 4K file with the Turkish
// OpenSubtitles subtitle): the subtitle fits at +0.06 s, and a re-fit at 44 minutes moved it to
// +0.44 s, its lines 0.4 s late and 8-30 % of them on the speech for 18 minutes (chance 33 %), until
// the final fit. Such a move stays out now: nothing that far off is served.
func TestRefitStaysOnTheSpeech(t *testing.T) {
	v := loadFixtureViewing(t, "westworld_s02e04.json")
	for g := 0; g <= v.last; g++ {
		m := v.playWindows(g)
		viewer := float64(g+1)*syncWindowLen - fbViewerLag
		if viewer < 2600 || m == nil || !m.fits() {
			continue
		}
		if off := offsetAt(m.Fit.Segments, viewer); off > 0.3 {
			t.Fatalf("at %.0f s served %s", viewer, offsetsText(m.Fit))
		}
	}
}

// servedChanges plays the fixture's windows one by one and counts the changes of the subtitle
// served, and those back to one served before.
func (v *fixtureViewing) servedChanges() (swaps, backs int, last *syncMeta) {
	by := v.inner
	seen := map[string]bool{by: true}
	for g := 0; g <= v.last; g++ {
		last = v.playWindows(g)
		if now := servedBy(v.inner, last); now != by {
			swaps++
			if seen[now] {
				backs++
			}
			seen[now], by = true, now
		}
	}
	return swaps, backs, last
}

// Westworld S02E04 (the fixture above, with the Turkish subtitle listed beside it): the subtitle was
// not accepted in the first minutes though it sits on the speech as it is, and its lines gave way
// to the other's at 231 s and came back at 336 s. They stay.
func TestRightSubtitleIsNotReplacedInAViewing(t *testing.T) {
	v := loadFixtureViewing(t, "westworld_s02e04.json")
	if v.alt == "" {
		t.Fatal("no alternative in the fixture")
	}
	if swaps, _, last := v.servedChanges(); swaps != 0 || !last.fits() {
		t.Errorf("%d changes, final %s", swaps, logState(last))
	}
}

// A viewer keeps a replacement while it fits, although the subtitle's own fit fits by now (real
// speech and lines of Avatar: Seven Havens S01E12, the replacement the same lines); when it stops
// fitting, the subtitle comes back.
func TestReplacementStaysInAViewing(t *testing.T) {
	v := loadFixtureViewing(t, "avatar_s01e12.json")
	y := v.svc.sync
	var sub []span
	for _, c := range vttCues(v.base) {
		sub = append(sub, span{c.start, c.end})
	}
	replaced := func(alt string) {
		altBase, err := v.svc.File(context.Background(), alt)
		if err != nil {
			t.Fatal(err)
		}
		y.store(v.key, syncMeta{State: syncStateReplaced, ReplacedBy: alt, Live: true}, altBase)
		y.listed([]Sub{{ID: v.inner, Source: SourceTurkceAltyazi, Language: "tr"}, {ID: alt, Source: SourceTurkceAltyazi, Language: "tr"}},
			nil, syncHash, 1)
	}
	alt := cacheProviderSub(t, v.svc, "3|same|1|1|1", sub)
	replaced(alt)
	if m := v.playWindows(v.last); m.State != syncStateReplaced || m.ReplacedBy != alt {
		t.Errorf("a fitting replacement gave way: %s", logState(m))
	}
	// Lines of no fit at all: another length, no pattern of the speech
	var none []span
	for i := range 40 {
		none = append(none, span{float64(i) * 3, float64(i)*3 + 1})
	}
	gone := cacheProviderSub(t, v.svc, "4|none|1|1|1", none)
	replaced(gone)
	if m := v.playWindows(v.last); !m.fits() {
		t.Errorf("a replacement that does not fit stayed: %s", logState(m))
	}
}

// Another subtitle's lines come only when what is served is near chance on the speech heard.
func TestServedOffSpeech(t *testing.T) {
	v := loadFixtureViewing(t, "avatar_s01e12.json")
	y := v.svc.sync
	if !y.servedOffSpeech(v.key, syncHash, 1, v.base, nil, v.ref.Probe.Duration) {
		t.Error("nothing heard yet: it may be replaced")
	}
	v.playWindows(v.last)
	if y.servedOffSpeech(v.key, syncHash, 1, v.base, y.meta(v.key), v.ref.Probe.Duration) {
		t.Error("the timed lines are on the speech")
	}
	var off []liveCue
	for _, c := range vttCues(v.base) {
		off = append(off, liveCue{start: c.start + 3, end: c.end + 3, text: c.text})
	}
	if !y.servedOffSpeech(v.key, syncHash, 1, renderVTT(off, false), nil, v.ref.Probe.Duration) {
		t.Error("lines 3 s off are not on the speech")
	}
}

// The Mentalist S04E16 (real data, testdata/sync/mentalist_s04e16.json, the Turkish OpenSubtitles
// subtitle, made for a cut with other ad breaks): the first fit puts its last step, 2.8 s, at
// 1878 s, and the speech has it at 1961 s. Re-fits found it from 31 minutes on and were held, their
// line refinement weighed more than the step, and the lines up to 1961 s came 2.8 s early until the
// final fit at 37 minutes. Now the step is in place before the viewer gets there.
func TestRefitMovesAStepBeforeTheViewer(t *testing.T) {
	v := loadFixtureViewing(t, "mentalist_s04e16.json")
	for g := 0; g <= v.last; g++ {
		m := v.playWindows(g)
		viewer := float64(g+1)*syncWindowLen - fbViewerLag
		if viewer < 1840 || viewer > 1960 || m == nil || !m.fits() {
			continue
		}
		// subtitle times of the lines before the step, as the viewer gets there
		if off := offsetAt(m.Fit.Segments, 1940); off < -12 {
			t.Fatalf("at %.0f s served %s", viewer, offsetsText(m.Fit))
		}
	}
}

func TestDropZigzags(t *testing.T) {
	var cues []liveCue
	for s := 10.0; s < 590; s += 6 {
		cues = append(cues, liveCue{start: s, end: s + 2})
	}
	// The speech: starts of the lines at +0.3 s, except the middle stretch when real
	speech := func(real bool) []refWindow {
		w := refWindow{Start: 0, Kind: "live", Speech: make([]byte, 600*syncRate)}
		for _, c := range cues {
			at := c.start + 0.3
			if real && c.start >= 200 && c.start < 400 {
				at = c.start - 0.3
			}
			i := int(at * syncRate)
			for k := i; k < i+150; k++ {
				w.Speech[k] = 1
			}
		}
		return []refWindow{w}
	}
	f := syncFit{Scale: 1, Segments: []syncSegment{{Offset: 0.3}, {From: 200, Offset: -0.3}, {From: 400, Offset: 0.3}}}
	if got := dropZigzags(f, cues, speech(false)); len(got.Segments) != 1 || got.Segments[0].Offset != 0.3 {
		t.Errorf("a zigzag stays: %s", offsetsText(got))
	}
	if got := dropZigzags(f, cues, speech(true)); len(got.Segments) != 3 {
		t.Errorf("speech backs the stretch, but: %s", offsetsText(got))
	}
	if got := dropZigzags(f, cues, nil); len(got.Segments) != 3 {
		t.Errorf("no speech heard, but: %s", offsetsText(got))
	}
}
