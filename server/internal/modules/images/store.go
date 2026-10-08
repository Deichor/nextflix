package images

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tempPrefix = ".dl-"
	// Hits refresh the mtime (the LRU clock) at most this often, to keep reads cheap.
	touchEvery = time.Hour
	staleTemp  = time.Hour
)

var errTooLarge = fmt.Errorf("image larger than %d bytes", maxImageBytes)

// store is the on-disk cache: <dir>/<size>/<file>. Eviction is LRU-ish by mtime.
type store struct {
	dir      string
	maxBytes int64
	total    atomic.Int64 // approximate size, exact after each cleanup
	cleaning sync.Mutex
	wake     chan struct{}
}

func newStore(dir string, maxBytes int64) (*store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &store{dir: dir, maxBytes: maxBytes, wake: make(chan struct{}, 1)}, nil
}

func (s *store) path(k key) string {
	return filepath.Join(s.dir, k.size, k.file)
}

// stat returns the cached file's info, refreshing its mtime when it is old.
func (s *store) stat(k key) (os.FileInfo, bool) {
	p := s.path(k)
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	if time.Since(fi.ModTime()) > touchEvery {
		now := time.Now()
		if os.Chtimes(p, now, now) == nil {
			if fresh, err := os.Stat(p); err == nil {
				fi = fresh
			}
		}
	}
	return fi, true
}

// write stores r atomically (temp file + rename), so readers never see partial images.
// want is the expected length (-1 if unknown).
func (s *store) write(k key, r io.Reader, want int64) error {
	dir := filepath.Join(s.dir, k.size)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(r, maxImageBytes+1))
	if err != nil {
		return err
	}
	if n > maxImageBytes {
		return errTooLarge
	}
	if n == 0 || (want >= 0 && n != want) {
		return fmt.Errorf("short image body: %d of %d bytes", n, want)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path(k)); err != nil {
		return err
	}
	ok = true

	if s.total.Add(n) > s.maxBytes {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

type cachedFile struct {
	path  string
	size  int64
	mtime time.Time
}

// cleanup recounts the cache, drops stale temp files and, when over the cap,
// deletes the least recently used files until the cache is at 90% of the cap.
func (s *store) cleanup() (removed int, total int64, err error) {
	s.cleaning.Lock()
	defer s.cleaning.Unlock()

	var files []cachedFile
	err = filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), tempPrefix) {
			if time.Since(info.ModTime()) > staleTemp {
				os.Remove(p)
			}
			return nil
		}
		files = append(files, cachedFile{path: p, size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, total, err
	}

	if total > s.maxBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
		target := s.maxBytes / 10 * 9
		for _, f := range files {
			if total <= target {
				break
			}
			if os.Remove(f.path) == nil {
				total -= f.size
				removed++
			}
		}
	}
	s.total.Store(total)
	return removed, total, nil
}
