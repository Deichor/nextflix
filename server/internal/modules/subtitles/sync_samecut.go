package subtitles

import (
	"context"
	"log"
	"math"
	"sort"
	"time"
)

// A switch to another file of the same cut during playback (a step down, a sidegrade) keeps the
// subtitle's timing. The file takes the fit the subtitle has on the file served before: when the
// timing starts, and again in every re-fit while its own result is none, untimed or such a copy,
// so a fit that lands on the other file later still moves over (The Mentalist S03E03: the NF
// WEB-DL's fit came 4 minutes after the switch to a KONTRAST WEBRip). Once the two files' speech
// matched at offset 0, its fits also use the other file's own-track windows when that track is
// timed to it (sameCutWindows): KONTRAST's own track was not, and its speech alone fitted
// -1.05 s where NF's track had the lines at -1.48 s.

// Same cut by the speech: the speech onsets of the two files match at offset 0. The Mentalist
// S03E03 NF WEB-DL and KONTRAST WEBRip: 91 % of 118 within 0.1 s; other episodes, and the same
// files 0.5 s and more apart: 14 % at most.
const (
	sameCutTol     = 0.1  // seconds: an onset of one file on one of the other's
	sameCutShare   = 0.6  // share of the onsets compared that match
	sameCutLoose   = 0.4  // and in every stretch with sameCutStretch onsets (a cut that differs from a break on)
	sameCutStretch = 10   // onsets
	sameCutOnsets  = 40   // onsets compared, at least
	sameCutOverlap = 30.0 // seconds two windows overlap to be compared
)

// sameCutCopy is a subtitle's fit on another file of the same cut, as a result of this file.
type sameCutCopy struct {
	m    syncMeta
	vtt  []byte // nil: served as it is
	from servedSub
	p    mediaProbe // this file's
}

// sameCut serves a subtitle first asked for this file with the fit it has on another file of the
// same cut (probed length within 0.05 s, same frame rate) that was served recently: a step down or
// sidegrade during playback then keeps the timing instead of starting from the subtitle as it is
// (Gilmore Girls S01E07: two more minutes 2.7 s off after a step down, although the fit on the
// first file was right). It is provisional; the file's own windows take over in the re-fits, and
// it only stays while it fits them (copyFits) or the files' speech matched (copySameCut); its
// frame rate is searched again. Equal lengths alone do not make one cut (The Mentalist S02E11 and
// S02E15 NF are both 2551.616 s long), but one subtitle names one episode: such pairs in the data
// lie within 30 ms of each other.
func (y *syncer) sameCut(ctx context.Context, inner, hash string, index int, base []byte) {
	key, err := syncKey(inner, hash, index)
	if err != nil || y.meta(key) != nil {
		return
	}
	c, ok := y.sameCutFit(ctx, inner, hash, index, base, mediaProbe{})
	if !ok {
		return
	}
	mu := y.decideLock(key)
	mu.Lock()
	y.storeSameCut(key, inner, hash, index, base, c)
	mu.Unlock()
}

// servedElsewhere are the other files the subtitle was served for recently, last served first.
func (y *syncer) servedElsewhere(inner, fk string) []servedSub {
	var out []servedSub
	y.mu.Lock()
	for _, s := range y.served {
		if s.inner == inner && s.fk != fk && time.Since(s.last) < liveServedFor {
			out = append(out, *s)
		}
	}
	y.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].last.After(out[j].last) })
	return out
}

// sameLength reports probes of the same length and frame rate (sameCut).
func sameLength(a, b mediaProbe) bool {
	return a.Duration > 0 && a.FPS > 0 && math.Abs(a.Duration-b.Duration) <= 0.05 && math.Abs(a.FPS-b.FPS) <= 0.01
}

// sameCutFit is the fit the subtitle has on the file of the same cut that served it most recently,
// as a provisional result of this file (p: its probe, zero to probe it); false when there is none.
func (y *syncer) sameCutFit(ctx context.Context, inner, hash string, index int, base []byte, p mediaProbe) (sameCutCopy, bool) {
	others := y.servedElsewhere(inner, syncFileKey(hash, index))
	if len(others) == 0 {
		return sameCutCopy{}, false
	}
	if p.Duration <= 0 || p.FPS <= 0 {
		var err error
		if p, err = y.probeFile(ctx, hash, index, torrFileURL(y.svc.cfg.TorrServerURL, hash, index)); err != nil {
			return sameCutCopy{}, false
		}
	}
	for _, o := range others {
		m := y.meta(o.key)
		if m == nil || m.stale() || !m.fits() && m.State != syncStateReplaced {
			continue
		}
		if op, ok := y.knownProbe(o.hash, o.index); !ok || !sameLength(p, op) {
			continue
		}
		a, ok := y.result(o.key, base)
		if !ok {
			continue
		}
		c := *m // a pre-timed fit stays so until played audio backs it
		c.Live, c.LiveWindows, c.LiveSeconds, c.SameCut, c.SameSpeech, c.RateWindows = true, 0, 0, o.fk, false, 0
		var vtt []byte
		if c.State != syncStateUnchanged {
			vtt = a.VTT
		}
		return sameCutCopy{c, vtt, o, p}, true
	}
	return sameCutCopy{}, false
}

// storeSameCut stores a copied fit for a file without a result (decide lock held).
func (y *syncer) storeSameCut(key, inner, hash string, index int, base []byte, c sameCutCopy) {
	if y.meta(key) != nil {
		return
	}
	log.Printf("subtitles: timing of %s on %s:%d: the fit on %s:%d (same cut: %.2f s, %.3f fps), provisional",
		shortSubID(inner), hash[:8], index, c.from.hash[:8], c.from.index, c.p.Duration, c.p.FPS)
	y.storeWatched(key, inner, hash, index, base, nil, c.m, c.vtt)
}

// takesCopy reports whether a result of the file may give way to a fit of the same cut: none,
// untimed (provisional and not fitting, or pre-timed), or a copy itself. A file's own fits stay.
func takesCopy(m *syncMeta) bool {
	return m == nil || m.Live && (m.SameCut != "" || untimedMeta(m))
}

// copySameCut is decide's step for a provisional fit m that does not fit while the stored result
// takes a copy (takesCopy): the subtitle's fit on a file of the same cut is served when it fits
// this file's windows (copyFits), or the two files' speech matched at offset 0 (then the copy
// is not held against the windows, and a pre-timed fit shows once this file plays). It reports
// whether a copy is served.
func (y *syncer) copySameCut(ctx context.Context, key, inner, hash string, index int, base []byte, ref syncRef, live []refWindow,
	liveSecs float64, served, prev *syncMeta, m syncMeta) bool {
	c, ok := y.sameCutFit(ctx, inner, hash, index, base, ref.Probe)
	if !ok || !c.m.fits() {
		return false
	}
	other, _ := y.knownRef(c.from.hash, c.from.index)
	same := speechMatches(fileSpeech(ref, live), y.speechOf(c.from.hash, c.from.index, other))
	if !same && (len(m.Fit.windows) == 0 || !copyFits(&c.m, m, base, ref.Probe.Duration)) {
		return false
	}
	c.m.SameSpeech, c.m.LiveWindows, c.m.LiveSeconds = same, len(live), liveSecs
	c.m.Pre = c.m.Pre && !(same && (len(live) > 0 || y.livePlaying(syncFileKey(hash, index))))
	if prev == nil || prev.SameCut != c.m.SameCut || prev.SameSpeech != same || fitChanged(prev, c.m) {
		how := "its offsets fit this file's windows"
		if same {
			how = "the speech matches at offset 0"
		}
		log.Printf("subtitles: timing of %s on %s:%d: the fit on %s:%d (same cut: %s; this file's own fit %s, peak %.2f), provisional",
			shortSubID(inner), hash[:8], index, c.from.hash[:8], c.from.index, how, m.State, m.Fit.Peak)
		y.storeWatched(key, inner, hash, index, base, served, c.m, c.vtt)
	}
	return true
}

// sameCutWindows adds to the windows a fit of the file uses the read own-track windows of a file
// of the same cut that served the subtitle recently, when that file's own track is timed to it
// and this file's is not known to be, once the two files' speech matched at offset 0. They take
// the place of this file's windows of the same stretches.
func (y *syncer) sameCutWindows(inner, hash string, index int, ref syncRef, live []refWindow) syncRef {
	if ref.Full || !y.svc.cfg.LiveTiming || ref.Own.State == ownTimed {
		return ref
	}
	for _, o := range y.servedElsewhere(inner, syncFileKey(hash, index)) {
		other, ok := y.knownRef(o.hash, o.index)
		if !ok || !sameLength(ref.Probe, other.Probe) || !hasOwnWindows(other.Windows) {
			continue
		}
		oc := y.liveFile(o.hash, o.index).ownChecked()
		if oc.State != ownTimed || !speechMatches(fileSpeech(ref, live), y.speechOf(o.hash, o.index, other)) {
			continue
		}
		var ws []refWindow
		for _, w := range oc.onSpeech(other.Windows) { // on the speech, as that file's fits use them
			if w.Kind == "subs" {
				ws = append(ws, w)
			}
		}
		shared := len(ws)
		for _, w := range ref.Windows {
			if !overlapsHalf(w, ws[:shared]) {
				ws = append(ws, w)
			}
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
		ref.Windows = ws
		f := y.liveFile(hash, index)
		f.mu.Lock()
		first := f.sameCut != o.fk
		f.sameCut = o.fk
		f.mu.Unlock()
		if first {
			log.Printf("subtitles: %s:%d is the same cut as %s:%d (their speech matches at offset 0): its fits use the %d own-track windows of %s:%d",
				hash[:8], index, o.hash[:8], o.index, shared, o.hash[:8], o.index)
		}
		return ref
	}
	return ref
}

// offerSameCut re-fits soon the other files the subtitle was served for recently whose result is
// provisional, now that it fits this file (fk): they may take the fit or share its windows.
func (y *syncer) offerSameCut(inner, fk string) {
	for _, s := range y.servedElsewhere(inner, fk) {
		if m := y.meta(s.key); m == nil || m.stale() || m.Live {
			y.onGrow(y.liveFile(s.hash, s.index))
		}
	}
}

// knownRef is a file's read reference, unless it is being read or there is none.
func (y *syncer) knownRef(hash string, index int) (syncRef, bool) {
	fk := syncFileKey(hash, index)
	st, release := y.refState(fk)
	defer release()
	if !st.mu.TryLock() {
		return syncRef{}, false
	}
	defer st.mu.Unlock()
	if !y.loadRef(fk, st) {
		return syncRef{}, false
	}
	return *st.ref, true
}

// speechOf is a file's speech as known now: of its reference ref and its played windows.
func (y *syncer) speechOf(hash string, index int, ref syncRef) []refWindow {
	var live []refWindow
	if y.svc.cfg.LiveTiming {
		live, _ = y.liveFile(hash, index).windowsOf(false)
	}
	return fileSpeech(ref, live)
}

// fileSpeech are the speech windows of a file's reference (read, read with the own-track
// windows) and played ones, one per stretch.
func fileSpeech(ref syncRef, live []refWindow) []refWindow {
	var out []refWindow
	for _, ws := range [][]refWindow{ref.Speech, ref.Windows, live} {
		for _, w := range ws {
			if w.Kind != "subs" && !overlapsHalf(w, out) {
				out = append(out, w)
			}
		}
	}
	return out
}

// speechMatches reports whether two files' speech windows match at offset 0 (same cut).
func speechMatches(a, b []refWindow) bool {
	tol := int(sameCutTol * syncRate)
	matched, n := 0, 0
	for _, wa := range a {
		sa := int(math.Round(wa.Start * syncRate))
		for _, wb := range b {
			sb := int(math.Round(wb.Start * syncRate))
			lo, hi := max(sa, sb), min(sa+len(wa.Speech), sb+len(wb.Speech))
			if hi-lo < int(sameCutOverlap*syncRate) {
				continue
			}
			other := onsetFrames(wb.Speech[lo-sb : hi-sb])
			m, k := 0, 0
			for _, i := range onsetFrames(wa.Speech[lo-sa : hi-sa]) {
				k++
				if j := sort.SearchInts(other, i-tol); j < len(other) && other[j] <= i+tol {
					m++
				}
			}
			if k >= sameCutStretch && float64(m) < sameCutLoose*float64(k) {
				return false
			}
			matched, n = matched+m, n+k
		}
	}
	return n >= sameCutOnsets && float64(matched) >= sameCutShare*float64(n)
}
