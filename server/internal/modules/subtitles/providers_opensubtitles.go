package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/upstream"
)

const (
	osDefaultBase = "https://api.opensubtitles.com/api/v1"
	userAgent     = "Nextflix v1"
	osTokenTTL    = 23 * time.Hour
	osMinGap      = 250 * time.Millisecond // the API allows 5 requests per second
)

// openSubtitles talks to the OpenSubtitles.com REST API.
type openSubtitles struct {
	base string
	http *upstream.Client

	mu           sync.Mutex
	token        string
	tokenFor     string // api key + username the token belongs to
	tokenExpires time.Time
	apiBase      string // base_url from login (vip users), else base
	lastReq      time.Time
	blockedUntil time.Time // 429 Retry-After
	dlBlocked    time.Time // download quota used up (406)
}

func newOpenSubtitles(base string, client *upstream.Client) *openSubtitles {
	if base == "" {
		base = osDefaultBase
	}
	return &openSubtitles{base: strings.TrimRight(base, "/"), http: client}
}

// ErrRateLimited is returned while a provider asked us to back off.
var ErrRateLimited = errors.New("rate limited")

// osQuotaFallback: how long a used-up download quota blocks downloads when the answer has no reset time.
const osQuotaFallback = time.Hour

// QuotaError: the OpenSubtitles download quota is used up until Until.
type QuotaError struct{ Until time.Time }

func (e QuotaError) Error() string {
	return "OpenSubtitles download quota used up until " + e.Until.UTC().Format(time.RFC3339)
}

func (e QuotaError) Unwrap() error { return ErrRateLimited }

type osSearchResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Language          string  `json:"language"`
			DownloadCount     int     `json:"download_count"`
			HearingImpaired   bool    `json:"hearing_impaired"`
			FPS               float64 `json:"fps"`
			Votes             int     `json:"votes"`
			Ratings           float64 `json:"ratings"`
			FromTrusted       bool    `json:"from_trusted"`
			ForeignPartsOnly  bool    `json:"foreign_parts_only"`
			AITranslated      bool    `json:"ai_translated"`
			MachineTranslated bool    `json:"machine_translated"`
			MovieHashMatch    bool    `json:"moviehash_match"`
			Release           string  `json:"release"`
			Uploader          struct {
				Name string `json:"name"`
				Rank string `json:"rank"`
			} `json:"uploader"`
			Files []struct {
				FileID   int    `json:"file_id"`
				CDNumber int    `json:"cd_number"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

func (o *openSubtitles) search(ctx context.Context, q Query, st Settings, moviehash string) ([]candidate, error) {
	if st.OpenSubtitlesAPIKey == "" {
		return nil, nil
	}
	langs := append([]string(nil), q.Langs...)
	sort.Strings(langs)
	// The API wants sorted, lower-case parameters (else it redirects).
	v := url.Values{}
	v.Set("languages", strings.Join(langs, ","))
	if q.isTV() {
		v.Set("parent_tmdb_id", strconv.Itoa(q.TmdbID))
		v.Set("season_number", strconv.Itoa(q.Season))
		v.Set("episode_number", strconv.Itoa(q.Episode))
		v.Set("type", "episode")
	} else {
		v.Set("tmdb_id", strconv.Itoa(q.TmdbID))
		v.Set("type", "movie")
	}
	if moviehash != "" {
		v.Set("moviehash", moviehash)
	}
	var res osSearchResponse
	if err := o.do(ctx, st, http.MethodGet, "/subtitles?"+strings.ReplaceAll(v.Encode(), "%2C", ","), nil, &res); err != nil {
		return nil, err
	}

	var out []candidate
	for _, d := range res.Data {
		a := d.Attributes
		if len(a.Files) == 0 {
			continue
		}
		f := a.Files[0]
		c := candidate{
			Sub: Sub{
				ID:        encodeID(SourceOpenSubtitles, normLang(a.Language), strconv.Itoa(f.FileID)+"|"+f.FileName),
				Source:    SourceOpenSubtitles,
				Language:  normLang(a.Language),
				Release:   firstNonEmpty(a.Release, f.FileName),
				HI:        a.HearingImpaired,
				Forced:    a.ForeignPartsOnly,
				Synced:    a.MovieHashMatch,
				Author:    a.Uploader.Name,
				FPS:       a.FPS,
				Downloads: a.DownloadCount,
			},
			Releases:     []string{a.Release, f.FileName},
			Rating:       a.Ratings,
			Votes:        a.Votes,
			Trusted:      a.FromTrusted || a.Uploader.Rank == "trusted" || a.Uploader.Rank == "administrator",
			Machine:      a.AITranslated || a.MachineTranslated,
			MultiCD:      len(a.Files) > 1,
			MovieHashHit: a.MovieHashMatch,
		}
		out = append(out, c)
	}
	return out, nil
}

type osDownloadResponse struct {
	Link         string `json:"link"`
	FileName     string `json:"file_name"`
	Remaining    *int   `json:"remaining"`
	Message      string `json:"message"`
	ResetTimeUTC string `json:"reset_time_utc"`
}

// fetch downloads one file (ref "<file_id>|<file name>") and returns its raw bytes.
func (o *openSubtitles) fetch(ctx context.Context, ref string, st Settings) ([]byte, string, error) {
	if st.OpenSubtitlesAPIKey == "" {
		return nil, "", errors.New("OpenSubtitles API key not set")
	}
	idPart, name, _ := strings.Cut(ref, "|")
	fileID, err := strconv.Atoi(idPart)
	if err != nil || fileID <= 0 {
		return nil, "", errors.New("bad OpenSubtitles file id")
	}
	if until := o.quotaUntil(); !until.IsZero() {
		return nil, "", QuotaError{Until: until} // not asked again before the reset
	}

	body, _ := json.Marshal(map[string]any{"file_id": fileID})
	var res osDownloadResponse
	err = o.do(ctx, st, http.MethodPost, "/download", body, &res)
	var se statusError
	if errors.As(err, &se) && (se.code == http.StatusNotAcceptable || (se.code == http.StatusTooManyRequests && osQuotaBody(se.body))) {
		// 406 (or a 429 about the quota): the daily download quota is reached until reset_time_utc
		var q osDownloadResponse
		_ = json.Unmarshal(se.body, &q)
		return nil, "", QuotaError{Until: o.blockDownloads(q.ResetTimeUTC)}
	}
	if err != nil {
		return nil, "", err
	}
	if res.Link == "" {
		return nil, "", fmt.Errorf("OpenSubtitles: no download link (%s)", res.Message)
	}
	if res.Remaining != nil && *res.Remaining <= 0 {
		// That was the quota's last download: the next ones are not even asked
		o.blockDownloads(res.ResetTimeUTC)
	}
	raw, err := getBytes(ctx, o.http, res.Link, nil)
	if err != nil {
		return nil, "", err
	}
	return raw, firstNonEmpty(res.FileName, name), nil
}

// quotaUntil is when a used-up download quota resets (zero while downloads are allowed).
func (o *openSubtitles) quotaUntil() time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	if time.Now().Before(o.dlBlocked) {
		return o.dlBlocked
	}
	return time.Time{}
}

// blockDownloads stops downloads until reset (RFC 3339; else osQuotaFallback from now).
func (o *openSubtitles) blockDownloads(reset string) time.Time {
	until := time.Now().Add(osQuotaFallback)
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(reset)); err == nil && t.After(time.Now()) {
		until = t
	}
	o.mu.Lock()
	o.dlBlocked = until
	o.mu.Unlock()
	log.Printf("subtitles: OpenSubtitles download quota used up until %s", until.UTC().Format(time.RFC3339))
	return until
}

// osQuotaBody: an error answer of /download that is about the download quota, not request pacing.
func osQuotaBody(b []byte) bool {
	l := strings.ToLower(string(b))
	for _, w := range []string{"quota", "remaining", "reset_time", "download limit", "allowed"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// login returns a cached JWT, logging in when username and password are set. Without them it
// returns "" (anonymous API key use, lower download quota).
func (o *openSubtitles) login(ctx context.Context, st Settings) (string, string) {
	if st.OpenSubtitlesUsername == "" || st.OpenSubtitlesPassword == "" {
		return "", o.base
	}
	owner := st.OpenSubtitlesAPIKey + "|" + st.OpenSubtitlesUsername + "|" + hashKey(st.OpenSubtitlesPassword)
	o.mu.Lock()
	if o.token != "" && o.tokenFor == owner && time.Now().Before(o.tokenExpires) {
		t, b := o.token, o.apiBase
		o.mu.Unlock()
		return t, b
	}
	o.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"username": st.OpenSubtitlesUsername, "password": st.OpenSubtitlesPassword})
	var res struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	if err := o.raw(ctx, st, o.base, "", http.MethodPost, "/login", body, &res); err != nil || res.Token == "" {
		return "", o.base
	}
	apiBase := o.base
	// VIP accounts get their own host; only follow it when it is an opensubtitles.com host.
	if h := strings.TrimSpace(res.BaseURL); h != "" && o.base == osDefaultBase {
		host := strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
		host = strings.Split(host, "/")[0]
		if strings.HasSuffix(host, ".opensubtitles.com") {
			apiBase = "https://" + host + "/api/v1"
		}
	}
	o.mu.Lock()
	o.token, o.tokenFor, o.tokenExpires, o.apiBase = res.Token, owner, time.Now().Add(osTokenTTL), apiBase
	o.mu.Unlock()
	return res.Token, apiBase
}

func (o *openSubtitles) dropToken() {
	o.mu.Lock()
	o.token = ""
	o.mu.Unlock()
}

// do sends an API request with the token; a 401 drops the token and retries once.
func (o *openSubtitles) do(ctx context.Context, st Settings, method, path string, body []byte, dst any) error {
	token, base := o.login(ctx, st)
	err := o.raw(ctx, st, base, token, method, path, body, dst)
	var se statusError
	if token != "" && errors.As(err, &se) && se.code == http.StatusUnauthorized {
		o.dropToken()
		token, base = o.login(ctx, st)
		err = o.raw(ctx, st, base, token, method, path, body, dst)
	}
	return err
}

type statusError struct {
	code int
	body []byte
}

func (e statusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.code, strings.TrimSpace(string(truncate(e.body, 200))))
}

func (o *openSubtitles) raw(ctx context.Context, st Settings, base, token, method, path string, body []byte, dst any) error {
	if err := o.wait(ctx); err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Api-Key", st.OpenSubtitlesAPIKey)
	h.Set("User-Agent", userAgent)
	h.Set("Accept", "application/json")
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	var resp *upstream.Response
	var err error
	if method == http.MethodPost {
		h.Set("Content-Type", "application/json")
		resp, err = o.http.Post(ctx, base+path, h, body)
	} else {
		resp, err = o.http.Get(ctx, base+path, h)
	}
	if err != nil {
		return fmt.Errorf("OpenSubtitles: %w", err)
	}
	b := resp.Body
	if resp.Status == http.StatusTooManyRequests {
		wait := retryAfter(resp.Header.Get("Retry-After"), 10*time.Second)
		o.mu.Lock()
		o.blockedUntil = time.Now().Add(wait)
		o.mu.Unlock()
		// The body tells a used-up download quota from request pacing (fetch)
		return fmt.Errorf("OpenSubtitles: %w for %s: %w", ErrRateLimited, wait, statusError{code: resp.Status, body: b})
	}
	if resp.Status < 200 || resp.Status > 299 {
		return statusError{code: resp.Status, body: b}
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(b, dst); err != nil {
		var syn *json.SyntaxError
		if len(b) == 0 || (errors.As(err, &syn) && syn.Offset >= int64(len(b))) {
			err = fmt.Errorf("%w: cut-off answer: %w", errTemporary, err)
		}
		return fmt.Errorf("OpenSubtitles: bad answer (status %d, %d bytes): %w", resp.Status, len(b), err)
	}
	return nil
}

// wait spaces requests and honours a pending Retry-After.
func (o *openSubtitles) wait(ctx context.Context) error {
	o.mu.Lock()
	now := time.Now()
	if now.Before(o.blockedUntil) {
		until := o.blockedUntil
		o.mu.Unlock()
		return fmt.Errorf("OpenSubtitles: %w until %s", ErrRateLimited, until.Format(time.TimeOnly))
	}
	next := o.lastReq.Add(osMinGap)
	if next.Before(now) {
		next = now
	}
	o.lastReq = next
	o.mu.Unlock()
	return sleepCtx(ctx, time.Until(next))
}

func retryAfter(v string, def time.Duration) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return def
}
