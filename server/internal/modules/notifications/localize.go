package notifications

import (
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
)

// localizeWait bounds how long the list waits for titles in another language. Slower
// lookups leave English text; they finish in the background and the TMDB client caches
// them for the next request.
const localizeWait = 3 * time.Second

// localText is the display text of one title in another language.
type localText struct {
	title    string
	overview string
	genres   []string
}

func (t NotificationType) isTV() bool {
	return t == TypeTMDBUpcomingTV || t == TypeTMDBNowAiringTV
}

type titleRef struct {
	tv bool
	id int
}

// localize returns copies of items with TMDB titles, overviews and genres in lang ("en" or
// "tr"). The generated title/message text stays English; the web builds its own wording.
func (s *Service) localize(items []Notification, lang string) []Notification {
	if lang == tmdb.LangEN || len(items) == 0 || !s.tm.Enabled() {
		return items
	}
	tm := s.tm.WithLang(lang)
	refs := map[titleRef]bool{}
	for _, n := range items {
		for _, id := range n.TMDBIDs {
			refs[titleRef{n.Type.isTV(), id}] = true
		}
	}

	var mu sync.Mutex
	found := map[titleRef]localText{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for r := range refs {
			wg.Add(1)
			go func(r titleRef) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				var t localText
				if r.tv {
					d, err := tm.GetTVDetails(r.id)
					if err != nil {
						return
					}
					t = localText{d.Name, d.Overview, genreNames(d.Genres)}
				} else {
					d, err := tm.GetMovieDetails(r.id)
					if err != nil {
						return
					}
					t = localText{d.Title, d.Overview, genreNames(d.Genres)}
				}
				mu.Lock()
				found[r] = t
				mu.Unlock()
			}(r)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(localizeWait):
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]Notification, len(items))
	for i, n := range items {
		tv := n.Type.isTV()
		out[i] = applyLocal(n, func(id int) (localText, bool) {
			t, ok := found[titleRef{tv, id}]
			return t, ok
		})
	}
	return out
}

// applyLocal returns n with the TMDB text of its titles replaced by local's. Slices are
// copied, so n (shared with the feed) is not modified.
func applyLocal(n Notification, local func(id int) (localText, bool)) Notification {
	firstTitle := ""
	if len(n.TMDBTitles) > 0 {
		firstTitle = n.TMDBTitles[0]
		titles := append([]string(nil), n.TMDBTitles...)
		for i, id := range n.TMDBIDs {
			if t, ok := local(id); ok && i < len(titles) && t.title != "" {
				titles[i] = t.title
			}
		}
		n.TMDBTitles = titles
	}
	if len(n.MediaDetails) > 0 {
		media := append([]NotificationMedia(nil), n.MediaDetails...)
		for i := range media {
			t, ok := local(media[i].ID)
			if !ok {
				continue
			}
			media[i].Title = orDefault(t.title, media[i].Title)
			media[i].Overview = orDefault(t.overview, media[i].Overview)
			if len(media[i].Genres) > 0 && len(t.genres) > 0 {
				media[i].Genres = t.genres
			}
		}
		n.MediaDetails = media
	}
	if len(n.TMDBIDs) > 0 {
		if t, ok := local(n.TMDBIDs[0]); ok {
			// The headline is the first title for single-title notifications
			if n.Title == firstTitle && t.title != "" {
				n.Title = t.title
			}
			n.Overview = orDefault(t.overview, n.Overview)
			if len(n.Genres) > 0 && len(t.genres) > 0 {
				n.Genres = t.genres
			}
		}
	}
	return n
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
