package streaming

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJackettGateLimits(t *testing.T) {
	g := newJackettGate()
	var rel []func()
	take := func(id string, slow, bg, want bool) {
		t.Helper()
		r, ok := g.take(id, slow, bg)
		if ok != want {
			t.Fatalf("take(%s slow=%v bg=%v) = %v, want %v", id, slow, bg, ok, want)
		}
		if ok {
			rel = append(rel, r)
		}
	}
	// Background checks leave the slow indexers (and FlareSolverr) to the viewer
	for range tsJackettMaxSlowBg {
		take("1337x", true, true, true)
	}
	take("uindex", true, true, false)
	for range tsJackettMaxSlow - tsJackettMaxSlowBg {
		take("1337x", true, false, true)
	}
	take("1337x", true, false, false)
	// Fast indexers count on their own
	for range tsJackettMaxFastBg {
		take("knaben", false, true, true)
	}
	take("knaben", false, true, false)
	take("therarbg", false, true, true)
	for range tsJackettMaxFast - tsJackettMaxFastBg {
		take("knaben", false, false, true)
	}
	take("knaben", false, false, false)
	for _, r := range rel {
		r()
	}
	if g.slow != 0 || g.slowBg != 0 || g.fast["knaben"] != 0 || g.fastBg["knaben"] != 0 {
		t.Fatalf("not released: slow %d, background %d, fast %d, fast background %d", g.slow, g.slowBg, g.fast["knaben"], g.fastBg["knaben"])
	}
}

// A caller that gives up gets its answer at once, but the request keeps its place until
// Jackett answered: that is what keeps abandoned requests from piling up in Jackett.
func TestJackettGateKeepsAbandonedRequests(t *testing.T) {
	g := newJackettGate()
	answer := make(chan struct{})
	reqCancelled := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := g.do(ctx, "1337x", true, true, func(rctx context.Context) ([]JackettResult, error) {
		<-answer
		reqCancelled <- rctx.Err() != nil
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	open := func() int {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.slow
	}
	if n := open(); n != 1 {
		t.Fatalf("open requests after the caller left: %d", n)
	}
	close(answer)
	if <-reqCancelled {
		t.Fatal("the request was cancelled with its caller")
	}
	deadline := time.Now().Add(time.Second)
	for open() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the place was not given back")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestJackettGateSkipsOverLimit(t *testing.T) {
	g := newJackettGate()
	for range tsJackettMaxSlowBg {
		if _, ok := g.take("1337x", true, true); !ok {
			t.Fatal("take failed")
		}
	}
	called := false
	_, err := g.do(context.Background(), "1337x", true, true, func(context.Context) ([]JackettResult, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, errIndexerBusy) || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}

// A Jackett that stopped answering is asked again only after the retry time, the last known
// list keeps the search per indexer, and it counts as down for the resolve's answer.
func TestIndexerListRemembersFailure(t *testing.T) {
	s := NewService(nil, Config{})
	searcher := &TorrentSearcher{jackettURL: "http://jackett.invalid:9117", jackettAPIKey: "k"}
	key := searcher.JackettURL()
	known := []tsIndexer{{ID: "knaben"}}
	if s.jackettFailedWithin(tsJackettDownFor) {
		t.Fatal("down before anything was asked")
	}
	s.indexerLists.Store(key, tsIndexerList{list: known, at: time.Now().Add(-time.Hour), failedAt: time.Now()})
	started := time.Now()
	list, err := s.indexersOf(context.Background(), searcher)
	if err != nil || len(list) != 1 || list[0].ID != "knaben" {
		t.Fatalf("list %v, err %v", list, err)
	}
	if d := time.Since(started); d > 100*time.Millisecond {
		t.Fatalf("waited %s for a Jackett that does not answer", d)
	}
	if !s.jackettFailedWithin(tsJackettDownFor) {
		t.Fatal("not reported as down")
	}
	s.indexerLists.Store(key, tsIndexerList{list: known, at: time.Now()})
	if s.jackettFailedWithin(tsJackettDownFor) {
		t.Fatal("reported as down after a good fetch")
	}
}
