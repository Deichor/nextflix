package recommendations

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nextflix/internal/platform/tmdb"
)

// relatedTMDB answers /movie/{id}/recommendations and /similar; paths ending in a down suffix 404.
func relatedTMDB(t *testing.T, down ...string) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, d := range down {
			if strings.HasSuffix(r.URL.Path, d) {
				http.NotFound(w, r)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": 9, "title": "Nine", "overview": "x"}}})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("k").WithBaseURL(srv.URL)
}

func TestFetchRelatedPartial(t *testing.T) {
	out, err := fetchRelated(relatedTMDB(t, "/similar"), "movie", 5)
	if !errors.Is(err, tmdb.ErrPartial) || len(out.recs) != 1 || len(out.similar) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	// Through the cache the partial result is served without an error
	recs, sim, err := tmdbCatalog{relatedTMDB(t, "/similar")}.Related("en", "movie", 5)
	if err != nil || len(recs) != 1 || len(sim) != 0 {
		t.Fatalf("%v %v %v", recs, sim, err)
	}
}

func TestFetchRelatedBothFail(t *testing.T) {
	_, err := fetchRelated(relatedTMDB(t, "/similar", "/recommendations"), "movie", 5)
	if err == nil || errors.Is(err, tmdb.ErrPartial) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchRelatedComplete(t *testing.T) {
	out, err := fetchRelated(relatedTMDB(t), "movie", 5)
	if err != nil || len(out.recs) != 1 || len(out.similar) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}
