// Package playbacklog writes the playback diagnostics the players send (waits, freezes, dropped
// frames, seeks) to daily log files, or to the server log when no log dir is set.
package playbacklog

import (
	"sync"
	"time"
)

const (
	maxBody   = 32 << 10
	maxEvents = 200
	// Per session (sid + client IP): a burst of batches, then one every refillEvery
	burst       = 8
	refillEvery = 4 * time.Second
	// Per client IP, across sessions. Generous because everything proxied through the web
	// container shares its IP; it also bounds how fast new session buckets appear.
	ipBurst       = 64
	ipRefillEvery = 250 * time.Millisecond
	idleAfter     = 10 * time.Minute
	maxBuckets    = 4096
)

type bucket struct {
	tokens float64
	at     time.Time
}

type limiter struct {
	burst  float64
	refill time.Duration
	m      map[string]*bucket
}

func newLimiter(burst float64, refill time.Duration) *limiter {
	return &limiter{burst: burst, refill: refill, m: map[string]*bucket{}}
}

// take removes one token of key's bucket; a new key is refused while the map is full.
func (l *limiter) take(key string, now time.Time) bool {
	b := l.m[key]
	if b == nil {
		if len(l.m) >= maxBuckets/2 {
			for k, old := range l.m {
				if now.Sub(old.at) > idleAfter {
					delete(l.m, k)
				}
			}
		}
		if len(l.m) >= maxBuckets {
			return false
		}
		b = &bucket{tokens: l.burst, at: now}
		l.m[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()/l.refill.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type Service struct {
	mu       sync.Mutex
	ips      *limiter
	sessions *limiter
	now      func() time.Time
	files    *fileLog
}

// NewService also keeps the lines in daily files under dir (none when dir is empty).
func NewService(dir string) *Service {
	return &Service{
		ips:      newLimiter(ipBurst, ipRefillEvery),
		sessions: newLimiter(burst, refillEvery),
		now:      time.Now,
		files:    newFileLog(dir),
	}
}

// allowIP takes one token of the client IP's bucket (checked before the body is read).
func (s *Service) allowIP(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ips.take(ip, s.now())
}

// allow takes one token of the session's bucket.
func (s *Service) allow(ip, sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions.take(ip+"|"+sid, s.now())
}
