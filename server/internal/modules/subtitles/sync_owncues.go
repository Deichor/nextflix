package subtitles

import (
	"fmt"
	"log"
	"maps"
	"math"
	"slices"
	"sort"
	"time"
)

// Own-track cues of the play streams: streaming's cue tap (wired in main.go) passes the cue
// times of one subtitle track of the played file (text, else PGS pictures), which its play
// ffmpeg demuxes anyway. They become live reference windows of kind "subs" on the 60 s grid
// (sync_live.go), so every stretch played has the file's own line starts for the fit and the
// line refinement (sync_refine.go), without reading the file again. Streams may overlap and
// come in any order: a window is finished once its frames were read by any of them.

const (
	ownRead      = 1 // frame flags of an unfinished own-track window
	ownShows     = 2
	ownStarts    = 4
	ownDoneShare = 0.98 // a window is finished once this share of it was read
	ownMaxCue    = 15.0 // seconds of a cue marked at most
	ownMinStarts = 4    // line starts an own-track window needs to be used (like cueWindow)
)

// OwnTrack is a subtitle track of the played file.
type OwnTrack struct {
	Stream             int
	Lang, Title, Codec string
	Default, Forced    bool
	Frames             int // packets the container counts, 0 unknown
}

// OwnCueSink takes the cues of one own track of a play stream (seconds of the file). Heard(t):
// the stream read the file up to t, so every cue starting before t was passed. Calls come from
// one goroutine.
type OwnCueSink interface {
	Cue(start, end float64)
	Heard(t float64)
	Close()
}

type ownSink struct {
	y      *syncer
	f      *liveFile
	start  float64
	codec  string
	pos    int // file frame read up to
	cues   int
	read   int // frames newly read
	closed bool
}

// OpenOwnCues: a play stream of the file starts at start seconds; it picks the own track to read
// (the one the reference windows read) and returns its sink; ok is false when the file needs no
// reference (no live timing, a whole subtitle of the file is known, or the file is covered).
func (s *Service) OpenOwnCues(hash string, index int, start float64, tracks []OwnTrack) (stream int, sink OwnCueSink, ok bool) {
	stream, k := s.sync.openOwnCues(hash, index, start, tracks)
	if k == nil {
		return -1, nil, false
	}
	return stream, k, true
}

func (y *syncer) openOwnCues(hash string, index int, start float64, tracks []OwnTrack) (int, *ownSink) {
	if !y.svc.cfg.LiveTiming || !infoHashRe.MatchString(hash) || index <= 0 || !(start >= 0) || start > syncMaxUnknown {
		return -1, nil
	}
	fk := syncFileKey(hash, index)
	y.mu.Lock()
	full := y.noLive[fk]
	probe, probed := y.probes[fk]
	y.mu.Unlock()
	if full || y.fullRefOnDisk(fk) {
		return -1, nil
	}
	f := y.liveFile(hash, index)
	want, duration := -1, f.knownDuration()
	if probed {
		want, duration = probe.OwnSub, probe.Duration
	}
	stream := pickOwnTrack(tracks, want, duration)
	if stream < 0 || f.ownState() == ownOther {
		return -1, nil
	}
	codec := ""
	for _, t := range tracks {
		if t.Stream == stream {
			codec = t.Codec
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.complete || f.duration > 0 && float64(len(f.ownDone)) >= math.Ceil(f.duration/syncWindowLen) {
		return -1, nil
	}
	f.ownTaps++
	f.lastTap = time.Now()
	return stream, &ownSink{y: y, f: f, start: start, codec: codec, pos: int(math.Round(start * syncRate))}
}

// knownDuration is the file's length as far as the live reference knows it (0 unknown).
func (f *liveFile) knownDuration() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.duration
}

// ownState is what the own-track check found for the file so far ("" not known).
func (f *liveFile) ownState() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.check.State
}

// checkOwnTrack applies the own-track check (sync_owncheck.go) to the windows a fit uses (ref
// read, live played): the own track's windows stay only when it is timed to the file, else
// the speech of the same stretches stands in (read with each window, heard while playing).
// ref.Own tells the line refinement whether the track is tight.
func (y *syncer) checkOwnTrack(hash string, index int, ref syncRef, live []refWindow) (syncRef, []refWindow) {
	if ref.Full {
		return ref, live
	}
	var f *liveFile
	if y.svc.cfg.LiveTiming {
		f = y.liveFile(hash, index)
	}
	pairs := ownPairsOf(ref, f)
	c := checkOwnCached(f, pairs, hash, index)
	ref.Own = c
	if c.State == ownTimed {
		return ref, live
	}
	if hasOwnWindows(ref.Windows) {
		ws := make([]refWindow, 0, len(ref.Windows))
		for _, w := range ref.Windows {
			if w.Kind == "subs" {
				i := slices.IndexFunc(ref.Speech, func(s refWindow) bool { return s.Start == w.Start })
				if i < 0 {
					continue
				}
				w = ref.Speech[i]
			}
			ws = append(ws, w)
		}
		ref.Windows = ws
	}
	if f != nil && hasOwnWindows(live) {
		live, _ = f.windowsOf(false)
	}
	return ref, live
}

// checkOwnCached is checkOwn, kept per file and done again when the pairs grew (by a third
// once it found something).
func checkOwnCached(f *liveFile, pairs []ownPair, hash string, index int) ownCheck {
	if f == nil {
		return checkOwn(pairs)
	}
	f.mu.Lock()
	prev, n := f.check, f.checkN
	f.mu.Unlock()
	if len(pairs) == n || prev.State != "" && len(pairs) < n+max(1, n/3) {
		return prev
	}
	c := checkOwn(pairs)
	f.mu.Lock()
	f.check, f.checkN = c, len(pairs)
	f.mu.Unlock()
	if c.State != prev.State || c.Tight != prev.Tight {
		logOwnCheck(hash, index, c)
	}
	return c
}

func logOwnCheck(hash string, index int, c ownCheck) {
	what := "not known yet"
	switch {
	case c.State == ownOther:
		what = "not timed to the file (another cut): timed from the speech only"
	case c.Tight:
		what = fmt.Sprintf("timed to the file (%+.2f s) and tight (%.0f %% of %d line starts on a speech start)",
			c.Fit.Segments[0].Offset, 100*c.Near, c.Matched)
	case c.State == ownTimed:
		what = fmt.Sprintf("timed to the file (%+.2f s) but loose (%.0f %% of %d line starts on a speech start): no line refinement",
			c.Fit.Segments[0].Offset, 100*c.Near, c.Matched)
	}
	log.Printf("subtitles: own track of %s:%d against the speech of %d stretches: %s", hash[:min(8, len(hash))], index, c.Pairs, what)
}

func hasOwnWindows(ws []refWindow) bool {
	return slices.ContainsFunc(ws, func(w refWindow) bool { return w.Kind == "subs" })
}

// ownPairsOf are the stretches with both the own track and speech: read own windows with the
// speech read with them (else heard there), and the grid windows both taps finished.
func ownPairsOf(ref syncRef, f *liveFile) []ownPair {
	var speech, own map[int]liveWin
	if f != nil {
		f.mu.Lock()
		speech, own = maps.Clone(f.done), maps.Clone(f.ownDone)
		f.mu.Unlock()
	}
	var pairs []ownPair
	for _, w := range ref.Windows {
		if w.Kind != "subs" {
			continue
		}
		var sp []byte
		if i := slices.IndexFunc(ref.Speech, func(s refWindow) bool { return s.Start == w.Start }); i >= 0 {
			sp = ref.Speech[i].Speech
		} else {
			sp = heardSpeech(speech, w.Start, len(w.Speech))
		}
		if n := min(len(sp), len(w.Speech)); n > 0 {
			pairs = append(pairs, ownPair{w.Start, w.Speech[:n], sp[:n]})
		}
	}
	for g, o := range own {
		s, ok := speech[g]
		if !ok {
			continue
		}
		frames := unpackBits(o.Bits, o.Frames)
		for _, i := range o.Starts {
			if i >= 0 && i < len(frames) {
				frames[i] = 2
			}
		}
		n := min(o.Frames, s.Frames)
		pairs = append(pairs, ownPair{float64(g) * syncWindowLen, frames[:n], unpackBits(s.Bits, s.Frames)[:n]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].start < pairs[j].start })
	return pairs
}

// heardSpeech is the played speech of [start, start+frames) from finished grid windows, nil
// unless they cover all of it.
func heardSpeech(done map[int]liveWin, start float64, frames int) []byte {
	a := int(math.Round(start * syncRate))
	if a < 0 || len(done) == 0 {
		return nil
	}
	out := make([]byte, frames)
	var bits []byte
	cur := -1
	for i := range out {
		g := (a + i) / liveWindowFrames
		if g != cur {
			w, ok := done[g]
			if !ok {
				return nil
			}
			bits, cur = unpackBits(w.Bits, w.Frames), g
		}
		j := a + i - g*liveWindowFrames
		if j >= len(bits) {
			return nil
		}
		out[i] = bits[j]
	}
	return out
}

// fullRefOnDisk reports whether the file's reference is a whole subtitle (without waiting for a
// reference being read).
func (y *syncer) fullRefOnDisk(fk string) bool {
	st, release := y.refState(fk)
	defer release()
	if !st.mu.TryLock() {
		return false
	}
	defer st.mu.Unlock()
	return y.loadRef(fk, st) && st.ref.Full
}

// pickOwnTrack is the track the reference windows read (probed, when it is listed), else the
// best rated one (a text track before a picture one); -1 when none is usable. duration is the
// file's (0 unknown).
func pickOwnTrack(tracks []OwnTrack, probed int, duration float64) int {
	best, bestScore := -1, -1
	for _, t := range tracks {
		score := ownTrackScore(t.Codec, t.Lang, t.Title, t.Forced, false, t.Frames, duration)
		if score < 0 {
			continue
		}
		if t.Stream == probed {
			return t.Stream
		}
		if score > bestScore {
			best, bestScore = t.Stream, score
		}
	}
	return best
}

func (s *ownSink) Cue(start, end float64) {
	if !(start >= 0) || !(end > start) || start > syncMaxUnknown {
		return
	}
	end = math.Min(end, start+ownMaxCue)
	s.f.mu.Lock()
	s.f.markOwn(int(math.Round(start*syncRate)), int(math.Round(end*syncRate)))
	s.f.mu.Unlock()
	s.cues++
}

func (s *ownSink) Heard(t float64) {
	if !(t > 0) || t > syncMaxUnknown {
		return
	}
	b := int(math.Round(t * syncRate))
	if b <= s.pos {
		return
	}
	f := s.f
	f.mu.Lock()
	n := f.readOwn(s.pos, b)
	s.pos = b
	f.ownGrown += float64(n) / syncRate
	f.checkComplete()
	grown := f.ownGrown >= f.growStep()
	if grown {
		f.ownGrown = 0
	}
	f.mu.Unlock()
	s.read += n
	if grown {
		s.y.onGrow(f)
	}
}

// Close ends the stream: logs what it brought and re-fits the file's subtitles.
func (s *ownSink) Close() {
	if s.closed {
		return
	}
	s.closed = true
	f := s.f
	f.mu.Lock()
	f.ownTaps--
	f.lastTap = time.Now()
	for w := range f.own {
		f.finishOwn(w)
	}
	f.checkComplete()
	done := len(f.ownDone)
	f.mu.Unlock()
	if s.read > 0 {
		log.Printf("subtitles: own track of %s:%d (%s) from %.0fs: %d cues, %.0f s read; %d own windows finished",
			f.hash[:8], f.index, s.codec, s.start, s.cues, float64(s.read)/syncRate, done)
	}
	s.y.onGrow(f)
}

// ownTapping reports whether a cue tap of the file runs.
func (f *liveFile) ownTapping() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownTaps > 0
}

// ownChunk is unfinished own-track window w (f.mu held).
func (f *liveFile) ownChunk(w int) []uint8 {
	c := f.own[w]
	if c == nil {
		c = make([]uint8, liveWindowFrames)
		f.own[w] = c
	}
	return c
}

// markOwn marks a cue (file frames [a, b)) in the unfinished windows it touches.
func (f *liveFile) markOwn(a, b int) {
	for i := max(a, 0); i < b; {
		w := i / liveWindowFrames
		end := min(b, (w+1)*liveWindowFrames)
		if _, ok := f.ownDone[w]; !ok {
			c := f.ownChunk(w)
			for j := i; j < end; j++ {
				c[j-w*liveWindowFrames] |= ownShows
			}
		}
		i = end
	}
	if w := a / liveWindowFrames; a >= 0 {
		if _, ok := f.ownDone[w]; !ok {
			f.ownChunk(w)[a%liveWindowFrames] |= ownStarts
		}
	}
}

// readOwn marks file frames [a, b) read and finishes the windows that are; it returns the
// frames newly read.
func (f *liveFile) readOwn(a, b int) int {
	n := 0
	for i := max(a, 0); i < b; {
		w := i / liveWindowFrames
		end := min(b, (w+1)*liveWindowFrames)
		if _, ok := f.ownDone[w]; !ok {
			c := f.ownChunk(w)
			for j := i; j < end; j++ {
				if k := j - w*liveWindowFrames; c[k]&ownRead == 0 {
					c[k] |= ownRead
					f.ownRead[w]++
					n++
				}
			}
			if end == (w+1)*liveWindowFrames || f.ownRead[w] >= f.windowFrames(w) {
				f.finishOwn(w)
			}
		}
		i = end
	}
	return n
}

// finishOwn keeps window w as finished once nearly all of it was read.
func (f *liveFile) finishOwn(w int) {
	n := f.windowFrames(w)
	c := f.own[w]
	if c == nil || float64(f.ownRead[w]) < ownDoneShare*float64(n) {
		return
	}
	shows := make([]byte, n)
	var starts []int
	for i, v := range c[:n] {
		if v&ownShows != 0 {
			shows[i] = 1
		}
		if v&ownStarts != 0 {
			starts = append(starts, i)
		}
	}
	f.ownDone[w] = liveWin{Bits: packBits(shows), Frames: n, Starts: starts}
	delete(f.own, w)
	delete(f.ownRead, w)
	f.dirty = true
}

// ownWindows returns the own-track windows with enough lines: finished ones, and of unfinished
// ones the longest stretch read when it has liveMinRun frames (f.mu held).
func (f *liveFile) ownWindows() []refWindow {
	var out []refWindow
	for w, d := range f.ownDone {
		if len(d.Starts) < ownMinStarts {
			continue
		}
		s := unpackBits(d.Bits, d.Frames)
		for _, i := range d.Starts {
			if i >= 0 && i < len(s) {
				s[i] = 2
			}
		}
		out = append(out, refWindow{Start: float64(w) * syncWindowLen, Kind: "subs", Speech: s})
	}
	for w, c := range f.own {
		first, n, a := 0, 0, -1
		for i := 0; i <= len(c); i++ {
			if i < len(c) && c[i]&ownRead != 0 {
				if a < 0 {
					a = i
				}
				continue
			}
			if a >= 0 && i-a > n {
				first, n = a, i-a
			}
			a = -1
		}
		if n < liveMinRun {
			continue
		}
		s := make([]byte, n)
		starts := 0
		for i := range s {
			switch v := c[first+i]; {
			case v&ownStarts != 0:
				s[i] = 2
				starts++
			case v&ownShows != 0:
				s[i] = 1
			}
		}
		if starts >= ownMinStarts {
			out = append(out, refWindow{Start: float64(w)*syncWindowLen + float64(first)/syncRate, Kind: "subs", Speech: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}
