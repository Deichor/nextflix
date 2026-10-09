package streaming

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/torbox"
)

// Loopback file proxy of debrid sources (see docs/ARCHITECTURE.md, Debrid). One CDN connection
// gives ~18 MB/s and every new range costs 0.2-0.7 s before its first byte, so the file is read
// in chunks: a reader's position starts a chunk of tsProxyFirst bytes, a reader that keeps going
// gets the next chunks fetched ahead of it in parallel (tsProxyAhead, tsProxyChunk each), and
// chunks stay cached (LRU, tsProxyCacheMax) so the header, the index at the end and the cues that
// ffmpeg reads before every seek, and the data around a paused position, never go out again.
// Fetches refresh a refused link (401/403/404/410) and resume a reset, cut or stalled read at
// the next byte, so ffmpeg / ffprobe see one unbroken file.

const (
	tsProxyFirst     = 4 << 20         // first chunk at a new position
	tsProxyChunkSize = 8 << 20         // chunks fetched ahead
	tsProxyAhead     = 3               // chunks fetched ahead of a sequential reader
	tsProxySeq       = 1 << 20         // read in one request: the reader is sequential, one chunk ahead
	tsProxySeqFull   = 4 << 20         // read in one request: all tsProxyAhead chunks ahead
	tsProxyCacheMax  = 256 << 20       // cached chunk bytes over all files
	tsProxyStall     = 5 * time.Second // a CDN read without bytes this long is resumed
	tsProxyFirstByte = 4 * time.Second // a CDN answer without headers this long is tried again
	tsProxyFetchMax  = 3 * time.Minute // one chunk fetch at most
)

type tsProxyChunk struct {
	start, end int64 // file bytes [start, end)
	buf        []byte
	n          int  // bytes in buf (from start)
	failed     bool // the fetch gave up; buf[:n] stays valid
	wake       chan struct{}
	readers    int
	used       time.Time
}

func (c *tsProxyChunk) done() bool { return c.failed || c.start+int64(c.n) >= c.end }

type tsProxyFile struct {
	hash   string
	index  int
	size   int64 // 0 until known
	ctype  string
	chunks []*tsProxyChunk // sorted by start, never overlapping
}

type tsProxyCache struct {
	mu    sync.Mutex
	files map[string]*tsProxyFile
	bytes int64
}

// proxy starts the loopback file proxy once and returns its base URL.
func (d *tsDebrid) proxy() (string, error) {
	d.proxyOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			d.proxyErr = err
			log.Printf("torbox: file proxy: %v", err)
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(d.serveFile), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		d.proxyBase = "http://" + ln.Addr().String()
	})
	return d.proxyBase, d.proxyErr
}

// proxyFile returns the cache entry of hash:index (size from the registered source when known).
func (d *tsDebrid) proxyFile(hash string, index int) *tsProxyFile {
	k := tsDebridKey(hash, index)
	pc := &d.cache
	pc.mu.Lock()
	f := pc.files[k]
	pc.mu.Unlock()
	if f != nil {
		return f
	}
	var size int64
	if src, ok := d.source(hash, index); ok {
		size = src.size
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.files == nil {
		pc.files = map[string]*tsProxyFile{}
	}
	if f = pc.files[k]; f == nil {
		f = &tsProxyFile{hash: hash, index: index, size: size}
		pc.files[k] = f
	}
	return f
}

// chunkAtLocked returns the chunk that holds (or is fetching) byte pos, starting a fetch of
// up to want bytes when there is none.
func (d *tsDebrid) chunkAtLocked(f *tsProxyFile, pos, want int64) *tsProxyChunk {
	i := sort.Search(len(f.chunks), func(i int) bool { return f.chunks[i].end > pos })
	if i < len(f.chunks) {
		c := f.chunks[i]
		if c.start <= pos && (!c.failed || pos < c.start+int64(c.n)) {
			return c
		}
		if c.start <= pos { // a failed fetch: keep what it got, fetch the rest anew
			c.end = c.start + int64(c.n)
			if c.n == 0 {
				d.dropChunkLocked(f, c)
			}
			return d.chunkAtLocked(f, pos, want)
		}
	}
	end := pos + want
	if f.size > 0 && end > f.size {
		end = f.size
	}
	if i < len(f.chunks) && f.chunks[i].start < end {
		end = f.chunks[i].start
	}
	if end <= pos {
		end = pos + 1 // unknown size and nothing cached: the fetch finds out
	}
	c := &tsProxyChunk{start: pos, end: end, wake: make(chan struct{}), used: time.Now()}
	d.makeRoomLocked(end - pos)
	// Eviction may have removed chunks of this file: find the place again
	i = sort.Search(len(f.chunks), func(i int) bool { return f.chunks[i].start > pos })
	c.buf = make([]byte, end-pos)
	d.cache.bytes += end - pos
	f.chunks = append(f.chunks, nil)
	copy(f.chunks[i+1:], f.chunks[i:])
	f.chunks[i] = c
	go d.fetchChunk(f, c)
	return c
}

// prefetchLocked makes sure the n chunks after from are cached or on their way (up to last).
func (d *tsDebrid) prefetchLocked(f *tsProxyFile, from, last int64, n int) {
	pos := from
	for k := 0; k < n && pos <= last && (f.size == 0 || pos < f.size); k++ {
		c := d.chunkAtLocked(f, pos, tsProxyChunkSize)
		if c.failed {
			return
		}
		pos = c.end
	}
}

// makeRoomLocked evicts the least recently used finished chunks nobody reads until need more
// bytes fit under tsProxyCacheMax.
func (d *tsDebrid) makeRoomLocked(need int64) {
	pc := &d.cache
	for pc.bytes+need > tsProxyCacheMax {
		var victim *tsProxyChunk
		var owner *tsProxyFile
		for _, f := range pc.files {
			for _, c := range f.chunks {
				if c.readers == 0 && c.done() && (victim == nil || c.used.Before(victim.used)) {
					victim, owner = c, f
				}
			}
		}
		if victim == nil {
			return // everything is in use: allow more for now
		}
		d.dropChunkLocked(owner, victim)
	}
}

func (d *tsDebrid) dropChunkLocked(f *tsProxyFile, c *tsProxyChunk) {
	for i, x := range f.chunks {
		if x == c {
			f.chunks = append(f.chunks[:i], f.chunks[i+1:]...)
			d.cache.bytes -= int64(len(c.buf))
			break
		}
	}
}

// progressLocked wakes the readers of c.
func (c *tsProxyChunk) progressLocked() {
	close(c.wake)
	c.wake = make(chan struct{})
}

// fetchChunk fills c from TorBox: one ranged GET, refreshed and resumed as needed.
func (d *tsDebrid) fetchChunk(f *tsProxyFile, c *tsProxyChunk) {
	ctx, cancel := context.WithTimeout(context.Background(), tsProxyFetchMax)
	defer cancel()
	pc := &d.cache
	fail := func() {
		pc.mu.Lock()
		c.failed = true
		c.progressLocked()
		pc.mu.Unlock()
		d.markBroken(f.hash, f.index)
	}
	refreshes, fails := 0, 0
	force := false
	for {
		pc.mu.Lock()
		pos, end := c.start+int64(c.n), c.end
		pc.mu.Unlock()
		if pos >= end {
			return
		}
		if fails > 6 || refreshes > 2 || ctx.Err() != nil {
			fail()
			return
		}
		link, err := d.link(ctx, f.hash, f.index, force)
		force = false
		if err != nil {
			fail()
			return
		}
		// A CDN answer slower than tsProxyFirstByte is a stalled connection (DPI): try a new one
		actx, acancel := context.WithCancel(ctx)
		slow := time.AfterFunc(tsProxyFirstByte, acancel)
		req, err := http.NewRequestWithContext(actx, http.MethodGet, link.url, nil)
		if err != nil {
			slow.Stop()
			acancel()
			fail()
			return
		}
		req.Header.Set("User-Agent", torbox.UserAgent)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, end-1))
		if fails > 0 {
			req.Close = true // a new connection after a reset
		}
		resp, err := d.hc.Do(req)
		slow.Stop()
		if err != nil {
			acancel()
			fails++
			tsDebridBackoff(ctx, fails)
			continue
		}
		resp.Body = tsCancelBody{resp.Body, acancel} // the body is read on actx
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			resp.Body.Close()
			log.Printf("torbox: link of %s:%d refused (%d), requesting a new one", tsShortHash(f.hash), f.index, resp.StatusCode)
			refreshes++
			force = true
			continue
		case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
			// Past the end: the file is shorter than assumed
			if _, _, total, ok := tsParseContentRange(resp.Header.Get("Content-Range")); ok && total > 0 {
				pc.mu.Lock()
				f.size = total
				pc.mu.Unlock()
			}
			resp.Body.Close()
			fail()
			return
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			resp.Body.Close()
			fails++
			tsDebridBackoff(ctx, fails)
			continue
		case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent:
			resp.Body.Close()
			log.Printf("torbox: read of %s:%d answered %d", tsShortHash(f.hash), f.index, resp.StatusCode)
			fail()
			return
		}
		start, last, total, ok := tsContentRange(resp)
		if !ok || start != pos {
			// A 200 for a range from the middle, or another range than asked
			resp.Body.Close()
			fails++
			tsDebridBackoff(ctx, fails)
			continue
		}
		pc.mu.Lock()
		if total > 0 && total != f.size {
			f.size = total
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			f.ctype = ct
		}
		if last+1 < c.end {
			c.end = last + 1 // the file ends before the chunk did
		}
		c.progressLocked() // readers learn the size
		pc.mu.Unlock()
		got := d.readChunk(resp, c)
		resp.Body.Close()
		pc.mu.Lock()
		finished := c.start+int64(c.n) >= c.end
		pc.mu.Unlock()
		if finished {
			return
		}
		if got > 0 {
			fails = 0
		}
		fails++
		if ctx.Err() == nil {
			log.Printf("torbox: read of %s:%d cut at %d, resuming", tsShortHash(f.hash), f.index, c.start+int64(c.n))
		}
		tsDebridBackoff(ctx, fails)
	}
}

// readChunk copies an upstream body into c until c is full, the body ends or it stalls.
func (d *tsDebrid) readChunk(resp *http.Response, c *tsProxyChunk) int {
	pc := &d.cache
	stall := time.AfterFunc(tsProxyStall, func() { resp.Body.Close() })
	defer stall.Stop()
	got := 0
	pc.mu.Lock()
	n, size := c.n, int(c.end-c.start)
	pc.mu.Unlock()
	for n < size {
		// Only this goroutine writes buf[n:]; readers see bytes once n is published under the lock
		k, err := resp.Body.Read(c.buf[n:min(size, n+256<<10)])
		if k > 0 {
			stall.Reset(tsProxyStall)
			n += k
			got += k
			pc.mu.Lock()
			c.n = n
			c.progressLocked()
			pc.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	return got
}

// serveFile answers GET/HEAD /f/<hash>/<index> (single byte ranges) from the chunk cache.
func (d *tsDebrid) serveFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "f" || !tsHashPattern.MatchString(parts[1]) {
		http.NotFound(w, r)
		return
	}
	hash := strings.ToLower(parts[1])
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, ok := d.source(hash, index); !ok {
		http.Error(w, "TorBox file unavailable", http.StatusBadGateway)
		return
	}
	ctx := r.Context()
	f := d.proxyFile(hash, index)
	pc := &d.cache

	rangeHdr := r.Header.Get("Range")
	if strings.Contains(rangeHdr, ",") {
		rangeHdr = "" // multiple ranges: the whole file
	}
	first, last, suffix, ranged := tsParseRange(rangeHdr)
	if suffix > 0 && f.size == 0 {
		ranged, first, last = false, 0, -1 // a suffix of an unknown size: the whole file
	}

	// The first bytes decide the answer: 502 when TorBox cannot deliver anything
	pc.mu.Lock()
	if suffix > 0 {
		first, last = max(0, f.size-suffix), f.size-1
	}
	if f.size > 0 && first >= f.size {
		size := f.size
		pc.mu.Unlock()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	c := d.chunkAtLocked(f, first, tsProxyFirst)
	c.readers++
	pc.mu.Unlock()
	release := func() {
		pc.mu.Lock()
		c.readers--
		c.used = time.Now()
		pc.mu.Unlock()
	}
	for {
		pc.mu.Lock()
		avail, failed, size, wake := c.start+int64(c.n) > first, c.failed, f.size, c.wake
		pc.mu.Unlock()
		if avail || (size > 0 && first >= size) {
			break
		}
		if failed {
			release()
			http.Error(w, "TorBox file unavailable", http.StatusBadGateway)
			return
		}
		select {
		case <-wake:
		case <-ctx.Done():
			release()
			return
		}
	}
	pc.mu.Lock()
	size, ctype := f.size, f.ctype
	pc.mu.Unlock()
	if size > 0 && first >= size {
		release()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if last < 0 || last >= size {
		last = size - 1
	}
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(last-first+1, 10))
	if ranged {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		release()
		return
	}
	d.copyRange(ctx, w, f, c, first, last)
}

// copyRange writes bytes first..last from the cache, starting in chunk c (whose reader count
// it owns), fetching ahead once the reader proves sequential.
func (d *tsDebrid) copyRange(ctx context.Context, w http.ResponseWriter, f *tsProxyFile, c *tsProxyChunk, first, last int64) {
	pc := &d.cache
	flusher, _ := w.(http.Flusher)
	pos := first
	failedAt := int64(-1)
	defer func() {
		pc.mu.Lock()
		c.readers--
		c.used = time.Now()
		pc.mu.Unlock()
	}()
	for pos <= last {
		pc.mu.Lock()
		stuck := c.failed && pos >= c.start+int64(c.n) && pos < c.end
		if stuck && failedAt == pos {
			// TorBox gave up on this part twice: the reader reconnects (ffmpeg -reconnect)
			pc.mu.Unlock()
			return
		}
		if stuck || pos >= c.end {
			// On to the chunk that holds pos; a failed fetch is tried once more by a new chunk
			if stuck {
				failedAt = pos
			}
			c.readers--
			c.used = time.Now()
			want := int64(tsProxyChunkSize)
			if pos-first < tsProxySeq {
				want = tsProxyFirst
			}
			c = d.chunkAtLocked(f, pos, want)
			c.readers++
		}
		if read := pos - first; read >= tsProxySeq {
			ahead := 1
			if read >= tsProxySeqFull {
				ahead = tsProxyAhead
			}
			d.prefetchLocked(f, c.end, last, ahead)
		}
		hi := min(c.start+int64(c.n), last+1, c.end)
		failed, wake := c.failed, c.wake
		var data []byte
		if hi > pos {
			data = c.buf[pos-c.start : hi-c.start]
		}
		pc.mu.Unlock()
		if len(data) == 0 {
			if failed {
				continue
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
			continue
		}
		if _, err := w.Write(data); err != nil {
			return
		}
		pos += int64(len(data))
	}
}

// tsParseRange reads a single "bytes=a-b", "bytes=a-" or "bytes=-n" header. last is -1 when open.
func tsParseRange(h string) (first, last, suffix int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !found {
		return 0, -1, 0, false
	}
	a, b, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, -1, 0, false
	}
	if a == "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, -1, 0, false
		}
		return 0, -1, n, true
	}
	first, err := strconv.ParseInt(a, 10, 64)
	if err != nil || first < 0 {
		return 0, -1, 0, false
	}
	last = -1
	if b != "" {
		if last, err = strconv.ParseInt(b, 10, 64); err != nil || last < first {
			return 0, -1, 0, false
		}
	}
	return first, last, 0, true
}

// tsContentRange reads the byte range of an upstream answer: Content-Range of a 206, the
// whole body of a 200.
func tsContentRange(resp *http.Response) (start, last, total int64, ok bool) {
	if resp.StatusCode == http.StatusOK {
		if resp.ContentLength < 0 {
			return 0, 0, 0, false
		}
		return 0, resp.ContentLength - 1, resp.ContentLength, true
	}
	return tsParseContentRange(resp.Header.Get("Content-Range"))
}

// tsParseContentRange reads "bytes a-b/total" ("bytes */total" of a 416 gives only total).
func tsParseContentRange(cr string) (start, last, total int64, ok bool) {
	cr = strings.TrimSpace(cr)
	if t, found := strings.CutPrefix(cr, "bytes */"); found {
		n, err := strconv.ParseInt(t, 10, 64)
		return 0, -1, n, err == nil
	}
	var t string
	if _, err := fmt.Sscanf(cr, "bytes %d-%d/%s", &start, &last, &t); err != nil {
		return 0, 0, 0, false
	}
	total, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		total = last + 1 // "*": unknown size
	}
	return start, last, total, last >= start
}

func tsDebridBackoff(ctx context.Context, fails int) {
	d := time.Duration(min(fails, 5)) * 300 * time.Millisecond
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// tsCancelBody cancels the request's own context once its body is closed.
type tsCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b tsCancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

const (
	tsDebridBrokenFor = 2 * time.Minute  // a file whose CDN reads gave up plays from TorrServer this long
	tsDebridWarmEvery = 20 * time.Second // keep-alive reads of a paused player's file at most this often
)

// markBroken: TorBox could not deliver a part of the file (after its retries). Until
// tsDebridBrokenFor passed, fileURL sends new reads of it to TorrServer (same file).
func (d *tsDebrid) markBroken(hash string, index int) {
	if !d.usable() {
		return // paused or off: fileURL uses TorrServer anyway
	}
	k := tsDebridKey(hash, index)
	d.mu.Lock()
	was := !d.broken[k].IsZero() && time.Since(d.broken[k]) < tsDebridBrokenFor
	d.broken[k] = time.Now()
	d.mu.Unlock()
	if !was {
		log.Printf("torbox: %s:%d could not be read, playing it from the swarm for %s", tsShortHash(hash), index, tsDebridBrokenFor)
	}
}

func (d *tsDebrid) isBroken(hash string, index int) bool {
	k := tsDebridKey(hash, index)
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.broken[k]
	if ok && time.Since(at) >= tsDebridBrokenFor {
		delete(d.broken, k)
		return false
	}
	return ok
}

// keepWarm keeps a paused player's file ready: a fresh link and a live CDN connection (a
// one-byte read), so the restart after the pause does not pay for them. At most every
// tsDebridWarmEvery per file.
func (d *tsDebrid) keepWarm(hash string, index int) {
	if _, ok := d.source(hash, index); !ok || d.isBroken(hash, index) {
		return
	}
	k := tsDebridKey(hash, index)
	d.mu.Lock()
	if time.Since(d.warmed[k]) < tsDebridWarmEvery {
		d.mu.Unlock()
		return
	}
	d.warmed[k] = time.Now()
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		link, err := d.link(ctx, hash, index, false)
		if err != nil {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.url, nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", torbox.UserAgent)
		req.Header.Set("Range", "bytes=0-0")
		resp, err := d.hc.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
}
