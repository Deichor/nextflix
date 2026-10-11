package subtitles

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Speech detector evaluation on real audio (NFX_AUDIO: 8 kHz mono PCM windows of snapshot files,
// read like readSyncWindow does, next to files.txt "name hash index" and windows.txt
// "name hash index start lines"; NFX_TESTDATA: the snapshot with the files' own-track windows).
// Truth is the file's own subtitle track in the same window: where its lines start, speech
// should start.

type vadWin struct {
	file  string
	start float64
	pcm   []int16
	own   refWindow
}

func loadVadWindows(t *testing.T) []vadWin {
	dir := os.Getenv("NFX_AUDIO")
	if dir == "" {
		t.Skip("NFX_AUDIO not set")
	}
	snap := snapshotDir(t)
	keys := map[string]string{}
	readLines := func(name string, fn func([]string)) {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if p := strings.Fields(sc.Text()); len(p) >= 3 {
				fn(p)
			}
		}
	}
	readLines("files.txt", func(p []string) { keys[p[0]] = syncFileKey(p[1], atoi(p[2])) })
	var out []vadWin
	refs := map[string]syncRef{}
	readLines("windows.txt", func(p []string) {
		raw, err := os.ReadFile(filepath.Join(dir, p[0]+"_"+p[3]+".s16"))
		if err != nil || len(raw) < 2*syncSampleRate*50 {
			return
		}
		r, ok := refs[p[0]]
		if !ok {
			b, err := os.ReadFile(filepath.Join(snap, "syncref", hashKey(keys[p[0]])+".json"))
			if err != nil || json.Unmarshal(b, &r) != nil {
				return
			}
			refs[p[0]] = r
		}
		start, _ := strconv.ParseFloat(p[3], 64)
		for _, w := range r.Windows {
			if w.Kind == "subs" && math.Abs(w.Start-start) < 0.01 {
				pcm := make([]int16, len(raw)/2)
				for i := range pcm {
					pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
				}
				out = append(out, vadWin{p[0], start, pcm, w})
			}
		}
	})
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ownCues are the own track's lines of a window (file times); a line ends where it stops
// showing or the next one starts.
func ownCues(w refWindow) []span {
	var out []span
	for i := 0; i < len(w.Speech); i++ {
		if w.Speech[i] != 2 {
			continue
		}
		j := i + 1
		for j < len(w.Speech) && w.Speech[j] == 1 {
			j++
		}
		out = append(out, span{w.Start + float64(i)/syncRate, w.Start + float64(j)/syncRate})
	}
	return out
}

// onsetBias: per own line start the nearest detected speech onset within 0.6 s (seconds, later
// positive), and the share of lines that have one.
func onsetBias(speech []byte, w vadWin) ([]float64, float64) {
	var ds []float64
	lines := 0
	for _, c := range ownCues(w.own) {
		i0 := int(math.Round((c.start - w.start) * syncRate))
		if i0 < 100 || i0 > len(speech)-100 {
			continue
		}
		lines++
		best := 61
		for j := max(i0-60, 1); j <= i0+60 && j < len(speech); j++ {
			if speech[j] != 0 && speech[j-1] == 0 && abs(j-i0) < abs(best) {
				best = j - i0
			}
		}
		if best <= 60 {
			ds = append(ds, float64(best)/syncRate)
		}
	}
	return ds, float64(len(ds)) / float64(max(lines, 1))
}

type vadDetector struct {
	name string
	run  func(pcm []int16) []byte
}

// vadLag is the offset that fits the file's own lines to its speech-only windows (the
// detector's lag as the alignment sees it).
func vadLag(ws []vadWin, det func([]int16) []byte) (lag float64, onsets []float64, matched float64, ok bool) {
	var cues []span
	var ref []refWindow
	n := 0.0
	for _, w := range ws {
		sp := det(w.pcm)
		ds, m := onsetBias(sp, w)
		onsets = append(onsets, ds...)
		matched += m
		n++
		cues = append(cues, ownCues(w.own)...)
		ref = append(ref, refWindow{Start: w.start, Kind: "audio", Speech: sp})
	}
	sort.Slice(cues, func(i, j int) bool { return cues[i].start < cues[j].start })
	// the best offset within 1 s (the truth is 0; a far peak of a few windows is no lag)
	starts := startSignal(cues, 1)
	d := int(syncMaxOffset * syncRate)
	total := make([]float64, 2*d+1)
	pw := prepare(ref)
	if len(pw) == 0 {
		return 0, onsets, matched / n, false
	}
	for _, w := range pw {
		for q, v := range windowCurve(w, starts, d) {
			total[q] += v
		}
	}
	q := argmax(total, d-100, d+100)
	return float64(q-d) / syncRate, onsets, matched / n, true
}

func mad(v []float64) float64 {
	m := median(v)
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - m)
	}
	return median(d)
}

// TestVadLag reports, per detector and file, the onset bias against the own track and the
// alignment lag, and how well a constant correction from the other files (leave one file out)
// brings each file's lag to 0.
func TestVadLag(t *testing.T) {
	all := loadVadWindows(t)
	byFile := map[string][]vadWin{}
	var names []string
	for _, w := range all {
		if byFile[w.file] == nil {
			names = append(names, w.file)
		}
		byFile[w.file] = append(byFile[w.file], w)
	}
	sort.Strings(names)
	t.Logf("%d windows of %d files", len(all), len(names))
	only := os.Getenv("NFX_DET")
	for _, d := range vadDetectors() {
		if only != "" && !strings.Contains(d.name, only) {
			continue
		}
		lags := map[string]float64{}
		var rows []string
		for _, name := range names {
			lag, ds, matched, ok := vadLag(byFile[name], d.run)
			if !ok {
				rows = append(rows, fmt.Sprintf("  %s: no fit", name))
				continue
			}
			lags[name] = lag
			rows = append(rows, fmt.Sprintf("  %s (%d windows): lag %+.2f s | onsets: %3.0f%% of lines, median %+.2f s, spread %.2f s",
				name, len(byFile[name]), lag, 100*matched, median(ds), mad(ds)))
		}
		var abs0, loo, vals []float64
		for name, l := range lags {
			var others []float64
			for o, x := range lags {
				if o != name {
					others = append(others, x)
				}
			}
			vals = append(vals, l)
			abs0 = append(abs0, math.Abs(l))
			loo = append(loo, math.Abs(l-median(others)))
		}
		t.Logf("DETECTOR %-16s median lag %+.3f s | |lag| mean %.3f max %.3f | leave-one-out corrected |lag| mean %.3f max %.3f",
			d.name, median(vals), meanOf(abs0), maxOf(abs0), meanOf(loo), maxOf(loo))
		for _, r := range rows {
			t.Log(r)
		}
	}
}

func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = math.Max(m, x)
	}
	return m
}

// TestVadAcousticOnsets separates the detector's delay from the own track's style: per line,
// the acoustic onset (the first frame whose band energy is 10 dB above the quiet of the 0.5 s
// before it, and stays there for 100 ms) against the line start and the detected onset.
func TestVadAcousticOnsets(t *testing.T) {
	all := loadVadWindows(t)
	type acc struct{ lead, det, end []float64 }
	files := map[string]*acc{}
	var names []string
	for _, w := range all {
		a := files[w.file]
		if a == nil {
			a = &acc{}
			files[w.file] = a
			names = append(names, w.file)
		}
		db := energiesDB(w.pcm, true)
		sp := speechFrames(w.pcm, syncSampleRate)
		cues := ownCues(w.own)
		for ci, c := range cues {
			i0 := int(math.Round((c.start - w.start) * syncRate))
			if i0 < 120 || i0 > len(db)-120 {
				continue
			}
			_ = ci
			// the first rise of 8 dB over the quiet 0.2 s before it, held for 100 ms
			fine := -1
			for j := i0 - 60; j <= i0+80; j++ {
				q := append([]float64(nil), db[j-20:j]...)
				sort.Float64s(q)
				floor := q[len(q)/2]
				if q[len(q)-1] > floor+6 {
					continue // not quiet before
				}
				ok := true
				for k := j; k < j+10; k++ {
					ok = ok && db[k] >= floor+8
				}
				if ok {
					fine = j
					break
				}
			}
			if fine < 0 {
				continue
			}
			a.lead = append(a.lead, float64(fine-i0)/syncRate)
			for j := max(fine-30, 1); j <= fine+60 && j < len(sp); j++ {
				if sp[j] != 0 && sp[j-1] == 0 {
					a.det = append(a.det, float64(j-fine)/syncRate)
					break
				}
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		a := files[n]
		t.Logf("%s: %3d lines | acoustic onset - line start: median %+.2f s (spread %.2f) | detected - acoustic onset: median %+.2f s (spread %.2f, %d)",
			n, len(a.lead), median(a.lead), mad(a.lead), median(a.det), mad(a.det), len(a.det))
	}
}

// TestSnapshotTwoOwnWindows: the speech of the played audio carries a per-file lag against
// the own track (its lines lead the speech by 0-0.35 s). With two own-track windows (what
// readEarly reads from the play start) the fitted Lag takes that style over to every live
// window. Real replays: audio only vs audio + the 2 own windows nearest the first heard one.
func TestSnapshotTwoOwnWindows(t *testing.T) {
	files, subs := loadSnapshot(t)
	for _, c := range replayCases {
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if f == nil || s == nil || len(f.live) == 0 || subsWindows(f.ref) < 2 {
			continue
		}
		tr, ok := findTruth(s.spans, f.ref)
		if c.known {
			tr, ok = truth{scale: 1, constant: true, c: c.constant, duration: f.ref.Probe.Duration}, true
		}
		if !ok {
			continue
		}
		ct := tr.cueTruth(s.spans)
		first := f.live[0].Start
		own := append([]refWindow(nil), f.ref.Windows...)
		sort.Slice(own, func(i, j int) bool { return math.Abs(own[i].Start-first) < math.Abs(own[j].Start-first) })
		two := &snapFile{name: f.name, live: f.live, ref: syncRef{Probe: f.ref.Probe, Skipped: true}}
		for _, w := range own {
			if w.Kind == "subs" && len(two.ref.Windows) < 2 {
				two.ref.Windows = append(two.ref.Windows, w)
			}
		}
		none := evalReplay(replayLive(t, f, s.spans, false, nil), s.spans, ct, nil)
		with := evalReplay(replayLive(t, two, s.spans, true, nil), s.spans, ct, nil)
		t.Logf("%-36s audio only: final %s | + 2 own windows: final %s", c.note, none.final, with.final)
	}
}

// vadFixture is a real audio window without the audio: its frame energies as the tap keeps them
// (1 + 2·dB of the pre-emphasised frame), and the own track's lines there.
type vadFixture struct {
	Name     string        `json:"name"`
	Energies []byte        `json:"energies"`
	Own      fixtureWindow `json:"own"`
}

func tapEnergies(pcm []int16) []uint8 {
	frame := syncSampleRate / syncRate
	out := make([]uint8, len(pcm)/frame)
	prev := 0.0
	for f := range out {
		var db float64
		db, prev = frameEnergy(pcm[f*frame:(f+1)*frame], prev)
		out[f] = uint8(min(255, 1+math.Round(db*2)))
	}
	return out
}

// TestVadWriteFixture writes two windows (a stereo downmix and a centre channel) as fixtures
// into NFX_FIXTURE_OUT.
func TestVadWriteFixture(t *testing.T) {
	out := os.Getenv("NFX_FIXTURE_OUT")
	if out == "" {
		t.Skip("NFX_FIXTURE_OUT not set")
	}
	var fx []vadFixture
	for _, w := range loadVadWindows(t) {
		if w.file == "e95a8a0e" && w.start == 1035 || w.file == "bf8fac49" && w.start == 20 {
			fx = append(fx, vadFixture{Name: fmt.Sprintf("%s@%.0f", w.file, w.start), Energies: tapEnergies(w.pcm), Own: fixtureWindowOf(w.own)})
		}
	}
	b, _ := json.Marshal(fx)
	if err := os.WriteFile(filepath.Join(out, "vad_onsets.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d windows, %d bytes", len(fx), len(b))
}

// Detector variants under evaluation.

// biquad is an RBJ cookbook second-order filter.
type biquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func newBiquad(highpass bool, f0, fs float64) *biquad {
	w := 2 * math.Pi * f0 / fs
	alpha := math.Sin(w) / (2 * 0.7071) // Q = 0.707 (Butterworth)
	cw := math.Cos(w)
	var b0, b1, b2 float64
	if highpass {
		b0, b1, b2 = (1+cw)/2, -(1 + cw), (1+cw)/2
	} else {
		b0, b1, b2 = (1-cw)/2, 1-cw, (1-cw)/2
	}
	a0 := 1 + alpha
	return &biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: -2 * cw / a0, a2: (1 - alpha) / a0}
}

func (q *biquad) step(x float64) float64 {
	y := q.b0*x + q.b1*q.x1 + q.b2*q.x2 - q.a1*q.y1 - q.a2*q.y2
	q.x2, q.x1, q.y2, q.y1 = q.x1, x, q.y1, y
	return y
}

// energiesDB: frame energies in dB, band-passed (300-3400 Hz) or pre-emphasised.
func energiesDB(pcm []int16, band bool) []float64 {
	frame := syncSampleRate / syncRate
	db := make([]float64, len(pcm)/frame)
	hp, lp := newBiquad(true, 300, syncSampleRate), newBiquad(false, 3400, syncSampleRate)
	prev := 0.0
	for f := range db {
		sum := 0.0
		for _, s := range pcm[f*frame : (f+1)*frame] {
			x := float64(s)
			var y float64
			if band {
				y = lp.step(hp.step(x))
			} else {
				y = x - 0.97*prev
				prev = x
			}
			sum += y * y
		}
		db[f] = 10 * math.Log10(sum/float64(frame)+1)
	}
	return db
}

// backtrack moves every speech onset back to where the energy left the quiet before it.
func backtrack(db []float64, sp []byte, rise float64, maxBack int) []byte {
	out := append([]byte(nil), sp...)
	for i := 1; i < len(sp); i++ {
		if sp[i] == 0 || sp[i-1] != 0 {
			continue
		}
		floor := math.Inf(1)
		for j := max(i-50, 0); j < i; j++ {
			floor = math.Min(floor, db[j])
		}
		j := i
		for j > 0 && i-j < maxBack && db[j-1] > floor+rise && out[j-1] == 0 {
			j--
		}
		for k := j; k < i; k++ {
			out[k] = 1
		}
	}
	return out
}

func vadDetectors() []vadDetector {
	ds := []vadDetector{
		{"current", func(pcm []int16) []byte { return speechFrames(pcm, syncSampleRate) }},
		{"band", func(pcm []int16) []byte { return speechOf(energiesDB(pcm, true)) }},
	}
	for _, rise := range []float64{3, 6} {
		for _, band := range []bool{false, true} {
			name := fmt.Sprintf("bt%.0f", rise)
			if band {
				name = "band+" + name
			}
			rise, band := rise, band
			ds = append(ds, vadDetector{name, func(pcm []int16) []byte {
				db := energiesDB(pcm, band)
				return backtrack(db, speechOf(db), rise, 40)
			}})
		}
	}
	return ds
}
