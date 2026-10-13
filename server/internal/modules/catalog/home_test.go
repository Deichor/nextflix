package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

// homeTMDB answers every list path with one item named after the path and records the languages
// asked per path. Paths in slow wait until the test ends.
type homeTMDB struct {
	mu    sync.Mutex
	langs map[string][]string
}

func newHomeTMDB(t *testing.T, slow ...string) (*tmdb.Client, *homeTMDB) {
	t.Helper()
	f := &homeTMDB{langs: map[string][]string{}}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.langs[r.URL.Path] = append(f.langs[r.URL.Path], r.URL.Query().Get("language"))
		f.mu.Unlock()
		for _, p := range slow {
			if r.URL.Path == p {
				<-release
			}
		}
		name := r.URL.Path + "?" + r.URL.Query().Get("with_genres")
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
			"id": 1, "title": name, "name": name, "original_title": name, "original_name": name,
			"original_language": "en", "overview": "overview of " + name,
		}}})
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return tmdb.New("k").WithBaseURL(srv.URL), f
}

func (f *homeTMDB) asked(path, lang string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.langs[path] {
		if l == lang {
			return true
		}
	}
	return false
}

func homeEngine(s *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	return r
}

func homeGet(t *testing.T, r http.Handler, path string) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return w, body
}

func stubTop10(region, mediaType, lang string) (any, bool) {
	return map[string]any{"region": region, "type": mediaType, "week": "2026-10-05", "items": []any{}}, true
}

type homeGenreJSON struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params"`
	Data   json.RawMessage   `json:"data"`
}

func TestHomeHasEverySection(t *testing.T) {
	tm, _ := newHomeTMDB(t)
	s := NewService(tm)
	s.SetTop10(stubTop10)
	w, body := homeGet(t, homeEngine(s), "/api/home")

	for _, k := range []string{"version", "lang", "generated_at", "upcoming_movies", "upcoming_tv_series",
		"popular_movies", "top10_tr_movie", "top10_tr_tv", "genres"} {
		v, ok := body[k]
		if !ok || string(v) == "null" {
			t.Errorf("%s: %s", k, v)
		}
	}
	if string(body["version"]) != "1" || string(body["lang"]) != `"en"` {
		t.Errorf("version %s, lang %s", body["version"], body["lang"])
	}
	var at string
	json.Unmarshal(body["generated_at"], &at)
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("generated_at %q: %v", at, err)
	}
	var genres []homeGenreJSON
	if err := json.Unmarshal(body["genres"], &genres); err != nil {
		t.Fatal(err)
	}
	want := []string{"action_adventure", "comedies", "scifi_fantasy", "horror", "acclaimed"}
	if len(genres) != len(want) {
		t.Fatalf("genres: %+v", genres)
	}
	for i, g := range genres {
		if g.ID != want[i] || string(g.Data) == "null" {
			t.Errorf("genre %d: %s %s", i, g.ID, g.Data)
		}
		if _, ok := g.Params["include_adult"]; ok {
			t.Errorf("genre %s params carry the defaults: %v", g.ID, g.Params)
		}
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, max-age=60" {
		t.Errorf("Cache-Control %q", cc)
	}
	if v := strings.Join(w.Header().Values("Vary"), ","); !strings.Contains(v, "X-Lang") || !strings.Contains(v, "Accept-Encoding") {
		t.Errorf("Vary %q", v)
	}
}

func TestHomeWithoutTop10IsNotCached(t *testing.T) {
	tm, _ := newHomeTMDB(t)
	w, body := homeGet(t, homeEngine(NewService(tm)), "/api/home")
	if string(body["top10_tr_movie"]) != "null" || string(body["top10_tr_tv"]) != "null" {
		t.Errorf("top10 without a source: %s %s", body["top10_tr_movie"], body["top10_tr_tv"])
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}

	// No TMDB key: every section is null, still 200
	s := NewService(tmdb.New(""))
	s.SetTop10(stubTop10)
	_, body = homeGet(t, homeEngine(s), "/api/home")
	for _, k := range []string{"upcoming_movies", "upcoming_tv_series", "popular_movies", "top10_tr_movie", "top10_tr_tv"} {
		if string(body[k]) != "null" {
			t.Errorf("no key, %s: %s", k, body[k])
		}
	}
}

func TestHomeLanguage(t *testing.T) {
	tm, f := newHomeTMDB(t)
	s := NewService(tm)
	var asked []string
	var mu sync.Mutex
	s.SetTop10(func(region, mediaType, lang string) (any, bool) {
		mu.Lock()
		asked = append(asked, lang)
		mu.Unlock()
		return stubTop10(region, mediaType, lang)
	})
	_, body := homeGet(t, homeEngine(s), "/api/home?x_lang=tr")
	if string(body["lang"]) != `"tr"` {
		t.Errorf("lang %s", body["lang"])
	}
	for _, p := range []string{"/movie/popular", "/discover/movie", "/trending/movie/day", "/tv/airing_today"} {
		if !f.asked(p, "tr-TR") {
			t.Errorf("%s not asked in tr-TR", p)
		}
	}
	if len(asked) != 2 || asked[0] != "tr" || asked[1] != "tr" {
		t.Errorf("Top 10 asked in %v", asked)
	}
}

// Each section is exactly the body of the endpoint the clients called before.
func TestHomeMatchesOldEndpoints(t *testing.T) {
	tm, _ := newHomeTMDB(t)
	r := homeEngine(NewService(tm))
	_, home := homeGet(t, r, "/api/home")

	same := func(name string, section json.RawMessage, path string) {
		t.Helper()
		_, old := homeGet(t, r, path)
		var a, b any
		json.Unmarshal(section, &a)
		raw, _ := json.Marshal(old)
		json.Unmarshal(raw, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s differs from %s:\n%s\n%s", name, path, section, raw)
		}
	}
	same("upcoming_movies", home["upcoming_movies"], "/api/upcoming-movies")
	same("upcoming_tv_series", home["upcoming_tv_series"], "/api/upcoming-tv-series")
	same("popular_movies", home["popular_movies"], "/api/tmdb/movie/popular")

	var genres []homeGenreJSON
	json.Unmarshal(home["genres"], &genres)
	for i, row := range tmdb.HomeGenreRows {
		q := url.Values{"sort_by": {"popularity.desc"}, "include_adult": {"false"}}
		for k, v := range row.Params {
			q.Set(k, v)
		}
		same("genre "+row.ID, genres[i].Data, "/api/tmdb/discover/movie?"+q.Encode())
	}
}

func TestHomeSlowSectionIsNull(t *testing.T) {
	defer func(d time.Duration) { homeDeadline = d }(homeDeadline)
	homeDeadline = 300 * time.Millisecond
	tm, _ := newHomeTMDB(t, "/movie/popular")
	s := NewService(tm)
	s.SetTop10(stubTop10)

	start := time.Now()
	w, body := homeGet(t, homeEngine(s), "/api/home")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("answered after %s", d)
	}
	if string(body["popular_movies"]) != "null" {
		t.Errorf("slow section: %s", body["popular_movies"])
	}
	if string(body["upcoming_movies"]) == "null" {
		t.Error("fast section missing")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
}
