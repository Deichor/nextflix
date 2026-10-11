package subtitles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests on a snapshot of a real server's data (NFX_TESTDATA: its data dir), skipped without one.

// snapshotTrack is the embedded English SDH track of an episode in the snapshot (found by a
// line of it) and the stored translation of the old protocol, if any.
func snapshotTrack(t *testing.T, line string) ([]byte, map[string]string) {
	t.Helper()
	root := os.Getenv("NFX_TESTDATA")
	if root == "" {
		t.Skip("NFX_TESTDATA not set")
	}
	files, _ := filepath.Glob(filepath.Join(root, "subtitles", "embedded", "*.vtt"))
	var vtt []byte
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), line) {
			vtt = b
			break
		}
	}
	if vtt == nil {
		t.Skip("track not in the snapshot")
	}
	cues := aiCues(vtt)
	var old map[string]string
	maps, _ := filepath.Glob(filepath.Join(root, "ai-subtitles", aiOldStoreKind, "*.json"))
	for _, m := range maps {
		b, _ := os.ReadFile(m)
		var f aiFile
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		if _, ok := f.Texts[cues[len(cues)/2].text]; ok {
			old = f.Texts
		}
	}
	return vtt, old
}

const avatarLine = "Lee Huang the Third"

// The real track through the service, the model misbehaving in every known way, the player starting
// in the middle: every answer only ever shows a line's own translation in the source's timing, and
// the answer turns final only once every line of the file is translated.
func TestAISnapshotEpisodeEndToEnd(t *testing.T) {
	vtt, _ := snapshotTrack(t, avatarLine)
	fastAI(t)
	chaos := newChaos()
	chaos.delay = 60 * time.Millisecond // the player sees partial answers
	s := aiTestService(t, chaos.call(t))
	src := embeddedSource(t, s, vtt)
	id := aiID("tr", aiMeta{"tv", 284833, 1, 1}, src)
	cues := aiCues(vtt)
	at := map[float64]string{}
	for _, c := range cues {
		at[c.start] = c.text
	}

	pos, polls := 837.6, 0
	var last FileAnswer
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); polls++ {
		a, err := s.FileFor(context.Background(), id, pos)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range vttCues(a.VTT) {
			text, ok := at[c.start]
			if !ok {
				t.Fatalf("cue at %.3f is not a source cue", c.start)
			}
			if c.text != wantTr(text) && hasLetter(text) {
				t.Fatalf("poll %d: %q shows %q", polls, text, c.text)
			}
		}
		if polls == 0 {
			near := false
			for _, c := range vttCues(a.VTT) {
				near = near || (c.start >= pos-aiLead && c.start < pos+60)
			}
			if !a.Partial || !near {
				t.Fatalf("first answer partial=%v, lines at the position %v", a.Partial, near)
			}
		}
		last = a
		if !a.Partial {
			break
		}
		if polls == 3 {
			pos = 13 // the viewer goes back to the start
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last.Partial {
		t.Fatalf("still partial after %d polls", polls)
	}
	out := vttCues(last.VTT)
	if len(out) != len(cues) {
		t.Fatalf("%d cues served of %d source cues", len(out), len(cues))
	}
	for i, c := range out {
		if c.start != cues[i].start || c.end != cues[i].end || c.text != wantTr(cues[i].text) {
			t.Fatalf("cue %d: %v-%v %q, source %v-%v %q", i, c.start, c.end, c.text, cues[i].start, cues[i].end, cues[i].text)
		}
	}
	t.Logf("%d source cues (%d before the HI strip), final after %d polls; requests by mode %v",
		len(cues), len(vttCues(vtt)), polls, chaos.modes)
}

// TestAIRealClaude translates the snapshot episode with the real model (NFX_ANTHROPIC_KEY), reports
// the cost and how many lines of the old protocol's translation belong to another line.
func TestAIRealClaude(t *testing.T) {
	key := os.Getenv("NFX_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("NFX_ANTHROPIC_KEY not set")
	}
	vtt, old := snapshotTrack(t, avatarLine)
	cues := aiCues(vtt)
	var mu sync.Mutex
	var used aiTokens
	requests := 0
	tr := newAITranslator(nil, t.TempDir())
	tr.call = func(ctx context.Context, k, system, user string) (string, aiTokens, error) {
		answer, u, err := claudeCall(ctx, k, system, user)
		mu.Lock()
		used.In, used.Out = used.In+u.In, used.Out+u.Out
		requests++
		mu.Unlock()
		return answer, u, err
	}
	lang, _ := aiLanguage("tr")
	j := tr.job("real|tr", lang, func() string {
		return "Title: Avatar: Seven Havens, S01E01\nGenre: Animation, Action & Adventure, Sci-Fi & Fantasy\n"
	})
	j.mu.Lock()
	j.watched = true
	j.mu.Unlock()
	j.feed(cues)
	start := time.Now()
	tr.kick(j, key)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		j.mu.Lock()
		left := 0
		for _, l := range j.lines {
			if !j.settledLocked(l.text) {
				left++
			}
		}
		running := j.running
		j.mu.Unlock()
		if left == 0 && running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d lines left", left)
		}
		time.Sleep(200 * time.Millisecond)
	}
	cost := (float64(used.In)*aiInputPrice + float64(used.Out)*aiOutputPrice) / 1e6
	j.mu.Lock()
	defer j.mu.Unlock()
	retried := 0
	for _, n := range j.tries {
		if n > 0 {
			retried++
		}
	}
	t.Logf("%d lines in %d requests, %s; %d in / %d out tokens = $%.4f; %d lines asked again, %d given up",
		len(j.lines), requests, time.Since(start).Round(time.Second), used.In, used.Out, cost, retried, len(j.gaveUp))
	for i := 120; i < 135 && i < len(j.lines); i++ {
		t.Logf("  %q -> %q", j.lines[i].text, j.done[j.lines[i].text])
	}
	if len(j.gaveUp) > 3 {
		t.Errorf("%d lines given up", len(j.gaveUp))
	}

	// The old protocol's lines that match the new translation of another line better than their own
	if old == nil {
		return
	}
	moved, compared := 0, 0
	for i, l := range j.lines {
		o, ok := old[l.text]
		if !ok || !hasLetter(o) {
			continue
		}
		compared++
		own := overlapScore(o, j.done[l.text])
		best, bestK := own, 0
		for k := -3; k <= 3; k++ {
			if k == 0 || i+k < 0 || i+k >= len(j.lines) {
				continue
			}
			if s := overlapScore(o, j.done[j.lines[i+k].text]); s > best {
				best, bestK = s, k
			}
		}
		if bestK != 0 && best >= 0.5 && best > own+0.3 {
			moved++
			if moved <= 25 {
				t.Logf("  old %q -> %q matches line %+d: %q", l.text, o, bestK, j.lines[i+bestK].text)
			}
		}
	}
	t.Logf("old protocol: %d of %d stored lines match another line's translation", moved, compared)
}

var overlapWord = regexp.MustCompile(`\p{L}+`)

// overlapScore is the share of the words of a (by 4-letter stems) found in b.
func overlapScore(a, b string) float64 {
	stem := func(w string) string {
		r := []rune(strings.ToLower(w))
		return string(r[:min(len(r), 4)])
	}
	in := map[string]bool{}
	for _, w := range overlapWord.FindAllString(b, -1) {
		in[stem(w)] = true
	}
	n, hit := 0, 0
	for _, w := range overlapWord.FindAllString(a, -1) {
		if len([]rune(w)) < 3 {
			continue
		}
		n++
		if in[stem(w)] {
			hit++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}
