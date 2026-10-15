package streaming

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func near(got, want float64) bool { return math.Abs(got/mbps-want) < 0.01 } // got in bytes/s, want in MB/s

// tsLinePoller feeds a tsLine one poll per tsLinePoll.
type tsLinePoller struct {
	l   *tsLine
	now time.Time
	key string
}

func (p *tsLinePoller) poll(busy bool, totals ...float64) float64 {
	v := 0.0
	for _, total := range totals {
		p.now = p.now.Add(tsLinePoll)
		v = max(v, p.l.observe(total*mbps, busy, p.key, p.now))
	}
	return v
}

func (p *tsLinePoller) repeat(n int, total float64) float64 {
	v := 0.0
	for range n {
		v = max(v, p.poll(true, total))
	}
	return v
}

func TestLineEstimate(t *testing.T) {
	var l tsLine
	p := &tsLinePoller{l: &l, now: time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC), key: "a"}
	if l.current(p.now) != 0 {
		t.Fatal("nothing seen: the line is unknown")
	}
	// A single spike, or a fast poll of an idle line, never counts
	p.poll(true, 20)
	p.poll(false, 0, 9, 9, 9, 9, 9)
	if l.current(p.now) != 0 {
		t.Fatalf("got %.2f MB/s from a spike and idle polls", l.current(p.now)/mbps)
	}
	// Fresh install: one torrent set seen briefly is not trusted, nor stored
	p.repeat(6, 0.6)
	if l.current(p.now) != 0 {
		t.Fatalf("one torrent set made the line %.2f MB/s", l.current(p.now)/mbps)
	}
	if _, _, ok := l.toSave(p.now); ok {
		t.Fatal("an estimate that is not trusted is stored")
	}
	// The median of 5 busy polls; trusted once another set sees as much
	p.poll(false, 0)
	p.key = "a,b"
	p.poll(true, 3.0, 3.1, 3.2, 3.3, 3.4)
	if l.current(p.now) != 0 {
		t.Fatal("trusted before a second torrent set confirmed it")
	}
	p.poll(false, 0)
	p.key = "c,d"
	if v := p.repeat(5, 3.0); !near(v, 3.2) || !near(l.current(p.now), 3.2) {
		t.Fatalf("got %.2f MB/s, want 3.20", l.current(p.now)/mbps)
	}
	// Weak swarms never lower it
	p.key = "e"
	p.repeat(5, 1)
	if !near(l.current(p.now), 3.2) {
		t.Fatalf("lowered to %.2f MB/s", l.current(p.now)/mbps)
	}
	// A faster line proves itself
	p.repeat(5, 5)
	if !near(l.current(p.now), 5) {
		t.Fatalf("got %.2f MB/s, want 5.00", l.current(p.now)/mbps)
	}
	// It halves over tsLineHalfLife, and newer busy polls above that count again
	p.now = p.now.Add(tsLineHalfLife)
	if !near(l.current(p.now), 2.5) {
		t.Fatalf("decayed to %.2f MB/s, want 2.50", l.current(p.now)/mbps)
	}
	p.poll(false, 0) // idle meanwhile
	p.repeat(5, 3)
	if !near(l.current(p.now), 3) {
		t.Fatalf("got %.2f MB/s, want 3.00 after a slower line was seen", l.current(p.now)/mbps)
	}
	// Stored at most every tsLineSaveEvery
	if _, _, ok := l.toSave(p.now); !ok {
		t.Fatal("a changed estimate is saved")
	}
	p.repeat(5, 4)
	if _, _, ok := l.toSave(p.now); ok {
		t.Fatal("saved again within tsLineSaveEvery")
	}
	if rate, _, ok := l.toSave(p.now.Add(tsLineSaveEvery)); !ok || !near(rate, 4) {
		t.Fatalf("saved %.2f MB/s (%v), want 4.00", rate/mbps, ok)
	}
}

// TorrServer's speed counts bursts: on a 3.34 MB/s line (28 Mbit) polls of 3.2-3.3 with
// bursts of 5-6 must not lift the estimate above the line.
func TestLineIgnoresBursts(t *testing.T) {
	var l tsLine
	p := &tsLinePoller{l: &l, now: time.Now(), key: "a,b"}
	p.poll(true, 3.3, 5.6, 3.2, 3.3, 6.1, 3.2, 3.3, 5.2, 3.2, 3.3)
	p.poll(false, 0)
	p.key = "c,d"
	p.poll(true, 3.2, 5.9, 3.3, 3.2, 5.4)
	if v := l.current(p.now); v == 0 || v > 3.34*mbps {
		t.Fatalf("got %.2f MB/s on a 3.34 MB/s line", v/mbps)
	}
}

// Fresh install: the first playback reads ahead at line speed from one torrent with few
// peers; 30 s of it teach the line.
func TestLineLearnsFromPlayback(t *testing.T) {
	needs := map[string]float64{"aa": 2 * mbps}
	stream := func(speed float64) (float64, bool, string) {
		return tsLineLoad([]tsLineTorrent{{Hash: "AA", DownloadSpeed: speed * mbps, ActivePeers: 3}}, false, needs)
	}
	if _, busy, _ := stream(2.1); busy {
		t.Fatal("a stream at its bitrate (read-ahead full) counts")
	}
	var l tsLine
	now := time.Now()
	for i := range 14 { // 28 s
		now = now.Add(tsLinePoll)
		total, busy, key := stream(3.2)
		l.observe(total, busy, key, now)
		if i == 12 && l.current(now) != 0 {
			t.Fatal("trusted before 30 s of read-ahead")
		}
	}
	for range 2 {
		now = now.Add(tsLinePoll)
		total, busy, key := stream(3.2)
		l.observe(total, busy, key, now)
	}
	if !near(l.current(now), 3.2) {
		t.Fatalf("after 32 s of read-ahead: %.2f MB/s, want 3.20", l.current(now)/mbps)
	}
}

func TestLineLoad(t *testing.T) {
	two := []tsLineTorrent{{Hash: "BB", DownloadSpeed: 1.5 * mbps, ActivePeers: 2}, {Hash: "aa", DownloadSpeed: 1.7 * mbps, ActivePeers: 3}, {Hash: "cc"}}
	if total, busy, key := tsLineLoad(two, true, nil); !busy || !near(total, 3.2) || key != "aa,bb" {
		t.Fatalf("two measured torrents: %.2f MB/s, busy %v, set %q", total/mbps, busy, key)
	}
	if _, busy, _ := tsLineLoad(two, false, nil); busy {
		t.Fatal("two torrents count without a measurement")
	}
	if _, busy, _ := tsLineLoad([]tsLineTorrent{{DownloadSpeed: 0.8 * mbps, ActivePeers: 3}}, true, nil); busy {
		t.Fatal("one weak swarm says nothing about the line")
	}
	if _, busy, _ := tsLineLoad([]tsLineTorrent{{DownloadSpeed: 3 * mbps, ActivePeers: 12}}, true, nil); !busy {
		t.Fatal("one measured torrent from many peers loads the line")
	}
	// A stream from 10 peers at its bitrate, its read-ahead full
	if _, busy, _ := tsLineLoad([]tsLineTorrent{{Hash: "aa", DownloadSpeed: 0.6 * mbps, ActivePeers: 10}}, false,
		map[string]float64{"aa": 0.6 * mbps}); busy {
		t.Fatal("a stream that only pulls its bitrate counts")
	}
}

func TestLineState(t *testing.T) {
	s := NewService(nil, Config{})
	s.playing["aa"] = 1
	if measuring, playing := s.lineState(); measuring || len(playing) != 1 {
		t.Fatalf("measuring %v, playing %v", measuring, playing)
	}
	s.line.reads.Add(1)
	if measuring, _ := s.lineState(); !measuring {
		t.Fatal("a measurement next to playback is not seen")
	}
	p := &tsProbe{}
	p.Format.BitRate = "16000000"
	s.probeCache.Store("aa:1", p)
	s.probeCache.Store("bb:1", p)
	if needs := s.playNeeds([]string{"aa"}); len(needs) != 1 || !near(needs["aa"], 2e6/mbps) {
		t.Fatalf("needs %v", needs)
	}
}

// TorBox measurement reads count like observations: two files confirm the line.
func TestLineTorBox(t *testing.T) {
	var l tsLine
	now := time.Now()
	if l.note(12*mbps, "torbox:aa", now); l.current(now) != 0 {
		t.Fatal("one cold TorBox read is trusted")
	}
	if v := l.note(11*mbps, "torbox:bb", now); !near(v, 12) || !near(l.current(now), 12) {
		t.Fatalf("got %.2f MB/s, want 12.00", l.current(now)/mbps)
	}
	s := NewService(nil, Config{})
	s.debrid.line.note(int64(14*mbps), time.Now())
	q := tsResolveQuery{mediaType: "movie", runtime: 120, line: 3 * mbps}
	uhd := measured("aa", "2160p", 14, 3.5)
	if !s.overLine(uhd, q) {
		t.Fatal("a swarm 4K over a 3 MB/s line fits")
	}
	if uhd.Debrid = true; s.overLine(uhd, q) {
		t.Fatal("a TorBox 4K is over the line although TorBox delivered 14 MB/s")
	}
}

// The stored estimate is known right after a restart.
func TestLineStored(t *testing.T) {
	db := memDB(t)
	if v, known := NewService(db, Config{}).lineRate(); known || v != 0 {
		t.Fatalf("fresh install: %.2f MB/s known %v", v/mbps, known)
	}
	if err := saveLineRate(db, &LineRate{Rate: 3355443, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if v, known := NewService(db, Config{}).lineRate(); !known || !near(v, 3.2) {
		t.Fatalf("after a restart: %.2f MB/s known %v, want 3.20", v/mbps, known)
	}
}

// A 23.9 GB 2160p needs 3.5 MB/s: on a 3.2 MB/s line a fitting 1080p ranks first; when nothing
// fits, the lowest need; an unknown line ranks as before.
func TestLineRanking(t *testing.T) {
	rel := func(i int, name string, gb float64) JackettResult {
		return JackettResult{Title: "The Test Film 2024 " + name, Seeders: 40, Size: int64(gb * float64(tsGB)),
			MagnetUri: fmt.Sprintf("magnet:?xt=urn:btih:%040x", i+1)}
	}
	raw := []JackettResult{
		rel(0, "2160p WEB-DL DDP5 1 Atmos HDR x265-GRP", 23.9),
		rel(1, "1080p WEB-DL DDP5 1 x264-GRP", 10.6),
	}
	q := tsResolveQuery{mediaType: "movie", title: "The Test Film", year: 2024, hevc: true, hdr: true, runtime: 120}
	top := func(line float64) tsCandidate {
		q.line = line * mbps
		return tsRankAll(raw, q)[0]
	}
	if c := top(0); c.quality != 2160 || c.parts.Line != 0 {
		t.Fatalf("unknown line: %dp first, want the 4K", c.quality)
	}
	if c := top(3.2); c.quality != 1080 {
		t.Fatalf("3.2 MB/s line: %dp first, want the fitting 1080p", c.quality)
	}
	if c := top(1); c.quality != 1080 || c.parts.Line >= 0 {
		t.Fatalf("1 MB/s line: %dp first (line %.1f), want the lower need", c.quality, c.parts.Line)
	}
	if c := top(8); c.quality != 2160 || c.parts.Line != 0 {
		t.Fatalf("8 MB/s line: %dp first, want the 4K", c.quality)
	}
}

func TestLinePlayable(t *testing.T) {
	uhd := measured("uhd", "2160p", 3.1, 3.5) // x0.89
	hd := measured("hd", "1080p", 0.9, 1.8)   // x0.50
	if i := tsChoosePlayable([]*tsResolveResult{uhd, hd}); i != 0 {
		t.Fatalf("unknown line: got %d, want the clearly faster 4K", i)
	}
	uhd.overLine = true
	if i := tsChoosePlayable([]*tsResolveResult{uhd, hd}); i != 1 {
		t.Fatalf("4K over the line: got %d, want the fitting 1080p", i)
	}
	// Nothing fits: the lowest need, whatever the ratios of checks sharing the line say
	hd.overLine = true
	if i := tsChoosePlayable([]*tsResolveResult{uhd, hd}); i != 1 {
		t.Fatalf("nothing fits: got %d, want the lower need", i)
	}
	// Never impossible: a dead fitting swarm does not beat a live one over the line
	dead := measured("dead", "1080p", 0, 1.2)
	dead.Health.Dead = true
	if i := tsChoosePlayable([]*tsResolveResult{dead, uhd}); i != 1 {
		t.Fatalf("got %d, want the live one over the line", i)
	}
}

func TestLineUpgradeRefused(t *testing.T) {
	s := NewService(nil, Config{})
	q := tsResolveQuery{mediaType: "movie", title: "The Test Film", runtime: 120, hevc: true, line: 3.2 * mbps}
	up := measured("aa", "2160p", 4.6, 3.5)
	if s.offerUpgrade("k", q, up) {
		t.Fatal("a 4K that needs 3.5 MB/s was offered on a 3.2 MB/s line")
	}
	if _, ok := s.upgrades.Load("k"); ok {
		t.Fatal("the refused upgrade left a state behind")
	}
}

func TestLineGatherWants1080p(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	rel := func(i int) JackettResult {
		return JackettResult{Title: fmt.Sprintf("Night of the Living Dead 1968 1080p BluRay x264-G%d", i), Seeders: 30,
			Size: 6 * tsGB, MagnetUri: fmt.Sprintf("magnet:?xt=urn:btih:%040x", i+1)}
	}
	raw := []JackettResult{rel(0), rel(1), rel(2)}
	if tsEnoughResults(raw, q, false) {
		t.Fatal("unknown line: an HEVC client waits for 4K")
	}
	q.line = 1.5 * mbps // a typical 4K (14 Mbit/s) needs more than 0.75 of it
	if !tsEnoughResults(raw, q, false) {
		t.Fatal("slow line: three 1080p are enough")
	}
}
