package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
	"nextflix/internal/platform/upstream"
)

const (
	defaultUpstream = "https://image.tmdb.org"
	defaultMaxBytes = 2 << 30 // 2 GiB
	maxImageBytes   = 20 << 20
	// Network budget of one image (retries and hedging included); waiting for a
	// download slot is limited separately.
	fetchBudget = 4 * time.Second
	slotWait    = 10 * time.Second
	// Upstream downloads at once (user requests and warmer together).
	maxDownloads = 12
	notFoundTTL  = 15 * time.Minute
	failureTTL   = 30 * time.Second
	negativeMax  = 5000
	cleanupEvery = 30 * time.Minute
)

func newUpstream() *upstream.Client {
	return upstream.New(upstream.Options{
		Attempts:        3,
		HedgeAfter:      1200 * time.Millisecond,
		Budget:          fetchBudget,
		MaxBody:         maxImageBytes,
		ReadIdleTimeout: 1500 * time.Millisecond,
		PoolSize:        maxDownloads,
		UserAgent:       "Nextflix image cache",
	})
}

var (
	errNotFound = errors.New("image not found upstream")
	errUpstream = errors.New("image upstream failed")
	// errUnreachable is a network failure or timeout; the handler answers 504.
	errUnreachable = errors.New("image upstream unreachable")
)

type Config struct {
	// Dir holds the cache; it is created if missing.
	Dir string
	// MaxBytes caps the cache size (default 2 GiB).
	MaxBytes int64
	// Upstream is the TMDB image host (default https://image.tmdb.org); tests point it elsewhere.
	Upstream string
	// WarmEvery is the home feed warm-up interval; 0 disables the warmer.
	WarmEvery time.Duration
	// Top10 lists the artwork of the home Top 10 rows in lang ("en" or "tr"); nil skips them.
	Top10 func(lang string) []Art
}

// Art is the TMDB artwork of one title (paths like "/abc.jpg").
type Art struct {
	Poster, Backdrop string
}

// Service is a disk-cached proxy for TMDB images.
type Service struct {
	cfg    Config
	store  *store
	tm     *tmdb.Client
	client *upstream.Client
	slots  chan struct{}

	flightMu sync.Mutex
	flight   map[key]*call

	// Remembered failures: TMDB 404s for long, other failures briefly.
	negMu sync.Mutex
	neg   map[key]negative

	baseCtx context.Context
}

type negative struct {
	until time.Time
	err   error
}

type call struct {
	done chan struct{}
	err  error
}

// NewService opens the cache dir; if that fails it falls back to a temp dir so images still load.
func NewService(cfg Config, tm *tmdb.Client) *Service {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.Upstream == "" {
		cfg.Upstream = defaultUpstream
	}
	cfg.Upstream = strings.TrimRight(cfg.Upstream, "/")

	st, err := newStore(cfg.Dir, cfg.MaxBytes)
	if err != nil {
		fallback := filepath.Join(os.TempDir(), "nextflix-img-cache")
		log.Printf("images: cache dir %q unusable (%v), using %s", cfg.Dir, err, fallback)
		if st, err = newStore(fallback, cfg.MaxBytes); err != nil {
			log.Printf("images: fallback cache dir unusable: %v", err)
		}
	}

	return &Service{
		cfg:     cfg,
		store:   st,
		tm:      tm,
		client:  newUpstream(),
		slots:   make(chan struct{}, maxDownloads),
		flight:  map[key]*call{},
		neg:     map[key]negative{},
		baseCtx: context.Background(),
	}
}

// Start runs cache cleanup and the warmer until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	s.baseCtx = ctx
	if s.store == nil {
		return
	}
	go s.cleanupLoop(ctx)
	if s.cfg.WarmEvery > 0 {
		go s.warmLoop(ctx)
	}
}

func (s *Service) cleanupLoop(ctx context.Context) {
	t := time.NewTicker(cleanupEvery)
	defer t.Stop()
	for {
		if removed, total, err := s.store.cleanup(); err != nil {
			log.Printf("images: cleanup: %v", err)
		} else if removed > 0 {
			log.Printf("images: evicted %d files, cache now %d MB", removed, total>>20)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.store.wake:
		}
	}
}

// ensure makes sure k is on disk and returns its info. Concurrent callers for the same
// key share one download, which keeps running even if the caller gives up.
func (s *Service) ensure(ctx context.Context, k key) (os.FileInfo, bool, error) {
	if s.store == nil {
		return nil, false, errUpstream
	}
	if fi, ok := s.store.stat(k); ok {
		return fi, true, nil
	}
	if err := s.negative(k); err != nil {
		return nil, false, err
	}

	c := s.download(k)
	select {
	case <-c.done:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if c.err != nil {
		return nil, false, c.err
	}
	fi, ok := s.store.stat(k)
	if !ok {
		return nil, false, errUpstream
	}
	return fi, false, nil
}

func (s *Service) download(k key) *call {
	s.flightMu.Lock()
	if c, ok := s.flight[k]; ok {
		s.flightMu.Unlock()
		return c
	}
	c := &call{done: make(chan struct{})}
	s.flight[k] = c
	s.flightMu.Unlock()

	go func() {
		c.err = s.fetch(k)
		s.flightMu.Lock()
		delete(s.flight, k)
		s.flightMu.Unlock()
		close(c.done)
	}()
	return c
}

func (s *Service) fetch(k key) error {
	wait, cancel := context.WithTimeout(s.baseCtx, slotWait)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-wait.Done():
		return fmt.Errorf("%w: no download slot: %v", errUnreachable, wait.Err())
	}

	resp, err := s.client.Get(s.baseCtx, s.cfg.Upstream+"/t/p/"+k.size+"/"+k.file, nil)
	if err != nil {
		err = fmt.Errorf("%w: %v", errUnreachable, err)
		log.Printf("images: %s: %v", k, err)
		s.markNegative(k, err, failureTTL)
		return err
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case resp.Status == http.StatusNotFound:
		s.markNegative(k, errNotFound, notFoundTTL)
		return errNotFound
	case resp.Status != http.StatusOK:
		err = fmt.Errorf("%w: status %d", errUpstream, resp.Status)
	case ct != "" && !strings.HasPrefix(ct, "image/"):
		err = fmt.Errorf("%w: content type %q", errUpstream, ct)
	}
	if err != nil {
		s.markNegative(k, err, failureTTL)
		return err
	}
	if err := s.store.write(k, bytes.NewReader(resp.Body), int64(len(resp.Body))); err != nil {
		return fmt.Errorf("%w: %v", errUpstream, err)
	}
	return nil
}

// negative returns the remembered failure of k, if any.
func (s *Service) negative(k key) error {
	s.negMu.Lock()
	defer s.negMu.Unlock()
	n, ok := s.neg[k]
	if !ok {
		return nil
	}
	if time.Now().After(n.until) {
		delete(s.neg, k)
		return nil
	}
	return n.err
}

func (s *Service) markNegative(k key, err error, ttl time.Duration) {
	s.negMu.Lock()
	defer s.negMu.Unlock()
	if len(s.neg) >= negativeMax {
		s.neg = map[key]negative{}
	}
	s.neg[k] = negative{until: time.Now().Add(ttl), err: err}
}
