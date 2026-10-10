package subtitles

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Automatic timing of subtitles that were not made for the playing file (math in
// sync_align.go). List gives each of them an id that names the playing file (wrapSyncID).
// Its file request answers at once and times the subtitle in the background: a reference is
// read from a few windows of the file (once per file, kept on disk), the subtitle is aligned
// to it and the corrected WebVTT is cached per (subtitle, file). Until then the answer is
// marked partial, so the players ask again (every 15 s) and take the corrected cues by
// themselves. A subtitle that does not fit even then is served as the best fitting other
// subtitle of its language, when one fits.

const (
	syncWindowLen     = 60.0      // seconds per reference window
	syncMaxWindows    = 12        // reference windows per file, at most
	syncMinWindows    = 8         // fewer are not reliable (checked on real episodes): no timing then
	syncByteBudget    = 800 << 20 // bytes the windows of one file may read
	syncWindowExtra   = 28 << 20  // read around a window (TorBox proxy: first chunk, read-ahead)
	syncWindowTimeout = 75 * time.Second
	syncJobLimit      = 5 * time.Minute
	syncRetryAfter    = 10 * time.Minute // a failed timing is not tried again before this
	syncAlternatives  = 3                // other subtitles tried when one does not fit
	syncReads         = 2                // files whose reference is read at the same time
)

// A file request waits this long for a quick timing result (tests shorten it).
var syncFirstWait = 2500 * time.Millisecond

// The window reads wait this long, so they do not compete with the playback start (tests shorten it).
var syncReadDelay = 20 * time.Second

// Sync states (X-Subtitle-Sync, logs).
const (
	syncStateSynced    = "synced"    // moved to fit the file
	syncStateUnchanged = "unchanged" // already fitted
	syncStateReplaced  = "replaced"  // did not fit; another subtitle of the language is served
	syncStatePoor      = "poor"      // did not fit, nothing better: served as it is
	syncStatePending   = "pending"
	syncStateFailed    = "failed" // no reference (file unreadable): served as it is
)

type syncMeta struct {
	State      string  `json:"state"`
	Fit        syncFit `json:"fit"`
	ReplacedBy string  `json:"replaced_by,omitempty"` // inner id whose cues are served
	Ref        string  `json:"ref"`                   // reference: full | subs | audio
	Windows    int     `json:"windows"`
}

func (m syncMeta) fits() bool { return m.State == syncStateSynced || m.State == syncStateUnchanged }

// syncRef is the timing reference of one file.
type syncRef struct {
	Probe   mediaProbe  `json:"probe"`
	Windows []refWindow `json:"windows"`
	Full    bool        `json:"full"` // made of a whole subtitle shipped for the file
	// windows the byte budget allows for this file
	MaxWindows int `json:"max_windows"`
}

func (r syncRef) kind() string {
	if r.Full {
		return "full"
	}
	subs := 0
	for _, w := range r.Windows {
		if w.Kind == "subs" {
			subs++
		}
	}
	if subs*2 > len(r.Windows) {
		return "subs"
	}
	return "audio"
}

type syncRefState struct {
	mu  sync.Mutex
	ref *syncRef
}

type syncer struct {
	svc   *Service
	read  syncWindowReader
	probe func(ctx context.Context, url string) (mediaProbe, error)
	sem   chan struct{}

	mu     sync.Mutex
	refs   map[string]*syncRefState // file key -> reference
	metas  map[string]*syncMeta     // result key -> result (nil: none on disk)
	failed map[string]time.Time
	lists  map[string][]Sub // file key -> last ranked list (alternatives, full references)
	probes map[string]mediaProbe
}

func newSyncer(svc *Service) *syncer {
	return &syncer{svc: svc, read: ffmpegSyncWindow, probe: probeMedia, sem: make(chan struct{}, syncReads),
		refs: map[string]*syncRefState{}, metas: map[string]*syncMeta{}, failed: map[string]time.Time{},
		lists: map[string][]Sub{}, probes: map[string]mediaProbe{}}
}

func syncFileKey(hash string, index int) string {
	return strings.ToLower(hash) + "|" + strconv.Itoa(index)
}

// syncKey names a result: the subtitle's cache key and the file.
func syncKey(inner, hash string, index int) (string, error) {
	source, _, ref, err := decodeID(inner)
	if err != nil {
		return "", err
	}
	return source + "|" + ref + "|" + syncFileKey(hash, index), nil
}

const syncIDPrefix = "sync|"

// wrapSyncID makes the id of a subtitle as served for one file.
func wrapSyncID(inner, hash string, index int) string {
	b, err := base64.RawURLEncoding.DecodeString(inner)
	if err != nil {
		return inner
	}
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s%s|%d|%s", syncIDPrefix, strings.ToLower(hash), index, b)))
}

func unwrapSyncID(id string) (inner, hash string, index int, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(b) > 1200 || !bytes.HasPrefix(b, []byte(syncIDPrefix)) {
		return "", "", 0, false
	}
	p := strings.SplitN(string(b[len(syncIDPrefix):]), "|", 3)
	if len(p) != 3 || !infoHashRe.MatchString(p[0]) {
		return "", "", 0, false
	}
	index, err = strconv.Atoi(p[1])
	if err != nil || index <= 0 {
		return "", "", 0, false
	}
	inner = base64.RawURLEncoding.EncodeToString([]byte(p[2]))
	if _, _, _, err := decodeID(inner); err != nil {
		return "", "", 0, false
	}
	return inner, strings.ToLower(p[0]), index, true
}

// FileAnswer is a subtitle file answer.
type FileAnswer struct {
	VTT     []byte
	Partial bool   // more follows: ask again (live track, timing still running)
	Sync    string // timing state of a subtitle served for a file ("" when not timed)
}

// serve answers a file request of a subtitle served for a file.
func (y *syncer) serve(ctx context.Context, inner, hash string, index int) (FileAnswer, error) {
	base, err := y.svc.File(ctx, inner)
	if err != nil {
		return FileAnswer{}, err
	}
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return FileAnswer{}, err
	}
	if a, ok := y.result(key, base); ok {
		return a, nil
	}
	if y.recentlyFailed(key) {
		return FileAnswer{VTT: base, Sync: syncStateFailed}, nil
	}
	call := y.svc.flights.start("sync|"+key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), syncJobLimit)
		defer cancel()
		start := time.Now()
		err := y.run(ctx, inner, hash, index, base)
		if err != nil {
			log.Printf("subtitles: timing of %s on %s:%d failed after %s: %v", shortSubID(inner), hash[:8], index,
				time.Since(start).Round(time.Second), err)
			y.mu.Lock()
			y.failed[key] = time.Now()
			y.mu.Unlock()
		}
		return nil, err
	})
	timer := time.NewTimer(syncFirstWait)
	defer timer.Stop()
	finished := false
	select {
	case <-call.done:
		finished = true
	case <-timer.C:
	case <-ctx.Done():
	}
	if a, ok := y.result(key, base); ok {
		return a, nil
	}
	if finished {
		return FileAnswer{VTT: base, Sync: syncStateFailed}, nil
	}
	return FileAnswer{VTT: renderVTT(vttCues(base), true), Partial: true, Sync: syncStatePending}, nil
}

// result returns the stored answer of key.
func (y *syncer) result(key string, base []byte) (FileAnswer, bool) {
	m := y.meta(key)
	if m == nil {
		return FileAnswer{}, false
	}
	if m.State == syncStateUnchanged || m.State == syncStatePoor {
		return FileAnswer{VTT: base, Sync: m.State}, true
	}
	b, ok := y.svc.files.read("synced", key, ".vtt", 0)
	if !ok {
		return FileAnswer{}, false
	}
	return FileAnswer{VTT: b, Sync: m.State}, true
}

func (y *syncer) meta(key string) *syncMeta {
	y.mu.Lock()
	m, ok := y.metas[key]
	y.mu.Unlock()
	if ok {
		return m
	}
	if b, found := y.svc.files.read("synced", key, ".json", 0); found {
		var v syncMeta
		if json.Unmarshal(b, &v) == nil && v.State != "" {
			m = &v
		}
	}
	y.mu.Lock()
	if len(y.metas) > 5000 {
		y.metas = map[string]*syncMeta{}
	}
	y.metas[key] = m
	y.mu.Unlock()
	return m
}

func (y *syncer) store(key string, m syncMeta, vtt []byte) {
	if vtt != nil {
		if err := y.svc.files.write("synced", key, ".vtt", vtt); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
	}
	if b, err := json.Marshal(m); err == nil {
		if err := y.svc.files.write("synced", key, ".json", b); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
	}
	y.mu.Lock()
	y.metas[key] = &m
	y.mu.Unlock()
}

func (y *syncer) recentlyFailed(key string) bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	at, ok := y.failed[key]
	if ok && time.Since(at) > syncRetryAfter {
		delete(y.failed, key)
		return false
	}
	return ok
}

// run times one subtitle for the file and stores the result.
func (y *syncer) run(ctx context.Context, inner, hash string, index int, base []byte) error {
	start := time.Now()
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return err
	}
	ref, err := y.reference(ctx, hash, index)
	if err != nil {
		return err
	}
	m, vtt, ref := y.fitOne(ctx, hash, index, ref, base)
	if !m.fits() {
		// Did not fit: the best fitting other subtitle of the language, when one fits
		for _, alt := range y.alternatives(hash, index, inner) {
			altBase, err := y.svc.File(ctx, alt)
			if err != nil {
				continue
			}
			altKey, err := syncKey(alt, hash, index)
			if err != nil {
				continue
			}
			am := y.meta(altKey)
			var altVTT []byte
			if am == nil {
				var a syncMeta
				a, altVTT, ref = y.fitOne(ctx, hash, index, ref, altBase)
				y.store(altKey, a, altVTT)
				am = &a
				logSync(alt, hash, index, a, time.Since(start))
			} else if a, ok := y.result(altKey, altBase); ok {
				altVTT = a.VTT
			}
			if am.fits() {
				if altVTT == nil {
					altVTT = altBase
				}
				m = syncMeta{State: syncStateReplaced, Fit: m.Fit, ReplacedBy: alt, Ref: m.Ref, Windows: m.Windows}
				vtt = altVTT
				break
			}
		}
	}
	y.store(key, m, vtt)
	logSync(inner, hash, index, m, time.Since(start))
	return nil
}

func logSync(inner, hash string, index int, m syncMeta, took time.Duration) {
	f := m.Fit
	offs := make([]string, len(f.Segments))
	for i, s := range f.Segments {
		offs[i] = fmt.Sprintf("%+.2fs", s.Offset)
		if i > 0 {
			offs[i] += fmt.Sprintf("@%.0fs", s.From)
		}
	}
	extra := ""
	if m.ReplacedBy != "" {
		extra = " by " + shortSubID(m.ReplacedBy)
	}
	log.Printf("subtitles: timing of %s on %s:%d: %s%s (scale %.4f, offsets %s, score %.2f over %.2f, peak %.1f; %s reference, %d windows, %s)",
		shortSubID(inner), hash[:8], index, m.State, extra, f.Scale, strings.Join(offs, " "), f.Score, f.Base, f.Peak,
		m.Ref, m.Windows, took.Round(100*time.Millisecond))
}

// shortSubID names a subtitle in logs: source and provider reference.
func shortSubID(id string) string {
	source, lang, ref, err := decodeID(id)
	if err != nil {
		return "?"
	}
	return truncateString(source+"/"+lang+"/"+ref, 60)
}

// fitOne aligns one subtitle to the file's reference (reading more windows where the offset
// changes) and returns its result and the WebVTT to serve (nil: serve it as it is).
func (y *syncer) fitOne(ctx context.Context, hash string, index int, ref syncRef, vtt []byte) (syncMeta, []byte, syncRef) {
	cues := vttCues(vtt)
	spans := make([]span, len(cues))
	for i, c := range cues {
		spans[i] = span{c.start, c.end}
	}
	// lengthFits still sees every cue (a longer cut runs past the end)
	timed := clampSpans(spans, ref.Probe.Duration)
	fit, ok := align(timed, ref.Windows)
	for round := 0; ok && !ref.Full && round < 2; round++ {
		extra := fit.extraWindows(syncWindowLen, ref.windowCap()-len(ref.Windows))
		if len(extra) == 0 {
			break
		}
		more, err := y.addWindows(ctx, hash, index, extra)
		if err != nil || len(more.Windows) == len(ref.Windows) {
			break
		}
		ref = more
		fit, ok = align(timed, ref.Windows)
	}
	m := syncMeta{State: syncStatePoor, Fit: fit, Ref: ref.kind(), Windows: len(ref.Windows)}
	if !ok || !fit.good() || !lengthFits(spans, fit, ref.Probe.Duration) {
		return m, nil, ref
	}
	if !fit.changes() {
		m.State = syncStateUnchanged
		return m, nil, ref
	}
	m.State = syncStateSynced
	return m, renderVTT(applyFit(cues, fit), false), ref
}

// lengthFits rejects a subtitle that runs past the end of the video or covers too little of it
// (another cut or episode), once timed.
func lengthFits(spans []span, f syncFit, duration float64) bool {
	if duration <= 0 || len(spans) == 0 {
		return true
	}
	first, last := math.Inf(1), math.Inf(-1)
	for _, s := range spans {
		first = math.Min(first, f.mapTime(s.start))
		last = math.Max(last, f.mapTime(s.end))
	}
	return last <= duration+60 && last-first >= 0.4*duration
}

func applyFit(cues []liveCue, f syncFit) []liveCue {
	out := make([]liveCue, 0, len(cues))
	for _, c := range cues {
		u := c.start * f.Scale
		off := offsetAt(f.Segments, u)
		start, end := u+off, c.end*f.Scale+off
		if end <= 0.05 {
			continue
		}
		out = append(out, liveCue{start: math.Max(start, 0), end: end, text: c.text})
	}
	return out
}

func vttCues(vtt []byte) []liveCue {
	var cues []liveCue
	parseVTTStream(bytes.NewReader(vtt), func(c liveCue) bool {
		cues = append(cues, c)
		return true
	})
	return cues
}

// alternatives are the other subtitles of inner's language listed for the file, best first
// (not forced). OpenSubtitles ones only when already on disk (a download counts against the
// daily quota the viewer needs), after the other sources.
func (y *syncer) alternatives(hash string, index int, inner string) []string {
	_, lang, _, err := decodeID(inner)
	if err != nil {
		return nil
	}
	y.mu.Lock()
	list := y.lists[syncFileKey(hash, index)]
	y.mu.Unlock()
	var out, cached []string
	for _, s := range list {
		in, _, _, ok := unwrapSyncID(s.ID)
		if !ok || in == inner || s.Forced || s.Language != lang {
			continue
		}
		if s.Source == SourceOpenSubtitles {
			if y.svc.hasFile(in) {
				cached = append(cached, in)
			}
			continue
		}
		out = append(out, in)
	}
	out = append(out, cached...)
	return out[:min(len(out), syncAlternatives)]
}

func (y *syncer) refState(fk string) *syncRefState {
	y.mu.Lock()
	defer y.mu.Unlock()
	st := y.refs[fk]
	if st == nil {
		if len(y.refs) >= 32 {
			for k, o := range y.refs {
				if o.mu.TryLock() {
					delete(y.refs, k)
					o.mu.Unlock()
				}
			}
		}
		st = &syncRefState{}
		y.refs[fk] = st
	}
	return st
}

// reference returns the file's timing reference, read once (cached on disk).
func (y *syncer) reference(ctx context.Context, hash string, index int) (syncRef, error) {
	fk := syncFileKey(hash, index)
	st := y.refState(fk)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ref != nil {
		return *st.ref, nil
	}
	if b, ok := y.svc.files.read("syncref", fk, ".json", 0); ok {
		var r syncRef
		if json.Unmarshal(b, &r) == nil && len(r.Windows) > 0 {
			st.ref = &r
			return r, nil
		}
	}
	url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
	select {
	case y.sem <- struct{}{}:
		defer func() { <-y.sem }()
	case <-ctx.Done():
		return syncRef{}, ctx.Err()
	}
	start := time.Now()
	probe, err := y.probeFile(ctx, hash, index, url)
	var r syncRef
	if cues := y.fullReference(ctx, hash, index); len(cues) > 0 {
		r = syncRef{Probe: probe, Windows: fullCueWindows(clampSpans(cues, probe.Duration), syncWindowLen), Full: true}
	} else {
		if err != nil {
			return syncRef{}, err
		}
		r = syncRef{Probe: probe, MaxWindows: probe.maxWindows()}
		if r.MaxWindows < syncMinWindows {
			return syncRef{}, fmt.Errorf("%.1f MB/s: %d windows would read more than %d MB", probe.Bitrate/(1<<20),
				syncMinWindows, syncByteBudget>>20)
		}
		select {
		case <-time.After(syncReadDelay):
		case <-ctx.Done():
			return syncRef{}, ctx.Err()
		}
		for _, at := range windowStarts(probe.Duration, syncWindowLen, r.MaxWindows) {
			if w, err := y.readWindow(ctx, url, probe, at); err == nil {
				r.Windows = append(r.Windows, w)
			} else if ctx.Err() != nil {
				return syncRef{}, ctx.Err()
			} else {
				log.Printf("subtitles: timing reference of %s:%d: %v", hash[:8], index, err)
			}
		}
		if len(r.Windows) < 2 {
			return syncRef{}, fmt.Errorf("only %d of the reference windows could be read", len(r.Windows))
		}
	}
	log.Printf("subtitles: timing reference of %s:%d: %s, %d windows, %d MB read in %s", hash[:8], index, r.kind(),
		len(r.Windows), readBytes(r.Windows)>>20, time.Since(start).Round(100*time.Millisecond))
	y.saveRef(fk, st, r)
	return r, nil
}

// addWindows reads more reference windows of the file (kept with its reference).
func (y *syncer) addWindows(ctx context.Context, hash string, index int, starts []float64) (syncRef, error) {
	fk := syncFileKey(hash, index)
	st := y.refState(fk)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ref == nil {
		return syncRef{}, errors.New("no reference")
	}
	r := *st.ref
	r.Windows = append([]refWindow(nil), r.Windows...)
	url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
	for _, at := range starts {
		if len(r.Windows) >= r.windowCap() {
			break
		}
		w, err := y.readWindow(ctx, url, r.Probe, at)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		r.Windows = append(r.Windows, w)
	}
	if added := r.Windows[len(st.ref.Windows):]; len(added) > 0 {
		log.Printf("subtitles: timing reference of %s:%d: %d more windows, %d MB read", hash[:8], index, len(added),
			readBytes(added)>>20)
	}
	y.saveRef(fk, st, r)
	return r, nil
}

func readBytes(ws []refWindow) int64 {
	var n int64
	for _, w := range ws {
		n += w.Read
	}
	return n
}

func (y *syncer) saveRef(fk string, st *syncRefState, r syncRef) {
	st.ref = &r
	if b, err := json.Marshal(r); err == nil {
		if err := y.svc.files.write("syncref", fk, ".json", b); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
	}
}

func (y *syncer) readWindow(ctx context.Context, url string, p mediaProbe, at float64) (refWindow, error) {
	wctx, cancel := context.WithTimeout(ctx, syncWindowTimeout)
	defer cancel()
	return y.read(wctx, url, p, at, syncWindowLen)
}

// windowStarts spreads reference windows over the file: from the opening scene to before the
// end credits, about one per 7 minutes (4 to 10, at most maxN).
func windowStarts(duration, length float64, maxN int) []float64 {
	if duration <= 0 {
		return nil
	}
	n := int(math.Round(duration / 420))
	n = max(4, min(10, maxN, n))
	a, b := 20.0, duration-length-60
	if b <= a {
		a, b = 0, math.Max(0, duration-length)
	}
	if b-a < float64(n-1)*length {
		n = max(1, int((b-a)/length)+1)
	}
	if n == 1 {
		return []float64{a}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Round(a + float64(i)*(b-a)/float64(n-1))
	}
	return out
}

func (y *syncer) probeFile(ctx context.Context, hash string, index int, url string) (mediaProbe, error) {
	fk := syncFileKey(hash, index)
	y.mu.Lock()
	p, ok := y.probes[fk]
	y.mu.Unlock()
	if ok {
		return p, nil
	}
	p, err := y.probe(ctx, url)
	if err != nil {
		return p, err
	}
	y.mu.Lock()
	if len(y.probes) > 1000 {
		y.probes = map[string]mediaProbe{}
	}
	y.probes[fk] = p
	y.mu.Unlock()
	return p, nil
}

// knownFPS is the probed frame rate of the file (0 while unknown).
func (y *syncer) knownFPS(hash string, index int) float64 {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.probes[syncFileKey(hash, index)].FPS
}

// fullReference returns the cues of a subtitle made for the file (shipped in the torrent, or
// an embedded track or hash match already on disk) to time against, nil without one.
func (y *syncer) fullReference(ctx context.Context, hash string, index int) []span {
	y.mu.Lock()
	list := y.lists[syncFileKey(hash, index)]
	y.mu.Unlock()
	for _, s := range list {
		if !s.Synced || s.Forced {
			continue
		}
		if _, _, _, wrapped := unwrapSyncID(s.ID); wrapped {
			continue // timed by us, not made for the file
		}
		source, _, ref, err := decodeID(s.ID)
		if err != nil {
			continue
		}
		var vtt []byte
		if source == SourceTorrent {
			vtt, err = y.svc.File(ctx, s.ID)
		} else if b, ok := y.svc.files.read(source, source+"|"+ref, ".vtt", 0); ok {
			vtt = b
		}
		if err != nil || vtt == nil {
			continue
		}
		cues := vttCues(vtt)
		if len(cues) < 30 {
			continue
		}
		out := make([]span, len(cues))
		for i, c := range cues {
			out[i] = span{c.start, c.end}
		}
		return out
	}
	return nil
}

// listed takes a ranked list for a file: subtitles not made for it get file ids, known timing
// results are applied (fitting ones count as synced), and the list is kept for alternatives.
func (y *syncer) listed(subs []Sub, made map[string]bool, hash string, index int) []Sub {
	for i, s := range subs {
		if made[s.ID] || s.Source == SourceTorrent || s.Source == SourceEmbedded {
			continue
		}
		subs[i].ID = wrapSyncID(s.ID, hash, index)
	}
	y.mu.Lock()
	if len(y.lists) > 200 {
		y.lists = map[string][]Sub{}
	}
	y.lists[syncFileKey(hash, index)] = append([]Sub(nil), subs...)
	y.mu.Unlock()
	return subs
}

// fitOf is the stored timing result of a provider subtitle for the file: 1 fits (as served),
// -1 does not fit, 0 unknown.
func (y *syncer) fitOf(id, hash string, index int) int {
	key, err := syncKey(id, hash, index)
	if err != nil {
		return 0
	}
	m := y.meta(key)
	switch {
	case m == nil:
		return 0
	case m.fits():
		return 1
	case m.State == syncStateReplaced || m.State == syncStatePoor:
		return -1
	}
	return 0
}

// windowCap is how many reference windows the file may have.
func (r syncRef) windowCap() int {
	if r.MaxWindows > 0 {
		return r.MaxWindows
	}
	return syncMaxWindows
}

// maxWindows is how many reference windows of this file fit the byte budget (syncMinWindows
// when its bitrate is unknown).
func (p mediaProbe) maxWindows() int {
	if p.Bitrate <= 0 {
		return syncMinWindows
	}
	return min(syncMaxWindows, int(syncByteBudget/(p.Bitrate*syncWindowLen+syncWindowExtra)))
}
