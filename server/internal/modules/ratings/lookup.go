package ratings

import (
	"context"
	"log"

	"nextflix/internal/platform/tmdb"
)

// TitleMeta is the display data stored with a rating.
type TitleMeta struct {
	Title        string
	PosterPath   string
	BackdropPath string
}

// TitleLookup fetches display data for rows saved without it (migrated My List entries).
type TitleLookup interface {
	Lookup(mediaType string, tmdbID int) (TitleMeta, error)
}

type tmdbLookup struct{ tm *tmdb.Client }

// NewTMDBLookup returns a TitleLookup backed by the shared TMDB client (nil when TMDB is not configured).
func NewTMDBLookup(tm *tmdb.Client) TitleLookup {
	if !tm.Enabled() {
		return nil
	}
	return tmdbLookup{tm}
}

func (l tmdbLookup) Lookup(mediaType string, id int) (TitleMeta, error) {
	if mediaType == "tv" {
		d, err := l.tm.GetTVDetails(id)
		if err != nil {
			return TitleMeta{}, err
		}
		return TitleMeta{d.Name, d.PosterPath, d.BackdropPath}, nil
	}
	d, err := l.tm.GetMovieDetails(id)
	if err != nil {
		return TitleMeta{}, err
	}
	return TitleMeta{d.Title, d.PosterPath, d.BackdropPath}, nil
}

const backfillLimit = 500

// Backfill fills empty titles and artwork from TMDB. Stored values are never overwritten.
func (s *Service) Backfill(ctx context.Context) {
	if s.titles == nil {
		return
	}
	var rows []TitleRating
	if err := s.db.Where("title = '' OR poster_path = ''").Limit(backfillLimit).Find(&rows).Error; err != nil {
		log.Printf("ratings: backfill query: %v", err)
		return
	}
	filled := 0
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		meta, err := s.titles.Lookup(r.MediaType, r.TmdbID)
		if err != nil {
			continue
		}
		updates := map[string]any{}
		if r.Title == "" && meta.Title != "" {
			updates["title"] = meta.Title
		}
		if r.PosterPath == "" && meta.PosterPath != "" {
			updates["poster_path"] = meta.PosterPath
		}
		if r.BackdropPath == "" && meta.BackdropPath != "" {
			updates["backdrop_path"] = meta.BackdropPath
		}
		if len(updates) == 0 {
			continue
		}
		// UpdateColumns keeps updated_at, so list order is not disturbed
		if err := s.db.Model(&TitleRating{}).Where("id = ?", r.ID).UpdateColumns(updates).Error; err == nil {
			filled++
		}
	}
	if filled > 0 {
		log.Printf("ratings: filled title data for %d of %d rows from TMDB", filled, len(rows))
	}
}
