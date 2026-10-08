package streaming

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nextflix/internal/platform/tmdb"
)

// TMDB's English title is not always what releases are called ("Detective Brothers" is
// "Brothers Investigation" on IMDb). With a tmdb_id, resolve also matches and searches
// IMDb's title, and for movies asks the indexers that support it by IMDb id (torznab
// t=movie&imdbid=), the most precise search there is.

const (
	tsIMDbQueryPrefix = "imdb:" // a pseudo query in the search list: search by IMDb id
	tsIMDbTitleTTL    = 7 * 24 * time.Hour
	tsIMDbFailTTL     = 10 * time.Minute
	tsIMDbFetchTime   = 4 * time.Second
	tsIMDbNamesWait   = 2 * time.Second // the names lookup goes on without IMDb after this
)

// Var so tests can point it at a fake server.
var imdbSuggestURL = "https://v3.sg.media-imdb.com/suggestion/x/%s.json"

var tsIMDbIDPattern = regexp.MustCompile(`^tt\d{5,10}$`)

// tsExternalIDSource is the optional part of the TMDB client that knows IMDb ids.
type tsExternalIDSource interface {
	GetExternalIDs(mediaType string, id int) (*tmdb.ExternalIDs, error)
}

type tsIMDbTitle struct {
	title string
	year  int
	at    time.Time
	ok    bool
}

// tsIMDbQuery returns the IMDb id of an "imdb:tt…" pseudo query.
func tsIMDbQuery(query string) (string, bool) {
	id, ok := strings.CutPrefix(query, tsIMDbQueryPrefix)
	return id, ok && tsIMDbIDPattern.MatchString(id)
}

// tsParseIMDbSuggestion reads the title ("l") and year ("y") of id from IMDb's suggestion JSON.
func tsParseIMDbSuggestion(body []byte, id string) (string, int, bool) {
	var doc struct {
		D []struct {
			ID    string `json:"id"`
			Title string `json:"l"`
			Year  int    `json:"y"`
		} `json:"d"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", 0, false
	}
	for _, d := range doc.D {
		if strings.EqualFold(d.ID, id) && strings.TrimSpace(d.Title) != "" {
			return strings.TrimSpace(d.Title), d.Year, true
		}
	}
	return "", 0, false
}

// imdbID returns the IMDb id of q's TMDB id ("" when unknown).
func (s *Service) imdbID(q tsResolveQuery) string {
	src, ok := s.cfg.TMDB.(tsExternalIDSource)
	if !ok || q.tmdbID <= 0 {
		return ""
	}
	ids, err := src.GetExternalIDs(q.mediaType, q.tmdbID)
	if err != nil || ids == nil {
		if err != nil {
			log.Printf("resolve %q: tmdb external ids: %v", q.title, err)
		}
		return ""
	}
	if id := strings.TrimSpace(ids.IMDBID); tsIMDbIDPattern.MatchString(id) {
		return id
	}
	return ""
}

// imdbTitle returns IMDb's (English) title of id, cached for 7 days; failures are cached
// for 10 minutes and never block a resolve.
func (s *Service) imdbTitle(id string) (string, int) {
	if v, ok := s.imdbTitles.Load(id); ok {
		e := v.(tsIMDbTitle)
		ttl := tsIMDbTitleTTL
		if !e.ok {
			ttl = tsIMDbFailTTL
		}
		if time.Since(e.at) < ttl {
			return e.title, e.year
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), tsIMDbFetchTime)
	defer cancel()
	e := tsIMDbTitle{at: time.Now()}
	body, err := tsGetBody(ctx, fmt.Sprintf(imdbSuggestURL, url.PathEscape(id)), 1<<20)
	if err == nil {
		e.title, e.year, e.ok = tsParseIMDbSuggestion(body, id)
		if !e.ok {
			err = fmt.Errorf("no title for %s", id)
		}
	}
	if err != nil {
		log.Printf("imdb title of %s: %v", id, err)
	}
	s.imdbTitles.Store(id, e)
	return e.title, e.year
}

func tsGetBody(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", bsUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// SearchIndexerIMDb asks one indexer for a movie by IMDb id (torznab t=movie&imdbid=).
func (ts *TorrentSearcher) SearchIndexerIMDb(ctx context.Context, indexer, imdbID string) ([]JackettResult, error) {
	if ts.jackettURL == "" || ts.jackettAPIKey == "" {
		return nil, fmt.Errorf("jackett is not configured")
	}
	params := url.Values{"apikey": {ts.jackettAPIKey}, "t": {"movie"}, "imdbid": {imdbID}, "cat": {"2000"}}
	u := fmt.Sprintf("%s/api/v2.0/indexers/%s/results/torznab/api?%s", strings.TrimSuffix(ts.jackettURL, "/"), url.PathEscape(indexer), params.Encode())
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
		return nil, fmt.Errorf("jackett torznab returned status %d", resp.StatusCode)
	}
	return tsParseTorznab(resp.Body, imdbID)
}

// tsParseTorznab reads a torznab feed of an IMDb id search. Items that name another IMDb id
// are dropped; the rest are marked IMDbMatch.
func tsParseTorznab(r io.Reader, imdbID string) ([]JackettResult, error) {
	var feed struct {
		Items []struct {
			Title     string `xml:"title"`
			Link      string `xml:"link"`
			Size      int64  `xml:"size"`
			Enclosure struct {
				URL    string `xml:"url,attr"`
				Length int64  `xml:"length,attr"`
			} `xml:"enclosure"`
			Indexer struct {
				ID   string `xml:"id,attr"`
				Name string `xml:",chardata"`
			} `xml:"jackettindexer"`
			Attrs []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"attr"`
		} `xml:"channel>item"`
	}
	if err := xml.NewDecoder(r).Decode(&feed); err != nil {
		return nil, fmt.Errorf("jackett torznab: %w", err)
	}
	wantNum := strings.TrimLeft(strings.TrimPrefix(strings.ToLower(imdbID), "tt"), "0")
	var out []JackettResult
	for _, it := range feed.Items {
		attr := map[string]string{}
		for _, a := range it.Attrs {
			if _, seen := attr[a.Name]; !seen {
				attr[a.Name] = a.Value
			}
		}
		if v := attr["imdbid"]; v != "" && !strings.EqualFold(v, imdbID) {
			continue
		}
		if v := attr["imdb"]; v != "" && strings.TrimLeft(strings.TrimPrefix(strings.ToLower(v), "tt"), "0") != wantNum {
			continue
		}
		r := JackettResult{
			Title:     strings.TrimSpace(it.Title),
			Size:      it.Size,
			Tracker:   strings.TrimSpace(it.Indexer.Name),
			InfoHash:  attr["infohash"],
			MagnetUri: attr["magneturl"],
			IMDbMatch: true,
		}
		if r.Size <= 0 {
			r.Size = it.Enclosure.Length
		}
		r.Seeders, _ = strconv.Atoi(attr["seeders"])
		r.Peers, _ = strconv.Atoi(attr["peers"])
		link := strings.TrimSpace(it.Link)
		if link == "" {
			link = it.Enclosure.URL
		}
		if strings.HasPrefix(link, "magnet:?") {
			if r.MagnetUri == "" {
				r.MagnetUri = link
			}
		} else {
			r.Link = link
		}
		if r.Title != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// tsReleaseTitleTokens is the title part of a release name: the tokens before its year or
// first release tag ("Inception (2010) [1080p]" -> "inception"). Used for IMDb id matches
// whose name differs from every known title.
func tsReleaseTitleTokens(release string) []string {
	tokens := tsNormalize(tsSitePrefPattern.ReplaceAllString(release, ""))
	for i, t := range tokens {
		if i > 0 && (tsYearPattern.MatchString(t) || tsReleaseTokens[t]) {
			return tokens[:i]
		}
	}
	return nil
}
