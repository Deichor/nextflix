package subtitles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	subf2mDefaultBase = "https://subf2m.co"
	subf2mGap         = time.Second
	subf2mSlugTTL     = 24 * time.Hour
	subf2mMissTTL     = 6 * time.Hour
	subf2mMaxProbes   = 4
	subf2mMaxLangs    = 3
)

// TitleInfo names a movie or TV show for providers that search by title.
type TitleInfo struct {
	Names []string // English title first, then the original title when it differs
	Year  int      // release year, first air year for TV
	// Genres and Overview give the AI translation its context
	Genres   []string
	Overview string
}

// TitleLookup returns the TitleInfo of a movie or TV show (mediaType "movie" | "tv").
type TitleLookup func(mediaType string, tmdbID int) (TitleInfo, error)

// subf2m scrapes subf2m.co, the Subscene archive. Its search is closed to robots, so the title
// page is found from the TMDB names in Subscene's slug style ("the-mentalist-second-season",
// "inception", newer ones "<name>-<year>" / "<name>-season-2") and checked against the IMDb id
// it links. Pages are per language; files come as zips (season packs too).
type subf2m struct {
	*scraper

	mu    sync.Mutex
	slugs map[string]slugEntry // "<type>|<tmdb>|<season>" -> slug ("" = not on the site)
}

type slugEntry struct {
	slug    string
	expires time.Time
}

type subf2mTitle struct {
	IMDb  string
	Names []string
	Year  int
}

func newSubf2m(base string, pages diskCache) *subf2m {
	if base == "" {
		base = subf2mDefaultBase
	}
	return &subf2m{
		scraper: newScraper("subf2m", base, "subf2m-pages", pages, subf2mGap, "isubcdn.com"),
		slugs:   map[string]slugEntry{},
	}
}

// subf2mLangs maps ISO 639-1 codes to the site's language paths.
var subf2mLangs = map[string]string{
	"tr": "turkish", "en": "english", "de": "german", "fr": "french", "es": "spanish", "it": "italian",
	"pt": "portuguese", "nl": "dutch", "ru": "russian", "ar": "arabic", "fa": "farsi_persian", "el": "greek",
	"pl": "polish", "sv": "swedish", "da": "danish", "no": "norwegian", "fi": "finnish", "ja": "japanese",
	"ko": "korean", "zh": "chinese-bg-code", "he": "hebrew", "hu": "hungarian", "cs": "czech", "ro": "romanian",
	"bg": "bulgarian", "hr": "croatian", "sr": "serbian", "uk": "ukranian", "id": "indonesian", "vi": "vietnamese",
}

var subf2mOrdinals = []string{"", "first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth",
	"ninth", "tenth", "eleventh", "twelfth", "thirteenth", "fourteenth", "fifteenth", "sixteenth", "seventeenth",
	"eighteenth", "nineteenth", "twentieth"}

var slugFold = strings.NewReplacer("ı", "i", "ß", "ss", "ø", "o", "æ", "ae", "œ", "oe", "ł", "l", "đ", "d")

// subsceneSlug turns a title into a Subscene style path ("Grey's Anatomy" -> "greys-anatomy");
// "" for names in a non-Latin script.
func subsceneSlug(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) && !unicode.In(r, unicode.Latin) {
			return ""
		}
	}
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(slugFold.Replace(strings.ToLower(name))) {
		switch {
		case unicode.Is(unicode.Mn, r) || r == '\'' || r == '’' || r == '.':
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return b.String()
}

// subf2mSlugs lists the title paths to try, most likely first.
func subf2mSlugs(q Query, t subf2mTitle) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] && len(out) < subf2mMaxProbes {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, n := range t.Names {
		base := subsceneSlug(n)
		if len(base) < 2 {
			continue
		}
		if q.isTV() {
			if q.Season < len(subf2mOrdinals) {
				add(base + "-" + subf2mOrdinals[q.Season] + "-season")
			}
			add(fmt.Sprintf("%s-season-%d", base, q.Season))
			continue
		}
		if t.Year > 0 {
			add(fmt.Sprintf("%s-%d", base, t.Year))
		}
		add(base)
	}
	return out
}

// subf2mRow is one subtitle of a title's language page.
type subf2mRow struct {
	Slug     string
	LangPath string
	ID       string
	Releases []string
	Author   string
	Comment  string
	Quality  int
	HI       bool
	Forced   bool
}

var (
	subf2mItem     = regexp.MustCompile(`<li class=['"]item\b`)
	subf2mDownload = regexp.MustCompile(`href=['"]/subtitles/([a-z0-9-]+)/([a-z_-]+)/(\d+)['"]`)
	subf2mList     = regexp.MustCompile(`(?s)<ul class=['"]scrolllist['"]>(.*?)</ul>`)
	subf2mLi       = regexp.MustCompile(`(?s)<li>(.*?)</li>`)
	subf2mRate     = regexp.MustCompile(`<span class=['"]rate ([a-z ]+)['"]`)
	subf2mAuthor   = regexp.MustCompile(`(?s)<b>\s*By\s*<a[^>]*>(.*?)</a>`)
	subf2mComment  = regexp.MustCompile(`(?s)<div class=['"]vertical-middle['"]>.*?<p>(.*?)</p>`)
	subf2mIMDb     = regexp.MustCompile(`class=['"]imdb['"][^>]*href=['"]https?://(?:www\.)?imdb\.com/title/(tt\d+)`)
	subf2mYear     = regexp.MustCompile(`<strong>\s*Year:\s*</strong>\s*(\d{4})`)
	subf2mSlugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,150}$`)
	subf2mLangRe   = regexp.MustCompile(`^[a-z][a-z_-]{1,40}$`)
	hiName         = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:hi|sdh)(?:[^a-z0-9]|$)`)
	forcedName     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])forced(?:[^a-z0-9]|$)`)
)

// forcedOnly reads an uploader comment that calls the subtitle forced ("forced BluRay altyazı"),
// but not one that only includes the forced lines ("Forced altyazı dahildir").
func forcedOnly(comment string) bool {
	if !strings.Contains(comment, "forced") {
		return false
	}
	for _, w := range []string{"dahil", "includ", "with forced", "+ forced", "+forced", "ve forced", "and forced"} {
		if strings.Contains(comment, w) {
			return false
		}
	}
	return true
}

// parseSubf2mList reads the subtitle rows of a title page.
func parseSubf2mList(page string) []subf2mRow {
	locs := subf2mItem.FindAllStringIndex(page, -1)
	var rows []subf2mRow
	for i, loc := range locs {
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		b := page[loc[0]:end]
		d := subf2mDownload.FindStringSubmatch(b)
		if d == nil {
			continue
		}
		r := subf2mRow{Slug: d[1], LangPath: d[2], ID: d[3]}
		if l := subf2mList.FindStringSubmatch(b); l != nil {
			for _, li := range subf2mLi.FindAllStringSubmatch(l[1], -1) {
				if name := taText(li[1]); name != "" {
					r.Releases = append(r.Releases, name)
					r.HI = r.HI || hiName.MatchString(name)
				}
			}
		}
		if m := subf2mRate.FindStringSubmatch(b); m != nil {
			switch strings.TrimSpace(m[1]) {
			case "good", "positive":
				r.Quality = 2
			case "bad":
				r.Quality = 4
			}
		}
		if m := subf2mAuthor.FindStringSubmatch(b); m != nil {
			r.Author = taText(m[1])
		}
		if m := subf2mComment.FindStringSubmatch(b); m != nil {
			r.Comment = taText(m[1])
			low := strings.ToLower(r.Comment)
			r.HI = r.HI || strings.Contains(low, "hearing impaired") || strings.Contains(low, "sdh")
			r.Forced = forcedOnly(low)
		}
		for _, n := range r.Releases {
			r.Forced = r.Forced || forcedName.MatchString(n)
		}
		rows = append(rows, r)
	}
	return rows
}

// episodeNamed reports whether a release name names an episode, and whether it is the asked one
// (double episodes and "2x10" count).
func episodeNamed(name string, season, episode int) (named, match bool) {
	for _, m := range relEpisode.FindAllStringSubmatch(name, -1) {
		named = true
		s, _ := strconv.Atoi(m[1])
		a, _ := strconv.Atoi(m[2])
		b := a
		if m[3] != "" {
			if n, _ := strconv.Atoi(m[3]); n > a && n-a <= 5 {
				b = n
			}
		}
		match = match || (s == season && episode >= a && episode <= b)
	}
	for _, m := range relEpisodeX.FindAllStringSubmatch(name, -1) {
		named = true
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		match = match || (s == season && e == episode)
	}
	if !named {
		if s, e := episodeOf(name); e > 0 {
			return true, e == episode && (s == 0 || s == season)
		}
	}
	return named, match
}

// subf2mMatches checks that a title page is the asked title: the IMDb id it links, else the year
// (movies; a season page shows the season's year).
func subf2mMatches(page string, q Query, t subf2mTitle) bool {
	if m := subf2mIMDb.FindStringSubmatch(page); m != nil && t.IMDb != "" {
		return strings.EqualFold(m[1], t.IMDb)
	}
	if q.isTV() {
		return true
	}
	m := subf2mYear.FindStringSubmatch(page)
	if m == nil || t.Year == 0 {
		return true
	}
	y, _ := strconv.Atoi(m[1])
	return y >= t.Year-1 && y <= t.Year+1
}

func (f *subf2m) search(ctx context.Context, q Query, t subf2mTitle) ([]candidate, error) {
	if q.isTV() && q.Season <= 0 {
		return nil, nil
	}
	var langs []string
	for _, l := range q.Langs {
		if _, ok := subf2mLangs[l]; ok && len(langs) < subf2mMaxLangs {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		return nil, nil
	}
	slug, page, err := f.resolve(ctx, q, t, subf2mLangs[langs[0]])
	if err != nil || slug == "" {
		return nil, err
	}
	out := f.candidates(page, slug, langs[0], q)
	var firstErr error
	for _, l := range langs[1:] {
		p, err := f.page(ctx, "/subtitles/"+slug+"/"+subf2mLangs[l])
		if err != nil {
			if !errors.Is(err, errNotFound) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, f.candidates(p, slug, l, q)...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// resolve finds the title's path and returns it with its page in lang ("" when not on the site).
func (f *subf2m) resolve(ctx context.Context, q Query, t subf2mTitle, lang string) (string, string, error) {
	key := fmt.Sprintf("%s|%d|%d", q.MediaType, q.TmdbID, q.Season)
	f.mu.Lock()
	e, ok := f.slugs[key]
	f.mu.Unlock()
	if ok && time.Now().Before(e.expires) && !(e.slug == "" && freshPages(ctx)) {
		if e.slug == "" {
			return "", "", nil
		}
		page, err := f.page(ctx, "/subtitles/"+e.slug+"/"+lang)
		if !errors.Is(err, errNotFound) {
			return e.slug, page, err
		}
	}
	for _, slug := range subf2mSlugs(q, t) {
		page, err := f.page(ctx, "/subtitles/"+slug+"/"+lang)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return "", "", err // the next call tries again
		}
		if subf2mMatches(page, q, t) {
			f.remember(key, slug, subf2mSlugTTL)
			return slug, page, nil
		}
	}
	f.remember(key, "", subf2mMissTTL)
	return "", "", nil
}

func (f *subf2m) remember(key, slug string, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.slugs) > 2000 {
		f.slugs = map[string]slugEntry{}
	}
	f.slugs[key] = slugEntry{slug: slug, expires: time.Now().Add(ttl)}
}

// candidates turns a language page into results; for TV the season's page lists every episode,
// so rows naming other episodes are dropped and rows naming none count as season packs.
func (f *subf2m) candidates(page, slug, lang string, q Query) []candidate {
	var out []candidate
	for _, r := range parseSubf2mList(page) {
		if r.Slug != slug || langFromName(r.LangPath) != lang {
			continue
		}
		pack := false
		if q.isTV() {
			named, match := false, false
			for _, n := range r.Releases {
				nm, m := episodeNamed(n, q.Season, q.Episode)
				named, match = named || nm, match || m
			}
			if named && !match {
				continue
			}
			pack = !named
		}
		release := firstNonEmpty(append(append([]string{}, r.Releases...), r.Comment, "subf2m")...)
		if len([]rune(release)) > 200 {
			release = string([]rune(release)[:200])
		}
		p := 0
		if pack {
			p = 1
		}
		ref := fmt.Sprintf("%s|%s|%s|%d|%d|%d", slug, r.LangPath, r.ID, q.Season, q.Episode, p)
		out = append(out, candidate{
			Sub: Sub{
				ID:       encodeID(SourceSubf2m, lang, ref),
				Source:   SourceSubf2m,
				Language: lang,
				Release:  release,
				HI:       r.HI,
				Forced:   r.Forced,
				Author:   r.Author,
			},
			Releases: r.Releases,
			Pack:     pack,
			Quality:  r.Quality,
		})
	}
	return out
}

// fetch downloads ref "<slug>|<language path>|<id>|<season>|<episode>|<pack>".
func (f *subf2m) fetch(ctx context.Context, ref, lang string) ([]byte, string, error) {
	p := strings.Split(ref, "|")
	if len(p) != 6 || !subf2mSlugRe.MatchString(p[0]) || !subf2mLangRe.MatchString(p[1]) || !taDigits.MatchString(p[2]) {
		return nil, "", errors.New("bad subf2m id")
	}
	season, _ := strconv.Atoi(p[3])
	episode, _ := strconv.Atoi(p[4])
	detail := "/subtitles/" + p[0] + "/" + p[1] + "/" + p[2]
	raw, err := f.get(ctx, detail+"/download", http.Header{"Referer": {f.base + detail}})
	if err != nil {
		return nil, "", fmt.Errorf("subf2m download: %w", err)
	}
	return subtitleDownload(p[0], raw, season, episode, p[5] == "1", lang)
}

// subf2mSearch looks the title up on subf2m with its TMDB names and IMDb id.
func (s *Service) subf2mSearch(ctx context.Context, q Query) ([]candidate, error) {
	if s.cfg.Titles == nil || (q.isTV() && q.Season <= 0) {
		return nil, nil
	}
	info, err := s.cfg.Titles(q.MediaType, q.TmdbID)
	if err != nil {
		return nil, fmt.Errorf("subf2m: title lookup: %w", err)
	}
	t := subf2mTitle{Names: info.Names, Year: info.Year}
	if t.Year == 0 {
		t.Year = q.Year
	}
	if s.cfg.IMDb != nil {
		t.IMDb, _ = s.cfg.IMDb(q.MediaType, q.TmdbID) // without it the year checks the page
	}
	return s.sf.search(ctx, q, t)
}
