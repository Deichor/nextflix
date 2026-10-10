package subtitles

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubSourceSearchAndDownload(t *testing.T) {
	pack := makeZip(t, map[string]string{
		"[HolySubs] Sousou no Frieren - 01 (1080p).ass": "1\n00:00:01,000 --> 00:00:02,000\nYanlış bölüm\n",
		"[HolySubs] Sousou no Frieren - 02 (1080p).ass": sampleSRT,
	})
	single := makeZip(t, map[string]string{"frieren.srt": sampleSRT})
	var searches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "SS" || r.URL.Query().Get("api_key") != "" {
			t.Errorf("key not in the header: %v", r.URL)
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/v1/movies/search":
			searches++
			if q.Get("searchType") != "imdb" || q.Get("imdb") != "tt22248376" || q.Get("season") != "1" {
				t.Errorf("title search %v", q)
			}
			io.WriteString(w, `{"success":true,"data":[
			 {"movieId":"9","title":"Frieren","link":"frieren-beyond-journeys-end-season-2"},
			 {"movieId":110744,"title":"Frieren","link":"frieren-beyond-journeys-end-season-1","releaseYear":2023}]}`)
		case "/api/v1/subtitles":
			if q.Get("movieId") != "110744" || q.Get("seasonNumber") != "1" || q.Get("episodeNumber") != "2" {
				t.Errorf("listing %v", q)
			}
			if q.Get("language") == "english" {
				http.NotFound(w, r)
				return
			}
			io.WriteString(w, `{"success":true,"data":[
			 {"subtitleId":10010996,"language":"turkish","releaseInfo":["[SubsPlease] Sousou no Frieren - 02 (1080p) [E5A85899]"],
			  "uploaderId":2006276,"contributors":[{"id":2006276,"displayname":"ToxicworlD"}]},
			 {"subtitleId":10016746,"language":"turkish","releaseInfo":["[SubsPlease] Sousou no Frieren - 03 (1080p) [7EF3F175]"]},
			 {"subtitleId":"10010472","language":"turkish","releaseInfo":["Sousou no Frieren S01 1080p WEBRip x265-9volt"]},
			 {"subtitleId":10020000,"language":"turkish","releaseInfo":["Frieren WEB"],"commentary":"S01E05\nÇeviri: x"}]}`)
		case "/api/v1/subtitles/10010472/download":
			w.Write(pack)
		case "/api/v1/subtitles/10010996/download":
			w.Write(single)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newSubSource(srv.URL+"/api/v1", testClient())
	st := Settings{SubSourceAPIKey: "SS"}
	q := Query{TmdbID: 209867, MediaType: "tv", Season: 1, Episode: 2, Langs: []string{"tr", "en"}}
	items, err := s.search(context.Background(), q, st, "tt22248376")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	ep, pk := items[0], items[1]
	if ep.Pack || ep.Author != "ToxicworlD" || ep.Language != "tr" || !pk.Pack {
		t.Fatalf("items %+v", items)
	}
	if _, err := s.search(context.Background(), q, st, "tt22248376"); err != nil || searches != 1 {
		t.Fatalf("title id not cached: %v %d", err, searches)
	}

	_, lang, ref, _ := decodeID(pk.ID)
	raw, name, err := s.fetch(context.Background(), ref, lang, st)
	if err != nil || !strings.Contains(name, "- 02") || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("pack: %v %q", err, name)
	}
	// A single episode's zip whose file name does not say the episode
	_, lang, ref, _ = decodeID(ep.ID)
	if raw, _, err = s.fetch(context.Background(), ref, lang, st); err != nil || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("single: %v", err)
	}
	if _, _, err := s.fetch(context.Background(), "../x|1|2", "tr", st); err == nil {
		t.Fatal("bad id accepted")
	}
}
