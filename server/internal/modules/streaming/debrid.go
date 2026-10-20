package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/torbox"
)

// TorBox debrid: a release TorBox has cached plays from its CDN at line speed instead of the
// BitTorrent swarm. Resolve asks which candidates are cached (debrid_resolve.go); a cached
// pick is a "debrid source". Its hash/index keep addressing it on every endpoint, and
// fileURL maps it to a loopback proxy (serveFile) that reads TorBox's HTTPS link with
// Range, refreshes the link on 403 / expiry and resumes cut or stalled reads, so ffmpeg,
// ffprobe, the thumbnails and the subtitle reads need no TorBox knowledge. TorrServer is not
// used for debrid sources. When TorBox fails, everything falls back to TorrServer.
// See docs/ARCHITECTURE.md (Debrid).

const (
	tsDebridCheckMax      = 40               // candidate hashes asked per resolve
	tsDebridCacheTTL      = 30 * time.Minute // checkcached answers (cached and not cached)
	tsDebridCheckTimeout  = 2500 * time.Millisecond
	tsDebridLinkFresh     = 2 * time.Hour // TorBox links open for 3 h
	tsDebridLinkTimeout   = 15 * time.Second
	tsDebridFetchTimeout  = 30 * time.Second // one shared link request (torrent lookup, add, requestdl)
	tsDebridSettingsFresh = 10 * time.Second
	tsDebridAddPerHour    = 20 // "add uncached" torrents sent to TorBox per hour

	tsSourceTorBox = "torbox" // tsResolveResult.Source of a debrid source

	tsDebridSourceIdle = 6 * time.Hour    // a registered source nothing asked for this long is forgotten
	tsDebridKeep       = 24 * time.Hour   // torrent ids and "added" marks of unregistered hashes
	tsDebridPruneEvery = 5 * time.Minute  // how often the maps above are pruned
	tsDebridRemapRetry = 30 * time.Second // a debrid file TorrServer could not find is looked up again after this
)

var (
	errDebridOff         = errors.New("torbox is off")
	errDebridUnavailable = errors.New("torbox unavailable")
	errDebridPaused      = errors.New("torbox paused after a failure")
	// errDebridTorrent: one torrent or file is not usable on TorBox; TorBox itself works
	errDebridTorrent = errors.New("torbox torrent problem")
)

// tsDebridSource is a file played from TorBox: path and size find it among the files of
// the TorBox torrent.
type tsDebridSource struct {
	hash   string
	index  int
	path   string
	size   int64
	title  string
	magnet string
}

type tsDebridLink struct {
	url       string
	torrentID int64
	fileID    int64
	at        time.Time
}

type tsDebridCheck struct {
	item *torbox.CachedItem // nil = not cached
	at   time.Time
}

// tsDebridTorrent holds the registered files of one hash.
type tsDebridTorrent struct {
	files map[int]tsDebridSource // index -> source
	used  time.Time              // last lookup
}

type tsDebridTorrentRef struct {
	id int64
	at time.Time
}

type tsDebridCall struct {
	done chan struct{}
	link *tsDebridLink
	err  error
}

type tsDebrid struct {
	s    *Service
	base string // TorBox API base URL
	hc   *http.Client

	mu        sync.Mutex
	st        *DebridSettings
	stAt      time.Time
	checks    map[string]tsDebridCheck      // hash -> checkcached answer
	sources   map[string]*tsDebridTorrent   // hash -> registered debrid sources
	torrents  map[string]tsDebridTorrentRef // hash -> TorBox torrent id
	links     map[string]*tsDebridLink      // "hash:index" -> download link
	calls     map[string]*tsDebridCall      // running link requests
	remaps    map[string]int                // "hash:index" -> TorrServer file id (fallback)
	noRemap   map[string]time.Time          // "hash:index" -> when TorrServer could not find it
	broken    map[string]time.Time          // "hash:index" -> when its CDN reads gave up (plays from TorrServer for a while)
	warmed    map[string]time.Time          // "hash:index" -> last keep-alive read
	downUntil time.Time
	downWhy   string
	added     map[string]time.Time // hashes sent with "add uncached"
	addTimes  []time.Time
	// hash -> last look for a cached TorBox source naming it (adoptSaved, reuse.go)
	adoptTried map[string]time.Time

	cache     tsProxyCache // chunks of the file proxy (debrid_proxy.go)
	proxyOnce sync.Once
	proxyBase string
	proxyErr  error

	line tsDebridLine // rates TorBox delivered lately (debrid_rate.go)
}

func newDebrid(s *Service, base string) *tsDebrid {
	d := &tsDebrid{
		s: s, base: base, hc: torbox.NewHTTPClient(),
		checks: map[string]tsDebridCheck{}, sources: map[string]*tsDebridTorrent{}, torrents: map[string]tsDebridTorrentRef{},
		links: map[string]*tsDebridLink{}, calls: map[string]*tsDebridCall{}, remaps: map[string]int{}, noRemap: map[string]time.Time{},
		broken: map[string]time.Time{}, warmed: map[string]time.Time{},
		added: map[string]time.Time{}, adoptTried: map[string]time.Time{},
	}
	go func() {
		t := time.NewTicker(tsDebridPruneEvery)
		defer t.Stop()
		for now := range t.C {
			d.pruneDebrid(now)
		}
	}()
	return d
}

// pruneDebrid forgets what grew stale: idle sources (with their links and remaps), old
// keep-alive, broken, adoption and add marks, unused torrent ids and expired cache answers.
func (d *tsDebrid) pruneDebrid(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for h, t := range d.sources {
		if now.Sub(t.used) >= tsDebridSourceIdle {
			delete(d.sources, h)
		}
	}
	live := func(k string) bool {
		h, _, _ := strings.Cut(k, ":")
		return d.sources[h] != nil
	}
	for k, l := range d.links {
		if !live(k) || now.Sub(l.at) >= tsDebridLinkFresh {
			delete(d.links, k)
		}
	}
	for k := range d.remaps {
		if !live(k) {
			delete(d.remaps, k)
		}
	}
	prune := func(m map[string]time.Time, keep time.Duration) {
		for k, at := range m {
			if now.Sub(at) >= keep {
				delete(m, k)
			}
		}
	}
	prune(d.noRemap, tsDebridRemapRetry)
	prune(d.warmed, tsDebridWarmEvery)
	prune(d.broken, tsDebridBrokenFor)
	prune(d.added, tsDebridKeep)
	prune(d.adoptTried, tsAdoptRetry)
	for h, r := range d.torrents {
		if d.sources[h] == nil && now.Sub(r.at) >= tsDebridKeep {
			delete(d.torrents, h)
		}
	}
	for h, c := range d.checks {
		if now.Sub(c.at) >= tsDebridCacheTTL {
			delete(d.checks, h)
		}
	}
}

func tsDebridKey(hash string, index int) string { return hash + ":" + strconv.Itoa(index) }

// settings returns the stored settings, read at most every tsDebridSettingsFresh.
func (d *tsDebrid) settings() DebridSettings {
	d.mu.Lock()
	if d.st != nil && time.Since(d.stAt) < tsDebridSettingsFresh {
		st := *d.st
		d.mu.Unlock()
		return st
	}
	d.mu.Unlock()
	st, err := d.s.DebridSettings()
	if err != nil {
		return DebridSettings{}
	}
	d.mu.Lock()
	d.st, d.stAt = st, time.Now()
	d.mu.Unlock()
	return *st
}

func (d *tsDebrid) settingsChanged() {
	d.mu.Lock()
	d.st = nil
	d.downUntil, d.downWhy = time.Time{}, ""
	d.torrents = map[string]tsDebridTorrentRef{}
	d.links = map[string]*tsDebridLink{}
	d.mu.Unlock()
}

// enabled: a key is set and TorBox is switched on (it may still be down for a while).
func (d *tsDebrid) enabled() bool {
	if d == nil {
		return false
	}
	st := d.settings()
	return st.TorBoxAPIKey != "" && !st.TorBoxDisabled
}

// usable: enabled and not paused after a failure.
func (d *tsDebrid) usable() bool {
	if !d.enabled() {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Now().After(d.downUntil)
}

func (d *tsDebrid) client() *torbox.Client {
	return torbox.New(d.base, d.settings().TorBoxAPIKey, d.hc)
}

// fail pauses TorBox after a failure that will not go away at once (transport errors, 5xx,
// timeouts), so resolve and play take the torrent path without waiting for it. Malformed
// answers and problems of one torrent (errDebridTorrent) do not pause it.
func (d *tsDebrid) fail(err error) {
	if err == nil || errors.Is(err, torbox.ErrMalformed) || errors.Is(err, context.Canceled) || errors.Is(err, errDebridTorrent) {
		return
	}
	pause, why := 30*time.Second, "unreachable"
	var apiErr *torbox.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Unauthorized():
		pause, why = 10*time.Minute, "unauthorized or plan expired"
	case errors.As(err, &apiErr) && apiErr.RateLimited():
		pause, why = time.Minute, "rate limited"
	case errors.As(err, &apiErr) && apiErr.Status < 500:
		return // a refused request (a torrent it does not have), not an outage
	}
	d.mu.Lock()
	wasDown := time.Now().Before(d.downUntil)
	d.downUntil, d.downWhy = time.Now().Add(pause), why
	d.mu.Unlock()
	if !wasDown {
		log.Printf("torbox: %s (%v), using torrents for %s", why, err, pause)
	}
}

// cachedItems returns the TorBox cache entries of the hashes that are cached. Answers are
// kept tsDebridCacheTTL; a failed or malformed answer leaves a hash unknown (not stored).
func (d *tsDebrid) cachedItems(ctx context.Context, hashes []string) map[string]*torbox.CachedItem {
	out := map[string]*torbox.CachedItem{}
	if !d.enabled() {
		return out
	}
	var miss []string
	now := time.Now()
	d.mu.Lock()
	for _, h := range hashes {
		h = strings.ToLower(h)
		if c, ok := d.checks[h]; ok && now.Sub(c.at) < tsDebridCacheTTL {
			if c.item != nil {
				out[h] = c.item
			}
			continue
		}
		miss = append(miss, h)
	}
	d.mu.Unlock()
	if len(miss) == 0 || !d.usable() {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, tsDebridCheckTimeout)
	defer cancel()
	cached, known, err := d.client().CheckCached(ctx, miss)
	if err != nil {
		if ctx.Err() != nil && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("checkcached: %w", context.DeadlineExceeded)
		}
		d.fail(err)
	}
	d.mu.Lock()
	for _, h := range miss {
		if it, ok := cached[h]; ok {
			item := it
			d.checks[h] = tsDebridCheck{item: &item, at: now}
			out[h] = &item
		} else if known[h] {
			d.checks[h] = tsDebridCheck{at: now}
		}
	}
	// Old entries go once the map grows
	if len(d.checks) > 5000 {
		for h, c := range d.checks {
			if now.Sub(c.at) >= tsDebridCacheTTL {
				delete(d.checks, h)
			}
		}
	}
	d.mu.Unlock()
	return out
}

// item returns the cached TorBox entry of one hash (asking TorBox when it is not known).
func (d *tsDebrid) item(ctx context.Context, hash string) *torbox.CachedItem {
	return d.cachedItems(ctx, []string{hash})[strings.ToLower(hash)]
}

func (d *tsDebrid) register(src tsDebridSource) {
	if d == nil {
		return
	}
	src.hash = strings.ToLower(src.hash)
	d.mu.Lock()
	t := d.sources[src.hash]
	if t == nil {
		t = &tsDebridTorrent{files: map[int]tsDebridSource{}}
		d.sources[src.hash] = t
	}
	t.files[src.index] = src
	t.used = time.Now()
	d.mu.Unlock()
}

func (d *tsDebrid) unregister(hash string, index int) {
	hash = strings.ToLower(hash)
	d.mu.Lock()
	if t := d.sources[hash]; t != nil {
		delete(t.files, index)
		if len(t.files) == 0 {
			delete(d.sources, hash)
		}
	}
	delete(d.links, tsDebridKey(hash, index))
	d.mu.Unlock()
}

// unregisterHash forgets every file of hash (it is checked through the swarm now, where
// TorrServer's file ids count).
func (d *tsDebrid) unregisterHash(hash string) {
	if d == nil || hash == "" {
		return
	}
	hash = strings.ToLower(hash)
	d.mu.Lock()
	if t := d.sources[hash]; t != nil {
		for idx := range t.files {
			delete(d.links, tsDebridKey(hash, idx))
		}
		delete(d.sources, hash)
	}
	d.mu.Unlock()
}

// forgetUncached unregisters hash when TorBox said it is not cached (a swarm check of it
// follows). An unknown answer keeps it: its plays fall back to TorrServer by themselves.
func (d *tsDebrid) forgetUncached(hash string) {
	if d == nil {
		return
	}
	hash = strings.ToLower(hash)
	d.mu.Lock()
	c, ok := d.checks[hash]
	d.mu.Unlock()
	if ok && c.item == nil {
		d.unregisterHash(hash)
	}
}

// registered returns the registered source of hash:index whether or not TorBox is enabled
// (its index is TorBox's list position, not TorrServer's file id).
func (d *tsDebrid) registered(hash string, index int) (tsDebridSource, bool) {
	if d == nil {
		return tsDebridSource{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.sources[strings.ToLower(hash)]
	if t == nil {
		return tsDebridSource{}, false
	}
	t.used = time.Now()
	src, ok := t.files[index]
	return src, ok
}

// listIndexed reports whether hash:index names a file by its place in TorBox's list rather than
// TorrServer's file id (debridURL's rule): a registered file, or any file of a registered
// torrent while TorBox is enabled.
func (d *tsDebrid) listIndexed(hash string, index int) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	t := d.sources[strings.ToLower(hash)]
	reg := false
	if t != nil {
		_, reg = t.files[index]
	}
	d.mu.Unlock()
	return reg || (t != nil && d.enabled())
}

// hashSource reports whether hash is a debrid source (any file) while TorBox is enabled.
func (d *tsDebrid) hashSource(hash string) (tsDebridSource, bool) {
	if d == nil {
		return tsDebridSource{}, false
	}
	d.mu.Lock()
	var found tsDebridSource
	ok := false
	if t := d.sources[strings.ToLower(hash)]; t != nil {
		t.used = time.Now()
		for _, src := range t.files {
			found, ok = src, true
			break
		}
	}
	d.mu.Unlock()
	return found, ok && d.enabled()
}

// source returns the debrid source of hash:index. Other files of a registered torrent (a
// subtitle file next to the video) are sources too, found by their place in TorBox's list.
func (d *tsDebrid) source(hash string, index int) (tsDebridSource, bool) {
	if d == nil {
		return tsDebridSource{}, false
	}
	hash = strings.ToLower(hash)
	if src, ok := d.registered(hash, index); ok {
		return src, d.enabled()
	}
	reg, ok := d.hashSource(hash)
	if !ok {
		return tsDebridSource{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	it := d.item(ctx, hash)
	if it == nil || index < 1 || index > len(it.Files) {
		return tsDebridSource{}, false
	}
	f := it.Files[index-1]
	return tsDebridSource{hash: hash, index: index, path: f.Name, size: f.Size, title: reg.title, magnet: reg.magnet}, true
}

// link returns a download link of a debrid source, cached until tsDebridLinkFresh; force
// asks for a new one (the old one was refused). One request per file at a time: it runs on
// its own timeout and each caller waits on its own ctx, so a caller giving up pauses nothing.
func (d *tsDebrid) link(ctx context.Context, hash string, index int, force bool) (*tsDebridLink, error) {
	src, ok := d.source(hash, index)
	if !ok {
		return nil, errDebridOff
	}
	k := tsDebridKey(src.hash, index)
	d.mu.Lock()
	if l := d.links[k]; l != nil && !force && time.Since(l.at) < tsDebridLinkFresh {
		d.mu.Unlock()
		return l, nil
	}
	call := d.calls[k]
	if call == nil {
		if !time.Now().After(d.downUntil) {
			why := d.downWhy
			d.mu.Unlock()
			return nil, fmt.Errorf("%w (%s): %w", errDebridUnavailable, why, errDebridPaused)
		}
		call = &tsDebridCall{done: make(chan struct{})}
		d.calls[k] = call
		go d.runLink(k, src, call)
	}
	d.mu.Unlock()
	select {
	case <-call.done:
		if call.err != nil {
			return nil, fmt.Errorf("%w: %w", errDebridUnavailable, call.err)
		}
		return call.link, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runLink is the shared link request of k; only its own failure pauses TorBox.
func (d *tsDebrid) runLink(k string, src tsDebridSource, call *tsDebridCall) {
	ctx, cancel := context.WithTimeout(context.Background(), tsDebridFetchTimeout)
	defer cancel()
	l, err := d.fetchLink(ctx, src)
	d.mu.Lock()
	call.link, call.err = l, err
	delete(d.calls, k)
	if err == nil {
		d.links[k] = l
	}
	d.mu.Unlock()
	close(call.done)
	d.fail(err)
}

func (d *tsDebrid) fetchLink(ctx context.Context, src tsDebridSource) (*tsDebridLink, error) {
	cl := d.client()
	for attempt := 0; attempt < 2; attempt++ {
		id, err := d.torrentID(ctx, cl, src, attempt > 0)
		if err != nil {
			return nil, err
		}
		t, err := d.readyTorrent(ctx, cl, id)
		var apiErr *torbox.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && attempt == 0 {
			// Removed from the account since: look it up (or add it) again
			d.mu.Lock()
			delete(d.torrents, src.hash)
			d.mu.Unlock()
			continue
		}
		if err != nil {
			return nil, err
		}
		fileID, ok := tsDebridMatchFile(t.Files, src, d.cachedNames(src.hash))
		if !ok {
			return nil, fmt.Errorf("file %q (%d bytes) not in the TorBox torrent: %w", path.Base(src.path), src.size, errDebridTorrent)
		}
		u, err := cl.RequestDL(ctx, id, fileID)
		if err != nil {
			return nil, err
		}
		return &tsDebridLink{url: u, torrentID: id, fileID: fileID, at: time.Now()}, nil
	}
	return nil, fmt.Errorf("torrent not found in the TorBox account: %w", errDebridTorrent)
}

// cachedNames is the file list of hash from checkcached (index = position + 1).
func (d *tsDebrid) cachedNames(hash string) []torbox.CachedFile {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.checks[hash]; ok && c.item != nil {
		return c.item.Files
	}
	return nil
}

// torrentID finds the torrent in the account or adds it (TorBox has it cached, so adding
// downloads nothing; without "add uncached" TorBox is told to refuse an uncached one).
func (d *tsDebrid) torrentID(ctx context.Context, cl *torbox.Client, src tsDebridSource, fresh bool) (int64, error) {
	d.mu.Lock()
	ref, ok := d.torrents[src.hash]
	d.mu.Unlock()
	if ok && !fresh {
		return ref.id, nil
	}
	id, err := cl.FindTorrent(ctx, src.hash, fresh)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		magnet := src.magnet
		if !strings.HasPrefix(magnet, "magnet:?") {
			magnet = tsBuildMagnet(src.hash, src.title, nil)
		}
		var cerr error
		id, cerr = cl.CreateTorrent(ctx, magnet, !d.settings().AddUncached)
		if cerr != nil {
			// It may be there already (TorBox refuses duplicates): its list, fresh
			if id, err = cl.FindTorrent(ctx, src.hash, true); err != nil || id == 0 {
				return 0, cerr
			}
		}
	}
	d.rememberTorrent(src.hash, id)
	return id, nil
}

// rememberTorrent keeps hash -> TorBox torrent id (found in the list or just created).
func (d *tsDebrid) rememberTorrent(hash string, id int64) {
	d.mu.Lock()
	d.torrents[strings.ToLower(hash)] = tsDebridTorrentRef{id: id, at: time.Now()}
	d.mu.Unlock()
}

// readyTorrent reads the torrent until its files are listed (a cached torrent just added
// may need a moment).
func (d *tsDebrid) readyTorrent(ctx context.Context, cl *torbox.Client, id int64) (*torbox.Torrent, error) {
	for try := 0; ; try++ {
		t, err := cl.Torrent(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(t.Files) > 0 && t.Ready() {
			return t, nil
		}
		if try >= 4 {
			return nil, fmt.Errorf("torrent %d not ready on TorBox (%s): %w", id, t.DownloadState, errDebridTorrent)
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func tsDebridNormPath(p string) string {
	return strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "/"))
}

// tsDebridMatchFile finds the source's file among the TorBox torrent's files: by path and
// size, by a path suffix and size (one side may lack the torrent folder), by base name and
// size, by the checkcached list position of the index, then by a unique size.
func tsDebridMatchFile(files []torbox.File, src tsDebridSource, cached []torbox.CachedFile) (int64, bool) {
	want := tsDebridNormPath(src.path)
	base := path.Base(want)
	sized := func(f torbox.File) bool { return src.size <= 0 || f.Size == src.size }
	for _, f := range files {
		if tsDebridNormPath(f.Name) == want && sized(f) {
			return f.ID, true
		}
	}
	for _, f := range files {
		n := tsDebridNormPath(f.Name)
		if (strings.HasSuffix(n, "/"+want) || strings.HasSuffix(want, "/"+n)) && sized(f) {
			return f.ID, true
		}
	}
	for _, f := range files {
		if (path.Base(tsDebridNormPath(f.Name)) == base || strings.EqualFold(f.ShortName, base)) && sized(f) {
			return f.ID, true
		}
	}
	if src.index >= 1 && src.index <= len(cached) {
		c := cached[src.index-1]
		for _, f := range files {
			if tsDebridNormPath(f.Name) == tsDebridNormPath(c.Name) && f.Size == c.Size {
				return f.ID, true
			}
		}
	}
	if src.size > 0 {
		var id int64
		n := 0
		for _, f := range files {
			if f.Size == src.size {
				id, n = f.ID, n+1
			}
		}
		if n == 1 {
			return id, true
		}
	}
	return 0, false
}

// debridURL is fileURL for a debrid source: the loopback proxy once a link works, else
// TorrServer (the same file found by path and size). ok is false for other files.
func (s *Service) debridURL(hash string, index int) (string, bool) {
	d := s.debrid
	src, ok := d.source(hash, index)
	if !ok {
		// TorBox off: a registered source's index is still TorBox's list position
		if src, ok = d.registered(hash, index); !ok {
			return "", false
		}
		return fmt.Sprintf("%s/stream?link=%s&index=%d&play", s.torrServerURL(), src.hash, d.torrServerIndex(src)), true
	}
	ctx, cancel := context.WithTimeout(context.Background(), tsDebridLinkTimeout)
	defer cancel()
	// A file whose CDN reads gave up a moment ago plays from the swarm (the player restarts on it)
	if !d.isBroken(src.hash, index) {
		if _, err := d.link(ctx, src.hash, index, false); err == nil {
			if base, err := d.proxy(); err == nil {
				return fmt.Sprintf("%s/f/%s/%d", base, src.hash, index), true
			}
		} else if !errors.Is(err, errDebridPaused) {
			log.Printf("torbox: %s:%d plays from the swarm: %v", tsShortHash(src.hash), index, err)
		}
	}
	return fmt.Sprintf("%s/stream?link=%s&index=%d&play", s.torrServerURL(), src.hash, d.torrServerIndex(src)), true
}

// torrServerIndex finds a debrid source's file in TorrServer (fallback): TorBox's list
// order need not be the torrent's. -1 when TorrServer cannot tell (TorBox's index could
// play the wrong file of a pack); that answer is kept for tsDebridRemapRetry.
func (d *tsDebrid) torrServerIndex(src tsDebridSource) int {
	k := tsDebridKey(src.hash, src.index)
	d.mu.Lock()
	idx, ok := d.remaps[k]
	failedAt, failed := d.noRemap[k]
	d.mu.Unlock()
	if ok {
		return idx
	}
	if failed && time.Since(failedAt) < tsDebridRemapRetry {
		return -1
	}
	id, err := d.findInTorrServer(src)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.noRemap[k] = time.Now()
		log.Printf("torbox: %s:%d not found in TorrServer: %v", tsShortHash(src.hash), src.index, err)
		return -1
	}
	delete(d.noRemap, k)
	d.remaps[k] = id
	return id
}

// findInTorrServer is TorrServer's file id of a debrid source, matched by path and size.
func (d *tsDebrid) findInTorrServer(src tsDebridSource) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	link := src.magnet
	if !strings.HasPrefix(link, "magnet:?") {
		link = src.hash
	}
	statURL := d.s.torrServerURL() + "/stream?" + url.Values{"link": {link}, "stat": {""}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("stat: HTTP %d", resp.StatusCode)
	}
	var st tsTorrentStat
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, fmt.Errorf("stat: %w", err)
	}
	files := make([]torbox.File, 0, len(st.FileStats))
	for _, f := range st.FileStats {
		files = append(files, torbox.File{ID: int64(f.ID), Name: f.Path, Size: f.Length})
	}
	id, found := tsDebridMatchFile(files, tsDebridSource{path: src.path, size: src.size}, nil)
	if !found {
		return 0, fmt.Errorf("no file %q of %d bytes", path.Base(src.path), src.size)
	}
	return int(id), nil
}

// debridStat answers TorrServer's stat for a debrid source from what is known (name and
// files), so pings, name lookups and scans never add it to TorrServer. ok is false for
// other torrents.
func (s *Service) debridStat(link string, out any) (bool, error) {
	hash := strings.ToLower(link)
	if strings.HasPrefix(link, "magnet:") {
		hash = tsHashFromMagnet(link)
	}
	src, ok := s.debrid.hashSource(hash)
	if !ok {
		return false, nil
	}
	st := tsTorrentStat{Hash: src.hash, Name: src.title}
	s.debrid.mu.Lock()
	if t := s.debrid.sources[src.hash]; t != nil {
		for _, f := range t.files {
			st.FileStats = append(st.FileStats, tsFileStat{ID: f.index, Path: f.path, Length: f.size})
		}
	}
	s.debrid.mu.Unlock()
	sort.Slice(st.FileStats, func(i, j int) bool { return st.FileStats[i].ID < st.FileStats[j].ID })
	b, err := json.Marshal(st)
	if err != nil {
		return true, err
	}
	return true, json.Unmarshal(b, out)
}

// debridOwns: res is a debrid source, so TorrServer must not load it (no DB save, no
// re-warm through the swarm). Registers it (a source from the cache after a restart).
func (s *Service) debridOwns(res *tsResolveResult) bool {
	if res == nil || !res.Debrid {
		return false
	}
	s.debrid.register(tsDebridSource{hash: strings.ToLower(res.Hash), index: res.Index, path: res.FileName,
		size: res.Size, title: res.TorrentTitle, magnet: res.Magnet})
	return s.debrid.enabled()
}

// debridRewarm is rewarm for a debrid source: the link and the header (probe cache) are
// fetched ahead of the play.
func (s *Service) debridRewarm(w *tsWarm, res *tsResolveResult) {
	s.setSource(w, res.Hash)
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, 40*time.Second)
		defer cancel()
		if _, err := s.debrid.link(ctx, res.Hash, res.Index, false); err != nil {
			log.Printf("warm: torbox link of %s: %v", res.TorrentTitle, err)
			return
		}
		if _, err := s.probeFile(ctx, res.Hash, res.Index); err != nil {
			log.Printf("warm: probe of %s: %v", res.TorrentTitle, err)
		}
	}()
}

// debridAddUncached sends a picked torrent source to TorBox when "add uncached" is on, so
// a later play is cached (at most tsDebridAddPerHour an hour, once per torrent).
func (s *Service) debridAddUncached(res tsResolveResult) {
	d := s.debrid
	if res.Debrid || !strings.HasPrefix(res.Magnet, "magnet:?") || !d.usable() || !d.settings().AddUncached {
		return
	}
	hash := strings.ToLower(res.Hash)
	now := time.Now()
	d.mu.Lock()
	_, added := d.added[hash]
	if c, ok := d.checks[hash]; added || (ok && c.item != nil) {
		d.mu.Unlock()
		return
	}
	recent := d.addTimes[:0]
	for _, t := range d.addTimes {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	d.addTimes = recent
	if len(recent) >= tsDebridAddPerHour {
		d.mu.Unlock()
		return
	}
	d.added[hash] = now
	d.addTimes = append(d.addTimes, now)
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		id, err := d.client().CreateTorrent(ctx, res.Magnet, false)
		if err != nil {
			d.fail(err)
			log.Printf("torbox: adding %q: %v", res.TorrentTitle, err)
			return
		}
		d.rememberTorrent(hash, id)
		log.Printf("torbox: added %q (not cached yet)", res.TorrentTitle)
	}()
}

// DebridFile is a file of a debrid torrent (index as in hash/index).
type DebridFile struct {
	ID     int
	Path   string
	Length int64
}

// DebridFiles lists the files of a debrid source's torrent (subtitle files next to the video).
func (s *Service) DebridFiles(hash string) ([]DebridFile, bool) {
	s.adoptSaved(strings.ToLower(hash))
	src, ok := s.debrid.hashSource(hash)
	if !ok {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	it := s.debrid.item(ctx, src.hash)
	if it == nil {
		return nil, false
	}
	out := make([]DebridFile, 0, len(it.Files))
	for i, f := range it.Files {
		out = append(out, DebridFile{ID: i + 1, Path: f.Name, Length: f.Size})
	}
	return out, true
}

// DebridFileURL is the URL the subtitle reads use for a debrid file ("" for other files).
func (s *Service) DebridFileURL(hash string, index int) string {
	hash = strings.ToLower(hash)
	s.adoptSaved(hash)
	u, ok := s.debridURL(hash, index)
	if !ok {
		return ""
	}
	return u
}
