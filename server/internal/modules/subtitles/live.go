package subtitles

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Live extraction of embedded text subtitles from big files. Matroska and MP4 interleave the
// subtitle packets with the video across the whole file, so a full extraction downloads all of
// it. While the file plays, TorrServer reads ahead of the playing position anyway, so ffmpeg
// reads the track from just before the viewer's position onward: the pieces playback needs
// next, mostly from TorrServer's cache. Cues collect in memory; every request answers with
// what is there so far (partial), and once the whole file was read the track is cached on disk
// like a full extraction. A seek outside the read ranges starts a new read there.

const (
	liveLead     = 30.0            // seconds read before the asked position
	liveAhead    = 600.0           // cues are wanted up to this far past the asked position
	liveJump     = 300.0           // an uncovered point this far past a running read's frontier starts a new read
	liveIdle     = 3 * time.Minute // a read nobody asked for this long stops
	liveMaxReads = 3               // reads server-wide; the least recently asked one gives way
	liveRunLimit = 3 * time.Hour
	liveKeep     = time.Hour // an idle track's cues are kept this long
	// PartialNote marks a partial WebVTT answer (players read it; a header may be hidden by CORS)
	PartialNote = "NOTE nextflix-partial"
)

type liveCue struct {
	start, end float64
	text       string
}

// liveSpan is a read range in seconds; to is +Inf for "to the end of the file".
type liveSpan struct{ from, to float64 }

type liveRun struct {
	from, frontier float64
	cancel         context.CancelFunc
}

// liveTrack is one embedded subtitle track (hash, file, stream) being read.
type liveTrack struct {
	key                  string
	hash                 string
	index, stream        int
	cues                 []liveCue
	seen                 map[string]bool
	spans                []liveSpan // finished reads, merged and sorted
	run                  *liveRun
	asked                time.Time
	complete             bool
	changed              chan struct{} // closed on the next new cue or read end
	completeNotification bool
}

// liveReader starts ffmpeg (tests replace it): the track's WebVTT on the returned reader;
// wait reports how the read ended.
type liveReader func(ctx context.Context, url string, stream int, from float64) (r io.Reader, wait func() error, err error)

type liveSubs struct {
	mu            sync.Mutex
	tracks        map[string]*liveTrack
	torrServerURL string
	start         liveReader
	onComplete    func(key string, vtt []byte)
}

func newLiveSubs(torrServerURL string, onComplete func(key string, vtt []byte)) *liveSubs {
	return &liveSubs{tracks: map[string]*liveTrack{}, torrServerURL: torrServerURL, start: ffmpegLiveReader, onComplete: onComplete}
}

// active reports whether key is read live (its later requests stay live).
func (l *liveSubs) active(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.tracks[key]
	return ok
}

// get returns the cues of the track read so far as WebVTT and whether more may follow,
// starting or moving the read for the position t (seconds of the file).
func (l *liveSubs) get(ctx context.Context, key, hash string, index, stream int, t float64) ([]byte, bool) {
	now := time.Now()
	l.mu.Lock()
	tr := l.tracks[key]
	if tr == nil {
		l.pruneLocked(now)
		tr = &liveTrack{key: key, hash: hash, index: index, stream: stream, seen: map[string]bool{}, changed: make(chan struct{})}
		l.tracks[key] = tr
	}
	tr.asked = now
	started := false
	if !tr.complete {
		if from, ok := tr.need(t); ok {
			l.stopOthersLocked(tr)
			l.startLocked(tr, from)
			started = true
		}
	}
	wait := tr.changed
	l.mu.Unlock()
	if started {
		timer := time.NewTimer(liveFirstWait)
		select {
		case <-wait:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return renderVTT(tr.cues, !tr.complete), !tr.complete
}

// need returns where a new read has to start for position t: the first point from t-liveLead
// on that no read covered or is about to reach. ok is false when the reads cover up to
// t+liveAhead (or a running read gets there).
func (tr *liveTrack) need(t float64) (float64, bool) {
	p := math.Max(0, t-liveLead)
	spans := append([]liveSpan(nil), tr.spans...)
	if tr.run != nil {
		spans = mergeSpans(append(spans, liveSpan{tr.run.from, tr.run.frontier}))
	}
	gap := p
	for _, s := range spans {
		if s.from <= gap+1 && s.to > gap {
			gap = s.to
		}
	}
	if gap > t+liveAhead || math.IsInf(gap, 1) {
		return 0, false
	}
	if r := tr.run; r != nil && gap >= r.from-1 && gap <= r.frontier+liveJump {
		return 0, false
	}
	return gap, true
}

// stopOthersLocked ends the reads of other tracks of the same file (the viewer switched
// tracks) and, past liveMaxReads, the least recently asked read.
func (l *liveSubs) stopOthersLocked(tr *liveTrack) {
	var running []*liveTrack
	for _, o := range l.tracks {
		if o == tr || o.run == nil {
			continue
		}
		if o.hash == tr.hash && o.index == tr.index {
			o.stopLocked()
			continue
		}
		running = append(running, o)
	}
	sort.Slice(running, func(i, j int) bool { return running[i].asked.Before(running[j].asked) })
	for len(running) >= liveMaxReads {
		running[0].stopLocked()
		running = running[1:]
	}
}

// stopLocked ends the running read; what it read counts as covered.
func (tr *liveTrack) stopLocked() {
	if tr.run == nil {
		return
	}
	tr.run.cancel()
	tr.endRunLocked(tr.run, false)
}

// endRunLocked records the range run read (to the end of the file when whole) and drops it.
func (tr *liveTrack) endRunLocked(run *liveRun, whole bool) {
	if tr.run != run {
		return
	}
	to := run.frontier
	if whole {
		to = math.Inf(1)
	}
	if to > run.from {
		tr.spans = mergeSpans(append(tr.spans, liveSpan{run.from, to}))
	}
	tr.run = nil
	tr.complete = len(tr.spans) == 1 && tr.spans[0].from <= 0 && math.IsInf(tr.spans[0].to, 1)
	tr.signalLocked()
}

func (tr *liveTrack) signalLocked() {
	close(tr.changed)
	tr.changed = make(chan struct{})
}

func (l *liveSubs) startLocked(tr *liveTrack, from float64) {
	tr.stopLocked()
	ctx, cancel := context.WithTimeout(context.Background(), liveRunLimit)
	run := &liveRun{from: from, frontier: from, cancel: cancel}
	tr.run = run
	go l.read(ctx, tr, run)
}

// read runs one ffmpeg read of tr from run.from and collects its cues. It stops at the end of
// the file, at the start of a range read before, when nobody asked for liveIdle, or when
// cancelled (a seek elsewhere, another track).
func (l *liveSubs) read(ctx context.Context, tr *liveTrack, run *liveRun) {
	defer run.cancel()
	url := torrFileURL(l.torrServerURL, tr.hash, tr.index)
	r, wait, err := l.start(ctx, url, tr.stream, run.from)
	if err != nil {
		log.Printf("subtitles: live read of %s: %v", tr.key, err)
		l.mu.Lock()
		tr.endRunLocked(run, false)
		l.mu.Unlock()
		return
	}
	go func() { // idle watch
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				l.mu.Lock()
				idle := time.Since(tr.asked) > liveIdle
				l.mu.Unlock()
				if idle {
					run.cancel()
					return
				}
			}
		}
	}()
	reached := false // the read ran into a range read before
	parseVTTStream(r, func(c liveCue) bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		if tr.run != run {
			return false
		}
		if k := fmt.Sprintf("%.3f|%s", c.start, c.text); !tr.seen[k] {
			tr.seen[k] = true
			tr.cues = append(tr.cues, c)
		}
		run.frontier = math.Max(run.frontier, c.end)
		for _, s := range tr.spans {
			if s.from > run.from && run.frontier >= s.from {
				reached = true
				return false
			}
		}
		tr.signalLocked()
		return true
	})
	if reached {
		run.cancel()
	}
	err = wait()
	l.mu.Lock()
	whole := err == nil && ctx.Err() == nil
	tr.endRunLocked(run, whole) // a read that reached the next range merges with it
	done := tr.complete && !tr.completeNotification
	if done {
		tr.completeNotification = true
	}
	vtt := renderVTT(tr.cues, false)
	n := len(tr.cues)
	l.mu.Unlock()
	if err != nil && ctx.Err() == nil && !reached {
		log.Printf("subtitles: live read of %s from %.0fs: %v", tr.key, run.from, err)
	}
	if done {
		log.Printf("subtitles: live read of %s complete (%d cues)", tr.key, n)
		if l.onComplete != nil {
			l.onComplete(tr.key, vtt)
		}
	}
}

// pruneLocked drops tracks that are complete (cached on disk) or idle for liveKeep.
func (l *liveSubs) pruneLocked(now time.Time) {
	for k, tr := range l.tracks {
		if tr.run == nil && (tr.completeNotification || now.Sub(tr.asked) > liveKeep) {
			delete(l.tracks, k)
		}
	}
}

// mergeSpans sorts spans and joins the ones that touch (within a second).
func mergeSpans(spans []liveSpan) []liveSpan {
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	var out []liveSpan
	for _, s := range spans {
		if n := len(out); n > 0 && s.from <= out[n-1].to+1 {
			out[n-1].to = math.Max(out[n-1].to, s.to)
			continue
		}
		out = append(out, s)
	}
	return out
}

// parseVTTStream reads WebVTT cues as they arrive and calls add for each; add returning false
// stops the reading.
func parseVTTStream(r io.Reader, add func(liveCue) bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var block []string
	flush := func() bool {
		defer func() { block = block[:0] }()
		for i, line := range block {
			if !strings.Contains(line, "-->") {
				continue
			}
			parts := strings.SplitN(line, "-->", 2)
			start, ok1 := parseVTTTime(parts[0])
			end, ok2 := parseVTTTime(strings.Fields(parts[1] + " x")[0])
			text := strings.TrimSpace(strings.Join(block[i+1:], "\n"))
			if !ok1 || !ok2 || end <= start || text == "" {
				return true
			}
			return add(liveCue{start: start, end: end, text: text})
		}
		return true
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			if len(block) > 0 && !flush() {
				return
			}
			continue
		}
		block = append(block, line)
	}
	if len(block) > 0 {
		flush()
	}
}

// parseVTTTime reads "01:02:03.456" or "02:03.456" as seconds.
func parseVTTTime(s string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, ",", ".")), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		if i < len(parts)-1 {
			total = (total + v) * 60
		} else {
			total += v
		}
	}
	return total, true
}

func formatVTTTime(sec float64) string {
	ms := int64(math.Round(sec * 1000))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// renderVTT writes cues (sorted by start) as WebVTT; partial adds PartialNote.
func renderVTT(cues []liveCue, partial bool) []byte {
	sorted := append([]liveCue(nil), cues...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	if partial {
		b.WriteString(PartialNote + "\n\n")
	}
	for _, c := range sorted {
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", formatVTTTime(c.start), formatVTTTime(c.end), c.text)
	}
	return []byte(b.String())
}

// ffmpegLiveReader reads one subtitle stream from about from on; -copyts keeps the file's
// timestamps and -flush_packets hands over each cue at once.
func ffmpegLiveReader(ctx context.Context, url string, stream int, from float64) (io.Reader, func() error, error) {
	args := []string{"-nostdin", "-v", "error",
		"-reconnect", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "5"}
	if from > 0 {
		args = append(args, "-ss", strconv.FormatFloat(from, 'f', 3, 64))
	}
	args = append(args, "-copyts", "-i", url, "-map", fmt.Sprintf("0:%d", stream), "-c:s", "webvtt",
		"-flush_packets", "1", "-f", "webvtt", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(truncateString(stderr.String(), 300)))
		}
		return nil
	}, nil
}

func truncateString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Var so tests can shorten it: a new read's first answer waits this long for cues.
var liveFirstWait = 4 * time.Second
