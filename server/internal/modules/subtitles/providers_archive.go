package subtitles

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ErrRarUnsupported is returned for RAR archives: there is no RAR reader in the standard library.
var ErrRarUnsupported = errors.New("RAR archives are not supported")

func isZip(b []byte) bool { return len(b) > 4 && bytes.HasPrefix(b, []byte("PK\x03\x04")) }
func isRar(b []byte) bool { return bytes.HasPrefix(b, []byte("Rar!\x1a\x07")) }

var subExts = map[string]int{".srt": 4, ".ass": 3, ".ssa": 3, ".vtt": 3, ".sub": 1}

var (
	epSxE    = regexp.MustCompile(`(?i)s(\d{1,2})[ ._-]*e(\d{1,3})`)
	epNxN    = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])(\d{1,2})x(\d{1,3})(?:[^0-9]|$)`)
	epE      = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:e|ep|episode|b[öo]l[üu]m)[ ._-]*(\d{1,3})(?:[^0-9]|$)`)
	epBolumN = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,3})[ ._-]*\.?[ ._-]*b[öo]l[üu]m`)
)

// episodeOf guesses season and episode from a file name; 0 when unknown.
func episodeOf(name string) (season, episode int) {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	if m := epSxE.FindStringSubmatch(base); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return s, e
	}
	if m := epNxN.FindStringSubmatch(base); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return s, e
	}
	if m := epE.FindStringSubmatch(base); m != nil {
		e, _ := strconv.Atoi(m[1])
		return 0, e
	}
	if m := epBolumN.FindStringSubmatch(base); m != nil {
		e, _ := strconv.Atoi(m[1])
		return 0, e
	}
	return 0, 0
}

// pickFromArchive returns the subtitle file of a zip that fits: the episode for packs (season and
// episode > 0), a file in lang when the names tell, else the biggest .srt.
func pickFromArchive(raw []byte, season, episode int, lang string) ([]byte, string, error) {
	if isRar(raw) {
		return nil, "", ErrRarUnsupported
	}
	if !isZip(raw) {
		return nil, "", errors.New("not a zip archive")
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, "", err
	}
	var best *zip.File
	bestScore := -1
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > 20<<20 {
			continue
		}
		name := f.Name
		low := strings.ToLower(name)
		if strings.HasPrefix(path.Base(low), "._") || strings.Contains(low, "__macosx/") {
			continue
		}
		extScore, ok := subExts[path.Ext(low)]
		if !ok {
			continue
		}
		score := extScore
		if episode > 0 {
			s, e := episodeOf(name)
			if e != episode || (s != 0 && season != 0 && s != season) {
				continue
			}
			score += 100
			if s == season {
				score += 10
			}
		}
		if lang != "" {
			switch GuessLanguage(path.Base(name)) {
			case lang:
				score += 20
			case "":
			default:
				score -= 20
			}
		}
		// Bigger files win ties (full subtitles over forced or sample ones).
		score = score*1000 + int(min(f.UncompressedSize64/1024, 999))
		if score > bestScore {
			best, bestScore = f, score
		}
	}
	if best == nil {
		if episode > 0 {
			return nil, "", errors.New("episode not found in the subtitle pack")
		}
		return nil, "", errors.New("no subtitle file in the archive")
	}
	rc, err := best.Open()
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 20<<20))
	return b, best.Name, err
}
