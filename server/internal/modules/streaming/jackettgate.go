package streaming

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Jackett keeps working on a request after its client left, and the indexers behind
// FlareSolverr answer one at a time (~10 s each). Requests left behind by a deadline pile up:
// on 2026-11-04 a 1337x answer took 2.8 hours and Jackett then stopped accepting connections,
// so resolves found only what Bitsearch had. Nearly all of that load were instant-play checks.
// So a request runs until Jackett answers, whoever still waits for it, and what an indexer
// still has open is limited; over the limit a query is skipped, not queued.

const (
	tsJackettReqTime = 3 * time.Minute // longer than the queue the limits below allow

	tsJackettMaxSlow   = 12 // open requests to slow indexers, all together (they share FlareSolverr)
	tsJackettMaxSlowBg = 2  // ... of them for background checks
	tsJackettMaxFast   = 8  // open requests to one fast indexer
	tsJackettMaxFastBg = 4  // ... of them for background checks
)

var errIndexerBusy = errors.New("indexer has too many open requests")

type tsJackettGate struct {
	mu           sync.Mutex
	fast         map[string]int
	fastBg       map[string]int
	slow, slowBg int
}

func newJackettGate() *tsJackettGate {
	return &tsJackettGate{fast: map[string]int{}, fastBg: map[string]int{}}
}

// take reserves a request; the returned func gives it back. ok is false over the limit.
func (g *tsJackettGate) take(id string, slow, background bool) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if slow {
		if g.slow >= tsJackettMaxSlow || (background && g.slowBg >= tsJackettMaxSlowBg) {
			return nil, false
		}
		g.slow++
		if background {
			g.slowBg++
		}
		return func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.slow--
			if background {
				g.slowBg--
			}
		}, true
	}
	if g.fast[id] >= tsJackettMaxFast || (background && g.fastBg[id] >= tsJackettMaxFastBg) {
		return nil, false
	}
	g.fast[id]++
	if background {
		g.fastBg[id]++
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.fast[id]--
		if background {
			g.fastBg[id]--
		}
	}, true
}

// do runs call detached from ctx, under the limits. The caller stops waiting when ctx ends;
// the request goes on and frees its place once Jackett answered.
func (g *tsJackettGate) do(ctx context.Context, id string, slow, background bool,
	call func(context.Context) ([]JackettResult, error)) ([]JackettResult, error) {
	release, ok := g.take(id, slow, background)
	if !ok {
		return nil, errIndexerBusy
	}
	done := make(chan tsQueryDone, 1)
	go func() {
		defer release()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tsJackettReqTime)
		defer cancel()
		res, err := call(rctx)
		done <- tsQueryDone{id: id, res: res, err: err}
	}()
	select {
	case d := <-done:
		return d.res, d.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
