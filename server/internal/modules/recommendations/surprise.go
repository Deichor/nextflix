package recommendations

import (
	"errors"
	"log"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// "Surprise me": one title per press, sampled from a per-profile candidate pool.
// See docs/ARCHITECTURE.md "Surprise me".
const (
	surpriseTTL      = 30 * time.Minute
	surpriseMemory   = 20 // the last picks of a profile are not repeated
	surpriseSeeds    = 8
	surpriseDislikes = 5
	surpriseGenres   = 3
	surpriseTaste    = 40 // titles whose genres make the taste profile
	surpriseTop      = 40 // sampled among the best candidates
	surpriseMinVotes = 50
	// Softmax temperature on scores scaled to [0, 1]: the best candidate is ~17x as likely as the 40th
	surpriseTemperature = 0.35

	tasteLove     = 3.0
	tasteLike     = 2.0
	tasteFavorite = 1.2 // favorite without a rating
	tasteFavBonus = 0.5 // favorite on top of love / like
	tasteDislike  = -3.0
	tasteFinished = 1.0
	tasteStarted  = 0.4

	genreSupport    = 0.8   // a top-genre discover list counts this x affinity
	trendingSupport = 0.3   // exploration
	dislikedGenre   = -0.35 // candidates whose genres average below this are dropped
)

// Surprise reasons.
const (
	SurpriseBecauseLoved   = "because_loved"
	SurpriseBecauseLiked   = "because_liked"
	SurpriseBecauseWatched = "because_watched"
	SurpriseFavoriteLike   = "favorite_like"
	SurpriseGenre          = "genre"
	SurpriseTrending       = "trending_pick"
)

// ErrNoSurprise means there was nothing left to pick (TMDB unreachable, or everything excluded).
var ErrNoSurprise = errors.New("no title to surprise with")

// Reason says why a surprise was picked. Title / MediaType / TmdbID name the seed title
// (because_* and favorite_like), GenreID the genre (genre).
type Reason struct {
	Kind      string `json:"kind"`
	Title     string `json:"title,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	TmdbID    int    `json:"tmdb_id,omitempty"`
	GenreID   int    `json:"genre_id,omitempty"`
}

// SurpriseTitle is a recommended item plus its logo.
type SurpriseTitle struct {
	Item
	LogoPath string `json:"logo_path"`
}

// Surprise is the answer of GET /api/recommendations/surprise.
type Surprise struct {
	Title  SurpriseTitle `json:"title"`
	Reason Reason        `json:"reason"`
}

// TMDB genre ids per media type; discover is only asked for the ones that exist there.
var (
	movieGenreIDs = map[int]bool{28: true, 12: true, 16: true, 35: true, 80: true, 99: true, 18: true,
		10751: true, 14: true, 36: true, 27: true, 10402: true, 9648: true, 10749: true, 878: true, 53: true,
		10752: true, 37: true}
	tvGenreIDs = map[int]bool{10759: true, 16: true, 35: true, 80: true, 99: true, 18: true, 10751: true,
		10762: true, 9648: true, 10764: true, 10765: true, 10768: true, 37: true}
)

// tasteSeed is a title that says something about the profile's taste.
type tasteSeed struct {
	key    titleKey
	title  string
	kind   string  // reason kind of titles found through it
	weight float64 // signed, with recency
	at     time.Time
}

type surpriseCand struct {
	item    Item
	support float64
	penalty float64
	score   float64
	best    float64 // largest single contribution, decides the reason
	reason  Reason
}

func (c *surpriseCand) add(v float64, r Reason) {
	c.support += v
	if v > c.best {
		c.best, c.reason = v, r
	}
}

type surprisePool struct {
	cands   []*surpriseCand // best first, rated titles already removed
	expires time.Time
}

type surpriseState struct {
	mu      sync.Mutex
	pools   map[cacheKey]surprisePool
	recent  map[uint][]titleKey
	history HistorySource
	rnd     *rand.Rand
}

// SetHistory lets the surprise use (and skip) the titles a profile has watched.
func (s *Service) SetHistory(h HistorySource) {
	s.surprise.mu.Lock()
	s.surprise.history = h
	s.surprise.mu.Unlock()
}

func (s *Service) invalidateSurprise(profileID uint) {
	s.surprise.mu.Lock()
	for k := range s.surprise.pools {
		if k.profile == profileID {
			delete(s.surprise.pools, k)
		}
	}
	s.surprise.mu.Unlock()
}

func (s *Service) historyOf(profileID uint) []Watched {
	s.surprise.mu.Lock()
	h := s.surprise.history
	s.surprise.mu.Unlock()
	if h == nil {
		return nil
	}
	w, err := h.History(profileID)
	if err != nil {
		log.Printf("recommendations: history of profile %d: %v", profileID, err)
	}
	return w
}

// ParseExclude reads "movie-27205,tv:1396" style keys.
func ParseExclude(raw string) []titleKey {
	var out []titleKey
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		i := strings.IndexAny(part, "-:")
		if i <= 0 {
			continue
		}
		mediaType := part[:i]
		id, err := strconv.Atoi(part[i+1:])
		if err != nil || id <= 0 || (mediaType != "movie" && mediaType != "tv") {
			continue
		}
		out = append(out, keyOf(mediaType, id))
	}
	return out
}

// Surprise picks one title the profile is likely to enjoy, different from the profile's
// last 20 picks, the exclude list, rated titles and titles it has played.
func (s *Service) Surprise(profileID uint, lang string, exclude []titleKey) (*Surprise, error) {
	history := s.historyOf(profileID)
	pool, err := s.surprisePool(profileID, lang, history)
	if err != nil {
		return nil, err
	}

	skip := map[titleKey]bool{}
	for _, k := range exclude {
		skip[k] = true
	}
	for _, w := range history {
		skip[keyOf(w.MediaType, w.TmdbID)] = true
	}
	s.surprise.mu.Lock()
	recent := append([]titleKey(nil), s.surprise.recent[profileID]...)
	s.surprise.mu.Unlock()

	c := s.sample(pool.cands, skip, recent)
	if c == nil && len(recent) > 0 {
		// Everything good was shown lately: start the memory over
		c = s.sample(pool.cands, skip, nil)
		s.surprise.mu.Lock()
		s.surprise.recent[profileID] = nil
		s.surprise.mu.Unlock()
	}
	if c == nil {
		return nil, ErrNoSurprise
	}

	k := keyOf(c.item.MediaType, c.item.ID)
	s.surprise.mu.Lock()
	r := append(s.surprise.recent[profileID], k)
	if len(r) > surpriseMemory {
		r = r[len(r)-surpriseMemory:]
	}
	s.surprise.recent[profileID] = r
	s.surprise.mu.Unlock()

	item := c.item
	if item.GenreIDs == nil {
		item.GenreIDs = []int{}
	}
	out := &Surprise{Title: SurpriseTitle{Item: item}, Reason: c.reason}
	if logo, err := s.catalog.Logo(lang, item.MediaType, item.ID); err == nil {
		out.Title.LogoPath = logo
	}
	return out, nil
}

// sample draws one of the best eligible candidates, weighted by a softmax of their scores.
func (s *Service) sample(cands []*surpriseCand, skip map[titleKey]bool, recent []titleKey) *surpriseCand {
	shown := map[titleKey]bool{}
	for _, k := range recent {
		shown[k] = true
	}
	top := make([]*surpriseCand, 0, surpriseTop)
	for _, c := range cands {
		k := keyOf(c.item.MediaType, c.item.ID)
		if skip[k] || shown[k] {
			continue
		}
		top = append(top, c)
		if len(top) == surpriseTop {
			break
		}
	}
	if len(top) == 0 {
		return nil
	}
	hi, lo := top[0].score, top[len(top)-1].score
	spread := hi - lo
	weights := make([]float64, len(top))
	total := 0.0
	for i, c := range top {
		n := 1.0
		if spread > 0 {
			n = (c.score - lo) / spread
		}
		weights[i] = math.Exp(n / surpriseTemperature)
		total += weights[i]
	}
	s.surprise.mu.Lock()
	x := s.surprise.rnd.Float64() * total
	s.surprise.mu.Unlock()
	for i, w := range weights {
		if x < w {
			return top[i]
		}
		x -= w
	}
	return top[len(top)-1]
}

// surprisePool returns the profile's candidates, cached 30 minutes per profile and language.
func (s *Service) surprisePool(profileID uint, lang string, history []Watched) (surprisePool, error) {
	key := cacheKey{profileID, lang}
	s.mu.Lock()
	gen := s.gen[profileID]
	s.mu.Unlock()
	s.surprise.mu.Lock()
	if p, ok := s.surprise.pools[key]; ok && s.now().Before(p.expires) {
		s.surprise.mu.Unlock()
		return p, nil
	}
	s.surprise.mu.Unlock()

	signals, err := s.ratings.Signals(profileID)
	if err != nil {
		return surprisePool{}, err
	}
	pool := s.buildSurprisePool(lang, signals, history)
	if len(pool.cands) == 0 {
		return pool, nil
	}
	pool.expires = s.now().Add(surpriseTTL)
	s.mu.Lock()
	current := s.gen[profileID] == gen
	s.mu.Unlock()
	if current {
		s.surprise.mu.Lock()
		if len(s.surprise.pools) >= cacheMaxLen {
			s.surprise.pools = map[cacheKey]surprisePool{}
		}
		s.surprise.pools[key] = pool
		s.surprise.mu.Unlock()
	}
	return pool, nil
}

// tasteSeeds turns ratings and watch history into weighted titles (love > like > favorite >
// finished > started; dislikes negative). A rated title ignores its history.
func tasteSeeds(signals []Signal, history []Watched, now time.Time) []tasteSeed {
	var out []tasteSeed
	rated := map[titleKey]bool{}
	for _, s := range signals {
		k := keyOf(s.MediaType, s.TmdbID)
		rated[k] = true
		var w float64
		var kind string
		switch s.Rating {
		case "love":
			w, kind = tasteLove, SurpriseBecauseLoved
		case "like":
			w, kind = tasteLike, SurpriseBecauseLiked
		case "dislike":
			w = tasteDislike
		default:
			if s.Favorite {
				w, kind = tasteFavorite, SurpriseFavoriteLike
			}
		}
		if s.Favorite && (s.Rating == "love" || s.Rating == "like") {
			w += tasteFavBonus
		}
		if w == 0 {
			continue
		}
		out = append(out, tasteSeed{key: k, title: s.Title, kind: kind, weight: w * recency(s.UpdatedAt, now), at: s.UpdatedAt})
	}
	for _, h := range history {
		k := keyOf(h.MediaType, h.TmdbID)
		if rated[k] {
			continue
		}
		w := tasteStarted
		if h.Finished {
			w = tasteFinished
		}
		out = append(out, tasteSeed{key: k, title: h.Title, kind: SurpriseBecauseWatched, weight: w * recency(h.UpdatedAt, now), at: h.UpdatedAt})
	}
	return out
}

// buildSurprisePool collects candidates from the related lists of the strongest titles, the
// profile's favorite genres and trending titles, and scores them.
func (s *Service) buildSurprisePool(lang string, signals []Signal, history []Watched) surprisePool {
	seeds := tasteSeeds(signals, history, s.now())
	var pos, neg []tasteSeed
	for _, sd := range seeds {
		if sd.weight > 0 {
			pos = append(pos, sd)
		} else {
			neg = append(neg, sd)
		}
	}
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].weight > pos[j].weight })
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].at.After(neg[j].at) })
	taste := append(append([]tasteSeed{}, pos...), neg...)
	sort.SliceStable(taste, func(i, j int) bool { return taste[i].at.After(taste[j].at) })
	if len(taste) > surpriseTaste {
		taste = taste[:surpriseTaste]
	}
	if len(pos) > surpriseSeeds {
		pos = pos[:surpriseSeeds]
	}
	if len(neg) > surpriseDislikes {
		neg = neg[:surpriseDislikes]
	}

	// Rated titles are never picked; watched ones are skipped per press (history changes often)
	rated := map[titleKey]bool{}
	for _, sig := range signals {
		rated[keyOf(sig.MediaType, sig.TmdbID)] = true
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, fetchers)
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		}()
	}

	related := map[titleKey]relatedLists{}
	genres := map[titleKey][]int{}
	for _, sd := range append(append([]tasteSeed{}, pos...), neg...) {
		k := sd.key
		run(func() {
			recs, similar, err := s.catalog.Related(lang, k.mediaType, k.id)
			if err != nil {
				log.Printf("recommendations: surprise related %s %d: %v", k.mediaType, k.id, err)
			}
			mu.Lock()
			related[k] = relatedLists{recs, similar}
			mu.Unlock()
		})
	}
	for _, sd := range taste {
		k := sd.key
		run(func() {
			g, err := s.catalog.Genres(k.mediaType, k.id)
			if err != nil {
				return
			}
			mu.Lock()
			genres[k] = g
			mu.Unlock()
		})
	}
	var trending []Item
	run(func() {
		t, err := s.catalog.Trending(lang)
		if err != nil {
			log.Printf("recommendations: surprise trending: %v", err)
		}
		mu.Lock()
		trending = t
		mu.Unlock()
	})
	wg.Wait()

	affinity := tasteAffinity(taste, genres)
	topGenres := favoriteGenres(affinity, surpriseGenres)
	discovered := map[int][]Item{} // by genre, movie and TV lists concatenated
	for _, g := range topGenres {
		for _, mt := range []string{"movie", "tv"} {
			if (mt == "movie" && !movieGenreIDs[g]) || (mt == "tv" && !tvGenreIDs[g]) {
				continue
			}
			run(func() {
				items, err := s.catalog.Discover(lang, mt, g)
				if err != nil {
					log.Printf("recommendations: surprise discover %s %d: %v", mt, g, err)
					return
				}
				mu.Lock()
				discovered[g] = append(discovered[g], items...)
				mu.Unlock()
			})
		}
	}
	wg.Wait()

	return surprisePool{
		cands: scoreSurprise(pos, neg, related, topGenres, discovered, trending, affinity, rated),
	}
}

// tasteAffinity sums seed weights per genre and scales them to [-1, 1].
func tasteAffinity(taste []tasteSeed, genres map[titleKey][]int) map[int]float64 {
	sum := map[int]float64{}
	for _, sd := range taste {
		for _, g := range genres[sd.key] {
			sum[g] += sd.weight
		}
	}
	maxAbs := 0.0
	for _, v := range sum {
		maxAbs = math.Max(maxAbs, math.Abs(v))
	}
	if maxAbs > 0 {
		for g := range sum {
			sum[g] /= maxAbs
		}
	}
	return sum
}

// favoriteGenres returns up to n genres with a clearly positive affinity, strongest first.
func favoriteGenres(affinity map[int]float64, n int) []int {
	var out []int
	for g, v := range affinity {
		if v >= 0.2 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if affinity[out[i]] != affinity[out[j]] {
			return affinity[out[i]] > affinity[out[j]]
		}
		return out[i] < out[j]
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// scoreSurprise ranks every candidate: support from loved / liked / favorite / watched titles,
// favorite genres and trending, minus what disliked titles point to, plus genre affinity and a
// quality prior. Rated, adult, artless and little-voted titles are dropped, and so are titles
// that look more like the dislikes than like the rest, or whose genres the profile dislikes.
func scoreSurprise(pos, neg []tasteSeed, related map[titleKey]relatedLists, topGenres []int,
	discovered map[int][]Item, trending []Item, affinity map[int]float64, rated map[titleKey]bool) []*surpriseCand {
	pool := map[titleKey]*surpriseCand{}
	get := func(it Item) *surpriseCand {
		k := keyOf(it.MediaType, it.ID)
		if rated[k] || it.Adult || it.BackdropPath == "" || it.PosterPath == "" || it.VoteCount < surpriseMinVotes {
			return nil
		}
		c := pool[k]
		if c == nil {
			c = &surpriseCand{item: it}
			pool[k] = c
		}
		return c
	}
	lists := func(k titleKey) []struct {
		items []Item
		w     float64
	} {
		l := related[k]
		return []struct {
			items []Item
			w     float64
		}{{l.recs, listWeightRecs}, {l.similar, listWeightSimilar}}
	}

	for _, sd := range pos {
		reason := Reason{Kind: sd.kind, Title: sd.title, MediaType: sd.key.mediaType, TmdbID: sd.key.id}
		for _, l := range lists(sd.key) {
			for i, it := range l.items {
				if c := get(it); c != nil {
					c.add(sd.weight*l.w*positionWeight(i), reason)
				}
			}
		}
	}
	for _, g := range topGenres {
		reason := Reason{Kind: SurpriseGenre, GenreID: g}
		for i, it := range discovered[g] {
			if c := get(it); c != nil {
				c.add(genreSupport*affinity[g]*positionWeight(i%20), reason)
			}
		}
	}
	for i, it := range trending {
		if c := get(it); c != nil {
			c.add(trendingSupport*positionWeight(i), Reason{Kind: SurpriseTrending})
		}
	}
	for _, sd := range neg {
		for _, l := range lists(sd.key) {
			for i, it := range l.items {
				if c := pool[keyOf(it.MediaType, it.ID)]; c != nil {
					c.penalty += -sd.weight * l.w * positionWeight(i)
				}
			}
		}
	}

	out := make([]*surpriseCand, 0, len(pool))
	for _, c := range pool {
		a := itemAffinity(c.item, affinity)
		if c.support <= 0 || c.penalty >= c.support || a < dislikedGenre {
			continue
		}
		c.score = c.support - c.penalty + genreWeight*a + prior(c.item)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].item.MediaType != out[j].item.MediaType {
			return out[i].item.MediaType < out[j].item.MediaType
		}
		return out[i].item.ID < out[j].item.ID
	})
	return out
}
