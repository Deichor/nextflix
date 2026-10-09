package streaming

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

// bigFakeVideo gives the fake CDN a 40 MB file and registers it.
func bigFakeVideo(t *testing.T) (*fakeTorBox, *Service, string) {
	t.Helper()
	f := newFakeTorBox(t)
	f.content = make([]byte, 40<<20)
	rand.New(rand.NewSource(2)).Read(f.content)
	s := newDebridService(t, f)
	if it := s.debrid.item(t.Context(), tbHash); it == nil {
		t.Fatal("not cached")
	}
	s.debrid.register(tsDebridSource{hash: tbHash, index: 1, path: tbVideo, size: int64(len(f.content)), title: "Movie (2020)"})
	return f, s, s.fileURL(tbHash, 1)
}

func proxyGet(t *testing.T, u, rng string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestDebridProxyFetchesAheadInParallel(t *testing.T) {
	f, _, u := bigFakeVideo(t)
	f.mu.Lock()
	f.delay = 50 * time.Millisecond
	f.mu.Unlock()
	resp, got := proxyGet(t, u, "bytes=1000-")
	if resp.StatusCode != 206 || !bytes.Equal(got, f.content[1000:]) {
		t.Fatalf("status %d, %d bytes, equal %v", resp.StatusCode, len(got), bytes.Equal(got, f.content[1000:]))
	}
	if resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 1000-%d/%d", len(f.content)-1, len(f.content)) {
		t.Fatalf("content-range %q", resp.Header.Get("Content-Range"))
	}
	f.mu.Lock()
	ranges := append([]string(nil), f.ranges...)
	f.mu.Unlock()
	// 4 MB at the position, then 8 MB chunks: bounded ranges, several at once
	if len(ranges) < 4 || ranges[0] != fmt.Sprintf("bytes=1000-%d", 1000+tsProxyFirst-1) {
		t.Fatalf("ranges %v", ranges)
	}
	for _, r := range ranges {
		if strings.HasSuffix(r, "-") {
			t.Fatalf("open-ended upstream range %q", r)
		}
	}
	if f.cdnPeak.Load() < 2 {
		t.Fatalf("at most %d CDN reads at once, want parallel chunks", f.cdnPeak.Load())
	}
}

func TestDebridProxyReusesCachedRanges(t *testing.T) {
	f, _, u := bigFakeVideo(t)
	size := len(f.content)
	// What ffmpeg reads before a seek: the header, the end, the header again
	_, head := proxyGet(t, u, "bytes=0-65535")
	resp, tail := proxyGet(t, u, "bytes=-4096")
	if resp.StatusCode != 206 || !bytes.Equal(tail, f.content[size-4096:]) ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", size-4096, size-1, size) {
		t.Fatalf("suffix: %d %q %d bytes", resp.StatusCode, resp.Header.Get("Content-Range"), len(tail))
	}
	reads := f.cdnReads.Load()
	_, head2 := proxyGet(t, u, "bytes=100-60000")
	_, tail2 := proxyGet(t, u, fmt.Sprintf("bytes=%d-", size-2000))
	if f.cdnReads.Load() != reads {
		t.Fatalf("cached ranges read again: %d -> %d CDN reads", reads, f.cdnReads.Load())
	}
	if !bytes.Equal(head, f.content[:65536]) || !bytes.Equal(head2, f.content[100:60001]) || !bytes.Equal(tail2, f.content[size-2000:]) {
		t.Fatal("wrong bytes")
	}
	// Past the end
	resp, _ = proxyGet(t, u, fmt.Sprintf("bytes=%d-", size))
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("past the end: %d", resp.StatusCode)
	}
	// HEAD answers the size without a body
	req, _ := http.NewRequest(http.MethodHead, u, nil)
	hr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	hr.Body.Close()
	if hr.StatusCode != 200 || hr.ContentLength != int64(size) || hr.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("head: %d %d", hr.StatusCode, hr.ContentLength)
	}
}

func TestDebridProxyEvictsOldChunks(t *testing.T) {
	_, s, _ := bigFakeVideo(t)
	d := s.debrid
	f := d.proxyFile(tbHash, 1)
	pc := &d.cache
	pc.mu.Lock()
	pc.bytes = tsProxyCacheMax // pretend the cache is full of finished chunks
	old := &tsProxyChunk{start: 0, end: 10, buf: make([]byte, 10), n: 10, wake: make(chan struct{}), used: time.Now().Add(-time.Hour)}
	busy := &tsProxyChunk{start: 10, end: 20, buf: make([]byte, 10), n: 10, wake: make(chan struct{}), readers: 1}
	f.chunks = []*tsProxyChunk{old, busy}
	d.makeRoomLocked(5)
	left := len(f.chunks)
	pc.bytes = 0
	pc.mu.Unlock()
	if left != 1 || f.chunks[0] != busy {
		t.Fatalf("%d chunks left, want only the one in use", left)
	}
}

// A chunk fetched ahead long ago outlives one the reader just left: it is the next one needed.
func TestDebridProxyKeepsPrefetchedChunks(t *testing.T) {
	_, s, _ := bigFakeVideo(t)
	d := s.debrid
	f := d.proxyFile(tbHash, 1)
	pc := &d.cache
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.bytes = tsProxyCacheMax
	read := &tsProxyChunk{start: 0, end: 10, buf: make([]byte, 10), n: 10, wake: make(chan struct{}), used: time.Now().Add(-time.Minute)}
	ahead := &tsProxyChunk{start: 10, end: 20, buf: make([]byte, 10), n: 10, wake: make(chan struct{}), used: time.Now().Add(-2 * time.Minute)}
	f.chunks = []*tsProxyChunk{read, ahead}
	d.prefetchLocked(f, 10, 19, 1)
	d.makeRoomLocked(5)
	pc.bytes = 0
	if len(f.chunks) != 1 || f.chunks[0] != ahead {
		t.Fatalf("%d chunks left, want only the prefetched one", len(f.chunks))
	}
}

func TestDebridParseRange(t *testing.T) {
	for _, c := range []struct {
		in                  string
		first, last, suffix int64
		ok                  bool
	}{
		{"bytes=0-", 0, -1, 0, true},
		{"bytes=10-20", 10, 20, 0, true},
		{"bytes=-500", 0, -1, 500, true},
		{"bytes=20-10", 0, -1, 0, false},
		{"items=0-1", 0, -1, 0, false},
		{"", 0, -1, 0, false},
	} {
		first, last, suffix, ok := tsParseRange(c.in)
		if first != c.first || last != c.last || suffix != c.suffix || ok != c.ok {
			t.Errorf("%q: %d %d %d %v", c.in, first, last, suffix, ok)
		}
	}
}
