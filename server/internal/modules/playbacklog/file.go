package playbacklog

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	keepDays   = 14
	maxDayFile = 50 << 20
	// What one client IP may write a day (a normal day is well under 1 MB), so one device
	// cannot fill the day's file and push out everyone else's logs.
	maxIPDay = 10 << 20
)

// fileLog keeps the playback lines in one file per day (dir/2026-10-10.log), so they outlive
// container rebuilds. Files older than keepDays are removed.
type fileLog struct {
	dir    string
	mu     sync.Mutex
	pruned string

	dayCap, ipCap int64
	day           string           // the day used and capped count for
	used          map[string]int64 // bytes written today per client IP
	capped        map[string]bool  // "" = the day's file is full; else the IP's budget is used up
}

func newFileLog(dir string) *fileLog {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("playback: no log files in %s: %v", dir, err)
		return nil
	}
	return &fileLog{dir: dir, dayCap: maxDayFile, ipCap: maxIPDay}
}

func (f *fileLog) write(now time.Time, ip string, lines []string) {
	if f == nil || len(lines) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	day := now.Format("2006-01-02")
	if f.pruned != day {
		f.pruned = day
		f.prune(now)
	}
	if f.day != day {
		f.day, f.used, f.capped = day, map[string]int64{}, map[string]bool{}
	}
	path := filepath.Join(f.dir, day+".log")
	if st, err := os.Stat(path); err == nil && st.Size() > f.dayCap {
		if !f.capped[""] {
			f.capped[""] = true
			log.Printf("playback: %s is over %d MB, dropping the rest of today's logs", filepath.Base(path), f.dayCap>>20)
		}
		return
	}
	stamp := now.Format("2006-01-02 15:04:05 ")
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(stamp + l + "\n")
	}
	if f.used[ip]+int64(sb.Len()) > f.ipCap {
		if !f.capped[ip] {
			f.capped[ip] = true
			log.Printf("playback: %s wrote %d MB today, dropping its logs until tomorrow", ip, f.ipCap>>20)
		}
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.WriteString(sb.String()); err == nil {
		f.used[ip] += int64(sb.Len())
	}
}

func (f *fileLog) prune(now time.Time) {
	names, _ := filepath.Glob(filepath.Join(f.dir, "*.log"))
	oldest := now.AddDate(0, 0, -keepDays).Format("2006-01-02")
	for _, n := range names {
		if day := strings.TrimSuffix(filepath.Base(n), ".log"); day < oldest {
			_ = os.Remove(n)
		}
	}
}
