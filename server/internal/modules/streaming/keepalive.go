package streaming

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// keepAlive: a paused player keeps its file ready (POST /keepalive?hash=&index=, every ~15 s).
// The torrent counts as playing for the stream guard and playback priority, as if its stream
// had just ended, its title's hold is renewed, and a TorBox file keeps a fresh link and a live
// CDN connection, so the restart after a long pause starts at once.
func (h handler) keepAlive(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	h.s.pausedPlayer(hash, time.Now())
	h.s.debrid.keepWarm(hash, index)
	c.Status(http.StatusNoContent)
}

func (s *Service) pausedPlayer(hash string, now time.Time) {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	s.lastUse[hash] = now
	if s.playing[hash] == 0 {
		s.streamEnd[hash] = now
		s.playEndAt[hash] = now
	}
	for _, w := range s.warms {
		if w.hashes[hash] {
			w.holdUntil = now.Add(tsHoldTime)
			w.releaseAt = time.Time{}
		}
	}
}
