package subtitles

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// Serving a subtitle with holes (holes.go): every file request of a provider subtitle listed for
// a playing file compares it with a reference of the file in another language. The holes count
// in the ranking; with the AI translation on, the reference's lines in them are translated
// (ai_job.go, nearest to the player first) and served in the reference's timing between the
// subtitle's own cues, which are never changed.

const (
	holeNear    = 10.0 // seconds before a hole the player counts as at it (AINote, first wait)
	holeJobKind = "holes"
)

// holeServed: timing states whose cues are on the file's timeline.
var holeServed = map[string]bool{syncStateSynced: true, syncStateUnchanged: true, syncStateLive: true, syncStateReplaced: true}

// holeState is the last comparison of a subtitle served for a file.
type holeState struct {
	Ref    string    `json:"ref"` // listed id of the reference
	Holes  []subHole `json:"holes"`
	Filled int       `json:"filled,omitempty"`
}

func holeKey(key string) string { return "holes|" + key }

// holesOf is how many reference lines the subtitle id misses on the file (0: none or unknown).
func (y *syncer) holesOf(id, hash string, index int) int {
	key, err := syncKey(id, hash, index)
	if err != nil {
		return 0
	}
	if st := y.holeState(key); st != nil {
		return holeTotal(st.Holes)
	}
	return 0
}

func (y *syncer) holeState(key string) *holeState {
	y.mu.Lock()
	st, ok := y.holes[key]
	y.mu.Unlock()
	if ok {
		return st
	}
	if b, found := y.svc.files.read("synced", holeKey(key), ".json", 0); found {
		var v holeState
		if json.Unmarshal(b, &v) == nil {
			st = &v
		}
	}
	y.mu.Lock()
	if len(y.holes) > 5000 {
		y.holes = map[string]*holeState{}
	}
	y.holes[key] = st
	y.mu.Unlock()
	return st
}

// noteHoles stores a comparison when it changed and logs it; the filled lines count once the
// translation of the holes settled.
func (y *syncer) noteHoles(key, inner, hash string, index int, st holeState, settled bool) {
	prev := y.holeState(key)
	if prev != nil && prev.Ref == st.Ref && sameHoles(prev.Holes, st.Holes) && (!settled || prev.Filled == st.Filled) {
		return
	}
	if b, err := json.Marshal(st); err == nil {
		if err := y.svc.files.write("synced", holeKey(key), ".json", b); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
	}
	y.mu.Lock()
	y.holes[key] = &st
	y.mu.Unlock()
	if prev == nil && len(st.Holes) == 0 {
		return
	}
	if prev != nil && len(prev.Holes) == 0 && len(st.Holes) == 0 {
		return
	}
	var parts []string
	for _, h := range st.Holes {
		parts = append(parts, fmt.Sprintf("%.0f-%.0f s (%d lines)", h.First, h.Last, h.Lines))
	}
	if len(parts) == 0 {
		parts = []string{"none any more"}
	}
	filled := ""
	if st.Filled > 0 {
		filled = fmt.Sprintf(", filled %d lines with AI", st.Filled)
	}
	log.Printf("subtitles: %s on %s:%d has holes against %s: %s%s", shortSubID(inner), hash[:8], index,
		shortSubID(rawSubID(st.Ref)), strings.Join(parts, ", "), filled)
}

func sameHoles(a, b []subHole) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Lines != b[i].Lines || math.Abs(a[i].From-b[i].From) > 1 || math.Abs(a[i].To-b[i].To) > 1 {
			return false
		}
	}
	return true
}

// holeRef is the reference a subtitle is compared with: another language, on the file's timeline.
type holeRef struct {
	id      string
	cues    []liveCue // without HI parts
	pending bool      // the best reference is still being timed: ask again
}

// holeReference picks the reference for a subtitle in lang served for the file: a subtitle made
// for the file whose cues are on disk (its own text track, an OpenSubtitles hash match), else a
// provider subtitle timed to fit the file, else the best one not timed yet (timed now; download
// allows fetching it, never from OpenSubtitles). English without HI first, then English, then
// without HI. The file's last list names the candidates.
func (s *Service) holeReference(ctx context.Context, hash string, index int, lang string, download bool, t float64) (holeRef, bool) {
	y := s.sync
	y.mu.Lock()
	list := y.lists[syncFileKey(hash, index)]
	y.mu.Unlock()
	var made, fitting, untimed []Sub
	for _, x := range list {
		// A live track or a quota-blocked download only counts when its whole file is on disk
		if x.Language == lang || x.Language == "" || x.Source == SourceAI || x.Forced || x.Quota && !s.hasFile(rawSubID(x.ID)) {
			continue
		}
		inner, h, i, wrapped := unwrapSyncID(x.ID)
		switch {
		case !wrapped && x.Source == SourceEmbedded:
			if eh, ei, _, err := embeddedRef(refOf(x.ID)); err == nil && eh == hash && ei == index && s.hasFile(x.ID) {
				made = append(made, x)
			}
		case !wrapped && x.Synced && x.Source == SourceOpenSubtitles:
			if s.hasFile(x.ID) {
				made = append(made, x)
			}
		case wrapped && h == hash && i == index:
			switch y.fitOf(inner, hash, index) {
			case 1:
				fitting = append(fitting, x)
			case 0:
				if s.hasFile(inner) || (download && x.Source != SourceOpenSubtitles) {
					untimed = append(untimed, x)
				}
			}
		}
	}
	if len(made) == 0 && len(fitting) == 0 && len(untimed) > 0 {
		// The best one is timed in the background (a download, a timing): the request does not wait
		x := holeOrder(untimed)[0]
		inner, _, _, _ := unwrapSyncID(x.ID)
		key, _ := syncKey(inner, hash, index)
		if y.recentlyFailed(key) {
			return holeRef{}, false
		}
		s.flights.start("holeref|"+x.ID, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), fetchLimit)
			defer cancel()
			if _, err := s.fileFor(ctx, x.ID, t, false); err != nil {
				y.mu.Lock()
				y.failed[key] = time.Now() // not asked again for a while
				y.mu.Unlock()
				return nil, err
			}
			return nil, nil
		})
		m := y.meta(key)
		running := s.flights.running("holeref|"+x.ID) || s.flights.running("sync|"+key)
		return holeRef{id: x.ID, pending: running || (m != nil && m.Live)}, false
	}
	for _, group := range [][]Sub{made, fitting} {
		for _, x := range holeOrder(group) {
			a, err := s.fileFor(ctx, x.ID, t, false)
			if err != nil {
				continue
			}
			switch {
			case a.Sync == "" || a.Sync == syncStateOriginal || a.Sync == syncStateSynced || a.Sync == syncStateUnchanged ||
				a.Sync == syncStateLive:
				if a.Partial && a.Sync == "" {
					continue // a track still being read
				}
				return holeRef{id: x.ID, cues: aiCues(a.VTT)}, true
			case a.Sync == syncStatePending:
				return holeRef{id: x.ID, pending: a.Partial}, false
			}
		}
	}
	return holeRef{}, false
}

func refOf(id string) string {
	_, _, ref, _ := decodeID(id)
	return ref
}

// holeOrder sorts references: English without HI, English, without HI, the rest (stable).
func holeOrder(subs []Sub) []Sub {
	rank := func(x Sub) int {
		switch {
		case x.Language == "en" && !x.HI:
			return 0
		case x.Language == "en":
			return 1
		case !x.HI:
			return 2
		}
		return 3
	}
	out := append([]Sub(nil), subs...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// holeEvidence is the file's own-track and speech windows (read and played) and its length.
func (y *syncer) holeEvidence(hash string, index int) (holeEvidence, float64) {
	fk := syncFileKey(hash, index)
	var read syncRef
	duration := 0.0
	st, release := y.refState(fk)
	if st.mu.TryLock() {
		if y.loadRef(fk, st) {
			read, duration = *st.ref, st.ref.Probe.Duration
		}
		st.mu.Unlock()
	}
	release()
	var live []refWindow
	if y.svc.cfg.LiveTiming {
		live, _ = y.liveFile(hash, index).windows()
	}
	if duration <= 0 {
		y.mu.Lock()
		duration = y.probes[fk].Duration
		y.mu.Unlock()
	}
	read, live = y.checkOwnTrack(hash, index, read, live) // own-track windows only when timed to the file
	return evidenceOf(mergeWindows(read.Windows, live)), duration
}

// withHoles compares a subtitle served for a file with a reference and fills its holes.
func (s *Service) withHoles(ctx context.Context, inner, hash string, index int, a FileAnswer, t float64) FileAnswer {
	if !holeServed[a.Sync] {
		return a
	}
	_, lang, _, err := decodeID(inner)
	key, err2 := syncKey(inner, hash, index)
	if err != nil || err2 != nil || lang == "" {
		return a
	}
	st := s.settings.get()
	aiOn := st.AITranslateEnabled && st.AnthropicAPIKey != ""
	ref, ok := s.holeReference(ctx, hash, index, lang, aiOn, t)
	if !ok {
		if ref.pending && aiOn && !a.Partial {
			// The reference is being timed: keep the players asking for the filled lines
			a.VTT, a.Partial = renderVTT(vttCues(a.VTT), true), true
		}
		return a
	}
	sub := vttCues(a.VTT)
	ev, duration := s.sync.holeEvidence(hash, index)
	holes := findHoles(sub, ref.cues, ev, duration)
	state := holeState{Ref: ref.id, Holes: holes}
	if len(holes) == 0 {
		s.sync.noteHoles(key, inner, hash, index, state, true)
		return a
	}
	a.Holes = fmt.Sprintf("%d holes, %d lines", len(holes), holeTotal(holes))
	l, ok := aiLanguage(lang)
	if !aiOn || !ok {
		s.sync.noteHoles(key, inner, hash, index, state, true)
		return a
	}
	out, filled, pending, near := s.fillHoles(ctx, sub, ref, holes, hash, index, l, st.AnthropicAPIKey, t)
	state.Filled = filled
	s.sync.noteHoles(key, inner, hash, index, state, !pending)
	a.Holes += fmt.Sprintf(", %d filled", filled)
	partial := a.Partial || pending
	vtt := renderVTT(out, partial)
	if near {
		vtt = []byte(strings.Replace(string(vtt), "WEBVTT\n\n", "WEBVTT\n\n"+AINote+"\n\n", 1))
	}
	a.VTT, a.Partial = vtt, partial
	return a
}

// fillHoles translates the reference's lines in the holes into l and merges them with the
// subtitle's cues. pending: lines are still being translated; near: some of them at the player.
func (s *Service) fillHoles(ctx context.Context, sub []liveCue, ref holeRef, holes []subHole, hash string, index int,
	l AILanguage, apiKey string, t float64) (out []liveCue, filled int, pending, near bool) {
	fill := holeLines(ref.cues, holes)
	meta := s.sync.titleOf(hash, index)
	j := s.ai.job(rawSubID(ref.id)+"|"+l.Code+"|"+holeJobKind, l, func() string { return s.aiTitle(meta) })
	given := holeContext(ref.cues, fill, sub)
	j.setGiven(given) // before the feed: no worker may ask them
	j.mu.Lock()
	j.focus, j.lastUsed, j.watched, j.opening = math.Max(t, 0), time.Now(), true, 0
	j.mu.Unlock()
	var around []liveCue
	for _, c := range ref.cues {
		if _, ok := given[c.text]; ok {
			around = append(around, c)
		}
	}
	j.feed(append(fill, around...))
	s.ai.kick(j, apiKey)

	nearPending := func() bool {
		for _, h := range holes {
			if t >= h.First-holeNear && t <= h.Last {
				for _, c := range fill {
					if c.start >= h.From && c.end <= h.To && c.end > t && !j.settledLocked(c.text) {
						return true
					}
				}
			}
		}
		return false
	}
	// The first answer at a hole waits a little for its lines, like a translation
	deadline := time.Now().Add(aiFirstWait)
	for {
		j.mu.Lock()
		wait := nearPending() && j.running > 0 && j.keyErr == nil
		j.mu.Unlock()
		if !wait || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			deadline = time.Now()
		case <-time.After(250 * time.Millisecond):
		}
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	out = append([]liveCue(nil), sub...)
	for _, c := range fill {
		text := j.done[c.text]
		if text == "" && j.gaveUp[c.text] {
			text = j.soft[c.text] // never the reference's own language
		}
		if text == "" {
			pending = pending || !j.gaveUp[c.text]
			continue
		}
		out = append(out, liveCue{start: c.start, end: c.end, text: text})
		filled++
	}
	ok := j.keyErr == nil
	return out, filled, pending && ok, ok && nearPending()
}

// holeContext gives the reference lines outside the holes the subtitle's own line at that time
// as their translation: the model sees how the scene around a hole was translated.
func holeContext(ref, fill, sub []liveCue) map[string]string {
	asked := map[string]bool{}
	for _, c := range fill {
		asked[c.text] = true
	}
	out := map[string]string{}
	for _, c := range ref {
		if asked[c.text] || !hasLetter(c.text) {
			continue
		}
		best, overlap := "", 0.0
		for _, x := range sub {
			if o := math.Min(c.end, x.end) - math.Max(c.start, x.start); o > overlap {
				best, overlap = x.text, o
			}
		}
		out[c.text] = best
	}
	return out
}

// noteTitle records which title a file plays (the prompt context of its hole translations).
func (y *syncer) noteTitle(hash string, index int, m aiMeta) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if len(y.titles) > 1000 {
		y.titles = map[string]aiMeta{}
	}
	y.titles[syncFileKey(hash, index)] = m
}

func (y *syncer) titleOf(hash string, index int) aiMeta {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.titles[syncFileKey(hash, index)]
}
