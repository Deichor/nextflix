package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Embedded subtitle tracks are read with ffprobe/ffmpeg straight from the TorrServer stream.
//
// In MKV and MP4 the subtitle packets are interleaved with the video across the whole file, so extracting one
// track means reading (downloading) the entire file. Probing is cheap (headers only). A whole extraction runs
// when that is practical right now: the file is small, or TorrServer already has most of it in its cache.
// Otherwise the track is Live: it is read from the playing position on while the file streams (live.go).
// Image based tracks (PGS, VobSub, DVB) cannot become WebVTT and are never available.

const (
	embeddedSmallFile     = 800 << 20 // extraction is fine below this size
	embeddedCachedEnough  = 0.9       // or when this share of the file is cached
	embeddedProbeTimeout  = 45 * time.Second
	embeddedExtractLimit  = 10 * time.Minute
	embeddedMaxOutputSize = 20 << 20
)

// inputLimits go before every -i (and ffprobe's input): the files are untrusted, so ffmpeg may
// only read them over HTTP(S) (TorrServer, the TorBox proxy, the local byte cap proxy) and only
// as video containers (no playlists, concat lists or image sequences that open other files).
var inputLimits = []string{
	"-protocol_whitelist", "http,https,tcp,tls",
	"-format_whitelist", "matroska,webm,mov,mp4,avi,mpegts,flv,asf",
}

// withInput appends the input limits and -i url to args.
func withInput(args []string, url string) []string {
	args = append(args, inputLimits...)
	return append(args, "-i", url)
}

var textSubCodecs = map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true}

type EmbeddedSub struct {
	StreamIndex            int // absolute ffprobe stream index, used as -map 0:<StreamIndex>
	Language, Title, Codec string
	Forced, HI             bool
	Available              bool // a text track (WebVTT can be made of it)
	Live                   bool // available, but read live while the file plays (big file, not cached)
}

type probeOutput struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
}

// ListEmbeddedSubs lists the subtitle tracks of a torrent's video file (ffprobe on the TorrServer URL).
func ListEmbeddedSubs(ctx context.Context, torrServerURL, hash string, fileIndex int) ([]EmbeddedSub, error) {
	if !torrHashPattern.MatchString(hash) || fileIndex < 0 {
		return nil, fmt.Errorf("invalid torrent file %q/%d", hash, fileIndex)
	}
	pctx, cancel := context.WithTimeout(ctx, embeddedProbeTimeout)
	defer cancel()
	args := withInput([]string{"-v", "error", "-print_format", "json", "-show_streams", "-select_streams", "s"},
		torrFileURL(torrServerURL, hash, fileIndex))
	out, err := exec.CommandContext(pctx, "ffprobe", args...).Output()
	if err != nil {
		if pctx.Err() != nil {
			// Killed at the time limit (a slow swarm): asked again on the next list, not cached as failed
			return nil, fmt.Errorf("ffprobe failed: %w", context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	subs, err := parseProbe(out)
	if err != nil || len(subs) == 0 {
		return subs, err
	}

	practical := extractionPractical(ctx, torrServerURL, hash, fileIndex)
	for i := range subs {
		subs[i].Live = subs[i].Available && !practical
	}
	return subs, nil
}

// parseProbe turns ffprobe's JSON into tracks; Available here only says whether the codec is text.
func parseProbe(out []byte) ([]EmbeddedSub, error) {
	var p probeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	var subs []EmbeddedSub
	for _, s := range p.Streams {
		if s.CodecType != "" && s.CodecType != "subtitle" {
			continue
		}
		tags := map[string]string{}
		for k, v := range s.Tags {
			tags[strings.ToLower(k)] = v
		}
		title := tags["title"]
		forced, hi := subFlags(title)
		forced = forced || s.Disposition["forced"] == 1
		hi = hi || s.Disposition["hearing_impaired"] == 1 || s.Disposition["captions"] == 1
		lang := iso639To1(tags["language"])
		if lang == "" {
			lang = GuessLanguage(title)
		}
		subs = append(subs, EmbeddedSub{
			StreamIndex: s.Index,
			Language:    lang,
			Title:       title,
			Codec:       s.CodecName,
			Forced:      forced,
			HI:          hi,
			Available:   textSubCodecs[s.CodecName],
		})
	}
	return subs, nil
}

// extractionPractical reports whether reading the whole file is cheap: a small file or one mostly cached.
func extractionPractical(ctx context.Context, torrServerURL, hash string, fileIndex int) bool {
	files, err := torrentFiles(ctx, torrServerURL, hash)
	if err != nil {
		return false
	}
	var offset, length int64 = 0, -1
	for _, f := range files {
		if f.ID == fileIndex {
			length = f.Length
			break
		}
		offset += f.Length
	}
	if length < 0 {
		return false
	}
	if length <= embeddedSmallFile {
		return true
	}
	cached, err := cachedBytes(ctx, torrServerURL, hash, offset, length)
	return err == nil && float64(cached) >= embeddedCachedEnough*float64(length)
}

type torrCache struct {
	PiecesLength int64 `json:"PiecesLength"`
	Pieces       map[string]struct {
		Completed bool `json:"Completed"`
	} `json:"Pieces"`
}

// cachedBytes sums the completed TorrServer cache pieces inside a file's byte range (files are laid out in id order).
func cachedBytes(ctx context.Context, torrServerURL, hash string, offset, length int64) (int64, error) {
	body := fmt.Sprintf(`{"action":"get","hash":%q}`, strings.ToLower(hash))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(torrServerURL, "/")+"/cache", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := torrHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("torrserver cache returned %d", resp.StatusCode)
	}
	var c torrCache
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&c); err != nil {
		return 0, err
	}
	if c.PiecesLength <= 0 {
		return 0, errors.New("torrserver cache has no piece length")
	}
	first, last := offset/c.PiecesLength, (offset+length-1)/c.PiecesLength
	var total int64
	for k, p := range c.Pieces {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil || id < first || id > last || !p.Completed {
			continue
		}
		total += c.PiecesLength
	}
	if total > length {
		total = length
	}
	return total, nil
}

// ExtractEmbeddedSub extracts one text subtitle track as WebVTT with ffmpeg. It reads the whole file.
func ExtractEmbeddedSub(ctx context.Context, torrServerURL, hash string, fileIndex, streamIndex int) ([]byte, error) {
	if !torrHashPattern.MatchString(hash) || fileIndex < 0 || streamIndex < 0 {
		return nil, fmt.Errorf("invalid torrent stream %q/%d/%d", hash, fileIndex, streamIndex)
	}
	ctx, cancel := context.WithTimeout(ctx, embeddedExtractLimit)
	defer cancel()
	args := withInput([]string{"-nostdin", "-v", "error"}, torrFileURL(torrServerURL, hash, fileIndex))
	args = append(args, "-map", fmt.Sprintf("0:%d", streamIndex), "-f", "webvtt", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stdout limitedBuffer
	var stderr bytes.Buffer
	stdout.max = embeddedMaxOutputSize
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// Normalise ffmpeg's output the same way as downloaded files.
	return ToWebVTT(stdout.Bytes(), "embedded.vtt")
}

func torrFileURL(torrServerURL, hash string, fileIndex int) string {
	if u := debridFileURL(hash, fileIndex); u != "" {
		return u
	}
	return fmt.Sprintf("%s/stream?link=%s&index=%d&play", strings.TrimRight(torrServerURL, "/"), strings.ToLower(hash), fileIndex)
}

// limitedBuffer stops ffmpeg (by failing the write) once the output grows past max.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, ErrTooLarge
	}
	return b.Buffer.Write(p)
}
