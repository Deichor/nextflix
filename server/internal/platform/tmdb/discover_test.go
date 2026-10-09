package tmdb

import (
	"net/url"
	"strings"
	"testing"
)

func TestDiscoverPages(t *testing.T) {
	c, f := newFake(t, func(path, language string) any {
		overview := "English overview"
		if language == "tr-TR" {
			overview = ""
		}
		if path == "/discover/tv" {
			return map[string]any{"page": 2, "total_pages": 7, "total_results": 130, "results": []map[string]any{
				{"id": 1, "name": "Show", "original_name": "Show", "original_language": "en", "overview": overview, "genre_ids": []int{16}},
			}}
		}
		return map[string]any{"page": 1, "total_pages": 3, "total_results": 41, "results": []map[string]any{
			{"id": 2, "title": "Film", "original_title": "Film", "original_language": "en", "overview": "x"},
		}}
	})

	params := url.Values{"with_genres": {"16,10765"}, "page": {"2"}}
	tr := c.WithLang(LangTR)
	page, err := tr.DiscoverTVPage(params)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalPages != 7 || page.TotalResults != 130 || len(page.Results) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if page.Results[0].Overview != "English overview" {
		t.Errorf("Turkish gap not filled from English: %q", page.Results[0].Overview)
	}
	q := f.query["/discover/tv?tr-TR"]
	if !strings.Contains(q, "with_genres=16%2C10765") || !strings.Contains(q, "page=2") {
		t.Errorf("query = %s", q)
	}
	if params.Get("language") != "" {
		t.Error("caller's params were modified")
	}

	// Cached per query and language
	tr.DiscoverTVPage(params)
	if n := f.count("/discover/tv?tr-TR"); n != 1 {
		t.Errorf("tr discover requests = %d, want 1", n)
	}

	m, err := c.DiscoverMoviePage(url.Values{"with_genres": {"878"}})
	if err != nil || m.TotalPages != 3 || len(m.Results) != 1 {
		t.Fatalf("movies = %+v, %v", m, err)
	}
	if !strings.Contains(f.query["/discover/movie?en-US"], "page=1") {
		t.Errorf("default page missing: %s", f.query["/discover/movie?en-US"])
	}
	if r := TVResult(page.Results[0]); r.MediaType != "tv" || r.Title != "Show" {
		t.Errorf("TVResult = %+v", r)
	}
}
