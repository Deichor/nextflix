package subtitles

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	listWait      = 6 * time.Second  // GET /api/subtitles answers with what is ready by then
	listTTL       = 30 * time.Minute // provider results in memory
	listErrTTL    = 3 * time.Minute  // failed provider calls are not retried before this
	providerLimit = 45 * time.Second // a provider call keeps filling the cache after the answer
	fetchLimit    = 60 * time.Second
	hashLimit     = 20 * time.Second
)

// A player waits this long for a whole extraction of an embedded track; after that (or when it
// fails) the track is read live while the extraction goes on (tests shorten it).
var embeddedWait = 20 * time.Second

// IMDbLookup returns the IMDb id ("tt0903747") of a movie or TV show (mediaType "movie" | "tv").
type IMDbLookup func(mediaType string, tmdbID int) (string, error)

// Config wires the service. Empty URLs use the public defaults; tests point them to fakes.
type Config struct {
	TorrServerURL    string
	CacheDir         string // converted files and scraped pages (default /app/data/subtitles)
	FlareSolverrURL  string // e.g. http://flaresolverr:8191/v1, used only on a Cloudflare challenge
	IMDb             IMDbLookup
	OpenSubtitlesURL string
	SubDLAPIURL      string
	SubDLDownloadURL string
	SubSourceAPIURL  string
	TurkceAltyaziURL string
	Subf2mURL        string
	YIFYURL          string
	Titles           TitleLookup // names and year for subf2m (nil = subf2m off)
	HTTPClient       *http.Client
	// Debrid maps files that play from TorBox (nil = TorrServer only)
	Debrid *DebridSource
	// LiveTiming: the play streams feed their audio (OpenSpeech), so timing results stay
	// provisional and improve while a file plays (sync_live.go)
	LiveTiming bool
	// AIDir keeps the AI translations for good (outside the swept cache; default /app/data/ai-subtitles)
	AIDir string
	// NextEpisode is the episode after season/episode (nil = no AI prefetch of the next episode)
	NextEpisode func(tvID, season, episode int) (int, int, bool)
	// LineGate holds the timing's window reads while a stream starts (nil = no wait)
	LineGate LineGate
	// ReadRoom says how fast pre-timing may read beside the playing streams (nil = always, no limit)
	ReadRoom ReadRoom
}

// Service lists subtitles from the torrent itself and online providers and serves them as WebVTT.
type Service struct {
	cfg      Config
	settings *settingsRepo
	files    diskCache
	lists    *listCache
	flights  flightGroup
	http     *http.Client
	wait     time.Duration

	os  *openSubtitles
	sdl *subDL
	ss  *subSource
	ta  *turkceAltyazi
	sf  *subf2m
	yts *yifySubs

	hashMu sync.Mutex
	hashes map[string]string // "<hash>|<index>" -> moviehash

	live *liveSubs // embedded tracks of big files, read while they play
	sync *syncer   // timing of provider subtitles for the playing file
	ai   *aiTranslator

	refreshMu sync.Mutex
	refreshed map[string]time.Time // list base key -> last refresh=1 that skipped the cache

	extract func(ctx context.Context, torrServerURL, hash string, fileIndex, streamIndex int) ([]byte, error)
}

// refreshEvery: a title's provider answers are fetched again (refresh=1) at most this often.
const refreshEvery = 5 * time.Minute

func NewService(db *gorm.DB, cfg Config) *Service {
	if cfg.CacheDir == "" {
		cfg.CacheDir = "/app/data/subtitles"
	}
	if cfg.AIDir == "" {
		cfg.AIDir = "/app/data/ai-subtitles"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	files := diskCache{dir: cfg.CacheDir}
	api := newProviderClient(true, true, nil)
	s := &Service{
		cfg:       cfg,
		settings:  newSettingsRepo(db),
		files:     files,
		lists:     newListCache(500),
		http:      client,
		wait:      listWait,
		os:        newOpenSubtitles(cfg.OpenSubtitlesURL, api),
		sdl:       newSubDL(cfg.SubDLAPIURL, cfg.SubDLDownloadURL, api),
		ss:        newSubSource(cfg.SubSourceAPIURL, api),
		ta:        newTurkceAltyazi(cfg.TurkceAltyaziURL, cfg.FlareSolverrURL, files),
		sf:        newSubf2m(cfg.Subf2mURL, files),
		yts:       newYIFY(cfg.YIFYURL, files),
		hashes:    map[string]string{},
		refreshed: map[string]time.Time{},
		extract:   ExtractEmbeddedSub,
	}
	if cfg.Debrid != nil {
		debridHook.Store(cfg.Debrid)
	}
	s.live = newLiveSubs(cfg.TorrServerURL, func(key string, vtt []byte) {
		if err := s.files.write(SourceEmbedded, key, ".vtt", vtt); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
	})
	s.sync = newSyncer(s)
	s.ai = newAITranslator(db, cfg.AIDir)
	return s
}

// allowRefresh reports whether a refresh=1 list of base may skip the cached provider answers
// now (at most every refreshEvery), and records it.
func (s *Service) allowRefresh(base string) bool {
	now := time.Now()
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if now.Sub(s.refreshed[base]) < refreshEvery {
		return false
	}
	if len(s.refreshed) > 1000 {
		for k, at := range s.refreshed {
			if now.Sub(at) >= refreshEvery {
				delete(s.refreshed, k)
			}
		}
	}
	s.refreshed[base] = now
	return true
}

// Start sweeps the disk cache (at once, then every cacheSweepEvery) until ctx ends.
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(cacheSweepEvery)
		defer t.Stop()
		for {
			if n := s.files.sweep(cacheMaxAge, cacheMaxBytes); n > 0 {
				log.Printf("subtitles: cache sweep removed %d files", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// EnsureSettings seeds the settings row (Turkish first, turkcealtyazi.org on).
func (s *Service) EnsureSettings() error { return s.settings.ensure() }

func (s *Service) Settings() SettingsView {
	return s.aiView(s.settings.get().view())
}

func (s *Service) UpdateSettings(in SettingsInput) (SettingsView, error) {
	st, err := s.settings.load()
	if err != nil {
		return SettingsView{}, err // saving defaults now would wipe the stored keys
	}
	before := st
	in.apply(&st)
	if err := s.settings.save(st); err != nil {
		return SettingsView{}, err
	}
	// New keys or toggles change what providers return.
	if before.OpenSubtitlesAPIKey != st.OpenSubtitlesAPIKey || before.SubDLAPIKey != st.SubDLAPIKey ||
		before.SubSourceAPIKey != st.SubSourceAPIKey || before.TurkceAltyaziEnabled != st.TurkceAltyaziEnabled || before.OpenSubtitlesUsername != st.OpenSubtitlesUsername {
		s.lists.clear()
	}
	// The download quota belongs to the account (or the bare key): new credentials may have downloads left.
	if before.OpenSubtitlesAPIKey != st.OpenSubtitlesAPIKey || before.OpenSubtitlesUsername != st.OpenSubtitlesUsername ||
		before.OpenSubtitlesPassword != st.OpenSubtitlesPassword {
		s.os.clearQuota()
	}
	if before.AnthropicAPIKey != st.AnthropicAPIKey {
		s.ai.resetKeyErrors()
	}
	return s.aiView(st.view()), nil
}

// aiView adds the AI translation's usage and account error to v.
func (s *Service) aiView(v SettingsView) SettingsView {
	v.AI.Usage = s.ai.usage()
	if e, at := s.ai.accountError(); e != nil {
		v.AI.Error, v.AI.ErrorKind, v.AI.ErrorAt = e.Msg, e.Kind, at.UTC().Format(time.RFC3339)
	}
	return v
}

// providerResult is what one source delivered.
type providerResult struct {
	source string
	items  []candidate
	err    error
}

// List merges every source and ranks the result. Sources that are not ready within listWait are
// left out; they keep running and fill the cache for the next call.
func (s *Service) List(ctx context.Context, q Query) []Sub {
	return s.list(ctx, q, s.wait, nil)
}

// list is List with its wait; nil targets are the AI translation targets of the settings.
func (s *Service) list(ctx context.Context, q Query, wait time.Duration, targets []string) []Sub {
	st := s.settings.get()
	if len(q.Langs) == 0 {
		q.Langs = st.langs()
	}
	if targets == nil {
		targets = s.aiTargets(st, q)
	}
	if len(targets) > 0 && st.AITranslateEnabled && st.AnthropicAPIKey != "" {
		q.Langs = aiSearchLangs(q.Langs, targets)
	} else {
		targets = nil
	}
	type job struct {
		source string
		key    string
		run    func(context.Context) ([]candidate, error)
	}
	base := fmt.Sprintf("%s|%d|%d|%d|%s", q.MediaType, q.TmdbID, q.Season, q.Episode, strings.Join(q.Langs, ","))
	// Late uploads: a refresh asks the providers again (and turkcealtyazi's title page)
	fresh := q.Refresh && q.TmdbID > 0 && s.allowRefresh(base)
	if fresh {
		log.Printf("subtitles: refreshing the provider answers of %s", base)
	}
	var jobs []job
	if q.Hash != "" {
		jobs = append(jobs, job{SourceTorrent, "torrent|" + q.Hash, func(ctx context.Context) ([]candidate, error) {
			return s.torrentCandidates(ctx, q.Hash)
		}})
		if q.Index > 0 {
			jobs = append(jobs, job{SourceEmbedded, fmt.Sprintf("embedded|%s|%d", q.Hash, q.Index), func(ctx context.Context) ([]candidate, error) {
				return s.embeddedCandidates(ctx, q.Hash, q.Index)
			}})
		}
	}
	if q.TmdbID > 0 {
		if st.OpenSubtitlesAPIKey != "" {
			key := "os|" + base + "|" + q.Hash + "|" + strconv.Itoa(q.Index)
			jobs = append(jobs, job{SourceOpenSubtitles, key, func(ctx context.Context) ([]candidate, error) {
				return s.os.search(ctx, q, st, s.movieHash(ctx, q.Hash, q.Index))
			}})
		}
		if st.SubDLAPIKey != "" {
			jobs = append(jobs, job{SourceSubDL, "subdl|" + base, func(ctx context.Context) ([]candidate, error) {
				return s.sdl.search(ctx, q, st)
			}})
		}
		if st.SubSourceAPIKey != "" && s.cfg.IMDb != nil {
			jobs = append(jobs, job{SourceSubSource, "ss|" + base, func(ctx context.Context) ([]candidate, error) {
				imdb, err := s.cfg.IMDb(q.MediaType, q.TmdbID)
				if err != nil {
					return nil, fmt.Errorf("subsource: IMDb id lookup: %w", err)
				}
				return s.ss.search(ctx, q, st, imdb)
			}})
		}
		if st.TurkceAltyaziEnabled && s.cfg.IMDb != nil {
			jobs = append(jobs, job{SourceTurkceAltyazi, "ta|" + base, func(ctx context.Context) ([]candidate, error) {
				imdb, err := s.cfg.IMDb(q.MediaType, q.TmdbID)
				if err != nil || imdb == "" {
					if err == nil {
						return nil, nil // no IMDb id: nothing to find
					}
					return nil, fmt.Errorf("turkcealtyazi: IMDb id lookup: %w", err)
				}
				if fresh {
					ctx = withFreshPages(ctx)
				}
				return s.ta.search(ctx, q, imdb)
			}})
		}
		if st.Subf2mEnabled && s.cfg.Titles != nil {
			jobs = append(jobs, job{SourceSubf2m, "subf2m|" + base, func(ctx context.Context) ([]candidate, error) {
				if fresh {
					ctx = withFreshPages(ctx)
				}
				return s.subf2mSearch(ctx, q)
			}})
		}
		if st.YIFYEnabled && !q.isTV() && s.cfg.IMDb != nil {
			jobs = append(jobs, job{SourceYIFY, "yify|" + base, func(ctx context.Context) ([]candidate, error) {
				imdb, err := s.cfg.IMDb(q.MediaType, q.TmdbID)
				if err != nil {
					return nil, fmt.Errorf("yifysubtitles: IMDb id lookup: %w", err)
				}
				if fresh {
					ctx = withFreshPages(ctx)
				}
				return s.yts.search(ctx, q, imdb)
			}})
		}
	}

	results := make(chan providerResult, len(jobs))
	for _, j := range jobs {
		j := j
		provider := j.source != SourceTorrent && j.source != SourceEmbedded
		if items, ok := s.lists.get(j.key); ok && !(fresh && provider) {
			results <- providerResult{source: j.source, items: items}
			continue
		}
		call := s.flights.start(j.key, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), providerLimit)
			defer cancel()
			items, err := j.run(ctx)
			if err != nil {
				// A network failure is asked again on the next call; other failures wait listErrTTL
				temp := isTemporary(err)
				log.Printf("subtitles: %s: %v (temporary: %v)", j.source, err, temp)
				if !temp {
					s.lists.set(j.key, nil, listErrTTL)
				}
				return nil, err
			}
			s.lists.set(j.key, items, listTTL)
			return items, nil
		})
		go func() {
			<-call.done
			items, _ := call.val.([]candidate)
			results <- providerResult{source: j.source, items: items, err: call.err}
		}()
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	var all []candidate
	for range jobs {
		select {
		case r := <-results:
			all = append(all, r.items...)
		case <-timer.C:
			return s.addAI(s.rank(all, q), q, targets)
		case <-ctx.Done():
			return s.addAI(s.rank(all, q), q, targets)
		}
	}
	return s.addAI(s.rank(all, q), q, targets)
}

func (s *Service) rank(all []candidate, q Query) []Sub {
	all = s.dropMissing(all)
	file := q.Hash != "" && q.Index > 0
	made := map[string]bool{} // made for the playing file (hash match)
	if file {
		if q.FPS == 0 {
			q.FPS = s.sync.knownFPS(q.Hash, q.Index)
		}
		for i := range all {
			if all[i].Synced {
				made[all[i].ID] = true
			} else {
				all[i].AutoSync = s.sync.fitOf(all[i].ID, q.Hash, q.Index)
				all[i].Holes = s.sync.holesOf(all[i].ID, q.Hash, q.Index)
			}
		}
		if q.TmdbID > 0 {
			s.sync.noteTitle(q.Hash, q.Index, aiMeta{q.MediaType, q.TmdbID, q.Season, q.Episode})
		}
	}
	subs := rankCandidates(all, q)
	if !s.OpenSubtitlesQuotaUntil().IsZero() {
		subs = demoteQuota(subs)
	}
	if file {
		subs = s.sync.listed(subs, made, q.Hash, q.Index)
	}
	return subs
}

// OpenSubtitlesQuotaUntil is when OpenSubtitles' used-up download quota resets (zero while it is not).
func (s *Service) OpenSubtitlesQuotaUntil() time.Time { return s.os.quotaUntil() }

// demoteQuota marks the OpenSubtitles items (their downloads are used up) and moves them behind the
// other items of their language; the order is kept otherwise.
func demoteQuota(subs []Sub) []Sub {
	out := make([]Sub, 0, len(subs))
	for i := 0; i < len(subs); {
		j := i
		for j < len(subs) && subs[j].Language == subs[i].Language {
			j++
		}
		var blocked []Sub
		for _, it := range subs[i:j] {
			if it.Source == SourceOpenSubtitles {
				it.Quota = true
				blocked = append(blocked, it)
			} else {
				out = append(out, it)
			}
		}
		out = append(out, blocked...)
		i = j
	}
	return out
}

func (s *Service) torrentCandidates(ctx context.Context, hash string) ([]candidate, error) {
	subs, err := ListTorrentSubs(ctx, s.cfg.TorrServerURL, hash)
	if err != nil {
		return nil, err
	}
	out := make([]candidate, 0, len(subs))
	for _, t := range subs {
		name := t.Path
		if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
			name = name[i+1:]
		}
		out = append(out, candidate{Sub: Sub{
			ID:       encodeID(SourceTorrent, t.Language, fmt.Sprintf("%s|%d|%s", hash, t.FileID, t.Format)),
			Source:   SourceTorrent,
			Language: t.Language,
			Release:  name,
			HI:       t.HI,
			Forced:   t.Forced,
			Synced:   true,
		}, Releases: []string{t.Path}}) // the folder may name the episode (season packs)
	}
	return out, nil
}

func (s *Service) embeddedCandidates(ctx context.Context, hash string, index int) ([]candidate, error) {
	subs, err := ListEmbeddedSubs(ctx, s.cfg.TorrServerURL, hash, index)
	if err != nil {
		return nil, err
	}
	// The frame rate ranks the subtitles of later lists (the header is read already)
	go func() {
		pctx, cancel := context.WithTimeout(context.Background(), embeddedProbeTimeout)
		defer cancel()
		_, _ = s.sync.probeFile(pctx, hash, index, torrFileURL(s.cfg.TorrServerURL, hash, index))
	}()
	var out []candidate
	for _, e := range subs {
		if !e.Available {
			continue
		}
		lang := normLang(e.Language)
		out = append(out, candidate{Sub: Sub{
			ID:       encodeID(SourceEmbedded, lang, fmt.Sprintf("%s|%d|%d", hash, index, e.StreamIndex)),
			Source:   SourceEmbedded,
			Language: lang,
			Release:  firstNonEmpty(e.Title, strings.ToUpper(e.Codec)),
			HI:       e.HI,
			Forced:   e.Forced,
			Synced:   true,
			Live:     e.Live,
		}})
	}
	return out, nil
}

// FileAt is File for a player at position t (seconds of the file): an embedded track of a
// big file that is not mostly cached is read live from about t on (live.go), and partial
// tells that the answer will grow (it carries PartialNote).
func (s *Service) FileAt(ctx context.Context, id string, t float64) ([]byte, bool, error) {
	a, err := s.FileFor(ctx, id, t)
	return a.VTT, a.Partial, err
}

// FileFor is FileAt with the timing state: a subtitle listed for the playing file (its id
// names the file) is timed to it, see sync.go, and its holes are filled (holes_fill.go).
func (s *Service) FileFor(ctx context.Context, id string, t float64) (FileAnswer, error) {
	return s.fileFor(ctx, id, t, true)
}

// fileFor is FileFor; holes false serves a subtitle without its hole filling (a translation
// source, a reference).
func (s *Service) fileFor(ctx context.Context, id string, t float64, holes bool) (FileAnswer, error) {
	if source, lang, ref, err := decodeID(id); err == nil && source == SourceAI {
		return s.serveAI(ctx, lang, ref, t)
	}
	if inner, hash, index, ok := unwrapSyncID(id); ok {
		a, err := s.sync.serve(ctx, inner, hash, index, t)
		if err == nil {
			s.sync.observe(ctx, inner, hash, index, t, a)
		}
		if err != nil || !holes {
			return a, err
		}
		return s.withHoles(ctx, inner, hash, index, a, t), nil
	}
	vtt, partial, err := s.fileAt(ctx, id, t)
	return FileAnswer{VTT: vtt, Partial: partial}, err
}

// PreTime starts the timing of a subtitle listed for a file that does not play yet (the next
// episode, see syncer.pretime) and returns its timing state; ids that are not timed answer
// "original".
func (s *Service) PreTime(ctx context.Context, id string) (string, error) {
	if inner, hash, index, ok := unwrapSyncID(id); ok {
		return s.sync.pretime(ctx, inner, hash, index)
	}
	if _, _, _, err := decodeID(id); err != nil {
		return "", err
	}
	return syncStateOriginal, nil
}

func (s *Service) fileAt(ctx context.Context, id string, t float64) ([]byte, bool, error) {
	source, lang, ref, err := decodeID(id)
	if err != nil {
		return nil, false, err
	}
	key := source + "|" + ref
	if source == SourceEmbedded {
		if b, ok := s.files.read(source, key, ".vtt", 0); ok {
			return b, false, nil
		}
		hash, index, stream, err := embeddedRef(ref)
		if err != nil {
			return nil, false, err
		}
		if s.live.active(key) || !extractionPractical(ctx, s.cfg.TorrServerURL, hash, index) {
			vtt, partial := s.live.get(ctx, key, hash, index, stream, math.Max(t, 0))
			return vtt, partial, nil
		}
		// A slow swarm can take minutes: the extraction goes on and caches the track, the player
		// gets the live read meanwhile (also when the extraction fails)
		call := s.startFile(source, lang, ref)
		timer := time.NewTimer(embeddedWait)
		defer timer.Stop()
		select {
		case <-call.done:
			if call.err == nil {
				return call.val.([]byte), false, nil
			}
			log.Printf("subtitles: extraction of %s failed, reading it live: %v", shortSubID(id), call.err)
		case <-timer.C:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		vtt, partial := s.live.get(ctx, key, hash, index, stream, math.Max(t, 0))
		return vtt, partial, nil
	}
	b, err := s.File(ctx, id)
	return b, false, err
}

// embeddedRef reads the "<hash>|<file index>|<stream index>" of an embedded subtitle id.
func embeddedRef(ref string) (hash string, index, stream int, err error) {
	p := strings.Split(ref, "|")
	if len(p) != 3 || !infoHashRe.MatchString(p[0]) {
		return "", 0, 0, ErrBadID
	}
	index, err1 := strconv.Atoi(p[1])
	stream, err2 := strconv.Atoi(p[2])
	if err1 != nil || err2 != nil || index < 0 || stream < 0 {
		return "", 0, 0, ErrBadID
	}
	return strings.ToLower(p[0]), index, stream, nil
}

// movieHash returns the OpenSubtitles hash of the playing file ("" when unknown or too slow).
func (s *Service) movieHash(ctx context.Context, hash string, index int) string {
	if hash == "" || index <= 0 || s.cfg.TorrServerURL == "" {
		return ""
	}
	key := fmt.Sprintf("%s|%d", hash, index)
	s.hashMu.Lock()
	h, ok := s.hashes[key]
	s.hashMu.Unlock()
	if ok {
		return h
	}
	ctx, cancel := context.WithTimeout(ctx, hashLimit)
	defer cancel()
	h, _, err := torrServerFileHash(ctx, s.http, s.cfg.TorrServerURL, hash, index)
	if err != nil {
		log.Printf("subtitles: moviehash %s: %v", key, err)
		return ""
	}
	s.hashMu.Lock()
	if len(s.hashes) > 5000 {
		s.hashes = map[string]string{}
	}
	s.hashes[key] = h
	s.hashMu.Unlock()
	return h
}

// ErrBadID is returned for ids that were not made by List.
var ErrBadID = errors.New("invalid subtitle id")

// File returns the WebVTT of a subtitle id, from the disk cache or downloaded and converted once.
func (s *Service) File(ctx context.Context, id string) ([]byte, error) {
	source, lang, ref, err := decodeID(id)
	if err != nil {
		return nil, err
	}
	key := source + "|" + ref
	if b, ok := s.files.read(source, key, ".vtt", 0); ok {
		return b, nil
	}
	if why, ok := s.missing(source, ref); ok {
		return nil, noSubtitle(why)
	}
	call := s.startFile(source, lang, ref)
	select {
	case <-call.done:
		if call.err != nil {
			return nil, call.err
		}
		return call.val.([]byte), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// missingFor: a download that held no subtitle that fits (ErrNoSubtitle) is not tried again, nor
// listed, for this long (a season pack may get the episode later).
const missingFor = 24 * time.Hour

// missing reports a subtitle whose download held nothing that fits within missingFor, and why.
func (s *Service) missing(source, ref string) (string, bool) {
	b, ok := s.files.read(source, source+"|"+ref, ".none", missingFor)
	return string(b), ok
}

// dropMissing leaves out the candidates whose download held nothing that fits (missing): the
// players would pick a season pack without the episode first, and its pre-timing too (The
// Mentalist S03E24: the subtitle shown instead was untimed for its first 21 s).
func (s *Service) dropMissing(all []candidate) []candidate {
	out := all[:0:0]
	for _, c := range all {
		if source, _, ref, err := decodeID(c.ID); err == nil && source != SourceTorrent && source != SourceEmbedded {
			if _, gone := s.missing(source, ref); gone {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// hasFile reports whether the converted subtitle of id is on disk (File needs no download).
func (s *Service) hasFile(id string) bool {
	source, _, ref, err := decodeID(id)
	return err == nil && s.files.has(source, source+"|"+ref, ".vtt")
}

// startFile downloads (or extracts) and caches a subtitle once per key in the background.
func (s *Service) startFile(source, lang, ref string) *flightCall {
	key := source + "|" + ref
	return s.flights.start("file|"+key, func() (any, error) {
		limit := fetchLimit
		if source == SourceEmbedded {
			limit = embeddedExtractLimit // reads the whole file
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		vtt, err := s.download(ctx, source, lang, ref)
		if err != nil {
			if errors.Is(err, ErrNoSubtitle) {
				log.Printf("subtitles: %s/%s: %v: left out of the lists for %s", source, truncateString(ref, 60), err, missingFor)
				if werr := s.files.write(source, key, ".none", []byte(err.Error())); werr != nil {
					log.Printf("subtitles: cache write: %v", werr)
				}
			}
			return nil, err
		}
		if err := s.files.write(source, key, ".vtt", vtt); err != nil {
			log.Printf("subtitles: cache write: %v", err)
		}
		return vtt, nil
	})
}

var infoHashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (s *Service) download(ctx context.Context, source, lang, ref string) ([]byte, error) {
	st := s.settings.get()
	var raw []byte
	var name string
	var err error
	switch source {
	case SourceTorrent, SourceEmbedded:
		p := strings.Split(ref, "|")
		if len(p) != 3 || !infoHashRe.MatchString(p[0]) {
			return nil, ErrBadID
		}
		a, err1 := strconv.Atoi(p[1])
		if err1 != nil || a < 0 {
			return nil, ErrBadID
		}
		if source == SourceEmbedded {
			b, err2 := strconv.Atoi(p[2])
			if err2 != nil || b < 0 {
				return nil, ErrBadID
			}
			return s.extract(ctx, s.cfg.TorrServerURL, p[0], a, b)
		}
		raw, err = FetchTorrentSub(ctx, s.cfg.TorrServerURL, p[0], a)
		name = "torrent." + firstNonEmpty(strings.Trim(p[2], "./\\"), "srt")
	case SourceOpenSubtitles:
		raw, name, err = s.os.fetch(ctx, ref, st)
	case SourceSubDL:
		raw, name, err = s.sdl.fetch(ctx, ref, lang)
	case SourceSubSource:
		raw, name, err = s.ss.fetch(ctx, ref, lang, st)
	case SourceTurkceAltyazi:
		if !st.TurkceAltyaziEnabled {
			return nil, errors.New("turkcealtyazi.org is disabled in settings")
		}
		raw, name, err = s.ta.fetch(ctx, ref, lang)
	case SourceSubf2m:
		if !st.Subf2mEnabled {
			return nil, errors.New("subf2m is disabled in settings")
		}
		raw, name, err = s.sf.fetch(ctx, ref, lang)
	case SourceYIFY:
		if !st.YIFYEnabled {
			return nil, errors.New("YIFY Subtitles is disabled in settings")
		}
		raw, name, err = s.yts.fetch(ctx, ref, lang)
	default:
		return nil, ErrBadID
	}
	if err != nil {
		return nil, err
	}
	if lang == "" {
		lang = GuessLanguage(name) // the archive's file name
	}
	return toWebVTTLang(raw, name, lang)
}

// encodeID builds the opaque id the web app passes back to /api/subtitles/file.
func encodeID(source, lang, ref string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(source + "|" + lang + "|" + ref))
}

func decodeID(id string) (source, lang, ref string, err error) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(b) > 1024 {
		return "", "", "", ErrBadID
	}
	parts := strings.SplitN(string(b), "|", 3)
	if len(parts) != 3 || parts[2] == "" {
		return "", "", "", ErrBadID
	}
	switch parts[0] {
	case SourceTorrent, SourceEmbedded, SourceOpenSubtitles, SourceSubDL, SourceSubSource, SourceTurkceAltyazi,
		SourceSubf2m, SourceYIFY, SourceAI:
	default:
		return "", "", "", ErrBadID
	}
	return parts[0], parts[1], parts[2], nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
