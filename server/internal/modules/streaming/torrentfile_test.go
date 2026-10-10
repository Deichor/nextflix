package streaming

import (
	"strings"
	"testing"
)

func TestTorrentFileInfo(t *testing.T) {
	data := []byte("d8:announce14:udp://a.b:1/an4:infod4:name1:xee")
	hash, trackers, err := tsTorrentFileInfo(data)
	if err != nil || len(hash) != 40 || len(trackers) != 1 || trackers[0] != "udp://a.b:1/an" {
		t.Fatalf("hash %q trackers %q err %v", hash, trackers, err)
	}
}

// A length near MaxInt64 must not overflow into a slice panic.
func TestBencodeStringHugeLength(t *testing.T) {
	for _, in := range []string{
		"9223372036854775807:abc",
		"9223372036854775806:abc",
		"4:abc",
		"12",
		":x",
	} {
		if _, _, err := bencodeString([]byte(in), 0); err == nil {
			t.Fatalf("%q: no error", in)
		}
	}
	if _, _, err := tsTorrentFileInfo([]byte("d9223372036854775807:xe")); err == nil {
		t.Fatal("huge key length accepted")
	}
	if s, next, err := bencodeString([]byte("3:abc"), 0); err != nil || s != "abc" || next != 5 {
		t.Fatalf("3:abc = %q %d %v", s, next, err)
	}
}

func TestBencodeSkipDepthLimit(t *testing.T) {
	deep := strings.Repeat("l", 100000) + strings.Repeat("e", 100000)
	if _, err := bencodeSkip([]byte(deep), 0); err == nil {
		t.Fatal("deep nesting accepted")
	}
	if _, _, err := tsTorrentFileInfo([]byte("d4:info" + deep + "e")); err == nil {
		t.Fatal("deep info accepted")
	}
	ok := strings.Repeat("l", 10) + strings.Repeat("e", 10)
	if end, err := bencodeSkip([]byte(ok), 0); err != nil || end != len(ok) {
		t.Fatalf("shallow nesting: %d %v", end, err)
	}
}
