package tmdb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeTMDB answers by path and language and counts the requests per "path?language".
type fakeTMDB struct {
	mu    sync.Mutex
	hits  map[string]int
	query map[string]string // last raw query per "path?language"
	body  func(path, language string) any
}

func newFake(t *testing.T, body func(path, language string) any) (*Client, *fakeTMDB) {
	t.Helper()
	f := &fakeTMDB{hits: map[string]int{}, query: map[string]string{}, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("language")
		f.mu.Lock()
		f.hits[r.URL.Path+"?"+lang]++
		f.query[r.URL.Path+"?"+lang] = r.URL.RawQuery
		f.mu.Unlock()
		json.NewEncoder(w).Encode(f.body(r.URL.Path, lang))
	}))
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.base = srv.URL
	return c, f
}

func (f *fakeTMDB) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

func TestNormalizeLang(t *testing.T) {
	for in, want := range map[string]string{"": "en", "tr": "tr", "tr-TR": "tr", "TR": "tr", "en-US": "en", "de": "en"} {
		if got := NormalizeLang(in); got != want {
			t.Errorf("NormalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
	c := New("k")
	if c.Lang() != LangEN || c.Language() != "en-US" {
		t.Fatalf("default client: %s %s", c.Lang(), c.Language())
	}
	tr := c.WithLang("tr")
	if tr.Lang() != LangTR || tr.Language() != "tr-TR" {
		t.Fatalf("tr client: %s %s", tr.Lang(), tr.Language())
	}
	if c.Lang() != LangEN {
		t.Fatal("WithLang changed the original client")
	}
	if c.WithLang("en") != c {
		t.Fatal("WithLang of the same language should return the client itself")
	}
}

func TestCacheKeysAreSeparatedByLanguage(t *testing.T) {
	c, f := newFake(t, func(path, lang string) any {
		title := map[string]string{"en-US": "Inception", "tr-TR": "Başlangıç"}[lang]
		return map[string]any{"id": 27205, "title": title, "original_title": "Inception",
			"original_language": "en", "overview": "text " + lang, "tagline": "tag " + lang}
	})

	en, err := c.GetMovieDetails(27205)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := c.WithLang("tr").GetMovieDetails(27205)
	if err != nil {
		t.Fatal(err)
	}
	if en.Title != "Inception" || tr.Title != "Başlangıç" {
		t.Fatalf("titles: en %q, tr %q", en.Title, tr.Title)
	}
	// Both come from the cache now
	c.GetMovieDetails(27205)
	c.WithLang("tr").GetMovieDetails(27205)
	if n := f.count("/movie/27205?en-US"); n != 1 {
		t.Errorf("en-US requests: %d, want 1", n)
	}
	if n := f.count("/movie/27205?tr-TR"); n != 1 {
		t.Errorf("tr-TR requests: %d, want 1", n)
	}
	// Cached (used by modules) keeps a value per language too
	a, _ := Cached(c, "k", func() (string, error) { return "en", nil })
	b, _ := Cached(c.WithLang("tr"), "k", func() (string, error) { return "tr", nil })
	if a != "en" || b != "tr" {
		t.Fatalf("Cached mixed languages: %q %q", a, b)
	}
}

func TestOverviewFallsBackToEnglish(t *testing.T) {
	c, f := newFake(t, func(path, lang string) any {
		switch {
		case path == "/movie/1" && lang == "tr-TR":
			// TMDB answer of an untranslated Japanese film
			return map[string]any{"id": 1, "title": "ルックバック", "original_title": "ルックバック",
				"original_language": "ja", "overview": "", "tagline": ""}
		case path == "/movie/1":
			return map[string]any{"id": 1, "title": "Look Back", "original_title": "ルックバック",
				"original_language": "ja", "overview": "Fujino draws.", "tagline": "Keep your eyes on my back."}
		case path == "/movie/2" && lang == "tr-TR":
			return map[string]any{"id": 2, "title": "Başlangıç", "original_title": "Inception",
				"original_language": "en", "overview": "Türkçe özet", "tagline": "Aklınız suç mahallidir."}
		case path == "/tv/5/season/1":
			if lang == "tr-TR" {
				return map[string]any{"season_number": 1, "overview": "Sezon", "episodes": []map[string]any{
					{"episode_number": 1, "name": "Kış Geliyor", "overview": "Türkçe"},
					{"episode_number": 2, "name": "Kral Yolu", "overview": ""},
					{"episode_number": 3, "name": "3. Bölüm", "overview": "Türkçe 3"},
					{"episode_number": 4, "name": "4. Bölüm", "overview": "Türkçe 4"},
				}}
			}
			return map[string]any{"season_number": 1, "overview": "Season", "episodes": []map[string]any{
				{"episode_number": 1, "name": "Winter Is Coming", "overview": "English 1"},
				{"episode_number": 2, "name": "The Kingsroad", "overview": "English 2"},
				{"episode_number": 3, "name": "Episode 3", "overview": "English 3"},
				{"episode_number": 4, "name": "Lord Snow", "overview": "English 4"},
			}}
		case path == "/movie/popular":
			if lang == "tr-TR" {
				return map[string]any{"results": []map[string]any{
					{"id": 1, "title": "ルックバック", "original_title": "ルックバック", "original_language": "ja", "overview": ""},
					{"id": 2, "title": "Başlangıç", "original_title": "Inception", "original_language": "en", "overview": "Türkçe özet"},
				}}
			}
			return map[string]any{"results": []map[string]any{
				{"id": 1, "title": "Look Back", "original_title": "ルックバック", "original_language": "ja", "overview": "Fujino draws."},
				{"id": 2, "title": "Inception", "original_title": "Inception", "original_language": "en", "overview": "A thief."},
			}}
		}
		return map[string]any{}
	})
	tr := c.WithLang("tr")

	d, err := tr.GetMovieDetails(1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Overview != "Fujino draws." || d.Tagline != "Keep your eyes on my back." || d.Title != "Look Back" {
		t.Errorf("fallback details: %q / %q / %q", d.Title, d.Overview, d.Tagline)
	}

	// Complete translation: English is never fetched
	d2, _ := tr.GetMovieDetails(2)
	if d2.Overview != "Türkçe özet" || d2.Title != "Başlangıç" {
		t.Errorf("translated details changed: %q / %q", d2.Title, d2.Overview)
	}
	if n := f.count("/movie/2?en-US"); n != 0 {
		t.Errorf("english fetched %d times for a complete translation", n)
	}

	s, err := tr.GetSeasonDetails(5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Episodes[0].Overview != "Türkçe" || s.Episodes[0].Name != "Kış Geliyor" {
		t.Errorf("translated episode changed: %+v", s.Episodes[0])
	}
	if s.Episodes[1].Overview != "English 2" || s.Episodes[1].Name != "Kral Yolu" {
		t.Errorf("episode fallback: %+v", s.Episodes[1])
	}
	// TMDB placeholder names: kept when English has no real name either, else English
	if s.Episodes[2].Name != "3. Bölüm" || s.Episodes[3].Name != "Lord Snow" || s.Episodes[3].Overview != "Türkçe 4" {
		t.Errorf("placeholder names: %q / %q", s.Episodes[2].Name, s.Episodes[3].Name)
	}

	list, err := tr.GetPopularMovies(1)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Title != "Look Back" || list[0].Overview != "Fujino draws." {
		t.Errorf("list fallback: %+v", list[0])
	}
	if list[1].Title != "Başlangıç" || list[1].Overview != "Türkçe özet" {
		t.Errorf("translated list item changed: %+v", list[1])
	}
	// The English list is cached under its own key and stays English
	en, _ := c.GetPopularMovies(1)
	if en[0].Title != "Look Back" || en[1].Title != "Inception" || f.count("/movie/popular?en-US") != 1 {
		t.Errorf("english list: %+v (requests %d)", en, f.count("/movie/popular?en-US"))
	}
}

func TestOriginalTurkishTitleIsKept(t *testing.T) {
	// A Turkish film: tr title == original title is a real translation, not a gap
	if Untranslated(LangTR, "Kış Uykusu", "Kış Uykusu", "tr") {
		t.Fatal("Turkish original title treated as untranslated")
	}
	if !Untranslated(LangTR, "ルックバック", "ルックバック", "ja") {
		t.Fatal("Japanese title not treated as untranslated")
	}
	if Untranslated(LangTR, "The Fix", "The Fix", "en") {
		t.Fatal("English original title should not trigger a fallback")
	}
	title, overview := "Kış Uykusu", ""
	FillText(LangTR, &title, &overview, "Kış Uykusu", "tr", "Winter Sleep", "An actor runs a hotel.")
	if title != "Kış Uykusu" || overview != "An actor runs a hotel." {
		t.Fatalf("FillText: %q / %q", title, overview)
	}
}

func TestImagesAndVideosPreferTheLanguage(t *testing.T) {
	c, f := newFake(t, func(path, lang string) any { return map[string]any{"id": 9} })
	c.GetMovieImages(9)
	if q := f.query["/movie/9/images?"]; strings.Contains(q, "include_image_language") {
		t.Errorf("english images query filters languages: %s", q)
	}
	c.WithLang("tr").GetMovieImages(9)
	if q := f.query["/movie/9/images?"]; !strings.Contains(q, "include_image_language=tr%2Cen%2Cnull") {
		t.Errorf("turkish images query: %s", q)
	}
	c.WithLang("tr").GetTVDetails(9)
	if q := f.query["/tv/9?tr-TR"]; !strings.Contains(q, "include_video_language=tr%2Cen%2Cnull") || !strings.Contains(q, "language=tr-TR") {
		t.Errorf("turkish tv details query: %s", q)
	}
}
