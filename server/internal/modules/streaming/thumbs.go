package streaming

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Trick play thumbnails: GET /thumb returns the keyframe at or before ?t= of a torrent file as a
// small JPEG. Times snap to a grid so frames are cached on disk and shared by every client.
// Frames only read pieces when that cannot slow a stream down: the playing torrent is fine
// (its viewer seeks there next), any torrent while nothing else plays; a torrent whose stream
// is starting, or another torrent while one plays, answers 204 at once. POST /thumb/prefetch
// queues frames around the playing position (background: one at a time, never next to a
// viewer's request, only while that torrent's stream runs steadily).

const (
	tsThumbGrid        = 10               // seconds between cached frames
	tsThumbMaxT        = 24 * 3600        // highest accepted ?t=
	tsThumbWait        = 6 * time.Second  // a request waits this long for its frame
	tsThumbJobMax      = 15 * time.Second // a frame whose request timed out may finish within this
	tsThumbFailMemory  = time.Minute      // a frame ffmpeg could not make is not tried again for this long
	tsThumbParallel    = 2                // ffmpeg runs at once, server-wide (one per torrent)
	tsThumbCacheMax    = 500 << 20        // disk cache cap
	tsThumbPrefetchMax = 12               // times per prefetch call
	tsThumbQueueMax    = 24               // queued prefetch frames (oldest dropped)
	tsThumbQueueAge    = time.Minute      // a queued frame older than this is dropped
	tsThumbIdlePoll    = 200 * time.Millisecond
)

// tsThumbWidths are the widths frames are made in; a request gets the next larger one.
var tsThumbWidths = []int{160, 240, 320, 480, 640}

var (
	errThumbSlow    = errors.New("frame not ready in time")
	errThumbNoFrame = errors.New("no frame at this time")
)

// thumbRunner runs ffmpeg with args and returns its stdout (tests replace it).
type thumbRunner func(ctx context.Context, args []string) ([]byte, error)

type thumbKey struct {
	hash     string
	index, t int
	w        int
}

func (k thumbKey) id() string { return fmt.Sprintf("%s:%d:%d:%d", k.hash, k.index, k.t, k.w) }

func (k thumbKey) path(dir string) string {
	return filepath.Join(dir, k.hash, strconv.Itoa(k.index), fmt.Sprintf("%d_%d.jpg", k.t, k.w))
}

type thumbJob struct {
	done     chan struct{}
	cancel   context.CancelFunc
	img      []byte
	err      error
	waiters  int
	keep     bool // a request timed out: finish it anyway, so the next one is a cache hit
	finished bool
}

type thumbQueued struct {
	k  thumbKey
	at time.Time
}

type thumbs struct {
	s       *Service
	dir     string
	max     int64
	run     thumbRunner
	wait    time.Duration
	jobMax  time.Duration
	global  chan struct{}
	sizeOne sync.Once
	size    atomic.Int64
	clean   atomic.Bool

	mu          sync.Mutex
	jobs        map[string]*thumbJob
	perHash     map[string]chan struct{} // one running frame per torrent
	perHashRefs map[string]int
	failed      map[string]time.Time
	userWaiters int
	newest      map[string]int // "hash:index" -> t of the latest viewer request
	queue       []thumbQueued
	queueSig    chan struct{}
	workerOnce  sync.Once
}

func newThumbs(s *Service, dir string) *thumbs {
	return &thumbs{
		s: s, dir: dir, max: tsThumbCacheMax, run: runThumbFFmpeg,
		wait: tsThumbWait, jobMax: tsThumbJobMax,
		global:      make(chan struct{}, tsThumbParallel),
		jobs:        map[string]*thumbJob{},
		perHash:     map[string]chan struct{}{},
		perHashRefs: map[string]int{},
		failed:      map[string]time.Time{},
		newest:      map[string]int{},
		queueSig:    make(chan struct{}, 1),
	}
}

// thumbCacheDir is THUMB_CACHE_DIR, else /app/data/thumbs (the db volume).
func thumbCacheDir() string {
	if d := strings.TrimSpace(os.Getenv("THUMB_CACHE_DIR")); d != "" {
		return d
	}
	return "/app/data/thumbs"
}

func thumbGridT(t float64) int { return int(t/tsThumbGrid) * tsThumbGrid }

func thumbWidth(w int) int {
	for _, a := range tsThumbWidths {
		if w <= a {
			return a
		}
	}
	return tsThumbWidths[len(tsThumbWidths)-1]
}

// thumbTime reads one ?t= value (seconds, 0..tsThumbMaxT).
func thumbTime(v string) (float64, bool) {
	t, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > tsThumbMaxT {
		return 0, false
	}
	return t, true
}

// thumbWidthParam reads ?w= (default 320, 64..1280, rounded up to a cached width).
func thumbWidthParam(c *gin.Context) (int, bool) {
	v := c.Query("w")
	if v == "" {
		return 320, true
	}
	w, err := strconv.Atoi(v)
	if err != nil || w < 64 || w > 1280 {
		return 0, false
	}
	return thumbWidth(w), true
}

// thumbGate says why the pieces of hash may not be read for a frame now ("" = go).
// background is prefetch: only while that torrent's stream runs steadily.
func (s *Service) thumbGate(hash string, background bool) string {
	now := time.Now()
	s.warmMu.Lock()
	why := func() string {
		if s.starting[hash] > 0 {
			return "its stream is about to start"
		}
		if s.playing[hash] > 0 {
			switch {
			case !s.playFlowing[hash]:
				return "its stream starts"
			case background && now.Sub(s.playStart[hash]) < tsStartQuiet:
				return "its stream started recently"
			}
			return ""
		}
		if background {
			return "it does not play"
		}
		for h, n := range s.playing {
			if n > 0 && h != hash {
				return "another torrent plays"
			}
		}
		for h, n := range s.starting {
			if n > 0 && h != hash {
				return "another torrent starts"
			}
		}
		return ""
	}()
	s.warmMu.Unlock()
	if why == "" && background {
		if other := s.startingStream(hash); other != "" {
			return "another torrent starts"
		}
	}
	return why
}

// get serves GET /torrent-stream/thumb?hash=&index=&t=&w=.
func (th *thumbs) get(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	t, ok := thumbTime(c.Query("t"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("t must be a time in seconds (0-%d)", tsThumbMaxT)})
		return
	}
	w, ok := thumbWidthParam(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "w must be 64-1280"})
		return
	}
	if d := th.knownDuration(hash, index); d > 0 && t > d {
		c.JSON(http.StatusBadRequest, gin.H{"error": "t is past the end of the file"})
		return
	}
	k := thumbKey{hash: hash, index: index, t: thumbGridT(t), w: w}
	c.Header("X-Thumb-T", strconv.Itoa(k.t))
	if img, ok := th.cached(k); ok {
		th.serve(c, img, "HIT")
		return
	}
	if th.failedRecently(k) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusNotFound, gin.H{"error": "no frame at this time"})
		return
	}
	if why := th.s.thumbGate(hash, false); why != "" {
		c.Header("Cache-Control", "no-store")
		c.Header("Retry-After", "2")
		c.Header("X-Thumb-Wait", why)
		c.Status(http.StatusNoContent)
		return
	}
	th.s.touch(hash)
	img, err := th.frame(c.Request.Context(), k, false, th.wait)
	switch {
	case err == nil:
		th.serve(c, img, "MISS")
	case c.Request.Context().Err() != nil:
		c.Status(499) // the viewer moved on
	case errors.Is(err, errThumbSlow) || errors.Is(err, context.DeadlineExceeded):
		c.Header("Cache-Control", "no-store")
		c.Header("Retry-After", "2")
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "the swarm is too slow for this frame now"})
	case errors.Is(err, errThumbNoFrame):
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusNotFound, gin.H{"error": "no frame at this time"})
	default:
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not make the frame"})
	}
}

func (th *thumbs) serve(c *gin.Context, img []byte, cache string) {
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Cache", cache)
	c.Data(http.StatusOK, "image/jpeg", img)
}

// prefetch serves POST /torrent-stream/thumb/prefetch?hash=&index=&t=a,b,c&w=: frames the
// player will likely scrub to (around the position, inside what is already loaded).
func (th *thumbs) prefetch(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	w, ok := thumbWidthParam(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "w must be 64-1280"})
		return
	}
	parts := strings.Split(c.Query("t"), ",")
	if len(parts) == 0 || len(parts) > tsThumbPrefetchMax || c.Query("t") == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("t must list 1-%d times", tsThumbPrefetchMax)})
		return
	}
	dur := th.knownDuration(hash, index)
	var keys []thumbKey
	seen := map[int]bool{}
	for _, p := range parts {
		t, ok := thumbTime(p)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid time in t"})
			return
		}
		if dur > 0 && t > dur {
			continue
		}
		g := thumbGridT(t)
		if !seen[g] {
			seen[g] = true
			keys = append(keys, thumbKey{hash: hash, index: index, t: g, w: w})
		}
	}
	if why := th.s.thumbGate(hash, true); why != "" {
		c.JSON(http.StatusAccepted, gin.H{"queued": 0, "skipped": why})
		return
	}
	queued, cached := 0, 0
	for _, k := range keys {
		if th.hasCached(k) {
			cached++
			continue
		}
		if th.enqueue(k) {
			queued++
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": queued, "cached": cached})
}

// knownDuration is the file length from a cached probe (0 = unknown; never probes).
func (th *thumbs) knownDuration(hash string, index int) float64 {
	if p, ok := th.s.probeCache.Load(fmt.Sprintf("%s:%d", hash, index)); ok {
		return p.(*tsProbe).duration()
	}
	return 0
}

// frame returns the frame of k, joining a running job for it or starting one. It waits up to
// wait. A job whose last waiter aborted its request is cancelled only when the viewer's latest
// request is more than 2 grid cells away (the scrub moved on); a frame next to it finishes and
// is cached, since the web player keeps a few requests in flight while the cursor moves.
func (th *thumbs) frame(ctx context.Context, k thumbKey, background bool, wait time.Duration) ([]byte, error) {
	th.mu.Lock()
	file := k.hash + ":" + strconv.Itoa(k.index)
	if !background {
		th.newest[file] = k.t
	}
	j := th.jobs[k.id()]
	if j == nil {
		jctx, cancel := context.WithTimeout(context.Background(), th.jobMax)
		j = &thumbJob{done: make(chan struct{}), cancel: cancel}
		th.jobs[k.id()] = j
		go th.runJob(jctx, k, j)
	}
	j.waiters++
	if !background {
		th.userWaiters++
	}
	th.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	var img []byte
	var err error
	timedOut := false
	select {
	case <-j.done:
		img, err = j.img, j.err
	case <-timer.C:
		err, timedOut = errThumbSlow, true
	case <-ctx.Done():
		err = ctx.Err()
	}
	th.mu.Lock()
	j.waiters--
	if !background {
		th.userWaiters--
	}
	if timedOut {
		j.keep = true
	}
	if j.waiters == 0 && !j.keep && !j.finished && ctx.Err() != nil {
		if d := k.t - th.newest[file]; d > 2*tsThumbGrid || d < -2*tsThumbGrid {
			j.cancel()
		}
	}
	th.mu.Unlock()
	return img, err
}

func (th *thumbs) runJob(ctx context.Context, k thumbKey, j *thumbJob) {
	defer func() {
		th.mu.Lock()
		delete(th.jobs, k.id())
		j.finished = true
		th.mu.Unlock()
		j.cancel()
		close(j.done)
	}()
	release, err := th.slots(ctx, k.hash)
	if err != nil {
		j.err = err
		return
	}
	defer release()
	if img, ok := th.cached(k); ok {
		j.img = img
		return
	}
	// The stream guard keeps the torrent loaded while ffmpeg reads it
	th.s.seekBegin(k.hash)
	defer th.s.seekEnd(k.hash)
	tonemap, small := th.videoInfo(ctx, k)
	img, err := th.run(ctx, thumbArgs(th.s.fileURL(k.hash, k.index), k, tonemap, small))
	if err == nil && !isJPEG(img) {
		err = errThumbNoFrame
	}
	if err != nil {
		if ctx.Err() == nil {
			th.mu.Lock()
			th.failed[k.id()] = time.Now()
			th.mu.Unlock()
			log.Printf("thumb %s:%d at %ds: %v", tsShortHash(k.hash), k.index, k.t, err)
		}
		j.err = err
		return
	}
	j.img = img
	th.store(k, img)
}

// slots takes the torrent's slot, then a server-wide one.
func (th *thumbs) slots(ctx context.Context, hash string) (func(), error) {
	th.mu.Lock()
	ch := th.perHash[hash]
	if ch == nil {
		ch = make(chan struct{}, 1)
		th.perHash[hash] = ch
	}
	th.perHashRefs[hash]++
	th.mu.Unlock()
	unref := func() {
		th.mu.Lock()
		if th.perHashRefs[hash]--; th.perHashRefs[hash] <= 0 {
			delete(th.perHashRefs, hash)
			delete(th.perHash, hash)
		}
		th.mu.Unlock()
	}
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		unref()
		return nil, ctx.Err()
	}
	select {
	case th.global <- struct{}{}:
	case <-ctx.Done():
		<-ch
		unref()
		return nil, ctx.Err()
	}
	return func() {
		<-th.global
		<-ch
		unref()
	}, nil
}

// videoInfo reads the HDR transfer and whether the container has an index from the probe
// (cached by the player; otherwise a short probe, and without one the frame is made anyway).
func (th *thumbs) videoInfo(ctx context.Context, k thumbKey) (tonemap string, small bool) {
	p, ok := th.s.probeCache.Load(fmt.Sprintf("%s:%d", k.hash, k.index))
	if !ok {
		pr, err := th.s.probeFileTimeout(ctx, k.hash, k.index, 3*time.Second)
		if err != nil {
			return "", false
		}
		p = pr
	}
	pr := p.(*tsProbe)
	if v := pr.video(); v != nil {
		tonemap = v.hdrTransfer()
	}
	return tonemap, pr.indexedContainer()
}

// thumbArgs: seek to the keyframe at or before t and write its frame as one scaled JPEG.
func thumbArgs(src string, k thumbKey, tonemap string, small bool) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-reconnect", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "2"}
	if small {
		args = append(args, "-probesize", "2000000", "-analyzeduration", "2000000")
	}
	// One decoder thread: frame threads would read several frames before the first comes out.
	// No -skip_frame nokey: H.264 counts only IDR frames as key, and files whose keyframes are
	// plain I-frames then read 10-30 MB to the next IDR (about 0.5 MB without it).
	args = append(args, "-threads", "1")
	if k.t > 0 {
		args = append(args, "-noaccurate_seek", "-ss", strconv.Itoa(k.t))
	}
	vf := fmt.Sprintf("scale=%d:trunc(ow/dar/2)*2,setsar=1", k.w)
	if tonemap != "" {
		// HDR to SDR like the re-encoded play stream (play.go)
		vf += ",zscale=tin=" + tonemap + ":min=bt2020nc:pin=bt2020:t=linear:npl=100,format=gbrpf32le," +
			"zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv"
	}
	vf += ",format=yuvj420p"
	return append(args, "-i", src, "-map", "0:v:0", "-an", "-sn", "-dn",
		"-fps_mode", "passthrough", "-frames:v", "1", "-vf", vf, "-c:v", "mjpeg", "-q:v", "5", "-f", "mjpeg", "pipe:1")
}

func runThumbFFmpeg(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var out bytes.Buffer
	var stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return nil, fmt.Errorf("ffmpeg: %w %s", err, msg)
	}
	if out.Len() == 0 {
		return nil, errThumbNoFrame
	}
	return out.Bytes(), nil
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 }

func (th *thumbs) failedRecently(k thumbKey) bool {
	th.mu.Lock()
	defer th.mu.Unlock()
	at, ok := th.failed[k.id()]
	if ok && time.Since(at) >= tsThumbFailMemory {
		delete(th.failed, k.id())
		return false
	}
	return ok
}

// ---- Disk cache: <dir>/<hash>/<index>/<t>_<w>.jpg, least recently used files go first ----

func (th *thumbs) hasCached(k thumbKey) bool {
	_, err := os.Stat(k.path(th.dir))
	return err == nil
}

func (th *thumbs) cached(k thumbKey) ([]byte, bool) {
	p := k.path(th.dir)
	img, err := os.ReadFile(p)
	if err != nil || !isJPEG(img) {
		return nil, false
	}
	// mtime is the LRU clock; refreshed at most once an hour
	if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > time.Hour {
		now := time.Now()
		_ = os.Chtimes(p, now, now)
	}
	return img, true
}

func (th *thumbs) store(k thumbKey, img []byte) {
	th.sizeOne.Do(func() { th.size.Store(th.diskUsage()) })
	p := k.path(th.dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		log.Printf("thumb cache: %v", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		log.Printf("thumb cache: %v", err)
		return
	}
	_, werr := tmp.Write(img)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if th.size.Add(int64(len(img))) > th.max && th.clean.CompareAndSwap(false, true) {
		go func() {
			defer th.clean.Store(false)
			th.cleanup()
		}()
	}
}

func (th *thumbs) diskUsage() int64 {
	var n int64
	_ = filepath.WalkDir(th.dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// cleanup deletes the least recently used frames down to 90 % of the cap.
func (th *thumbs) cleanup() {
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	_ = filepath.WalkDir(th.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				files = append(files, file{p, info.Size(), info.ModTime()})
				total += info.Size()
			}
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	target := th.max * 9 / 10
	removed := 0
	for _, f := range files {
		if total <= target {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
			removed++
			// Empty <index> and <hash> folders go too
			dir := filepath.Dir(f.path)
			for i := 0; i < 2 && dir != th.dir; i++ {
				if os.Remove(dir) != nil {
					break
				}
				dir = filepath.Dir(dir)
			}
		}
	}
	th.size.Store(total)
	if removed > 0 {
		log.Printf("thumb cache: removed %d frames, %d MB left", removed, total>>20)
	}
}

// ---- Prefetch: one background frame at a time, never next to a viewer's request ----

func (th *thumbs) enqueue(k thumbKey) bool {
	th.workerOnce.Do(func() { go th.worker() })
	th.mu.Lock()
	for _, q := range th.queue {
		if q.k == k {
			th.mu.Unlock()
			return false
		}
	}
	if _, running := th.jobs[k.id()]; running {
		th.mu.Unlock()
		return false
	}
	th.queue = append(th.queue, thumbQueued{k, time.Now()})
	if len(th.queue) > tsThumbQueueMax {
		th.queue = th.queue[len(th.queue)-tsThumbQueueMax:]
	}
	th.mu.Unlock()
	select {
	case th.queueSig <- struct{}{}:
	default:
	}
	return true
}

func (th *thumbs) next() (thumbKey, bool) {
	th.mu.Lock()
	defer th.mu.Unlock()
	for len(th.queue) > 0 {
		q := th.queue[0]
		th.queue = th.queue[1:]
		if time.Since(q.at) < tsThumbQueueAge {
			return q.k, true
		}
	}
	return thumbKey{}, false
}

func (th *thumbs) worker() {
	for range th.queueSig {
		for {
			k, ok := th.next()
			if !ok {
				break
			}
			th.prefetchOne(k)
		}
	}
}

func (th *thumbs) prefetchOne(k thumbKey) {
	if th.hasCached(k) || th.failedRecently(k) {
		return
	}
	// A viewer's frames come first
	for waited := time.Duration(0); ; waited += tsThumbIdlePoll {
		th.mu.Lock()
		busy := th.userWaiters > 0
		th.mu.Unlock()
		if !busy {
			break
		}
		if waited > 10*time.Second {
			return
		}
		time.Sleep(tsThumbIdlePoll)
	}
	if th.s.thumbGate(k.hash, true) != "" {
		return
	}
	_, _ = th.frame(context.Background(), k, true, th.jobMax)
}
