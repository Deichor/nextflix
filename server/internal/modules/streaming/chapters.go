package streaming

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Chapter is a named chapter marker of a file, in seconds.
type Chapter struct {
	Start, End float64
	Title      string
}

// Chapters returns the chapter markers and the length of a file. The player probed the file
// before it asks, so this is normally a cache hit; otherwise it joins or runs the probe.
func (s *Service) Chapters(ctx context.Context, hash string, index int) ([]Chapter, float64, error) {
	p, err := s.probeFileTimeout(ctx, strings.ToLower(hash), index, 15*time.Second)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Chapter, 0, len(p.Chapters))
	for _, c := range p.Chapters {
		start, err1 := strconv.ParseFloat(c.StartTime, 64)
		end, err2 := strconv.ParseFloat(c.EndTime, 64)
		if err1 != nil || err2 != nil || end <= start {
			continue
		}
		title := c.Tags["title"]
		if title == "" {
			title = c.Tags["TITLE"]
		}
		out = append(out, Chapter{Start: start, End: end, Title: title})
	}
	return out, p.duration(), nil
}
