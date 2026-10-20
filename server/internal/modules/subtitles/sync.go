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
	"path"
	"slices"
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
// subtitle of its language, when one fits. The audio of the play streams adds windows of every
// stretch played (sync_live.go), so results improve while the file plays.

const (
	syncWindowLen     = 60.0      // seconds per reference window
	syncMaxWindows    = 12        // reference windows per file, at most
	syncMinWindows    = 8         // fewer are not reliable (checked on real episodes): no read then, the played audio times it
	syncByteBudget    = 800 << 20 // bytes the windows of one file may read
	syncWindowExtra   = 28 << 20  // read around a window (TorBox proxy: first chunk, read-ahead)
	syncWindowTimeout = 75 * time.Second
	syncLineWait      = 2 * time.Minute // how long the reads may wait for a stream's start (LineGate)
	syncJobLimit      = 5*time.Minute + syncLineWait
	syncPreJobLimit   = 30 * time.Minute // a pre-timing job: its reads wait for room and are throttled
	syncRoomWait      = 3 * time.Minute  // a pre-timing job's reads wait this long for room, together; then it gives up
	syncRoomPoll      = 5 * time.Second
	syncRetryAfter    = 10 * time.Minute // a failed timing is not tried again before this
	syncAlternatives  = 3                // other subtitles tried when one does not fit
	syncReads         = 2                // files whose reference is read at the same time
	syncStartWindows  = 3                // speech windows a pre-timing reads at the start of a file above the byte budget
	syncStartLead     = 15.0             // seconds the first of them starts before the subtitle's dialogue
)

// A file request waits this long for a quick timing result (tests shorten it).
var syncFirstWait = 2500 * time.Millisecond

// The window reads wait this long, so they do not compete with the playback start (tests shorten it).
var syncReadDelay = 20 * time.Second

// LineGate keeps the window reads off the line while a stream starts (streaming implements it).
type LineGate interface {
	// WaitStartQuiet waits while a stream starts (the file's own too); false when ctx ended.
	WaitStartQuiet(ctx context.Context, hash string) bool
}

// ReadRoom says how much of the line the window reads of a file that does not play yet
// (pre-timing) may take now (streaming implements it).
type ReadRoom interface {
	// RoomToRead is the bytes/s a read of a file of torrent hash may take beside the playing
	// streams (0: none now): the line is known and they leave room on it, and on their swarm
	// when hash is one of theirs (a season pack's next episode)
	RoomToRead(hash string) float64
}

type preTimingKey struct{}

// preTiming marks the reads of a pre-timing job (pretime): while its file does not play, every
// window read waits for room beside the playing streams and takes at most that (readRoom).
type preTiming struct {
	hash   string
	index  int
	cancel context.CancelFunc // ends the job when it gives up
	mu     sync.Mutex
	waited time.Duration
	logged bool
	gaveUp bool
}

func (p *preTiming) givenUp() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gaveUp
}

// startReadKey marks the reads of readStart: they stop once the file plays.
type startReadKey struct{}

type readRateKey struct{}

// readRate is the bytes/s a window read under ctx may pull (0: no cap; cappedURL keeps to it).
func readRate(ctx context.Context) float64 {
	r, _ := ctx.Value(readRateKey{}).(float64)
	return r
}

// readRoom is the bytes/s the next window read under ctx may pull (0: no cap); false when ctx
// ended. A read of a pre-timing job waits while its file does not play and the playing streams
// leave less room than the file's bitrate (a window would read slower than it plays); after
// y.roomWait of waiting in all the job gives up (its ctx ends).
func (y *syncer) readRoom(ctx context.Context, bitrate float64) (float64, bool) {
	p, _ := ctx.Value(preTimingKey{}).(*preTiming)
	room := y.svc.cfg.ReadRoom
	if p == nil || room == nil {
		return 0, ctx.Err() == nil
	}
	for {
		if y.viewing(p.hash, p.index) {
			if ctx.Value(startReadKey{}) != nil {
				return 0, false // the start of a file above the byte budget: its tap hears it now
			}
			return 0, ctx.Err() == nil // the episode opened: its timing reads like any other
		}
		if r := room.RoomToRead(p.hash); r > 0 && r >= bitrate {
			return r, ctx.Err() == nil
		}
		p.mu.Lock()
		over, first := p.waited >= y.roomWait, !p.logged
		p.gaveUp, p.logged = over, true
		p.mu.Unlock()
		if over {
			p.cancel()
			return 0, false
		}
		if first {
			log.Printf("subtitles: pre-timing reads of %s:%d wait: no room beside the playing streams", p.hash[:8], p.index)
		}
		t := time.NewTimer(y.roomPoll)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return 0, false
		}
		p.mu.Lock()
		p.waited += y.roomPoll
		p.mu.Unlock()
	}
}

// viewing reports whether the file plays or a subtitle was asked for it in a viewing.
func (y *syncer) viewing(hash string, index int) bool {
	fk := syncFileKey(hash, index)
	return y.livePlaying(fk) || len(y.servedFor(fk)) > 0
}

type lineBudgetKey struct{}

// lineBudget is how long the reads of one timing job may still wait in the LineGate, together.
type lineBudget struct {
	mu   sync.Mutex
	left time.Duration
}

// withLineBudget gives a timing job its shared wait (syncLineWait), so seeks that start new
// streams cannot make it run out of time.
func (y *syncer) withLineBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, lineBudgetKey{}, &lineBudget{left: y.lineWait})
}

// waitLine waits in the LineGate before a window read of hash, at most what is left of the
// job's budget; after that the reads go on without waiting. False when ctx ended.
func (y *syncer) waitLine(ctx context.Context, hash string) bool {
	g := y.svc.cfg.LineGate
	if g == nil {
		return ctx.Err() == nil
	}
	b, _ := ctx.Value(lineBudgetKey{}).(*lineBudget)
	if b == nil {
		b = &lineBudget{left: y.lineWait}
	}
	b.mu.Lock()
	left := b.left
	b.mu.Unlock()
	if left > 0 {
		wctx, cancel := context.WithTimeout(ctx, left)
		start := time.Now()
		g.WaitStartQuiet(wctx, hash)
		cancel()
		b.mu.Lock()
		b.left -= time.Since(start)
		b.mu.Unlock()
	}
	return ctx.Err() == nil
}

// Sync states (X-Subtitle-Sync, logs).
const (
	syncStateSynced    = "synced"    // moved to fit the file
	syncStateUnchanged = "unchanged" // already fitted
	syncStateReplaced  = "replaced"  // did not fit; another subtitle of the language is served
	syncStatePoor      = "poor"      // did not fit, nothing better: served as it is
	syncStatePending   = "pending"
	syncStateFailed    = "failed"   // no reference (file unreadable): served as it is
	syncStateLive      = "live"     // timed (or replaced) from what was read and played so far; may still change
	syncStateOriginal  = "original" // not timed: made for the file, or no file named (header only)
)

type syncMeta struct {
	State      string  `json:"state"`
	Fit        syncFit `json:"fit"`
	ReplacedBy string  `json:"replaced_by,omitempty"` // inner id whose cues are served
	Ref        string  `json:"ref"`                   // reference: full, or the window kinds (subs+audio+live)
	Windows    int     `json:"windows"`
	// Live: provisional, the file is not covered yet; live windows and seconds of it used
	Live        bool    `json:"live,omitempty"`
	LiveWindows int     `json:"live_windows,omitempty"`
	LiveSeconds float64 `json:"live_seconds,omitempty"`
	// RateWindows: the windows of the last fit that tried every frame rate (0 unknown)
	RateWindows int `json:"rate_windows,omitempty"`
	// Version of the timing that made it (syncVersion; 0 before versions)
	Version int `json:"version,omitempty"`
	// Pre: timed before the file played (pre-timing); served once a re-fit with its played audio
	// backs it
	Pre bool `json:"pre,omitempty"`
	// SameCut: the fit was taken from this file of the same cut (a switch during playback)
	SameCut string `json:"same_cut,omitempty"`
	// SameSpeech: the two files' speech matched at offset 0 (speechMatches)
	SameSpeech bool `json:"same_speech,omitempty"`
}

// syncVersion is the version of the timing. A result of an older one is timed again when it is
// asked for (served as it is meanwhile): final results are never re-fitted otherwise, so fixes of
// the timing would not reach them. 1: rates compared net of their offset changes and searched
// again, changes placed over undecided windows (The Mentalist S03E02 kept 24/23.976 for good).
// 2: own-track windows on the speech's timeline (The Mentalist S03E09 was served 0.5 s early).
const syncVersion = 2

func (m syncMeta) fits() bool { return m.State == syncStateSynced || m.State == syncStateUnchanged }

// stale reports a result of an older timing.
func (m *syncMeta) stale() bool { return m != nil && m.Version < syncVersion }

// syncRef is the timing reference of one file.
type syncRef struct {
	Probe   mediaProbe  `json:"probe"`
	Windows []refWindow `json:"windows"`
	Full    bool        `json:"full"` // made of a whole subtitle shipped for the file
	// windows the byte budget allows for this file
	MaxWindows int `json:"max_windows"`
	// Skipped: no windows are read (too few fit the byte budget); only the played audio times it
	Skipped bool `json:"skipped,omitempty"`
	// Start: the speech of its first minutes of dialogue was read for a pre-timing (readStart)
	Start bool `json:"start,omitempty"`
	// Speech read with the own-track windows (same starts): checks the track, stands in for it
	Speech []refWindow `json:"speech,omitempty"`
	// Paired: the reads that pair the windows (own track and speech) were tried; a pair that
	// could not be read is not tried again
	Paired bool     `json:"paired,omitempty"`
	Own    ownCheck `json:"-"` // the own-track check of a fit (checkOwnTrack)
	// OwnAside: the speech stands in for the windows of an own track that found no fit to it (NoFit)
	OwnAside bool `json:"-"`
	// Busy: a stand-in (only the probe) while the file's reference is being read
	Busy bool `json:"-"`
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
	mu    sync.Mutex
	ref   *syncRef
	users int // callers holding it (guarded by syncer.mu); only unused states are evicted
}

type syncer struct {
	svc  *Service
	read syncWindowReader
	// readSubs reads only the own subtitle track (the early windows of a file above the byte budget)
	readSubs syncWindowReader
	probe    func(ctx context.Context, url string) (mediaProbe, error)
	sem      chan struct{}

	mu     sync.Mutex
	refs   map[string]*syncRefState // file key -> reference
	metas  map[string]*syncMeta     // result key -> result (nil: none on disk)
	failed map[string]time.Time
	lists  map[string][]Sub // file key -> last ranked list (alternatives, full references)
	probes map[string]mediaProbe
	holes  map[string]*holeState // result key -> last hole comparison (nil: none on disk)
	titles map[string]aiMeta     // file key -> the title it plays (hole translations)
	altsAt map[string]time.Time  // result key -> last re-fit that fitted its alternatives

	// Live timing (sync_live.go)
	lives   map[string]*liveFile  // file key -> live reference
	served  map[string]*servedSub // result key -> subtitle served for a file
	noLive  map[string]bool       // file keys timed from a whole subtitle (no tap needed)
	early   map[string]bool       // file keys whose early own-track windows were asked for
	liveSem chan struct{}         // re-fits at a time (CPU)
	onGrow  func(f *liveFile)     // more live audio: re-fit soon (tests replace it)
	// result key -> *sync.Mutex: one decide per result at a time (first timing and live re-fits)
	deciding sync.Map

	refitDelay, refitGap, pendingGap time.Duration
	lineWait                         time.Duration // the LineGate wait of one job (syncLineWait; tests shorten it)
	roomWait, roomPoll               time.Duration // a pre-timing job's wait for room (syncRoomWait, syncRoomPoll)
}

func newSyncer(svc *Service) *syncer {
	y := &syncer{svc: svc, read: ffmpegSyncWindow, readSubs: ffmpegSubsWindow, probe: probeMedia, sem: make(chan struct{}, syncReads),
		refs: map[string]*syncRefState{}, metas: map[string]*syncMeta{}, failed: map[string]time.Time{},
		lists: map[string][]Sub{}, probes: map[string]mediaProbe{}, holes: map[string]*holeState{}, titles: map[string]aiMeta{},
		lives: map[string]*liveFile{}, served: map[string]*servedSub{}, noLive: map[string]bool{}, early: map[string]bool{},
		liveSem: make(chan struct{}, 1), refitDelay: liveRefitDelay, refitGap: liveRefitGap, pendingGap: livePendingGap,
		lineWait: syncLineWait, roomWait: syncRoomWait, roomPoll: syncRoomPoll, altsAt: map[string]time.Time{}}
	y.onGrow = y.scheduleRefit
	return y
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
	// Holes describes the holes found against a reference ("2 holes, 40 lines, 38 filled"; "" none)
	Holes string
}

// serve answers a file request of a subtitle served for a file, the player at t (seconds of the
// file, 0 unknown).
func (y *syncer) serve(ctx context.Context, inner, hash string, index int, t float64) (FileAnswer, error) {
	base, err := y.svc.File(ctx, inner)
	if err != nil {
		return FileAnswer{}, err
	}
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return FileAnswer{}, err
	}
	fk := syncFileKey(hash, index)
	first, fresh := y.noteServed(key, inner, hash, index, t)
	// Live audio may still come: keep the players asking
	listening := func() bool { return y.svc.cfg.LiveTiming && (fresh || y.livePlaying(fk)) }
	pending := func() FileAnswer {
		return FileAnswer{VTT: renderVTT(vttCues(base), true), Partial: true, Sync: syncStatePending}
	}
	// A viewing that starts untimed is re-fitted sooner (sync_live.go)
	if first && y.svc.cfg.LiveTiming && untimedMeta(y.meta(key)) {
		y.markPending(hash, index)
	}
	if a, ok := y.answer(key, base, listening); ok {
		if y.meta(key).stale() && !y.recentlyFailed(key) {
			// Timed by an older version: served as it is while it is timed again
			y.startTiming(inner, hash, index, base, false)
			if !a.Partial {
				a.VTT, a.Partial = renderVTT(vttCues(a.VTT), true), true
			}
		}
		return a, nil
	}
	if y.recentlyFailed(key) {
		if listening() {
			y.kickLive(hash, index)
			return pending(), nil
		}
		return FileAnswer{VTT: base, Sync: syncStateFailed}, nil
	}
	call := y.startTiming(inner, hash, index, base, false)
	timer := time.NewTimer(syncFirstWait)
	defer timer.Stop()
	finished := false
	select {
	case <-call.done:
		finished = true
	case <-timer.C:
	case <-ctx.Done():
	}
	if a, ok := y.answer(key, base, listening); ok {
		return a, nil
	}
	if finished && !listening() {
		return FileAnswer{VTT: base, Sync: syncStateFailed}, nil
	}
	return pending(), nil
}

// pretime starts the timing of a subtitle for a file that does not play yet (the players ask for
// the subtitle they would show on the next episode near the end of the current one), so its
// reference is read and the subtitle fitted before the file opens. It is not a viewing: nothing
// is noted as served (live re-fits, the first answers' grace). Reads wait in the LineGate like
// every timing read, and until the file plays each window read (TorBox files too: the same home
// line) also waits for room beside the playing streams and takes at most that (readRoom); the
// job gives up after syncRoomWait of waiting. It returns the timing state.
func (y *syncer) pretime(ctx context.Context, inner, hash string, index int) (string, error) {
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return "", err
	}
	if m := y.meta(key); m != nil && !m.stale() {
		return m.State, nil
	}
	if y.recentlyFailed(key) {
		return syncStateFailed, nil
	}
	if y.svc.flights.running("sync|" + key) {
		return syncStatePending, nil
	}
	base, err := y.svc.File(ctx, inner)
	if err != nil {
		return "", err
	}
	log.Printf("subtitles: pre-timing %s on %s:%d (next episode)", shortSubID(inner), hash[:8], index)
	y.startTiming(inner, hash, index, base, true)
	return syncStatePending, nil
}

// answer is the stored answer of key; a provisional one is marked live (pending while nothing
// fits yet) and partial while more audio may come. A pre-timed fit shows once a re-fit with the
// played audio backed it: fits from the read windows alone can miss a teaser's own offset.
func (y *syncer) answer(key string, base []byte, listening func() bool) (FileAnswer, bool) {
	a, ok := y.result(key, base)
	if !ok {
		return a, false
	}
	if m := y.meta(key); m != nil && m.Live {
		a.Sync = syncStatePending
		if m.fits() || m.State == syncStateReplaced {
			a.Sync = syncStateLive
		}
		if listening() {
			if m.Pre {
				return FileAnswer{VTT: renderVTT(vttCues(base), true), Partial: true, Sync: syncStatePending}, true
			}
			a.VTT, a.Partial = renderVTT(vttCues(a.VTT), true), true
		}
	}
	return a, true
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
	m.Version = syncVersion
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

// startTiming starts (or joins) the background timing of a subtitle for the file (one job per
// result); pre marks a pre-timing job (pretime). A failed job, or one that left a result of an
// older version, is not started again for syncRetryAfter, unless it was a pre-timing job of a
// file nobody plays yet (its reads waited or were cut for the playing streams): the episode's
// first request times it again.
func (y *syncer) startTiming(inner, hash string, index int, base []byte, pre bool) *flightCall {
	key, _ := syncKey(inner, hash, index)
	return y.svc.flights.start("sync|"+key, func() (any, error) {
		limit := syncJobLimit
		if pre {
			limit = syncPreJobLimit
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		ctx = y.withLineBudget(ctx)
		defer cancel()
		var p *preTiming
		if pre {
			p = &preTiming{hash: hash, index: index, cancel: cancel}
			ctx = context.WithValue(ctx, preTimingKey{}, p)
		}
		start := time.Now()
		err := y.run(ctx, inner, hash, index, base)
		switch {
		case p != nil && p.givenUp():
			log.Printf("subtitles: pre-timing of %s on %s:%d given up after %s: no room beside the playing streams",
				shortSubID(inner), hash[:8], index, time.Since(start).Round(time.Second))
		case err != nil:
			log.Printf("subtitles: timing of %s on %s:%d failed after %s: %v", shortSubID(inner), hash[:8], index,
				time.Since(start).Round(time.Second), err)
		}
		if (err != nil || y.meta(key).stale()) && (p == nil || y.viewing(hash, index)) {
			y.mu.Lock()
			y.failed[key] = time.Now()
			y.mu.Unlock()
		}
		return nil, err
	})
}

// run times one subtitle for the file and stores the result. A reference saved before the
// own-track check times it at once; the reads that pair its windows come after that fit, which
// is then done again with them. A read reference that times nothing (an own track still to be
// checked, whose speech could not be read) is an error: the job is not started again for
// syncRetryAfter.
func (y *syncer) run(ctx context.Context, inner, hash string, index int, base []byte) error {
	if y.svc.cfg.LiveTiming {
		y.sameCut(ctx, inner, hash, index, base)
	}
	ref, err := y.reference(ctx, hash, index)
	if err != nil {
		return err
	}
	if ref.Skipped && ctx.Value(preTimingKey{}) != nil {
		ref = y.startRef(ctx, hash, index, base, ref)
	}
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return err
	}
	mu := y.decideLock(key)
	decideNow := func(ref syncRef) bool {
		mu.Lock()
		defer mu.Unlock()
		return y.decide(ctx, inner, hash, index, base, ref, true)
	}
	decided := decideNow(ref)
	if ref.unpaired() || y.svc.flights.running("syncpair|"+syncFileKey(hash, index)) {
		if paired, ok := y.pairRef(ctx, hash, index); ok {
			decided = decideNow(paired) || decided
		}
	}
	if !decided && !ref.Skipped && !ref.Full {
		return errors.New("too little reference to time it (own-track windows wait for their check against the speech)")
	}
	return nil
}

// knownProbe is a file's probe from memory or its reference on disk, without probing it.
func (y *syncer) knownProbe(hash string, index int) (mediaProbe, bool) {
	fk := syncFileKey(hash, index)
	y.mu.Lock()
	p, ok := y.probes[fk]
	y.mu.Unlock()
	if ok {
		return p, true
	}
	st, release := y.refState(fk)
	defer release()
	if !st.mu.TryLock() {
		return mediaProbe{}, false
	}
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) {
		return mediaProbe{}, false
	}
	return st.ref.Probe, st.ref.Probe.Duration > 0
}

func (y *syncer) decideLock(key string) *sync.Mutex {
	mu, _ := y.deciding.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// decide fits a subtitle (else the best fitting alternative) to the file's read and live
// windows and stores the result; false when there is too little reference yet. read allows
// reading more windows where the offset changes.
func (y *syncer) decide(ctx context.Context, inner, hash string, index int, base []byte, ref syncRef, read bool) bool {
	start := time.Now()
	key, err := syncKey(inner, hash, index)
	if err != nil {
		return false
	}
	var live []refWindow
	var liveSecs float64
	var f *liveFile
	complete := false
	if ref.Full {
		y.mu.Lock()
		y.noLive[syncFileKey(hash, index)] = true
		y.mu.Unlock()
	} else if y.svc.cfg.LiveTiming {
		f = y.liveFile(hash, index)
		f.setDuration(ref.Probe.Duration)
		live, liveSecs = f.windows()
		f.mu.Lock()
		complete = f.complete
		f.mu.Unlock()
	}
	r := y.fitRef(inner, hash, index, ref, live, nil)
	if f != nil {
		y.saveLive(f) // the file has a timed subtitle: keep its live windows (and the own-track check)
	}
	if tooLittleRef(r.ref, r.live) {
		return false // too little to tell yet
	}
	served := y.meta(key)
	prev := served
	if prev.stale() {
		prev = nil // an older timing's result holds back nothing (rate, hysteresis)
	}
	m, vtt, r := y.fitAside(ctx, inner, hash, index, r, base, read, prev, complete)
	ref, live = r.ref, r.live
	provisional := !ref.Full && y.svc.cfg.LiveTiming && !complete
	if provisional && !m.fits() && takesCopy(prev) {
		copied, unmatched := y.copySameCut(ctx, key, inner, hash, index, base, r, liveSecs, served, prev, m)
		if copied {
			return true
		}
		if unmatched && prev.SameSpeech {
			// its file's speech no longer matches: the copy is held against this file's windows
			p := *prev
			p.SameSpeech = false
			prev = &p
		}
	}
	if y.keepAccepted(key, base, prev, m, ref.Probe.Duration, complete) {
		y.keepRates(key, prev, m)
		logKept(inner, hash, index, m, len(live), liveSecs)
		return true
	}
	if keepStable(prev, m, base, ref.Probe.Duration, complete) {
		if _, ok := y.result(key, base); ok {
			if prev.Pre && len(live) > 0 {
				// The played audio backs the pre-timed fit: served from now on
				kept := *prev
				kept.Pre, kept.LiveWindows, kept.LiveSeconds = false, len(live), liveSecs
				kept.RateWindows = max(kept.RateWindows, m.RateWindows)
				y.storeWatched(key, inner, hash, index, base, prev, kept, nil)
				logSync(inner, hash, index, kept, time.Since(start))
			} else {
				y.keepRates(key, prev, m)
			}
			return true
		}
	}
	// Timed before the file played (pre-timing): none of its audio was heard
	pre := provisional && len(live) == 0 && !y.livePlaying(syncFileKey(hash, index))
	if !m.fits() && !(provisional && m.Fit.linedUp() && (prev == nil || prev.State != syncStateReplaced)) {
		// Did not fit: the best fitting other subtitle of the language, when one fits. While
		// provisional, a lined up subtitle stays served as it is (a few minutes cannot tell
		// whether another release's offset holds for the whole file).
		alts := y.alternatives(hash, index, inner)
		if len(alts) > 0 && (prev == nil || !prev.fits()) && !y.altsDue(key, read) {
			return true // what is served stays until the alternatives are fitted again
		}
		for _, alt := range alts {
			altBase, err := y.svc.File(ctx, alt)
			if err != nil {
				continue
			}
			altKey, err := syncKey(alt, hash, index)
			if err != nil {
				continue
			}
			am := y.meta(altKey)
			if am.stale() {
				am = nil
			}
			altPrev := am
			var altVTT []byte
			if am != nil && am.Live {
				am = nil // provisional: time it with the windows there are now
			}
			if am != nil {
				if a, ok := y.result(altKey, altBase); ok {
					altVTT = a.VTT
				} else if am.State == syncStateSynced {
					am = nil // its timed cues are gone from the cache: time it again
				}
			}
			if am == nil {
				var a syncMeta
				a, altVTT, r = y.fitAside(ctx, inner, hash, index, r, altBase, read, altPrev, complete)
				ref, live = r.ref, r.live
				if y.keepAccepted(altKey, altBase, altPrev, a, ref.Probe.Duration, complete) {
					y.keepRates(altKey, altPrev, a)
					logKept(alt, hash, index, a, len(live), liveSecs)
					am = altPrev
					r, _ := y.result(altKey, altBase)
					altVTT = r.VTT
				} else {
					a.Live, a.LiveWindows, a.LiveSeconds, a.Pre = !ref.Full && y.svc.cfg.LiveTiming && !complete, len(live), liveSecs, pre
					y.store(altKey, a, altVTT)
					am = &a
					if read || fitChanged(altPrev, a) {
						logSync(alt, hash, index, a, time.Since(start))
					}
				}
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
	m.Live, m.LiveWindows, m.LiveSeconds, m.Pre = provisional, len(live), liveSecs, pre
	y.storeWatched(key, inner, hash, index, base, served, m, vtt)
	if read || fitChanged(prev, m) {
		logSync(inner, hash, index, m, time.Since(start))
		if m.fits() && y.svc.cfg.LiveTiming {
			y.offerSameCut(inner, syncFileKey(hash, index))
		}
	}
	return true
}

// storeWatched stores a result of key and, while a player of it sent its position, logs how the
// cues ahead of the viewer moved (logNear).
func (y *syncer) storeWatched(key, inner, hash string, index int, base []byte, prev *syncMeta, m syncMeta, vtt []byte) {
	_, watched := y.viewerAt(key)
	var before []liveCue
	if watched {
		before = y.servedCues(key, base, prev)
	}
	y.store(key, m, vtt)
	if watched {
		y.logNear(key, inner, hash, index, before, y.servedCues(key, base, &m))
	}
}

// keepAccepted reports whether a provisional fit that was accepted stays served although the
// new one does not fit: a few more windows can tip a good timing below the bars (seen on The
// Mentalist S02E10: accepted from 3 windows, poor at 6, two offsets fitting again at 8), and
// serving the subtitle untimed meanwhile is worse. Once the file is covered the new result counts;
// a fit taken from another file of the same cut stays only while it fits this file (copyFits).
func (y *syncer) keepAccepted(key string, base []byte, prev *syncMeta, m syncMeta, duration float64, complete bool) bool {
	if m.fits() || complete || prev == nil || !prev.Live || !prev.fits() || !copyFits(prev, m, base, duration) {
		return false
	}
	_, ok := y.result(key, base)
	return ok
}

// keepStable reports whether a provisional fit that was accepted stays served although the new
// one fits too: unless the new offsets beat it on the current windows by an offset change's
// worth of matched lines, the cues would only move back and forth between re-fits.
func keepStable(prev *syncMeta, m syncMeta, base []byte, duration float64, complete bool) bool {
	if complete || prev == nil || !prev.Live || !prev.fits() || !m.fits() || len(m.Fit.windows) == 0 ||
		m.Fit.Scale != prev.Fit.Scale || !fitChanged(prev, m) || !copyFits(prev, m, base, duration) {
		return false
	}
	old, lines := rescore(baseSpans(base, duration), m.Fit.windows, prev.Fit)
	return m.Fit.Score*lines-old < syncSplitPenalty
}

// copyFits reports whether a fit taken from another file of the same cut (sameCut) fits this
// file's own windows, the new fit's: its offsets beat chance there by the gain bar. The copy is
// not held against them (another cut, or another episode of the same length), unless the files'
// speech matched (speech alone may miss the bar: The Mentalist S03E03, gain 0.12 for the right fit).
func copyFits(prev *syncMeta, m syncMeta, base []byte, duration float64) bool {
	if prev.SameCut == "" || prev.SameSpeech || len(m.Fit.windows) == 0 {
		return true
	}
	old, lines := rescore(baseSpans(base, duration), m.Fit.windows, prev.Fit)
	return lines > 0 && old/lines-m.Fit.Base >= syncMinGain
}

// baseSpans are the subtitle's cue times as the fits see them.
func baseSpans(base []byte, duration float64) []span {
	cues := vttCues(base)
	spans := make([]span, len(cues))
	for i, c := range cues {
		spans[i] = span{c.start, c.end}
	}
	return clampSpans(spans, duration)
}

// keepRates notes on a kept result that every frame rate was tried with m's windows (meta only,
// nothing served changes), so the next re-fits keep its rate until the reference grew by half
// again instead of trying every rate each time.
func (y *syncer) keepRates(key string, prev *syncMeta, m syncMeta) {
	if prev != nil && m.RateWindows > prev.RateWindows {
		kept := *prev
		kept.RateWindows = m.RateWindows
		y.store(key, kept, nil)
	}
}

// altsDue reports whether a decide may fit the alternatives of key now: the first timing (read)
// always, a re-fit at most every refitGap, so the re-fits of an untimed viewing's first minutes
// (every 5 s) do not fit up to syncAlternatives other subtitles at every frame rate each time.
func (y *syncer) altsDue(key string, read bool) bool {
	now := time.Now()
	y.mu.Lock()
	defer y.mu.Unlock()
	if !read && now.Sub(y.altsAt[key]) < y.refitGap {
		return false
	}
	if len(y.altsAt) > 1000 {
		y.altsAt = map[string]time.Time{}
	}
	y.altsAt[key] = now
	return true
}

func logKept(inner, hash string, index int, m syncMeta, liveWindows int, liveSecs float64) {
	log.Printf("subtitles: timing of %s on %s:%d: kept the last accepted fit (new fit %s: score %.2f over %.2f, peak %.2f; %d windows (%d live, %.0f s played))",
		shortSubID(inner), hash[:8], index, m.State, m.Fit.Score, m.Fit.Base, m.Fit.Peak, m.Windows, liveWindows, liveSecs)
}

// fitChanged reports whether a new result differs from the stored one in what is served.
func fitChanged(prev *syncMeta, m syncMeta) bool {
	if prev == nil || prev.State != m.State || prev.ReplacedBy != m.ReplacedBy || prev.Live != m.Live || prev.Pre != m.Pre ||
		prev.Fit.Scale != m.Fit.Scale || len(prev.Fit.Segments) != len(m.Fit.Segments) {
		return true
	}
	for i, s := range m.Fit.Segments {
		p := prev.Fit.Segments[i]
		if math.Abs(p.Offset-s.Offset) > 0.05 || (i > 0 && math.Abs(p.From-s.From) > 1) {
			return true
		}
	}
	return false
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
	if m.Live {
		extra += " (provisional)"
	}
	if m.Pre {
		extra += " (pre-timed: shown once the played audio backs it)"
	}
	live := ""
	if m.LiveWindows > 0 {
		live = fmt.Sprintf(" (%d live, %.0f s played)", m.LiveWindows, m.LiveSeconds)
	}
	log.Printf("subtitles: timing of %s on %s:%d: %s%s (scale %.4f, offsets %s, score %.2f over %.2f, peak %.1f; %s reference, %d windows%s, %s)",
		shortSubID(inner), hash[:8], index, m.State, extra, f.Scale, strings.Join(offs, " "), f.Score, f.Base, f.Peak,
		m.Ref, m.Windows, live, took.Round(100*time.Millisecond))
}

// shortSubID names a subtitle in logs: source and provider reference.
func shortSubID(id string) string {
	source, lang, ref, err := decodeID(id)
	if err != nil {
		return "?"
	}
	return truncateString(source+"/"+lang+"/"+ref, 60)
}

// fitRefs are the windows a fit of a subtitle for the file uses (fitRef).
type fitRefs struct {
	ref  syncRef
	live []refWindow
	// own, ownLive: the file's reference as read and its live windows, before the own-track check
	// (a set-aside track's windows, decide's fallback)
	own     syncRef
	ownLive []refWindow
	same    map[string]bool // the speech matched with files of the same cut, by file key (sameSpeech)
}

// aside are the windows of an own track set aside (OwnAside), as read and played, moved onto the
// speech by the check's best lag.
func (r fitRefs) aside() fitRefs {
	c := r.ref.Own
	a := fitRefs{ref: r.own, live: c.onSpeech(r.ownLive), own: r.own, ownLive: r.ownLive, same: r.same}
	a.ref.Windows = c.onSpeech(r.own.Windows)
	return a
}

// fitRef builds the windows a fit uses from the file's reference and live windows: own-track
// windows only as the own-track check allows (checkOwnTrack; a set-aside track's while there is
// too little speech), with the timed own-track windows of a file of the same cut (sameCutWindows).
// Every fit builds them so, the first timing's after reading more windows too (fitOne). same
// keeps the speech matches with other files for one decide (nil: a new one).
func (y *syncer) fitRef(inner, hash string, index int, ref syncRef, live []refWindow, same map[string]bool) fitRefs {
	if same == nil {
		same = map[string]bool{}
	}
	r := fitRefs{own: ref, ownLive: live, same: same}
	r.ref, r.live = y.checkOwnTrack(hash, index, ref, live)
	r.own.Own = r.ref.Own
	if r.ref.OwnAside && tooLittleRef(r.ref, r.live) {
		a := r.aside() // too little speech yet: the own track's windows, as before the check
		r.ref, r.live = a.ref, a.live
	}
	r.ref = y.sameCutWindows(inner, hash, index, r)
	return r
}

// tooLittleRef reports whether the windows are too few to tell a fit yet.
func tooLittleRef(ref syncRef, live []refWindow) bool {
	merged := mergeWindows(ref.Windows, live)
	return !ref.Full && (windowSeconds(merged) < liveMinCover || (len(live) > 0 || ref.Skipped) && refLines(prepare(merged)) < liveMinLines)
}

// fitOne aligns one subtitle to the file's reference and live windows (with read, reading more
// windows where the offset changes) and returns its result and the WebVTT to serve (nil: serve
// it as it is). A provisional fit that fitted before keeps its scale (fewer FFTs per re-fit)
// until the reference has grown by half since every rate was tried, and the final fit (complete)
// tries them all: a rate chosen from a few minutes is not kept for good (The Mentalist S03E02).
// The timed cues inside own-track windows are refined onto the own lines (sync_refine.go). The
// windows read are added to the file's reference as read (r.own), and the fit's are built from
// it again (fitRef); it returns those it fitted with.
func (y *syncer) fitOne(ctx context.Context, inner, hash string, index int, r fitRefs, vtt []byte, read bool, prev *syncMeta, final bool) (syncMeta, []byte, fitRefs) {
	ref, live := r.ref, r.live
	cues := vttCues(vtt)
	spans := make([]span, len(cues))
	for i, c := range cues {
		spans[i] = span{c.start, c.end}
	}
	scales := syncScales
	if n := len(mergeWindows(ref.Windows, live)); prev != nil && prev.Live && prev.fits() && prev.Fit.Scale > 0 && !final &&
		2*n < 3*prev.RateWindows {
		scales = []float64{prev.Fit.Scale}
	}
	// lengthFits still sees every cue (a longer cut runs past the end)
	timed := clampSpans(spans, ref.Probe.Duration)
	var windows []refWindow
	fitTo := func() (syncFit, bool) {
		windows = mergeWindows(ref.Windows, live)
		if len(live) > 0 || ref.Skipped { // early windows of a file above the byte budget: the live rules
			return alignLive(timed, windows, scales)
		}
		return alignWith(timed, prepare(windows), scales, syncSplitPenalty, -1)
	}
	fit, ok := fitTo()
	for round := 0; read && ok && !ref.Full && !ref.Skipped && round < 2; round++ {
		extra := fit.extraWindows(syncWindowLen, r.own.windowCap()-len(r.own.Windows))
		if len(extra) == 0 {
			break
		}
		more, err := y.addWindows(ctx, hash, index, extra)
		if err != nil || len(more.Windows) == len(r.own.Windows) {
			break
		}
		r = y.fitRef(inner, hash, index, more, r.ownLive, r.same) // as every fit builds it
		ref, live = r.ref, r.live
		fit, ok = fitTo()
	}
	m := syncMeta{State: syncStatePoor, Fit: fit, Ref: refKind(ref, windows), Windows: len(windows)}
	if len(scales) > 1 {
		m.RateWindows = len(windows)
	} else if prev != nil {
		m.RateWindows = prev.RateWindows
	}
	if !ok || !fit.good() || !lengthFits(spans, fit, ref.Probe.Duration) {
		return m, nil, r
	}
	served := refineFit(cues, fit, refineWindows(ref, live))
	if !fit.changes() && !refineMoved(cues, served) {
		m.State = syncStateUnchanged
		return m, nil, r
	}
	m.State = syncStateSynced
	return m, renderVTT(served, false), r
}

// fitAside is fitOne and, when the speech does not time the subtitle, a fit to the windows of an
// own track set aside (OwnAside: no fit to the speech; as before the check), for the subtitle and
// each alternative alike. It returns the windows of the fit with the speech.
func (y *syncer) fitAside(ctx context.Context, inner, hash string, index int, r fitRefs, vtt []byte, read bool, prev *syncMeta, final bool) (syncMeta, []byte, fitRefs) {
	m, out, r := y.fitOne(ctx, inner, hash, index, r, vtt, read, prev, final)
	if r.ref.OwnAside && !m.fits() {
		if om, ovtt, _ := y.fitOne(ctx, inner, hash, index, r.aside(), vtt, false, prev, final); om.fits() {
			return om, ovtt, r
		}
	}
	return m, out, r
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

// refState returns the reference state of a file; release gives it back.
func (y *syncer) refState(fk string) (st *syncRefState, release func()) {
	y.mu.Lock()
	defer y.mu.Unlock()
	st = y.refs[fk]
	if st == nil {
		if len(y.refs) >= 32 {
			for k, o := range y.refs {
				if o.users == 0 {
					delete(y.refs, k)
				}
			}
		}
		st = &syncRefState{}
		y.refs[fk] = st
	}
	st.users++
	return st, func() {
		y.mu.Lock()
		st.users--
		y.mu.Unlock()
	}
}

// loadRef fills st from the reference saved on disk; false without one.
func (y *syncer) loadRef(fk string, st *syncRefState) bool {
	if st.ref != nil {
		return true
	}
	b, ok := y.svc.files.read("syncref", fk, ".json", 0)
	if !ok {
		return false
	}
	var r syncRef
	if json.Unmarshal(b, &r) != nil || (len(r.Windows) == 0 && !r.Skipped) {
		return false
	}
	st.ref = &r
	return true
}

// reference returns the file's timing reference, read once (cached on disk). One saved before
// picture tracks counted is probed again (pairRef then reads a PGS track found now).
func (y *syncer) reference(ctx context.Context, hash string, index int) (syncRef, error) {
	fk := syncFileKey(hash, index)
	st, release := y.refState(fk)
	defer release()
	st.mu.Lock()
	defer st.mu.Unlock()
	url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
	if y.loadRef(fk, st) {
		r := *st.ref
		if !r.Full && !r.Probe.subsKnown() {
			if p, err := y.probeFile(ctx, hash, index, url); err == nil {
				r.Probe.OwnSub, r.Probe.SubCodec = p.OwnSub, p.SubCodec
				y.saveRef(fk, st, r)
			}
		}
		return r, nil
	}
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
			// Too dear to read: the played audio times it (sync_live.go)
			r.Skipped = true
			log.Printf("subtitles: timing reference of %s:%d: %.1f MB/s, %d windows would read more than %d MB: no read, timed from the played audio",
				hash[:8], index, probe.Bitrate/(1<<20), syncMinWindows, syncByteBudget>>20)
			y.saveRef(fk, st, r)
			return r, nil
		}
		select {
		case <-time.After(syncReadDelay):
		case <-ctx.Done():
			return syncRef{}, ctx.Err()
		}
		starts := windowStarts(probe.Duration, syncWindowLen, r.MaxWindows)
		for _, at := range starts {
			if w, speech, err := y.readWindow(ctx, hash, url, probe, at, r.bytesRead()); err == nil {
				r.Windows = append(r.Windows, w)
				r.Speech = append(r.Speech, speech...)
			} else if ctx.Err() != nil {
				break // out of time: what was read counts (below)
			} else {
				log.Printf("subtitles: timing reference of %s:%d: %v", hash[:8], index, err)
			}
		}
		r.Paired = true
		// The reference is kept for good: a weak one is not saved, a later timing reads it again
		if len(r.Windows) < max(3, (len(starts)+1)/2) {
			if ctx.Err() != nil {
				return syncRef{}, ctx.Err()
			}
			return syncRef{}, fmt.Errorf("only %d of %d reference windows could be read", len(r.Windows), len(starts))
		}
	}
	kind := r.kind()
	if kind == "subs" {
		kind += " (" + r.Probe.ownKind() + ")"
	}
	log.Printf("subtitles: timing reference of %s:%d: %s, %d windows, %d MB read in %s", hash[:8], index, kind,
		len(r.Windows), r.bytesRead()>>20, time.Since(start).Round(100*time.Millisecond))
	y.saveRef(fk, st, r)
	return r, nil
}

// unpaired reports whether the reference was saved before the own-track check and reads would
// pair its windows: own-track windows without their speech, or speech windows of a file whose
// probe found an own track only now (a PGS track; the speech is their pair).
func (r syncRef) unpaired() bool {
	if r.Paired || r.Full || r.Skipped || r.Probe.Audio < 0 {
		return false
	}
	if !hasOwnWindows(r.Windows) {
		return r.Probe.OwnSub >= 0 && len(r.Windows) > 0
	}
	for _, w := range r.Windows {
		if w.Kind == "subs" && !hasWindowAt(r.Speech, w.Start) {
			return true
		}
	}
	return false
}

func hasWindowAt(ws []refWindow, start float64) bool {
	return slices.ContainsFunc(ws, func(w refWindow) bool { return w.Start == start })
}

// bytesRead is what the reference's reads pulled (windows and the speech read with them).
func (r syncRef) bytesRead() int64 { return readBytes(r.Windows) + readBytes(r.Speech) }

// pairRef completes a reference saved before the own-track check (unpaired): one pass of reads
// per file at a time (callers join it), without holding the reference's lock, so re-fits and
// holes use the saved reference meanwhile. What was read is merged into the reference as it is
// then, also when the pass is cut; a failed read is not tried again. It returns the reference
// after the pass; false when ctx ended first.
func (y *syncer) pairRef(ctx context.Context, hash string, index int) (syncRef, bool) {
	fk := syncFileKey(hash, index)
	call := y.svc.flights.start("syncpair|"+fk, func() (any, error) {
		pctx, cancel := context.WithTimeout(context.Background(), syncJobLimit)
		defer cancel()
		y.readPairs(y.withLineBudget(pctx), hash, index)
		return nil, nil
	})
	select {
	case <-call.done:
	case <-ctx.Done():
		return syncRef{}, false
	}
	st, release := y.refState(fk)
	defer release()
	st.mu.Lock()
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) {
		return syncRef{}, false
	}
	return *st.ref, true
}

// readPairs reads what pairs the windows of the saved reference (unpaired): the speech of
// own-track windows read without it, or the own track where only speech was read.
func (y *syncer) readPairs(ctx context.Context, hash string, index int) {
	fk := syncFileKey(hash, index)
	st, release := y.refState(fk)
	defer release()
	st.mu.Lock()
	ok := y.loadRef(fk, st)
	var r syncRef
	if ok {
		r = *st.ref
	}
	st.mu.Unlock()
	if !ok || !r.unpaired() {
		return
	}
	select {
	case y.sem <- struct{}{}:
		defer func() { <-y.sem }()
	case <-ctx.Done():
		return
	}
	select {
	case <-time.After(syncReadDelay):
	case <-ctx.Done():
		return
	}
	start := time.Now()
	url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
	upgrade := !hasOwnWindows(r.Windows)
	var todo []float64
	for _, w := range r.Windows {
		if upgrade && w.Kind != "subs" || !upgrade && w.Kind == "subs" && !hasWindowAt(r.Speech, w.Start) {
			todo = append(todo, w.Start)
		}
	}
	speechOnly := r.Probe
	speechOnly.OwnSub = -1
	var own, speech []refWindow
	for _, at := range todo {
		if !y.waitLine(ctx, hash) {
			break
		}
		if upgrade { // the own track there (the speech read before is its pair)
			if _, ok := y.readRoom(ctx, r.Probe.Bitrate); !ok {
				break
			}
			wctx, cancel := context.WithTimeout(ctx, syncWindowTimeout)
			if o, err := y.readSubs(wctx, url, r.Probe, at, syncWindowLen); err == nil && o.Kind == "subs" {
				own = append(own, o)
			}
			cancel()
		} else if s, err := y.readOne(ctx, url, speechOnly, at); err == nil && s.Kind != "subs" {
			speech = append(speech, s)
		}
	}
	cut := ctx.Err() != nil
	st.mu.Lock()
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) {
		return
	}
	c := *st.ref
	c.Windows = append([]refWindow(nil), c.Windows...)
	c.Speech = append([]refWindow(nil), c.Speech...)
	for _, s := range speech {
		if !hasWindowAt(c.Speech, s.Start) {
			c.Speech = append(c.Speech, s)
		}
	}
	for _, o := range own {
		i := slices.IndexFunc(c.Windows, func(w refWindow) bool { return w.Start == o.Start && w.Kind != "subs" })
		if i < 0 {
			continue
		}
		if !hasWindowAt(c.Speech, o.Start) {
			c.Speech = append(c.Speech, c.Windows[i])
		}
		c.Windows[i] = o
	}
	c.Paired = c.Paired || !cut
	what := "the speech of its own-track windows"
	if upgrade {
		what = "its own " + c.Probe.ownKind() + " track where its speech was read"
	}
	extra := ""
	if cut {
		extra = " (cut: the rest next time)"
	}
	log.Printf("subtitles: timing reference of %s:%d: read %s, %d of %d windows, %d MB in %s%s", hash[:8], index, what,
		len(own)+len(speech), len(todo), (readBytes(own)+readBytes(speech))>>20, time.Since(start).Round(100*time.Millisecond), extra)
	y.saveRef(fk, st, c)
}

// startRef completes the reference of a file above the byte budget for a pre-timing job (the
// next episode, which does not play yet): the speech of its first minutes of dialogue is read
// once per file (readStart; callers join a running pass), so the episode does not open untimed
// for the two minutes its first live fit needs (Westworld S01E05 4K: 0.8 s late from 105 s to
// 218 s). Those windows are fitted by the live rules, like the early own-track windows. It
// returns the reference after the pass (ref when ctx ended first).
func (y *syncer) startRef(ctx context.Context, hash string, index int, base []byte, ref syncRef) syncRef {
	if ref.Start || ref.Probe.Audio < 0 {
		return ref
	}
	fk := syncFileKey(hash, index)
	for range 2 { // a pass joined as it ended cut (its job gave up) goes on with this job
		call := y.svc.flights.start("syncstart|"+fk, func() (any, error) {
			y.readStart(ctx, hash, index, base)
			return nil, nil
		})
		select {
		case <-call.done:
		case <-ctx.Done():
			return ref
		}
		st, release := y.refState(fk)
		st.mu.Lock()
		if y.loadRef(fk, st) {
			ref = *st.ref
		}
		st.mu.Unlock()
		release()
		if ref.Start || ctx.Err() != nil {
			break
		}
	}
	return ref
}

// readStart reads the speech windows of startWindows (only the audio: an own track would wait
// for its check against the speech) without holding the reference's lock, within the pre-timing's
// room, and stops once the file plays: its tap hears that stretch then, and a read ahead of a
// playing stream of such a file would take its line. A pass cut by the job's end keeps what it
// read and is tried again by the next pre-timing.
func (y *syncer) readStart(ctx context.Context, hash string, index int, base []byte) {
	fk := syncFileKey(hash, index)
	st, release := y.refState(fk)
	defer release()
	st.mu.Lock()
	ok := y.loadRef(fk, st)
	var r syncRef
	if ok {
		r = *st.ref
	}
	st.mu.Unlock()
	if !ok || !r.Skipped || r.Start {
		return
	}
	var todo []float64
	for _, at := range startWindows(r.Probe, vttCues(base)) {
		if !slices.ContainsFunc(r.Windows, func(w refWindow) bool { return math.Abs(w.Start-at) < syncWindowLen/2 }) {
			todo = append(todo, at)
		}
	}
	var got []refWindow
	began := time.Now()
	if len(todo) > 0 {
		select {
		case y.sem <- struct{}{}:
			defer func() { <-y.sem }()
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(syncReadDelay):
		case <-ctx.Done():
			return
		}
		url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
		speechOnly := r.Probe
		speechOnly.OwnSub = -1
		sctx := context.WithValue(ctx, startReadKey{}, true)
		for _, at := range todo {
			if !y.waitLine(sctx, hash) || y.viewing(hash, index) {
				break
			}
			if w, err := y.readOne(sctx, url, speechOnly, at); err == nil && w.Kind == "audio" {
				got = append(got, w)
			} else if ctx.Err() != nil {
				break
			}
		}
	}
	cut := ctx.Err() != nil
	st.mu.Lock()
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) {
		return
	}
	c := *st.ref
	c.Windows = append(append([]refWindow(nil), c.Windows...), got...)
	c.Start = c.Start || !cut
	if len(todo) > 0 {
		extra := ""
		if cut {
			extra = " (cut: the rest next time)"
		}
		log.Printf("subtitles: timing reference of %s:%d: the speech of its start (above the byte budget), %d of %d windows from %.0fs, %d MB read in %s%s",
			hash[:8], index, len(got), len(todo), todo[0], readBytes(got)>>20, time.Since(began).Round(100*time.Millisecond), extra)
	}
	y.saveRef(fk, st, c)
}

// startWindows are the starts of the speech windows read at the start of a file above the byte
// budget: contiguous ones, as many as syncByteBudget allows (syncStartWindows at most, none when
// fewer than a live fit needs), the first syncStartLead before the subtitle's first stretch of
// dialogue (5 lines within a minute; a title or credit line before it does not count), where an
// episode opened at 0 or past its intro needs its first cues.
func startWindows(p mediaProbe, cues []liveCue) []float64 {
	n := syncStartWindows
	if p.Bitrate > 0 {
		n = min(n, int(syncByteBudget/(p.Bitrate*syncWindowLen+syncWindowExtra)))
	}
	if float64(n)*syncWindowLen < liveMinCover || p.Duration < float64(n)*syncWindowLen {
		return nil
	}
	for i := 0; i+4 < len(cues); i++ {
		if cues[i+4].start-cues[i].start > syncWindowLen {
			continue
		}
		at := math.Max(0, math.Min(math.Floor(cues[i].start-syncStartLead), p.Duration-float64(n)*syncWindowLen))
		out := make([]float64, n)
		for j := range out {
			out[j] = at + float64(j)*syncWindowLen
		}
		return out
	}
	return nil
}

// updateRefSubs puts the own-track choice of a fresh probe p into the file's saved reference
// (saved before picture tracks counted) and returns the reference's probe.
func (y *syncer) updateRefSubs(fk string, st *syncRefState, p mediaProbe) mediaProbe {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) {
		return p
	}
	r := *st.ref
	r.Probe.OwnSub, r.Probe.SubCodec = p.OwnSub, p.SubCodec
	y.saveRef(fk, st, r)
	return r.Probe
}

// addWindows reads more reference windows of the file (kept with its reference).
func (y *syncer) addWindows(ctx context.Context, hash string, index int, starts []float64) (syncRef, error) {
	fk := syncFileKey(hash, index)
	st, release := y.refState(fk)
	defer release()
	st.mu.Lock()
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) { // an evicted state starts empty
		return syncRef{}, errors.New("no reference")
	}
	r := *st.ref
	r.Windows = append([]refWindow(nil), r.Windows...)
	r.Speech = append([]refWindow(nil), r.Speech...)
	url := torrFileURL(y.svc.cfg.TorrServerURL, hash, index)
	for _, at := range starts {
		if len(r.Windows) >= r.windowCap() {
			break
		}
		if slices.ContainsFunc(r.Windows, func(w refWindow) bool { return math.Abs(w.Start-at) < syncWindowLen/2 }) {
			continue // read meanwhile by another subtitle's timing
		}
		w, speech, err := y.readWindow(ctx, hash, url, r.Probe, at, r.bytesRead())
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		r.Windows = append(r.Windows, w)
		r.Speech = append(r.Speech, speech...)
	}
	if added := r.Windows[len(st.ref.Windows):]; len(added) > 0 {
		log.Printf("subtitles: timing reference of %s:%d: %d more windows, %d MB read", hash[:8], index, len(added),
			(r.bytesRead()-st.ref.bytesRead())>>20)
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

// readWindow reads one reference window of the file of hash, after a stream's start; a window
// of the own track comes with the speech of the same stretch (sync_owncheck.go; read again
// right away, so mostly from the TorBox proxy's or TorrServer's cache) while the file's reads
// (used bytes before this one) stay within syncByteBudget.
func (y *syncer) readWindow(ctx context.Context, hash, url string, p mediaProbe, at float64, used int64) (refWindow, []refWindow, error) {
	if !y.waitLine(ctx, hash) {
		return refWindow{}, nil, ctx.Err()
	}
	w, err := y.readOne(ctx, url, p, at)
	if err != nil || w.Kind != "subs" || p.Audio < 0 || used+2*w.Read > syncByteBudget {
		return w, nil, err
	}
	speechOnly := p
	speechOnly.OwnSub = -1
	if s, err := y.readOne(ctx, url, speechOnly, at); err == nil && s.Kind != "subs" {
		return w, []refWindow{s}, nil
	}
	return w, nil, nil
}

// readOne reads one window at at, within the room of a pre-timing job (readRoom): a throttled read
// may take as long as its byte cap needs at that rate.
func (y *syncer) readOne(ctx context.Context, url string, p mediaProbe, at float64) (refWindow, error) {
	rate, ok := y.readRoom(ctx, p.Bitrate)
	if !ok {
		if err := ctx.Err(); err != nil {
			return refWindow{}, err
		}
		return refWindow{}, errors.New("the file plays")
	}
	timeout := syncWindowTimeout
	if rate > 0 {
		timeout = max(timeout, time.Duration(float64(windowByteCap(p, syncWindowLen))/rate*float64(time.Second)))
	}
	wctx, cancel := context.WithTimeout(context.WithValue(ctx, readRateKey{}, rate), timeout)
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
	var files []torrFile
	filesRead := false
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
			// Collections and season packs ship subtitles of their other files too
			if !filesRead {
				files, _ = torrentFiles(ctx, y.svc.cfg.TorrServerURL, hash)
				filesRead = true
			}
			p := strings.Split(ref, "|")
			if len(p) != 3 {
				continue
			}
			if id, err := strconv.Atoi(p[1]); err != nil || !torrentSubForFile(files, id, index) {
				continue
			}
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

// torrentSubForFile reports whether the torrent's subtitle file sub belongs to its file video:
// named after it ("Movie.mkv", "Subs/Movie.en.srt"), or below its folder and naming its episode,
// or naming none while the folder holds no other big file (another movie or episode).
func torrentSubForFile(files []torrFile, sub, video int) bool {
	var s, v *torrFile
	for i := range files {
		switch files[i].ID {
		case sub:
			s = &files[i]
		case video:
			v = &files[i]
		}
	}
	if s == nil || v == nil {
		return false
	}
	norm := func(p string) string { return strings.ToLower(strings.ReplaceAll(p, "\\", "/")) }
	stem := func(p string) string { b := path.Base(p); return strings.TrimSuffix(b, path.Ext(b)) }
	sp, vp := norm(s.Path), norm(v.Path)
	if rest, ok := strings.CutPrefix(stem(sp), stem(vp)); ok {
		named := true
		for _, t := range nameTokens(rest) { // only language and flag tokens may follow
			if !flagTokens[t] && codeOrName(t, true) == "" {
				named = false
			}
		}
		if named {
			return true
		}
	}
	dir := path.Dir(vp) + "/"
	if dir == "./" {
		dir = ""
	}
	rel, ok := strings.CutPrefix(sp, dir)
	if !ok {
		return false
	}
	vs, ve := episodeOf(vp)
	for _, part := range strings.Split(rel, "/") {
		if ss, se := episodeOf(part); se != 0 {
			return se == ve && (ss == 0 || vs == 0 || ss == vs)
		}
	}
	for _, f := range files {
		p := norm(f.Path)
		if f.ID != video && f.Length >= v.Length/5 && strings.HasPrefix(p, dir) && subtitleExts[path.Ext(p)] == "" {
			return false
		}
	}
	return true
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
// -1 does not fit, 0 unknown (also a result of an older timing: it is timed again when served).
func (y *syncer) fitOf(id, hash string, index int) int {
	key, err := syncKey(id, hash, index)
	if err != nil {
		return 0
	}
	m := y.meta(key)
	switch {
	case m == nil || m.stale():
		return 0
	case m.fits():
		return 1
	case m.Live:
		return 0 // may still fit once more of the file was played
	case m.State == syncStateReplaced || m.State == syncStatePoor:
		return -1
	}
	return 0
}

// windowCap is how many reference windows the file may have.
func (r syncRef) windowCap() int {
	if r.Skipped {
		return 0
	}
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
