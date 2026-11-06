package recommendations

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

type memExposures struct{ rows map[string]ExposureRow }

func newMemExposures() *memExposures { return &memExposures{rows: map[string]ExposureRow{}} }

func (m *memExposures) Exposures(uint) ([]ExposureRow, error) {
	out := make([]ExposureRow, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

func (m *memExposures) SaveExposures(rows []ExposureRow, _ time.Time) error {
	for _, r := range rows {
		m.rows[fmt.Sprintf("%s:%d", r.MediaType, r.TmdbID)] = r
	}
	return nil
}

// rotationFixture: 14 loved or liked films, each with its own 40 related titles.
func rotationFixture(start time.Time) (*Service, *memExposures, []Signal) {
	var signals []Signal
	cat := &fakeCatalog{related: map[titleKey]relatedLists{}, genres: map[titleKey][]int{}}
	for i := range 14 {
		id := 9000 + i
		rating := "like"
		if i < 3 {
			rating = "love"
		}
		signals = append(signals, Signal{MediaType: "movie", TmdbID: id, Title: fmt.Sprintf("Seed%d", i), Rating: rating, UpdatedAt: start})
		cat.related[keyOf("movie", id)] = relatedLists{recs: movies(1000*(i+1), 40, 18)}
	}
	s := NewService(RatingsFunc(func(uint) ([]Signal, error) { return signals, nil }), cat)
	store := newMemExposures()
	s.EnableRotation(store)
	return s, store, signals
}

func firstCards(resp *Response) map[int]bool {
	out := map[int]bool{}
	for _, row := range resp.Rows {
		for i, it := range row.Items {
			if i < shownPerRow {
				out[it.ID] = true
			}
		}
	}
	return out
}

func overlap(a, b map[int]bool) float64 {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return float64(n) / float64(max(len(a), 1))
}

func TestRotationSameWithinWindowOtherInTheNext(t *testing.T) {
	start := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
	now := start
	s, _, _ := rotationFixture(start)
	s.now = func() time.Time { return now }

	a, _, err := s.compute(1, "tr")
	if err != nil {
		t.Fatal(err)
	}
	s.NoteShown(1, a)
	// Within the window: what was just counted must not move the rows on screen
	now = start.Add(30 * time.Minute)
	b, _, _ := s.compute(1, "tr")
	if !reflect.DeepEqual(a.Rows, b.Rows) {
		t.Fatal("rows changed within the window")
	}
	now = start.Add(rotationWindow)
	c, _, _ := s.compute(1, "tr")
	if got := overlap(firstCards(a), firstCards(c)); got > 0.8 {
		t.Fatalf("%.0f %% of the visible cards stayed in the next window", got*100)
	}
}

// Two visits a day for two weeks: rows keep changing, every seed gets its turn, and a title
// that was shown a few times does not keep coming back.
func TestRotationOverTwoWeeks(t *testing.T) {
	start := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
	now := start
	s, store, signals := rotationFixture(start)
	s.now = func() time.Time { return now }

	seen := map[int]int{}
	seedRows := map[string]bool{}
	var prev map[int]bool
	worst := 0.0
	const visits = 28
	for v := range visits {
		now = start.Add(time.Duration(v) * 12 * time.Hour)
		resp, _, err := s.compute(1, "tr")
		if err != nil {
			t.Fatal(err)
		}
		s.NoteShown(1, resp)
		cards := firstCards(resp)
		for id := range cards {
			seen[id]++
		}
		if prev != nil {
			worst = max(worst, overlap(cards, prev))
		}
		prev = cards
		for _, row := range resp.Rows {
			seedRows[row.ID] = true
		}
	}
	t.Logf("distinct titles %d, most shown %d of %d visits, worst overlap of two visits %.0f %%, rows %d",
		len(seen), maxOf(seen), visits, worst*100, len(seedRows))
	if worst > 0.8 {
		t.Errorf("two visits in a row shared %.0f %% of their visible cards", worst*100)
	}
	if len(seen) < 100 {
		t.Errorf("only %d distinct titles in two weeks", len(seen))
	}
	if m := maxOf(seen); m > visits/2 {
		t.Errorf("one title was shown in %d of %d visits", m, visits)
	}
	// Every seed got a row at some point (Top Picks plus some of the 14)
	if len(seedRows) < len(signals)/2 {
		t.Errorf("only %d different rows", len(seedRows))
	}
	if len(store.rows) == 0 {
		t.Error("nothing was counted")
	}
}

func maxOf(m map[int]int) int {
	top := 0
	for _, v := range m {
		top = max(top, v)
	}
	return top
}

// A shown title sinks, and it comes back once it was not shown for a while.
func TestFatigueSinksAndRecovers(t *testing.T) {
	now := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
	k := keyOf("movie", 1)
	fresh := &rotation{shown: map[titleKey]float64{}}
	twice := &rotation{shown: map[titleKey]float64{k: 2}}
	if twice.mult(k) != fresh.mult(k) {
		t.Fatal("the first showings already cost something")
	}
	tired := &rotation{shown: map[titleKey]float64{k: 5}}
	if r := tired.mult(k) / fresh.mult(k); r > 0.4 {
		t.Fatalf("5 showings keep %.2f of the score", r)
	}
	row := ExposureRow{Shown: 5, LastShownAt: now}
	if got := decayedShown(row, now.AddDate(0, 0, 28)); got > fatigueFree {
		t.Fatalf("after 4 weeks the count is still %.2f", got)
	}
}

// One seed cannot fill Top Picks, even when it has the best scores.
func TestDiversifyMixesSeeds(t *testing.T) {
	strong, weak := keyOf("movie", 1), keyOf("movie", 2)
	var cands []*candidate
	for i := range 30 {
		cands = append(cands, &candidate{item: movie(100 + i), score: 10 - float64(i)*0.05, bySeed: map[titleKey]float64{strong: 5}})
	}
	for i := range 30 {
		cands = append(cands, &candidate{item: movie(200 + i), score: 8 - float64(i)*0.05, bySeed: map[titleKey]float64{weak: 3}})
	}
	out := diversify(cands, topPicksSize)
	n := 0
	for _, c := range out[:topPicksSize] {
		if c.bySeed[strong] > 0 {
			n++
		}
	}
	if n > 14 || n < 6 {
		t.Fatalf("%d of %d from the strongest seed", n, topPicksSize)
	}
	if len(out) != len(cands) {
		t.Fatalf("lost candidates: %d", len(out))
	}
}

func TestWatchWeight(t *testing.T) {
	for _, c := range []struct {
		name string
		w    Watched
		want float64
	}{
		{"movie finished", Watched{MediaType: "movie", Finished: true, WatchedSeconds: 7000}, weightLike},
		{"movie left after 53 s", Watched{MediaType: "movie", WatchedSeconds: 53}, 0},
		{"movie left after an hour", Watched{MediaType: "movie", WatchedSeconds: 3600}, watchedStarted},
		{"show left after 53 s", Watched{MediaType: "tv", WatchedSeconds: 53}, 0},
		{"show, one episode", Watched{MediaType: "tv", FinishedEpisodes: 1, WatchedSeconds: 2500}, 0.75},
		{"show binged", Watched{MediaType: "tv", FinishedEpisodes: 60, WatchedSeconds: 150000}, weightLove},
	} {
		if got := watchWeight(c.w); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The strongest titles always feed the pool; the others take turns.
func TestPickSeedsKeepsStrongest(t *testing.T) {
	now := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
	var pos []seed
	for i := range 14 {
		pos = append(pos, seed{Signal: Signal{MediaType: "movie", TmdbID: i, Title: "S"}, weight: 14 - float64(i)})
	}
	turns := map[int]bool{}
	for v := range 20 {
		r := &rotation{seed: uint64(windowOf(now)) + uint64(v), shown: map[titleKey]float64{}}
		got := map[int]bool{}
		for _, sd := range r.pickSeeds(pos, maxSeeds) {
			got[sd.TmdbID] = true
			turns[sd.TmdbID] = true
		}
		if len(got) != maxSeeds {
			t.Fatalf("%d seeds", len(got))
		}
		for i := range fixedSeeds {
			if !got[i] {
				t.Fatalf("strong seed %d left out", i)
			}
		}
	}
	if len(turns) != len(pos) {
		t.Fatalf("only %d of %d titles ever got a turn", len(turns), len(pos))
	}
}

// A title only started never names a row, even when it feeds the pool.
func TestStartedTitleGetsNoRow(t *testing.T) {
	now := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
	started := keyOf("movie", 77)
	cat := &fakeCatalog{related: map[titleKey]relatedLists{started: {recs: movies(100, 20, 80)}}, genres: map[titleKey][]int{}}
	s := NewService(RatingsFunc(func(uint) ([]Signal, error) { return nil, nil }), cat)
	s.now = func() time.Time { return now }
	s.SetHistory(HistoryFunc(func(uint) ([]Watched, error) {
		return []Watched{{MediaType: "movie", TmdbID: 77, Title: "Half", WatchedSeconds: 1800, UpdatedAt: now}}, nil
	}))
	resp, _, err := s.compute(1, "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 1 || resp.Rows[0].ID != "top-picks" {
		t.Fatalf("rows: %+v", resp.Rows)
	}
}

// The watch history makes rows on its own, and nothing watched is ever recommended.
func TestHistoryInRecommendations(t *testing.T) {
	now := time.Date(2026, 11, 6, 7, 0, 0, 0, time.UTC)
	binged, left := keyOf("tv", 5920), keyOf("tv", 320754)
	cat := &fakeCatalog{
		related: map[titleKey]relatedLists{
			binged: {recs: append(movies(100, 20, 80), Item{ID: left.id, MediaType: "tv", Title: "Left", PosterPath: "/l.jpg", VoteCount: 500})},
		},
		genres: map[titleKey][]int{},
	}
	s := NewService(RatingsFunc(func(uint) ([]Signal, error) { return nil, nil }), cat)
	s.now = func() time.Time { return now }
	s.SetHistory(HistoryFunc(func(uint) ([]Watched, error) {
		return []Watched{
			{MediaType: "tv", TmdbID: binged.id, Title: "The Mentalist", FinishedEpisodes: 60, WatchedSeconds: 150000, UpdatedAt: now},
			{MediaType: "tv", TmdbID: left.id, Title: "Left", WatchedSeconds: 53, UpdatedAt: now},
		}, nil
	}))
	resp, _, err := s.compute(1, "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) < 2 {
		t.Fatalf("rows: %+v", resp.Rows)
	}
	because := resp.Rows[1]
	if because.Reason != ReasonBecauseWatched || because.Title != "Because you watched The Mentalist" {
		t.Fatalf("row %q (%s)", because.Title, because.Reason)
	}
	for _, row := range resp.Rows {
		if contains(row.Items, left.id) {
			t.Fatalf("a title the profile started was recommended in %q", row.Title)
		}
	}
}

func TestWithHistoryKeepsRatings(t *testing.T) {
	now := time.Now()
	signals := []Signal{
		{MediaType: "movie", TmdbID: 1, Title: "Hated", Rating: "dislike", UpdatedAt: now},
		{MediaType: "tv", TmdbID: 2, Title: "Liked", Rating: "like", UpdatedAt: now},
	}
	got := withHistory(signals, []Watched{
		{MediaType: "movie", TmdbID: 1, Finished: true},
		{MediaType: "tv", TmdbID: 2, FinishedEpisodes: 60},
	})
	if w := signalWeight(got[0]); w != weightDislike {
		t.Fatalf("watching a disliked film changed its weight to %v", w)
	}
	if w := signalWeight(got[1]); w != weightLove {
		t.Fatalf("a binged show that was only liked: %v", w)
	}
	if got[1].Rating != "like" {
		t.Fatalf("rating changed to %q", got[1].Rating)
	}
}
