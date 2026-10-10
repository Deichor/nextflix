package subtitles

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveScrapedProviders asks the real subf2m and YIFY Subtitles sites through the service
// (list, ranking, download, conversion). Skipped unless NEXTFLIX_LIVE_SUBTITLES=1.
func TestLiveScrapedProviders(t *testing.T) {
	if os.Getenv("NEXTFLIX_LIVE_SUBTITLES") != "1" {
		t.Skip("set NEXTFLIX_LIVE_SUBTITLES=1 to ask the real sites")
	}
	titles := map[int]struct {
		imdb string
		info TitleInfo
	}{
		5920:  {"tt1196946", TitleInfo{Names: []string{"The Mentalist"}, Year: 2008}},
		27205: {"tt1375666", TitleInfo{Names: []string{"Inception"}, Year: 2010}},
	}
	svc := NewService(nil, Config{
		CacheDir: t.TempDir(),
		IMDb:     func(_ string, id int) (string, error) { return titles[id].imdb, nil },
		Titles:   func(_ string, id int) (TitleInfo, error) { return titles[id].info, nil },
	})
	svc.wait = 60 * time.Second
	if err := svc.settings.save(Settings{Subf2mEnabled: true, YIFYEnabled: true, DefaultLangs: "tr"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		q    Query
		want map[string]bool // sources that must deliver
	}{
		{"The Mentalist S02E10", Query{TmdbID: 5920, MediaType: "tv", Season: 2, Episode: 10}, map[string]bool{SourceSubf2m: true}},
		{"Inception (2010)", Query{TmdbID: 27205, MediaType: "movie"}, map[string]bool{SourceSubf2m: true, SourceYIFY: true}},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		items := svc.List(ctx, c.q)
		bySource := map[string][]Sub{}
		for _, it := range items {
			bySource[it.Source] = append(bySource[it.Source], it)
		}
		for _, source := range []string{SourceSubf2m, SourceYIFY} {
			got := bySource[source]
			t.Logf("%s / %s: %d Turkish subtitles", c.name, source, len(got))
			for _, it := range got {
				t.Logf("  %s [%s] by %s, score %.3f", it.Release, it.Language, it.Author, it.Score)
			}
			if len(got) == 0 {
				if c.want[source] {
					t.Errorf("%s: nothing from %s", c.name, source)
				}
				continue
			}
			vtt, err := svc.File(ctx, got[0].ID)
			if err != nil {
				t.Errorf("%s: %s download: %v", c.name, source, err)
				continue
			}
			cues := strings.Count(string(vtt), "-->")
			sample := ""
			for _, line := range strings.Split(string(vtt), "\n") {
				if strings.ContainsAny(line, "ışğİŞĞ") {
					sample = line
					break
				}
			}
			t.Logf("  downloaded %q: %d cues, %d bytes of WebVTT, sample %q", got[0].Release, cues, len(vtt), sample)
			if cues < 100 {
				t.Errorf("%s: %s file has only %d cues", c.name, source, cues)
			}
		}
		cancel()
	}
}
