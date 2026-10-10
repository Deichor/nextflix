package catalog

import (
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Query params /api/tmdb/discover/movie passes on to TMDB, each with a validator that returns
// the normalized value ("" drops it). Anything else is ignored, so cache keys stay bounded.
var discoverParamRules = map[string]func(string) string{
	"sort_by":                  oneOf(movieSorts),
	"with_genres":              genreList,
	"without_genres":           genreList,
	"with_original_language":   langCode,
	"vote_count.gte":           intIn(0, 10_000_000),
	"vote_count.lte":           intIn(0, 10_000_000),
	"vote_average.gte":         floatIn(0, 10),
	"vote_average.lte":         floatIn(0, 10),
	"primary_release_date.gte": isoDate,
	"primary_release_date.lte": isoDate,
	"release_date.gte":         isoDate,
	"release_date.lte":         isoDate,
	"page":                     intIn(1, 500),
}

var movieSorts = map[string]bool{}

func init() {
	for _, f := range []string{"popularity", "vote_average", "vote_count", "primary_release_date",
		"revenue", "title", "original_title"} {
		movieSorts[f+".asc"], movieSorts[f+".desc"] = true, true
	}
}

// discoverParams keeps the whitelisted params of q (first value each); adult titles stay off.
func discoverParams(q url.Values) map[string]string {
	out := map[string]string{"include_adult": "false"}
	for k, rule := range discoverParamRules {
		if v := q.Get(k); v != "" {
			if n := rule(v); n != "" {
				out[k] = n
			}
		}
	}
	return out
}

var genreListRe = regexp.MustCompile(`^[0-9]{1,6}([,|][0-9]{1,6}){0,9}$`)

func genreList(v string) string {
	if genreListRe.MatchString(v) {
		return v
	}
	return ""
}

var langCodeRe = regexp.MustCompile(`^[a-z]{2}$`)

func langCode(v string) string {
	if langCodeRe.MatchString(v) {
		return v
	}
	return ""
}

func isoDate(v string) string {
	if t, err := time.Parse("2006-01-02", v); err == nil && t.Year() >= 1870 && t.Year() <= 2200 {
		return t.Format("2006-01-02")
	}
	return ""
}

func oneOf(set map[string]bool) func(string) string {
	return func(v string) string {
		if set[v] {
			return v
		}
		return ""
	}
}

func intIn(lo, hi int) func(string) string {
	return func(v string) string {
		if n, err := strconv.Atoi(v); err == nil && n >= lo && n <= hi {
			return strconv.Itoa(n)
		}
		return ""
	}
}

func floatIn(lo, hi float64) func(string) string {
	return func(v string) string {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= lo && f <= hi {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return ""
	}
}
