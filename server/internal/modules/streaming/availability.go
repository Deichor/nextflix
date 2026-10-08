package streaming

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Resolve does not search for titles that are not out yet: an unaired episode has no
// source, and a film that is only in cinemas would only turn up cinema recordings.

const (
	tsAvailabilityWait = 4 * time.Second // a slower lookup counts as unknown

	// HTTP 425 Too Early: the title exists, but cannot be streamed yet
	statusTooEarly = 425
)

// AvailabilityChecker reports whether a title is out yet. catalog implements it (wired in
// main.go); season and episode are 0 for movies.
type AvailabilityChecker interface {
	Availability(mediaType string, tmdbID, season, episode int) TitleAvailability
}

// TitleAvailability: Status is available, not_released, in_theaters or unknown; dates are
// YYYY-MM-DD or "" when unknown.
type TitleAvailability struct {
	Status      string
	ReleaseDate string
	DigitalDate string
}

// notOut reports whether q names a title that must not be searched (not released, or only
// in cinemas). Without a tmdb_id, a checker or an answer in time it is never blocked.
func (s *Service) notOut(q tsResolveQuery) (TitleAvailability, bool) {
	if s.cfg.Availability == nil || q.tmdbID <= 0 {
		return TitleAvailability{}, false
	}
	ch := make(chan TitleAvailability, 1)
	go func() {
		ch <- s.cfg.Availability.Availability(q.mediaType, q.tmdbID, q.season, q.episode)
	}()
	select {
	case a := <-ch:
		blocked := a.Status == "not_released" || a.Status == "in_theaters"
		if blocked {
			log.Printf("resolve %q: not searched, %s (release %q, digital %q)", q.title, a.Status, a.ReleaseDate, a.DigitalDate)
		}
		return a, blocked
	case <-time.After(tsAvailabilityWait):
		log.Printf("resolve %q: availability lookup timed out", q.title)
		return TitleAvailability{}, false
	}
}

func tsDateOrNil(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// tsNotOutBody is the 425 answer of resolve.
func tsNotOutBody(a TitleAvailability) gin.H {
	return gin.H{
		"error":        "not_released",
		"status":       a.Status,
		"release_date": tsDateOrNil(a.ReleaseDate),
		"digital_date": tsDateOrNil(a.DigitalDate),
	}
}
