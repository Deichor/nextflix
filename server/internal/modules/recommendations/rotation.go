package recommendations

import (
	"hash/fnv"
	"log"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Rows change between visits, not while one is on screen: the same window gives the same
// rows, a title shown in a few windows sinks and comes back after a while. A title counts as
// shown when a client got it among the first cards of a row (no client change needed; it is
// not proof that someone looked at it). See docs/ARCHITECTURE.md "Recommendations".
const (
	rotationWindow = 6 * time.Hour
	shownPerRow    = 8 // cards a row shows before scrolling

	fatigueFree     = 2.0  // showings that cost nothing: it sinks after a few times, not after one
	fatigueStep     = 0.6  // then score x 1/(1 + 0.6 x further showings): 5 times is about a third
	fatigueHalfDays = 14.0 // the count halves in this many days without being shown
	fixedSeeds      = 4    // strongest titles always feed the pool, so Top Picks keeps a core
	jitterShare     = 0.25 // every window moves a score within +-12.5 %
	sameSeedDecay   = 0.85 // each Top Picks title from one seed lowers the next one's score
	exposureKeep    = 120 * 24 * time.Hour
)

// ExposureRow is how often a profile's rows showed a title.
type ExposureRow struct {
	ProfileID   uint    `gorm:"primaryKey;autoIncrement:false"`
	MediaType   string  `gorm:"primaryKey"`
	TmdbID      int     `gorm:"primaryKey;autoIncrement:false"`
	Shown       float64 // decayed count at LastShownAt, see fatigueHalfDays
	LastWindow  int64
	LastShownAt time.Time `gorm:"index"`
}

func (ExposureRow) TableName() string { return "recommendation_exposures" }

// Models returns the module's tables for AutoMigrate.
func Models() []any { return []any{&ExposureRow{}} }

// ExposureStore keeps what the rows showed.
type ExposureStore interface {
	Exposures(profileID uint) ([]ExposureRow, error)
	SaveExposures(rows []ExposureRow, olderThan time.Time) error
}

type gormExposures struct{ db *gorm.DB }

// NewExposureStore keeps exposures in the database.
func NewExposureStore(db *gorm.DB) ExposureStore { return gormExposures{db} }

func (g gormExposures) Exposures(profileID uint) ([]ExposureRow, error) {
	var rows []ExposureRow
	err := g.db.Where("profile_id = ?", profileID).Find(&rows).Error
	return rows, err
}

func (g gormExposures) SaveExposures(rows []ExposureRow, olderThan time.Time) error {
	return g.db.Transaction(func(tx *gorm.DB) error {
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&rows).Error; err != nil {
				return err
			}
		}
		return tx.Where("last_shown_at < ?", olderThan).Delete(&ExposureRow{}).Error
	})
}

// EnableRotation turns on rotating seeds and fatigue. Without it the rows are a plain ranking.
func (s *Service) EnableRotation(store ExposureStore) { s.exposures = store }

func windowOf(t time.Time) int64 { return t.Unix() / int64(rotationWindow/time.Second) }

// decayedShown is the count of a row at now.
func decayedShown(r ExposureRow, now time.Time) float64 {
	days := math.Max(0, now.Sub(r.LastShownAt).Hours()/24)
	return r.Shown * math.Pow(0.5, days/fatigueHalfDays)
}

// rotation is what one window of one profile changes in the ranking.
type rotation struct {
	seed  uint64
	shown map[titleKey]float64
}

func (s *Service) newRotation(profileID uint, now time.Time) *rotation {
	window := windowOf(now)
	h := fnv.New64a()
	var b [16]byte
	for i := range 8 {
		b[i] = byte(uint64(profileID) >> (8 * i))
		b[8+i] = byte(uint64(window) >> (8 * i))
	}
	h.Write(b[:])
	r := &rotation{seed: h.Sum64(), shown: map[titleKey]float64{}}
	rows, err := s.exposures.Exposures(profileID)
	if err != nil {
		log.Printf("recommendations: exposures of profile %d: %v", profileID, err)
		return r
	}
	for _, row := range rows {
		// What this window showed does not move its own rows
		if row.LastWindow == window {
			row.Shown = math.Max(0, row.Shown-1)
		}
		r.shown[keyOf(row.MediaType, row.TmdbID)] = decayedShown(row, now)
	}
	return r
}

// mult scales a title's score: fatigue plus a small change per window. It depends only on the
// window and the title, so a new computation in the window gives the same rows.
func (r *rotation) mult(k titleKey) float64 {
	h := fnv.New64a()
	var b [16]byte
	for i := range 8 {
		b[i] = byte(r.seed >> (8 * i))
		b[8+i] = byte(uint64(k.id) >> (8 * i))
	}
	h.Write(b[:])
	h.Write([]byte(k.mediaType))
	u := float64(h.Sum64()>>11) / (1 << 53)
	return (1 - jitterShare/2 + jitterShare*u) / (1 + fatigueStep*math.Max(0, r.shown[k]-fatigueFree))
}

// pickSeeds keeps the fixedSeeds strongest and draws the rest up to n, stronger ones more often
// (weighted sampling without replacement), so every positive title gets its turn. The result is
// in the order of the draw, which decides the "Because you ..." rows. pos must be strongest first.
func (r *rotation) pickSeeds(pos []seed, n int) []seed {
	if len(pos) == 0 {
		return nil
	}
	rnd := rand.New(rand.NewPCG(r.seed, 0x9e3779b97f4a7c15))
	type drawn struct {
		sd  seed
		key float64
	}
	all := make([]drawn, 0, len(pos))
	for _, sd := range pos {
		all = append(all, drawn{sd, math.Pow(rnd.Float64(), 1/sd.weight)})
	}
	fixed := min(fixedSeeds, n, len(all))
	others := all[fixed:]
	sort.SliceStable(others, func(i, j int) bool { return others[i].key > others[j].key })
	chosen := all[:min(n, len(all))]
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].key > chosen[j].key })
	out := make([]seed, 0, len(chosen))
	for _, d := range chosen {
		out = append(out, d.sd)
	}
	return out
}

// rotateItems orders a list that has no scores (the trending fallback) by fatigue and window.
func (r *rotation) rotateItems(items []Item) []Item {
	out := append([]Item(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		return r.mult(keyOf(out[i].MediaType, out[i].ID)) > r.mult(keyOf(out[j].MediaType, out[j].ID))
	})
	return out
}

// scaled applies a multiplier so that it also lowers a negative score.
func scaled(v, m float64) float64 {
	if v < 0 {
		return v / m
	}
	return v * m
}

// bestSeed is the seed a candidate owes most to.
func (c *candidate) bestSeed() titleKey {
	var best titleKey
	top := -1.0
	for k, v := range c.bySeed {
		if v > top || (v == top && (k.id < best.id || (k.id == best.id && k.mediaType < best.mediaType))) {
			best, top = k, v
		}
	}
	return best
}

// diversify moves a mix of seeds to the front (the Top Picks part of cands): one loved film must
// not fill the row with its neighbours.
func diversify(cands []*candidate, n int) []*candidate {
	if len(cands) <= 1 {
		return cands
	}
	rest := append([]*candidate(nil), cands...)
	front := make([]*candidate, 0, len(cands))
	count := map[titleKey]int{}
	for len(front) < n && len(rest) > 0 {
		at, top := 0, math.Inf(-1)
		for i, c := range rest {
			v := scaled(c.score, math.Pow(sameSeedDecay, float64(count[c.bestSeed()])))
			if v > top {
				at, top = i, v
			}
		}
		count[rest[at].bestSeed()]++
		front = append(front, rest[at])
		rest = append(rest[:at], rest[at+1:]...)
	}
	return append(front, rest...)
}

// shownOf lists the titles a client sees without scrolling.
func shownOf(resp *Response) []titleKey {
	seen := map[titleKey]bool{}
	var out []titleKey
	for _, row := range resp.Rows {
		for i, it := range row.Items {
			if i == shownPerRow {
				break
			}
			if k := keyOf(it.MediaType, it.ID); !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// NoteShown counts the rows a client got, once per title and window.
func (s *Service) NoteShown(profileID uint, resp *Response) {
	if s.exposures == nil || resp == nil {
		return
	}
	now := s.now()
	window := windowOf(now)
	s.mu.Lock()
	last := s.noted[profileID]
	if last.resp == resp && last.window == window {
		s.mu.Unlock()
		return
	}
	if s.noted == nil {
		s.noted = map[uint]notedRows{}
	}
	s.noted[profileID] = notedRows{resp, window}
	s.mu.Unlock()

	rows, err := s.exposures.Exposures(profileID)
	if err != nil {
		log.Printf("recommendations: exposures of profile %d: %v", profileID, err)
		return
	}
	known := make(map[titleKey]ExposureRow, len(rows))
	for _, r := range rows {
		known[keyOf(r.MediaType, r.TmdbID)] = r
	}
	var changed []ExposureRow
	for _, k := range shownOf(resp) {
		r, ok := known[k]
		if ok && r.LastWindow == window {
			continue
		}
		shown := 1.0
		if ok {
			shown += decayedShown(r, now)
		}
		changed = append(changed, ExposureRow{ProfileID: profileID, MediaType: k.mediaType, TmdbID: k.id,
			Shown: shown, LastWindow: window, LastShownAt: now})
	}
	if err := s.exposures.SaveExposures(changed, now.Add(-exposureKeep)); err != nil {
		log.Printf("recommendations: saving exposures of profile %d: %v", profileID, err)
	}
}

// Taste from watching. A movie finished counts like a like. A show counts more with every
// episode finished, up to a love: binging it says more than a click. Under 10 minutes of a
// title says nothing, but it still leaves the recommendations.
const (
	watchedMovie      = weightLike
	watchedStarted    = 0.4
	watchedEpisodeMin = 0.5
	watchedEpisode    = 0.25
	watchedMinSeconds = 600
)

func watchWeight(w Watched) float64 {
	switch {
	case w.MediaType == "movie" && w.Finished:
		return watchedMovie
	case w.MediaType != "movie" && w.FinishedEpisodes > 0:
		return math.Min(weightLove, watchedEpisodeMin+watchedEpisode*float64(w.FinishedEpisodes))
	case w.WatchedSeconds >= watchedMinSeconds:
		return watchedStarted
	}
	return 0
}

// withHistory adds the watch history to the signals. Every watched title stays out of the
// candidates; one that was also rated keeps its rating unless watching says more.
func withHistory(signals []Signal, history []Watched) []Signal {
	out := append([]Signal(nil), signals...)
	at := make(map[titleKey]int, len(out))
	for i, sig := range out {
		at[keyOf(sig.MediaType, sig.TmdbID)] = i
	}
	for _, w := range history {
		weight := watchWeight(w)
		if i, ok := at[keyOf(w.MediaType, w.TmdbID)]; ok {
			out[i].Watched = weight
			continue
		}
		out = append(out, Signal{MediaType: w.MediaType, TmdbID: w.TmdbID, Title: w.Title, Watched: weight, UpdatedAt: w.UpdatedAt})
	}
	return out
}
