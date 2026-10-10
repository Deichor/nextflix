package catalog

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func checkAvail(t *testing.T, name string, got Availability, status, release string) {
	t.Helper()
	if got.Status != status || str(got.ReleaseDate) != release {
		t.Errorf("%s: got %s / %s, want %s / %s", name, got.Status, str(got.ReleaseDate), status, release)
	}
}

func TestEpisodeAvailability(t *testing.T) {
	today := "2026-10-08"
	season := &tmdb.Season{AirDate: "2026-10-16", Episodes: []tmdb.Episode{
		{EpisodeNumber: 1, AirDate: "2026-10-16"},
		{EpisodeNumber: 2, AirDate: ""},
	}}
	checkAvail(t, "future episode", episodeAvailability(season, 1, today), StatusNotReleased, "2026-10-16")
	checkAvail(t, "undated episode of a future season", episodeAvailability(season, 2, today), StatusNotReleased, "<nil>")
	checkAvail(t, "missing episode of a future season", episodeAvailability(season, 9, today), StatusNotReleased, "<nil>")

	aired := &tmdb.Season{AirDate: "2026-09-01", Episodes: []tmdb.Episode{
		{EpisodeNumber: 1, AirDate: "2026-09-01"},
		{EpisodeNumber: 2, AirDate: "2026-10-08"}, // airs today
		{EpisodeNumber: 3},
	}}
	checkAvail(t, "aired episode", episodeAvailability(aired, 1, today), StatusAvailable, "2026-09-01")
	checkAvail(t, "episode airing today", episodeAvailability(aired, 2, today), StatusAvailable, "2026-10-08")
	checkAvail(t, "undated episode of a running season", episodeAvailability(aired, 3, today), StatusUnknown, "<nil>")
	checkAvail(t, "no season", episodeAvailability(nil, 1, today), StatusUnknown, "<nil>")
}

func TestShowAndSeasonAvailability(t *testing.T) {
	today := "2026-10-08"
	checkAvail(t, "future show", showAvailability(&tmdb.TVDetails{TV: tmdb.TV{FirstAirDate: "2026-10-16"}}, today), StatusNotReleased, "2026-10-16")
	checkAvail(t, "running show", showAvailability(&tmdb.TVDetails{TV: tmdb.TV{FirstAirDate: "2008-01-20"}}, today), StatusAvailable, "2008-01-20")
	checkAvail(t, "planned show", showAvailability(&tmdb.TVDetails{Status: "Planned"}, today), StatusNotReleased, "<nil>")
	checkAvail(t, "undated show", showAvailability(&tmdb.TVDetails{}, today), StatusUnknown, "<nil>")
	checkAvail(t, "future season", seasonAvailability(&tmdb.Season{AirDate: "2027-01-01"}, today), StatusNotReleased, "2027-01-01")
}

func TestLocalDayIsIstanbul(t *testing.T) {
	// 22:30 UTC is already the next day in Istanbul (UTC+3)
	now := time.Date(2026, 10, 15, 22, 30, 0, 0, time.UTC)
	if d := localDay(now); d != "2026-10-16" {
		t.Fatalf("local day %s", d)
	}
	season := &tmdb.Season{Episodes: []tmdb.Episode{{EpisodeNumber: 1, AirDate: "2026-10-16"}}}
	checkAvail(t, "episode at midnight Istanbul", episodeAvailability(season, 1, localDay(now)), StatusAvailable, "2026-10-16")
}

func TestMovieAvailability(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	rd := func(country string, typ int, date string) tmdb.ReleaseDate {
		return tmdb.ReleaseDate{Country: country, Type: typ, Date: date}
	}

	// In cinemas since last week, no digital date yet: must not be searched
	a := movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "2026-10-01"), rd("TR", tmdb.ReleaseTheatrical, "2026-10-03")}, "", now)
	checkAvail(t, "theatrical only", a, StatusInTheaters, "<nil>")
	if str(a.TheatricalDate) != "2026-10-01" || a.DigitalDate != nil {
		t.Errorf("theatrical only: dates %s %s", str(a.TheatricalDate), str(a.DigitalDate))
	}

	// ... with an announced digital release: release_date is that date
	a = movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "2026-10-01"), rd("US", tmdb.ReleaseDigital, "2026-11-04"),
		rd("GB", tmdb.ReleaseDigital, "2026-11-10")}, "", now)
	checkAvail(t, "theatrical with digital ahead", a, StatusInTheaters, "2026-11-04")
	if str(a.DigitalDate) != "2026-11-04" {
		t.Errorf("digital date %s", str(a.DigitalDate))
	}

	// Digital release in any region: available
	a = movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "2026-08-01"), rd("DE", tmdb.ReleaseDigital, "2026-10-02"),
		rd("US", tmdb.ReleaseDigital, "2026-10-20")}, "", now)
	checkAvail(t, "digital released", a, StatusAvailable, "2026-10-02")
	checkAvail(t, "physical released", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleasePhysical, "2026-09-01")}, "", now), StatusAvailable, "2026-09-01")
	checkAvail(t, "tv premiere", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTV, "2026-10-08")}, "", now), StatusAvailable, "2026-10-08")

	// Not in cinemas yet: release_date is the earliest theatrical date
	a = movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleasePremiere, "2026-09-10"), rd("US", tmdb.ReleaseTheatrical, "2026-12-18"),
		rd("TR", tmdb.ReleaseTheatricalLimited, "2026-12-11")}, "", now)
	checkAvail(t, "upcoming", a, StatusNotReleased, "2026-12-11")
	checkAvail(t, "direct to streaming", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseDigital, "2026-10-30")}, "", now), StatusNotReleased, "2026-10-30")
	checkAvail(t, "festival premiere only", movieAvailability([]tmdb.ReleaseDate{rd("FR", tmdb.ReleasePremiere, "2026-09-01")}, "", now), StatusNotReleased, "<nil>")

	// Older films rarely list a digital date
	checkAvail(t, "old theatrical only", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "1994-09-23")}, "", now), StatusAvailable, "1994-09-23")
	checkAvail(t, "theatrical 5 months ago", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "2026-05-01")}, "", now), StatusAvailable, "2026-05-01")
	checkAvail(t, "old film with a digital re-release ahead", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "1994-09-23"),
		rd("JP", tmdb.ReleaseDigital, "2026-12-01")}, "", now), StatusAvailable, "1994-09-23")
	checkAvail(t, "theatrical 5 months ago, digital ahead", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "2026-05-01"),
		rd("US", tmdb.ReleaseDigital, "2026-11-01")}, "", now), StatusInTheaters, "2026-11-01")

	// Missing data
	checkAvail(t, "no data", movieAvailability(nil, "", now), StatusUnknown, "<nil>")
	checkAvail(t, "no dates, past primary", movieAvailability(nil, "2020-01-01", now), StatusUnknown, "<nil>")
	checkAvail(t, "no dates, future primary", movieAvailability(nil, "2027-05-01", now), StatusNotReleased, "2027-05-01")
	checkAvail(t, "bad dates", movieAvailability([]tmdb.ReleaseDate{rd("US", tmdb.ReleaseTheatrical, "soon")}, "", now), StatusUnknown, "<nil>")
}

func TestAvailabilityHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(tmdb.New("")) // no API key: every lookup fails
	r := gin.New()
	Register(r.Group("/api"), s)

	for _, tc := range []struct {
		url    string
		status int
	}{
		{"/api/availability?media_type=movie&tmdb_id=1", 200},
		{"/api/availability?media_type=tv&tmdb_id=320754&season=1&episode=1", 200},
		{"/api/availability?media_type=tv&tmdb_id=320754", 200},
		{"/api/availability?media_type=book&tmdb_id=1", 400},
		{"/api/availability?media_type=movie&tmdb_id=x", 400},
		{"/api/availability?media_type=tv&tmdb_id=1&season=-1", 400},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", tc.url, nil))
		if w.Code != tc.status {
			t.Errorf("%s: %d, want %d", tc.url, w.Code, tc.status)
			continue
		}
		if tc.status != 200 {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		// TMDB failures are unknown, never a block; every key is present
		if body["status"] != StatusUnknown {
			t.Errorf("%s: status %v", tc.url, body["status"])
		}
		for _, k := range []string{"release_date", "theatrical_date", "digital_date"} {
			if v, ok := body[k]; !ok || v != nil {
				t.Errorf("%s: %s = %v (present %v), want null", tc.url, k, v, ok)
			}
		}
	}
}

type fakeHomeRelease struct {
	found map[int]bool
	asked []int
}

func (f *fakeHomeRelease) HomeReleaseOut(id int) bool {
	f.asked = append(f.asked, id)
	return f.found[id]
}

// A film TMDB lists as in cinemas whose home release is on the indexers is available; one
// with cinema recordings only stays in_theaters. Films not worth a search are not searched.
func TestAvailabilityWithHomeRelease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now()
	day := func(d int) string { return localDay(now.AddDate(0, 0, d)) }
	rd := func(typ int, date string) tmdb.ReleaseDate {
		return tmdb.ReleaseDate{Country: "US", Type: typ, Date: date}
	}
	s := NewService(tmdb.New(""))
	f := &fakeHomeRelease{found: map[int]bool{1368337: true}}
	s.SetHomeReleaseFinder(f)
	s.releaseDatesCache.items = map[int]releaseDatesEntry{}
	for id, dates := range map[int][]tmdb.ReleaseDate{
		1368337: {rd(tmdb.ReleaseTheatrical, day(-86)), rd(tmdb.ReleaseDigital, day(39))}, // The Odyssey
		7:       {rd(tmdb.ReleaseTheatrical, day(-3))},                                    // cam rips only
		8:       {rd(tmdb.ReleaseTheatrical, day(30))},                                    // in cinemas next month
		9:       {rd(tmdb.ReleaseDigital, day(-30))},                                      // out
	} {
		s.releaseDatesCache.items[id] = releaseDatesEntry{dates: dates, expires: now.Add(time.Hour)}
	}
	r := gin.New()
	Register(r.Group("/api"), s)
	get := func(id int) map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/availability?media_type=movie&tmdb_id="+strconv.Itoa(id), nil))
		var body map[string]any
		json.Unmarshal(w.Body.Bytes(), &body)
		return body
	}

	b := get(1368337)
	if b["status"] != StatusAvailable || b["digital_release_found"] != true || b["release_date"] != day(-86) || b["digital_date"] != day(39) {
		t.Fatalf("odyssey: %v", b)
	}
	if b = get(7); b["status"] != StatusInTheaters || b["digital_release_found"] != nil {
		t.Fatalf("cinema only: %v", b)
	}
	if b = get(8); b["status"] != StatusNotReleased {
		t.Fatalf("upcoming: %v", b)
	}
	if b = get(9); b["status"] != StatusAvailable || b["digital_release_found"] != nil {
		t.Fatalf("out: %v", b)
	}
	if fmt.Sprint(f.asked) != "[1368337 7]" {
		t.Fatalf("searched %v", f.asked)
	}

	today := day(0)
	for _, c := range []struct {
		a    Availability
		want bool
	}{
		{Availability{Status: StatusInTheaters}, true},
		{Availability{Status: StatusNotReleased}, true}, // a festival premiere only
		{Availability{Status: StatusNotReleased, ReleaseDate: ptr(day(-1))}, true},
		{Availability{Status: StatusNotReleased, ReleaseDate: ptr(day(5)), DigitalDate: ptr(day(5))}, true},
		{Availability{Status: StatusNotReleased, ReleaseDate: ptr(day(5))}, false},
		// cinema release still ahead, even with a digital date after it
		{Availability{Status: StatusNotReleased, ReleaseDate: ptr(day(5)), TheatricalDate: ptr(day(5)), DigitalDate: ptr(day(40))}, false},
		{Availability{Status: StatusAvailable}, false},
		{Availability{Status: StatusUnknown}, false},
	} {
		if got := homeReleaseWorth(c.a, today); got != c.want {
			t.Errorf("%s %s/%s: %v", c.a.Status, str(c.a.ReleaseDate), str(c.a.DigitalDate), got)
		}
	}
}
