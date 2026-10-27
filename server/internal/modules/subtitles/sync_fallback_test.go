package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The Mentalist S03E24: turkcealtyazi's season 3 pack holds S03E23 and S03E24 as one file named
// for E23, so E24 is not in it. It was listed for E24 first, the players picked it (and its
// pre-timing) and fell back to an untimed subtitle. Such a download is remembered: no second
// download, a 404, and the lists leave it out.
func TestPackWithoutTheEpisodeIsRemembered(t *testing.T) {
	pack := makeZip(t, map[string]string{
		"The.Mentalist.S03E22.HDTV.tr.srt":    sampleSRT,
		"The.Mentalist.S03E23E24.HDTV.tr.srt": sampleSRT,
	})
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subtitle/100-200.zip" {
			http.NotFound(w, r)
			return
		}
		downloads++
		w.Write(pack)
	}))
	defer srv.Close()
	svc := NewService(nil, Config{CacheDir: t.TempDir(), SubDLAPIURL: srv.URL + "/api/v1", SubDLDownloadURL: srv.URL})
	ctx := context.Background()
	e23 := encodeID(SourceSubDL, "tr", "/subtitle/100-200.zip|3|23")
	e24 := encodeID(SourceSubDL, "tr", "/subtitle/100-200.zip|3|24")

	for i := range 2 {
		if _, err := svc.File(ctx, e24); !errors.Is(err, ErrNoSubtitle) || !strings.Contains(err.Error(), "episode not found") {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if downloads != 1 {
		t.Fatalf("%d downloads, want 1", downloads)
	}
	if b, err := svc.File(ctx, e23); err != nil || !bytes.Contains(b, []byte("Merhaba")) {
		t.Fatalf("E23: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	_, err := svc.File(ctx, e24)
	if !fileError(c, err) || w.Code != http.StatusNotFound {
		t.Fatalf("answer %d", w.Code)
	}

	q := Query{MediaType: "tv", Season: 3, Episode: 24, Langs: []string{"tr"}}
	all := []candidate{{Sub: Sub{ID: e24, Source: SourceSubDL, Language: "tr"}}, {Sub: Sub{ID: e23, Source: SourceSubDL, Language: "tr"}}}
	if subs := svc.rank(all, q); len(subs) != 1 || subs[0].ID != e23 {
		t.Fatalf("listed %+v", subs)
	}
}

// When the pick a player pre-times for the next episode gives no cues, the subtitle the
// episode's list offers first then is pre-timed instead, so it is timed when the episode opens.
func TestPretimeTakesTheNextPick(t *testing.T) {
	file := dialogue(43, episode)
	svc, _ := newPreTest(t, file)
	sub := subtitleFor(file, 1, func(float64) float64 { return -22 })
	packRef := "333899|/sub/333899/x.html|3|24|1"
	pack := encodeID(SourceTurkceAltyazi, "tr", packRef)
	if err := svc.files.write(SourceTurkceAltyazi, SourceTurkceAltyazi+"|"+packRef, ".none", []byte("episode not found in the subtitle pack")); err != nil {
		t.Fatal(err)
	}
	next := cacheProviderSub(t, svc, "12752783|next|3|24|0", sub)
	svc.sync.listed([]Sub{{ID: pack, Source: SourceTurkceAltyazi, Language: "tr"}, {ID: next, Source: SourceTurkceAltyazi, Language: "tr"}},
		nil, syncHash, 1)
	ctx := context.Background()

	if state, err := svc.PreTime(ctx, wrapSyncID(pack, syncHash, 1)); err != nil || state != syncStatePending {
		t.Fatalf("pre-timing: %q %v", state, err)
	}
	key, _ := syncKey(next, syncHash, 1)
	waitFlight(t, svc, "sync|"+key)
	if m := svc.sync.meta(key); m == nil || !m.fits() || !m.Pre || math.Abs(m.Fit.Segments[0].Offset+22) > 0.1 {
		t.Fatalf("next pick: %+v", m)
	}
	packKey, _ := syncKey(pack, syncHash, 1)
	if svc.sync.meta(packKey) != nil {
		t.Fatal("the pack was timed")
	}

	// Nothing else of the language: the pick's error, nothing timed
	other := encodeID(SourceTurkceAltyazi, "en", packRef)
	svc.sync.listed([]Sub{{ID: other, Source: SourceTurkceAltyazi, Language: "en"}}, nil, syncHash, 2)
	if _, err := svc.PreTime(ctx, wrapSyncID(other, syncHash, 2)); err == nil {
		t.Fatal("no error without another subtitle")
	}
}

// A re-fit whose offsets moved less than fitChanged notices keeps the served ones.
func TestPinnedFit(t *testing.T) {
	prev := &syncMeta{State: syncStateSynced, Live: true, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, 0.03}, {900, -1.5}}}}
	f := syncFit{Scale: 1, Segments: []syncSegment{{0, 0.04}, {900.6, -1.47}}, Score: 0.5}
	if p, ok := pinnedFit(prev, f); !ok || p.Segments[0].Offset != 0.03 || p.Segments[1].From != 900 || p.Score != 0.5 {
		t.Fatalf("pinned %+v %v", p, ok)
	}
	if f.Segments[0].Offset != 0.04 {
		t.Fatal("the new fit was changed")
	}
	moved := func(edit func(*syncFit)) syncFit {
		g := f
		g.Segments = append([]syncSegment(nil), f.Segments...)
		edit(&g)
		return g
	}
	for name, g := range map[string]syncFit{
		"offset": moved(func(g *syncFit) { g.Segments[0].Offset = 0.1 }),
		"split":  moved(func(g *syncFit) { g.Segments[1].From = 902 }),
		"lag":    moved(func(g *syncFit) { g.Lag = 0.1 }),
		"scale":  moved(func(g *syncFit) { g.Scale = 25 / 23.976 }),
		"count":  moved(func(g *syncFit) { g.Segments = g.Segments[:1] }),
	} {
		if _, ok := pinnedFit(prev, g); ok {
			t.Errorf("%s: pinned", name)
		}
	}
	poor := *prev
	poor.State = syncStatePoor
	if _, ok := pinnedFit(&poor, f); ok {
		t.Error("pinned to a fit that does not fit")
	}
	if _, ok := pinnedFit(nil, f); ok {
		t.Error("pinned without a fit")
	}

	// The own-track check keeps its offsets so too
	check := func(off float64) ownCheck {
		return ownCheck{State: ownTimed, Tight: true, Pairs: 12, Near: 0.6, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, off}}}}
	}
	if c := settleOwn(check(0.08), check(0.10)); c.Fit.Segments[0].Offset != 0.08 {
		t.Errorf("check offset %+.2f, want +0.08", c.Fit.Segments[0].Offset)
	}
	if c := settleOwn(check(0.08), check(0.2)); c.Fit.Segments[0].Offset != 0.2 {
		t.Errorf("check offset %+.2f, want +0.20", c.Fit.Segments[0].Offset)
	}
}

// The Mentalist S03E23 (real data, testdata/sync/mentalist_s03e23.json: the NF WEB-DL's 13 read
// windows and their speech, the played speech and cue tap windows of a viewing from the start,
// the subf2m Turkish subtitle's cue times). It replaced the season pack's double episode (E23 3 s
// late, then E24) and was fitted again with every re-fit, a minute at a time: its offset went
// +0.03 / +0.04 s and the own-track check's +0.08 / +0.10 s, and the line refinement moved lines
// back and forth by up to 0.45 s with them (13 flips, 11 moves of lines the viewer saw already).
// Kept offsets leave only the lines just refined in the stretch heard last (1 flip, 0.2 s).
func TestRefitsKeepRefinedLines(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "sync", "mentalist_s03e23.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	ref := syncRef{Probe: mediaProbe{Duration: fx.Duration, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"}, Paired: true}
	for _, w := range fx.Subs {
		ref.Windows = append(ref.Windows, w.window())
	}
	for _, w := range fx.Speech {
		ref.Speech = append(ref.Speech, w.window())
	}
	// The pack's file: E23 3 s late (another cut, not lined up), then E24
	var sub, double []span
	for _, c := range fx.Sub {
		sub = append(sub, span{c[0], c[1]})
		double = append(double, span{c[0] + 3, c[1] + 3})
	}
	for _, c := range sub {
		double = append(double, span{c.start + fx.Duration, c.end + fx.Duration})
	}
	svc := replayService(t.TempDir(), ref)
	y := svc.sync
	inner := cacheProviderSub(t, svc, "1|pack|3|23|1", double)
	alt := cacheProviderSub(t, svc, "2|alt|3|23|1", sub)
	y.listed([]Sub{{ID: inner, Source: SourceTurkceAltyazi, Language: "tr"}, {ID: alt, Source: SourceTurkceAltyazi, Language: "tr"}},
		nil, syncHash, 1)
	lf := y.liveFile(syncHash, 1)
	lf.mu.Lock()
	for _, l := range fx.Live {
		lf.done[int(math.Round(l.Start/syncWindowLen))] = liveWin{Bits: l.Bits, Frames: l.N}
	}
	for _, fw := range fx.Own {
		w := fw.window()
		var starts []int
		for i, v := range w.Speech {
			if v == 2 {
				starts = append(starts, i)
			}
		}
		lf.ownDone[int(math.Round(w.Start/syncWindowLen))] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech), Starts: starts}
	}
	lf.mu.Unlock()
	saved := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	defer log.SetOutput(saved)
	steps := replaySteps(t, svc, syncHash, 1, inner, ref)
	flips, shown, ahead, replaced := 0, 0, 0, 0
	for _, s := range steps {
		t.Log(s)
		flips += s.flips
		shown += s.shown
		ahead += s.ahead
		if strings.HasPrefix(s.state, syncStateReplaced) {
			replaced++
		}
	}
	t.Logf("%d steps (%d replaced); moved: shown %d, ahead %d, flips %d", len(steps), replaced, shown, ahead, flips)
	if replaced < len(steps)-3 || !strings.HasPrefix(steps[len(steps)-1].state, syncStateReplaced) || flips > 2 || shown > 5 {
		t.Errorf("%d of %d steps replaced, %d flips, %d lines moved that were shown", replaced, len(steps), flips, shown)
	}
}

// A replacement serves another subtitle's cues: logged as such, not as moves of lines matched by
// text across the two (The Mentalist S03E23: "max +1077.00s"). Within one subtitle a line said
// twice is matched to its own occurrence.
func TestReplacementIsNoMove(t *testing.T) {
	var buf bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(saved)
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	served := cacheProviderSub(t, svc, "1|served|3|23|1", []span{{100, 102}, {1177, 1179}})
	alt := cacheProviderSub(t, svc, "2|alt|3|23|1", []span{{100.5, 102}, {130, 132}})
	key, _ := syncKey(served, syncHash, 1)
	base, _ := svc.File(context.Background(), served)
	altVTT, _ := svc.File(context.Background(), alt)
	y.noteServed(key, served, syncHash, 1, 90)
	m := syncMeta{State: syncStateReplaced, ReplacedBy: alt, Live: true}
	y.storeWatched(key, served, syncHash, 1, base, nil, m, altVTT)
	if out := buf.String(); !strings.Contains(out, "serves the cues of") || strings.Contains(out, " moved ") {
		t.Fatalf("log: %s", out)
	}

	before := []liveCue{{100, 101, "Evet."}, {115, 116, "Evet."}, {120, 121, "Tamam"}}
	after := []liveCue{{78, 79, "Evet."}, {93, 94, "Evet."}, {98, 99, "Tamam"}}
	if mv := movesNear(before, after, 0, 300); mv.moved != 3 || mv.max != -22 || mv.median != -22 {
		t.Fatalf("moves %+v", mv)
	}
}
