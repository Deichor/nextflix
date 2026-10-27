package subtitles

import (
	"bytes"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The server names a subtitle served for a file as the players' logs do (subsLog.test.mjs).
func TestDiagSubLogIDLikeThePlayers(t *testing.T) {
	for id, want := range map[string]string{"": "811c9dc5", "a": "e40c292c", "foobar": "bf9cf968"} {
		if got := subLogID(id); got != want {
			t.Errorf("%q: %s, want %s", id, got, want)
		}
	}
	tr := "c3luY3xhMjJmMzdiNDJiMWQzYmE0NDdmYThjZDAxOGU3NDY2ZWFhMGJhMTFmfDF8c3ViZjJtfHR1cmtpc2h8MzU3ODQ0NQ"
	inner, hash, index, ok := unwrapSyncID(tr)
	if !ok || syncLogID(inner, hash, index) != "71abc72f" {
		t.Fatalf("%v %s", ok, syncLogID(inner, hash, index))
	}
}

func cuesOf(spans []span, shift float64) []liveCue {
	out := make([]liveCue, len(spans))
	for i, s := range spans {
		out[i] = liveCue{start: s.start + shift, end: s.end + shift, text: fmt.Sprintf("line %d", i)}
	}
	return out
}

// The residual finds a constant leftover offset of the served cues against the heard speech.
func TestDiagResidualOfServedCues(t *testing.T) {
	file := dialogue(21, 900)
	ws := liveWindowsOf(file, gridStarts(0, 900), 0, 1)
	for _, c := range []struct {
		shift, within float64
	}{{0, 1}, {0.5, 0}, {-0.25, 1}, {-0.7, 0}} {
		r := residualOf(cuesOf(file, c.shift), ws, 300, 480)
		if r.n < 20 || math.Abs(r.median-c.shift) > 0.011 || math.Abs(r.within-c.within) > 0.05 ||
			r.chance > 0.5 {
			t.Errorf("shift %+.2f: %+v", c.shift, r)
		}
	}
	// Speech not heard around a cue: not counted
	if r := residualOf(cuesOf(file, 0), ws[:2], 300, 480); r.n != 0 {
		t.Fatalf("cues outside the heard windows counted: %+v", r)
	}
}

// In a stretch without dialogue (a film's action or music) the residual looks further back.
func TestDiagResidualLooksBackInQuietStretches(t *testing.T) {
	svc := newLiveTest(t, 3600)
	var file []span
	for _, c := range dialogue(5, 3600) {
		if c.start < 420 {
			file = append(file, c)
		}
	}
	inner := cacheProviderSub(t, svc, "1|quiet|2|10|1", file)
	key, _ := syncKey(inner, syncHash, 1)
	injectLive(svc.sync.liveFile(syncHash, 1), file, []int{4, 5, 6, 7, 8, 9, 10, 11})
	a := FileAnswer{VTT: vttOf(file), Partial: true, Sync: syncStateLive}
	line, ok := svc.sync.residualLine(key, inner, syncHash, 1, 650, a, "x")
	if !ok || !strings.Contains(line, " at 650s (350-740s): median ") {
		t.Fatalf("%v %s", ok, line)
	}
	if _, ok := svc.sync.residualLine(key, inner, syncHash, 1, 1500, a, "x"); ok {
		t.Fatal("a residual without speech near the viewer")
	}
}

func TestDiagSpeechInJoinsHeardWindows(t *testing.T) {
	f := newLiveFile(syncHash, 1)
	injectLive(f, dialogue(3, 600), []int{2, 3, 5})
	ws := f.speechIn(130, 340)
	if len(ws) != 2 || ws[0].Start != 120 || len(ws[0].Speech) != 2*liveWindowFrames || ws[1].Start != 300 {
		t.Fatalf("%d windows: %+v", len(ws), ws)
	}
}

func TestDiagMovesSeenAndAhead(t *testing.T) {
	before := []liveCue{{90, 92, "a"}, {96, 101, "b"}, {103, 104, "c"}, {130, 131, "d"}, {30, 31, "e"}}
	after := []liveCue{{90.5, 92.5, "a"}, {96.4, 101.4, "b"}, {103.4, 104.4, "c"}, {130, 131, "d"}, {30.5, 31, "e"}}
	seen := movesSeen(before, after, 100)
	if seen.moved != 2 || seen.max != 0.5 || !seen.onScreen || math.Abs(seen.here-0.4) > 1e-9 {
		t.Fatalf("seen %+v", seen)
	}
	mv := movesNear(before, after, 100, 400)
	if mv.moved != 1 || mv.ahead != 1 || mv.cues != 2 {
		t.Fatalf("near %+v", mv)
	}
	if s := movesSeen(before, after, 95); s.onScreen {
		t.Fatalf("no cue shows at 95 s: %+v", s)
	}
}

// syncBuf is a log sink that the residual's goroutine may write to while the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitLog waits until the log has want (the residual is logged in the background).
func waitLog(t *testing.T, buf *syncBuf, want string) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(buf.String(), want) {
			return buf.String()
		}
	}
	t.Fatalf("no %q in the log:\n%s", want, buf.String())
	return ""
}

// A viewing that gets the subtitle as it is while a pre-timed fit waits says what that fit would
// move, how long the viewer had the raw cues, and how the served cues lie on the heard speech.
func TestDiagObserveAViewing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newLiveTest(t, episode)
	file := dialogue(13, episode)
	raw := subtitleFor(file, 1, func(float64) float64 { return 2.5 })
	inner := cacheProviderSub(t, svc, "1|diag|2|10|1", raw)
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	fit := syncFit{Scale: 1, Segments: []syncSegment{{Offset: 2.5}}, Score: 0.4, Base: 0.1, Peak: 0.2}
	svc.sync.store(key, syncMeta{State: syncStateSynced, Fit: fit, Ref: "live", Live: true, Pre: true}, vttOf(file))
	injectLive(svc.sync.liveFile(syncHash, 1), file, []int{6, 7, 8, 9, 10, 11})
	r := gin.New()
	Register(r.Group("/api"), svc)
	get := func(at float64) string {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/subtitles/file?id=%s&t=%g", id, at), nil))
		return w.Header().Get("X-Subtitle-Sync")
	}
	buf := &syncBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	tag := "id=" + subLogID(id)
	if s := get(500); s != syncStatePending {
		t.Fatalf("pre-timed answer %q", s)
	}
	out := waitLog(t, buf, "subtitles: residual of ")
	if !strings.Contains(out, "subtitles: pending of turkcealtyazi/tr/1|diag|2|10|1 on 73ec17eb:1: its pre-timed fit held back would move ") ||
		!strings.Contains(out, "of the viewer at 500s: max +2.50s, median +2.50s "+tag) ||
		!strings.Contains(out, "(pending: the subtitle as it is) "+tag) {
		t.Fatalf("log:\n%s", out)
	}
	get(510) // once per viewing
	if strings.Count(buf.String(), "pending of") != 1 {
		t.Fatalf("held fit logged again:\n%s", buf.String())
	}
	m := *svc.sync.meta(key)
	m.Pre = false // the played audio backed it
	svc.sync.store(key, m, vttOf(file))
	time.Sleep(20 * time.Millisecond)
	if s := get(570); s != syncStateLive {
		t.Fatalf("timed answer %q", s)
	}
	out = waitLog(t, buf, "(live provisional, offset +2.50s, live reference) "+tag)
	res := regexp.MustCompile(`residual of turkcealtyazi/tr/1\|diag\|2\|10\|1 on 73ec17eb:1 at 570s \(480-660s\): median ([-+.0-9]+)s, (\d+) % within 0.3s \(chance \d+ %\), n=(\d+)`).
		FindStringSubmatch(out)
	if !strings.Contains(out, "subtitles: first timed answer of turkcealtyazi/tr/1|diag|2|10|1 on 73ec17eb:1 at 570s after 0 s of the raw subtitle (from 500s): moved ") ||
		!strings.Contains(out, "max +2.50s, median +2.50s (live) "+tag) || res == nil {
		t.Fatalf("log:\n%s", out)
	}
	med, _ := strconv.ParseFloat(res[1], 64)
	if share, _ := strconv.Atoi(res[2]); math.Abs(med) > 0.12 || share < 80 {
		t.Fatalf("residual of the timed cues: %s", res[0])
	}
	// Requests without a player (another subtitle's reference, a translation source) log nothing
	n := strings.Count(buf.String(), "\n")
	if _, err := svc.FileFor(t.Context(), id, 700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if strings.Count(buf.String(), "\n") != n {
		t.Fatalf("a request without a player was observed:\n%s", buf.String())
	}
}

// The move log says which moved cues the viewer saw already and how the one on screen moved.
func TestDiagLogNearSplitsSeenAndAhead(t *testing.T) {
	svc := newLiveTest(t, episode)
	inner := cacheProviderSub(t, svc, "1|split|2|10|1", dialogue(61, episode))
	key, _ := syncKey(inner, syncHash, 1)
	buf := &syncBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	before := []liveCue{{980, 984, "a"}, {990, 993, "b"}, {1100, 1101, "c"}}
	after := []liveCue{{980.6, 984.6, "a"}, {990.6, 993.6, "b"}, {1101.5, 1102, "c"}}
	svc.sync.noteServed(key, inner, syncHash, 1, 991)
	svc.sync.logNear(key, inner, syncHash, 1, before, after)
	want := "moved 1 of 1 cues within 300 s of the viewer at 991s: max +1.50s, median +1.50s; seen=2 seen_max=+0.60s ahead=1 here=+0.60s id=" +
		syncLogID(inner, syncHash, 1)
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log: %s", buf.String())
	}
}
