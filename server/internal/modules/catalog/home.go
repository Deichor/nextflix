package catalog

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
	"nextflix/internal/platform/tmdb"
)

// Top10Source returns the Top 10 list of a region ("TR", "global") and type ("movie", "tv") in
// lang; ok is false before the first download. Wired in main.go.
type Top10Source func(region, mediaType, lang string) (any, bool)

// SetTop10 adds the Top 10 rows to GET /api/home. Called once at startup.
func (s *Service) SetTop10(src Top10Source) { s.top10 = src }

// homeDeadline: sections not ready by then are null; their fetches keep running and fill the cache.
var homeDeadline = 8 * time.Second

type homeGenre struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params"`
	Data   any               `json:"data"`
}

// homeResponse is GET /api/home. Each section is the body of its old endpoint, or null.
type homeResponse struct {
	Version          int         `json:"version"`
	Lang             string      `json:"lang"`
	GeneratedAt      string      `json:"generated_at"`
	UpcomingMovies   any         `json:"upcoming_movies"`
	UpcomingTVSeries any         `json:"upcoming_tv_series"`
	PopularMovies    any         `json:"popular_movies"`
	Top10TRMovie     any         `json:"top10_tr_movie"`
	Top10TRTV        any         `json:"top10_tr_tv"`
	Genres           []homeGenre `json:"genres"`
}

// GET /api/home?x_lang= – the profile-independent home rows in one answer: upcoming movies and
// TV, popular movies, the Turkish Top 10 and the genre rows (tmdb.HomeGenreRows).
func (s *Service) home(c *gin.Context) {
	lang := httpserver.Lang(c)
	tm := s.tm.WithLang(lang)

	var (
		mu    sync.Mutex
		slots = map[string]any{}
		wg    sync.WaitGroup
	)
	run := func(name string, fetch func() (any, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("catalog: home %s panicked: %v", name, r)
				}
			}()
			v, err := fetch()
			if err != nil {
				log.Printf("catalog: home %s: %v", name, err)
				return
			}
			if v == nil {
				return
			}
			mu.Lock()
			slots[name] = v
			mu.Unlock()
		}()
	}

	if tm.Enabled() {
		run("upcoming_movies", func() (any, error) { return tm.GetUpcomingMovies() })
		run("upcoming_tv_series", func() (any, error) { return tm.GetUpcomingTVSeries() })
		run("popular_movies", func() (any, error) {
			movies, err := tm.GetPopularMovies(1)
			if err != nil {
				return nil, err
			}
			return gin.H{"results": movies, "page": 1, "total_results": len(movies)}, nil
		})
		for _, row := range tmdb.HomeGenreRows {
			run("genre:"+row.ID, func() (any, error) {
				q := row.Query()
				movies, err := tm.DiscoverMovies(q)
				if err != nil {
					return nil, err
				}
				return gin.H{"results": movies, "total_results": len(movies), "filters": q}, nil
			})
		}
		if src := s.top10; src != nil {
			for name, mediaType := range map[string]string{"top10_tr_movie": "movie", "top10_tr_tv": "tv"} {
				run(name, func() (any, error) {
					if list, ok := src("TR", mediaType, lang); ok {
						return list, nil
					}
					return nil, nil // not downloaded yet
				})
			}
		}
	}

	all := make(chan struct{})
	go func() {
		wg.Wait()
		close(all)
	}()
	timer := time.NewTimer(homeDeadline)
	defer timer.Stop()
	select {
	case <-all:
	case <-timer.C:
	}

	mu.Lock()
	slot := func(name string) any { return slots[name] }
	resp := homeResponse{
		Version:          1,
		Lang:             lang,
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		UpcomingMovies:   slot("upcoming_movies"),
		UpcomingTVSeries: slot("upcoming_tv_series"),
		PopularMovies:    slot("popular_movies"),
		Top10TRMovie:     slot("top10_tr_movie"),
		Top10TRTV:        slot("top10_tr_tv"),
	}
	for _, row := range tmdb.HomeGenreRows {
		resp.Genres = append(resp.Genres, homeGenre{ID: row.ID, Params: row.Params, Data: slot("genre:" + row.ID)})
	}
	mu.Unlock()

	complete := resp.UpcomingMovies != nil && resp.UpcomingTVSeries != nil && resp.PopularMovies != nil &&
		resp.Top10TRMovie != nil && resp.Top10TRTV != nil
	for _, g := range resp.Genres {
		complete = complete && g.Data != nil
	}
	h := c.Writer.Header()
	if complete {
		h.Set("Cache-Control", "private, max-age=60")
	} else {
		h.Set("Cache-Control", "no-store")
	}
	h.Add("Vary", "Accept-Encoding")
	c.JSON(http.StatusOK, resp)
}
