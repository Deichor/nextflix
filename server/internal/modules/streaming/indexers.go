package streaming

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Jackett is queried per indexer instead of through its aggregate endpoint, which answers
// only when the slowest indexer does (1337x needs FlareSolverr: ~15 s per search). Resolve
// ranks as soon as enough is there: every indexer it waits for answered, or the indexers
// that usually answer fast are done (or tsIndexerQuick passed) and the results already hold
// tsSearchEnough candidates of the top wanted resolution; tsIndexerSoft is the latest. The
// deadlines count from the start of the resolve. Slower indexers keep running in the
// background and their results are merged later (see mergeLate). Bitsearch takes part like
// one more indexer.

// Vars so tests can shorten them.
var (
	tsIndexerSoft     = 3500 * time.Millisecond // resolve goes on with what arrived by then
	tsIndexerQuick    = 1200 * time.Millisecond // ... or from then on, once enough good results are there
	tsIndexerSettle   = 1500 * time.Millisecond // ... and 1080p+ counts as enough even while a fast one still runs
	tsIndexerFast     = time.Second             // an indexer whose average answer time is this or less is "fast"
	tsIndexerLateTime = 25 * time.Second        // slow indexers keep running this long
	tsIndexerListTTL  = 10 * time.Minute
)

// tsSearchEnough usable candidates of the top wanted resolution let resolve rank early.
const tsSearchEnough = 3

// Indexers that rarely carry useful movie / TV releases: queried, but never waited for.
var tsBackgroundIndexers = map[string]bool{"internetarchive": true}

type tsIndexer struct {
	ID        string
	Movies    bool // has category 2000
	TV        bool // has category 5000
	MovieIMDb bool // movie search takes an IMDb id
}

type tsIndexerList struct {
	list []tsIndexer
	at   time.Time
}

// Indexers lists the configured Jackett indexers (torznab t=indexers, works with the API key).
func (ts *TorrentSearcher) Indexers(ctx context.Context) ([]tsIndexer, error) {
	if ts.jackettURL == "" || ts.jackettAPIKey == "" {
		return nil, fmt.Errorf("jackett is not configured")
	}
	params := url.Values{"apikey": {ts.jackettAPIKey}, "t": {"indexers"}, "configured": {"true"}}
	u := fmt.Sprintf("%s/api/v2.0/indexers/all/results/torznab/api?%s", strings.TrimSuffix(ts.jackettURL, "/"), params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, tsStripURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jackett indexer list returned status %d", resp.StatusCode)
	}
	var doc struct {
		Indexers []struct {
			ID         string `xml:"id,attr"`
			Configured string `xml:"configured,attr"`
			Categories []struct {
				ID string `xml:"id,attr"`
			} `xml:"caps>categories>category"`
			MovieSearch struct {
				Available string `xml:"available,attr"`
				Params    string `xml:"supportedParams,attr"`
			} `xml:"caps>searching>movie-search"`
		} `xml:"indexer"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("jackett indexer list: %w", err)
	}
	var out []tsIndexer
	for _, ix := range doc.Indexers {
		if ix.ID == "" || strings.EqualFold(ix.Configured, "false") {
			continue
		}
		it := tsIndexer{ID: ix.ID}
		for _, c := range ix.Categories {
			it.Movies = it.Movies || c.ID == "2000"
			it.TV = it.TV || c.ID == "5000"
		}
		if ms := ix.MovieSearch; !strings.EqualFold(ms.Available, "no") {
			for _, p := range strings.Split(ms.Params, ",") {
				it.MovieIMDb = it.MovieIMDb || strings.EqualFold(strings.TrimSpace(p), "imdbid")
			}
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, errors.New("jackett has no configured indexers")
	}
	return out, nil
}

// SearchIndexer runs one query against one indexer ("all" = Jackett's aggregate endpoint).
func (ts *TorrentSearcher) SearchIndexer(ctx context.Context, indexer, query string, categories ...string) ([]JackettResult, error) {
	if ts.jackettURL == "" || ts.jackettAPIKey == "" {
		return nil, fmt.Errorf("jackett is not configured")
	}
	params := url.Values{"apikey": {ts.jackettAPIKey}, "Query": {query}}
	for _, cat := range categories {
		params.Add("Category[]", cat)
	}
	u := fmt.Sprintf("%s/api/v2.0/indexers/%s/results?%s", strings.TrimSuffix(ts.jackettURL, "/"), url.PathEscape(indexer), params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, tsStripURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jackett returned status %d", resp.StatusCode)
	}
	var jr JackettResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return nil, err
	}
	return jr.Results, nil
}

// tsStripURL drops the request URL from an error, it contains the API key.
func tsStripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("jackett request failed: %w", ue.Err)
	}
	return err
}

// tsRelevantIndexers keeps the indexers that carry the media type (YTS only has movies,
// EZTV only TV).
func tsRelevantIndexers(all []tsIndexer, mediaType string) []string {
	var out []string
	for _, ix := range all {
		if (mediaType == "movie" && ix.Movies) || (mediaType == "tv" && ix.TV) || (!ix.Movies && !ix.TV) {
			out = append(out, ix.ID)
		}
	}
	return out
}

// indexersOf returns the configured indexers of a Jackett URL (cached).
func (s *Service) indexersOf(ctx context.Context, searcher *TorrentSearcher) ([]tsIndexer, error) {
	key := searcher.JackettURL()
	if v, ok := s.indexerLists.Load(key); ok {
		if l := v.(tsIndexerList); time.Since(l.at) < tsIndexerListTTL {
			return l.list, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	list, err := searcher.Indexers(ctx)
	if err != nil {
		return nil, err
	}
	log.Printf("jackett indexer list of %s: %d indexers in %s", key, len(list), time.Since(started).Round(time.Millisecond))
	s.indexerLists.Store(key, tsIndexerList{list: list, at: time.Now()})
	return list, nil
}

// waitFor reports whether resolve waits for an indexer: not a background one, and not one
// that was slower than the soft deadline last time.
func (s *Service) waitFor(id string) bool {
	if tsBackgroundIndexers[id] {
		return false
	}
	v, ok := s.indexerTimes.Load(id)
	return !ok || v.(time.Duration) <= tsIndexerSoft
}

// fastIndexer reports whether an indexer usually answers within tsIndexerFast; one without
// a known answer time counts as fast until it answered once.
func (s *Service) fastIndexer(id string) bool {
	if tsBackgroundIndexers[id] {
		return false
	}
	v, ok := s.indexerTimes.Load(id)
	return !ok || v.(time.Duration) <= tsIndexerFast
}

// noteIndexerTime keeps a moving average of each indexer's answer time.
func (s *Service) noteIndexerTime(id string, took time.Duration, err error) {
	if err != nil && took < tsIndexerSoft {
		return // a quick failure says nothing about its speed
	}
	if v, ok := s.indexerTimes.Load(id); ok {
		took = (v.(time.Duration)*2 + took) / 3
	}
	s.indexerTimes.Store(id, took)
}

type tsIndexerFetch func(ctx context.Context, indexer, query string) ([]JackettResult, error)

// tsJob is one query to one source (a Jackett indexer or Bitsearch).
type tsJob struct{ id, query string }

func tsJobs(ids, queries []string) []tsJob {
	var out []tsJob
	for _, id := range ids {
		for _, q := range queries {
			out = append(out, tsJob{id, q})
		}
	}
	return out
}

type tsQueryDone struct {
	id  string
	res []JackettResult
	err error
}

// tsGatherPolicy says when tsGatherJobs stops waiting.
type tsGatherPolicy struct {
	start     time.Time            // soft, quick and settle count from here (zero = when the gather starts)
	soft      time.Duration        // rank with what arrived by then
	lateLimit time.Duration        // slower requests keep running this long
	wait      func(id string) bool // sources waited for until soft (nil = all)
	fast      func(id string) bool // sources that usually answer quickly (nil = none)
	quick     time.Duration        // from then on, enough results suffice while fast sources still run
	settle    time.Duration        // from then on, enough counts as settled while fast sources still run (0 = never)
	// enough reports whether the results so far are good enough to rank now (nil = never
	// early). It is asked once the fast sources are done or quick passed; settled is true
	// when both hold, or settle passed.
	enough func(early []JackettResult, settled bool) bool
	onDone func(id string, took time.Duration, err error)
	// more brings jobs that start while the gather runs (the other names of a title once
	// their lookup answered); "every waited source answered" waits until it is closed
	more <-chan []tsJob
}

// tsGathered is what tsGatherJobs collected by the time it stopped waiting.
type tsGathered struct {
	early  []JackettResult
	late   <-chan []JackettResult // results of the sources still running (nil when none is left)
	cut    string                 // why it stopped waiting
	failed map[string]bool        // sources that had finished with every request failed
}

// tsGather queries every indexer with every query at once (tsGatherJobs without an early cut).
func tsGather(ctx, bg context.Context, ids, queries []string, fetch tsIndexerFetch, soft, lateLimit time.Duration,
	wait func(id string) bool, onDone func(id string, took time.Duration, err error)) ([]JackettResult, <-chan []JackettResult, error) {
	g, err := tsGatherJobs(ctx, bg, tsJobs(ids, queries), fetch, tsGatherPolicy{soft: soft, lateLimit: lateLimit, wait: wait, onDone: onDone})
	if err != nil {
		return nil, nil, err
	}
	return g.early, g.late, nil
}

// tsAfter is a timer channel firing d after start (nil when d is 0 or below).
func tsAfter(start time.Time, d time.Duration) (<-chan time.Time, func()) {
	if d <= 0 {
		return nil, func() {}
	}
	t := time.NewTimer(max(0, d-time.Since(start)))
	return t.C, func() { t.Stop() }
}

// tsGatherJobs runs every job at once. It returns what arrived once all sources it waits
// for answered, enough arrived (see tsGatherPolicy) or soft passed, but keeps waiting while
// nothing at all arrived. Each query's results count as soon as they arrive, so a slow
// extra query does not hold back the others of its source. Jobs from pol.more start as
// they come, also after the cut. Slower queries keep running on bg for at most lateLimit;
// their results go to late as they finish, and late is closed when all are done. err is
// set when every request failed.
func tsGatherJobs(ctx, bg context.Context, jobs []tsJob, fetch tsIndexerFetch, pol tsGatherPolicy) (*tsGathered, error) {
	reqCtx, reqCancel := context.WithTimeout(bg, pol.lateLimit)
	start := pol.start
	if start.IsZero() {
		start = time.Now()
	}
	outCh := make(chan tsQueryDone, len(jobs))
	quit := make(chan struct{}) // closed once nobody reads outCh any more
	waited := func(id string) bool { return pol.wait == nil || pol.wait(id) }
	isFast := func(id string) bool { return pol.fast != nil && pol.fast(id) }
	left := map[string]int{}           // queries still running per source
	total := map[string]int{}          // queries per source
	launched := map[string]time.Time{} // when the running queries of a source started
	failures := map[string]int{}
	lastErrOf := map[string]error{}
	var mu sync.Mutex // failed is written by the late goroutine too
	failed := map[string]bool{}
	pending, waiting, fastLeft := 0, 0, 0 // sources still running
	launch := func(batch []tsJob) {
		now := time.Now()
		for _, j := range batch {
			if left[j.id] == 0 {
				pending++
				launched[j.id] = now
				if waited(j.id) {
					waiting++
				}
				if isFast(j.id) {
					fastLeft++
				}
			}
			left[j.id]++
			total[j.id]++
			go func(j tsJob) {
				res, err := fetch(reqCtx, j.id, j.query)
				select {
				case outCh <- tsQueryDone{id: j.id, res: res, err: err}:
				case <-quit:
				}
			}(j)
		}
	}
	// finish books one answered query
	finish := func(d tsQueryDone) {
		left[d.id]--
		if d.err != nil {
			failures[d.id]++
			lastErrOf[d.id] = d.err
		}
		if left[d.id] > 0 {
			return
		}
		pending--
		if waited(d.id) {
			waiting--
		}
		if isFast(d.id) {
			fastLeft--
		}
		var err error
		mu.Lock()
		if failures[d.id] == total[d.id] {
			err = lastErrOf[d.id]
			failed[d.id] = true
		} else {
			delete(failed, d.id)
		}
		mu.Unlock()
		if pol.onDone != nil {
			pol.onDone(d.id, time.Since(launched[d.id]), err)
		}
	}
	failedNow := func() map[string]bool {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]bool, len(failed))
		for id := range failed {
			out[id] = true
		}
		return out
	}
	launch(jobs)
	moreC := pol.more

	var early []JackettResult
	var lastErr error
	ok := false
	softC, stopSoft := tsAfter(start, pol.soft)
	defer stopSoft()
	var quickC, settleC <-chan time.Time
	if pol.enough != nil && pol.quick > 0 && pol.quick < pol.soft {
		c, stop := tsAfter(start, pol.quick)
		defer stop()
		quickC = c
		if pol.settle > pol.quick && pol.settle < pol.soft {
			c, stop := tsAfter(start, pol.settle)
			defer stop()
			settleC = c
		}
	}
	softPassed, quickPassed, settlePassed := false, false, false
	cut := "all answered"
	for {
		if pending == 0 && !ok {
			moreC = nil // every request failed: more queries to the same sources would not help
		}
		if pending == 0 && moreC == nil {
			break
		}
		if len(early) > 0 {
			if waiting == 0 && moreC == nil {
				cut = "every waited indexer answered"
				break
			}
			if softPassed {
				cut = "soft deadline"
				break
			}
			settled := (fastLeft == 0 && quickPassed) || settlePassed
			if pol.enough != nil && (fastLeft == 0 || quickPassed) && pol.enough(early, settled) {
				cut = "enough results"
				break
			}
		}
		select {
		case d := <-outCh:
			if d.err != nil {
				lastErr = d.err
			} else {
				ok = true
				early = append(early, d.res...)
			}
			finish(d)
		case batch, open := <-moreC:
			if !open {
				moreC = nil
				break
			}
			launch(batch)
		case <-softC:
			softPassed = true
		case <-quickC:
			quickPassed = true
		case <-settleC:
			settlePassed = true
		case <-ctx.Done():
			close(quit)
			reqCancel()
			return nil, ctx.Err()
		}
	}
	if pending == 0 && moreC == nil {
		close(quit)
		reqCancel()
		if !ok {
			if lastErr == nil {
				lastErr = errors.New("no indexer answered")
			}
			return nil, lastErr
		}
		return &tsGathered{early: early, cut: cut, failed: failedNow()}, nil
	}

	g := &tsGathered{early: early, cut: cut, failed: failedNow()}
	// Room for every running query plus extra names; a send that still finds it full waits
	// at most until lateLimit
	late := make(chan []JackettResult, 4*len(jobs)+32)
	go func() {
		defer reqCancel()
		defer close(late)
		defer close(quit)
		for pending > 0 || moreC != nil {
			select {
			case d := <-outCh:
				if d.err == nil && len(d.res) > 0 {
					select {
					case late <- d.res:
					case <-reqCtx.Done():
						return
					}
				}
				finish(d)
			case batch, open := <-moreC:
				if !open {
					moreC = nil
					break
				}
				launch(batch)
			case <-reqCtx.Done():
				return
			}
		}
	}()
	g.late = late
	return g, nil
}

// tsEnoughResults: the results hold tsSearchEnough usable candidates of the top resolution
// the client wants (2160p for HEVC clients without a cap, else 1080p or the cap; a 4K needs
// tsUHDMinSeeders). settled (the fast indexers answered and tsIndexerQuick passed, or
// tsIndexerSettle passed) lowers a 4K bar to 1080p and up: 4K is preferred, but when the
// fast majority has little of it the slow rest rarely has more, and a late one still
// becomes an upgrade.
func tsEnoughResults(raw []JackettResult, q tsResolveQuery, settled bool) bool {
	want := 1080
	switch {
	case q.maxQ > 0:
		want = q.maxQ
	case q.hevc:
		want = 2160
	}
	floor := min(want, 1080)
	top, hd := 0, 0
	for _, c := range tsRankAll(raw, q) {
		if tsAvoided(c, q) || c.quality == 0 || (c.quality == 2160 && c.raw.Seeders < tsUHDMinSeeders) {
			continue
		}
		if c.quality >= want {
			top++
		}
		if c.quality >= floor {
			hd++
		}
	}
	return top >= tsSearchEnough || (settled && hd >= tsSearchEnough)
}

// gatherPolicy is the search policy of a resolve. An upgrade search is not in a hurry and
// waits for every indexer.
func (s *Service) gatherPolicy(q tsResolveQuery) tsGatherPolicy {
	p := tsGatherPolicy{soft: tsIndexerSoft, lateLimit: tsIndexerLateTime, wait: s.waitFor, onDone: s.noteIndexerTime}
	if q.upgradeFrom > 0 {
		p.soft = tsIndexerLateTime
		return p
	}
	p.fast, p.quick, p.settle = s.fastIndexer, tsIndexerQuick, tsIndexerSettle
	p.enough = func(early []JackettResult, settled bool) bool { return tsEnoughResults(early, q, settled) }
	return p
}

type tsSearchResult struct {
	raw  []JackettResult
	base string                 // Jackett URL that answered
	late <-chan []JackettResult // results of slower indexers (nil when none is left)
	cut  string                 // why the search stopped waiting
}

// tsSearchQueries returns the queries of a resolve (title plus its eagerly searched other
// names, and for a movie with a known IMDb id the "imdb:tt…" pseudo query) and the Jackett
// category.
func tsSearchQueries(q tsResolveQuery) ([]string, string) {
	queries, category := tsNameQueries(q, q.title)
	queries = append(queries, tsExtraSearchQueries(q)...)
	if q.mediaType == "movie" && q.imdbID != "" {
		queries = append(queries, tsIMDbQueryPrefix+q.imdbID)
	}
	return queries, category
}

// tsExtraSearchQueries returns the queries for q.extraQueries alone.
func tsExtraSearchQueries(q tsResolveQuery) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range q.extraQueries {
		qs, _ := tsNameQueries(q, name)
		for _, s := range qs {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func tsNameQueries(q tsResolveQuery, name string) ([]string, string) {
	if q.mediaType == "movie" {
		var queries []string
		if q.year > 0 {
			queries = append(queries, fmt.Sprintf("%s %d", name, q.year))
		}
		return append(queries, name), "2000"
	}
	return []string{
		fmt.Sprintf("%s S%02dE%02d", name, q.season, q.episode),
		fmt.Sprintf("%s S%02d", name, q.season),
	}, "5000"
}

// tsBroadQueries are the TV queries without an episode tag, which find the other notations
// ("Cukur 94 S4 E1", "Cukur.BL.90", "Bölüm 5", whole-show packs): the other names (folded,
// at most 2), "<name> <absolute>" from season 2 on, and without other names the bare title
// (and its ASCII form).
func tsBroadQueries(q tsResolveQuery, names []string, abs int) []string {
	if q.mediaType != "tv" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) bool {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			return false
		}
		seen[k] = true
		out = append(out, s)
		return true
	}
	first := tsFold(q.title)
	taken := 0
	for _, n := range names {
		if taken < 2 && add(tsFold(n)) {
			if taken == 0 {
				first = tsFold(n)
			}
			taken++
		}
	}
	if abs > 0 && q.season > 1 {
		add(fmt.Sprintf("%s %d", first, abs))
	}
	if taken == 0 {
		// A foreign show is released under its original name; the English one bare finds
		// mostly other titles ("The Pit": "Quatermass and the Pit")
		add(q.title)
		add(tsFold(q.title))
	}
	return out
}

// tsMergeLate joins two late result channels into one (nil when both are nil).
func tsMergeLate(a, b <-chan []JackettResult) <-chan []JackettResult {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := make(chan []JackettResult, cap(a)+cap(b)) // never blocks, also without a reader
	var wg sync.WaitGroup
	for _, ch := range []<-chan []JackettResult{a, b} {
		wg.Add(1)
		go func(ch <-chan []JackettResult) {
			defer wg.Done()
			for batch := range ch {
				out <- batch
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// search queries the relevant indexers of the first Jackett URL that answers, and Bitsearch.
func (s *Service) search(ctx context.Context, q tsResolveQuery) (*tsSearchResult, error) {
	queries, _ := tsSearchQueries(q)
	return s.searchQueries(ctx, q, queries, time.Time{}, nil)
}

// tsLaterQueries are the queries a names lookup adds once it answered: the eagerly searched
// other names and, for a movie, the IMDb id. Queries in have are left out.
func tsLaterQueries(q tsResolveQuery, n tsTitleNames, have []string) []string {
	var add []string
	if n.eager {
		qq := q
		qq.extraQueries = n.queries
		add = tsExtraSearchQueries(qq)
		add = append(add, tsBroadQueries(q, n.queries, n.abs)...)
	}
	if q.mediaType == "movie" && n.imdbID != "" {
		add = append(add, tsIMDbQueryPrefix+n.imdbID)
	}
	seen := map[string]bool{}
	for _, h := range have {
		seen[h] = true
	}
	var out []string
	for _, a := range add {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// laterJobs are the jobs of tsLaterQueries for a gather round over ids (plus Bitsearch).
func (s *Service) laterJobs(q tsResolveQuery, n tsTitleNames, ids, queries []string, withSide bool) []tsJob {
	extra := tsLaterQueries(q, n, queries)
	jobs := tsJobs(ids, extra)
	if withSide && len(extra) > 0 {
		had := map[string]bool{}
		for _, b := range tsBitsearchQueries(q, queries) {
			had[b] = true
		}
		for _, b := range tsBitsearchQueries(q, append(append([]string(nil), queries...), extra...)) {
			if !had[b] {
				jobs = append(jobs, tsJob{tsBitsearchID, b})
			}
		}
	}
	return jobs
}

// searchQueries runs queries against every relevant indexer of the first Jackett URL that
// answers, with the Bitsearch queries as one more source in the same gather (same cut-off,
// same latency tracking). When every Jackett request of a URL failed the next URL is tried;
// when all failed, what Bitsearch found is the answer. The deadlines of the first round
// count from start (zero = now). names (optional) is a lookup still running: its queries
// join the gather when it answers.
func (s *Service) searchQueries(ctx context.Context, q tsResolveQuery, queries []string, start time.Time,
	names *tsNamesLater) (*tsSearchResult, error) {
	pol := s.gatherPolicy(q)
	pol.start = start
	if names != nil && pol.enough != nil {
		// Results may match the other names only
		pol.enough = func(early []JackettResult, settled bool) bool {
			qq := q
			if n, ok := names.get(); ok {
				qq.names = n.names
			}
			return tsEnoughResults(early, qq, settled)
		}
	}
	var side []tsJob
	if s.bitsearch != nil && s.bitsearchEnabled() {
		side = tsJobs([]string{tsBitsearchID}, tsBitsearchQueries(q, queries))
	}
	_, category := tsNameQueries(q, q.title)
	var lastErr error
	var sideOnly *tsGathered // Bitsearch results of a round whose Jackett requests all failed
	for round, searcher := range s.jackettSearchers() {
		ids := []string{"all"}
		imdbOK := map[string]bool{} // indexers whose movie search takes an IMDb id
		if list, err := s.indexersOf(ctx, searcher); err == nil {
			ids = tsRelevantIndexers(list, q.mediaType)
			for _, ix := range list {
				imdbOK[ix.ID] = ix.MovieIMDb
			}
		} else if ctx.Err() != nil {
			break
		} else {
			// Unreachable, or a Jackett without the indexer list: the aggregate endpoint
			log.Printf("resolve %q: jackett indexer list at %s: %v", q.title, searcher.JackettURL(), err)
		}
		if len(ids) == 0 {
			lastErr = errors.New("no jackett indexer carries this media type")
			continue
		}
		fetch := func(ctx context.Context, id, query string) ([]JackettResult, error) {
			if id == tsBitsearchID {
				return s.bitsearch.Search(ctx, query)
			}
			if imdbID, ok := tsIMDbQuery(query); ok {
				if !imdbOK[id] {
					return nil, nil
				}
				return searcher.SearchIndexerIMDb(ctx, id, imdbID)
			}
			return searcher.SearchIndexer(ctx, id, query, category)
		}
		jobs := append(tsJobs(ids, queries), side...)
		withSide := len(side) > 0
		side = nil // Bitsearch runs once
		rpol := pol
		if round > 0 {
			rpol.start = time.Time{} // a fallback Jackett gets its own deadlines
		}
		if names != nil {
			if n, ok := names.get(); ok {
				jobs = append(jobs, s.laterJobs(q, n, ids, queries, withSide)...)
			} else {
				more := make(chan []tsJob, 1) // never blocks the lookup, also after the round
				go func(ids []string, withSide bool) {
					defer close(more)
					<-names.done
					if jobs := s.laterJobs(q, names.n, ids, queries, withSide); len(jobs) > 0 {
						more <- jobs
					}
				}(ids, withSide)
				rpol.more = more
			}
		}
		g, err := tsGatherJobs(ctx, q.warmCtx(), jobs, fetch, rpol)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break // the viewer gave up
			}
			continue
		}
		jackettFailed := true
		for _, id := range ids {
			jackettFailed = jackettFailed && g.failed[id]
		}
		if !jackettFailed {
			sr := &tsSearchResult{raw: g.early, base: searcher.JackettURL(), late: g.late, cut: g.cut}
			if sideOnly != nil {
				sr.raw = append(sr.raw, sideOnly.early...)
				sr.late = tsMergeLate(sr.late, sideOnly.late)
			}
			return sr, nil
		}
		lastErr = fmt.Errorf("every indexer request to %s failed", searcher.JackettURL())
		if withSide {
			sideOnly = g
		}
	}
	if lastErr == nil {
		lastErr = errors.New("jackett is not configured")
	}
	if sideOnly != nil && (len(sideOnly.early) > 0 || sideOnly.late != nil) {
		log.Printf("resolve %q: jackett failed (%v), using %d bitsearch results", q.title, lastErr, len(sideOnly.early))
		return &tsSearchResult{raw: sideOnly.early, late: sideOnly.late, cut: sideOnly.cut}, nil
	}
	return nil, lastErr
}
