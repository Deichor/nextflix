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
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nextflix/internal/platform/torbox"
)

// Auto-pick for the "Watch" button: search Jackett, rank releases for direct browser
// playback, then confirm the best ones through TorrServer before answering.

const (
	tsResolveSearchTime   = 30 * time.Second
	tsResolveValidateTime = 20 * time.Second
	tsResolveLinkTime     = 20 * time.Second // link-only results go through Jackett/FlareSolverr
	tsResolveGrace        = 3 * time.Second  // extra wait for a promising better-ranked candidate once one works
	tsResolveUpgradeGrace = 3 * time.Second  // ... that is a higher resolution and can still become an upgrade
	tsResolveHigherGrace  = 15 * time.Second // ... that is a higher resolution without an upgrade path
	tsResolveMaxCheck     = 4
	tsGB                  = int64(1) << 30
	tsMB                  = int64(1) << 20
)

type tsResolveResult struct {
	Magnet       string    `json:"magnet"`
	Hash         string    `json:"hash"`
	Index        int       `json:"index"`
	FileName     string    `json:"file_name"`
	TorrentTitle string    `json:"torrent_title"`
	Quality      string    `json:"quality"`
	Seeders      int       `json:"seeders"`
	Size         int64     `json:"size"` // bytes of the chosen file
	HDR          bool      `json:"hdr,omitempty"`
	Health       *tsHealth `json:"health,omitempty"`   // swarm health measured when it was picked
	Duration     float64   `json:"duration,omitempty"` // seconds, when the file was probed
	// The probed duration fits the expected runtime. False when the probe failed or the
	// runtime is unknown; such a source is only picked when no verified one exists.
	DurationVerified bool `json:"duration_verified"`
	Cached           bool `json:"cached,omitempty"`
	// Format of the file: from the release name, corrected by the probe once it read the header
	Format *tsTags `json:"format,omitempty"`
	// Debrid: the file plays from TorBox's cache (Source "torbox"), not the swarm (debrid.go)
	Source string `json:"source,omitempty"`
	Debrid bool   `json:"debrid,omitempty"`
	// Why the file breaks the client's caps when nothing fitting was found (it plays
	// re-encoded or above the cap); "" for a fitting source
	CapsFallback string `json:"caps_fallback,omitempty"`
	// A cinema recording (TS, TELESYNC, CAM ...), only picked with ?cinema=1
	Cinema bool `json:"cinema,omitempty"`

	durationChecked bool // a runtime was known, so DurationVerified means something
	nameQ           int  // resolution the release name claims (Quality follows the probed file)
	// A snapshot of a running check before its health verdict or its duration check
	partial bool
	// The candidate score, corrected by rankAdj when the probe changed the format (see
	// applyProbedFormat); unfit names why the client cannot show the file right ("" = fine)
	rank    float64
	rankAdj float64
	unfit   string
	avoided bool
	// The probed video breaks the client's caps (see applyProbedCaps); unfit says why.
	// reencode: play has to re-encode its video for the client
	overCaps, reencode bool
	// The file needs more than the home line carries (see linerate.go)
	overLine bool
	// Not probed yet, and the name says play re-encodes its video for the client (x265 for a
	// browser): it only wins when nothing else delivers
	nameUnfit bool
}

// fits: nothing (probe or name) says the client cannot use the file as it is.
func (r *tsResolveResult) fits() bool { return r.unfit == "" && !r.nameUnfit }

// demoted: the probed file is smaller than its release name claimed.
func (r *tsResolveResult) demoted() bool {
	q := tsQualityHeight(r.Quality)
	return q > 0 && r.nameQ > q
}

// durationOK: the duration is verified, or there was no runtime to check it against.
func (r *tsResolveResult) durationOK() bool { return !r.durationChecked || r.DurationVerified }

// good: keeps up (or is unmeasured) and has a verified duration.
func (r *tsResolveResult) good() bool { return r.keepsUp() && r.durationOK() }

type tsResolveQuery struct {
	mediaType string
	title     string
	year      int
	firstAir  int // TV: TMDB's first-air year (0 = unknown); a name dated otherwise is another show
	season    int
	episode   int
	avoid     []string // torrents that failed to play or had the wrong length, ranked last
	hevc      bool     // the client decodes HEVC (Main and Main10), so 4K HEVC plays without a transcode
	hdr       bool     // the client display is HDR (only set together with hevc)
	// Premium audio the client decodes itself (?acodecs= eac3, ac3, truehd, dts) and whether it
	// passes Atmos through (?atmos=1); see tsAudioScore
	acodecs map[string]bool
	atmos   bool
	dv      bool // the client shows Dolby Vision, profile 5 included (?dv=1, only together with hevc)
	runtime int  // expected runtime in minutes (0 = unknown): bitrate a file needs, duration check
	tmdbID  int  // TMDB id; the server looks the runtime up itself (the client runtime is a fallback)
	maxQ    int  // highest resolution to pick (0 = no cap); the player lowers it after stalls
	// Audio languages: the title's original one (TMDB, ISO 639-1, "" = unknown yet) and the profile's
	// (X-Lang). A release whose audio has neither is demoted (see lang.go)
	origLang string
	lang     string
	// Other names of the title (original, transliterated, alternative): accepted like title;
	// extraQueries of them are searched together with title
	names        []string
	extraQueries []string
	// IMDb id of tmdbID ("" = unknown): movies are also searched by it
	imdbID string
	// Movie directors (TMDB, normalized names; nil = unknown), see tsDisclaimed
	directors [][]string
	// TMDB files the title as a documentary: "Documentary" in a name is no bonus material
	documentary bool
	// TV: episodes per season (TMDB, [0] = season 1; nil = unknown) and the absolute number
	// of season/episode (0 = unknown), see episodes.go
	seasonEps []int
	absEp     int
	// Upgrade search: only resolutions above this one (0 = a normal resolve)
	upgradeFrom int
	// Fallback ranking (nothing fitting was found): names above maxQ are kept, not rejected
	overCap bool
	// The viewer asked for a cinema recording of a film still in cinemas (?cinema=1, cinema.go)
	cinema bool
	// What the home line downloads at least, bytes/s (linerate.go; 0 = unknown)
	line float64
	// Warm-up bookkeeping of the title (nil in tests): the torrents this resolve adds and
	// the context of its background work
	warm *tsWarm
}

// warmCtx bounds background work of this resolve: it ends when the title is released.
func (q tsResolveQuery) warmCtx() context.Context {
	if q.warm != nil {
		return q.warm.ctx
	}
	return context.Background()
}

type tsCandidate struct {
	raw     JackettResult
	magnet  string
	hash    string
	quality int
	hdr     bool
	score   float64
	kind    int      // tv: 1 single episode, 2 season pack, 3 complete series or a pack spanning seasons
	ep      tsEpInfo // tv: the episode info of the name
	parts   tsScoreParts
	tags    tsTags   // format read from the name
	rest    []string // the name's normalized tokens after the title
	// A Dolby Vision tag at the end of a cut-off name: it may hide an HDR10 base layer
	maybeCutDV bool
	cinema     bool // a cinema recording (only ranked with ?cinema=1)
}

const (
	// Jackett seeder counts are often stale, so the measured swarm health decides; only a
	// 2160p with almost no seeders ranks below 1080p
	tsUHDMinSeeders = 3
	tsLastResort    = -150 // cinema leaks rank below every home release
)

var (
	tsBtihPattern   = regexp.MustCompile(`(?i)xt=urn:btih:([a-z0-9]+)`)
	tsYearPattern   = regexp.MustCompile(`^(19|20)\d{2}$`)
	tsEpPattern     = regexp.MustCompile(`^s(\d{1,2})e(\d{1,4})(?:e(\d{1,4}))?$`)
	tsXEpPattern    = regexp.MustCompile(`^(\d{1,2})x(\d{1,3})$`)
	tsSeasonPattern = regexp.MustCompile(`^s(\d{1,2})$`)
	tsH264Pattern   = regexp.MustCompile(`[xh][ ._-]?264|\bavc\b`) // "H 264", "x-264": indexers turn dots into spaces or dashes
	tsHEVCPattern   = regexp.MustCompile(`[xh][ ._-]?265|hevc`)
	tsAV1Pattern    = regexp.MustCompile(`\bav1\b`)
	tsHDRPattern    = regexp.MustCompile(`\bhdr(10)?\b|\bdv\b|dolby.?vision`)
	tsHDR10Pattern  = regexp.MustCompile(`\bhdr(10)?\b|hdr10plus|\bhlg\b`) // HDR10, HDR10+, HLG: a base layer browsers show correctly
	tsDVPattern     = regexp.MustCompile(`\bdv\b|\bdovi\b|dolby.?vision`)
	// Bonus material and compilations, matched on the normalized name (see tsExtrasName)
	tsExtrasPattern = regexp.MustCompile(`\b(greatest moments|making of|inside the episodes?|behind the scenes|recaps?|samples?|featurettes?|bloopers|deleted scenes|trailers?|teasers?|extras?|bonus|interviews?|documentary|prologue|promo)\b`)
	// Of those, the ones that are no episode title ("S01E01 Recap"; "S01E05 Prologue" may be one)
	tsBonusOnlyPattern = regexp.MustCompile(`\b(greatest moments|making of|inside the episodes?|behind the scenes|recaps?|samples?|featurettes?|bloopers|deleted scenes|trailers?|teasers?)\b`)
	// The same in Cyrillic names (tsNormalize drops other scripts): trailer, teaser, making-of,
	// bonus, interview, sample, promo, prologue
	tsExtrasCyrillic = regexp.MustCompile(`трейлер|тизер|фильм о фильме|о съ[её]мках|бонус|интервью|с[еэ]мпл|промо|пролог`)

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
		"predvd": true, "pdvd": true, "hqcam": true, "hdscr": true, "line": true,
	}
	// Cinema leaks and hardcoded subs: only used when no home release exists at all
	tsLastResortTokens = map[string]bool{
		"dcprip": true, "lte": true, "hc": true, "hcrip": true, "korsub": true, "hardsub": true, "hardcoded": true, "r5": true,
	}
	tsStreamServiceTokens = map[string]bool{
		"amzn": true, "nf": true, "dsnp": true, "atvp": true, "hmax": true, "pcok": true, "pmtp": true, "itunes": true, "hulu": true,
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
	s = tsAccentReplacer.Replace(strings.ToLower(tsFold(s)))
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
// tsJoinWords end a longer title: "Quatermass and | The Pit" is not "The Pit"
var tsJoinWords = map[string]bool{"and": true, "of": true, "the": true, "a": true, "an": true, "in": true, "on": true,
	"at": true, "to": true, "for": true, "from": true, "with": true, "vs": true, "versus": true, "ve": true, "ile": true,
	"de": true, "da": true, "du": true, "la": true, "le": true, "el": true, "der": true, "die": true, "und": true}

func tsMatchTitle(release, title []string) ([]string, bool) {
	if len(title) == 0 || len(release) < len(title) {
		return nil, false
	}
	for start := 0; start <= len(release)-len(title) && start <= 6; start++ {
		// Tokens may lead the title (site tags, a foreign title), but not as the start of a longer title
		if start > 0 && tsJoinWords[release[start-1]] {
			continue
		}
		match := true
		for i, t := range title {
			if release[start+i] != t {
				match = false
				break
			}
		}
		if match {
			rest := release[start+len(title):]
			// "Çukur / Cukur (Season: 4 ...": the original and its transliteration
			if tsHasPrefix(rest, title) {
				rest = rest[len(title):]
			}
			return rest, true
		}
	}
	return nil, false
}

// tsInTitle reports whether the normalized phrase is part of q's title or one of its names.
func tsInTitle(phrase string, q tsResolveQuery) bool {
	for _, name := range append([]string{q.title}, q.names...) {
		if strings.Contains(" "+strings.Join(tsNormalize(name), " ")+" ", " "+phrase+" ") {
			return true
		}
	}
	return false
}

// tsExtrasName: the name (raw, and its normalized tokens) carries a bonus-material word
// ("Making of", "Featurette", "Trailer", "Трейлер", ...) that the title itself does not.
func tsExtrasName(name string, tokens []string, q tsResolveQuery) bool {
	if m := tsExtrasCyrillic.FindString(strings.ToLower(name)); m != "" {
		inTitle := false
		for _, n := range append([]string{q.title}, q.names...) {
			inTitle = inTitle || strings.Contains(strings.ToLower(n), m)
		}
		if !inTitle {
			return true
		}
	}
	for _, m := range tsExtrasPattern.FindAllString(strings.Join(tokens, " "), -1) {
		if !tsInTitle(m, q) && !(m == "documentary" && q.documentary) {
			return true
		}
	}
	return false
}

// tsDisclaimerFiller are words of "NOT the ... film" that name nobody
var tsDisclaimerFiller = map[string]bool{"the": true, "a": true, "an": true, "same": true, "original": true,
	"real": true, "actual": true, "official": true, "other": true, "famous": true, "new": true, "big": true}

// tsDisclaimed: the name says it is not some film ("... (NOT The Chris Nolan FILM)"), so it is
// a namesake's release. It is only kept when the words between "not" and "film" / "movie" name
// someone and q's directors are known and not among them (then q is that namesake).
func tsDisclaimed(tokens []string, q tsResolveQuery) bool {
	for i, t := range tokens {
		if t != "not" {
			continue
		}
		for j := i + 1; j < len(tokens) && j <= i+7; j++ {
			if tokens[j] != "film" && tokens[j] != "movie" {
				continue
			}
			if tsInTitle(strings.Join(tokens[i:j+1], " "), q) {
				break // "This Is Not a Film"
			}
			named := false
			for _, w := range tokens[i+1 : j] {
				named = named || !tsDisclaimerFiller[w]
			}
			if !named || len(q.directors) == 0 {
				return true
			}
			for _, d := range q.directors {
				if len(d) > 0 && slices.Contains(tokens[i+1:j], d[len(d)-1]) {
					return true
				}
			}
			return false
		}
	}
	return false
}

// tsSourceParts are the halves of split source tags ("HD-TS", "Tele-Sync", "DVD-Scr").
var tsSourceParts = map[string]bool{"hd": true, "hq": true, "tele": true, "sync": true, "dvd": true, "bd": true, "rip": true, "pre": true}

var tsResolutionPattern = regexp.MustCompile(`^\d{3,4}[pi]$`)

// tsEpisodeTitleSpan is the [from, to) range of the words between a single-episode tag and
// the first release tag after it ("S02E05 The Line 1080p" -> "The Line"): the episode's own
// title. Empty without both tags, or when the words are only a source tag ("S01E01 HD TS 720p").
func tsEpisodeTitleSpan(tokens []string) (int, int) {
	for i, t := range tokens {
		if !tsEpPattern.MatchString(t) && !tsXEpPattern.MatchString(t) {
			continue
		}
		for j := i + 1; j < len(tokens); j++ {
			if !tsReleaseTokens[tokens[j]] && !tsResolutionPattern.MatchString(tokens[j]) {
				continue
			}
			for _, w := range tokens[i+1 : j] {
				if !tsBadSourceTokens[w] && !tsSourceParts[w] {
					return i + 1, j
				}
			}
			return 0, 0
		}
		return 0, 0
	}
	return 0, 0
}

// tsScore ranks one Jackett result; ok=false means it is rejected.
// Order of importance: source tier (cinema leaks last), resolution, codec/HDR fit for the client,
// then within a tier audio / HDR / bitrate for what the client can use, and seeders (see
// rankformat.go). c.parts holds the breakdown.
func tsScore(r JackettResult, q tsResolveQuery, titleTokens []string) (tsCandidate, bool) {
	c := tsCandidate{raw: r}
	if r.Seeders <= 0 {
		return c, false
	}
	rest, ok := tsMatchTitle(tsNormalize(r.Title), titleTokens)
	if !ok {
		return c, false
	}
	// Cinema recordings only when the viewer asked for one (?cinema=1, see cinema.go)
	// An episode's own title ("Show S02E05 The Line 1080p") is not a source or bonus tag
	tv := q.mediaType == "tv"
	from, to := 0, 0
	if tv {
		from, to = tsEpisodeTitleSpan(rest)
	}
	recording := false
	for i, t := range rest {
		if i >= from && i < to {
			continue
		}
		// "HD-TS", "Tele-Sync", "DVD-Scr" are split by tsNormalize
		if tsBadSourceTokens[t] || (i+1 < len(rest) && len(t) > 1 && len(rest[i+1]) > 1 && tsBadSourceTokens[t+rest[i+1]]) {
			if !q.cinema {
				return c, false
			}
			recording = true
		}
	}
	// Specials, compilations and bonus material ("Greatest Moments", "The Making of an Epic")
	// are not the title, and neither is a namesake ("... (NOT The Chris Nolan FILM)"). The
	// whole name counts: an IMDb id match is scored with the name's own title.
	all := tsNormalize(r.Title)
	extras := all
	if tv {
		if f, t := tsEpisodeTitleSpan(all); t > f && !tsBonusOnlyPattern.MatchString(strings.Join(all[f:t], " ")) {
			extras = slices.Concat(all[:f], all[t:])
		}
	}
	if tsExtrasName(r.Title, extras, q) || tsDisclaimed(all, q) {
		return c, false
	}

	var p tsScoreParts
	var minSize, maxSize int64 // 0 = no limit
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
				p.Match += 10
			}
		case tsReleaseTokens[next]:
			if q.year > 0 {
				p.Match -= 5
			}
		default:
			return c, false // title continues, so it is a different film
		}
		for _, t := range rest {
			if tsEpPattern.MatchString(t) || tsSeasonPattern.MatchString(t) {
				return c, false
			}
		}
		minSize, maxSize = 7*tsGB/10, 20*tsGB
	} else {
		// Optional year / country right after the show name ("Doctor Who 2005", "The Office US")
		year := 0
		for len(rest) > 0 && (tsYearPattern.MatchString(rest[0]) || tsCountryTokens[rest[0]]) {
			if year == 0 && tsYearPattern.MatchString(rest[0]) {
				year, _ = strconv.Atoi(rest[0])
			}
			rest = rest[1:]
		}
		if year > 0 {
			n := len(rest)
			rest = tsDropDate(rest) // "Show 2024 10 15": a date, not episodes 10-15
			// A show of the same name from another year ("Doctor Who 2023" for the 2005 one)
			if len(rest) == n && q.firstAir > 0 && (year < q.firstAir-1 || year > q.firstAir+1) {
				return c, false
			}
		}
		if len(rest) == 0 {
			return c, false
		}
		c.ep = tsParseEpisode(rest)
		c.kind = tsEpisodeKind(c.ep, q.epTarget())
		switch c.kind {
		case 1:
			p.Match += 30
			minSize, maxSize = 80*tsMB, 8*tsGB
		case 2:
			maxSize = 100 * tsGB
		case 3:
			p.Match -= 10
			maxSize = 300 * tsGB
		default:
			return c, false
		}
	}

	tokens := map[string]bool{}
	for _, t := range rest {
		tokens[t] = true
	}
	c.quality = tsResolution(tokens, q.hevc)
	if (q.maxQ > 0 && c.quality > q.maxQ && !q.overCap) || (q.upgradeFrom > 0 && c.quality <= q.upgradeFrom) {
		return c, false
	}
	uhd := q.hevc && c.quality == 2160
	if uhd {
		// 4K web / bluray encodes are much larger; tiny "4K" files are fakes or upscales
		switch c.kind {
		case 0:
			minSize, maxSize = 3*tsGB/2, 40*tsGB
		case 1:
			maxSize = 16 * tsGB
		case 2:
			maxSize = 200 * tsGB
		}
	}
	if r.Size > 0 && ((minSize > 0 && r.Size < minSize) || (maxSize > 0 && r.Size > maxSize)) {
		return c, false
	}

	switch c.quality {
	case 2160:
		switch {
		case !q.hevc:
			p.Res = 20 // would be transcoded down to 1080p
		case r.Seeders >= tsUHDMinSeeders:
			p.Res = 95
		default:
			p.Res = 35 // a thin swarm cannot feed a 4K stream, a well-seeded 1080p wins
		}
	case 1080:
		p.Res = 50
	case 720:
		p.Res = 30
	case 480:
		p.Res = 5
	default:
		p.Res = 12
		if tsCapped(q) {
			p.Res = tsUnknownResCapped // may be a 4K HEVC file: below every labeled fitting one until probed
		}
	}
	p.Source = tsSourceScore(rest)
	if q.cinema && (recording || tsSourceOf(rest) == "cinema") {
		c.cinema = true
		p.Source = tsCinemaScore(rest, q)
	}

	lower := strings.ToLower(r.Title)
	c.tags = tsParseTags(r.Title)
	c.maybeCutDV = tsMaybeTruncatedDV(r.Title)
	c.hdr = c.tags.HDR10 || c.tags.HDR10Plus || c.tags.DV
	p.Audio, p.HDR, p.Penalty = tsFormatParts(c.tags, q, c.maybeCutDV)
	if q.hevc {
		// Both codecs are copied as is, so neither gets an edge
		switch {
		case tsH264Pattern.MatchString(lower), tsHEVCPattern.MatchString(lower):
			p.Codec += 10
		case tsAV1Pattern.MatchString(lower):
			p.Codec -= 30 // re-encoded
		case strings.Contains(lower, "yts") || strings.Contains(lower, "yify"):
			p.Codec += 10
		}
		if strings.Contains(lower, "remux") {
			p.Penalty -= 30 // bitrate too high to stream from a swarm
		}
	} else {
		switch {
		case tsH264Pattern.MatchString(lower):
			p.Codec += 20
		case tsHEVCPattern.MatchString(lower):
			p.Codec -= 15
		case strings.Contains(lower, "yts") || strings.Contains(lower, "yify"):
			p.Codec += 15 // YTS releases are small x264 encodes
		}
		if strings.Contains(lower, "10bit") || strings.Contains(lower, "10-bit") {
			p.Codec -= 5
		}
		if strings.Contains(lower, "remux") {
			p.Penalty -= 10
		}
	}
	p.Lang = tsLangScore(r.Title, rest, q.origLang)
	if c.cinema && tsNamesLang(rest, q.lang) {
		p.Lang = tsLangMultiPart // a recording dubbed in the profile's language is fine
	}
	p.Subs = tsSubNameScore(rest, q.lang, q.origLang)
	c.rest = rest
	p.Rate = tsRateScore(r.Size, c.quality, q.runtime, q.mediaType, c.kind)
	p.Line = tsLinePart(tsCandidateNeed(c, q), q.line)
	p.Seeds = tsSeedScore(r.Seeders)

	c.parts = p
	c.score = p.total()
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

// tsResolution reads the resolution tag. An explicit one (2160p, 4K, 1080p, 720p, 480p) wins
// over UHD-ish words ("Criterion 1080p UHD BluRay" is a 1080p). With HEVC support 2160p wins
// when a title names several; otherwise 1080p does (kept from the H.264-only ranking).
func tsResolution(tokens map[string]bool, hevc bool) int {
	uhd := tokens["2160p"] || tokens["4k"]
	switch {
	case hevc && uhd:
		return 2160
	case tokens["1080p"] || tokens["1080i"]:
		return 1080
	case tokens["720p"]:
		return 720
	case uhd:
		return 2160
	case tokens["480p"] || tokens["576p"]:
		return 480
	case tokens["uhd"]:
		return 2160
	case tokens["dvdrip"] || tokens["xvid"]:
		return 480
	}
	return 0
}

// tsVideoQuality classes a video by its real size (0 when unknown). Widescreen files are less
// tall and 4:3 ones less wide, so either dimension counts.
func tsVideoQuality(width, height int) int {
	switch {
	case width <= 0 && height <= 0:
		return 0
	case height >= 1600 || width >= 3200:
		return 2160
	case height >= 900 || width >= 1800:
		return 1080
	case height >= 600 || width >= 1200:
		return 720
	}
	return 480
}

// probedQuality is the resolution of a file's probed video (0 when it was not probed yet).
func (s *Service) probedQuality(hash string, index int) int {
	v, ok := s.probeCache.Load(s.probeKey(hash, index))
	if !ok {
		return 0
	}
	if st := v.(*tsProbe).video(); st != nil {
		return tsVideoQuality(st.Width, st.Height)
	}
	return 0
}

// applyProbedQuality sets r.Quality from the probed file when its header was read; the
// release name may claim another resolution. True when it changed.
func (s *Service) applyProbedQuality(r *tsResolveResult) bool {
	q := s.probedQuality(r.Hash, r.Index)
	if q == 0 || tsQualityLabel(q) == r.Quality {
		return false
	}
	if r.nameQ == 0 {
		r.nameQ = tsQualityHeight(r.Quality)
	}
	r.Quality = tsQualityLabel(q)
	return true
}

// tsSourceScore ranks where a release comes from: REMUX > BluRay > WEB-DL > WEBRip > HDTV >
// HDRip/unknown, and cinema leaks (DCPRip, LTE, R5, hardcoded subs) far below all of them.
// A remux's bitrate is penalised separately.
func tsSourceScore(rest []string) float64 {
	switch tsSourceOf(rest) {
	case "cinema":
		return tsLastResort
	case "remux":
		return 18
	case "bluray":
		return 15
	case "webdl":
		return 12
	case "webrip":
		return 6
	case "hdtv":
		return 3
	}
	return 0
}

// tsResolveKey names a resolve job and its cached source. The version prefix drops picks
// of older rankings; the client capability and the quality cap are part of it because the
// best source differs. The runtime and TMDB id are not: they only refine the checks.
// v8: picks judged by the best 3 s burst, or labeled by name instead of the probed file, are
// resolved again. v9: format-aware ranking (audio, HDR, bitrate) and the audio / DV caps.
func tsResolveKey(q tsResolveQuery) string {
	return tsResolveBaseKey(q) + tsResolveCaps(q)
}

// tsResolveBaseKey names the title / episode without the client capability; release
// matches every key that starts with it.
func tsResolveBaseKey(q tsResolveQuery) string {
	return fmt.Sprintf("v10|%s|%s|%d|%d|%d", q.mediaType, strings.Join(tsNormalize(q.title), " "), q.year, q.season, q.episode)
}

// tsResolveCaps holds the capabilities that change the ranking: hevc, hdr, dv, native audio
// (atmos plus the premium codecs it decodes) and the quality cap. Codecs that only matter
// for play (aac, opus, ...) and premium codecs without atmos=1 rank the same, so they are
// not part of it.
func tsResolveCaps(q tsResolveQuery) string {
	caps := ""
	if q.hevc {
		caps = "|hevc"
		if q.hdr {
			caps += "|hdr"
		}
		if q.dv {
			caps += "|dv"
		}
	}
	if q.nativeAudio() {
		caps += "|atmos"
		for _, c := range []string{"ac3", "truehd", "dts"} {
			if q.acodecs[c] {
				caps += "+" + c
			}
		}
	}
	if q.maxQ > 0 {
		caps += fmt.Sprintf("|max%d", q.maxQ)
	}
	if q.cinema {
		caps += "|cinema" // a normal resolve never reuses a cinema recording
	}
	return caps
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

// errDuplicateTorrent: another candidate of the same resolve already checks this torrent.
var errDuplicateTorrent = errors.New("same torrent as a better-ranked candidate")

// errAvoidedTorrent: a listing without an info hash turned out to be a torrent the player avoids.
var errAvoidedTorrent = errors.New("avoided torrent")

// checkCandidate loads a candidate in TorrServer, picks the file to play, warms its swarm up
// for at most window while measuring whether it keeps up with the file's bitrate, and
// checks the file's duration. claim (optional) reserves the torrent for this check; a
// torrent another check claimed is skipped. A failed check drops its torrent again.
// progress (optional) gets a snapshot with the health so far after each poll once the
// duration is checked, so resolve can answer before the measurement ends.
func (s *Service) checkCandidate(ctx context.Context, c tsCandidate, q tsResolveQuery, jackettBase string, window time.Duration,
	claim func(hash string) bool, progress func(*tsResolveResult)) (res *tsResolveResult, err error) {
	outer := ctx
	magnet := c.magnet
	if magnet == "" {
		m, err := tsResolveLink(ctx, c.raw.Link, jackettBase, c.raw.Title)
		if err != nil {
			return nil, fmt.Errorf("resolve link: %w", err)
		}
		magnet = m
	}
	added := tsHashFromMagnet(magnet)
	if c.hash == "" && added != "" {
		s.listingHashes.Store(tsListingKey(c.raw), added) // later rankings know it (tsKnownHashes)
	}
	if tsAvoided(tsCandidate{hash: added}, q) {
		return nil, errAvoidedTorrent
	}
	if added != "" && claim != nil && !claim(added) {
		return nil, errDuplicateTorrent
	}
	if !s.waitQuiet(ctx, added, "candidate check") {
		return nil, ctx.Err()
	}
	s.trackTorrent(q.warm, added)
	defer func() {
		if err != nil && added != "" {
			s.dropFrom(q.warm, added)
		}
	}()

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

	file, ok := tsPickFile(stat.FileStats, q, c)
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
	if hash != added {
		added = hash
		s.trackTorrent(q.warm, hash)
	}
	if tsAvoided(tsCandidate{hash: hash}, q) {
		return nil, errAvoidedTorrent
	}
	res = &tsResolveResult{
		Magnet:          magnet,
		Hash:            hash,
		Index:           file.ID,
		FileName:        filepath.Base(file.Path),
		TorrentTitle:    c.raw.Title,
		Quality:         tsQualityLabel(c.quality),
		Seeders:         c.raw.Seeders,
		Size:            file.Length,
		HDR:             c.hdr,
		durationChecked: q.runtime > 0,
		nameQ:           c.quality,
		rank:            c.score,
		Cinema:          c.cinema,
		nameUnfit:       tsNameReencode(c, q),
	}
	nameTags := c.tags
	res.Format = &nameTags
	// Snapshots for progress: the result without health (partial until the duration is
	// checked) and the latest health so far (partial until its verdict)
	var base atomic.Pointer[tsResolveResult]
	var live atomic.Pointer[tsHealth]
	report := func() {
		b, h := base.Load(), live.Load()
		if progress == nil || b == nil {
			return
		}
		r := *b
		r.Health = h
		r.partial = r.partial || h == nil || h.partial
		progress(&r)
	}
	early := *res
	early.partial = true
	base.Store(&early)
	report()
	// Health and duration are checked in parallel; a wrong duration stops the health read.
	// The probe goes first unless the swarm is strong (see awaitHeader).
	mctx := tsMeasureCtx(outer)
	hctx, hcancel := context.WithCancel(mctx)
	defer hcancel()
	healthCh := make(chan *tsHealth, 1)
	warm := make(chan bool, 1) // true once the swarm shows peers or data, false when the warm-up ends without
	headerRead := make(chan struct{})
	var headerOnce sync.Once
	go func() {
		// A panic still answers (no health) so the check does not wait forever
		defer tsCatch("health check of "+c.raw.Title, func(error) {
			select {
			case healthCh <- nil:
			default:
			}
		})
		need := tsNeedRate(file.Length, q.runtime, q.mediaType)
		s.awaitHeader(hctx, hash, need, headerRead)
		measured := time.Now()
		// Measure mid-file so cached start pieces (earlier probes) do not inflate the rate
		hooks := tsWarmHooks{
			alive: func() { warm <- true },
			live:  func(h *tsHealth) { live.Store(h); report() },
		}
		health := s.measureWarm(hctx, hash, file.ID, tsMidOffset(file.Length, 0), need,
			window, hooks)
		select {
		case warm <- false:
		default:
		}
		if hctx.Err() == nil {
			series := ""
			if health != nil {
				series = " [" + health.series + "]"
			}
			log.Printf("resolve %q: health of %q (%s) in %s: %s%s", q.title, c.raw.Title, tsQualityLabel(c.quality),
				time.Since(measured).Round(time.Millisecond), health, series)
		}
		healthCh <- health
	}()
	duration, verified, err := s.checkDuration(outer, c, q, hash, file.ID, warm,
		func() { headerOnce.Do(func() { close(headerRead) }) })
	if err != nil {
		return nil, err
	}
	res.Duration, res.DurationVerified = math.Round(duration), verified
	// The probe read the header: the real resolution beats the release name
	if s.applyProbedQuality(res) {
		log.Printf("resolve %q: %q is %s, not %s as its name says", q.title, c.raw.Title, res.Quality, tsQualityLabel(c.quality))
	}
	// Audio with neither the original language nor the profile's (a dub-only release) is unfit too
	s.applyProbedLanguage(res, q)
	formatMoved := s.applyProbedFormat(res, c, q)
	// After the format, which replaces res.Format
	subsMoved := s.applyProbedSubs(res, c, q, stat.FileStats, file)
	// A file above the client's cap or with video it cannot decode is unfit for it
	s.applyProbedCaps(res, q)
	if p := s.cachedProbe(res.Hash, res.Index); p != nil && p.video() != nil {
		res.nameUnfit = false // the probe knows (unfit says so when it is)
	}
	if formatMoved || subsMoved || res.unfit != "" {
		log.Printf("resolve %q: %q probed as {%s}: rank %.1f -> %.1f%s", q.title, c.raw.Title, res.Format, c.score, res.rank,
			tsUnfitNote(res.unfit))
	}
	snap := *res
	base.Store(&snap)
	report()
	health := <-healthCh
	if outer.Err() != nil {
		return nil, outer.Err() // cancelled: the measurement is incomplete
	}
	if health == nil && mctx.Err() != nil {
		health = live.Load() // the pick stopped its measurement: the health so far
	}
	res.Health = health
	s.noteChecked(q, res)
	return res, nil
}

type tsMeasureKey struct{}

// tsWithMeasure gives a check a measurement context of its own (mctx, a child of ctx), so
// the pick can stop its swarm measurement while its duration check (the probe the player
// needs next) goes on.
func tsWithMeasure(ctx, mctx context.Context) context.Context {
	return context.WithValue(ctx, tsMeasureKey{}, mctx)
}

// tsMeasureCtx is the context a check measures the swarm on (ctx itself when it has none).
func tsMeasureCtx(ctx context.Context) context.Context {
	if m, ok := ctx.Value(tsMeasureKey{}).(context.Context); ok {
		return m
	}
	return ctx
}

func tsPickFile(files []tsFileStat, q tsResolveQuery, c tsCandidate) (tsFileStat, bool) {
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
		return tsPickMovieFile(videos, q)
	}

	// The episode by its season and number, else its absolute number (see episodes.go)
	if i := tsPickEpisodeFile(videos, q, c); i >= 0 {
		return videos[i], true
	}
	// A single-episode release whose file names carry no episode tag
	if c.kind == 1 {
		return videos[0], true
	}
	return tsFileStat{}, false
}

// tsPickMovieFile picks the film among videos (largest first). A collection ("The Matrix
// 1999 2003 Trilogy") holds several full-length films: the file (or its folder) named with
// the title and year wins, and without one the torrent is rejected. Videos under half the
// largest (extras, trailers) do not make a collection.
func tsPickMovieFile(videos []tsFileStat, q tsResolveQuery) (tsFileStat, bool) {
	var full []tsFileStat
	dirs := map[string]int{}
	for _, f := range videos {
		if f.Length*2 >= videos[0].Length {
			full = append(full, f)
			dirs[path.Dir(strings.ReplaceAll(f.Path, "\\", "/"))]++
		}
	}
	if len(full) < 2 {
		return videos[0], true
	}
	titles := tsTitleTokens(q)
	for _, f := range full {
		p := strings.ReplaceAll(f.Path, "\\", "/")
		if tsMovieFileNamed(strings.TrimSuffix(path.Base(p), path.Ext(p)), titles, q.year) {
			return f, true
		}
	}
	// A folder of its own names the film ("The.Matrix.1999.1080p/xyz.mkv"); the pack's
	// folder that holds them all does not
	for _, f := range full {
		dir := path.Dir(strings.ReplaceAll(f.Path, "\\", "/"))
		if dirs[dir] == 1 && dir != "." && tsMovieFileNamed(path.Base(dir), titles, q.year) {
			return f, true
		}
	}
	return tsFileStat{}, false
}

// tsMovieFileNamed: name is the title, followed by its year (+-1, when known), a release
// tag or nothing ("The Matrix Reloaded 2003" is not "The Matrix").
func tsMovieFileNamed(name string, titles [][]string, year int) bool {
	tokens := tsNormalize(name)
	for _, title := range titles {
		rest, ok := tsMatchTitle(tokens, title)
		if !ok {
			continue
		}
		if len(rest) == 0 || tsReleaseTokens[rest[0]] || tsResolutionPattern.MatchString(rest[0]) {
			return true
		}
		if tsYearPattern.MatchString(rest[0]) {
			if y, _ := strconv.Atoi(rest[0]); year == 0 || (y >= year-1 && y <= year+1) {
				return true
			}
		}
	}
	return false
}

// searchTitle runs the search of a resolve that started at started: the title's queries at
// once, the other names' (from names, a running lookup) as soon as they are known, the
// season list for absolute episode numbers, and for few results the alternative titles and
// bare title. q comes back with the names and the absolute episode; named reports whether
// the names lookup answered in time.
func (s *Service) searchTitle(ctx context.Context, q tsResolveQuery, started time.Time, names *tsNamesLater) (*tsSearchResult,
	tsResolveQuery, bool, error) {
	// The TMDB / IMDb lookups run while the title is searched; the queries of the other
	// names join the search when they arrive. The search deadlines count from started.
	seasonEps := s.seasonEpisodesLater(q, started)
	queries, _ := tsSearchQueries(q)
	sr, err := s.searchQueries(ctx, q, queries, started, names)
	if err != nil {
		return nil, q, false, err
	}
	if sr.base != "" {
		s.jackettBase.Store("base", sr.base)
	}
	// Absolute episode numbers ("Cukur 94", "Cukur.BL.90", "Episodes 01-107") need the season list
	if q.mediaType == "tv" {
		q.seasonEps = seasonEps()
		q.absEp = tsAbsoluteEpisode(q.seasonEps, q.season, q.episode)
	}
	// Ranking matches the other names too: wait for them unless the title alone found plenty
	n, named := names.get()
	if !named && len(tsRankAll(sr.raw, q)) < tsFewCandidates {
		select {
		case <-names.done:
			n, named = names.n, true
		case <-ctx.Done():
		}
	}
	if named {
		q.names, q.imdbID, q.origLang = n.names, n.imdbID, n.lang
		if n.eager {
			q.extraQueries = n.queries
		}
		log.Printf("resolve %q: other names lookup took %s", q.title, names.took.Round(time.Millisecond))
	} else {
		log.Printf("resolve %q: other names lookup still running after %s, ranking by the title", q.title,
			time.Since(started).Round(time.Millisecond))
	}
	// An English original (or one whose names are unknown) with few results: its alternative
	// titles and, for TV, the bare title (other episode notations, packs) may find more.
	// Non-English originals searched those with their other names already.
	if !(named && n.eager) {
		if k := len(tsRankAll(sr.raw, q)); k < tsFewCandidates {
			alt := q
			if named {
				alt.extraQueries = n.queries
			}
			more := append(tsExtraSearchQueries(alt), tsBroadQueries(alt, nil, 0)...)
			if len(more) > 0 {
				log.Printf("resolve %q: %d usable results, also searching %q", q.title, k, more)
				if res, err := s.searchQueries(ctx, alt, more, time.Time{}, nil); err == nil {
					sr.raw = append(sr.raw, res.raw...)
					sr.late = tsMergeLate(sr.late, res.late)
				}
			}
		}
	}
	if q.mediaType == "tv" {
		tsApplyAbsolute(&q, sr.raw)
		if q.absEp > 0 && q.season > 1 {
			log.Printf("resolve %q: S%02dE%02d is episode %d (tmdb seasons %v)", q.title, q.season, q.episode, q.absEp, q.seasonEps)
		}
	}
	return sr, q, named, nil
}

// addLate appends a batch of a slower indexer to raw, without the releases a length check
// already rejected (like the first batch, see withoutRejectedHome).
func (s *Service) addLate(q tsResolveQuery, raw, batch []JackettResult) []JackettResult {
	return append(raw, s.knownAvoided(q, s.withoutRejectedHome(q, batch))...)
}

// tsListingKey names an indexer listing without an info hash (a Jackett download link).
func tsListingKey(r JackettResult) string { return fmt.Sprintf("%s|%d", r.Title, r.Size) }

// knownAvoided gives listings without an info hash the one an earlier check found behind
// their link when q avoids it, so they rank last (an avoided torrent listed once more under
// another name, "Detour (1945) [BluRay] (1080p) [YTS AM]", took a check slot in every
// resolve after the player avoided it). Others keep their link (its trackers). raw is
// changed in place and returned.
func (s *Service) knownAvoided(q tsResolveQuery, raw []JackettResult) []JackettResult {
	if len(q.avoid) == 0 {
		return raw
	}
	for i, r := range raw {
		if r.InfoHash != "" || tsHashFromMagnet(r.MagnetUri) != "" {
			continue
		}
		if h, ok := s.listingHashes.Load(tsListingKey(r)); ok && tsAvoided(tsCandidate{hash: h.(string)}, q) {
			raw[i].InfoHash = h.(string)
		}
	}
	return raw
}

// findSource searches Jackett, ranks the releases and returns the best one TorrServer can open.
// The seed holds the other candidates (checks of higher resolutions may still be running)
// and the indexers still searching; the caller passes it to afterPick or cancels it.
func (s *Service) findSource(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
	started := time.Now()
	if line, ok := s.lineRate(); ok {
		q.line = line
	}
	runtime := s.lookupRuntime(q)
	names := s.titleNamesLater(q)
	sr, q, named, err := s.searchTitle(ctx, q, started, names)
	if err != nil {
		log.Printf("resolve %q: search failed: %v", q.title, err)
		return nil, nil, &tsResolveError{status: http.StatusBadGateway, msg: "Torrent search failed. Check the Jackett settings."}
	}
	var from string
	var rtTook time.Duration
	q.runtime, from, rtTook = runtime()
	log.Printf("resolve %q: expected runtime %dm (%s, lookup %s)", q.title, q.runtime, from, rtTook.Round(time.Millisecond))

	raw := s.knownAvoided(q, s.withoutRejectedHome(q, sr.raw)) // another video under the film's name (homedur.go)
	cands := tsRankCandidates(raw, q)
	// Nothing usable from the fast indexers: wait for the slow ones
	for len(cands) == 0 && sr.late != nil {
		select {
		case batch, ok := <-sr.late:
			if !ok {
				sr.late = nil
				break
			}
			raw = s.addLate(q, raw, batch)
			tsApplyAbsolute(&q, raw)
			cands = tsRankCandidates(raw, q)
		case <-ctx.Done():
			return nil, nil, errNoSource
		}
	}
	// Nothing within the client's caps by name: the ones above them are better than nothing
	rq := q
	if len(cands) == 0 && q.upgradeFrom == 0 && q.maxQ > 0 {
		if over := s.fallbackCandidates(q, raw, nil, nil); len(over) > 0 {
			log.Printf("resolve %q%s: no release within the caps by name, checking %d that are not", q.title, tsResolveCaps(q), len(over))
			cands, rq.overCap = over, true
		}
	}
	// Releases TorBox has cached play from it (debrid_resolve.go)
	cands, debridItems := s.debridCandidates(ctx, rq, raw, cands)
	// An upgrade the line cannot carry is not worth a check (over-line ones rank after the
	// rest; a TorBox-cached one TorBox delivered enough for has no line part)
	if q.upgradeFrom > 0 && len(cands) > 0 {
		if cands = slices.DeleteFunc(cands, func(c tsCandidate) bool { return c.parts.Line < 0 }); len(cands) == 0 {
			log.Printf("resolve %q: every release above %dp needs more than the line does (~%.2f MB/s)", q.title, q.upgradeFrom,
				q.line/float64(tsMB))
			return nil, nil, errNoSource
		}
	}
	pendingNote := ""
	if sr.late != nil {
		pendingNote = " (slower indexers still searching)"
	}
	log.Printf("resolve %q: %d results in %s (%s)%s, checking %d candidates", q.title, len(raw),
		time.Since(started).Round(time.Millisecond), sr.cut, pendingNote, len(cands))
	if len(cands) == 0 {
		return nil, nil, errNoSource
	}
	tsLogRanking(q, cands)
	tsLogOverLine(q, raw)
	s.noteRanked(q, raw) // releases a subtitle scan may try later (subalt.go)

	checkStart := time.Now()
	pick, warmCtx, warmCancel := s.runChecks(ctx, q, cands, debridItems, sr.base)
	for j, err := range pick.errs {
		if err != nil && !pick.kept[j] {
			log.Printf("resolve %q: candidate %q rejected: %v", q.title, cands[j].raw.Title, err)
		}
	}
	seedQ := q // q stays as the checks read it
	if n, ok := names.get(); ok && !named {
		seedQ.names, seedQ.imdbID, seedQ.origLang = n.names, n.imdbID, n.lang // late results match the other names too
	}
	seed := &tsUpgradeSeed{q: seedQ, cands: cands, pick: pick, ctx: warmCtx, cancel: warmCancel, raw: raw, late: sr.late, base: sr.base}
	res := pick.res
	// Home releases of the wrong length: the film is in cinemas after all (homedur.go)
	if s.noteHomeDurations(q, cands, pick) && (res == nil || !res.durationOK()) {
		seed.cancel()
		return nil, nil, errNoSource
	}
	// Nothing that fits the client's caps (or nothing at all): more candidates are checked
	// before an unfit one is played
	if q.upgradeFrom == 0 && (res == nil || res.overCaps) {
		res, seed = s.capsFallback(ctx, q, res, seed, debridItems)
		pick = seed.pick
	}
	if res == nil {
		seed.cancel()
		return nil, nil, errNoSource
	}
	if res.overCaps {
		res.CapsFallback = res.unfit
		log.Printf("resolve %q%s: nothing that fits the client was found, falling back to %q (%s: %s)", q.title, tsResolveCaps(q),
			res.TorrentTitle, res.Quality, res.unfit)
	}
	if pick.live {
		running := 0
		for _, k := range pick.kept {
			if k {
				running++
			}
		}
		note := ""
		if res.partial {
			note = ", before its verdict"
		}
		log.Printf("resolve %q: nothing keeps up after %s, answering with the best so far (%q, %s%s); %d checks keep measuring",
			q.title, time.Since(checkStart).Round(time.Millisecond), res.TorrentTitle, res.Health, note, running-1)
	} else if !res.keepsUp() {
		log.Printf("resolve %q: no candidate keeps up, using the closest one", q.title)
	}
	if !res.durationOK() {
		log.Printf("resolve %q: no candidate of verified duration, using one of unknown length", q.title)
	}
	if res.nameUnfit {
		log.Printf("resolve %q: nothing that fits the client delivers, using %q (its name says the video is re-encoded)", q.title,
			res.TorrentTitle)
	}
	if res.overLine && !res.good() {
		log.Printf("resolve %q: nothing that fits the line plays, %q %s", q.title, res.TorrentTitle,
			tsLineNote(tsNeedOf(res, q), s.lineFor(q)))
	}
	log.Printf("resolve %q: candidate #%d (%s) validated in %s", q.title, pick.best+1, res.Quality, time.Since(checkStart).Round(time.Millisecond))
	return res, seed, nil
}

// runChecks checks cands (best first) in parallel like a resolve does (see tsPickBestKeep)
// and returns the pick with the context its kept checks run on and its cancel.
func (s *Service) runChecks(ctx context.Context, q tsResolveQuery, cands []tsCandidate, items map[string]*torbox.CachedItem,
	base string) (*tsPick, context.Context, context.CancelFunc) {
	topQ := 0
	for _, c := range cands {
		topQ = max(topQ, c.quality)
	}
	// Higher resolutions may keep warming after the pick (see upgrade.go) and alternatives
	// stay as spares, so the checks run on a context that outlives this resolve. Once it has
	// answered that work is background and yields to a starting stream (quiet.go); an upgrade
	// search is background from the start.
	upgradable := q.hevc && q.maxQ == 0
	phase := &tsPhase{}
	phase.background.Store(q.upgradeFrom > 0 || tsInBackground(ctx))
	warmCtx, warmCancel := context.WithTimeout(tsWithPhase(q.warmCtx(), phase), tsUpgradeWarmTime)
	// Wait a little for a still-running candidate of a higher resolution than the best success
	graceFor := func(best int) time.Duration {
		for j := 0; j < best; j++ {
			if cands[j].quality > cands[best].quality {
				if upgradable {
					return tsResolveUpgradeGrace
				}
				return tsResolveHigherGrace
			}
		}
		return tsResolveGrace
	}
	// Still-running checks that go on: higher resolutions (upgrade) and, for a normal
	// resolve, ones of the picked resolution (spares for a sidegrade). The pick's resolution
	// is the probed one.
	keep := func(i int, pick *tsResolveResult) bool {
		// A starving (or unmeasured) pick: every running check goes on, any of them may become
		// the faster source a sidegrade offers
		if q.upgradeFrom == 0 && pick != nil && !pick.good() {
			return true
		}
		floor := q.upgradeFrom
		if floor == 0 && pick != nil {
			floor = tsQualityHeight(pick.Quality)
		}
		if upgradable && floor > 0 && cands[i].quality > floor {
			// An upgrade the line cannot carry would only stall later
			need := tsCandidateNeed(cands[i], q)
			return !tsOverLine(need, s.lineFor(q)) || (items[strings.ToLower(cands[i].hash)] != nil && s.torBoxCarries(need))
		}
		return q.upgradeFrom == 0 && pick != nil && cands[i].quality == floor
	}
	// Results and snapshots learn whether the line carries their file (the line may have shown
	// more since the ranking)
	mark := func(r *tsResolveResult) *tsResolveResult {
		if r != nil {
			r.overLine = s.overLine(r, q)
		}
		return r
	}
	claim := tsClaims(cands)
	live := newLive(len(cands))
	gate := s.newDebridGate(cands, items)
	pick := tsPickBestKeep(ctx, warmCtx, len(cands), graceFor, keep, func(ctx context.Context, i int) (*tsResolveResult, error) {
		window := tsWarmWindow
		if cands[i].quality == topQ {
			window = tsWarmWindowTop
		}
		res, err := gate.check(ctx, i, q, func(h string) bool { return claim(h, i) }, func(r *tsResolveResult) { live.set(i, mark(r)) },
			func(ctx context.Context) (*tsResolveResult, error) {
				return s.checkCandidate(ctx, cands[i], q, base, window, func(h string) bool { return claim(h, i) },
					func(r *tsResolveResult) { live.set(i, mark(r)) })
			})
		return mark(res), err
	}, live)
	phase.background.Store(true)
	return pick, warmCtx, warmCancel
}

// tsClaims hands each torrent to one candidate check. Known hashes belong to their
// candidate up front; link-only candidates learn theirs during the check.
func tsClaims(cands []tsCandidate) func(hash string, i int) bool {
	var mu sync.Mutex
	owner := map[string]int{}
	for i, c := range cands {
		if _, taken := owner[c.hash]; c.hash != "" && !taken {
			owner[c.hash] = i
		}
	}
	return func(hash string, i int) bool {
		mu.Lock()
		defer mu.Unlock()
		if j, ok := owner[hash]; ok {
			return j == i
		}
		owner[hash] = i
		return true
	}
}

// tsRankCandidates scores, de-duplicates and sorts the results and keeps the top ones.
// The torrents in q.avoid (failed to play, wrong length) go last.
func tsRankCandidates(raw []JackettResult, q tsResolveQuery) []tsCandidate {
	return tsTopCandidates(tsRankAll(raw, q), q)
}

// tsTopCandidates keeps the tsResolveMaxCheck best of ranked candidates.
func tsTopCandidates(cands []tsCandidate, q tsResolveQuery) []tsCandidate {
	if len(cands) > tsResolveMaxCheck {
		// When only 4K releases made the cut, keep the best smaller one as a fallback check
		top := cands[:tsResolveMaxCheck]
		allUHD := true
		for _, c := range top {
			allUHD = allUHD && c.quality == 2160
		}
		if allUHD {
			for _, c := range cands[tsResolveMaxCheck:] {
				if c.quality != 2160 && !tsAvoided(c, q) {
					top[len(top)-1] = c
					break
				}
			}
		}
		cands = top
	}
	return cands
}

// tsRankAll scores every result, merges copies of one torrent (see tsDedupe) and sorts them
// best first, avoided torrents last. Copies of one info hash are scored with the fullest name
// any indexer gave it, or that a cut-off name continues in (tsFullerTitles), so a cut-off name does not lose its format tags.
func tsRankAll(raw []JackettResult, q tsResolveQuery) []tsCandidate {
	names := tsTitleTokens(q)
	fuller := tsFullerTitles(raw)
	score := func(r JackettResult) (tsCandidate, bool) {
		for _, titleTokens := range names {
			if cand, ok := tsScore(r, q, titleTokens); ok {
				return cand, true
			}
		}
		if r.IMDbMatch && q.mediaType == "movie" {
			if tokens := tsReleaseTitleTokens(r.Title); len(tokens) > 0 {
				return tsScore(r, q, tokens)
			}
		}
		return tsCandidate{}, false
	}
	var scored []tsCandidate
	for i, r := range raw {
		if t := fuller[i]; t != r.Title {
			alt := r
			alt.Title = t
			if cand, ok := score(alt); ok {
				scored = append(scored, cand)
				continue
			}
		}
		if cand, ok := score(r); ok {
			scored = append(scored, cand)
		}
	}
	cands := tsDedupe(scored)
	tsSortCandidates(cands, q)
	return cands
}

func tsAvoided(c tsCandidate, q tsResolveQuery) bool {
	if c.hash == "" {
		return false
	}
	for _, h := range q.avoid {
		if strings.EqualFold(h, c.hash) {
			return true
		}
	}
	return false
}

// tsDedupe keeps one entry per torrent. Indexers list the same torrent under slightly
// different names ("x265-GRP", "x265 GRP [eztv]", "www.Site.org - ..."), often without a
// hash, so entries also count as one torrent when their name stems match and their sizes
// are within 2 %, unless both carry different hashes. The entry kept is the one with a
// hash, then the best-seeded one; nothing is summed.
func tsDedupe(cands []tsCandidate) []tsCandidate {
	better := func(a, b tsCandidate) bool { // a is the more useful entry of one torrent
		if (a.hash != "") != (b.hash != "") {
			return a.hash != ""
		}
		if a.raw.Seeders != b.raw.Seeders {
			return a.raw.Seeders > b.raw.Seeders
		}
		return a.score > b.score
	}
	order := append([]tsCandidate(nil), cands...)
	sort.SliceStable(order, func(i, j int) bool { return better(order[i], order[j]) })

	var out []tsCandidate
	stems := []string{}
	for _, c := range order {
		stem := tsStem(c.raw.Title)
		dup := false
		for k, kept := range out {
			if c.hash != "" && kept.hash != "" {
				if c.hash == kept.hash {
					dup = true
					break
				}
				continue
			}
			if stem != "" && stem == stems[k] && tsSameSize(c.raw.Size, kept.raw.Size) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, c)
			stems = append(stems, stem)
		}
	}
	return out
}

func tsSameSize(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return d*50 <= max(a, b) // within 2 %
}

var (
	tsBracketPattern  = regexp.MustCompile(`\[[^\]]*\]`)
	tsSitePrefPattern = regexp.MustCompile(`(?i)^\s*www\.[a-z0-9-]+\.[a-z]{2,6}\s*-\s*`)
	tsStemNoise       = map[string]bool{"mkv": true, "mp4": true, "avi": true, "eztv": true, "eztvx": true, "io": true, "to": true,
		"re": true, "rarbg": true, "ettv": true, "tgx": true, "torrentgalaxy": true, "1337x": true}
)

// tsStem is a release name without site tags: bracketed parts, a "www.site.org - " prefix
// and trailing site / extension tokens are dropped.
func tsStem(title string) string {
	t := tsSitePrefPattern.ReplaceAllString(title, "")
	t = tsBracketPattern.ReplaceAllString(t, " ")
	tokens := tsNormalize(t)
	for len(tokens) > 0 && tsStemNoise[tokens[len(tokens)-1]] {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, " ")
}

// tsMaybeTruncatedDV: indexers cut long names ("... Atmos DV" of "... Atmos DV HDR10"), so a
// Dolby Vision tag at the very end, or in a name cut with "...", may hide an HDR10 base layer.
func tsMaybeTruncatedDV(title string) bool {
	t := strings.TrimSpace(title)
	if strings.HasSuffix(t, "...") || strings.HasSuffix(t, "…") {
		return true
	}
	tokens := tsNormalize(t)
	if len(tokens) == 0 {
		return false
	}
	last := tokens[len(tokens)-1]
	return last == "dv" || last == "dovi" || last == "dolby" || last == "vision"
}

type tsOutcome struct {
	i   int
	res *tsResolveResult
	err error
}

// Vars so tests can shorten them.
var (
	// Resolve answers at the latest this long after the checks started, with the best so far
	// (a running check's snapshot counts, even before its verdict); the other checks keep
	// measuring
	tsResolveAnswerCap = 12 * time.Second
	// ... plus up to this long while no check has a real measurement yet (or only ones far
	// below their need while others still measure)
	tsResolveCapExtra = 4 * time.Second
)

// tsCapUsable: a measured rate / need ratio worth answering with at the cap.
const tsCapUsable = 0.1

// tsPromising: a better-ranked running check delivering this share of its need so far is
// worth a grace before a good candidate answers; resolve does not wait for any other.
const tsPromising = 0.8

// Vars so tests can shorten them.
var (
	// The swarm measurement of a check starts once the probe read the header, at the latest
	// this long after the file was picked
	tsProbeHeadStart = 2500 * time.Millisecond
	tsHeadPoll       = 500 * time.Millisecond // TorrServer stat poll while the probe reads the header
)

// tsHeadStrongMin: the least TorrServer speed during the probe that counts as a strong swarm.
const tsHeadStrongMin = 2 * tsMB

// awaitHeader holds a check's swarm measurement until the probe read the header, at the latest
// tsProbeHeadStart (the probe needs the start and often the tail, which a full-speed mid-file
// read would compete with). A swarm that already delivers tsWarmFastFactor times what keeps
// up (and tsHeadStrongMin) while the probe reads has room for both: the measurement starts.
func (s *Service) awaitHeader(ctx context.Context, hash string, need int64, headerRead <-chan struct{}) {
	head := time.NewTimer(tsProbeHeadStart)
	defer head.Stop()
	poll := time.NewTicker(tsHeadPoll)
	defer poll.Stop()
	strong := max(tsWarmFastFactor*tsKeepUpFactor*float64(need), float64(tsHeadStrongMin))
	for {
		select {
		case <-headerRead:
			return
		case <-head.C:
			return
		case <-ctx.Done():
			return
		case <-poll.C:
			var st tsSwarmStat
			sctx, cancel := context.WithTimeout(ctx, tsHeadPoll)
			err := s.swarmStat(sctx, hash, &st)
			cancel()
			if err == nil && st.DownloadSpeed >= strong {
				log.Printf("warm: %s delivers %.1f MB/s while its header is read: measuring now", tsShortHash(hash),
					st.DownloadSpeed/float64(tsMB))
				return
			}
		}
	}
}

// tsLive holds the latest snapshot of each running check (see checkCandidate's progress).
type tsLive struct {
	mu      sync.Mutex
	res     []*tsResolveResult
	changed chan struct{}
}

func newLive(n int) *tsLive {
	return &tsLive{res: make([]*tsResolveResult, n), changed: make(chan struct{}, 1)}
}

func (l *tsLive) set(i int, r *tsResolveResult) {
	l.mu.Lock()
	l.res[i] = r
	l.mu.Unlock()
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

func (l *tsLive) get(i int) *tsResolveResult {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.res[i]
}

// tsPick is the outcome of tsPickBestKeep.
type tsPick struct {
	best    int // -1 when nothing succeeded
	res     *tsResolveResult
	live    bool               // res is a snapshot of the still-running check best (kept)
	errs    []error            // failure of each candidate
	results []*tsResolveResult // finished checks by candidate
	kept    []bool             // checks left running in the background
	later   <-chan tsOutcome   // their outcomes (and those of the cancelled ones)
	cancels []context.CancelFunc
	// stop only the swarm measurement of a check (see tsWithMeasure)
	measures []context.CancelFunc
}

// stop cancels the check of candidate i (no-op when unknown).
func (p *tsPick) stop(i int) {
	if i >= 0 && i < len(p.cancels) && p.cancels[i] != nil {
		p.cancels[i]()
	}
}

// stopMeasure ends the swarm measurement of candidate i; its duration check (the probe the
// player needs next) goes on and caches the header.
func (p *tsPick) stopMeasure(i int) {
	if i >= 0 && i < len(p.measures) && p.measures[i] != nil {
		p.measures[i]()
		return
	}
	p.stop(i)
}

// tsCatch, deferred directly in a goroutine, turns a panic into an error for onPanic (logged
// with its stack) instead of killing the server.
func tsCatch(what string, onPanic func(error)) {
	r := recover()
	if r == nil {
		return
	}
	log.Printf("resolve: %s panicked: %v\n%s", what, r, debug.Stack())
	onPanic(fmt.Errorf("%s panicked: %v", what, r))
}

// tsPickBest runs check for candidates 0..n-1 (best first) in parallel and returns the
// best-ranked one that succeeded and keeps up (see tsChoose). It returns as soon as every
// better-ranked check has finished, or grace after the first success when a better one is
// still slow. i is -1 when nothing succeeded; errs holds the failure of each candidate.
func tsPickBest(ctx context.Context, n int, grace time.Duration, check func(context.Context, int) (*tsResolveResult, error)) (int, *tsResolveResult, []error) {
	return tsPickBestFn(ctx, n, func(int) time.Duration { return grace }, check)
}

// tsPickBestFn is tsPickBest with the grace period chosen from the best good success so far.
func tsPickBestFn(ctx context.Context, n int, graceFor func(best int) time.Duration, check func(context.Context, int) (*tsResolveResult, error)) (int, *tsResolveResult, []error) {
	p := tsPickBestKeep(ctx, nil, n, graceFor, nil, check, nil)
	return p.best, p.res, p.errs
}

// tsPickBestKeep is tsPickBestFn with the checks running on checkCtx (nil = ctx). When it
// returns, the still-running checks that keep(i, pick) selects go on in the background
// (outcomes on tsPick.later); all others are cancelled. While only starving sources or ones
// of unknown duration have answered it waits up to tsStarvingGrace for a good one. No wait
// (a grace either) goes past tsResolveAnswerCap: then a good one answers, else the most
// playable of the finished checks and the snapshots of running ones from live (optional;
// tsChoosePlayable), whose check then keeps running too. While no check has a real
// measurement (or only ones below tsCapUsable while others still measure) it waits up to
// tsResolveCapExtra longer. Without any result or snapshot the first one to arrive answers.
func tsPickBestKeep(ctx, checkCtx context.Context, n int, graceFor func(best int) time.Duration,
	keep func(i int, pick *tsResolveResult) bool, check func(context.Context, int) (*tsResolveResult, error), live *tsLive) *tsPick {
	if checkCtx == nil {
		checkCtx = ctx
	}
	outs := make(chan tsOutcome, n) // never blocks a check, also after the pick returned
	cancels := make([]context.CancelFunc, n)
	measures := make([]context.CancelFunc, n)
	for i := 0; i < n; i++ {
		cctx, cancel := context.WithCancel(checkCtx)
		mctx, mcancel := context.WithCancel(cctx)
		cancels[i], measures[i] = cancel, mcancel
		go func(i int) {
			defer cancel()
			var res *tsResolveResult
			var err error
			// Always report an outcome, a panicking check as a failed one
			defer func() {
				if res == nil && err == nil {
					err = errors.New("no result")
				}
				outs <- tsOutcome{i, res, err}
			}()
			defer tsCatch(fmt.Sprintf("candidate check %d", i), func(perr error) { res, err = nil, perr })
			res, err = check(tsWithMeasure(cctx, mctx), i)
		}(i)
	}

	p := &tsPick{best: -1, errs: make([]error, n), results: make([]*tsResolveResult, n), kept: make([]bool, n), later: outs,
		cancels: cancels, measures: measures}
	finished := make([]bool, n)
	done := func(best int) *tsPick {
		if best >= 0 {
			p.best, p.res = best, p.results[best]
			if !finished[best] {
				p.res, p.live, p.kept[best] = live.get(best), true, true
			}
		}
		for i := range finished {
			if finished[i] || i == best {
				continue
			}
			if keep != nil && keep(i, p.res) {
				p.kept[i] = true
			} else {
				cancels[i]()
			}
		}
		return p
	}
	// The finished checks plus, with snapshots, the latest snapshot of each running one that
	// has its verdict and duration
	view := func(snapshots bool) []*tsResolveResult {
		if !snapshots || live == nil {
			return p.results
		}
		merged := make([]*tsResolveResult, n)
		for i := range merged {
			if merged[i] = p.results[i]; !finished[i] {
				if r := live.get(i); r != nil && !r.partial {
					merged[i] = r
				}
			}
		}
		return merged
	}
	choose := func(snapshots bool) int {
		best, _ := tsChoose(view(snapshots), finished)
		return best
	}
	// Every check as it stands: finished results and the latest snapshot of running ones
	current := func() []*tsResolveResult {
		all := make([]*tsResolveResult, n)
		for i := range all {
			if all[i] = p.results[i]; !finished[i] {
				all[i] = live.get(i)
			}
		}
		return all
	}
	goodNow := func() int {
		merged := view(true)
		if best, _ := tsChoose(merged, finished); best >= 0 && merged[best].good() && merged[best].fits() {
			return best
		}
		return -1
	}
	// At the cap anything counts: a good one if there is one, else the most playable source
	// of the finished checks and every snapshot (see tsChoosePlayable)
	atCap := func() int {
		if best := goodNow(); best >= 0 {
			return best
		}
		if best := tsChoosePlayable(current()); best >= 0 {
			return best
		}
		return choose(true)
	}
	// Worth answering at the cap: something good or measured to deliver (≥ 0.1 of its need),
	// or no running check left that could still show a measurement
	enough := func() bool {
		if goodNow() >= 0 {
			return true
		}
		waiting := false
		for i, r := range current() {
			if r.measured() && r.Health.ratio() >= tsCapUsable && r.fits() {
				return true
			}
			waiting = waiting || (!finished[i] && !r.measured())
		}
		return !waiting
	}

	// A better-ranked running check (of at least best's resolution) that delivers
	// tsPromising of its need so far may still beat best. Without snapshots every
	// better-ranked running check counts.
	promising := func(best int) bool {
		bq := tsQualityHeight(view(true)[best].Quality)
		for j := 0; j < best; j++ {
			if finished[j] {
				continue
			}
			if live == nil {
				return true
			}
			r := live.get(j)
			if r == nil || r.Health == nil || r.Health.Dead || !r.fits() || tsQualityHeight(r.Quality) < bq {
				continue
			}
			if r.Health.ratio() >= tsPromising {
				return true
			}
		}
		return false
	}

	// The grace deadline only ever moves earlier
	var grace *time.Timer
	var graceAt time.Time
	setGrace := func(d time.Duration) {
		at := time.Now().Add(d)
		if grace != nil && !at.Before(graceAt) {
			return
		}
		if grace != nil {
			grace.Stop()
		}
		grace, graceAt = time.NewTimer(d), at
	}
	capTimer := time.NewTimer(tsResolveAnswerCap)
	var extra *time.Timer
	defer func() {
		capTimer.Stop()
		if grace != nil {
			grace.Stop()
		}
		if extra != nil {
			extra.Stop()
		}
	}()
	// A good one answers at once (now) unless a promising better-ranked check still runs;
	// then its grace starts and every snapshot looks again
	settle := func() (int, bool) {
		best := goodNow()
		if best < 0 {
			return -1, false
		}
		if !promising(best) {
			return best, true
		}
		setGrace(graceFor(best))
		return best, false
	}
	capC := capTimer.C
	var extraC <-chan time.Time
	capped := false    // past the cap without anything to answer with: the first result or snapshot answers
	extending := false // past the cap, waiting up to tsResolveCapExtra for a measured candidate

	for pending := n; pending > 0; {
		var graceC <-chan time.Time
		if grace != nil {
			graceC = grace.C
		}
		var liveC <-chan struct{}
		if live != nil {
			liveC = live.changed
		}
		select {
		case o := <-outs:
			pending--
			finished[o.i] = true
			p.results[o.i], p.errs[o.i] = o.res, o.err
			if o.res != nil {
				p.errs[o.i] = nil
			}
			best, final := tsChoose(p.results, finished)
			if final && best >= 0 {
				if !p.results[best].good() {
					best = atCap() // all done, none good: the most playable
				}
				return done(best)
			}
			if capped || (extending && enough()) {
				if best := atCap(); best >= 0 {
					return done(best)
				}
			}
			if o.res != nil {
				g, now := settle()
				switch {
				case now:
					return done(g)
				case g >= 0: // in the grace of a promising better-ranked check
				case p.results[best].good() && p.results[best].fits():
					setGrace(graceFor(best))
				default:
					// Starving, or unfit for the client: the running checks may still give a good one
					setGrace(tsStarvingGrace)
				}
			}
		case <-capC:
			// A hard wall: whatever is running or in its grace, the best so far answers now,
			// unless nothing has been measured yet (then up to tsResolveCapExtra more)
			capC = nil
			if live != nil && tsResolveCapExtra > 0 && !enough() {
				extending = true
				extra = time.NewTimer(tsResolveCapExtra)
				extraC = extra.C
				continue
			}
			if best := atCap(); best >= 0 {
				return done(best)
			}
			capped = true
		case <-extraC:
			extraC, extending = nil, false
			if best := atCap(); best >= 0 {
				return done(best)
			}
			capped = true
		case <-liveC:
			if capped || (extending && enough()) {
				if best := atCap(); best >= 0 {
					return done(best)
				}
			}
			if g, now := settle(); now {
				return done(g)
			}
		case <-graceC:
			if extending {
				grace = nil // the extension decides
				continue
			}
			if best := goodNow(); best >= 0 {
				return done(best)
			}
			return done(atCap())
		case <-ctx.Done():
			for i := range p.errs {
				if !finished[i] {
					p.errs[i] = ctx.Err()
				}
			}
			return done(choose(false))
		}
	}
	return done(-1)
}
