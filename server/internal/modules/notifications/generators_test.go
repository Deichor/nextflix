package notifications

import (
	"testing"
	"time"

	"nextflix/internal/platform/tmdb"
)

func TestDaysUntilCountsCalendarDays(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 10, 23, 59, 0, 0, time.UTC),
	} {
		for date, want := range map[string]int{"2026-10-09": -1, "2026-10-10": 0, "2026-10-11": 1, "2026-10-12": 2, "2026-11-10": 31} {
			if got, ok := daysUntil(date, now); !ok || got != want {
				t.Errorf("daysUntil(%s, %s) = %d, want %d", date, now.Format(time.Kitchen), got, want)
			}
		}
	}
	if _, ok := daysUntil("soon", time.Now()); ok {
		t.Error("unparsable date accepted")
	}
}

func TestPickersSkipRecentTitles(t *testing.T) {
	good := tmdb.TV{ID: 1, Name: "A", VoteAverage: 8, Popularity: 100}
	other := tmdb.TV{ID: 2, Name: "B", VoteAverage: 8, Popularity: 100}
	recent := map[int]bool{1: true}

	if tv, ok := pickTV([]tmdb.TV{good, other}, recent); !ok || tv.ID != 2 {
		t.Fatalf("pickTV = %d %v, want 2", tv.ID, ok)
	}
	if _, ok := pickTV([]tmdb.TV{good}, recent); ok {
		t.Fatal("pickTV picked a recent show")
	}

	all := func(tmdb.TV) bool { return true }
	if got := pickShows([]tmdb.TV{good, other}, all, recent); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("pickShows = %+v", got)
	}

	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	movie := func(id int) tmdb.MovieWithVideos {
		var m tmdb.MovieWithVideos
		m.ID, m.Title, m.VoteAverage, m.Popularity, m.ReleaseDate = id, "M", 7, 50, "2026-10-11"
		return m
	}
	got := upcomingCandidates([]tmdb.MovieWithVideos{movie(1), movie(2)}, now, recent)
	if len(got) != 1 || got[0].movie.ID != 2 || got[0].days != 1 {
		t.Fatalf("upcomingCandidates = %+v", got)
	}
}

func TestUnknownProfilesShareReadState(t *testing.T) {
	s := &Service{}
	if o, _ := s.owner(7); o != "7" {
		t.Fatalf("no checker: owner = %q", o)
	}
	s.SetProfiles(func(id uint) (bool, error) { return id == 1, nil })
	for id, want := range map[uint]string{0: "shared", 1: "1", 7: "shared"} {
		if o, err := s.owner(id); err != nil || o != want {
			t.Fatalf("owner(%d) = %q %v, want %q", id, o, err, want)
		}
	}
}
