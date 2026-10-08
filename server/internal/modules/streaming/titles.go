package streaming

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"nextflix/internal/platform/tmdb"
)

// TMDB's English title of a foreign show ("Money Trap") is rarely what its releases are
// called; they mostly carry the original title in ASCII ("Organize.Isler.S01E01"). Resolve
// also searches for and accepts the original title, its transliteration and a couple of
// alternative titles.

const (
	tsTitleNamesWait = 2500 * time.Millisecond // a slower TMDB lookup searches the title alone
	tsExtraNamesMax  = 4                       // extra names searched besides the title
	tsAltTitlesMax   = 2
	tsFewCandidates  = 5 // English originals: fewer usable results than this search the alternative titles too
)

// tsAltTitleSource is the optional part of the TMDB client that lists alternative titles.
type tsAltTitleSource interface {
	GetAlternativeTitles(mediaType string, id int) ([]tmdb.AlternativeTitle, error)
}

var tsFoldReplacer = strings.NewReplacer(
	"ş", "s", "Ş", "S", "ğ", "g", "Ğ", "G", "ı", "i", "İ", "I", "ç", "c", "Ç", "C",
	"ö", "o", "Ö", "O", "ü", "u", "Ü", "U",
	"ß", "ss", "æ", "ae", "Æ", "AE", "œ", "oe", "Œ", "OE", "ø", "o", "Ø", "O",
	"đ", "d", "Đ", "D", "ł", "l", "Ł", "L", "þ", "th", "Þ", "Th", "ð", "d", "Ð", "D",
)

// tsFold transliterates a title to ASCII letters where it can: Turkish letters and a few
// others by table, every other accent by dropping the combining mark ("Amélie" -> "Amelie").
// Case, punctuation and letters of other scripts are kept.
func tsFold(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	s = tsFoldReplacer.Replace(s)
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// tsLatinName reports whether a name can appear in a release name: mostly Latin letters
// after folding and at least 3 letters or digits.
func tsLatinName(name string) bool {
	letters, latin := 0, 0
	for _, r := range tsFold(name) {
		if unicode.IsLetter(r) {
			letters++
			if r < 0x80 {
				latin++
			}
		}
	}
	return latin*5 >= letters*4 && len(strings.Join(tsNormalize(name), "")) >= 3
}

// tsTitleNames are other names of a title.
type tsTitleNames struct {
	names   []string // accepted in release names like the title
	queries []string // of those, the ones searched for
	eager   bool     // searched together with the title (not an English original)
	imdbID  string   // IMDb id of the title ("" = unknown)
	abs     int      // TV: absolute number of the episode (0 = unknown), searched as "<name> <abs>"
	lang    string   // original language (ISO 639-1, "" = unknown)
}

// tsBuildNames picks the extra names: for a non-English original the original title, its
// ASCII form and (TV) the part before a colon, then IMDb's English title, then up to 2
// alternative titles (US, GB, TR first). A name whose tokens start with an already searched
// name is accepted but not searched: keyword search finds it with the shorter name.
func tsBuildNames(mediaType, title, original, lang, imdbTitle string, alts []tmdb.AlternativeTitle) tsTitleNames {
	n := tsTitleNames{eager: lang != "" && lang != "en", lang: strings.ToLower(lang)}
	matchSeen := map[string]bool{strings.Join(tsNormalize(title), " "): true}
	searched := [][]string{tsNormalize(title)}
	querySeen := map[string]bool{strings.ToLower(title): true}

	addQuery := func(name string) {
		if len(n.queries) >= tsExtraNamesMax || querySeen[strings.ToLower(name)] {
			return
		}
		tokens := tsNormalize(name)
		for _, s := range searched {
			if len(tokens) > len(s) && tsHasPrefix(tokens, s) {
				return
			}
		}
		querySeen[strings.ToLower(name)] = true
		n.queries = append(n.queries, name)
		searched = append(searched, tokens)
	}
	// add accepts a name; withRaw also searches its non-ASCII spelling
	add := func(name string, withRaw bool) bool {
		name = strings.TrimSpace(name)
		if name == "" || !tsLatinName(name) {
			return false
		}
		key := strings.Join(tsNormalize(name), " ")
		if matchSeen[key] {
			return false
		}
		matchSeen[key] = true
		n.names = append(n.names, name)
		folded := tsFold(name)
		if withRaw && folded != name {
			addQuery(name)
		}
		addQuery(folded)
		return true
	}

	if original != "" && !strings.EqualFold(original, title) {
		add(original, n.eager)
		if mediaType == "tv" {
			if head := tsMainTitle(original); head != original {
				add(head, false)
			}
		}
	}
	if imdbTitle != "" && !strings.EqualFold(imdbTitle, title) {
		add(imdbTitle, false)
	}

	rank := map[string]int{"US": 0, "GB": 1, "TR": 2}
	sorted := append([]tmdb.AlternativeTitle(nil), alts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, ok := rank[sorted[i].Country]
		if !ok {
			ri = len(rank)
		}
		rj, ok := rank[sorted[j].Country]
		if !ok {
			rj = len(rank)
		}
		return ri < rj
	})
	taken := 0
	for _, a := range sorted {
		if taken >= tsAltTitlesMax {
			break
		}
		if add(a.Title, false) {
			taken++
		}
	}
	return n
}

// titleNames looks up the original title, language and alternative titles of q's TMDB id.
// Nothing comes back without a tmdb_id or when TMDB is slower than tsTitleNamesWait.
func (s *Service) titleNames(q tsResolveQuery) tsTitleNames {
	if q.tmdbID <= 0 || s.cfg.TMDB == nil {
		return tsTitleNames{}
	}
	start := time.Now()
	ch := make(chan tsTitleNames, 1)
	go func() { ch <- s.lookupTitleNames(q) }()
	select {
	case n := <-ch:
		if len(n.names) > 0 || n.imdbID != "" {
			log.Printf("resolve %q: other names %q, searching %q (eager %v, imdb %q) in %s", q.title, n.names, n.queries, n.eager,
				n.imdbID, time.Since(start).Round(time.Millisecond))
		}
		return n
	case <-time.After(tsTitleNamesWait):
		log.Printf("resolve %q: original title lookup timed out", q.title)
		return tsTitleNames{}
	}
}

// tsNamesLater is a titleNames lookup running next to the search.
type tsNamesLater struct {
	done chan struct{} // closed once n is set, at most tsTitleNamesWait after the start
	n    tsTitleNames
	took time.Duration
}

func (s *Service) titleNamesLater(q tsResolveQuery) *tsNamesLater {
	l := &tsNamesLater{done: make(chan struct{})}
	go func() {
		start := time.Now()
		l.n = s.titleNames(q)
		l.took = time.Since(start)
		close(l.done)
	}()
	return l
}

// get returns the names once the lookup answered.
func (l *tsNamesLater) get() (tsTitleNames, bool) {
	select {
	case <-l.done:
		return l.n, true
	default:
		return tsTitleNames{}, false
	}
}

func (s *Service) lookupTitleNames(q tsResolveQuery) tsTitleNames {
	// IMDb runs next to the TMDB lookups; a slow answer is left out (and cached for next time)
	start := time.Now()
	imdbIDCh, imdbTitleCh := make(chan string, 1), make(chan string, 1)
	go func() {
		id := s.imdbID(q)
		imdbIDCh <- id
		title := ""
		if id != "" {
			title, _ = s.imdbTitle(id)
		}
		imdbTitleCh <- title
	}()

	var original, lang string
	if q.mediaType == "movie" {
		d, err := s.cfg.TMDB.GetMovieDetails(q.tmdbID)
		if err != nil || d == nil {
			return tsTitleNames{}
		}
		original, lang = d.OriginalTitle, d.OriginalLanguage
	} else {
		d, err := s.cfg.TMDB.GetTVDetails(q.tmdbID)
		if err != nil || d == nil {
			return tsTitleNames{}
		}
		original, lang = d.OriginalName, d.OriginalLanguage
	}
	var alts []tmdb.AlternativeTitle
	if src, ok := s.cfg.TMDB.(tsAltTitleSource); ok {
		if a, err := src.GetAlternativeTitles(q.mediaType, q.tmdbID); err == nil {
			alts = a
		} else {
			log.Printf("resolve %q: tmdb alternative titles: %v", q.title, err)
		}
	}
	var imdbID, imdbTitle string
	timeout := time.After(tsIMDbNamesWait - time.Since(start))
	select {
	case imdbTitle = <-imdbTitleCh:
		imdbID = <-imdbIDCh
	case <-timeout:
		select {
		case imdbID = <-imdbIDCh:
		default:
		}
		log.Printf("resolve %q: imdb lookup timed out (id %q)", q.title, imdbID)
	}
	n := tsBuildNames(q.mediaType, q.title, original, lang, imdbTitle, alts)
	n.imdbID = imdbID
	if q.mediaType == "tv" && n.eager {
		n.abs = tsAbsoluteEpisode(s.seasonEpisodes(q.tmdbID), q.season, q.episode)
	}
	return n
}

// tsTitleTokens returns the normalized title and other names of q, longest first, so the
// most specific name wins when one starts with another.
func tsTitleTokens(q tsResolveQuery) [][]string {
	out := [][]string{tsNormalize(q.title)}
	seen := map[string]bool{strings.Join(out[0], " "): true}
	for _, name := range q.names {
		tokens := tsNormalize(name)
		key := strings.Join(tokens, " ")
		if len(tokens) == 0 || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tokens)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func tsHasPrefix(tokens, prefix []string) bool {
	if len(prefix) == 0 || len(tokens) < len(prefix) {
		return false
	}
	for i, t := range prefix {
		if tokens[i] != t {
			return false
		}
	}
	return true
}

// tsMainTitle is a title without its subtitle ("Organize İşler: Karun Hazinesi" ->
// "Organize İşler"); hyphens inside words ("Spider-Man") are kept.
func tsMainTitle(title string) string {
	cut := len(title)
	for _, sep := range []string{":", " - ", " – ", " ("} {
		if i := strings.Index(title, sep); i > 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(title[:cut])
}

// englishTitle replaces the title of a query that names a tmdb_id with TMDB's English
// title. The web shows titles in the profile language ("Başlangıç"), but releases are
// named in English, and resolve, prefetch, upgrade and release must agree on the keys.
// The client's title stays when TMDB is unavailable or slower than tsTitleNamesWait.
func (s *Service) englishTitle(q *tsResolveQuery) {
	if q.tmdbID <= 0 || s.cfg.TMDB == nil {
		return
	}
	key := fmt.Sprintf("%s:%d", q.mediaType, q.tmdbID)
	if v, ok := s.titles.Load(key); ok {
		q.title = v.(string)
		return
	}
	ch := make(chan string, 1)
	go func() {
		title := ""
		if q.mediaType == "movie" {
			if d, err := s.cfg.TMDB.GetMovieDetails(q.tmdbID); err == nil && d != nil {
				title = d.Title
			}
		} else if d, err := s.cfg.TMDB.GetTVDetails(q.tmdbID); err == nil && d != nil {
			title = d.Name
		}
		ch <- strings.TrimSpace(title)
	}()
	select {
	case title := <-ch:
		if len(tsNormalize(title)) == 0 {
			return
		}
		s.titles.Store(key, title)
		if title != q.title {
			log.Printf("resolve: using English title %q for %q (tmdb %s)", title, q.title, key)
		}
		q.title = title
	case <-time.After(tsTitleNamesWait):
		log.Printf("resolve %q: English title lookup timed out", q.title)
	}
}
