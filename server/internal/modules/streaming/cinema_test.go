package streaming

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The 425 gate of a film in cinemas is skipped only with ?cinema=1; one not released at all
// stays blocked.
func TestCinemaFlagSkipsOnlyTheTheaterGate(t *testing.T) {
	s := NewService(nil, Config{})
	f := &fakeAvailability{a: TitleAvailability{Status: "in_theaters", DigitalDate: "2026-11-17"}}
	s.cfg.Availability = f
	q := odysseyQuery()
	if _, blocked := s.notOut(q); !blocked {
		t.Fatal("in theaters without the flag was searched")
	}
	q.cinema = true
	if _, blocked := s.notOut(q); blocked {
		t.Fatal("the flag did not skip the gate")
	}
	f.a = TitleAvailability{Status: "not_released"}
	if _, blocked := s.notOut(q); !blocked {
		t.Fatal("a film not released at all was searched")
	}
}

// Within the cinema tier the best capture ranks first; bonus material and namesakes stay
// out; without the flag no recording is ranked at all.
func TestCinemaRanking(t *testing.T) {
	raw := append(odysseyResults(),
		odysseyRelease(50, "The Odyssey 2026 1080p HDCAM x264-CAMGRP", 20000, 2.0),
		odysseyRelease(51, "The Odyssey 2026 720p TS x264-TSGRP", 3000, 1.5),
		odysseyRelease(52, "The Odyssey The Making Of An Epic (2026) 1080p HDTS x264", 9000, 1.5),
	)
	q := odysseyQuery()
	q.hevc = true // both 1080p recordings are HEVC
	for _, c := range tsRankAll(raw, q) {
		if c.cinema || tsSourceOf(c.rest) == "cinema" {
			t.Fatalf("a recording was ranked without the flag: %q", c.raw.Title)
		}
	}
	q.cinema = true
	cands := tsRankAll(raw, q)
	pos := map[string]int{}
	for i, c := range cands {
		pos[c.raw.Title] = i
	}
	hdts, telesync := pos[odysseyCinema[1]], pos[odysseyCinema[0]]
	ts, cam := pos["The Odyssey 2026 720p TS x264-TSGRP"], pos["The Odyssey 2026 1080p HDCAM x264-CAMGRP"]
	if max(hdts, telesync) != 1 || ts < 2 || cam <= ts || !cands[0].cinema {
		t.Fatalf("order %q", titles(cands))
	}
	for _, n := range append(odysseyTraps, "The Odyssey The Making Of An Epic (2026) 1080p HDTS x264") {
		if _, ok := pos[n]; ok {
			t.Errorf("trap ranked: %q", n)
		}
	}
	// Home releases still count (a real one passes the duration check), below the recordings
	if p, ok := pos[odysseyHome[0]]; !ok || p < 2 {
		t.Fatalf("home release at %d (%v)", p, ok)
	}
	// Capture order of the source part
	score := func(name string) float64 { return tsCinemaScore(tsNormalize(name), q) }
	order := []string{"1080p HQ HDTS", "1080p TELESYNC", "1080p TS", "1080p HDCAM"}
	for i := 1; i < len(order); i++ {
		if score(order[i-1]) <= score(order[i]) {
			t.Fatalf("%q (%.0f) not above %q (%.0f)", order[i-1], score(order[i-1]), order[i], score(order[i]))
		}
	}
	if score("1080p HDTS HC") >= score("1080p HDTS") {
		t.Fatal("hardcoded subs not below")
	}
	// A dub in the profile's language is fine for a recording
	q.lang = "tr"
	c, ok := tsScore(odysseyRelease(60, "The Odyssey 2026 1080p TELESYNC TR Dublaj x264", 500, 2), q, tsNormalize("The Odyssey"))
	if !ok || c.parts.Lang != tsLangMultiPart {
		t.Fatalf("tr dub: %v %+v", ok, c.parts)
	}
}

// The cache key keeps cinema recordings apart from normal resolves.
func TestCinemaCacheKey(t *testing.T) {
	q := odysseyQuery()
	plain := tsResolveKey(q)
	q.cinema = true
	if tsResolveKey(q) == plain || tsResolveBaseKey(q) != tsResolveBaseKey(odysseyQuery()) {
		t.Fatalf("keys %q %q", plain, tsResolveKey(q))
	}
}

// With the flag a film in cinemas resolves to a recording (cinema: true) and the duration
// check still applies: 86 min files of a 173 min film are rejected.
func TestCinemaResolveKeepsTheDurationCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, c := range []struct {
		minutes int
		query   string
		code    int
	}{
		{172, "", statusTooEarly},
		{172, "&cinema=1", 200},
		{86, "&cinema=1", 404},
	} {
		s := odysseyService(t, c.minutes)
		s.cfg.Availability = &fakeAvailability{a: TitleAvailability{Status: "in_theaters", DigitalDate: "2026-11-17"}}
		r := gin.New()
		Register(r.Group("/api"), s)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?type=movie&title=The%20Odyssey&year=2026&tmdb_id=1368337"+c.query, nil))
		if w.Code != c.code {
			t.Fatalf("%dm%s: %d %s", c.minutes, c.query, w.Code, w.Body.String())
		}
		if c.code != 200 {
			continue
		}
		var res tsResolveResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !res.Cinema {
			t.Fatalf("answer %s", w.Body.String())
		}
	}
}
