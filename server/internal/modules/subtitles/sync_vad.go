package subtitles

import (
	"math"
	"sort"
)

// speechFrames is a simple voice activity detector for the timing reference: 16-bit mono PCM
// becomes 10 ms frames, 1 where speech is likely. The audio should be the dialogue (centre)
// channel when the file has one. A frame is speech when its pre-emphasised energy (which damps
// bass, music beds and rumble) stands well above the window's quiet level; short pauses are
// bridged and clicks dropped so the result looks like subtitle lines.
func speechFrames(pcm []int16, sampleRate int) []byte {
	frame := sampleRate / syncRate
	if frame <= 0 || len(pcm) < frame {
		return nil
	}
	db := make([]float64, len(pcm)/frame)
	prev := 0.0
	for f := range db {
		db[f], prev = frameEnergy(pcm[f*frame:(f+1)*frame], prev)
	}
	return speechOf(db)
}

// frameEnergy is the pre-emphasised energy of one frame in dB; prev is the sample before it.
func frameEnergy(pcm []int16, prev float64) (db, last float64) {
	sum := 0.0
	for _, s := range pcm {
		x := float64(s)
		y := x - 0.97*prev
		prev = x
		sum += y * y
	}
	return 10 * math.Log10(sum/float64(len(pcm))+1), prev
}

// speechOf marks the speech frames of a window's frame energies (dB).
func speechOf(db []float64) []byte {
	n := len(db)
	if n == 0 {
		return nil
	}
	sorted := append([]float64(nil), db...)
	sort.Float64s(sorted)
	floor := sorted[int(0.15*float64(n-1))]
	top := sorted[int(0.97*float64(n-1))]
	out := make([]byte, n)
	if top-floor < 8 {
		return out // flat: nothing to tell apart
	}
	thr := floor + math.Max(8, 0.4*(top-floor))
	for f, v := range db {
		if v > thr {
			out[f] = 1
		}
	}
	fillRuns(out, 0, 30) // pauses up to 300 ms inside a line
	fillRuns(out, 1, 10) // bursts under 100 ms are clicks
	return out
}

// fillRuns flips inner runs of value v shorter than maxLen frames.
func fillRuns(b []byte, v byte, maxLen int) {
	for i := 0; i < len(b); {
		if b[i] != v {
			i++
			continue
		}
		j := i
		for j < len(b) && b[j] == v {
			j++
		}
		if i > 0 && j < len(b) && j-i < maxLen {
			for k := i; k < j; k++ {
				b[k] = 1 - v
			}
		}
		i = j
	}
}
