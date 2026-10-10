package catalog

import "time"

// NextEpisode is the episode after season/episode of a show, when it is already out
// (TMDB's season list; specials follow only a special).
func (s *Service) NextEpisode(tvID, season, episode int) (int, int, bool) {
	d, err := s.tm.GetTVDetails(tvID)
	if err != nil || d == nil {
		return 0, 0, false
	}
	ns, ne, found := 0, 0, false
	for _, x := range d.Seasons {
		if x.SeasonNumber == season && episode < x.EpisodeCount {
			ns, ne, found = season, episode+1, true
			break
		}
	}
	if !found {
		for _, x := range d.Seasons {
			if x.SeasonNumber > season && x.EpisodeCount > 0 && (!found || x.SeasonNumber < ns) {
				ns, ne, found = x.SeasonNumber, 1, true
			}
		}
	}
	if !found {
		return 0, 0, false
	}
	if sea, err := s.Season("en", tvID, ns); err == nil &&
		episodeAvailability(sea, ne, localDay(time.Now())).Status == StatusNotReleased {
		return 0, 0, false
	}
	return ns, ne, true
}
