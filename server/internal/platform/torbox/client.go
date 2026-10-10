// Package torbox is a small client for the TorBox debrid API (https://api.torbox.app).
// Only what streaming needs: the cache check, adding a cached torrent, listing it and
// requesting a download link. The API key is never part of an error message.
package torbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://api.torbox.app/v1/api"
	// Cloudflare in front of the API rejects some default user agents
	UserAgent = "Nextflix/1.0"
	// checkcached takes about 100 hashes per call
	MaxCheckBatch = 100
)

// Plan names by TorBox's plan number.
var planNames = map[int]string{0: "Free", 1: "Essential", 2: "Pro", 3: "Standard"}

func PlanName(plan int) string {
	if n, ok := planNames[plan]; ok {
		return n
	}
	return "Plan " + strconv.Itoa(plan)
}

// ErrMalformed: the answer was not what the API documents. Callers treat it as "unknown".
var ErrMalformed = errors.New("torbox: malformed response")

// APIError is an answer with an HTTP error status or success=false.
type APIError struct {
	Status int
	Code   string // TorBox's error code, e.g. BAD_TOKEN, AUTH_ERROR, PLAN_RESTRICTED_FEATURE
	Detail string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("torbox: status %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unauthorized: a bad or missing key, or a plan that does not allow the call (expired).
func (e *APIError) Unauthorized() bool {
	switch e.Code {
	case "AUTH_ERROR", "BAD_TOKEN", "NO_AUTH", "PLAN_RESTRICTED_FEATURE", "ACTIVE_LIMIT":
		return true
	}
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

func (e *APIError) RateLimited() bool { return e.Status == http.StatusTooManyRequests }

// Client talks to one account. Safe for concurrent use.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New returns a client for key. An empty base uses DefaultBaseURL; a nil hc a client with
// short connect timeouts over HTTP/1.1 (see docs/ARCHITECTURE.md, DPI resilience).
func New(base, key string, hc *http.Client) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	if hc == nil {
		hc = NewHTTPClient()
	}
	return &Client{base: strings.TrimSuffix(base, "/"), key: key, http: hc}
}

// NewHTTPClient is the transport used for the API and the CDN downloads: HTTP/1.1 (one reset
// costs one request, not all), short dial / TLS / header timeouts, no overall timeout (bodies
// may be long streams; callers bound them with their context).
func NewHTTPClient() *http.Client {
	dialer := &lastGoodDialer{d: &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, last: map[string]string{}}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   6 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{}, // no HTTP/2
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}}
}

// HTTP is the client's transport (the CDN reads use it too).
func (c *Client) HTTP() *http.Client { return c.http }

type envelope struct {
	Success *bool           `json:"success"`
	Error   json.RawMessage `json:"error"`
	Detail  string          `json:"detail"`
	Data    json.RawMessage `json:"data"`
}

func (e envelope) code() string {
	var s string
	if json.Unmarshal(e.Error, &s) == nil {
		return s
	}
	return ""
}

// do sends one request and decodes the envelope. GETs are tried twice on transport errors
// and 5xx (on a new connection). The URL (it may hold the key) never reaches an error.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, ctype string, auth bool) (*envelope, error) {
	u := c.base + "/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	tries := 1
	if method == http.MethodGet {
		tries = 2
	}
	var lastErr error
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, fmt.Errorf("torbox %s: bad request", path)
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")
		if auth {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if try > 0 {
			req.Close = true
		}
		resp, err := c.http.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			lastErr = fmt.Errorf("torbox %s: %w", path, err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if rerr != nil {
			lastErr = fmt.Errorf("torbox %s: read: %w", path, rerr)
			continue
		}
		var env envelope
		jerr := json.Unmarshal(raw, &env)
		if resp.StatusCode >= 400 {
			e := &APIError{Status: resp.StatusCode}
			if jerr == nil {
				e.Code, e.Detail = env.code(), truncate(env.Detail, 200)
			}
			if resp.StatusCode >= 500 {
				lastErr = e
				continue
			}
			return nil, e
		}
		if jerr != nil {
			return nil, fmt.Errorf("%w (%s)", ErrMalformed, path)
		}
		if env.Success != nil && !*env.Success {
			return nil, &APIError{Status: resp.StatusCode, Code: env.code(), Detail: truncate(env.Detail, 200)}
		}
		return &env, nil
	}
	return nil, lastErr
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// User is the part of user/me Nextflix shows.
type User struct {
	Plan             int    `json:"plan"`
	PremiumExpiresAt string `json:"premium_expires_at"`
	IsSubscribed     bool   `json:"is_subscribed"`
	CooldownUntil    string `json:"cooldown_until"`
}

// Me returns the account's plan (a cheap call that checks the key).
func (c *Client) Me(ctx context.Context) (*User, error) {
	env, err := c.do(ctx, http.MethodGet, "user/me", url.Values{"settings": {"false"}}, nil, "", true)
	if err != nil {
		return nil, err
	}
	var u User
	if json.Unmarshal(env.Data, &u) != nil || string(env.Data) == "null" {
		return nil, ErrMalformed
	}
	return &u, nil
}

// CachedFile is one file of a cached torrent: Name is its path inside the torrent
// ("Big Buck Bunny/Big Buck Bunny.mp4").
type CachedFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type CachedItem struct {
	Hash  string       `json:"hash"`
	Name  string       `json:"name"`
	Size  int64        `json:"size"`
	Files []CachedFile `json:"files"`
}

// CheckCached asks which hashes TorBox has cached, in batches of MaxCheckBatch. cached holds
// the cached ones (lower-case hash); known holds every hash the answers covered, so a hash
// in known but not in cached is not cached. A failed or malformed batch leaves its hashes
// unknown (err is the last such failure); a malformed item makes its batch unknown too, but
// the well-formed cached items still count.
func (c *Client) CheckCached(ctx context.Context, hashes []string) (cached map[string]CachedItem, known map[string]bool, err error) {
	cached, known = map[string]CachedItem{}, map[string]bool{}
	for i := 0; i < len(hashes); i += MaxCheckBatch {
		batch := hashes[i:min(i+MaxCheckBatch, len(hashes))]
		env, e := c.do(ctx, http.MethodGet, "torrents/checkcached",
			url.Values{"hash": {strings.Join(batch, ",")}, "format": {"list"}, "list_files": {"true"}}, nil, "", true)
		if e != nil {
			err = e
			continue
		}
		items, complete := parseCached(env.Data)
		for _, it := range items {
			cached[it.Hash] = it
		}
		if !complete {
			err = fmt.Errorf("%w (checkcached)", ErrMalformed)
			continue
		}
		for _, h := range batch {
			known[strings.ToLower(h)] = true
		}
	}
	return cached, known, err
}

// parseCached reads checkcached's data: a list (format=list), an object keyed by hash
// (format=object) or null / {} / [] for "none cached". complete is false when the shape or
// any item was not understood.
func parseCached(data json.RawMessage) (items []CachedItem, complete bool) {
	d := bytes.TrimSpace(data)
	if len(d) == 0 || string(d) == "null" {
		return nil, true
	}
	var raws []json.RawMessage
	switch d[0] {
	case '[':
		if json.Unmarshal(d, &raws) != nil {
			return nil, false
		}
	case '{':
		var m map[string]json.RawMessage
		if json.Unmarshal(d, &m) != nil {
			return nil, false
		}
		for _, v := range m {
			raws = append(raws, v)
		}
	default:
		return nil, false
	}
	complete = true
	for _, r := range raws {
		var it CachedItem
		if json.Unmarshal(r, &it) != nil || !validHash(it.Hash) {
			complete = false
			continue
		}
		it.Hash = strings.ToLower(it.Hash)
		ok := true
		for _, f := range it.Files {
			if f.Name == "" || f.Size < 0 {
				ok = false
			}
		}
		if !ok {
			complete = false
			continue
		}
		items = append(items, it)
	}
	return items, complete
}

func validHash(h string) bool {
	if len(h) != 40 {
		return false
	}
	for _, r := range h {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// File is a file of a torrent in the account.
type File struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"` // path inside the torrent
	ShortName string `json:"short_name"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mimetype"`
}

type Torrent struct {
	ID               int64  `json:"id"`
	Hash             string `json:"hash"`
	Name             string `json:"name"`
	DownloadState    string `json:"download_state"`
	DownloadFinished bool   `json:"download_finished"`
	DownloadPresent  bool   `json:"download_present"`
	Files            []File `json:"files"`
}

// Ready: the files can be downloaded now.
func (t *Torrent) Ready() bool {
	return t.DownloadPresent || t.DownloadFinished || t.DownloadState == "cached" || t.DownloadState == "completed"
}

// myListPage is how many torrents one mylist call asks for; myListMaxPages bounds a lookup.
var myListPage = 1000

const myListMaxPages = 50

// FindTorrent returns the account's torrent id of hash (0 when it is not there), reading
// mylist page by page (bypass skips TorBox's own 10 min list cache).
func (c *Client) FindTorrent(ctx context.Context, hash string, bypass bool) (int64, error) {
	hash = strings.ToLower(hash)
	for page := 0; page < myListMaxPages; page++ {
		list, err := c.MyList(ctx, bypass, page*myListPage)
		if err != nil {
			return 0, err
		}
		for _, t := range list {
			if t.Hash == hash {
				return t.ID, nil
			}
		}
		if len(list) < myListPage {
			return 0, nil
		}
	}
	return 0, nil
}

// MyList returns one page of the torrents in the account, from offset (bypass skips TorBox's
// own 10 min list cache).
func (c *Client) MyList(ctx context.Context, bypass bool, offset int) ([]Torrent, error) {
	q := url.Values{"limit": {strconv.Itoa(myListPage)}, "offset": {strconv.Itoa(offset)}}
	if bypass {
		q.Set("bypass_cache", "true")
	}
	env, err := c.do(ctx, http.MethodGet, "torrents/mylist", q, nil, "", true)
	if err != nil {
		return nil, err
	}
	d := bytes.TrimSpace(env.Data)
	if len(d) == 0 || string(d) == "null" {
		return nil, nil
	}
	var list []Torrent
	if json.Unmarshal(d, &list) != nil {
		return nil, ErrMalformed
	}
	for i := range list {
		list[i].Hash = strings.ToLower(list[i].Hash)
	}
	return list, nil
}

// Torrent returns one torrent of the account, fresh.
func (c *Client) Torrent(ctx context.Context, id int64) (*Torrent, error) {
	env, err := c.do(ctx, http.MethodGet, "torrents/mylist",
		url.Values{"id": {strconv.FormatInt(id, 10)}, "bypass_cache": {"true"}}, nil, "", true)
	if err != nil {
		return nil, err
	}
	var t Torrent
	if json.Unmarshal(env.Data, &t) != nil || t.ID == 0 {
		return nil, ErrMalformed
	}
	t.Hash = strings.ToLower(t.Hash)
	return &t, nil
}

// CreateTorrent adds a magnet to the account and returns its torrent id. With onlyIfCached
// TorBox refuses a torrent it does not have cached (nothing is downloaded).
func (c *Client) CreateTorrent(ctx context.Context, magnet string, onlyIfCached bool) (int64, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("magnet", magnet)
	if onlyIfCached {
		_ = w.WriteField("add_only_if_cached", "true")
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	env, err := c.do(ctx, http.MethodPost, "torrents/createtorrent", nil, &body, w.FormDataContentType(), true)
	if err != nil {
		return 0, err
	}
	var d struct {
		TorrentID int64 `json:"torrent_id"`
		QueuedID  int64 `json:"queued_id"`
	}
	if json.Unmarshal(env.Data, &d) != nil || d.TorrentID == 0 {
		return 0, ErrMalformed
	}
	return d.TorrentID, nil
}

// RequestDL returns a direct HTTPS link (CDN, supports Range) to one file. It opens for about
// 3 hours; a download started within them keeps going.
func (c *Client) RequestDL(ctx context.Context, torrentID, fileID int64) (string, error) {
	env, err := c.do(ctx, http.MethodGet, "torrents/requestdl", url.Values{
		"token": {c.key}, "torrent_id": {strconv.FormatInt(torrentID, 10)},
		"file_id": {strconv.FormatInt(fileID, 10)}, "redirect": {"false"},
	}, nil, "", false)
	if err != nil {
		return "", err
	}
	var link string
	if json.Unmarshal(env.Data, &link) != nil {
		return "", ErrMalformed
	}
	u, perr := url.Parse(link)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		// The fake server in tests speaks plain HTTP on loopback
		if perr != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) {
			return "", ErrMalformed
		}
	}
	return link, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// lastGoodDialer remembers the address each host:port last connected to and dials it when
// the name does not resolve (Docker's DNS fails now and then: "server misbehaving", i/o timeouts).
// TLS still verifies the certificate against the host name.
type lastGoodDialer struct {
	d    *net.Dialer
	mu   sync.Mutex
	last map[string]string // "host:port" -> "ip:port"
}

func (l *lastGoodDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := l.d.DialContext(ctx, network, addr)
	if err == nil {
		l.mu.Lock()
		l.last[addr] = conn.RemoteAddr().String()
		l.mu.Unlock()
		return conn, nil
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return nil, err
	}
	l.mu.Lock()
	ip, ok := l.last[addr]
	l.mu.Unlock()
	if !ok || ctx.Err() != nil {
		return nil, err
	}
	return l.d.DialContext(ctx, network, ip)
}
