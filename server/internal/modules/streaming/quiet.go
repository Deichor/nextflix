package streaming

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// A starting stream gets the line to itself: once resolve has answered, its background work
// (candidate checks that keep running and their duration probes, upgrade warm-ups, spare
// measurements, late candidate checks and their probes) waits while another torrent starts:
// the player's probe runs, or its play stream is not past its start yet (see tsPlayRun). Work
// on a TorBox file does not wait for a starting TorBox stream (CDN reads take nothing from a
// swarm), only for a starting torrent. Work before the answer (resolve, prefetch on the
// detail page) never waits for that. Background work of a title also waits while a player
// waits for a new resolve of it (a step down, a slow-start handover with avoid), and any work
// of a title that gives way to another title's playback waits too (priority.go). The subtitle
// timing's window reads wait for any starting stream, the file's own included (WaitStartQuiet).

// Vars so tests can shorten them.
var (
	tsStartQuiet    = 30 * time.Second       // a play stream this young counts as starting for thumbnails (thumbs.go)
	tsStartAhead    = 30 * time.Second       // a stream this far ahead of real time is past its start
	tsStartSteady   = 60 * time.Second       // ... and so is one that flowed this long without a gap (and kept up)
	tsStartGap      = 5 * time.Second        // this long without a write is a gap (a stall), not a steady flow
	tsStartQuietMax = 120 * time.Second      // a start never holds background work longer than this
	tsQuietPoll     = 500 * time.Millisecond // how often deferred work looks again
)

type tsPhaseKey struct{}

// tsPhase marks the work of one resolve as background once it has answered.
type tsPhase struct{ background atomic.Bool }

func tsWithPhase(ctx context.Context, p *tsPhase) context.Context {
	return context.WithValue(ctx, tsPhaseKey{}, p)
}

// tsBackground marks ctx as background work from the start.
func tsBackground(ctx context.Context) context.Context {
	p := &tsPhase{}
	p.background.Store(true)
	return tsWithPhase(ctx, p)
}

func tsInBackground(ctx context.Context) bool {
	p, _ := ctx.Value(tsPhaseKey{}).(*tsPhase)
	return p != nil && p.background.Load()
}

// tsPlayStream is one running play stream; play.go counts the bytes it delivers.
type tsPlayStream struct {
	run       *tsPlayRun   // guarded by Service.warmMu
	rate      atomic.Int64 // bytes per second of media it carries (0 = unknown: re-encoded, audio only)
	bytes     atomic.Int64
	firstAt   atomic.Int64 // unix nanos of its first bytes (0 = none yet)
	steadyAt  atomic.Int64 // unix nanos since it flows without a gap
	idleSince atomic.Int64 // unix nanos of the end of its last write (0 = writing now)
}

// tsPlayRun is the start of a torrent's playback: its first stream and the restarts and seeks
// that follow until one of them is past its start. A run starts until a stream is
// tsStartAhead ahead of real time, or flowed tsStartSteady without a gap and kept up (healthy:
// a seek after that starts a new run), or tsStartQuietMax after its first stream began. After
// that timeout the restarts and seeks of the torrent join the run without a new start, so a
// stream that keeps stalling never holds background work back for good, until one of its
// streams gets healthy. Guarded by Service.warmMu.
type tsPlayRun struct {
	start   time.Time
	firstAt time.Time // first bytes of any of its streams
	endAt   time.Time // its last stream ended (zero while one runs)
	streams []*tsPlayStream
	settled bool // past its start
	healthy bool // ... because a stream got healthy (not by the timeout)
}

// setRate sets the media rate of the stream from its probe: the bitrate of what it carries
// (the copied video and, when copied, the chosen audio track), else the file's bitrate for a
// copied video; unknown for a re-encoded video.
func (st *tsPlayStream) setRate(p *tsProbe, plan tsVideoPlan, aplan tsAudioPlan, audio int, track tsTrack) {
	if st == nil || p == nil {
		return
	}
	var bps float64
	if track != tsTrackAudio {
		if !plan.copy {
			return
		}
		v := p.video()
		if v == nil || v.bitRate() <= 0 {
			if f, err := strconv.ParseFloat(p.Format.BitRate, 64); err == nil && f > 0 {
				st.rate.Store(int64(f / 8))
			}
			return
		}
		bps = v.bitRate()
	}
	if track != tsTrackVideo && aplan.copy {
		bps += p.audioStream(audio).bitRate()
	}
	if bps > 0 {
		st.rate.Store(int64(bps / 8))
	}
}

// bitRate is the stream's bits per second from ffprobe or Matroska's statistics tags (BPS),
// 0 when unknown.
func (ps *tsProbeStream) bitRate() float64 {
	if ps == nil {
		return 0
	}
	if f, err := strconv.ParseFloat(ps.BitRate, 64); err == nil && f > 0 {
		return f
	}
	for k, v := range ps.Tags {
		if k := strings.ToUpper(k); k == "BPS" || strings.HasPrefix(k, "BPS-") {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				return f
			}
		}
	}
	return 0
}

// writing / wrote are called around every write of the stream (play.go).
func (st *tsPlayStream) writing(now time.Time) {
	if idle := st.idleSince.Swap(0); idle != 0 && now.Sub(time.Unix(0, idle)) >= tsStartGap {
		st.steadyAt.Store(now.UnixNano())
	}
}

func (st *tsPlayStream) wrote(n int, now time.Time) {
	st.bytes.Add(int64(n))
	if st.firstAt.CompareAndSwap(0, now.UnixNano()) {
		st.steadyAt.Store(now.UnixNano())
	}
	st.idleSince.Store(now.UnixNano())
}

// ahead is how many seconds of media the stream delivered beyond the time since its first
// bytes; known is false without first bytes or a rate.
func (st *tsPlayStream) ahead(now time.Time) (sec float64, known bool) {
	first, rate := st.firstAt.Load(), st.rate.Load()
	if first == 0 || rate <= 0 {
		return 0, false
	}
	return float64(st.bytes.Load())/float64(rate) - now.Sub(time.Unix(0, first)).Seconds(), true
}

// settled says how the stream got past its start ("" = not yet).
func (st *tsPlayStream) settled(now time.Time) string {
	if st.firstAt.Load() == 0 {
		return ""
	}
	ahead, known := st.ahead(now)
	if known && ahead >= tsStartAhead.Seconds() {
		return strconv.Itoa(int(ahead)) + " s ahead"
	}
	if idle := st.idleSince.Load(); idle != 0 && now.Sub(time.Unix(0, idle)) >= tsStartGap {
		return "" // in a gap right now
	}
	if now.Sub(time.Unix(0, st.steadyAt.Load())) >= tsStartSteady && (!known || ahead >= 0) {
		return "flows steadily"
	}
	return ""
}

// beginStreamLocked registers a new play stream of hash. It joins the torrent's run unless
// that run got healthy or the torrent was idle (not played within playGrace). Needs s.warmMu,
// before s.playing counts the stream.
func (s *Service) beginStreamLocked(hash string, now time.Time) *tsPlayStream {
	s.startingLocked(hash, now)
	r := s.playRuns[hash]
	idle := s.playing[hash] <= 0
	if end, ok := s.playEndAt[hash]; idle && ok && now.Sub(end) < s.playGrace {
		idle = false
	}
	if r == nil || r.healthy || idle {
		r = &tsPlayRun{start: now}
		s.playRuns[hash] = r
	}
	st := &tsPlayStream{run: r}
	r.streams = append(r.streams, st)
	r.endAt = time.Time{}
	s.playStreams[hash] = append(s.playStreams[hash], st)
	return st
}

// endStreamLocked unregisters st (nil: the oldest stream of hash). Needs s.warmMu.
func (s *Service) endStreamLocked(hash string, st *tsPlayStream, now time.Time) {
	s.startingLocked(hash, now) // a seek that ends a healthy stream begins a new run
	list := s.playStreams[hash]
	i := 0
	for st != nil && i < len(list) && list[i] != st {
		i++
	}
	if i >= len(list) {
		return
	}
	st = list[i]
	if list = append(list[:i:i], list[i+1:]...); len(list) == 0 {
		delete(s.playStreams, hash)
	} else {
		s.playStreams[hash] = list
	}
	r := st.run
	for j, o := range r.streams {
		if o == st {
			r.streams = append(r.streams[:j:j], r.streams[j+1:]...)
			break
		}
	}
	if len(r.streams) == 0 {
		r.endAt = now
	}
}

// startingLocked reports whether the run of hash still starts; a run that just got past its
// start logs why. Needs s.warmMu.
func (s *Service) startingLocked(hash string, now time.Time) bool {
	r := s.playRuns[hash]
	if r == nil || r.healthy || len(r.streams) == 0 {
		return false
	}
	why := ""
	for _, st := range r.streams {
		if why = st.settled(now); why != "" {
			r.healthy = true
			break
		}
	}
	if r.settled {
		return false // timed out before; a healthy stream now lets the next seek start anew
	}
	if why == "" && now.Sub(r.start) >= tsStartQuietMax {
		why = "the start took too long, background work goes on"
	}
	if why == "" {
		return true
	}
	r.settled = true
	log.Printf("warm: %s is past its start after %s: %s", tsShortHash(hash), now.Sub(r.start).Round(time.Second), why)
	return false
}

// playedFor is how long ago the current run of hash sent its first bytes (0 = none yet).
func (s *Service) playedFor(hash string, now time.Time) time.Duration {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	if r := s.playRuns[hash]; r != nil && !r.firstAt.IsZero() {
		return now.Sub(r.firstAt)
	}
	return 0
}

// pruneRunsLocked forgets the runs of torrents that stopped playing long ago. Needs s.warmMu.
func (s *Service) pruneRunsLocked(now time.Time) {
	for h, r := range s.playRuns {
		if len(r.streams) == 0 && !r.endAt.IsZero() && now.Sub(r.endAt) >= tsHoldTime {
			delete(s.playRuns, h)
		}
	}
}

// startingStream returns a torrent other than except that is starting: the player probes it,
// or its play stream is not past its start (see tsPlayRun). "" = none.
func (s *Service) startingStream(except string) string {
	if all := s.startingStreams(except); len(all) > 0 {
		return all[0]
	}
	return ""
}

// startingStreams lists every torrent other than except that is starting (see startingStream).
func (s *Service) startingStreams(except string) []string {
	now := time.Now()
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	var out []string
	for h, n := range s.starting {
		if h != except && n > 0 {
			out = append(out, h)
		}
	}
	for h := range s.playRuns {
		if h != except && s.starting[h] <= 0 && s.startingLocked(h, now) {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// Rescue work (a sidegrade search, spare measurements: what sidegrade can offer) of a title
// whose playing source starves does not wait for a starting stream: that start may itself be
// failing (Night of the Living Dead on a 28 Mbit line: the pick at x0.11 held the search's
// check of a YTS that kept up for 55 s, until the start cap).
const (
	tsRescueRatio  = 0.5              // a source measured below this share of its need starves
	tsRescueRecent = 30 * time.Second // ... as does one whose player reported draining this recently
)

// rescueCtx is the context of rescue work (what) of w: foreground while its source starves,
// so it passes the start-quiet gate (playback priority still applies), else background.
// Resolve's measurement no longer counts once the source's stream got healthy.
func (s *Service) rescueCtx(w *tsWarm, what string) context.Context {
	s.warmMu.Lock()
	measured := w.health.below(tsRescueRatio)
	if measured && w.source != "" {
		s.startingLocked(w.source, time.Now())
		if r := s.playRuns[w.source]; r != nil && r.healthy {
			measured = false
		}
	}
	starving := measured || (!w.drainAt.IsZero() && time.Since(w.drainAt) < tsRescueRecent)
	s.warmMu.Unlock()
	if !starving {
		return tsBackground(w.ctx)
	}
	log.Printf("warm: %s of %s does not wait for a starting stream: its source starves", what, w.key)
	return w.ctx
}

// titleResolving reports whether a player waits for a resolve of the title / episode of key
// (any capability variant: a step down with maxq, a handover with avoid).
func (s *Service) titleResolving(key string) bool {
	base := tsKeyBase(key)
	g := &s.flights
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, f := range g.m {
		if tsKeyBase(k) == base && f.waiters.Load() > 0 {
			return true
		}
	}
	return false
}

// startingFor returns a starting torrent that a read of hash waits for ("" = none); with own
// a stream of hash itself counts too. A TorBox read beside a starting TorBox stream competes
// with no swarm: it goes on.
func (s *Service) startingFor(hash string, own bool) string {
	except := hash
	if own {
		except = ""
	}
	debrid := hash != "" && s.debridStreams(hash)
	for _, other := range s.startingStreams(except) {
		if !debrid || !s.debridStreams(other) {
			return other
		}
	}
	return ""
}

// yieldReason returns what work on hash under ctx waits for and why: background work waits
// while another torrent's stream starts and while a player waits for a new resolve of its
// title, and any work of a title that must give way to playback (priority.go) waits while
// another title plays. "" = none.
func (s *Service) yieldReason(ctx context.Context, hash string) (string, string) {
	if other := s.playbackYield(ctx, hash); other != "" {
		return other, "plays"
	}
	if !tsInBackground(ctx) {
		return "", ""
	}
	if other := s.startingFor(hash, false); other != "" {
		return other, "starts"
	}
	if w := tsWarmOf(ctx); w != nil && s.titleResolving(w.key) {
		return "a player", "resolves the title again"
	}
	return "", ""
}

// waitQuiet holds work on hash (what names it in the log) while yieldReason says so. It
// returns false when ctx ended.
func (s *Service) waitQuiet(ctx context.Context, hash, what string) bool {
	return s.holdWhile(ctx, hash, what, func() (string, string) { return s.yieldReason(ctx, hash) })
}

// WaitStartQuiet holds a background read of the file of hash (the subtitle timing's window
// reads) while a stream starts, the file's own included. It returns false when ctx ended.
func (s *Service) WaitStartQuiet(ctx context.Context, hash string) bool {
	hash = strings.ToLower(hash)
	return s.holdWhile(ctx, hash, "subtitle timing read", func() (string, string) {
		if other := s.startingFor(hash, true); other != "" {
			return other, "starts"
		}
		return "", ""
	})
}

// holdWhile waits while reason names what to wait for, logging the wait.
func (s *Service) holdWhile(ctx context.Context, hash, what string, reason func() (string, string)) bool {
	var since time.Time
	for {
		other, why := reason()
		if other == "" {
			if !since.IsZero() {
				log.Printf("warm: resuming %s of %s after %s", what, tsShortHash(hash), time.Since(since).Round(time.Second))
			}
			return ctx.Err() == nil
		}
		if since.IsZero() {
			since = time.Now()
			log.Printf("warm: deferring %s of %s while %s %s", what, tsShortHash(hash), tsShortHash(other), why)
		}
		if !tsSleep(ctx, tsQuietPoll) {
			return false
		}
	}
}

func tsShortHash(h string) string {
	if h == "" {
		return "a new torrent"
	}
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
