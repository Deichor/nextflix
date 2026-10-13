package recommendations

import (
	"errors"
	"log"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	cacheTTL    = 30 * time.Minute
	partialTTL  = 2 * time.Minute // the matcher was not ready for some seeds or TMDB's lists failed
	fetchers    = 6
	cacheMaxLen = 200
	// Expired rows are answered this long while new ones are computed.
	staleFor = 24 * time.Hour
)

type cached struct {
	resp    *Response
	expires time.Time
}

type Service struct {
	ratings  RatingsSource
	catalog  Catalog
	similar  SimilarSource  // nil = TMDB's lists for every seed
	profiles ProfileChecker // nil = every id is accepted
	now      func() time.Time

	mu       sync.Mutex
	cache    map[cacheKey]cached
	gen      map[uint]uint64 // per profile, bumped on invalidation; stale computations are not cached
	inflight map[cacheKey]*call

	surprise surpriseState
}

// cacheKey: rows are kept per profile and content language.
type cacheKey struct {
	profile uint
	lang    string
}

type call struct {
	done    chan struct{}
	gen     uint64 // profile generation it started with
	resp    *Response
	err     error
	partial bool
}

func NewService(ratings RatingsSource, catalog Catalog) *Service {
	return &Service{
		ratings:  ratings,
		catalog:  catalog,
		now:      time.Now,
		cache:    map[cacheKey]cached{},
		gen:      map[uint]uint64{},
		inflight: map[cacheKey]*call{},
		surprise: surpriseState{
			pools:  map[cacheKey]surprisePool{},
			recent: map[uint][]surprisePick{},
			rnd:    rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9e3779b97f4a7c15)),
		},
	}
}

// SetSimilar makes the positive seeds' rows use the matcher's list instead of TMDB's
// recommendations and similar lists. Called once at startup.
func (s *Service) SetSimilar(src SimilarSource) { s.similar = src }

// SetProfiles makes For and Surprise refuse ids of profiles that do not exist. Called once at startup.
func (s *Service) SetProfiles(check ProfileChecker) { s.profiles = check }

// ErrUnknownProfile is returned for a profile id that does not exist.
var ErrUnknownProfile = errors.New("unknown profile")

func (s *Service) checkProfile(profileID uint) error {
	if s.profiles == nil {
		return nil
	}
	ok, err := s.profiles(profileID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnknownProfile
	}
	return nil
}

// Invalidate drops a profile's cached rows and surprise pools in every language (called when its ratings change).
func (s *Service) Invalidate(profileID uint) {
	s.mu.Lock()
	for k := range s.cache {
		if k.profile == profileID {
			delete(s.cache, k)
		}
	}
	s.gen[profileID]++
	s.mu.Unlock()
	s.invalidateSurprise(profileID)
}

// For returns the recommendation rows of a profile with titles in lang ("en" or "tr"),
// cached for 30 minutes. Expired rows (up to staleFor) are answered at once while new ones are
// computed in the background; invalidated rows are gone, so a rating change waits for new rows.
func (s *Service) For(profileID uint, lang string) (*Response, error) {
	key := cacheKey{profileID, lang}
	s.mu.Lock()
	if c, ok := s.cache[key]; ok {
		now := s.now()
		if now.Before(c.expires) {
			s.mu.Unlock()
			return c.resp, nil
		}
		if now.Before(c.expires.Add(staleFor)) {
			if cl, ok := s.inflight[key]; !ok || cl.gen != s.gen[profileID] {
				cl := s.startLocked(key)
				go s.complete(key, cl, true)
			}
			s.mu.Unlock()
			return c.resp, nil
		}
	}
	s.mu.Unlock()
	if err := s.checkProfile(profileID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	// A computation started before an invalidation would answer with the old ratings
	if cl, ok := s.inflight[key]; ok && cl.gen == s.gen[profileID] {
		s.mu.Unlock()
		<-cl.done
		return cl.resp, cl.err
	}
	cl := s.startLocked(key)
	s.mu.Unlock()
	s.complete(key, cl, false)
	return cl.resp, cl.err
}

// startLocked registers a computation of key; s.mu must be held.
func (s *Service) startLocked(key cacheKey) *call {
	cl := &call{done: make(chan struct{}), gen: s.gen[key.profile], err: errors.New("recommendations: compute failed")}
	s.inflight[key] = cl
	return cl
}

// complete computes the rows of key and caches them unless the profile was invalidated meanwhile.
// A background run checks the profile first and logs instead of panicking.
func (s *Service) complete(key cacheKey, cl *call, background bool) {
	defer func() {
		if background {
			if r := recover(); r != nil {
				log.Printf("recommendations: background compute for profile %d panicked: %v", key.profile, r)
			}
		}
		s.mu.Lock()
		if s.inflight[key] == cl {
			delete(s.inflight, key)
		}
		if cl.err == nil && len(cl.resp.Rows) > 0 && s.gen[key.profile] == cl.gen {
			if len(s.cache) >= cacheMaxLen {
				s.cache = map[cacheKey]cached{}
			}
			ttl := cacheTTL
			if cl.partial {
				ttl = partialTTL
			}
			s.cache[key] = cached{resp: cl.resp, expires: s.now().Add(ttl)}
		}
		s.mu.Unlock()
		close(cl.done)
	}()
	if background {
		if err := s.checkProfile(key.profile); err != nil {
			cl.err = err
			log.Printf("recommendations: background compute for profile %d: %v", key.profile, err)
			return
		}
	}
	cl.resp, cl.partial, cl.err = s.compute(key.profile, key.lang)
	if background && cl.err != nil {
		log.Printf("recommendations: background compute for profile %d: %v", key.profile, cl.err)
	}
}

// compute builds the rows; partial reports that some seeds fell back to TMDB's lists or lack them.
func (s *Service) compute(profileID uint, lang string) (resp *Response, partial bool, err error) {
	signals, err := s.ratings.Signals(profileID)
	if err != nil {
		return nil, false, err
	}
	seeds, dislikes := pickSeeds(signals, s.now())

	var rows []Row
	if len(seeds) > 0 {
		in := s.fetch(lang, signals, seeds, dislikes)
		partial = in.partial
		rows = buildRows(score(in), seeds)
	}
	if len(rows) == 0 {
		trending, err := s.catalog.Trending(lang)
		if err != nil {
			log.Printf("recommendations: trending: %v", err)
		}
		rows = trendingRows(trending, signals)
	}
	return &Response{Rows: normalize(rows)}, partial, nil
}

// fetch loads the related lists of all seeds and dislikes and the genres of rated titles in parallel.
func (s *Service) fetch(lang string, signals []Signal, seeds, dislikes []seed) scoreInput {
	in := scoreInput{
		signals:  signals,
		seeds:    seeds,
		dislikes: dislikes,
		related:  map[titleKey]relatedLists{},
		genres:   map[titleKey][]int{},
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

	for i, sd := range append(append([]seed{}, seeds...), dislikes...) {
		k := keyOf(sd.MediaType, sd.TmdbID)
		positive := i < len(seeds)
		run(func() {
			if positive && s.similar != nil {
				// ok with an empty list: the matcher found nothing similar
				if items, ok := s.similar(lang, k.mediaType, k.id); ok {
					mu.Lock()
					in.related[k] = relatedLists{items, nil}
					mu.Unlock()
					return
				}
				mu.Lock()
				in.partial = true
				mu.Unlock()
			}
			recs, similar, err := s.catalog.Related(lang, k.mediaType, k.id)
			if err != nil {
				log.Printf("recommendations: related %s %d: %v", k.mediaType, k.id, err)
			}
			mu.Lock()
			in.related[k] = relatedLists{recs, similar}
			if err != nil {
				in.partial = true
			}
			mu.Unlock()
		})
	}
	for _, sig := range genreTitles(signals) {
		k := keyOf(sig.MediaType, sig.TmdbID)
		run(func() {
			g, err := s.catalog.Genres(k.mediaType, k.id)
			if err != nil {
				return
			}
			mu.Lock()
			in.genres[k] = g
			mu.Unlock()
		})
	}
	wg.Wait()
	return in
}

// normalize copies items (TMDB data is shared) and turns nil slices into [] for JSON.
func normalize(rows []Row) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		items := make([]Item, len(r.Items))
		for i, it := range r.Items {
			if it.GenreIDs == nil {
				it.GenreIDs = []int{}
			}
			items[i] = it
		}
		r.Items = items
		out = append(out, r)
	}
	return out
}
