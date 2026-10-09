package catalog

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Text helpers of the search ranker and the typo index: folding (Turkish letters, accents,
// punctuation), article stripping, match levels, edit distances and a phonetic key.

// foldRune maps letters that do not decompose into a base letter plus marks.
var foldRune = map[rune]string{
	'ı': "i", 'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'ł': "l", 'đ': "d", 'ð': "d", 'þ': "th",
}

// normalize lowercases s, folds Turkish letters and accents (İ/ı -> i, ş -> s, é -> e), drops
// apostrophes and turns every other non-alphanumeric run into one space.
func normalize(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if !ascii {
		s = norm.NFD.String(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	space := true // no leading space
	for _, r := range s {
		switch {
		case r == '\'' || r == '’' || r == '`' || r == '´' || unicode.Is(unicode.Mn, r):
			continue
		case r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9'):
			b.WriteRune(r)
			space = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 'a' - 'A')
			space = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if f, ok := foldRune[unicode.ToLower(r)]; ok {
				b.WriteString(f)
			} else {
				b.WriteRune(unicode.ToLower(r))
			}
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// stripArticle drops a leading English article ("the mandalorian" -> "mandalorian").
func stripArticle(n string) string {
	for _, a := range [...]string{"the ", "a ", "an "} {
		if strings.HasPrefix(n, a) && len(n) > len(a) {
			return n[len(a):]
		}
	}
	return n
}

// hasLatin reports whether a normalized string has an ASCII letter (typo correction is for
// Latin-script titles; TMDB itself finds the others).
func hasLatin(n string) bool {
	for i := 0; i < len(n); i++ {
		if n[i] >= 'a' && n[i] <= 'z' {
			return true
		}
	}
	return false
}

// Match levels of a query against one title, best first.
const (
	matchExact       = 1.0
	matchNoArticle   = 0.97
	matchPrefixWord  = 0.8 // query + word boundary starts the title
	matchPrefix      = 0.72
	matchPhrase      = 0.68 // whole words, contiguous
	matchAllWords    = 0.6
	matchWordPrefix  = 0.55 // a later word starts with the query
	matchSubstring   = 0.45
	matchFuzzyFactor = 0.6 // fuzzy similarity is scaled to stay below every literal match
	fuzzyMinSim      = 0.72
)

// query is a normalized search text.
type query struct {
	raw   string
	norm  string
	bare  string // without a leading article
	words []string
}

func newQuery(s string) query {
	n := normalize(s)
	return query{raw: s, norm: n, bare: stripArticle(n), words: strings.Fields(n)}
}

// textScore rates how well q matches a title (already normalized): exact > exact ignoring
// articles > prefix > whole words > substring > fuzzy, with a small bonus for covering more
// of the title. 0 when nothing matches.
func textScore(q query, title string) float64 {
	if q.norm == "" || title == "" {
		return 0
	}
	if title == q.norm {
		return matchExact
	}
	bare := stripArticle(title)
	if bare == q.bare {
		return matchNoArticle
	}
	cov := func() float64 { return 0.1 * float64(len(q.bare)) / float64(max(len(bare), 1)) }
	for _, t := range [...]string{title, bare} {
		for _, p := range [...]string{q.norm, q.bare} {
			if strings.HasPrefix(t, p) {
				if len(t) == len(p) || t[len(p)] == ' ' {
					return matchPrefixWord + cov()
				}
				return matchPrefix + cov()
			}
		}
	}
	padded := " " + title + " "
	if strings.Contains(padded, " "+q.bare+" ") {
		return matchPhrase + cov()
	}
	if len(q.words) > 1 {
		all := true
		for _, w := range q.words {
			if isArticle(w) {
				continue
			}
			if !strings.Contains(padded, " "+w+" ") {
				all = false
				break
			}
		}
		if all {
			return matchAllWords + cov()
		}
	}
	if strings.Contains(padded, " "+q.bare) {
		return matchWordPrefix + cov()
	}
	if len(q.bare) >= 3 && strings.Contains(title, q.bare) {
		return matchSubstring + cov()
	}
	if sim := similarity(q.bare, bare); sim >= fuzzyMinSim {
		return matchFuzzyFactor * sim
	}
	return 0
}

func isArticle(w string) bool { return w == "the" || w == "a" || w == "an" }

// similarity blends Damerau-Levenshtein and Jaro-Winkler similarity (and a phonetic match)
// of two normalized strings into [0, 1].
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	dl := 1 - float64(damerauLevenshtein(ra, rb))/float64(max(len(ra), len(rb)))
	sim := 0.6*dl + 0.4*jaroWinkler(ra, rb)
	if phonetic(a) == phonetic(b) {
		sim += 0.08
	}
	return min(sim, 0.99)
}

// damerauLevenshtein is the optimal string alignment distance (adjacent swaps cost 1).
func damerauLevenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	w := len(b) + 1
	prev2 := make([]int, w)
	prev := make([]int, w)
	cur := make([]int, w)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}

// jaroWinkler returns the Jaro-Winkler similarity (prefix scale 0.1, up to 4 characters).
func jaroWinkler(a, b []rune) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	window := max(len(a), len(b))/2 - 1
	window = max(window, 0)
	ma := make([]bool, len(a))
	mb := make([]bool, len(b))
	matches := 0
	for i := range a {
		lo, hi := max(0, i-window), min(len(b), i+window+1)
		for j := lo; j < hi; j++ {
			if !mb[j] && a[i] == b[j] {
				ma[i], mb[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	trans, k := 0, 0
	for i := range a {
		if !ma[i] {
			continue
		}
		for !mb[k] {
			k++
		}
		if a[i] != b[k] {
			trans++
		}
		k++
	}
	m := float64(matches)
	jaro := (m/float64(len(a)) + m/float64(len(b)) + (m-float64(trans)/2)/m) / 3
	prefix := 0
	for prefix < min(4, len(a), len(b)) && a[prefix] == b[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

// phonetic is a simplified Metaphone-like key of a normalized string: consonant skeleton per
// word (c/q -> k, z -> s, ph -> f, w -> v, h dropped, vowels dropped after the first letter,
// final "ng" -> "n", doubled letters collapsed). "mandoloarien" and "mandalorian" share "mndlrn".
func phonetic(n string) string {
	var b strings.Builder
	for wi, w := range strings.Fields(n) {
		if wi > 0 {
			b.WriteByte(' ')
		}
		var last byte
		for i := 0; i < len(w); i++ {
			c := w[i]
			var next byte
			if i+1 < len(w) {
				next = w[i+1]
			}
			switch c {
			case 'a', 'e', 'i', 'o', 'u', 'y':
				if i == 0 {
					c = 'a'
				} else {
					last = 0 // vowels separate doubled consonants ("mama" stays "mm")
					continue
				}
			case 'h':
				continue
			case 'c':
				if next == 'e' || next == 'i' || next == 'y' {
					c = 's'
				} else {
					c = 'k'
				}
			case 'q':
				c = 'k'
			case 'z':
				c = 's'
			case 'w':
				c = 'v'
			case 'p':
				if next == 'h' {
					c = 'f'
				}
			case 'g':
				if last == 'n' && i == len(w)-1 {
					continue
				}
			case 'x':
				if last != 'k' {
					b.WriteByte('k')
				}
				c = 's'
			}
			if c == last {
				continue
			}
			b.WriteByte(c)
			last = c
		}
	}
	return b.String()
}
