package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reuseTorrServer is TorrServer for one saved torrent: its stat (file 1 of size bytes) and a
// stream delivered at rate bytes/s. hits counts every request.
func reuseTorrServer(t *testing.T, hash string, size int64, rate int, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprintf(w, `{"hash":%q,"name":"Movie.2020.1080p.WEB-DL.x264-GRP","download_speed":%d,"active_peers":4,"connected_seeders":3,`+
				`"file_stats":[{"id":1,"path":"Movie.2020.1080p.WEB-DL.x264-GRP.mkv","length":%d}]}`, hash, rate, size)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, rate/20)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func reuseSetup(t *testing.T, f *fakeTorBox, torrServer string, torbox bool) *Service {
	t.Helper()
	fastWarm(t)
	old := tsReuseWindow
	tsReuseWindow = 600 * time.Millisecond
	t.Cleanup(func() { tsReuseWindow = old })
	s := newDebridService(t, f)
	s.cfg.TorrServerURL = torrServer
	if !torbox {
		off := false
		in := DebridSettingsInput{}
		in.TorBox = &struct {
			APIKey      optString `json:"api_key"`
			Enabled     *bool     `json:"enabled"`
			Prefer      *bool     `json:"prefer"`
			AddUncached *bool     `json:"add_uncached"`
		}{Enabled: &off}
		if _, err := s.UpdateDebridSettings(in); err != nil {
			t.Fatal(err)
		}
		s.debrid.settingsChanged()
	}
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		var p tsProbe
		err := json.Unmarshal([]byte(`{"format":{"duration":"6000"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080}]}`), &p)
		return &p, err
	}
	return s
}

func reuseRun(s *Service, hash string, index int) tsReuseAnswer {
	q := tsResolveQuery{mediaType: "movie", title: "Movie", year: 2020, runtime: 100}
	key := tsResolveKey(q)
	return s.reuseSaved(context.Background(), key, q, tsSaved{hash: hash, index: index, at: 1646}, s.holdWarm(key))
}

// Game of Thrones S01E01 (2026-10-09): a source that played from TorBox the night before was
// resumed from Continue Watching after a server restart and went straight to the swarm
// (0.71 MB/s for 3.2 MB/s needed). A saved hash TorBox has cached plays from TorBox.
func TestReuseCachedOnTorBoxPlaysDebrid(t *testing.T) {
	f := newFakeTorBox(t)
	var hits atomic.Int32
	ts := reuseTorrServer(t, tbHash, 3<<20, 1<<20, &hits)
	s := reuseSetup(t, f, ts.URL, true)

	// Saved with TorrServer's index 2: TorBox's list decides (the video is its first file)
	ans := reuseRun(s, tbHash, 2)
	if ans.Action != "play" || ans.Source == nil || !ans.Source.Debrid || ans.Source.Index != 1 || ans.Source.Source != tsSourceTorBox {
		t.Fatalf("answer %+v %+v, want TorBox file 1", ans, ans.Source)
	}
	if u := s.fileURL(tbHash, 1); !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("play reads %q, want the TorBox proxy", u)
	}
	// The subtitle reads (moviehash, embedded tracks) go through the proxy too
	if u := s.DebridFileURL(strings.ToUpper(tbHash), 1); !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("subtitles read %q, want the TorBox proxy", u)
	}
	if hits.Load() != 0 {
		t.Fatalf("TorrServer asked %d times for a TorBox source", hits.Load())
	}
}

// Not on TorBox and the swarm cannot keep up: resolve again at once instead of buffering.
func TestReuseUncachedSlowSwarmResolvesAgain(t *testing.T) {
	f := newFakeTorBox(t)
	hash := strings.Repeat("c", 40)
	var hits atomic.Int32
	// need = 600 MB / 100 min = 100 KB/s; the swarm delivers 40 KB/s
	ts := reuseTorrServer(t, hash, 600_000_000, 40_000, &hits)
	s := reuseSetup(t, f, ts.URL, true)
	start := time.Now()
	ans := reuseRun(s, hash, 1)
	if ans.Action != "resolve" || !strings.Contains(ans.Reason, "does not keep up") {
		t.Fatalf("answer %+v, want resolve", ans)
	}
	if f.checks.Load() != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("checkcached %d times, took %s", f.checks.Load(), time.Since(start))
	}

	// A swarm that keeps up plays the saved torrent, measured
	fast := reuseTorrServer(t, hash, 600_000_000, 2_000_000, &hits)
	s = reuseSetup(t, f, fast.URL, true)
	ans = reuseRun(s, hash, 1)
	if ans.Action != "play" || ans.Source.Debrid || ans.Source.Health == nil || !ans.Source.Health.KeepsUp || ans.Source.Index != 1 {
		t.Fatalf("answer %+v %+v, want the torrent", ans, ans.Source)
	}
	if ans.Source.Quality != "1080p" || !strings.HasPrefix(ans.Source.Magnet, "magnet:?xt=urn:btih:"+hash) {
		t.Fatalf("source %+v", ans.Source)
	}
}

// TorBox off: the saved torrent plays from its swarm, TorBox is never asked.
func TestReuseTorBoxDisabledPlaysTorrent(t *testing.T) {
	f := newFakeTorBox(t)
	var hits atomic.Int32
	ts := reuseTorrServer(t, tbHash, 600_000_000, 2_000_000, &hits)
	s := reuseSetup(t, f, ts.URL, false)
	ans := reuseRun(s, tbHash, 1)
	if ans.Action != "play" || ans.Source.Debrid || ans.Source.Index != 1 {
		t.Fatalf("answer %+v %+v, want the torrent", ans, ans.Source)
	}
	if f.checks.Load() != 0 || f.dls.Load() != 0 {
		t.Fatalf("TorBox asked: checkcached %d, requestdl %d", f.checks.Load(), f.dls.Load())
	}
	if u := s.fileURL(tbHash, 1); !strings.HasPrefix(u, ts.URL+"/stream?") {
		t.Fatalf("play reads %q, want TorrServer", u)
	}
}

// After a restart, a client that plays a saved source without /reuse (old TV app) still reads
// a cached TorBox source from TorBox: probe, files and the subtitle reads adopt it.
func TestAdoptSavedTorBoxSource(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	res := tsResolveResult{Magnet: tsBuildMagnet(tbHash, "Movie (2020)", nil), Hash: tbHash, Index: 1, FileName: "Movie.2020.1080p.mkv",
		TorrentTitle: "Movie (2020)", Size: 3 << 20, Source: tsSourceTorBox, Debrid: true}
	data, _ := json.Marshal(res)
	if err := saveSource(s.db, &StreamSource{SourceKey: "v10|movie|movie|2020|0|0|hevc", Result: string(data), ResolvedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// A fresh process on the same database
	s2 := NewService(s.db, Config{TorBoxURL: f.srv.URL, TorrServerURL: "http://127.0.0.1:1"})
	if u := s2.fileURL(tbHash, 1); !strings.HasPrefix(u, "http://127.0.0.1:1/") {
		t.Fatalf("before adoption %q", u)
	}
	if u := s2.DebridFileURL(tbHash, 1); !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("adopted %q, want the TorBox proxy", u)
	}
	// Torrents no cached source names stay with TorrServer (and are not looked up again at once)
	other := strings.Repeat("d", 40)
	s2.adoptSaved(other)
	if _, ok := s2.debrid.hashSource(other); ok {
		t.Fatal("adopted a torrent source")
	}
}

func TestReuseOffsetFollowsResumePosition(t *testing.T) {
	s := NewService(nil, Config{})
	size := int64(6000) << 20
	if got := s.reuseOffset(tsSaved{hash: tbHash, index: 1, at: 3000}, size, 100); got != 3000<<20 {
		t.Fatalf("offset %d, want the middle", got)
	}
	if got := s.reuseOffset(tsSaved{hash: tbHash, index: 1}, size, 100); got != 0 {
		t.Fatalf("offset %d at the start", got)
	}
	if got := s.reuseOffset(tsSaved{hash: tbHash, index: 1, at: 5999}, size, 100); got > size-tsWarmMaxBytes {
		t.Fatalf("offset %d leaves no room to measure", got)
	}
}

func TestHashIn(t *testing.T) {
	if !tsHashIn(strings.ToUpper(tbHash), []string{"x", tbHash}) || tsHashIn(tbHash, nil) {
		t.Fatal("tsHashIn")
	}
}

// GoT S01E01 (2026-10-09): a slow swarm stalled, the player stepped down to the TorBox 1080p
// (a resolve with maxq=1080) and progress saved that hash; every later resume played the 1080p.
// A saved step-down pick gives way to the title's own (uncapped) source.
func TestReuseStepDownPickResumesUncappedSource(t *testing.T) {
	f := newFakeTorBox(t)
	var hits atomic.Int32
	low := strings.Repeat("e", 40)
	ts := reuseTorrServer(t, low, 600_000_000, 2_000_000, &hits)
	s := reuseSetup(t, f, ts.URL, true)
	q := tsResolveQuery{mediaType: "movie", title: "Movie", year: 2020, runtime: 100}
	key := tsResolveKey(q)
	store := func(k, hash string, debrid bool) {
		res := tsResolveResult{Magnet: tsBuildMagnet(hash, "Movie (2020)", nil), Hash: hash, Index: 1, FileName: "Movie.2020.mkv",
			TorrentTitle: "Movie (2020)", Quality: "2160p", Size: 3 << 20, Debrid: debrid}
		if debrid {
			res.Source = tsSourceTorBox
		}
		data, _ := json.Marshal(res)
		if err := saveSource(s.db, &StreamSource{SourceKey: k, Result: string(data), ResolvedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	store(key, tbHash, true)
	store(key+"|max1080", low, false)

	ans := s.reuseSaved(context.Background(), key, q, tsSaved{hash: low, index: 1, at: 1646}, s.holdWarm(key))
	if ans.Action != "play" || ans.Source.Hash != tbHash || !ans.Source.Debrid {
		t.Fatalf("answer %+v %+v, want the uncapped TorBox source", ans, ans.Source)
	}
	// A capped request (the player stepped down in this session) keeps its pick
	q.maxQ = 1080
	capped := tsResolveKey(q)
	ans = s.reuseSaved(context.Background(), capped, q, tsSaved{hash: low, index: 1, at: 1646}, s.holdWarm(capped))
	if ans.Action != "play" || ans.Source.Hash != low {
		t.Fatalf("capped answer %+v %+v, want the saved 1080p", ans, ans.Source)
	}
}
