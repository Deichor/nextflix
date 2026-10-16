package subtitles

import (
	"context"
	"sync"
	"testing"
	"time"
)

// gateFake holds the reads until open is closed and records the files that asked.
type gateFake struct {
	mu     sync.Mutex
	open   chan struct{}
	hashes []string
}

func (g *gateFake) WaitStartQuiet(ctx context.Context, hash string) bool {
	g.mu.Lock()
	g.hashes = append(g.hashes, hash)
	g.mu.Unlock()
	select {
	case <-g.open:
		return true
	case <-ctx.Done():
		return false
	}
}

func (g *gateFake) asked() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.hashes)
}

// The window reads wait in the LineGate while a stream starts; once it opens the reference is
// read as before, every window after the gate.
func TestWindowReadsWaitForTheLine(t *testing.T) {
	file := dialogue(11, episode)
	svc, reads := newSyncTest(t, file)
	g := &gateFake{open: make(chan struct{})}
	svc.cfg.LineGate = g
	type out struct {
		r   syncRef
		err error
	}
	done := make(chan out, 1)
	go func() {
		r, err := svc.sync.reference(context.Background(), syncHash, 1)
		done <- out{r, err}
	}()
	time.Sleep(50 * time.Millisecond)
	if g.asked() != 1 || *reads != 0 {
		t.Fatalf("gate asked %d times, %d windows read while a stream starts", g.asked(), *reads)
	}
	close(g.open)
	o := <-done
	if o.err != nil || len(o.r.Windows) == 0 || len(o.r.Windows) != *reads {
		t.Fatalf("reference %d windows, %v", len(o.r.Windows), o.err)
	}
	if g.asked() != *reads || g.hashes[0] != syncHash {
		t.Fatalf("gate asked %d times (%q) for %d reads", g.asked(), g.hashes[0], *reads)
	}
	// A wait that ends with its context fails the read instead of reading
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g2 := &gateFake{open: make(chan struct{})}
	svc.cfg.LineGate = g2
	if _, _, err := svc.sync.readWindow(ctx, syncHash, "http://torrserver", o.r.Probe, 20, 0); err == nil {
		t.Fatal("a cancelled wait read the window")
	}
}

// The waits of one job share syncLineWait: once it is used up (seeks keep starting streams),
// the reads go on without waiting, so the job does not run out of time.
func TestLineWaitBudgetIsShared(t *testing.T) {
	file := dialogue(11, episode)
	svc, reads := newSyncTest(t, file)
	g := &gateFake{open: make(chan struct{})} // never opens
	svc.cfg.LineGate = g
	svc.sync.lineWait = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	r, err := svc.sync.reference(svc.sync.withLineBudget(ctx), syncHash, 1)
	took := time.Since(start)
	if err != nil || len(r.Windows) == 0 || *reads != len(r.Windows) {
		t.Fatalf("reference %d windows (%d reads), %v", len(r.Windows), *reads, err)
	}
	if took < 90*time.Millisecond || took > 2*time.Second || g.asked() != 1 {
		t.Fatalf("waited %s, gate asked %d times; want one wait of the whole budget", took, g.asked())
	}
}
