package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/upstream"
)

const (
	taDefaultBase = "https://turkcealtyazi.org"
	taUserAgent   = "Mozilla/5.0 (compatible; Nextflix/1.0; self-hosted media player; on-demand subtitle lookup)"
	taMinGap      = 2 * time.Second
	taPageTTL     = 24 * time.Hour
	taCookieTTL   = 20 * time.Minute
)

// turkceAltyazi scrapes turkcealtyazi.org politely: one request every 2 s at most, pages cached
// on disk for 24 h, files forever (by the service), only when a title is opened.
type turkceAltyazi struct {
	base         string
	flareSolverr string
	http         *upstream.Client
	pages        diskCache

	gap     time.Duration
	gate    sync.Mutex // held while waiting for the next request slot
	lastReq time.Time

	mu        sync.Mutex
	cfCookies string
	cfUA      string
	cfExpires time.Time
}

func newTurkceAltyazi(base, flareSolverr string, pages diskCache) *turkceAltyazi {
	if base == "" {
		base = taDefaultBase
	}
	t := &turkceAltyazi{base: strings.TrimRight(base, "/"), flareSolverr: flareSolverr, pages: pages, gap: taMinGap}
	host := hostOf(t.base)
	// Redirects (/tt<id>/ -> /mov/<id>/<name>.html) must stay on the site; no hedging, one
	// request at a time is the polite rate
	t.http = newProviderClient(false, false, func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Hostname() != host {
			return errors.New("unexpected redirect")
		}
		return nil
	})
	return t
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

// taRow is one subtitle in a title's list.
type taRow struct {
	ID         string
	Href       string
	Lang       string
	Quality    int
	Season     int
	Episode    int
	Pack       bool
	CDs        int
	Translator string
	FPS        float64
	Downloads  int
	Uploader   string
	Source     string
	Groups     string
}

var (
	taBlockSplit = regexp.MustCompile(`<div class="altsonsez\d`)
	taLink       = regexp.MustCompile(`<a[^>]*\bid="(\d+)"[^>]*\bhref="(/sub/\d+/[A-Za-z0-9_.-]+\.html)"`)
	taFlag       = regexp.MustCompile(`class="aldil"><span class="flag([a-z]{2})"`)
	taKal        = regexp.MustCompile(`<span class="kal(\d)"`)
	taCD         = regexp.MustCompile(`(?s)<div class="alcd">(.*?)</div>`)
	taSeason     = regexp.MustCompile(`S<b>\s*(\d+)\s*</b>`)
	taEpisode    = regexp.MustCompile(`E<b>\s*(\d+)\s*</b>`)
	taTranslator = regexp.MustCompile(`(?s)<div class="alcevirmen">(.*?)</div>`)
	taFPS        = regexp.MustCompile(`<div class="alfps">([^<]*)</div>`)
	taDownloads  = regexp.MustCompile(`<div class="alindirme">([^<]*)</div>`)
	taUploader   = regexp.MustCompile(`(?s)<div class="algonderen">(.*?)</div>`)
	taRip        = regexp.MustCompile(`(?s)<div class="ripdiv">(.*?)</div>`)
	taRps        = regexp.MustCompile(`class="rps r(\d+)"`)
	taTags       = regexp.MustCompile(`<[^>]*>`)
	taSpaces     = regexp.MustCompile(`\s+`)
	taForm       = regexp.MustCompile(`(?s)<form[^>]*action="(/ind[^"]*)"[^>]*>(.*?)</form>`)
	taInput      = regexp.MustCompile(`<input[^>]*name="([a-z_]+)"[^>]*value="([^"]*)"`)
	taDigits     = regexp.MustCompile(`^\d{1,10}$`)
	taHref       = regexp.MustCompile(`^/sub/\d+/[A-Za-z0-9_.-]+\.html$`)
	taToken      = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// taSources maps the release-type sprite (rps.png) to its label.
var taSources = map[string]string{"1": "HD", "2": "DVDRip", "3": "DVDScr", "4": "R5", "5": "CAM", "6": "WEBRip",
	"7": "BDRip", "8": "WEB-DL", "9": "HDRip", "10": "HDTS", "11": "BRRip", "12": "BluRay"}

func taText(s string) string {
	s = taTags.ReplaceAllString(s, " ")
	return strings.TrimSpace(taSpaces.ReplaceAllString(html.UnescapeString(s), " "))
}

// parseTAList reads the subtitle rows of a title page.
func parseTAList(page string) []taRow {
	locs := taBlockSplit.FindAllStringIndex(page, -1)
	var rows []taRow
	for i, loc := range locs {
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		b := page[loc[0]:end]
		m := taLink.FindStringSubmatch(b)
		if m == nil {
			continue
		}
		r := taRow{ID: m[1], Href: m[2], CDs: 1}
		if f := taFlag.FindStringSubmatch(b); f != nil {
			r.Lang = f[1]
		}
		if k := taKal.FindStringSubmatch(b); k != nil {
			r.Quality, _ = strconv.Atoi(k[1])
		}
		if cd := taCD.FindStringSubmatch(b); cd != nil {
			inner := cd[1]
			if s := taSeason.FindStringSubmatch(inner); s != nil {
				r.Season, _ = strconv.Atoi(s[1])
				if e := taEpisode.FindStringSubmatch(inner); e != nil {
					r.Episode, _ = strconv.Atoi(e[1])
				}
				r.Pack = strings.Contains(inner, "Paket")
			} else if n, err := strconv.Atoi(taText(inner)); err == nil && n > 0 {
				r.CDs = n
			}
		}
		if t := taTranslator.FindStringSubmatch(b); t != nil {
			r.Translator = taText(t[1])
		}
		if f := taFPS.FindStringSubmatch(b); f != nil {
			r.FPS, _ = strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(f[1]), ",", "."), 64)
		}
		if d := taDownloads.FindStringSubmatch(b); d != nil {
			r.Downloads, _ = strconv.Atoi(strings.NewReplacer(",", "", ".", "", " ", "").Replace(d[1]))
		}
		if u := taUploader.FindStringSubmatch(b); u != nil {
			r.Uploader = taText(u[1])
		}
		if rip := taRip.FindStringSubmatch(b); rip != nil {
			if s := taRps.FindStringSubmatch(rip[1]); s != nil {
				r.Source = taSources[s[1]]
			}
			r.Groups = taText(rip[1])
		}
		rows = append(rows, r)
	}
	return rows
}

func (t *turkceAltyazi) search(ctx context.Context, q Query, imdbID string) ([]candidate, error) {
	num := strings.TrimPrefix(strings.ToLower(imdbID), "tt")
	if !taDigits.MatchString(num) {
		return nil, errors.New("turkcealtyazi: no IMDb id")
	}
	page, err := t.page(ctx, "/tt"+num+"/")
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, l := range q.Langs {
		want[l] = true
	}

	var out []candidate
	for _, r := range parseTAList(page) {
		lang := normLang(r.Lang)
		if !want[lang] {
			continue
		}
		if q.isTV() {
			if r.Season != q.Season || (!r.Pack && r.Episode != q.Episode) {
				continue
			}
		}
		release, releases := taRelease(r)
		pack := 0
		if r.Pack {
			pack = 1
		}
		ref := fmt.Sprintf("%s|%s|%d|%d|%d", r.ID, r.Href, q.Season, q.Episode, pack)
		out = append(out, candidate{
			Sub: Sub{
				ID:        encodeID(SourceTurkceAltyazi, lang, ref),
				Source:    SourceTurkceAltyazi,
				Language:  lang,
				Release:   release,
				Author:    firstNonEmpty(r.Translator, r.Uploader),
				FPS:       r.FPS,
				Downloads: r.Downloads,
			},
			Releases: releases,
			Pack:     r.Pack,
			Quality:  r.Quality,
			MultiCD:  r.CDs > 1,
		})
	}
	return out, nil
}

// taRelease builds the shown release and the names the ranking compares ("BDRip-HDRINVASION").
func taRelease(r taRow) (string, []string) {
	groups := strings.Split(r.Groups, "/")
	shownGroups := r.Groups
	if len(groups) > 3 {
		// Some rows list dozens of compatible releases.
		shownGroups = fmt.Sprintf("%s / %s / %s +%d", strings.TrimSpace(groups[0]), strings.TrimSpace(groups[1]),
			strings.TrimSpace(groups[2]), len(groups)-3)
	}
	shown := strings.TrimSpace(r.Source + " " + shownGroups)
	if r.Pack {
		shown = strings.TrimSpace(fmt.Sprintf("S%02d (pack) %s", r.Season, shown))
	}
	var rel []string
	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if strings.ContainsAny(g, " .") {
			rel = append(rel, strings.TrimSpace(r.Source+" "+g))
		} else {
			rel = append(rel, strings.TrimSpace(r.Source+"-"+g))
		}
	}
	if len(rel) == 0 && r.Source != "" {
		rel = []string{r.Source}
	}
	return shown, rel
}

// fetch downloads ref "<id>|<href>|<season>|<episode>|<pack>" through the page's download form.
func (t *turkceAltyazi) fetch(ctx context.Context, ref, lang string) ([]byte, string, error) {
	p := strings.Split(ref, "|")
	if len(p) != 5 || !taDigits.MatchString(p[0]) || !taHref.MatchString(p[1]) {
		return nil, "", errors.New("bad turkcealtyazi id")
	}
	season, _ := strconv.Atoi(p[2])
	episode, _ := strconv.Atoi(p[3])
	pack := p[4] == "1"

	page, err := t.page(ctx, p[1])
	if err != nil {
		return nil, "", err
	}
	fm := taForm.FindStringSubmatch(page)
	if fm == nil {
		return nil, "", errors.New("turkcealtyazi: download form not found")
	}
	form := url.Values{}
	for _, in := range taInput.FindAllStringSubmatch(fm[2], -1) {
		form.Set(in[1], html.UnescapeString(in[2]))
	}
	if form.Get("altid") != p[0] || !taDigits.MatchString(form.Get("idid")) || !taToken.MatchString(form.Get("sidid")) {
		return nil, "", errors.New("turkcealtyazi: unexpected download form")
	}

	raw, err := t.post(ctx, fm[1], form, t.base+p[1])
	if err != nil {
		return nil, "", err
	}
	if !pack {
		season, episode = 0, 0
	}
	switch {
	case isZip(raw), isRar(raw):
		return pickFromArchive(raw, season, episode, lang)
	case bytes.Contains(raw[:min(len(raw), 512)], []byte("-->")):
		return raw, p[1] + ".srt", nil
	}
	return nil, "", errors.New("turkcealtyazi: download is not a subtitle archive")
}

// taFreshPageTTL: a refresh (late uploads) reads pages older than this again.
const taFreshPageTTL = 10 * time.Minute

type freshPagesKey struct{}

// withFreshPages marks a search of a refresh: cached pages count only for taFreshPageTTL.
func withFreshPages(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshPagesKey{}, true)
}

func freshPages(ctx context.Context) bool {
	v, _ := ctx.Value(freshPagesKey{}).(bool)
	return v
}

// page returns a site page from the 24 h disk cache (taFreshPageTTL for a refresh) or fetches it.
func (t *turkceAltyazi) page(ctx context.Context, path string) (string, error) {
	ttl := taPageTTL
	if freshPages(ctx) {
		ttl = taFreshPageTTL
	}
	if b, ok := t.pages.read("turkcealtyazi-pages", path, ".html", ttl); ok {
		return string(b), nil
	}
	body, err := t.get(ctx, path)
	if err != nil {
		return "", err
	}
	_ = t.pages.write("turkcealtyazi-pages", path, ".html", body)
	return string(body), nil
}

// wait blocks until the next request slot (one every 2 s across the whole server).
func (t *turkceAltyazi) wait(ctx context.Context) error {
	t.gate.Lock()
	defer t.gate.Unlock()
	if err := sleepCtx(ctx, time.Until(t.lastReq.Add(t.gap))); err != nil {
		return err
	}
	t.lastReq = time.Now()
	return nil
}

func (t *turkceAltyazi) get(ctx context.Context, path string) ([]byte, error) {
	if err := t.wait(ctx); err != nil {
		return nil, err
	}
	resp, err := t.http.Get(ctx, t.base+path, t.headers())
	if err != nil {
		return nil, fmt.Errorf("turkcealtyazi: %w", err)
	}
	if isChallenge(resp.Status, resp.Header, resp.Body) {
		return t.solve(ctx, t.base+path)
	}
	if resp.Status == http.StatusNotFound {
		return nil, errors.New("turkcealtyazi: title not found")
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("turkcealtyazi: %w", statusError{code: resp.Status})
	}
	return resp.Body, nil
}

// post sends the download form; it may be repeated after a reset (it only hands out a file).
func (t *turkceAltyazi) post(ctx context.Context, path string, form url.Values, referer string) ([]byte, error) {
	if err := t.wait(ctx); err != nil {
		return nil, err
	}
	h := t.headers()
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Referer", referer)
	resp, err := t.http.Post(ctx, t.base+path, h, []byte(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("turkcealtyazi: download: %w", err)
	}
	if isChallenge(resp.Status, resp.Header, resp.Body) {
		return nil, errors.New("turkcealtyazi: download blocked by Cloudflare")
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("turkcealtyazi: download: %w", statusError{code: resp.Status})
	}
	return resp.Body, nil
}

func (t *turkceAltyazi) headers() http.Header {
	h := http.Header{}
	ua := taUserAgent
	t.mu.Lock()
	if time.Now().Before(t.cfExpires) {
		ua = t.cfUA
		h.Set("Cookie", t.cfCookies)
	}
	t.mu.Unlock()
	h.Set("User-Agent", ua)
	h.Set("Accept-Language", "tr-TR,tr;q=0.9,en;q=0.5")
	return h
}

func isChallenge(status int, header http.Header, body []byte) bool {
	if header.Get("cf-mitigated") == "challenge" {
		return true
	}
	if status != http.StatusForbidden && status != http.StatusServiceUnavailable {
		return false
	}
	head := body[:min(len(body), 4096)]
	return bytes.Contains(head, []byte("challenge-platform")) || bytes.Contains(head, []byte("Just a moment"))
}

// taNetErrors are FlareSolverr messages of a page load that failed on the network (the
// browser inside it sees the same DPI resets); those are tried once more.
var taNetErrors = []string{"ERR_CONNECTION", "ERR_EMPTY_RESPONSE", "ERR_SSL", "ERR_TIMED_OUT", "ERR_NETWORK"}

// solve loads a page through FlareSolverr and keeps its cookies for the next direct requests.
func (t *turkceAltyazi) solve(ctx context.Context, pageURL string) ([]byte, error) {
	if t.flareSolverr == "" {
		return nil, errors.New("turkcealtyazi: Cloudflare challenge and no FlareSolverr")
	}
	var err error
	for try := 0; try < 2; try++ {
		var page []byte
		if page, err = t.solveOnce(ctx, pageURL); err == nil || !errors.Is(err, errTemporary) || ctx.Err() != nil {
			return page, err
		}
	}
	return nil, err
}

func (t *turkceAltyazi) solveOnce(ctx context.Context, pageURL string) ([]byte, error) {
	payload, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": pageURL, "maxTimeout": 60000})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.flareSolverr, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 75 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("FlareSolverr: %w", err)
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
		return nil, fmt.Errorf("FlareSolverr: %w", err)
	}
	if res.Status != "ok" || res.Solution.Status != http.StatusOK {
		err := fmt.Errorf("FlareSolverr: %s (status %d)", res.Message, res.Solution.Status)
		for _, n := range taNetErrors {
			if strings.Contains(res.Message, n) {
				return nil, fmt.Errorf("%w: %w", err, errTemporary)
			}
		}
		return nil, err
	}
	var cookies []string
	for _, c := range res.Solution.Cookies {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	t.mu.Lock()
	t.cfCookies, t.cfUA, t.cfExpires = strings.Join(cookies, "; "), res.Solution.UserAgent, time.Now().Add(taCookieTTL)
	t.mu.Unlock()
	return []byte(res.Solution.Response), nil
}
