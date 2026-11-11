package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"
)

// The rows cache is kept in a file, so after a restart the first request is answered from it
// (fresh or stale, see For) instead of waiting seconds for a new computation.

const persistFile = "recommendations-cache.json"

type persistedRows struct {
	Profile uint      `json:"profile"`
	Lang    string    `json:"lang"`
	Expires time.Time `json:"expires"`
	Resp    *Response `json:"resp"`
}

// Persist loads the rows saved in dir and saves the cache there after every change until ctx
// ends. Called once at startup, before requests are served.
func (s *Service) Persist(ctx context.Context, dir string) {
	path := filepath.Join(dir, persistFile)
	if n, err := s.loadRows(path); err != nil {
		log.Printf("recommendations: load %s: %v", path, err)
	} else if n > 0 {
		log.Printf("recommendations: loaded rows of %d profile languages", n)
	}
	s.saveReq = make(chan struct{}, 1)
	go s.saveLoop(ctx, path)
}

func (s *Service) loadRows(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var saved []persistedRows
	if err := json.Unmarshal(raw, &saved); err != nil {
		return 0, err
	}
	now := s.now()
	n := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range saved {
		if p.Resp == nil || len(p.Resp.Rows) == 0 || !now.Before(p.Expires.Add(staleFor)) {
			continue
		}
		s.cache[cacheKey{p.Profile, p.Lang}] = cached{resp: p.Resp, expires: p.Expires}
		n++
	}
	return n, nil
}

// requestSave asks the save loop to write the cache; it never blocks.
func (s *Service) requestSave() {
	if s.saveReq == nil {
		return
	}
	select {
	case s.saveReq <- struct{}{}:
	default:
	}
}

func (s *Service) saveLoop(ctx context.Context, path string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.saveReq:
			if err := s.saveRows(path); err != nil {
				log.Printf("recommendations: save %s: %v", path, err)
			}
		}
	}
}

func (s *Service) saveRows(path string) error {
	now := s.now()
	s.mu.Lock()
	saved := make([]persistedRows, 0, len(s.cache))
	for k, c := range s.cache {
		if now.Before(c.expires.Add(staleFor)) {
			saved = append(saved, persistedRows{Profile: k.profile, Lang: k.lang, Expires: c.expires, Resp: c.resp})
		}
	}
	s.mu.Unlock()

	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	// Write and rename, so a crash never leaves half a file
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
