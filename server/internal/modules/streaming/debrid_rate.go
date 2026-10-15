package streaming

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// TorBox line speed. A cached file plays at line speed through the chunked proxy
// (debrid_proxy.go), but one short cold read underrates it: a fresh CDN connection starts
// slow and the proxy reads ahead in parallel only once a reader proved sequential (GoT
// S01E01 4K: 2.28 MB/s in the resolve check, 14.74 MB/s a minute later). So a TorBox file is
// measured with a few parallel bounded reads (debridSpeed), and the best rate TorBox
// delivered recently (measurements, and the proxy's own CDN reads per tsLineBucket) counts
// when a measurement came out low although its first bytes arrived fast (debridHealth).

const (
	tsDebridSpeedTime  = 2500 * time.Millisecond // parallel read after the first byte
	tsDebridSpeedSkip  = 500 * time.Millisecond  // connection ramp left out of the rate
	tsDebridSpeedLanes = 3                       // parallel reads
	tsDebridSpeedLane  = 16 << 20                // bytes per read (a bounded range: no read-ahead past it)
	tsDebridFastFirst  = 2 * time.Second         // first bytes this fast: the CDN answers normally
	tsLineFresh        = 10 * time.Minute        // line rates older than this are forgotten
	tsLineBucket       = 2 * time.Second         // the proxy's CDN bytes are summed per bucket
)

// tsDebridLine remembers the rates TorBox delivered lately.
type tsDebridLine struct {
	mu      sync.Mutex
	samples []tsLineSample // oldest first
	bucket  int64          // start of the running proxy bucket (unix ns / tsLineBucket)
	bytes   int64          // CDN bytes in it
}

type tsLineSample struct {
	rate int64
	at   time.Time
}

func (l *tsDebridLine) note(rate int64, at time.Time) {
	if rate <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(at)
	l.samples = append(l.samples, tsLineSample{rate, at})
}

func (l *tsDebridLine) pruneLocked(now time.Time) {
	i := 0
	for i < len(l.samples) && now.Sub(l.samples[i].at) > tsLineFresh {
		i++
	}
	l.samples = l.samples[i:]
}

// best is the highest rate within tsLineFresh (0 = none).
func (l *tsDebridLine) best(now time.Time) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	var b int64
	for _, s := range l.samples {
		b = max(b, s.rate)
	}
	return b
}

// fetched counts n bytes the proxy got from the CDN; a finished bucket becomes a sample.
func (l *tsDebridLine) fetched(n int, now time.Time) {
	b := now.UnixNano() / int64(tsLineBucket)
	l.mu.Lock()
	if b != l.bucket {
		done := l.bytes
		l.bucket, l.bytes = b, 0
		if done > 0 {
			l.pruneLocked(now)
			l.samples = append(l.samples, tsLineSample{int64(float64(done) / tsLineBucket.Seconds()), now})
		}
	}
	l.bytes += int64(n)
	l.mu.Unlock()
}

// debridHealth measures a TorBox file (debridSpeed) and judges it with the line: a low
// measurement whose first bytes came fast keeps up when TorBox recently delivered
// tsKeepUpFactor times the file's need (the cold read was the slow part, not TorBox).
func (s *Service) debridHealth(ctx context.Context, hash string, index int, size, offset, need int64, seeders int) *tsHealth {
	h, first, cached := s.debridSpeed(ctx, hash, index, size, offset, need, seeders)
	if h == nil {
		return nil
	}
	// Bytes the proxy had cached say nothing about the line (the proxy's CDN reads count it)
	if !cached {
		s.debrid.line.note(h.Rate, time.Now())
		s.lineTorBox(hash, h.Rate) // the same home line (linerate.go)
	}
	if h.KeepsUp {
		return h
	}
	if line := s.debrid.line.best(time.Now()); first <= tsDebridFastFirst && float64(line) >= tsKeepUpFactor*float64(need) {
		h.series += fmt.Sprintf("; TorBox line %.2f MB/s", float64(line)/float64(tsMB))
		h.Rate, h.KeepsUp = line, true
		h.Peak = max(h.Peak, line)
	}
	return h
}

// debridSpeed reads tsDebridSpeedLanes bounded ranges of the file from offset through the
// proxy in parallel for tsDebridSpeedTime after the first byte (or until they are read). The
// rate leaves out the first tsDebridSpeedSkip (connection ramp); peak is the best 1 s. It
// returns nil when nothing arrived, the time to the first byte, and whether most bytes read
// came from the proxy's cache rather than the CDN.
func (s *Service) debridSpeed(ctx context.Context, hash string, index int, size, offset, need int64, seeders int) (*tsHealth, time.Duration, bool) {
	base, err := s.debrid.proxy()
	if err != nil || size <= 0 {
		return nil, 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, tsDebridSpeedTime+8*time.Second)
	defer cancel()
	started := time.Now()
	var total atomic.Int64
	var laneRead [tsDebridSpeedLanes]atomic.Int64
	var laneCached [tsDebridSpeedLanes]int64 // bytes of each lane cached before the read
	firstCh := make(chan struct{})
	var firstOnce sync.Once
	var wg sync.WaitGroup
	for k := int64(0); k < tsDebridSpeedLanes; k++ {
		from := offset + k*tsDebridSpeedLane
		if from >= size {
			break
		}
		to := min(from+tsDebridSpeedLane, size) - 1
		laneCached[k] = s.debrid.cachedFrom(hash, index, from)
		read := &laneRead[k]
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/f/%s/%d", base, hash, index), nil)
			if err != nil {
				return
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				return
			}
			buf := make([]byte, 128<<10)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					firstOnce.Do(func() { close(firstCh) })
					total.Add(int64(n))
					read.Add(int64(n))
				}
				if err != nil {
					return
				}
			}
		}()
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	defer func() { cancel(); <-allDone }()

	select {
	case <-firstCh:
	case <-allDone:
		return nil, 0, false
	case <-ctx.Done():
		return nil, 0, false
	}
	first := time.Now()
	ttfb := first.Sub(started)
	type sample struct {
		at   time.Duration
		read int64
	}
	samples := []sample{{0, total.Load()}}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for over := false; !over; {
		select {
		case <-tick.C:
		case <-allDone:
			over = true
		case <-ctx.Done():
			return nil, ttfb, false
		}
		at := time.Since(first)
		samples = append(samples, sample{at, total.Load()})
		over = over || at >= tsDebridSpeedTime
	}
	last := samples[len(samples)-1]
	took := max(last.at, 50*time.Millisecond)
	rate := float64(last.read) / took.Seconds()
	var peak float64
	for i, a := range samples {
		// The steady rate after the ramp, when it covers a second
		if a.at >= tsDebridSpeedSkip && i > 0 && samples[i-1].at < tsDebridSpeedSkip && last.at-a.at >= time.Second {
			rate = max(rate, float64(last.read-a.read)/(last.at-a.at).Seconds())
		}
		for _, b := range samples[i+1:] {
			if b.at-a.at >= time.Second {
				peak = max(peak, float64(b.read-a.read)/(b.at-a.at).Seconds())
				break
			}
		}
	}
	h := newHealth(int64(rate), need, 0, seeders)
	h.Peak = int64(max(peak, rate))
	h.sampled = last.at
	h.series = fmt.Sprintf("%d reads, first byte %s, %.1f MB in %s", min(tsDebridSpeedLanes, (size-offset+tsDebridSpeedLane-1)/tsDebridSpeedLane),
		ttfb.Round(10*time.Millisecond), float64(last.read)/float64(tsMB), last.at.Round(10*time.Millisecond))
	var fromCDN, read int64
	for k := range laneRead {
		n := laneRead[k].Load()
		read += n
		fromCDN += max(0, n-laneCached[k])
	}
	return h, ttfb, fromCDN*2 < read
}

// debridStreams: hash plays from TorBox's CDN now (a registered source, TorBox usable, its
// reads not given up for any of its files), so reading it never competes with a swarm.
func (s *Service) debridStreams(hash string) bool {
	d := s.debrid
	if !d.usable() {
		return false
	}
	src, ok := d.hashSource(hash)
	if !ok {
		return false
	}
	d.mu.Lock()
	var indexes []int
	if t := d.sources[src.hash]; t != nil {
		for idx := range t.files {
			indexes = append(indexes, idx)
		}
	}
	d.mu.Unlock()
	for _, idx := range indexes {
		if d.isBroken(src.hash, idx) {
			return false
		}
	}
	return true
}

// tsHealthSeries is the measurement detail of h for a log line ("" when there is none).
func tsHealthSeries(h *tsHealth) string {
	if h == nil || h.series == "" {
		return ""
	}
	return " [" + h.series + "]"
}
