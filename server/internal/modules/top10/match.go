package top10

import (
	"regexp"
	"strings"
	"unicode"

	"nextflix/internal/platform/tmdb"
)

// Match is a TMDB search hit reduced to what the Top 10 rows show.
type Match struct {
	TmdbID        int
	MediaType     string
	Title         string
	OriginalTitle string
	Overview      string
	PosterPath    string
	BackdropPath  string
	VoteAverage   float64
	Date          string // release_date or first_air_date
	Popularity    float64
	Adult         bool
}

// Searcher finds TMDB titles by name (page 1 of /search/movie or /search/tv).
type Searcher interface {
	SearchMovies(query string) ([]Match, error)
	SearchTV(query string) ([]Match, error)
}

// Localized is the display text and artwork of a title in another language.
type Localized struct {
	Title        string
	Overview     string
	PosterPath   string
	BackdropPath string
}

// Localizer looks a title up in lang ("tr"); ok is false when it cannot.
type Localizer interface {
	Localize(lang, mediaType string, tmdbID int) (l Localized, ok bool)
}

type tmdbLocalizer struct{ tm *tmdb.Client }

// NewTMDBLocalizer returns a Localizer on the shared TMDB client (nil when TMDB is not configured).
// Details are cached by the client for 24h.
func NewTMDBLocalizer(tm *tmdb.Client) Localizer {
	if !tm.Enabled() {
		return nil
	}
	return tmdbLocalizer{tm}
}

func (l tmdbLocalizer) Localize(lang, mediaType string, id int) (Localized, bool) {
	tm := l.tm.WithLang(lang)
	if mediaType == "tv" {
		d, err := tm.GetTVDetails(id)
		if err != nil {
			return Localized{}, false
		}
		return Localized{d.Name, d.Overview, d.PosterPath, d.BackdropPath}, true
	}
	d, err := tm.GetMovieDetails(id)
	if err != nil {
		return Localized{}, false
	}
	return Localized{d.Title, d.Overview, d.PosterPath, d.BackdropPath}, true
}

type tmdbSearcher struct{ tm *tmdb.Client }

// NewTMDBSearcher returns a Searcher on the shared TMDB client (nil when TMDB is not configured).
func NewTMDBSearcher(tm *tmdb.Client) Searcher {
	if !tm.Enabled() {
		return nil
	}
	return tmdbSearcher{tm}
}

func (s tmdbSearcher) SearchMovies(q string) ([]Match, error) {
	res, err := s.tm.SearchMoviesOnly(q, 1)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(res.Results))
	for _, m := range res.Results {
		out = append(out, Match{TmdbID: m.ID, MediaType: "movie", Title: m.Title, OriginalTitle: m.OriginalTitle,
			Overview: m.Overview, PosterPath: m.PosterPath, BackdropPath: m.BackdropPath, VoteAverage: m.VoteAverage,
			Date: m.ReleaseDate, Popularity: m.Popularity, Adult: m.Adult})
	}
	return out, nil
}

func (s tmdbSearcher) SearchTV(q string) ([]Match, error) {
	res, err := s.tm.SearchTVOnly(q, 1)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(res.Results))
	for _, t := range res.Results {
		out = append(out, Match{TmdbID: t.ID, MediaType: "tv", Title: t.Name, OriginalTitle: t.OriginalName,
			Overview: t.Overview, PosterPath: t.PosterPath, BackdropPath: t.BackdropPath, VoteAverage: t.VoteAverage,
			Date: t.FirstAirDate, Popularity: t.Popularity, Adult: t.Adult})
	}
	return out, nil
}

// normTitle lower-cases a title and reduces it to letters and digits separated by single spaces.
func normTitle(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "&", " and ")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		if r != '\'' && r != '’' {
			space = true
		}
	}
	return b.String()
}

var seasonSuffix = regexp.MustCompile(`(?i):\s*(season|series|part|volume|vol\.|limited series|book|chapter|collection)\b.*$`)

// queries lists the search terms for a Netflix title: the title itself, the series name from
// season_title (TV) and the part before a colon ("Monster: The Lizzie Borden Story" -> "Monster").
func queries(e entry, mediaType string) []string {
	var out []string
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" || q == "N/A" {
			return
		}
		for _, have := range out {
			if strings.EqualFold(have, q) {
				return
			}
		}
		out = append(out, q)
	}
	add(e.Title)
	if mediaType == "tv" {
		add(seasonSuffix.ReplaceAllString(e.SeasonTitle, ""))
	}
	if i := strings.Index(e.Title, ":"); i > 0 {
		add(e.Title[:i])
	}
	return out
}

const looseCandidates = 5

// pickMatch prefers an exact (normalized) title match with the highest popularity, then the most
// popular of TMDB's first few results. Adult titles are never picked.
func pickMatch(query string, hits []Match) (Match, bool, bool) {
	q := normTitle(query)
	var best Match
	found := false
	for _, h := range hits {
		if h.Adult {
			continue
		}
		if normTitle(h.Title) != q && normTitle(h.OriginalTitle) != q {
			continue
		}
		if !found || h.Popularity > best.Popularity {
			best, found = h, true
		}
	}
	if found {
		return best, true, true
	}
	for i, h := range hits {
		if i >= looseCandidates {
			break
		}
		if h.Adult {
			continue
		}
		if !found || h.Popularity > best.Popularity {
			best, found = h, true
		}
	}
	return best, found, false
}
