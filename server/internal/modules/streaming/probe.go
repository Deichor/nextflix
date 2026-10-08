package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

type tsProbeStream struct {
	CodecType string            `json:"codec_type"`
	CodecName string            `json:"codec_name"`
	PixFmt    string            `json:"pix_fmt"`
	Channels  int               `json:"channels"`
	Tags      map[string]string `json:"tags"`
}

type tsProbe struct {
	Streams []tsProbeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// probeFile runs ffprobe on a torrent file through TorrServer (cached per hash:index).
func (s *Service) probeFile(ctx context.Context, hash string, index int) (*tsProbe, error) {
	key := fmt.Sprintf("%s:%d", hash, index)
	if p, ok := s.probeCache.Load(key); ok {
		return p.(*tsProbe), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", s.fileURL(hash, index)).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var p tsProbe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	s.probeCache.Store(key, &p)
	return &p, nil
}
