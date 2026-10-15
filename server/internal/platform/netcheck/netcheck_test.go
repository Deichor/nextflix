package netcheck

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func fakeChecker(up *atomic.Bool, calls *atomic.Int32) *Checker {
	return &Checker{
		targets: []string{"a:1", "b:1"},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			calls.Add(1)
			if up.Load() {
				c1, c2 := net.Pipe()
				c2.Close()
				return c1, nil
			}
			return nil, errors.New("unreachable")
		},
	}
}

func TestOnlineCachesAndRechecksOffline(t *testing.T) {
	var up atomic.Bool
	var calls atomic.Int32
	c := fakeChecker(&up, &calls)

	if c.Online(context.Background()) {
		t.Fatal("offline expected")
	}
	n := calls.Load()
	up.Store(true)
	if c.Online(context.Background()) {
		t.Fatal("the offline answer should be cached for a moment")
	}
	if calls.Load() != n {
		t.Fatal("cached answer should not probe")
	}

	// The offline answer expires sooner than an online one
	c.mu.Lock()
	c.at = time.Now().Add(-keepOffline - time.Second)
	c.mu.Unlock()
	if !c.Online(context.Background()) {
		t.Fatal("online expected after the offline answer expired")
	}
	up.Store(false)
	c.mu.Lock()
	c.at = time.Now().Add(-keepOffline - time.Second)
	c.mu.Unlock()
	if !c.Online(context.Background()) {
		t.Fatal("an online answer is kept longer")
	}
}

func TestOnlineUnknownCountsAsOnline(t *testing.T) {
	var up atomic.Bool
	var calls atomic.Int32
	c := fakeChecker(&up, &calls)
	c.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !c.Online(ctx) {
		t.Fatal("no answer yet should not claim offline")
	}
}
