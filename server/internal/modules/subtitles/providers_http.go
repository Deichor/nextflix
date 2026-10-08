package subtitles

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"nextflix/internal/platform/upstream"
)

// The ISP's DPI resets some TLS connections from Docker, so the online providers go through
// platform/upstream: GETs retry on a fresh connection (and the APIs hedge a slow first try),
// POSTs (OpenSubtitles login and download, the turkcealtyazi form) retry resets the same way.
// Those POSTs are safe to repeat.

// newProviderClient builds the client of the online providers. retry5xx also retries 500/503
// (the APIs); turkcealtyazi only retries 502/504, as its 503 is a Cloudflare challenge.
func newProviderClient(hedge, retry5xx bool, checkRedirect func(*http.Request, []*http.Request) error) *upstream.Client {
	o := upstream.Options{
		Attempts:        3,
		Budget:          25 * time.Second,
		MaxBody:         24 << 20,
		PoolSize:        8,
		DialTimeout:     4 * time.Second,
		TLSTimeout:      5 * time.Second,
		HeaderTimeout:   15 * time.Second,
		ReadIdleTimeout: 5 * time.Second,
		UserAgent:       userAgent,
		CheckRedirect:   checkRedirect,
		RetryStatus: func(code int) bool {
			return code == http.StatusBadGateway || code == http.StatusGatewayTimeout ||
				(retry5xx && (code == http.StatusInternalServerError || code == http.StatusServiceUnavailable))
		},
	}
	if hedge {
		o.HedgeAfter = 4 * time.Second
	}
	return upstream.New(o)
}

// errTemporary marks provider failures worth retrying on the next call (5xx, FlareSolverr
// network errors); see isTemporary.
var errTemporary = errors.New("temporary")

// isTemporary reports whether a provider failure came from the network or an overloaded
// server, so its empty answer must not be cached: the next call asks again. Real "no results"
// and other errors (bad key, quota) are cached as before.
func isTemporary(err error) bool {
	if err == nil {
		return false
	}
	var se statusError
	if errors.As(err, &se) && se.code >= 500 {
		return true
	}
	return errors.Is(err, upstream.ErrTransport) || errors.Is(err, errTemporary) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// getBytes fetches u (200 only) through c.
func getBytes(ctx context.Context, c *upstream.Client, u string, header http.Header) ([]byte, error) {
	resp, err := c.Get(ctx, u, header)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, statusError{code: resp.Status, body: truncate(resp.Body, 512)}
	}
	return resp.Body, nil
}

// redacted hides a secret (an API key in a URL) in an error message but keeps the chain.
type redacted struct {
	err    error
	secret string
}

func (r redacted) Error() string {
	if r.secret == "" {
		return r.err.Error()
	}
	return strings.ReplaceAll(r.err.Error(), r.secret, "***")
}

func (r redacted) Unwrap() error { return r.err }

func redact(err error, secret string) error {
	if err == nil {
		return nil
	}
	return redacted{err: err, secret: secret}
}
