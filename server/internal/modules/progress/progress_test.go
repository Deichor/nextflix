package progress

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nextflix/internal/platform/database"
)

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE profiles (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO profiles (id, name) VALUES (1, 'Home')")
	if err := database.Migrate(db, Models()...); err != nil {
		t.Fatal(err)
	}
	return NewService(db)
}

// save stores an episode with the given position (of 100) and age.
func save(t *testing.T, s *Service, tv, season, episode int, pos float64, age time.Duration) {
	t.Helper()
	row := StreamProgress{ProfileID: 1, MediaType: "tv", TmdbID: tv, Season: season, Episode: episode, Title: "Show",
		Position: pos, Duration: 100}
	if _, err := s.Save(row); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&StreamProgress{}).Where(keyWhere, 1, "tv", tv, season, episode).
		UpdateColumn("updated_at", time.Now().Add(-age))
}

// tenEpisodes: every season has 10 episodes, all released.
func tenEpisodes(_, season, episode int) (int, int, bool) {
	if episode < 10 {
		return season, episode + 1, true
	}
	return season + 1, 1, true
}

func TestContinueSkipsFinishedNextEpisodes(t *testing.T) {
	s := testService(t)
	s.SetNextEpisode(tenEpisodes)
	save(t, s, 7, 1, 2, 99, 2*time.Hour)   // watched earlier
	save(t, s, 7, 1, 3, 98, 1*time.Hour)   // watched earlier
	save(t, s, 7, 1, 1, 99, 1*time.Minute) // rewatched last
	out, err := s.ContinueWatching(context.Background(), 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("out %+v %v", out, err)
	}
	if out[0].Season != 1 || out[0].Episode != 4 || out[0].Position != 0 {
		t.Fatalf("want S1E4 at 0, got %+v", out[0])
	}

	// A saved, unfinished next episode keeps its position
	save(t, s, 7, 1, 4, 30, 3*time.Hour)
	save(t, s, 7, 1, 1, 99, 0)
	out, _ = s.ContinueWatching(context.Background(), 1)
	if len(out) != 1 || out[0].Episode != 4 || out[0].Position != 30 {
		t.Fatalf("want S1E4 at 30, got %+v", out)
	}
}

func TestContinueGivesUpAfterMaxSteps(t *testing.T) {
	s := testService(t)
	s.SetNextEpisode(tenEpisodes)
	for e := 2; e <= 2+maxNextSteps; e++ {
		save(t, s, 9, 1, e, 99, time.Duration(e)*time.Hour)
	}
	save(t, s, 9, 1, 1, 99, 0)
	out, _ := s.ContinueWatching(context.Background(), 1)
	if len(out) != 0 {
		t.Fatalf("want nothing, got %+v", out)
	}
}

func TestContinueCapsLookups(t *testing.T) {
	s := testService(t)
	var calls atomic.Int32
	s.SetNextEpisode(func(tv, season, episode int) (int, int, bool) {
		calls.Add(1)
		return 0, 0, false // show ended
	})
	for tv := 1; tv <= 40; tv++ {
		save(t, s, tv, 1, 1, 99, time.Duration(tv)*time.Minute)
	}
	if _, err := s.ContinueWatching(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n > maxNextLookups {
		t.Fatalf("%d lookups, cap is %d", n, maxNextLookups)
	}
}

func TestContinueHonoursDeadline(t *testing.T) {
	s := testService(t)
	release := make(chan struct{})
	defer close(release)
	s.SetNextEpisode(func(int, int, int) (int, int, bool) {
		<-release
		return 1, 2, true
	})
	save(t, s, 5, 1, 1, 99, time.Minute)
	save(t, s, 6, 1, 1, 10, 2*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := s.ContinueWatching(ctx, 1)
	if err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("took %v: %v", time.Since(start), err)
	}
	if len(out) != 1 || out[0].TmdbID != 6 {
		t.Fatalf("want only the unfinished show, got %+v", out)
	}
}

func TestInputSizeCaps(t *testing.T) {
	ok := saveInput{Title: "Show", Magnet: "magnet:?xt=urn:btih:abc", Hash: "abc"}
	if !inputSizeOK(ok) {
		t.Fatal("normal input refused")
	}
	long := make([]byte, maxMagnetLen+1)
	for i := range long {
		long[i] = 'a'
	}
	for _, in := range []saveInput{
		{Magnet: string(long)}, {Title: string(long)}, {EpisodeTitle: string(long)},
		{PosterPath: string(long[:maxPathLen+1])}, {Hash: string(long[:maxHashLen+1])},
	} {
		if inputSizeOK(in) {
			t.Fatalf("oversized input accepted: %d bytes", len(in.Magnet)+len(in.Title)+len(in.EpisodeTitle)+len(in.PosterPath)+len(in.Hash))
		}
	}
}
