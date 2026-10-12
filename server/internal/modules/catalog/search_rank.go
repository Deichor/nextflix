package catalog

import (
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
)

// Search ranking: TMDB's multi search finds the candidates, our own score orders them, and the
// typo index corrects queries TMDB finds nothing good for.

const (
	textWeight    = 3.0
	popWeight     = 0.4  // × log10(1 + popularity)
	voteWeight    = 0.3  // × log10(1 + vote_count)
	tmdbPosWeight = 0.3  // TMDB's own order (it also matches alternative titles)
	recentBoost   = 0.15 // released this or last year and popular
	tasteWeight   = 0.35 // × mean genre affinity of the profile in [-1, 1]

	// Below this best text score the query is treated as misspelled (no prefix match or better)
	correctBelow = matchPrefixWord
	correctMin   = 0.75 // index similarity of a correction
	// A title with fewer votes and less popularity than this is obscure: a correction that is
	// 10 times as popular is still offered
	minKnownVotes = 50
	minKnownPop   = 5.0
	// Search pages read per query (20 results each); a filtered search reads more
	rankPages     = 3
	filteredPages = 5
)

var (
	// Normalized title phrases of making-ofs, specials and podcasts
	extraPhrases = []string{"making of", "behind the scenes", "the making", "special look", "featurette",
		"official podcast", "kamera arkasi", "a look inside", "disney gallery", "first look", "sneak peek"}
	// Query words that ask for such titles
	extraWords = []string{"making", "behind", "documentary", "belgesel", "special", "podcast", "gallery",
		"featurette", "look", "peek"}
)

const (
	genreDocumentary = 99
	genreNews        = 10763
	genreTalk        = 10767
)

func asksForExtras(q query) bool {
	for _, w := range q.words {
		for _, x := range extraWords {
			if w == x {
				return true
			}
		}
	}
	return false
}

func isExtra(title string) bool {
	for _, p := range extraPhrases {
		if strings.Contains(title, p) {
			return true
		}
	}
	return false
}

func hasGenre(r tmdb.SearchResult, ids ...int) bool {
	for _, g := range r.GenreIDs {
		for _, id := range ids {
			if g == id {
				return true
			}
		}
	}
	return false
}

// rankInput is everything a score depends on besides the result.
type rankInput struct {
	q       query
	extras  bool // the query asks for documentaries / making-ofs
	taste   map[int]float64
	aliases func(mediaType string, id int) []string
	now     time.Time
}

func newRankInput(q query, taste map[int]float64, aliases func(string, int) []string) rankInput {
	return rankInput{q: q, extras: asksForExtras(q), taste: taste, aliases: aliases, now: time.Now()}
}

// textMatch is the best text score of r's title, original title and known localized titles.
func (in rankInput) textMatch(r tmdb.SearchResult) float64 {
	best := textScore(in.q, normalize(r.Title))
	if r.OriginalTitle != r.Title {
		best = max(best, textScore(in.q, normalize(r.OriginalTitle)))
	}
	if in.aliases != nil {
		for _, a := range in.aliases(r.MediaType, r.ID) {
			best = max(best, textScore(in.q, normalize(a)))
		}
	}
	return best
}

// score blends text match, popularity, votes, TMDB's order, recency and taste, minus
// penalties for barely rated titles, documentaries / making-ofs / talk shows the query does
// not ask for and a missing poster. pos/total is the place in TMDB's answer.
func (in rankInput) score(r tmdb.SearchResult, text float64, pos, total int) float64 {
	s := textWeight*text +
		popWeight*math.Log10(1+max(r.Popularity, 0)) +
		voteWeight*math.Log10(1+float64(max(r.VoteCount, 0)))
	if total > 0 {
		s += tmdbPosWeight * (1 - float64(pos)/float64(total))
	}
	release, _ := time.Parse("2006-01-02", r.ReleaseDate)
	upcoming := !release.IsZero() && release.After(in.now.AddDate(0, 0, -60))
	if !release.IsZero() && release.Year() >= in.now.Year()-1 && r.Popularity >= 20 {
		s += recentBoost
	}
	switch {
	case upcoming:
	case r.VoteCount < 5:
		s -= 0.6
	case r.VoteCount < 30:
		s -= 0.25
	}
	if !in.extras {
		if hasGenre(r, genreDocumentary) {
			s -= 0.5
		}
		if isExtra(normalize(r.Title)) || isExtra(normalize(r.OriginalTitle)) {
			s -= 0.7
		}
	}
	if hasGenre(r, genreTalk, genreNews) {
		s -= 0.4
	}
	if r.PosterPath == "" {
		s -= 0.4
	}
	if len(in.taste) > 0 && len(r.GenreIDs) > 0 {
		sum := 0.0
		for _, g := range r.GenreIDs {
			sum += in.taste[commonGenre(r.MediaType, g)]
		}
		s += tasteWeight * sum / float64(len(r.GenreIDs))
	}
	return s
}

type rankedResult struct {
	r     tmdb.SearchResult
	text  float64
	score float64
}

// rankPool scores a TMDB answer for q.
func rankPool(in rankInput, pool []tmdb.SearchResult) []rankedResult {
	out := make([]rankedResult, 0, len(pool))
	for i, r := range pool {
		text := in.textMatch(r)
		out = append(out, rankedResult{r: r, text: text, score: in.score(r, text, i, len(pool))})
	}
	return out
}

func resultKey(mediaType string, id int) string { return mediaType + ":" + strconv.Itoa(id) }

// RankedSearch searches query, ranks the results with our score and keeps those that pass f.
// When TMDB finds nothing that matches well and the typo index knows a close title, the
// corrected query is searched too, merged in, and named in DidYouMean.
func (s *Service) RankedSearch(tm *tmdb.Client, raw string, f Filter, profileID uint) (*DiscoverPage, error) {
	q := newQuery(clampQuery(raw))
	pages := rankPages
	if !f.Empty() {
		pages = filteredPages
	}

	// The index answers in microseconds: look for a correction before TMDB answers and, when a
	// query word is unknown, search the correction at the same time
	var correction *Hit
	for _, h := range s.index.Correct(q.raw, 3, correctMin) {
		if hb := stripArticle(normalize(h.Title)); hb != q.bare {
			correction = &h
		}
		break
	}
	type poolAnswer struct {
		pool searchEntry
		err  error
	}
	var corrCh chan poolAnswer
	startCorrection := func() {
		if corrCh != nil || correction == nil {
			return
		}
		corrCh = make(chan poolAnswer, 1)
		go func(text string) {
			p, err := s.searchPool(tm, text, pages)
			corrCh <- poolAnswer{p, err}
		}(correction.Title)
	}
	if correction != nil && !s.index.KnownWords(q.raw) {
		startCorrection()
	}
	taste := s.taste.affinity(profileID)

	pool, err := s.searchPool(tm, q.raw, pages)
	in := newRankInput(q, taste, s.aliasesWith(pool.en))
	ranked := rankPool(in, pool.results)
	// A match only counts when the title has some audience: an obscure film literally named
	// "Interstelar" must not hide Interstellar
	best := 0.0
	for _, r := range ranked {
		if r.r.VoteCount >= minKnownVotes || r.r.Popularity >= minKnownPop || correction == nil || correction.Pop < r.r.Popularity*10 {
			best = max(best, r.text)
		}
	}

	didYouMean := ""
	if correction != nil && best < correctBelow {
		startCorrection()
		ans := <-corrCh
		if ans.err != nil {
			log.Printf("catalog: search correction %q: %v", correction.Title, ans.err)
		} else {
			cin := newRankInput(newQuery(correction.Title), taste, s.aliasesWith(ans.pool.en))
			corr := rankPool(cin, ans.pool.results)
			corr = append(corr, s.indexExtras(tm, cin, corr, correction)...)
			for _, r := range corr {
				if r.text >= matchPrefixWord {
					didYouMean = correction.Title
					break
				}
			}
			ranked = mergeRanked(ranked, corr)
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}

	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := []tmdb.SearchResult{}
	for _, r := range ranked {
		if f.Match(r.r) {
			out = append(out, r.r)
		}
	}
	return &DiscoverPage{Page: 1, TotalPages: 1, TotalResults: len(out), Results: out, DidYouMean: didYouMean}, nil
}

// mergeRanked joins two ranked lists; a title in both keeps its better score.
func mergeRanked(a, b []rankedResult) []rankedResult {
	at := map[string]int{}
	for i, r := range a {
		at[resultKey(r.r.MediaType, r.r.ID)] = i
	}
	for _, r := range b {
		if i, ok := at[resultKey(r.r.MediaType, r.r.ID)]; ok {
			if r.score > a[i].score {
				a[i] = r
			}
			continue
		}
		at[resultKey(r.r.MediaType, r.r.ID)] = len(a)
		a = append(a, r)
	}
	return a
}

// indexExtras adds the corrected title itself when the TMDB search of the correction missed
// it (details from the shared 24 h cache, 1.5 s at most).
func (s *Service) indexExtras(tm *tmdb.Client, in rankInput, have []rankedResult, h *Hit) []rankedResult {
	for _, r := range have {
		if r.r.MediaType == h.MediaType && r.r.ID == h.ID {
			return nil
		}
	}
	ch := make(chan *tmdb.SearchResult, 1)
	go func() { ch <- titleResult(tm, h.MediaType, h.ID) }()
	select {
	case r := <-ch:
		if r == nil || !usable(*r) {
			return nil
		}
		text := in.textMatch(*r)
		return []rankedResult{{r: *r, text: text, score: in.score(*r, text, 0, 1)}}
	case <-time.After(1500 * time.Millisecond):
		return nil
	}
}

// titleResult returns a title's details in the search result shape, nil on errors.
func titleResult(tm *tmdb.Client, mediaType string, id int) *tmdb.SearchResult {
	genreIDs := func(gs []tmdb.Genre) []int {
		ids := make([]int, 0, len(gs))
		for _, g := range gs {
			ids = append(ids, g.ID)
		}
		return ids
	}
	if mediaType == "tv" {
		d, err := tm.GetTVDetails(id)
		if err != nil {
			return nil
		}
		r := tmdb.TVResult(d.TV)
		r.GenreIDs = genreIDs(d.Genres)
		return &r
	}
	d, err := tm.GetMovieDetails(id)
	if err != nil {
		return nil
	}
	r := tmdb.MovieResult(d.Movie)
	r.GenreIDs = genreIDs(d.Genres)
	return &r
}

// aliasesWith returns the learned localized titles of a title plus its English title in en.
func (s *Service) aliasesWith(en map[string]string) func(mediaType string, id int) []string {
	return func(mediaType string, id int) []string {
		a := s.index.aliasesOf(mediaType, id)
		if t, ok := en[resultKey(mediaType, id)]; ok {
			a = append(a, t)
		}
		return a
	}
}

// aliasesOf returns the localized titles learned for a title.
func (ix *TitleIndex) aliasesOf(mediaType string, id int) []string {
	k := kindOf(mediaType)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []string
	for _, lang := range [...]string{tmdb.LangEN, tmdb.LangTR} {
		if a, ok := ix.aliases[aliasKey{k, int32(id), lang}]; ok {
			out = append(out, a)
		}
	}
	return out
}

// learnTitles feeds the localized titles of a TMDB answer into the typo index.
func (s *Service) learnTitles(lang string, pool []tmdb.SearchResult) {
	for _, r := range pool {
		if r.Title != "" && r.Title != r.OriginalTitle {
			s.index.Learn(r.MediaType, r.ID, lang, r.Title)
		}
	}
}

// commonGenre maps TV genres onto the movie genre they stand for, so movie and TV taste add
// up (Sci-Fi & Fantasy -> Sci-Fi, Action & Adventure -> Action, ...).
func commonGenre(mediaType string, id int) int {
	if mediaType == "tv" {
		if p, ok := filterGenres[id]; ok && p.movie != 0 {
			return p.movie
		}
	}
	return id
}

// ---- Taste (light personalization) ----

// RatedTitle is one rating or favorite of a profile (fed from the ratings module in main.go).
type RatedTitle struct {
	MediaType string
	TmdbID    int
	Rating    string // "love", "like", "dislike" or ""
	Favorite  bool
}

// RatingsSource lists a profile's rated and favorite titles.
type RatingsSource func(profileID uint) ([]RatedTitle, error)

const (
	tasteTTL       = 30 * time.Minute
	tasteMaxTitles = 40
)

type tasteEntry struct {
	affinity map[int]float64
	expires  time.Time
}

// tasteCache holds the genre affinity per profile. It is computed in the background: a search
// never waits for it. gen counts invalidations per profile: a compute that started before the
// last one is dropped.
type tasteCache struct {
	mu      sync.Mutex
	source  RatingsSource
	tm      *tmdb.Client
	entries map[uint]tasteEntry
	running map[uint]uint64 // generation of the running compute
	gen     map[uint]uint64
}

// SetRatings enables personalization with the profile's ratings.
func (s *Service) SetRatings(src RatingsSource) {
	s.taste.mu.Lock()
	s.taste.source = src
	s.taste.tm = s.tm
	s.taste.mu.Unlock()
}

// InvalidateTaste drops a profile's cached affinity (ratings changed).
func (s *Service) InvalidateTaste(profileID uint) {
	s.local.invalidate()
	t := &s.taste
	t.mu.Lock()
	t.init()
	delete(t.entries, profileID)
	delete(t.running, profileID)
	t.gen[profileID]++
	t.mu.Unlock()
}

func (t *tasteCache) init() {
	if t.entries == nil {
		t.entries, t.running, t.gen = map[uint]tasteEntry{}, map[uint]uint64{}, map[uint]uint64{}
	}
}

func (t *tasteCache) affinity(profileID uint) map[int]float64 {
	if profileID == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.source == nil {
		return nil
	}
	t.init()
	e, ok := t.entries[profileID]
	if _, busy := t.running[profileID]; (!ok || time.Now().After(e.expires)) && !busy {
		gen := t.gen[profileID]
		t.running[profileID] = gen
		go t.compute(profileID, gen, t.source, t.tm)
	}
	return e.affinity
}

func ratingWeight(r RatedTitle) float64 {
	w := 0.0
	switch r.Rating {
	case "love":
		w = 2
	case "like":
		w = 1
	case "dislike":
		w = -2
	}
	if r.Favorite {
		w += 1
	}
	return w
}

// compute sums rating weights per genre (details from the shared TMDB cache) and stores them
// scaled to [-1, 1], unless the profile was invalidated meanwhile.
func (t *tasteCache) compute(profileID uint, gen uint64, source RatingsSource, tm *tmdb.Client) {
	aff := map[int]float64{}
	defer func() {
		t.mu.Lock()
		if t.gen[profileID] == gen {
			t.entries[profileID] = tasteEntry{affinity: aff, expires: time.Now().Add(tasteTTL)}
		}
		if g, ok := t.running[profileID]; ok && g == gen {
			delete(t.running, profileID)
		}
		t.mu.Unlock()
	}()
	rated, err := source(profileID)
	if err != nil {
		log.Printf("catalog: taste of profile %d: %v", profileID, err)
		return
	}
	if len(rated) > tasteMaxTitles {
		rated = rated[:tasteMaxTitles]
	}
	tm = tm.WithLang(tmdb.LangEN)
	aff = affinityOf(rated, func(mediaType string, id int) []int {
		if res := titleResult(tm, mediaType, id); res != nil {
			return res.GenreIDs
		}
		return nil
	})
}

// affinityOf sums rating weights per (common) genre and scales them to [-1, 1]. It is empty
// when every sum is 0, so no score turns NaN.
func affinityOf(rated []RatedTitle, genres func(mediaType string, id int) []int) map[int]float64 {
	aff := map[int]float64{}
	for _, r := range rated {
		w := ratingWeight(r)
		if w == 0 {
			continue
		}
		for _, g := range genres(r.MediaType, r.TmdbID) {
			aff[commonGenre(r.MediaType, g)] += w
		}
	}
	top := 0.0
	for _, v := range aff {
		top = max(top, math.Abs(v))
	}
	if top <= 0 || math.IsNaN(top) || math.IsInf(top, 0) {
		return map[int]float64{}
	}
	for g, v := range aff {
		if aff[g] = v / top; aff[g] == 0 {
			delete(aff, g)
		}
	}
	return aff
}
