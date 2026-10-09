package recommendations

import (
	"log"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	cacheTTL    = 30 * time.Minute
	fetchers    = 6
	cacheMaxLen = 200
)

type cached struct {
	resp    *Response
	expires time.Time
}

type Service struct {
	ratings RatingsSource
	catalog Catalog
	now     func() time.Time

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
	done chan struct{}
	resp *Response
	err  error
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
// cached for 30 minutes.
func (s *Service) For(profileID uint, lang string) (*Response, error) {
	key := cacheKey{profileID, lang}
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && s.now().Before(c.expires) {
		s.mu.Unlock()
		return c.resp, nil
	}
	if cl, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		<-cl.done
		return cl.resp, cl.err
	}
	cl := &call{done: make(chan struct{})}
	s.inflight[key] = cl
	gen := s.gen[profileID]
	s.mu.Unlock()

	cl.resp, cl.err = s.compute(profileID, lang)

	s.mu.Lock()
	delete(s.inflight, key)
	if cl.err == nil && len(cl.resp.Rows) > 0 && s.gen[profileID] == gen {
		if len(s.cache) >= cacheMaxLen {
			s.cache = map[cacheKey]cached{}
		}
		s.cache[key] = cached{resp: cl.resp, expires: s.now().Add(cacheTTL)}
	}
	s.mu.Unlock()
	close(cl.done)
	return cl.resp, cl.err
}

func (s *Service) compute(profileID uint, lang string) (*Response, error) {
	signals, err := s.ratings.Signals(profileID)
	if err != nil {
		return nil, err
	}
	seeds, dislikes := pickSeeds(signals, s.now())

	var rows []Row
	if len(seeds) > 0 {
		in := s.fetch(lang, signals, seeds, dislikes)
		rows = buildRows(score(in), seeds)
	}
	if len(rows) == 0 {
		trending, err := s.catalog.Trending(lang)
		if err != nil {
			log.Printf("recommendations: trending: %v", err)
		}
		rows = trendingRows(trending, signals)
	}
	return &Response{Rows: normalize(rows)}, nil
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

	for _, sd := range append(append([]seed{}, seeds...), dislikes...) {
		k := keyOf(sd.MediaType, sd.TmdbID)
		run(func() {
			recs, similar, err := s.catalog.Related(lang, k.mediaType, k.id)
			if err != nil {
				log.Printf("recommendations: related %s %d: %v", k.mediaType, k.id, err)
			}
			mu.Lock()
			in.related[k] = relatedLists{recs, similar}
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
