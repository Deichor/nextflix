//go:build live

package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/torbox"
)

// Read-only check against the real TorBox account with the public-domain Big Buck Bunny
// torrent (cached on TorBox; it is only added with add_only_if_cached). Needs TORBOX_API_KEY,
// ffmpeg and ffprobe; see the torbox-live.sh script. Never prints the key.
//
//	go test -tags live -run TestLiveTorBox -v ./internal/modules/streaming/
func TestLiveTorBox(t *testing.T) {
	key := os.Getenv("TORBOX_API_KEY")
	if key == "" {
		t.Skip("TORBOX_API_KEY not set")
	}
	const hash = "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c"
	ctx := context.Background()
	cl := torbox.New(os.Getenv("TORBOX_URL"), key, nil)

	start := time.Now()
	me, err := cl.Me(ctx)
	if err != nil {
		t.Fatalf("user/me: %v", err)
	}
	t.Logf("user/me: plan %s, premium until %s (%s)", torbox.PlanName(me.Plan), me.PremiumExpiresAt, since(start))

	start = time.Now()
	cached, known, err := cl.CheckCached(ctx, []string{hash})
	if err != nil || !known[hash] {
		t.Fatalf("checkcached: %v (known %v)", err, known[hash])
	}
	it, ok := cached[hash]
	if !ok {
		t.Fatalf("Big Buck Bunny is not cached on TorBox: stopping (nothing is added)")
	}
	t.Logf("checkcached: cached %q, %d files (%s)", it.Name, len(it.Files), since(start))
	for i, f := range it.Files {
		t.Logf("  #%d %s (%d bytes)", i+1, f.Name, f.Size)
	}

	s := NewService(memDB(t), Config{TorBoxURL: os.Getenv("TORBOX_URL"), TorrServerURL: envDefault("TORRSERVER_URL", "http://torrserver:8090")})
	in := DebridSettingsInput{}
	in.TorBox = &struct {
		APIKey      optString `json:"api_key"`
		Enabled     *bool     `json:"enabled"`
		Prefer      *bool     `json:"prefer"`
		AddUncached *bool     `json:"add_uncached"`
	}{APIKey: optString{Value: key}}
	if _, err := s.UpdateDebridSettings(in); err != nil {
		t.Fatal(err)
	}
	files := make([]tsFileStat, 0, len(it.Files))
	for i, f := range it.Files {
		files = append(files, tsFileStat{ID: i + 1, Path: f.Name, Length: f.Size})
	}
	q := tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny", year: 2008}
	file, ok := tsPickFile(files, q, tsCandidate{})
	if !ok {
		t.Fatal("no video file")
	}
	s.debrid.checks[hash] = tsDebridCheck{item: &it, at: time.Now()}
	s.debrid.register(tsDebridSource{hash: hash, index: file.ID, path: file.Path, size: file.Length, title: it.Name,
		magnet: tsBuildMagnet(hash, it.Name, nil)})
	t.Logf("video: #%d %s", file.ID, file.Path)

	// createtorrent (only if cached) or the account's copy, then requestdl
	start = time.Now()
	link, err := s.debrid.link(ctx, hash, file.ID, false)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	host := ""
	if u, err := url.Parse(link.url); err == nil {
		host = u.Host
	}
	t.Logf("link: torrent %d, file %d, CDN host %s (%s)", link.torrentID, link.fileID, host, since(start))

	// TorrServer's id of the same file (is TorBox's list in torrent order?). Opt-in: it adds the
	// torrent to that TorrServer for a while.
	if os.Getenv("NFX_LIVE_TORRSERVER") != "1" {
		t.Log("TorrServer id check skipped (NFX_LIVE_TORRSERVER=1 enables it)")
	} else if id := s.debrid.torrServerIndex(tsDebridSource{hash: hash, index: file.ID, path: file.Path, size: file.Length,
		magnet: tsBuildMagnet(hash, it.Name, nil)}); id == file.ID {
		t.Logf("TorrServer file id %d: same as TorBox's list position", id)
	} else {
		t.Logf("TorrServer file id %d, TorBox list position %d (mapped by path and size)", id, file.ID)
	}

	start = time.Now()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type,codec_name,width,height",
		"-of", "json", link.url).Output()
	if err != nil {
		t.Fatalf("ffprobe of the CDN URL: %v", err)
	}
	t.Logf("ffprobe CDN URL (%s): %s", since(start), compact(out))
	proxyURL := s.fileURL(hash, file.ID)
	start = time.Now()
	if _, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", proxyURL).Output(); err != nil {
		t.Fatalf("ffprobe of the proxy: %v", err)
	}
	t.Logf("ffprobe proxy (%s)", since(start))

	// Line speed through the proxy: 64 MB from the middle
	start = time.Now()
	req, _ := http.NewRequest(http.MethodGet, proxyURL, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", file.Length/2, file.Length/2+64<<20-1))
	if resp, err := http.DefaultClient.Do(req); err == nil {
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		took := time.Since(start)
		t.Logf("proxy read: %d MB in %s = %.1f MB/s", n>>20, took.Round(time.Millisecond), float64(n)/took.Seconds()/(1<<20))
	} else {
		t.Errorf("proxy read: %v", err)
	}

	// The server's own endpoints
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	ln, err := net.Listen("tcp", envDefault("NFX_LIVE_ADDR", "127.0.0.1:18255"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String() + "/api/torrent-stream/"
	file2 := fmt.Sprintf("hash=%s&index=%d", hash, file.ID)

	start = time.Now()
	if body, err := get(base + "probe?" + file2); err != nil {
		t.Errorf("probe: %v", err)
	} else {
		t.Logf("GET /probe (%s): %s", since(start), compact(body))
	}
	for _, at := range []int{0, 300} {
		start = time.Now()
		resp, err := http.Get(fmt.Sprintf("%splay?%s&t=%d&acodecs=aac,opus", base, file2, at))
		if err != nil {
			t.Errorf("play t=%d: %v", at, err)
			continue
		}
		buf := make([]byte, 64<<10)
		n, _ := resp.Body.Read(buf)
		ttfb := time.Since(start)
		var total int64 = int64(n)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			k, err := resp.Body.Read(buf)
			total += int64(k)
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		t.Logf("GET /play t=%d: status %d, first bytes after %s, %.1f MB in the first ~5 s", at, resp.StatusCode,
			ttfb.Round(time.Millisecond), float64(total)/(1<<20))
	}
	t.Logf("file name %s", path.Base(file.Path))
}

func since(t time.Time) string { return time.Since(t).Round(time.Millisecond).String() }

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func get(u string) ([]byte, error) {
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return b, err
}

func compact(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return strings.TrimSpace(string(b))
	}
	out, _ := json.Marshal(v)
	return string(out)
}
