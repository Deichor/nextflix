package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// hashRecorder lists the hashes a fake TorrServer call got.
type hashRecorder struct {
	mu   sync.Mutex
	seen map[string]int
}

func newHashRecorder() *hashRecorder { return &hashRecorder{seen: map[string]int{}} }

func (r *hashRecorder) add(h string) {
	r.mu.Lock()
	r.seen[h]++
	r.mu.Unlock()
}

func (r *hashRecorder) list() string {
	time.Sleep(60 * time.Millisecond) // saves and pings run in goroutines
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for h := range r.seen {
		out = append(out, h[:1])
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func (r *hashRecorder) reset() {
	r.mu.Lock()
	r.seen = map[string]int{}
	r.mu.Unlock()
}

// pausedOf reads w.paused under the lock (the janitor may tick meanwhile).
func pausedOf(s *Service, w *tsWarm) bool {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	return w.paused
}

// shortPriority sets the playback grace (before the janitor starts with the first hold).
func shortPriority(s *Service, grace time.Duration) {
	s.playGrace, s.playIntent = grace, time.Minute
}

func warmWith(s *Service, q tsResolveQuery, hashes ...string) *tsWarm {
	w := s.holdWarm(tsResolveKey(q))
	for _, h := range hashes {
		s.trackTorrent(w, h)
	}
	return w
}

// Spider-Man plays while Sonic 3 (a detail page prefetch on another client) holds its pick and
// two upgrade candidates: Sonic 3 gives way, its torrents are saved in TorrServer's DB and
// dropped, never pinged; the playing title's spare, another capability variant of it, the
// next episode of a show and a title a player just asked for stay.
func TestPlaybackPriorityPausesOtherTitles(t *testing.T) {
	s, drops := newWarmService(t)
	shortPriority(s, 50*time.Millisecond)
	saves, pings := newHashRecorder(), newHashRecorder()
	s.saveFn, s.pingFn = saves.add, pings.add

	spider := tsResolveQuery{mediaType: "movie", title: "Spider-Man", year: 2002, hevc: true}
	playing := warmWith(s, spider, hx('a'), hx('b')) // the source and a spare
	s.setSource(playing, hx('a'))
	variant := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Spider-Man", year: 2002}, hx('c'))
	sonic := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Sonic the Hedgehog 3", year: 2024}, hx('d'), hx('e'), hx('f'))
	next := warmWith(s, tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 2}, hx('7'))
	s.markNext(next)
	asked := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}, hx('8'))
	s.markIntent(asked)

	// Nothing plays: nobody gives way
	s.priorityTick(time.Now())
	if pausedOf(s, sonic) || s.playbackYield(sonic.ctx, hx('d')) != "" {
		t.Fatal("paused without playback")
	}

	s.playBegin(hx('a'))
	if got := s.playbackYield(sonic.ctx, hx('d')); got != hx('a') {
		t.Fatalf("Sonic 3 work yields to %q, want the playing torrent", got)
	}
	for name, w := range map[string]*tsWarm{"playing": playing, "variant": variant, "next": next, "asked": asked} {
		if got := s.playbackYield(w.ctx, ""); got != "" {
			t.Fatalf("%s title yields to %s", name, got[:1])
		}
	}
	if got := s.playbackYield(context.Background(), hx('d')); got != "" {
		t.Fatal("a request without a title (a player's probe) must never wait")
	}

	s.priorityTick(time.Now()) // pause, and save (tsReleaseSettle is 0 in tests)
	if !pausedOf(s, sonic) || pausedOf(s, playing) || pausedOf(s, variant) || pausedOf(s, next) || pausedOf(s, asked) {
		t.Fatal("only Sonic 3 must be paused")
	}
	if got := saves.list(); got != "d,e,f" {
		t.Fatalf("saved %q, want Sonic 3's torrents", got)
	}
	if got := drops.list(); got != "" {
		t.Fatalf("dropped %q before the save", got)
	}
	s.priorityTick(time.Now()) // drop
	if got := drops.list(); got != "d,e,f" {
		t.Fatalf("dropped %q, want Sonic 3's torrents", got)
	}
	drops.reset()
	s.priorityTick(time.Now())
	if got := drops.list(); got != "" {
		t.Fatalf("dropped %q twice", got)
	}

	// The hold pings skip the paused title
	pings.reset()
	s.warmTick(time.Now().Add(time.Minute))
	if got := pings.list(); strings.ContainsAny(got, "def") {
		t.Fatalf("pinged %q, a paused title's torrents must stay closed", got)
	}

	// A seek ends the stream for a moment: still paused within the grace
	s.playEnd(hx('a'))
	s.priorityTick(time.Now())
	if !pausedOf(s, sonic) {
		t.Fatal("resumed within the grace after the stream ended")
	}
	time.Sleep(80 * time.Millisecond)
	pings.reset()
	s.priorityTick(time.Now())
	if pausedOf(s, sonic) || s.playbackYield(sonic.ctx, hx('d')) != "" {
		t.Fatal("still paused after the last stream ended")
	}
	if got := pings.list(); got != "d,e,f" {
		t.Fatalf("added again %q, want Sonic 3's torrents", got)
	}
}

// A torrent another, unpaused title holds (a pack two episodes share) stays loaded.
func TestPlaybackPriorityKeepsSharedTorrents(t *testing.T) {
	s, drops := newWarmService(t)
	shortPriority(s, time.Second)
	s.saveFn = func(string) {}
	show := tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 1}
	playing := warmWith(s, show, hx('a'), hx('p'))
	s.setSource(playing, hx('a'))
	other := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}, hx('p'), hx('c'))
	s.playBegin(hx('a'))
	defer s.playEnd(hx('a'))
	s.priorityTick(time.Now())
	s.priorityTick(time.Now())
	if !pausedOf(s, other) {
		t.Fatal("Charade not paused")
	}
	if got := drops.list(); got != "c" {
		t.Fatalf("dropped %q, want only Charade's own torrent", got)
	}
}

// Background work of a paused title waits in the gate and goes on once playback ends; it
// never holds a measurement slot while it waits.
func TestPlaybackPriorityGateWaits(t *testing.T) {
	s, _ := newWarmService(t)
	shortPriority(s, 20*time.Millisecond)
	playing := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968}, hx('a'))
	s.setSource(playing, hx('a'))
	other := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}, hx('c'))
	s.playBegin(hx('a'))

	got := make(chan bool, 1)
	go func() { got <- s.warmSlot(tsBackground(other.ctx), hx('c'), "spare measurement") }()
	select {
	case <-got:
		t.Fatal("Charade measured while another title plays")
	case <-time.After(100 * time.Millisecond):
	}
	if len(s.warmRounds) != 0 {
		t.Fatal("a waiting title holds a measurement slot")
	}
	// Its resolve does not count as failed when it runs out of time now
	if !s.pausedSince(other, time.Now().Add(-time.Second)) || s.pausedSince(playing, time.Time{}) {
		t.Fatal("pausedSince")
	}
	// The playing title's own work does not wait
	if !s.waitQuiet(tsBackground(playing.ctx), hx('a'), "probe") {
		t.Fatal("the playing title waited")
	}
	s.playEnd(hx('a'))
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("gate failed")
		}
		<-s.warmRounds
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after playback ended")
	}
}

// A detail page prefetch while another title plays is put off and starts on resume; the next
// episode's prefetch of the playing show is not.
func TestPrefetchDeferredDuringPlayback(t *testing.T) {
	s, _ := newWarmService(t)
	shortPriority(s, 20*time.Millisecond)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	post := func(query string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/prefetch?"+query, nil))
		var out struct{ Status string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Status
	}
	show := tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 1}
	playing := warmWith(s, show, hx('a'))
	s.setSource(playing, hx('a'))
	charade := tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}
	s.storeSource(tsResolveKey(charade), tsResolveResult{Hash: hx('c'), Magnet: "magnet:?xt=urn:btih:" + hx('c'), Quality: "1080p"})
	nextEp := tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 2}
	s.storeSource(tsResolveKey(nextEp), tsResolveResult{Hash: hx('e'), Magnet: "magnet:?xt=urn:btih:" + hx('e'), Quality: "1080p"})
	s.playBegin(hx('a'))

	if got := post("type=movie&title=Charade&year=1963"); got != "deferred" {
		t.Fatalf("prefetch during playback: %q", got)
	}
	if got := post("type=tv&title=Pioneer+One&season=1&episode=2&next_pos=3000&next_dur=3300"); got != "ready" {
		t.Fatalf("next episode prefetch: %q", got)
	}
	var w *tsWarm
	s.warmMu.Lock()
	for k, o := range s.warms {
		if strings.HasPrefix(k, tsResolveBaseKey(charade)) {
			w = o
		}
	}
	s.warmMu.Unlock()
	s.priorityTick(time.Now())
	if w == nil || !pausedOf(s, w) || w.source != "" {
		t.Fatal("the deferred title must be held, paused and not warmed")
	}
	s.playEnd(hx('a'))
	time.Sleep(40 * time.Millisecond)
	s.priorityTick(time.Now())
	deadline := time.Now().Add(time.Second)
	for {
		s.warmMu.Lock()
		src := w.source
		s.warmMu.Unlock()
		if src == hx('c') {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deferred prefetch did not warm the cached source on resume")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTitleGroup(t *testing.T) {
	a := tsResolveKey(tsResolveQuery{mediaType: "tv", title: "Pioneer One", year: 2010, season: 1, episode: 1, hevc: true})
	b := tsResolveKey(tsResolveQuery{mediaType: "tv", title: "Pioneer One", year: 2010, season: 2, episode: 5, maxQ: 720})
	c := tsResolveKey(tsResolveQuery{mediaType: "movie", title: "Pioneer One", year: 2010})
	if tsTitleGroup(a) != tsTitleGroup(b) || tsTitleGroup(a) == tsTitleGroup(c) {
		t.Fatalf("groups %q %q %q", tsTitleGroup(a), tsTitleGroup(b), tsTitleGroup(c))
	}
}
