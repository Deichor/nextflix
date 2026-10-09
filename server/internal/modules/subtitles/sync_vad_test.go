package subtitles

import (
	"bytes"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Speech-like bursts (voiced harmonics with a syllable rhythm) over quiet room noise and a low
// music bed must come out as the speech frames.
func TestSpeechFrames(t *testing.T) {
	const sr = syncSampleRate
	rng := rand.New(rand.NewSource(1))
	lines := []span{{2, 4.5}, {5.2, 6}, {9, 13}, {20, 22.5}, {24, 29}}
	n := 30 * sr
	pcm := make([]int16, n)
	for i := range pcm {
		t := float64(i) / sr
		v := rng.NormFloat64()*40 + 600*math.Sin(2*math.Pi*55*t) // room noise, bass bed
		for _, l := range lines {
			if t >= l.start && t < l.end {
				env := 0.6 + 0.4*math.Sin(2*math.Pi*4*t)
				f0 := 140 + 20*math.Sin(2*math.Pi*0.7*t)
				for h := 1; h <= 12; h++ {
					v += env * 3000 / float64(h) * math.Sin(2*math.Pi*f0*float64(h)*t)
				}
			}
		}
		pcm[i] = int16(math.Max(-32768, math.Min(32767, v)))
	}
	got := speechFrames(pcm, sr)
	if len(got) != 30*syncRate {
		t.Fatalf("%d frames", len(got))
	}
	agree := 0
	for f, v := range got {
		ts := float64(f) / syncRate
		want := byte(0)
		for _, l := range lines {
			if ts >= l.start && ts < l.end {
				want = 1
			}
		}
		if v == want {
			agree++
		}
	}
	if share := float64(agree) / float64(len(got)); share < 0.95 {
		t.Fatalf("only %.2f of the frames right", share)
	}
}

func TestFillRuns(t *testing.T) {
	b := []byte{1, 1, 0, 0, 1, 1, 0, 0, 0, 0, 0, 1}
	fillRuns(b, 0, 3)
	want := []byte{1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 1}
	for i := range b {
		if b[i] != want[i] {
			t.Fatalf("got %v", b)
		}
	}
}

func TestParseMediaProbe(t *testing.T) {
	out := []byte(`{"format":{"duration":"2562.048","bit_rate":"7063218"},"streams":[
	 {"index":0,"codec_type":"video","codec_name":"h264","avg_frame_rate":"24000/1001","r_frame_rate":"24000/1001"},
	 {"index":1,"codec_type":"audio","codec_name":"eac3","channels":6,"channel_layout":"5.1(side)","disposition":{"default":1}},
	 {"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"chi"}},
	 {"index":3,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng"}},
	 {"index":4,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng","title":"English [SDH]"}},
	 {"index":5,"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle"}]}`)
	p, err := parseMediaProbe(out)
	if err != nil || p.Duration != 2562.048 || math.Abs(p.FPS-23.976) > 0.001 || p.Audio != 1 || !p.Center || p.TextSub != 3 || p.Bitrate != 7063218.0/8 || p.maxWindows() != 10 {
		t.Fatalf("probe %+v %v", p, err)
	}
}

// A window read never pulls more than its cap, whatever ffmpeg asks for.
func TestCappedURL(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<20/16*10) // 10 MB
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.mkv", time.Time{}, bytes.NewReader(data))
	}))
	defer up.Close()
	url, used, stop, err := cappedURL(up.URL, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=100-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(got) > 1<<20 || !bytes.Equal(got, data[100:100+len(got)]) {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(got))
	}
	if n := used(); n < 1<<20 || n > 1<<20+64<<10 {
		t.Fatalf("used %d", n)
	}
	resp, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after the cap: %d", resp.StatusCode)
	}
}
