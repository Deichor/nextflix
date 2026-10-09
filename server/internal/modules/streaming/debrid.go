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
	tsDebridSettingsFresh = 10 * time.Second
	tsDebridAddPerHour    = 20 // "add uncached" torrents sent to TorBox per hour

	tsSourceTorBox = "torbox" // tsResolveResult.Source of a debrid source
)

var (
	errDebridOff         = errors.New("torbox is off")
	errDebridUnavailable = errors.New("torbox unavailable")
	errDebridPaused      = errors.New("torbox paused after a failure")
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
	checks    map[string]tsDebridCheck  // hash -> checkcached answer
	sources   map[string]tsDebridSource // "hash:index" -> registered debrid source
	torrents  map[string]int64          // hash -> TorBox torrent id
	links     map[string]*tsDebridLink  // "hash:index" -> download link
	calls     map[string]*tsDebridCall  // running link requests
	remaps    map[string]int            // "hash:index" -> TorrServer file id (fallback)
	broken    map[string]time.Time      // "hash:index" -> when its CDN reads gave up (plays from TorrServer for a while)
	warmed    map[string]time.Time      // "hash:index" -> last keep-alive read
	downUntil time.Time
	downWhy   string
	added     map[string]bool // hashes sent with "add uncached"
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
	return &tsDebrid{
		s: s, base: base, hc: torbox.NewHTTPClient(),
		checks: map[string]tsDebridCheck{}, sources: map[string]tsDebridSource{}, torrents: map[string]int64{},
		links: map[string]*tsDebridLink{}, calls: map[string]*tsDebridCall{}, remaps: map[string]int{},
		broken: map[string]time.Time{}, warmed: map[string]time.Time{},
		added: map[string]bool{}, adoptTried: map[string]time.Time{},
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
	d.torrents = map[string]int64{}
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

// fail pauses TorBox after a failure that will not go away at once, so resolve and play take
// the torrent path without waiting for it. Malformed answers do not pause it.
func (d *tsDebrid) fail(err error) {
	if err == nil || errors.Is(err, torbox.ErrMalformed) || errors.Is(err, context.Canceled) {
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
	d.mu.Lock()
	d.sources[tsDebridKey(src.hash, src.index)] = src
	d.mu.Unlock()
}

func (d *tsDebrid) unregister(hash string, index int) {
	k := tsDebridKey(hash, index)
	d.mu.Lock()
	delete(d.sources, k)
	delete(d.links, k)
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
	for k, src := range d.sources {
		if src.hash == hash {
			delete(d.sources, k)
			delete(d.links, k)
		}
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

// hashSource reports whether hash is a debrid source (any file) while TorBox is enabled.
func (d *tsDebrid) hashSource(hash string) (tsDebridSource, bool) {
	if d == nil {
		return tsDebridSource{}, false
	}
	hash = strings.ToLower(hash)
	d.mu.Lock()
	var found tsDebridSource
	ok := false
	for _, src := range d.sources {
		if src.hash == hash {
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
	d.mu.Lock()
	src, ok := d.sources[tsDebridKey(hash, index)]
	d.mu.Unlock()
	if ok {
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
// asks for a new one (the old one was refused). One request per file at a time.
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
	if call := d.calls[k]; call != nil {
		d.mu.Unlock()
		select {
		case <-call.done:
			return call.link, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !time.Now().After(d.downUntil) {
		why := d.downWhy
		d.mu.Unlock()
		return nil, fmt.Errorf("%w (%s): %w", errDebridUnavailable, why, errDebridPaused)
	}
	call := &tsDebridCall{done: make(chan struct{})}
	d.calls[k] = call
	d.mu.Unlock()

	call.link, call.err = d.fetchLink(ctx, src)
	d.mu.Lock()
	delete(d.calls, k)
	if call.err == nil {
		d.links[k] = call.link
	}
	d.mu.Unlock()
	close(call.done)
	if call.err != nil {
		d.fail(call.err)
		return nil, fmt.Errorf("%w: %v", errDebridUnavailable, call.err)
	}
	return call.link, nil
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
			return nil, fmt.Errorf("file %q (%d bytes) not in the TorBox torrent", path.Base(src.path), src.size)
		}
		u, err := cl.RequestDL(ctx, id, fileID)
		if err != nil {
			return nil, err
		}
		return &tsDebridLink{url: u, torrentID: id, fileID: fileID, at: time.Now()}, nil
	}
	return nil, errors.New("torrent not found in the TorBox account")
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
	id, ok := d.torrents[src.hash]
	d.mu.Unlock()
	if ok && !fresh {
		return id, nil
	}
	find := func(bypass bool) (int64, error) {
		list, err := cl.MyList(ctx, bypass)
		if err != nil {
			return 0, err
		}
		for _, t := range list {
			if t.Hash == src.hash {
				return t.ID, nil
			}
		}
		return 0, nil
	}
	id, err := find(fresh)
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
			if id, err = find(true); err != nil || id == 0 {
				return 0, cerr
			}
		}
	}
	d.mu.Lock()
	d.torrents[src.hash] = id
	d.mu.Unlock()
	return id, nil
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
			return nil, fmt.Errorf("torrent %d not ready on TorBox (%s)", id, t.DownloadState)
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
		return "", false
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
// order need not be the torrent's.
func (d *tsDebrid) torrServerIndex(src tsDebridSource) int {
	k := tsDebridKey(src.hash, src.index)
	d.mu.Lock()
	idx, ok := d.remaps[k]
	d.mu.Unlock()
	if ok {
		return idx
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	link := src.magnet
	if !strings.HasPrefix(link, "magnet:?") {
		link = src.hash
	}
	statURL := d.s.torrServerURL() + "/stream?" + url.Values{"link": {link}, "stat": {""}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statURL, nil)
	if err != nil {
		return src.index
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return src.index
	}
	defer resp.Body.Close()
	var st tsTorrentStat
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil {
		return src.index
	}
	files := make([]torbox.File, 0, len(st.FileStats))
	for _, f := range st.FileStats {
		files = append(files, torbox.File{ID: int64(f.ID), Name: f.Path, Size: f.Length})
	}
	id, found := tsDebridMatchFile(files, tsDebridSource{path: src.path, size: src.size}, nil)
	if !found {
		return src.index
	}
	d.mu.Lock()
	d.remaps[k] = int(id)
	d.mu.Unlock()
	return int(id)
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
	for _, f := range s.debrid.sources {
		if f.hash == src.hash {
			st.FileStats = append(st.FileStats, tsFileStat{ID: f.index, Path: f.path, Length: f.size})
		}
	}
	s.debrid.mu.Unlock()
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
	if c, ok := d.checks[hash]; d.added[hash] || (ok && c.item != nil) {
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
	d.added[hash] = true
	d.addTimes = append(d.addTimes, now)
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := d.client().CreateTorrent(ctx, res.Magnet, false); err != nil {
			d.fail(err)
			log.Printf("torbox: adding %q: %v", res.TorrentTitle, err)
			return
		}
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
