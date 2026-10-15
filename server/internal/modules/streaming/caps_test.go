package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nextflix/internal/platform/torbox"
)

// The release name of the TorBox Big Buck Bunny file: no resolution tag, a 4K HEVC file.
const bbbUnlabeled = "big-buck-bunny-2008-4-k-60-fps-blender-animated-short-x-265"

var (
	bbb4K   = strings.Repeat("4", 40)
	bbb720  = strings.Repeat("7", 40)
	bbb1080 = strings.Repeat("1", 40)
)

// bbbFile is a fake torrent: its file size and what ffprobe reads from it.
type bbbFile struct {
	size          int64
	codec, pix    string
	width, height int
}

var bbbFiles = map[string]bbbFile{
	bbb4K:   {size: 7 * tsGB, codec: "hevc", pix: "yuv420p10le", width: 3840, height: 2160},
	bbb720:  {size: 800 * tsMB, codec: "h264", pix: "yuv420p", width: 1280, height: 720},
	bbb1080: {size: 1200 * tsMB, codec: "h264", pix: "yuv420p", width: 1920, height: 1080},
}

// newCapsService has a fake Jackett answering results and a fake TorrServer whose swarms
// deliver 4x what each file needs for a 10 minute runtime.
func newCapsService(t *testing.T, results []JackettResult) *Service {
	t.Helper()
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("t") == "indexers":
			w.Write([]byte(`<indexers>
<indexer id="tpb" configured="true"><caps><searching><movie-search available="yes" supportedParams="q" /></searching><categories><category id="2000"/></categories></caps></indexer>
</indexers>`))
		case strings.HasSuffix(r.URL.Path, "/results"):
			json.NewEncoder(w).Encode(JackettResponse{Results: results})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(jackett.Close)
	var open atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		open.Add(1)
		defer open.Add(-1)
		link := r.URL.Query().Get("link")
		hash := tsHashFromMagnet(link)
		if hash == "" {
			hash = strings.ToLower(link)
		}
		f, ok := bbbFiles[hash]
		if !ok {
			http.NotFound(w, r)
			return
		}
		rate := 4 * tsNeedRate(f.size, 10, "movie")
		if _, stat := r.URL.Query()["stat"]; stat {
			fmt.Fprintf(w, `{"hash":%q,"name":"Big Buck Bunny","file_stats":[{"id":1,"path":"Big Buck Bunny/bbb.mkv","length":%d}],`+
				`"download_speed":%d,"active_peers":5,"connected_seeders":4}`, hash, f.size, rate)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, rate/20)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if waitOr(r.Context(), 50*time.Millisecond) != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: jackett.URL, JackettAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{JackettURL: jackett.URL, JackettAPIKey: "k", TorrServerURL: ts.URL})
	quiesce(t, s, &open)
	s.dropFn = func(string) {}
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		f := bbbFiles[hash]
		p := &tsProbe{Streams: []tsProbeStream{
			{CodecType: "video", CodecName: f.codec, PixFmt: f.pix, Width: f.width, Height: f.height},
			{CodecType: "audio", CodecName: "aac", Channels: 2},
		}}
		p.Format.Duration = "596"
		return p, nil
	}
	return s
}

// quiesce stops the background work of s's resolves at the end of a test: its titles are
// released and the fake TorrServer is no longer read.
func quiesce(t *testing.T, s *Service, open *atomic.Int32) {
	t.Cleanup(func() {
		s.warmMu.Lock()
		var warms []*tsWarm
		for _, w := range s.warms {
			warms = append(warms, w)
		}
		s.warmMu.Unlock()
		for _, w := range warms {
			s.releaseWarm(w, false)
		}
		deadline := time.Now().Add(5 * time.Second)
		for open.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond) // a measurement ends at its next poll
	})
}

func bbbRelease(name, hash string) JackettResult {
	return JackettResult{Title: name, Seeders: 40, Size: bbbFiles[hash].size, MagnetUri: "magnet:?xt=urn:btih:" + hash}
}

func bbbQuery(hevc bool, maxQ int) tsResolveQuery {
	return tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny", year: 2008, runtime: 10, hevc: hevc, maxQ: maxQ}
}

func findBBB(t *testing.T, s *Service, q tsResolveQuery) *tsResolveResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, seed, err := s.findSource(ctx, q)
	if err != nil {
		t.Fatalf("findSource: %v", err)
	}
	seed.cancel()
	return res
}

// A name without a resolution tag ranks below every labeled fitting one for a capped client;
// an HEVC client without a cap ranks it as before.
func TestUnlabeledRanksBelowFittingForCappedClients(t *testing.T) {
	raw := []JackettResult{
		bbbRelease(bbbUnlabeled, bbb4K),
		{Title: "Big Buck Bunny 2008 DVDRip XviD", Seeders: 2, Size: 800 * tsMB, MagnetUri: "magnet:?xt=urn:btih:" + strings.Repeat("5", 40)},
	}
	for _, q := range []tsResolveQuery{bbbQuery(false, 720), bbbQuery(false, 0), bbbQuery(true, 1080)} {
		c := tsRankAll(raw, q)
		if len(c) != 2 || c[0].quality != 480 || c[1].parts.Res != tsUnknownResCapped {
			t.Fatalf("%s: got %q, res parts %v", tsResolveCaps(q), titles(c), c)
		}
	}
	if c := tsRankAll(raw, bbbQuery(true, 0)); c[0].raw.Title != bbbUnlabeled || c[0].parts.Res != 12 {
		t.Fatalf("hevc client: got %q", titles(c))
	}
}

// The exact case: the unlabeled 4K HEVC file and a labeled 720p H.264 one, for a 720p client
// without HEVC. The 4K one probes as 2160p HEVC (unfit) and the 720p one is picked.
func TestCapsSkipUnlabeled4KHEVC(t *testing.T) {
	s := newCapsService(t, []JackettResult{bbbRelease(bbbUnlabeled, bbb4K), bbbRelease("Big.Buck.Bunny.2008.720p.BluRay.x264", bbb720)})
	res := findBBB(t, s, bbbQuery(false, 720))
	if res.Hash != bbb720 || res.Quality != "720p" || res.CapsFallback != "" || res.unfit != "" {
		t.Fatalf("picked %q (%s), fallback %q, unfit %q", res.TorrentTitle, res.Quality, res.CapsFallback, res.unfit)
	}
	// Its probed facts break the caps of this client, not of an HEVC one
	p := s.cachedProbe(bbb4K, 1)
	if why := tsCapsMiss(p, bbbQuery(false, 720)); !strings.Contains(why, "2160p above the 720p cap") || !strings.Contains(why, "hevc") {
		t.Fatalf("caps miss %q", why)
	}
	if why := tsCapsMiss(p, bbbQuery(true, 0)); why != "" {
		t.Fatalf("hevc client: caps miss %q", why)
	}
}

// The extra checks after an unfit pick: unchecked names that may fit first, then the ones
// whose name breaks the caps (above maxq too), cheapest first.
func TestCapsChecksMoreBeforeFallingBack(t *testing.T) {
	q := bbbQuery(false, 720)
	more := tsFallbackCandidates(t, q, []JackettResult{bbbRelease(bbbUnlabeled, bbb4K), bbbRelease("Big.Buck.Bunny.2008.720p.BluRay.x264", bbb720),
		bbbRelease("Big.Buck.Bunny.2008.1080p.BluRay.x264", bbb1080)}, []string{bbb4K})
	if len(more) != 2 || more[0].hash != bbb720 || more[1].hash != bbb1080 {
		t.Fatalf("fallback candidates %q", titles(more))
	}
}

func tsFallbackCandidates(t *testing.T, q tsResolveQuery, raw []JackettResult, checked []string) []tsCandidate {
	t.Helper()
	var done []tsCandidate
	for _, h := range checked {
		done = append(done, tsCandidate{hash: h})
	}
	return (&Service{}).fallbackCandidates(q, raw, done, nil)
}

// An HEVC client without a cap gets the 4K file as before: it fits.
func TestCapsHEVCClientKeeps4K(t *testing.T) {
	s := newCapsService(t, []JackettResult{bbbRelease(bbbUnlabeled, bbb4K)})
	res := findBBB(t, s, bbbQuery(true, 0))
	if res.Hash != bbb4K || res.Quality != "2160p" || res.unfit != "" || res.CapsFallback != "" {
		t.Fatalf("picked %q (%s), unfit %q, fallback %q", res.TorrentTitle, res.Quality, res.unfit, res.CapsFallback)
	}
}

// Only unfit files: the 4K one is the fallback when nothing else exists; with a labeled 1080p
// H.264 one above the cap the cheaper one (1080p, copied) wins.
func TestCapsFallbackWhenNothingFits(t *testing.T) {
	s := newCapsService(t, []JackettResult{bbbRelease(bbbUnlabeled, bbb4K)})
	res := findBBB(t, s, bbbQuery(false, 720))
	if res.Hash != bbb4K || res.CapsFallback == "" || !res.overCaps {
		t.Fatalf("picked %q, fallback %q", res.TorrentTitle, res.CapsFallback)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"caps_fallback":"2160p above the 720p cap, hevc yuv420p10le video is re-encoded"`) {
		t.Fatalf("json %s", b)
	}

	s = newCapsService(t, []JackettResult{bbbRelease(bbbUnlabeled, bbb4K), bbbRelease("Big.Buck.Bunny.2008.1080p.BluRay.x264", bbb1080)})
	res = findBBB(t, s, bbbQuery(false, 720))
	if res.Hash != bbb1080 || res.CapsFallback != "1080p above the 720p cap" {
		t.Fatalf("picked %q, fallback %q", res.TorrentTitle, res.CapsFallback)
	}
}

// A cached source that breaks the client's caps is not reused (dropped and resolved again),
// unless it was stored as the fallback for these caps; fallbacks expire sooner.
func TestCachedSourceRespectsCaps(t *testing.T) {
	s := NewService(memDB(t), Config{TorrServerURL: "http://127.0.0.1:1"})
	s.dropFn = func(string) {}
	q := bbbQuery(false, 720)
	key := tsResolveKey(q)
	stale := tsResolveResult{Hash: bbb4K, Index: 1, Quality: "2160p", TorrentTitle: bbbUnlabeled, Format: &tsTags{Video: "hevc", Probed: true}}
	s.storeSource(key, stale)
	if _, ok := s.cachedFor(key, q); ok {
		t.Fatal("a 4K HEVC source was reused for a 720p client without HEVC")
	}
	if _, ok := s.cachedSource(key); ok {
		t.Fatal("the source was not dropped")
	}
	// Same file, but its resolution was not known when stored: the probe tells
	stale.Quality, stale.Format = "", nil
	s.storeSource(key, stale)
	s.probeCache.Store(bbb4K+":1", &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "hevc", PixFmt: "yuv420p", Width: 3840, Height: 2160}}})
	if _, ok := s.cachedFor(key, q); ok {
		t.Fatal("reused although the probe says 2160p HEVC")
	}
	// An HEVC client without a cap keeps it
	hq := bbbQuery(true, 0)
	s.storeSource(tsResolveKey(hq), stale)
	if _, ok := s.cachedFor(tsResolveKey(hq), hq); !ok {
		t.Fatal("the HEVC client lost its 4K source")
	}
	// The fallback chosen for these caps is reused, for tsFallbackTTL
	stale.CapsFallback = "2160p above the 720p cap"
	s.storeSource(key, stale)
	if _, ok := s.cachedFor(key, q); !ok {
		t.Fatal("the fallback was not reused")
	}
	if ttl := tsSourceTTLOf(&stale); ttl != tsFallbackTTL {
		t.Fatalf("fallback ttl %s", ttl)
	}
	// After a restart the row keeps its fallback mark
	s2 := NewService(s.db, Config{TorrServerURL: "http://127.0.0.1:1"})
	if r, ok := s2.cachedFor(key, q); !ok || r.CapsFallback == "" {
		t.Fatalf("after restart: %v %+v", ok, r)
	}
}

// Fallback choice: a fitting result beats an unfit one; among unfit ones that break the caps
// the good one, then the lowest resolution.
func TestChooseCheapestFallback(t *testing.T) {
	good := func(q string, unfit string, over bool) *tsResolveResult {
		return &tsResolveResult{Quality: q, unfit: unfit, overCaps: over, DurationVerified: true, durationChecked: true,
			Health: &tsHealth{Rate: 10, Need: 1, KeepsUp: true, ActivePeers: 3}}
	}
	done := []bool{true, true, true}
	r4k, r1080 := good("2160p", "2160p above the 720p cap", true), good("1080p", "1080p above the 720p cap", true)
	if best, final := tsChoose([]*tsResolveResult{r4k, r1080, nil}, done); best != 1 || !final {
		t.Fatalf("got %d, want the 1080p fallback", best)
	}
	fit := good("720p", "", false)
	fit.Health.KeepsUp = false
	if best, _ := tsChoose([]*tsResolveResult{r4k, r1080, fit}, done); best != 2 {
		t.Fatalf("got %d, want the fitting 720p", best)
	}
	starving := good("1080p", "1080p above the 720p cap", true)
	starving.Health = &tsHealth{Rate: 1, Need: 10, ActivePeers: 2}
	if best := tsChoosePlayable([]*tsResolveResult{starving, r4k}); best != 1 {
		t.Fatalf("playable: got %d, want the 4K that keeps up", best)
	}
	if !tsFallbackWins(r1080, r4k) || tsFallbackWins(r4k, r1080) || !tsFallbackWins(good("720p", "", false), r4k) {
		t.Fatal("fallback order")
	}
	// The live case: a dead, unprobed extra pick never replaces a fallback that delivers
	r4k.Health.KeepsUp = false
	dead := &tsResolveResult{Quality: "1080p", durationChecked: true, Health: &tsHealth{Need: 1, Dead: true}}
	if tsFallbackWins(dead, r4k) {
		t.Fatal("a dead swarm replaced the delivering 4K fallback")
	}
	if !tsFallbackWins(fit, r4k) {
		t.Fatal("a delivering fitting one did not replace an unfit one that starves")
	}
	// The live Night of the Living Dead case: a 1080p H.264 copy costs less than a 720p HEVC
	// 10-bit re-encode
	copy1080 := good("1080p", "1080p above the 720p cap", true)
	hevc720 := good("720p", "hevc yuv420p10le video is re-encoded", true)
	hevc720.reencode = true
	if best, _ := tsChoose([]*tsResolveResult{hevc720, copy1080, nil}, done); best != 1 || !tsCheaperFallback(copy1080, hevc720) {
		t.Fatalf("got %d, want the 1080p copy", best)
	}
}

// A good but unfit first result does not end the round with the short grace: a fitting check
// still running gets its time.
func TestUnfitFirstDoesNotCutFitChecks(t *testing.T) {
	ok := &tsHealth{Rate: 10, Need: 1, KeepsUp: true, ActivePeers: 3}
	i, res, _ := tsPickBest(context.Background(), 2, 50*time.Millisecond, func(ctx context.Context, i int) (*tsResolveResult, error) {
		if i == 0 {
			return &tsResolveResult{Quality: "1080p", unfit: "1080p above the 720p cap", overCaps: true, Health: ok}, nil
		}
		if err := waitOr(ctx, 400*time.Millisecond); err != nil {
			return nil, err
		}
		return &tsResolveResult{Quality: "720p", Health: ok}, nil
	})
	if i != 1 || res.unfit != "" {
		t.Fatalf("got %d (%+v), want the fitting 720p", i, res)
	}
}

// The TorBox gate does not stop other checks for a TorBox pick that breaks the caps.
func TestDebridGateIgnoresUnfitPick(t *testing.T) {
	cands := []tsCandidate{{hash: tbHash, quality: 0}, {hash: strings.Repeat("b", 40), quality: 720}}
	g := (&Service{}).newDebridGate(cands, map[string]*torbox.CachedItem{tbHash: {Hash: tbHash}})
	r := &tsResolveResult{Quality: "2160p", unfit: "2160p above the 720p cap", overCaps: true, DurationVerified: true, durationChecked: true,
		Health: &tsHealth{Rate: 10, Need: 1, KeepsUp: true}}
	g.finish(r, nil)
	if err := g.wait(context.Background(), 1); err != nil {
		t.Fatalf("lower check stopped: %v", err)
	}
}

// Detour (1945) on a browser: the x265 SARTRE release won at the cap before its probe told
// it is re-encoded (15-87 s probe, no frame in the slow-start window).
func TestNameUnfitLosesToH264(t *testing.T) {
	x265 := measured("sartre", "1080p", 0.9, 1.0)
	x265.nameUnfit = true
	h264 := measured("oft", "1080p", 0.3, 1.0)
	if i := tsChoosePlayable([]*tsResolveResult{x265, h264}); i != 1 {
		t.Fatalf("got %d, want the H.264 that delivers", i)
	}
	dead := measured("rarbg", "1080p", 0, 0.3)
	dead.Health.Dead = true
	if i := tsChoosePlayable([]*tsResolveResult{x265, dead}); i != 0 {
		t.Fatalf("got %d, want the x265 when nothing else delivers", i)
	}
	// Both keep up: the H.264 is picked although the x265 ranks first
	x265.Health, h264.Health = newHealth(3*tsMB, tsMB, 3, 2), newHealth(2*tsMB, tsMB, 3, 2)
	if i, final := tsChoose([]*tsResolveResult{x265, h264}, []bool{true, true}); i != 1 || !final {
		t.Fatalf("got %d (final %v), want the H.264", i, final)
	}
	if i, final := tsChoose([]*tsResolveResult{x265, nil}, []bool{true, false}); i != 0 || final {
		t.Fatalf("got %d (final %v), want the x265 only once nothing else is left", i, final)
	}
}

// A listing without a hash whose link a check found to be an avoided torrent ranks last.
func TestKnownAvoidedListing(t *testing.T) {
	s := NewService(nil, Config{})
	yts := JackettResult{Title: "Detour (1945) [BluRay] (1080p) [YTS AM]", Seeders: 21, Size: 1080 * tsMB, Link: "http://jackett/dl/yts"}
	oft := JackettResult{Title: "Detour 1945 1080p BluRay x264-OFT", Seeders: 4, Size: 2830 * tsMB,
		MagnetUri: "magnet:?xt=urn:btih:" + strings.Repeat("b", 40)}
	avoided := strings.Repeat("f", 40)
	s.listingHashes.Store(tsListingKey(yts), avoided)
	q := tsResolveQuery{mediaType: "movie", title: "Detour", year: 1945, runtime: 68}
	if raw := s.knownAvoided(q, []JackettResult{yts, oft}); raw[0].InfoHash != "" {
		t.Fatal("a listing that is not avoided lost its link")
	}
	q.avoid = []string{avoided}
	cands := tsRankAll(s.knownAvoided(q, []JackettResult{yts, oft}), q)
	if len(cands) != 2 || !strings.Contains(cands[0].raw.Title, "OFT") {
		t.Fatalf("got %q first, want the OFT before the avoided YTS", cands[0].raw.Title)
	}
}
