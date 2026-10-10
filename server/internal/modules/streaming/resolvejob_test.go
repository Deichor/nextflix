package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func parseQueryOf(s *Service, query string) (tsResolveQuery, bool, int) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	q, ok := handler{s: s}.parseQuery(c)
	return q, ok, w.Code
}

// A title in another script is fine with a tmdb_id: the English title replaces it.
func TestParseQueryNonLatinTitle(t *testing.T) {
	jp := url.QueryEscape("オデッセイ")
	s := NewService(nil, Config{TMDB: odysseyTMDB{}})
	q, ok, _ := parseQueryOf(s, "type=movie&tmdb_id=7&title="+jp)
	if !ok || q.title != "The Odyssey" {
		t.Fatalf("title %q ok %v", q.title, ok)
	}
	// Without a tmdb_id, or when TMDB has no title, nothing is searchable
	if _, ok, code := parseQueryOf(s, "type=movie&title="+jp); ok || code != http.StatusBadRequest {
		t.Fatalf("no tmdb_id: ok %v code %d", ok, code)
	}
	if _, ok, code := parseQueryOf(NewService(nil, Config{}), "type=movie&tmdb_id=7&title="+jp); ok || code != http.StatusBadRequest {
		t.Fatalf("no TMDB: ok %v code %d", ok, code)
	}
	if _, ok, code := parseQueryOf(s, "type=movie&tmdb_id=7&title="); ok || code != http.StatusBadRequest {
		t.Fatalf("empty title: ok %v code %d", ok, code)
	}
}

func doneFlight(hash string) *tsFlight {
	f := &tsFlight{done: make(chan struct{}), res: &tsResolveResult{Hash: hash}}
	close(f.done)
	return f
}

// A refresh with ?avoid= that joins a job started without it gets a fresh job, not the
// avoided torrent back.
func TestJoinAvoidingRunsAgain(t *testing.T) {
	bad, good := strings.Repeat("a", 40), strings.Repeat("b", 40)
	avoid := []string{bad}
	var starts int
	var dropped string
	start := func() (*tsFlight, bool) {
		starts++
		if starts == 1 {
			return doneFlight(bad), false // joined an older job
		}
		return doneFlight(good), true
	}
	f, ok := tsJoinAvoiding(context.Background(), start, avoid, func(h string) { dropped = h })
	if !ok || f.res.Hash != good || starts != 2 || dropped != bad {
		t.Fatalf("got %+v ok %v after %d starts (dropped %q)", f.res, ok, starts, dropped)
	}

	// A job this request started knew the list: its pick stands (everything else failed)
	starts = 0
	own := func() (*tsFlight, bool) { starts++; return doneFlight(bad), true }
	if f, ok := tsJoinAvoiding(context.Background(), own, avoid, func(string) { t.Fatal("dropped") }); !ok || f.res.Hash != bad || starts != 1 {
		t.Fatalf("own job: %+v after %d starts", f.res, starts)
	}

	// At most one fresh job
	starts = 0
	always := func() (*tsFlight, bool) { starts++; return doneFlight(bad), false }
	if _, ok := tsJoinAvoiding(context.Background(), always, avoid, func(string) {}); !ok || starts != 2 {
		t.Fatalf("%d starts", starts)
	}

	// The viewer gives up
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	running := &tsFlight{done: make(chan struct{})}
	if _, ok := tsJoinAvoiding(ctx, func() (*tsFlight, bool) { return running, false }, avoid, func(string) {}); ok || running.waiters.Load() != 0 {
		t.Fatalf("ok %v waiters %d", ok, running.waiters.Load())
	}
}
