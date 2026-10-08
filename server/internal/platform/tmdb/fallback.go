package tmdb

import (
	"fmt"
	"log"
)

// TMDB leaves many translations incomplete: a tr-TR answer can have an empty overview or
// tagline, and an untranslated title comes back in the original language (often another
// script, e.g. Japanese). Those gaps are filled from the en-US answer, which is fetched
// (and cached) only when an item needs it.

// Untranslated reports whether a localized title is just the original title of a title
// whose original language is neither lang nor English (English is no better then).
func Untranslated(lang, title, original, origLang string) bool {
	return title != "" && title == original && origLang != "" && origLang != lang && origLang != LangEN
}

// MissingText reports whether a localized item lacks text the English answer may have.
func MissingText(lang, title, original, origLang, overview string) bool {
	if lang == LangEN {
		return false
	}
	return overview == "" || title == "" || Untranslated(lang, title, original, origLang)
}

// FillText copies the English overview into an empty one and the English title over an
// empty or untranslated one.
func FillText(lang string, title, overview *string, original, origLang, enTitle, enOverview string) {
	if *overview == "" {
		*overview = enOverview
	}
	if enTitle != "" && (*title == "" || Untranslated(lang, *title, original, origLang)) {
		*title = enTitle
	}
}

// text points at the translatable fields of one list item.
type text struct {
	id       int
	title    *string
	overview *string
	original string
	origLang string
}

func movieText(m *Movie) text {
	return text{m.ID, &m.Title, &m.Overview, m.OriginalTitle, m.OriginalLanguage}
}

func tvText(t *TV) text {
	return text{t.ID, &t.Name, &t.Overview, t.OriginalName, t.OriginalLanguage}
}

func movieVideosText(m *MovieWithVideos) text { return movieText(&m.Movie) }

func searchText(r *SearchResult) text {
	return text{r.ID, &r.Title, &r.Overview, r.OriginalTitle, r.OriginalLanguage}
}

func (t text) missing(lang string) bool {
	return MissingText(lang, *t.title, t.original, t.origLang, *t.overview)
}

func (t text) fill(lang string, en text) {
	FillText(lang, t.title, t.overview, t.original, t.origLang, *en.title, *en.overview)
}

// fillList fills the gaps of a localized list from the English list english returns. The
// English list is only fetched when an item has a gap and is never modified.
func fillList[T any](c *Client, items []T, get func(*T) text, english func() ([]T, error)) {
	lang := c.Lang()
	if lang == LangEN {
		return
	}
	need := false
	for i := range items {
		if get(&items[i]).missing(lang) {
			need = true
			break
		}
	}
	if !need {
		return
	}
	en, err := english()
	if err != nil {
		log.Printf("tmdb: english fallback: %v", err)
		return
	}
	byID := make(map[int]text, len(en))
	for i := range en {
		t := get(&en[i])
		byID[t.id] = t
	}
	for i := range items {
		t := get(&items[i])
		if !t.missing(lang) {
			continue
		}
		if e, ok := byID[t.id]; ok {
			t.fill(lang, e)
		}
	}
}

// fillMovieDetails fills the gaps of localized movie details from the English details.
func (c *Client) fillMovieDetails(d *MovieDetails) {
	if !c.localized() || (!movieText(&d.Movie).missing(c.Lang()) && d.Tagline != "") {
		return
	}
	en, err := c.english().GetMovieDetails(d.ID)
	if err != nil {
		log.Printf("tmdb: english fallback of movie %d: %v", d.ID, err)
		return
	}
	movieText(&d.Movie).fill(c.Lang(), movieText(&en.Movie))
	if d.Tagline == "" {
		d.Tagline = en.Tagline
	}
}

// fillTVDetails fills the gaps of localized TV details (and its season overviews).
func (c *Client) fillTVDetails(d *TVDetails) {
	if !c.localized() {
		return
	}
	need := tvText(&d.TV).missing(c.Lang()) || d.Tagline == ""
	for _, s := range d.Seasons {
		need = need || s.Overview == ""
	}
	if !need {
		return
	}
	en, err := c.english().GetTVDetails(d.ID)
	if err != nil {
		log.Printf("tmdb: english fallback of TV %d: %v", d.ID, err)
		return
	}
	tvText(&d.TV).fill(c.Lang(), tvText(&en.TV))
	if d.Tagline == "" {
		d.Tagline = en.Tagline
	}
	fillSeasons(d.Seasons, en.Seasons)
}

func fillSeasons(seasons, en []Season) {
	byNumber := make(map[int]Season, len(en))
	for _, s := range en {
		byNumber[s.SeasonNumber] = s
	}
	for i := range seasons {
		if e, ok := byNumber[seasons[i].SeasonNumber]; ok && seasons[i].Overview == "" {
			seasons[i].Overview = e.Overview
		}
	}
}

// fillSeason fills empty season and episode overviews and names from the English season.
func fillSeason(s, en *Season) {
	if s.Overview == "" {
		s.Overview = en.Overview
	}
	byNumber := make(map[int]Episode, len(en.Episodes))
	for _, e := range en.Episodes {
		byNumber[e.EpisodeNumber] = e
	}
	for i := range s.Episodes {
		ep := &s.Episodes[i]
		e, ok := byNumber[ep.EpisodeNumber]
		if !ok {
			continue
		}
		if ep.Overview == "" {
			ep.Overview = e.Overview
		}
		if (ep.Name == "" || placeholderEpisodeName(ep.Name, ep.EpisodeNumber)) && !placeholderEpisodeName(e.Name, e.EpisodeNumber) {
			ep.Name = orDefault(e.Name, ep.Name)
		}
	}
}

// placeholderEpisodeName reports TMDB's generated name of an untitled episode
// ("Episode 3", Turkish "3. Bölüm").
func placeholderEpisodeName(name string, n int) bool {
	return name == fmt.Sprintf("Episode %d", n) || name == fmt.Sprintf("%d. Bölüm", n)
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func seasonMissingText(s *Season) bool {
	if s.Overview == "" {
		return true
	}
	for _, e := range s.Episodes {
		if e.Overview == "" || e.Name == "" || placeholderEpisodeName(e.Name, e.EpisodeNumber) {
			return true
		}
	}
	return false
}
