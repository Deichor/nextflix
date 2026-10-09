// Package playbacklog writes the playback diagnostics the players send (waits, freezes, dropped
// frames, seeks) to the server log as "playback: ..." lines. Nothing is stored.
package playbacklog

import (
	"sync"
	"time"
)

const (
	maxBody   = 32 << 10
	maxEvents = 200
	// Per client IP: a burst of batches, then one every refillEvery
	burst       = 8
	refillEvery = 4 * time.Second
	idleAfter   = 10 * time.Minute
)

type bucket struct {
	tokens float64
	at     time.Time
}

type Service struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
	files   *fileLog
}

// NewService also keeps the lines in daily files under dir (none when dir is empty).
func NewService(dir string) *Service {
	return &Service{buckets: map[string]*bucket{}, now: time.Now, files: newFileLog(dir)}
}

// allow takes one token of ip's bucket.
func (s *Service) allow(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.buckets) > 256 {
		for k, b := range s.buckets {
			if now.Sub(b.at) > idleAfter {
				delete(s.buckets, k)
			}
		}
	}
	b := s.buckets[ip]
	if b == nil {
		b = &bucket{tokens: burst, at: now}
		s.buckets[ip] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.at).Seconds()/refillEvery.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
