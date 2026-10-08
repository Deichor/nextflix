package recommendations

import (
	"errors"
	"fmt"
	"net/url"

	"nextflix/internal/platform/tmdb"
)

type tmdbCatalog struct{ tm *tmdb.Client }

// NewTMDBCatalog returns a Catalog on the shared TMDB client (24 h cache).
func NewTMDBCatalog(tm *tmdb.Client) Catalog { return tmdbCatalog{tm} }

type related struct{ recs, similar []Item }

func (c tmdbCatalog) Related(lang, mediaType string, id int) ([]Item, []Item, error) {
	tm := c.tm.WithLang(lang)
	r, err := tmdb.Cached(tm, fmt.Sprintf("recs_related_%s_%d", mediaType, id), func() (related, error) {
		var out related
		var errRecs, errSim error
		if mediaType == "tv" {
			var recs, sim *tmdb.RelatedTV
			if recs, errRecs = tm.GetRecommendedTVShows(id, 1); errRecs == nil {
				out.recs = fromTVs(recs.Results)
			}
			if sim, errSim = tm.GetSimilarTVShows(id, 1); errSim == nil {
				out.similar = fromTVs(sim.Results)
			}
		} else {
			var recs, sim *tmdb.RelatedMovies
			if recs, errRecs = tm.GetRecommendedMovies(id, 1); errRecs == nil {
				out.recs = fromMovies(recs.Results)
			}
			if sim, errSim = tm.GetSimilarMovies(id, 1); errSim == nil {
				out.similar = fromMovies(sim.Results)
			}
		}
		if errRecs != nil && errSim != nil {
			return out, errors.Join(errRecs, errSim)
		}
		return out, nil
	})
	return r.recs, r.similar, err
}

func (c tmdbCatalog) Genres(mediaType string, id int) ([]int, error) {
	var genres []tmdb.Genre
	if mediaType == "tv" {
		d, err := c.tm.GetTVDetails(id)
		if err != nil {
			return nil, err
		}
		genres = d.Genres
	} else {
		d, err := c.tm.GetMovieDetails(id)
		if err != nil {
			return nil, err
		}
		genres = d.Genres
	}
	ids := make([]int, 0, len(genres))
	for _, g := range genres {
		ids = append(ids, g.ID)
	}
	return ids, nil
}

type trendingResult struct {
	ID               int     `json:"id"`
	MediaType        string  `json:"media_type"`
	Title            string  `json:"title"`
	Name             string  `json:"name"`
	OriginalTitle    string  `json:"original_title"`
	OriginalName     string  `json:"original_name"`
	OriginalLanguage string  `json:"original_language"`
	Overview         string  `json:"overview"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
	Popularity       float64 `json:"popularity"`
	ReleaseDate      string  `json:"release_date"`
	FirstAirDate     string  `json:"first_air_date"`
	GenreIDs         []int   `json:"genre_ids"`
	Adult            bool    `json:"adult"`
}

func (c tmdbCatalog) Trending(lang string) ([]Item, error) {
	tm := c.tm.WithLang(lang)
	return tmdb.Cached(tm, "recs_trending_all_week", func() ([]Item, error) {
		var res struct {
			Results []trendingResult `json:"results"`
		}
		if err := tm.Get("/trending/all/week", url.Values{"language": {tm.Language()}}, &res); err != nil {
			return nil, err
		}
		c.fillTrending(tm.Lang(), res.Results)
		out := make([]Item, 0, len(res.Results))
		for _, r := range res.Results {
			switch r.MediaType {
			case "movie":
				out = append(out, Item{ID: r.ID, MediaType: "movie", Title: r.Title, Overview: r.Overview,
					PosterPath: r.PosterPath, BackdropPath: r.BackdropPath, VoteAverage: r.VoteAverage,
					ReleaseDate: r.ReleaseDate, GenreIDs: r.GenreIDs, Popularity: r.Popularity,
					VoteCount: r.VoteCount, Adult: r.Adult})
			case "tv":
				out = append(out, Item{ID: r.ID, MediaType: "tv", Title: r.Name, Overview: r.Overview,
					PosterPath: r.PosterPath, BackdropPath: r.BackdropPath, VoteAverage: r.VoteAverage,
					FirstAirDate: r.FirstAirDate, GenreIDs: r.GenreIDs, Popularity: r.Popularity,
					VoteCount: r.VoteCount, Adult: r.Adult})
			}
		}
		return out, nil
	})
}

func fromMovies(ms []tmdb.Movie) []Item {
	out := make([]Item, 0, len(ms))
	for _, m := range ms {
		out = append(out, Item{ID: m.ID, MediaType: "movie", Title: m.Title, Overview: m.Overview,
			PosterPath: m.PosterPath, BackdropPath: m.BackdropPath, VoteAverage: m.VoteAverage,
			ReleaseDate: m.ReleaseDate, GenreIDs: m.GenreIDs, Popularity: m.Popularity,
			VoteCount: m.VoteCount, Adult: m.Adult})
	}
	return out
}

func fromTVs(ts []tmdb.TV) []Item {
	out := make([]Item, 0, len(ts))
	for _, t := range ts {
		out = append(out, Item{ID: t.ID, MediaType: "tv", Title: t.Name, Overview: t.Overview,
			PosterPath: t.PosterPath, BackdropPath: t.BackdropPath, VoteAverage: t.VoteAverage,
			FirstAirDate: t.FirstAirDate, GenreIDs: t.GenreIDs, Popularity: t.Popularity,
			VoteCount: t.VoteCount, Adult: t.Adult})
	}
	return out
}

// fillTrending fills empty overviews and untranslated titles of a localized trending list
// from the English one (fetched only when needed, cached).
func (c tmdbCatalog) fillTrending(lang string, items []trendingResult) {
	if lang == tmdb.LangEN {
		return
	}
	text := func(r *trendingResult) (*string, string) {
		if r.MediaType == "tv" {
			return &r.Name, r.OriginalName
		}
		return &r.Title, r.OriginalTitle
	}
	var en map[int]Item
	for i := range items {
		r := &items[i]
		title, original := text(r)
		if !tmdb.MissingText(lang, *title, original, r.OriginalLanguage, r.Overview) {
			continue
		}
		if en == nil {
			list, err := c.Trending(tmdb.LangEN)
			if err != nil {
				return
			}
			en = make(map[int]Item, len(list))
			for _, it := range list {
				en[it.ID] = it
			}
		}
		if e, ok := en[r.ID]; ok && e.MediaType == r.MediaType {
			tmdb.FillText(lang, title, &r.Overview, original, r.OriginalLanguage, e.Title, e.Overview)
		}
	}
}
