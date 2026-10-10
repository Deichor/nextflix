package recommendations

import (
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tuning of the surprise pool; see docs/ARCHITECTURE.md "Surprise me".
const (
	surpriseSeeds    = 20 // positive titles whose related lists are fetched
	surpriseDislikes = 5
	surpriseTaste    = 40 // most recent titles whose genres make the genre affinity
	surpriseMinVotes = 50
	surpriseFetchers = 8

	tasteLove     = 3.0
	tasteLike     = 2.0
	tasteFavorite = 1.2 // favorite without a rating
	tasteFavBonus = 0.5 // favorite on top of love / like
	tasteDislike  = -3.0
	tasteFinished = 1.0
	tasteStarted  = 0.4

	comboGenres       = 3 // the strongest genres, paired for "Because you like drama and crime"
	adjacentGenres    = 4
	wildGenres        = 4   // never-watched genres asked per pool
	tasteMinVote      = 6.5 // related and genre lists also hold weak titles
	adjacentMinVote   = 7.0
	adjacentMinVotes  = 200
	wildMinVote       = 7.2
	wildMinVotesMovie = 500
	wildMinVotesTV    = 300
	wildClassicYear   = 1975
	dislikedGenre     = -0.35 // titles whose genres average below this are dropped
)

// TMDB genre ids per media type.
var (
	movieGenreIDs = map[int]bool{28: true, 12: true, 16: true, 35: true, 80: true, 99: true, 18: true,
		10751: true, 14: true, 36: true, 27: true, 10402: true, 9648: true, 10749: true, 878: true, 53: true,
		10752: true, 37: true}
	tvGenreIDs = map[int]bool{10759: true, 16: true, 35: true, 80: true, 99: true, 18: true, 10751: true,
		10762: true, 10763: true, 9648: true, 10764: true, 10765: true, 10766: true, 10767: true, 10768: true, 37: true}
)

// Wild cards: acclaimed titles in these original languages (tv: also series) ...
var wildLanguages = []struct {
	lang string
	tv   bool
}{{"ko", true}, {"ja", false}, {"es", true}, {"fr", false}, {"it", false}, {"tr", true}, {"hi", false}, {"de", false}}

// ... and in these genres when the profile never watched them (horror is left out on purpose).
var wildGenreIDs = []int{99, 16, 37, 10402, 36, 10752, 35, 10749, 14, 878, 9648, 53, 12, 10751, 28, 80, 18}

// canonGenre maps a TMDB TV genre onto the movie genre it mostly stands for.
func canonGenre(g int) int {
	switch g {
	case 10759:
		return 28
	case 10765:
		return 878
	case 10768:
		return 10752
	case 10762:
		return 10751
	}
	return g
}

func canonGenres(ids []int) []int {
	out := make([]int, 0, len(ids))
	for _, g := range ids {
		c := canonGenre(g)
		dup := false
		for _, o := range out {
			dup = dup || o == c
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// tvGenre is the TV genre of a canonical genre, 0 when TV has none.
func tvGenre(g int) int {
	switch g {
	case 28, 12:
		return 10759
	case 878, 14:
		return 10765
	case 10752:
		return 10768
	}
	if tvGenreIDs[g] {
		return g
	}
	return 0
}

func movieGenres(gs []int) []int {
	for _, g := range gs {
		if !movieGenreIDs[g] {
			return nil
		}
	}
	return gs
}

func tvGenres(gs []int) []int {
	var out []int
	for _, g := range gs {
		t := tvGenre(g)
		if t == 0 {
			return nil
		}
		dup := false
		for _, o := range out {
			dup = dup || o == t
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out
}

func primaryGenre(it Item) int {
	if len(it.GenreIDs) == 0 {
		return 0
	}
	return canonGenre(it.GenreIDs[0])
}

func yearOfItem(it Item) int {
	d := it.ReleaseDate
	if d == "" {
		d = it.FirstAirDate
	}
	if len(d) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(d[:4])
	return y
}

// canonAffinity is the average affinity of a title's (canonical) genres.
func canonAffinity(it Item, affinity map[int]float64) float64 {
	gs := canonGenres(it.GenreIDs)
	if len(gs) == 0 {
		return 0
	}
	sum := 0.0
	for _, g := range gs {
		sum += affinity[g]
	}
	return sum / float64(len(gs))
}

// tasteSeed is a title that says something about the profile's taste.
type tasteSeed struct {
	key    titleKey
	title  string
	kind   string  // reason kind of titles found through it
	weight float64 // signed, with recency
	at     time.Time
}

// tasteSeeds turns ratings and watch history into weighted titles (love > like > favorite >
// finished > started; dislikes negative). A rated title ignores its history.
func tasteSeeds(signals []Signal, history []Watched, now time.Time) []tasteSeed {
	var out []tasteSeed
	rated := map[titleKey]bool{}
	for _, s := range signals {
		k := keyOf(s.MediaType, s.TmdbID)
		rated[k] = true
		var w float64
		var kind string
		switch s.Rating {
		case "love":
			w, kind = tasteLove, SurpriseBecauseLoved
		case "like":
			w, kind = tasteLike, SurpriseBecauseLiked
		case "dislike":
			w = tasteDislike
		default:
			if s.Favorite {
				w, kind = tasteFavorite, SurpriseFavoriteLike
			}
		}
		if s.Favorite && (s.Rating == "love" || s.Rating == "like") {
			w += tasteFavBonus
		}
		if w == 0 {
			continue
		}
		out = append(out, tasteSeed{key: k, title: s.Title, kind: kind, weight: w * recency(s.UpdatedAt, now), at: s.UpdatedAt})
	}
	for _, h := range history {
		k := keyOf(h.MediaType, h.TmdbID)
		if rated[k] {
			continue
		}
		w := tasteStarted
		if h.Finished {
			w = tasteFinished
		}
		out = append(out, tasteSeed{key: k, title: h.Title, kind: SurpriseBecauseWatched, weight: w * recency(h.UpdatedAt, now), at: h.UpdatedAt})
	}
	return out
}

// tasteAffinity sums seed weights per genre and scales them to [-1, 1].
func tasteAffinity(taste []tasteSeed, genres map[titleKey][]int) map[int]float64 {
	sum := map[int]float64{}
	for _, sd := range taste {
		for _, g := range genres[sd.key] {
			sum[g] += sd.weight
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

// genreStrength weighs the genres of all positive titles: each title adds at most 1 (love and
// like 1, favorite 0.6, finished 0.5, started 0.2) and the sum grows logarithmically, so many
// liked titles beat one loved title. Genres with a negative affinity get nothing.
func genreStrength(pos []tasteSeed, genres map[titleKey][]int, affinity map[int]float64) map[int]float64 {
	sum := map[int]float64{}
	for _, sd := range pos {
		c := math.Min(1, sd.weight/tasteLike)
		for _, g := range genres[sd.key] {
			sum[g] += c
		}
	}
	out := map[int]float64{}
	for g, v := range sum {
		if affinity[g] > 0 {
			out[g] = math.Log1p(v)
		}
	}
	return out
}

// topGenres returns up to n genres by strength, strongest first.
func topGenres(strength map[int]float64, n int) []int {
	out := make([]int, 0, len(strength))
	for g := range strength {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if strength[out[i]] != strength[out[j]] {
			return strength[out[i]] > strength[out[j]]
		}
		return out[i] < out[j]
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// groupPlan is a group still without titles plus the discover queries that fill it.
type groupPlan struct {
	group   *surpriseGroup
	queries []ExploreQuery
}

// planSurprise lists the discover groups: genre pairs (taste blend), liked genres across eras
// (adjacent) and acclaimed foreign, never-watched-genre and classic titles (wild).
func (s *Service) planSurprise(strength, affinity map[int]float64) []groupPlan {
	var plans []groupPlan
	maxS := 0.0
	for _, v := range strength {
		maxS = math.Max(maxS, v)
	}
	idList := func(gs []int) string {
		parts := make([]string, len(gs))
		for i, g := range gs {
			parts[i] = strconv.Itoa(g)
		}
		return strings.Join(parts, "-")
	}

	top := topGenres(strength, comboGenres)
	var pairs [][]int
	if len(top) == 1 {
		pairs = [][]int{{top[0]}}
	}
	for i := 0; i < len(top); i++ {
		for j := i + 1; j < len(top); j++ {
			pairs = append(pairs, []int{top[i], top[j]})
		}
	}
	for _, p := range pairs {
		sum := 0.0
		for _, g := range p {
			sum += strength[g]
		}
		var qs []ExploreQuery
		if mv := movieGenres(p); mv != nil {
			qs = append(qs,
				ExploreQuery{MediaType: "movie", Genres: mv, Sort: "popularity.desc", MinVotes: 300},
				ExploreQuery{MediaType: "movie", Genres: mv, Sort: "vote_average.desc", MinVotes: 1000})
		}
		if tv := tvGenres(p); tv != nil {
			qs = append(qs, ExploreQuery{MediaType: "tv", Genres: tv, Sort: "popularity.desc", MinVotes: 200})
		}
		plans = append(plans, groupPlan{&surpriseGroup{
			id: "genres:" + idList(p), bucket: BucketTaste, weight: tasteLike * sum / (float64(len(p)) * maxS),
			reason: Reason{Kind: SurpriseGenre, GenreID: p[0], GenreIDs: p},
		}, qs})
	}

	for _, g := range topGenres(strength, adjacentGenres) {
		var qs []ExploreQuery
		if movieGenreIDs[g] {
			qs = append(qs,
				ExploreQuery{MediaType: "movie", Genres: []int{g}, ToYear: 1999, MinVotes: 300},
				ExploreQuery{MediaType: "movie", Genres: []int{g}, FromYear: 2000, MinVotes: 300})
		}
		if t := tvGenre(g); t != 0 {
			qs = append(qs, ExploreQuery{MediaType: "tv", Genres: []int{t}, MinVotes: adjacentMinVotes})
		}
		plans = append(plans, groupPlan{&surpriseGroup{
			id: "adjacent:" + strconv.Itoa(g), bucket: BucketAdjacent, weight: strength[g] / maxS,
			reason: Reason{Kind: SurpriseAdjacent, GenreID: g},
		}, qs})
	}

	wild := func(id string, qs ...ExploreQuery) {
		plans = append(plans, groupPlan{&surpriseGroup{id: "wild:" + id, bucket: BucketWild, weight: 1, reason: Reason{Kind: SurpriseWild}}, qs})
	}
	for _, l := range wildLanguages {
		qs := []ExploreQuery{{MediaType: "movie", Language: l.lang, MinVotes: wildMinVotesMovie}}
		if l.lang == "ja" {
			qs[0].WithoutGenres = []int{16} // anime has its own group
		}
		if l.tv {
			qs = append(qs, ExploreQuery{MediaType: "tv", Language: l.lang, MinVotes: wildMinVotesTV})
		}
		wild("lang-"+l.lang, qs...)
	}
	wild("anime",
		ExploreQuery{MediaType: "movie", Language: "ja", Genres: []int{16}, MinVotes: wildMinVotesMovie},
		ExploreQuery{MediaType: "tv", Language: "ja", Genres: []int{16}, MinVotes: wildMinVotesTV})
	var never []int
	for _, g := range wildGenreIDs {
		if strength[g] == 0 && affinity[g] >= 0 {
			never = append(never, g)
		}
	}
	for i := len(never) - 1; i > 0; i-- {
		j := int(s.random() * float64(i+1))
		never[i], never[j] = never[j], never[i]
	}
	if len(never) > wildGenres {
		never = never[:wildGenres]
	}
	for _, g := range never {
		qs := []ExploreQuery{{MediaType: "movie", Genres: []int{g}, MinVotes: 1000, Sort: "vote_count.desc"}}
		if t := tvGenre(g); t != 0 {
			qs = append(qs, ExploreQuery{MediaType: "tv", Genres: []int{t}, MinVotes: wildMinVotesTV, Sort: "vote_count.desc"})
		}
		wild("genre-"+strconv.Itoa(g), qs...)
	}
	wild("classic", ExploreQuery{MediaType: "movie", ToYear: wildClassicYear - 1, MinVotes: wildMinVotesMovie})
	return plans
}

// buildSurprisePool fetches everything the groups need and fills them.
func (s *Service) buildSurprisePool(lang string, signals []Signal, history []Watched) surprisePool {
	seeds := tasteSeeds(signals, history, s.now())
	var pos, neg []tasteSeed
	for _, sd := range seeds {
		if sd.weight > 0 {
			pos = append(pos, sd)
		} else {
			neg = append(neg, sd)
		}
	}
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].weight > pos[j].weight })
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].at.After(neg[j].at) })
	taste := append([]tasteSeed{}, seeds...)
	sort.SliceStable(taste, func(i, j int) bool { return taste[i].at.After(taste[j].at) })
	if len(taste) > surpriseTaste {
		taste = taste[:surpriseTaste]
	}
	fetched := pos
	if len(fetched) > surpriseSeeds {
		fetched = fetched[:surpriseSeeds]
	}
	if len(neg) > surpriseDislikes {
		neg = neg[:surpriseDislikes]
	}

	rated := map[titleKey]bool{}
	for _, sig := range signals {
		rated[keyOf(sig.MediaType, sig.TmdbID)] = true
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, surpriseFetchers)
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		}()
	}

	related := map[titleKey]relatedLists{}
	genres := map[titleKey][]int{}
	for _, sd := range append(append([]tasteSeed{}, fetched...), neg...) {
		k := sd.key
		run(func() {
			recs, similar, err := s.catalog.Related(lang, k.mediaType, k.id)
			if err != nil {
				log.Printf("recommendations: surprise related %s %d: %v", k.mediaType, k.id, err)
			}
			mu.Lock()
			related[k] = relatedLists{recs, similar}
			mu.Unlock()
		})
	}
	asked := map[titleKey]bool{}
	for _, sd := range append(append([]tasteSeed{}, taste...), fetched...) {
		k := sd.key
		if asked[k] {
			continue
		}
		asked[k] = true
		run(func() {
			g, err := s.catalog.Genres(k.mediaType, k.id)
			if err != nil {
				return
			}
			mu.Lock()
			genres[k] = canonGenres(g)
			mu.Unlock()
		})
	}
	var trending []Item
	if len(pos) == 0 {
		run(func() {
			t, err := s.catalog.Trending(lang)
			if err != nil {
				log.Printf("recommendations: surprise trending: %v", err)
			}
			mu.Lock()
			trending = t
			mu.Unlock()
		})
	}
	wg.Wait()

	affinity := tasteAffinity(taste, genres)
	strength := genreStrength(pos, genres, affinity)
	plans := s.planSurprise(strength, affinity)
	results := make([][][]Item, len(plans))
	for i, p := range plans {
		results[i] = make([][]Item, len(p.queries))
		for j, q := range p.queries {
			run(func() {
				items, err := s.catalog.Explore(lang, q)
				if err != nil {
					log.Printf("recommendations: surprise discover %+v: %v", q, err)
					return
				}
				mu.Lock()
				results[i][j] = items
				mu.Unlock()
			})
		}
	}
	wg.Wait()

	return surprisePool{groups: surpriseGroups(surpriseInput{
		pos: fetched, neg: neg, related: related, affinity: affinity, strength: strength,
		plans: plans, results: results, trending: trending, rated: rated,
	})}
}

type surpriseInput struct {
	pos, neg []tasteSeed
	related  map[titleKey]relatedLists
	affinity map[int]float64
	strength map[int]float64
	plans    []groupPlan
	results  [][][]Item // per plan, per query
	trending []Item
	rated    map[titleKey]bool
}

// surpriseGroups fills the groups. Every title needs artwork, 50 votes and no disliked genre,
// and is dropped when the dislikes point to it more than the liked titles; taste titles need
// a 6.5 average. Adjacent and wild titles must not be in any seed's related lists; wild ones
// also need the quality floor, no genre with a negative affinity and something new: a language
// other than English, a never-watched main genre or an old year.
func surpriseGroups(in surpriseInput) []*surpriseGroup {
	support := map[titleKey]float64{}
	penalty := map[titleKey]float64{}
	near := map[titleKey]bool{}
	walk := func(k titleKey, fn func(it Item, w float64)) {
		l := in.related[k]
		for _, list := range []struct {
			items []Item
			w     float64
		}{{l.recs, listWeightRecs}, {l.similar, listWeightSimilar}} {
			for i, it := range list.items {
				near[keyOf(it.MediaType, it.ID)] = true
				fn(it, list.w*positionWeight(i))
			}
		}
	}
	for _, sd := range in.pos {
		walk(sd.key, func(it Item, w float64) { support[keyOf(it.MediaType, it.ID)] += sd.weight * w })
	}
	for _, sd := range in.neg {
		walk(sd.key, func(it Item, w float64) { penalty[keyOf(it.MediaType, it.ID)] += -sd.weight * w })
	}

	usable := func(it Item) bool {
		k := keyOf(it.MediaType, it.ID)
		return !in.rated[k] && !it.Adult && it.BackdropPath != "" && it.PosterPath != "" &&
			it.VoteCount >= surpriseMinVotes && (penalty[k] == 0 || penalty[k] < support[k]) && canonAffinity(it, in.affinity) >= dislikedGenre
	}
	novel := func(it Item) bool {
		if it.OriginalLanguage != "" && it.OriginalLanguage != "en" {
			return true
		}
		if y := yearOfItem(it); y > 0 && y < wildClassicYear {
			return true
		}
		g := primaryGenre(it)
		return g != 0 && in.strength[g] == 0
	}
	hated := func(it Item) bool {
		for _, g := range canonGenres(it.GenreIDs) {
			if in.affinity[g] < 0 {
				return true
			}
		}
		return false
	}
	quality := func(it Item) float64 { return it.VoteAverage + 0.5*math.Log10(1+float64(it.VoteCount)) }

	var out []*surpriseGroup
	finish := func(g *surpriseGroup, byKey map[titleKey]*surpriseCand) {
		if len(byKey) == 0 {
			return
		}
		for _, c := range byKey {
			g.cands = append(g.cands, c)
		}
		sort.Slice(g.cands, func(i, j int) bool {
			a, b := g.cands[i], g.cands[j]
			if a.score != b.score {
				return a.score > b.score
			}
			if a.item.MediaType != b.item.MediaType {
				return a.item.MediaType < b.item.MediaType
			}
			return a.item.ID < b.item.ID
		})
		out = append(out, g)
	}
	keep := func(byKey map[titleKey]*surpriseCand, it Item, score float64, genre int) {
		k := keyOf(it.MediaType, it.ID)
		if c := byKey[k]; c != nil {
			if c.score < score {
				c.score = score
			}
			return
		}
		byKey[k] = &surpriseCand{item: it, score: score, genre: genre}
	}

	// Taste blend: the related titles of every positive title, one group per title
	for _, sd := range in.pos {
		byKey := map[titleKey]*surpriseCand{}
		k := sd.key
		walk(k, func(it Item, w float64) {
			if usable(it) && it.VoteAverage >= tasteMinVote {
				keep(byKey, it, 2*w+0.5*genreWeight*canonAffinity(it, in.affinity)+prior(it), primaryGenre(it))
			}
		})
		finish(&surpriseGroup{
			id: "seed:" + k.mediaType + "-" + strconv.Itoa(k.id), bucket: BucketTaste, weight: sd.weight,
			reason: Reason{Kind: sd.kind, Title: sd.title, MediaType: k.mediaType, TmdbID: k.id},
		}, byKey)
	}

	for i, p := range in.plans {
		g := *p.group
		byKey := map[titleKey]*surpriseCand{}
		for _, items := range in.results[i] {
			for pos, it := range items {
				if !usable(it) {
					continue
				}
				k := keyOf(it.MediaType, it.ID)
				switch g.bucket {
				case BucketTaste:
					if it.VoteAverage >= tasteMinVote {
						keep(byKey, it, positionWeight(pos)+0.5*genreWeight*canonAffinity(it, in.affinity)+prior(it), primaryGenre(it))
					}
				case BucketAdjacent:
					if !near[k] && it.VoteAverage >= adjacentMinVote && it.VoteCount >= adjacentMinVotes {
						keep(byKey, it, quality(it), g.reason.GenreID)
					}
				case BucketWild:
					floor := wildMinVotesMovie
					if it.MediaType == "tv" {
						floor = wildMinVotesTV
					}
					if !near[k] && it.VoteAverage >= wildMinVote && it.VoteCount >= floor && novel(it) && !hated(it) {
						keep(byKey, it, quality(it), primaryGenre(it))
					}
				}
			}
		}
		finish(&g, byKey)
	}

	if len(in.pos) == 0 {
		byKey := map[titleKey]*surpriseCand{}
		for i, it := range in.trending {
			if usable(it) {
				keep(byKey, it, positionWeight(i)+prior(it), primaryGenre(it))
			}
		}
		finish(&surpriseGroup{id: "trending", bucket: BucketTaste, weight: 1, reason: Reason{Kind: SurpriseTrending}}, byKey)
	}
	return out
}
