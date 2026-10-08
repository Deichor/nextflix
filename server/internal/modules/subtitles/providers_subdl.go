package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	subdlDefaultAPI = "https://api.subdl.com/api/v1"
	subdlDefaultDL  = "https://dl.subdl.com"
)

// subDL talks to the SubDL API (search) and its download host (zip packs or raw unpacked files).
type subDL struct {
	api, dl string
	http    *upstream.Client

	mu           sync.Mutex
	blockedUntil time.Time
}

func newSubDL(api, dl string, client *upstream.Client) *subDL {
	if api == "" {
		api = subdlDefaultAPI
	}
	if dl == "" {
		dl = subdlDefaultDL
	}
	return &subDL{api: strings.TrimRight(api, "/"), dl: strings.TrimRight(dl, "/"), http: client}
}

type subdlSub struct {
	ReleaseName  string          `json:"release_name"`
	Name         string          `json:"name"`
	Lang         string          `json:"lang"`
	Language     string          `json:"language"`
	Author       string          `json:"author"`
	URL          string          `json:"url"`
	Season       *int            `json:"season"`
	Episode      *int            `json:"episode"`
	EpisodeFrom  *int            `json:"episode_from"`
	EpisodeEnd   *int            `json:"episode_end"`
	FullSeason   bool            `json:"full_season"`
	HI           bool            `json:"hi"`
	FPS          any             `json:"fps"`
	Releases     []string        `json:"releases"`
	UnpackFiles  []subdlFile     `json:"-"`
	RawUnpack    json.RawMessage `json:"unpack_files"`
	DownloadsAny any             `json:"downloads"`
}

type subdlFile struct {
	Name        string `json:"name"`
	ReleaseName string `json:"release_name"`
	Season      *int   `json:"season"`
	Episode     *int   `json:"episode"`
	Language    string `json:"language"`
	HI          bool   `json:"hi"`
	Format      string `json:"format"`
	URL         string `json:"url"`
}

type subdlResponse struct {
	Status    bool       `json:"status"`
	Error     string     `json:"error"`
	Message   string     `json:"message"`
	Subtitles []subdlSub `json:"subtitles"`
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func (d *subDL) search(ctx context.Context, q Query, st Settings) ([]candidate, error) {
	if st.SubDLAPIKey == "" {
		return nil, nil
	}
	langs := make([]string, len(q.Langs))
	for i, l := range q.Langs {
		langs[i] = strings.ToUpper(l)
	}
	base := url.Values{}
	base.Set("api_key", st.SubDLAPIKey)
	base.Set("tmdb_id", strconv.Itoa(q.TmdbID))
	base.Set("languages", strings.Join(langs, ","))
	base.Set("subs_per_page", "30")
	base.Set("hi", "1")
	base.Set("releases", "1")
	base.Set("unpack", "1")
	base.Set("client", "custom_integration")
	if q.isTV() {
		base.Set("type", "tv")
		base.Set("season_number", strconv.Itoa(q.Season))
		base.Set("episode_number", strconv.Itoa(q.Episode))
	} else {
		base.Set("type", "movie")
	}

	calls := []url.Values{base}
	if q.isTV() {
		// Season packs come from a second query.
		packs := url.Values{}
		for k, v := range base {
			packs[k] = v
		}
		packs.Del("episode_number")
		packs.Set("full_season", "1")
		calls = append(calls, packs)
	}

	var all []candidate
	var firstErr error
	for _, v := range calls {
		subs, err := d.query(ctx, v)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, d.toCandidates(subs, q)...)
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

func (d *subDL) query(ctx context.Context, v url.Values) ([]subdlSub, error) {
	d.mu.Lock()
	if time.Now().Before(d.blockedUntil) {
		d.mu.Unlock()
		return nil, fmt.Errorf("SubDL: %w", ErrRateLimited)
	}
	d.mu.Unlock()

	resp, err := d.http.Get(ctx, d.api+"/subtitles?"+v.Encode(), http.Header{"Accept": {"application/json"}})
	if err != nil {
		return nil, fmt.Errorf("SubDL: %w", redact(err, v.Get("api_key"))) // the URL carries the API key
	}
	if resp.Status == http.StatusTooManyRequests {
		wait := retryAfter(resp.Header.Get("Retry-After"), 30*time.Second)
		d.mu.Lock()
		d.blockedUntil = time.Now().Add(wait)
		d.mu.Unlock()
		return nil, fmt.Errorf("SubDL: %w for %s", ErrRateLimited, wait)
	}
	var res subdlResponse
	if err := json.Unmarshal(resp.Body, &res); err != nil {
		if resp.Status >= 500 || len(resp.Body) == 0 {
			err = fmt.Errorf("%w: %w", errTemporary, err)
		}
		return nil, fmt.Errorf("SubDL: status %d: %w", resp.Status, err)
	}
	if !res.Status {
		msg := firstNonEmpty(res.Error, res.Message)
		// "can't find subtitle" is an empty result, not a failure.
		if resp.Status == http.StatusOK || strings.Contains(strings.ToLower(msg), "not found") ||
			strings.Contains(strings.ToLower(msg), "can't find") {
			return nil, nil
		}
		if resp.Status >= 500 {
			return nil, fmt.Errorf("SubDL: %s (status %d): %w", msg, resp.Status, errTemporary)
		}
		return nil, fmt.Errorf("SubDL: %s", msg)
	}
	for i := range res.Subtitles {
		s := &res.Subtitles[i]
		if len(s.RawUnpack) > 0 && s.RawUnpack[0] == '[' {
			_ = json.Unmarshal(s.RawUnpack, &s.UnpackFiles)
		}
	}
	return res.Subtitles, nil
}

var subdlURL = regexp.MustCompile(`^/subtitle/[A-Za-z0-9_-]+(?:\.zip|/[A-Za-z0-9_-]+)$`)

// subdlPath drops the query SubDL appends to file URLs (it carries the API key; downloads work without it).
func subdlPath(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func (d *subDL) toCandidates(subs []subdlSub, q Query) []candidate {
	var out []candidate
	for _, s := range subs {
		s.URL = subdlPath(s.URL)
		if !subdlURL.MatchString(s.URL) {
			continue
		}
		lang := normLang(firstNonEmpty(s.Language, s.Lang))
		release := firstNonEmpty(s.ReleaseName, s.Name)
		ref := s.URL
		hi := s.HI
		if q.isTV() {
			season := intOr(s.Season, q.Season)
			if season != 0 && season != q.Season {
				continue
			}
			from, end := intOr(s.EpisodeFrom, 0), intOr(s.EpisodeEnd, 0)
			ep := intOr(s.Episode, 0)
			pack := s.FullSeason || (from > 0 && end > from)
			switch {
			case pack && from > 0 && end >= from && (q.Episode < from || q.Episode > end):
				continue
			case !pack && ep != 0 && ep != q.Episode:
				continue
			}
			if pack {
				// Prefer the single unpacked file of this episode; else pick it from the zip later.
				ref = fmt.Sprintf("%s|%d|%d", s.URL, q.Season, q.Episode)
				for _, f := range s.UnpackFiles {
					f.URL = subdlPath(f.URL)
					if intOr(f.Season, q.Season) == q.Season && intOr(f.Episode, -1) == q.Episode && subdlURL.MatchString(f.URL) {
						ref = f.URL + "|" + f.Name
						release = firstNonEmpty(f.ReleaseName, f.Name, release)
						hi = hi || f.HI
						break
					}
				}
			}
		}
		c := candidate{
			Sub: Sub{
				ID:       encodeID(SourceSubDL, lang, ref),
				Source:   SourceSubDL,
				Language: lang,
				Release:  release,
				HI:       hi,
				Author:   s.Author,
				FPS:      anyFloat(s.FPS),
			},
			Releases: append([]string{release}, s.Releases...),
		}
		c.Downloads = int(anyFloat(s.DownloadsAny))
		out = append(out, c)
	}
	return out
}

// fetch downloads ref: "/subtitle/x.zip[|season|episode]" (a pack) or "/subtitle/n/file|name" (raw).
func (d *subDL) fetch(ctx context.Context, ref, lang string) ([]byte, string, error) {
	parts := strings.Split(ref, "|")
	path := parts[0]
	if !subdlURL.MatchString(path) {
		return nil, "", errors.New("bad SubDL file id")
	}
	raw, err := getBytes(ctx, d.http, d.dl+path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("SubDL download: %w", err)
	}
	if !strings.HasSuffix(path, ".zip") && !isZip(raw) {
		name := path
		if len(parts) > 1 {
			name = parts[1]
		}
		return raw, name, nil
	}
	season, episode := 0, 0
	if len(parts) == 3 {
		season, _ = strconv.Atoi(parts[1])
		episode, _ = strconv.Atoi(parts[2])
	}
	return pickFromArchive(raw, season, episode, lang)
}

func anyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}
