package subtitles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const yifyDefaultBase = "https://yifysubtitles.ch"

// yifySubs scrapes YIFY Subtitles: movies only, one page per IMDb id listing every language,
// files as zips.
type yifySubs struct{ *scraper }

func newYIFY(base string, pages diskCache) *yifySubs {
	if base == "" {
		base = yifyDefaultBase
	}
	return &yifySubs{newScraper("yifysubtitles", base, "yify-pages", pages, time.Second)}
}

type yifyRow struct {
	Slug     string
	Lang     string
	Releases []string
	Uploader string
	Rating   int
	HI       bool
}

var (
	yifyRowStart = regexp.MustCompile(`<tr data-id="\d+"`)
	yifyRating   = regexp.MustCompile(`(?s)<td class="rating-cell">\s*<span class="label[^"]*">\s*(-?\d+)\s*</span>`)
	yifyLang     = regexp.MustCompile(`<span class="sub-lang">([^<]+)</span>`)
	yifyLink     = regexp.MustCompile(`(?s)<a href="/subtitles/([a-z0-9-]+)">\s*<span class="text-muted">subtitle</span>(.*?)</a>`)
	yifyUploader = regexp.MustCompile(`(?s)<td class="uploader-cell">(.*?)</td>`)
	yifySlugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,200}$`)
	yifyBreak    = regexp.MustCompile(`(?i)<br\s*/?>`)
)

// parseYIFYList reads the subtitle rows of a movie page.
func parseYIFYList(page string) []yifyRow {
	locs := yifyRowStart.FindAllStringIndex(page, -1)
	var rows []yifyRow
	for i, loc := range locs {
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		b := page[loc[0]:end]
		if j := strings.Index(b, "</tr>"); j > 0 {
			b = b[:j]
		}
		l := yifyLink.FindStringSubmatch(b)
		lang := yifyLang.FindStringSubmatch(b)
		if l == nil || lang == nil {
			continue
		}
		r := yifyRow{Slug: l[1], Lang: langFromName(lang[1]), HI: strings.Contains(b, "hi-subtitle")}
		for _, name := range yifyBreak.Split(l[2], -1) {
			if name = taText(name); name != "" {
				r.Releases = append(r.Releases, name)
			}
		}
		if m := yifyRating.FindStringSubmatch(b); m != nil {
			r.Rating, _ = strconv.Atoi(m[1])
		}
		if m := yifyUploader.FindStringSubmatch(b); m != nil {
			r.Uploader = taText(m[1])
		}
		rows = append(rows, r)
	}
	return rows
}

func (y *yifySubs) search(ctx context.Context, q Query, imdbID string) ([]candidate, error) {
	if q.isTV() {
		return nil, nil
	}
	num := strings.TrimPrefix(strings.ToLower(imdbID), "tt")
	if !taDigits.MatchString(num) {
		return nil, nil
	}
	page, err := y.page(ctx, "/movie-imdb/tt"+num)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, l := range q.Langs {
		want[l] = true
	}
	var out []candidate
	for _, r := range parseYIFYList(page) {
		if !want[r.Lang] {
			continue
		}
		quality := 0
		switch {
		case r.Rating > 0:
			quality = 2
		case r.Rating < 0:
			quality = 4
		}
		release := firstNonEmpty(append(append([]string{}, r.Releases...), "YIFY Subtitles")...)
		out = append(out, candidate{
			Sub: Sub{
				ID:       encodeID(SourceYIFY, r.Lang, r.Slug),
				Source:   SourceYIFY,
				Language: r.Lang,
				Release:  release,
				HI:       r.HI || hiName.MatchString(release),
				Forced:   forcedName.MatchString(release),
				Author:   r.Uploader,
			},
			Releases: r.Releases,
			Quality:  quality,
		})
	}
	return out, nil
}

// fetch downloads ref "<subtitle slug>" ("inception-2010-turkish-yify-392342").
func (y *yifySubs) fetch(ctx context.Context, ref, lang string) ([]byte, string, error) {
	if !yifySlugRe.MatchString(ref) {
		return nil, "", errors.New("bad YIFY Subtitles id")
	}
	raw, err := y.get(ctx, "/subtitle/"+ref+".zip", http.Header{"Referer": {y.base + "/subtitles/" + ref}})
	if err != nil {
		return nil, "", fmt.Errorf("yifysubtitles download: %w", err)
	}
	return subtitleDownload(ref, raw, 0, 0, false, lang)
}
