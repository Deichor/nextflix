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
)

// fileLog keeps the playback lines in one file per day (dir/2026-10-10.log), so they outlive
// container rebuilds. Files older than keepDays are removed.
type fileLog struct {
	dir    string
	mu     sync.Mutex
	day    string
	pruned string
}

func newFileLog(dir string) *fileLog {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("playback: no log files in %s: %v", dir, err)
		return nil
	}
	return &fileLog{dir: dir}
}

func (f *fileLog) write(now time.Time, lines []string) {
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
	path := filepath.Join(f.dir, day+".log")
	if st, err := os.Stat(path); err == nil && st.Size() > maxDayFile {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	stamp := now.Format("2006-01-02 15:04:05 ")
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(stamp + l + "\n")
	}
	_, _ = file.WriteString(sb.String())
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
