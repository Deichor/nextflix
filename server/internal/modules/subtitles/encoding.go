package subtitles

import (
	"bytes"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Subtitle files come in whatever the uploader's editor saved. Turkish ones are mostly Windows-1254 or
// ISO-8859-9 when not UTF-8, so those are tried first and the decoded text is scored to pick the right one.

type charsetCandidate struct {
	name   string
	decode func([]byte) string
}

var singleByteCandidates = []charsetCandidate{
	{"windows-1254", decodeWith(charmap.Windows1254)},
	{"iso-8859-9", decodeWith(charmap.ISO8859_9)},
	{"windows-1252", decodeWith(charmap.Windows1252)},
	{"iso-8859-1", decodeWith(charmap.ISO8859_1)},
}

func decodeWith(cm *charmap.Charmap) func([]byte) string {
	return func(b []byte) string {
		out, err := cm.NewDecoder().Bytes(b)
		if err != nil {
			return string(bytes.ToValidUTF8(b, []byte("�")))
		}
		return string(out)
	}
}

// decodeText converts raw subtitle bytes to UTF-8 text with LF line endings and reports the charset it chose.
func decodeText(raw []byte) (string, string) {
	text, charset := detectAndDecode(raw)
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text, charset
}

func detectAndDecode(raw []byte) (string, string) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(bytes.ToValidUTF8(raw[3:], []byte("�"))), "utf-8-bom"
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return decodeUTF16(raw[2:], false), "utf-16le"
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return decodeUTF16(raw[2:], true), "utf-16be"
	}
	if le, ok := looksUTF16(raw); ok {
		if le {
			return decodeUTF16(raw, false), "utf-16le"
		}
		return decodeUTF16(raw, true), "utf-16be"
	}
	if utf8.Valid(raw) {
		s := string(raw)
		// Text that went through a wrong decode once ("TÃ¼rkÃ§e") is still valid UTF-8; undo it when that scores better.
		if fixed, ok := repairMojibake(s); ok {
			return fixed, "utf-8-mojibake"
		}
		return s, "utf-8"
	}

	best, bestName, bestScore := "", "", 0
	// A mostly UTF-8 file with a few broken bytes competes as well.
	lossy := string(bytes.ToValidUTF8(raw, []byte("�")))
	best, bestName, bestScore = lossy, "utf-8", turkishScore(lossy)
	for _, c := range singleByteCandidates {
		s := c.decode(raw)
		// Strictly better only: ties keep the earlier (more Turkish) candidate.
		if score := turkishScore(s); score > bestScore {
			best, bestName, bestScore = s, c.name, score
		}
	}
	return best, bestName
}

// looksUTF16 detects BOM-less UTF-16 by the zero bytes every ASCII character leaves behind.
func looksUTF16(b []byte) (littleEndian, ok bool) {
	n := len(b)
	if n > 4096 {
		n = 4096
	}
	n &^= 1
	if n < 8 {
		return false, false
	}
	var evenZero, oddZero int
	for i := 0; i < n; i += 2 {
		if b[i] == 0 {
			evenZero++
		}
		if b[i+1] == 0 {
			oddZero++
		}
	}
	pairs := n / 2
	switch {
	case oddZero*10 > pairs*7 && evenZero*10 < pairs:
		return true, true
	case evenZero*10 > pairs*7 && oddZero*10 < pairs:
		return false, true
	}
	return false, false
}

func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			units[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(units))
}

// repairMojibake reverses UTF-8 text that was decoded as Windows-1254/1252 and saved again as UTF-8.
func repairMojibake(s string) (string, bool) {
	if mojibakeCount(s) == 0 {
		return "", false
	}
	for _, cm := range []*charmap.Charmap{charmap.Windows1254, charmap.Windows1252} {
		b, err := cm.NewEncoder().Bytes([]byte(s))
		if err != nil || !utf8.Valid(b) {
			continue
		}
		if fixed := string(b); turkishScore(fixed) > turkishScore(s) {
			return fixed, true
		}
	}
	return "", false
}

// mojibakeCount counts lead characters of UTF-8 sequences shown as single bytes, e.g. "Ã§" (ç) or "Ä±" (ı).
func mojibakeCount(s string) int {
	n := 0
	prev := rune(0)
	for _, r := range s {
		if (prev == 'Ã' || prev == 'Ä' || prev == 'Å' || prev == 'Â') && r >= 0x80 {
			n++
		}
		prev = r
	}
	return n
}

// turkishScore rates how plausible decoded text is: Turkish letters score up, characters a wrong charset produces
// (Icelandic ð ý þ for ğ ı ş, C1 controls, replacement characters, mojibake pairs) score down.
func turkishScore(s string) int {
	score := 0
	for _, r := range s {
		switch {
		case strings.ContainsRune("şŞğĞıİ", r):
			score += 3
		case strings.ContainsRune("çÇöÖüÜ", r):
			score++
		case strings.ContainsRune("ðÐýÝþÞ", r):
			score -= 3
		case r == utf8.RuneError:
			score -= 5
		case r >= 0x80 && r <= 0x9F:
			score -= 5
		}
	}
	return score - 4*mojibakeCount(s)
}
