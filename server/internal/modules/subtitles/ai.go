package subtitles

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AI translation: when no source has a subtitle in a wanted language, Claude translates the best
// other one (ai_job.go runs it, ai_prompt.go is the request protocol). Every line is paid once:
// the checked translations stay on disk for good.

// AINote marks an answer whose lines at the player's position are still being translated
const AINote = "NOTE nextflix-ai-translating"

const aiStoryMax = 250 // characters of the story in the prompt

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

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
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
	// The source exactly as the player would get it: its timing (and later re-timing) is the translation's
	a, err := s.fileFor(ctx, src, t, false)
	if err != nil {
		return FileAnswer{}, err
	}
	cues := aiCues(a.VTT)
	j := s.ai.job(raw+"|"+l.Code, l, func() string { return s.aiTitle(meta) })
	j.mu.Lock()
	j.focus, j.lastUsed, j.watched, j.opening = math.Max(t, 0), time.Now(), true, 0
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
	// Partial keeps the players asking (with their position) until every line of the whole source is done
	partial := a.Partial || !complete
	vtt := renderVTT(out, partial)
	if translating {
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
	if acct, _ := s.ai.accountError(); s.cfg.NextEpisode == nil || acct != nil {
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
		if !j.watched {
			j.opening = aiPrefetchAhead
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
// While the key or account cannot be used only translations with stored lines are listed.
func (s *Service) addAI(subs []Sub, q Query, targets []string) []Sub {
	acct, _ := s.ai.accountError()
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
		if src == nil || (acct != nil && !s.ai.has(rawSubID(src.ID)+"|"+lang)) {
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

// aiTimingTier ranks a source by how well its timing is known to fit the playing file: the
// file's own text tracks and subtitles made for it (0; 1 for a track still being read), then
// subtitles timed to the file (2), then the rest (3).
func aiTimingTier(x Sub) int {
	_, _, _, timed := unwrapSyncID(x.ID)
	switch {
	case x.Synced && !timed && !x.Live:
		return 0
	case x.Synced && !timed:
		return 1
	case x.Synced:
		return 2
	}
	return 3
}

// pickAISource is the subtitle to translate into lang: the best timing first (aiTimingTier), within
// it one already (partly) translated, else English, else the best other one; never forced or
// quota-blocked ones, nor ones whose timing does not fit the file. OpenSubtitles comes after the
// others unless its file is on disk: its small daily quota may be used up before the list knows it.
func (s *Service) pickAISource(subs []Sub, lang string) *Sub {
	var cands []Sub
	for _, x := range subs {
		if x.Source != SourceAI && x.Language != "" && x.Language != lang && !x.Forced && !x.Quota {
			if inner, hash, index, ok := unwrapSyncID(x.ID); ok && s.sync.poorFor(inner, hash, index) {
				continue // its timing does not fit the playing file
			}
			cands = append(cands, x)
		}
	}
	free := func(x Sub) bool { return x.Source != SourceOpenSubtitles || s.hasFile(rawSubID(x.ID)) }
	prefs := []func(Sub) bool{
		func(x Sub) bool { return s.ai.has(rawSubID(x.ID) + "|" + lang) },
		func(x Sub) bool { return x.Language == "en" && !x.HI },
		func(x Sub) bool { return x.Language == "en" },
		func(x Sub) bool { return !x.HI },
		func(Sub) bool { return true },
	}
	for _, needFree := range []bool{true, false} {
		for tier := 0; tier <= 3; tier++ {
			for _, pref := range prefs {
				for _, x := range cands {
					if aiTimingTier(x) == tier && (!needFree || free(x)) && pref(x) {
						return &x
					}
				}
			}
		}
	}
	return nil
}

// aiPoorWindows: a provisional "poor" timing heard over this many played windows counts as not fitting.
const aiPoorWindows = 5

// poorFor reports that the subtitle inner does not fit the file (its timing found nothing better;
// a result of an older timing does not tell).
func (y *syncer) poorFor(inner, hash string, index int) bool {
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return false
	}
	m := y.meta(key)
	return m != nil && !m.stale() && m.State == syncStatePoor && (!m.Live || m.LiveWindows >= aiPoorWindows)
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
	stored := s.settings.get().AnthropicAPIKey
	key = strings.TrimSpace(key)
	if key == "" {
		key = stored
	}
	if key == "" {
		return AIKeyError{"no Claude API key is set", aiErrNoKey}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, used, err := s.ai.call(ctx, key, "Answer with an empty \"lines\" list.", "Test.")
	s.ai.recordUsage(used, 0)
	// A test of the stored key tells the translations whether it can be used (e.g. after a top-up)
	var keyErr AIKeyError
	switch {
	case key != stored:
	case err == nil:
		s.ai.resetKeyErrors()
	case errors.As(err, &keyErr):
		s.ai.noteAccountError(keyErr, time.Now())
	}
	return err
}

// AIAccountError is the key or account error that holds the AI translation (nil without one).
func (s *Service) AIAccountError() (*AIKeyError, time.Time) { return s.ai.accountError() }
