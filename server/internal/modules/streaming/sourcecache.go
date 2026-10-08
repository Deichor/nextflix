package streaming

import (
	"encoding/json"
	"log"
	"time"
)

// Resolved sources are kept in memory and in the stream_sources table, so a title that was
// played or prefetched once starts instantly, also after a restart.

const tsSourceTTL = 7 * 24 * time.Hour

type tsCacheEntry struct {
	res     tsResolveResult
	expires time.Time
}

// cachedSource returns a still valid resolved source for key.
func (s *Service) cachedSource(key string) (*tsResolveResult, bool) {
	now := time.Now()
	if v, ok := s.resolveCache.Load(key); ok {
		entry := v.(tsCacheEntry)
		if now.Before(entry.expires) {
			res := entry.res
			s.applyProbedQuality(&res) // a probe may have read the file since it was stored
			return &res, true
		}
		s.resolveCache.Delete(key)
	}
	if s.db == nil {
		return nil, false
	}

	row, err := loadSource(s.db, key)
	if err != nil {
		log.Printf("source cache: load %q: %v", key, err)
		return nil, false
	}
	if row == nil {
		return nil, false
	}
	expires := row.ResolvedAt.Add(tsSourceTTL)
	var res tsResolveResult
	if !now.Before(expires) || json.Unmarshal([]byte(row.Result), &res) != nil || !tsHashPattern.MatchString(res.Hash) {
		_ = deleteSource(s.db, key)
		return nil, false
	}
	s.resolveCache.Store(key, tsCacheEntry{res: res, expires: expires})
	s.debridOwns(&res) // a debrid source plays from TorBox again (after a restart)
	s.applyProbedQuality(&res)
	return &res, true
}

// storeSource remembers a resolved source and drops expired rows.
func (s *Service) storeSource(key string, res tsResolveResult) {
	now := time.Now()
	res.Cached = false
	s.resolveCache.Store(key, tsCacheEntry{res: res, expires: now.Add(tsSourceTTL)})
	s.noteTitle(key, res.Hash)
	s.persistTorrent(res)
	if s.db == nil {
		return
	}
	data, err := json.Marshal(res)
	if err != nil {
		return
	}
	if err := saveSource(s.db, &StreamSource{SourceKey: key, Result: string(data), ResolvedAt: now}); err != nil {
		log.Printf("source cache: save %q: %v", key, err)
	}
	if err := pruneSources(s.db, now.Add(-tsSourceTTL)); err != nil {
		log.Printf("source cache: prune: %v", err)
	}
}

// forgetSource drops a cached source (used by ?refresh=1 when the cached torrent did not play).
func (s *Service) forgetSource(key string) {
	s.resolveCache.Delete(key)
	if s.db != nil {
		if err := deleteSource(s.db, key); err != nil {
			log.Printf("source cache: delete %q: %v", key, err)
		}
	}
}
