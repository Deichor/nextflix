package streaming

import (
	"context"
	"testing"
	"time"
)

func TestDebridLineKeepsTheBestRecentRate(t *testing.T) {
	var l tsDebridLine
	now := time.Now()
	l.note(5*tsMB, now.Add(-tsLineFresh-time.Second))
	l.note(3*tsMB, now.Add(-time.Minute))
	l.note(9*tsMB, now.Add(-2*time.Minute))
	if b := l.best(now); b != 9*tsMB {
		t.Fatalf("best %d, want 9 MB/s (the stale 5 MB/s one is forgotten too)", b)
	}
	if b := l.best(now.Add(tsLineFresh)); b != 0 {
		t.Fatalf("best after tsLineFresh %d", b)
	}
	// The proxy's CDN bytes are summed per bucket; a finished bucket is a sample
	var p tsDebridLine
	start := time.Unix(0, (now.UnixNano()/int64(tsLineBucket)+1)*int64(tsLineBucket))
	p.fetched(int(20*tsMB), start)
	p.fetched(int(20*tsMB), start.Add(tsLineBucket/2))
	p.fetched(1, start.Add(tsLineBucket))
	if b := p.best(start.Add(tsLineBucket)); b != 20*tsMB {
		t.Fatalf("proxy bucket rate %d, want 20 MB/s", b)
	}
}

// A measurement below the need keeps up when TorBox delivered enough lately and its first
// bytes came fast; without such a rate it stays a measured (not unmeasured) starving one.
func TestDebridHealthUsesTheLine(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	size := int64(len(f.content))
	need := 1000 * tsMB // far above what the fake CDN's 3 MB give in one read
	h := s.debridHealth(context.Background(), tbHash, 1, size, 0, need, 3)
	if h == nil || h.KeepsUp || h.Rate <= 0 || h.Peak <= 0 {
		t.Fatalf("without a line rate: %v", h)
	}
	s.debrid.line.note(2*need, time.Now())
	h = s.debridHealth(context.Background(), tbHash, 1, size, 0, need, 3)
	if h == nil || !h.KeepsUp || h.Rate < int64(tsKeepUpFactor*float64(need)) {
		t.Fatalf("with a line rate: %v", h)
	}
}

// Background work on a TorBox file goes on beside a starting TorBox stream, but still waits
// for a starting torrent.
func TestQuietLetsTorBoxBesideTorBox(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	other := "aa8255ecdc7ca55fb0bbf81323d87062db1f6d1c"
	s.debrid.register(tsDebridSource{hash: other, index: 1, path: tbVideo, size: 3 << 20, title: "Movie (2020) 4K"})
	ctx := tsBackground(context.Background())
	s.playBegin(tbHash)
	defer s.playEnd(tbHash)
	if o, _ := s.yieldReason(ctx, other); o != "" {
		t.Fatalf("a TorBox warm-up waits for the starting TorBox stream %s", o)
	}
	if o, _ := s.yieldReason(ctx, hx('t')); o != tbHash {
		t.Fatalf("a torrent's warm-up does not wait for the starting stream: %q", o)
	}
	s.playBegin(hx('p'))
	defer s.playEnd(hx('p'))
	if o, why := s.yieldReason(ctx, other); o != hx('p') || why != "starts" {
		t.Fatalf("a TorBox warm-up does not wait for a starting torrent: %q %q", o, why)
	}
}
