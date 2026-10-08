package streaming

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeIndexers answers each indexer after its delay with one result named after it.
func fakeIndexers(delays map[string]time.Duration, fail map[string]bool) tsIndexerFetch {
	return func(ctx context.Context, id, query string) ([]JackettResult, error) {
		select {
		case <-time.After(delays[id]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if fail[id] {
			return nil, errors.New(id + " failed")
		}
		if strings.HasPrefix(id, "empty") {
			return nil, nil
		}
		return []JackettResult{{Title: id + " " + query}}, nil
	}
}

func trackers(res []JackettResult) string {
	var out []string
	for _, r := range res {
		out = append(out, strings.Fields(r.Title)[0])
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func waitAll(ids ...string) func(string) bool { return func(string) bool { return true } }

func TestGatherSoftDeadlineThenLateMerge(t *testing.T) {
	ms := time.Millisecond
	delays := map[string]time.Duration{"yts": 10 * ms, "tpb": 20 * ms, "1337x": 300 * ms}
	start := time.Now()
	early, late, err := tsGather(context.Background(), context.Background(), []string{"yts", "tpb", "1337x"}, []string{"q"},
		fakeIndexers(delays, nil), 80*ms, 2*time.Second, waitAll(), nil)
	took := time.Since(start)
	if err != nil || trackers(early) != "tpb,yts" {
		t.Fatalf("early = %q, %v", trackers(early), err)
	}
	if took < 70*ms || took > 200*ms {
		t.Fatalf("returned after %s, want the soft deadline (80ms)", took)
	}
	if late == nil {
		t.Fatal("no late channel while 1337x is still searching")
	}
	var got []JackettResult
	for batch := range late {
		got = append(got, batch...)
	}
	if trackers(got) != "1337x" {
		t.Fatalf("late = %q, want the slow indexer's results", trackers(got))
	}
}

func TestGatherReturnsOnceWaitedIndexersAnswer(t *testing.T) {
	ms := time.Millisecond
	delays := map[string]time.Duration{"yts": 10 * ms, "1337x": 400 * ms, "internetarchive": 400 * ms}
	wait := func(id string) bool { return id == "yts" } // the others were slow last time
	start := time.Now()
	early, late, err := tsGather(context.Background(), context.Background(), []string{"yts", "1337x", "internetarchive"},
		[]string{"a", "b"}, fakeIndexers(delays, nil), time.Second, 2*time.Second, wait, nil)
	if err != nil || trackers(early) != "yts,yts" || time.Since(start) > 150*ms {
		t.Fatalf("early = %q after %s (%v), want both yts queries at once", trackers(early), time.Since(start), err)
	}
	n := 0
	for batch := range late {
		n += len(batch)
	}
	if n != 4 {
		t.Fatalf("late results: %d, want 2 indexers x 2 queries", n)
	}
}

func TestGatherWaitsWhileNothingArrived(t *testing.T) {
	ms := time.Millisecond
	// Only the slow indexer has the title: resolve must not give up at the soft deadline
	delays := map[string]time.Duration{"empty-yts": 5 * ms, "1337x": 150 * ms}
	early, late, err := tsGather(context.Background(), context.Background(), []string{"empty-yts", "1337x"}, []string{"q"},
		fakeIndexers(delays, nil), 20*ms, 2*time.Second, waitAll(), nil)
	if err != nil || trackers(early) != "1337x" || late != nil {
		t.Fatalf("early = %q late=%v err=%v, want the slow results and nothing pending", trackers(early), late, err)
	}
}

func TestGatherErrorsAndLimits(t *testing.T) {
	ms := time.Millisecond
	fetch := fakeIndexers(map[string]time.Duration{"a": ms, "b": 2 * ms}, map[string]bool{"a": true, "b": true})
	if _, _, err := tsGather(context.Background(), context.Background(), []string{"a", "b"}, []string{"q"}, fetch, 50*ms, time.Second, waitAll(), nil); err == nil {
		t.Fatal("no error although every indexer failed (the 502 path)")
	}
	// One failing indexer is fine
	fetch = fakeIndexers(map[string]time.Duration{"a": ms, "b": 2 * ms}, map[string]bool{"a": true})
	if early, _, err := tsGather(context.Background(), context.Background(), []string{"a", "b"}, []string{"q"}, fetch, 50*ms, time.Second, waitAll(), nil); err != nil || trackers(early) != "b" {
		t.Fatalf("early = %q, %v", trackers(early), err)
	}
	// Slow indexers are bounded by the late limit
	fetch = fakeIndexers(map[string]time.Duration{"fast": ms, "slow": time.Minute}, nil)
	start := time.Now()
	_, late, _ := tsGather(context.Background(), context.Background(), []string{"fast", "slow"}, []string{"q"}, fetch, 10*ms, 100*ms, waitAll(), nil)
	for range late {
	}
	if time.Since(start) > time.Second {
		t.Fatalf("late search ran %s, want it cut at the late limit", time.Since(start))
	}
	// A released title cancels them at once
	bg, cancel := context.WithCancel(context.Background())
	start = time.Now()
	_, late, _ = tsGather(context.Background(), bg, []string{"fast", "slow"}, []string{"q"}, fetch, 10*ms, time.Minute, waitAll(), nil)
	cancel()
	for range late {
	}
	if time.Since(start) > time.Second {
		t.Fatal("late search not cancelled with the title")
	}
}

func TestRelevantIndexers(t *testing.T) {
	all := []tsIndexer{
		{ID: "yts", Movies: true}, {ID: "eztv", TV: true}, {ID: "1337x", Movies: true, TV: true}, {ID: "nyaasi", Movies: true, TV: true},
	}
	if got := strings.Join(tsRelevantIndexers(all, "movie"), ","); got != "yts,1337x,nyaasi" {
		t.Fatalf("movie indexers: %s", got)
	}
	if got := strings.Join(tsRelevantIndexers(all, "tv"), ","); got != "eztv,1337x,nyaasi" {
		t.Fatalf("tv indexers: %s", got)
	}
}

func TestIndexerListAndSlowness(t *testing.T) {
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "indexers" || r.URL.Query().Get("apikey") != "k" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><indexers>
<indexer id="yts" configured="true"><caps><categories><category id="2000" name="Movies"/></categories></caps></indexer>
<indexer id="eztv" configured="true"><caps><categories><category id="5000" name="TV"/></categories></caps></indexer>
<indexer id="1337x" configured="true"><caps><categories><category id="2000"/><category id="5000"/></categories></caps></indexer>
</indexers>`))
	}))
	defer jackett.Close()
	list, err := NewTorrentSearcher(jackett.URL, "k", 0).Indexers(context.Background())
	if err != nil || len(list) != 3 || !list[0].Movies || list[0].TV || !list[1].TV || !list[2].Movies || !list[2].TV {
		t.Fatalf("indexers: %+v %v", list, err)
	}

	s := NewService(nil, Config{})
	if !s.waitFor("1337x") || s.waitFor("internetarchive") {
		t.Fatal("unknown indexers are waited for, background ones never")
	}
	s.noteIndexerTime("1337x", 14500*time.Millisecond, nil)
	if s.waitFor("1337x") {
		t.Fatal("an indexer slower than the soft deadline is still waited for")
	}
	s.noteIndexerTime("yts", 2600*time.Millisecond, nil)
	if !s.waitFor("yts") {
		t.Fatal("a fast indexer is not waited for")
	}
}

func TestGatherDeliversEachQuery(t *testing.T) {
	ms := time.Millisecond
	// The English-title query is fast, the original-title query of the same indexer is slow
	fetch := func(ctx context.Context, id, query string) ([]JackettResult, error) {
		d := 5 * ms
		if query == "slow" {
			d = 300 * ms
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []JackettResult{{Title: id + " " + query}}, nil
	}
	start := time.Now()
	early, late, err := tsGather(context.Background(), context.Background(), []string{"tpb"}, []string{"fast", "slow"},
		fetch, 50*ms, 2*time.Second, waitAll(), nil)
	if err != nil || len(early) != 1 || early[0].Title != "tpb fast" || time.Since(start) > 200*ms {
		t.Fatalf("early = %v after %s (%v), want the fast query at the soft deadline", early, time.Since(start), err)
	}
	var got []JackettResult
	for batch := range tsMergeLate(late, nil) {
		got = append(got, batch...)
	}
	if len(got) != 1 || got[0].Title != "tpb slow" {
		t.Fatalf("late = %v", got)
	}
}

func TestMergeLate(t *testing.T) {
	a, b := make(chan []JackettResult, 2), make(chan []JackettResult, 2)
	a <- []JackettResult{{Title: "a1"}}
	b <- []JackettResult{{Title: "b1"}}
	b <- []JackettResult{{Title: "b2"}}
	close(a)
	close(b)
	n := 0
	for batch := range tsMergeLate(a, b) {
		n += len(batch)
	}
	if n != 3 || tsMergeLate(nil, nil) != nil {
		t.Fatalf("merged %d batches", n)
	}
}

// releases answers each source after its delay with releases of the given resolution.
func releases(delays map[string]time.Duration, quality map[string]string) tsIndexerFetch {
	return func(ctx context.Context, id, query string) ([]JackettResult, error) {
		select {
		case <-time.After(delays[id]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var out []JackettResult
		for i := 0; i < 3; i++ {
			out = append(out, JackettResult{Title: fmt.Sprintf("Charade 1963 %s BluRay x264-%s%d", quality[id], id, i),
				Seeders: 20, Size: 4 * tsGB, MagnetUri: fmt.Sprintf("magnet:?xt=urn:btih:%038x%02d", int(id[0]), i)})
		}
		return out, nil
	}
}

func cutPolicy(q tsResolveQuery, fast ...string) tsGatherPolicy {
	isFast := map[string]bool{}
	for _, id := range fast {
		isFast[id] = true
	}
	return tsGatherPolicy{
		soft: 400 * time.Millisecond, lateLimit: 2 * time.Second, quick: 120 * time.Millisecond,
		wait:   func(string) bool { return true },
		fast:   func(id string) bool { return isFast[id] },
		enough: func(early []JackettResult, settled bool) bool { return tsEnoughResults(early, q, settled) },
	}
}

func TestGatherCutsOnceEnoughIsThere(t *testing.T) {
	ms := time.Millisecond
	movie := tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}
	jobs := tsJobs([]string{"yts", "tpb", "kickass"}, []string{"q"})

	// The fast indexers bring three 1080p releases: no waiting for kickass (slow, but waited for)
	fetch := releases(map[string]time.Duration{"yts": 10 * ms, "tpb": 20 * ms, "kickass": 300 * ms},
		map[string]string{"yts": "1080p", "tpb": "1080p", "kickass": "1080p"})
	start := time.Now()
	g, err := tsGatherJobs(context.Background(), context.Background(), jobs, fetch, cutPolicy(movie, "yts", "tpb"))
	if err != nil || g.cut != "enough results" || time.Since(start) > 150*ms || g.late == nil {
		t.Fatalf("cut %q after %s (%v)", g.cut, time.Since(start), err)
	}

	// A fast indexer still running: enough results wait for tsIndexerQuick, not the soft deadline
	fetch = releases(map[string]time.Duration{"yts": 10 * ms, "tpb": 350 * ms, "kickass": 350 * ms},
		map[string]string{"yts": "1080p", "tpb": "1080p", "kickass": "1080p"})
	start = time.Now()
	g, _ = tsGatherJobs(context.Background(), context.Background(), jobs, fetch, cutPolicy(movie, "yts", "tpb"))
	if took := time.Since(start); g.cut != "enough results" || took < 100*ms || took > 300*ms {
		t.Fatalf("cut %q after %s, want at the quick mark", g.cut, took)
	}

	// An HEVC client wants 4K: 1080p from the fast ones is only enough once they are all done
	// and the quick mark passed; 720p never is (soft deadline)
	hevc := movie
	hevc.hevc = true
	fetch = releases(map[string]time.Duration{"yts": 10 * ms, "tpb": 20 * ms, "kickass": 350 * ms},
		map[string]string{"yts": "1080p", "tpb": "720p", "kickass": "1080p"})
	start = time.Now()
	g, _ = tsGatherJobs(context.Background(), context.Background(), jobs, fetch, cutPolicy(hevc, "yts", "tpb"))
	if took := time.Since(start); g.cut != "enough results" || took < 100*ms || took > 300*ms {
		t.Fatalf("HEVC, 1080p only: cut %q after %s", g.cut, took)
	}
	fetch = releases(map[string]time.Duration{"yts": 10 * ms, "tpb": 20 * ms, "kickass": 600 * ms},
		map[string]string{"yts": "720p", "tpb": "720p", "kickass": "1080p"})
	g, _ = tsGatherJobs(context.Background(), context.Background(), jobs, fetch, cutPolicy(movie, "yts", "tpb"))
	if g.cut != "soft deadline" {
		t.Fatalf("720p only: cut %q, want the soft deadline", g.cut)
	}
}

func TestFastIndexerFromAverage(t *testing.T) {
	s := NewService(nil, Config{})
	if !s.fastIndexer("yts") || s.fastIndexer("internetarchive") {
		t.Fatal("unknown indexers count as fast until measured, background ones never")
	}
	s.noteIndexerTime("yts", 300*time.Millisecond, nil)
	s.noteIndexerTime("kickasstorrents-to", 2200*time.Millisecond, nil)
	if !s.fastIndexer("yts") || s.fastIndexer("kickasstorrents-to") || !s.waitFor("kickasstorrents-to") {
		t.Fatal("0.3 s is fast; 2.2 s is waited for but not fast")
	}
}

// The deadlines count from pol.start (the start of the resolve), not from the gather: time
// spent on lookups before it is not added on top.
func TestGatherDeadlinesFromStart(t *testing.T) {
	ms := time.Millisecond
	movie := tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}
	jobs := tsJobs([]string{"yts", "tpb", "kickass"}, []string{"q"})
	fetch := releases(map[string]time.Duration{"yts": 10 * ms, "tpb": 20 * ms, "kickass": 2 * time.Second},
		map[string]string{"yts": "720p", "tpb": "720p", "kickass": "1080p"})
	pol := cutPolicy(movie, "yts", "tpb")
	pol.start = time.Now().Add(-300 * ms) // soft is 400 ms
	start := time.Now()
	g, err := tsGatherJobs(context.Background(), context.Background(), jobs, fetch, pol)
	if took := time.Since(start); err != nil || g.cut != "soft deadline" || took > 250*ms {
		t.Fatalf("cut %q after %s (%v), want the soft deadline 100 ms in", g.cut, took, err)
	}
}

// 1080p and up from the fast indexers is enough for an HEVC client once settle passed, even
// while a fast indexer is still running.
func TestGatherSettlesWithHD(t *testing.T) {
	ms := time.Millisecond
	hevc := tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963, hevc: true}
	jobs := tsJobs([]string{"yts", "tpb", "kickass"}, []string{"q"})
	fetch := releases(map[string]time.Duration{"yts": 10 * ms, "tpb": 600 * ms, "kickass": 600 * ms},
		map[string]string{"yts": "1080p", "tpb": "2160p", "kickass": "1080p"})
	pol := cutPolicy(hevc, "yts", "tpb")
	pol.soft, pol.settle = 500*ms, 200*ms
	start := time.Now()
	g, _ := tsGatherJobs(context.Background(), context.Background(), jobs, fetch, pol)
	if took := time.Since(start); g.cut != "enough results" || took < 180*ms || took > 400*ms {
		t.Fatalf("cut %q after %s, want enough at the settle mark", g.cut, took)
	}
}

// Jobs from pol.more (other names of the title) start when they arrive: before the cut
// their results count at once, after it they arrive as late results. A gather whose sources
// all answered still waits for them.
func TestGatherAddsLaterJobs(t *testing.T) {
	ms := time.Millisecond
	fetch := fakeIndexers(map[string]time.Duration{"a": 10 * ms, "b": 10 * ms}, nil)
	jobs := tsJobs([]string{"a", "b"}, []string{"title"})
	later := func(after time.Duration) <-chan []tsJob {
		more := make(chan []tsJob, 1)
		go func() {
			time.Sleep(after)
			more <- tsJobs([]string{"a", "b"}, []string{"other"})
			close(more)
		}()
		return more
	}

	start := time.Now()
	g, err := tsGatherJobs(context.Background(), context.Background(), jobs, fetch,
		tsGatherPolicy{soft: time.Second, lateLimit: 2 * time.Second, more: later(80 * ms)})
	if took := time.Since(start); err != nil || len(g.early) != 4 || g.late != nil || took < 80*ms || took > 500*ms {
		t.Fatalf("got %d results, late %v after %s (%v), want all 4 once the later jobs answered", len(g.early), g.late != nil, took, err)
	}

	g, err = tsGatherJobs(context.Background(), context.Background(), jobs, fetch,
		tsGatherPolicy{soft: 50 * ms, lateLimit: 2 * time.Second, more: later(150 * ms)})
	if err != nil || g.cut != "soft deadline" || len(g.early) != 2 || g.late == nil {
		t.Fatalf("cut %q with %d results (%v)", g.cut, len(g.early), err)
	}
	var late []string
	for batch := range g.late {
		for _, r := range batch {
			late = append(late, r.Title)
		}
	}
	sort.Strings(late)
	if strings.Join(late, "|") != "a other|b other" {
		t.Fatalf("late = %q", late)
	}

	// Every request failed: no waiting for more queries to the same sources
	bad := fakeIndexers(map[string]time.Duration{"a": 5 * ms, "b": 5 * ms}, map[string]bool{"a": true, "b": true})
	start = time.Now()
	if _, err := tsGatherJobs(context.Background(), context.Background(), jobs, bad,
		tsGatherPolicy{soft: time.Second, lateLimit: 2 * time.Second, more: later(time.Second)}); err == nil || time.Since(start) > 300*ms {
		t.Fatalf("err %v after %s, want a quick failure", err, time.Since(start))
	}
}

// Settled, any 1080p+ counts: a couple of 4K among plenty of 1080p no longer holds the
// search until the soft deadline.
func TestEnoughResultsAnyHDOnceSettled(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	rel := func(i int, quality, codec string) JackettResult {
		return JackettResult{Title: fmt.Sprintf("Night of the Living Dead 1968 %s BluRay %s-G%d", quality, codec, i), Seeders: 30,
			Size: 6 * tsGB, MagnetUri: fmt.Sprintf("magnet:?xt=urn:btih:%040x", i+1)}
	}
	raw := []JackettResult{rel(0, "2160p", "x265"), rel(1, "2160p", "x265"), rel(2, "1080p", "x264"), rel(3, "1080p", "x264"), rel(4, "1080p", "x264")}
	if tsEnoughResults(raw, q, false) {
		t.Fatal("two 4K are not enough before the fast indexers settled")
	}
	if !tsEnoughResults(raw, q, true) {
		t.Fatal("five 1080p+ are enough once settled")
	}
	if !tsEnoughResults(raw[:3], q, true) {
		t.Fatal("three 1080p+ (two 4K, one 1080p) are enough once settled")
	}
	if tsEnoughResults(raw[:2], q, true) {
		t.Fatal("two candidates are never enough")
	}
}
