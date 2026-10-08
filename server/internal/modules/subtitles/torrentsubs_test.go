package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
)

func TestGuessLanguage(t *testing.T) {
	cases := map[string]string{
		"Movie.2024.tr.srt":                          "tr",
		"Movie.2024.TR.srt":                          "tr",
		"Turkish.srt":                                "tr",
		"tur.forced.srt":                             "tr",
		"2_English.srt":                              "en",
		"Subs/Türkçe.srt":                            "tr",
		"Subs/Turkish/3.srt":                         "tr",
		`Subs\eng\1.srt`:                             "en",
		"Movie.2024.Türkçe.Altyazı.srt":              "tr",
		"The.Movie.2020.İNGİLİZCE.srt":               "en",
		"Movie.2019.ENG.HI.srt":                      "en",
		"It.2017.en.sdh.srt":                         "en",
		"Movie.pt-BR.srt":                            "pt",
		"Movie.2024.1080p.WEB-DL.x264-GROUP.srt":     "",
		"It.2017.1080p.srt":                          "",
		"movie.hi.srt":                               "",
		"Dan.in.Real.Life.2007.srt":                  "",
		"Dan.in.Real.Life.2007.dan.srt":              "da",
		"Movie.Name.2024.English-WWW.MY-SUBS.CO.srt": "en",
		"Film.2023.German.forced.ass":                "de",
		"Film.ger.sub":                               "de",
		"":                                           "",
	}
	for name, want := range cases {
		if got := GuessLanguage(name); got != want {
			t.Errorf("GuessLanguage(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSubFlags(t *testing.T) {
	cases := map[string][2]bool{
		"Movie.en.forced.srt":           {true, false},
		"Movie.en.SDH.srt":              {false, true},
		"Movie.English.HI.srt":          {false, true},
		"Movie.Hearing.Impaired.en.srt": {false, true},
		"Movie.en.srt":                  {false, false},
	}
	for name, want := range cases {
		forced, hi := subFlags(name)
		if forced != want[0] || hi != want[1] {
			t.Errorf("subFlags(%q) = %v %v, want %v", name, forced, hi, want)
		}
	}
}

const testHash = "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c"

// fakeTorrServer serves a stat with the given files and their contents for &play.
func fakeTorrServer(t *testing.T, files []torrFile, content map[int][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/stream" || q.Get("link") != testHash {
			http.NotFound(w, r)
			return
		}
		if _, ok := q["stat"]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": testHash, "file_stats": files})
			return
		}
		id, err := strconv.Atoi(q.Get("index"))
		if _, play := q["play"]; !play || err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data, ok := content[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Flush first so no Content-Length is sent and the size cap has to work on the body.
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write(data)
	}))
}

func TestListTorrentSubs(t *testing.T) {
	files := []torrFile{
		{1, "Movie/Movie.2024.mkv", 2 << 30},
		{2, "Movie/Subs/2_English.srt", 80_000},
		{3, "Movie/Subs/Movie.tr.forced.ass", 20_000},
		{4, "Movie/Movie.idx", 1000},
		{5, "Movie/Movie.sub", 3 << 20},
		{6, "Movie/extra.tur.SDH.sub", 50_000},
		{7, "Movie/huge.en.srt", 6 << 20},
		{8, "Movie/Movie.nfo", 100},
		{9, "Movie/subs.VTT", 100},
	}
	srv := fakeTorrServer(t, files, nil)
	defer srv.Close()

	got, err := ListTorrentSubs(context.Background(), srv.URL+"/", testHash)
	if err != nil {
		t.Fatal(err)
	}
	want := []TorrentSub{
		{FileID: 2, Path: "Movie/Subs/2_English.srt", Language: "en", Format: "srt"},
		{FileID: 3, Path: "Movie/Subs/Movie.tr.forced.ass", Language: "tr", Format: "ass", Forced: true},
		{FileID: 6, Path: "Movie/extra.tur.SDH.sub", Language: "tr", Format: "sub", HI: true},
		{FileID: 9, Path: "Movie/subs.VTT", Language: "", Format: "vtt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	if _, err := ListTorrentSubs(context.Background(), srv.URL, "nothex"); err == nil {
		t.Fatal("invalid hash must fail")
	}
	if _, err := ListTorrentSubs(context.Background(), srv.URL, "0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("unknown torrent must fail")
	}
}

func TestFetchTorrentSub(t *testing.T) {
	srt := readFixture(t, "tr-cp1254.srt")
	srv := fakeTorrServer(t, nil, map[int][]byte{
		1: srt,
		2: bytes.Repeat([]byte("a"), maxSubtitleBytes+10),
	})
	defer srv.Close()

	got, err := FetchTorrentSub(context.Background(), srv.URL, testHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, srt) {
		t.Fatal("content differs")
	}
	if vtt := convert(t, got, "x.srt"); vtt != turkishVTT {
		t.Fatalf("converted:\n%s", vtt)
	}
	if _, err := FetchTorrentSub(context.Background(), srv.URL, testHash, 2); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized file: err = %v", err)
	}
	if _, err := FetchTorrentSub(context.Background(), srv.URL, testHash, 3); err == nil {
		t.Fatal("missing file must fail")
	}
}
