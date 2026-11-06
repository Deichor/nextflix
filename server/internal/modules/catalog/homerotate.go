package catalog

import (
	"hash/fnv"
	"math/rand/v2"
	"time"

	"nextflix/internal/platform/tmdb"
)

const (
	homeRotationPages = 3  // of a list the row draws from
	homeRowSize       = 20 // cards of a row, as one TMDB page gave
)

// rotateHomeRow picks a row's cards from the first pages of its list, another draw every UTC
// day. Page one of "popular" or a genre barely changes, so these rows looked the same for weeks.
func rotateHomeRow(rowID string, now time.Time, pages [][]tmdb.Movie) []tmdb.Movie {
	seen := map[int]bool{}
	var all []tmdb.Movie
	for _, page := range pages {
		for _, m := range page {
			if !seen[m.ID] {
				seen[m.ID] = true
				all = append(all, m)
			}
		}
	}
	h := fnv.New64a()
	h.Write([]byte(rowID))
	h.Write([]byte(now.UTC().Format(time.DateOnly)))
	rnd := rand.New(rand.NewPCG(h.Sum64(), 0x9e3779b97f4a7c15))
	rnd.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	return all[:min(homeRowSize, len(all))]
}
