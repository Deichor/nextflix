package providers

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	showcaseKey = "providers:showcase"
	showcaseTTL = 15 * time.Minute
)

type ShowcaseItem struct {
	Provider WatchProvider    `json:"provider"`
	Content  []DiscoverResult `json:"content"`
}

type showcaseResponse struct {
	Showcase []ShowcaseItem `json:"showcase"`
}

func providerID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid provider ID"})
		return 0, false
	}
	return id, true
}

func pageParam(c *gin.Context) int {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	return max(page, 1)
}

func providerJSON(p WatchProvider) gin.H {
	return gin.H{
		"id":               p.ProviderID,
		"name":             p.ProviderName,
		"logo_path":        p.LogoPath,
		"logo_url":         logoURL(p.LogoPath),
		"display_priority": p.DisplayPriority,
		"primary_color":    p.PrimaryColor,
		"secondary_color":  p.SecondaryColor,
	}
}

// GET /api/providers
func (s *Service) list(c *gin.Context) {
	providers, err := s.AllProviders()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, len(providers))
	for i, p := range providers {
		out[i] = providerJSON(p)
	}
	c.JSON(http.StatusOK, gin.H{"providers": out, "regions": SupportedRegions})
}

// GET /api/providers/showcase – top 8 providers with up to 15 interleaved movie/TV titles each.
func (s *Service) showcase(c *gin.Context) {
	var cached showcaseResponse
	if ok, err := s.cache.GetJSON(c.Request.Context(), showcaseKey, &cached); err == nil && ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	providers, err := s.AllProviders()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(providers) > 8 {
		providers = providers[:8]
	}

	items := make([]ShowcaseItem, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p WatchProvider) {
			defer wg.Done()
			var movies, tv []DiscoverResult
			var inner sync.WaitGroup
			inner.Add(2)
			go func() {
				defer inner.Done()
				if r, err := s.Trending(p.ProviderID, "movie", 1); err == nil {
					movies = r.Results
				}
			}()
			go func() {
				defer inner.Done()
				if r, err := s.Trending(p.ProviderID, "tv", 1); err == nil {
					tv = r.Results
				}
			}()
			inner.Wait()

			p.LogoPath = logoURL(p.LogoPath)
			items[i] = ShowcaseItem{Provider: p, Content: interleave(movies, tv, 15)}
		}(i, p)
	}
	wg.Wait()

	resp := showcaseResponse{Showcase: items}
	if len(items) > 0 {
		go s.cache.SetJSON(context.Background(), showcaseKey, resp, showcaseTTL)
	}
	c.JSON(http.StatusOK, resp)
}

// interleave alternates movie and TV results (with full image URLs) up to limit items.
func interleave(movies, tv []DiscoverResult, limit int) []DiscoverResult {
	movies, tv = imageResults(movies), imageResults(tv)
	out := make([]DiscoverResult, 0, limit)
	m, t := 0, 0
	for len(out) < limit && (m < len(movies) || t < len(tv)) {
		if m < len(movies) {
			movies[m].MediaType = "movie"
			out = append(out, movies[m])
			m++
		}
		if t < len(tv) {
			tv[t].MediaType = "tv"
			out = append(out, tv[t])
			t++
		}
	}
	return out
}

// GET /api/providers/:id
func (s *Service) get(c *gin.Context) {
	id, ok := providerID(c)
	if !ok {
		return
	}
	p, err := s.ProviderByID(id, "movie")
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	body := providerJSON(*p)
	body["regions"] = SupportedRegions
	c.JSON(http.StatusOK, body)
}

// contentList serves one provider list (movies, tv, trending, ...) with full image URLs.
func (s *Service) contentList(fetch func(c *gin.Context, id, page int) (*ProviderContentResponse, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := providerID(c)
		if !ok {
			return
		}
		res, err := fetch(c, id, pageParam(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, withImages(res))
	}
}

func (s *Service) discoverBy(mediaType string) gin.HandlerFunc {
	return s.contentList(func(c *gin.Context, id, page int) (*ProviderContentResponse, error) {
		return s.Discover(id, mediaType, page, c.DefaultQuery("sort_by", "popularity.desc"))
	})
}

func (s *Service) typed(fetch func(id int, mediaType string, page int) (*ProviderContentResponse, error)) gin.HandlerFunc {
	return s.contentList(func(c *gin.Context, id, page int) (*ProviderContentResponse, error) {
		return fetch(id, c.DefaultQuery("type", "movie"), page)
	})
}

// GET /api/providers/:id/content – movies + TV of a provider; the first 10 of each carry a trailer URL.
func (s *Service) content(c *gin.Context) {
	id, ok := providerID(c)
	if !ok {
		return
	}
	page := pageParam(c)

	movies, moviesErr := s.Discover(id, "movie", page, "popularity.desc")
	tv, tvErr := s.Discover(id, "tv", page, "popularity.desc")
	if moviesErr != nil && tvErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch content"})
		return
	}

	movieResults, tvResults := []DiscoverResult{}, []DiscoverResult{}
	var wg sync.WaitGroup
	if moviesErr == nil {
		movieResults = imageResults(movies.Results)
		wg.Add(1)
		go func() { defer wg.Done(); s.addTrailers(movieResults, "movie", 10) }()
	}
	if tvErr == nil {
		tvResults = imageResults(tv.Results)
		wg.Add(1)
		go func() { defer wg.Done(); s.addTrailers(tvResults, "tv", 10) }()
	}
	wg.Wait()

	info := gin.H{"id": id, "name": ""}
	if p, err := s.ProviderByID(id, "movie"); err == nil {
		info = gin.H{
			"id":              p.ProviderID,
			"name":            p.ProviderName,
			"logo_url":        logoURL(p.LogoPath),
			"primary_color":   p.PrimaryColor,
			"secondary_color": p.SecondaryColor,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"provider": info,
		"movies":   movieResults,
		"tv_shows": tvResults,
		"page":     page,
		"regions":  SupportedRegions,
	})
}

// GET /api/content/:type/:id/providers
func (s *Service) contentProviders(c *gin.Context) {
	mediaType := c.Param("type")
	if mediaType != "movie" && mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid media type. Use 'movie' or 'tv'"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid content ID"})
		return
	}
	availability, err := s.ContentProviders(id, mediaType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "media_type": mediaType, "availability": availability})
}
