package catalog

import (
	"time"

	"nextflix/internal/platform/tmdb"
)

// upcomingWindow is how far ahead an unreleased next episode still shows in Continue Watching.
const upcomingWindow = 30 * 24 * time.Hour

// NextEpisode is the episode after season/episode of a show, when it is already out
// (TMDB's season list; specials follow only a special).
func (s *Service) NextEpisode(tvID, season, episode int) (int, int, bool) {
	d, err := s.tm.GetTVDetails(tvID)
	if err != nil || d == nil {
		return 0, 0, false
	}
	ns, ne, found := nextAfter(d, season, episode)
	if !found {
		return 0, 0, false
	}
	if sea, err := s.Season("en", tvID, ns); err == nil &&
		episodeAvailability(sea, ne, localDay(time.Now())).Status == StatusNotReleased {
		return 0, 0, false
	}
	return ns, ne, true
}

// UpcomingEpisode is the episode after season/episode when it is not out yet but airs within
// upcomingWindow, with its air date (YYYY-MM-DD).
func (s *Service) UpcomingEpisode(tvID, season, episode int) (int, int, string, bool) {
	d, err := s.tm.GetTVDetails(tvID)
	if err != nil || d == nil {
		return 0, 0, "", false
	}
	now := time.Now()
	today, limit := localDay(now), localDay(now.Add(upcomingWindow))
	ns, ne, found := nextAfter(d, season, episode)
	date := ""
	if found {
		if sea, err := s.Season("en", tvID, ns); err == nil {
			if a := episodeAvailability(sea, ne, today); a.Status == StatusNotReleased && a.ReleaseDate != nil {
				date = *a.ReleaseDate
			}
		}
	} else if n := d.NextEpisodeToAir; n != nil && (n.SeasonNumber > season || n.SeasonNumber == season && n.EpisodeNumber > episode) {
		// A new season TMDB lists without episodes yet
		ns, ne, date = n.SeasonNumber, n.EpisodeNumber, dateOf(n.AirDate)
	}
	if date == "" || date <= today || date > limit {
		return 0, 0, "", false
	}
	return ns, ne, date, true
}

// nextAfter is the episode after season/episode in the show's season list.
func nextAfter(d *tmdb.TVDetails, season, episode int) (int, int, bool) {
	for _, x := range d.Seasons {
		if x.SeasonNumber == season && episode < x.EpisodeCount {
			return season, episode + 1, true
		}
	}
	ns, found := 0, false
	for _, x := range d.Seasons {
		if x.SeasonNumber > season && x.EpisodeCount > 0 && (!found || x.SeasonNumber < ns) {
			ns, found = x.SeasonNumber, true
		}
	}
	return ns, 1, found
}
