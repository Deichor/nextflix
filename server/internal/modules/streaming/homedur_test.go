package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// catalogLike answers availability like the catalog: in cinemas unless a home release is out.
type catalogLike struct{ s *Service }

func (c catalogLike) Availability(_ string, id, _, _ int) TitleAvailability {
	if c.s.HomeReleaseOut(id) {
		return TitleAvailability{Status: "available", ReleaseDate: "2026-07-15"}
	}
	return TitleAvailability{Status: "in_theaters", ReleaseDate: "2026-07-15", DigitalDate: "2026-11-17"}
}

// odysseyService is fakeIndexer's Odyssey plus a TorrServer whose files all probe as minutes long.
func odysseyService(t *testing.T, minutes int) *Service {
	t.Helper()
	var results atomic.Pointer[[]JackettResult]
	var hits atomic.Int32
	all := odysseyResults()
	results.Store(&all)
	s := fakeIndexer(t, &results, &hits)
	var open atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		open.Add(1)
		defer open.Add(-1)
		link := r.URL.Query().Get("link")
		hash := tsHashFromMagnet(link)
		if hash == "" {
			hash = strings.ToLower(link)
		}
		if _, stat := r.URL.Query()["stat"]; stat {
			fmt.Fprintf(w, `{"hash":%q,"name":"The Odyssey","file_stats":[{"id":1,"path":"The Odyssey/the.odyssey.mkv","length":%d}],`+
				`"download_speed":%d,"active_peers":5,"connected_seeders":4}`, hash, 3*tsGB, 20*tsMB)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, tsMB)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if waitOr(r.Context(), 50*time.Millisecond) != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	s.cfg.TorrServerURL = ts.URL
	s.dropFn = func(string) {}
	quiesce(t, s, &open)
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		p := &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "h264", PixFmt: "yuv420p", Width: 1920, Height: 800}}}
		p.Format.Duration = fmt.Sprint(minutes * 60)
		return p, nil
	}
	s.cfg.Availability = catalogLike{s}
	return s
}

// The Odyssey on 2026-10-09: the home check found Kitsune, QxR, LAMA and YTS "WEB-DL"s, but
// every one of them is an 86 min file for a 173 min film. Resolve rejects them, the film is
// in cinemas again (425 in_theaters instead of 404) and the rejected hashes never count again.
func TestWrongLengthHomeReleasesTurnTheVerdict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := odysseyService(t, 86)
	const id = 1368337
	if !s.HomeReleaseOut(id) {
		t.Fatal("the home check did not find the WEB-DLs")
	}
	r := gin.New()
	Register(r.Group("/api"), s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?type=movie&title=The%20Odyssey&year=2026&tmdb_id=1368337", nil))
	var body struct {
		Status      string `json:"status"`
		DigitalDate string `json:"digital_date"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != statusTooEarly || err != nil || body.Status != "in_theaters" ||
		body.DigitalDate != "2026-11-17" {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	if s.HomeReleaseOut(id) {
		t.Fatal("still counted as out")
	}
	rejected := s.home.rejectedHome(id)
	if len(rejected) < tsHomeDurMin {
		t.Fatalf("rejected %v", rejected)
	}
	// The next home check (after the verdict expired) skips them: the one QxR release left
	// is a single group, not a home release
	s.home.mu.Lock()
	s.home.cache[id] = tsHomeVerdict{found: true, expires: time.Now().Add(-time.Second)}
	s.home.mu.Unlock()
	if s.HomeReleaseOut(id) {
		t.Fatal("the rejected releases counted again")
	}
	if a, blocked := s.notOut(odysseyQuery()); !blocked || a.Status != "in_theaters" {
		t.Fatalf("availability %+v", a)
	}
}

// Files of the right length keep the verdict; so does a single wrong one or a wrong one next
// to a right one.
func TestHomeVerdictNeedsSeveralWrongLengths(t *testing.T) {
	s := odysseyService(t, 172)
	if !s.HomeReleaseOut(1368337) {
		t.Fatal("no home release")
	}
	res, seed, err := s.findSource(context.Background(), odysseyQuery())
	if err != nil || !res.DurationVerified {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	seed.cancel()
	if !s.HomeReleaseOut(1368337) {
		t.Fatal("verdict turned with files of the right length")
	}
	if tsHomeMislabeled(1, false) || tsHomeMislabeled(3, true) || !tsHomeMislabeled(2, false) {
		t.Fatal("tsHomeMislabeled")
	}
}

// A .torrent-link-only home release (no hash) of the wrong length neither counts toward the
// verdict nor rejects every hashless release of the movie.
func TestHomeDurationsSkipHashless(t *testing.T) {
	s := NewService(nil, Config{})
	const id = 42
	s.home.cache[id] = tsHomeVerdict{found: true, expires: time.Now().Add(time.Hour)}
	q := tsResolveQuery{mediaType: "movie", title: "Sintel", tmdbID: id}
	web := []string{"1080p", "web", "dl"}
	cands := []tsCandidate{{hash: hx('a'), rest: web}, {rest: web}}
	wrong := &tsDurationError{verdict: "86m for 173m"}
	p := &tsPick{errs: []error{wrong, wrong}, results: make([]*tsResolveResult, 2)}
	if s.noteHomeDurations(q, cands, p) {
		t.Fatal("a hashless release counted toward the verdict")
	}
	cands = append(cands, tsCandidate{hash: hx('b'), rest: web})
	p = &tsPick{errs: []error{wrong, wrong, wrong}, results: make([]*tsResolveResult, 3)}
	if !s.noteHomeDurations(q, cands, p) {
		t.Fatal("two wrong-length releases did not turn the verdict")
	}
	if rejected := s.home.rejectedHome(id); rejected[""] || len(rejected) != 2 {
		t.Fatalf("rejected %v", rejected)
	}
}
