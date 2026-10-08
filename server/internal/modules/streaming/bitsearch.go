package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bitsearch (a DHT index) lists torrents no Jackett indexer has, e.g. single files shared
// as "Kardeşler.Araştırma.2026.mp4". Its search page is scraped and the results join the
// Jackett ones (see searchQueries): same ranking, dedupe and health checks.

const (
	tsBitsearchID     = "native:bitsearch" // its id in tsGather and the latency averages (Jackett has a "bitsearch" indexer too)
	bsDefaultBase     = "https://bitsearch.to"
	bsGap             = time.Second // at most one request per second
	bsCacheTTL        = 30 * time.Minute
	bsDownFor         = 2 * time.Minute // pause after a failure
	bsRequestTime     = 12 * time.Second
	bsCookieTTL       = 30 * time.Minute
	bsMaxQueries      = 4 // per resolve
	bsUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"
	bsMaxPageBytes    = 4 << 20
	bsFlareSolverTime = 75 * time.Second
)

// tsOpenTrackers are well-known open trackers added to magnets built from a bare info hash.
var tsOpenTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://explodie.org:6969/announce",
	"udp://tracker.theoks.net:6969/announce",
	"udp://tracker.qu.ax:6969/announce",
}

var errBitsearchDown = errors.New("bitsearch is paused after a failure")

// bsItem is one search result of the Bitsearch page.
type bsItem struct {
	Title    string
	Category string
	Size     int64
	Date     time.Time
	Seeders  int
	Leechers int
	InfoHash string   // lowercase hex
	Trackers []string // from the page's magnet ("DHT" dropped)
}

type bsCacheEntry struct {
	res []JackettResult
	at  time.Time
}

type bsFlight struct {
	done chan struct{}
	res  []JackettResult
	err  error
}

type bitsearch struct {
	flare  string // FlareSolverr endpoint, only used on a Cloudflare challenge
	client *http.Client

	mu        sync.Mutex
	base      string    // follows the site's redirects (bitsearch.to -> bitsearch.eu)
	next      time.Time // earliest start of the next request
	downUntil time.Time
	cache     map[string]bsCacheEntry
	flights   map[string]*bsFlight
	cfCookies string
	cfUA      string
	cfExpires time.Time
}

func newBitsearch(base, flare string) *bitsearch {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		base = bsDefaultBase
	}
	return &bitsearch{
		base:    base,
		flare:   flare,
		client:  &http.Client{Timeout: bsRequestTime},
		cache:   map[string]bsCacheEntry{},
		flights: map[string]*bsFlight{},
	}
}

// bsKey is the query Bitsearch gets: it folds accents itself, so "Kardeşler Araştırma" and
// "Kardesler Arastirma" are one request.
func bsKey(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(tsFold(query))), " ")
}

// tsBitsearchQueries picks the Bitsearch queries of a resolve: every name once, and for a
// movie with a year only the "<name> <year>" form (keyword search, so it is the precise one).
// TV goes episode queries first, then absolute and bare ones, then season ones; a season
// query is left out when its bare name is searched (keyword search finds it too).
func tsBitsearchQueries(q tsResolveQuery, queries []string) []string {
	var keys []string
	seen := map[string]bool{}
	yearSuffix := ""
	if q.mediaType == "movie" && q.year > 0 {
		yearSuffix = " " + strconv.Itoa(q.year)
	}
	for _, query := range queries {
		if _, ok := tsIMDbQuery(query); ok {
			continue
		}
		key := bsKey(query)
		if key == "" || seen[key] || (yearSuffix != "" && !strings.HasSuffix(key, yearSuffix)) {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if q.mediaType == "tv" {
		class := func(k string) int {
			switch {
			case bsEpisodeKey.MatchString(k):
				return 0
			case bsSeasonKey.MatchString(k):
				return 2
			}
			return 1
		}
		var kept []string
		for _, k := range keys {
			if m := bsSeasonKey.FindStringSubmatch(k); m != nil && seen[m[1]] {
				continue
			}
			kept = append(kept, k)
		}
		sort.SliceStable(kept, func(i, j int) bool { return class(kept[i]) < class(kept[j]) })
		keys = kept
	}
	if len(keys) > bsMaxQueries {
		keys = keys[:bsMaxQueries]
	}
	return keys
}

var (
	bsEpisodeKey = regexp.MustCompile(` s\d{1,2}e\d{1,4}$`)
	bsSeasonKey  = regexp.MustCompile(`^(.+) s\d{1,2}$`)
)

// Search returns the video results of one query, cached for 30 minutes. Concurrent calls
// for one query share the request.
func (b *bitsearch) Search(ctx context.Context, query string) ([]JackettResult, error) {
	key := bsKey(query)
	if key == "" {
		return nil, nil
	}
	b.mu.Lock()
	if e, ok := b.cache[key]; ok && time.Since(e.at) < bsCacheTTL {
		b.mu.Unlock()
		return e.res, nil
	}
	if time.Now().Before(b.downUntil) {
		b.mu.Unlock()
		return nil, errBitsearchDown
	}
	if f, ok := b.flights[key]; ok {
		b.mu.Unlock()
		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &bsFlight{done: make(chan struct{})}
	b.flights[key] = f
	b.mu.Unlock()

	f.res, f.err = b.fetch(ctx, key)

	b.mu.Lock()
	delete(b.flights, key)
	now := time.Now()
	switch {
	case f.err == nil:
		for k, e := range b.cache {
			if now.Sub(e.at) >= bsCacheTTL {
				delete(b.cache, k)
			}
		}
		b.cache[key] = bsCacheEntry{res: f.res, at: now}
	case ctx.Err() == nil:
		b.downUntil = now.Add(bsDownFor)
		log.Printf("bitsearch: %v (paused for %s)", f.err, bsDownFor)
	}
	b.mu.Unlock()
	close(f.done)
	return f.res, f.err
}

// wait keeps the requests at least bsGap apart.
func (b *bitsearch) wait(ctx context.Context) error {
	b.mu.Lock()
	at := b.next
	if now := time.Now(); at.Before(now) {
		at = now
	}
	b.next = at.Add(bsGap)
	b.mu.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *bitsearch) fetch(ctx context.Context, key string) ([]JackettResult, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	pageURL := b.base + "/search?" + url.Values{"q": {key}}.Encode()
	b.mu.Unlock()
	page, err := b.get(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	var out []JackettResult
	for _, it := range tsParseBitsearch(page) {
		if bsVideoCategory(it.Category) {
			out = append(out, it.result())
		}
	}
	return out, nil
}

func (b *bitsearch) get(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	ua := bsUserAgent
	b.mu.Lock()
	if time.Now().Before(b.cfExpires) {
		ua = b.cfUA
		req.Header.Set("Cookie", b.cfCookies)
	}
	b.mu.Unlock()
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("bitsearch request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, bsMaxPageBytes))
	if err != nil {
		return "", err
	}
	if final := resp.Request.URL; final != nil {
		b.followBase(final)
	}
	if bsChallenge(resp, body) {
		return b.solve(ctx, pageURL)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bitsearch returned status %d", resp.StatusCode)
	}
	return string(body), nil
}

// followBase remembers where the site redirected to, so later searches skip the redirect.
func (b *bitsearch) followBase(final *url.URL) {
	base := final.Scheme + "://" + final.Host
	b.mu.Lock()
	defer b.mu.Unlock()
	if final.Host != "" && base != b.base {
		log.Printf("bitsearch: now at %s (was %s)", base, b.base)
		b.base = base
	}
}

func bsChallenge(resp *http.Response, body []byte) bool {
	if resp.Header.Get("cf-mitigated") == "challenge" {
		return true
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	head := body[:min(len(body), 4096)]
	return bytes.Contains(head, []byte("challenge-platform")) || bytes.Contains(head, []byte("Just a moment"))
}

// solve loads a page through FlareSolverr and keeps its cookies for the next direct requests.
func (b *bitsearch) solve(ctx context.Context, pageURL string) (string, error) {
	if b.flare == "" {
		return "", errors.New("bitsearch: Cloudflare challenge and no FlareSolverr")
	}
	payload, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": pageURL, "maxTimeout": 60000})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.flare, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: bsFlareSolverTime}).Do(req)
	if err != nil {
		return "", fmt.Errorf("bitsearch FlareSolverr: %w", err)
	}
	defer resp.Body.Close()
	var res struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status    int    `json:"status"`
			Response  string `json:"response"`
			UserAgent string `json:"userAgent"`
			Cookies   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&res); err != nil {
		return "", fmt.Errorf("bitsearch FlareSolverr: %w", err)
	}
	if res.Status != "ok" || res.Solution.Status != http.StatusOK {
		return "", fmt.Errorf("bitsearch FlareSolverr: %s (status %d)", res.Message, res.Solution.Status)
	}
	var cookies []string
	for _, c := range res.Solution.Cookies {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	b.mu.Lock()
	b.cfCookies, b.cfUA, b.cfExpires = strings.Join(cookies, "; "), res.Solution.UserAgent, time.Now().Add(bsCookieTTL)
	b.mu.Unlock()
	return res.Solution.Response, nil
}

// bsVideoCategory keeps what can be a movie or an episode. Category filters on the site
// itself would lose most DHT finds: they are filed as "Other/Video".
func bsVideoCategory(cat string) bool {
	c := strings.ToLower(strings.TrimSpace(cat))
	return c == "" || c == "other" || c == "other/video" ||
		strings.HasPrefix(c, "movies") || strings.HasPrefix(c, "tv") || strings.HasPrefix(c, "anime")
}

func (it bsItem) result() JackettResult {
	trackers := append([]string(nil), it.Trackers...)
	for _, tr := range tsOpenTrackers {
		dup := false
		for _, have := range trackers {
			dup = dup || have == tr
		}
		if !dup {
			trackers = append(trackers, tr)
		}
	}
	return JackettResult{
		Title:        it.Title,
		MagnetUri:    tsBuildMagnet(it.InfoHash, it.Title, trackers),
		Seeders:      it.Seeders,
		Peers:        it.Seeders + it.Leechers,
		Size:         it.Size,
		CategoryDesc: it.Category,
		Tracker:      "Bitsearch",
		InfoHash:     it.InfoHash,
	}
}

var (
	bsCardPattern     = regexp.MustCompile(`href="/torrent/[^"]*"[^>]*>`)
	bsTagPattern      = regexp.MustCompile(`<[^>]*>`)
	bsHashPattern     = regexp.MustCompile(`(?i)/download/torrent/([0-9a-f]{40})|btih:([0-9a-f]{40})`)
	bsMagnetPattern   = regexp.MustCompile(`href="(magnet:\?[^"]+)"`)
	bsLabelPattern    = regexp.MustCompile(`(?i)<i class="[^"]*"></i>\s*<span>([^<]+)</span>`)
	bsSeedersPattern  = regexp.MustCompile(`(?is)>\s*([\d,.]+)\s*</span>\s*<span[^>]*>\s*seeders?\s*<`)
	bsLeechersPattern = regexp.MustCompile(`(?is)>\s*([\d,.]+)\s*</span>\s*<span[^>]*>\s*leechers?\s*<`)
	bsSizePattern     = regexp.MustCompile(`(?i)^([\d.,]+)\s*([KMGT]i?B|B|bytes)$`)
	bsDatePattern     = regexp.MustCompile(`^\d{1,2}/\d{1,2}/\d{4}$`)
)

// tsParseBitsearch reads the result cards of a Bitsearch search page. A card starts with
// the link to its detail page; title, category, size, date, seeders, leechers, the
// download link (info hash) and the magnet follow before the next card.
func tsParseBitsearch(page string) []bsItem {
	if i := strings.Index(page, "</main>"); i > 0 {
		page = page[:i]
	}
	starts := bsCardPattern.FindAllStringIndex(page, -1)
	var out []bsItem
	for n, at := range starts {
		end := len(page)
		if n+1 < len(starts) {
			end = starts[n+1][0]
		}
		card := page[at[1]:end]
		title := card
		if i := strings.Index(title, "</a>"); i >= 0 {
			title = title[:i]
		}
		it := bsItem{Title: strings.Join(strings.Fields(html.UnescapeString(bsTagPattern.ReplaceAllString(title, " "))), " ")}
		if m := bsHashPattern.FindStringSubmatch(card); m != nil {
			it.InfoHash = strings.ToLower(m[1] + m[2])
		}
		if it.Title == "" || it.InfoHash == "" {
			continue
		}
		for _, m := range bsLabelPattern.FindAllStringSubmatch(card, -1) {
			v := strings.TrimSpace(html.UnescapeString(m[1]))
			switch {
			case bsSizePattern.MatchString(v):
				if it.Size == 0 {
					it.Size = bsParseSize(v)
				}
			case bsDatePattern.MatchString(v):
				if it.Date.IsZero() {
					it.Date, _ = time.Parse("1/2/2006", v)
				}
			case it.Category == "":
				it.Category = v
			}
		}
		if m := bsSeedersPattern.FindStringSubmatch(card); m != nil {
			it.Seeders = bsParseCount(m[1])
		}
		if m := bsLeechersPattern.FindStringSubmatch(card); m != nil {
			it.Leechers = bsParseCount(m[1])
		}
		if m := bsMagnetPattern.FindStringSubmatch(card); m != nil {
			it.Trackers = bsMagnetTrackers(html.UnescapeString(m[1]))
		}
		out = append(out, it)
	}
	return out
}

// bsParseSize reads "1.56 GB" (binary units, as the site computes them).
func bsParseSize(s string) int64 {
	m := bsSizePattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return 0
	}
	mult := 1.0
	switch strings.ToUpper(m[2][:1]) {
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	case "T":
		mult = 1 << 40
	}
	return int64(v * mult)
}

func bsParseCount(s string) int {
	n, _ := strconv.Atoi(strings.NewReplacer(",", "", ".", "").Replace(s))
	return n
}

// bsMagnetTrackers returns the real tracker URLs of a magnet ("tr=DHT" is a placeholder).
func bsMagnetTrackers(magnet string) []string {
	i := strings.Index(magnet, "?")
	if i < 0 {
		return nil
	}
	vals, err := url.ParseQuery(magnet[i+1:])
	if err != nil {
		return nil
	}
	var out []string
	for _, tr := range vals["tr"] {
		if strings.Contains(tr, "://") {
			out = append(out, tr)
		}
	}
	return out
}
