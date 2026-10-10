package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAI answers every numbered line with "TR <line>" and records the batch sizes.
type fakeAI struct {
	mu    sync.Mutex
	sizes []int
}

func (f *fakeAI) call(_ context.Context, _, _, user string) (string, aiTokens, error) {
	var out []string
	for _, line := range strings.Split(user, "\n") {
		if m := aiAnswerLine.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+": TR "+m[3])
		}
	}
	f.mu.Lock()
	f.sizes = append(f.sizes, len(out))
	f.mu.Unlock()
	return strings.Join(out, "\n"), aiTokens{In: 100, Out: 100}, nil
}

func waitIdle(t *testing.T, j *aiJob) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		idle := j.running == 0
		j.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("translation did not finish")
}

func TestAIJobTranslatesAroundThePlayer(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeAI{}
	tr := newAITranslator(nil, dir)
	tr.call = fake.call
	var cues []liveCue
	for i := 0; i < 400; i++ { // a line every 5 s
		cues = append(cues, liveCue{start: float64(i * 5), end: float64(i*5 + 4), text: fmt.Sprintf("line %d", i)})
	}
	cues = append(cues, liveCue{start: 2001, end: 2002, text: "♪"})

	key := "src|tr"
	lang, _ := aiLanguage("tr")
	j := tr.job(key, lang, func() string { return "Show, S01E01" })
	j.mu.Lock()
	j.focus, j.ahead = 600, aiAhead
	j.mu.Unlock()
	j.feed(cues)
	tr.kick(j, "key")
	waitIdle(t, j)

	j.mu.Lock()
	_, early := j.done["line 0"]
	at := j.done["line 120"]
	_, late := j.done["line 398"]
	music := j.done["♪"]
	j.mu.Unlock()
	if early || late || at != "TR line 120" || music != "♪" {
		t.Fatalf("early %v late %v at %q music %q", early, late, at, music)
	}
	// Requests run in parallel: the small one at the position is among them, whichever ended first
	fake.mu.Lock()
	sizes := append([]int(nil), fake.sizes...)
	fake.mu.Unlock()
	small := false
	for _, n := range sizes {
		small = small || n == aiFirstBatch
	}
	if !small {
		t.Fatalf("request sizes %v", sizes)
	}

	// The lines are kept: a new translator reads them back
	tr2 := newAITranslator(nil, dir)
	tr2.call = func(context.Context, string, string, string) (string, aiTokens, error) {
		return "", aiTokens{}, errors.New("must not be asked")
	}
	if !tr2.has(key) {
		t.Fatal("translation not stored")
	}
	j2 := tr2.job(key, lang, func() string { return "" })
	if j2.done["line 120"] != "TR line 120" {
		t.Fatalf("stored lines %d", len(j2.done))
	}
}

func TestAIAnswerKeepsLineBreaks(t *testing.T) {
	if got := aiSourceText("Hello &amp; bye\n<i>there</i>"); got != "Hello & bye <br> <i>there</i>" {
		t.Fatalf("source %q", got)
	}
	got := parseAIAnswer("1: Merhaba & güle güle <br> <i>orada</i>\n2) İki\n9: out of range\nnoise", []string{"a", "b"})
	if got[1] != "Merhaba &amp; güle güle\n<i>orada</i>" || got[2] != "İki" || len(got) != 2 {
		t.Fatalf("parsed %v", got)
	}
}

func TestAIKeyErrorStopsTheJob(t *testing.T) {
	var calls atomic.Int32
	tr := newAITranslator(nil, t.TempDir())
	tr.call = func(context.Context, string, string, string) (string, aiTokens, error) {
		calls.Add(1)
		return "", aiTokens{}, AIKeyError{"bad key"}
	}
	lang, _ := aiLanguage("tr")
	j := tr.job("k|tr", lang, func() string { return "" })
	j.mu.Lock()
	j.ahead = aiAhead
	j.mu.Unlock()
	j.feed([]liveCue{{0, 1, "a"}, {2, 3, "b"}})
	tr.kick(j, "key")
	waitIdle(t, j)
	tr.kick(j, "key")
	waitIdle(t, j)
	if calls.Load() != 1 {
		t.Fatalf("asked %d times with a bad key", calls.Load())
	}
}

func TestAddAIListsTranslationWhenLanguageMissing(t *testing.T) {
	s := NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	enHI := Sub{ID: encodeID(SourceSubDL, "en", "1"), Source: SourceSubDL, Language: "en", HI: true}
	en := Sub{ID: encodeID(SourceSubDL, "en", "2"), Source: SourceSubDL, Language: "en", Release: "Show.S01E01"}
	de := Sub{ID: encodeID(SourceSubDL, "de", "3"), Source: SourceSubDL, Language: "de"}
	trForced := Sub{ID: encodeID(SourceSubDL, "tr", "4"), Source: SourceSubDL, Language: "tr", Forced: true}
	q := Query{MediaType: "tv", TmdbID: 7, Season: 1, Episode: 1, Langs: []string{"tr", "en", "de"}}

	subs := s.addAI([]Sub{trForced, enHI, en, de}, q, []string{"tr", "de"})
	if len(subs) != 5 || subs[1].Source != SourceAI || subs[1].Language != "tr" || subs[1].AIFrom != "en" {
		t.Fatalf("list %+v", subs)
	}
	_, lang, ref, err := decodeID(subs[1].ID)
	if err != nil || lang != "tr" || ref != "tv:7:1:1|"+en.ID {
		t.Fatalf("ai id %q %q %v", lang, ref, err)
	}
	if _, err := s.FileFor(context.Background(), subs[1].ID, 0); !errors.Is(err, ErrAIOff) {
		t.Fatalf("served while off: %v", err)
	}
}

func TestAISettings(t *testing.T) {
	s := defaultSettings()
	var in SettingsInput
	body := `{"ai_translate":{"enabled":true,"api_key":"SECRET","langs":["profile","DE","xx","de"]}}`
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	in.apply(&s)
	v := s.view()
	if !s.AITranslateEnabled || s.AnthropicAPIKey != "SECRET" || s.AITranslateLangs != "profile,de" ||
		!v.AI.APIKeySet || strings.Join(v.AI.Langs, ",") != "profile,de" {
		t.Fatalf("settings %+v", s)
	}
	svc := NewService(nil, Config{CacheDir: t.TempDir()})
	targets := svc.aiTargets(s, Query{UILang: "tr"})
	if strings.Join(targets, ",") != "tr,de" {
		t.Fatalf("targets %v", targets)
	}
	if got := aiSearchLangs([]string{"tr"}, targets); strings.Join(got, ",") != "tr,de,en" {
		t.Fatalf("search langs %v", got)
	}
}

func TestServeAITranslatesTheSource(t *testing.T) {
	s := NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	fake := &fakeAI{}
	s.ai.call = fake.call
	on, key := true, "KEY"
	var in SettingsInput
	in.AI = &struct {
		Enabled *bool     `json:"enabled"`
		APIKey  optString `json:"api_key"`
		Langs   *[]string `json:"langs"`
	}{Enabled: &on, APIKey: optString{Set: true, Value: key}}
	if _, err := s.UpdateSettings(in); err != nil {
		t.Fatal(err)
	}

	// An embedded track already on disk is the source
	hash := strings.Repeat("ab", 20)
	var cues []liveCue
	for i := 0; i < 30; i++ {
		cues = append(cues, liveCue{start: float64(i * 4), end: float64(i*4 + 3), text: fmt.Sprintf("Line %d", i)})
	}
	ref := hash + "|1|2"
	if err := s.files.write(SourceEmbedded, SourceEmbedded+"|"+ref, ".vtt", renderVTT(cues, false)); err != nil {
		t.Fatal(err)
	}
	id := aiID("tr", aiMeta{"movie", 5, 0, 0}, encodeID(SourceEmbedded, "en", ref))

	a, err := s.FileFor(context.Background(), id, 0)
	if err != nil || !strings.Contains(string(a.VTT), "TR Line 0") {
		t.Fatalf("first answer %v:\n%s", err, a.VTT)
	}
	j := s.ai.jobs[encodeID(SourceEmbedded, "en", ref)+"|tr"]
	waitIdle(t, j)
	a, err = s.FileFor(context.Background(), id, 0)
	body := string(a.VTT)
	if err != nil || a.Partial || strings.Contains(body, AINote) || strings.Count(body, "TR Line") != 30 {
		t.Fatalf("final answer partial=%v %v:\n%s", a.Partial, err, body)
	}
}

func TestStripHI(t *testing.T) {
	cases := map[string]string{
		"[door opens]":                                "",
		"<i>(sighs)</i>":                              "",
		"- JOHN: Hi there.\n- [laughs]":               "- Hi there.",
		"<i>MAN ON TV: Breaking news</i>":             "<i>Breaking news</i>",
		"I said no (quietly).":                        "I said no.",
		"♪ ♪":                                         "",
		"♪ On the horizon ♪":                          "♪ On the horizon ♪",
		"Okay. I: am fine":                            "Okay. I: am fine",
		"* dramatic music continues *":                "",
		"* dramatic music\nintensifies, then fades *": "",
		"-* soft music playing\n- Hello!":             "- Hello!",
		"♪ I just wanna be free ♪":                    "♪ I just wanna be free ♪",
	}
	for in, want := range cases {
		if got := stripHI(in); got != want {
			t.Errorf("stripHI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickAISourceAvoidsOpenSubtitles(t *testing.T) {
	s := NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	os := Sub{ID: encodeID(SourceOpenSubtitles, "en", "9"), Source: SourceOpenSubtitles, Language: "en"}
	sdh := Sub{ID: encodeID(SourceEmbedded, "en", strings.Repeat("ab", 20)+"|1|2"), Source: SourceEmbedded, Language: "en", HI: true}
	if got := s.pickAISource([]Sub{os, sdh}, "tr"); got == nil || got.ID != sdh.ID {
		t.Fatalf("picked %+v", got)
	}
	subs := s.addAI([]Sub{os, sdh}, Query{MediaType: "movie", TmdbID: 1, Langs: []string{"tr", "en"}}, []string{"tr"})
	if subs[0].Source != SourceAI || subs[0].HI {
		t.Fatalf("ai item %+v", subs[0])
	}
}

func TestAIAnswerRangesAreSplit(t *testing.T) {
	batch := []string{"Why is no one else\nin the Brigade", "taking today seriously?!", "Well..."}
	got := parseAIAnswer("1-2: Neden Tugay'da kimse <br> bugünü ciddiye almıyor?!\n3: Şey...", batch)
	if got[1] != "Neden Tugay'da kimse" || got[2] != "bugünü ciddiye almıyor?!" || got[3] != "Şey..." {
		t.Fatalf("parsed %v", got)
	}
	got = parseAIAnswer("1-3: Yani... Bugün...", []string{"Well...", "I mean...", "Today is..."})
	if got[1] != "Yani..." || got[2] != "Bugün..." || got[3] != "" {
		t.Fatalf("parsed %v", got)
	}
	// Fewer words than lines: the last line shows nothing
	if parts := splitAcross("Evet", []string{"Yes", "sir"}); parts[0] != "Evet" || parts[1] != "" {
		t.Fatalf("parts %q", parts)
	}
}

func TestAIAsksOnlyForSkippedLines(t *testing.T) {
	var mu sync.Mutex
	var asked []int
	tr := newAITranslator(nil, t.TempDir())
	tr.call = func(_ context.Context, _, _, user string) (string, aiTokens, error) {
		var out []string
		n := 0
		for _, line := range strings.Split(user, "\n") {
			if m := aiAnswerLine.FindStringSubmatch(line); m != nil {
				n++
				if m[3] != "line 3" || len(asked) > 0 { // the first answer skips line 3
					out = append(out, m[1]+": TR "+m[3])
				}
			}
		}
		mu.Lock()
		asked = append(asked, n)
		mu.Unlock()
		return strings.Join(out, "\n"), aiTokens{}, nil
	}
	lang, _ := aiLanguage("tr")
	j := tr.job("k|tr", lang, func() string { return "" })
	var cues []liveCue
	for i := 0; i < 12; i++ {
		cues = append(cues, liveCue{start: float64(i), end: float64(i) + 0.5, text: fmt.Sprintf("line %d", i)})
	}
	j.feed(cues)
	batch := []string{"line 0", "line 1", "line 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "line 9"}
	tr.translate(j, "key", batch)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(asked) != "[10 1]" || j.done["line 3"] != "TR line 3" || j.done["line 9"] != "TR line 9" {
		t.Fatalf("asked %v, line 3 %q", asked, j.done["line 3"])
	}
}

func TestAISkipsASourceThatDoesNotFit(t *testing.T) {
	s := NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	hash := strings.Repeat("cd", 20)
	bad := encodeID(SourceSubDL, "en", "1")
	good := encodeID(SourceOpenSubtitles, "en", "2")
	key, _ := syncKey(bad, hash, 2)
	s.sync.store(key, syncMeta{State: syncStatePoor, Live: true, LiveWindows: 7}, nil)
	list := s.sync.listed([]Sub{
		{ID: bad, Source: SourceSubDL, Language: "en"},
		{ID: good, Source: SourceOpenSubtitles, Language: "en"},
	}, map[string]bool{}, hash, 2)

	if got := s.pickAISource(list, "tr"); got == nil || rawSubID(got.ID) != good {
		t.Fatalf("picked %+v", got)
	}
	if alt := s.aiAlternative(list[0].ID, hash, 2, "tr"); rawSubID(alt) != good {
		t.Fatalf("alternative %q", alt)
	}
}
