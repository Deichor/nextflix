package segments

import (
	"math"
	"time"
)

// Community lookups per client IP: a burst (an evening of episodes and source switches), then
// one every lookupRefill, so one device cannot spend TheIntroDB's 500 reads a day.
const (
	lookupBurst   = 10
	lookupRefill  = 6 * time.Minute
	lookupIdle    = 2 * time.Hour
	lookupMaxIPs  = 1024
	shortestEp    = 60          // seconds; shorter lengths get no community lookup
	longestEp     = 4 * 60 * 60 // longer ones neither
	durationRound = 2 * durationSlack
)

type bucket struct {
	tokens float64
	at     time.Time
}

// ipLimiter is a token bucket per client IP. Not safe for concurrent use (Service.mu guards it).
type ipLimiter struct {
	m map[string]*bucket
}

// take removes one token of ip's bucket; a new ip is refused while the map is full.
func (l *ipLimiter) take(ip string, now time.Time) bool {
	if l.m == nil {
		l.m = map[string]*bucket{}
	}
	b := l.m[ip]
	if b == nil {
		if len(l.m) >= lookupMaxIPs {
			for k, old := range l.m {
				if now.Sub(old.at) > lookupIdle {
					delete(l.m, k)
				}
			}
			if len(l.m) >= lookupMaxIPs {
				return false
			}
		}
		b = &bucket{tokens: lookupBurst, at: now}
		l.m[ip] = b
	}
	b.tokens = min(lookupBurst, b.tokens+now.Sub(b.at).Seconds()/lookupRefill.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// lookupLength rounds a file length to the one its community lookup is cached under; every
// length within durationSlack of it shares that lookup.
func lookupLength(duration float64) int {
	return int(math.Round(duration/durationRound)) * durationRound
}
