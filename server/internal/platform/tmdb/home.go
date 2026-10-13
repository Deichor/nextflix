package tmdb

// HomeGenreRow is one genre row of the home screen: a /discover/movie query with a stable id.
type HomeGenreRow struct {
	ID     string
	Params map[string]string
}

// HomeGenreRows are the home screen's genre rows in order. GET /api/home, the image warmer and the
// clients' fallback (GENRE_ROWS in web/src/features/home/HomeScreen.tsx) use these.
var HomeGenreRows = []HomeGenreRow{
	{"action_adventure", map[string]string{"with_genres": "28|12", "vote_count.gte": "300"}},
	{"comedies", map[string]string{"with_genres": "35", "vote_count.gte": "300"}},
	{"scifi_fantasy", map[string]string{"with_genres": "878|14", "vote_count.gte": "300"}},
	{"horror", map[string]string{"with_genres": "27", "vote_count.gte": "200"}},
	{"acclaimed", map[string]string{"sort_by": "vote_average.desc", "vote_count.gte": "5000"}},
}

// Query returns the row's DiscoverMovies params: popularity order and no adult titles unless the
// row says otherwise. It equals what GET /api/tmdb/discover/movie passes on, so the cache is shared.
func (r HomeGenreRow) Query() map[string]string {
	q := map[string]string{"sort_by": "popularity.desc", "include_adult": "false"}
	for k, v := range r.Params {
		q[k] = v
	}
	return q
}
