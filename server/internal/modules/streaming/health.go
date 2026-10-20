package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Swarm health: Jackett seeder counts are often stale, so resolve reads the middle of each
// validated file and compares the real download rate with the bitrate the file needs
// (size / runtime). A torrent that was just added is cold (1-3 peers in the first seconds)
// and the first second or two are often a burst (TorrServer's read-ahead, the first peers
// unchoking), so the verdict is the rate delivered after tsWarmSkip over the trailing
// tsWarmSpan (see window), and keeping up needs at least two polls that delivered the
// file's bitrate: a single burst never makes a weak swarm look healthy.

const (
	tsKeepUpFactor      = 1.3              // headroom over the average bitrate for peaks and seeks
	tsWarmFastFactor    = 3                // ... this many times that decides after tsWarmFastSpan already
	tsWarmMaxBytes      = int64(128) << 20 // longest read of one measurement
	tsMidFileShare      = 45               // percent into the file where the first measurement reads
	tsMidFileStep       = 13               // percent the read moves per later measurement (fresh pieces)
	tsDefaultMovieMin   = 110
	tsDefaultEpisodeMin = 50
)

// Warm-up timings. Vars so tests can shorten them.
var (
	tsWarmPoll      = time.Second             // TorrServer stat poll (its speed is per second)
	tsWarmSkip      = 1500 * time.Millisecond // the start burst, left out of the verdict
	tsWarmSustain   = 3 * time.Second         // the verdict needs this much data after tsWarmSkip
	tsWarmFastSpan  = 2 * time.Second         // ... or this much at tsWarmFastFactor times the rate
	tsWarmSpan      = 8 * time.Second         // the verdict is the delivered rate over this trailing span
	tsWarmDeadAfter = 8 * time.Second         // no peers and no data this long: dead swarm
	tsWarmWindowTop = 15 * time.Second        // longest warm-up for the highest resolution on offer
	tsWarmWindow    = 8 * time.Second         // lower resolutions
	// How long resolve waits for a healthy candidate once only starving ones have answered
	// (tsResolveAnswerCap usually ends the wait first)
	tsStarvingGrace = 15 * time.Second
)

// tsHealth is the measured swarm health of a resolved source (cached with it).
type tsHealth struct {
	Rate        int64         `json:"rate"`           // bytes/s delivered after the warm-up (see window)
	Peak        int64         `json:"peak,omitempty"` // bytes/s, best 3 s window (diagnostics only)
	Need        int64         `json:"need"`           // bytes/s the file needs on average
	ActivePeers int           `json:"active_peers"`
	Seeders     int           `json:"seeders"`
	KeepsUp     bool          `json:"keeps_up"`
	Dead        bool          `json:"dead,omitempty"` // no peers and no data for tsWarmDeadAfter
	partial     bool          // live health before the verdict: the rate of the samples so far, never keeps up
	sampled     time.Duration // how long the samples cover
	series      string        // per-poll read / TorrServer rates in MB/s (log only)
	// TorrServer served the read from its RAM cache (see fromCache): judged by what the swarm
	// delivered meanwhile, and no proof that the swarm keeps up
	cached bool
}

// MarshalJSON adds "measuring" for a health before its verdict, so players don't take a few
// seconds of samples for a starving swarm.
func (h tsHealth) MarshalJSON() ([]byte, error) {
	type plain tsHealth
	return json.Marshal(struct {
		plain
		Measuring bool `json:"measuring,omitempty"`
	}{plain(h), h.partial})
}

func (h *tsHealth) ratio() float64 {
	if h == nil || h.Need <= 0 {
		return 0
	}
	return float64(h.Rate) / float64(h.Need)
}

// below: the source delivers under share of its need by a verdict, or by a live health over
// tsMeasuredSpan (a few samples before the verdict say nothing yet).
func (h *tsHealth) below(share float64) bool {
	return h != nil && (!h.partial || h.sampled >= tsMeasuredSpan) && h.ratio() < share
}

func (h *tsHealth) String() string {
	if h == nil {
		return "not measured"
	}
	verdict := "keeps up"
	switch {
	case h.Dead:
		verdict = "dead"
	case h.partial:
		verdict = "measuring"
	case !h.KeepsUp:
		verdict = "starving"
	}
	if h.cached {
		verdict += " (read from TorrServer's cache)"
	}
	return fmt.Sprintf("%.2f MB/s (peak %.2f) for %.2f MB/s needed (x%.2f), %d active peers, %d seeders: %s",
		float64(h.Rate)/float64(tsMB), float64(h.Peak)/float64(tsMB), float64(h.Need)/float64(tsMB), h.ratio(),
		h.ActivePeers, h.Seeders, verdict)
}

// keepsUp is true for an unmeasured source, so a failed measurement never hides a candidate;
// a snapshot without a verdict does not keep up yet.
func (r *tsResolveResult) keepsUp() bool {
	if r.partial {
		return r.Health != nil && r.Health.KeepsUp
	}
	return r.Health == nil || r.Health.KeepsUp
}

func newHealth(rate, need int64, activePeers, seeders int) *tsHealth {
	return &tsHealth{
		Rate: rate, Need: need, ActivePeers: activePeers, Seeders: seeders,
		KeepsUp: float64(rate) >= tsKeepUpFactor*float64(need),
	}
}

// tsNeedRate is the average bytes/s of a file: size / runtime (TMDB runtime in minutes when
// known, else a typical movie or episode length).
func tsNeedRate(size int64, runtimeMin int, mediaType string) int64 {
	if size <= 0 {
		return 0
	}
	if runtimeMin <= 0 {
		runtimeMin = tsDefaultMovieMin
		if mediaType == "tv" {
			runtimeMin = tsDefaultEpisodeMin
		}
	}
	return size / int64(runtimeMin*60)
}

// tsMidOffset is where measurement number round reads (aligned to 1 MiB). Each round moves
// on, so pieces cached by an earlier round never inflate the rate.
func tsMidOffset(size int64, round int) int64 {
	share := int64(tsMidFileShare-5+round*tsMidFileStep)%90 + 5 // 45, 58, 71, 84, 7, ...
	return (size * share / 100) &^ (1<<20 - 1)
}

// tsChoose picks among the finished checks (best rank first), in this order:
//  1. the best-ranked one that keeps up and whose duration is verified (or could not be checked)
//  2. a verified one that is closest to keeping up
//  3. one of unknown duration (the probe failed): best-ranked that keeps up, else the closest
//
// A good one whose file turned out smaller than its name said (demoted) gives way to a later
// good one of a higher real resolution, and one whose probed format ranks it lower than a
// finished good one of the same resolution gives way to that one. A file the client cannot
// show right (unfit: DV profile 5 without dv, Hi10P / 4:4:4, above its maxq, video it cannot
// decode) is only taken when no other check is left; of the ones that break the caps the
// lowest good resolution (the cheapest re-encode). final is false while a running check could
// still beat it.
func tsChoose(results []*tsResolveResult, finished []bool) (best int, final bool) {
	fit := results
	unfit := false
	for _, r := range results {
		unfit = unfit || (r != nil && !r.fits())
	}
	if unfit {
		fit = make([]*tsResolveResult, len(results))
		for i, r := range results {
			if r != nil && r.fits() {
				fit[i] = r
			}
		}
	}
	if best, final = tsChooseFit(fit, finished); best >= 0 || !unfit {
		return best, final
	}
	best, _ = tsChooseFit(tsCheapest(results), finished)
	return best, tsAllDone(finished)
}

func tsAllDone(finished []bool) bool {
	for _, f := range finished {
		if !f {
			return false
		}
	}
	return true
}

func tsChooseFit(results []*tsResolveResult, finished []bool) (best int, final bool) {
	allDone := tsAllDone(finished)
	doneBefore := true
	demoted := -1
	for i, r := range results {
		if r != nil && r.good() {
			if !r.demoted() {
				if demoted >= 0 && tsQualityHeight(results[demoted].Quality) >= tsQualityHeight(r.Quality) {
					return demoted, doneBefore
				}
				return tsProbeReorder(results, finished, i), doneBefore
			}
			if demoted < 0 {
				demoted = i
			}
		}
		doneBefore = doneBefore && finished[i]
	}
	if demoted >= 0 {
		return demoted, allDone
	}
	// Among the results ok accepts, the one most likely to play (alive, then rate / need, then the
	// lower need): with every swarm dead the ratios tie at 0, and a 4K must not win just by rank
	closest := func(ok func(*tsResolveResult) bool) int {
		sub := make([]*tsResolveResult, len(results))
		for i, r := range results {
			if r != nil && ok(r) {
				sub[i] = r
			}
		}
		return tsChoosePlayable(sub)
	}
	if best = closest((*tsResolveResult).durationOK); best >= 0 {
		return best, allDone
	}
	for i, r := range results {
		if r != nil && r.keepsUp() {
			return i, allDone
		}
	}
	return closest(func(*tsResolveResult) bool { return true }), allDone
}

// tsProbeMargin: how much higher a probe-corrected rank must be to overtake a better-ranked pick.
const tsProbeMargin = 3

// tsProbeReorder: the good pick i gives way to a finished good one of the same resolution
// whose rank the probe raised (or i's lowered) clearly above its own.
func tsProbeReorder(results []*tsResolveResult, finished []bool, i int) int {
	best := i
	for j, r := range results {
		if j == i || r == nil || !finished[j] || !r.good() || r.demoted() || r.avoided || r.Quality != results[i].Quality {
			continue
		}
		if r.rankAdj > results[i].rankAdj && r.rank > results[best].rank+tsProbeMargin {
			best = j
		}
	}
	return best
}

// tsMeasuredSpan: a running check's health counts as a measurement once its samples cover this.
const tsMeasuredSpan = 2 * time.Second

// measured: the result has a health verdict, or a live health over at least tsMeasuredSpan.
func (r *tsResolveResult) measured() bool {
	if r == nil || r.Health == nil {
		return false
	}
	return !r.Health.partial || r.Health.sampled >= tsMeasuredSpan || r.Health.Dead
}

// alive: the swarm showed active peers or delivered data and is not judged dead.
func (r *tsResolveResult) alive() bool {
	return r.Health != nil && !r.Health.Dead && (r.Health.ActivePeers > 0 || r.Health.Rate > 0)
}

// tsCapVerifiedMin: a source of verified length delivering this share of its need plays with
// some buffering; at the cap it beats one of unknown length whatever that one delivers.
const tsCapVerifiedMin = 0.5

// tsCapClearly: ratio a is clearly better than b (by 0.1 and by half again).
func tsCapClearly(a, b float64) bool { return a-b >= 0.1 && a >= 1.5*b }

// tsChoosePlayable picks, when nothing keeps up at the answer cap, the source most likely to
// play: finished checks and snapshots of running ones alike (rs, best rank first). In order:
// alive (active peers or data, not dead) over a dead or silent swarm, fit for the client
// (not unfit, nor unprobed with a name it gets re-encoded by; of two that break its caps the
// good one, then the lower resolution), within
// the line over one that needs more than it carries (overLine), measured over unmeasured (a
// 4K below 0.3 never beats a <= 1080p with a higher ratio), a verified duration (unless it
// delivers under tsCapVerifiedMin and the other one clearly more), of two over the line the
// lower need, a clearly better rate / need ratio, the lower need (bitrate) when the ratios
// are close, then more active peers, connected seeders, Jackett seeders and the rank. -1 when
// rs is empty.
func tsChoosePlayable(rs []*tsResolveResult) int {
	better := func(a, b *tsResolveResult) bool {
		if a.alive() != b.alive() {
			return a.alive()
		}
		if (a.unfit == "") != (b.unfit == "") {
			return a.unfit == ""
		}
		// Not probed yet: an x265 name for a browser is re-encoded, and its probe is slow
		if a.nameUnfit != b.nameUnfit {
			return b.nameUnfit
		}
		if a.overCaps && b.overCaps && (tsCheaperFallback(a, b) || tsCheaperFallback(b, a)) {
			return tsCheaperFallback(a, b)
		}
		if a.overLine != b.overLine {
			return b.overLine
		}
		if a.measured() != b.measured() {
			return a.measured()
		}
		ra, rb := a.Health.ratio(), b.Health.ratio()
		qa, qb := tsQualityHeight(a.Quality), tsQualityHeight(b.Quality)
		if qa == 2160 && ra < 0.3 && qb > 0 && qb <= 1080 && rb > ra {
			return false
		}
		if qb == 2160 && rb < 0.3 && qa > 0 && qa <= 1080 && ra > rb {
			return true
		}
		// A verified length beats an unknown one (a probe that failed or still runs: the
		// player waits for it) unless the verified swarm delivers under tsCapVerifiedMin of its
		// need and the other one clearly more
		if va, vb := a.durationOK(), b.durationOK(); va != vb {
			rv, ru := ra, rb
			if vb {
				rv, ru = rb, ra
			}
			if rv >= tsCapVerifiedMin || !tsCapClearly(ru, rv) {
				return va
			}
		}
		na, nb := tsResultNeed(a), tsResultNeed(b)
		// Nothing fits the line: the lowest need stalls least (the ratios of checks that
		// share the line say little)
		if a.overLine && na > 0 && nb > 0 && na != nb {
			return na < nb
		}
		if tsCapClearly(ra, rb) || tsCapClearly(rb, ra) {
			return ra > rb
		}
		if na > 0 && nb > 0 && (float64(na) < 0.8*float64(nb) || float64(nb) < 0.8*float64(na)) {
			return na < nb
		}
		if ra != rb && math.Abs(ra-rb) >= 0.05 {
			return ra > rb
		}
		pa, sa := tsPeers(a)
		pb, sb := tsPeers(b)
		if pa != pb {
			return pa > pb
		}
		if sa != sb {
			return sa > sb
		}
		return a.Seeders > b.Seeders
	}
	best := -1
	for i, r := range rs {
		if r != nil && (best < 0 || better(r, rs[best])) {
			best = i
		}
	}
	return best
}

// tsResultNeed is the bytes/s a result needs: measured, else from its size (0 = unknown).
func tsResultNeed(r *tsResolveResult) int64 {
	if r.Health != nil && r.Health.Need > 0 {
		return r.Health.Need
	}
	return tsNeedRate(r.Size, 0, "movie")
}

func tsPeers(r *tsResolveResult) (active, seeders int) {
	if r.Health == nil {
		return 0, 0
	}
	return r.Health.ActivePeers, r.Health.Seeders
}

type tsSwarmStat struct {
	DownloadSpeed    float64 `json:"download_speed"` // bytes/s
	ActivePeers      int     `json:"active_peers"`
	ConnectedSeeders int     `json:"connected_seeders"`
	// Piece data the torrent got from peers since it was loaded (omitted while 0)
	Downloaded *int64 `json:"bytes_read_useful_data"`
}

// tsWarmSample is one poll during a warm-up.
type tsWarmSample struct {
	at    time.Duration // since the read started
	read  int64         // bytes read so far
	speed float64       // TorrServer download_speed in bytes/s, -1 when the stat failed
	peers int           // active peers, -1 when the stat failed
	got   int64         // TorrServer's downloaded bytes (Downloaded), -1 when unknown
}

// tsCacheMinRead: a read this small says nothing either way, so it is never judged as cached.
const tsCacheMinRead = int64(4) << 20

// tsWarmMeter judges a warm-up from its samples (no I/O, so tests can feed it).
type tsWarmMeter struct {
	need      float64 // bytes/s the file needs
	enough    float64 // bytes/s that count as keeping up
	samples   []tsWarmSample
	peak      float64 // best rate sustained over tsWarmSustain (diagnostics)
	peers     int
	seeders   int
	alive     time.Duration // last sample with peers or new data
	seenAlive bool
	dead      bool
	base      int64 // TorrServer's downloaded bytes before the read, -1 when unknown
}

func newWarmMeter(need int64) *tsWarmMeter {
	return &tsWarmMeter{need: float64(need), enough: tsKeepUpFactor * float64(need), base: -1}
}

// fromCache: the read got at least 4 times what the torrent downloaded meanwhile, so
// TorrServer served it from its RAM cache (pieces played or read ahead minutes before; a
// dead swarm read 1157 MB/s that way). Such a read says nothing about the swarm.
func (m *tsWarmMeter) fromCache() bool {
	if m.base < 0 || len(m.samples) == 0 {
		return false
	}
	got := int64(-1)
	for _, s := range m.samples {
		if s.got >= 0 {
			got = s.got
		}
	}
	read := m.samples[len(m.samples)-1].read
	return got >= 0 && read >= tsCacheMinRead && (got-m.base)*4 < read
}

// swarmMeter is the meter of a cached read with the bytes the swarm delivered in place of the
// bytes read; nil when the read was not cached.
func (m *tsWarmMeter) swarmMeter() *tsWarmMeter {
	if !m.fromCache() {
		return nil
	}
	o := newWarmMeter(int64(m.need))
	o.seeders = m.seeders
	got := m.base
	for _, s := range m.samples {
		if s.got >= 0 {
			got = s.got
		}
		s.read = max(got-m.base, 0)
		o.add(s)
	}
	return o
}

// add records a sample and reports whether the warm-up can stop: the delivered rate clearly
// keeps up, or the swarm showed no peers and no data for tsWarmDeadAfter.
func (m *tsWarmMeter) add(s tsWarmSample) (done bool) {
	var prev tsWarmSample
	if n := len(m.samples); n > 0 {
		prev = m.samples[n-1]
	}
	if s.peers > 0 || s.read > prev.read {
		m.alive, m.seenAlive = s.at, true
	}
	if s.peers >= 0 {
		m.peers = s.peers
	}
	m.samples = append(m.samples, s)
	if s.at >= tsWarmSustain {
		from := tsWarmSample{} // the start of the read
		for _, p := range m.samples {
			if s.at-p.at >= tsWarmSustain {
				from = p
			}
		}
		m.peak = max(m.peak, m.rate(from, s))
	}
	m.dead = s.at-m.alive >= tsWarmDeadAfter
	return m.keepsUp() || m.dead
}

// rate between two samples: the read rate or TorrServer's average download speed
// (it also counts read-ahead pieces), whichever is higher.
func (m *tsWarmMeter) rate(from, to tsWarmSample) float64 {
	d := (to.at - from.at).Seconds()
	if d <= 0 {
		return 0
	}
	rate := float64(to.read-from.read) / d
	sum, n := 0.0, 0
	for _, p := range m.samples {
		if p.at > from.at && p.at <= to.at && p.speed >= 0 {
			sum += p.speed
			n++
		}
	}
	if n > 0 && sum/float64(n) > rate {
		rate = sum / float64(n)
	}
	return rate
}

// tsWarmStats describes the verdict window of a warm-up: the polls that start after
// tsWarmSkip and lie within the trailing tsWarmSpan.
type tsWarmStats struct {
	avg    float64       // delivered bytes/s (see window)
	median float64       // median per-poll rate (the old verdict, log only)
	span   time.Duration // time the window covers
	useful int           // polls that delivered at least the file's bitrate
}

// window measures the verdict window. BitTorrent delivers in bursts (whole pieces, peers
// unchoking in turns), so a median of per-second rates sees 0 in most seconds of a swarm
// that delivers several times what the file needs; avg is the delivered rate instead: the
// bytes read / the window's time, or TorrServer's average download speed (it also counts
// read-ahead pieces) when higher, its single highest poll left out once there are 4, so one
// speed spike never decides. ok is false until the window covers tsWarmSustain.
func (m *tsWarmMeter) window() (st tsWarmStats, ok bool) {
	n := len(m.samples)
	if n < 2 {
		return st, false
	}
	last := m.samples[n-1]
	first := -1
	var rates, speeds []float64
	for i := 1; i < n; i++ {
		from, to := m.samples[i-1], m.samples[i]
		if from.at < tsWarmSkip || from.at < last.at-tsWarmSpan {
			continue
		}
		d := (to.at - from.at).Seconds()
		if d <= 0 {
			continue
		}
		if first < 0 {
			first = i - 1
		}
		v := float64(to.read-from.read) / d
		if to.speed >= 0 {
			speeds = append(speeds, to.speed)
			v = max(v, to.speed)
		}
		rates = append(rates, v)
		if v > 0 && v >= m.need {
			st.useful++
		}
	}
	if first < 0 {
		return st, false
	}
	from := m.samples[first]
	st.span = last.at - from.at
	st.avg = float64(last.read-from.read) / st.span.Seconds()
	if len(speeds) > 0 {
		sort.Float64s(speeds)
		if len(speeds) >= 4 {
			speeds = speeds[:len(speeds)-1]
		}
		sum := 0.0
		for _, v := range speeds {
			sum += v
		}
		st.avg = max(st.avg, sum/float64(len(speeds)))
	}
	sort.Float64s(rates)
	if k := len(rates); k%2 == 1 {
		st.median = rates[k/2]
	} else {
		st.median = (rates[k/2-1] + rates[k/2]) / 2
	}
	return st, st.span+tsWarmPoll/4 >= tsWarmSustain // polls come a little late (stat latency)
}

// keepsUp: the delivered rate reaches enough over tsWarmSustain, or tsWarmFastFactor times
// that over tsWarmFastSpan, and at least two polls delivered the file's bitrate (a single
// burst never makes a swarm keep up).
func (m *tsWarmMeter) keepsUp() bool {
	st, ok := m.window()
	if st.useful < 2 {
		return false
	}
	if ok {
		return st.avg >= m.enough
	}
	return st.span+tsWarmPoll/4 >= tsWarmFastSpan && st.avg >= tsWarmFastFactor*m.enough
}

// verdict: the window is long enough to judge (or already clearly keeps up).
func (m *tsWarmMeter) verdict() bool {
	_, ok := m.window()
	return ok || m.keepsUp()
}

// health is the verdict (or the health so far); before any poll after tsWarmSkip the rate
// is the one of the whole read.
func (m *tsWarmMeter) health(need int64) *tsHealth {
	if o := m.swarmMeter(); o != nil {
		h := o.health(need)
		h.cached = true
		return h
	}
	st, _ := m.window()
	rate := st.avg
	if st.span <= 0 && len(m.samples) > 0 {
		rate = m.rate(tsWarmSample{}, m.samples[len(m.samples)-1])
	}
	h := newHealth(int64(rate), need, m.peers, m.seeders)
	h.KeepsUp = m.keepsUp()
	h.Peak = int64(max(m.peak, rate))
	h.Dead = m.dead && !h.KeepsUp
	if n := len(m.samples); n > 0 {
		h.sampled = m.samples[n-1].at
	}
	h.series = fmt.Sprintf("median %.2f; %s", st.median/float64(tsMB), m.series())
	return h
}

// wholeRead is the health of a read that ended (EOF or tsWarmMaxBytes) before the verdict
// window could judge it: a fast source finishes a short read in a few seconds, so the rate
// of the whole read decides.
func (m *tsWarmMeter) wholeRead(need int64) *tsHealth {
	if o := m.swarmMeter(); o != nil {
		h := o.wholeRead(need)
		h.cached = true
		return h
	}
	h := m.health(need)
	if n := len(m.samples); n > 0 {
		rate := m.rate(tsWarmSample{}, m.samples[n-1])
		h.Rate, h.Peak = int64(rate), max(h.Peak, int64(rate))
		h.KeepsUp = rate >= m.enough
		h.Dead = m.dead && !h.KeepsUp
	}
	return h
}

// series lists each poll's read rate / TorrServer speed in MB/s ("-" = stat failed).
func (m *tsWarmMeter) series() string {
	var b strings.Builder
	prev := tsWarmSample{}
	for _, s := range m.samples {
		d := (s.at - prev.at).Seconds()
		read := 0.0
		if d > 0 {
			read = float64(s.read-prev.read) / d / float64(tsMB)
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if s.speed < 0 {
			fmt.Fprintf(&b, "%.2f/-", read)
		} else {
			fmt.Fprintf(&b, "%.2f/%.2f", read, s.speed/float64(tsMB))
		}
		prev = s
	}
	return b.String()
}

// tsWarmHooks are optional callbacks of a measurement.
type tsWarmHooks struct {
	alive func()          // once, as soon as the swarm shows peers or data
	live  func(*tsHealth) // after each poll (partial until the verdict)
}

// measureHealth warms up and measures a file from its start (see measureWarm).
func (s *Service) measureHealth(ctx context.Context, hash string, index int, need int64, window time.Duration) *tsHealth {
	return s.measureWarm(ctx, hash, index, 0, need, window, tsWarmHooks{})
}

// measureWarm reads a file from offset for up to window while polling TorrServer's stat
// every tsWarmPoll, and returns the health (nil when it could not be measured). It stops
// early once the swarm clearly keeps up or stays dead for tsWarmDeadAfter.
// Background work (see quiet.go) waits while another torrent's stream starts; a measurement
// that is interrupted by one ends with its verdict when it has one, else it starts over.
func (s *Service) measureWarm(ctx context.Context, hash string, index int, offset, need int64, window time.Duration, hooks tsWarmHooks) *tsHealth {
	if need <= 0 {
		return nil
	}
	for {
		if !s.waitQuiet(ctx, hash, "health check") {
			return nil
		}
		h, yielded := s.measureOnce(ctx, hash, index, offset, need, window, &hooks)
		if !yielded {
			return h
		}
	}
}

// measureOnce is one measurement of measureWarm; yielded reports that a starting stream (or
// another title's playback, priority.go) interrupted it before it had a verdict.
func (s *Service) measureOnce(ctx context.Context, hash string, index int, offset, need int64, window time.Duration, hooks *tsWarmHooks) (h *tsHealth, yielded bool) {
	rctx, cancel := context.WithTimeout(ctx, window)
	defer cancel() // also stops the read

	src := s.fileURL(hash, index)
	// A read of TorrServer (also a TorBox file while TorBox is paused) is judged by TorrServer's
	// own stat; a TorBox proxy read has no swarm
	swarm := strings.HasPrefix(src, s.torrServerURL()+"/")
	if swarm {
		// It pulls without limit: what TorrServer downloads meanwhile shows the line (linerate.go)
		s.line.reads.Add(1)
		defer s.line.reads.Add(-1)
	}
	stat := func(ctx context.Context, out *tsSwarmStat) error {
		sctx, cancel := context.WithTimeout(ctx, tsWarmPoll)
		defer cancel()
		if swarm {
			return s.torrServerStat(sctx, hash, out)
		}
		return s.swarmStat(sctx, hash, out)
	}
	m := newWarmMeter(need)
	// What the torrent downloaded before, so a read served from TorrServer's cache shows
	var before tsSwarmStat
	if err := stat(ctx, &before); err == nil {
		m.base = 0 // omitted while 0
		if before.Downloaded != nil {
			m.base = *before.Downloaded
		}
	}
	// A swarm verdict lets resolve answer with this file as a cached source without measuring
	// it again (tsCachedFresh)
	final := func(h *tsHealth) *tsHealth {
		if swarm && h != nil && !h.cached && (m.verdict() || h.Dead || h.sampled >= tsMeasuredSpan) {
			s.noteSwarm(hash, index, h)
		}
		return h
	}

	var read atomic.Int64
	var failed, complete atomic.Bool
	ended := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(ended)
		opened, whole := tsReadInto(rctx, src, offset, &read)
		failed.Store(!opened)
		complete.Store(whole)
	}()

	tick := time.NewTicker(tsWarmPoll)
	defer tick.Stop()
	for {
		finished := false
		select {
		case <-tick.C:
		case <-ended:
			finished = true
		case <-rctx.Done():
			finished = true
		}
		if ctx.Err() != nil || failed.Load() {
			return nil, false
		}
		sample := tsWarmSample{speed: -1, peers: -1, got: -1}
		var st tsSwarmStat
		if err := stat(ctx, &st); err == nil {
			sample.speed, sample.peers, m.seeders = st.DownloadSpeed, st.ActivePeers, st.ConnectedSeeders
			if st.Downloaded != nil {
				sample.got = *st.Downloaded
			}
		}
		sample.read, sample.at = read.Load(), time.Since(start)
		done := m.add(sample)
		if m.seenAlive && hooks.alive != nil {
			hooks.alive()
			hooks.alive = nil
		}
		verdict := m.verdict()
		if hooks.live != nil {
			h := m.health(need)
			if !verdict {
				h.partial, h.KeepsUp = true, false
			}
			hooks.live(h)
		}
		if done || finished {
			if complete.Load() && !verdict {
				return final(m.wholeRead(need)), false
			}
			return final(m.health(need)), false
		}
		// A resolve's checks turn background once it answered; another title's playback
		// stops any read of a title that gives way (priority.go)
		if other, why := s.yieldReason(ctx, hash); other != "" {
			if verdict {
				log.Printf("warm: health check of %s cut short after %s: %s %s", tsShortHash(hash), sample.at.Round(time.Second), tsShortHash(other), why)
				return final(m.health(need)), false
			}
			return nil, true
		}
	}
}

// tsReadInto streams src from offset into total until ctx ends, tsWarmMaxBytes or EOF.
// opened is false when the file could not be opened (no answer within ctx is a measured
// zero); complete reports that the read reached EOF or tsWarmMaxBytes.
func tsReadInto(ctx context.Context, src string, offset int64, total *atomic.Int64) (opened, complete bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ctx.Err() != nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false, false
	}
	buf := make([]byte, 64<<10)
	for total.Load() < tsWarmMaxBytes {
		n, err := resp.Body.Read(buf)
		total.Add(int64(n))
		if err != nil {
			return true, err == io.EOF
		}
	}
	return true, true
}

func (s *Service) swarmStat(ctx context.Context, hash string, out *tsSwarmStat) error {
	return s.torrentStat(ctx, hash, out)
}

// torrentStat decodes TorrServer's stat of a loaded torrent into out.
func (s *Service) torrentStat(ctx context.Context, hash string, out any) error {
	if ok, err := s.debridStat(hash, out); ok {
		return err // a debrid source: answered without loading it into TorrServer
	}
	return s.torrServerStat(ctx, hash, out)
}

// torrServerStat asks TorrServer itself, also about a debrid source.
func (s *Service) torrServerStat(ctx context.Context, hash string, out any) error {
	s.lineStart() // something downloads: the line sampler runs
	statURL := s.torrServerURL() + "/stream?" + url.Values{"link": {hash}, "stat": {""}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("torrserver returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
