package catalog

import (
	"math/rand/v2"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

var syllables = strings.Fields("ka ri mo na lo te sa vi du ne ga ro li mi to ra be an or el is th st ch wa ve ho re ma de la pa co")

// syntheticTitles returns n made-up titles (1-4 words) with popularity like the exports'.
func syntheticTitles(n int) []IndexTitle {
	rng := rand.New(rand.NewPCG(1, 2))
	out := make([]IndexTitle, 0, n)
	for i := range n {
		words := make([]string, 1+rng.IntN(4))
		for w := range words {
			var b strings.Builder
			for range 2 + rng.IntN(3) {
				b.WriteString(syllables[rng.IntN(len(syllables))])
			}
			words[w] = b.String()
		}
		if rng.IntN(5) == 0 {
			words = append([]string{"The"}, words...)
		}
		out = append(out, IndexTitle{Kind: uint8(i % 2), ID: int32(1_000_000 + i), Pop: float32(1 + rng.ExpFloat64()*3),
			Title: strings.Join(words, " ")})
	}
	return out
}

// The index part of a search must stay well under 150 ms at the production size.
func TestIndexPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	titles := append(syntheticTitles(indexMaxTitles-len(fixtureTitles())), fixtureTitles()...)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	ix := NewTitleIndex(titles)
	build := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	nt, nn := ix.Size()
	t.Logf("index: %d titles, %d names, built in %s, heap +%.1f MB", nt, nn, build.Round(time.Millisecond),
		float64(after.HeapAlloc-before.HeapAlloc)/(1<<20))

	queries := []string{"mandoloarien", "mandalorien", "the mandolorian", "brekin bad", "interstelar", "game of trones",
		"la casa de papl", "karimona", "the", "st", "lorimakanu tesavi", "dune", "a"}
	var lat []time.Duration
	for range 3 {
		for _, q := range queries {
			s := time.Now()
			ix.Correct(q, 3, correctMin)
			ix.KnownWords(q)
			ix.Suggest(q, "en", 8)
			lat = append(lat, time.Since(s))
		}
	}
	sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
	p50, p95, worst := lat[len(lat)/2], lat[len(lat)*95/100], lat[len(lat)-1]
	t.Logf("correct+suggest per query: p50 %s p95 %s max %s", p50, p95, worst)
	limit := 150 * time.Millisecond
	if raceEnabled {
		limit *= 10
	}
	if p95 > limit {
		t.Errorf("p95 %s over %s", p95, limit)
	}
	if hits := ix.Correct("mandoloarien", 1, correctMin); len(hits) == 0 || hits[0].ID != 82856 {
		t.Errorf("mandoloarien among 200k titles: %+v", hits)
	}
}

func BenchmarkCorrect(b *testing.B) {
	ix := NewTitleIndex(append(syntheticTitles(indexMaxTitles), fixtureTitles()...))
	b.ResetTimer()
	for b.Loop() {
		ix.Correct("mandoloarien", 3, correctMin)
	}
}

func BenchmarkSuggest(b *testing.B) {
	ix := NewTitleIndex(append(syntheticTitles(indexMaxTitles), fixtureTitles()...))
	b.ResetTimer()
	for b.Loop() {
		ix.Suggest("mandolori", "en", 8)
	}
}
