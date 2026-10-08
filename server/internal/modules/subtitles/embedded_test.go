package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestParseProbe(t *testing.T) {
	out := []byte(`{"streams":[
		{"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"tur","title":"Türkçe"},"disposition":{"default":1,"forced":0}},
		{"index":3,"codec_name":"ass","codec_type":"subtitle","tags":{"LANGUAGE":"eng","title":"English SDH"},"disposition":{"hearing_impaired":0}},
		{"index":4,"codec_name":"hdmv_pgs_subtitle","codec_type":"subtitle","tags":{"language":"ger"},"disposition":{"forced":1}},
		{"index":5,"codec_name":"mov_text","codec_type":"subtitle","tags":{"language":"und","title":"Forced"}}
	]}`)
	got, err := parseProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []EmbeddedSub{
		{StreamIndex: 2, Language: "tr", Title: "Türkçe", Codec: "subrip", Available: true},
		{StreamIndex: 3, Language: "en", Title: "English SDH", Codec: "ass", HI: true, Available: true},
		{StreamIndex: 4, Language: "de", Codec: "hdmv_pgs_subtitle", Forced: true},
		{StreamIndex: 5, Title: "Forced", Codec: "mov_text", Forced: true, Available: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestExtractionPractical(t *testing.T) {
	const piece = 1 << 20
	files := []torrFile{{1, "a.srt", 10 * piece}, {2, "small.mkv", 700 << 20}, {3, "big.mkv", 2000 << 20}}
	completed := map[string]any{}
	// big.mkv covers pieces 710..2709; cache 1900 of its 2000 pieces
	for i := 710; i < 710+1900; i++ {
		completed[strconv.Itoa(i)] = map[string]any{"Id": i, "Size": piece, "Length": piece, "Completed": true}
	}
	cached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream":
			_ = json.NewEncoder(w).Encode(map[string]any{"file_stats": files})
		case "/cache":
			pieces := map[string]any{}
			if cached {
				pieces = completed
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"PiecesLength": piece, "Pieces": pieces})
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	if !extractionPractical(ctx, srv.URL, testHash, 2) {
		t.Fatal("small file must be practical")
	}
	if extractionPractical(ctx, srv.URL, testHash, 3) {
		t.Fatal("big uncached file must not be practical")
	}
	cached = true
	if !extractionPractical(ctx, srv.URL, testHash, 3) {
		t.Fatal("big file 95% cached must be practical")
	}
	if extractionPractical(ctx, srv.URL, testHash, 9) {
		t.Fatal("unknown file must not be practical")
	}
}

// TestEmbeddedFFmpeg muxes a Turkish SRT into an MKV and reads it back through a fake TorrServer.
// It needs ffmpeg/ffprobe (present in the server image, not in plain golang images).
func TestEmbeddedFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	mkv := filepath.Join(t.TempDir(), "movie.mkv")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=70",
		"-i", filepath.Join("testdata", "tr-utf8-bom.srt"), "-map", "0", "-map", "1", "-c:v", "mpeg4", "-c:s", "srt",
		"-metadata:s:s:0", "language=tur", "-metadata:s:s:0", "title=Türkçe", mkv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(mkv)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cache":
			_ = json.NewEncoder(w).Encode(map[string]any{"PiecesLength": 1 << 20})
		case r.URL.Query().Has("stat"):
			_ = json.NewEncoder(w).Encode(map[string]any{"file_stats": []torrFile{{1, "movie.mkv", int64(len(data))}}})
		default:
			http.ServeContent(w, r, "movie.mkv", time.Time{}, bytes.NewReader(data))
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	subs, err := ListEmbeddedSubs(ctx, srv.URL, testHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Language != "tr" || subs[0].Codec != "subrip" || !subs[0].Available {
		t.Fatalf("subs = %+v", subs)
	}
	vtt, err := ExtractEmbeddedSub(ctx, srv.URL, testHash, 1, subs[0].StreamIndex)
	if err != nil {
		t.Fatal(err)
	}
	if string(vtt) != turkishVTT {
		t.Fatalf("got:\n%s\nwant:\n%s", vtt, turkishVTT)
	}
}
