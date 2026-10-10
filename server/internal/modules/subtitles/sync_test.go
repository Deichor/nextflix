package subtitles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const syncHash = "73ec17eb899cf91f2ca248fdd65a55e25c30478f"

func TestSyncIDRoundTrip(t *testing.T) {
	inner := encodeID(SourceTurkceAltyazi, "tr", "301213|/sub/301213/the-mentalist.html|2|7|1")
	id := wrapSyncID(inner, syncHash, 1)
	in, hash, index, ok := unwrapSyncID(id)
	if !ok || in != inner || hash != syncHash || index != 1 {
		t.Fatalf("unwrap %q %q %d %v", in, hash, index, ok)
	}
	if _, _, _, ok := unwrapSyncID(inner); ok {
		t.Fatal("a plain id is not a file id")
	}
	if _, _, _, err := decodeID(id); err == nil {
		t.Fatal("a file id is not a plain id")
	}
}

func TestWrongEpisode(t *testing.T) {
	q := Query{MediaType: "tv", Season: 2, Episode: 7}
	cases := map[string]bool{
		"The.Mentalist.S02E07.720p.WEB-DL.DD5.1.H.264-EbP":    false,
		"The.Mentalist.S02E06.720p.WEB-DL.DD5.1.H.264-EbP":    true,
		"The Mentalist - [2x07] - Red Bulls":                  false,
		"Mentalist - 2x08 - Red Bulls":                        true,
		"The.Mentalist.S02E06E07.HDTV":                        false,
		"The.Mentalist.S02E06-1080p.WEB":                      true,
		"S02 (pack) HDTV":                                     false,
		"Season 2/The.Mentalist.S02E05.srt":                   true,
		"The.Mentalist.S02E07.1920x1080.WEB":                  false,
		"[720pMkv.Com]_The.Mentalist.S02E07.480p.WEB.DL.x264": false,
	}
	for name, want := range cases {
		if got := wrongEpisode(candidate{Sub: Sub{Release: name}}, q); got != want {
			t.Errorf("%s: wrong=%v want %v", name, got, want)
		}
	}
	if wrongEpisode(candidate{Sub: Sub{Release: "S02E01"}, Pack: true}, q) {
		t.Error("a pack is never the wrong episode")
	}
	if wrongEpisode(candidate{Sub: Sub{Release: "S02E01"}}, Query{MediaType: "movie"}) {
		t.Error("movies have no episodes")
	}
}

func vttOf(cues []span) []byte {
	lc := make([]liveCue, len(cues))
	for i, c := range cues {
		lc[i] = liveCue{start: c.start, end: c.end, text: fmt.Sprintf("line %d", i)}
	}
	return renderVTT(lc, false)
}

// newSyncTest is a service whose file reads come from the given true cues of the file.
func newSyncTest(t *testing.T, file []span) (*Service, *int) {
	saved := syncReadDelay
	syncReadDelay = 0
	t.Cleanup(func() { syncReadDelay = saved })
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver"})
	reads := 0
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, TextSub: -1}, nil
	}
	svc.sync.read = func(_ context.Context, _ string, _ mediaProbe, start, _ float64) (refWindow, error) {
		reads++
		return truthWindows(file, []float64{start}, 0.1, 0, int64(start))[0], nil
	}
	return svc, &reads
}

func cacheProviderSub(t *testing.T, svc *Service, ref string, cues []span) string {
	id := encodeID(SourceTurkceAltyazi, "tr", ref)
	if err := svc.files.write(SourceTurkceAltyazi, SourceTurkceAltyazi+"|"+ref, ".vtt", vttOf(cues)); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServeTimesTheSubtitle(t *testing.T) {
	file := dialogue(11, episode)
	svc, reads := newSyncTest(t, file)
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 1300 {
			return 0
		}
		return -4
	})
	inner := cacheProviderSub(t, svc, "1|a|2|7|1", sub)
	id := wrapSyncID(inner, syncHash, 1)

	a, err := svc.FileFor(context.Background(), id, 0)
	if err != nil || a.Partial || a.Sync != syncStateSynced {
		t.Fatalf("answer partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	got := vttCues(a.VTT)
	if len(got) != len(file) {
		t.Fatalf("%d cues, want %d", len(got), len(file))
	}
	on := 0
	for i := range got {
		if math.Abs(got[i].start-file[i].start) <= 0.05 {
			on++
		}
	}
	if share := float64(on) / float64(len(got)); share < 0.97 {
		t.Fatalf("only %.2f of the lines on time", share)
	}
	n := *reads
	// cached: no new reads, same answer
	b, _ := svc.FileFor(context.Background(), id, 0)
	if *reads != n || string(b.VTT) != string(a.VTT) || b.Sync != syncStateSynced {
		t.Fatalf("second answer reads %d->%d sync %q", n, *reads, b.Sync)
	}
	// the list now counts it as synced
	if svc.sync.fitOf(inner, syncHash, 1) != 1 {
		t.Fatal("fit not recorded")
	}
}

func TestServeReplacesASubtitleThatDoesNotFit(t *testing.T) {
	file := dialogue(21, episode)
	svc, _ := newSyncTest(t, file)
	bad := cacheProviderSub(t, svc, "1|bad|2|7|1", dialogue(99, episode))
	good := cacheProviderSub(t, svc, "2|good|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2.5 }))
	svc.sync.listed([]Sub{
		{ID: bad, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: good, Source: SourceTurkceAltyazi, Language: "tr"},
	}, map[string]bool{}, syncHash, 1)

	a, err := svc.FileFor(context.Background(), wrapSyncID(bad, syncHash, 1), 0)
	if err != nil || a.Sync != syncStateReplaced {
		t.Fatalf("sync %q err %v", a.Sync, err)
	}
	if got := vttCues(a.VTT); len(got) != len(file) || math.Abs(got[10].start-file[10].start) > 0.05 {
		t.Fatal("not the fitting subtitle's timed cues")
	}
	if svc.sync.fitOf(bad, syncHash, 1) != -1 || svc.sync.fitOf(good, syncHash, 1) != 1 {
		t.Fatal("fits not recorded")
	}
	// Ranked again: the fitting one first, shown as synced
	subs := rankCandidates([]candidate{
		{Sub: Sub{ID: bad, Source: SourceTurkceAltyazi, Language: "tr", Downloads: 30000}, AutoSync: -1},
		{Sub: Sub{ID: good, Source: SourceTurkceAltyazi, Language: "tr"}, AutoSync: 1},
	}, Query{Langs: []string{"tr"}})
	if subs[0].ID != good || !subs[0].Synced || subs[1].Synced {
		t.Fatalf("ranked %+v", subs)
	}
}

func TestServeRetimesAFallbackWhoseCuesAreGone(t *testing.T) {
	file := dialogue(23, episode)
	svc, _ := newSyncTest(t, file)
	bad := cacheProviderSub(t, svc, "1|bad|2|7|1", dialogue(98, episode))
	good := cacheProviderSub(t, svc, "2|good|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 2.5 }))
	svc.sync.listed([]Sub{
		{ID: bad, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: good, Source: SourceTurkceAltyazi, Language: "tr"},
	}, map[string]bool{}, syncHash, 1)
	if a, err := svc.FileFor(context.Background(), wrapSyncID(good, syncHash, 1), 0); err != nil || a.Sync != syncStateSynced {
		t.Fatalf("good: sync %q err %v", a.Sync, err)
	}
	// The cache sweep removed the timed cues; the result entry is still known
	goodKey, _ := syncKey(good, syncHash, 1)
	if err := os.Remove(svc.files.path("synced", goodKey, ".vtt")); err != nil {
		t.Fatal(err)
	}

	a, err := svc.FileFor(context.Background(), wrapSyncID(bad, syncHash, 1), 0)
	if err != nil || a.Sync != syncStateReplaced {
		t.Fatalf("sync %q err %v", a.Sync, err)
	}
	if got := vttCues(a.VTT); len(got) != len(file) || math.Abs(got[10].start-file[10].start) > 0.05 {
		t.Fatal("served the fallback untimed")
	}
}

func TestServePendingIsPartial(t *testing.T) {
	file := dialogue(31, episode)
	svc, _ := newSyncTest(t, file)
	block := make(chan struct{})
	svc.sync.read = func(ctx context.Context, _ string, _ mediaProbe, start, _ float64) (refWindow, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return truthWindows(file, []float64{start}, 0, 0, 1)[0], nil
	}
	saved := syncFirstWait
	syncFirstWait = 0
	inner := cacheProviderSub(t, svc, "1|p|2|7|1", subtitleFor(file, 1, func(float64) float64 { return 1 }))
	a, err := svc.FileFor(context.Background(), wrapSyncID(inner, syncHash, 1), 0)
	if err != nil || !a.Partial || a.Sync != syncStatePending || len(vttCues(a.VTT)) != len(file) {
		t.Fatalf("partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	// released: the next request joins the running timing and gets its result
	close(block)
	syncFirstWait = saved
	if b, _ := svc.FileFor(context.Background(), wrapSyncID(inner, syncHash, 1), 0); b.Partial || b.Sync != syncStateSynced {
		t.Fatalf("after: partial=%v sync=%q", b.Partial, b.Sync)
	}
}

func TestTorrentSubForFile(t *testing.T) {
	const gb = 2 << 30
	cases := []struct {
		name       string
		files      []torrFile
		sub, video int
		want       bool
	}{
		{"named after the file", []torrFile{{1, "Movie.One.mkv", gb}, {2, "Movie.One.en.srt", 1}, {3, "Movie.Two.mkv", gb}}, 2, 1, true},
		{"named after another file", []torrFile{{1, "Movie.One.mkv", gb}, {2, "Movie.Two.srt", 1}, {3, "Movie.Two.mkv", gb}}, 2, 1, false},
		{"sequel name", []torrFile{{1, "Toy Story.mkv", gb}, {2, "Toy Story 2.srt", 1}, {3, "Toy Story 2.mkv", gb}}, 2, 1, false},
		{"own folder", []torrFile{{1, "C/One/One.mkv", gb}, {2, "C/One/Subs/English.srt", 1}, {3, "C/Two/Two.mkv", gb}}, 2, 1, true},
		{"other folder", []torrFile{{1, "C/One/One.mkv", gb}, {2, "C/Two/Subs/English.srt", 1}, {3, "C/Two/Two.mkv", gb}}, 2, 1, false},
		{"sample is not another file", []torrFile{{1, "M/M.mkv", gb}, {2, "M/Subs/English.srt", 1}, {3, "M/Sample/s.mkv", 50 << 20}}, 2, 1, true},
		{"episode folder", []torrFile{{1, "S/Show.S01E01.mkv", gb}, {2, "S/Subs/Show.S01E02/2_English.srt", 1}, {3, "S/Show.S01E02.mkv", gb}}, 2, 3, true},
		{"other episode folder", []torrFile{{1, "S/Show.S01E01.mkv", gb}, {2, "S/Subs/Show.S01E02/2_English.srt", 1}, {3, "S/Show.S01E02.mkv", gb}}, 2, 1, false},
		{"pack subtitle naming no episode", []torrFile{{1, "S/E01.mkv", gb}, {2, "S/Subs/English.srt", 1}, {3, "S/E02.mkv", gb}}, 2, 1, false},
		{"unknown file", []torrFile{{1, "M.mkv", gb}}, 2, 1, false},
	}
	for _, c := range cases {
		if got := torrentSubForFile(c.files, c.sub, c.video); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFullReferenceOnlyFromTheFilesSubtitle(t *testing.T) {
	files := []torrFile{
		{1, "Collection/Movie.One.2001/Movie.One.2001.mkv", 2 << 30},
		{2, "Collection/Movie.One.2001/Subs/English.vtt", 80_000},
		{3, "Collection/Movie.Two.2003/Movie.Two.2003.mkv", 2 << 30},
	}
	srv := fakeTorrServer(t, files, map[int][]byte{2: vttOf(dialogue(41, episode))})
	defer srv.Close()
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: srv.URL})
	sub := Sub{ID: encodeID(SourceTorrent, "en", testHash+"|2|vtt"), Source: SourceTorrent, Language: "en", Synced: true}
	ctx := context.Background()

	svc.sync.listed([]Sub{sub}, map[string]bool{}, testHash, 3)
	if got := svc.sync.fullReference(ctx, testHash, 3); got != nil {
		t.Fatalf("another movie's subtitle used as the reference (%d cues)", len(got))
	}
	svc.sync.listed([]Sub{sub}, map[string]bool{}, testHash, 1)
	if got := svc.sync.fullReference(ctx, testHash, 1); len(got) == 0 {
		t.Fatal("the file's own subtitle not used")
	}
}

func TestWeakReferenceIsNotKept(t *testing.T) {
	file := dialogue(51, episode)
	svc, _ := newSyncTest(t, file)
	n := 0
	svc.sync.read = func(_ context.Context, _ string, _ mediaProbe, start, _ float64) (refWindow, error) {
		if n++; n > 2 {
			return refWindow{}, errors.New("timed out")
		}
		return truthWindows(file, []float64{start}, 0.1, 0, int64(start))[0], nil
	}
	if r, err := svc.sync.reference(context.Background(), syncHash, 1); err == nil {
		t.Fatalf("a reference of %d windows accepted", len(r.Windows))
	}
	if svc.files.has("syncref", syncFileKey(syncHash, 1), ".json") {
		t.Fatal("weak reference saved")
	}
}

func TestRefStateEvictionKeepsUsedStates(t *testing.T) {
	y := NewService(nil, Config{CacheDir: t.TempDir()}).sync
	held, release := y.refState("held")
	for i := range 40 {
		_, r := y.refState(strconv.Itoa(i))
		r()
	}
	st, r := y.refState("held")
	r()
	release()
	if st != held {
		t.Fatal("a state in use was evicted")
	}
}

func TestAddWindowsAfterEviction(t *testing.T) {
	file := dialogue(61, episode)
	svc, _ := newSyncTest(t, file)
	ctx := context.Background()
	ref, err := svc.sync.reference(ctx, syncHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		_, r := svc.sync.refState(strconv.Itoa(i))
		r()
	}
	more, err := svc.sync.addWindows(ctx, syncHash, 1, []float64{1000})
	if err != nil || len(more.Windows) != len(ref.Windows)+1 {
		t.Fatalf("%d -> %d windows: %v", len(ref.Windows), len(more.Windows), err)
	}
}

// TestSyncWindowFFmpeg reads windows of a generated MKV through the byte cap: cues come at the
// file's times, and a subtitle read in a stretch without lines (which ffmpeg would continue to
// the end of the file) stops at the cap. It needs ffmpeg (in the server image, not in plain
// golang images) and writes a ~150 MB file to the temp dir.
func TestSyncWindowFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	var srt strings.Builder
	for i := 0; i < 20; i++ { // lines at 1, 4, ... 58 s; none after
		s := 3*i + 1
		fmt.Fprintf(&srt, "%d\n00:00:%02d,000 --> 00:00:%02d,500\nLine %d\n\n", i+1, s, s+1, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.srt"), []byte(srt.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(dir, "f.mkv")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=s=320x180:d=180:r=24",
		"-f", "lavfi", "-i", "sine=frequency=300:duration=180", "-i", filepath.Join(dir, "s.srt"),
		"-map", "0", "-map", "1", "-map", "2", "-vf", "noise=alls=60:allf=t", "-c:v", "mpeg4", "-q:v", "4", "-g", "240",
		"-c:a", "aac", "-c:s", "srt", "-metadata:s:s:0", "language=eng", mkv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(mkv)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.mkv", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	ctx := context.Background()
	p, err := probeMedia(ctx, srv.URL)
	if err != nil || p.TextSub < 0 || p.Duration < 179 || p.Bitrate <= 0 {
		t.Fatalf("probe %+v %v", p, err)
	}
	limit := windowByteCap(p, 20) + 64<<10

	w, err := ffmpegSyncWindow(ctx, srv.URL, p, 20.5, 20)
	if err != nil || w.Kind != "subs" {
		t.Fatalf("window %v kind %q", err, w.Kind)
	}
	var at []int
	for i, v := range w.Speech {
		if v == 2 {
			at = append(at, i)
		}
	}
	// lines start at 22, 25, ... 40 (from the file's start time: AAC priming moves them ~20 ms)
	if len(at) != 7 || at[0] < 148 || at[0] > 154 || at[6]-at[0] != 1800 || w.Read > limit {
		t.Fatalf("line starts at frames %v, read %d (cap %d)", at, w.Read, limit)
	}
	// No lines after 58 s: the subtitle read would go on to the end; the cap stops it
	w, _ = ffmpegSyncWindow(ctx, srv.URL, p, 100, 20)
	if w.Read > limit {
		t.Fatalf("silent stretch read %d (cap %d, file %d)", w.Read, limit, len(data))
	}
	t.Logf("file %d MB, cap per 20 s window %d MB", len(data)>>20, limit>>20)
}
