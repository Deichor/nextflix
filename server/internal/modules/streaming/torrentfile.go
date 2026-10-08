package streaming

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// tsTorrentFileInfo reads the info-hash and trackers from a .torrent file
func tsTorrentFileInfo(data []byte) (string, []string, error) {
	if len(data) == 0 || data[0] != 'd' {
		return "", nil, errors.New("not a torrent file")
	}
	var hash string
	var trackers []string
	i := 1
	for i < len(data) && data[i] != 'e' {
		key, next, err := bencodeString(data, i)
		if err != nil {
			return "", nil, err
		}
		end, err := bencodeSkip(data, next)
		if err != nil {
			return "", nil, err
		}
		switch key {
		case "info":
			sum := sha1.Sum(data[next:end])
			hash = hex.EncodeToString(sum[:])
		case "announce":
			if s, _, err := bencodeString(data, next); err == nil {
				trackers = append(trackers, s)
			}
		case "announce-list":
			trackers = append(trackers, bencodeAllStrings(data[next:end])...)
		}
		i = end
	}
	if hash == "" {
		return "", nil, errors.New("torrent has no info dictionary")
	}
	return hash, trackers, nil
}

func bencodeString(data []byte, i int) (string, int, error) {
	colon := i
	for colon < len(data) && data[colon] != ':' {
		if data[colon] < '0' || data[colon] > '9' {
			return "", 0, errors.New("invalid bencode string")
		}
		colon++
	}
	n, err := strconv.Atoi(string(data[i:colon]))
	if err != nil || colon+1+n > len(data) || n < 0 {
		return "", 0, errors.New("invalid bencode string")
	}
	return string(data[colon+1 : colon+1+n]), colon + 1 + n, nil
}

// bencodeSkip returns the index just after the value starting at i
func bencodeSkip(data []byte, i int) (int, error) {
	if i >= len(data) {
		return 0, errors.New("unexpected end of bencode")
	}
	switch data[i] {
	case 'i':
		end := i + 1
		for end < len(data) && data[end] != 'e' {
			end++
		}
		if end >= len(data) {
			return 0, errors.New("unterminated integer")
		}
		return end + 1, nil
	case 'l', 'd':
		j := i + 1
		for j < len(data) && data[j] != 'e' {
			next, err := bencodeSkip(data, j)
			if err != nil {
				return 0, err
			}
			j = next
		}
		if j >= len(data) {
			return 0, errors.New("unterminated container")
		}
		return j + 1, nil
	default:
		_, next, err := bencodeString(data, i)
		return next, err
	}
}

// bencodeAllStrings collects tracker URLs from a nested bencode list
func bencodeAllStrings(data []byte) []string {
	var out []string
	i := 0
	for i < len(data) {
		switch data[i] {
		case 'l', 'e':
			i++
		default:
			if data[i] < '0' || data[i] > '9' {
				return out
			}
			s, next, err := bencodeString(data, i)
			if err != nil {
				return out
			}
			if strings.Contains(s, "://") {
				out = append(out, s)
			}
			i = next
		}
	}
	return out
}
