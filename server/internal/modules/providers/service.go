package providers

import (
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"

	"nextflix/internal/platform/tmdb"
)

// SupportedRegions are merged into one availability view.
var SupportedRegions = []string{"US", "IN", "BD"}

// DiscoverResult is a TV show from TMDB /discover/tv.
type DiscoverResult struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	OriginalName     string  `json:"original_name"`
	Overview         string  `json:"overview"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	FirstAirDate     string  `json:"first_air_date,omitempty"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
	Popularity       float64 `json:"popularity"`
	GenreIDs         []int   `json:"genre_ids"`
	OriginalLanguage string  `json:"original_language"`
	MediaType        string  `json:"media_type,omitempty"`
}

// ContentResponse is the TV content of one provider merged across regions.
type ContentResponse struct {
	ProviderID   int              `json:"provider_id"`
	Regions      []string         `json:"regions"`
	Page         int              `json:"page"`
	TotalPages   int              `json:"total_pages"`
	TotalResults int              `json:"total_results"`
	Results      []DiscoverResult `json:"results"`
}

type Service struct {
	tm *tmdb.Client
}

func NewService(tm *tmdb.Client) *Service {
	return &Service{tm: tm}
}

// DiscoverTV returns a provider's TV shows merged across regions, sorted by popularity, with
// names and overviews in lang ("en" or "tr"). The result is cached and shared: copy before modifying.
func (s *Service) DiscoverTV(lang string, providerID, page int, sortBy string) (*ContentResponse, error) {
	tm := s.tm.WithLang(lang)
	if sortBy == "" {
		sortBy = "popularity.desc"
	}
	key := fmt.Sprintf("discover_tv_provider_%d_page_%d_sort_%s", providerID, page, sortBy)
	return tmdb.Cached(tm, key, func() (*ContentResponse, error) {
		byID := map[int]DiscoverResult{}
		totalResults, totalPages := 0, 0
		for _, region := range SupportedRegions {
			if !tm.Enabled() {
				break
			}
			params := url.Values{
				"with_watch_providers": {strconv.Itoa(providerID)},
				"watch_region":         {region},
				"page":                 {strconv.Itoa(page)},
				"sort_by":              {sortBy},
				"language":             {tm.Language()},
			}
			var res struct {
				TotalPages   int              `json:"total_pages"`
				TotalResults int              `json:"total_results"`
				Results      []DiscoverResult `json:"results"`
			}
			if err := tm.Get("/discover/tv", params, &res); err != nil {
				log.Printf("providers: discover tv for provider %d in %s: %v", providerID, region, err)
				continue
			}
			for _, r := range res.Results {
				if _, ok := byID[r.ID]; !ok {
					r.MediaType = "tv"
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
		if tm.Lang() != tmdb.LangEN {
			s.fillFromEnglish(tm.Lang(), results, providerID, page, sortBy)
		}

		return &ContentResponse{
			ProviderID:   providerID,
			Regions:      SupportedRegions,
			Page:         page,
			TotalPages:   totalPages,
			TotalResults: totalResults,
			Results:      results,
		}, nil
	})
}

// withImages returns a copy of resp whose poster/backdrop paths are full image URLs.
func withImages(resp *ContentResponse) ContentResponse {
	out := *resp
	out.Results = make([]DiscoverResult, len(resp.Results))
	for i, r := range resp.Results {
		r.PosterPath = tmdb.ImageURL(r.PosterPath, "w500")
		r.BackdropPath = tmdb.ImageURL(r.BackdropPath, "w1280")
		out.Results[i] = r
	}
	return out
}

// fillFromEnglish fills empty overviews and untranslated names from the English answer
// (fetched only when a show needs it, cached).
func (s *Service) fillFromEnglish(lang string, results []DiscoverResult, providerID, page int, sortBy string) {
	var en map[int]DiscoverResult
	for i := range results {
		r := &results[i]
		if !tmdb.MissingText(lang, r.Name, r.OriginalName, r.OriginalLanguage, r.Overview) {
			continue
		}
		if en == nil {
			resp, err := s.DiscoverTV(tmdb.LangEN, providerID, page, sortBy)
			if err != nil {
				return
			}
			en = make(map[int]DiscoverResult, len(resp.Results))
			for _, e := range resp.Results {
				en[e.ID] = e
			}
		}
		if e, ok := en[r.ID]; ok {
			tmdb.FillText(lang, &r.Name, &r.Overview, r.OriginalName, r.OriginalLanguage, e.Name, e.Overview)
		}
	}
}
