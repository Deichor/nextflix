package streaming

import (
	"log"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nextflix/internal/platform/tmdb"
)

// Episode numbers in release and file names. Besides S01E05 and 1x05, foreign (mostly
// Turkish) releases write "Bölüm 5", "5. Bölüm", "BL.90", "B05", "Sezon 1 Bölüm 5", "ep.1",
// or only the absolute number after the title ("Cukur 94 S4 E1", "Cukur.BL.90"). Absolute
// numbers are mapped with TMDB's episode counts per season; a show is sometimes released
// as one season ("Season: 1 / Episodes: 01-107 of 107").

const (
	tsSeasonEpsTTL     = 24 * time.Hour
	tsSeasonEpsMissTTL = 10 * time.Minute
	tsSeasonEpsWait    = 2500 * time.Millisecond // counted from the start of the resolve
	tsMaxAbsolute      = 1899                    // bare numbers above this are years
)

var (
	// "e05", "ep1", "b05", "bl90", "blm3", "bolum05"
	tsEpMarkPattern = regexp.MustCompile(`^(?:e|ep|eps|b|bl|blm|bolum)(\d{1,4})$`)
	tsNumberPattern = regexp.MustCompile(`^\d{1,4}$`)
	tsEpWords       = map[string]bool{"ep": true, "eps": true, "episode": true, "episodes": true, "bolum": true, "bolumu": true,
		"bolumler": true, "bl": true, "blm": true}
	tsSeasonWords = map[string]bool{"season": true, "seasons": true, "sezon": true, "sezonu": true, "sezonlar": true, "sez": true}
	// Bare numbers that are formats, not episodes
	tsFormatNumbers = map[int]bool{480: true, 576: true, 720: true, 1080: true, 264: true, 265: true}
)

// tsEpInfo is what a name says about seasons and episodes.
type tsEpInfo struct {
	season, seasonTo int  // season or season range (0 = none)
	ep, epTo         int  // episode range of that season (0 = none)
	num, numTo       int  // episode number(s) without a season: absolute, or of a season the context gives
	lead             bool // num is the bare number right after the title ("Cukur 94 S4 E1")
	complete         bool
	marked           bool // the first token belongs to the episode info (else the title goes on)
}

func (e tsEpInfo) String() string {
	var b strings.Builder
	if e.season > 0 {
		b.WriteString("S" + strconv.Itoa(e.season))
		if e.seasonTo > e.season {
			b.WriteString("-S" + strconv.Itoa(e.seasonTo))
		}
	}
	if e.ep > 0 {
		b.WriteString("E" + strconv.Itoa(e.ep))
		if e.epTo > e.ep {
			b.WriteString("-" + strconv.Itoa(e.epTo))
		}
	}
	if e.num > 0 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("#" + strconv.Itoa(e.num))
		if e.numTo > e.num {
			b.WriteString("-" + strconv.Itoa(e.numTo))
		}
	}
	if e.complete {
		b.WriteString(" complete")
	}
	return strings.TrimSpace(b.String())
}

// tsEpNumber reads a bare episode number: 1..tsMaxAbsolute, not a resolution or codec.
func tsEpNumber(t string) (int, bool) {
	if !tsNumberPattern.MatchString(t) {
		return 0, false
	}
	n, _ := strconv.Atoi(t)
	if n < 1 || n > tsMaxAbsolute || tsFormatNumbers[n] {
		return 0, false
	}
	return n, true
}

// tsParseEpisode reads the episode info of normalized tokens (the part after the title).
// The first notation of a kind wins; numbers after format tags ("DDP5 1", "H 264") are not
// read, a bare number only counts right after the title or next to an episode word.
func tsParseEpisode(tokens []string) tsEpInfo {
	var e tsEpInfo
	setEp := func(from, to int) {
		switch {
		case e.season > 0 && e.ep == 0:
			e.ep, e.epTo = from, to
		case e.season == 0 && e.num == 0:
			e.num, e.numTo = from, to
		}
	}
	setSeason := func(from, to int) {
		if e.season == 0 {
			e.season, e.seasonTo = from, to
		}
	}
	// numRange reads "<n> <m>" ("01-107" normalizes to two tokens) starting at i
	numRange := func(i, n int) (int, int) {
		if i+1 < len(tokens) {
			if m, ok := tsEpNumber(tokens[i+1]); ok && m > n {
				return m, i + 1
			}
		}
		return n, i
	}
	for i := 0; i < len(tokens); i++ {
		start, t := i, tokens[i]
		next := ""
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		used := true
		if m := tsEpPattern.FindStringSubmatch(t); m != nil {
			s, _ := strconv.Atoi(m[1])
			e1, _ := strconv.Atoi(m[2])
			e2 := e1
			if m[3] != "" {
				e2, _ = strconv.Atoi(m[3])
			} else if mm := tsEpMarkPattern.FindStringSubmatch(next); mm != nil && strings.HasPrefix(next, "e") {
				// "S01E01-E03" normalizes to "s01e01 e03"
				if n, _ := strconv.Atoi(mm[1]); n > e1 {
					e2 = n
					i++
				}
			}
			if e.season == 0 && e.ep == 0 {
				e.season, e.seasonTo, e.ep, e.epTo = s, s, e1, e2
			}
		} else if m := tsXEpPattern.FindStringSubmatch(t); m != nil {
			s, _ := strconv.Atoi(m[1])
			ep, _ := strconv.Atoi(m[2])
			if e.season == 0 && e.ep == 0 {
				e.season, e.seasonTo, e.ep, e.epTo = s, s, ep, ep
			}
		} else if m := tsSeasonPattern.FindStringSubmatch(t); m != nil {
			s, _ := strconv.Atoi(m[1])
			to := s
			if mm := tsSeasonPattern.FindStringSubmatch(next); mm != nil {
				if n, _ := strconv.Atoi(mm[1]); n > s {
					to = n
					i++
				}
			}
			setSeason(s, to)
		} else if m := tsEpMarkPattern.FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			setEp(n, n)
		} else if t == "series" && tsNumberPattern.MatchString(next) {
			n, _ := strconv.Atoi(next)
			if e.season > 0 {
				// "Season: 2 / Series: 108-138": episodes (Russian trackers)
				to, j := numRange(i+1, n)
				i = j
				setEp(n, to)
			} else {
				// "Series 2": a season (British)
				i++
				setSeason(n, n)
			}
		} else if tsSeasonWords[t] && next != "" && tsNumberPattern.MatchString(next) {
			s, _ := strconv.Atoi(next)
			i++
			to := s
			if i+1 < len(tokens) {
				if n, err := strconv.Atoi(tokens[i+1]); err == nil && n > s && n < 100 && tsNumberPattern.MatchString(tokens[i+1]) {
					to = n
					i++
				}
			}
			setSeason(s, to)
		} else if tsEpWords[t] && next != "" {
			if n, ok := tsEpNumber(next); ok {
				to, j := numRange(i+1, n)
				i = j
				setEp(n, to)
			} else {
				used = false
			}
		} else if n, ok := tsEpNumber(t); ok && tsSeasonWords[next] {
			setSeason(n, n) // "4. Sezon"
			i++
		} else if ok && tsEpWords[next] {
			setEp(n, n) // "5. Bölüm"
			i++
		} else if ok && i == 0 {
			to, j := numRange(i, n)
			i = j
			e.num, e.numTo, e.lead = n, to, true
		} else if t == "complete" {
			e.complete = true
		} else if t == "series" && start == 0 {
			// marks the episode info like the old "Show Series ..." names, says nothing itself
		} else {
			used = false
		}
		if start == 0 && used {
			e.marked = true
		}
	}
	// "Bölüm 5 Sezon 2": the number belongs to the season named after it
	if e.season > 0 && e.ep == 0 && e.num > 0 && !e.lead {
		e.ep, e.epTo, e.num, e.numTo = e.num, e.numTo, 0, 0
	}
	if e.epTo < e.ep {
		e.epTo = e.ep
	}
	if e.numTo < e.num {
		e.numTo = e.num
	}
	return e
}

// tsDropDate drops the month and day that follow the year of a dated name ("Show 2024 10 15").
func tsDropDate(rest []string) []string {
	if len(rest) >= 2 && len(rest[0]) == 2 && len(rest[1]) == 2 {
		m, err1 := strconv.Atoi(rest[0])
		d, err2 := strconv.Atoi(rest[1])
		if err1 == nil && err2 == nil && m >= 1 && m <= 12 && d >= 1 && d <= 31 {
			return rest[2:]
		}
	}
	return rest
}

// tsEpTarget is the requested episode.
type tsEpTarget struct {
	season, episode int
	abs             int   // absolute number (0 = unknown)
	counts          []int // episodes per season, counts[0] = season 1 (nil = unknown)
}

func (q tsResolveQuery) epTarget() tsEpTarget {
	abs := q.absEp
	if abs == 0 && q.season == 1 {
		abs = q.episode
	}
	return tsEpTarget{season: q.season, episode: q.episode, abs: abs, counts: q.seasonEps}
}

// count is the number of episodes of a season (0 = unknown).
func (t tsEpTarget) count(season int) int {
	if season < 1 || season > len(t.counts) {
		return 0
	}
	return t.counts[season-1]
}

// spans: an absolute range covers episodes of another season than the target's.
func (t tsEpTarget) spans(from, to int) bool {
	c := t.count(t.season)
	if t.abs == 0 || c == 0 {
		return to-from > 40 // unknown: a long range is likely the whole show
	}
	first := t.abs - t.episode + 1
	return from < first || to > first+c-1
}

// absKind: an absolute number or range containing the target is a single episode (1), a
// pack of its season (2) or one spanning seasons (3).
func (t tsEpTarget) absKind(from, to int) int {
	if t.abs == 0 || t.abs < from || t.abs > to {
		return 0
	}
	switch {
	case from == to:
		return 1
	case t.spans(from, to):
		return 3
	}
	return 2
}

// absNumbers: the episode numbers of e run past the length of its season and fit the
// absolute numbers of that season ("Season 1 Episodes 01-107" of a show released as one
// season, "S01E94", "Season 2 Episodes 34-67"). A stale count (a running season) does not
// make "S05E10" absolute: season 5 starts far above 10.
func (t tsEpTarget) absNumbers(e tsEpInfo) bool {
	c := t.count(e.season)
	if c == 0 || e.epTo <= c {
		return false
	}
	first := tsAbsoluteEpisode(t.counts, e.season, 1)
	return first > 0 && e.ep >= first
}

// tsSeasonCounts lists the episode counts of TMDB's seasons (season 0, the specials, left out).
func tsSeasonCounts(seasons []tmdb.Season) []int {
	n := 0
	for _, s := range seasons {
		if s.SeasonNumber > n && s.SeasonNumber < 100 {
			n = s.SeasonNumber
		}
	}
	if n == 0 {
		return nil
	}
	counts := make([]int, n)
	for _, s := range seasons {
		if s.SeasonNumber >= 1 && s.SeasonNumber <= n {
			counts[s.SeasonNumber-1] = s.EpisodeCount
		}
	}
	return counts
}

// tsAbsoluteEpisode is the absolute number of season/episode (0 when a count is missing).
func tsAbsoluteEpisode(counts []int, season, episode int) int {
	if season < 1 || episode < 1 {
		return 0
	}
	abs := episode
	for s := 1; s < season; s++ {
		if s > len(counts) || counts[s-1] <= 0 {
			return 0
		}
		abs += counts[s-1]
	}
	return abs
}

// tsEpisodeKind classifies a release by its episode info:
// 1 = the wanted episode, 2 = a pack of its season containing it, 3 = complete series or a
// pack spanning seasons, 0 = no match.
func tsEpisodeKind(e tsEpInfo, t tsEpTarget) int {
	if !e.marked {
		return 0 // the title goes on: another show
	}
	single := func(from, to int) int {
		if from == to {
			return 1
		}
		return 2
	}
	switch {
	case e.season > 0 && e.ep > 0:
		if t.absNumbers(e) {
			if k := t.absKind(e.ep, e.epTo); k > 0 {
				return k
			}
		}
		if e.season == t.season && t.episode >= e.ep && t.episode <= e.epTo {
			return single(e.ep, e.epTo)
		}
		return 0
	case e.season > 0:
		if t.season >= e.season && t.season <= e.seasonTo {
			if e.seasonTo > e.season {
				return 3
			}
			return 2
		}
		return 0
	case e.num > 0:
		return t.absKind(e.num, e.numTo)
	case e.complete:
		return 3
	}
	return 0
}

// tsFileEpisode reads the episode info of a file in a torrent: its name (the title and a
// year after it dropped when they lead) and the season of its folders ("Season 2/...",
// "S02/...", "2. Sezon/...").
func tsFileEpisode(path string, titles [][]string) (info tsEpInfo, dirSeason int) {
	path = strings.ReplaceAll(path, "\\", "/")
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	tokens := tsNormalize(base)
	rest, matched := tokens, false
	for _, title := range titles {
		if r, ok := tsMatchTitle(tokens, title); ok {
			rest, matched = r, true
			break
		}
	}
	for len(rest) > 0 && tsYearPattern.MatchString(rest[0]) {
		rest = rest[1:]
	}
	info = tsParseEpisode(rest)
	if !matched && info.season == 0 && info.ep == 0 && info.num == 0 {
		// "[Group] Show - 05 [1080p]": the first bare number before any format tag
		for i, t := range rest {
			if i > 6 || tsReleaseTokens[t] {
				break
			}
			if _, ok := tsEpNumber(t); ok {
				info = tsParseEpisode(rest[i:])
				break
			}
		}
	}
	if dir := filepath.Dir(path); dir != "." && dir != "/" {
		for _, part := range strings.Split(dir, "/") {
			d := tsParseEpisode(tsNormalize(part))
			if d.season > 0 && d.seasonTo == d.season {
				dirSeason = d.season
			}
		}
	}
	return info, dirSeason
}

// tsPickEpisodeFile picks the file of the wanted episode among videos (largest first). It
// first looks for the episode by season and number (the file's own, its folder's, or the
// season pack's), then by its absolute number; a pack whose numbers run past the season's
// length is numbered absolutely and only matched that way. -1 when no file matches.
func tsPickEpisodeFile(videos []tsFileStat, q tsResolveQuery, c tsCandidate) int {
	t := q.epTarget()
	titles := tsTitleTokens(q)
	packSeason := 0
	if c.ep.season > 0 && c.ep.seasonTo == c.ep.season && !t.absNumbers(c.ep) {
		packSeason = c.ep.season
	}
	type parsed struct {
		info tsEpInfo
		ctx  int // season the file belongs to (0 = unknown)
	}
	files := make([]parsed, len(videos))
	maxNum := 0 // highest season-less number among the target season's files
	for i, f := range videos {
		info, dir := tsFileEpisode(f.Path, titles)
		ctx := info.season
		if ctx == 0 {
			ctx = dir
		}
		if ctx == 0 {
			ctx = packSeason
		}
		files[i] = parsed{info, ctx}
		if info.ep == 0 && (ctx == 0 || ctx == t.season) {
			maxNum = max(maxNum, info.numTo)
		}
	}
	absNumbered := t.count(t.season) > 0 && maxNum > t.count(t.season)

	for i, p := range files {
		e := p.info
		if e.season > 0 && e.ep > 0 && e.ep == e.epTo && e.season == t.season && e.ep == t.episode && !t.absNumbers(e) {
			return i
		}
		if !absNumbered && e.ep == 0 && e.num > 0 && e.num == e.numTo && p.ctx == t.season && e.num == t.episode {
			return i
		}
	}
	if t.abs == 0 {
		return -1
	}
	for i, p := range files {
		e := p.info
		// "S01E94" in a show released as one season
		if e.season > 0 && e.ep > 0 && e.ep == e.epTo && e.ep == t.abs && t.absNumbers(e) {
			return i
		}
		// The season of "Cukur.094.ser.01.sez" (a whole-show pack labelled season 1) is context only
		if e.ep == 0 && e.num > 0 && e.num == e.numTo && e.num == t.abs &&
			(p.ctx == 0 || p.ctx == t.season || t.abs > t.count(p.ctx) || t.season == 1) {
			return i
		}
	}
	return -1
}

// tsAbsoluteFromNames checks the absolute number against releases that carry both kinds
// ("Cukur 94 S4 E1"): each name of the target season votes for its own offset. The answer
// is the absolute number most names agree on with its votes (0 when none carries both).
func tsAbsoluteFromNames(raw []JackettResult, q tsResolveQuery) (int, int) {
	if q.mediaType != "tv" || q.season < 2 {
		return 0, 0
	}
	votes := map[int]int{}
	titles := tsTitleTokens(q)
	for _, r := range raw {
		tokens := tsNormalize(r.Title)
		for _, title := range titles {
			rest, ok := tsMatchTitle(tokens, title)
			if !ok {
				continue
			}
			for len(rest) > 0 && tsYearPattern.MatchString(rest[0]) {
				rest = rest[1:]
			}
			e := tsParseEpisode(rest)
			if e.lead && e.num == e.numTo && e.season == q.season && e.ep > 0 && e.ep == e.epTo && e.num >= e.ep {
				votes[e.num-e.ep+q.episode]++
			}
			break
		}
	}
	best, n := 0, 0
	for abs, v := range votes {
		if v > n || (v == n && abs < best) {
			best, n = abs, v
		}
	}
	return best, n
}

// tsApplyAbsolute sets q.absEp from TMDB's season counts, or without them from the release
// names that carry both numbers. TMDB wins a disagreement: one uploader's numbering can be
// off ("Cukur 94 S4 E1", while TMDB and the season packs "Season: 4 / Episodes: 093-131"
// start season 4 at 93), and names with both numbers match by season and episode anyway.
func tsApplyAbsolute(q *tsResolveQuery, raw []JackettResult) {
	if q.mediaType != "tv" {
		return
	}
	q.absEp = tsAbsoluteEpisode(q.seasonEps, q.season, q.episode)
	named, votes := tsAbsoluteFromNames(raw, *q)
	switch {
	case named == 0 || named == q.absEp:
	case q.absEp == 0:
		log.Printf("resolve %q: S%02dE%02d is episode %d by %d release names (no tmdb counts)", q.title, q.season, q.episode, named, votes)
		q.absEp = named
	case votes >= 2:
		log.Printf("resolve %q: %d release names count S%02dE%02d as episode %d, tmdb as %d (kept)", q.title, votes, q.season,
			q.episode, named, q.absEp)
	}
}

// seasonEpisodes returns the episode counts per season of a TMDB show (counts[0] = season
// 1, specials left out; nil when unknown), cached.
func (s *Service) seasonEpisodes(tmdbID int) []int {
	if tmdbID <= 0 || s.cfg.TMDB == nil {
		return nil
	}
	if v, ok := s.seasonEps.Load(tmdbID); ok {
		if e := v.(tsSeasonEpsEntry); time.Now().Before(e.expires) {
			return e.counts
		}
	}
	var counts []int
	ttl := tsSeasonEpsMissTTL
	if d, err := s.cfg.TMDB.GetTVDetails(tmdbID); err == nil && d != nil {
		counts = tsSeasonCounts(d.Seasons)
		if len(counts) > 0 {
			ttl = tsSeasonEpsTTL
		}
	} else if err != nil {
		log.Printf("resolve: tmdb tv %d seasons: %v", tmdbID, err)
	}
	s.seasonEps.Store(tmdbID, tsSeasonEpsEntry{counts: counts, expires: time.Now().Add(ttl)})
	return counts
}

type tsSeasonEpsEntry struct {
	counts  []int
	expires time.Time
}

// seasonEpisodesLater runs seasonEpisodes in the background; wait returns its answer, or nil
// once tsSeasonEpsWait (counted from start) passed.
func (s *Service) seasonEpisodesLater(q tsResolveQuery, start time.Time) (wait func() []int) {
	if q.mediaType != "tv" || q.tmdbID <= 0 || s.cfg.TMDB == nil {
		return func() []int { return nil }
	}
	ch := make(chan []int, 1)
	go func() { ch <- s.seasonEpisodes(q.tmdbID) }()
	return func() []int {
		select {
		case c := <-ch:
			return c
		default:
		}
		select {
		case c := <-ch:
			return c
		case <-time.After(max(0, tsSeasonEpsWait-time.Since(start))):
			log.Printf("resolve %q: tmdb season list timed out", q.title)
			return nil
		}
	}
}
