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
	// Over every entry of the title
	FinishedEpisodes int
	WatchedSeconds   float64
	UpdatedAt        time.Time
}

// History returns every title the profile has progress for (finished or not), newest first.
func (s *Service) History(profileID uint) ([]TitleWatch, error) {
	rows, err := listByProfile(s.db, profileID, historyLimit)
	if err != nil {
		return nil, err
	}
	at := map[string]int{}
	out := make([]TitleWatch, 0, len(rows))
	for _, r := range rows {
		key := fmt.Sprintf("%s:%d", r.MediaType, r.TmdbID)
		i, ok := at[key]
		if !ok {
			i = len(out)
			at[key] = i
			out = append(out, TitleWatch{MediaType: r.MediaType, TmdbID: r.TmdbID, Title: r.Title,
				Finished: r.finished(), UpdatedAt: r.UpdatedAt})
		}
		out[i].WatchedSeconds += r.Position
		if r.MediaType == "tv" && r.finished() {
			out[i].FinishedEpisodes++
		}
	}
	return out, nil
}
