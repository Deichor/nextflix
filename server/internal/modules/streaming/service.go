package streaming

import (
	"context"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type Config struct {
	TorrServerURL string
	JackettURL    string
	JackettAPIKey string
	// TMDB gives resolve the expected runtime of a title (nil = only the client's runtime)
	TMDB TMDBClient
	// Availability keeps resolve from searching titles that are not out yet (nil = never)
	Availability AvailabilityChecker
	// BitsearchURL enables Bitsearch as an extra search source ("" = off, e.g. in tests)
	BitsearchURL string
	// FlareSolverrURL is used when Bitsearch answers with a Cloudflare challenge
	FlareSolverrURL string
	// TorBoxURL is the TorBox API base ("" = https://api.torbox.app/v1/api; tests use a fake)
	TorBoxURL string
}

type Service struct {
	db  *gorm.DB
	cfg Config

	probeCache   sync.Map // "hash:index" -> *tsProbe
	resolveCache sync.Map // resolve query key -> tsCacheEntry (in front of stream_sources)
	jackettBase  sync.Map // "base" -> last Jackett URL that answered
	runtimes     sync.Map // "movie:id" / "tv:id:s:e" -> tsRuntimeEntry
	titles       sync.Map // "movie:id" / "tv:id" -> English TMDB title
	imdbTitles   sync.Map // IMDb id -> tsIMDbTitle
	seasonEps    sync.Map // TMDB tv id -> tsSeasonEpsEntry (episodes per season)
	bitsearch    *bitsearch
	debrid       *tsDebrid // TorBox (debrid.go)

	probeMu sync.Mutex
	probing map[string]*tsProbeCall // "hash:index" -> running ffprobe
	// Tests: replace ffprobe, and the search of a draining player's sidegrade (findSource)
	probeFn      func(ctx context.Context, hash string, index int) (*tsProbe, error)
	sideSearchFn func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error)

	flights       tsFlightGroup // running resolve jobs, shared by prefetch and play
	failedAt      sync.Map      // resolve query key -> time.Time of the last failed job
	prefetchSlots chan struct{} // limits background prefetch jobs

	upgrades     sync.Map      // resolve query key -> *tsUpgrade
	upgradeSlots chan struct{} // titles warmed for an upgrade at once
	warmRounds   chan struct{} // background warm-up measurements at once

	// Subtitle alternatives (subalt.go): resolve base key -> *tsSubAlt, one scan at a time
	subAlts      sync.Map
	subAltSlot   chan struct{}
	subAltScanFn func(a *tsSubAlt, q tsResolveQuery, current string, curQ int, lang string) // tests

	indexerLists sync.Map // Jackett URL -> tsIndexerList
	indexerTimes sync.Map // indexer id -> time.Duration, moving average of its answer time

	// Warm-up bookkeeping (warm.go)
	warmMu      sync.Mutex
	warms       map[string]*tsWarm   // resolve key -> live warm state
	playing     map[string]int       // hash -> running play streams
	playStart   map[string]time.Time // hash -> start of its latest play stream (quiet.go)
	playFlowing map[string]bool      // hash -> its latest play stream sent its first bytes
	starting    map[string]int       // hash -> running player probes (its stream is about to start)
	playEndAt   map[string]time.Time // hash -> end of its last play stream (priority.go)
	lastUse     map[string]time.Time // hash -> last request for it
	// Stream guard (guard.go): no drop touches a torrent a player uses
	streamEnd   map[string]time.Time            // hash -> end of its last play stream (kept streamKeep)
	seeking     map[string]int                  // hash -> running seekpoint requests
	dropLater   map[string]bool                 // drops the guard vetoed, retried by the janitor
	hashTitles  map[string]map[string]time.Time // hash -> titles (keys without caps) that held it
	streamKeep  time.Duration                   // a stream that ended this recently still guards its torrent
	janitorOnce sync.Once
	dropFn      func(hash string) // tests: replaces the TorrServer drop
	pingFn      func(hash string) // tests: replaces the TorrServer stat ping
	saveFn      func(hash string) // tests: replaces saving a paused torrent to TorrServer's DB
	// Playback priority timings (priority.go), per service so tests can shorten them
	playGrace, playIntent, pauseSettle time.Duration
}

func NewService(db *gorm.DB, cfg Config) *Service {
	s := &Service{
		db: db, cfg: cfg,
		prefetchSlots: make(chan struct{}, tsPrefetchMax),
		upgradeSlots:  make(chan struct{}, tsUpgradeMax),
		warmRounds:    make(chan struct{}, tsUpgradeRounds),
		subAltSlot:    make(chan struct{}, 1),
		warms:         map[string]*tsWarm{},
		playing:       map[string]int{},
		playStart:     map[string]time.Time{},
		playFlowing:   map[string]bool{},
		starting:      map[string]int{},
		probing:       map[string]*tsProbeCall{},
		lastUse:       map[string]time.Time{},
		playEndAt:     map[string]time.Time{},
		streamEnd:     map[string]time.Time{},
		seeking:       map[string]int{},
		dropLater:     map[string]bool{},
		hashTitles:    map[string]map[string]time.Time{},
		streamKeep:    tsStreamKeep,
		playGrace:     tsPlayGrace,
		playIntent:    tsPlayIntent,
		pauseSettle:   tsReleaseSettle,
	}
	s.debrid = newDebrid(s, cfg.TorBoxURL)
	if cfg.BitsearchURL != "" {
		s.bitsearch = newBitsearch(cfg.BitsearchURL, cfg.FlareSolverrURL)
	}
	return s
}

func (s *Service) torrServerURL() string {
	if u := strings.TrimSpace(s.cfg.TorrServerURL); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return "http://torrserver:8090"
}

// bitsearchEnabled reads the indexer settings toggle (on when the settings cannot be read).
func (s *Service) bitsearchEnabled() bool {
	if s.db == nil {
		return true
	}
	st, err := loadSettings(s.db)
	return err != nil || !st.BitsearchDisabled
}
