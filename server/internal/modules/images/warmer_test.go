package images

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nextflix/internal/platform/tmdb"
)

func TestHomeKeysCoverTop10AndComingSoon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/images") {
			json.NewEncoder(w).Encode(map[string]any{"logos": []any{}})
			return
		}
		name := strings.NewReplacer("/", "_").Replace(strings.TrimPrefix(r.URL.Path, "/"))
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
			"id": 1, "title": "One", "name": "One", "overview": "x",
			"poster_path": "/" + name + "_p.jpg", "backdrop_path": "/" + name + "_b.jpg",
		}}})
	}))
	t.Cleanup(srv.Close)
	tm := tmdb.New("k").WithBaseURL(srv.URL)
	s := NewService(Config{Dir: t.TempDir(), Top10: func(lang string) []Art {
		return []Art{{Poster: "/top_" + lang + "_p.jpg", Backdrop: "/top_" + lang + "_b.jpg"}}
	}}, tm)

	keys := map[string]int{}
	for i, k := range s.homeKeys(tm) {
		keys[k.String()] = i
	}
	for _, want := range []string{
		"w342/top_en_p.jpg", "w780/top_en_b.jpg",
		"w342/movie_upcoming_p.jpg", "w500/movie_upcoming_b.jpg", "w780/movie_upcoming_b.jpg",
		"w300/trending_movie_day_b.jpg", "w500/discover_movie_b.jpg",
	} {
		if _, ok := keys[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	// On-screen order: trending, Top 10, cards, coming soon, genres
	order := []string{"w300/trending_movie_day_b.jpg", "w342/top_en_p.jpg", "w300/movie_popular_b.jpg",
		"w342/movie_upcoming_p.jpg", "w300/discover_movie_b.jpg"}
	for i := 1; i < len(order); i++ {
		if keys[order[i-1]] >= keys[order[i]] {
			t.Errorf("%s should come before %s", order[i-1], order[i])
		}
	}
}
