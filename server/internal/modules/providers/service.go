package providers

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"nextflix/internal/platform/cache"
	"nextflix/internal/platform/tmdb"
)

// SupportedRegions are merged into one availability view.
var SupportedRegions = []string{"US", "IN", "BD"}

type brandColors struct{ primary, secondary string }

var providerColors = map[int]brandColors{
	8:   {"#E50914", "#B20710"}, // Netflix
	9:   {"#00A8E1", "#146EB4"}, // Amazon Prime Video
	119: {"#00A8E1", "#146EB4"}, // Amazon Prime Video (alternate)
	337: {"#0063E5", "#040714"}, // Disney+
	15:  {"#1CE783", "#040405"}, // Hulu
	318: {"#5822B4", "#000000"}, // Max
	350: {"#000000", "#FFFFFF"}, // Apple TV+
	531: {"#6B5CE7", "#1A1A2E"}, // Paramount+
	386: {"#FFD700", "#0057B8"}, // Peacock
	283: {"#DF0024", "#000000"}, // Crunchyroll
	192: {"#E6121D", "#000000"}, // YouTube Premium
}

type WatchProvider struct {
	ProviderID      int    `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	LogoPath        string `json:"logo_path"`
	DisplayPriority int    `json:"display_priority"`
	PrimaryColor    string `json:"primary_color"`
	SecondaryColor  string `json:"secondary_color"`
}

func (p *WatchProvider) applyColors() {
	colors, ok := providerColors[p.ProviderID]
	if !ok {
		colors = brandColors{"#1a1a1a", "#333333"}
	}
	p.PrimaryColor, p.SecondaryColor = colors.primary, colors.secondary
}

type ProviderAvailability struct {
	Link     string          `json:"link"`
	Flatrate []WatchProvider `json:"flatrate,omitempty"`
	Rent     []WatchProvider `json:"rent,omitempty"`
	Buy      []WatchProvider `json:"buy,omitempty"`
	Free     []WatchProvider `json:"free,omitempty"`
	Ads      []WatchProvider `json:"ads,omitempty"`
}

// DiscoverResult is a movie (title/release_date) or TV show (name/first_air_date) from /discover.
type DiscoverResult struct {
	ID               int     `json:"id"`
	Title            string  `json:"title,omitempty"`
	Name             string  `json:"name,omitempty"`
	Overview         string  `json:"overview"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	ReleaseDate      string  `json:"release_date,omitempty"`
	FirstAirDate     string  `json:"first_air_date,omitempty"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
	Popularity       float64 `json:"popularity"`
	GenreIDs         []int   `json:"genre_ids"`
	OriginalLanguage string  `json:"original_language"`
	MediaType        string  `json:"media_type,omitempty"`
	TrailerURL       string  `json:"trailer_url,omitempty"`
}

// ProviderContentResponse is discover content of one provider merged across regions.
type ProviderContentResponse struct {
	ProviderID   int              `json:"provider_id"`
	ProviderName string           `json:"provider_name"`
	Regions      []string         `json:"regions"`
	Page         int              `json:"page"`
	TotalPages   int              `json:"total_pages"`
	TotalResults int              `json:"total_results"`
	Results      []DiscoverResult `json:"results"`
}

var errNoProviders = errors.New("no providers returned by TMDB")

type Service struct {
	apiKey string
	cache  *cache.Cache
	tm     *tmdb.Client // own client: its 24h in-memory cache holds the provider lookups
}

func NewService(apiKey string, c *cache.Cache) *Service {
	return &Service{apiKey: apiKey, cache: c, tm: tmdb.New(apiKey)}
}

// WatchProviders lists the providers for "movie" or "tv", merged across regions and sorted by priority.
func (s *Service) WatchProviders(mediaType string) ([]WatchProvider, error) {
	providers, err := tmdb.Cached(s.tm, fmt.Sprintf("providers_%s_combined", mediaType), func() ([]WatchProvider, error) {
		if !s.tm.Enabled() {
			return nil, errNoProviders
		}
		byID := map[int]WatchProvider{}
		for _, region := range SupportedRegions {
			var res struct {
				Results []WatchProvider `json:"results"`
			}
			if err := s.tm.Get("/watch/providers/"+mediaType, url.Values{"watch_region": {region}}, &res); err != nil {
				log.Printf("providers: list for region %s: %v", region, err)
				continue
			}
			for _, p := range res.Results {
				p.applyColors()
				if existing, ok := byID[p.ProviderID]; !ok || p.DisplayPriority < existing.DisplayPriority {
					byID[p.ProviderID] = p
				}
			}
		}
		if len(byID) == 0 {
			return nil, errNoProviders
		}
		return sortedProviders(byID), nil
	})
	if errors.Is(err, errNoProviders) {
		// Empty, but not cached so a later request can retry.
		return []WatchProvider{}, nil
	}
	return providers, err
}

func sortedProviders(byID map[int]WatchProvider) []WatchProvider {
	out := make([]WatchProvider, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayPriority < out[j].DisplayPriority })
	return out
}

func (s *Service) ProviderByID(providerID int, mediaType string) (*WatchProvider, error) {
	providers, err := s.WatchProviders(mediaType)
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		if p.ProviderID == providerID {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("provider with ID %d not found", providerID)
}

// AllProviders merges the movie and TV provider lists.
func (s *Service) AllProviders() ([]WatchProvider, error) {
	movie, err := s.WatchProviders("movie")
	if err != nil {
		return nil, err
	}
	tv, err := s.WatchProviders("tv")
	if err != nil {
		return nil, err
	}
	byID := map[int]WatchProvider{}
	for _, p := range movie {
		byID[p.ProviderID] = p
	}
	for _, p := range tv {
		if _, ok := byID[p.ProviderID]; !ok {
			byID[p.ProviderID] = p
		}
	}
	return sortedProviders(byID), nil
}

// Discover returns a provider's content merged across regions, sorted by popularity.
// The result is cached and shared: copy before modifying.
func (s *Service) Discover(providerID int, mediaType string, page int, sortBy string) (*ProviderContentResponse, error) {
	if sortBy == "" {
		sortBy = "popularity.desc"
	}
	key := fmt.Sprintf("discover_%s_provider_%d_page_%d_sort_%s", mediaType, providerID, page, sortBy)
	return tmdb.Cached(s.tm, key, func() (*ProviderContentResponse, error) {
		byID := map[int]DiscoverResult{}
		totalResults, totalPages := 0, 0
		for _, region := range SupportedRegions {
			if !s.tm.Enabled() {
				break
			}
			params := url.Values{
				"with_watch_providers": {strconv.Itoa(providerID)},
				"watch_region":         {region},
				"page":                 {strconv.Itoa(page)},
				"sort_by":              {sortBy},
			}
			var res struct {
				Page         int              `json:"page"`
				TotalPages   int              `json:"total_pages"`
				TotalResults int              `json:"total_results"`
				Results      []DiscoverResult `json:"results"`
			}
			if err := s.tm.Get("/discover/"+mediaType, params, &res); err != nil {
				log.Printf("providers: discover %s for provider %d in %s: %v", mediaType, providerID, region, err)
				continue
			}
			for _, r := range res.Results {
				if _, ok := byID[r.ID]; !ok {
					r.MediaType = mediaType
					byID[r.ID] = r
				}
			}
			totalResults = max(totalResults, res.TotalResults)
			totalPages = max(totalPages, res.TotalPages)
		}

		results := make([]DiscoverResult, 0, len(byID))
		for _, r := range byID {
			results = append(results, r)
		}
		sort.Slice(results, func(i, j int) bool { return results[i].Popularity > results[j].Popularity })

		name := ""
		if p, err := s.ProviderByID(providerID, mediaType); err == nil {
			name = p.ProviderName
		}
		return &ProviderContentResponse{
			ProviderID:   providerID,
			ProviderName: name,
			Regions:      SupportedRegions,
			Page:         page,
			TotalPages:   totalPages,
			TotalResults: totalResults,
			Results:      results,
		}, nil
	})
}

func (s *Service) Trending(id int, mediaType string, page int) (*ProviderContentResponse, error) {
	return s.Discover(id, mediaType, page, "popularity.desc")
}

func (s *Service) Popular(id int, mediaType string, page int) (*ProviderContentResponse, error) {
	return s.Discover(id, mediaType, page, "vote_count.desc")
}

func (s *Service) TopRated(id int, mediaType string, page int) (*ProviderContentResponse, error) {
	return s.Discover(id, mediaType, page, "vote_average.desc")
}

func (s *Service) NewReleases(id int, mediaType string, page int) (*ProviderContentResponse, error) {
	if mediaType == "movie" {
		return s.Discover(id, mediaType, page, "primary_release_date.desc")
	}
	return s.Discover(id, mediaType, page, "first_air_date.desc")
}

// ContentProviders returns where a title can be watched, limited to SupportedRegions.
func (s *Service) ContentProviders(contentID int, mediaType string) (map[string]ProviderAvailability, error) {
	key := fmt.Sprintf("content_providers_%s_%d", mediaType, contentID)
	return tmdb.Cached(s.tm, key, func() (map[string]ProviderAvailability, error) {
		var res struct {
			Results map[string]ProviderAvailability `json:"results"`
		}
		if err := s.tm.Get(fmt.Sprintf("/%s/%d/watch/providers", mediaType, contentID), nil, &res); err != nil {
			return nil, err
		}
		out := map[string]ProviderAvailability{}
		for _, region := range SupportedRegions {
			a, ok := res.Results[region]
			if !ok {
				continue
			}
			for _, list := range [][]WatchProvider{a.Flatrate, a.Rent, a.Buy, a.Free, a.Ads} {
				for i := range list {
					list[i].applyColors()
				}
			}
			out[region] = a
		}
		return out, nil
	})
}

// TrailerURL returns the YouTube URL of a title's first trailer or teaser, or "".
func (s *Service) TrailerURL(contentID int, mediaType string) string {
	key := fmt.Sprintf("trailer_%s_%d", mediaType, contentID)
	u, _ := tmdb.Cached(s.tm, key, func() (string, error) {
		var v tmdb.Videos
		if err := s.tm.Get(fmt.Sprintf("/%s/%d/videos", mediaType, contentID), nil, &v); err != nil {
			return "", err
		}
		for _, video := range v.Results {
			if video.Site == "YouTube" && (video.Type == "Trailer" || video.Type == "Teaser") {
				return "https://www.youtube.com/watch?v=" + video.Key, nil
			}
		}
		return "", nil
	})
	return u
}

// addTrailers fills TrailerURL for the first n results concurrently.
func (s *Service) addTrailers(results []DiscoverResult, mediaType string, n int) {
	var wg sync.WaitGroup
	for i := range results[:min(n, len(results))] {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].TrailerURL = s.TrailerURL(results[i].ID, mediaType)
		}(i)
	}
	wg.Wait()
}

func logoURL(path string) string {
	return tmdb.ImageURL(path, "w185")
}

// withImages returns a copy of resp whose poster/backdrop paths are full image URLs.
func withImages(resp *ProviderContentResponse) ProviderContentResponse {
	out := *resp
	out.Results = imageResults(resp.Results)
	return out
}

func imageResults(in []DiscoverResult) []DiscoverResult {
	out := make([]DiscoverResult, len(in))
	for i, r := range in {
		r.PosterPath = tmdb.ImageURL(r.PosterPath, "w500")
		r.BackdropPath = tmdb.ImageURL(r.BackdropPath, "w1280")
		out[i] = r
	}
	return out
}
