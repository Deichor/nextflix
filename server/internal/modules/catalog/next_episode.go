package catalog

import "time"

// NextEpisode is the episode after season/episode of a show, when it is already out
// (TMDB's season list; specials are skipped).
func (s *Service) NextEpisode(tvID, season, episode int) (int, int, bool) {
	d, err := s.tm.GetTVDetails(tvID)
	if err != nil || d == nil {
		return 0, 0, false
	}
	ns, ne := 0, 0
	for _, x := range d.Seasons {
		if x.SeasonNumber == season && episode < x.EpisodeCount {
			ns, ne = season, episode+1
			break
		}
	}
	if ns == 0 {
		for _, x := range d.Seasons {
			if x.SeasonNumber > season && x.EpisodeCount > 0 && (ns == 0 || x.SeasonNumber < ns) {
				ns, ne = x.SeasonNumber, 1
			}
		}
	}
	if ns == 0 {
		return 0, 0, false
	}
	if sea, err := s.Season("en", tvID, ns); err == nil &&
		episodeAvailability(sea, ne, localDay(time.Now())).Status == StatusNotReleased {
		return 0, 0, false
	}
	return ns, ne, true
}
