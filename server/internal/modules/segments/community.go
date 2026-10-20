package segments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Community timestamp databases (free reads, no key):
//   - TheIntroDB (theintrodb.org): asked by TMDB id (v4 resolves it and the TMDB episode numbers to
//     TVDB server-side), ?duration_ms= picks the release version.
//     500 reads a day per IP without a key.
//   - IntroDB (introdb.app): keyed by the show's IMDb id.
const (
	theIntroDBURL = "https://api.theintrodb.org/v4/media"
	introDBURL    = "https://api.introdb.app/segments"
)

// errRateLimited pauses the lookups for a while (TheIntroDB counts reads per day).
var errRateLimited = errors.New("rate limited")

// found is one database's answer; times in seconds, 0 = none.
type found struct {
	introStart, introEnd, creditsStart, creditsEnd float64
}

func (f found) empty() bool { return f.introEnd <= 0 && f.creditsStart <= 0 }

type msRange struct {
	StartMs *int64 `json:"start_ms"`
	EndMs   *int64 `json:"end_ms"`
}

func sec(ms *int64) float64 {
	if ms == nil || *ms <= 0 {
		return 0
	}
	return float64(*ms) / 1000
}

// theIntroDB asks TheIntroDB. A null intro start means the start of the file, a null credits
// end the end of it; 0 may stand for "no segment".
func (s *Service) theIntroDB(ctx context.Context, tmdbID, season, episode int, duration float64) (found, error) {
	q := url.Values{"tmdb_id": {strconv.Itoa(tmdbID)}, "season": {strconv.Itoa(season)}, "episode": {strconv.Itoa(episode)}}
	if duration > 0 {
		q.Set("duration_ms", strconv.FormatInt(int64(duration*1000), 10))
	}
	var body struct {
		Intro   []msRange `json:"intro"`
		Credits []msRange `json:"credits"`
	}
	if ok, err := s.getJSON(ctx, s.theIntroDBURL+"?"+q.Encode(), &body); !ok || err != nil {
		return found{}, err
	}
	var f found
	if len(body.Intro) > 0 {
		f.introStart, f.introEnd = sec(body.Intro[0].StartMs), sec(body.Intro[0].EndMs)
	}
	if len(body.Credits) > 0 {
		f.creditsStart, f.creditsEnd = sec(body.Credits[0].StartMs), sec(body.Credits[0].EndMs)
	}
	return f, nil
}

// introDB asks IntroDB by the show's IMDb id ("outro" is the end credits).
func (s *Service) introDB(ctx context.Context, tmdbID, season, episode int) (found, error) {
	if s.imdb == nil {
		return found{}, nil
	}
	imdbID, err := s.imdb(tmdbID)
	if err != nil || imdbID == "" {
		return found{}, err
	}
	q := url.Values{"imdb_id": {imdbID}, "season": {strconv.Itoa(season)}, "episode": {strconv.Itoa(episode)}}
	var body struct {
		Intro *msRange `json:"intro"`
		Outro *msRange `json:"outro"`
	}
	if ok, err := s.getJSON(ctx, s.introDBURL+"?"+q.Encode(), &body); !ok || err != nil {
		return found{}, err
	}
	var f found
	if body.Intro != nil {
		f.introStart, f.introEnd = sec(body.Intro.StartMs), sec(body.Intro.EndMs)
	}
	if body.Outro != nil {
		f.creditsStart, f.creditsEnd = sec(body.Outro.StartMs), sec(body.Outro.EndMs)
	}
	return f, nil
}

// getJSON reads a JSON answer; ok is false for "not found" (404).
func (s *Service) getJSON(ctx context.Context, u string, dst any) (bool, error) {
	resp, err := s.http.Get(ctx, u, http.Header{"Accept": {"application/json"}})
	if err != nil {
		return false, err
	}
	switch {
	case resp.Status == http.StatusNotFound:
		return false, nil
	case resp.Status == http.StatusTooManyRequests:
		return false, errRateLimited
	case resp.Status != http.StatusOK:
		return false, fmt.Errorf("status %d", resp.Status)
	}
	if err := json.Unmarshal(resp.Body, dst); err != nil {
		return false, err
	}
	return true, nil
}
