package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AI translation: when no source has a subtitle in a wanted language, Claude translates the best
// other one. Only the lines around the player are translated (cost follows what is watched), every
// line once: the results stay on disk for good.
const (
	aiAhead         = 15 * 60.0 // seconds of the file translated ahead of the player
	aiPrefetchAhead = 3 * 60.0  // the next episode: its opening only
	aiFirstBatch    = 20        // lines of the first request at a position (quick start)
	aiBatch         = 60        // lines of later requests (the prompt is paid once per request)
	aiSplitMin      = 8         // a shorter request whose answer misses lines is not split again
	aiWorkers       = 3         // requests at a time per translation
	aiParallel      = 6         // requests at a time in total
	aiFirstWait     = 10 * time.Second
	aiRetryAfter    = time.Minute
	aiKeyRetryAfter = 2 * time.Minute
	aiMaxTries      = 3
	aiCallLimit     = 3 * time.Minute
	aiJobIdle       = 30 * time.Minute
	aiStoryMax      = 250 // characters of the story in the prompt
	// AINote marks an answer whose lines at the player's position are still being translated
	AINote = "NOTE nextflix-ai-translating"
)

// ErrAIOff is returned for an AI subtitle while the AI translation is off or has no key.
var ErrAIOff = errors.New("AI translation is off")

// aiMeta names the title of a translation: prompt context and the next episode.
type aiMeta struct {
	MediaType               string
	TmdbID, Season, Episode int
}

func (m aiMeta) String() string {
	return fmt.Sprintf("%s:%d:%d:%d", m.MediaType, m.TmdbID, m.Season, m.Episode)
}

func parseAIMeta(s string) (aiMeta, bool) {
	p := strings.Split(s, ":")
	if len(p) != 4 || (p[0] != "movie" && p[0] != "tv") {
		return aiMeta{}, false
	}
	var n [3]int
	for i := range n {
		v, err := strconv.Atoi(p[i+1])
		if err != nil || v < 0 {
			return aiMeta{}, false
		}
		n[i] = v
	}
	return aiMeta{p[0], n[0], n[1], n[2]}, true
}

// aiID is the id of the translation of the listed subtitle src into lang.
func aiID(lang string, m aiMeta, src string) string {
	return encodeID(SourceAI, lang, m.String()+"|"+src)
}

type aiCue struct {
	start float64
	text  string
}

// aiJob is the translation of one subtitle into one language.
type aiJob struct {
	key   string // raw source id | language
	lang  AILanguage
	title string

	mu       sync.Mutex
	cues     []aiCue // first appearance of each text, by start
	seen     map[string]bool
	done     map[string]string // source text -> translation
	inflight map[string]bool
	failed   map[string]time.Time
	tries    map[string]int
	running  int
	focus    float64 // the player's position
	ahead    float64 // seconds translated ahead of focus
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
	return &aiTranslator{db: db, store: diskCache{dir: dir}, call: claudeCall, sem: make(chan struct{}, aiParallel),
		jobs: map[string]*aiJob{}, prefetched: map[string]time.Time{}}
}

const aiStoreKind = "translations"

type aiFile struct {
	Lang  string            `json:"lang"`
	Texts map[string]string `json:"texts"`
}

// job returns the running translation of key or starts one with the stored lines.
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
	j := &aiJob{key: key, lang: lang, seen: map[string]bool{}, done: map[string]string{}, inflight: map[string]bool{},
		failed: map[string]time.Time{}, tries: map[string]int{}, lastUsed: now}
	if b, ok := t.store.read(aiStoreKind, key, ".json", 0); ok {
		var f aiFile
		if json.Unmarshal(b, &f) == nil && f.Texts != nil {
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

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// feed adds the lines of the source (it may grow while a track is read).
func (j *aiJob) feed(cues []liveCue) {
	j.mu.Lock()
	defer j.mu.Unlock()
	added := false
	for _, c := range cues {
		if j.seen[c.text] {
			continue
		}
		j.seen[c.text] = true
		if !hasLetter(c.text) {
			j.done[c.text] = c.text // "♪", numbers: nothing to translate
			continue
		}
		j.cues = append(j.cues, aiCue{c.start, c.text})
		added = true
	}
	if added {
		sort.SliceStable(j.cues, func(a, b int) bool { return j.cues[a].start < j.cues[b].start })
	}
}

// atLocked is the index of the first line at the player's position.
func (j *aiJob) atLocked(focus float64) int {
	return sort.Search(len(j.cues), func(i int) bool { return j.cues[i].start >= focus-5 })
}

// readyLocked: the lines at the position are translated, or nothing more will come for now.
func (j *aiJob) readyLocked() bool {
	i := j.atLocked(j.focus)
	if i >= len(j.cues) || j.keyErr != nil || j.running == 0 {
		return true
	}
	_, ok := j.done[j.cues[i].text]
	return ok
}

// takeLocked picks the next lines to translate: from the player's position on, within ahead. The
// first request at a position is small so the player gets lines quickly.
func (j *aiJob) takeLocked(now time.Time) []string {
	if j.keyErr != nil && now.Sub(j.keyErrAt) < aiKeyRetryAfter {
		return nil
	}
	i := j.atLocked(j.focus)
	size := aiBatch
	if i < len(j.cues) {
		first := j.cues[i].text
		if _, ok := j.done[first]; !ok && !j.inflight[first] {
			size = aiFirstBatch
		}
	}
	var out []string
	for ; i < len(j.cues) && len(out) < size; i++ {
		c := j.cues[i]
		if len(out) == 0 && c.start > j.focus+j.ahead {
			break
		}
		if _, ok := j.done[c.text]; ok || j.inflight[c.text] || now.Sub(j.failed[c.text]) < aiRetryAfter {
			continue
		}
		out = append(out, c.text)
	}
	for _, s := range out {
		j.inflight[s] = true
	}
	return out
}

// beforeLocked is up to n source lines before text (prompt context).
func (j *aiJob) beforeLocked(text string, n int) []string {
	for i, c := range j.cues {
		if c.text == text {
			var out []string
			for k := max(0, i-n); k < i; k++ {
				out = append(out, j.cues[k].text)
			}
			return out
		}
	}
	return nil
}

func (t *aiTranslator) kick(j *aiJob, apiKey string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for j.running < aiWorkers {
		batch := j.takeLocked(time.Now())
		if len(batch) == 0 {
			return
		}
		j.running++
		go t.work(j, apiKey, batch)
	}
}

func (t *aiTranslator) work(j *aiJob, apiKey string, batch []string) {
	for len(batch) > 0 {
		t.translate(j, apiKey, batch)
		j.mu.Lock()
		batch = j.takeLocked(time.Now())
		if len(batch) == 0 {
			j.running--
		}
		j.mu.Unlock()
	}
}

func (t *aiTranslator) translate(j *aiJob, apiKey string, batch []string) {
	ctx, cancel := context.WithTimeout(context.Background(), aiCallLimit)
	defer cancel()
	j.mu.Lock()
	before := j.beforeLocked(batch[0], 2)
	title := j.title
	j.mu.Unlock()
	system, user := aiPrompt(j.lang, title, before, batch)

	t.sem <- struct{}{}
	answer, used, err := t.call(ctx, apiKey, system, user)
	<-t.sem

	got := parseAIAnswer(answer, batch)
	for n, tr := range got {
		if foreignScript(tr, j.lang.Code) {
			delete(got, n) // written in another language
		}
	}
	// Lines missing: with the last line answered the numbers followed the input (some lines were
	// skipped) and only those are asked again; without it they may be shifted, so halves are.
	var missing []string
	var missingNums []int
	for i, src := range batch {
		if _, ok := got[i+1]; !ok {
			missing = append(missing, src)
			missingNums = append(missingNums, i+1)
		}
	}
	_, lastOK := got[len(batch)]
	if err == nil && len(missing) > 0 {
		if !lastOK && len(batch) > aiSplitMin {
			log.Printf("subtitles: AI translation into %s missed lines %v of %d, asking again in halves", j.lang.Code, missingNums, len(batch))
			t.recordUsage(used, 0)
			h := len(batch) / 2
			t.translate(j, apiKey, batch[:h])
			t.translate(j, apiKey, batch[h:])
			return
		}
		log.Printf("subtitles: AI translation into %s missed lines %v of %d", j.lang.Code, missingNums, len(batch))
		if lastOK && len(missing) < len(batch) {
			defer t.translate(j, apiKey, missing)
		}
	}
	var keyErr AIKeyError
	isKeyErr := errors.As(err, &keyErr)
	now := time.Now()
	n := 0
	j.mu.Lock()
	for i, src := range batch {
		delete(j.inflight, src)
		if tr, ok := got[i+1]; ok && err == nil {
			j.done[src] = tr
			n++
			continue
		}
		if isKeyErr {
			continue
		}
		j.tries[src]++
		if j.tries[src] >= aiMaxTries {
			j.done[src] = src // keep the original line rather than ask forever
			continue
		}
		j.failed[src] = now
	}
	if isKeyErr {
		j.keyErr, j.keyErrAt = err, now
	}
	j.mu.Unlock()
	if err != nil {
		log.Printf("subtitles: AI translation of %d lines into %s: %v", len(batch), j.lang.Code, err)
	}
	t.recordUsage(used, n)
	if n > 0 {
		t.save(j)
	}
}

func (t *aiTranslator) save(j *aiJob) {
	j.saveMu.Lock()
	defer j.saveMu.Unlock()
	j.mu.Lock()
	b, err := json.Marshal(aiFile{Lang: j.lang.Code, Texts: j.done})
	j.mu.Unlock()
	if err == nil {
		err = t.store.write(aiStoreKind, j.key, ".json", b)
	}
	if err != nil {
		log.Printf("subtitles: AI translation store: %v", err)
	}
}

// foreignScript reports Chinese, Japanese or Korean script in a translation into another language.
func foreignScript(text, lang string) bool {
	if lang == "zh" || lang == "ja" || lang == "ko" {
		return false
	}
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func aiSourceText(s string) string {
	return strings.ReplaceAll(html.UnescapeString(s), "\n", " <br> ")
}

// aiPrompt builds a request; about is aiTitle's block.
func aiPrompt(lang AILanguage, about string, before, batch []string) (system, user string) {
	system = fmt.Sprintf("You translate film and TV subtitles into %[1]s. Write only in %[1]s. Each input line is \"n: text\". "+
		"Answer only with numbered lines, \"n: translation\", in order, and nothing else. "+
		"A sentence often runs over several lines: translate each line on its own when you can; when %[1]s word order needs "+
		"some lines together, answer them once as \"a-b: translation\" (for example \"26-27: ...\"). Cover every number exactly once. "+
		"<br> marks a line break, keep it. Keep <i>, <b> and <u> tags. "+
		"Write natural, concise spoken %[1]s that reads well as subtitles. Keep names. "+
		"Fit word choice and tone to the title's genre and story without changing the meaning.", lang.Name)
	var b strings.Builder
	b.WriteString(about)
	if len(before) > 0 {
		b.WriteString("Lines before (context only):\n")
		for _, s := range before {
			b.WriteString("- " + aiSourceText(s) + "\n")
		}
	}
	b.WriteString("\n")
	for i, s := range batch {
		fmt.Fprintf(&b, "%d: %s\n", i+1, aiSourceText(s))
	}
	return system, b.String()
}

var aiAnswerLine = regexp.MustCompile(`^\s*(\d+)(?:\s*[-–]\s*(\d+))?\s*[:.)]\s?(.*)$`)

// aiMaxRange is the most lines one "a-b: translation" answer may cover.
const aiMaxRange = 6

// parseAIAnswer reads "n: translation" and "a-b: translation" lines as cue texts of batch (1-based); a
// range is divided over its lines. An empty text means the line shows nothing.
func parseAIAnswer(answer string, batch []string) map[int]string {
	out := map[int]string{}
	for _, line := range strings.Split(answer, "\n") {
		m := aiAnswerLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b := a
		if m[2] != "" {
			b, _ = strconv.Atoi(m[2])
		}
		if a < 1 || b < a || b > len(batch) || b-a >= aiMaxRange {
			continue
		}
		if a == b {
			if text := cleanCueText(m[3]); text != "" {
				out[a] = text
			}
			continue
		}
		for i, part := range splitAcross(m[3], batch[a-1:b]) {
			out[a+i] = cleanCueText(part)
		}
	}
	return out
}

// splitAcross divides one translation of several source lines over them, by words in proportion
// to the lengths of the lines.
func splitAcross(text string, srcs []string) []string {
	if parts := strings.Split(text, "<br>"); len(parts) == len(srcs) {
		return parts // one part per line already
	}
	words := strings.Fields(strings.ReplaceAll(text, "<br>", " "))
	total := 0
	for _, s := range srcs {
		total += utf8.RuneCountInString(s) + 1
	}
	out := make([]string, len(srcs))
	from, acc := 0, 0
	for i, s := range srcs {
		acc += utf8.RuneCountInString(s) + 1
		to := int(math.Round(float64(len(words)) * float64(acc) / float64(total)))
		// Every line gets a word, the first ones when there are too few
		to = max(to, from+1)
		to = min(to, max(from+1, len(words)-(len(srcs)-1-i)), len(words))
		if i == len(srcs)-1 {
			to = len(words)
		}
		out[i] = strings.Join(words[from:to], " ")
		from = to
	}
	return out
}

// renderLocked is the translated cues of the source, how far the translation is and whether the
// lines at the player's position are still missing.
func (j *aiJob) renderLocked(src []liveCue) (out []liveCue, complete, translating bool) {
	for _, c := range src {
		if tr, ok := j.done[c.text]; ok && tr != "" {
			out = append(out, liveCue{start: c.start, end: c.end, text: tr})
		}
	}
	complete = true
	for _, c := range j.cues {
		if _, ok := j.done[c.text]; !ok {
			complete = false
			break
		}
	}
	return out, complete, !j.doneAtLocked() && j.keyErr == nil
}

// doneAtLocked: the line at the player's position is translated.
func (j *aiJob) doneAtLocked() bool {
	i := j.atLocked(j.focus)
	if i >= len(j.cues) {
		return true
	}
	_, ok := j.done[j.cues[i].text]
	return ok
}

// serveAI answers a file request of an AI subtitle: the source as served for the player (timed
// for the file when it is a provider subtitle) with the translated lines.
func (s *Service) serveAI(ctx context.Context, lang, ref string, t float64) (FileAnswer, error) {
	metaStr, src, ok := strings.Cut(ref, "|")
	meta, ok2 := parseAIMeta(metaStr)
	l, ok3 := aiLanguage(lang)
	if !ok || !ok2 || !ok3 {
		return FileAnswer{}, ErrBadID
	}
	raw := rawSubID(src)
	if source, _, _, err := decodeID(raw); err != nil || source == SourceAI {
		return FileAnswer{}, ErrBadID
	}
	st := s.settings.get()
	if !st.AITranslateEnabled || st.AnthropicAPIKey == "" {
		return FileAnswer{}, ErrAIOff
	}
	// A source that does not fit the playing file is not worth translating: another one is taken
	if inner, hash, index, ok := unwrapSyncID(src); ok && s.sync.poorFor(inner, hash, index) {
		if alt := s.aiAlternative(src, hash, index, l.Code); alt != "" {
			log.Printf("subtitles: AI translation source %s does not fit %s:%d, using %s", shortSubID(inner), hash[:8], index, shortSubID(rawSubID(alt)))
			src, raw = alt, rawSubID(alt)
		}
	}
	a, err := s.FileFor(ctx, src, t)
	if err != nil {
		return FileAnswer{}, err
	}
	cues := aiCues(a.VTT)
	j := s.ai.job(raw+"|"+l.Code, l, func() string { return s.aiTitle(meta) })
	j.mu.Lock()
	j.focus, j.ahead, j.lastUsed = math.Max(t, 0), aiAhead, time.Now()
	j.mu.Unlock()
	j.feed(cues)
	s.ai.kick(j, st.AnthropicAPIKey)
	if meta.MediaType == "tv" {
		s.aiPrefetchNext(meta, l)
	}

	// The first answer waits a little for the lines at the player's position
	deadline := time.Now().Add(aiFirstWait)
	for {
		j.mu.Lock()
		ready := j.readyLocked()
		j.mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return FileAnswer{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	j.mu.Lock()
	out, complete, translating := j.renderLocked(cues)
	keyErr := j.keyErr
	j.mu.Unlock()
	if len(out) == 0 && keyErr != nil {
		return FileAnswer{}, keyErr
	}
	// Partial keeps the players asking (with their position) until every line is translated
	partial := a.Partial || !complete
	vtt := renderVTT(out, partial)
	if translating && keyErr == nil {
		vtt = []byte(strings.Replace(string(vtt), "WEBVTT\n\n", "WEBVTT\n\n"+AINote+"\n\n", 1))
	}
	return FileAnswer{VTT: vtt, Partial: partial, Sync: a.Sync}, nil
}

// aiTitle is the prompt context of a title: name, genres and a short story.
func (s *Service) aiTitle(m aiMeta) string {
	if s.cfg.Titles == nil || m.TmdbID <= 0 {
		return ""
	}
	info, err := s.cfg.Titles(m.MediaType, m.TmdbID)
	if err != nil || len(info.Names) == 0 {
		return ""
	}
	var b strings.Builder
	switch {
	case m.MediaType == "tv":
		fmt.Fprintf(&b, "Title: %s, S%02dE%02d\n", info.Names[0], m.Season, m.Episode)
	case info.Year > 0:
		fmt.Fprintf(&b, "Title: %s (%d)\n", info.Names[0], info.Year)
	default:
		fmt.Fprintf(&b, "Title: %s\n", info.Names[0])
	}
	if len(info.Genres) > 0 {
		fmt.Fprintf(&b, "Genre: %s\n", strings.Join(info.Genres, ", "))
	}
	if story := strings.TrimSpace(info.Overview); story != "" {
		if r := []rune(story); len(r) > aiStoryMax {
			story = strings.TrimSpace(string(r[:aiStoryMax])) + "…"
		}
		fmt.Fprintf(&b, "Story: %s\n", story)
	}
	return b.String()
}

// aiPrefetchNext translates the opening of the next episode once, so it starts without a wait.
func (s *Service) aiPrefetchNext(m aiMeta, lang AILanguage) {
	if s.cfg.NextEpisode == nil {
		return
	}
	key := m.String() + "|" + lang.Code
	s.ai.mu.Lock()
	now := time.Now()
	if now.Sub(s.ai.prefetched[key]) < 6*time.Hour {
		s.ai.mu.Unlock()
		return
	}
	if len(s.ai.prefetched) > 1000 {
		s.ai.prefetched = map[string]time.Time{}
	}
	s.ai.prefetched[key] = now
	s.ai.mu.Unlock()

	go func() {
		ns, ne, ok := s.cfg.NextEpisode(m.TmdbID, m.Season, m.Episode)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		st := s.settings.get()
		q := Query{TmdbID: m.TmdbID, MediaType: "tv", Season: ns, Episode: ne, Langs: st.langs()}
		var src string
		for _, it := range s.list(ctx, q, providerLimit, []string{lang.Code}) {
			if it.Source == SourceAI && it.Language == lang.Code {
				_, _, ref, _ := decodeID(it.ID)
				_, src, _ = strings.Cut(ref, "|")
				break
			}
		}
		if src == "" {
			return // a subtitle in the language exists, or nothing to translate from
		}
		vtt, err := s.File(ctx, src)
		if err != nil {
			log.Printf("subtitles: AI prefetch of S%02dE%02d: %v", ns, ne, err)
			return
		}
		next := aiMeta{"tv", m.TmdbID, ns, ne}
		j := s.ai.job(src+"|"+lang.Code, lang, func() string { return s.aiTitle(next) })
		j.mu.Lock()
		if j.ahead == 0 {
			j.ahead = aiPrefetchAhead
		}
		j.mu.Unlock()
		j.feed(aiCues(vtt))
		s.ai.kick(j, st.AnthropicAPIKey)
	}()
}

// aiTargets are the languages an AI translation may be listed for ("profile" is the viewer's).
func (s *Service) aiTargets(st Settings, q Query) []string {
	if !st.AITranslateEnabled || st.AnthropicAPIKey == "" {
		return nil
	}
	var out []string
	for _, c := range st.aiLangs() {
		if c == aiProfileLang {
			c = q.UILang
			if _, ok := aiLanguage(c); !ok {
				c = st.langs()[0]
			}
		}
		if _, ok := aiLanguage(c); ok && !containsString(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// aiSearchLangs adds the targets and English (the usual source) to the searched languages.
func aiSearchLangs(langs, targets []string) []string {
	out := append([]string(nil), langs...)
	for _, l := range append(append([]string(nil), targets...), "en") {
		if !containsString(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// addAI lists a translation for every target without a usable subtitle, in its language's place.
func (s *Service) addAI(subs []Sub, q Query, targets []string) []Sub {
	for _, lang := range targets {
		human := false
		for _, x := range subs {
			if x.Language == lang && x.Source != SourceAI && !x.Forced && !x.Quota {
				human = true
				break
			}
		}
		if human {
			continue
		}
		src := s.pickAISource(subs, lang)
		if src == nil {
			continue
		}
		meta := aiMeta{q.MediaType, q.TmdbID, q.Season, q.Episode}
		item := Sub{ID: aiID(lang, meta, src.ID), Source: SourceAI, Language: lang, Label: langLabel(lang),
			Release: src.Release, Synced: src.Synced, Author: "Claude", AIFrom: src.Language}
		order := func(l string) int {
			for i, v := range q.Langs {
				if v == l {
					return i
				}
			}
			return len(q.Langs)
		}
		at := len(subs)
		for i, x := range subs {
			if order(x.Language) > order(lang) {
				at = i
				break
			}
		}
		subs = append(subs[:at], append([]Sub{item}, subs[at:]...)...)
	}
	return subs
}

// rawSubID is the provider id of a listed subtitle (without the file it is timed for).
func rawSubID(id string) string {
	if inner, _, _, ok := unwrapSyncID(id); ok {
		return inner
	}
	return id
}

// pickAISource is the subtitle to translate into lang: one already (partly) translated, else
// English, else the best other one; never forced, live or quota-blocked ones. OpenSubtitles comes
// last unless its file is on disk: its small daily quota may be used up before the list knows it.
func (s *Service) pickAISource(subs []Sub, lang string) *Sub {
	var cands []Sub
	for _, x := range subs {
		if x.Source != SourceAI && x.Language != "" && x.Language != lang && !x.Forced && !x.Live && !x.Quota {
			if inner, hash, index, ok := unwrapSyncID(x.ID); ok && s.sync.poorFor(inner, hash, index) {
				continue // its timing does not fit the playing file
			}
			cands = append(cands, x)
		}
	}
	for _, x := range cands {
		if s.ai.has(rawSubID(x.ID) + "|" + lang) {
			return &x
		}
	}
	free := func(x Sub) bool { return x.Source != SourceOpenSubtitles || s.hasFile(rawSubID(x.ID)) }
	for _, pref := range []func(Sub) bool{
		func(x Sub) bool { return free(x) && x.Language == "en" && !x.HI },
		func(x Sub) bool { return free(x) && x.Language == "en" },
		func(x Sub) bool { return free(x) && !x.HI },
		free,
		func(x Sub) bool { return x.Language == "en" },
		func(Sub) bool { return true },
	} {
		for _, x := range cands {
			if pref(x) {
				return &x
			}
		}
	}
	return nil
}

// aiPoorWindows: a provisional "poor" timing heard over this many played windows counts as not fitting.
const aiPoorWindows = 5

// poorFor reports that the subtitle inner does not fit the file (its timing found nothing better).
func (y *syncer) poorFor(inner, hash string, index int) bool {
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return false
	}
	m := y.meta(key)
	return m != nil && m.State == syncStatePoor && (!m.Live || m.LiveWindows >= aiPoorWindows)
}

// aiAlternative is another source for a translation into lang than src (which does not fit the
// file), from the file's last list; an OpenSubtitles download may be used for it.
func (s *Service) aiAlternative(src, hash string, index int, lang string) string {
	s.sync.mu.Lock()
	list := s.sync.lists[syncFileKey(hash, index)]
	var others []Sub
	for _, x := range list {
		if x.ID != src {
			others = append(others, x)
		}
	}
	s.sync.mu.Unlock()
	if alt := s.pickAISource(others, lang); alt != nil {
		return alt.ID
	}
	return ""
}

var (
	hiNote    = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)
	hiSpeaker = regexp.MustCompile(`^(\s*(?:<[ibu]>)?\s*-?\s*)\p{Lu}[\p{Lu}\d .'&-]*\p{Lu}:\s*`)
	cueTag    = regexp.MustCompile(`</?[ibu]>`)
	hiGap     = regexp.MustCompile(`\s+([.,!?;:])|(\s)\s+`)
	// A music note of an SDH subtitle ("* tense music plays *", "♪ soft music continues"), not lyrics
	hiMusic = regexp.MustCompile(`(?i)^[-\s]*[*♪♫#].*\b(music|musical|playing|plays|continues|intensifies|fades|ends|softens|stinger|humming|hums|vocali[sz]ing|chim(?:e|es|ing))\b`)
)

// stripHI removes what a hearing-impaired subtitle adds (sound notes, speaker labels): the
// translation is a regular subtitle, and those parts are not paid for.
func stripHI(text string) string {
	var lines []string
	inMusic := false // a music note going on over lines ("* dramatic music\nintensifies *")
	for _, l := range strings.Split(text, "\n") {
		plain := cueTag.ReplaceAllString(l, "")
		if strings.HasPrefix(strings.TrimSpace(plain), "-") {
			inMusic = false // a new speaker line
		}
		if inMusic || hiMusic.MatchString(plain) {
			// It ends on this line when the line closes it (a second marker, or the first one of a continuation)
			markers := strings.Count(plain, "*") + strings.Count(plain, "♪") + strings.Count(plain, "♫") + strings.Count(plain, "#")
			inMusic = (inMusic && markers == 0) || (!inMusic && markers < 2)
			continue
		}
		l = hiSpeaker.ReplaceAllString(hiNote.ReplaceAllString(l, ""), "$1")
		l = hiGap.ReplaceAllString(l, "$1$2")
		if !hasLetter(cueTag.ReplaceAllString(l, "")) {
			continue
		}
		lines = append(lines, strings.TrimSpace(l))
	}
	return strings.Join(lines, "\n")
}

// aiCues are the cues of a source without its hearing-impaired parts (empty ones dropped).
func aiCues(vtt []byte) []liveCue {
	var out []liveCue
	for _, c := range vttCues(vtt) {
		if c.text = stripHI(c.text); c.text != "" {
			out = append(out, c)
		}
	}
	return out
}

// AIUsage counts the AI translation per month.
type AIUsage struct {
	Month        string `gorm:"primaryKey;column:month" json:"month"`
	Lines        int64  `gorm:"column:lines" json:"lines"`
	InputTokens  int64  `gorm:"column:input_tokens" json:"input_tokens"`
	OutputTokens int64  `gorm:"column:output_tokens" json:"output_tokens"`
}

func (AIUsage) TableName() string { return "ai_translation_usage" }

// AIUsageView is this month's usage in the settings.
type AIUsageView struct {
	AIUsage
	CostUSD float64 `json:"cost_usd"`
}

func (t *aiTranslator) recordUsage(used aiTokens, lines int) {
	if t.db == nil || (used.In == 0 && used.Out == 0 && lines == 0) {
		return
	}
	row := AIUsage{Month: time.Now().Format("2006-01"), Lines: int64(lines), InputTokens: used.In, OutputTokens: used.Out}
	err := t.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "month"}},
		DoUpdates: clause.Assignments(map[string]any{
			"lines":         gorm.Expr("lines + ?", row.Lines),
			"input_tokens":  gorm.Expr("input_tokens + ?", row.InputTokens),
			"output_tokens": gorm.Expr("output_tokens + ?", row.OutputTokens),
		}),
	}).Create(&row).Error
	if err != nil {
		log.Printf("subtitles: AI usage: %v", err)
	}
}

func (t *aiTranslator) usage() AIUsageView {
	v := AIUsageView{AIUsage: AIUsage{Month: time.Now().Format("2006-01")}}
	if t.db != nil {
		var row AIUsage
		if t.db.First(&row, "month = ?", v.Month).Error == nil {
			v.AIUsage = row
		}
	}
	v.CostUSD = math.Round((float64(v.InputTokens)*aiInputPrice+float64(v.OutputTokens)*aiOutputPrice)/1e6*10000) / 10000
	return v
}

// TestAIKey sends a tiny request with key (empty: the stored one).
func (s *Service) TestAIKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		key = s.settings.get().AnthropicAPIKey
	}
	if key == "" {
		return AIKeyError{"no Claude API key is set"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, used, err := s.ai.call(ctx, strings.TrimSpace(key), "Answer with OK.", "OK?")
	s.ai.recordUsage(used, 0)
	return err
}
