package streaming

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Home line capacity. The swarm verdict is per torrent, so a 2160p that needs 3.5 MB/s was
// picked on a 3.2 MB/s line and stalled after its preloaded seconds. What TorrServer
// downloads in all while it pulls as fast as it can says what the line does at least: a
// sampler lists TorrServer's torrents every tsLinePoll while a measurement reads TorrServer
// or a stream plays. A poll counts while a measurement reads (it pulls without limit) and two
// torrents download or one from many peers, or while a playing torrent downloads
// tsLineReadAhead times its file's bitrate (TorrServer fills its read-ahead as fast as the
// line allows; once it is full the stream only pulls its bitrate, which says nothing). The
// median of tsLineSpan such polls in a row is an observation (TorrServer's speed counts
// bursts, so an average overshot a 28 Mbit line by 6-11 %); the estimate is the highest one,
// but it is only trusted (used, and stored, so the first open after a restart knows it) once
// an observation of another torrent set reached tsLineConfirm of it, or one set was seen
// steadily for tsLineSteady. TorBox measurement reads are observations too (one per file):
// the same line. It decays with tsLineHalfLife, so a slower line is learned too.

const (
	tsLineFit        = 0.75             // share of the line one stream can count on
	tsLinePenalty    = 100              // rank part of a release whose need is over that share (see tsLinePart)
	tsLinePoll       = 2 * time.Second  // TorrServer torrent list poll while a measurement reads it or a stream plays
	tsLineSpan       = 5                // busy polls in a row whose median is one observation
	tsLineMin        = 256 << 10        // bytes/s: below this a poll says nothing about the line
	tsLineTorrentMin = 128 << 10        // bytes/s a torrent downloads to count as loading the line
	tsLineBusyPeers  = 8                // ... or the active peers of one torrent
	tsLineReadAhead  = 1.25             // a playing torrent downloading this times its bitrate fills its read-ahead
	tsLineConfirm    = 0.8              // an observation this close to the estimate confirms it
	tsLineSteady     = 30 * time.Second // busy polls of one torrent set this long confirm it too
	tsLineHalfLife   = 4 * 24 * time.Hour
	tsLineSaveEvery  = time.Minute
)

// tsLine is the line estimate of a Service (zero value ready).
type tsLine struct {
	once    sync.Once    // loads the stored value, starts the sampler
	reads   atomic.Int32 // measurement reads of TorrServer running
	mu      sync.Mutex
	rate    float64         // bytes/s seen at
	at      time.Time       // the decay counts from here
	trusted bool            // confirmed (see above)
	sets    map[string]bool // torrent sets whose observations reached tsLineConfirm of rate
	recent  []float64       // the current run of busy polls
	runKey  string          // the torrent set of that run ...
	runFrom time.Time       // ... busy since
	dirty   bool
	savedAt time.Time
}

// current is the trusted, decayed estimate at now (0 = unknown).
func (l *tsLine) current(now time.Time) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.trusted {
		return 0
	}
	return l.decayedLocked(now)
}

func (l *tsLine) decayedLocked(now time.Time) float64 {
	if l.rate <= 0 {
		return 0
	}
	v := l.rate * math.Pow(0.5, max(now.Sub(l.at), 0).Seconds()/tsLineHalfLife.Seconds())
	if v < tsLineMin {
		return 0
	}
	return v
}

// observe takes one poll: total bytes/s over all torrents, whether it counts (busy) and the
// set of torrents that download (key). It returns the trusted estimate when it rose or became
// trusted (0 otherwise).
func (l *tsLine) observe(total float64, busy bool, key string, now time.Time) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !busy || total < tsLineMin {
		l.recent, l.runKey = l.recent[:0], ""
		return 0
	}
	if key != l.runKey {
		l.runKey, l.runFrom = key, now
	}
	l.recent = append(l.recent, total)
	if len(l.recent) > tsLineSpan {
		l.recent = l.recent[1:]
	}
	if len(l.recent) < tsLineSpan {
		return 0
	}
	sorted := append([]float64(nil), l.recent...)
	sort.Float64s(sorted)
	median := sorted[tsLineSpan/2]
	v := l.addLocked(median, key, now)
	// One set seen this long is a confirmation of its own (a first playback teaches the line)
	if !l.trusted && now.Sub(l.runFrom) >= tsLineSteady {
		v = l.addLocked(median, key+" (steady)", now)
	}
	return v
}

// note takes one observation of another source (a TorBox read of one file, key names it).
func (l *tsLine) note(rate float64, key string, now time.Time) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rate < tsLineMin {
		return 0
	}
	return l.addLocked(rate, key, now)
}

// addLocked takes an observation of the torrent set (or file) key. A higher one is the new
// estimate; one within tsLineConfirm of it confirms it once per key.
func (l *tsLine) addLocked(v float64, key string, now time.Time) float64 {
	cur := l.decayedLocked(now)
	if v < tsLineConfirm*cur || (v <= cur && l.sets[key]) {
		return 0
	}
	// The sets that saw at least tsLineConfirm of the new value still confirm it
	if v > cur && cur < tsLineConfirm*v || l.sets == nil {
		l.sets = map[string]bool{}
	}
	l.sets[key] = true
	if v > cur {
		l.rate, l.at = v, now
	}
	if l.trusted || len(l.sets) >= 2 {
		changed := v > cur || !l.trusted
		l.trusted, l.dirty = true, l.dirty || changed
		if changed {
			return l.decayedLocked(now)
		}
	}
	return 0
}

// load sets the stored estimate (before any poll); only trusted ones are stored.
func (l *tsLine) load(rate float64, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate, l.at, l.trusted = rate, at, true
}

// toSave returns the estimate to store when it changed and the last save is old enough.
func (l *tsLine) toSave(now time.Time) (rate float64, at time.Time, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty || !l.trusted || now.Sub(l.savedAt) < tsLineSaveEvery {
		return 0, time.Time{}, false
	}
	l.dirty, l.savedAt = false, now
	return l.rate, l.at, true
}

// tsLineTorrent is the part of TorrServer's torrent list the sampler reads.
type tsLineTorrent struct {
	Hash          string  `json:"hash"`
	DownloadSpeed float64 `json:"download_speed"` // bytes/s
	ActivePeers   int     `json:"active_peers"`
}

// tsLineLoad sums one torrent list: total bytes/s, whether the poll counts (see above;
// measuring: a measurement reads TorrServer; needs: bytes/s of the playing torrents' files)
// and the set of torrents that download.
func tsLineLoad(list []tsLineTorrent, measuring bool, needs map[string]float64) (total float64, busy bool, key string) {
	var hashes []string
	many, readAhead := false, false
	for _, t := range list {
		if t.DownloadSpeed <= 0 {
			continue
		}
		total += t.DownloadSpeed
		h := strings.ToLower(t.Hash)
		if need := needs[h]; need > 0 && t.DownloadSpeed >= tsLineReadAhead*need {
			readAhead = true
		}
		if t.DownloadSpeed >= tsLineTorrentMin {
			hashes = append(hashes, h)
			many = many || t.ActivePeers >= tsLineBusyPeers
		}
	}
	sort.Strings(hashes)
	busy = readAhead || (measuring && (many || len(hashes) >= 2))
	return total, busy, strings.Join(hashes, ",")
}

// lineState says whether a measurement reads TorrServer (while a title is warm or a stream
// plays) and which torrents play.
func (s *Service) lineState() (measuring bool, playing []string) {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	for h := range s.playing {
		playing = append(playing, strings.ToLower(h))
	}
	return s.line.reads.Load() > 0 && (len(s.warms) > 0 || len(s.playing) > 0), playing
}

// playNeeds is the bytes/s the probed files of the playing torrents need (their highest
// bitrate; a torrent without a probed bitrate is left out).
func (s *Service) playNeeds(playing []string) map[string]float64 {
	if len(playing) == 0 {
		return nil
	}
	needs := map[string]float64{}
	for _, h := range playing {
		needs[h] = 0
	}
	s.probeCache.Range(func(k, v any) bool {
		key, _ := k.(string)
		h, _, _ := strings.Cut(key, ":")
		p, _ := v.(*tsProbe)
		if _, ok := needs[h]; ok && p != nil {
			if br, err := strconv.ParseFloat(p.Format.BitRate, 64); err == nil && br > 0 {
				needs[h] = max(needs[h], br/8)
			}
		}
		return true
	})
	return needs
}

// lineRate is what the home line downloads at least (bytes/s); known is false until a
// trusted estimate exists.
func (s *Service) lineRate() (bytesPerSec float64, known bool) {
	s.lineStart()
	v := s.line.current(time.Now())
	return v, v > 0
}

// lineFor is the line q may count on: the one it was ranked with, or more once the line
// showed more since.
func (s *Service) lineFor(q tsResolveQuery) float64 {
	v, _ := s.lineRate()
	return max(v, q.line)
}

// lineTorBox takes the rate a TorBox measurement of a file read: the same home line, and CDN
// reads are bound by it, not by a swarm (a cold connection still reads low, so it needs
// confirming like any other observation).
func (s *Service) lineTorBox(hash string, rate int64) {
	s.lineStart()
	now := time.Now()
	before := s.line.current(now)
	if v := s.line.note(float64(rate), "torbox:"+hash, now); v > 0 {
		log.Printf("line: the line does at least %.2f MB/s (TorBox read; was %.2f)", v/float64(tsMB), before/float64(tsMB))
	}
}

// torBoxCarries: TorBox recently delivered enough for a stream of need bytes/s.
func (s *Service) torBoxCarries(need int64) bool {
	if s.debrid == nil {
		return false
	}
	best := float64(s.debrid.line.best(time.Now()))
	return best > 0 && !tsOverLine(need, best)
}

// lineStart loads the stored estimate and starts the sampler, once.
func (s *Service) lineStart() {
	s.line.once.Do(func() {
		if s.db != nil {
			if row, err := loadLineRate(s.db); err != nil {
				log.Printf("line: load: %v", err)
			} else if row != nil && row.Rate > 0 {
				s.line.load(float64(row.Rate), row.ObservedAt)
			}
		}
		go s.lineLoop()
	})
}

func (s *Service) lineLoop() {
	tick := time.NewTicker(tsLinePoll)
	defer tick.Stop()
	for range tick.C {
		now := time.Now()
		var list []tsLineTorrent
		ok := false
		measuring, playing := s.lineState()
		if measuring || len(playing) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), tsLinePoll)
			ok = s.torrServerPost(ctx, map[string]any{"action": "list"}, &list) == nil
			cancel()
		}
		total, busy, key := tsLineLoad(list, measuring, s.playNeeds(playing))
		before := s.line.current(now)
		if v := s.line.observe(total, busy && ok, key, now); v > 0 && v >= 1.1*before {
			log.Printf("line: the line does at least %.2f MB/s (was %.2f)", v/float64(tsMB), before/float64(tsMB))
		}
		if rate, at, save := s.line.toSave(now); save && s.db != nil {
			if err := saveLineRate(s.db, &LineRate{Rate: int64(rate), ObservedAt: at}); err != nil {
				log.Printf("line: save: %v", err)
			}
		}
	}
}

// tsOverLine: a stream of need bytes/s is more than the known line (bytes/s, 0 = unknown)
// carries next to everything else.
func tsOverLine(need int64, line float64) bool {
	return line > 0 && need > 0 && float64(need) > tsLineFit*line
}

// tsCandidateNeed is the bytes/s a release needs by its size (0 for packs and unknown sizes).
func tsCandidateNeed(c tsCandidate, q tsResolveQuery) int64 {
	if c.kind >= 2 {
		return 0
	}
	return tsNeedRate(c.raw.Size, q.runtime, q.mediaType)
}

// tsLinePart ranks a release whose need is over the line (q.line known) after the ones that
// fit; of those that do not, the lower need first.
func tsLinePart(need int64, line float64) float64 {
	if !tsOverLine(need, line) {
		return 0
	}
	v := -tsLinePenalty - 60*math.Log2(float64(need)/(tsLineFit*line))
	return math.Round(v*10) / 10
}

// tsLogOverLine logs the releases that would have been checked without the line part.
func tsLogOverLine(q tsResolveQuery, raw []JackettResult) {
	if q.line <= 0 {
		return
	}
	all := tsRankAll(raw, q)
	sort.SliceStable(all, func(i, j int) bool { return all[i].score-all[i].parts.Line > all[j].score-all[j].parts.Line })
	for _, c := range all[:min(len(all), tsResolveMaxCheck)] {
		if c.parts.Line < 0 && !tsAvoided(c, q) {
			log.Printf("resolve %q: %q %s: ranked after the ones that fit", q.title, c.raw.Title, tsLineNote(tsCandidateNeed(c, q), q.line))
		}
	}
}

// overLine: r's file needs more than the line q may count on carries. A TorBox file is
// judged by what TorBox delivered lately when that is known.
func (s *Service) overLine(r *tsResolveResult, q tsResolveQuery) bool {
	need := tsNeedOf(r, q)
	if r.Debrid && s.torBoxCarries(need) {
		return false
	}
	return tsOverLine(need, s.lineFor(q))
}

// tsNeedOf is the bytes/s r's file needs: measured (with the runtime its check knew), else by
// its size and q's runtime.
func tsNeedOf(r *tsResolveResult, q tsResolveQuery) int64 {
	if r.Health != nil && r.Health.Need > 0 {
		return r.Health.Need
	}
	return tsNeedRate(r.Size, q.runtime, q.mediaType)
}

// tsLineNote is a log fragment: "needs 3.50 MB/s, the line does ~3.20 MB/s".
func tsLineNote(need int64, line float64) string {
	return fmt.Sprintf("needs %.2f MB/s, the line does ~%.2f MB/s", float64(need)/float64(tsMB), line/float64(tsMB))
}
