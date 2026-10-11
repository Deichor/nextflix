package subtitles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkCase asks four lines (ids 11..14) of a scene; lines around them are context.
func checkCase() (map[int]aiCheck, []aiInLine) {
	src := []string{"Geet, this way!", "Humanity is not ready\nfor the Avatar's return.", "-And yet...", "Sir, I don't understand."}
	asked := map[int]aiCheck{}
	var in []aiInLine
	for i, s := range src {
		var near []string
		for k := max(0, i-2); k <= min(len(src)-1, i+2); k++ {
			if k != i {
				near = append(near, src[k])
			}
		}
		asked[11+i] = aiCheck{src: s, near: near, pos: i}
		in = append(in, aiInLine{ID: 11 + i, Text: s})
	}
	return asked, in
}

func TestCheckAIAnswerMapsOnlyMatchingItems(t *testing.T) {
	asked, in := checkCase()
	names := aiNames([]string{"Geet, this way!", "Come here, Geet.", "Humanity is not ready\nfor the Avatar's return.", "-And yet...", "Sir, I don't understand.", "the Avatar is here"})
	good := func() []aiOutLine {
		var out []aiOutLine
		for _, l := range in {
			out = append(out, fakeItem(l))
		}
		return out
	}
	for _, c := range []struct {
		name      string
		answer    func() string
		untrusted bool
		ok        []int // ids accepted (always with their own line's translation)
	}{
		{"right", func() string { return fakeAnswer(good()) }, false, []int{11, 12, 13, 14}},
		{"out of order", func() string { g := good(); slices.Reverse(g); return fakeAnswer(g) }, false, []int{11, 12, 13, 14}},
		{"commentary around the JSON", func() string { return "Sure:\n" + fakeAnswer(good()) + "\nDone." }, false, []int{11, 12, 13, 14}},
		{"not JSON", func() string { return "1: Geet, bu yol!\n2: ..." }, true, nil},
		{"cut off", func() string { s := fakeAnswer(good()); return s[:len(s)/2] }, true, nil},
		{"skips a line: its neighbours are asked again", func() string { g := good(); return fakeAnswer(append(g[:1], g[2:]...)) }, false, []int{14}},
		{"merges two lines", func() string {
			g := good()
			g[1].Text += " " + g[2].Text
			g[2].Text = ""
			return fakeAnswer(g)
		}, false, []int{11}},
		{"shifted by the context (old protocol)", func() string {
			// The model answered the line before the batch too and numbered on: every item one line late
			all := append([]aiInLine{{Text: "Hello there, friend."}}, in...)
			var out []aiOutLine
			for i, l := range in {
				out = append(out, aiOutLine{ID: l.ID, Src: fakeEcho(all[i].Text), Text: fakeTr(all[i].Text)})
			}
			return fakeAnswer(out)
		}, true, nil},
		{"one item echoes its neighbour", func() string { g := good(); g[3].Src = fakeEcho(in[2].Text); return fakeAnswer(g) }, false, []int{11, 12}},
		{"answers twice", func() string { g := good(); return fakeAnswer(append(g, g[0])) }, true, nil},
		{"answers a context line", func() string {
			return fakeAnswer(append(good(), aiOutLine{ID: 0, Src: "Hello there", Text: "Merhaba"}))
		}, false, []int{11, 12, 13, 14}},
		{"invents an id for a context line", func() string {
			return fakeAnswer(append(good(), aiOutLine{ID: 15, Src: "Hello there", Text: "Merhaba"}))
		}, false, []int{11, 12, 13, 14}},
		{"two context lines answered first, the ids numbered on (old protocol, shift by two)", func() string {
			all := []string{"Hello there, friend.", "Who are you?"}
			for _, l := range in {
				all = append(all, l.Text)
			}
			var out []aiOutLine
			for i, l := range in {
				out = append(out, aiOutLine{ID: l.ID, Src: fakeEcho(all[i]), Text: fakeTr(all[i])})
			}
			return fakeAnswer(out)
		}, true, nil},
		{"foreign script", func() string { g := good(); g[0].Text = "这边"; return fakeAnswer(g) }, false, []int{12, 13, 14}},
		{"names of the line before", func() string { g := good(); g[2].Text = "Geet, bu yol!"; return fakeAnswer(g) }, false, []int{11, 12, 14}},
		{"untranslated", func() string { g := good(); g[1].Text = in[1].Text; return fakeAnswer(g) }, false, []int{11, 13, 14}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := asked
			v := checkAIAnswer(c.answer(), a, "tr", names)
			if (v.untrusted != "") != c.untrusted {
				t.Fatalf("untrusted %q", v.untrusted)
			}
			var ok []int
			for id, tr := range v.ok {
				ok = append(ok, id)
				if tr != wantTr(a[id].src) {
					t.Fatalf("line %d %q got %q", id, a[id].src, tr)
				}
			}
			slices.Sort(ok)
			if fmt.Sprint(ok) != fmt.Sprint(c.ok) {
				t.Fatalf("accepted %v, want %v (why %v)", ok, c.ok, v.why)
			}
		})
	}
}

func TestAISoftFaults(t *testing.T) {
	names := map[string]bool{"Geet": true}
	for _, c := range []struct {
		src, tr, near string
		fault         bool
	}{
		{"-Shh!\n-...she saved us", "ve Eski Dünya harabelerinde\nkaybolduk.", "", true}, // a two-speaker line answered with one
		{"-Shh!\n-...she saved us", "-Şşt!\n-...bizi kurtardı", "", false},
		{"Yes.", "Evet, ve sonra da hep birlikte eve gidip yemek yedik.", "", true}, // much longer: another line's text
		{"-And yet...", "Geet, bu yol!", "Geet, this way!", true},
		{"Geet, this way!", "Geet, bu taraftan!", "Hey!", false},
		{"Jae!", "Jae!", "", false}, // a name stays as it is
		{"I don't understand that.", "I don't understand that.", "", true},
	} {
		if got := aiSoftFault(c.tr, aiCheck{src: c.src, near: []string{c.near}}, names) != ""; got != c.fault {
			t.Errorf("%q -> %q: fault %v", c.src, c.tr, got)
		}
	}
}

func TestAIEchoMatches(t *testing.T) {
	for _, c := range []struct {
		echo, src string
		ok        bool
	}{
		{"-Shh! ...she saved", "-Shh!\n-...she saved us", true},
		{"Sir, I", "Sir, I don't understand.", true},
		{"Sir", "Sir, I don't understand.", true},
		{"I don't understand", "Sir, I don't understand.", false},
		{"", "Hello", false},
		{"", "♪", true},
		{"Hello &amp; bye", "Hello & bye now", true},
	} {
		if got := aiEchoMatches(c.echo, c.src); got != c.ok {
			t.Errorf("echo %q of %q: %v", c.echo, c.src, got)
		}
	}
}

func TestAIRequestHasIdsAndContext(t *testing.T) {
	lang, _ := aiLanguage("tr")
	tr := newAITranslator(nil, t.TempDir())
	j := tr.job("k|tr", lang, func() string { return "Title: Show\n" })
	j.feed(numberedCues(10, 3))
	j.mu.Lock()
	j.done["Line 3 is here"] = "çev Line 3 is here"
	lines, checks := j.requestLocked([]aiLine{j.lines[5], j.lines[6]})
	j.mu.Unlock()
	user := aiUser("Title: Show\n", lines)
	got := aiRequestLines(t, user)
	if len(got) != 6 || got[0].ID != 0 || got[0].Context != "Line 3 is here" || got[0].Translated != "çev Line 3 is here" ||
		got[2].ID == 0 || got[2].Text != "Line 5 is here" || got[3].ID == 0 || got[5].ID != 0 || got[5].Text != "" {
		t.Fatalf("request %+v", got)
	}
	if len(checks) != 2 || !strings.HasPrefix(user, "Title: Show\n") {
		t.Fatalf("checks %v", checks)
	}
}

// chaosAI misbehaves in every way it knows, one way per request in turn, while each asked line
// was asked fewer than 3 times (then it answers right, so every line ends translated).
type chaosAI struct {
	mu     sync.Mutex
	n      int
	asked  map[string]int
	modes  map[string]int
	refuse string        // a line that is always refused
	delay  time.Duration // per request
}

var chaosModes = []string{"right", "skip", "merge", "context shift", "reverse", "commentary", "truncated",
	"network error", "refusal", "duplicate", "unknown id", "wrong echo", "context answered", "foreign script", "not JSON", "right"}

func (c *chaosAI) call(t testing.TB) aiCallFunc {
	return func(_ context.Context, _, _, user string) (string, aiTokens, error) {
		all := aiRequestLines(t, user)
		ask := asked(all)
		tok := aiTokens{In: 50, Out: 50}
		c.mu.Lock()
		fresh, refuse := true, false
		for _, l := range ask {
			c.asked[l.Text]++
			fresh = fresh && c.asked[l.Text] < 3
			refuse = refuse || l.Text == c.refuse
		}
		if refuse {
			c.mu.Unlock()
			return "", tok, errAIRefused
		}
		mode := "right"
		if fresh {
			mode = chaosModes[c.n%len(chaosModes)]
			c.n++
		}
		c.modes[mode]++
		pick := c.n % len(ask)
		c.mu.Unlock()
		time.Sleep(c.delay)

		var items []aiOutLine
		for _, l := range ask {
			items = append(items, fakeItem(l))
		}
		switch mode {
		case "skip":
			items = append(items[:pick], items[pick+1:]...)
		case "merge":
			if pick+1 < len(items) {
				items[pick].Text += " " + items[pick+1].Text
				items[pick+1].Text = ""
			}
		case "context shift":
			var seq []aiOutLine
			for i, l := range all {
				if i < len(ask) {
					seq = append(seq, aiOutLine{ID: ask[i].ID, Src: fakeEcho(l.Text + l.Context), Text: fakeTr(l.Text + l.Context)})
				}
			}
			items = seq
		case "reverse":
			slices.Reverse(items)
		case "commentary":
			return "Here are the translations:\n" + fakeAnswer(items) + "\nDone.", tok, nil
		case "truncated":
			s := fakeAnswer(items)
			return s[:len(s)/2], tok, errAITruncated
		case "network error":
			return "", aiTokens{}, errors.New("connection reset by peer")
		case "refusal":
			return "", tok, errAIRefused
		case "duplicate":
			items = append(items, items[pick])
		case "unknown id":
			items = append(items, aiOutLine{ID: 99999, Src: "x", Text: "y"})
		case "wrong echo":
			items[pick].Src = fakeEcho(ask[(pick+1)%len(ask)].Text)
		case "context answered":
			for _, l := range all {
				if l.ID == 0 {
					items = append(items, aiOutLine{ID: 100000 + len(items), Src: fakeEcho(l.Context), Text: fakeTr(l.Context)})
				}
			}
		case "foreign script":
			items[pick].Text = "翻译"
		case "not JSON":
			return "1: " + fakeTr(ask[0].Text), tok, nil
		}
		return fakeAnswer(items), tok, nil
	}
}

func newChaos() *chaosAI { return &chaosAI{asked: map[string]int{}, modes: map[string]int{}} }

// checkJob asserts that every settled line shows its own translation (or, given up, itself).
func checkJob(t *testing.T, j *aiJob, cues []liveCue, refused string) {
	t.Helper()
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cues {
		if !hasLetter(c.text) {
			continue
		}
		if c.text == refused {
			if !j.gaveUp[c.text] {
				t.Fatalf("refused line %q not given up", c.text)
			}
			continue
		}
		if got := j.done[c.text]; got != wantTr(c.text) {
			t.Fatalf("%q -> %q (gave up %v)", c.text, got, j.gaveUp[c.text])
		}
	}
	out, complete, translating := j.renderLocked(cues)
	if !complete || translating || len(out) != len(cues) {
		t.Fatalf("complete %v translating %v: %d of %d cues", complete, translating, len(out), len(cues))
	}
	for i, c := range out {
		if c.start != cues[i].start || c.end != cues[i].end {
			t.Fatalf("cue %d timing %v-%v, source %v-%v", i, c.start, c.end, cues[i].start, cues[i].end)
		}
		want := wantTr(cues[i].text)
		if cues[i].text == refused {
			want = cues[i].text // shown as it is
		}
		if c.text != want {
			t.Fatalf("cue %d %q shows %q", i, cues[i].text, c.text)
		}
	}
}

func TestAIJobSurvivesAMisbehavingModel(t *testing.T) {
	fastAI(t)
	chaos := newChaos()
	chaos.refuse = "Line 77 is here"
	tr := newAITranslator(nil, t.TempDir())
	tr.call = chaos.call(t)
	lang, _ := aiLanguage("tr")
	j := tr.job("chaos|tr", lang, func() string { return "" })
	cues := numberedCues(1000, 4)
	j.mu.Lock()
	j.focus, j.watched = 500, true
	j.mu.Unlock()
	j.feed(cues)
	tr.kick(j, "key")
	waitSettled(t, j)
	checkJob(t, j, cues, chaos.refuse)
	for _, m := range chaosModes {
		if chaos.modes[m] == 0 {
			t.Errorf("mode %q never used", m)
		}
	}
	// What is stored is only checked lines (never the refused one)
	j2 := newAITranslator(nil, tr.store.dir).job("chaos|tr", lang, func() string { return "" })
	if _, ok := j2.done[chaos.refuse]; ok || len(j2.done) != 999 {
		t.Fatalf("stored %d lines", len(j2.done))
	}
	t.Logf("requests by mode: %v", chaos.modes)
}

// A line whose answer only looks off (here: left in English) is asked once more; the model giving
// such an answer again is taken at its word rather than leaving the line out.
func TestAIAcceptsADoubtfulAnswerGivenTwice(t *testing.T) {
	fastAI(t)
	stubborn := "Ellora Ellora Ellora"
	fake := &fakeAI{}
	good := fake.call(t)
	var copies int
	var mu sync.Mutex
	tr := newAITranslator(nil, t.TempDir())
	tr.call = func(ctx context.Context, key, system, user string) (string, aiTokens, error) {
		lines := asked(aiRequestLines(t, user))
		for _, l := range lines {
			if l.Text == stubborn {
				mu.Lock()
				copies++
				mu.Unlock()
				return fakeAnswer([]aiOutLine{{ID: l.ID, Src: stubborn, Text: stubborn}}), aiTokens{}, nil
			}
		}
		return good(ctx, key, system, user)
	}
	lang, _ := aiLanguage("tr")
	j := tr.job("doubt|tr", lang, func() string { return "" })
	j.feed([]liveCue{{0, 1, stubborn}})
	tr.kick(j, "key")
	waitSettled(t, j)
	j.mu.Lock()
	defer j.mu.Unlock()
	if copies != 2 || j.done[stubborn] != stubborn || j.gaveUp[stubborn] {
		t.Fatalf("asked %d times, done %q", copies, j.done[stubborn])
	}
}

func TestAIJobFollowsAGrowingSource(t *testing.T) {
	fastAI(t)
	fake := &fakeAI{}
	tr := newAITranslator(nil, t.TempDir())
	tr.call = fake.call(t)
	lang, _ := aiLanguage("tr")
	j := tr.job("grow|tr", lang, func() string { return "" })
	all := numberedCues(40, 3)
	j.mu.Lock()
	j.watched = true
	j.mu.Unlock()
	j.feed(all[:25])
	tr.kick(j, "key")
	waitSettled(t, j)
	j.mu.Lock()
	_, complete, _ := j.renderLocked(all) // the player's answer already has more lines than were fed
	j.mu.Unlock()
	if complete {
		t.Fatal("complete before the new lines were translated")
	}
	j.feed(all)
	tr.kick(j, "key")
	waitSettled(t, j)
	checkJob(t, j, all, "")
}

func TestAIJobWaitsOutNetworkErrors(t *testing.T) {
	fastAI(t)
	var mu sync.Mutex
	fails := 6
	fake := &fakeAI{}
	good := fake.call(t)
	tr := newAITranslator(nil, t.TempDir())
	tr.call = func(ctx context.Context, key, system, user string) (string, aiTokens, error) {
		mu.Lock()
		f := fails > 0
		fails--
		mu.Unlock()
		if f {
			return "", aiTokens{}, errors.New("503 overloaded")
		}
		return good(ctx, key, system, user)
	}
	lang, _ := aiLanguage("tr")
	j := tr.job("net|tr", lang, func() string { return "" })
	cues := numberedCues(30, 2)
	j.feed(cues)
	tr.kick(j, "key") // no player polls: the job alone retries
	waitSettled(t, j)
	checkJob(t, j, cues, "")
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cues {
		if j.tries[c.text] != 0 {
			t.Fatalf("a network error counted as a failed answer for %q", c.text)
		}
	}
}

func TestAIPrefetchTranslatesOnlyTheOpening(t *testing.T) {
	fastAI(t)
	fake := &fakeAI{}
	tr := newAITranslator(nil, t.TempDir())
	tr.call = fake.call(t)
	lang, _ := aiLanguage("tr")
	j := tr.job("next|tr", lang, func() string { return "" })
	cues := numberedCues(200, 5)
	j.mu.Lock()
	j.opening = aiPrefetchAhead
	j.mu.Unlock()
	j.feed(cues)
	tr.kick(j, "key")
	waitSettled(t, j)
	j.mu.Lock()
	n := len(j.done)
	j.watched, j.opening = true, 0 // the player opens it: the rest follows
	j.mu.Unlock()
	if n != int(aiPrefetchAhead/5)+1 {
		t.Fatalf("prefetched %d lines", n)
	}
	tr.kick(j, "key")
	waitSettled(t, j)
	checkJob(t, j, cues, "")
}
