package subtitles

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const hashChunk = 64 * 1024

// movieHash is the OpenSubtitles hash: file size plus the 64-bit little-endian word sums of
// the first and the last 64 KiB, as 16 hex digits.
func movieHash(head, tail []byte, size int64) string {
	h := uint64(size)
	for _, b := range [][]byte{head, tail} {
		for i := 0; i+8 <= len(b); i += 8 {
			h += binary.LittleEndian.Uint64(b[i:])
		}
	}
	return fmt.Sprintf("%016x", h)
}

// movieHashReader hashes a seekable source of known size.
func movieHashReader(r io.ReaderAt, size int64) (string, error) {
	if size < hashChunk {
		return "", errors.New("file too small for a movie hash")
	}
	head := make([]byte, hashChunk)
	if _, err := r.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	tail := make([]byte, hashChunk)
	if _, err := r.ReadAt(tail, size-hashChunk); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return movieHash(head, tail, size), nil
}

// torrServerFileHash hashes a torrent file through two HTTP range reads on TorrServer.
func torrServerFileHash(ctx context.Context, client *http.Client, torrServerURL, hash string, index int) (string, int64, error) {
	u := torrFileURL(torrServerURL, hash, index) // TorrServer, or the debrid proxy
	head, size, err := rangeRead(ctx, client, u, 0, hashChunk-1)
	if err != nil {
		return "", 0, err
	}
	if size < hashChunk {
		return "", 0, errors.New("file too small for a movie hash")
	}
	tail, _, err := rangeRead(ctx, client, u, size-hashChunk, size-1)
	if err != nil {
		return "", 0, err
	}
	if len(head) != hashChunk || len(tail) != hashChunk {
		return "", 0, errors.New("short range read")
	}
	return movieHash(head, tail, size), size, nil
}

// rangeRead fetches bytes from..to and returns them with the total size from Content-Range.
func rangeRead(ctx context.Context, client *http.Client, u string, from, to int64) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, 0, fmt.Errorf("range read: status %d", resp.StatusCode)
	}
	total := int64(-1)
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			total, _ = strconv.ParseInt(cr[i+1:], 10, 64)
		}
	}
	if total <= 0 {
		return nil, 0, errors.New("range read: no total size")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, to-from+1))
	return b, total, err
}
