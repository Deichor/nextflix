package subtitles

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// dialogueCues: a line every 3 s (2.5 s long) from a to b, texted by prefix and time.
func dialogueCues(prefix string, a, b float64) []liveCue {
	var out []liveCue
	for t := a; t < b; t += 3 {
		out = append(out, liveCue{start: t, end: t + 2.5, text: fmt.Sprintf("%s line at %.0f", prefix, t)})
	}
	return out
}

// without drops the cues starting in [a, b).
func without(cues []liveCue, a, b float64) []liveCue {
	var out []liveCue
	for _, c := range cues {
		if c.start < a || c.start >= b {
			out = append(out, c)
		}
	}
	return out
}

// scene is dialogue with the given pauses (no lines in them, in either subtitle).
func scene(prefix string, end float64, pauses ...[2]float64) []liveCue {
	cues := dialogueCues(prefix, 0, end)
	for _, p := range pauses {
		cues = without(cues, p[0], p[1])
	}
	return cues
}

func TestFindHolesFindsAMissingScene(t *testing.T) {
	ref := scene("Ref", 900, [2]float64{300, 345})
	sub := without(scene("Alt", 900, [2]float64{300, 345}), 501, 600)
	holes := findHoles(sub, ref, holeEvidence{}, 900)
	if len(holes) != 1 || holes[0].First < 500 || holes[0].Last > 601 || holes[0].Lines < 30 {
		t.Fatalf("holes %+v", holes)
	}
}

func TestFindHolesIgnoresPausesOfBoth(t *testing.T) {
	// A 45 s pause in both (music, action), a song only the reference subtitles, sound notes
	ref := scene("Ref", 900, [2]float64{300, 345}, [2]float64{600, 640})
	for t := 600.0; t < 640; t += 4 {
		ref = append(ref, liveCue{start: t, end: t + 3, text: "♪ We sail the seven seas ♪"})
	}
	ref = append(ref, liveCue{start: 320, end: 322, text: "[explosion]"}, liveCue{start: 325, end: 327, text: "(thunder rumbling)"})
	sub := scene("Alt", 900, [2]float64{300, 345}, [2]float64{600, 640})
	ref = aiCues(renderVTT(ref, false))
	if holes := findHoles(sub, ref, holeEvidence{}, 900); len(holes) != 0 {
		t.Fatalf("holes %+v", holes)
	}
	// Short pauses, or few lines in a long one, are no holes either
	sub = without(without(scene("Alt", 900), 400, 412), 700, 718)
	ref = without(scene("Ref", 900), 703, 712) // 3 lines left in the 18 s pause of the subtitle
	if holes := findHoles(sub, ref, holeEvidence{}, 900); len(holes) != 0 {
		t.Fatalf("holes %+v", holes)
	}
}

func TestFindHolesStartsLateEndsEarly(t *testing.T) {
	ref := scene("Ref", 1200)
	sub := without(without(scene("Alt", 1200), 0, 60), 1100, 1200)
	holes := findHoles(sub, ref, holeEvidence{}, 1210)
	if len(holes) != 2 || holes[0].From != 0 || holes[0].Lines < 18 || holes[1].Last < 1199 || holes[1].Lines < 30 {
		t.Fatalf("holes %+v", holes)
	}
}

// ownWindow is a window of the file's own track with the given cue starts.
func ownWindow(start float64, cues []liveCue) refWindow {
	w := refWindow{Start: start, Kind: "subs", Speech: make([]byte, int(syncWindowLen*syncRate))}
	for _, c := range cues {
		markCue(w.Speech, c.start-start, c.end-start)
	}
	return w
}

func TestFindHolesEvidence(t *testing.T) {
	ref := scene("Ref", 900)
	sub := without(scene("Alt", 900), 480, 560)
	own := scene("Own", 900)
	// The file's own track has lines there: a hole
	ev := holeEvidence{own: []refWindow{ownWindow(60, own), ownWindow(480, own), ownWindow(700, own)}}
	if holes := findHoles(sub, ref, ev, 900); len(holes) != 1 {
		t.Fatalf("own track with lines: holes %+v", holes)
	}
	// The own track is silent where the reference has lines: they are not this file's
	quiet := without(own, 470, 560)
	ev = holeEvidence{own: []refWindow{ownWindow(60, quiet), ownWindow(480, quiet), ownWindow(700, quiet)}}
	if holes := findHoles(sub, ref, ev, 900); len(holes) != 0 {
		t.Fatalf("silent own track: holes %+v", holes)
	}
	// A forced-only own track (a line a minute) speaks for nothing
	forced := []liveCue{{start: 100, end: 102, text: "SIGN"}, {start: 760, end: 762, text: "SIGN"}}
	ev = holeEvidence{own: []refWindow{ownWindow(60, forced), ownWindow(480, forced), ownWindow(700, forced)}}
	if holes := findHoles(sub, ref, ev, 900); len(holes) != 1 {
		t.Fatalf("forced own track: holes %+v", holes)
	}
	// Silent audio where the reference has lines
	silent := refWindow{Start: 470, Kind: "live", Speech: make([]byte, int(syncWindowLen*syncRate))}
	if holes := findHoles(sub, ref, holeEvidence{speech: []refWindow{silent}}, 900); len(holes) != 0 {
		t.Fatalf("silent audio: holes %+v", holes)
	}
	// Speech there: still a hole
	talk := silent
	talk.Speech = make([]byte, len(silent.Speech))
	for i := range talk.Speech {
		if i%300 < 200 {
			talk.Speech[i] = 1
		}
	}
	if holes := findHoles(sub, ref, holeEvidence{speech: []refWindow{talk}}, 900); len(holes) != 1 {
		t.Fatalf("speech: holes %+v", holes)
	}
	// Without a reference subtitle the own track alone finds it (nothing to fill it with)
	ev = holeEvidence{own: []refWindow{ownWindow(490, own)}}
	if holes := findHoles(sub, nil, ev, 900); len(holes) != 1 || holes[0].Lines < 15 {
		t.Fatalf("own track only: holes %+v", holes)
	}
}

func TestHoleLinesNeverOverlapTheSubtitle(t *testing.T) {
	sub := []liveCue{{start: 0, end: 10, text: "a"}, {start: 40, end: 50, text: "b"}}
	ref := []liveCue{
		{start: 9, end: 12, text: "cut at the start"},   // middle inside: starts after a
		{start: 8, end: 10.3, text: "mostly in a"},      // middle inside a: dropped
		{start: 15, end: 18, text: "inside"},            // kept as it is
		{start: 17, end: 19, text: "overlaps inside"},   // the reference's own overlaps stay
		{start: 39.8, end: 40.4, text: "too short now"}, // middle at 40.1, in b
		{start: 38, end: 41, text: "cut at the end"},
		{start: 20, end: 22, text: "♪ la la ♪"}, // a song: not filled
	}
	h := subHole{From: 10, To: 40}
	got := holeLines(ref, []subHole{h})
	want := map[string][2]float64{"cut at the start": {10.05, 12}, "inside": {15, 18}, "overlaps inside": {17, 19}, "cut at the end": {38, 39.95}}
	if len(got) != len(want) {
		t.Fatalf("lines %+v", got)
	}
	for _, c := range got {
		w, ok := want[c.text]
		if !ok || c.start != w[0] || c.end != w[1] {
			t.Fatalf("line %+v, want %v", c, w)
		}
		for _, s := range sub {
			if c.start < s.end && c.end > s.start {
				t.Fatalf("%+v overlaps %+v", c, s)
			}
		}
	}
}

func TestHoleRankingPrefersCompleteSubtitles(t *testing.T) {
	q := Query{MediaType: "tv", Langs: []string{"tr"}, Title: "Show.S01E02.1080p.WEB-DL-GRP"}
	holey := candidate{Sub: Sub{ID: "holey", Language: "tr", Release: "Show.S01E02.1080p.WEB-DL-GRP", Holes: 40}, AutoSync: 1}
	complete := candidate{Sub: Sub{ID: "complete", Language: "tr", Release: "Show.S01E02.HDTV"}}
	poor := candidate{Sub: Sub{ID: "poor", Language: "tr", Release: "Show.S01E02.1080p.WEB-DL-GRP"}, AutoSync: -1}
	forced := candidate{Sub: Sub{ID: "forced", Language: "tr", Forced: true}}
	for _, c := range []struct {
		in   []candidate
		want string
	}{
		{[]candidate{holey, complete}, "complete,holey"},
		{[]candidate{holey, poor}, "holey,poor"},
		{[]candidate{forced, poor, holey, complete}, "complete,holey,poor,forced"},
	} {
		var got []string
		for _, s := range rankCandidates(c.in, q) {
			got = append(got, s.ID)
		}
		if strings.Join(got, ",") != c.want {
			t.Fatalf("order %v, want %s", got, c.want)
		}
	}
	// Without holes known the order is as before: the release match first
	h := holey
	h.Holes = 0
	if got := rankCandidates([]candidate{complete, h}, q); got[0].ID != "holey" || got[0].Holes != 0 {
		t.Fatalf("order %v", got)
	}
}

// holeTest is a service with a Turkish subtitle (lines missing in holes) and an English one of
// the same file, both timed to fit it, listed for the file.
type holeTest struct {
	s       *Service
	tr, en  string // listed ids
	trCues  []liveCue
	enCues  []liveCue
	hash    string
	index   int
	trInner string
	holeA   [2]float64
	holeB   [2]float64
	fake    *fakeAI
	mu      sync.Mutex
	context []aiInLine // context lines of the requests
}

func newHoleTest(t *testing.T, ai bool) *holeTest {
	t.Helper()
	fastAI(t)
	h := &holeTest{hash: strings.Repeat("9a", 20), index: 1, holeA: [2]float64{300, 360}, holeB: [2]float64{840, 880}, fake: &fakeAI{}}
	if ai {
		call := h.fake.call(t)
		h.s = aiTestService(t, func(ctx context.Context, key, system, user string) (string, aiTokens, error) {
			h.mu.Lock()
			for _, l := range aiRequestLines(t, user) {
				if l.ID == 0 {
					h.context = append(h.context, l)
				}
			}
			h.mu.Unlock()
			return call(ctx, key, system, user)
		})
	} else {
		h.s = NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	}
	h.enCues = scene("Line", 1200, [2]float64{600, 650})
	tr := scene("Satır", 1200, [2]float64{600, 650})
	h.trCues = without(without(tr, h.holeA[0], h.holeA[1]), h.holeB[0], h.holeB[1])
	store := func(source, lang, ref string, cues []liveCue) string {
		if err := h.s.files.write(source, source+"|"+ref, ".vtt", renderVTT(cues, false)); err != nil {
			t.Fatal(err)
		}
		id := encodeID(source, lang, ref)
		key, _ := syncKey(id, h.hash, h.index)
		h.s.sync.store(key, syncMeta{State: syncStateUnchanged}, nil)
		return id
	}
	h.trInner = store(SourceTurkceAltyazi, "tr", "1|/sub/1/show.html|1|2|1", h.trCues)
	enInner := store(SourceSubDL, "en", "2", h.enCues)
	list := h.s.sync.listed([]Sub{
		{ID: h.trInner, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: enInner, Source: SourceSubDL, Language: "en"},
	}, map[string]bool{}, h.hash, h.index)
	h.tr, h.en = list[0].ID, list[1].ID
	h.s.sync.noteTitle(h.hash, h.index, aiMeta{"tv", 1, 1, 2})
	return h
}

func TestHolesFilledWithAI(t *testing.T) {
	h := newHoleTest(t, true)
	ctx := context.Background()
	a, err := h.s.FileFor(ctx, h.tr, 310) // the player is in the first hole
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(a.VTT), wantTr("Line line at 312")) {
		t.Fatalf("first answer without the lines at the player:\n%s", a.VTT)
	}
	var final FileAnswer
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if final, err = h.s.FileFor(ctx, h.tr, 310); err != nil {
			t.Fatal(err)
		}
		if !final.Partial {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Partial || strings.Contains(string(final.VTT), AINote) || final.Sync != syncStateUnchanged {
		t.Fatalf("final answer partial=%v sync=%q:\n%s", final.Partial, final.Sync, final.VTT)
	}
	got := vttCues(final.VTT)
	human := map[liveCue]bool{}
	for _, c := range h.trCues {
		human[c] = true
	}
	added := 0
	for _, c := range got {
		if human[c] {
			delete(human, c)
			continue
		}
		added++
		in := (c.start >= h.holeA[0] && c.end <= h.holeA[1]+3) || (c.start >= h.holeB[0] && c.end <= h.holeB[1]+3)
		if !in || !strings.HasPrefix(c.text, "çev Line line at") {
			t.Fatalf("added cue %+v", c)
		}
		if want := fmt.Sprintf("çev Line line at %.0f", c.start); c.text != want {
			t.Fatalf("added cue %+v, want the translation of its own line", c)
		}
	}
	if len(human) != 0 || added != 34 {
		t.Fatalf("%d human cues lost, %d added", len(human), added)
	}
	if final.Holes != "2 holes, 34 lines, 34 filled" {
		t.Fatalf("holes %q", final.Holes)
	}
	// Only the lines in the holes were asked; the subtitle's own lines came as context
	asked := 0
	for _, r := range h.fake.requests() {
		for _, l := range r {
			if !strings.HasPrefix(l.Text, "Line line at") {
				t.Fatalf("asked %q", l.Text)
			}
			asked++
		}
	}
	if asked != 34 {
		t.Fatalf("%d lines asked", asked)
	}
	h.mu.Lock()
	given := false
	for _, l := range h.context {
		given = given || (l.Context == "Line line at 297" && l.Translated == "Satır line at 297")
	}
	h.mu.Unlock()
	if !given {
		t.Fatal("the subtitle's own line before the hole was not given as context")
	}
	// The list knows the holes now
	if n := h.s.sync.holesOf(h.trInner, h.hash, h.index); n != 34 {
		t.Fatalf("holesOf %d", n)
	}
}

func TestHolesWithoutAIOnlyCount(t *testing.T) {
	h := newHoleTest(t, false)
	plain, err := h.s.fileFor(context.Background(), h.tr, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.s.FileFor(context.Background(), h.tr, 0)
	if err != nil || string(a.VTT) != string(plain.VTT) || a.Partial || a.Holes != "2 holes, 34 lines" {
		t.Fatalf("answer %v partial=%v holes=%q", err, a.Partial, a.Holes)
	}
	if n := h.s.sync.holesOf(h.trInner, h.hash, h.index); n != 34 {
		t.Fatalf("holesOf %d", n)
	}
	// A complete subtitle is served exactly as before
	en, _ := h.s.fileFor(context.Background(), h.en, 0, false)
	b, err := h.s.FileFor(context.Background(), h.en, 0)
	if err != nil || string(b.VTT) != string(en.VTT) || b.Holes != "" {
		t.Fatalf("complete subtitle changed: %v holes=%q", err, b.Holes)
	}
}

func TestHolesNearestToThePlayerFirst(t *testing.T) {
	h := newHoleTest(t, true)
	if _, err := h.s.FileFor(context.Background(), h.tr, 845); err != nil {
		t.Fatal(err)
	}
	// The small first request (the player waits for it) holds the whole hole at the player
	found := false
	for _, r := range h.fake.requests() {
		var texts []string
		for _, l := range r {
			texts = append(texts, l.Text)
		}
		all := len(r) <= aiFirstBatch
		for at := 840; at < 880; at += 3 {
			all = all && containsString(texts, fmt.Sprintf("Line line at %d", at))
		}
		found = found || all
	}
	if !found {
		t.Fatalf("requests %v", h.fake.requests())
	}
}

func TestHolesWaitForTheReferenceTiming(t *testing.T) {
	h := newHoleTest(t, true)
	// The English subtitle is not timed yet (and cannot be: no file to read)
	enInner, _, _, _ := unwrapSyncID(h.en)
	key, _ := syncKey(enInner, h.hash, h.index)
	if err := os.Remove(h.s.files.path("synced", key, ".json")); err != nil {
		t.Fatal(err)
	}
	h.s.sync.mu.Lock()
	delete(h.s.sync.metas, key)
	h.s.sync.mu.Unlock()

	a, err := h.s.FileFor(context.Background(), h.tr, 310)
	if err != nil || !a.Partial || a.Holes != "" {
		t.Fatalf("while the reference is timed: partial=%v holes=%q %v", a.Partial, a.Holes, err)
	}
	// Its timing fails: the subtitle is served as it is, final
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if a, err = h.s.FileFor(context.Background(), h.tr, 310); err != nil || !a.Partial {
			break
		}
	}
	if err != nil || a.Partial || a.Holes != "" {
		t.Fatalf("after the reference failed: partial=%v holes=%q %v", a.Partial, a.Holes, err)
	}
}
