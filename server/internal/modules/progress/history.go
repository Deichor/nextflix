package progress

import (
	"fmt"
	"time"
)

const historyLimit = 2000

// TitleWatch is one title a profile has played: its newest entry decides Finished.
type TitleWatch struct {
	MediaType string
	TmdbID    int
	Title     string
	Finished  bool
	UpdatedAt time.Time
}

// History returns every title the profile has progress for (finished or not), newest first.
func (s *Service) History(profileID uint) ([]TitleWatch, error) {
	rows, err := listByProfile(s.db, profileID, historyLimit)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]TitleWatch, 0, len(rows))
	for _, r := range rows {
		key := fmt.Sprintf("%s:%d", r.MediaType, r.TmdbID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, TitleWatch{MediaType: r.MediaType, TmdbID: r.TmdbID, Title: r.Title,
			Finished: r.finished(), UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}
