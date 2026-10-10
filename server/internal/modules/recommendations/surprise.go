package recommendations

import (
	"errors"
	"log"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// "Surprise me": one title per press, drawn from three buckets of a per-profile pool
// (taste blend 50 %, adjacent discovery 25 %, wild card 25 %). See docs/ARCHITECTURE.md "Surprise me".
const (
	surpriseTTL    = 30 * time.Minute
	surpriseMemory = 30 // the last picks of a profile are not repeated
	surpriseTop    = 12 // a group is sampled among its best eligible titles
	seedRotation   = 4  // a group used in the last 4 picks is 5x less likely
	rotationFactor = 0.2

	// Softmax temperatures on scores scaled to [0, 1]: taste picks lean to the best match,
	// discoveries are spread more evenly
	tasteTemperature   = 0.35
	exploreTemperature = 0.7
)

// Buckets and their share of the presses.
const (
	BucketTaste    = "taste"
	BucketAdjacent = "adjacent"
	BucketWild     = "wild"
)

var bucketShare = []struct {
	name  string
	share float64
}{{BucketTaste, 0.5}, {BucketAdjacent, 0.25}, {BucketWild, 0.25}}

// Surprise reasons.
const (
	SurpriseBecauseLoved   = "because_loved"
	SurpriseBecauseLiked   = "because_liked"
	SurpriseBecauseWatched = "because_watched"
	SurpriseFavoriteLike   = "favorite_like"
	SurpriseGenre          = "genre"     // taste blend from the profile's top genres
	SurpriseAdjacent       = "adjacent"  // a liked genre, but nothing like the seeds
	SurpriseWild           = "wild_card" // acclaimed and outside the profile's taste
	SurpriseTrending       = "trending_pick"
)

// ErrNoSurprise means there was nothing left to pick (TMDB unreachable, or everything excluded).
var ErrNoSurprise = errors.New("no title to surprise with")

// Reason says why a surprise was picked. Title / MediaType / TmdbID name the seed title
// (because_* and favorite_like); GenreIDs the genres (genre), GenreID the genre (adjacent);
// Language the original language of a non-English wild card.
type Reason struct {
	Kind      string `json:"kind"`
	Bucket    string `json:"bucket"`
	Title     string `json:"title,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	TmdbID    int    `json:"tmdb_id,omitempty"`
	GenreID   int    `json:"genre_id,omitempty"`
	GenreIDs  []int  `json:"genre_ids,omitempty"`
	Language  string `json:"language,omitempty"`
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

type surpriseCand struct {
	item  Item
	score float64
	genre int // main genre (canonical): it must not be one of the last pick's genres
}

// surpriseGroup is one source a press draws from: a seed's related titles, a genre pair,
// a liked genre or a wild-card theme.
type surpriseGroup struct {
	id     string
	bucket string
	weight float64
	reason Reason
	cands  []*surpriseCand // best first
}

type surprisePool struct {
	groups  []*surpriseGroup
	expires time.Time
}

// surprisePick is what a profile's memory keeps of a pick.
type surprisePick struct {
	key       titleKey
	bucket    string
	group     string
	genre     int
	genres    []int // canonical genres of the title plus the main genre
	mediaType string
}

type surpriseState struct {
	pickMu  sync.Mutex // see pick
	mu      sync.Mutex
	pools   map[cacheKey]surprisePool
	recent  map[uint][]surprisePick
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

func (s *Service) random() float64 {
	s.surprise.mu.Lock()
	defer s.surprise.mu.Unlock()
	return s.surprise.rnd.Float64()
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

// Surprise picks one title for the profile, different from its last 30 picks, the exclude
// list, rated titles and titles it has played.
func (s *Service) Surprise(profileID uint, lang string, exclude []titleKey) (*Surprise, error) {
	if err := s.checkProfile(profileID); err != nil {
		return nil, err
	}
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
	g, c := s.pick(profileID, pool.groups, skip)
	if c == nil {
		return nil, ErrNoSurprise
	}

	item := c.item
	if item.GenreIDs == nil {
		item.GenreIDs = []int{}
	}
	reason := g.reason
	reason.Bucket = g.bucket
	if reason.Kind == SurpriseWild && item.OriginalLanguage != "" && item.OriginalLanguage != "en" {
		reason.Language = item.OriginalLanguage
	}
	out := &Surprise{Title: SurpriseTitle{Item: item}, Reason: reason}
	if logo, err := s.catalog.Logo(lang, item.MediaType, item.ID); err == nil {
		out.Title.LogoPath = logo
	}
	return out, nil
}

// draw picks a bucket, then a group, then a title. The strict pass keeps the variety guards
// (no group and no genre twice in a row); when nothing passes them they are relaxed.
func (s *Service) draw(groups []*surpriseGroup, skip map[titleKey]bool, recent []surprisePick, hideRecent bool) (*surpriseGroup, *surpriseCand) {
	hidden := map[titleKey]bool{}
	for k := range skip {
		hidden[k] = true
	}
	if hideRecent {
		for _, p := range recent {
			hidden[p.key] = true
		}
	}
	order := s.bucketOrder(recent)
	for _, strict := range []bool{true, false} {
		for _, b := range order {
			if g, c := s.drawIn(b, groups, hidden, recent, strict); c != nil {
				return g, c
			}
		}
	}
	return nil, nil
}

// bucketOrder draws the bucket of this press (50 / 25 / 25), then lists the others as fallbacks.
// After an adjacent or wild pick the same bucket is not drawn; after a taste pick taste counts
// half. That keeps the long-run shares at exactly 50 / 25 / 25.
func (s *Service) bucketOrder(recent []surprisePick) []string {
	last := ""
	if len(recent) > 0 {
		last = recent[len(recent)-1].bucket
	}
	weights := make([]float64, len(bucketShare))
	total := 0.0
	for i, b := range bucketShare {
		w := b.share
		if b.name == last {
			if last == BucketTaste {
				w /= 2
			} else {
				w = 0
			}
		}
		weights[i] = w
		total += w
	}
	x := s.random() * total
	first := len(weights) - 1
	for i, w := range weights {
		if x < w {
			first = i
			break
		}
		x -= w
	}
	out := []string{bucketShare[first].name}
	for i, b := range bucketShare {
		if i != first && b.name != last {
			out = append(out, b.name)
		}
	}
	if last != "" && last != bucketShare[first].name {
		out = append(out, last)
	}
	return out
}

func (s *Service) drawIn(bucket string, groups []*surpriseGroup, hidden map[titleKey]bool, recent []surprisePick, strict bool) (*surpriseGroup, *surpriseCand) {
	var last *surprisePick
	if len(recent) > 0 {
		last = &recent[len(recent)-1]
	}
	type option struct {
		g     *surpriseGroup
		cands []*surpriseCand
	}
	var opts []option
	var weights []float64
	total := 0.0
	for _, g := range groups {
		if g.bucket != bucket || g.weight <= 0 {
			continue
		}
		if strict && last != nil && g.id == last.group {
			continue
		}
		var el []*surpriseCand
		for _, c := range g.cands {
			if hidden[keyOf(c.item.MediaType, c.item.ID)] || (strict && last != nil && c.genre != 0 && slices.Contains(last.genres, c.genre)) {
				continue
			}
			el = append(el, c)
			if len(el) == surpriseTop {
				break
			}
		}
		if len(el) == 0 {
			continue
		}
		w := g.weight
		for i := len(recent) - 1; i >= 0 && i >= len(recent)-seedRotation; i-- {
			if recent[i].group == g.id {
				w *= rotationFactor
				break
			}
		}
		opts = append(opts, option{g, el})
		weights = append(weights, w)
		total += w
	}
	if len(opts) == 0 {
		return nil, nil
	}
	x := s.random() * total
	o := opts[len(opts)-1]
	for i, w := range weights {
		if x < w {
			o = opts[i]
			break
		}
		x -= w
	}
	temp := exploreTemperature
	if bucket == BucketTaste {
		temp = tasteTemperature
	}
	return o.g, s.sample(o.cands, temp, recent)
}

// sample draws one title, weighted by a softmax of the scores and away from the media type
// of the last two picks (movies and TV take turns more often).
func (s *Service) sample(cands []*surpriseCand, temperature float64, recent []surprisePick) *surpriseCand {
	hi, lo := cands[0].score, cands[0].score
	for _, c := range cands {
		hi, lo = math.Max(hi, c.score), math.Min(lo, c.score)
	}
	spread := hi - lo
	weights := make([]float64, len(cands))
	total := 0.0
	for i, c := range cands {
		n := 1.0
		if spread > 0 {
			n = (c.score - lo) / spread
		}
		w := math.Exp(n / temperature)
		for j := len(recent) - 1; j >= 0 && j >= len(recent)-2; j-- {
			if recent[j].mediaType == c.item.MediaType {
				w *= 0.5
			}
		}
		weights[i] = w
		total += w
	}
	x := s.random() * total
	for i, w := range weights {
		if x < w {
			return cands[i]
		}
		x -= w
	}
	return cands[len(cands)-1]
}

// surprisePool returns the profile's groups, cached 30 minutes per profile and language.
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
	if len(pool.groups) == 0 {
		return pool, nil
	}
	counts := map[string]int{}
	for _, g := range pool.groups {
		counts[g.bucket] += len(g.cands)
	}
	log.Printf("recommendations: surprise pool of profile %d (%s): %d groups, titles taste %d / adjacent %d / wild %d",
		profileID, lang, len(pool.groups), counts[BucketTaste], counts[BucketAdjacent], counts[BucketWild])
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

// pick draws a title and records it in the profile's memory. Concurrent presses are
// serialized here so both cannot read the same memory and lose a pick.
func (s *Service) pick(profileID uint, groups []*surpriseGroup, skip map[titleKey]bool) (*surpriseGroup, *surpriseCand) {
	s.surprise.pickMu.Lock()
	defer s.surprise.pickMu.Unlock()
	s.surprise.mu.Lock()
	recent := append([]surprisePick(nil), s.surprise.recent[profileID]...)
	s.surprise.mu.Unlock()

	g, c := s.draw(groups, skip, recent, true)
	if c == nil && len(recent) > 0 {
		// Everything good was shown lately: the memory starts over (the guards still see the last picks)
		g, c = s.draw(groups, skip, recent, false)
		recent = nil
	}
	if c == nil {
		return nil, nil
	}
	recent = append(recent, surprisePick{
		key: keyOf(c.item.MediaType, c.item.ID), bucket: g.bucket, group: g.id, genre: c.genre,
		genres: append(canonGenres(c.item.GenreIDs), c.genre), mediaType: c.item.MediaType,
	})
	if len(recent) > surpriseMemory {
		recent = recent[len(recent)-surpriseMemory:]
	}
	s.surprise.mu.Lock()
	s.surprise.recent[profileID] = recent
	s.surprise.mu.Unlock()
	return g, c
}
