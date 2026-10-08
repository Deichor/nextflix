package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	thumbHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	thumbHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	thumbHashC = "cccccccccccccccccccccccccccccccccccccccc"
)

var fakeJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g', 0xFF, 0xD9}

// fakeFFmpeg counts runs and the most that ran at once (per torrent and overall).
type fakeFFmpeg struct {
	mu      sync.Mutex
	runs    []string // joined args
	running map[string]int
	maxHash int
	active  int
	maxAll  int
	delay   time.Duration
	gate    chan struct{} // when set, a run waits for it
	sawDone atomic.Int32  // runs that ended through ctx
}

func newFakeFFmpeg() *fakeFFmpeg { return &fakeFFmpeg{running: map[string]int{}} }

func (f *fakeFFmpeg) run(ctx context.Context, args []string) ([]byte, error) {
	src := ""
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			src = args[i+1]
		}
	}
	f.mu.Lock()
	f.runs = append(f.runs, strings.Join(args, " "))
	f.running[src]++
	f.active++
	f.maxHash = max(f.maxHash, f.running[src])
	f.maxAll = max(f.maxAll, f.active)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running[src]--
		f.active--
		f.mu.Unlock()
	}()
	var wait <-chan time.Time
	if f.delay > 0 {
		wait = time.After(f.delay)
	}
	if f.gate != nil || wait != nil {
		select {
		case <-f.gate:
		case <-wait:
		case <-ctx.Done():
			f.sawDone.Add(1)
			return nil, ctx.Err()
		}
	}
	return fakeJPEG, nil
}

func (f *fakeFFmpeg) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

func (f *fakeFFmpeg) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) == 0 {
		return ""
	}
	return f.runs[len(f.runs)-1]
}

func newThumbTest(t *testing.T) (*Service, *thumbs, *fakeFFmpeg, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := NewService(nil, Config{TorrServerURL: "http://ts"})
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		return &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "h264"}}}, nil
	}
	th := newThumbs(s, t.TempDir())
	f := newFakeFFmpeg()
	th.run = f.run
	r := gin.New()
	api := r.Group("/api")
	api.GET("/torrent-stream/thumb", th.get)
	api.POST("/torrent-stream/thumb/prefetch", th.prefetch)
	return s, th, f, r
}

func thumbGet(r http.Handler, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/torrent-stream/thumb?"+query, nil))
	return w
}

// playFlowing marks hash as a running, flowing play stream that started at.
func playFlowing(s *Service, hash string, at time.Time) {
	s.playBegin(hash)
	s.playFlows(hash)
	s.warmMu.Lock()
	s.playStart[hash] = at
	s.warmMu.Unlock()
}

func TestThumbGridAndWidth(t *testing.T) {
	for in, want := range map[float64]int{0: 0, 9.99: 0, 10: 10, 125.4: 120, 3599.9: 3590} {
		if got := thumbGridT(in); got != want {
			t.Errorf("grid(%v) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[int]int{64: 160, 160: 160, 200: 240, 320: 320, 400: 480, 1280: 640} {
		if got := thumbWidth(in); got != want {
			t.Errorf("width(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestThumbValidation(t *testing.T) {
	s, _, f, r := newThumbTest(t)
	s.probeCache.Store(thumbHashA+":1", &tsProbe{Format: struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	}{"matroska", "600"}})
	for _, q := range []string{
		"hash=xyz&index=1&t=10",
		"hash=" + thumbHashA + "&index=-1&t=10",
		"hash=" + thumbHashA + "&t=10",
		"hash=" + thumbHashA + "&index=1",
		"hash=" + thumbHashA + "&index=1&t=-5",
		"hash=" + thumbHashA + "&index=1&t=NaN",
		"hash=" + thumbHashA + "&index=1&t=abc",
		"hash=" + thumbHashA + "&index=1&t=90000",
		"hash=" + thumbHashA + "&index=1&t=10&w=20",
		"hash=" + thumbHashA + "&index=1&t=10&w=4000",
		"hash=" + thumbHashA + "&index=1&t=601", // past the probed end
	} {
		if w := thumbGet(r, q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, w.Code)
		}
	}
	if f.count() != 0 {
		t.Fatalf("ffmpeg ran %d times for invalid requests", f.count())
	}
	// The real route is registered
	gin.SetMode(gin.TestMode)
	t.Setenv("THUMB_CACHE_DIR", t.TempDir())
	rr := gin.New()
	Register(rr.Group("/api"), NewService(nil, Config{}))
	if w := thumbGet(rr, "hash=bad&index=0&t=0"); w.Code != http.StatusBadRequest {
		t.Fatalf("registered route: status %d, want 400", w.Code)
	}
}

func TestThumbCachesOnDiskOnGrid(t *testing.T) {
	_, th, f, r := newThumbTest(t)
	w := thumbGet(r, "hash="+strings.ToUpper(thumbHashA)+"&index=1&t=125.4")
	if w.Code != http.StatusOK || w.Header().Get("X-Cache") != "MISS" || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("first: %d %v", w.Code, w.Header())
	}
	if w.Header().Get("X-Thumb-T") != "120" {
		t.Fatalf("X-Thumb-T = %q, want 120", w.Header().Get("X-Thumb-T"))
	}
	args := f.last()
	for _, want := range []string{"-noaccurate_seek -ss 120 -i http://ts/stream?link=" + thumbHashA + "&index=1&play",
		"-fps_mode passthrough -frames:v 1", "scale=320:", "-f mjpeg pipe:1"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "tonemap") {
		t.Errorf("SDR source tone-mapped: %s", args)
	}
	if _, err := os.Stat(filepath.Join(th.dir, thumbHashA, "1", "120_320.jpg")); err != nil {
		t.Fatalf("not on disk: %v", err)
	}
	// Same grid point (and a width that rounds to 320): served from the disk cache
	w = thumbGet(r, "hash="+thumbHashA+"&index=1&t=129&w=300")
	if w.Code != http.StatusOK || w.Header().Get("X-Cache") != "HIT" || f.count() != 1 {
		t.Fatalf("second: %d %s, runs %d", w.Code, w.Header().Get("X-Cache"), f.count())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("cache-control %q", w.Header().Get("Cache-Control"))
	}
	// t=0 does not seek
	thumbGet(r, "hash="+thumbHashA+"&index=1&t=3")
	if strings.Contains(f.last(), "-ss") {
		t.Fatalf("t=0 seeks: %s", f.last())
	}
}

func TestThumbHDRIsToneMapped(t *testing.T) {
	s, _, f, r := newThumbTest(t)
	s.probeCache.Store(thumbHashA+":0", &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084"}}})
	if w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=30&w=640"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if a := f.last(); !strings.Contains(a, "zscale=tin=smpte2084") || !strings.Contains(a, "tonemap=hable") || !strings.Contains(a, "scale=640:") {
		t.Fatalf("args %s", a)
	}
}

func TestThumbDedupesInFlight(t *testing.T) {
	_, _, f, r := newThumbTest(t)
	f.gate = make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = thumbGet(r, "hash="+thumbHashA+"&index=1&t=50").Code
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d: %d", i, c)
		}
	}
	if f.count() != 1 {
		t.Fatalf("ffmpeg ran %d times, want 1", f.count())
	}
}

func TestThumbLimitsConcurrency(t *testing.T) {
	_, _, f, r := newThumbTest(t)
	f.delay = 80 * time.Millisecond
	var wg sync.WaitGroup
	for _, h := range []string{thumbHashA, thumbHashB, thumbHashC} {
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(h string, i int) {
				defer wg.Done()
				if w := thumbGet(r, "hash="+h+"&index=0&t="+[]string{"10", "20", "30"}[i]); w.Code != http.StatusOK {
					t.Errorf("%s t=%d: %d", h[:1], i, w.Code)
				}
			}(h, i)
		}
	}
	wg.Wait()
	if f.count() != 9 {
		t.Fatalf("runs %d, want 9", f.count())
	}
	if f.maxHash != 1 || f.maxAll != tsThumbParallel {
		t.Fatalf("max per torrent %d (want 1), overall %d (want %d)", f.maxHash, f.maxAll, tsThumbParallel)
	}
}

func TestThumbGateProtectsStreams(t *testing.T) {
	s, _, f, r := newThumbTest(t)
	// Another torrent plays: no read of this one
	playFlowing(s, thumbHashB, time.Now().Add(-time.Minute))
	w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=10")
	if w.Code != http.StatusNoContent || w.Header().Get("Retry-After") == "" || f.count() != 0 {
		t.Fatalf("other plays: %d %q runs %d", w.Code, w.Header().Get("X-Thumb-Wait"), f.count())
	}
	// The playing torrent itself is fine
	if w := thumbGet(r, "hash="+thumbHashB+"&index=0&t=10"); w.Code != http.StatusOK {
		t.Fatalf("playing torrent: %d", w.Code)
	}
	// Its stream restarts after a seek and has not sent bytes yet: wait
	s.playEnd(thumbHashB)
	s.playBegin(thumbHashB)
	if w := thumbGet(r, "hash="+thumbHashB+"&index=0&t=20"); w.Code != http.StatusNoContent {
		t.Fatalf("starting stream: %d", w.Code)
	}
	// A cached frame is always served
	if w := thumbGet(r, "hash="+thumbHashB+"&index=0&t=15"); w.Code != http.StatusOK || w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("cached frame while starting: %d", w.Code)
	}
	// A player probe of the torrent: its stream is about to start
	s.playFlows(thumbHashB)
	s.probeBegin(thumbHashB)
	if w := thumbGet(r, "hash="+thumbHashB+"&index=0&t=40"); w.Code != http.StatusNoContent {
		t.Fatalf("probing: %d", w.Code)
	}
	s.probeEnd(thumbHashB)
	// Nothing plays: any torrent may be read
	s.playEnd(thumbHashB)
	if w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=10"); w.Code != http.StatusOK {
		t.Fatalf("idle: %d", w.Code)
	}
}

func TestThumbGuardsTheTorrentWhileReading(t *testing.T) {
	s, _, f, r := newThumbTest(t)
	f.gate = make(chan struct{})
	done := make(chan int)
	go func() { done <- thumbGet(r, "hash="+thumbHashA+"&index=0&t=10").Code }()
	time.Sleep(50 * time.Millisecond)
	if s.idle(thumbHashA) {
		t.Fatal("a torrent ffmpeg reads for a frame may be dropped")
	}
	close(f.gate)
	<-done
	if !s.idle(thumbHashA) {
		t.Fatal("still guarded after the frame")
	}
}

func TestThumbSlowSwarmAnswersQuicklyAndFinishes(t *testing.T) {
	_, th, f, r := newThumbTest(t)
	th.wait = 60 * time.Millisecond
	f.delay = 200 * time.Millisecond
	start := time.Now()
	w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=70")
	if w.Code != http.StatusGatewayTimeout || time.Since(start) > 150*time.Millisecond || w.Header().Get("Retry-After") == "" {
		t.Fatalf("slow: %d after %s", w.Code, time.Since(start))
	}
	// The job goes on without its viewer, so the retry is a cache hit
	time.Sleep(250 * time.Millisecond)
	if w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=70"); w.Code != http.StatusOK || w.Header().Get("X-Cache") != "HIT" || f.count() != 1 {
		t.Fatalf("retry: %d %s runs %d", w.Code, w.Header().Get("X-Cache"), f.count())
	}
	// A job that never finishes in time is stopped at jobMax
	th.jobMax = 100 * time.Millisecond
	f.delay = time.Second
	thumbGet(r, "hash="+thumbHashA+"&index=0&t=80")
	time.Sleep(200 * time.Millisecond)
	if f.sawDone.Load() != 1 {
		t.Fatalf("job not stopped at jobMax")
	}
}

func TestThumbViewerLeavingCancelsTheJob(t *testing.T) {
	_, th, f, _ := newThumbTest(t)
	f.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error)
	go func() {
		_, err := th.frame(ctx, thumbKey{hash: thumbHashA, t: 10, w: 320}, false, time.Second)
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("no error after cancel")
	}
	time.Sleep(50 * time.Millisecond)
	if f.sawDone.Load() != 1 {
		t.Fatal("ffmpeg kept running for nobody")
	}
	th.mu.Lock()
	jobs, slots := len(th.jobs), len(th.perHash)
	th.mu.Unlock()
	if jobs != 0 || slots != 0 {
		t.Fatalf("left %d jobs, %d torrent slots", jobs, slots)
	}
}

func TestThumbFailureIsRemembered(t *testing.T) {
	_, th, _, r := newThumbTest(t)
	runs := 0
	th.run = func(ctx context.Context, args []string) ([]byte, error) {
		runs++
		return nil, errThumbNoFrame
	}
	if w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=7000"); w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
	if w := thumbGet(r, "hash="+thumbHashA+"&index=0&t=7005"); w.Code != http.StatusNotFound || runs != 1 {
		t.Fatalf("status %d runs %d", w.Code, runs)
	}
}

func TestThumbCacheEvictsLeastRecentlyUsed(t *testing.T) {
	_, th, _, _ := newThumbTest(t)
	big := append(append([]byte{}, fakeJPEG[:4]...), make([]byte, 1000)...)
	th.max = 3500
	old := time.Now().Add(-3 * time.Hour)
	for i := 0; i < 3; i++ {
		k := thumbKey{hash: thumbHashA, index: 0, t: i * 10, w: 320}
		th.store(k, big)
		_ = os.Chtimes(k.path(th.dir), old.Add(time.Duration(i)*time.Minute), old.Add(time.Duration(i)*time.Minute))
	}
	// Reading the oldest makes it recent
	if _, ok := th.cached(thumbKey{hash: thumbHashA, index: 0, t: 0, w: 320}); !ok {
		t.Fatal("miss")
	}
	th.store(thumbKey{hash: thumbHashB, index: 2, t: 0, w: 320}, big) // over the cap
	deadline := time.Now().Add(time.Second)
	for th.clean.Load() || time.Now().Before(deadline) && th.size.Load() > th.max {
		time.Sleep(10 * time.Millisecond)
	}
	if th.size.Load() > th.max*9/10 {
		t.Fatalf("size %d after cleanup", th.size.Load())
	}
	if !th.hasCached(thumbKey{hash: thumbHashA, index: 0, t: 0, w: 320}) {
		t.Fatal("the recently read frame was removed")
	}
	if th.hasCached(thumbKey{hash: thumbHashA, index: 0, t: 10, w: 320}) {
		t.Fatal("the least recently used frame was kept")
	}
}

func TestThumbPrefetchOnlyBehindASteadyStream(t *testing.T) {
	s, th, f, r := newThumbTest(t)
	post := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/thumb/prefetch?"+q, nil))
		return w
	}
	if w := post("hash=" + thumbHashA + "&index=0"); w.Code != http.StatusBadRequest {
		t.Fatalf("no t: %d", w.Code)
	}
	if w := post("hash=" + thumbHashA + "&index=0&t=" + strings.Repeat("1,", tsThumbPrefetchMax) + "1"); w.Code != http.StatusBadRequest {
		t.Fatalf("too many: %d", w.Code)
	}
	if w := post("hash=" + thumbHashA + "&index=0&t=10,x"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad time: %d", w.Code)
	}
	// Not playing: nothing is read
	if w := post("hash=" + thumbHashA + "&index=0&t=10,20"); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"queued":0`) {
		t.Fatalf("idle: %d %s", w.Code, w.Body.String())
	}
	// A stream that just started: not yet
	playFlowing(s, thumbHashA, time.Now())
	if w := post("hash=" + thumbHashA + "&index=0&t=10,20"); !strings.Contains(w.Body.String(), `"queued":0`) {
		t.Fatalf("young stream: %s", w.Body.String())
	}
	// A steady stream: queued, one at a time, on the grid
	s.warmMu.Lock()
	s.playStart[thumbHashA] = time.Now().Add(-time.Minute)
	s.warmMu.Unlock()
	thumbGet(r, "hash="+thumbHashA+"&index=0&t=20") // already cached
	w := post("hash=" + thumbHashA + "&index=0&t=11,15,21,35")
	if !strings.Contains(w.Body.String(), `"queued":2`) || !strings.Contains(w.Body.String(), `"cached":1`) {
		t.Fatalf("steady: %s", w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !(th.hasCached(thumbKey{hash: thumbHashA, t: 10, w: 320}) && th.hasCached(thumbKey{hash: thumbHashA, t: 30, w: 320})) {
		time.Sleep(10 * time.Millisecond)
	}
	if !th.hasCached(thumbKey{hash: thumbHashA, t: 10, w: 320}) || !th.hasCached(thumbKey{hash: thumbHashA, t: 30, w: 320}) {
		t.Fatal("prefetched frames missing")
	}
	if f.count() != 3 || f.maxAll != 1 {
		t.Fatalf("runs %d (want 3), at once %d", f.count(), f.maxAll)
	}
}
