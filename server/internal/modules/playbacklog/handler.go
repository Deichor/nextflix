package playbacklog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type batch struct {
	SID     string  `json:"sid"`
	Seq     int     `json:"seq"`
	Client  string  `json:"client"`
	App     string  `json:"app"` // the player's version; older players send none
	Ctx     *ctx    `json:"ctx"`
	Dropped int     `json:"dropped"`
	Events  []event `json:"events"`
}

type ctx struct {
	Type    string  `json:"type"`
	TmdbID  int     `json:"tmdb"`
	Season  int     `json:"s"`
	Episode int     `json:"e"`
	Title   string  `json:"title"`
	Src     string  `json:"src"`
	Mode    string  `json:"mode"`
	Debrid  bool    `json:"debrid"`
	Quality string  `json:"q"`
	Dur     float64 `json:"dur"`
}

type event struct {
	T int64          `json:"t"`
	K string         `json:"k"`
	P float64        `json:"p"`
	X map[string]any `json:"x"`
}

var (
	sidRe   = regexp.MustCompile(`^[a-z0-9]{4,16}$`)
	kindRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	fieldRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
	tokenRe = regexp.MustCompile(`[^A-Za-z0-9._:/+-]`)
)

// POST /api/playback-log (JSON body, any content type; text/plain avoids a CORS preflight)
func (s *Service) receive(c *gin.Context) {
	ip := c.RemoteIP()
	if !s.allowIP(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many playback logs"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body over 32 KB"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "unreadable body"})
		return
	}
	var b batch
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&b); err != nil || !sidRe.MatchString(b.SID) || len(b.Events) > maxEvents {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playback log"})
		return
	}
	if !s.allow(ip, b.SID) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many playback logs"})
		return
	}
	lines := format(b)
	if s.files != nil {
		// The full batch goes to the file; stdout only gets the header line
		log.Printf("playback: %s", lines[0])
		s.files.write(s.now(), lines)
	} else {
		for _, line := range lines {
			log.Printf("playback: %s", line)
		}
	}
	c.Status(http.StatusNoContent)
}

// format turns a batch into one header line and one line per valid event.
func format(b batch) []string {
	id := b.SID + " " + token(b.Client, 8)
	head := fmt.Sprintf("%s #%d", id, b.Seq)
	if x := b.Ctx; x != nil {
		head += fmt.Sprintf(" %s:%d", token(x.Type, 8), x.TmdbID)
		if x.Season > 0 || x.Episode > 0 {
			head += fmt.Sprintf(" S%02dE%02d", x.Season, x.Episode)
		}
		head += fmt.Sprintf(" %s src=%s %s q=%s debrid=%t dur=%.0f",
			quote(x.Title, 50), token(x.Src, 24), token(x.Mode, 8), token(x.Quality, 8), x.Debrid, x.Dur)
	}
	skipped := 0
	lines := []string{""}
	for _, e := range b.Events {
		if !kindRe.MatchString(e.K) || len(e.X) > 16 {
			skipped++
			continue
		}
		at := time.UnixMilli(e.T).Format("15:04:05.000")
		lines = append(lines, fmt.Sprintf("%s %s pos=%s %s%s", id, at, num(e.P), e.K, fields(e.X)))
	}
	head += fmt.Sprintf(" events=%d", len(b.Events)-skipped)
	if b.Dropped > 0 || skipped > 0 {
		head += fmt.Sprintf(" dropped=%d invalid=%d", max(b.Dropped, 0), skipped)
	}
	if b.App != "" {
		head += " app=" + token(b.App, 16)
	}
	lines[0] = head
	return lines
}

func fields(x map[string]any) string {
	keys := make([]string, 0, len(x))
	for k := range x {
		if fieldRe.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		var v string
		switch t := x[k].(type) {
		case float64:
			v = num(t)
		case bool:
			v = strconv.FormatBool(t)
		case string:
			v = token(t, 40)
		default:
			continue
		}
		sb.WriteString(" " + k + "=" + v)
	}
	return sb.String()
}

func num(f float64) string {
	return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64)
}

// token keeps a short value that cannot break the log line.
func token(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	s = tokenRe.ReplaceAllString(s, "_")
	if s == "" {
		return "-"
	}
	return s
}

func quote(s string, n int) string {
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return strconv.Quote(s)
}
