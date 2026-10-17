package subtitles

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Live timing: the play streams' dialogue audio (streaming's speech tap, wired in main.go) goes
// through the same speech detector as the window reads, so every stretch the viewer plays (and
// the player buffers ahead) becomes reference windows with exact file times, without reading
// the file again. The windows lie on a 60 s grid of the file, so the stretches of several play
// streams (a seek starts a new one; ranges overlap, in any order) merge. Every 30 s of new
// audio re-fits the subtitles served for the file with all windows, read and live. Until the
// file is covered such results stay provisional: answers are partial while it plays, so the
// players keep asking (every 15 s) and take the better timing. While a subtitle served in the
// first minutes of a viewing is untimed, every 10 s of audio re-fits it (at most every 5 s), so
// its first timing comes as soon as the windows allow.

const (
	liveWindowFrames  = int(syncWindowLen * syncRate) // frames per grid window
	liveFrameSamples  = syncSampleRate / syncRate     // PCM samples per frame
	liveDoneShare     = 0.9                           // a window is finished once this share of it was heard
	liveMinRun        = 20 * syncRate                 // frames an unfinished window needs to count
	liveRefitGrowth   = 30.0                          // seconds of new audio that start a re-fit
	liveMinCover      = 120.0                         // seconds of reference a fit needs (2 windows)
	liveMinLines      = 30                            // reference line starts a fit with live windows needs (alignLive)
	liveSplitWindows  = 4                             // windows from which a live fit may change its offset once
	liveMinDrift      = 10.0                          // seconds another frame rate drifts over contiguous windows to be tried
	liveEarlyWindows  = 2                             // own-track windows read from the play start of a file above the byte budget
	liveCompleteShare = 0.85                          // share of the file's windows that ends live timing
	livePlayingFor    = 90 * time.Second              // a file counts as playing this long after its last tap
	liveFilesKept     = 16                            // files whose live reference stays in memory
	liveServedFor     = 10 * time.Minute              // subtitles served this recently are re-fitted
	liveGrace         = 30 * time.Second              // first answers stay partial (the tap may open later)
	liveSessionGap    = 5 * time.Minute               // a request after this long starts a new session
	liveRefitDelay    = 3 * time.Second               // batches a burst of audio (syncer.refitDelay)
	liveRefitGap      = 15 * time.Second              // between re-fits of one file (syncer.refitGap)
	livePendingGrowth = 10.0                          // seconds of new audio that re-fit a file with an untimed served subtitle
	livePendingGap    = 5 * time.Second               // between re-fits of such a file (syncer.pendingGap)
	livePendingFor    = 5 * time.Minute               // of a viewing, in which an untimed subtitle is re-fitted that often
	liveNearFor       = 2 * time.Minute               // a player position this recent counts for the move log
	liveNearAhead     = 300.0                         // seconds ahead of the viewer whose cue moves are logged
)

// liveFile is the live reference of one file. Lock order: syncer.mu before liveFile.mu.
type liveFile struct {
	fk    string
	hash  string
	index int

	mu       sync.Mutex
	chunks   map[int][]uint8 // unfinished grid windows: frame energies (0 not heard, else 1 + 2·dB)
	covered  map[int]int     // frames heard per unfinished window
	done     map[int]liveWin // finished windows
	own      map[int][]uint8 // unfinished own-track windows (sync_owncues.go): ownRead|ownShows|ownStarts per frame
	ownRead  map[int]int     // frames read per unfinished own-track window
	ownDone  map[int]liveWin // finished own-track windows
	ownTaps  int
	ownGrown float64  // seconds of own track read since the last re-fit was asked for
	ownPic   bool     // the cue tap reads a picture (PGS) track
	check    ownCheck // the own track against the speech (sync_owncheck.go)
	checkN   int      // stretches that check compared (it is redone when there are more)
	sameCut  string   // the file of the same cut whose own-track windows the fits use (sameCutWindows; logged once)
	duration float64
	complete bool
	taps     int
	lastTap  time.Time
	grown    float64 // seconds heard since the last re-fit was asked for
	heard    float64 // seconds received (logs)
	dirty    bool    // finished windows not on disk yet
	refit    bool    // a re-fit loop runs
	again    bool    // more audio arrived meanwhile
	lastFit  time.Time
	// pending: a subtitle served in a viewing's first minutes is untimed (re-fit sooner)
	pending bool
	wake    chan struct{} // the re-fit loop's wait is computed again
}

// liveWin is a finished window: its speech frames (own-track windows: frames a cue shows), one
// bit each, and for own-track windows the frames where a cue starts.
type liveWin struct {
	Bits   []byte `json:"bits"`
	Frames int    `json:"n"`
	Starts []int  `json:"starts,omitempty"`
}

type liveSaved struct {
	Windows  map[int]liveWin `json:"windows"`
	Own      map[int]liveWin `json:"own,omitempty"`
	Complete bool            `json:"complete,omitempty"`
	Check    *ownSaved       `json:"check,omitempty"` // the own-track check, once decided
}

func newLiveFile(hash string, index int) *liveFile {
	return &liveFile{fk: syncFileKey(hash, index), hash: strings.ToLower(hash), index: index,
		chunks: map[int][]uint8{}, covered: map[int]int{}, done: map[int]liveWin{},
		own: map[int][]uint8{}, ownRead: map[int]int{}, ownDone: map[int]liveWin{}, wake: make(chan struct{}, 1)}
}

// growStep is the new audio (or own track) in seconds that re-fits the file (f.mu held).
func (f *liveFile) growStep() float64 {
	if f.pending {
		return livePendingGrowth
	}
	return liveRefitGrowth
}

// setPending marks whether an untimed subtitle is served in a viewing's first minutes.
func (f *liveFile) setPending(p bool) {
	f.mu.Lock()
	changed := f.pending != p
	f.pending = p
	f.mu.Unlock()
	if changed {
		f.nudge()
	}
}

// nudge makes a waiting re-fit loop compute its wait again.
func (f *liveFile) nudge() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// windowFrames is the length of grid window w (the last one ends with the file).
func (f *liveFile) windowFrames(w int) int {
	n := liveWindowFrames
	if f.duration > 0 {
		n = min(n, max(int(math.Round((f.duration-float64(w)*syncWindowLen)*syncRate)), 1))
	}
	return n
}

func (f *liveFile) setDuration(d float64) {
	if d <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.duration == d {
		return
	}
	f.duration = d
	if f.taps == 0 { // a running stream finishes its windows itself
		for w := range f.chunks {
			f.finishIfHeard(w)
		}
	}
	if f.ownTaps == 0 {
		for w := range f.own {
			f.finishOwn(w)
		}
	}
	f.checkComplete()
}

// checkComplete: the file is covered once enough grid windows are finished, of either kind.
func (f *liveFile) checkComplete() {
	n := len(f.done)
	for w := range f.ownDone {
		if _, ok := f.done[w]; !ok {
			n++
		}
	}
	if f.duration > 0 && float64(n) >= liveCompleteShare*math.Ceil(f.duration/syncWindowLen) {
		f.complete = true
	}
}

func (f *liveFile) finishIfHeard(w int) {
	n := f.windowFrames(w)
	if float64(f.covered[w]) < liveDoneShare*float64(n) {
		return
	}
	f.done[w] = liveWin{Bits: packBits(classifyEnergies(f.chunks[w][:n])), Frames: n}
	delete(f.chunks, w)
	delete(f.covered, w)
	f.dirty = true
}

type liveFrame struct {
	at int // frame of the file
	v  uint8
}

// add takes frame energies of a play stream whose previous batch ended in window prev (-1:
// none); it reports whether a re-fit is due.
func (f *liveFile) add(frames []liveFrame, prev int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	var touched []int
	if prev >= 0 {
		touched = append(touched, prev)
	}
	for _, fr := range frames {
		if fr.at < 0 {
			continue
		}
		w, i := fr.at/liveWindowFrames, fr.at%liveWindowFrames
		if _, ok := f.done[w]; ok {
			continue
		}
		c := f.chunks[w]
		if c == nil {
			c = make([]uint8, liveWindowFrames)
			f.chunks[w] = c
		}
		if c[i] == 0 {
			f.covered[w]++
			f.grown += 1.0 / syncRate
		}
		c[i] = fr.v
		if len(touched) == 0 || touched[len(touched)-1] != w {
			touched = append(touched, w)
		}
	}
	// A window is finished once the stream has passed it (or heard all of it), so its end is in
	for i, w := range touched {
		if _, ok := f.chunks[w]; ok && (i+1 < len(touched) || f.covered[w] == f.windowFrames(w)) {
			f.finishIfHeard(w)
		}
	}
	f.checkComplete()
	f.heard += float64(len(frames)) / syncRate
	if f.grown >= f.growStep() {
		f.grown = 0
		return true
	}
	return false
}

// windows returns the live reference windows (finished ones, and unfinished ones with at least
// liveMinRun frames heard in one stretch) and the seconds they cover. Own-track windows come
// first; a speech window of the same stretch is left out.
func (f *liveFile) windows() ([]refWindow, float64) { return f.windowsOf(true) }

// windowsOf is windows, without own-track windows unless withOwn.
func (f *liveFile) windowsOf(withOwn bool) ([]refWindow, float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []refWindow
	if withOwn {
		out = f.ownWindows()
	}
	own := len(out)
	for w, d := range f.done {
		lw := refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)}
		if !overlapsHalf(lw, out[:own]) {
			out = append(out, lw)
		}
	}
	for w, c := range f.chunks {
		first, last := -1, -1
		for i, v := range c {
			if v != 0 {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 || f.covered[w] < liveMinRun || float64(f.covered[w]) < liveDoneShare*float64(last-first+1) {
			continue
		}
		lw := refWindow{Start: float64(w)*syncWindowLen + float64(first)/syncRate, Kind: "live",
			Speech: classifyEnergies(c[first : last+1])}
		if !overlapsHalf(lw, out[:own]) {
			out = append(out, lw)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, windowSeconds(out)
}

func windowSeconds(ws []refWindow) float64 {
	s := 0.0
	for _, w := range ws {
		s += float64(len(w.Speech)) / syncRate
	}
	return s
}

// classifyEnergies runs the speech detector over stored frame energies; frames not heard (a
// small gap) repeat the last heard one.
func classifyEnergies(c []uint8) []byte {
	db := make([]float64, len(c))
	first := -1
	last := 0.0
	for i, v := range c {
		if v != 0 {
			last = float64(v-1) / 2
			if first < 0 {
				first = i
				for j := range i {
					db[j] = last
				}
			}
		}
		db[i] = last
	}
	return speechOf(db)
}

func packBits(speech []byte) []byte {
	out := make([]byte, (len(speech)+7)/8)
	for i, v := range speech {
		if v != 0 {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

func unpackBits(bits []byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		if i/8 < len(bits) && bits[i/8]&(1<<(i%8)) != 0 {
			out[i] = 1
		}
	}
	return out
}

// SpeechSink takes the PCM of one play stream (streaming's speech tap): 8 kHz mono s16le,
// sample 0 at start seconds of the file. Write and Close come from one goroutine.
type SpeechSink struct {
	y     *syncer
	f     *liveFile
	start float64
	base  int   // file frame of the stream's first frame
	frame int64 // stream frame being summed
	sum   float64
	n     int
	prev  float64
	next  int64 // byte offset expected next
	heard int64 // bytes received
	gaps  int64 // bytes missing (dropped by the tap)
	buf   []liveFrame
	lastW int // grid window of the last frame (-1 none)
}

// Write takes PCM from byte offset at of the stream (a later offset than expected is a gap).
func (s *SpeechSink) Write(at int64, pcm []byte) {
	if at != s.next {
		if at > s.next {
			s.gaps += at - s.next
		}
		s.frame, s.prev = -1, 0
	}
	s.next = at + int64(len(pcm))
	s.heard += int64(len(pcm))
	first := at / 2
	s.buf = s.buf[:0]
	for i := 0; i+1 < len(pcm); i += 2 {
		fr := (first + int64(i/2)) / liveFrameSamples
		if fr != s.frame {
			s.frame, s.sum, s.n = fr, 0, 0
		}
		x := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		d := x - 0.97*s.prev
		s.prev = x
		s.sum += d * d
		if s.n++; s.n == liveFrameSamples {
			db := 10 * math.Log10(s.sum/liveFrameSamples+1)
			s.buf = append(s.buf, liveFrame{at: s.base + int(fr), v: uint8(min(255, 1+math.Round(db*2)))})
		}
	}
	if len(s.buf) == 0 {
		return
	}
	grown := s.f.add(s.buf, s.lastW)
	s.lastW = s.buf[len(s.buf)-1].at / liveWindowFrames
	if grown {
		s.y.onGrow(s.f)
	}
}

// Close ends the stream: logs what it brought and re-fits the file's subtitles.
func (s *SpeechSink) Close() {
	f := s.f
	f.mu.Lock()
	f.taps--
	f.lastTap = time.Now()
	for w := range f.chunks {
		f.finishIfHeard(w)
	}
	f.checkComplete()
	done, complete := len(f.done), f.complete
	f.mu.Unlock()
	if s.heard > 0 {
		_, covered := f.windows()
		extra := ""
		if complete {
			extra = " (complete)"
		}
		log.Printf("subtitles: live audio of %s:%d from %.0fs: %.0f s heard, %.0f s missing; %.0f s of the file covered, %d windows finished%s",
			f.hash[:8], f.index, s.start, float64(s.heard)/(2*syncSampleRate), float64(s.gaps)/(2*syncSampleRate),
			covered, done, extra)
	}
	s.y.onGrow(f)
}

// OpenSpeech starts listening to a play stream of the file from start seconds (streaming's
// speech tap, wired in main.go); nil when the file needs no live timing.
func (s *Service) OpenSpeech(hash string, index int, start float64) *SpeechSink {
	return s.sync.openSpeech(hash, index, start)
}

func (y *syncer) openSpeech(hash string, index int, start float64) *SpeechSink {
	if !y.svc.cfg.LiveTiming || !infoHashRe.MatchString(hash) || index <= 0 || !(start >= 0) || start > syncMaxUnknown {
		return nil
	}
	fk := syncFileKey(hash, index)
	y.mu.Lock()
	full := y.noLive[fk]
	y.mu.Unlock()
	if full {
		return nil
	}
	f := y.liveFile(hash, index)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.complete {
		return nil
	}
	f.taps++
	f.lastTap = time.Now()
	go y.readEarly(hash, index, start)
	return &SpeechSink{y: y, f: f, start: start, base: int(math.Round(start * syncRate)), frame: -1, lastW: -1}
}

// readEarly reads, once per file, the cues of its own subtitle track in the first
// liveEarlyWindows minutes from where playback starts, when the file reads no windows (above
// the byte budget): the player fetches those bytes anyway (from TorrServer's cache), and exact
// lines fit sooner and better than the speech heard so far (Avatar: Seven Havens S01E02
// replayed: the first fit after 120 s of audio instead of 180 s, the first 5 minutes within
// 0.3 s 81 % instead of 66 %).
func (y *syncer) readEarly(hash string, index int, start float64) {
	fk := syncFileKey(hash, index)
	y.mu.Lock()
	if p, known := y.probes[fk]; y.early[fk] || known && p.OwnSub < 0 {
		y.mu.Unlock()
		return
	}
	y.early[fk] = true
	y.mu.Unlock()
	time.Sleep(syncReadDelay) // not during the playback start
	ctx, cancel := context.WithTimeout(context.Background(), liveEarlyWindows*syncWindowTimeout+syncLineWait)
	defer cancel()
	ctx = y.withLineBudget(ctx)
	st, release := y.refState(fk)
	defer release()
	var r syncRef
	ok := false
	for try := 0; !ok; try++ {
		st.mu.Lock()
		if ok = y.loadRef(fk, st); ok {
			r = *st.ref
		}
		st.mu.Unlock()
		if !ok && try == 12 { // no timing asked for in a minute: the next stream tries again
			y.mu.Lock()
			delete(y.early, fk)
			y.mu.Unlock()
			return
		}
		if !ok {
			time.Sleep(5 * time.Second)
		}
	}
	if f := y.liveFile(hash, index); !r.Skipped || len(r.Windows) > 0 || f.ownTapping() || f.ownState() == ownOther {
		return // (the play stream's cue tap brings those cues; not timed to the file: no use)
	}
	url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
	if !r.Probe.subsKnown() {
		p, err := y.probeFile(ctx, hash, index, url)
		if err != nil {
			return
		}
		r.Probe = y.updateRefSubs(fk, st, p)
	}
	if r.Probe.OwnSub < 0 {
		return
	}
	select {
	case y.sem <- struct{}{}:
		defer func() { <-y.sem }()
	case <-ctx.Done():
		return
	}
	at := math.Floor(start)
	if r.Probe.Duration > 0 {
		at = math.Max(0, math.Min(at, r.Probe.Duration-liveEarlyWindows*syncWindowLen))
	}
	var got []refWindow
	for i := range liveEarlyWindows {
		if !y.waitLine(ctx, hash) {
			break
		}
		wctx, wcancel := context.WithTimeout(ctx, syncWindowTimeout)
		w, err := y.readSubs(wctx, url, r.Probe, at+float64(i)*syncWindowLen, syncWindowLen)
		wcancel()
		if err == nil && w.Kind == "subs" {
			got = append(got, w)
		}
	}
	if len(got) == 0 {
		return
	}
	st.mu.Lock()
	if y.loadRef(fk, st) {
		r = *st.ref
		r.Windows = append(append([]refWindow(nil), r.Windows...), got...)
		y.saveRef(fk, st, r)
	}
	st.mu.Unlock()
	log.Printf("subtitles: timing reference of %s:%d: %d early windows of its own %s track from %.0fs, %d MB read", hash[:8], index,
		len(got), r.Probe.ownKind(), at, readBytes(got)>>20)
	y.onGrow(y.liveFile(hash, index))
}

// liveFile returns the file's live reference (loaded from disk once).
func (y *syncer) liveFile(hash string, index int) *liveFile {
	fk := syncFileKey(hash, index)
	y.mu.Lock()
	f := y.lives[fk]
	d := y.probes[fk].Duration
	y.mu.Unlock()
	if f != nil {
		return f
	}
	f = newLiveFile(hash, index)
	f.duration = d
	if b, ok := y.svc.files.read("synclive", fk, ".json", 0); ok {
		var saved liveSaved
		if json.Unmarshal(b, &saved) == nil {
			for w, win := range saved.Windows {
				if w >= 0 && win.Frames > 0 && win.Frames <= liveWindowFrames {
					f.done[w] = win
				}
			}
			for w, win := range saved.Own {
				if w >= 0 && win.Frames > 0 && win.Frames <= liveWindowFrames {
					f.ownDone[w] = win
				}
			}
			f.complete = saved.Complete
			if c := saved.Check; c != nil && (c.State == ownTimed || c.State == ownOther) {
				f.check = ownCheck{State: c.State, Tight: c.Tight && c.State == ownTimed, Pairs: c.Pairs}
				f.checkN = c.Pairs
			}
		}
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if o := y.lives[fk]; o != nil {
		return o
	}
	if len(y.lives) >= liveFilesKept {
		y.evictLive()
	}
	y.lives[fk] = f
	return f
}

// evictLive drops the idle live references used longest ago (y.mu held).
func (y *syncer) evictLive() {
	type idle struct {
		fk string
		at time.Time
	}
	var list []idle
	for fk, f := range y.lives {
		f.mu.Lock()
		if f.taps == 0 && f.ownTaps == 0 && !f.refit {
			list = append(list, idle{fk, f.lastTap})
		}
		f.mu.Unlock()
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	for _, e := range list {
		if len(y.lives) < liveFilesKept {
			break
		}
		delete(y.lives, e.fk)
	}
}

// saveLive keeps the finished windows on disk (only files with a timed subtitle get here).
func (y *syncer) saveLive(f *liveFile) {
	f.mu.Lock()
	if !f.dirty {
		f.mu.Unlock()
		return
	}
	saved := liveSaved{Windows: make(map[int]liveWin, len(f.done)), Complete: f.complete}
	for w, d := range f.done {
		saved.Windows[w] = d
	}
	if len(f.ownDone) > 0 {
		saved.Own = make(map[int]liveWin, len(f.ownDone))
		for w, d := range f.ownDone {
			saved.Own[w] = d
		}
	}
	if f.check.State != "" {
		saved.Check = &ownSaved{State: f.check.State, Tight: f.check.Tight, Pairs: f.check.Pairs}
	}
	f.dirty = false
	f.mu.Unlock()
	if b, err := json.Marshal(saved); err == nil {
		if err := y.svc.files.write("synclive", f.fk, ".json", b); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
	}
}

// livePlaying reports whether a play stream of the file runs or ended moments ago.
func (y *syncer) livePlaying(fk string) bool {
	y.mu.Lock()
	f := y.lives[fk]
	y.mu.Unlock()
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.taps > 0 || f.ownTaps > 0 || time.Since(f.lastTap) < livePlayingFor
}

// scheduleRefit re-fits the file's served subtitles soon (one loop per file).
func (y *syncer) scheduleRefit(f *liveFile) {
	f.mu.Lock()
	if f.refit {
		f.again = true
		f.mu.Unlock()
		return
	}
	f.refit = true
	f.mu.Unlock()
	go func() {
		for {
			y.waitRefit(f)
			y.refitFile(f)
			f.mu.Lock()
			f.lastFit = time.Now()
			if !f.again {
				f.refit = false
				f.mu.Unlock()
				return
			}
			f.again = false
			f.mu.Unlock()
		}
	}()
}

// waitRefit waits refitDelay (a burst of audio is one re-fit) and refitGap since the file's last
// re-fit (pendingGap while it has an untimed subtitle); a nudge computes the wait again.
func (y *syncer) waitRefit(f *liveFile) {
	start := time.Now()
	for {
		f.mu.Lock()
		gap := y.refitGap
		if f.pending {
			gap = y.pendingGap
		}
		due := start.Add(y.refitDelay)
		if d := f.lastFit.Add(gap); d.After(due) {
			due = d
		}
		f.mu.Unlock()
		wait := time.Until(due)
		if wait <= 0 {
			return
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
			return
		case <-f.wake:
			t.Stop()
		}
	}
}

type servedSub struct {
	key, inner, fk string
	hash           string
	index          int
	first, last    time.Time
	// the player's position at its last request (seconds of the file, 0 unknown)
	at float64
}

// noteServed records a file request of a subtitle served for a file at the player's position t;
// first is true for the first request of a viewing session, fresh during its first liveGrace.
func (y *syncer) noteServed(key, inner, hash string, index int, t float64) (first, fresh bool) {
	now := time.Now()
	y.mu.Lock()
	defer y.mu.Unlock()
	s := y.served[key]
	if s == nil || now.Sub(s.last) > liveSessionGap {
		if len(y.served) > 500 {
			for k, o := range y.served {
				if now.Sub(o.last) > liveServedFor {
					delete(y.served, k)
				}
			}
		}
		s = &servedSub{key: key, inner: inner, fk: syncFileKey(hash, index), hash: strings.ToLower(hash), index: index, first: now}
		y.served[key] = s
		first = true
	}
	s.last = now
	if t > 0 {
		s.at = t
	}
	return first, now.Sub(s.first) < liveGrace
}

// viewerAt is where the player of a served subtitle is now (seconds of the file), from its last
// request with a position; false when unknown or not recent.
func (y *syncer) viewerAt(key string) (float64, bool) {
	y.mu.Lock()
	s := y.served[key]
	var at float64
	var since time.Duration
	if s != nil {
		at, since = s.at, time.Since(s.last)
	}
	y.mu.Unlock()
	if s == nil || at <= 0 || since > liveNearFor {
		return 0, false
	}
	if y.livePlaying(s.fk) {
		at += since.Seconds() // it played on since (a pause is not known here)
	}
	return at, true
}

// servedCues is what a player of key shows with result m (the subtitle as it is while untimed).
func (y *syncer) servedCues(key string, base []byte, m *syncMeta) []liveCue {
	if !untimedMeta(m) {
		if a, ok := y.result(key, base); ok {
			return vttCues(a.VTT)
		}
	}
	return vttCues(base)
}

// logNear logs how a change of what is served moved the cues within liveNearAhead seconds ahead
// of the viewer (the player's last position), so slips near the viewer show in the log.
func (y *syncer) logNear(key, inner, hash string, index int, before, after []liveCue) {
	at, ok := y.viewerAt(key)
	if !ok {
		return
	}
	mv := movesNear(before, after, at, at+liveNearAhead)
	if mv.moved == 0 && mv.other == 0 {
		return
	}
	other := ""
	if mv.other > 0 {
		other = fmt.Sprintf(", %d lines of another subtitle", mv.other)
	}
	log.Printf("subtitles: timing of %s on %s:%d moved %d of %d cues within %.0f s of the viewer at %.0fs: max %+.2fs, median %+.2fs%s",
		shortSubID(inner), hash[:8], index, mv.moved, mv.cues, liveNearAhead, at, mv.max, mv.median, other)
}

type cueMoves struct {
	cues, moved, other int     // cues in the range, moved by 0.1 s or more, without a partner (another subtitle)
	max, median        float64 // signed: the largest move in size, the median move of the moved ones
}

// movesNear compares two served cue lists in [from, to) (old or new start): each cue is matched to
// the old cue of the same text that started nearest to it.
func movesNear(before, after []liveCue, from, to float64) cueMoves {
	old := map[string][]float64{}
	for _, c := range before {
		old[c.text] = append(old[c.text], c.start)
	}
	var mv cueMoves
	var moves []float64
	for _, c := range after {
		starts := old[c.text]
		best, found := 0.0, false
		for _, s := range starts {
			if !found || math.Abs(s-c.start) < math.Abs(best-c.start) {
				best, found = s, true
			}
		}
		in := c.start >= from && c.start < to
		if found {
			in = in || best >= from && best < to
		}
		if !in {
			continue
		}
		mv.cues++
		if !found {
			mv.other++
			continue
		}
		if d := c.start - best; math.Abs(d) >= 0.1 {
			mv.moved++
			moves = append(moves, d)
			if math.Abs(d) > math.Abs(mv.max) {
				mv.max = d
			}
		}
	}
	if len(moves) > 0 {
		sort.Float64s(moves)
		mv.median = moves[len(moves)/2]
	}
	return mv
}

// untimedMeta reports a result served as the subtitle is: none yet, a provisional one that does
// not fit (yet), or a pre-timed one its played audio did not back yet.
func untimedMeta(m *syncMeta) bool {
	return m == nil || m.Live && (m.Pre || !m.fits() && m.State != syncStateReplaced)
}

// markPending notes that the file serves an untimed subtitle in a viewing's first minutes, and
// re-fits it soon when it has live windows.
func (y *syncer) markPending(hash string, index int) {
	f := y.liveFile(hash, index)
	f.setPending(true)
	if _, covered := f.windows(); covered > 0 {
		y.onGrow(f)
	}
}

func (y *syncer) servedFor(fk string) []servedSub {
	y.mu.Lock()
	defer y.mu.Unlock()
	var out []servedSub
	for _, s := range y.served {
		if s.fk == fk && time.Since(s.last) < liveServedFor {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// refitFile times the subtitles served for the file again with every window it has.
func (y *syncer) refitFile(f *liveFile) {
	subs := y.servedFor(f.fk)
	if len(subs) == 0 {
		f.setPending(false)
		return
	}
	y.refitSubs(f, subs)
	pending := false
	for _, s := range subs {
		if time.Since(s.first) < livePendingFor && untimedMeta(y.meta(s.key)) {
			pending = true
		}
	}
	f.setPending(pending)
}

func (y *syncer) refitSubs(f *liveFile, subs []servedSub) {
	y.liveSem <- struct{}{}
	defer func() { <-y.liveSem }()
	ctx, cancel := context.WithTimeout(context.Background(), syncJobLimit)
	defer cancel()
	ctx = y.withLineBudget(ctx)
	ref := y.liveRef(ctx, f.hash, f.index)
	for _, s := range subs {
		if m := y.meta(s.key); m != nil && !m.Live {
			continue // final
		}
		// A running first timing takes the live windows too once its read is done. While the
		// read runs (ref skipped) the live windows time it alone: a file opened mid-way (another
		// source after a stall) is not served untimed for the whole read.
		if !ref.Skipped && y.svc.flights.running("sync|"+s.key) {
			continue
		}
		mu := y.decideLock(s.key)
		if !mu.TryLock() {
			continue
		}
		if base, err := y.svc.File(ctx, s.inner); err == nil {
			if y.meta(s.key) == nil {
				// a fit of the same cut that landed since the timing started (sameCut)
				if c, ok := y.sameCutFit(ctx, s.inner, f.hash, f.index, base, ref.Probe); ok {
					y.storeSameCut(s.key, s.inner, f.hash, f.index, base, c)
				}
			}
			y.decide(ctx, s.inner, f.hash, f.index, base, ref, false)
		}
		mu.Unlock()
	}
}

// liveRef is the file's read reference when there is one, without waiting for a read that
// runs (then, like for files above the byte budget, only the probe; Busy).
func (y *syncer) liveRef(ctx context.Context, hash string, index int) syncRef {
	fk := syncFileKey(hash, index)
	st, release := y.refState(fk)
	busy := true
	if st.mu.TryLock() {
		busy = false
		ok := y.loadRef(fk, st)
		var r syncRef
		if ok {
			r = *st.ref
		}
		st.mu.Unlock()
		if ok {
			release()
			return r
		}
	}
	release()
	p, _ := y.probeFile(ctx, hash, index, torrFileURL(y.svc.cfg.TorrServerURL, hash, index))
	return syncRef{Probe: p, Skipped: true, Busy: busy}
}

// kickLive re-fits the file soon when it has live windows (a timing whose read failed).
func (y *syncer) kickLive(hash string, index int) {
	f := y.liveFile(hash, index)
	if _, covered := f.windows(); covered >= liveMinCover {
		y.onGrow(f)
	}
}

// mergeWindows combines read and live windows: a window of the file's own subtitle track
// beats a live one, a live one an audio window read for the same stretch.
func mergeWindows(read, live []refWindow) []refWindow {
	var out []refWindow
	for _, w := range read {
		if w.Kind == "subs" {
			out = append(out, w)
		}
	}
	subs := len(out)
	for _, w := range live {
		if !overlapsHalf(w, out[:subs]) {
			out = append(out, w)
		}
	}
	chosen := len(out)
	for _, w := range read {
		if w.Kind != "subs" && !overlapsHalf(w, out[:chosen]) {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// overlapsHalf reports whether more than half of w lies in one of ws.
func overlapsHalf(w refWindow, ws []refWindow) bool {
	for _, o := range ws {
		if math.Min(w.end(), o.end())-math.Max(w.Start, o.Start) > (w.end()-w.Start)/2 {
			return true
		}
	}
	return false
}

// refKind names the reference a fit used (logs): full, or the kinds of its windows.
func refKind(ref syncRef, windows []refWindow) string {
	if ref.Full {
		return "full"
	}
	n := map[string]int{}
	for _, w := range windows {
		n[w.Kind]++
	}
	var kinds []string
	for _, k := range []string{"subs", "audio", "live"} {
		if n[k] > 0 {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, "+")
}
