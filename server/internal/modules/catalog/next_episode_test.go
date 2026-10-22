package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nextflix/internal/platform/tmdb"
)

// weeklyTMDB answers /tv/1 as a weekly show: season 1 has 8 episodes, episode e airs on
// airs[e-1] (empty: no date yet); season 2 is listed without episodes and next is its first one.
func weeklyTMDB(t *testing.T, airs []string, next map[string]any) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tv/1/season/1" {
			var eps []map[string]any
			for e, d := range airs {
				eps = append(eps, map[string]any{"episode_number": e + 1, "air_date": d})
			}
			json.NewEncoder(w).Encode(map[string]any{"season_number": 1, "air_date": airs[0], "episodes": eps})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": "Show", "next_episode_to_air": next,
			"seasons": []map[string]any{{"season_number": 1, "episode_count": len(airs)}, {"season_number": 2, "episode_count": 0}}})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("k").WithBaseURL(srv.URL)
}

func day(offset int) string { return localDay(time.Now().AddDate(0, 0, offset)) }

func TestUpcomingEpisode(t *testing.T) {
	airs := []string{day(-14), day(-7), day(6), day(13), day(40), ""}
	s := NewService(weeklyTMDB(t, airs, map[string]any{"season_number": 2, "episode_number": 1, "air_date": day(20)}))
	for _, c := range []struct {
		episode, we int
		date        string
	}{
		{2, 3, day(6)},  // the latest aired one watched: next week's episode
		{3, 4, day(13)}, // ahead of time (watched early), still dated
		{4, 0, ""},      // airs after the window
		{5, 0, ""},      // no date yet
		{1, 0, ""},      // the next one is out: NextEpisode's job
	} {
		ns, ne, date, ok := s.UpcomingEpisode(1, 1, c.episode)
		if c.we == 0 {
			if ok {
				t.Errorf("after S1E%d: got S%dE%d %s, want none", c.episode, ns, ne, date)
			}
			continue
		}
		if !ok || ns != 1 || ne != c.we || date != c.date {
			t.Errorf("after S1E%d: S%dE%d %s %v, want S1E%d %s", c.episode, ns, ne, date, ok, c.we, c.date)
		}
	}
	if _, _, ok := s.NextEpisode(1, 1, 2); ok {
		t.Error("NextEpisode offers an episode that is not out")
	}
}

func TestUpcomingNewSeason(t *testing.T) {
	airs := []string{day(-14), day(-7)}
	s := NewService(weeklyTMDB(t, airs, map[string]any{"season_number": 2, "episode_number": 1, "air_date": day(20)}))
	ns, ne, date, ok := s.UpcomingEpisode(1, 1, 2)
	if !ok || ns != 2 || ne != 1 || date != day(20) {
		t.Fatalf("got S%dE%d %s %v, want S2E1 %s", ns, ne, date, ok, day(20))
	}
	if _, _, _, ok := s.UpcomingEpisode(1, 2, 1); ok {
		t.Error("an announced episode the profile is past is offered")
	}
}
