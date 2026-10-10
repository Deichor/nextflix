package subtitles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/upstream"
)

// Shared pieces of the keyless scraped providers (subf2m, YIFY Subtitles).

const (
	scrapePageTTL  = 24 * time.Hour
	scrapeFreshTTL = 10 * time.Minute // a refresh (late uploads) reads older pages again
)

// errNotFound is a page the site does not have (404): an empty result, not a failure.
var errNotFound = errors.New("page not found")

// politeGate lets one request to a site start every gap, across the whole server.
type politeGate struct {
	gap  time.Duration
	mu   sync.Mutex
	last time.Time
}

func (g *politeGate) wait(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := sleepCtx(ctx, time.Until(g.last.Add(g.gap))); err != nil {
		return err
	}
	g.last = time.Now()
	return nil
}

// scraper fetches the pages of one site politely and keeps them on disk.
type scraper struct {
	name  string // used in errors
	base  string
	kind  string // diskCache folder of the pages
	http  *upstream.Client
	pages diskCache
	gate  politeGate
}

// newScraper builds a client without hedging (one request at a time is the polite rate) whose
// redirects stay on the site or the extra hosts (a download CDN).
func newScraper(name, base, kind string, pages diskCache, gap time.Duration, extraHosts ...string) *scraper {
	s := &scraper{name: name, base: strings.TrimRight(base, "/"), kind: kind, pages: pages, gate: politeGate{gap: gap}}
	host := hostOf(s.base)
	s.http = newProviderClient(false, true, func(req *http.Request, via []*http.Request) error {
		h := req.URL.Hostname()
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if h == host {
			return nil
		}
		for _, x := range extraHosts {
			if h == x || strings.HasSuffix(h, "."+x) {
				return nil
			}
		}
		return fmt.Errorf("unexpected redirect to %s", h)
	})
	return s
}

// page returns a site page from the disk cache (24 h, 10 min for a refresh) or fetches it.
func (s *scraper) page(ctx context.Context, path string) (string, error) {
	ttl := scrapePageTTL
	if freshPages(ctx) {
		ttl = scrapeFreshTTL
	}
	if b, ok := s.pages.read(s.kind, path, ".html", ttl); ok {
		return string(b), nil
	}
	body, err := s.get(ctx, path, nil)
	if err != nil {
		return "", err
	}
	_ = s.pages.write(s.kind, path, ".html", body)
	return string(body), nil
}

func (s *scraper) get(ctx context.Context, path string, header http.Header) ([]byte, error) {
	if err := s.gate.wait(ctx); err != nil {
		return nil, err
	}
	if header == nil {
		header = http.Header{}
	}
	header.Set("Accept-Language", "en-US,en;q=0.8")
	resp, err := s.http.Get(ctx, s.base+path, header)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	switch {
	case resp.Status == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", s.name, errNotFound)
	case isChallenge(resp.Status, resp.Header, resp.Body):
		return nil, fmt.Errorf("%s: blocked by a Cloudflare challenge", s.name)
	case resp.Status != http.StatusOK:
		return nil, fmt.Errorf("%s: %w", s.name, statusError{code: resp.Status, body: truncate(resp.Body, 256)})
	}
	return resp.Body, nil
}

// subtitleDownload reads a downloaded file: an archive (the subtitle that fits is picked) or a
// plain subtitle. Episode > 0 picks that episode of a pack; a single subtitle whose archive
// does not name it falls back to the best file when pack is false.
func subtitleDownload(name string, raw []byte, season, episode int, pack bool, lang string) ([]byte, string, error) {
	switch {
	case isZip(raw), isRar(raw):
		b, file, err := pickFromArchive(raw, season, episode, lang)
		if err != nil && episode > 0 && !pack && !errors.Is(err, ErrRarUnsupported) {
			return pickFromArchive(raw, 0, 0, lang)
		}
		return b, file, err
	case strings.Contains(string(raw[:min(len(raw), 512)]), "-->"):
		return raw, name + ".srt", nil
	}
	return nil, "", errors.New("download is not a subtitle archive")
}

// langNames maps the language names the scraped sites show to ISO 639-1.
var langNames = map[string]string{
	"turkish": "tr", "english": "en", "german": "de", "french": "fr", "spanish": "es", "italian": "it",
	"portuguese": "pt", "brazilian portuguese": "pt", "brazillian portuguese": "pt", "dutch": "nl",
	"russian": "ru", "arabic": "ar", "farsi/persian": "fa", "farsi persian": "fa", "persian": "fa", "greek": "el", "polish": "pl",
	"swedish": "sv", "danish": "da", "norwegian": "no", "finnish": "fi", "japanese": "ja", "korean": "ko",
	"chinese": "zh", "chinese bg code": "zh", "big 5 code": "zh", "hebrew": "he", "hungarian": "hu",
	"czech": "cs", "romanian": "ro", "bulgarian": "bg", "croatian": "hr", "serbian": "sr", "ukrainian": "uk",
	"ukranian": "uk", "azerbaijani": "az", "indonesian": "id", "vietnamese": "vi", "thai": "th", "malay": "ms",
}

// langFromName maps a shown language name ("Turkish", "Farsi/Persian") to its code ("" = unknown).
func langFromName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.NewReplacer("_", " ", "-", " ").Replace(n)
	if c, ok := langNames[n]; ok {
		return c
	}
	if c := normLang(n); len(c) == 2 {
		return c
	}
	return ""
}
