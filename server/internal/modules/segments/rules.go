package segments

import (
	"regexp"
	"strings"
)

// Chapter names (whole name, an optional number and a "- Song" style suffix allowed). Generic
// names ("Chapter 3", "Opening Scene") never match, so they never move the skip buttons.
var (
	chapterTail  = `(?:\s*\d{1,2})?(?:\s*[-:–(\[].*)?$`
	introNames   = regexp.MustCompile(`^(?:intro|introduction|opening|op|opening (?:credits|titles|theme|song|sequence)|title sequence|main titles?|theme song|giriş|giriş jeneriği|vorspann|générique(?: de)? début|apertura|sigla(?: iniziale)?)` + chapterTail)
	creditsNames = regexp.MustCompile(`^(?:credits|end credits|ending|ending (?:credits|titles|theme|song)|closing credits|ed|outro|end titles|kapanış jeneriği|son jenerik|abspann|générique(?: de)? fin|créditos(?: finales)?|titoli di coda|sigla finale)` + chapterTail)
)

func chapterKind(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case t == "":
		return ""
	case introNames.MatchString(t):
		return "intro"
	case creditsNames.MatchString(t):
		return "credits"
	}
	return ""
}

// fromChapters finds the intro and the end credits among named chapters.
func fromChapters(chapters []Chapter, duration float64) (intro, credits *Segment) {
	for _, c := range chapters {
		switch chapterKind(c.Title) {
		case "intro":
			if intro == nil {
				intro = validIntro(c.Start, c.End, duration, "chapters")
			}
		case "credits":
			if credits == nil {
				credits = validCredits(c.Start, c.End, duration, "chapters")
			}
		}
	}
	return intro, credits
}

// validIntro keeps an intro that looks like one: 5 s to 4 min long and in the first half of
// the episode (an intro after a long cold open still counts).
func validIntro(start, end, duration float64, source string) *Segment {
	if start < 1 {
		start = 0
	}
	n := end - start
	if end <= 0 || n < 5 || n > 240 || (duration > 0 && end > duration*0.5) {
		return nil
	}
	return &Segment{Start: start, End: end, Source: source}
}

// validCredits keeps end credits only where an episode really ends: in the second half, at
// most 15 minutes before the end and not in its last 5 s. Without a known length nothing is
// trusted (a wrong release version would put the prompt in the middle of the episode).
func validCredits(start, end, duration float64, source string) *Segment {
	if duration <= 0 || start < duration*0.5 || duration-start > 15*60 || start > duration-5 {
		return nil
	}
	if end <= start || end > duration {
		end = duration
	}
	if end-start < 5 {
		return nil
	}
	return &Segment{Start: start, End: end, Source: source}
}
