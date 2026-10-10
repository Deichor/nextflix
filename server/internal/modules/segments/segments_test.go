package segments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestChapterKind(t *testing.T) {
	cases := map[string]string{
		"Intro":                  "intro",
		"Opening":                "intro",
		"OP":                     "intro",
		"OP - Kick Back":         "intro",
		"Opening Credits":        "intro",
		"Giriş":                  "intro",
		"Credits":                "credits",
		"End Credits":            "credits",
		"ED: Chainsaw Blood":     "credits",
		"Ending":                 "credits",
		"Outro":                  "credits",
		"Chapter 01":             "",
		"Opening Scene":          "",
		"The Ending of Everyone": "",
		"Prologue":               "",
		"":                       "",
	}
	for title, want := range cases {
		if got := chapterKind(title); got != want {
			t.Errorf("chapterKind(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestFromChapters(t *testing.T) {
	ch := []Chapter{
		{0, 95, "Prologue"}, {95, 185, "Opening"}, {185, 1300, "Part A"}, {1300, 1390, "Ending"}, {1390, 1420, "Preview"},
	}
	intro, credits := fromChapters(ch, 1420)
	if intro == nil || intro.Start != 95 || intro.End != 185 {
		t.Fatalf("intro = %+v", intro)
	}
	if credits == nil || credits.Start != 1300 || credits.End != 1390 {
		t.Fatalf("credits = %+v", credits)
	}
}

func TestRulesRejectMidEpisode(t *testing.T) {
	// Credits in the first half, or 20 minutes before the end, are another version's or wrong
	if validCredits(1000, 0, 2700, "x") != nil || validCredits(1500, 0, 2700, "x") != nil {
		t.Fatal("credits far from the end must be rejected")
	}
	if validCredits(2600, 0, 0, "x") != nil {
		t.Fatal("credits without a known length must be rejected")
	}
	if c := validCredits(2600, 0, 2700, "x"); c == nil || c.End != 2700 {
		t.Fatalf("credits to the end: %+v", c)
	}
	if validIntro(1500, 1560, 2700, "x") != nil || validIntro(10, 12, 2700, "x") != nil || validIntro(0, 400, 2700, "x") != nil {
		t.Fatal("late, short or long intros must be rejected")
	}
	if i := validIntro(0.4, 60, 2700, "x"); i == nil || i.Start != 0 {
		t.Fatalf("intro at the start: %+v", i)
	}
}

func TestCommunityFallbackAndShare(t *testing.T) {
	var theCalls, introCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/the"):
			theCalls.Add(1)
			if r.URL.Query().Get("duration_ms") != "2900000" {
				t.Errorf("duration_ms = %q", r.URL.Query().Get("duration_ms"))
			}
			w.Write([]byte(`{"tmdb_id":1396,"type":"tv","season":1,"episode":2,"intro":[{"start_ms":314360,"end_ms":330461}]}`))
		default:
			introCalls.Add(1)
			w.Write([]byte(`{"intro":{"start_ms":314500,"end_ms":331000},"outro":{"start_ms":2843000,"end_ms":2901000}}`))
		}
	}))
	defer srv.Close()
	s := NewService(nil, Config{IMDb: func(int) (string, error) { return "tt0903747", nil }})
	s.theIntroDBURL, s.introDBURL = srv.URL+"/the", srv.URL+"/intro"

	r := s.Get(context.Background(), Query{TmdbID: 1396, Season: 1, Episode: 2, Duration: 2900})
	if r.Intro == nil || r.Intro.Start != 314.36 || r.Intro.Source != "theintrodb+introdb" {
		t.Fatalf("intro = %+v", r.Intro)
	}
	if r.Credits == nil || r.Credits.Start != 2843 || r.Credits.End != 2900 {
		t.Fatalf("credits = %+v", r.Credits)
	}
	if theCalls.Load() != 1 || introCalls.Load() != 1 {
		t.Fatalf("calls: %d %d", theCalls.Load(), introCalls.Load())
	}
}

func TestTheIntroDBRateLimitPausesAndShortensTheCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "s.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/the") {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"intro":{"start_ms":314500,"end_ms":331000}}`))
	}))
	defer srv.Close()
	s := NewService(db, Config{IMDb: func(int) (string, error) { return "tt0903747", nil }})
	s.theIntroDBURL, s.introDBURL = srv.URL+"/the", srv.URL+"/intro"

	r := s.Get(context.Background(), Query{TmdbID: 1396, Season: 1, Episode: 2, Duration: 2900})
	if r.Intro == nil || r.Intro.Source != "introdb" {
		t.Fatalf("intro = %+v", r.Intro)
	}
	s.mu.Lock()
	paused := time.Until(s.pausedTo)
	s.mu.Unlock()
	if paused < limitPause-time.Minute {
		t.Fatalf("TheIntroDB's 429 did not pause the lookups (%v)", paused)
	}
	var row SkipSegment
	if err := db.First(&row).Error; err != nil || row.Source != "introdb" {
		t.Fatalf("saved %+v %v", row, err)
	}
	// Without TheIntroDB's data the row is asked again after missingTTL, with it after foundTTL
	db.Model(&row).Update("fetched_at", time.Now().Add(-missingTTL-time.Hour))
	if _, ok := s.cached(1396, 1, 2, 2900); ok {
		t.Fatal("a row without TheIntroDB data kept for foundTTL")
	}
	db.Model(&row).Updates(map[string]any{"source": "theintrodb+introdb", "incomplete": false})
	if _, ok := s.cached(1396, 1, 2, 2900); !ok {
		t.Fatal("a TheIntroDB row expired early")
	}
}

func TestIntroDBFailureShortensTheCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "s.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/the") {
			w.Write([]byte(`{"intro":[{"start_ms":314360,"end_ms":330461}]}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	s := NewService(db, Config{IMDb: func(int) (string, error) { return "tt0903747", nil }})
	s.theIntroDBURL, s.introDBURL = srv.URL+"/the", srv.URL+"/intro"

	r := s.Get(context.Background(), Query{TmdbID: 1396, Season: 1, Episode: 2, Duration: 2900})
	if r.Intro == nil || r.Intro.Source != "theintrodb" || r.Credits != nil {
		t.Fatalf("intro = %+v, credits = %+v", r.Intro, r.Credits)
	}
	var row SkipSegment
	if err := db.First(&row).Error; err != nil || row.Source != "theintrodb" {
		t.Fatalf("saved %+v %v", row, err)
	}
	// The credits IntroDB failed to answer are asked again after missingTTL, not foundTTL
	db.Model(&row).Update("fetched_at", time.Now().Add(-missingTTL-time.Hour))
	if _, ok := s.cached(1396, 1, 2, 2900); ok {
		t.Fatal("a row missing credits after a failure kept for foundTTL")
	}
}
