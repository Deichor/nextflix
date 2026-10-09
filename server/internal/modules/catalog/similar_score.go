package catalog

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"nextflix/internal/platform/tmdb"
)

// "More Like This" matcher, scoring half: pure functions over the source title and the
// candidates collected by similar.go. See docs/ARCHITECTURE.md "More Like This".

// TMDB genre ids the rules need (movie ids; TV ids are mapped with commonGenre).
const (
	gAnimation   = 16
	gFamily      = 10751
	gDrama       = 18
	gDocumentary = 99
	gHorror      = 27
	gCrime       = 80
	gThriller    = 53
	gTVKids      = 10762
	gTVNews      = 10763
	gTVReality   = 10764
	gTVTalk      = 10767
)

// Score weights
const (
	wList       = 1.4  // × TMDB list signal (recommendations 1.0, similar 0.45, both +0.35)
	wGenre      = 2.5  // × weighted genre Jaccard
	wKeyword    = 3.5  // × IDF-weighted keyword similarity (0-1)
	wKwProxy    = 0.5  // per keyword list a candidate came from (before its keywords are known)
	wCreator    = 1.4  // same director (movies) or creator (TV)
	wCast       = 0.35 // per shared top-billed actor in a lead role
	wEra        = 0.8  // × exp(-|years apart| / 12)
	wQuality    = 0.9  // × (Bayesian rating - simPriorMean)
	wVotes      = 0.2  // × log10(1 + votes)
	wCountry    = 0.25 // shares an origin country
	wCrossType  = 1.0  // movie for a show or the other way round
	simMainMult = 1.6  // the source's main genre counts 1.6×
	simBroadW   = 0.6  // Drama, Action, Adventure and Comedy say little on their own
	kwAvgW      = 1.5  // assumed mean weight of a candidate's keywords
	simKwMinDF  = 15   // keyword discover lists need this many titles with the keyword
	// A keyword's weight is ln(cap / (1 + titles carrying it)): "mafia" (~1.4k movies) 2.7,
	// "new york city" (~15k) 0.3, "based on novel or book" 0.
	kwCapMovie = 20000.0
	kwCapTV    = 4000.0
	// Quality: IMDb-style weighted rating towards simPriorMean with m prior votes
	simPriorMean = 6.6
	simPriorMov  = 400.0
	simPriorTV   = 200.0
	// Hard vote floors (relaxed for non-English and little-known sources)
	simFloorMovie = 150
	simFloorTV    = 80
	simNicheVotes = 2000
	simMinScore   = 3.0 // weaker candidates only fill a list shorter than simMinItems
	simMinItems   = 12
	simLimit      = 24
	simBlockMax   = 8 // collection parts shown first
	simRelatedMax = 3 // spin-offs (title contains the other and same creator or listed)
	simPerColl    = 2 // other collections
	simPerFran    = 2 // same title prefix ("Star Trek: ...")
	simPerCreator = 4
	simCrossMax   = 3
)

// Keywords that say little about a title even when they are rare enough.
var genericKeywords = map[string]bool{
	"sequel": true, "prequel": true, "remake": true, "woman director": true,
	"duringcreditsstinger": true, "aftercreditsstinger": true, "based on novel or book": true,
	"based on novel": true, "based on comic": true, "based on manga": true, "based on short story": true,
	"based on play or musical": true, "based on true story": true, "biography": true,
	"female protagonist": true, "male protagonist": true, "anime": true, "3d": true, "imax": true,
	"independent film": true, "miniseries": true, "live action remake": true, "short film": true,
	"black and white": true, "silent film": true, "pre-code": true, "tv movie": true,
}

// Original languages whose audiences mostly want more from the same language.
var strongLanguages = map[string]bool{
	"ja": true, "ko": true, "tr": true, "hi": true, "ta": true, "te": true, "ml": true, "kn": true,
	"bn": true, "zh": true, "cn": true, "th": true, "id": true, "tl": true, "fa": true, "ar": true,
	"ur": true, "pa": true,
}

// simFacts is the language-independent data of one title (genres in TMDB order, main first).
type simFacts struct {
	MediaType     string
	ID            int
	Title         string // English (fills untranslated localized text)
	OriginalTitle string
	Overview      string // English
	Genres        []int
	Keywords      []int
	Language      string
	Countries     []string
	Date          string
	Runtime       int // minutes (TV: an episode)
	Collection    int
	VoteAverage   float64
	VoteCount     int
	Popularity    float64
	Adult         bool
	Video         bool
	TVType        string // Scripted, Miniseries, Documentary, Reality, News, Talk Show, Video
	PosterPath    string
}

// simSource is the title the list is built for.
type simSource struct {
	simFacts
	Creators     []int // movie directors or TV creators
	Cast         []int // top billed
	KeywordNames map[int]string

	kwWeight map[int]float64 // IDF weight per keyword (generic ones 0)
	kwDF     map[int]int     // titles carrying the keyword
	kwTotal  float64
	floor    int     // vote floor
	prior    float64 // m of the weighted rating
}

// simCand is one candidate with the signals that brought it in.
type simCand struct {
	res        tmdb.SearchResult // display data in the request language
	facts      *simFacts         // nil until fetched (stage 2)
	recPos     int               // position in TMDB recommendations, -1 = not listed
	simPos     int               // position in TMDB similar, -1 = not listed
	kwLists    int               // keyword discover lists it came from
	byCreator  bool              // the source's director / creator made it
	castHits   int               // top-billed actors of the source in a lead role
	collection bool              // part of the source's collection
	related    bool              // spin-off block
	parts      simParts
	score      float64
}

func newSimCand(r tmdb.SearchResult) *simCand { return &simCand{res: r, recPos: -1, simPos: -1} }

// view returns the facts of the candidate, or what its list entry tells.
func (c *simCand) view() *simFacts {
	if c.facts != nil {
		return c.facts
	}
	return &simFacts{MediaType: c.res.MediaType, ID: c.res.ID, Genres: c.res.GenreIDs,
		Language: c.res.OriginalLanguage, Date: c.res.ReleaseDate, VoteAverage: c.res.VoteAverage,
		VoteCount: c.res.VoteCount, Popularity: c.res.Popularity, Adult: c.res.Adult, PosterPath: c.res.PosterPath}
}

// prepare sets the keyword weights and the quality constants of the source. df returns how
// many titles of the source's media type carry a keyword (-1 when unknown).
func (s *simSource) prepare(df func(kw int) int) {
	kwCap := kwCapMovie
	s.floor, s.prior = simFloorMovie, simPriorMov
	if s.MediaType == "tv" {
		kwCap = kwCapTV
		s.floor, s.prior = simFloorTV, simPriorTV
	}
	// Non-English and little-known sources get little-known matches: a lower floor and a
	// weaker pull of the rating towards the mean
	niche := s.VoteCount < simNicheVotes
	if s.Language != "en" || niche {
		s.floor /= 3
		s.prior /= 2
	}
	if niche {
		s.prior /= 2
	}
	s.floor = min(s.floor, max(15, s.VoteCount/4))

	s.kwWeight, s.kwDF, s.kwTotal = map[int]float64{}, map[int]int{}, 0
	for _, k := range s.Keywords {
		if genericKeywords[strings.ToLower(s.KeywordNames[k])] {
			continue
		}
		n := df(k)
		if n < 0 {
			continue
		}
		if w := math.Log(kwCap / float64(1+n)); w > 0 {
			s.kwWeight[k], s.kwDF[k] = w, n
			s.kwTotal += w
		}
	}
}

// topKeywords returns up to n keywords by weight (at least minW) that enough titles carry to
// make a discover list worth asking for.
func (s *simSource) topKeywords(n int, minW float64) []int {
	var out []int
	for k, w := range s.kwWeight {
		if w >= minW && s.kwDF[k] >= simKwMinDF {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if s.kwWeight[out[i]] != s.kwWeight[out[j]] {
			return s.kwWeight[out[i]] > s.kwWeight[out[j]]
		}
		return out[i] < out[j]
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Genres too broad to define a title when it has a more specific one
var broadGenres = map[int]bool{gDrama: true, 28: true, 12: true, 35: true}

// mainGenreRaw is the source's main genre as a TMDB id of its media type: the first genre, or
// the first specific one when the first is broad (Interstellar: Adventure, Drama, Sci-Fi ->
// Sci-Fi; Parasite: Comedy, Thriller, Drama -> Thriller). 0 without genres.
func (s *simSource) mainGenreRaw() int {
	if len(s.Genres) == 0 {
		return 0
	}
	for _, g := range s.Genres {
		if !broadGenres[commonGenre(s.MediaType, g)] {
			return g
		}
	}
	return s.Genres[0]
}

// mainGenre is mainGenreRaw in movie terms.
func (s *simSource) mainGenre() int { return commonGenre(s.MediaType, s.mainGenreRaw()) }

func genreSet(mediaType string, ids []int) map[int]bool {
	m := make(map[int]bool, len(ids))
	for _, g := range ids {
		m[commonGenre(mediaType, g)] = true
	}
	return m
}

func factsGenre(f *simFacts, g int) bool {
	for _, x := range f.Genres {
		if commonGenre(f.MediaType, x) == g || x == g {
			return true
		}
	}
	return false
}

// genreJaccard is the weighted Jaccard index of the two genre sets (TV genres in movie terms):
// the source's main genre weighs 1.6×, broad genres 0.6.
func genreJaccard(src *simSource, f *simFacts) float64 {
	a, b := genreSet(src.MediaType, src.Genres), genreSet(f.MediaType, f.Genres)
	main := src.mainGenre()
	weight := func(g int) float64 {
		w := 1.0
		if broadGenres[g] {
			w = simBroadW
		}
		if g == main {
			w *= simMainMult
		}
		return w
	}
	inter, union := 0.0, 0.0
	for g := range a {
		union += weight(g)
		if b[g] {
			inter += weight(g)
		}
	}
	for g := range b {
		if !a[g] {
			union += weight(g)
		}
	}
	if union == 0 {
		return 0
	}
	return inter / union
}

// keywordSim is a cosine-like similarity of the keyword sets (0-1): the IDF weight of the
// shared keywords over the source's total weight and the candidate's keyword count (a title
// tagged with 60 keywords does not match everything). Identical sets give 1.
func keywordSim(src *simSource, kws []int) float64 {
	if src.kwTotal == 0 || len(kws) == 0 {
		return 0
	}
	shared := 0.0
	for _, k := range kws {
		shared += src.kwWeight[k]
	}
	return math.Min(1, 2*shared/math.Sqrt(src.kwTotal*kwAvgW*float64(max(len(kws), 8))))
}

func listSignal(c *simCand) float64 {
	s := 0.0
	if c.recPos >= 0 {
		s += 1 / (1 + 0.05*float64(c.recPos))
	}
	if c.simPos >= 0 {
		s += 0.45 / (1 + 0.05*float64(c.simPos))
	}
	if c.recPos >= 0 && c.simPos >= 0 {
		s += 0.35
	}
	return s
}

func languageTerm(src *simSource, f *simFacts) float64 {
	switch {
	case f.Language == src.Language && strongLanguages[src.Language]:
		return 1.5
	case f.Language == src.Language && src.Language != "en":
		return 0.7
	case f.Language == src.Language:
		return 0.3
	case strongLanguages[src.Language]:
		return -1.0
	case src.Language != "en":
		return -0.2
	}
	return -0.3
}

func yearOf(date string) int { return tmdb.YearOf(date) }

func eraTerm(src *simSource, f *simFacts) float64 {
	a, b := yearOf(src.Date), yearOf(f.Date)
	if a == 0 || b == 0 {
		return 0.3 * wEra
	}
	return wEra * math.Exp(-math.Abs(float64(a-b))/12)
}

func qualityTerm(src *simSource, f *simFacts) float64 {
	v := float64(max(f.VoteCount, 0))
	wr := v/(v+src.prior)*f.VoteAverage + src.prior/(v+src.prior)*simPriorMean
	return wQuality*(wr-simPriorMean) + wVotes*math.Log10(1+v)
}

func isKids(f *simFacts) bool { return factsGenre(f, gFamily) || factsGenre(f, gTVKids) }

// audienceTerm keeps animation and live action, and kids' and adult titles, apart unless the
// source mixes them. drop: never show the candidate.
func audienceTerm(src *simSource, f *simFacts) (pen float64, drop bool) {
	sAnim, cAnim := factsGenre(&src.simFacts, gAnimation), factsGenre(f, gAnimation)
	sKids, cKids := isKids(&src.simFacts), isKids(f)
	sAnime, cAnime := sAnim && src.Language == "ja", cAnim && f.Language == "ja"
	mature := factsGenre(f, gHorror) || factsGenre(f, gCrime) || factsGenre(f, gThriller)
	switch {
	case !sAnim && cAnim:
		if cKids {
			return 0, true
		}
		pen -= 2.2
	case sAnim && !cAnim:
		switch {
		case sAnime:
			pen -= 1.8
		case sKids && !cKids:
			pen -= 2.2
		case sKids:
			pen -= 0.8
		default:
			pen -= 0.8
		}
	case sAnim && cAnim:
		if sAnime != cAnime {
			pen -= 0.6
		}
		if sKids && !cKids && !sAnime {
			pen -= 0.4
		}
	}
	switch {
	case sKids && !cKids && mature:
		if !sAnim && !cAnim {
			pen -= 1.5
		} else {
			return 0, true
		}
	case !sKids && cKids && !sAnim:
		pen -= 1.0
	case sAnime && cKids && !sKids:
		pen -= 0.6
	}
	return pen, false
}

// excluded drops what never belongs in the list: adult titles, videos, making-ofs, titles
// without a poster or genres or not released yet, documentaries, news / talk / reality shows
// and shorts (unless the source is one), and titles below the vote floor (collection parts
// and the creator's works need a third of it).
func excluded(src *simSource, c *simCand, f *simFacts, today string) bool {
	if f.Adult || f.Video || f.PosterPath == "" || f.Date == "" || f.Date > today || len(f.Genres) == 0 {
		return true
	}
	if f.MediaType == src.MediaType && f.ID == src.ID {
		return true
	}
	if isExtra(normalize(c.res.Title)) || isExtra(normalize(c.res.OriginalTitle)) || isExtra(normalize(f.Title)) {
		return true
	}
	for _, g := range []int{gDocumentary, gTVNews, gTVTalk, gTVReality} {
		if factsGenre(f, g) && !factsGenre(&src.simFacts, g) {
			return true
		}
	}
	switch f.TVType {
	case "Documentary", "News", "Talk Show", "Reality", "Video":
		if f.TVType != src.TVType {
			return true
		}
	}
	if f.MediaType == "movie" && f.Runtime > 0 && f.Runtime < 45 && !(src.MediaType == "movie" && src.Runtime > 0 && src.Runtime < 45) {
		return true
	}
	floor := src.floor
	if c.collection || c.byCreator {
		floor /= 3
	}
	return f.VoteCount < floor
}

// simParts are the score terms of one candidate (debug output and tests).
type simParts struct {
	List, Genre, Keyword, Lang, Era, Quality, Audience, People, Cross, Country float64
}

func (p simParts) sum() float64 {
	return p.List + p.Genre + p.Keyword + p.Lang + p.Era + p.Quality + p.Audience + p.People + p.Cross + p.Country
}

func (p simParts) String() string {
	return fmt.Sprintf("list %.2f genre %.2f kw %.2f lang %.2f era %.2f qual %.2f aud %.2f people %.2f cross %.2f country %.2f",
		p.List, p.Genre, p.Keyword, p.Lang, p.Era, p.Quality, p.Audience, p.People, p.Cross, p.Country)
}

// scoreCand scores one candidate; ok = false drops it.
func scoreCand(src *simSource, c *simCand, today string) (simParts, bool) {
	var p simParts
	f := c.view()
	if excluded(src, c, f, today) {
		return p, false
	}
	aud, drop := audienceTerm(src, f)
	if drop && !c.collection {
		return p, false
	}
	j := genreJaccard(src, f)
	kw := 0.0
	if c.facts != nil {
		kw = keywordSim(src, f.Keywords)
	}
	// Nothing in common at all
	if j == 0 && kw < 0.2 && !c.collection && !c.related && !c.byCreator {
		return p, false
	}
	p.List = wList * listSignal(c)
	p.Genre = wGenre * j
	p.Lang = languageTerm(src, f)
	p.Era = eraTerm(src, f)
	p.Quality = qualityTerm(src, f)
	p.Audience = aud
	if c.facts != nil {
		p.Keyword = wKeyword * kw
		if shareCountry(src.Countries, f.Countries) {
			p.Country = wCountry
		}
	} else {
		p.Keyword = math.Min(2, float64(c.kwLists)) * wKwProxy
	}
	if c.byCreator {
		p.People += wCreator
		if j == 0 {
			p.People -= wCreator / 2 // the director's film in another genre
		}
	}
	p.People += math.Min(2, float64(c.castHits)) * wCast
	if f.MediaType != src.MediaType {
		// Only a strong link carries a title of the other type
		if !c.byCreator && !c.related && c.castHits < 2 && kw < 0.4 {
			return p, false
		}
		p.Cross = -wCrossType
	}
	return p, true
}

func shareCountry(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// franchiseKey is the normalized title part before a colon ("Star Trek: Voyager" -> "star trek").
func franchiseKey(title string) string {
	i := strings.IndexAny(title, ":")
	if i <= 0 {
		return ""
	}
	k := normalize(title[:i])
	if len([]rune(k)) < 4 {
		return ""
	}
	return k
}

// spinOff reports whether one title contains the other as whole words ("El Camino: A Breaking
// Bad Movie" and "Breaking Bad"). The shorter one needs two words or ten letters, so a
// one-word title like "Thriller" does not claim "Fine Thriller".
func spinOff(a, b string) bool {
	na, nb := normalize(a), normalize(b)
	if len(na) > len(nb) {
		na, nb = nb, na
	}
	if na == nb || na == "" || !strings.Contains(na, " ") && len(na) < 10 {
		return false
	}
	return strings.Contains(" "+nb+" ", " "+na+" ")
}

func simToday() string { return time.Now().UTC().Format("2006-01-02") }

// rankSimilar scores the candidates and builds the list: the collection (release order) and
// spin-offs first, then the best scores with per-collection, franchise, creator and cross-type
// caps.
func rankSimilar(src *simSource, cands []*simCand, today string, limit int) []*simCand {
	var block, related, rest []*simCand
	for _, c := range cands {
		p, ok := scoreCand(src, c, today)
		if !ok {
			continue
		}
		c.parts, c.score = p, p.sum()
		switch {
		case c.collection:
			block = append(block, c)
		case c.related:
			related = append(related, c)
		default:
			rest = append(rest, c)
		}
	}
	byDate := func(l []*simCand) {
		sort.SliceStable(l, func(i, j int) bool {
			di, dj := l[i].view().Date, l[j].view().Date
			if di != dj {
				return di < dj
			}
			return l[i].res.ID < l[j].res.ID
		})
	}
	byDate(block)
	if len(block) > simBlockMax {
		block = block[:simBlockMax]
	}
	sort.SliceStable(related, func(i, j int) bool { return related[i].score > related[j].score })
	if len(related) > simRelatedMax {
		rest = append(rest, related[simRelatedMax:]...)
		related = related[:simRelatedMax]
	}
	byDate(related)
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].score != rest[j].score {
			return rest[i].score > rest[j].score
		}
		return rest[i].res.ID < rest[j].res.ID
	})

	out := append(append([]*simCand{}, block...), related...)
	colls, fran := map[int]int{}, map[string]int{}
	creator, cross := 0, 0
	for _, c := range out {
		if k := franchiseKey(c.res.Title); k != "" {
			fran[k]++
		}
		if c.res.MediaType != src.MediaType {
			cross++
		}
	}
	for _, c := range rest {
		if len(out) >= limit {
			break
		}
		if c.score < simMinScore && len(out) >= simMinItems {
			break
		}
		f := c.view()
		if f.Collection != 0 && (f.Collection == src.Collection || colls[f.Collection] >= simPerColl) {
			continue
		}
		k := franchiseKey(c.res.Title)
		if k != "" && fran[k] >= simPerFran {
			continue
		}
		if c.byCreator && creator >= simPerCreator {
			continue
		}
		if c.res.MediaType != src.MediaType && cross >= simCrossMax {
			continue
		}
		if f.Collection != 0 {
			colls[f.Collection]++
		}
		if k != "" {
			fran[k]++
		}
		if c.byCreator {
			creator++
		}
		if c.res.MediaType != src.MediaType {
			cross++
		}
		out = append(out, c)
	}
	return out
}
