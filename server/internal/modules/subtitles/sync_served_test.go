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
