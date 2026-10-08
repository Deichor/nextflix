package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"nextflix/internal/platform/tmdb"
)

// Answer of https://v3.sg.media-imdb.com/suggestion/x/tt40656390.json
const brothersSuggestion = `{"d":[{"i":{"height":857,"imageUrl":"https://m.media-amazon.com/images/M/MV5BZjJkNTgxNjktMzU5NS00OThjLTk3MzctZGI5YTk2M2UxNjY3XkEyXkFqcGc@._V1_.jpg","width":600},"id":"tt40656390","l":"Brothers Investigation","q":"feature","qid":"movie","rank":17904,"s":"Cem Gelinoglu, Dogu Demirkol","tl":"2026","y":2026}],"q":"tt40656390","v":1}`

func TestParseIMDbSuggestion(t *testing.T) {
	title, year, ok := tsParseIMDbSuggestion([]byte(brothersSuggestion), "tt40656390")
	if !ok || title != "Brothers Investigation" || year != 2026 {
		t.Fatalf("got %q %d %v", title, year, ok)
	}
	if _, _, ok := tsParseIMDbSuggestion([]byte(brothersSuggestion), "tt1375666"); ok {
		t.Fatal("matched another id")
	}
	if _, _, ok := tsParseIMDbSuggestion([]byte(`{"d":[]}`), "tt40656390"); ok {
		t.Fatal("empty answer matched")
	}
	if _, _, ok := tsParseIMDbSuggestion([]byte(`<html>`), "tt40656390"); ok {
		t.Fatal("garbage matched")
	}
	if id, ok := tsIMDbQuery("imdb:tt40656390"); !ok || id != "tt40656390" {
		t.Fatal("imdb pseudo query not recognised")
	}
	if _, ok := tsIMDbQuery("imdb:x; rm"); ok {
		t.Fatal("invalid IMDb id accepted")
	}
}

// brothersTMDB is the shared TMDB client for "Detective Brothers" (tmdb 1586156).
type brothersTMDB struct{ titleTMDB }

func (brothersTMDB) GetMovieDetails(id int) (*tmdb.MovieDetails, error) {
	return &tmdb.MovieDetails{Movie: tmdb.Movie{ID: id, Title: "Detective Brothers", OriginalTitle: "Kardeşler Araştırma", OriginalLanguage: "tr"}}, nil
}

func (brothersTMDB) GetExternalIDs(string, int) (*tmdb.ExternalIDs, error) {
	return &tmdb.ExternalIDs{IMDBID: "tt40656390"}, nil
}

func TestTitleNamesAddIMDbTitle(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/tt40656390.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(brothersSuggestion))
	}))
	defer srv.Close()
	old := imdbSuggestURL
	imdbSuggestURL = srv.URL + "/%s.json"
	defer func() { imdbSuggestURL = old }()

	s := NewService(nil, Config{TMDB: &brothersTMDB{}})
	q := tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026, tmdbID: 1586156}
	n := s.titleNames(q)
	if n.imdbID != "tt40656390" || !n.eager {
		t.Fatalf("names = %+v", n)
	}
	if strings.Join(n.names, "|") != "Kardeşler Araştırma|Brothers Investigation" {
		t.Fatalf("names = %q", n.names)
	}
	if strings.Join(n.queries, "|") != "Kardeşler Araştırma|Kardesler Arastirma|Brothers Investigation" {
		t.Fatalf("queries = %q", n.queries)
	}
	// Cached: the next resolve asks IMDb no more
	s.titleNames(q)
	if hits.Load() != 1 {
		t.Fatalf("%d IMDb requests", hits.Load())
	}
}

func TestIMDbTitleFailureTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	old := imdbSuggestURL
	imdbSuggestURL = srv.URL + "/%s.json"
	defer func() { imdbSuggestURL = old }()

	s := NewService(nil, Config{TMDB: &brothersTMDB{}})
	n := s.titleNames(tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026, tmdbID: 1586156})
	if n.imdbID != "tt40656390" || strings.Join(n.names, "|") != "Kardeşler Araştırma" {
		t.Fatalf("names = %+v", n)
	}
}

const torznabFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>TheRARBG</title>
    <item>
      <title>Kardesler Arastirma 2026 1080p NF WEB-DL DDP5 1 H 264-TURG</title>
      <guid>magnet:?xt=urn:btih:AAC2293760E0BB60DB966E755A2E34BBBA26ED75&amp;dn=x</guid>
      <jackettindexer id="therarbg">TheRARBG</jackettindexer>
      <size>4005057003</size>
      <link>magnet:?xt=urn:btih:AAC2293760E0BB60DB966E755A2E34BBBA26ED75&amp;dn=x&amp;tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce</link>
      <category>2000</category>
      <enclosure url="magnet:?xt=urn:btih:AAC2293760E0BB60DB966E755A2E34BBBA26ED75&amp;dn=x" length="4005057003" type="application/x-bittorrent" />
      <torznab:attr name="category" value="2000" />
      <torznab:attr name="imdb" value="40656390" />
      <torznab:attr name="imdbid" value="tt40656390" />
      <torznab:attr name="seeders" value="31" />
      <torznab:attr name="peers" value="82" />
      <torznab:attr name="infohash" value="AAC2293760E0BB60DB966E755A2E34BBBA26ED75" />
      <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:AAC2293760E0BB60DB966E755A2E34BBBA26ED75&amp;dn=x&amp;tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce" />
    </item>
    <item>
      <title>Brothers Inv 2026 720p WEBRip x264</title>
      <jackettindexer id="therarbg">TheRARBG</jackettindexer>
      <size>1205057003</size>
      <link>http://jackett:9117/dl/therarbg/?jackett_apikey=k&amp;path=abc</link>
      <torznab:attr name="seeders" value="4" />
    </item>
    <item>
      <title>Some Other Movie 2026 1080p WEB-DL</title>
      <jackettindexer id="therarbg">TheRARBG</jackettindexer>
      <size>3005057003</size>
      <link>magnet:?xt=urn:btih:BBC2293760E0BB60DB966E755A2E34BBBA26ED75</link>
      <torznab:attr name="imdbid" value="tt1375666" />
      <torznab:attr name="seeders" value="50" />
    </item>
  </channel>
</rss>`

func TestParseTorznabIMDb(t *testing.T) {
	res, err := tsParseTorznab(strings.NewReader(torznabFeed), "tt40656390")
	if err != nil || len(res) != 2 {
		t.Fatalf("results = %+v, %v (the other IMDb id must be dropped)", res, err)
	}
	a, b := res[0], res[1]
	if !a.IMDbMatch || a.Seeders != 31 || a.Peers != 82 || a.Size != 4005057003 || a.Tracker != "TheRARBG" ||
		!strings.HasPrefix(a.MagnetUri, "magnet:?xt=urn:btih:AAC2") || a.InfoHash == "" || a.Link != "" {
		t.Fatalf("first = %+v", a)
	}
	if !b.IMDbMatch || b.MagnetUri != "" || !strings.Contains(b.Link, "/dl/therarbg/") || b.Seeders != 4 {
		t.Fatalf("link-only = %+v", b)
	}
}

func TestIMDbMatchRanksUnknownName(t *testing.T) {
	res, _ := tsParseTorznab(strings.NewReader(torznabFeed), "tt40656390")
	q := tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026}
	got := titles(tsRankAll(res, q))
	if strings.Join(got, "|") != "Kardesler Arastirma 2026 1080p NF WEB-DL DDP5 1 H 264-TURG|Brothers Inv 2026 720p WEBRip x264" {
		t.Fatalf("ranked = %q", got)
	}
	// The same names from a text search are not the title
	for i := range res {
		res[i].IMDbMatch = false
	}
	if n := len(tsRankAll(res, q)); n != 0 {
		t.Fatalf("%d text results matched", n)
	}
	if got := strings.Join(tsReleaseTitleTokens("www.Site.org - Inception (2010) [1080p]"), " "); got != "inception" {
		t.Fatalf("title tokens = %q", got)
	}
	if tsReleaseTitleTokens("2010") != nil {
		t.Fatal("a bare year is a title")
	}
}

func TestIndexerCapsIMDb(t *testing.T) {
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><indexers>
<indexer id="yts" configured="true"><caps><searching><search available="yes" supportedParams="q" /><tv-search available="no" supportedParams="q" /><movie-search available="yes" supportedParams="q,imdbid" /></searching><categories><category id="2000"/></categories></caps></indexer>
<indexer id="thepiratebay" configured="true"><caps><searching><movie-search available="yes" supportedParams="q" /></searching><categories><category id="2000"/><category id="5000"/></categories></caps></indexer>
<indexer id="off" configured="true"><caps><searching><movie-search available="no" supportedParams="q,imdbid" /></searching><categories><category id="2000"/></categories></caps></indexer>
</indexers>`))
	}))
	defer jackett.Close()
	list, err := NewTorrentSearcher(jackett.URL, "k", 0).Indexers(context.Background())
	if err != nil || len(list) != 3 || !list[0].MovieIMDb || list[1].MovieIMDb || list[2].MovieIMDb {
		t.Fatalf("indexers = %+v, %v", list, err)
	}
}

func memDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // one in-memory database
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	return db
}

// A movie search asks the indexers that support it by IMDb id as well and merges the results.
func TestSearchMergesIMDbResults(t *testing.T) {
	var mu sync.Mutex
	asked := map[string][]string{} // indexer -> queries
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qv := r.URL.Query()
		if qv.Get("apikey") != "k" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		switch {
		case qv.Get("t") == "indexers":
			w.Write([]byte(`<indexers>
<indexer id="therarbg" configured="true"><caps><searching><movie-search available="yes" supportedParams="q,imdbid" /></searching><categories><category id="2000"/></categories></caps></indexer>
<indexer id="thepiratebay" configured="true"><caps><searching><movie-search available="yes" supportedParams="q" /></searching><categories><category id="2000"/></categories></caps></indexer>
</indexers>`))
		case strings.HasSuffix(r.URL.Path, "/results/torznab/api"):
			id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v2.0/indexers/"), "/")[0]
			mu.Lock()
			asked[id] = append(asked[id], "imdb:"+qv.Get("imdbid")+":"+qv.Get("t"))
			mu.Unlock()
			w.Write([]byte(torznabFeed))
		case strings.HasSuffix(r.URL.Path, "/results"):
			id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v2.0/indexers/"), "/")[0]
			mu.Lock()
			asked[id] = append(asked[id], qv.Get("Query"))
			mu.Unlock()
			json.NewEncoder(w).Encode(JackettResponse{Results: []JackettResult{{Title: "Detective Brothers 2026 1080p WEB-DL x264", Seeders: 9,
				Size: 2 * tsGB, MagnetUri: "magnet:?xt=urn:btih:ccc2293760e0bb60db966e755a2e34bbba26ed75", Tracker: id}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer jackett.Close()

	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: jackett.URL, JackettAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{JackettURL: jackett.URL, JackettAPIKey: "k"})
	q := tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026, imdbID: "tt40656390"}
	sr, err := s.search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked["therarbg"], ",") == "" || !strings.Contains(strings.Join(asked["therarbg"], ","), "imdb:tt40656390:movie") {
		t.Fatalf("therarbg asked %q", asked["therarbg"])
	}
	for _, a := range asked["thepiratebay"] {
		if strings.HasPrefix(a, "imdb:") {
			t.Fatal("an indexer without IMDb support was asked by id")
		}
	}
	imdb := 0
	for _, r := range sr.raw {
		if r.IMDbMatch {
			imdb++
		}
	}
	if imdb != 2 {
		t.Fatalf("%d IMDb results merged of %d", imdb, len(sr.raw))
	}
	got := titles(tsRankAll(sr.raw, q))
	if len(got) != 3 || !strings.Contains(strings.Join(got, "|"), "Kardesler Arastirma 2026 1080p NF WEB-DL DDP5 1 H 264-TURG") {
		t.Fatalf("ranked = %q", got)
	}
}

// The title is searched at once; the other names and the IMDb id join the same search when
// their lookup answers, and the search waits for them even though the indexers answered.
func TestSearchAddsNamesWhenTheyArrive(t *testing.T) {
	start := time.Now()
	var mu sync.Mutex
	asked := map[string]time.Duration{} // query -> when it was asked
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qv := r.URL.Query()
		switch {
		case qv.Get("t") == "indexers":
			w.Write([]byte(`<indexers>
<indexer id="therarbg" configured="true"><caps><searching><movie-search available="yes" supportedParams="q,imdbid" /></searching><categories><category id="2000"/></categories></caps></indexer>
</indexers>`))
		case strings.HasSuffix(r.URL.Path, "/results/torznab/api"):
			mu.Lock()
			asked["imdb:"+qv.Get("imdbid")] = time.Since(start)
			mu.Unlock()
			w.Write([]byte(torznabFeed))
		case strings.HasSuffix(r.URL.Path, "/results"):
			query := qv.Get("Query")
			mu.Lock()
			asked[query] = time.Since(start)
			mu.Unlock()
			json.NewEncoder(w).Encode(JackettResponse{Results: []JackettResult{{Title: query + " 1080p WEB-DL x264", Seeders: 9,
				Size: 2 * tsGB, MagnetUri: "magnet:?xt=urn:btih:ccc2293760e0bb60db966e755a2e34bbba26ed75"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer jackett.Close()
	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: jackett.URL, JackettAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{JackettURL: jackett.URL, JackettAPIKey: "k"})
	q := tsResolveQuery{mediaType: "movie", title: "Detective Brothers", year: 2026}
	names := &tsNamesLater{done: make(chan struct{})}
	go func() {
		time.Sleep(150 * time.Millisecond)
		names.n = tsTitleNames{names: []string{"Kardesler Arastirma"}, queries: []string{"Kardesler Arastirma"}, eager: true, imdbID: "tt40656390"}
		close(names.done)
	}()
	queries, _ := tsSearchQueries(q)
	sr, err := s.searchQueries(context.Background(), q, queries, start, names)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if at, ok := asked["Detective Brothers 2026"]; !ok || at > 100*time.Millisecond {
		t.Fatalf("title asked at %s (%v), want before the names arrived", at, ok)
	}
	for _, later := range []string{"Kardesler Arastirma 2026", "Kardesler Arastirma", "imdb:tt40656390"} {
		if at, ok := asked[later]; !ok || at < 150*time.Millisecond {
			t.Fatalf("%q asked at %s (%v), want once the names arrived", later, at, ok)
		}
	}
	found := map[string]bool{}
	for _, r := range sr.raw {
		found[r.Title] = true
	}
	if !found["Kardesler Arastirma 2026 1080p WEB-DL x264"] || sr.late != nil {
		t.Fatalf("results %v (late %v), want the other name's results in the first batch", found, sr.late != nil)
	}
}
