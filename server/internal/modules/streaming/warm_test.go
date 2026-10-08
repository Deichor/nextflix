package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fastWarm shortens the warm-up timings 20x for tests that go through HTTP.
func fastWarm(t *testing.T) {
	poll, skip, sustain, fast, span, dead := tsWarmPoll, tsWarmSkip, tsWarmSustain, tsWarmFastSpan, tsWarmSpan, tsWarmDeadAfter
	tsWarmPoll, tsWarmSkip, tsWarmSustain, tsWarmFastSpan, tsWarmSpan, tsWarmDeadAfter =
		50*time.Millisecond, 75*time.Millisecond, 150*time.Millisecond, 100*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() {
		tsWarmPoll, tsWarmSkip, tsWarmSustain, tsWarmFastSpan, tsWarmSpan, tsWarmDeadAfter = poll, skip, sustain, fast, span, dead
	})
}

// samplesOf builds 1 s polls from per-second rates (MB/s); speed is TorrServer's report.
func samplesOf(rates []float64, speed func(sec int, rate float64) float64) []tsWarmSample {
	var out []tsWarmSample
	read := 0.0
	for i, r := range rates {
		read += r * mbps
		out = append(out, tsWarmSample{at: time.Duration(i+1) * time.Second, read: int64(read), speed: speed(i+1, r) * mbps, peers: 5})
	}
	return out
}

// rampSamples simulates a cold swarm: 1 s polls, the rate climbs linearly from 0 to peak
// MB/s within rampSec, no peers for the first two seconds.
func rampSamples(peak float64, rampSec, total int) []tsWarmSample {
	var out []tsWarmSample
	read := 0.0
	for s := 1; s <= total; s++ {
		rate := peak * float64(min(s, rampSec)) / float64(rampSec) // MB/s during second s
		read += rate * mbps
		peers := 0
		if s > 2 {
			peers = 4
		}
		out = append(out, tsWarmSample{at: time.Duration(s) * time.Second, read: int64(read), speed: rate * mbps, peers: peers})
	}
	return out
}

func TestWarmMeterRampKeepsUp(t *testing.T) {
	// GoT S01E01 2160p: 3.94 MB/s needed would fail cold; here 2 MB/s needed (2.6 to keep up)
	need := int64(2 * mbps)
	m := newWarmMeter(need)
	doneAt := 0
	for i, s := range rampSamples(5, 10, 15) {
		done := m.add(s)
		if i+1 == 5 && (done || m.health(need).KeepsUp) {
			t.Fatal("the old 5 s cold check would already keep up; the ramp does not show the warm-up")
		}
		if done {
			doneAt = i + 1
			break
		}
	}
	h := m.health(need)
	// The rate delivered after the start burst (2-8 s) reaches 2.6 MB/s after 8 s
	if doneAt != 8 || !h.KeepsUp || h.Dead {
		t.Fatalf("ramp to 5 MB/s within 10 s: done after %d s, %v", doneAt, h)
	}
}

func TestWarmMeterBurstIsNotHealth(t *testing.T) {
	// The Little Shop of Horrors 1960 1080p: the best 3 s said 1.05 MB/s (x0.82), playback got
	// about 0.22 MB/s. A read-ahead burst in the first seconds, then the real swarm.
	need := int64(1342177) // 1.28 MB/s
	rates := []float64{3.5, 2.5, 0.9, 0.25, 0.2, 0.22, 0.2, 0.25, 0.2, 0.22, 0.2, 0.21, 0.22, 0.2, 0.23}
	m := newWarmMeter(need)
	for _, s := range samplesOf(rates, func(_ int, r float64) float64 { return r }) {
		if m.add(s) {
			t.Fatalf("stopped at %s on a burst", s.at)
		}
	}
	h := m.health(need)
	if h.KeepsUp || float64(h.Rate) > 0.3*mbps || h.ratio() > 0.25 {
		t.Fatalf("burst judged as %v, want about 0.22 MB/s", h)
	}
	if float64(h.Peak) < 1.0*mbps {
		t.Fatalf("peak %v should still show the burst", h)
	}

	// One TorrServer speed spike in the middle of a thin swarm changes nothing either
	m = newWarmMeter(need)
	spiky := samplesOf(make([]float64, 15), func(sec int, _ float64) float64 {
		if sec == 9 {
			return 6
		}
		return 0.3
	})
	for _, s := range spiky {
		m.add(s)
	}
	if h := m.health(need); h.KeepsUp || float64(h.Rate) > 0.35*mbps {
		t.Fatalf("speed spike judged as %v", h)
	}
}

func TestWarmMeterSteadySwarmStopsEarly(t *testing.T) {
	// 3 MB/s from the start for a file needing 1 MB/s: done once 3 s after the skip are in
	need := int64(mbps)
	m := newWarmMeter(need)
	doneAt := 0
	for _, s := range samplesOf([]float64{3, 3, 3, 3, 3, 3, 3, 3}, func(_ int, r float64) float64 { return r }) {
		if m.add(s) {
			doneAt = int(s.at / time.Second)
			break
		}
	}
	if h := m.health(need); doneAt != 5 || !h.KeepsUp {
		t.Fatalf("done after %d s: %v", doneAt, h)
	}
}

func TestWarmMeterDeadSwarm(t *testing.T) {
	need := int64(mbps)
	m := newWarmMeter(need)
	doneAt := 0
	for s := 1; s <= 15; s++ {
		if m.add(tsWarmSample{at: time.Duration(s) * time.Second, speed: 0, peers: 0}) {
			doneAt = s
			break
		}
	}
	if h := m.health(need); doneAt != 8 || !h.Dead || h.KeepsUp {
		t.Fatalf("dead swarm: done after %d s, %v", doneAt, h)
	}

	// Peers but no data yet is starving, not dead
	m = newWarmMeter(need)
	for s := 1; s <= 15; s++ {
		if m.add(tsWarmSample{at: time.Duration(s) * time.Second, peers: 2}) {
			t.Fatalf("stopped after %d s on a swarm with peers", s)
		}
	}
	if h := m.health(need); h.Dead || h.KeepsUp {
		t.Fatalf("swarm with peers: %v", h)
	}
}

// rampTorrServer serves a file whose rate climbs from 0 to peak bytes/s within ramp; its
// stat reports no peers (dead) or 3.
func rampTorrServer(peak float64, ramp time.Duration, dead bool) *httptest.Server {
	start := time.Now()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["stat"]; ok {
			peers := 3
			if dead {
				peers = 0
			}
			fmt.Fprintf(w, `{"download_speed":0,"active_peers":%d,"connected_seeders":1}`, peers)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		for {
			share := min(1, float64(time.Since(start))/float64(ramp))
			if n := int(peak * share / 50); n > 0 && !dead {
				if _, err := w.Write(make([]byte, n)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
			select {
			case <-time.After(20 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
}

func TestMeasureWarmOverHTTP(t *testing.T) {
	fastWarm(t)
	hash := strings.Repeat("a", 40)
	need := int64(mbps) // keeps up from 1.3 MB/s

	// Cold start: nothing for a while, then 5 MB/s
	ts := rampTorrServer(5*mbps, 500*time.Millisecond, false)
	defer ts.Close()
	alive := false
	h := NewService(nil, Config{TorrServerURL: ts.URL}).measureWarm(context.Background(), hash, 1, 0, need, 1500*time.Millisecond, tsWarmHooks{alive: func() { alive = true }})
	if h == nil || !h.KeepsUp || !alive {
		t.Fatalf("warming swarm: %v (alive %v)", h, alive)
	}

	dead := rampTorrServer(5*mbps, time.Millisecond, true)
	defer dead.Close()
	start := time.Now()
	h = NewService(nil, Config{TorrServerURL: dead.URL}).measureWarm(context.Background(), hash, 1, 0, need, 3*time.Second, tsWarmHooks{})
	if h == nil || !h.Dead || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("dead swarm: %v after %s", h, time.Since(start))
	}
}

func TestMidOffsetMovesOn(t *testing.T) {
	size := 20 * tsGB
	seen := map[int64]bool{}
	for round := 0; round < 6; round++ {
		off := tsMidOffset(size, round)
		if off < size/20 || off > size*95/100 || seen[off] || off%(1<<20) != 0 {
			t.Fatalf("round %d: offset %d", round, off)
		}
		seen[off] = true
	}
	if tsMidOffset(size, 0) != (size*45/100)&^(1<<20-1) {
		t.Fatal("the first measurement reads at 45 %")
	}
}

// seriesSamples builds 1 s polls from a logged series ("read/speed" in MB/s per poll).
func seriesSamples(t *testing.T, series string) []tsWarmSample {
	t.Helper()
	var out []tsWarmSample
	read := 0.0
	for i, f := range strings.Fields(series) {
		var r, sp float64
		if _, err := fmt.Sscanf(f, "%f/%f", &r, &sp); err != nil {
			t.Fatalf("series %q: %v", f, err)
		}
		read += r * mbps
		out = append(out, tsWarmSample{at: time.Duration(i+1) * time.Second, read: int64(read), speed: sp * mbps, peers: 3})
	}
	return out
}

// Warm-ups recorded on the live stack (health log series): the verdict and when it came.
func TestWarmMeterRecordedSeries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		need   float64 // MB/s
		series string
		keeps  bool
		doneAt int // seconds, 0 = ran to the end
	}{
		// Bursty reads: 0 MB/s in one second, 15 the next; far above the need it decides after 4 s
		{"Charade BHDStudio", 1.01, "6.09/18.79 23.44/23.42 0.00/19.48 15.33/1.80 4.58/2.11 2.11/2.39", true, 4},
		// The mid-file reader still waits for its first piece while the swarm delivers to TorrServer
		{"Charade Criterion", 0.70, "0.00/0.00 0.00/0.00 0.00/0.09 0.00/1.50 0.00/4.82 0.00/9.06", true, 5},
		{"His Girl Friday YTS", 0.27, "0.11/0.94 0.09/0.42 0.30/0.84 0.52/1.02 0.22/0.90 0.52/1.20", true, 5},
		{"Night of the Living Dead AMIAB", 1.75, "6.48/5.38 10.53/10.10 12.89/14.21 14.52/13.15 16.27/14.75 5.66/13.32", true, 4},
		{"Carnival of Souls OFT", 0.70, "0.00/0.02 0.00/0.02 0.00/0.02 0.00/0.02 0.00/0.02 0.00/0.02 0.00/0.02 0.00/0.02", false, 0},
		// One 4 MB piece in 8 s is a burst, not a swarm that keeps up
		{"single piece", 0.29, "0.00/0.00 0.00/0.00 0.00/0.00 4.00/0.00 0.00/0.00 0.00/0.00 0.00/0.00 0.00/0.00", false, 0},
		// Pieces every third second: the median says 0, the swarm delivers 5x the need
		{"bursty YTS", 0.29, "0.00/0.00 5.10/0.00 0.00/0.00 0.00/0.00 4.80/0.00 0.00/0.00 0.00/0.00 5.20/0.00 0.00/0.00 0.00/0.00 4.90/0.00", true, 8},
	} {
		need := int64(tc.need * mbps)
		m := newWarmMeter(need)
		doneAt := 0
		for _, s := range seriesSamples(t, tc.series) {
			if m.add(s) {
				doneAt = int(s.at / time.Second)
				break
			}
		}
		h := m.health(need)
		if h.KeepsUp != tc.keeps || doneAt != tc.doneAt {
			t.Errorf("%s: done after %d s (want %d), %v", tc.name, doneAt, tc.doneAt, h)
		}
	}
}
