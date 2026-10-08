package recommendations

import (
	"math"
	"sort"
	"strconv"
	"time"
)

// Tuning of the scoring; see docs/ARCHITECTURE.md "Recommendations".
const (
	weightLove     = 3.0
	weightLike     = 1.5
	weightFavorite = 2.0
	weightDislike  = -3.0

	maxSeeds       = 8
	maxDislikes    = 5
	maxGenreTitles = 40

	listWeightRecs    = 1.0
	listWeightSimilar = 0.5
	positionDecay     = 0.08 // item i of a TMDB list counts 1/(1+0.08*i)
	recencyDays       = 120.0

	genreWeight     = 1.5
	voteWeight      = 0.6
	minVotes        = 10 // titles with fewer TMDB votes are noise in the related lists
	popularityScale = 0.1

	topPicksSize   = 20
	becauseRows    = 3
	becauseMinSize = 8
	becauseMaxSize = 20
)

// signalWeight is the additive weight of a rating plus the favorite flag.
func signalWeight(s Signal) float64 {
	w := 0.0
	switch s.Rating {
	case "love":
		w = weightLove
	case "like":
		w = weightLike
	case "dislike":
		w = weightDislike
	}
	if s.Favorite {
		w += weightFavorite
	}
	return w
}

// recency fades old signals towards half strength: 1.0 today, ~0.68 after 120 days.
func recency(updated, now time.Time) float64 {
	days := now.Sub(updated).Hours() / 24
	if days < 0 {
		days = 0
	}
	return 0.5 + 0.5*math.Exp(-days/recencyDays)
}

type seed struct {
	Signal
	weight float64 // |signal weight| x recency
}

// pickSeeds returns the strongest recent positive titles and the most recent dislikes.
func pickSeeds(signals []Signal, now time.Time) (pos, neg []seed) {
	for _, s := range signals {
		w := signalWeight(s)
		switch {
		case w > 0:
			pos = append(pos, seed{s, w * recency(s.UpdatedAt, now)})
		case s.Rating == "dislike":
			neg = append(neg, seed{s, -weightDislike * recency(s.UpdatedAt, now)})
		}
	}
	sort.SliceStable(pos, func(i, j int) bool {
		if pos[i].weight != pos[j].weight {
			return pos[i].weight > pos[j].weight
		}
		return pos[i].UpdatedAt.After(pos[j].UpdatedAt)
	})
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].UpdatedAt.After(neg[j].UpdatedAt) })
	if len(pos) > maxSeeds {
		pos = pos[:maxSeeds]
	}
	if len(neg) > maxDislikes {
		neg = neg[:maxDislikes]
	}
	return pos, neg
}

// genreTitles returns the most recent rated titles used for genre affinity.
func genreTitles(signals []Signal) []Signal {
	out := make([]Signal, 0, len(signals))
	for _, s := range signals {
		if signalWeight(s) != 0 {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > maxGenreTitles {
		out = out[:maxGenreTitles]
	}
	return out
}

// genreAffinity sums signal weights per genre and scales them to [-1, 1].
func genreAffinity(signals []Signal, genres map[titleKey][]int) map[int]float64 {
	sum := map[int]float64{}
	for _, s := range signals {
		w := signalWeight(s)
		for _, g := range genres[keyOf(s.MediaType, s.TmdbID)] {
			sum[g] += w
		}
	}
	maxAbs := 0.0
	for _, v := range sum {
		maxAbs = math.Max(maxAbs, math.Abs(v))
	}
	if maxAbs > 0 {
		for g := range sum {
			sum[g] /= maxAbs
		}
	}
	return sum
}

type relatedLists struct{ recs, similar []Item }

type candidate struct {
	item    Item
	base    float64 // support from positive seeds
	penalty float64 // support from disliked titles
	score   float64
	bySeed  map[titleKey]float64
	extra   float64 // genre affinity + prior, shared by every row
}

func positionWeight(i int) float64 { return 1 / (1 + positionDecay*float64(i)) }

// scoreInput is everything score needs; the service fills it from TMDB.
type scoreInput struct {
	signals  []Signal
	seeds    []seed
	dislikes []seed
	related  map[titleKey]relatedLists
	genres   map[titleKey][]int
}

// score builds the ranked candidate pool. Rated titles, adult titles, titles without a poster
// and titles whose dislike support is at least as strong as their positive support are dropped.
func score(in scoreInput) []*candidate {
	rated := map[titleKey]bool{}
	for _, s := range in.signals {
		rated[keyOf(s.MediaType, s.TmdbID)] = true
	}
	affinity := genreAffinity(genreTitles(in.signals), in.genres)

	pool := map[titleKey]*candidate{}
	visit := func(sd seed, positive bool) {
		sk := keyOf(sd.MediaType, sd.TmdbID)
		lists := in.related[sk]
		for _, l := range []struct {
			items []Item
			w     float64
		}{{lists.recs, listWeightRecs}, {lists.similar, listWeightSimilar}} {
			for i, it := range l.items {
				k := keyOf(it.MediaType, it.ID)
				if rated[k] || it.Adult || it.PosterPath == "" || it.VoteCount < minVotes {
					continue
				}
				c := pool[k]
				if c == nil {
					c = &candidate{item: it, bySeed: map[titleKey]float64{}}
					pool[k] = c
				}
				v := sd.weight * l.w * positionWeight(i)
				if positive {
					c.base += v
					c.bySeed[sk] += v
				} else {
					c.penalty += v
				}
			}
		}
	}
	for _, sd := range in.seeds {
		visit(sd, true)
	}
	for _, sd := range in.dislikes {
		visit(sd, false)
	}

	out := make([]*candidate, 0, len(pool))
	for _, c := range pool {
		if c.base <= 0 || c.penalty >= c.base {
			continue
		}
		c.extra = genreWeight*itemAffinity(c.item, affinity) + prior(c.item)
		c.score = c.base - c.penalty + c.extra
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].item.ID < out[j].item.ID
	})
	return out
}

func itemAffinity(it Item, affinity map[int]float64) float64 {
	if len(it.GenreIDs) == 0 {
		return 0
	}
	sum := 0.0
	for _, g := range it.GenreIDs {
		sum += affinity[g]
	}
	return sum / float64(len(it.GenreIDs))
}

// prior is a small quality/popularity nudge (well under one seed's support).
func prior(it Item) float64 {
	trust := math.Min(1, float64(it.VoteCount)/200)
	return voteWeight*(it.VoteAverage/10)*trust + popularityScale*math.Log10(1+math.Max(0, it.Popularity))
}

// buildRows turns the ranked pool into "Top Picks" plus up to three "Because you ..." rows.
func buildRows(cands []*candidate, seeds []seed) []Row {
	if len(cands) == 0 {
		return nil
	}
	used := map[titleKey]bool{}
	top := make([]Item, 0, topPicksSize)
	for _, c := range cands {
		if len(top) == topPicksSize {
			break
		}
		top = append(top, c.item)
		used[keyOf(c.item.MediaType, c.item.ID)] = true
	}
	rows := []Row{{ID: "top-picks", Title: "Top Picks for You", Reason: ReasonTopPicks, Items: top}}

	for _, sd := range seeds {
		if len(rows) > becauseRows {
			break
		}
		if sd.Title == "" {
			continue
		}
		sk := keyOf(sd.MediaType, sd.TmdbID)
		var mine []*candidate
		for _, c := range cands {
			if c.bySeed[sk] > 0 {
				mine = append(mine, c)
			}
		}
		sort.SliceStable(mine, func(i, j int) bool {
			return mine[i].bySeed[sk]+mine[i].extra > mine[j].bySeed[sk]+mine[j].extra
		})

		// Prefer titles not shown in earlier rows; reuse shown ones only to reach the minimum size
		pick := map[titleKey]bool{}
		for _, c := range mine {
			k := keyOf(c.item.MediaType, c.item.ID)
			if !used[k] && len(pick) < becauseMaxSize {
				pick[k] = true
			}
		}
		for _, c := range mine {
			if len(pick) >= becauseMinSize {
				break
			}
			pick[keyOf(c.item.MediaType, c.item.ID)] = true
		}
		if len(pick) < becauseMinSize {
			continue
		}
		items := make([]Item, 0, len(pick))
		for _, c := range mine {
			k := keyOf(c.item.MediaType, c.item.ID)
			if pick[k] {
				items = append(items, c.item)
				used[k] = true
			}
		}
		title, reason := becauseTitle(sd)
		rows = append(rows, Row{
			ID:     "because-" + sd.MediaType + "-" + strconv.Itoa(sd.TmdbID),
			Title:  title,
			Reason: reason,
			Items:  items,
		})
	}
	return rows
}

func becauseTitle(sd seed) (string, string) {
	switch sd.Rating {
	case "love":
		return "Because you loved " + sd.Title, ReasonBecauseLoved
	case "like":
		return "Because you liked " + sd.Title, ReasonBecauseLiked
	}
	return "Because you added " + sd.Title + " to Favorites", ReasonBecauseFavored
}

// trendingRows is the fallback for profiles without positive signals.
func trendingRows(items []Item, signals []Signal) []Row {
	rated := map[titleKey]bool{}
	for _, s := range signals {
		rated[keyOf(s.MediaType, s.TmdbID)] = true
	}
	out := make([]Item, 0, topPicksSize)
	for _, it := range items {
		if len(out) == topPicksSize {
			break
		}
		if it.Adult || it.PosterPath == "" || rated[keyOf(it.MediaType, it.ID)] {
			continue
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil
	}
	return []Row{{ID: "trending", Title: "Trending Now", Reason: ReasonPopular, Items: out}}
}
