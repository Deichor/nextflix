package streaming

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Auto-pick for the "Watch" button: search Jackett, rank releases for direct browser
// playback, then confirm the best ones through TorrServer before answering.

const (
	tsResolveCacheTTL     = 6 * time.Hour
	tsResolveSearchTime   = 30 * time.Second
	tsResolveValidateTime = 25 * time.Second
	tsResolveLinkTime     = 15 * time.Second
	tsResolveMaxCheck     = 4
	tsGB                  = int64(1) << 30
	tsMB                  = int64(1) << 20
)

type tsResolveResult struct {
	Magnet       string `json:"magnet"`
	Hash         string `json:"hash"`
	Index        int    `json:"index"`
	FileName     string `json:"file_name"`
	TorrentTitle string `json:"torrent_title"`
	Quality      string `json:"quality"`
	Seeders      int    `json:"seeders"`
	Size         int64  `json:"size"` // bytes of the chosen file
}

type tsResolveQuery struct {
	mediaType string
	title     string
	year      int
	season    int
	episode   int
}

type tsCandidate struct {
	raw     JackettResult
	magnet  string
	hash    string
	quality int
	score   float64
	kind    int // tv: 1 single episode, 2 season pack, 3 complete series
}

type tsCacheEntry struct {
	res     tsResolveResult
	expires time.Time
}

var (
	tsBtihPattern   = regexp.MustCompile(`(?i)xt=urn:btih:([a-z0-9]+)`)
	tsYearPattern   = regexp.MustCompile(`^(19|20)\d{2}$`)
	tsEpPattern     = regexp.MustCompile(`^s(\d{1,2})e(\d{1,3})(?:e(\d{1,3}))?$`)
	tsXEpPattern    = regexp.MustCompile(`^(\d{1,2})x(\d{1,3})$`)
	tsSeasonPattern = regexp.MustCompile(`^s(\d{1,2})$`)
	tsEpOnlyPattern = regexp.MustCompile(`^e(\d{1,3})$`)
	tsH264Pattern   = regexp.MustCompile(`x\.?264|h\.?264|\bavc\b`)
	tsHEVCPattern   = regexp.MustCompile(`x\.?265|h\.?265|hevc`)
	tsHDRPattern    = regexp.MustCompile(`\bhdr(10)?\b|\bdv\b|dolby.?vision`)

	tsAccentReplacer = strings.NewReplacer(
		"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
		"è", "e", "é", "e", "ê", "e", "ë", "e",
		"ì", "i", "í", "i", "î", "i", "ï", "i", "ı", "i",
		"ò", "o", "ó", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
		"ù", "u", "ú", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n", "ş", "s", "ğ", "g",
		"'", "", "’", "", "`", "", "&", " and ",
	)
	tsNumberWords = map[string]string{
		"one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
		"six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12",
	}
	tsBadSourceTokens = map[string]bool{
		"cam": true, "camrip": true, "hdcam": true, "ts": true, "hdts": true, "telesync": true, "tsrip": true,
		"screener": true, "scr": true, "dvdscr": true, "bdscr": true, "tc": true, "hdtc": true, "telecine": true,
		"predvd": true, "pdvd": true, "hqcam": true,
	}
	tsReleaseTokens = map[string]bool{
		"2160p": true, "1080p": true, "1080i": true, "720p": true, "576p": true, "480p": true, "4k": true, "uhd": true,
		"bluray": true, "bdrip": true, "brrip": true, "web": true, "webrip": true, "webdl": true, "dl": true,
		"hdrip": true, "dvdrip": true, "hdtv": true, "x264": true, "x265": true, "h264": true, "h265": true, "hevc": true,
		"extended": true, "remastered": true, "unrated": true, "directors": true, "imax": true, "proper": true,
		"repack": true, "multi": true, "dual": true, "complete": true, "theatrical": true, "uncut": true, "hdr": true,
		"10bit": true, "remux": true, "amzn": true, "nf": true, "dsnp": true, "hmax": true, "atvp": true, "yts": true,
	}
	tsCountryTokens = map[string]bool{"us": true, "uk": true, "au": true, "nz": true, "ca": true}
)

func tsNormalize(s string) []string {
	s = tsAccentReplacer.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	tokens := strings.Fields(b.String())
	for i, t := range tokens {
		if n, ok := tsNumberWords[t]; ok {
			tokens[i] = n
		}
	}
	return tokens
}

// tsHashFromMagnet returns the lowercase hex info-hash of a magnet (base32 is converted)
func tsHashFromMagnet(magnet string) string {
	m := tsBtihPattern.FindStringSubmatch(magnet)
	if m == nil {
		return ""
	}
	h := m[1]
	if len(h) == 32 {
		if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h)); err == nil {
			return hex.EncodeToString(b)
		}
	}
	if tsHashPattern.MatchString(h) {
		return strings.ToLower(h)
	}
	return ""
}

func tsBuildMagnet(hash, name string, trackers []string) string {
	v := "magnet:?xt=urn:btih:" + hash
	if name != "" {
		v += "&dn=" + url.QueryEscape(name)
	}
	for i, tr := range trackers {
		if i >= 10 {
			break
		}
		v += "&tr=" + url.QueryEscape(tr)
	}
	return v
}

func tsQualityLabel(q int) string {
	switch q {
	case 2160:
		return "2160p"
	case 1080:
		return "1080p"
	case 720:
		return "720p"
	case 480:
		return "480p"
	}
	return ""
}

// tsMatchTitle returns the tokens that follow the searched title in a release name, or ok=false
func tsMatchTitle(release, title []string) ([]string, bool) {
	if len(title) == 0 || len(release) < len(title) {
		return nil, false
	}
	for start := 0; start <= len(release)-len(title) && start <= 6; start++ {
		match := true
		for i, t := range title {
			if release[start+i] != t {
				match = false
				break
			}
		}
		if match {
			return release[start+len(title):], true
		}
	}
	return nil, false
}

// tsScore ranks one Jackett result; ok=false means it is rejected
func tsScore(r JackettResult, q tsResolveQuery, titleTokens []string) (tsCandidate, bool) {
	c := tsCandidate{raw: r}
	if r.Seeders <= 0 {
		return c, false
	}
	rest, ok := tsMatchTitle(tsNormalize(r.Title), titleTokens)
	if !ok {
		return c, false
	}
	for _, t := range rest {
		if tsBadSourceTokens[t] {
			return c, false
		}
	}

	score := 0.0
	if q.mediaType == "movie" {
		if len(rest) == 0 {
			return c, false
		}
		next := rest[0]
		switch {
		case tsYearPattern.MatchString(next):
			y, _ := strconv.Atoi(next)
			if q.year > 0 {
				if y < q.year-1 || y > q.year+1 {
					return c, false
				}
				score += 10
			}
		case tsReleaseTokens[next]:
			if q.year > 0 {
				score -= 5
			}
		default:
			return c, false // title continues, so it is a different film
		}
		for _, t := range rest {
			if tsEpPattern.MatchString(t) || tsSeasonPattern.MatchString(t) {
				return c, false
			}
		}
		if r.Size > 0 && (r.Size < 7*tsGB/10 || r.Size > 20*tsGB) {
			return c, false
		}
	} else {
		// Optional year / country right after the show name ("Doctor Who 2005", "The Office US")
		for len(rest) > 0 && (tsYearPattern.MatchString(rest[0]) || tsCountryTokens[rest[0]]) {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return c, false
		}
		c.kind = tsEpisodeKind(rest, q.season, q.episode)
		switch c.kind {
		case 1:
			score += 30
			if r.Size > 0 && (r.Size < 80*tsMB || r.Size > 8*tsGB) {
				return c, false
			}
		case 2:
			if r.Size > 100*tsGB {
				return c, false
			}
		case 3:
			score -= 10
			if r.Size > 300*tsGB {
				return c, false
			}
		default:
			return c, false
		}
	}

	joined := " " + strings.Join(rest, " ") + " "
	switch {
	case strings.Contains(joined, " 1080p ") || strings.Contains(joined, " 1080i "):
		c.quality, score = 1080, score+50
	case strings.Contains(joined, " 720p "):
		c.quality, score = 720, score+30
	case strings.Contains(joined, " 2160p ") || strings.Contains(joined, " 4k ") || strings.Contains(joined, " uhd "):
		c.quality, score = 2160, score+20
	case strings.Contains(joined, " 480p ") || strings.Contains(joined, " 576p ") || strings.Contains(joined, " dvdrip ") || strings.Contains(joined, " xvid "):
		c.quality, score = 480, score+5
	default:
		score += 12
	}

	lower := strings.ToLower(r.Title)
	switch {
	case tsH264Pattern.MatchString(lower):
		score += 20
	case tsHEVCPattern.MatchString(lower):
		score -= 15
	case strings.Contains(lower, "yts") || strings.Contains(lower, "yify"):
		score += 15 // YTS releases are small x264 encodes
	}
	if strings.Contains(lower, "10bit") || strings.Contains(lower, "10-bit") {
		score -= 5
	}
	if tsHDRPattern.MatchString(lower) {
		score -= 8
	}
	if strings.Contains(lower, "remux") {
		score -= 10
	}

	score += 6 * math.Log2(1+float64(r.Seeders))
	if r.Seeders < 5 {
		score -= 20
	}

	c.score = score
	c.magnet = strings.TrimSpace(r.MagnetUri)
	if !strings.HasPrefix(c.magnet, "magnet:?") {
		c.magnet = ""
	}
	c.hash = tsHashFromMagnet(c.magnet)
	if c.hash == "" && tsHashPattern.MatchString(r.InfoHash) {
		c.hash = strings.ToLower(r.InfoHash)
	}
	if c.magnet == "" && c.hash != "" {
		c.magnet = tsBuildMagnet(c.hash, r.Title, nil)
	}
	if c.magnet == "" && r.Link == "" {
		return c, false
	}
	return c, true
}

// tsEpisodeKind classifies the release tokens after the show name:
// 1 = the wanted episode, 2 = a season pack containing it, 3 = complete series, 0 = no match
func tsEpisodeKind(rest []string, season, episode int) int {
	first := rest[0]
	marker := tsEpPattern.MatchString(first) || tsXEpPattern.MatchString(first) || tsSeasonPattern.MatchString(first) ||
		first == "season" || first == "seasons" || first == "complete" || first == "series"
	if !marker {
		return 0
	}

	for i, t := range rest {
		if m := tsEpPattern.FindStringSubmatch(t); m != nil {
			s, _ := strconv.Atoi(m[1])
			e1, _ := strconv.Atoi(m[2])
			e2 := e1
			if m[3] != "" {
				e2, _ = strconv.Atoi(m[3])
			} else if i+1 < len(rest) {
				// "S01E01-E03" normalizes to "s01e01 e03"
				if mm := tsEpOnlyPattern.FindStringSubmatch(rest[i+1]); mm != nil {
					e2, _ = strconv.Atoi(mm[1])
				}
			}
			if s == season && episode >= e1 && episode <= e2 {
				if e2 > e1 {
					return 2 // multi-episode pack, the file has to be picked by name
				}
				return 1
			}
			return 0
		}
		if m := tsXEpPattern.FindStringSubmatch(t); m != nil {
			s, _ := strconv.Atoi(m[1])
			e, _ := strconv.Atoi(m[2])
			if s == season && e == episode {
				return 1
			}
			return 0
		}
	}

	// Season packs: "s01", "s01 s05" (range), "season 1", "seasons 1 5"
	for i, t := range rest {
		if m := tsSeasonPattern.FindStringSubmatch(t); m != nil {
			from, _ := strconv.Atoi(m[1])
			to := from
			if i+1 < len(rest) {
				if mm := tsSeasonPattern.FindStringSubmatch(rest[i+1]); mm != nil {
					to, _ = strconv.Atoi(mm[1])
				}
			}
			if season >= from && season <= to {
				if to > from {
					return 3
				}
				return 2
			}
			return 0
		}
		if (t == "season" || t == "seasons") && i+1 < len(rest) {
			from, err := strconv.Atoi(rest[i+1])
			if err != nil {
				continue
			}
			to := from
			if i+2 < len(rest) {
				if n, err := strconv.Atoi(rest[i+2]); err == nil && n > from && n < 100 {
					to = n
				}
			}
			if season >= from && season <= to {
				if to > from {
					return 3
				}
				return 2
			}
			return 0
		}
	}
	for _, t := range rest {
		if t == "complete" {
			return 3
		}
	}
	return 0
}

func tsResolveKey(q tsResolveQuery) string {
	return fmt.Sprintf("%s|%s|%d|%d|%d", q.mediaType, strings.Join(tsNormalize(q.title), " "), q.year, q.season, q.episode)
}

// jackettSearchers returns searchers for the configured Jackett URL plus container fallbacks
func (s *Service) jackettSearchers() []*TorrentSearcher {
	var st IndexerSettings
	if p, err := loadSettings(s.db); err == nil {
		st = *p
	}
	apiKey := st.JackettAPIKey
	if apiKey == "" {
		apiKey = s.cfg.JackettAPIKey
	}

	var bases []string
	if b, ok := s.jackettBase.Load("base"); ok {
		bases = append(bases, b.(string))
	}
	bases = append(bases, st.JackettURL, s.cfg.JackettURL, "http://jackett:9117")

	seen := map[string]bool{}
	var out []*TorrentSearcher
	for _, b := range bases {
		b = strings.TrimSuffix(strings.TrimSpace(b), "/")
		if b == "" || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, NewTorrentSearcher(b, apiKey, 0))
	}
	return out
}

// search runs all queries in parallel against the first Jackett URL that answers
func (s *Service) search(ctx context.Context, q tsResolveQuery) ([]JackettResult, string, error) {
	var queries []string
	category := "2000"
	if q.mediaType == "movie" {
		if q.year > 0 {
			queries = append(queries, fmt.Sprintf("%s %d", q.title, q.year))
		}
		queries = append(queries, q.title)
	} else {
		category = "5000"
		queries = append(queries,
			fmt.Sprintf("%s S%02dE%02d", q.title, q.season, q.episode),
			fmt.Sprintf("%s S%02d", q.title, q.season),
		)
	}

	var lastErr error
	for _, searcher := range s.jackettSearchers() {
		sctx, cancel := context.WithTimeout(ctx, tsResolveSearchTime)
		var mu sync.Mutex
		var wg sync.WaitGroup
		var results []JackettResult
		failures := 0
		for _, query := range queries {
			wg.Add(1)
			go func(query string) {
				defer wg.Done()
				res, err := searcher.SearchJackettRaw(sctx, query, category)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					failures++
					lastErr = err
					return
				}
				results = append(results, res...)
			}(query)
		}
		wg.Wait()
		cancel()
		if failures < len(queries) {
			return results, searcher.JackettURL(), nil
		}
		if ctx.Err() != nil {
			break // the viewer gave up
		}
	}
	if lastErr == nil {
		lastErr = errors.New("jackett is not configured")
	}
	return nil, "", lastErr
}

// tsResolveLink turns a Jackett download link into a magnet (redirect or .torrent file)
func tsResolveLink(ctx context.Context, link, jackettBase, name string) (string, error) {
	// Jackett builds links with the host it believes it has; use the one that answered us
	if u, err := url.Parse(link); err == nil && jackettBase != "" {
		if b, err := url.Parse(jackettBase); err == nil && strings.Contains(u.Path, "/dl/") {
			u.Scheme, u.Host = b.Scheme, b.Host
			link = u.String()
		}
	}

	ctx, cancel := context.WithTimeout(ctx, tsResolveLinkTime)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for hop := 0; hop < 4; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			if strings.HasPrefix(loc, "magnet:?") {
				return loc, nil
			}
			next, err := resp.Request.URL.Parse(loc)
			if err != nil || loc == "" {
				return "", errors.New("bad redirect")
			}
			link = next.String()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download link returned %d", resp.StatusCode)
		}
		if s := strings.TrimSpace(string(body)); strings.HasPrefix(s, "magnet:?") {
			return s, nil
		}
		hash, trackers, err := tsTorrentFileInfo(body)
		if err != nil {
			return "", err
		}
		return tsBuildMagnet(hash, name, trackers), nil
	}
	return "", errors.New("too many redirects")
}

// checkCandidate loads a candidate in TorrServer and picks the file to play
func (s *Service) checkCandidate(ctx context.Context, c tsCandidate, q tsResolveQuery, jackettBase string) (*tsResolveResult, error) {
	magnet := c.magnet
	if magnet == "" {
		m, err := tsResolveLink(ctx, c.raw.Link, jackettBase, c.raw.Title)
		if err != nil {
			return nil, fmt.Errorf("resolve link: %w", err)
		}
		magnet = m
	}

	ctx, cancel := context.WithTimeout(ctx, tsResolveValidateTime)
	defer cancel()
	statURL := s.torrServerURL() + "/stream?" + url.Values{"link": {magnet}, "stat": {""}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrserver returned %d", resp.StatusCode)
	}
	var stat tsTorrentStat
	if err := json.NewDecoder(resp.Body).Decode(&stat); err != nil {
		return nil, err
	}

	file, ok := tsPickFile(stat.FileStats, q, c.kind)
	if !ok {
		return nil, errors.New("no matching video file")
	}

	hash := strings.ToLower(stat.Hash)
	if !tsHashPattern.MatchString(hash) {
		hash = tsHashFromMagnet(magnet)
	}
	if !tsHashPattern.MatchString(hash) {
		return nil, errors.New("unknown info hash")
	}
	return &tsResolveResult{
		Magnet:       magnet,
		Hash:         hash,
		Index:        file.ID,
		FileName:     filepath.Base(file.Path),
		TorrentTitle: c.raw.Title,
		Quality:      tsQualityLabel(c.quality),
		Seeders:      c.raw.Seeders,
		Size:         file.Length,
	}, nil
}

func tsPickFile(files []tsFileStat, q tsResolveQuery, kind int) (tsFileStat, bool) {
	var videos []tsFileStat
	for _, f := range files {
		if tsVideoExts[strings.ToLower(filepath.Ext(f.Path))] {
			videos = append(videos, f)
		}
	}
	// Skip sample clips whenever a real file exists
	if len(videos) > 1 {
		var main []tsFileStat
		for _, f := range videos {
			if !strings.Contains(strings.ToLower(filepath.Base(f.Path)), "sample") {
				main = append(main, f)
			}
		}
		if len(main) > 0 {
			videos = main
		}
	}
	if len(videos) == 0 {
		return tsFileStat{}, false
	}
	sort.Slice(videos, func(i, j int) bool { return videos[i].Length > videos[j].Length })

	if q.mediaType == "movie" {
		return videos[0], true
	}

	s, e := q.season, q.episode
	epPatterns := []*regexp.Regexp{
		regexp.MustCompile(fmt.Sprintf(`(?i)s0*%d[ ._-]*e0*%d(?:[^0-9]|$)`, s, e)),
		regexp.MustCompile(fmt.Sprintf(`(?i)(?:^|[^0-9])0*%dx0*%d(?:[^0-9]|$)`, s, e)),
	}
	for _, f := range videos {
		name := filepath.Base(f.Path)
		for _, p := range epPatterns {
			if p.MatchString(name) {
				return f, true
			}
		}
	}

	// Packs with folders per season: "Season 1/Episode 02.mkv", "S01/E02 - Title.mkv"
	seasonDir := regexp.MustCompile(fmt.Sprintf(`(?i)(?:^|[^a-z0-9])(?:s|season)[ ._-]*0*%d(?:[^0-9]|$)`, s))
	epOnly := regexp.MustCompile(fmt.Sprintf(`(?i)(?:^|[^a-z0-9])(?:e|ep|episode)[ ._-]*0*%d(?:[^0-9]|$)`, e))
	for _, f := range videos {
		dir := filepath.Dir(f.Path)
		if (kind == 2 || seasonDir.MatchString(dir)) && epOnly.MatchString(filepath.Base(f.Path)) {
			return f, true
		}
	}

	// A single-episode release whose file names carry no episode tag
	if kind == 1 {
		return videos[0], true
	}
	return tsFileStat{}, false
}

// resolve finds the best playable torrent for a movie or episode
func (h handler) resolve(c *gin.Context) {
	s := h.s
	q := tsResolveQuery{
		mediaType: c.Query("type"),
		title:     strings.TrimSpace(c.Query("title")),
	}
	q.year, _ = strconv.Atoi(c.Query("year"))
	q.season, _ = strconv.Atoi(c.Query("season"))
	q.episode, _ = strconv.Atoi(c.Query("episode"))
	if q.mediaType != "movie" && q.mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be movie or tv"})
		return
	}
	titleTokens := tsNormalize(q.title)
	if len(titleTokens) == 0 || len(q.title) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	if q.mediaType == "tv" && (q.season < 0 || q.episode < 1) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "season and episode are required for tv"})
		return
	}
	if q.mediaType == "movie" {
		q.season, q.episode = 0, 0
	}

	key := tsResolveKey(q)
	if v, ok := s.resolveCache.Load(key); ok {
		entry := v.(tsCacheEntry)
		if time.Now().Before(entry.expires) {
			c.JSON(http.StatusOK, entry.res)
			return
		}
		s.resolveCache.Delete(key)
	}

	ctx := c.Request.Context()
	raw, jackettBase, err := s.search(ctx, q)
	if err != nil {
		log.Printf("resolve %q: search failed: %v", q.title, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Torrent search failed. Check the Jackett settings."})
		return
	}
	s.jackettBase.Store("base", jackettBase)

	// Rank and de-duplicate
	best := map[string]tsCandidate{}
	for _, r := range raw {
		cand, ok := tsScore(r, q, titleTokens)
		if !ok {
			continue
		}
		id := cand.hash
		if id == "" {
			id = "link:" + strings.ToLower(r.Title) + fmt.Sprint(r.Size)
		}
		if prev, ok := best[id]; !ok || cand.score > prev.score {
			best[id] = cand
		}
	}
	cands := make([]tsCandidate, 0, len(best))
	for _, cand := range best {
		cands = append(cands, cand)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > tsResolveMaxCheck {
		cands = cands[:tsResolveMaxCheck]
	}
	log.Printf("resolve %q: %d results, checking %d candidates", q.title, len(raw), len(cands))

	// Check candidates in parallel, take the best-ranked one that works
	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		res *tsResolveResult
		err error
	}
	outs := make([]chan outcome, len(cands))
	for i, cand := range cands {
		outs[i] = make(chan outcome, 1)
		go func(ch chan outcome, cand tsCandidate) {
			res, err := s.checkCandidate(checkCtx, cand, q, jackettBase)
			ch <- outcome{res, err}
		}(outs[i], cand)
	}
	for i, ch := range outs {
		o := <-ch
		if o.res != nil {
			s.resolveCache.Store(key, tsCacheEntry{res: *o.res, expires: time.Now().Add(tsResolveCacheTTL)})
			c.JSON(http.StatusOK, o.res)
			return
		}
		log.Printf("resolve %q: candidate %q rejected: %v", q.title, cands[i].raw.Title, o.err)
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "No playable source found"})
}
