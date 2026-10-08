// Package upstream is an HTTP client for flaky upstreams (TMDB API, image CDN, subtitle
// providers). The ISP's DPI resets some TLS connections, so every request gets short
// connect timeouts, retries on a fresh connection and, for GETs, optionally a hedged second try.
package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Options configure a Client; zero values take the defaults.
type Options struct {
	// Attempts caps the tries per request, hedges included (default 3).
	Attempts int
	// HedgeAfter starts a parallel try on a new connection when no try has response
	// headers yet after this long. 0 disables hedging.
	HedgeAfter time.Duration
	// Budget is the deadline of one request including retries and body; 0 means ctx only.
	Budget time.Duration
	// MaxBody caps the response body (default 8 MiB).
	MaxBody int64
	// HTTP2 allows HTTP/2. The default is HTTP/1.1 with a per-host pool: a reset then
	// kills one request instead of every request multiplexed on the connection.
	HTTP2 bool
	// PoolSize caps the pooled connections per host (default 16).
	PoolSize int
	// Per-try timeouts (defaults 3 s, 3 s, 5 s).
	DialTimeout, TLSTimeout, HeaderTimeout time.Duration
	// ReadIdleTimeout aborts a try whose body made no progress for this long (default 2 s);
	// DPI sometimes stalls a connection mid-body instead of resetting it.
	ReadIdleTimeout time.Duration
	UserAgent       string
	// OnTryError, if set, sees every failed try (for logs and measurements).
	OnTryError func(err error)
	// RetryStatus decides which response statuses are retried (default: 429 and 5xx).
	RetryStatus func(code int) bool
	// CheckRedirect is used by the underlying http.Clients (default: follow up to 10).
	CheckRedirect func(req *http.Request, via []*http.Request) error
}

// Response is a fully read upstream response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// ErrTooLarge is returned when the body exceeds Options.MaxBody.
var ErrTooLarge = errors.New("upstream: response body too large")

// ErrTransport marks a request that failed on the network after every try (resets, timeouts,
// stalled or cut bodies, the budget); callers may treat it as temporary.
var ErrTransport = errors.New("upstream: transport failure")

type redirectError struct{ err error }

func (e redirectError) Error() string { return e.err.Error() }
func (e redirectError) Unwrap() error { return e.err }

type transportError struct{ err error }

func (e transportError) Error() string   { return e.err.Error() }
func (e transportError) Unwrap() []error { return []error{ErrTransport, e.err} }

// Stats counts what a Client did since it was created.
type Stats struct {
	Requests, Tries, Retries, Hedges, Failures int64
}

// Client runs GETs with retries and hedging. It is safe for concurrent use.
type Client struct {
	opts Options
	// pooled serves first tries; fresh opens a new connection per try (retries, hedges).
	pooled, fresh *http.Client

	requests, tries, retries, hedges, failures atomic.Int64
}

func New(o Options) *Client {
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	if o.MaxBody <= 0 {
		o.MaxBody = 8 << 20
	}
	if o.PoolSize <= 0 {
		o.PoolSize = 16
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 3 * time.Second
	}
	if o.TLSTimeout <= 0 {
		o.TLSTimeout = 3 * time.Second
	}
	if o.HeaderTimeout <= 0 {
		o.HeaderTimeout = 5 * time.Second
	}
	if o.ReadIdleTimeout <= 0 {
		o.ReadIdleTimeout = 2 * time.Second
	}
	if check := o.CheckRedirect; check != nil {
		// A refused redirect is final, not a network failure to retry
		o.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if err := check(req, via); err != nil {
				return redirectError{err}
			}
			return nil
		}
	}
	if o.RetryStatus == nil {
		o.RetryStatus = func(code int) bool { return code == http.StatusTooManyRequests || code >= 500 }
	}
	return &Client{
		opts:   o,
		pooled: &http.Client{Transport: newTransport(o, true), CheckRedirect: o.CheckRedirect},
		fresh:  &http.Client{Transport: newTransport(o, false), CheckRedirect: o.CheckRedirect},
	}
}

func newTransport(o Options, keepAlive bool) *http.Transport {
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer(o.DialTimeout),
		TLSHandshakeTimeout:   o.TLSTimeout,
		ResponseHeaderTimeout: o.HeaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4 * o.PoolSize,
		MaxIdleConnsPerHost:   o.PoolSize,
		DisableKeepAlives:     !keepAlive,
		ForceAttemptHTTP2:     o.HTTP2,
	}
	if keepAlive {
		t.MaxConnsPerHost = o.PoolSize
	}
	if !o.HTTP2 {
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return t
}

// dialer dials IPv4 only when the host has no IPv6 route (Docker bridges usually have
// none), so no time goes to AAAA addresses that cannot work.
func dialer(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if IPv6Routable() {
		return d.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network == "tcp" {
			network = "tcp4"
		}
		return d.DialContext(ctx, network, addr)
	}
}

var ipv6Once = sync.OnceValue(func() bool {
	// A UDP "dial" sends nothing; it only fails when there is no route.
	c, err := net.Dial("udp6", "[2606:4700:4700::1111]:53")
	if err != nil {
		return false
	}
	c.Close()
	return true
})

// IPv6Routable reports whether the host has a route to the IPv6 internet.
func IPv6Routable() bool { return ipv6Once() }

// Stats returns the counters of c.
func (c *Client) Stats() Stats {
	return Stats{
		Requests: c.requests.Load(), Tries: c.tries.Load(), Retries: c.retries.Load(),
		Hedges: c.hedges.Load(), Failures: c.failures.Load(),
	}
}

type result struct {
	resp      *Response
	err       error
	permanent bool // retrying cannot help
}

// Get fetches url and reads the whole body. Transport errors, resets, truncated bodies,
// 429 and 5xx (Options.RetryStatus) are retried on a new connection; other statuses are
// returned as they are. When the budget runs out the error wraps context.DeadlineExceeded;
// every network failure wraps ErrTransport.
func (c *Client) Get(ctx context.Context, url string, header http.Header) (*Response, error) {
	return c.do(ctx, http.MethodGet, url, header, nil)
}

// Post sends body like Get, with the same retries on a new connection but never hedged. Only
// for endpoints that are safe to repeat (a reset may hide a request the server did handle).
func (c *Client) Post(ctx context.Context, url string, header http.Header, body []byte) (*Response, error) {
	return c.do(ctx, http.MethodPost, url, header, body)
}

func (c *Client) do(ctx context.Context, method, url string, header http.Header, body []byte) (*Response, error) {
	c.requests.Add(1)
	if c.opts.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.Budget)
		defer cancel()
	}

	results := make(chan result, c.opts.Attempts)
	gotHeaders := make(chan struct{}, c.opts.Attempts)
	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	planned, running := 0, 0
	start := func() {
		tctx, cancel := context.WithCancel(ctx)
		cancels = append(cancels, cancel)
		client := c.pooled
		if planned > 0 {
			client = c.fresh
		}
		planned++
		running++
		c.tries.Add(1)
		go func() {
			r := c.try(tctx, cancel, client, method, url, header, body, gotHeaders)
			if r.err != nil && c.opts.OnTryError != nil {
				c.opts.OnTryError(r.err)
			}
			results <- r
		}()
	}
	start()

	var hedge, retry <-chan time.Time
	if c.opts.HedgeAfter > 0 && c.opts.Attempts > 1 && method == http.MethodGet {
		t := time.NewTimer(c.opts.HedgeAfter)
		defer t.Stop()
		hedge = t.C
	}
	headers := false
	retryPending := false
	var last result
	for {
		select {
		case <-gotHeaders:
			headers = true
		case <-hedge:
			hedge = nil
			if !headers && !retryPending && planned < c.opts.Attempts {
				c.hedges.Add(1)
				start()
			}
		case <-retry:
			retry, retryPending = nil, false
			c.retries.Add(1)
			start()
		case r := <-results:
			running--
			if r.err == nil && !c.opts.RetryStatus(r.resp.Status) {
				return r.resp, nil
			}
			last = r
			if r.permanent {
				c.failures.Add(1)
				return nil, r.err
			}
			if ctx.Err() != nil {
				c.failures.Add(1)
				return nil, transportError{budgetErr(ctx, last.err)}
			}
			if !retryPending && planned < c.opts.Attempts {
				retryPending = true
				retry = time.After(100*time.Millisecond + rand.N(200*time.Millisecond))
			} else if running == 0 && !retryPending {
				c.failures.Add(1)
				if last.resp != nil {
					return last.resp, nil
				}
				return nil, transportError{last.err}
			}
		case <-ctx.Done():
			c.failures.Add(1)
			return nil, transportError{budgetErr(ctx, last.err)}
		}
	}
}

func budgetErr(ctx context.Context, last error) error {
	if last != nil {
		return fmt.Errorf("upstream: %w (last error: %v)", ctx.Err(), last)
	}
	return fmt.Errorf("upstream: %w", ctx.Err())
}

var errStalled = errors.New("body stalled")

func (c *Client) try(ctx context.Context, cancel context.CancelFunc, client *http.Client, method, url string, header http.Header,
	body []byte, gotHeaders chan<- struct{}) result {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return result{err: err, permanent: true}
	}
	if header != nil {
		req.Header = header.Clone()
	}
	if c.opts.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		var re redirectError
		if errors.As(err, &re) {
			return result{err: err, permanent: true}
		}
		return result{err: err}
	}
	defer resp.Body.Close()
	gotHeaders <- struct{}{} // buffered: one slot per try

	var stalled atomic.Bool
	idle := time.AfterFunc(c.opts.ReadIdleTimeout, func() {
		stalled.Store(true)
		cancel()
	})
	defer idle.Stop()
	data, err := io.ReadAll(&progressReader{r: io.LimitReader(resp.Body, c.opts.MaxBody+1), idle: idle, d: c.opts.ReadIdleTimeout})
	if err != nil {
		if stalled.Load() {
			err = errStalled
		}
		return result{err: fmt.Errorf("read body: %w", err)}
	}
	if int64(len(data)) > c.opts.MaxBody {
		return result{err: ErrTooLarge, permanent: true}
	}
	return result{resp: &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}}
}

// progressReader pushes the idle timer back whenever bytes arrive.
type progressReader struct {
	r    io.Reader
	idle *time.Timer
	d    time.Duration
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.idle.Reset(p.d)
	}
	return n, err
}
