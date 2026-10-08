package images

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

// Sizes the UI and the warmer use; everything else is rejected.
var allowedSizes = map[string]bool{
	"w92": true, "w154": true, "w185": true, "w300": true, "w342": true,
	"w500": true, "w780": true, "w1280": true, "original": true,
}

// TMDB file names are short random ids plus an extension (e.g. "/kqjL17yufvn9OVLyXYpvtyrFfak.jpg").
var fileRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}\.(jpg|jpeg|png|webp|svg)$`)

var errBadPath = errors.New("invalid image path")

// key identifies one cached image: a TMDB size and a bare file name.
type key struct {
	size string
	file string
}

func (k key) String() string { return k.size + "/" + k.file }

// parseKey validates the :size and *file route params (file comes with a leading slash).
func parseKey(size, file string) (key, error) {
	if !allowedSizes[size] {
		return key{}, errBadPath
	}
	file = strings.TrimPrefix(file, "/")
	if !fileRe.MatchString(file) {
		return key{}, errBadPath
	}
	return key{size: size, file: file}, nil
}

// TMDB list item paths look like "/abc.jpg"; anything else (empty, absolute URLs) is skipped.
func keyFromTMDBPath(size, p string) (key, bool) {
	if !strings.HasPrefix(p, "/") {
		return key{}, false
	}
	k, err := parseKey(size, p)
	return k, err == nil
}

func (k key) contentType() string {
	switch path.Ext(k.file) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}
