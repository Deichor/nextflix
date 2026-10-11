package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// The translation of one subtitle into one language. Once a player asks for it, every line of the
// source is translated in the background, the lines from the player's position on first, then the
// ones before it. A line counts only after its answer was checked (ai_prompt.go); a line whose
// answers keep failing is retried with backoff in ever smaller requests and finally shown as it is.

const (
	aiPrefetchAhead = 3 * 60.0 // the next episode: its opening only
	aiLead          = 5.0      // a line this far before the player counts as "at the position"
	aiFirstBatch    = 20       // lines of the first request at a position (quick start)
	aiBatch         = 60       // lines of later requests (the prompt is paid once per request)
	aiBatchChars    = 4000     // source characters per request at most (bounds the answer)
	aiContextLines  = 2        // source lines around the asked ones as context
	aiWorkers       = 3        // requests at a time per translation
	aiParallel      = 6        // requests at a time in total
	aiMaxTries      = 5        // answers per line before it is shown untranslated
	aiCallLimit     = 3 * time.Minute
	aiJobIdle       = 30 * time.Minute
	aiStoreKind     = "translations-v2"
	aiStoreVersion  = 2
	// aiOldStoreKind held the translations of the numbered-lines protocol, whose lines could be shifted
	aiOldStoreKind = "translations"
)

// Vars so tests can shorten them.
var (
	aiFirstWait     = 10 * time.Second
	aiKeyRetryAfter = 2 * time.Minute
	aiRetryWait     = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}
	aiPauseBase     = 10 * time.Second // after a failed request (network, overload); doubles up to aiPauseMax
	aiPauseMax      = 5 * time.Minute
	aiWaitMax       = 30 * time.Second // a worker waiting for retries looks again at least this often
)

type aiLine struct {
	id    int
	start float64
	text  string
}

type aiJob struct {
	key   string // raw source id | language
	lang  AILanguage
	title string

	mu       sync.Mutex
	lines    []aiLine // distinct texts with letters, by first start
	ids      map[string]int
	nextID   int
	names    map[string]bool
	done     map[string]string // source text -> checked translation (stored)
	given    map[string]string // source text -> its translation from elsewhere: only context, never asked (holes)
	soft     map[string]string // source text -> last answer that failed only a plausibility check
	doubted  map[string]bool   // an answer of the line itself failed a plausibility check before
	gaveUp   map[string]bool   // shown as soft[text] or the source text (not stored)
	inflight map[string]bool
	tries    map[string]int
	retryAt  map[string]time.Time
	paused   time.Time // no request before this (the last one failed)
	apiErrs  int
	running  int
	waiting  bool
	focus    float64 // the player's position
	opening  float64 // > 0: only lines before this second (a prefetched next episode)
	watched  bool    // a player asked for it: the whole file is translated
	apiKey   string
	keyErr   error
	keyErrAt time.Time
	lastUsed time.Time

	saveMu sync.Mutex
}

type aiTranslator struct {
	db    *gorm.DB
	store diskCache
	call  aiCallFunc
	sem   chan struct{}

	mu         sync.Mutex
	jobs       map[string]*aiJob
	prefetched map[string]time.Time
}

func newAITranslator(db *gorm.DB, dir string) *aiTranslator {
	t := &aiTranslator{db: db, store: diskCache{dir: dir}, call: claudeCall, sem: make(chan struct{}, aiParallel),
		jobs: map[string]*aiJob{}, prefetched: map[string]time.Time{}}
	t.dropOldStore()
	return t
}

// dropOldStore removes the translations of the old protocol once: some of their lines belong to
// other lines, and nothing tells which.
func (t *aiTranslator) dropOldStore() {
	if t.store.dir == "" {
		return
	}
	old := filepath.Join(t.store.dir, aiOldStoreKind)
	list, err := os.ReadDir(old)
	if err != nil {
		return
	}
	if err := os.RemoveAll(old); err != nil {
		log.Printf("subtitles: AI translation: removing the old translations: %v", err)
		return
	}
	log.Printf("subtitles: AI translation: removed %d translations of the old protocol (they are made again when watched)", len(list))
}

type aiFile struct {
	Version int               `json:"version"`
	Lang    string            `json:"lang"`
	Texts   map[string]string `json:"texts"`
}

// job returns the translation of key or starts one with the stored lines.
func (t *aiTranslator) job(key string, lang AILanguage, title func() string) *aiJob {
	t.mu.Lock()
	if j, ok := t.jobs[key]; ok {
		t.mu.Unlock()
		return j
	}
	now := time.Now()
	for k, j := range t.jobs {
		j.mu.Lock()
		idle := j.running == 0 && now.Sub(j.lastUsed) > aiJobIdle
		j.mu.Unlock()
		if idle {
			delete(t.jobs, k)
		}
	}
	j := &aiJob{key: key, lang: lang, ids: map[string]int{}, names: map[string]bool{}, done: map[string]string{},
		soft: map[string]string{}, doubted: map[string]bool{}, gaveUp: map[string]bool{}, inflight: map[string]bool{}, tries: map[string]int{},
		retryAt: map[string]time.Time{}, lastUsed: now}
	if b, ok := t.store.read(aiStoreKind, key, ".json", 0); ok {
		var f aiFile
		if json.Unmarshal(b, &f) == nil && f.Version == aiStoreVersion && f.Texts != nil {
			j.done = f.Texts
		}
	}
	t.jobs[key] = j
	t.mu.Unlock()
	name := title()
	j.mu.Lock()
	j.title = name
	j.mu.Unlock()
	return j
}

// has reports whether some lines of key are translated (a list prefers that source).
func (t *aiTranslator) has(key string) bool {
	t.mu.Lock()
	j, ok := t.jobs[key]
	t.mu.Unlock()
	if ok {
		j.mu.Lock()
		defer j.mu.Unlock()
		return len(j.done) > 0
	}
	return t.store.has(aiStoreKind, key, ".json")
}

// resetKeyErrors lets the translations try again at once (a new key).
func (t *aiTranslator) resetKeyErrors() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, j := range t.jobs {
		j.mu.Lock()
		j.keyErr = nil
		j.mu.Unlock()
	}
}

// feed adds the lines of the source (it may grow while a track is read).
func (j *aiJob) feed(cues []liveCue) {
	j.mu.Lock()
	defer j.mu.Unlock()
	added := false
	for _, c := range cues {
		if _, ok := j.ids[c.text]; ok || !hasLetter(c.text) {
			continue // known, or nothing to translate ("♪", numbers): shown as it is
		}
		j.nextID++
		j.ids[c.text] = j.nextID
		j.lines = append(j.lines, aiLine{j.nextID, c.start, c.text})
		added = true
	}
	if !added {
		return
	}
	sort.SliceStable(j.lines, func(a, b int) bool { return j.lines[a].start < j.lines[b].start })
	texts := make([]string, len(j.lines))
	for i, l := range j.lines {
		texts[i] = l.text
	}
	j.names = aiNames(texts)
}

// settledLocked: the line needs no request any more.
func (j *aiJob) settledLocked(text string) bool {
	_, ok := j.done[text]
	_, given := j.given[text]
	return ok || given || j.gaveUp[text]
}

// setGiven sets the context lines whose translation comes from elsewhere (a human subtitle's line
// at that time): fed like the others, they are shown to the model but never asked.
func (j *aiJob) setGiven(given map[string]string) {
	j.mu.Lock()
	j.given = given
	j.mu.Unlock()
}

// atLocked is the index of the first line at the player's position.
func (j *aiJob) atLocked(focus float64) int {
	return sort.Search(len(j.lines), func(i int) bool { return j.lines[i].start >= focus-aiLead })
}

// pendingAtLocked: the line at the player's position still waits for its translation.
func (j *aiJob) pendingAtLocked() bool {
	i := j.atLocked(j.focus)
	return i < len(j.lines) && !j.settledLocked(j.lines[i].text)
}

// readyLocked: the lines at the position are there, or nothing more will come for now.
func (j *aiJob) readyLocked() bool {
	return !j.pendingAtLocked() || j.keyErr != nil || j.running == 0
}

// takeLocked picks the next lines to ask for, nearest to the player first: from the position on,
// then backwards from it. wait > 0 is how long until lines waiting for a retry are due.
func (j *aiJob) takeLocked(now time.Time) (batch []aiLine, wait time.Duration) {
	if j.keyErr != nil && now.Sub(j.keyErrAt) < aiKeyRetryAfter {
		return nil, 0 // the players' requests kick it again later
	}
	if now.Before(j.paused) {
		return nil, j.paused.Sub(now)
	}
	var cand []int
	for i, l := range j.lines {
		if j.settledLocked(l.text) || j.inflight[l.text] || (j.opening > 0 && l.start > j.opening) {
			continue
		}
		if at := j.retryAt[l.text]; now.Before(at) {
			if d := at.Sub(now); wait == 0 || d < wait {
				wait = d
			}
			continue
		}
		cand = append(cand, i)
	}
	if len(cand) == 0 {
		return nil, wait
	}
	from := j.focus - aiLead
	sort.SliceStable(cand, func(a, b int) bool {
		la, lb := j.lines[cand[a]], j.lines[cand[b]]
		aheadA, aheadB := la.start >= from, lb.start >= from
		if aheadA != aheadB {
			return aheadA
		}
		if aheadA {
			return la.start < lb.start
		}
		return la.start > lb.start
	})
	first := j.lines[cand[0]]
	tries := j.tries[first.text]
	size := aiBatch
	switch {
	case tries >= 3:
		size = 1
	case tries == 2:
		size = 3
	case tries == 1:
		size = 8
	case cand[0] == j.atLocked(j.focus):
		size = aiFirstBatch // the player waits for these
	}
	chars := 0
	for _, i := range cand {
		l := j.lines[i]
		if j.tries[l.text] != tries {
			continue // lines failed as often go together: a line that always fails isolates itself
		}
		if len(batch) > 0 && chars+len(l.text) > aiBatchChars {
			break
		}
		batch = append(batch, l)
		chars += len(l.text)
		if len(batch) >= size {
			break
		}
	}
	for _, l := range batch {
		j.inflight[l.text] = true
	}
	return batch, 0
}

func (t *aiTranslator) kick(j *aiJob, apiKey string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if apiKey != "" {
		j.apiKey = apiKey
	}
	for j.running < aiWorkers {
		batch, wait := j.takeLocked(time.Now())
		if len(batch) == 0 {
			if wait > 0 && j.running == 0 {
				j.running++
				go t.work(j, nil) // waits for the retries
			}
			return
		}
		j.running++
		go t.work(j, batch)
	}
}

// work translates batches until nothing is left; one worker stays to wait for lines due for a retry.
func (t *aiTranslator) work(j *aiJob, batch []aiLine) {
	for {
		if len(batch) > 0 {
			t.translate(j, batch)
		}
		j.mu.Lock()
		var wait time.Duration
		batch, wait = j.takeLocked(time.Now())
		if len(batch) > 0 {
			j.mu.Unlock()
			continue
		}
		if wait <= 0 || j.waiting || time.Since(j.lastUsed) > aiJobIdle {
			j.running--
			j.mu.Unlock()
			return
		}
		j.waiting = true
		j.mu.Unlock()
		time.Sleep(min(wait, aiWaitMax))
		j.mu.Lock()
		j.waiting = false
		j.mu.Unlock()
	}
}

// requestLocked builds the request of batch: the lines in screen order with the ones around them
// as context, and what checking the answer needs.
func (j *aiJob) requestLocked(batch []aiLine) ([]aiInLine, map[int]aiCheck) {
	asked := map[int]bool{}
	for _, l := range batch {
		asked[l.id] = true
	}
	index := map[int]int{}
	for i, l := range j.lines {
		index[l.id] = i
	}
	include := map[int]bool{}
	for _, l := range batch {
		i := index[l.id]
		for k := max(0, i-aiContextLines); k <= min(len(j.lines)-1, i+aiContextLines); k++ {
			include[k] = true
		}
	}
	var at []int
	for k := range include {
		at = append(at, k)
	}
	sort.Ints(at)
	var lines []aiInLine
	checks := map[int]aiCheck{}
	for _, k := range at {
		l := j.lines[k]
		if !asked[l.id] {
			tr := j.done[l.text]
			if tr == "" {
				tr = j.given[l.text]
			}
			lines = append(lines, aiInLine{Context: aiPlain(l.text), Translated: aiPlain(tr)})
			continue
		}
		lines = append(lines, aiInLine{ID: l.id, Text: aiPlain(l.text)})
		var near []string
		for n := max(0, k-2); n <= min(len(j.lines)-1, k+2); n++ {
			if n != k {
				near = append(near, j.lines[n].text)
			}
		}
		checks[l.id] = aiCheck{src: l.text, near: near, pos: len(checks)}
	}
	return lines, checks
}

var (
	errAIRefused   = errors.New("AI translation: Claude declined the lines")
	errAITruncated = errors.New("AI translation: the answer was cut off")
)

func (t *aiTranslator) translate(j *aiJob, batch []aiLine) {
	ctx, cancel := context.WithTimeout(context.Background(), aiCallLimit)
	defer cancel()
	j.mu.Lock()
	lines, checks := j.requestLocked(batch)
	about, key, lang, names := j.title, j.apiKey, j.lang, j.names
	j.mu.Unlock()

	t.sem <- struct{}{}
	answer, used, err := t.call(ctx, key, aiSystem(lang), aiUser(about, lines))
	<-t.sem

	var keyErr AIKeyError
	now := time.Now()
	if errors.As(err, &keyErr) || (err != nil && !errors.Is(err, errAIRefused) && !errors.Is(err, errAITruncated)) {
		// The request itself failed: the lines are asked again later, no try is counted
		j.mu.Lock()
		for _, l := range batch {
			delete(j.inflight, l.text)
		}
		if errors.As(err, &keyErr) {
			j.keyErr, j.keyErrAt = err, now
		} else {
			j.apiErrs++
			j.paused = now.Add(min(aiPauseBase<<min(j.apiErrs-1, 8), aiPauseMax))
		}
		j.mu.Unlock()
		log.Printf("subtitles: AI translation of %d lines into %s: %v", len(batch), lang.Code, err)
		t.recordUsage(used, 0)
		return
	}

	var v aiVerdict
	switch {
	case err != nil:
		v = aiVerdict{untrusted: err.Error()}
	default:
		v = checkAIAnswer(answer, checks, lang.Code, names)
	}
	n := 0
	var failed []string
	j.mu.Lock()
	j.apiErrs, j.keyErr = 0, nil // the request went through
	for _, l := range batch {
		delete(j.inflight, l.text)
		if tr, ok := v.ok[l.id]; ok && v.untrusted == "" {
			j.done[l.text] = tr
			delete(j.soft, l.text)
			n++
			continue
		}
		if tr, ok := v.soft[l.id]; ok && v.untrusted == "" {
			own := v.why[l.id] != aiNextToMissing
			if own && j.doubted[l.text] {
				j.done[l.text] = tr // the second answer that looks off: the model insists
				delete(j.soft, l.text)
				n++
				continue
			}
			j.soft[l.text] = tr
			j.doubted[l.text] = j.doubted[l.text] || own
		}
		j.tries[l.text]++
		why := v.untrusted
		if why == "" {
			why = v.why[l.id]
		}
		failed = append(failed, why)
		if j.tries[l.text] >= aiMaxTries {
			j.gaveUp[l.text] = true
			log.Printf("subtitles: AI translation into %s gave up on a line after %d tries (%s)", lang.Code, j.tries[l.text], why)
			continue
		}
		j.retryAt[l.text] = now.Add(aiRetryWait[min(j.tries[l.text], len(aiRetryWait))-1])
	}
	j.mu.Unlock()
	if len(failed) > 0 {
		log.Printf("subtitles: AI translation into %s: %d of %d lines asked again (%s)", lang.Code, len(failed), len(batch), summarize(failed))
	}
	t.recordUsage(used, n)
	if n > 0 {
		t.save(j)
	}
}

// summarize counts reasons: "empty x2, not answered".
func summarize(why []string) string {
	count := map[string]int{}
	var order []string
	for _, w := range why {
		if count[w] == 0 {
			order = append(order, w)
		}
		count[w]++
	}
	parts := make([]string, len(order))
	for i, w := range order {
		parts[i] = w
		if count[w] > 1 {
			parts[i] += " x" + strconv.Itoa(count[w])
		}
	}
	return strings.Join(parts, ", ")
}

func (t *aiTranslator) save(j *aiJob) {
	j.saveMu.Lock()
	defer j.saveMu.Unlock()
	j.mu.Lock()
	b, err := json.Marshal(aiFile{Version: aiStoreVersion, Lang: j.lang.Code, Texts: j.done})
	j.mu.Unlock()
	if err == nil {
		err = t.store.write(aiStoreKind, j.key, ".json", b)
	}
	if err != nil {
		log.Printf("subtitles: AI translation store: %v", err)
	}
}

// renderLocked is the translated source: every cue in the source's timing, a line given up on as
// its last answer or as it is. complete: no line is pending; translating: the line at the
// player's position is.
func (j *aiJob) renderLocked(src []liveCue) (out []liveCue, complete, translating bool) {
	complete = true
	for _, c := range src {
		text := c.text
		switch {
		case !hasLetter(c.text):
		case j.done[c.text] != "":
			text = j.done[c.text]
		case j.gaveUp[c.text]:
			if s := j.soft[c.text]; s != "" {
				text = s
			}
		default:
			complete = false
			continue // pending: the answer stays partial, the players ask again
		}
		out = append(out, liveCue{start: c.start, end: c.end, text: text})
	}
	return out, complete, j.pendingAtLocked() && j.keyErr == nil
}
