package catalog

import (
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

// Availability tells whether a title can be watched at home yet, so the player can say
// "coming on <date>" instead of searching for a source that cannot exist.

const (
	StatusAvailable   = "available"
	StatusNotReleased = "not_released"
	StatusInTheaters  = "in_theaters"
	StatusUnknown     = "unknown"

	releaseDatesTTL = 6 * time.Hour
	// A theatrical release this old without a listed digital date has a home release anyway
	// (TMDB rarely lists one for older films).
	theatricalWindow = 120 * 24 * time.Hour
	// Titles first shown this long ago are treated as available whatever TMDB lists.
	oldTitleAge = 365 * 24 * time.Hour
)

// Availability of a movie, show or episode. Dates are YYYY-MM-DD or nil when unknown.
type Availability struct {
	Status         string  `json:"status"`
	ReleaseDate    *string `json:"release_date"`
	TheatricalDate *string `json:"theatrical_date"`
	DigitalDate    *string `json:"digital_date"`
	// A film TMDB lists as in cinemas (or not out) whose home release is already on the
	// indexers: Status is available
	DigitalReleaseFound bool `json:"digital_release_found,omitempty"`
}

var istanbul = func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Istanbul"); err == nil {
		return loc
	}
	return time.FixedZone("Europe/Istanbul", 3*60*60) // UTC+3 all year since 2016
}()

// localDay is t's date in Istanbul as YYYY-MM-DD; TMDB dates compare as plain strings.
func localDay(t time.Time) string { return t.In(istanbul).Format("2006-01-02") }

func dateOf(s string) string {
	if len(s) < 10 {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s[:10]); err != nil {
		return ""
	}
	return s[:10]
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func earliest(a, b string) string {
	if a == "" || (b != "" && b < a) {
		return b
	}
	return a
}

// episodeAvailability: an episode is out once its air date has come. An episode without a
// date (or missing from the season) is not out while the season itself starts later.
func episodeAvailability(season *tmdb.Season, episode int, today string) Availability {
	if season == nil {
		return Availability{Status: StatusUnknown}
	}
	for _, ep := range season.Episodes {
		if ep.EpisodeNumber != episode {
			continue
		}
		if d := dateOf(ep.AirDate); d != "" {
			if d > today {
				return Availability{Status: StatusNotReleased, ReleaseDate: ptr(d)}
			}
			return Availability{Status: StatusAvailable, ReleaseDate: ptr(d)}
		}
		break
	}
	if d := dateOf(season.AirDate); d != "" && d > today {
		return Availability{Status: StatusNotReleased}
	}
	return Availability{Status: StatusUnknown}
}

// seasonAvailability judges a whole season by its air date.
func seasonAvailability(season *tmdb.Season, today string) Availability {
	if season == nil {
		return Availability{Status: StatusUnknown}
	}
	return byDate(dateOf(season.AirDate), today)
}

// showAvailability judges a show by its first air date.
func showAvailability(d *tmdb.TVDetails, today string) Availability {
	if d == nil {
		return Availability{Status: StatusUnknown}
	}
	a := byDate(dateOf(d.FirstAirDate), today)
	if a.Status == StatusUnknown && (d.Status == "Planned" || d.Status == "In Production" || d.Status == "Pilot") {
		a.Status = StatusNotReleased
	}
	return a
}

func byDate(d, today string) Availability {
	switch {
	case d == "":
		return Availability{Status: StatusUnknown}
	case d > today:
		return Availability{Status: StatusNotReleased, ReleaseDate: ptr(d)}
	}
	return Availability{Status: StatusAvailable, ReleaseDate: ptr(d)}
}

// movieAvailability applies the release rules to TMDB's release dates of every region:
//   - a digital (4), physical (5) or TV (6) release on or before today: available
//   - a theatrical release (2, 3) on or before today: in_theaters, unless it is over a year
//     old, or older than theatricalWindow with no digital date announced (then available)
//   - otherwise not_released, release_date = the earliest upcoming theatrical date (else the
//     earliest upcoming home release); a festival premiere (1) older than theatricalWindow
//     without any other date counts as available
//
// Without any dates, primary (the movie's release_date) decides between not_released and
// unknown.
func movieAvailability(dates []tmdb.ReleaseDate, primary string, now time.Time) Availability {
	today := localDay(now)
	windowStart := localDay(now.Add(-theatricalWindow))
	yearAgo := localDay(now.Add(-oldTitleAge))

	var theatrical, first, digital, futureDigital, futureHome, homeReleased string
	for _, r := range dates {
		d := dateOf(r.Date)
		if d == "" {
			continue
		}
		switch r.Type {
		case tmdb.ReleaseDigital, tmdb.ReleasePhysical, tmdb.ReleaseTV:
			if d <= today {
				homeReleased = earliest(homeReleased, d)
			} else {
				futureHome = earliest(futureHome, d)
			}
			if r.Type == tmdb.ReleaseDigital {
				digital = earliest(digital, d)
				if d > today {
					futureDigital = earliest(futureDigital, d)
				}
			}
		case tmdb.ReleaseTheatricalLimited, tmdb.ReleaseTheatrical:
			theatrical = earliest(theatrical, d)
			first = earliest(first, d)
		case tmdb.ReleasePremiere:
			first = earliest(first, d)
		}
	}
	a := Availability{TheatricalDate: ptr(theatrical), DigitalDate: ptr(digital)}

	switch {
	case homeReleased != "":
		a.Status, a.ReleaseDate = StatusAvailable, ptr(homeReleased)
	case theatrical != "" && theatrical <= today:
		// An old film may list a future digital re-release in some region
		if theatrical <= yearAgo || (futureDigital == "" && theatrical <= windowStart) {
			a.Status, a.ReleaseDate = StatusAvailable, ptr(theatrical)
			break
		}
		a.Status, a.ReleaseDate, a.DigitalDate = StatusInTheaters, ptr(futureDigital), ptr(futureDigital)
	case theatrical != "":
		a.Status, a.ReleaseDate = StatusNotReleased, ptr(theatrical)
	case futureHome != "":
		a.Status, a.ReleaseDate = StatusNotReleased, ptr(futureHome)
	case first != "":
		// Only a (festival) premiere so far
		if first <= windowStart {
			a.Status, a.ReleaseDate = StatusAvailable, ptr(first)
		} else {
			a.Status = StatusNotReleased
		}
	default:
		if d := dateOf(primary); d != "" && d > today {
			a.Status, a.ReleaseDate = StatusNotReleased, ptr(d)
		} else {
			a.Status = StatusUnknown
		}
	}
	return a
}

type releaseDatesEntry struct {
	dates   []tmdb.ReleaseDate
	expires time.Time
}

type releaseDatesCache struct {
	mu    sync.Mutex
	items map[int]releaseDatesEntry
}

// releaseDates returns a movie's release dates, cached for 6 hours.
func (s *Service) releaseDates(movieID int) ([]tmdb.ReleaseDate, error) {
	now := time.Now()
	c := &s.releaseDatesCache
	c.mu.Lock()
	e, ok := c.items[movieID]
	c.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.dates, nil
	}
	dates, err := s.tm.GetMovieReleaseDates(movieID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.items == nil {
		c.items = map[int]releaseDatesEntry{}
	}
	for k, old := range c.items {
		if now.After(old.expires) {
			delete(c.items, k)
		}
	}
	c.items[movieID] = releaseDatesEntry{dates: dates, expires: now.Add(releaseDatesTTL)}
	c.mu.Unlock()
	return dates, nil
}

// HomeReleaseFinder tells whether a home (non-cinema) release of a movie is already on the
// indexers; TMDB's digital dates often lag the real ones. streaming implements it (wired in
// main.go). It answers false when it found only cinema recordings or cannot tell in time.
type HomeReleaseFinder interface {
	HomeReleaseOut(tmdbID int) bool
}

// SetHomeReleaseFinder is called once at startup, before the server answers requests.
func (s *Service) SetHomeReleaseFinder(f HomeReleaseFinder) { s.homeRelease = f }

// homeReleaseWorth: a movie TMDB does not count as out whose home release may already exist:
// in cinemas, or not released with a release date that has passed (or none) or a digital
// date ahead. A film whose cinema release is still ahead is not searched.
func homeReleaseWorth(a Availability, today string) bool {
	switch a.Status {
	case StatusInTheaters:
		return true
	case StatusNotReleased:
		return a.ReleaseDate == nil || *a.ReleaseDate <= today || (a.DigitalDate != nil && *a.DigitalDate > today)
	}
	return false
}

// withHomeRelease turns a movie that is not out by TMDB's dates into an available one when
// its home release is on the indexers.
func (s *Service) withHomeRelease(tmdbID int, a Availability, today string) Availability {
	if s.homeRelease == nil || !homeReleaseWorth(a, today) || !s.homeRelease.HomeReleaseOut(tmdbID) {
		return a
	}
	a.Status, a.ReleaseDate, a.DigitalReleaseFound = StatusAvailable, a.TheatricalDate, true
	return a
}

// Availability of a movie, a show (season and episode 0), a season (episode 0) or an
// episode. TMDB failures give unknown, which never blocks playback.
func (s *Service) Availability(mediaType string, tmdbID, season, episode int) Availability {
	unknown := Availability{Status: StatusUnknown}
	now := time.Now()
	today := localDay(now)
	switch mediaType {
	case "movie":
		dates, err := s.releaseDates(tmdbID)
		if err != nil {
			log.Printf("availability: release dates of movie %d: %v", tmdbID, err)
			return unknown
		}
		primary := ""
		if len(dates) == 0 {
			if d, err := s.tm.GetMovieDetails(tmdbID); err == nil && d != nil {
				primary = d.ReleaseDate
			}
		}
		return s.withHomeRelease(tmdbID, movieAvailability(dates, primary, now), today)
	case "tv":
		if season <= 0 && episode <= 0 {
			d, err := s.tm.GetTVDetails(tmdbID)
			if err != nil {
				log.Printf("availability: TV %d: %v", tmdbID, err)
				return unknown
			}
			return showAvailability(d, today)
		}
		se, err := s.Season(tmdb.LangEN, tmdbID, season)
		if err != nil {
			log.Printf("availability: season %d of TV %d: %v", season, tmdbID, err)
			return unknown
		}
		if episode <= 0 {
			return seasonAvailability(se, today)
		}
		return episodeAvailability(se, episode, today)
	}
	return unknown
}

// GET /api/availability?media_type=movie|tv&tmdb_id=N[&season=S&episode=E]
func (s *Service) availability(c *gin.Context) {
	mediaType := c.Query("media_type")
	if mediaType != "movie" && mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type must be movie or tv"})
		return
	}
	id, err := strconv.Atoi(c.Query("tmdb_id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tmdb_id"})
		return
	}
	season, episode := 0, 0
	if mediaType == "tv" {
		season, _ = strconv.Atoi(c.Query("season"))
		episode, _ = strconv.Atoi(c.Query("episode"))
		if season < 0 || episode < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid season or episode"})
			return
		}
	}
	c.JSON(http.StatusOK, s.Availability(mediaType, id, season, episode))
}
