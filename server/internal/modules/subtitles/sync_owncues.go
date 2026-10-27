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
	f.ownPic = pictureSubCodecs[codec]
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
func (f *liveFile) ownState() string { return f.ownChecked().State }

// ownChecked is the file's own-track check so far.
func (f *liveFile) ownChecked() ownCheck {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.check
}

// checkOwnTrack applies the own-track check (sync_owncheck.go) to the windows a fit uses (ref
// read, live played): the own track's windows stay when it is timed to the file (moved onto the
// speech by the offsets the check found, onSpeech), else the speech of the same stretches stands
// in (read with each window, heard while playing). Set aside while the check does not know (a
// text track's, as before the check: its speech may not tell, music or noise) or found no fit
// (NoFit; a PGS track's too) (ref.OwnAside), the track's windows still time a subtitle the speech
// does not (decide): an unchecked track may be another cut's (The Mentalist S03E22: its cue tap
// timed the subtitle 0.82 s early while the read ran). ref.Own tells the line refinement whether
// the track is tight.
func (y *syncer) checkOwnTrack(hash string, index int, ref syncRef, live []refWindow) (syncRef, []refWindow) {
	if ref.Full {
		return ref, live
	}
	var f *liveFile
	picture := ref.Probe.picture()
	if y.svc.cfg.LiveTiming {
		f = y.liveFile(hash, index)
		f.mu.Lock()
		picture = picture || f.ownPic
		f.mu.Unlock()
	}
	c := checkOwnCached(f, ownPairsOf(ref, f), !ref.Busy, hash, index)
	ref.Own = c
	if c.State == ownTimed {
		ref.Windows = c.onSpeech(ref.Windows)
		return ref, c.onSpeech(live)
	}
	ref.OwnAside = c.State == "" && (c.NoFit || !picture) && (hasOwnWindows(ref.Windows) || hasOwnWindows(live))
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

// checkOwnCached is checkOwn, kept per file (and on disk with its live windows): done again
// when the stretches grew (by a third once it found something), only from all of them (full:
// ref is the file's reference, not a stand-in while it is read). Each change of the result
// re-fits the subtitles with other windows, so a decision stands (settleOwn). A loose track's
// offsets per stretch are kept with it (withLocal).
func checkOwnCached(f *liveFile, pairs []ownPair, full bool, hash string, index int) ownCheck {
	pairs = distinctPairs(pairs)
	if f == nil {
		c := checkOwn(pairs)
		c.local, c.localN = localOffsets(pairs, c), len(pairs)
		return c
	}
	f.mu.Lock()
	prev, n := f.check, f.checkN
	f.mu.Unlock()
	if !full || len(pairs) == n || prev.State != "" && len(pairs) < n+max(1, n/3) {
		return f.withLocal(prev, n, pairs)
	}
	c := settleLag(prev, settleOwn(prev, checkOwn(pairs)), pairs)
	c.local, c.localN = localOffsets(pairs, c), len(pairs)
	f.mu.Lock()
	if !sameSaved(c.saved(), f.check.saved()) {
		f.dirty = true
	}
	f.check, f.checkN = c, len(pairs)
	f.mu.Unlock()
	if c.State != prev.State || c.Tight != prev.Tight || c.NoFit != prev.NoFit || c.NoFit && math.Abs(c.Lag-prev.Lag) >= 0.05 {
		logOwnCheck(hash, index, c)
	}
	return c
}

// withLocal is the file's check c (from n pairs) with a loose track's offsets per stretch of
// these pairs, kept with the check until the pairs change.
func (f *liveFile) withLocal(c ownCheck, n int, pairs []ownPair) ownCheck {
	if c.State != ownTimed || c.Tight || c.localN == len(pairs) {
		return c
	}
	c.local, c.localN = localOffsets(pairs, c), len(pairs)
	f.mu.Lock()
	if f.checkN == n {
		f.check.local, f.check.localN = c.local, c.localN
	}
	f.mu.Unlock()
	return c
}

// settleOwn is the check that stands after a new one: a decision is kept against one that does
// not decide, or decides otherwise from fewer than a third more stretches (The Mentalist S02E22's
// captions went timed, not known, timed at 15, 26 and 34 stretches); a tight track stays tight
// down to ownLooseShare of its line starts on an onset. Offsets that moved by 0.05 s at most stay
// (closeSegments): the line refinement turned +0.08 / +0.10 s on The Mentalist S03E23 into moves
// of 35 lines by up to 0.4 s, back and forth.
func settleOwn(prev, c ownCheck) ownCheck {
	if prev.State == "" {
		return c
	}
	if c.State == "" || c.State != prev.State && c.Pairs < prev.Pairs+max(1, prev.Pairs/3) {
		return prev
	}
	if c.State == ownTimed && prev.State == ownTimed && prev.Tight && !c.Tight && c.Near >= ownLooseShare {
		c.Tight = true
	}
	if c.State == ownTimed && prev.State == ownTimed && closeSegments(prev.Fit.Segments, c.Fit.Segments) {
		c.Fit.Segments = prev.Fit.Segments
	}
	return c
}

// settleLag keeps the offset a track without a fit (NoFit) moves its set-aside windows by while
// a new check finds none (its fit took two offsets, or one outside the lag) or one more than
// 0.1 s away from fewer than a third more stretches than it was found from, as long as more of
// its line starts than by chance still lie on speech onsets there: every set-aside window moves
// with it, so each flip moved the whole subtitle (The Mentalist S03E17: +0.38, 0 and +0.37 s at
// 14, 15 and 17 stretches; the cues -0.33 s, then +0.37 s).
func settleLag(prev, c ownCheck, pairs []ownPair) ownCheck {
	from := prev.lagPairs
	if from == 0 {
		from = prev.Pairs
	}
	c.lagPairs = c.Pairs
	if !prev.NoFit || !c.NoFit || prev.Lag == 0 || math.Abs(c.Lag-prev.Lag) <= 0.1 ||
		c.Lag != 0 && c.Pairs >= from+max(1, from/3) {
		return c
	}
	if near, _ := nearShare(pairs, prev.Lag); near >= ownChanceShare {
		c.Lag, c.lagPairs = prev.Lag, from
	}
	return c
}

func logOwnCheck(hash string, index int, c ownCheck) {
	what := "not known yet"
	offset := 0.0
	if len(c.Fit.Segments) > 0 {
		offset = c.Fit.Segments[0].Offset
	}
	switch {
	case c.State == ownOther:
		what = "not timed to the file (another cut): timed from the speech only"
	case c.NoFit:
		what = fmt.Sprintf("no fit (at best %.0f %% of %d line starts on a speech start), not known yet: the speech times subtitles, the track only those the speech does not (its windows moved %+.2f s)",
			100*c.Near, c.Matched, c.Lag)
	case c.Tight:
		what = fmt.Sprintf("timed to the file (%+.2f s) and tight (%.0f %% of %d line starts on a speech start)",
			offset, 100*c.Near, c.Matched)
	case c.State == ownTimed:
		what = fmt.Sprintf("timed to the file (%+.2f s) but loose (%.0f %% of %d line starts on a speech start): no line refinement",
			offset, 100*c.Near, c.Matched)
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
	viewer := s.y.fileViewer(f.fk)
	f.mu.Lock()
	n := f.readOwn(s.pos, b)
	s.pos = b
	f.ownGrown += float64(n) / syncRate
	f.checkComplete()
	grown := f.ownGrown >= f.growStep(viewer)
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
