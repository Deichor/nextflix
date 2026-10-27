// Package daylog keeps log lines in one file per UTC day (dir/2026-10-10.log), so they outlive
// container rebuilds and docker's log rotation. Files older than Keep days are removed; a day's
// file and each key's share of it (a client IP) are capped.
package daylog

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Keep   int   // days kept
	DayCap int64 // bytes a day's file may reach
	KeyCap int64 // bytes one key may write a day (0: no cap)
}

type Writer struct {
	dir  string
	name string // names the log in its own messages ("playback")
	opt  Options

	mu     sync.Mutex
	pruned string
	day    string           // the day used and capped count for
	used   map[string]int64 // bytes written today per key
	capped map[string]bool  // "" = the day's file is full; else the key's budget is used up
}

// New keeps files under dir; nil when dir is empty or cannot be made (a nil Writer writes nothing).
func New(dir, name string, opt Options) *Writer {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("%s: no log files in %s: %v", name, dir, err)
		return nil
	}
	return &Writer{dir: dir, name: name, opt: opt}
}

// Write appends lines to the day's file of now, each stamped with now (UTC, to the second).
func (w *Writer) Write(now time.Time, key string, lines []string) {
	if w == nil || len(lines) == 0 {
		return
	}
	now = now.UTC()
	w.mu.Lock()
	defer w.mu.Unlock()
	day := now.Format("2006-01-02")
	if w.pruned != day {
		w.pruned = day
		w.prune(now)
	}
	if w.day != day {
		w.day, w.used, w.capped = day, map[string]int64{}, map[string]bool{}
	}
	path := filepath.Join(w.dir, day+".log")
	if st, err := os.Stat(path); err == nil && st.Size() > w.opt.DayCap {
		if !w.capped[""] {
			w.capped[""] = true
			log.Printf("%s: %s is over %d MB, dropping the rest of today's logs", w.name, filepath.Base(path), w.opt.DayCap>>20)
		}
		return
	}
	stamp := now.Format("2006-01-02 15:04:05 ")
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(stamp + l + "\n")
	}
	if w.opt.KeyCap > 0 && w.used[key]+int64(sb.Len()) > w.opt.KeyCap {
		if !w.capped[key] {
			w.capped[key] = true
			log.Printf("%s: %s wrote %d MB today, dropping its logs until tomorrow", w.name, key, w.opt.KeyCap>>20)
		}
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.WriteString(sb.String()); err == nil {
		w.used[key] += int64(sb.Len())
	}
}

func (w *Writer) prune(now time.Time) {
	names, _ := filepath.Glob(filepath.Join(w.dir, "*.log"))
	oldest := now.AddDate(0, 0, -w.opt.Keep).Format("2006-01-02")
	for _, n := range names {
		if day := strings.TrimSuffix(filepath.Base(n), ".log"); day < oldest {
			_ = os.Remove(n)
		}
	}
}
