package subtitles

import (
	"bufio"
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

// Real data for the residual line (sync_diag.go): how served cues lie on the heard speech, per
// stretch of diagResidualAround*2 seconds as a viewer at its middle gets it logged.
//
// TestSnapshotResidual (NFX_TESTDATA: a copy of /app/data; NFX_DIAG_PAIRS: lines
// "<hash>\t<index>\t<source>|<lang>|<ref>", the pairs the server served): the stored result of
// each pair, the subtitle as it is, and the served cues moved by +0.5 s; and the cue tap's own
// tracks of files whose check found them timed (tight) or another cut's.
//
// TestTruthResidual (NFX_TESTDATA: the snapshot the truth was made on; NFX_TRUTH: the
// subtitle-truth dirs; NFX_TRUTH_FILES: "<dir>=<hash>:<index>,..."): the residual against the
// served cues' real error (served start minus the spoken start, ASR truth) in the same stretches.

type resStat struct {
	label            string
	meds, withins    []float64
	n, stretches     int
	errMeds, errWith []float64 // truth: per stretch
}

func (s *resStat) add(r residual) {
	s.meds = append(s.meds, r.median)
	s.withins = append(s.withins, r.within)
	s.n += r.n
	s.stretches++
}

func (s resStat) String() string {
	if s.stretches == 0 {
		return s.label + ": none"
	}
	absMed := make([]float64, len(s.meds))
	for i, m := range s.meds {
		absMed[i] = math.Abs(m)
	}
	return fmt.Sprintf("%-28s %3d stretches (%4d cues): median %+.2fs (|median| p50 %.2f p90 %.2f), within 0.3 s p10 %.0f %% p50 %.0f %% p90 %.0f %%",
		s.label, s.stretches, s.n, median(s.meds), quantile(absMed, 0.5), quantile(absMed, 0.9),
		100*quantile(s.withins, 0.1), 100*quantile(s.withins, 0.5), 100*quantile(s.withins, 0.9))
}

func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[min(int(q*float64(len(s))), len(s)-1)]
}

// stretches calls fn for each stretch of 2*diagResidualAround seconds the speech covers.
func stretches(ws []refWindow, fn func(from, to float64)) {
	step := 2 * diagResidualAround
	for from := 0.0; ; from += step {
		heard, last := false, 0.0
		for _, w := range ws {
			last = math.Max(last, w.end())
			if w.end() > from && w.Start < from+step {
				heard = true
			}
		}
		if from >= last {
			return
		}
		if heard {
			fn(from, from+step)
		}
	}
}

// joined is the snapshot's speech windows as speechIn hands them out.
func joined(ws []refWindow) []refWindow { return joinWindows(ws) }

func shifted(cues []liveCue, d float64) []liveCue {
	out := make([]liveCue, len(cues))
	for i, c := range cues {
		out[i] = liveCue{c.start + d, c.end + d, c.text}
	}
	return out
}

func TestSnapshotResidual(t *testing.T) {
	dir := snapshotDir(t)
	path := os.Getenv("NFX_DIAG_PAIRS")
	if path == "" {
		t.Skip("NFX_DIAG_PAIRS not set")
	}
	cache := diskCache{dir}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	liveOf := func(hash string, index int) ([]refWindow, liveSaved) {
		var s liveSaved
		b, ok := cache.read("synclive", syncFileKey(hash, index), ".json", 0)
		if !ok || json.Unmarshal(b, &s) != nil {
			return nil, s
		}
		var ws []refWindow
		for w, d := range s.Windows {
			ws = append(ws, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
		}
		return joined(ws), s
	}
	stats := map[string]*resStat{}
	stat := func(label string) *resStat {
		if stats[label] == nil {
			stats[label] = &resStat{label: label}
		}
		return stats[label]
	}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), "\t", 3)
		id := strings.SplitN(p[len(p)-1], "|", 3)
		if len(p) != 3 || len(id) != 3 {
			continue
		}
		hash, inner := p[0], encodeID(id[0], id[1], id[2])
		index, _ := strconv.Atoi(p[1])
		source, _, ref, _ := decodeID(inner)
		raw, ok := cache.read(source, source+"|"+ref, ".vtt", 0)
		key, _ := syncKey(inner, hash, index)
		mb, ok2 := cache.read("synced", key, ".json", 0)
		var m syncMeta
		if !ok || !ok2 || json.Unmarshal(mb, &m) != nil {
			continue
		}
		served := vttCues(raw)
		if m.State == syncStateSynced || m.State == syncStateReplaced {
			b, ok := cache.read("synced", key, ".vtt", 0)
			if !ok {
				continue
			}
			served = vttCues(b)
		}
		ws, _ := liveOf(hash, index)
		if len(ws) == 0 {
			continue
		}
		state := m.State
		if m.Live {
			state += " provisional"
		}
		var per []string
		stretches(ws, func(from, to float64) {
			if r := residualOf(served, ws, from, to); r.n >= diagMinCues {
				stat("served " + state).add(r)
				per = append(per, fmt.Sprintf("%.0f:%+.2f/%.0f%%", from, r.median, 100*r.within))
				if m.fits() {
					stat("chance (fitting, moved 2-4 s)").add(residual{n: r.n, median: 0, within: r.chance})
				}
			}
			if m.fits() {
				if r := residualOf(shifted(served, 0.5), ws, from, to); r.n >= diagMinCues {
					stat("served fitting +0.5 s").add(r)
				}
				if r := residualOf(shifted(served, -0.3), ws, from, to); r.n >= diagMinCues {
					stat("served fitting -0.3 s").add(r)
				}
			}
			if r := residualOf(vttCues(raw), ws, from, to); r.n >= diagMinCues {
				stat("as it is (" + state + ")").add(r)
			}
		})
		t.Logf("%s:%d %-60s %-22s %s", hash[:8], index, shortSubID(inner), state, strings.Join(per, " "))
	}
	// Own tracks from the cue tap, against the speech of the same stretches
	lives, _ := filepath.Glob(filepath.Join(dir, "synclive", "*.json"))
	for _, p := range lives {
		b, _ := os.ReadFile(p)
		var s liveSaved
		if json.Unmarshal(b, &s) != nil || s.Check == nil || len(s.Own) == 0 {
			continue
		}
		var ws []refWindow
		var own []liveCue
		for w, d := range s.Windows {
			ws = append(ws, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
		}
		for w, d := range s.Own {
			for _, f := range d.Starts {
				own = append(own, liveCue{start: float64(w)*syncWindowLen + float64(f)/syncRate, end: 0, text: "own"})
			}
		}
		label := "own track " + s.Check.State
		if s.Check.State == ownTimed && s.Check.Tight {
			label += " tight"
		} else if s.Check.State == ownTimed {
			label += " loose"
		}
		js := joined(ws)
		var per []string
		stretches(js, func(from, to float64) {
			if r := residualOf(own, js, from, to); r.n >= diagMinCues {
				stat(label).add(r)
				per = append(per, fmt.Sprintf("%.0f:%+.2f/%.0f%%", from, r.median, 100*r.within))
			}
		})
		if len(per) > 0 {
			t.Logf("%s own track (%s, %d stretches checked) %s", filepath.Base(p)[:8], label, s.Check.Pairs, strings.Join(per, " "))
		}
	}
	var labels []string
	for l := range stats {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		t.Logf("%s", stats[l])
	}
}

// TestSnapshotResidualFit (NFX_DIAG_FIT: "<hash>:<index>:<seconds>,..."; the length when the
// snapshot has no probe of it): for files whose served pairs are not known (films), every raw
// subtitle of a fitting length is fitted to all the speech of the file (heard, read); the ones that
// fit stand for a final timing, the ones that do not for another release or title served wrong.
func TestSnapshotResidualFit(t *testing.T) {
	dir := snapshotDir(t)
	spec := os.Getenv("NFX_DIAG_FIT")
	if spec == "" {
		t.Skip("NFX_DIAG_FIT not set")
	}
	cache := diskCache{dir}
	type rawSub struct {
		name  string
		cues  []liveCue
		spans []span
	}
	var subs []rawSub
	for _, kind := range []string{SourceTorrent, SourceEmbedded, SourceOpenSubtitles, SourceSubDL, SourceSubSource,
		SourceTurkceAltyazi, SourceSubf2m, SourceYIFY} {
		ps, _ := filepath.Glob(filepath.Join(dir, kind, "*.vtt"))
		for _, p := range ps {
			b, _ := os.ReadFile(p)
			cues := vttCues(b)
			if len(cues) < 100 {
				continue
			}
			s := rawSub{name: kind + "/" + filepath.Base(p)[:8], cues: cues}
			for _, c := range cues {
				s.spans = append(s.spans, span{c.start, c.end})
			}
			subs = append(subs, s)
		}
	}
	stats := map[string]*resStat{}
	stat := func(label string) *resStat {
		if stats[label] == nil {
			stats[label] = &resStat{label: label}
		}
		return stats[label]
	}
	for _, f := range strings.Split(spec, ",") {
		p := strings.Split(f, ":")
		if len(p) < 2 {
			continue
		}
		hash := p[0]
		index, _ := strconv.Atoi(p[1])
		fk := syncFileKey(hash, index)
		var ref syncRef
		if b, ok := cache.read("syncref", fk, ".json", 0); ok {
			_ = json.Unmarshal(b, &ref)
		}
		duration := ref.Probe.Duration
		if len(p) > 2 {
			duration, _ = strconv.ParseFloat(p[2], 64)
		}
		var live []refWindow
		var saved liveSaved
		if b, ok := cache.read("synclive", fk, ".json", 0); ok && json.Unmarshal(b, &saved) == nil {
			for w, d := range saved.Windows {
				live = append(live, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
			}
		}
		// the speech: heard windows, then read ones where none was heard
		speech := append([]refWindow(nil), live...)
		for _, w := range append(append([]refWindow(nil), ref.Windows...), ref.Speech...) {
			overlaps := false
			for _, o := range speech {
				overlaps = overlaps || math.Min(w.end(), o.end()) > math.Max(w.Start, o.Start)
			}
			if w.Kind != "subs" && !overlaps {
				w.Kind = "live"
				speech = append(speech, w)
			}
		}
		sort.Slice(speech, func(i, j int) bool { return speech[i].Start < speech[j].Start })
		ws := joined(speech)
		if len(speech) < 4 || duration <= 0 {
			t.Logf("%s:%d: %d speech windows, length %.0f s: skipped", hash[:8], index, len(speech), duration)
			continue
		}
		fits, others := 0, 0
		for _, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if last < 0.6*duration || last > 1.3*duration+120 {
				continue
			}
			fit, ok := alignLive(clampSpans(s.spans, duration), speech, syncScales)
			good := ok && fit.good() && lengthFits(s.spans, fit, duration)
			if good {
				fits++
			} else if others++; others > 12 {
				continue // enough of those
			}
			served := applyFit(s.cues, fit)
			var per []string
			stretches(ws, func(from, to float64) {
				r := residualOf(served, ws, from, to)
				if r.n < diagMinCues {
					return
				}
				if good {
					stat("fits").add(r)
					stat("fits: chance (moved 2-4 s)").add(residual{n: r.n, within: r.chance})
					if q := residualOf(shifted(served, 0.5), ws, from, to); q.n >= diagMinCues {
						stat("fits +0.5 s").add(q)
					}
					per = append(per, fmt.Sprintf("%.0f:%+.2f/%.0f%%", from, r.median, 100*r.within))
				} else {
					stat("does not fit (its best offsets)").add(r)
				}
			})
			if good {
				// A viewer every minute of the heard speech: how often a residual line comes, and from how far back
				near, back, none := 0, 0, 0
				for _, w := range ws {
					for at := w.Start + diagResidualAround; at < w.end()-diagResidualAround; at += diagResidualStep {
						switch {
						case residualOf(served, ws, at-diagResidualAround, at+diagResidualAround).n >= diagMinCues:
							near++
						case residualOf(served, ws, at-diagResidualBack, at+diagResidualAround).n >= diagMinCues:
							back++
						default:
							none++
						}
					}
				}
				t.Logf("%s:%d %s fits (%.4f, %d offsets, score %.2f over %.2f; viewer minutes with a residual %d, from 300 s back %d, none %d): %s",
					hash[:8], index, s.name, fit.Scale, len(fit.Segments), fit.Score, fit.Base, near, back, none, strings.Join(per, " "))
			}
		}
		// the file's own track from the cue tap
		if len(saved.Own) > 0 {
			var own []liveCue
			for w, d := range saved.Own {
				for _, fr := range d.Starts {
					own = append(own, liveCue{start: float64(w)*syncWindowLen + float64(fr)/syncRate, text: "own"})
				}
			}
			var per []string
			stretches(ws, func(from, to float64) {
				if r := residualOf(own, ws, from, to); r.n >= diagMinCues {
					stat("own track (cue tap)").add(r)
					per = append(per, fmt.Sprintf("%.0f:%+.2f/%.0f%%", from, r.median, 100*r.within))
				}
			})
			t.Logf("%s:%d own track: %s", hash[:8], index, strings.Join(per, " "))
		}
		t.Logf("%s:%d (%.0f s, %d speech windows): %d subtitles fit, %d tried that do not", hash[:8], index, duration, len(speech), fits, others)
	}
	var labels []string
	for l := range stats {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		t.Logf("%s", stats[l])
	}
}

type truthLine struct {
	RawS  float64  `json:"raw_s"`
	Text  string   `json:"text"`
	Truth *float64 `json:"truth"`
	Conf  string   `json:"conf"`
}

func TestTruthResidual(t *testing.T) {
	dir := snapshotDir(t)
	root, files := os.Getenv("NFX_TRUTH"), os.Getenv("NFX_TRUTH_FILES")
	if root == "" || files == "" {
		t.Skip("NFX_TRUTH / NFX_TRUTH_FILES not set")
	}
	cache := diskCache{dir}
	all := map[string]*resStat{}
	stat := func(label string) *resStat {
		if all[label] == nil {
			all[label] = &resStat{label: label}
		}
		return all[label]
	}
	var pairs [][2]float64 // residual median, true error median per stretch (served)
	for _, spec := range strings.Split(files, ",") {
		name, file, _ := strings.Cut(spec, "=")
		hash, idx, _ := strings.Cut(file, ":")
		index, _ := strconv.Atoi(idx)
		var s liveSaved
		b, ok := cache.read("synclive", syncFileKey(hash, index), ".json", 0)
		if !ok || json.Unmarshal(b, &s) != nil {
			t.Logf("%s: no live windows", name)
			continue
		}
		var live []refWindow
		for w, d := range s.Windows {
			live = append(live, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
		}
		ws := joined(live)
		for _, lang := range []string{"en", "other", "tr2", "own_2", "own"} {
			linesFile := map[string]string{"en": "en_lines.json", "other": "tr_lines.json", "tr2": "tr2_lines.json", "own_2": "own_2_lines.json", "own": "own_lines.json"}[lang]
			var lj struct {
				Lines []truthLine `json:"lines"`
			}
			b, err := os.ReadFile(filepath.Join(root, name, linesFile))
			if err != nil || json.Unmarshal(b, &lj) != nil {
				continue
			}
			// the truth of each line by its raw start; served cues matched by text, nearest raw start + its offset
			type tl struct{ raw, truth float64 }
			byText := map[string][]tl{}
			var truthCues []liveCue
			for _, l := range lj.Lines {
				if l.Truth == nil || (l.Conf != "high" && l.Conf != "medium") {
					continue
				}
				text := strings.TrimSpace(l.Text)
				byText[text] = append(byText[text], tl{l.RawS, *l.Truth})
				truthCues = append(truthCues, liveCue{start: *l.Truth, text: text})
			}
			score := func(label string, cues []liveCue, rawOf func(i int) float64) {
				stretches(ws, func(from, to float64) {
					r := residualOf(cues, ws, from, to)
					if r.n < diagMinCues {
						return
					}
					var errs []float64
					for i, c := range cues {
						if c.start < from || c.start >= to {
							continue
						}
						cands := byText[strings.TrimSpace(c.text)]
						best, found := tl{}, false
						for _, x := range cands {
							if !found || math.Abs(x.raw-rawOf(i)) < math.Abs(best.raw-rawOf(i)) {
								best, found = x, true
							}
						}
						if found && math.Abs(best.raw-rawOf(i)) < 0.05 {
							errs = append(errs, c.start-best.truth)
						}
					}
					if len(errs) < 5 {
						return
					}
					st := stat(label)
					st.add(r)
					em := median(errs)
					in := 0
					for _, e := range errs {
						if math.Abs(e) <= 0.3 {
							in++
						}
					}
					st.errMeds = append(st.errMeds, em)
					st.errWith = append(st.errWith, float64(in)/float64(len(errs)))
					if strings.HasPrefix(label, "served") {
						pairs = append(pairs, [2]float64{r.median, em})
					}
					if os.Getenv("NFX_EVAL") == "verbose" {
						t.Logf("%s %-5s %-14s %5.0f-%5.0f residual %+.2f %3.0f %% n=%2d chance %3.0f %% | truth %+.2f %3.0f %% n=%d",
							name, lang, label, from, to, r.median, 100*r.within, r.n, 100*r.chance, em, 100*float64(in)/float64(len(errs)), len(errs))
					}
				})
			}
			if strings.HasPrefix(lang, "own") {
				var own []liveCue
				for _, l := range lj.Lines {
					own = append(own, liveCue{start: l.RawS, text: strings.TrimSpace(l.Text)})
				}
				score("own track "+name, own, func(i int) float64 { return own[i].start })
				continue
			}
			sub := map[string]string{"en": "en", "other": "other", "tr2": "tr2"}[lang]
			rawB, err1 := os.ReadFile(filepath.Join(root, name, "in", sub+".vtt"))
			servedB, err2 := os.ReadFile(filepath.Join(root, name, "in", "served_"+sub+".vtt"))
			if err1 != nil || err2 != nil {
				continue
			}
			raw, served := vttCues(rawB), vttCues(servedB)
			rawStart := func(cues []liveCue) func(i int) float64 {
				// the raw cue of each served one: same text, the k-th occurrence
				seen := map[string]int{}
				pos := map[string][]float64{}
				for _, c := range raw {
					pos[strings.TrimSpace(c.text)] = append(pos[strings.TrimSpace(c.text)], c.start)
				}
				out := make([]float64, len(cues))
				for i, c := range cues {
					k := strings.TrimSpace(c.text)
					if p := pos[k]; seen[k] < len(p) {
						out[i] = p[seen[k]]
					} else {
						out[i] = math.NaN()
					}
					seen[k]++
				}
				return func(i int) float64 { return out[i] }
			}
			score("served", served, rawStart(served))
			score("served +0.5 s", shifted(served, 0.5), rawStart(served))
			score("as it is", raw, rawStart(raw))
			// the truth itself: where the speech really starts (the residual's floor)
			score("truth", truthCues, func(i int) float64 {
				for _, x := range byText[truthCues[i].text] {
					if x.truth == truthCues[i].start {
						return x.raw
					}
				}
				return math.NaN()
			})
		}
	}
	var labels []string
	for l := range all {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		s := all[l]
		absErr := make([]float64, len(s.errMeds))
		for i, e := range s.errMeds {
			absErr[i] = math.Abs(e)
		}
		t.Logf("%s\n    truth: median %+.2fs (|median| p50 %.2f p90 %.2f), within 0.3 s p10 %.0f %% p50 %.0f %% p90 %.0f %%", s, median(s.errMeds),
			quantile(absErr, 0.5), quantile(absErr, 0.9), 100*quantile(s.errWith, 0.1), 100*quantile(s.errWith, 0.5), 100*quantile(s.errWith, 0.9))
	}
	// How well the residual's median follows the real error of the served cues
	var dev []float64
	big, caught := 0, 0
	for _, p := range pairs {
		dev = append(dev, math.Abs(p[0]-p[1]))
		if math.Abs(p[1]) >= 0.3 {
			big++
			if math.Abs(p[0]) >= 0.2 && p[0]*p[1] > 0 {
				caught++
			}
		}
	}
	t.Logf("served stretches: %d; |residual median - true median| p50 %.2f p90 %.2f; stretches off by 0.3 s or more: %d, residual saw %d of them (|median| >= 0.2 s, same sign)",
		len(pairs), quantile(dev, 0.5), quantile(dev, 0.9), big, caught)
}
