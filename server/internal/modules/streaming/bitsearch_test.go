package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const kardeslerHash = "46753efc530a7033d12b82a4daff6ac57a65ab58"

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Saved from https://bitsearch.eu/search?q=Kardesler+Arastirma (bitsearch.to redirects there)
func TestParseBitsearchSingleResult(t *testing.T) {
	items := tsParseBitsearch(readFixture(t, "bitsearch_kardesler.html"))
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	it := items[0]
	if it.Title != "Kardeşler.Araştırma.2026.mp4" || it.Category != "Other/Video" || it.InfoHash != kardeslerHash {
		t.Fatalf("item = %+v", it)
	}
	if it.Seeders != 1 || it.Leechers != 0 {
		t.Fatalf("swarm = %d/%d", it.Seeders, it.Leechers)
	}
	gb := 1.56
	if want := int64(gb * float64(tsGB)); it.Size != want {
		t.Fatalf("size = %d, want %d", it.Size, want)
	}
	if !it.Date.Equal(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date = %s", it.Date)
	}
	if len(it.Trackers) != 5 || it.Trackers[4] != "udp://tracker.bitsearch.to:1337/announce" {
		t.Fatalf("trackers = %q (DHT placeholder must be dropped)", it.Trackers)
	}
}

func TestParseBitsearchPage(t *testing.T) {
	items := tsParseBitsearch(readFixture(t, "bitsearch_inception.html"))
	if len(items) != 20 {
		t.Fatalf("%d items, want the 20 of the page", len(items))
	}
	cats := map[string]int{}
	for _, it := range items {
		if !tsHashPattern.MatchString(it.InfoHash) || it.Size <= 0 || it.Title == "" || it.Date.IsZero() {
			t.Fatalf("incomplete item %+v", it)
		}
		cats[it.Category]++
	}
	if cats["Movies"] != 11 || cats["Other/Video"] != 8 || cats["Other"] != 1 {
		t.Fatalf("categories = %v", cats)
	}
	first := items[0]
	if first.Title != "Inception.2010.1080p.BluRay.DDP5.1.x265.10bit-GalaxyRG265[TGx]" || first.Seeders <= 0 {
		t.Fatalf("first = %+v", first)
	}
	// Entities in names are decoded, site prefixes kept (tsStem drops them)
	found := false
	for _, it := range items {
		found = found || strings.HasPrefix(it.Title, "www.UIndex.org - Inception 2010 BluRay")
	}
	if !found {
		t.Fatal("whitespace in titles is not collapsed")
	}
}

func TestBitsearchMagnet(t *testing.T) {
	it := tsParseBitsearch(readFixture(t, "bitsearch_kardesler.html"))[0]
	r := it.result()
	if tsHashFromMagnet(r.MagnetUri) != kardeslerHash || r.InfoHash != kardeslerHash || r.Tracker != "Bitsearch" {
		t.Fatalf("result = %+v", r)
	}
	if !strings.Contains(r.MagnetUri, "&dn=Karde%C5%9Fler.Ara%C5%9Ft%C4%B1rma.2026.mp4") {
		t.Fatalf("magnet name: %s", r.MagnetUri)
	}
	n := strings.Count(r.MagnetUri, "&tr=")
	if n != 10 || !strings.Contains(r.MagnetUri, "tracker.bitsearch.to") || !strings.Contains(r.MagnetUri, "tracker.opentrackr.org") {
		t.Fatalf("%d trackers in %s", n, r.MagnetUri)
	}
	if strings.Contains(r.MagnetUri, "tr=DHT") {
		t.Fatal("DHT placeholder in the magnet")
	}
	// A bare hash still gets the open trackers
	bare := bsItem{Title: "x", InfoHash: kardeslerHash}.result()
	if strings.Count(bare.MagnetUri, "&tr=") != len(tsOpenTrackers) {
		t.Fatalf("bare magnet: %s", bare.MagnetUri)
	}
}

func TestBitsearchCategoryFilter(t *testing.T) {
	for cat, want := range map[string]bool{"Other/Video": true, "Movies": true, "Movies/Dub/Dual Audio": true, "TV": true,
		"Anime/Subbed": true, "Other": true, "": true, "Other/Audio": false, "Softwares": false, "Games": false, "Ebooks": false} {
		if bsVideoCategory(cat) != want {
			t.Errorf("%q: %v", cat, !want)
		}
	}
}

func TestBitsearchQueries(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026, imdbID: "tt40656390",
		extraQueries: []string{"Kardeşler Araştırma", "Kardesler Arastirma", "Brothers Investigation"}}
	queries, _ := tsSearchQueries(q)
	if queries[len(queries)-1] != "imdb:tt40656390" {
		t.Fatalf("no IMDb query: %q", queries)
	}
	got := strings.Join(tsBitsearchQueries(q, queries), "|")
	if got != "detective brothers 2026|kardesler arastirma 2026|brothers investigation 2026" {
		t.Fatalf("bitsearch queries = %q", got)
	}
	tv := tsResolveQuery{mediaType: "tv", title: "Money Trap", season: 1, episode: 2, extraQueries: []string{"Organize İşler", "Organize Isler"}}
	queries, _ = tsSearchQueries(tv)
	if got := strings.Join(tsBitsearchQueries(tv, queries), "|"); got != "money trap s01e02|organize isler s01e02|money trap s01|organize isler s01" {
		t.Fatalf("tv queries = %q", got)
	}
}

// The Bitsearch find of "Detective Brothers" (TMDB) is matched through its original title.
func TestBitsearchCandidateRanks(t *testing.T) {
	raw := []JackettResult{tsParseBitsearch(readFixture(t, "bitsearch_kardesler.html"))[0].result()}
	q := tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026, hevc: true,
		names: []string{"Kardeşler Araştırma", "Brothers Investigation"}}
	cands := tsRankCandidates(raw, q)
	if len(cands) != 1 || cands[0].hash != kardeslerHash || cands[0].magnet == "" {
		t.Fatalf("candidates = %+v", cands)
	}
	q.names = nil
	if len(tsRankCandidates(raw, q)) != 0 {
		t.Fatal("matched without the original title")
	}
}

func bitsearchServer(t *testing.T, page string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/search" || r.URL.Query().Get("q") == "" || !strings.Contains(r.UserAgent(), "Mozilla") {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBitsearchSearchCachesAndPaces(t *testing.T) {
	var hits atomic.Int32
	srv := bitsearchServer(t, readFixture(t, "bitsearch_kardesler.html"), &hits)
	// The configured URL redirects like bitsearch.to -> bitsearch.eu
	var redirects atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, srv.URL+r.URL.RequestURI(), http.StatusMovedPermanently)
	}))
	defer old.Close()
	b := newBitsearch(old.URL, "")

	ctx := context.Background()
	start := time.Now()
	res, err := b.Search(ctx, "Kardeşler Araştırma 2026")
	if err != nil || len(res) != 1 || res[0].InfoHash != kardeslerHash {
		t.Fatalf("search = %+v, %v", res, err)
	}
	// Same query in another spelling: cached
	if res, err := b.Search(ctx, "kardesler  arastirma 2026"); err != nil || len(res) != 1 || hits.Load() != 1 {
		t.Fatalf("cached search = %d results, %v, %d requests", len(res), err, hits.Load())
	}
	// Another query waits for the 1 s gap and skips the redirect
	if _, err := b.Search(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < bsGap {
		t.Fatalf("two requests within %s", took)
	}
	if hits.Load() != 2 || redirects.Load() != 1 {
		t.Fatalf("%d requests, %d redirects", hits.Load(), redirects.Load())
	}
}

func TestBitsearchSharesRunningRequest(t *testing.T) {
	var hits atomic.Int32
	srv := bitsearchServer(t, readFixture(t, "bitsearch_kardesler.html"), &hits)
	b := newBitsearch(srv.URL, "")
	done := make(chan int, 3)
	for i := 0; i < 3; i++ {
		go func() {
			res, _ := b.Search(context.Background(), "Kardesler Arastirma 2026")
			done <- len(res)
		}()
	}
	for i := 0; i < 3; i++ {
		if n := <-done; n != 1 {
			t.Fatalf("got %d results", n)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests for one query", hits.Load())
	}
}

func TestBitsearchDownPauses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	b := newBitsearch(srv.URL, "")
	if _, err := b.Search(context.Background(), "a"); err == nil {
		t.Fatal("no error for a 502")
	}
	if _, err := b.Search(context.Background(), "b"); !errors.Is(err, errBitsearchDown) || hits.Load() != 1 {
		t.Fatalf("second search: %v after %d requests", err, hits.Load())
	}
}

func TestBitsearchCloudflareUsesFlareSolverr(t *testing.T) {
	page := readFixture(t, "bitsearch_kardesler.html")
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<html><title>Just a moment...</title></html>"))
	}))
	defer site.Close()
	var asked atomic.Int32
	flare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		var in struct{ Cmd, URL string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Cmd != "request.get" || !strings.HasPrefix(in.URL, site.URL+"/search?q=") {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{
			"status": 200, "response": page, "userAgent": "UA", "cookies": []map[string]string{{"name": "cf_clearance", "value": "x"}}}})
	}))
	defer flare.Close()
	res, err := newBitsearch(site.URL, flare.URL).Search(context.Background(), "Kardesler Arastirma")
	if err != nil || len(res) != 1 || asked.Load() != 1 {
		t.Fatalf("search through FlareSolverr = %+v, %v", res, err)
	}
}

// Jackett is not configured (every request fails at once): Bitsearch, gathered like one more
// indexer, is the answer on its own.
func TestSearchBitsearchWhenJackettFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	page := readFixture(t, "bitsearch_kardesler.html")
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(page))
	}))
	defer site.Close()
	s := NewService(db, Config{BitsearchURL: site.URL})
	q := tsResolveQuery{mediaType: "movie", title: "Kardeşler Araştırma", year: 2026}
	sr, err := s.search(context.Background(), q)
	if err != nil || len(sr.raw) != 1 || sr.raw[0].InfoHash != kardeslerHash || sr.base != "" {
		t.Fatalf("search = %+v, %v", sr, err)
	}
	// Its answer time is tracked like an indexer's
	if _, ok := s.indexerTimes.Load(tsBitsearchID); !ok {
		t.Fatal("bitsearch answer time not tracked")
	}

	// Without Bitsearch the search fails
	s = NewService(db, Config{})
	if _, err := s.search(context.Background(), q); err == nil {
		t.Fatal("no error without any source")
	}
}
