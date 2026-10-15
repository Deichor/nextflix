// Package netcheck tells whether the server can reach the internet, so a failure caused by a
// dropped connection can be told apart from "nothing found" and shown as such.
package netcheck

import (
	"context"
	"log"
	"net"
	"sync"
	"time"
)

const (
	dialTimeout = 2500 * time.Millisecond
	keepOnline  = 30 * time.Second // an online answer is trusted this long
	keepOffline = 4 * time.Second  // an offline one is checked again soon
)

// Public resolvers on 443: plain TCP, no DNS, so a hijacked or dead resolver does not matter.
var defaultTargets = []string{"1.1.1.1:443", "8.8.8.8:443", "9.9.9.9:443"}

// Checker probes the internet on demand and caches the answer.
type Checker struct {
	targets []string
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)

	mu      sync.Mutex
	known   bool
	online  bool
	at      time.Time
	probing chan struct{}
}

func New() *Checker {
	d := &net.Dialer{Timeout: dialTimeout}
	return &Checker{targets: defaultTargets, dial: d.DialContext}
}

// Online reports whether any probe target answered recently. Concurrent callers share one
// probe; a caller whose ctx ends first gets the last known answer (online when none).
func (c *Checker) Online(ctx context.Context) bool {
	c.mu.Lock()
	if c.known && time.Since(c.at) < c.ttl() {
		on := c.online
		c.mu.Unlock()
		return on
	}
	wait := c.probing
	if wait == nil {
		wait = make(chan struct{})
		c.probing = wait
		go c.probe(wait)
	}
	c.mu.Unlock()

	select {
	case <-wait:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.known || c.online
}

func (c *Checker) ttl() time.Duration {
	if c.online {
		return keepOnline
	}
	return keepOffline
}

func (c *Checker) probe(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout+500*time.Millisecond)
	defer cancel()
	ok := make(chan bool, len(c.targets))
	for _, t := range c.targets {
		go func(addr string) {
			conn, err := c.dial(ctx, "tcp", addr)
			if err == nil {
				conn.Close()
			}
			ok <- err == nil
		}(t)
	}
	on := false
	for range c.targets {
		if <-ok {
			on = true
			break
		}
	}

	c.mu.Lock()
	if c.known && c.online != on {
		if on {
			log.Println("netcheck: the internet is reachable again")
		} else {
			log.Println("netcheck: the internet is unreachable")
		}
	}
	c.known, c.online, c.at, c.probing = true, on, time.Now(), nil
	c.mu.Unlock()
	close(done)
}
