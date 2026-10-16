package subtitles

import (
	"log"
	"math"
	"sort"
	"time"
)

// Own-track cues of the play streams: streaming's cue tap (wired in main.go) passes the cue
// times of one text subtitle track of the played file, which its play ffmpeg demuxes anyway.
// They become live reference windows of kind "subs" on the 60 s grid (sync_live.go), so every
// stretch played has the file's own line starts for the fit and the line refinement
// (sync_refine.go), without reading the file again. Streams may overlap and come in any order:
// a window is finished once its frames were read by any of them.

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
	want := -1
	if probed {
		want = probe.TextSub
	}
	stream := pickOwnTrack(tracks, want)
	if stream < 0 {
		return -1, nil
	}
	f := y.liveFile(hash, index)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.complete || f.duration > 0 && float64(len(f.ownDone)) >= math.Ceil(f.duration/syncWindowLen) {
		return -1, nil
	}
	f.ownTaps++
	f.lastTap = time.Now()
	return stream, &ownSink{y: y, f: f, start: start, pos: int(math.Round(start * syncRate))}
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
// best rated one; -1 when none is usable.
func pickOwnTrack(tracks []OwnTrack, probed int) int {
	best, bestScore := -1, -1
	for _, t := range tracks {
		score := ownTrackScore(t.Codec, t.Lang, t.Title, t.Forced, false)
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
		log.Printf("subtitles: own track of %s:%d from %.0fs: %d cues, %.0f s read; %d own windows finished",
			f.hash[:8], f.index, s.start, s.cues, float64(s.read)/syncRate, done)
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
