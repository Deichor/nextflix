package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"nextflix/internal/platform/tmdb"
)

func TestAffinityAllZeroIsEmpty(t *testing.T) {
	genres := func(string, int) []int { return []int{28} }
	rated := []RatedTitle{{MediaType: "movie", TmdbID: 1, Rating: "love"}, {MediaType: "movie", TmdbID: 2, Rating: "dislike"}}
	aff := affinityOf(rated, genres)
	if len(aff) != 0 {
		t.Fatalf("affinity = %v, want empty", aff)
	}
	aff = affinityOf([]RatedTitle{{MediaType: "movie", TmdbID: 1, Rating: "like"}}, genres)
	if aff[28] != 1 {
		t.Fatalf("affinity = %v", aff)
	}
	for _, v := range aff {
		if math.IsNaN(v) {
			t.Fatal("NaN affinity")
		}
	}
}

// genreTMDB answers /movie/{id} with the genre {id}.
func genreTMDB(t *testing.T) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/movie/"), "%d", &id)
		json.NewEncoder(w).Encode(map[string]any{"id": id, "title": "T", "genres": []map[string]any{{"id": id}}})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("k").WithBaseURL(srv.URL)
}

func TestInvalidateTasteDropsRunningCompute(t *testing.T) {
	s := NewService(genreTMDB(t))
	release := make(chan struct{})
	calls := make(chan int32, 4)
	var n atomic.Int32
	s.SetRatings(func(uint) ([]RatedTitle, error) {
		call := n.Add(1)
		calls <- call
		if call == 1 {
			<-release // the old ratings: genre 28
			return []RatedTitle{{MediaType: "movie", TmdbID: 28, Rating: "love"}}, nil
		}
		return []RatedTitle{{MediaType: "movie", TmdbID: 35, Rating: "love"}}, nil
	})
	s.taste.affinity(7)
	<-calls
	s.InvalidateTaste(7)
	s.taste.affinity(7) // a new compute starts at once
	<-calls
	waitTaste(t, s, 7, 35)
	close(release)
	// The old compute finishes and must not overwrite the new result
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.taste.mu.Lock()
		_, busy := s.taste.running[7]
		aff := s.taste.entries[7].affinity
		s.taste.mu.Unlock()
		if aff[28] != 0 {
			t.Fatalf("stale taste stored: %v", aff)
		}
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	s.taste.mu.Lock()
	aff := s.taste.entries[7].affinity
	s.taste.mu.Unlock()
	if aff[35] != 1 || aff[28] != 0 {
		t.Fatalf("affinity = %v, want genre 35 only", aff)
	}
}

func waitTaste(t *testing.T, s *Service, profile uint, genre int) {
	t.Helper()
	for range 400 {
		s.taste.mu.Lock()
		v := s.taste.entries[profile].affinity[genre]
		s.taste.mu.Unlock()
		if v == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("taste for genre %d not computed", genre)
}

func TestClampQuery(t *testing.T) {
	long := strings.Repeat("ğ", 500)
	got := clampQuery("  " + long + "  ")
	if utf8.RuneCountInString(got) != maxQueryRunes || !utf8.ValidString(got) {
		t.Fatalf("clamped to %d runes, valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if clampQuery("  dune ") != "dune" {
		t.Error("short query changed")
	}
}

func TestDiscoverParamsWhitelist(t *testing.T) {
	q := url.Values{
		"sort_by": {"popularity.desc"}, "with_genres": {"28|12"}, "vote_count.gte": {"0300"},
		"primary_release_date.gte": {"2025-01-01"}, "page": {"2"},
		"include_adult": {"true"}, "api_key": {"x"}, "junk": {"1"}, "language": {"de-DE"},
		"vote_average.gte": {"NaN"}, "with_original_language": {"en'"},
	}
	got := discoverParams(q)
	want := map[string]string{"sort_by": "popularity.desc", "with_genres": "28|12", "vote_count.gte": "300",
		"primary_release_date.gte": "2025-01-01", "page": "2", "include_adult": "false"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if p := discoverParams(url.Values{"sort_by": {"x"}, "page": {"9999"}, "with_genres": {"1,2;3"}}); len(p) != 1 {
		t.Errorf("invalid values kept: %v", p)
	}
}

func TestPersonalizeSimilarOnlyLeadingBlockFixed(t *testing.T) {
	s := &Service{}
	s.SetLocalSignals(func() (map[uint][]LocalTitle, error) {
		return map[uint][]LocalTitle{7: {{MediaType: "movie", TmdbID: 2, Watched: true, Finished: true}}}, nil
	})
	mk := func(id int, reason string, score float64) SimilarItem {
		return SimilarItem{SearchResult: tmdb.SearchResult{ID: id, MediaType: "movie", Title: fmt.Sprint(id)},
			Reason: reason, score: score}
	}
	// A spin-off further down must not freeze the matches before it
	items := []SimilarItem{mk(1, "collection", 9), mk(2, "match", 8), mk(3, "match", 7), mk(4, "spin_off", 1)}
	var got []int
	for _, it := range s.personalizeSimilar(items, 7) {
		got = append(got, it.ID)
	}
	if fmt.Sprint(got) != "[1 3 2 4]" {
		t.Errorf("personalized = %v, want [1 3 2 4]", got)
	}
}

func TestSimilarOverflowSpinOffsAreMatches(t *testing.T) {
	src := show(1396, "Breaking Bad", "en", "2008-01-20", 18000, 8.9, gDrama, gCrime)
	f := newFakeSim(src)
	f.src.Creators = []int{66633}
	var made []tmdb.SearchResult
	for i := range simRelatedMax + 2 {
		made = append(made, f.add(show(500+i, fmt.Sprintf("Breaking Bad Story %d", i+1), "en", "2019-10-11",
			5000-100*i, 7.1, gCrime, gDrama)))
	}
	f.works[66633] = &simPersonWorks{Made: made}
	f.list("recommendations:1", made...)

	items := build(t, f)
	spin := 0
	for i, it := range items {
		if it.Reason == "spin_off" {
			spin++
			if i >= simRelatedMax {
				t.Errorf("spin-off at %d: %v", i, titles(items))
			}
		}
	}
	if spin != simRelatedMax {
		t.Errorf("%d spin-offs, want %d: %v", spin, simRelatedMax, titles(items))
	}
}
