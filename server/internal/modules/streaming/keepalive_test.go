package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestKeepAliveGuardsAPausedPlayersTorrent(t *testing.T) {
	s := NewService(memDB(t), Config{TorrServerURL: "http://127.0.0.1:1"})
	hash := strings.Repeat("ab", 20)
	// The stream died during a long pause: without keep-alives the guard lets go after a minute
	s.playBegin(hash)
	s.playEnd(hash)
	later := time.Now().Add(5 * time.Minute)
	s.warmMu.Lock()
	free := s.streamBusyLocked(hash, later)
	s.warmMu.Unlock()
	if free != "" {
		t.Fatalf("guarded without a keep-alive: %q", free)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/keepalive?hash="+hash+"&index=1", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("keepalive: %d", w.Code)
	}
	s.warmMu.Lock()
	why := s.streamBusyLocked(hash, time.Now().Add(30*time.Second))
	s.warmMu.Unlock()
	if why == "" {
		t.Fatal("a paused player's torrent is not guarded")
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/keepalive?hash=nope&index=1", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad hash: %d", w.Code)
	}
}

func TestDebridBrokenFilePlaysFromTorrServer(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	if u := s.fileURL(tbHash, 1); !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("fileURL %q, want the proxy", u)
	}
	s.debrid.markBroken(tbHash, 1)
	if u := s.fileURL(tbHash, 1); !strings.HasPrefix(u, "http://127.0.0.1:1/stream?link="+tbHash) {
		t.Fatalf("fileURL %q, want TorrServer while broken", u)
	}
	s.debrid.mu.Lock()
	s.debrid.broken[tsDebridKey(tbHash, 1)] = time.Now().Add(-tsDebridBrokenFor)
	s.debrid.mu.Unlock()
	if u := s.fileURL(tbHash, 1); !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("fileURL %q, want the proxy again", u)
	}
}
