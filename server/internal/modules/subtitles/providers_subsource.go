package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/upstream"
)

const (
	subsourceDefaultAPI = "https://api.subsource.net/api/v1"
	subsourceTitleTTL   = 6 * time.Hour
)

// subSource talks to the SubSource API (the successor of Subscene). Each season of a show is its
// own title there, found by IMDb id; downloads are zips.
type subSource struct {
	api  string
	http *upstream.Client

	mu           sync.Mutex
	blockedUntil time.Time
	titles       map[string]ssTitle // "<imdb>|<season>" -> title id
}

type ssTitle struct {
	id string
	at time.Time
}

func newSubSource(api string, client *upstream.Client) *subSource {
	if api == "" {
		api = subsourceDefaultAPI
	}
	return &subSource{api: strings.TrimRight(api, "/"), http: client, titles: map[string]ssTitle{}}
}

// ssLangs maps ISO 639-1 to SubSource's language names.
var ssLangs = map[string]string{
	"tr": "turkish", "en": "english", "ar": "arabic", "fa": "farsi_persian", "de": "german", "fr": "french",
	"es": "spanish", "it": "italian", "pt": "portuguese", "nl": "dutch", "ru": "russian", "el": "greek",
	"pl": "polish", "sv": "swedish", "da": "danish", "no": "norwegian", "fi": "finnish", "ja": "japanese",
	"ko": "korean", "zh": "chinese_bg_code", "he": "hebrew", "hu": "hungarian", "cs": "czech", "ro": "romanian",
	"bg": "bulgarian", "hr": "croatian", "sr": "serbian", "uk": "ukrainian", "az": "azerbaijani", "id": "indonesian",
}

func ssLangCode(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for code, n := range ssLangs {
		if n == name {
			return code
		}
	}
	return normLang(name)
}

// ssID decodes an id or a year sent as a number or a string.
type ssID string

func (f *ssID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = ssID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = ssID(n.String())
	}
	return nil
}

type ssTitleResult struct {
	MovieID ssID   `json:"movieId"`
	Season  ssID   `json:"season"`
	Link    string `json:"link"`
}

type ssItem struct {
	SubtitleID      ssID     `json:"subtitleId"`
	Language        string   `json:"language"`
	ReleaseInfo     []string `json:"releaseInfo"`
	Commentary      *string  `json:"commentary"`
	HearingImpaired bool     `json:"hearingImpaired"`
	ForeignParts    bool     `json:"foreignParts"`
	UploaderID      ssID     `json:"uploaderId"`
	Contributors    []struct {
		ID          ssID   `json:"id"`
		DisplayName string `json:"displayname"`
	} `json:"contributors"`
}

func (it ssItem) commentary() string {
	if it.Commentary == nil {
		return ""
	}
	return *it.Commentary
}

func (it ssItem) uploader() string {
	for _, c := range it.Contributors {
		if c.ID == it.UploaderID {
			return c.DisplayName
		}
	}
	return ""
}

var (
	ssDigits = regexp.MustCompile(`^\d{1,12}$`)
	ssSeason = regexp.MustCompile(`-season-(\d+)$`)
)

func (s *subSource) search(ctx context.Context, q Query, st Settings, imdb string) ([]candidate, error) {
	if st.SubSourceAPIKey == "" || imdb == "" {
		return nil, nil
	}
	season := 0
	if q.isTV() {
		season = q.Season
	}
	movieID, err := s.titleID(ctx, st.SubSourceAPIKey, imdb, season)
	if err != nil || movieID == "" {
		return nil, err
	}
	var all []candidate
	var firstErr error
	for _, l := range q.Langs {
		name, ok := ssLangs[l]
		if !ok {
			continue
		}
		v := url.Values{"movieId": {movieID}, "language": {name}, "limit": {"100"}}
		if q.isTV() {
			v.Set("seasonNumber", strconv.Itoa(q.Season))
			v.Set("episodeNumber", strconv.Itoa(q.Episode))
		}
		var res struct {
			Data []ssItem `json:"data"`
		}
		if err := s.getJSON(ctx, st.SubSourceAPIKey, "/subtitles?"+v.Encode(), &res); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, s.toCandidates(res.Data, q)...)
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

// titleID finds the SubSource title of an IMDb id (the season's own title for a show).
func (s *subSource) titleID(ctx context.Context, key, imdb string, season int) (string, error) {
	ck := fmt.Sprintf("%s|%d", imdb, season)
	s.mu.Lock()
	if t, ok := s.titles[ck]; ok && time.Since(t.at) < subsourceTitleTTL {
		s.mu.Unlock()
		return t.id, nil
	}
	s.mu.Unlock()

	v := url.Values{"searchType": {"imdb"}, "imdb": {imdb}}
	if season > 0 {
		v.Set("season", strconv.Itoa(season))
	}
	var res struct {
		Data []ssTitleResult `json:"data"`
	}
	if err := s.getJSON(ctx, key, "/movies/search?"+v.Encode(), &res); err != nil {
		return "", err
	}
	id := pickSSTitle(res.Data, season)
	s.mu.Lock()
	s.titles[ck] = ssTitle{id: id, at: time.Now()}
	s.mu.Unlock()
	return id, nil
}

// pickSSTitle prefers the result that names the season (its field or "-season-N" link), else the first.
func pickSSTitle(rs []ssTitleResult, season int) string {
	first := ""
	for _, r := range rs {
		if !ssDigits.MatchString(string(r.MovieID)) {
			continue
		}
		if first == "" {
			first = string(r.MovieID)
		}
		if season <= 0 {
			break
		}
		n, _ := strconv.Atoi(string(r.Season))
		if m := ssSeason.FindStringSubmatch(r.Link); n == 0 && m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		if n == season {
			return string(r.MovieID)
		}
	}
	return first
}

func (s *subSource) toCandidates(items []ssItem, q Query) []candidate {
	var out []candidate
	for _, it := range items {
		id := string(it.SubtitleID)
		if !ssDigits.MatchString(id) {
			continue
		}
		lang := ssLangCode(it.Language)
		releases := make([]string, 0, len(it.ReleaseInfo))
		for _, r := range it.ReleaseInfo {
			if r = strings.TrimSpace(r); r != "" {
				releases = append(releases, r)
			}
		}
		pack := false
		ref := id
		if q.isTV() {
			ok, p := ssEpisodeFits(releases, it.commentary(), q)
			if !ok {
				continue
			}
			pack = p
			ref = fmt.Sprintf("%s|%d|%d", id, q.Season, q.Episode)
		}
		c := strings.ToLower(it.commentary())
		release := ""
		if len(releases) > 0 {
			release = releases[0]
		}
		out = append(out, candidate{
			Sub: Sub{
				ID:       encodeID(SourceSubSource, lang, ref),
				Source:   SourceSubSource,
				Language: lang,
				Release:  release,
				HI:       it.HearingImpaired || strings.Contains(c, "sdh"),
				Forced:   it.ForeignParts || strings.Contains(c, "forced"),
				Author:   it.uploader(),
			},
			Releases: releases,
			Pack:     pack,
		})
	}
	return out
}

// ssEpisodeFits keeps a subtitle whose names name the asked episode, or none (a season pack).
// Names of other episodes only drop it.
func ssEpisodeFits(releases []string, commentary string, q Query) (ok, pack bool) {
	named := false
	for _, r := range releases {
		s, e := episodeOf(r)
		if e == 0 {
			continue
		}
		named = true
		if e == q.Episode && (s == 0 || s == q.Season) {
			return true, false
		}
	}
	if named {
		return false, false
	}
	// The uploader's note may carry it ("S01E03")
	if m := epSxE.FindStringSubmatch(commentary); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return s == q.Season && e == q.Episode, false
	}
	return true, true
}

// fetch downloads ref "<id>[|<season>|<episode>]" and takes the subtitle out of the zip.
func (s *subSource) fetch(ctx context.Context, ref, lang string, st Settings) ([]byte, string, error) {
	if st.SubSourceAPIKey == "" {
		return nil, "", errors.New("SubSource has no API key in settings")
	}
	p := strings.Split(ref, "|")
	if !ssDigits.MatchString(p[0]) || (len(p) != 1 && len(p) != 3) {
		return nil, "", ErrBadID
	}
	resp, err := s.get(ctx, st.SubSourceAPIKey, "/subtitles/"+p[0]+"/download")
	if err != nil {
		return nil, "", err
	}
	raw := resp.Body
	if !isZip(raw) && !isRar(raw) {
		if bytes.Contains(raw[:min(len(raw), 512)], []byte("-->")) {
			return raw, "subsource.srt", nil
		}
		return nil, "", errors.New("SubSource: download is not a subtitle archive")
	}
	season, episode := 0, 0
	if len(p) == 3 {
		season, _ = strconv.Atoi(p[1])
		episode, _ = strconv.Atoi(p[2])
	}
	b, name, err := pickFromArchive(raw, season, episode, lang)
	if err != nil && episode > 0 && !isRar(raw) && !archiveNamesEpisodes(raw) {
		// A single episode's zip whose file names do not say the episode (names of other
		// episodes mean the asked one is missing: no fallback then)
		return pickFromArchive(raw, 0, 0, lang)
	}
	return b, name, err
}

func (s *subSource) getJSON(ctx context.Context, key, path string, v any) error {
	resp, err := s.get(ctx, key, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return fmt.Errorf("SubSource: %w", err)
	}
	return nil
}

// get sends one request with the key in a header (never in a URL or an error).
func (s *subSource) get(ctx context.Context, key, path string) (*upstream.Response, error) {
	s.mu.Lock()
	if time.Now().Before(s.blockedUntil) {
		s.mu.Unlock()
		return nil, fmt.Errorf("SubSource: %w", ErrRateLimited)
	}
	s.mu.Unlock()

	resp, err := s.http.Get(ctx, s.api+path, http.Header{"Accept": {"application/json"}, "X-API-Key": {key}})
	if err != nil {
		return nil, fmt.Errorf("SubSource: %w", err)
	}
	switch {
	case resp.Status == http.StatusOK:
		return resp, nil
	case resp.Status == http.StatusTooManyRequests:
		wait := retryAfter(resp.Header.Get("Retry-After"), time.Minute)
		if t, err := time.Parse(time.RFC3339, resp.Header.Get("X-RateLimit-Reset")); err == nil && time.Until(t) > 0 {
			wait = time.Until(t)
		}
		s.mu.Lock()
		s.blockedUntil = time.Now().Add(wait)
		s.mu.Unlock()
		return nil, fmt.Errorf("SubSource: %w for %s", ErrRateLimited, wait.Round(time.Second))
	case resp.Status == http.StatusNotFound && !strings.HasSuffix(path, "/download"):
		return &upstream.Response{Status: http.StatusOK, Body: []byte(`{"data":[]}`)}, nil // nothing listed
	}
	return nil, fmt.Errorf("SubSource: %w", statusError{code: resp.Status, body: truncate(resp.Body, 512)})
}
