package streaming

// Cinema recordings on request: for a film that is only in cinemas the viewer may still
// choose to watch a recording ("Watch the cinema recording anyway", ?cinema=1 on resolve,
// prefetch, sidegrade and upgrade). Then the 425 gate of in_theaters is skipped, cinema
// recordings (TS, TELESYNC, HDTS, CAM, ...) are ranked instead of rejected, and the source
// cache key carries "|cinema" so a normal resolve never reuses one. Bonus material,
// namesakes and the duration check apply as usual.

// Source part of a cinema recording, best picture first: a DCP rip, then HDTS / TELESYNC /
// HDTC, screeners, R5, plain TS / telecine, and CAM far below. tsCinemaBase (one resolution
// step) puts them above the home sources: a film still in cinemas rarely has a real one (one that
// is real passes the duration check and is checked when the recordings fail).
const (
	tsCinemaBase     = 45
	tsCinemaDCP      = 34
	tsCinemaTS       = 30 // HDTS, TELESYNC, HDTC
	tsCinemaScreener = 26
	tsCinemaR5       = 24
	tsCinemaPlainTS  = 20 // TS, TC, telecine, pre-DVD
	tsCinemaOther    = 10 // LTE, line audio, hardcoded-sub leaks
	tsCinemaCam      = -20
	tsCinemaHQ       = 5   // "HQ HDTS", "HQ-CAM": a cleaner capture of its kind
	tsCinemaHardsub  = -15 // burned-in subtitles of another language than the profile's
)

// tsCinemaScore ranks a cinema recording by its capture (see the constants above).
func tsCinemaScore(rest []string, q tsResolveQuery) float64 {
	has := map[string]bool{}
	for i, t := range rest {
		has[t] = true
		if i+1 < len(rest) && len(t) > 1 && len(rest[i+1]) > 1 {
			has[t+rest[i+1]] = true // "HD-TS", "Tele-Sync", "DCP-Rip"
		}
	}
	hasAny := func(tokens ...string) bool {
		for _, t := range tokens {
			if has[t] {
				return true
			}
		}
		return false
	}
	var v float64
	switch {
	case hasAny("dcprip"):
		v = tsCinemaDCP
	case hasAny("hdts", "telesync", "hdtc"):
		v = tsCinemaTS
	case hasAny("screener", "scr", "dvdscr", "bdscr", "hdscr"):
		v = tsCinemaScreener
	case hasAny("r5"):
		v = tsCinemaR5
	case hasAny("ts", "tsrip", "tc", "telecine", "predvd", "pdvd"):
		v = tsCinemaPlainTS
	case hasAny("cam", "camrip", "hdcam", "hqcam"):
		v = tsCinemaCam
	default:
		v = tsCinemaOther
	}
	if hasAny("hq", "hqcam") {
		v += tsCinemaHQ
	}
	if hasAny("hc", "hcrip", "hardsub", "hardcoded", "korsub") && !(has["korsub"] && q.lang == "ko") {
		v += tsCinemaHardsub
	}
	return tsCinemaBase + v
}

// tsProfileLangTokens name an audio language in release names, by profile language.
var tsProfileLangTokens = map[string][]string{
	"tr": {"tr", "tur", "turk", "turkish", "turkce", "dublaj"},
	"en": {"eng", "english"},
}

// tsNamesLang: the name tags audio in the profile's language.
func tsNamesLang(rest []string, lang string) bool {
	for _, t := range rest {
		for _, w := range tsProfileLangTokens[lang] {
			if t == w {
				return true
			}
		}
		if l, ok := tsLangTokens[t]; ok && l == lang {
			return true
		}
	}
	return false
}
