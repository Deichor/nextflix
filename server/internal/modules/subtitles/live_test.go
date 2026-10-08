package subtitles

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const liveHash = "0123456789abcdef0123456789abcdef01234567"

// fakeReads replaces ffmpeg: each read gets a pipe the test writes cues into.
type fakeReads struct {
	mu    sync.Mutex
	reads []*fakeRead
}

type fakeRead struct {
	from float64
	w    *io.PipeWriter
	ctx  context.Context
	done chan error
}

func (f *fakeReads) start(ctx context.Context, url string, stream int, from float64) (io.Reader, func() error, error) {
	r, w := io.Pipe()
	fr := &fakeRead{from: from, w: w, ctx: ctx, done: make(chan error, 1)}
	go func() {
		<-ctx.Done()
		w.CloseWithError(ctx.Err())
	}()
	f.mu.Lock()
	f.reads = append(f.reads, fr)
	f.mu.Unlock()
	return r, func() error {
		select {
		case err := <-fr.done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

func (f *fakeReads) last() *fakeRead {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) == 0 {
		return nil
	}
	return f.reads[len(f.reads)-1]
}

func (f *fakeReads) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

func (r *fakeRead) cue(start, end float64, text string) {
	fmt.Fprintf(r.w, "%s --> %s\n%s\n\n", formatVTTTime(start), formatVTTTime(end), text)
}

func (r *fakeRead) end() {
	r.done <- nil
	r.w.Close()
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseVTTStream(t *testing.T) {
	in := "WEBVTT\n\n00:00:01.000 --> 00:00:02.500 align:start\nMerhaba\n\n1\n01:02.000 --> 01:03.000\nİki\nsatır\n\nbroken --> x\nNo\n\n"
	var got []liveCue
	parseVTTStream(strings.NewReader(in), func(c liveCue) bool { got = append(got, c); return true })
	if len(got) != 2 || got[0].start != 1 || got[0].end != 2.5 || got[1].start != 62 || got[1].text != "İki\nsatır" {
		t.Fatalf("cues %+v", got)
	}
}

func TestLiveReadGrowsAndCompletes(t *testing.T) {
	saved := liveFirstWait
	defer func() { liveFirstWait = saved }()
	liveFirstWait = 300 * time.Millisecond
	reads := &fakeReads{}
	var cached atomic.Value
	l := newLiveSubs("http://torrserver", func(key string, vtt []byte) { cached.Store(string(vtt)) })
	l.start = reads.start
	ctx := context.Background()

	// The first answer waits for the first cue of the new read, which starts before t
	go func() {
		waitUntil(t, func() bool { return reads.last() != nil })
		reads.last().cue(100, 102, "Birinci")
	}()
	vtt, partial := l.get(ctx, "k", liveHash, 1, 3, 120)
	if !partial || !strings.Contains(string(vtt), PartialNote) || !strings.Contains(string(vtt), "Birinci") {
		t.Fatalf("first answer partial=%v:\n%s", partial, vtt)
	}
	if r := reads.last(); r.from != 120-liveLead {
		t.Fatalf("read starts at %v", r.from)
	}
	// Positions the running read reaches soon need no new read
	if _, _ = l.get(ctx, "k", liveHash, 1, 3, 200); reads.count() != 1 {
		t.Fatalf("%d reads", reads.count())
	}
	// A seek far past the frontier starts a read there; the first one stops
	first := reads.last()
	l.get(ctx, "k", liveHash, 1, 3, 2000)
	if reads.count() != 2 || reads.last().from != 2000-liveLead || first.ctx.Err() == nil {
		t.Fatalf("seek: %d reads, from %v, first stopped %v", reads.count(), reads.last().from, first.ctx.Err())
	}
	second := reads.last()
	second.cue(1990, 1995, "İkinci")
	second.end() // to the end of the file
	waitUntil(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.tracks["k"].run == nil })
	// Back to the start: the gap before the first read is read and stops at the read range
	l.get(ctx, "k", liveHash, 1, 3, 10)
	third := reads.last()
	if reads.count() != 3 || third.from != 0 {
		t.Fatalf("back: %d reads from %v", reads.count(), third.from)
	}
	third.cue(5, 6, "Sıfır")
	third.cue(100, 102, "Birinci") // reached the first read's range: merged, then the gap after it
	waitUntil(t, func() bool { return third.ctx.Err() != nil })
	waitUntil(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.tracks["k"].run == nil })
	l.get(ctx, "k", liveHash, 1, 3, 90)
	fourth := reads.last()
	if reads.count() != 4 || fourth.from != 102 {
		t.Fatalf("gap: %d reads from %v", reads.count(), fourth.from)
	}
	fourth.cue(1985, 1991, "Son") // runs into the read to the end: every second is covered
	waitUntil(t, func() bool { return cached.Load() != nil })
	vtt, partial = l.get(ctx, "k", liveHash, 1, 3, 500)
	if partial || strings.Contains(string(vtt), PartialNote) || strings.Count(string(vtt), "-->") != 4 {
		t.Fatalf("complete answer partial=%v:\n%s", partial, vtt)
	}
	if c := cached.Load().(string); !strings.HasPrefix(c, "WEBVTT") || strings.Index(c, "Sıfır") > strings.Index(c, "Birinci") {
		t.Fatalf("cached:\n%s", c)
	}
}

func TestLiveSwitchingTrackStopsTheOther(t *testing.T) {
	saved := liveFirstWait
	defer func() { liveFirstWait = saved }()
	liveFirstWait = 10 * time.Millisecond
	reads := &fakeReads{}
	l := newLiveSubs("http://torrserver", nil)
	l.start = reads.start
	l.get(context.Background(), "a", liveHash, 1, 3, 0)
	a := reads.last()
	l.get(context.Background(), "b", liveHash, 1, 4, 0)
	if a.ctx.Err() == nil || reads.count() != 2 {
		t.Fatal("the other track of the file should stop")
	}
}

func TestFileAtLiveForBigFiles(t *testing.T) {
	saved := liveFirstWait
	defer func() { liveFirstWait = saved }()
	liveFirstWait = 10 * time.Millisecond
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cache":
			io.WriteString(w, `{"PiecesLength":1048576,"Pieces":{}}`)
		default: // stat: one 3 GB file, nothing cached
			io.WriteString(w, `{"file_stats":[{"id":1,"path":"Below.S01E01.mkv","length":3221225472}]}`)
		}
	}))
	defer ts.Close()
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: ts.URL})
	reads := &fakeReads{}
	svc.live.start = reads.start
	id := encodeID(SourceEmbedded, "tr", liveHash+"|1|4")
	vtt, partial, err := svc.FileAt(context.Background(), id, 600)
	if err != nil || !partial || !strings.Contains(string(vtt), PartialNote) || reads.count() != 1 {
		t.Fatalf("FileAt: %v partial=%v reads=%d\n%s", err, partial, reads.count(), vtt)
	}
	if _, _, err := svc.FileAt(context.Background(), encodeID(SourceEmbedded, "tr", "bad|1|4"), 0); err != ErrBadID {
		t.Fatalf("bad ref: %v", err)
	}
}

func TestRefreshSkipsTheListCacheRateLimited(t *testing.T) {
	var searches atomic.Int32
	osSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searches.Add(1)
		io.WriteString(w, `{"data":[]}`)
	}))
	defer osSrv.Close()
	svc := NewService(nil, Config{CacheDir: t.TempDir(), OpenSubtitlesURL: osSrv.URL})
	if err := svc.settings.save(Settings{OpenSubtitlesAPIKey: "K", DefaultLangs: "tr,en"}); err != nil {
		t.Fatal(err)
	}
	q := Query{TmdbID: 285322, MediaType: "tv", Season: 1, Episode: 1}
	list := func(refresh bool) {
		q.Refresh = refresh
		svc.List(context.Background(), q)
	}
	list(false)
	list(false)
	if n := searches.Load(); n != 1 {
		t.Fatalf("cached list asked again: %d", n)
	}
	list(true)
	if n := searches.Load(); n != 2 {
		t.Fatalf("refresh did not ask again: %d", n)
	}
	list(true) // within refreshEvery
	if n := searches.Load(); n != 2 {
		t.Fatalf("refresh not rate limited: %d", n)
	}
}
