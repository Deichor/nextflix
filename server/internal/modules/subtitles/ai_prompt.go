package subtitles

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The request protocol of the AI translation. The lines go out as JSON with an id each, context
// lines without one; the answer is JSON (structured output) with one item per id that echoes the
// first words of its line. Every item is checked against its line before it counts, so an answer
// that does not map 1:1 (shifted, merged, skipped lines) is asked again and never stored.

// aiInLine is a line of a request: one to translate (ID, Text) or a context line (Context and
// its translation when there is one).
type aiInLine struct {
	ID         int    `json:"id,omitempty"`
	Text       string `json:"text,omitempty"`
	Context    string `json:"context,omitempty"`
	Translated string `json:"translated,omitempty"`
}

// aiOutLine is an item of the answer.
type aiOutLine struct {
	ID   int    `json:"id"`
	Src  string `json:"src"`
	Text string `json:"text"`
}

// aiSchema is the structured output format of every request (one schema: it is compiled once).
var aiSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"lines": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "integer"},
					"src":  map[string]any{"type": "string"},
					"text": map[string]any{"type": "string"},
				},
				"required":             []string{"id", "src", "text"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"lines"},
	"additionalProperties": false,
}

// aiEchoWords is how many words of its line an answer item echoes.
const aiEchoWords = 3

// aiNextToMissing: a fine looking answer next to a missing one (it may hold both lines).
const aiNextToMissing = "next to a missing line"

func aiSystem(lang AILanguage) string {
	return fmt.Sprintf("You translate film and TV subtitles into %[1]s.\n"+
		"The user message gives the title, then JSON whose \"lines\" are in the order they are shown on screen. "+
		"Translate every line that has an \"id\" and a \"text\". Lines with \"context\" instead are only there to "+
		"show what is said around them (with their translation in \"translated\" when there is one): never answer them.\n"+
		"Answer with one item per id: \"id\" copied, \"src\" the first %[2]d words of that line's \"text\" copied exactly, "+
		"\"text\" the translation of that line. Every id exactly once, in order.\n"+
		"Each translation is shown only while its own line is spoken, so it must say what that line says. When a "+
		"sentence runs over several lines, keep each part in its own line and reorder only as much as %[1]s grammar "+
		"needs; never leave a line empty and never move a whole line into another.\n"+
		"Keep the line breaks (\"\\n\") and the leading \"-\" of each speaker's line. Keep <i>, <b> and <u> tags. "+
		"Write natural, concise spoken %[1]s that reads well as subtitles. Keep names. Fit word choice and tone to the "+
		"title's genre and story without changing the meaning. Write only in %[1]s; a line that is only a name or a "+
		"sound may stay as it is.", lang.Name, aiEchoWords)
}

// aiUser is the user message: the title block (aiTitle) and the lines.
func aiUser(about string, lines []aiInLine) string {
	b, _ := json.Marshal(struct {
		Lines []aiInLine `json:"lines"`
	}{lines})
	return about + "\n" + string(b)
}

// aiPlain is a cue text as the model sees it: entities decoded (the answer is escaped again).
func aiPlain(s string) string { return html.UnescapeString(s) }

// aiCheck is what checkAIAnswer needs of one asked line.
type aiCheck struct {
	src  string   // source text
	near []string // source texts of the lines around it
	pos  int      // place among the asked lines, in screen order
}

// aiVerdict sorts the items of an answer.
type aiVerdict struct {
	ok   map[int]string // id -> accepted translation
	soft map[int]string // id -> translation that failed only a plausibility check
	why  map[int]string // id -> why it was not accepted
	// untrusted: the answer as a whole does not map 1:1 to the lines; nothing of it counts
	untrusted string
}

var aiWordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

func aiWords(s string) []string {
	return aiWordRe.FindAllString(strings.ToLower(cueTag.ReplaceAllString(aiPlain(s), "")), -1)
}

// aiJSONObject is the outermost JSON object of an answer (structured output answers only JSON;
// anything around it is ignored).
func aiJSONObject(s string) string {
	i, k := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || k < i {
		return ""
	}
	return s[i : k+1]
}

// checkAIAnswer reads an answer for the lines asked (by id) and checks every item against its line.
func checkAIAnswer(answer string, asked map[int]aiCheck, lang string, names map[string]bool) aiVerdict {
	v := aiVerdict{ok: map[int]string{}, soft: map[int]string{}, why: map[int]string{}}
	var out struct {
		Lines []aiOutLine `json:"lines"`
	}
	if err := json.Unmarshal([]byte(aiJSONObject(answer)), &out); err != nil {
		v.untrusted = "no JSON answer"
		return v
	}
	if len(out.Lines) == 0 {
		v.untrusted = "no lines answered"
		return v
	}
	distrust := func(why string) aiVerdict {
		return aiVerdict{ok: map[int]string{}, soft: map[int]string{}, why: map[int]string{}, untrusted: why}
	}
	seen := map[int]bool{}
	var echoBad []int
	for _, l := range out.Lines {
		c, ok := asked[l.ID]
		switch {
		case !ok:
			continue // a context line answered with an invented id: the echoes check the others
		case seen[l.ID]:
			return distrust(fmt.Sprintf("id %d answered twice", l.ID))
		}
		seen[l.ID] = true
		if !aiEchoMatches(l.Src, c.src) {
			echoBad = append(echoBad, l.ID)
			v.why[l.ID] = "echo of another line"
			continue
		}
		text := cleanCueText(l.Text)
		if why := aiHardFault(text, c.src, lang); why != "" {
			v.why[l.ID] = why
			continue
		}
		if why := aiSoftFault(text, c, names); why != "" {
			v.soft[l.ID] = text
			v.why[l.ID] = why
			continue
		}
		v.ok[l.ID] = text
	}
	// One stray echo may be a slip; more mean the items follow other lines than their ids say
	if len(echoBad) > 1 {
		return distrust(fmt.Sprintf("%d items echo other lines", len(echoBad)))
	}
	for id := range asked {
		if !seen[id] {
			v.why[id] = "not answered"
		}
	}
	// A line answered empty, not at all or with another line's start: its text may sit in a
	// neighbour (two lines merged into one), so the neighbours are asked again too
	byPos := map[int]int{}
	for id, c := range asked {
		byPos[c.pos] = id
	}
	for id, c := range asked {
		if w := v.why[id]; w != "empty" && w != "not answered" && w != "echo of another line" {
			continue
		}
		for _, p := range []int{c.pos - 1, c.pos + 1} {
			if n, ok := byPos[p]; ok {
				if tr, ok := v.ok[n]; ok {
					delete(v.ok, n)
					v.soft[n], v.why[n] = tr, aiNextToMissing
				}
			}
		}
	}
	return v
}

// aiEchoMatches: the echo is the start of the line (at least one word, up to aiEchoWords).
func aiEchoMatches(echo, src string) bool {
	sw, ew := aiWords(src), aiWords(echo)
	if len(sw) == 0 {
		return true // nothing to echo ("♪", "...")
	}
	n := min(len(sw), aiEchoWords)
	if len(ew) == 0 {
		return false
	}
	ew = ew[:min(len(ew), n)]
	for i, w := range ew {
		if sw[i] != w {
			return false
		}
	}
	return true
}

// aiHardFault is why a translation cannot stand for its line at all ("" = none).
func aiHardFault(text, src, lang string) string {
	switch {
	case text == "" || (hasLetter(src) && !hasLetter(text)):
		return "empty"
	case foreignScript(text, lang):
		return "foreign script"
	}
	return ""
}

// aiSoftFault is why a translation looks like it belongs to another line ("" = none). Such a
// line is asked again; a second answer that looks off too is taken (the model insists).
func aiSoftFault(text string, c aiCheck, names map[string]bool) string {
	src := aiPlain(c.src)
	ns, nt := utf8.RuneCountInString(src), utf8.RuneCountInString(aiPlain(text))
	switch {
	case nt > ns*5/2+20:
		return "much longer than its line"
	case ns >= 25 && nt*4 < ns:
		return "much shorter than its line"
	case (dialogLines(src) >= 2) != (dialogLines(text) >= 2):
		return "speaker lines differ"
	}
	sw := aiWords(src)
	if tw := aiWords(text); len(sw) >= 3 && strings.Join(sw, " ") == strings.Join(tw, " ") {
		return "not translated"
	}
	if n := foreignName(text, c, names); n != "" {
		return "name " + n + " of another line"
	}
	return ""
}

// dialogLines counts the lines of a cue that start with a speaker dash.
func dialogLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(cueTag.ReplaceAllString(l, "")), "-") {
			n++
		}
	}
	return n
}

// foreignName is a name in text that its own line does not have but a line around it has.
func foreignName(text string, c aiCheck, names map[string]bool) string {
	own := map[string]bool{}
	for _, w := range aiWordRe.FindAllString(aiPlain(c.src), -1) {
		own[w] = true
	}
	for _, w := range aiWordRe.FindAllString(aiPlain(text), -1) {
		if !names[w] || own[w] {
			continue
		}
		for _, n := range c.near {
			for _, x := range aiWordRe.FindAllString(aiPlain(n), -1) {
				if x == w {
					return w
				}
			}
		}
	}
	return ""
}

// aiNames are the words of the source that are names: capitalized inside a sentence and never
// written in lower case.
func aiNames(texts []string) map[string]bool {
	capital, lower := map[string]bool{}, map[string]bool{}
	for _, t := range texts {
		for _, line := range strings.Split(aiPlain(cueTag.ReplaceAllString(t, "")), "\n") {
			words := strings.Fields(line)
			for i, w := range words {
				core := aiWordRe.FindString(w)
				r := []rune(core)
				if len(r) < 3 {
					continue
				}
				if string(r[0]) == strings.ToLower(string(r[0])) {
					lower[strings.ToLower(core)] = true
					continue
				}
				prev := ""
				if i > 0 {
					prev = words[i-1]
				}
				if prev != "" && !strings.ContainsAny(prev[len(prev)-1:], ".!?:") && strings.ToUpper(core) != core {
					capital[core] = true
				}
			}
		}
	}
	out := map[string]bool{}
	for w := range capital {
		if !lower[strings.ToLower(w)] {
			out[w] = true
		}
	}
	return out
}
