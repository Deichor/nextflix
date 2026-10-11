package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastAI shortens the waits of the AI translation for a test.
func fastAI(t *testing.T) {
	t.Helper()
	retry, pause, pauseMax, waitMax, first := aiRetryWait, aiPauseBase, aiPauseMax, aiWaitMax, aiFirstWait
	aiRetryWait = []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond}
	aiPauseBase, aiPauseMax, aiWaitMax, aiFirstWait = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, 2*time.Second
	t.Cleanup(func() {
		aiRetryWait, aiPauseBase, aiPauseMax, aiWaitMax, aiFirstWait = retry, pause, pauseMax, waitMax, first
	})
}

// aiRequestLines reads a request the way the model does.
func aiRequestLines(t testing.TB, user string) []aiInLine {
	t.Helper()
	i := strings.Index(user, `{"lines"`)
	if i < 0 {
		t.Fatalf("no lines in %q", user)
	}
	var in struct {
		Lines []aiInLine `json:"lines"`
	}
	if err := json.Unmarshal([]byte(user[i:]), &in); err != nil {
		t.Fatal(err)
	}
	return in.Lines
}

var fakeLead = regexp.MustCompile(`^((?:<[ibu]>)*-?\s*)`)

// fakeTr is the fake model's translation: every line keeps its tags and speaker dash, so a
// translation can be told apart from the one of any other line.
func fakeTr(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		lead := fakeLead.FindString(l)
		lines[i] = lead + "çev " + l[len(lead):]
	}
	return strings.Join(lines, "\n")
}

// wantTr is what the job stores for a source cue text answered by fakeTr.
func wantTr(text string) string { return cleanCueText(fakeTr(aiPlain(text))) }

func fakeEcho(text string) string {
	w := strings.Fields(text)
	return strings.Join(w[:min(len(w), aiEchoWords)], " ")
}

func fakeItem(l aiInLine) aiOutLine {
	return aiOutLine{ID: l.ID, Src: fakeEcho(l.Text), Text: fakeTr(l.Text)}
}

func fakeAnswer(items []aiOutLine) string {
	b, _ := json.Marshal(map[string]any{"lines": items})
	return string(b)
}

func asked(lines []aiInLine) []aiInLine {
	var out []aiInLine
	for _, l := range lines {
		if l.ID != 0 {
			out = append(out, l)
		}
	}
	return out
}

// fakeAI answers every line right and records the requests.
type fakeAI struct {
	mu    sync.Mutex
	calls [][]aiInLine // the asked lines of each request
}

func (f *fakeAI) call(t testing.TB) aiCallFunc {
	return func(_ context.Context, _, _, user string) (string, aiTokens, error) {
		lines := asked(aiRequestLines(t, user))
		f.mu.Lock()
		f.calls = append(f.calls, lines)
		f.mu.Unlock()
		var items []aiOutLine
		for _, l := range lines {
			items = append(items, fakeItem(l))
		}
		return fakeAnswer(items), aiTokens{In: 100, Out: 100}, nil
	}
}

func (f *fakeAI) requests() [][]aiInLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]aiInLine(nil), f.calls...)
}

// waitSettled waits until every line of the job is translated or given up on.
func waitSettled(t *testing.T, j *aiJob) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		left := 0
		for _, l := range j.lines {
			if !j.settledLocked(l.text) && (j.opening == 0 || l.start <= j.opening) {
				left++
			}
		}
		idle := j.running == 0
		j.mu.Unlock()
		if left == 0 && idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	t.Fatalf("translation did not finish: %d of %d lines done, %d given up", len(j.done), len(j.lines), len(j.gaveUp))
}

func numberedCues(n int, every float64) []liveCue {
	var cues []liveCue
	for i := 0; i < n; i++ {
		cues = append(cues, liveCue{start: float64(i) * every, end: float64(i)*every + every - 1, text: fmt.Sprintf("Line %d is here", i)})
	}
	return cues
}

func TestAIJobTranslatesTheWholeFileFromThePlayerOn(t *testing.T) {
	fastAI(t)
	dir := t.TempDir()
	fake := &fakeAI{}
	tr := newAITranslator(nil, dir)
	tr.call = fake.call(t)
	cues := append(numberedCues(400, 5), liveCue{start: 2001, end: 2002, text: "♪"})

	key := "src|tr"
	lang, _ := aiLanguage("tr")
	j := tr.job(key, lang, func() string { return "Show, S01E01" })
	j.mu.Lock()
	j.focus, j.watched = 600, true
	j.mu.Unlock()
	j.feed(cues)
	tr.kick(j, "key")
	waitSettled(t, j)

	j.mu.Lock()
	for _, c := range cues[:400] {
		if j.done[c.text] != wantTr(c.text) {
			t.Fatalf("%q -> %q", c.text, j.done[c.text])
		}
	}
	out, complete, translating := j.renderLocked(cues)
	j.mu.Unlock()
	if !complete || translating || len(out) != len(cues) || out[400].text != "♪" {
		t.Fatalf("complete %v translating %v cues %d", complete, translating, len(out))
	}
	// The first request starts at the player (a small one), the lines before it come last
	reqs := fake.requests()
	small := false
	for _, r := range reqs {
		small = small || (r[0].Text == "Line 119 is here" && len(r) == aiFirstBatch)
	}
	if !small {
		t.Fatal("no small first request at the player's position")
	}
	for _, r := range reqs[:3] {
		for _, l := range r {
			var n int
			fmt.Sscanf(l.Text, "Line %d", &n)
			if n < 119 {
				t.Fatalf("a line behind the player was asked among the first requests: %q", l.Text)
			}
		}
	}

	// The lines are kept: a new translator reads them back without asking
	tr2 := newAITranslator(nil, dir)
	tr2.call = func(context.Context, string, string, string) (string, aiTokens, error) {
		return "", aiTokens{}, errors.New("must not be asked")
	}
	if !tr2.has(key) {
		t.Fatal("translation not stored")
	}
	j2 := tr2.job(key, lang, func() string { return "" })
	if j2.done["Line 120 is here"] != wantTr("Line 120 is here") || len(j2.done) != 400 {
		t.Fatalf("stored lines %d", len(j2.done))
	}
}

func TestAIOldStoreIsDropped(t *testing.T) {
	dir := t.TempDir()
	old := diskCache{dir: dir}
	if err := old.write(aiOldStoreKind, "src|tr", ".json", []byte(`{"lang":"tr","texts":{"- Not always.":"Efendim, anlamıyorum."}}`)); err != nil {
		t.Fatal(err)
	}
	// A file of the new kind without the version is not trusted either
	if err := old.write(aiStoreKind, "other|tr", ".json", []byte(`{"lang":"tr","texts":{"a":"b"}}`)); err != nil {
		t.Fatal(err)
	}
	tr := newAITranslator(nil, dir)
	if _, err := os.Stat(filepath.Join(dir, aiOldStoreKind)); !os.IsNotExist(err) {
		t.Fatalf("old store kept: %v", err)
	}
	lang, _ := aiLanguage("tr")
	if j := tr.job("other|tr", lang, func() string { return "" }); len(j.done) != 0 {
		t.Fatalf("unversioned lines trusted: %v", j.done)
	}
}

func TestAIKeyErrorStopsTheJob(t *testing.T) {
	fastAI(t)
	var calls atomic.Int32
	tr := newAITranslator(nil, t.TempDir())
	tr.call = func(context.Context, string, string, string) (string, aiTokens, error) {
		calls.Add(1)
		return "", aiTokens{}, AIKeyError{"bad key"}
	}
	lang, _ := aiLanguage("tr")
	j := tr.job("k|tr", lang, func() string { return "" })
	j.feed([]liveCue{{0, 1, "a"}, {2, 3, "b"}})
	tr.kick(j, "key")
	time.Sleep(50 * time.Millisecond)
	tr.kick(j, "key")
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("asked %d times with a bad key", calls.Load())
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.tries["a"] != 0 || j.gaveUp["a"] {
		t.Fatal("a key error counted as a failed line")
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

// aiTestService is a service with the AI translation on, answered by call.
func aiTestService(t *testing.T, call aiCallFunc) *Service {
	t.Helper()
	s := NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	s.ai.call = call
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
	return s
}

// embeddedSource stores vtt as an embedded track of a file and returns its subtitle id.
func embeddedSource(t *testing.T, s *Service, vtt []byte) string {
	t.Helper()
	ref := strings.Repeat("ab", 20) + "|1|2"
	if err := s.files.write(SourceEmbedded, SourceEmbedded+"|"+ref, ".vtt", vtt); err != nil {
		t.Fatal(err)
	}
	return encodeID(SourceEmbedded, "en", ref)
}

func TestServeAITranslatesTheSource(t *testing.T) {
	fastAI(t)
	fake := &fakeAI{}
	s := aiTestService(t, fake.call(t))
	cues := numberedCues(30, 4)
	src := embeddedSource(t, s, renderVTT(cues, false))
	id := aiID("tr", aiMeta{"movie", 5, 0, 0}, src)

	a, err := s.FileFor(context.Background(), id, 0)
	if err != nil || !strings.Contains(string(a.VTT), wantTr("Line 0 is here")) {
		t.Fatalf("first answer %v:\n%s", err, a.VTT)
	}
	waitSettled(t, s.ai.jobs[src+"|tr"])
	a, err = s.FileFor(context.Background(), id, 0)
	body := string(a.VTT)
	if err != nil || a.Partial || strings.Contains(body, AINote) || strings.Count(body, "çev Line") != 30 {
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

func TestPickAISourcePrefersTheFilesOwnTiming(t *testing.T) {
	s := NewService(nil, Config{CacheDir: t.TempDir(), AIDir: t.TempDir()})
	hash := strings.Repeat("ef", 20)
	timed := Sub{ID: wrapSyncID(encodeID(SourceSubDL, "en", "1"), hash, 1), Source: SourceSubDL, Language: "en", Synced: true}
	untimed := Sub{ID: wrapSyncID(encodeID(SourceSubDL, "en", "2"), hash, 1), Source: SourceSubDL, Language: "en"}
	live := Sub{ID: encodeID(SourceEmbedded, "en", hash+"|1|3"), Source: SourceEmbedded, Language: "en", Synced: true, Live: true, HI: true}
	track := Sub{ID: encodeID(SourceEmbedded, "en", hash+"|1|2"), Source: SourceEmbedded, Language: "en", Synced: true, HI: true}
	for _, c := range []struct {
		list []Sub
		want string
	}{
		{[]Sub{untimed, timed, live, track}, track.ID}, // the file's SDH track beats a regular provider subtitle
		{[]Sub{untimed, timed, live}, live.ID},
		{[]Sub{untimed, timed}, timed.ID},
		{[]Sub{untimed}, untimed.ID},
	} {
		if got := s.pickAISource(c.list, "tr"); got == nil || got.ID != c.want {
			t.Fatalf("picked %+v, want %s", got, c.want)
		}
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
