package subtitles

import (
	"strings"
	"sync/atomic"
)

// DebridSource lets the subtitle reads follow a file that plays from a debrid service
// (TorBox) instead of TorrServer. Wired in main.go from the streaming module.
type DebridSource struct {
	// FileURL is the URL to read hash/index from ("" = not a debrid file: TorrServer)
	FileURL func(hash string, index int) string
	// Files lists the torrent's files when hash is a debrid source (ok false otherwise)
	Files func(hash string) ([]DebridFile, bool)
}

type DebridFile struct {
	ID     int
	Path   string
	Length int64
}

var debridHook atomic.Pointer[DebridSource]

func debridFileURL(hash string, index int) string {
	if d := debridHook.Load(); d != nil && d.FileURL != nil {
		return d.FileURL(strings.ToLower(hash), index)
	}
	return ""
}

func debridFiles(hash string) ([]torrFile, bool) {
	d := debridHook.Load()
	if d == nil || d.Files == nil {
		return nil, false
	}
	files, ok := d.Files(strings.ToLower(hash))
	if !ok {
		return nil, false
	}
	out := make([]torrFile, 0, len(files))
	for _, f := range files {
		out = append(out, torrFile{ID: f.ID, Path: f.Path, Length: f.Length})
	}
	return out, true
}
