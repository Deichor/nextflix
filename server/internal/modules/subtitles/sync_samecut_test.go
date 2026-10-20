package subtitles

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Mentalist S03E03 (real data, testdata/sync/mentalist_s03e03.json): playback started on an
// NF WEB-DL (own English track timed to it, tight) and switched after 27 s to a KONTRAST WEBRip
// of the same cut (2568.512 s, same speech at offset 0) whose own English track is not timed to
// it (its lines 0 to 4 s off by stretch). The fixture has both files' read own-track windows with
// the speech read with them, and the Turkish turkcealtyazi subtitle's cue times.
func loadS03E03(t *testing.T) (kontrast, nf syncRef, sub []span) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sync", "mentalist_s03e03.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil || fx.Other == nil {
		t.Fatalf("fixture: %v", err)
	}
	ref := func(f *syncFixture) syncRef {
		r := syncRef{Probe: mediaProbe{Duration: fx.Duration, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"}, Paired: true}
		for _, w := range f.Subs {
			r.Windows = append(r.Windows, w.window())
		}
		for _, w := range f.Speech {
			r.Speech = append(r.Speech, w.window())
		}
		return r
	}
	for _, c := range fx.Sub {
		sub = append(sub, span{c[0], c[1]})
	}
	return ref(&fx), ref(fx.Other), sub
}

// setRef makes r the file's reference, as if it was read.
func setRef(y *syncer, hash string, index int, r syncRef) {
	st, release := y.refState(syncFileKey(hash, index))
	st.mu.Lock()
	st.ref = &r
	st.mu.Unlock()
	release()
}

// KONTRAST's own track found no fit to its 6 read stretches of speech, and 6 are too few to call
// it another cut: it stayed "not known" and, as a text track, timed every subtitle (peak 0.0,
// -21 to -3 s). Now the speech read with its windows stands in. NF's track is timed and tight.
func TestOwnTrackWithoutAFitGivesWay(t *testing.T) {
	kontrast, nf, _ := loadS03E03(t)
	if c := checkOwn(ownPairsOf(kontrast, nil)); c.State != "" || !c.NoFit || c.Pairs != 6 {
		t.Fatalf("KONTRAST: %+v", c)
	}
	if c := checkOwn(ownPairsOf(nf, nil)); c.State != ownTimed || !c.Tight {
		t.Fatalf("NF: %+v", c)
	}
	y := newLiveTest(t, kontrast.Probe.Duration).sync
	got, _ := y.checkOwnTrack(syncHash, 1, kontrast, nil)
	if !got.OwnAside || hasOwnWindows(got.Windows) || len(got.Windows) != 6 || got.Own.State != "" {
		t.Fatalf("KONTRAST's windows: %+v, %s", got.Own, refKind(got, got.Windows))
	}
	if _, s := y.openOwnCues(syncHash, 1, 0, []OwnTrack{{Stream: 2, Lang: "eng", Codec: "subrip"}}); s == nil {
		t.Fatal("not known: its cue tap still runs (more stretches may decide)")
	}
}

// A track set aside (no fit to the speech) still times a subtitle that the speech does not: the
// speech may be what does not tell (music, noise), as before the check. A PGS track's too
// (Westworld S01E06: its PGS track found no fit from 3-5 stretches, and the speech alone did
// not time the subtitle, so it was served untimed).
func TestOwnTrackAsideTimesWhatTheSpeechDoesNot(t *testing.T) {
	for _, codec := range []string{"subrip", "hdmv_pgs_subtitle"} {
		svc := newLiveTest(t, 1500)
		y := svc.sync
		file := dialogue(72, 1500)
		starts := windowStarts(1500, syncWindowLen, 6)
		noise := speechWindows(dialogue(91, 1500), starts, 0.15, 0.08, 0.1, 3) // speech that does not tell
		for i := range noise {
			noise[i].Kind = "audio"
		}
		ref := syncRef{Probe: mediaProbe{Duration: 1500, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: codec},
			Windows: truthWindows(file, starts, 0, 0, 1), Speech: noise, Paired: true}
		if got, _ := y.checkOwnTrack(syncHash, 1, ref, nil); !got.OwnAside || hasOwnWindows(got.Windows) {
			t.Fatalf("%s not set aside: %+v", codec, got.Own)
		}
		ctx := context.Background()
		inner := cacheProviderSub(t, svc, "1|aside|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
		base, _ := svc.File(ctx, inner)
		key, _ := syncKey(inner, syncHash, 1)
		if !y.decide(ctx, inner, syncHash, 1, base, ref, false) {
			t.Fatalf("%s: too little to tell", codec)
		}
		if m := y.meta(key); m == nil || !m.fits() || m.Ref != "subs" || math.Abs(m.Fit.Segments[0].Offset-2) > 0.15 {
			t.Fatalf("%s: %+v", codec, m)
		}
	}
}

// An alternative gets the windows of a set-aside track too: the subtitle served fits nothing, the
// next one of its language only the own track (review: alternatives were fitted to the speech
// alone).
func TestOwnTrackAsideTimesAnAlternative(t *testing.T) {
	svc := newLiveTest(t, 1500)
	y := svc.sync
	file := dialogue(72, 1500)
	starts := windowStarts(1500, syncWindowLen, 6)
	noise := speechWindows(dialogue(91, 1500), starts, 0.15, 0.08, 0.1, 3)
	for i := range noise {
		noise[i].Kind = "audio"
	}
	ref := syncRef{Probe: mediaProbe{Duration: 1500, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"},
		Windows: truthWindows(file, starts, 0, 0, 1), Speech: noise, Paired: true}
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|asidemain|2|7|1", dialogue(93, 1500))
	alt := cacheProviderSub(t, svc, "1|asidealt|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2 }))
	y.listed([]Sub{{ID: inner, Source: SourceTurkceAltyazi, Language: "tr"}, {ID: alt, Source: SourceTurkceAltyazi, Language: "tr"}}, nil, syncHash, 1)
	base, _ := svc.File(ctx, inner)
	key, _ := syncKey(inner, syncHash, 1)
	if !y.decide(ctx, inner, syncHash, 1, base, ref, false) {
		t.Fatal("too little to tell")
	}
	if m := y.meta(key); m == nil || m.State != syncStateReplaced || m.ReplacedBy != alt {
		t.Fatalf("%+v", m)
	}
}

// The first timing reads more windows where the offset changes; the fit after that read builds its
// windows as every fit does (fitRef), so the windows of a set-aside own track (lines 0 to 3 s off
// by stretch, like KONTRAST's) do not come back with the windows read.
func TestOwnTrackAsideAfterMoreWindows(t *testing.T) {
	svc := newLiveTest(t, episode)
	y := svc.sync
	file := dialogue(77, episode)
	starts := windowStarts(episode, syncWindowLen, 6)
	shift := func(at float64) float64 {
		i := slices.Index(starts, at)
		if i < 0 {
			i = int(at)
		}
		return []float64{-2, 2.5, -1, 3, 0.5, -3}[i%6]
	}
	reads := 0
	y.read = func(_ context.Context, _ string, p mediaProbe, at, _ float64) (refWindow, error) {
		reads++
		if p.OwnSub < 0 {
			w := truthWindows(file, []float64{at}, 0.1, 0, int64(at))[0]
			w.Kind = "audio"
			for i, v := range w.Speech {
				w.Speech[i] = min(v, 1)
			}
			return w, nil
		}
		moved := make([]span, len(file))
		for i, c := range file {
			moved[i] = span{c.start + shift(at), c.end + shift(at)}
		}
		return truthWindows(moved, []float64{at}, 0.4, 0, int64(at)+7)[0], nil
	}
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"}
	ref := syncRef{Probe: probe, Paired: true, MaxWindows: syncMaxWindows}
	ctx := context.Background()
	for _, at := range starts {
		w, speech, err := y.readWindow(ctx, syncHash, "", probe, at, 0)
		if err != nil {
			t.Fatal(err)
		}
		ref.Windows, ref.Speech = append(ref.Windows, w), append(ref.Speech, speech...)
	}
	setRef(y, syncHash, 1, ref)
	if got, _ := y.checkOwnTrack(syncHash, 1, ref, nil); !got.OwnAside {
		t.Fatalf("not set aside: %+v", got.Own)
	}
	reads = 0
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 1200 {
			return 2
		}
		return 4
	})
	inner := cacheProviderSub(t, svc, "1|more|2|7|1", sub)
	base, _ := svc.File(ctx, inner)
	key, _ := syncKey(inner, syncHash, 1)
	if !y.decide(ctx, inner, syncHash, 1, base, ref, true) {
		t.Fatal("too little to tell")
	}
	m := y.meta(key)
	if reads == 0 || m == nil || strings.Contains(m.Ref, "subs") {
		t.Fatalf("%d reads: %+v", reads, m)
	}
	for _, u := range []float64{600, 1800} {
		if want := 2.0 + 2*float64(int(u/1200)); !m.fits() || math.Abs(m.Fit.mapTime(u)-u-want) > 0.15 {
			t.Fatalf("at %.0f s: %s, want %+.1f s", u, describeFit(m.Fit), want)
		}
	}
}

// switchTest serves one subtitle on two files of one length (a, b), b playing; refits are recorded.
type switchTest struct {
	svc        *Service
	y          *syncer
	ctx        context.Context
	a, b       string
	refB       syncRef
	inner      string
	base       []byte
	keyA, keyB string
	fileB      *liveFile
	grown      []string
}

func newSwitchTest(t *testing.T, duration float64, refA, refB syncRef, sub []span) *switchTest {
	s := &switchTest{svc: newLiveTest(t, duration), a: syncHash, b: "5c8f6d1e2a3b4c5d6e7f8091a2b3c4d5e6f70812", ctx: context.Background(),
		refB: refB}
	y := s.svc.sync
	s.y = y
	y.onGrow = func(f *liveFile) { s.grown = append(s.grown, f.fk) }
	setRef(y, s.a, 1, refA)
	setRef(y, s.b, 1, refB)
	s.inner = cacheProviderSub(t, s.svc, "333899|switch|3|3|1", sub)
	s.base, _ = s.svc.File(s.ctx, s.inner)
	s.keyA, _ = syncKey(s.inner, s.a, 1)
	s.keyB, _ = syncKey(s.inner, s.b, 1)
	y.noteServed(s.keyA, s.inner, s.a, 1, 20)
	y.noteServed(s.keyB, s.inner, s.b, 1, 27)
	s.fileB = y.liveFile(s.b, 1)
	s.fileB.mu.Lock()
	s.fileB.lastTap = time.Now() // b plays
	s.fileB.mu.Unlock()
	return s
}

// served is what a player of b gets now.
func (s *switchTest) served(t *testing.T) FileAnswer {
	t.Helper()
	a, ok := s.y.answer(s.keyB, s.base, func() bool { return true })
	if !ok {
		t.Fatal("nothing stored for b")
	}
	return a
}

// After the switch KONTRAST's timing ran before NF's own track was checked: its speech fitted
// the subtitle 0.4-0.75 s off what NF's track says (-1.05 s, -1.45 s from 521 s against -1.48 s,
// -2.20 s from 667 s, -1.80 s from 1946 s). Once NF's track is timed and its fit lands, KONTRAST
// is re-fitted with NF's own-track windows (their speech matches at offset 0) and serves NF's
// timing, through later re-fits too.
func TestSameCutSharesTheTimedOwnTrack(t *testing.T) {
	kontrast, nf, sub := loadS03E03(t)
	s := newSwitchTest(t, kontrast.Probe.Duration, nf, kontrast, sub)
	y := s.y
	if !y.decide(s.ctx, s.inner, s.b, 1, s.base, kontrast, false) {
		t.Fatal("KONTRAST: too little to tell")
	}
	if m := y.meta(s.keyB); m == nil || m.SameCut != "" || m.Ref != "audio" {
		t.Fatalf("KONTRAST before NF's check: %+v", m)
	}
	if !y.decide(s.ctx, s.inner, s.a, 1, s.base, nf, false) {
		t.Fatal("NF: too little to tell")
	}
	mNF := y.meta(s.keyA)
	if mNF == nil || !mNF.fits() || y.liveFile(s.a, 1).ownState() != ownTimed {
		t.Fatalf("NF: %+v", mNF)
	}
	if !slices.Contains(s.grown, syncFileKey(s.b, 1)) {
		t.Fatalf("NF's fit did not re-fit KONTRAST: %v", s.grown)
	}
	for range 2 {
		y.refitFile(s.fileB)
		m := y.meta(s.keyB)
		if m == nil || !m.fits() || !strings.Contains(m.Ref, "subs") || s.served(t).Sync != syncStateLive {
			t.Fatalf("KONTRAST after NF's fit: %+v", m)
		}
		for _, u := range []float64{300, 1000, 2200} {
			if d := m.Fit.mapTime(u) - mNF.Fit.mapTime(u); math.Abs(d) > 0.15 {
				t.Fatalf("at %.0f s %+.2f s off NF's fit: %s against %s", u, d, describeFit(m.Fit), describeFit(mNF.Fit))
			}
		}
	}
}

// KONTRAST's first timing, NF's track already checked: the fit with NF's windows has its offset
// change between windows far apart, so a window is read there. The fit after that read keeps NF's
// windows (review: it fitted the reference as read, KONTRAST's speech only, and served that).
func TestSameCutWindowsAfterMoreWindows(t *testing.T) {
	kontrast, nf, sub := loadS03E03(t)
	s := newSwitchTest(t, kontrast.Probe.Duration, nf, kontrast, sub)
	y := s.y
	reads := 0
	y.read = func(_ context.Context, _ string, p mediaProbe, at, _ float64) (refWindow, error) {
		reads++ // a stretch without lines: it adds a window, not evidence
		w := refWindow{Start: at, Kind: "subs", Speech: make([]byte, int(syncWindowLen*syncRate))}
		if p.OwnSub < 0 {
			w.Kind = "audio"
		}
		return w, nil
	}
	if !y.decide(s.ctx, s.inner, s.a, 1, s.base, nf, false) {
		t.Fatal("NF: too little to tell")
	}
	mNF := y.meta(s.keyA)
	if !y.decide(s.ctx, s.inner, s.b, 1, s.base, kontrast, true) {
		t.Fatal("KONTRAST: too little to tell")
	}
	m := y.meta(s.keyB)
	if reads == 0 || m == nil || !m.fits() || !strings.Contains(m.Ref, "subs") {
		t.Fatalf("%d reads: %+v", reads, m)
	}
	for _, u := range []float64{300, 1000, 2200} {
		if d := m.Fit.mapTime(u) - mNF.Fit.mapTime(u); math.Abs(d) > 0.15 {
			t.Fatalf("at %.0f s %+.2f s off NF's fit: %s against %s", u, d, describeFit(m.Fit), describeFit(mNF.Fit))
		}
	}
}

// A subtitle that does not fit a file's own windows (here: speech that does not tell) takes the
// fit it has on the file of the same cut once that fit lands, when the two files' speech matched
// at offset 0, although its offsets do not beat chance on this file's windows; it shows while the
// file plays even though it was pre-timed there, and stays through later re-fits.
func TestSameCutFitLandsAfterTheSwitch(t *testing.T) {
	file := dialogue(59, episode)
	starts := windowStarts(episode, syncWindowLen, 8)
	speech := speechWindows(file, starts, 0.15, 0.08, 0.1, 3)
	for i := range speech {
		speech[i].Kind = "audio"
	}
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}
	ref := syncRef{Probe: probe, Windows: speech}
	sub := dialogue(83, episode) // its lines are not the file's
	s := newSwitchTest(t, episode, ref, ref, sub)
	y := s.y
	if !y.decide(s.ctx, s.inner, s.b, 1, s.base, ref, false) {
		t.Fatal("b: too little to tell")
	}
	if m := y.meta(s.keyB); m == nil || m.fits() {
		t.Fatalf("b before a's fit: %+v", m)
	}
	moved := make([]span, len(sub))
	for i, c := range sub {
		moved[i] = span{c.start + 1.5, c.end + 1.5}
	}
	y.store(s.keyA, syncMeta{State: syncStateSynced, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, 1.5}}}, Live: true, Pre: true}, vttOf(moved))
	y.offerSameCut(s.inner, syncFileKey(s.a, 1))
	if !slices.Contains(s.grown, syncFileKey(s.b, 1)) {
		t.Fatalf("a's fit did not re-fit b: %v", s.grown)
	}
	for range 2 {
		y.refitFile(s.fileB)
		m := y.meta(s.keyB)
		a := s.served(t)
		cues := vttCues(a.VTT)
		if m == nil || !m.fits() || m.SameCut != syncFileKey(s.a, 1) || !m.SameSpeech || m.Pre || a.Sync != syncStateLive ||
			len(cues) != len(sub) || math.Abs(cues[10].start-moved[10].start) > 0.01 {
			t.Fatalf("b after a's fit: %+v, %q", m, a.Sync)
		}
	}
	// a file whose speech is another's does not take it
	other := speechWindows(dialogue(61, episode), starts, 0.15, 0.08, 0.1, 3)
	for i := range other {
		other[i].Kind = "audio"
	}
	c := newSwitchTest(t, episode, ref, syncRef{Probe: probe, Windows: other}, sub)
	c.y.store(c.keyA, syncMeta{State: syncStateSynced, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, 1.5}}}, Live: true}, vttOf(moved))
	if !c.y.decide(c.ctx, c.inner, c.b, 1, c.base, c.refB, false) {
		t.Fatal("other speech: too little to tell")
	}
	if m := c.y.meta(c.keyB); m == nil || m.SameCut != "" || m.fits() {
		t.Fatalf("another file's speech took the fit: %+v", m)
	}
}

// The files' speech matches at offset 0 only for the same cut: not 0.5 s apart, nor against
// another episode's speech.
func TestSpeechMatchesTheSameCut(t *testing.T) {
	kontrast, nf, _ := loadS03E03(t)
	a, b := fileSpeech(kontrast, nil), fileSpeech(nf, nil)
	if len(a) != 6 || !speechMatches(a, b) {
		t.Fatalf("%d windows: no match", len(a))
	}
	shifted := make([]refWindow, len(b))
	for i, w := range b {
		w.Start += 0.5
		shifted[i] = w
	}
	if speechMatches(a, shifted) {
		t.Fatal("0.5 s apart matched")
	}
	other := speechWindows(dialogue(83, kontrast.Probe.Duration), []float64{20, 506, 991, 1477, 1963, 2449}, 0.15, 0.08, 0.1, 3)
	if speechMatches(a, other) {
		t.Fatal("another episode matched")
	}
}

// A copy the speech match held (SameSpeech) is held against this file's windows once that file's
// speech no longer matches (review: copyFits kept it for minutes right after the re-check failed).
func TestSameCutCopyGivesWayWhenTheSpeechNoLongerMatches(t *testing.T) {
	file := dialogue(59, episode)
	starts := windowStarts(episode, syncWindowLen, 8)
	speech := func(seed int64) []refWindow {
		ws := speechWindows(dialogue(seed, episode), starts, 0.15, 0.08, 0.1, 3)
		for i := range ws {
			ws[i].Kind = "audio"
		}
		return ws
	}
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}
	sub := dialogue(83, episode) // its lines are neither file's
	moved := shiftSpans(sub, 1.5)
	s := newSwitchTest(t, episode, syncRef{Probe: probe, Windows: speechWindows(file, starts, 0.15, 0.08, 0.1, 3)},
		syncRef{Probe: probe, Windows: speech(61)}, sub)
	y := s.y
	fit := syncMeta{State: syncStateSynced, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, 1.5}}}, Live: true}
	y.store(s.keyA, fit, vttOf(moved))
	copied := fit
	copied.SameCut, copied.SameSpeech = syncFileKey(s.a, 1), true
	y.store(s.keyB, copied, vttOf(moved))
	if !y.decide(s.ctx, s.inner, s.b, 1, s.base, s.refB, false) {
		t.Fatal("too little to tell")
	}
	if m := y.meta(s.keyB); m == nil || m.SameCut != "" || m.fits() {
		t.Fatalf("the copy stayed: %+v", m)
	}
}

// While the reference of the file a copy came from cannot be loaded (a read runs), its speech is
// not compared: the copy the speech match held stays (review: it counted as no match and was
// dropped).
func TestSameCutCopyStaysWhileTheOtherReferenceIsRead(t *testing.T) {
	file := dialogue(59, episode)
	starts := windowStarts(episode, syncWindowLen, 8)
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}
	ref := syncRef{Probe: probe, Windows: speechWindows(file, starts, 0.15, 0.08, 0.1, 3)}
	sub := dialogue(83, episode)
	moved := shiftSpans(sub, 1.5)
	s := newSwitchTest(t, episode, ref, ref, sub)
	y := s.y
	fit := syncMeta{State: syncStateSynced, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, 1.5}}}, Live: true}
	y.store(s.keyA, fit, vttOf(moved))
	copied := fit
	copied.SameCut, copied.SameSpeech = syncFileKey(s.a, 1), true
	y.store(s.keyB, copied, vttOf(moved))
	fk := syncFileKey(s.a, 1)
	y.mu.Lock()
	y.probes[fk] = probe
	y.mu.Unlock()
	st, release := y.refState(fk)
	defer release()
	st.mu.Lock()
	defer st.mu.Unlock()
	if !y.decide(s.ctx, s.inner, s.b, 1, s.base, s.refB, false) {
		t.Fatal("too little to tell")
	}
	if m := y.meta(s.keyB); m == nil || m.SameCut != fk || !m.SameSpeech || !m.fits() {
		t.Fatalf("the copy gave way: %+v", m)
	}
}

// A speech match is kept for the decide, no match is not: a fit that reads more windows compares
// again (review: a no-match from fewer windows stuck).
func TestSameSpeechComparesAgainAfterNoMatch(t *testing.T) {
	file := dialogue(59, episode)
	starts := windowStarts(episode, syncWindowLen, 8)
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}
	ref := syncRef{Probe: probe, Windows: speechWindows(file, starts, 0.15, 0.08, 0.1, 3)}
	s := newSwitchTest(t, episode, ref, ref, dialogue(83, episode))
	r := fitRefs{own: ref, same: map[string]bool{}}
	o := servedSub{fk: syncFileKey(s.a, 1), hash: s.a, index: 1}
	if s.y.sameSpeech(r, s.b, 1, o, syncRef{Probe: probe}) {
		t.Fatal("matched without the other file's speech")
	}
	if !s.y.sameSpeech(r, s.b, 1, o, ref) {
		t.Fatal("the no-match stuck")
	}
	if !s.y.sameSpeech(r, s.b, 1, o, syncRef{Probe: probe}) {
		t.Fatal("the match was not kept")
	}
}

// A copy takes the fit of the most recent file of the same cut that fits: not a copy itself (it
// was not timed to that file), and past a file that replaced the subtitle (review: the first
// such file ended the search).
func TestSameCutCopiesOnlyFits(t *testing.T) {
	file := dialogue(59, episode)
	starts := windowStarts(episode, syncWindowLen, 8)
	speech := speechWindows(file, starts, 0.15, 0.08, 0.1, 3)
	for i := range speech {
		speech[i].Kind = "audio"
	}
	probe := mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none"}
	ref := syncRef{Probe: probe, Windows: speech}
	sub := dialogue(83, episode)
	moved := shiftSpans(sub, 1.5)
	fit := syncMeta{State: syncStateSynced, Fit: syncFit{Scale: 1, Segments: []syncSegment{{0, 1.5}}}, Live: true}
	s := newSwitchTest(t, episode, ref, ref, sub)
	y := s.y
	c := "6d9f7e2f3b4c5d6e7f8091a2b3c4d5e6f7081923" // a third file of the cut, served last
	setRef(y, c, 1, ref)
	keyC, _ := syncKey(s.inner, c, 1)
	y.noteServed(keyC, s.inner, c, 1, 30)
	copied := fit
	copied.SameCut = syncFileKey(s.a, 1)
	y.store(keyC, copied, vttOf(moved))
	if _, ok := y.sameCutFit(s.ctx, s.inner, s.b, 1, s.base, probe); ok {
		t.Fatal("a copy was copied on")
	}
	y.store(keyC, syncMeta{State: syncStateReplaced, ReplacedBy: "x", Live: true}, vttOf(sub))
	y.store(s.keyA, fit, vttOf(moved))
	if !y.decide(s.ctx, s.inner, s.b, 1, s.base, ref, false) {
		t.Fatal("too little to tell")
	}
	if m := y.meta(s.keyB); m == nil || !m.fits() || m.SameCut != syncFileKey(s.a, 1) {
		t.Fatalf("b: %+v", m)
	}
}

// An onset of one file counts once, also where windows of the other overlap (review: it counted
// once per window, so a stretch read and played passed the 40 onsets with 30).
func TestSpeechMatchesCountsOnsetsOnce(t *testing.T) {
	w := refWindow{Start: 100, Kind: "live", Speech: make([]byte, liveWindowFrames)}
	for i := 50; i+100 < len(w.Speech); i += 200 { // 30 onsets
		for j := i; j < i+100; j++ {
			w.Speech[j] = 1
		}
	}
	if speechMatches([]refWindow{w}, []refWindow{w, w}) {
		t.Fatal("30 onsets counted twice")
	}
	v := w
	v.Start = 160
	if !speechMatches([]refWindow{w, v}, []refWindow{w, v, w}) {
		t.Fatal("60 onsets did not match")
	}
}

// Only a result that is none, untimed or a copy gives way to a fit of the same cut.
func TestTakesCopy(t *testing.T) {
	for _, c := range []struct {
		name string
		m    *syncMeta
		want bool
	}{
		{"none", nil, true},
		{"poor, provisional", &syncMeta{State: syncStatePoor, Live: true}, true},
		{"pre-timed", &syncMeta{State: syncStateSynced, Live: true, Pre: true}, true},
		{"a copy", &syncMeta{State: syncStateSynced, Live: true, SameCut: "x|1"}, true},
		{"its own provisional fit", &syncMeta{State: syncStateSynced, Live: true}, false},
		{"replaced, provisional", &syncMeta{State: syncStateReplaced, Live: true}, false},
		{"final", &syncMeta{State: syncStatePoor}, false},
	} {
		if got := takesCopy(c.m); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
