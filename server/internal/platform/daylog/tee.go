package daylog

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const teeQueue = 1024

type teeLine struct {
	at   time.Time
	text string
}

// Tee is an io.Writer for the standard logger (beside stderr) that keeps the lines keep picks
// in a Writer. Lines are queued and written by one goroutine, so a slow disk never holds up
// logging and the Writer's own messages cannot deadlock the logger; a full queue drops lines
// (counted in the file).
type Tee struct {
	keep    func(line string) bool
	q       chan teeLine
	dropped atomic.Int64
}

func NewTee(w *Writer, keep func(line string) bool) *Tee {
	t := &Tee{keep: keep, q: make(chan teeLine, teeQueue)}
	if w != nil {
		go t.drain(w)
	}
	return t
}

// Write takes one log entry (the logger writes each in one call); the file gets it without the
// logger's date prefix.
func (t *Tee) Write(p []byte) (int, error) {
	s := strings.TrimRight(stripStamp(string(p)), "\n")
	if !t.keep(s) {
		return len(p), nil
	}
	select {
	case t.q <- teeLine{time.Now(), strings.ReplaceAll(s, "\n", " ")}:
	default:
		t.dropped.Add(1)
	}
	return len(p), nil
}

func (t *Tee) drain(w *Writer) {
	for l := range t.q {
		if n := t.dropped.Swap(0); n > 0 {
			w.Write(l.at, "", []string{fmt.Sprintf("daylog: %d lines dropped (queue full)", n)})
		}
		w.Write(l.at, "", []string{l.text})
	}
}

// stripStamp drops the standard logger's "2006/01/02 15:04:05 " prefix.
func stripStamp(s string) string {
	const layout = "2006/01/02 15:04:05 "
	if len(s) < len(layout) {
		return s
	}
	for i := range len(layout) {
		c, l := s[i], layout[i]
		if l >= '0' && l <= '9' {
			if c < '0' || c > '9' {
				return s
			}
		} else if c != l {
			return s
		}
	}
	return s[len(layout):]
}
