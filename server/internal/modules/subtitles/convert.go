package subtitles

import (
	"errors"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrUnsupportedFormat is returned when no cues could be read from a subtitle file.
var ErrUnsupportedFormat = errors.New("unsupported or empty subtitle file")

const defaultMicroDVDFPS = 23.976

type cue struct {
	start, end time.Duration
	text       string
}

// ToWebVTT converts any supported subtitle (srt, ass/ssa, vtt, microdvd .sub) in any common encoding
// (UTF-8 with/without BOM, UTF-16 LE/BE BOM, Windows-1254, ISO-8859-9, Windows-1252, ISO-8859-1) to UTF-8 WebVTT.
func ToWebVTT(raw []byte, nameHint string) ([]byte, error) {
	text, _ := decodeText(raw)
	var cues []cue
	switch detectFormat(text, nameHint) {
	case "vtt":
		cues = parseTimed(text, true)
	case "ass":
		cues = parseASS(text)
	case "sub":
		cues = parseMicroDVD(text)
	default:
		cues = parseTimed(text, false)
	}
	if len(cues) == 0 {
		return nil, ErrUnsupportedFormat
	}
	return writeVTT(cues), nil
}

var microDVDLine = regexp.MustCompile(`^\{(\d+)\}\{(\d*)\}(.*)$`)

// detectFormat sniffs the content first and falls back to the file extension.
func detectFormat(text, nameHint string) string {
	head := strings.TrimSpace(text)
	if len(head) > 8192 {
		head = head[:8192]
	}
	switch {
	case strings.HasPrefix(head, "WEBVTT"):
		return "vtt"
	case strings.Contains(head, "[Script Info]") || strings.Contains(head, "[Events]") || strings.Contains(head, "\nDialogue:"):
		return "ass"
	case microDVDLine.MatchString(firstLine(head)):
		return "sub"
	case strings.Contains(head, "-->"):
		return "srt"
	}
	switch strings.ToLower(filepath.Ext(nameHint)) {
	case ".vtt":
		return "vtt"
	case ".ass", ".ssa":
		return "ass"
	case ".sub":
		return "sub"
	}
	return "srt"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// SRT and WebVTT timing lines. Tolerates missing hours, dots or colons before the milliseconds and stray spaces.
var timingLine = regexp.MustCompile(`^\s*((?:\d+\s*:\s*)?\d{1,2}\s*:\s*\d{1,2}(?:\s*[,.:]\s*\d{1,3})?)\s*-{1,2}>\s*((?:\d+\s*:\s*)?\d{1,2}\s*:\s*\d{1,2}(?:\s*[,.:]\s*\d{1,3})?)`)

var numberLine = regexp.MustCompile(`^\s*\d+\s*$`)

// parseTimed reads SRT and WebVTT. Everything between two timing lines is the first cue's text, minus the index
// (or VTT cue identifier) that belongs to the next cue, so missing or extra blank lines do not lose cues.
func parseTimed(text string, vtt bool) []cue {
	lines := strings.Split(text, "\n")
	var cues []cue
	var cur *cue
	var body []string
	skipBlock := false
	prevBlank := true

	flush := func(nextIsTiming bool) {
		if cur == nil {
			return
		}
		// Drop the next cue's index or identifier.
		if nextIsTiming && len(body) > 0 {
			last := len(body) - 1
			if numberLine.MatchString(body[last]) || (vtt && last >= 1 && strings.TrimSpace(body[last-1]) == "") {
				body = body[:last]
			}
		}
		cur.text = strings.Join(body, "\n")
		cues = append(cues, *cur)
		cur, body = nil, nil
	}

	for _, line := range lines {
		if m := timingLine.FindStringSubmatch(line); m != nil {
			start, ok1 := parseClock(m[1])
			end, ok2 := parseClock(m[2])
			flush(true)
			skipBlock = false
			prevBlank = false
			if ok1 && ok2 {
				cur = &cue{start: start, end: end}
			}
			continue
		}
		blank := strings.TrimSpace(line) == ""
		if vtt && prevBlank && !blank {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "NOTE") || strings.HasPrefix(t, "STYLE") || strings.HasPrefix(t, "REGION") {
				skipBlock = true
			}
		}
		if blank {
			skipBlock = false
		}
		prevBlank = blank
		if cur != nil && !skipBlock {
			body = append(body, line)
		}
	}
	flush(false)
	return cues
}

// parseClock reads [h:]mm:ss[,.:]fff; a short fraction is a decimal fraction (",5" is 500 ms).
func parseClock(s string) (time.Duration, bool) {
	s = strings.Join(strings.Fields(s), "")
	frac := ""
	if i := strings.LastIndexAny(s, ",."); i >= 0 {
		s, frac = s[:i], s[i+1:]
	}
	parts := strings.Split(s, ":")
	if frac == "" && len(parts) == 4 {
		frac, parts = parts[3], parts[:3]
	}
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		nums[i] = n
	}
	var h, m, sec int
	if len(nums) == 3 {
		h, m, sec = nums[0], nums[1], nums[2]
	} else {
		m, sec = nums[0], nums[1]
	}
	ms := 0
	if frac != "" {
		for len(frac) < 3 {
			frac += "0"
		}
		ms, _ = strconv.Atoi(frac[:3])
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second + time.Duration(ms)*time.Millisecond
	return d, true
}

// ASS/SSA

type assStyle struct{ italic, bold, underline bool }

var assOverride = regexp.MustCompile(`\{[^}]*\}`)

func parseASS(text string) []cue {
	section := ""
	eventFormat := []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
	var styleFormat []string
	styles := map[string]assStyle{}
	var cues []cue

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line)
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		inEvents := section == "[events]"
		inStyles := strings.Contains(section, "styles")

		switch {
		case key == "format" && inEvents:
			eventFormat = splitFormat(value)
		case key == "format" && inStyles:
			styleFormat = splitFormat(value)
		case key == "style" && inStyles && styleFormat != nil:
			f := splitFields(value, len(styleFormat))
			st := assStyle{}
			for i, name := range styleFormat {
				if i >= len(f) {
					break
				}
				on := f[i] != "0" && f[i] != ""
				switch name {
				case "italic":
					st.italic = on
				case "bold":
					st.bold = on
				case "underline":
					st.underline = on
				}
			}
			if i := indexOf(styleFormat, "name"); i >= 0 && i < len(f) {
				styles[f[i]] = st
			}
		case key == "dialogue" && (inEvents || section == ""):
			f := splitFields(value, len(eventFormat))
			field := func(name string) string {
				if i := indexOf(eventFormat, name); i >= 0 && i < len(f) {
					return f[i]
				}
				return ""
			}
			start, ok1 := parseASSClock(field("start"))
			end, ok2 := parseASSClock(field("end"))
			if !ok1 || !ok2 {
				continue
			}
			body := assText(field("text"), styles[strings.TrimPrefix(field("style"), "*")])
			if strings.TrimSpace(body) == "" {
				continue
			}
			cues = append(cues, cue{start: start, end: end, text: body})
		}
	}
	return cues
}

func splitFormat(v string) []string {
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	return parts
}

// splitFields splits on commas, keeping commas inside the last field (the dialogue text).
func splitFields(v string, n int) []string {
	f := strings.SplitN(v, ",", n)
	for i := 0; i < len(f)-1; i++ {
		f[i] = strings.TrimSpace(f[i])
	}
	return f
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func parseASSClock(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	return parseClock(s)
}

var (
	assToggle  = regexp.MustCompile(`^(?:i\d?|b\d|u\d?)$`)
	assDrawing = regexp.MustCompile(`^p\d+$`)
	assReset   = regexp.MustCompile(`^r[A-Z*]`) // \r<StyleName>; the named style is not applied
)

// assText turns an ASS dialogue text into cue text: override blocks are dropped except italic/bold/underline
// switches, \N and \n become line breaks, \h a space, and vector drawings are skipped.
func assText(s string, base assStyle) string {
	var out strings.Builder
	state := base
	open := map[byte]bool{}
	setTag := func(tag byte, on bool) {
		if open[tag] == on {
			return
		}
		if on {
			out.WriteString("<" + string(tag) + ">")
		} else {
			out.WriteString("</" + string(tag) + ">")
		}
		open[tag] = on
	}
	apply := func() {
		setTag('i', state.italic)
		setTag('b', state.bold)
		setTag('u', state.underline)
	}
	drawing := false
	apply()

	for len(s) > 0 {
		if s[0] == '{' {
			endIdx := strings.IndexByte(s, '}')
			if endIdx < 0 {
				s = s[1:]
				continue
			}
			block := s[1:endIdx]
			s = s[endIdx+1:]
			for _, tag := range strings.Split(block, `\`) {
				switch {
				case tag == "r" || assReset.MatchString(tag):
					state = base
				case assDrawing.MatchString(tag):
					drawing = tag[1:] != "0"
				case assToggle.MatchString(tag):
					on := tag[1:] != "0"
					switch tag[0] {
					case 'i':
						state.italic = on
					case 'b':
						state.bold = on
					case 'u':
						state.underline = on
					}
				}
			}
			apply()
			continue
		}
		next := strings.IndexByte(s, '{')
		if next < 0 {
			next = len(s)
		}
		chunk := s[:next]
		s = s[next:]
		if drawing {
			continue
		}
		chunk = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(chunk)
		out.WriteString(chunk)
	}
	state = assStyle{}
	apply()
	return out.String()
}

// MicroDVD: {start frame}{end frame}text, "|" separates lines; {y:i} styles a line, {Y:i} the whole cue.

var microDVDTag = regexp.MustCompile(`\{([yY]):([ibus,]+)\}`)

func parseMicroDVD(text string) []cue {
	fps := defaultMicroDVDFPS
	var cues []cue
	for i, line := range strings.Split(text, "\n") {
		m := microDVDLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		startF, _ := strconv.ParseFloat(m[1], 64)
		endF, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			endF = startF + 3*fps
		}
		body := strings.TrimSpace(m[3])
		// The first cue often carries the frame rate, e.g. {1}{1}23.976.
		if i < 3 && startF <= 1 && endF <= 1 {
			if v, err := strconv.ParseFloat(strings.ReplaceAll(body, ",", "."), 64); err == nil && v > 1 && v < 121 {
				fps = v
				continue
			}
		}
		cues = append(cues, cue{start: frames(startF, fps), end: frames(endF, fps), text: microDVDText(body)})
	}
	return cues
}

func frames(n, fps float64) time.Duration {
	return time.Duration(n / fps * float64(time.Second))
}

func microDVDText(body string) string {
	wholeItalic := false
	if m := microDVDTag.FindStringSubmatch(body); m != nil && m[1] == "Y" && strings.Contains(m[2], "i") {
		wholeItalic = true
	}
	lines := strings.Split(body, "|")
	for i, l := range lines {
		italic := false
		if m := microDVDTag.FindStringSubmatch(l); m != nil && m[1] == "y" && strings.Contains(m[2], "i") {
			italic = true
		}
		l = strings.TrimSpace(assOverride.ReplaceAllString(l, ""))
		if l != "" && strings.HasPrefix(l, "/") {
			l, italic = strings.TrimSpace(l[1:]), true
		}
		if italic && !wholeItalic && l != "" {
			l = "<i>" + l + "</i>"
		}
		lines[i] = l
	}
	out := strings.Join(lines, "\n")
	if wholeItalic {
		out = "<i>" + out + "</i>"
	}
	return out
}

// WebVTT output

var (
	htmlTag     = regexp.MustCompile(`<\s*(/?)\s*([a-zA-Z][a-zA-Z0-9.]*)[^>]*>`)
	srtOverride = regexp.MustCompile(`\{\\[^}]*\}`)
	srtBraceTag = regexp.MustCompile(`\{(/?)([ibu])\}`)
)

func writeVTT(cues []cue) []byte {
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].start < cues[j].start })
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	seen := map[string]bool{}
	for i, c := range cues {
		text := cleanCueText(c.text)
		if text == "" {
			continue
		}
		end := c.end
		if end <= c.start {
			// Broken end time: show until the next cue, at most a few seconds.
			end = c.start + 3*time.Second
			if i+1 < len(cues) && cues[i+1].start > c.start && cues[i+1].start < end {
				end = cues[i+1].start
			}
		}
		key := fmt.Sprintf("%d|%d|%s", c.start, end, text)
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttClock(c.start), vttClock(end), text)
	}
	return []byte(b.String())
}

func vttClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// cleanCueText keeps <i>, <b> and <u> (balanced), drops every other tag and override code, escapes the rest
// and removes blank lines, which would end a WebVTT cue early.
func cleanCueText(s string) string {
	s = srtOverride.ReplaceAllString(s, "")
	s = srtBraceTag.ReplaceAllString(s, "<$1$2>")

	var out strings.Builder
	var stack []string
	last := 0
	writeText := func(t string) {
		t = html.UnescapeString(t)
		t = strings.ReplaceAll(t, " ", " ")
		t = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(t)
		out.WriteString(t)
	}
	for _, m := range htmlTag.FindAllStringSubmatchIndex(s, -1) {
		writeText(s[last:m[0]])
		last = m[1]
		closing := s[m[2]:m[3]] == "/"
		name := strings.ToLower(s[m[4]:m[5]])
		if name != "i" && name != "b" && name != "u" {
			if name == "br" {
				out.WriteString("\n")
			}
			continue
		}
		if !closing {
			stack = append(stack, name)
			out.WriteString("<" + name + ">")
			continue
		}
		// Close only tags that are open, closing inner ones first.
		at := -1
		for k := len(stack) - 1; k >= 0; k-- {
			if stack[k] == name {
				at = k
				break
			}
		}
		if at < 0 {
			continue
		}
		for k := len(stack) - 1; k >= at; k-- {
			out.WriteString("</" + stack[k] + ">")
		}
		stack = append(stack[:at], stack[at+1:]...)
		for _, reopen := range stack[at:] {
			out.WriteString("<" + reopen + ">")
		}
	}
	writeText(s[last:])
	for k := len(stack) - 1; k >= 0; k-- {
		out.WriteString("</" + stack[k] + ">")
	}

	// Drop blank lines; a line holding only tags joins a neighbour so the tags stay balanced.
	var lines []string
	pending := ""
	for _, l := range strings.Split(out.String(), "\n") {
		l = strings.TrimSpace(strings.ReplaceAll(l, "-->", "->"))
		switch {
		case l == "":
		case onlyClosingTags.MatchString(l) && len(lines) > 0:
			lines[len(lines)-1] += l
		case emptyTags.MatchString(l):
			pending += l
		default:
			lines = append(lines, pending+l)
			pending = ""
		}
	}
	if len(lines) == 0 {
		return ""
	}
	lines[len(lines)-1] += pending
	return strings.Join(lines, "\n")
}

var (
	emptyTags       = regexp.MustCompile(`^(?:</?[ibu]>)+$`)
	onlyClosingTags = regexp.MustCompile(`^(?:</[ibu]>)+$`)
)
