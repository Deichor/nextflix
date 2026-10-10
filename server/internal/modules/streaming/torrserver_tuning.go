package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Minimum TorrServer settings for smooth streaming. Values the user raised further are kept.
// The read-ahead is CacheSize * ReaderReadAHead %: 2 GB * 95 % = 1.9 GB, about 8 minutes of a
// 4K WEB-DL (4 MB/s) buffered ahead of the player. PreloadCache only applies to TorrServer's
// own preload, which Nextflix does not use, so it is left alone. UseDisk stays as it is (off:
// RAM cache).
// TorrServer prioritizes at most ConnectionsLimit / readers missing pieces ahead of a reader
// (cache.go setLoadPriority), so 200 pieces of 2-16 MB bound how far ahead the playing file
// downloads as much as the cache does.
var tsTuningMin = map[string]int64{
	"CacheSize":        2 << 30, // RAM cache per active torrent (default 64 MB)
	"ReaderReadAHead":  95,      // percent of the cache read ahead of the player (5-100)
	"ConnectionsLimit": 200,     // peers per torrent (default 25), also the read-ahead window (above)
}

// TorrServer settings that must have exactly this value.
var tsTuningExact = map[string]int64{
	"PeersListenPort": 41413, // fixed BitTorrent port, so inbound peers can be forwarded to it (0 = random)
	// Seconds a torrent without readers stays open. Nextflix drops and pings its torrents
	// itself (warm.go), so this is only the safety net for torrents it lost track of.
	"TorrentDisconnectTimeout": 45,
}

// TorrServer switches that must have this value: DHT, PEX and UPnP find peers, uploading keeps
// them unchoking us, and the responsive reader hands out data before its piece is verified.
var tsTuningBool = map[string]bool{
	"DisableDHT":     false,
	"DisablePEX":     false,
	"DisableUPNP":    false,
	"DisableUpload":  false,
	"ResponsiveMode": true,
}

var tuneOnce sync.Once

// tsTuneRetryStep: the wait after tuning attempt n is n times this.
var tsTuneRetryStep = 5 * time.Second

// startTorrServerTuning applies tsTuningMin in the background once per process.
// TorrServer may still be starting, so it retries for a while and only logs failures.
func (s *Service) startTorrServerTuning() {
	tuneOnce.Do(func() { go s.tuneTorrServerRetrying() })
}

// tuneTorrServerRetrying tries the tuning up to 8 times, then starts the DB prune either way.
func (s *Service) tuneTorrServerRetrying() {
	defer s.startDBPrune()
	for attempt := 1; attempt <= 8; attempt++ {
		changed, err := s.tuneTorrServer(context.Background())
		if err == nil {
			if changed {
				log.Printf("torrserver: streaming settings applied %v %v %v", tsTuningMin, tsTuningExact, tsTuningBool)
			}
			return
		}
		log.Printf("torrserver tuning (attempt %d): %v", attempt, err)
		time.Sleep(time.Duration(attempt) * tsTuneRetryStep)
	}
}

// tuneTorrServer raises the settings below tsTuningMin. TorrServer replaces the whole
// settings object and restarts its torrent client on set, so it only writes when needed.
func (s *Service) tuneTorrServer(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cur, err := s.torrServerSettings(ctx, map[string]any{"action": "get"})
	if err != nil {
		return false, err
	}
	changed := false
	value := func(k string) (int64, bool) {
		n, _ := cur[k].(json.Number)
		v, err := strconv.ParseInt(n.String(), 10, 64)
		return v, err == nil
	}
	for k, min := range tsTuningMin {
		if v, ok := value(k); ok && v >= min {
			continue
		}
		cur[k] = min
		changed = true
	}
	for k, want := range tsTuningExact {
		if v, ok := value(k); ok && v == want {
			continue
		}
		cur[k] = want
		changed = true
	}
	for k, want := range tsTuningBool {
		if v, ok := cur[k].(bool); !ok || v == want { // unknown to this TorrServer, or right
			continue
		}
		cur[k] = want
		changed = true
	}
	if !changed {
		return false, nil
	}
	_, err = s.torrServerSettings(ctx, map[string]any{"action": "set", "sets": cur})
	return true, err
}

func (s *Service) torrServerSettings(ctx context.Context, body map[string]any) (map[string]any, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.torrServerURL()+"/settings", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("settings %v: HTTP %d", body["action"], resp.StatusCode)
	}
	if body["action"] != "get" {
		return nil, nil
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty settings")
	}
	return out, nil
}

// startDBPrune removes old saved torrents now and every 6 hours.
func (s *Service) startDBPrune() {
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			s.pruneTorrServerDB(ctx)
			cancel()
			time.Sleep(6 * time.Hour)
		}
	}()
}
