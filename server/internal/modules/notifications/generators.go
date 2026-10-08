package notifications

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"nextflix/internal/platform/tmdb"
)

var errNoTMDB = errors.New("TMDB service not available")

func posterURL(path string) string   { return tmdb.ImageURL(path, "w500") }
func backdropURL(path string) string { return tmdb.ImageURL(path, "w1280") }

// daysUntil returns whole days until a "YYYY-MM-DD" date (negative if past), ok=false if unparsable.
func daysUntil(date string, now time.Time) (int, bool) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	return int(t.Sub(now).Hours() / 24), true
}

func movieMedia(m tmdb.Movie) NotificationMedia {
	return NotificationMedia{
		ID:          m.ID,
		Title:       m.Title,
		PosterURL:   posterURL(m.PosterPath),
		BackdropURL: backdropURL(m.BackdropPath),
		Rating:      m.VoteAverage,
		Year:        tmdb.YearOf(m.ReleaseDate),
		Overview:    m.Overview,
		SourceType:  "tmdb",
		SourceID:    strconv.Itoa(m.ID),
	}
}

func tvMedia(t tmdb.TV) NotificationMedia {
	return NotificationMedia{
		ID:          t.ID,
		Title:       t.Name,
		PosterURL:   posterURL(t.PosterPath),
		BackdropURL: backdropURL(t.BackdropPath),
		Rating:      t.VoteAverage,
		Year:        tmdb.YearOf(t.FirstAirDate),
		Overview:    t.Overview,
		SourceType:  "tmdb",
		SourceID:    strconv.Itoa(t.ID),
	}
}

func genreNames(genres []tmdb.Genre) []string {
	var out []string
	for _, g := range genres {
		out = append(out, g.Name)
	}
	return out
}

// movieExtras returns the trailer key and genres of a movie (empty on failure).
func (s *Service) movieExtras(id int) (string, []string) {
	d, err := s.tm.GetMovieDetailsWithExtras(id)
	if err != nil {
		return "", nil
	}
	return d.Videos.TrailerKey(), genreNames(d.Genres)
}

func (s *Service) tvExtras(id int) (string, []string) {
	d, err := s.tm.GetTVDetails(id)
	if err != nil {
		return "", nil
	}
	return d.Videos.TrailerKey(), genreNames(d.Genres)
}

// comingSoon features the next well-rated movie releasing within 90 days.
func (s *Service) comingSoon(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	exclude := s.recentIDs()

	var all []tmdb.MovieWithVideos
	for page := 1; page <= 3; page++ {
		if movies, err := s.tm.GetUpcomingMoviesList(page); err == nil {
			all = append(all, movies...)
		}
	}
	if len(all) == 0 {
		return errors.New("no upcoming movies from TMDB")
	}

	type candidate struct {
		movie tmdb.MovieWithVideos
		days  int
	}
	now := time.Now()
	var candidates []candidate
	for _, m := range all {
		if exclude[m.ID] || m.ReleaseDate == "" {
			continue
		}
		days, ok := daysUntil(m.ReleaseDate, now)
		if ok && days > 0 && days <= 90 && m.VoteAverage >= 6.0 && m.Popularity >= 30 && !m.Adult {
			candidates = append(candidates, candidate{m, days})
		}
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no suitable coming soon movies (excluded %d recently used)", len(exclude))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].days == candidates[j].days {
			return candidates[i].movie.VoteAverage > candidates[j].movie.VoteAverage
		}
		return candidates[i].days < candidates[j].days
	})

	m, days := candidates[0].movie, candidates[0].days
	var message, priority string
	switch {
	case days == 1:
		message, priority = fmt.Sprintf("🎬 %s releases TOMORROW! Don't miss it!", m.Title), "high"
	case days <= 3:
		message, priority = fmt.Sprintf("🎬 %s releases in just %d days!", m.Title, days), "high"
	case days <= 7:
		message, priority = fmt.Sprintf("🎬 %s releases this week (%d days)!", m.Title, days), "high"
	case days <= 14:
		message, priority = fmt.Sprintf("🎬 %s releases in %d days - mark your calendar!", m.Title, days), "medium"
	case days <= 30:
		message, priority = fmt.Sprintf("🎬 %s releases in %d days (⭐ %.1f)", m.Title, days, m.VoteAverage), "medium"
	default:
		message, priority = fmt.Sprintf("🎬 %s coming in %d days - highly anticipated!", m.Title, days), "low"
	}

	media := movieMedia(m.Movie)
	return s.add(ctx, Notification{
		ID:           fmt.Sprintf("coming_%d_%d", m.ID, now.Unix()),
		Type:         TypeTMDBComingSoon,
		Title:        m.Title,
		Message:      message,
		TMDBIDs:      []int{m.ID},
		TMDBTitles:   []string{m.Title},
		Timestamp:    now.Unix(),
		Priority:     priority,
		Category:     "new",
		DaysUntil:    days,
		ReleaseDate:  m.ReleaseDate,
		PosterURL:    media.PosterURL,
		BackdropURL:  media.BackdropURL,
		TrailerKey:   m.Videos.TrailerKey(),
		Rating:       m.VoteAverage,
		Overview:     m.Overview,
		Popularity:   m.Popularity,
		MediaDetails: []NotificationMedia{media},
	})
}

// pickFirst returns the first unused item matching good, else the first unused one.
func pickFirst[T any](items []T, id func(T) int, good func(T) bool, exclude map[int]bool) (T, bool) {
	for _, it := range items {
		if !exclude[id(it)] && good(it) {
			return it, true
		}
	}
	for _, it := range items {
		if !exclude[id(it)] {
			return it, true
		}
	}
	var zero T
	return zero, false
}

// singleMovie features one popular, well-reviewed movie.
func (s *Service) singleMovie(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	exclude := s.recentIDs()
	movies, err := s.tm.GetPopularMovies(1)
	if err != nil || len(movies) == 0 {
		return errors.New("no popular movies from TMDB")
	}
	m, ok := pickFirst(movies, func(m tmdb.Movie) int { return m.ID },
		func(m tmdb.Movie) bool { return m.VoteAverage >= 7.0 && m.Popularity >= 100 && !m.Adult }, exclude)
	if !ok {
		return fmt.Errorf("no suitable TMDB movie found (excluded %d recently used)", len(exclude))
	}

	trailer, genres := s.movieExtras(m.ID)
	now := time.Now()
	return s.add(ctx, Notification{
		ID:          fmt.Sprintf("tmdb_movie_%d_%d", m.ID, now.Unix()),
		Type:        TypeTMDBNowPlaying,
		Title:       m.Title,
		Message:     fmt.Sprintf("%s is getting great reviews! ⭐ %.1f", m.Title, m.VoteAverage),
		TMDBIDs:     []int{m.ID},
		TMDBTitles:  []string{m.Title},
		Timestamp:   now.Unix(),
		Priority:    "medium",
		Category:    "trending",
		PosterURL:   posterURL(m.PosterPath),
		BackdropURL: backdropURL(m.BackdropPath),
		TrailerKey:  trailer,
		Rating:      m.VoteAverage,
		Overview:    m.Overview,
		Genres:      genres,
	})
}

// singleTV features one well-rated show that is currently on the air.
func (s *Service) singleTV(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	lists, err := s.tm.GetUpcomingTVSeries()
	if err != nil || len(lists.OnTheAir) == 0 {
		return errors.New("no suitable TMDB TV show")
	}
	t, _ := pickFirst(lists.OnTheAir, func(t tmdb.TV) int { return t.ID },
		func(t tmdb.TV) bool { return t.VoteAverage >= 7.0 && t.Popularity >= 50 }, nil)

	trailer, genres := s.tvExtras(t.ID)
	now := time.Now()
	return s.add(ctx, Notification{
		ID:          fmt.Sprintf("tmdb_tv_%d_%d", t.ID, now.Unix()),
		Type:        TypeTMDBNowAiringTV,
		Title:       t.Name,
		Message:     fmt.Sprintf("%s has new episodes! ⭐ %.1f", t.Name, t.VoteAverage),
		TMDBIDs:     []int{t.ID},
		TMDBTitles:  []string{t.Name},
		Timestamp:   now.Unix(),
		Priority:    "medium",
		Category:    "trending",
		PosterURL:   posterURL(t.PosterPath),
		BackdropURL: backdropURL(t.BackdropPath),
		TrailerKey:  trailer,
		Rating:      t.VoteAverage,
		Overview:    t.Overview,
		Genres:      genres,
	})
}

// upcomingMovies bundles the 5 best-scored movies releasing within 180 days.
func (s *Service) upcomingMovies(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	var all []tmdb.MovieWithVideos
	for page := 1; page <= 2; page++ {
		if movies, err := s.tm.GetUpcomingMoviesList(page); err == nil {
			all = append(all, movies...)
		}
	}

	type candidate struct {
		movie tmdb.MovieWithVideos
		days  int
	}
	now := time.Now()
	var candidates []candidate
	for _, m := range all {
		if m.VoteAverage < 6.0 || m.Popularity < 40 || m.Adult || m.Title == "" || m.ReleaseDate == "" {
			continue
		}
		if days, ok := daysUntil(m.ReleaseDate, now); ok && days > 0 && days <= 180 {
			candidates = append(candidates, candidate{m, days})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	score := func(m tmdb.MovieWithVideos) float64 { return m.VoteAverage + m.Popularity/100 }
	sort.Slice(candidates, func(i, j int) bool { return score(candidates[i].movie) > score(candidates[j].movie) })
	candidates = candidates[:min(5, len(candidates))]

	var ids []int
	var titles []string
	var media []NotificationMedia
	totalDays := 0
	for _, c := range candidates {
		ids = append(ids, c.movie.ID)
		titles = append(titles, c.movie.Title)
		media = append(media, movieMedia(c.movie.Movie))
		totalDays += c.days
	}
	avgDays := totalDays / len(candidates)

	message := fmt.Sprintf("%d highly anticipated movies coming soon! (avg %d days)", len(candidates), avgDays)
	if len(candidates) == 1 {
		message = fmt.Sprintf("%s releases in %d days - highly anticipated!", titles[0], candidates[0].days)
	}

	first := candidates[0].movie
	return s.add(ctx, Notification{
		ID:           fmt.Sprintf("upcoming_%d_%d", len(candidates), now.Unix()),
		Type:         TypeTMDBUpcoming,
		Title:        "Coming Soon to Theaters",
		Message:      message,
		TMDBIDs:      ids,
		TMDBTitles:   titles,
		Timestamp:    now.Unix(),
		BackdropURL:  backdropURL(first.BackdropPath),
		PosterURL:    posterURL(first.PosterPath),
		TrailerKey:   first.Videos.TrailerKey(),
		Rating:       first.VoteAverage,
		Overview:     first.Overview,
		Priority:     "medium",
		Category:     "new",
		MediaDetails: media,
		DaysUntil:    avgDays,
	})
}

// trendingMovies bundles up to 4 popular, highly rated movies.
func (s *Service) trendingMovies(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	exclude := s.recentIDs()
	movies, err := s.tm.GetPopularMovies(1)
	if err != nil {
		return fmt.Errorf("failed to fetch trending movies from TMDB: %w", err)
	}

	var picked []tmdb.Movie
	for _, m := range movies {
		if exclude[m.ID] {
			continue
		}
		if m.VoteAverage >= 7.0 && m.Popularity >= 100 && !m.Adult && m.Title != "" {
			picked = append(picked, m)
			if len(picked) == 4 {
				break
			}
		}
	}
	if len(picked) == 0 {
		return nil
	}

	var ids []int
	var titles []string
	var media []NotificationMedia
	for _, m := range picked {
		ids = append(ids, m.ID)
		titles = append(titles, m.Title)
		media = append(media, movieMedia(m))
	}
	first := picked[0]
	message := fmt.Sprintf("%d movies are trending worldwide right now!", len(picked))
	if len(picked) == 1 {
		message = fmt.Sprintf("%s is trending worldwide! (⭐ %.1f)", first.Title, first.VoteAverage)
	}

	trailer, genres := s.movieExtras(first.ID)
	now := time.Now()
	return s.add(ctx, Notification{
		ID:           fmt.Sprintf("trending_%d_%d", len(picked), now.Unix()),
		Type:         TypeTMDBTrending,
		Title:        first.Title,
		Message:      message,
		TMDBIDs:      ids,
		TMDBTitles:   titles,
		Timestamp:    now.Unix(),
		BackdropURL:  backdropURL(first.BackdropPath),
		PosterURL:    posterURL(first.PosterPath),
		TrailerKey:   trailer,
		Rating:       first.VoteAverage,
		Genres:       genres,
		Overview:     first.Overview,
		Priority:     "high",
		Category:     "trending",
		MediaDetails: media,
	})
}

// tvBundle builds a notification of up to 3 shows from a TMDB TV list.
func (s *Service) tvBundle(ctx context.Context, shows []tmdb.TV, good func(tmdb.TV) bool, typ NotificationType,
	title, priority, category string, message func(n int, first string) string) error {
	var picked []tmdb.TV
	for _, t := range shows {
		if good(t) {
			picked = append(picked, t)
			if len(picked) == 3 {
				break
			}
		}
	}
	if len(picked) == 0 {
		return nil
	}

	var ids []int
	var titles []string
	var media []NotificationMedia
	for _, t := range picked {
		ids = append(ids, t.ID)
		titles = append(titles, t.Name)
		media = append(media, tvMedia(t))
	}
	first := picked[0]
	trailer, genres := s.tvExtras(first.ID)
	now := time.Now()
	return s.add(ctx, Notification{
		ID:           fmt.Sprintf("notif_%d", now.UnixNano()),
		Type:         typ,
		Title:        title,
		Message:      message(len(picked), first.Name),
		TMDBIDs:      ids,
		TMDBTitles:   titles,
		Timestamp:    now.Unix(),
		BackdropURL:  backdropURL(first.BackdropPath),
		PosterURL:    posterURL(first.PosterPath),
		TrailerKey:   trailer,
		Rating:       first.VoteAverage,
		Genres:       genres,
		Overview:     first.Overview,
		Priority:     priority,
		Category:     category,
		MediaDetails: media,
	})
}

// upcomingTV features shows airing today.
func (s *Service) upcomingTV(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	lists, err := s.tm.GetUpcomingTVSeries()
	if err != nil {
		return nil
	}
	return s.tvBundle(ctx, lists.AiringToday,
		func(t tmdb.TV) bool {
			return t.VoteAverage >= 6.0 && t.Popularity >= 30 && t.Name != "" && t.FirstAirDate != ""
		},
		TypeTMDBUpcomingTV, "New Episodes Coming", "medium", "new",
		func(n int, first string) string {
			if n == 1 {
				return fmt.Sprintf("New episodes of %s are coming soon!", first)
			}
			return fmt.Sprintf("%d exciting TV series have new episodes coming!", n)
		})
}

// nowAiringTV features well-rated shows that are on the air.
func (s *Service) nowAiringTV(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	lists, err := s.tm.GetUpcomingTVSeries()
	if err != nil {
		return nil
	}
	return s.tvBundle(ctx, lists.OnTheAir,
		func(t tmdb.TV) bool { return t.VoteAverage >= 7.0 && t.Popularity >= 50 && t.Name != "" },
		TypeTMDBNowAiringTV, "Now Airing", "high", "trending",
		func(n int, first string) string {
			if n == 1 {
				return fmt.Sprintf("%s has new episodes airing now!", first)
			}
			return fmt.Sprintf("%d popular series are currently airing new episodes!", n)
		})
}

// nowPlaying features one movie that is in theaters.
func (s *Service) nowPlaying(ctx context.Context) error {
	if !s.tm.Enabled() {
		return errNoTMDB
	}
	exclude := s.recentIDs()
	movies, err := s.tm.GetNowPlayingMovies(1)
	if err != nil {
		return fmt.Errorf("failed to fetch now playing movies from TMDB: %w", err)
	}
	if len(movies) == 0 {
		return nil
	}
	m, ok := pickFirst(movies, func(m tmdb.MovieWithVideos) int { return m.ID },
		func(m tmdb.MovieWithVideos) bool { return m.VoteAverage >= 7.0 && m.Popularity >= 100 && !m.Adult }, exclude)
	if !ok {
		return fmt.Errorf("no suitable now playing movie found (excluded %d recently used)", len(exclude))
	}

	now := time.Now()
	return s.add(ctx, Notification{
		ID:          fmt.Sprintf("now_playing_%d_%d", m.ID, now.Unix()),
		Type:        TypeTMDBNowPlaying,
		Title:       m.Title,
		Message:     fmt.Sprintf("%s is now playing and getting great reviews! (⭐ %.1f)", m.Title, m.VoteAverage),
		TMDBIDs:     []int{m.ID},
		TMDBTitles:  []string{m.Title},
		Timestamp:   now.Unix(),
		Priority:    "high",
		Category:    "trending",
		PosterURL:   posterURL(m.PosterPath),
		BackdropURL: backdropURL(m.BackdropPath),
		TrailerKey:  m.Videos.TrailerKey(),
		Rating:      m.VoteAverage,
		Overview:    m.Overview,
	})
}
