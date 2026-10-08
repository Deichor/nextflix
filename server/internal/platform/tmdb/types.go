package tmdb

import "time"

// JSON tags mirror TMDB's field names; API responses depend on them, so keep them stable.

type Movie struct {
	ID               int     `json:"id"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"original_title"`
	Overview         string  `json:"overview"`
	ReleaseDate      string  `json:"release_date"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	GenreIDs         []int   `json:"genre_ids"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
	Popularity       float64 `json:"popularity"`
	Adult            bool    `json:"adult"`
	Video            bool    `json:"video"`
	OriginalLanguage string  `json:"original_language"`
}

type TV struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	OriginalName     string  `json:"original_name"`
	Overview         string  `json:"overview"`
	FirstAirDate     string  `json:"first_air_date"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	GenreIDs         []int   `json:"genre_ids"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
	Popularity       float64 `json:"popularity"`
	Adult            bool    `json:"adult"`
	OriginalLanguage string  `json:"original_language"`
}

// MovieResults is TMDB's plain {"results": [...]} movie list.
type MovieResults struct {
	Results []Movie `json:"results"`
}

// TVResults is TMDB's plain {"results": [...]} TV list.
type TVResults struct {
	Results []TV `json:"results"`
}

type MovieWithVideos struct {
	Movie
	Videos Videos `json:"videos"`
}

type MovieDetails struct {
	Movie
	Tagline             string      `json:"tagline"`
	Runtime             int         `json:"runtime"`
	Budget              int64       `json:"budget"`
	Revenue             int64       `json:"revenue"`
	Status              string      `json:"status"`
	Genres              []Genre     `json:"genres"`
	ProductionCompanies []Company   `json:"production_companies"`
	ProductionCountries []Country   `json:"production_countries"`
	SpokenLanguages     []Language  `json:"spoken_languages"`
	Credits             Credits     `json:"credits,omitempty"`
	IMDBID              string      `json:"imdb_id"`
	Homepage            string      `json:"homepage"`
	BelongsToCollection *Collection `json:"belongs_to_collection"`
	Certification       string      `json:"certification,omitempty"`
	Awards              []string    `json:"awards,omitempty"`
}

type MovieDetailsWithExtras struct {
	MovieDetails
	Videos Videos `json:"videos"`
}

type TVDetails struct {
	TV
	Tagline             string     `json:"tagline"`
	NumberOfEpisodes    int        `json:"number_of_episodes"`
	NumberOfSeasons     int        `json:"number_of_seasons"`
	EpisodeRunTime      []int      `json:"episode_run_time"`
	InProduction        bool       `json:"in_production"`
	LastAirDate         string     `json:"last_air_date"`
	Status              string     `json:"status"`
	Type                string     `json:"type"`
	Genres              []Genre    `json:"genres"`
	ProductionCompanies []Company  `json:"production_companies"`
	ProductionCountries []Country  `json:"production_countries"`
	SpokenLanguages     []Language `json:"spoken_languages"`
	Credits             Credits    `json:"credits,omitempty"`
	Videos              Videos     `json:"videos,omitempty"`
	Homepage            string     `json:"homepage"`
	Networks            []Network  `json:"networks"`
	Seasons             []Season   `json:"seasons"`
	Certification       string     `json:"certification,omitempty"`
	Awards              []string   `json:"awards,omitempty"`
}

type Network struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	LogoPath      string `json:"logo_path"`
	OriginCountry string `json:"origin_country"`
}

type Collection struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}

type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Company struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Country struct {
	ISO31661 string `json:"iso_3166_1"`
	Name     string `json:"name"`
}

type Language struct {
	ISO6391 string `json:"iso_639_1"`
	Name    string `json:"name"`
}

type Credits struct {
	Cast []Cast `json:"cast"`
	Crew []Crew `json:"crew"`
}

type Cast struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	Order       int    `json:"order"`
	ProfilePath string `json:"profile_path"`
}

type Crew struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Job         string `json:"job"`
	Department  string `json:"department"`
	ProfilePath string `json:"profile_path"`
}

type Videos struct {
	Results []Video `json:"results"`
}

type Video struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Site        string `json:"site"`
	Size        int    `json:"size"`
	Type        string `json:"type"`
	Official    bool   `json:"official"`
	PublishedAt string `json:"published_at"`
}

// TrailerKey returns the YouTube key of the first trailer or teaser, or "".
func (v Videos) TrailerKey() string {
	for _, video := range v.Results {
		if video.Site == "YouTube" && (video.Type == "Trailer" || video.Type == "Teaser") && video.Key != "" {
			return video.Key
		}
	}
	return ""
}

type Season struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Overview     string    `json:"overview"`
	PosterPath   string    `json:"poster_path"`
	SeasonNumber int       `json:"season_number"`
	EpisodeCount int       `json:"episode_count"`
	AirDate      string    `json:"air_date"`
	Episodes     []Episode `json:"episodes,omitempty"`
}

type Episode struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Overview       string  `json:"overview"`
	EpisodeNumber  int     `json:"episode_number"`
	SeasonNumber   int     `json:"season_number"`
	StillPath      string  `json:"still_path"`
	AirDate        string  `json:"air_date"`
	Runtime        int     `json:"runtime"`
	VoteAverage    float64 `json:"vote_average"`
	VoteCount      int     `json:"vote_count"`
	Crew           []Crew  `json:"crew"`
	GuestStars     []Cast  `json:"guest_stars"`
	ShowID         int     `json:"show_id"`
	ProductionCode string  `json:"production_code"`
	EpisodeType    string  `json:"episode_type"`
}

type Image struct {
	AspectRatio float64 `json:"aspect_ratio"`
	FilePath    string  `json:"file_path"`
	Height      int     `json:"height"`
	ISO6391     string  `json:"iso_639_1"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
	Width       int     `json:"width"`
}

type Images struct {
	ID        int     `json:"id"`
	Backdrops []Image `json:"backdrops"`
	Logos     []Image `json:"logos"`
	Posters   []Image `json:"posters"`
}

// SearchResult is a movie or TV show normalized to one shape (TV name/first_air_date map to title/release_date).
type SearchResult struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	Popularity    float64 `json:"popularity"`
	MediaType     string  `json:"media_type"`
	Adult         bool    `json:"adult"`
	GenreIDs      []int   `json:"genre_ids"`
}

type MultiSearchResponse struct {
	Page         int            `json:"page"`
	Results      []SearchResult `json:"results"`
	TotalPages   int            `json:"total_pages"`
	TotalResults int            `json:"total_results"`
}

type rawSearchResult struct {
	ID            int     `json:"id"`
	Title         string  `json:"title,omitempty"`
	Name          string  `json:"name,omitempty"`
	OriginalTitle string  `json:"original_title,omitempty"`
	OriginalName  string  `json:"original_name,omitempty"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date,omitempty"`
	FirstAirDate  string  `json:"first_air_date,omitempty"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	Popularity    float64 `json:"popularity"`
	MediaType     string  `json:"media_type"`
	Adult         bool    `json:"adult"`
	GenreIDs      []int   `json:"genre_ids"`
}

type rawMultiSearchResponse struct {
	Page         int               `json:"page"`
	Results      []rawSearchResult `json:"results"`
	TotalPages   int               `json:"total_pages"`
	TotalResults int               `json:"total_results"`
}

// RelatedMovies is a paged similar/recommended movie list.
type RelatedMovies struct {
	Page         int     `json:"page"`
	Results      []Movie `json:"results"`
	TotalPages   int     `json:"total_pages"`
	TotalResults int     `json:"total_results"`
}

// RelatedTV is a paged similar/recommended TV list.
type RelatedTV struct {
	Page         int  `json:"page"`
	Results      []TV `json:"results"`
	TotalPages   int  `json:"total_pages"`
	TotalResults int  `json:"total_results"`
}

// UpcomingMovies bundles the movie lists behind /api/upcoming-movies.
type UpcomingMovies struct {
	TrendingDaily  []Movie   `json:"trending_daily"`
	TrendingWeekly []Movie   `json:"trending_weekly"`
	NowPlaying     []Movie   `json:"now_playing"`
	Upcoming       []Movie   `json:"upcoming"`
	CachedAt       time.Time `json:"cached_at"`
}

// UpcomingTVSeries bundles the TV lists behind /api/upcoming-tv-series.
type UpcomingTVSeries struct {
	AiringToday    []TV      `json:"airing_today"`
	OnTheAir       []TV      `json:"on_the_air"`
	TrendingDaily  []TV      `json:"trending_daily"`
	TrendingWeekly []TV      `json:"trending_weekly"`
	CachedAt       time.Time `json:"cached_at"`
}

type releaseDatesResponse struct {
	ID      int `json:"id"`
	Results []struct {
		ISO31661     string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
			Type          int    `json:"type"`
			ReleaseDate   string `json:"release_date"`
		} `json:"release_dates"`
	} `json:"results"`
}

type contentRatingsResponse struct {
	ID      int `json:"id"`
	Results []struct {
		ISO31661 string `json:"iso_3166_1"`
		Rating   string `json:"rating"`
	} `json:"results"`
}
