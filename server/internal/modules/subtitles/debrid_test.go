package subtitles

import (
	"context"
	"testing"
)

func TestDebridHookRedirectsTorrentReads(t *testing.T) {
	const hash = "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c"
	debridHook.Store(&DebridSource{
		FileURL: func(h string, index int) string {
			if h == hash && index == 2 {
				return "http://127.0.0.1:9/f/" + h + "/2"
			}
			return ""
		},
		Files: func(h string) ([]DebridFile, bool) {
			if h != hash {
				return nil, false
			}
			return []DebridFile{{ID: 1, Path: "Movie/Movie.en.srt", Length: 900}, {ID: 2, Path: "Movie/Movie.mp4", Length: 5 << 20}}, true
		},
	})
	t.Cleanup(func() { debridHook.Store(nil) })

	if got := torrFileURL("http://torrserver:8090", "DD8255ECDC7CA55FB0BBF81323D87062DB1F6D1C", 2); got != "http://127.0.0.1:9/f/"+hash+"/2" {
		t.Fatalf("debrid file: %s", got)
	}
	if got := torrFileURL("http://torrserver:8090", hash, 1); got != "http://torrserver:8090/stream?link="+hash+"&index=1&play" {
		t.Fatalf("other file: %s", got)
	}
	// The file list comes from TorBox, TorrServer (unreachable here) is not asked
	subs, err := ListTorrentSubs(context.Background(), "http://127.0.0.1:1", hash)
	if err != nil || len(subs) != 1 || subs[0].FileID != 1 || subs[0].Language != "en" {
		t.Fatalf("subs %+v %v", subs, err)
	}
}
