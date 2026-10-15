package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/torbox"
)

// Saved sources (Continue Watching, a resumed episode) skip the search, but not the debrid
// decision of resolve: a release TorBox has cached plays from TorBox, and a torrent's swarm
// is measured where playback resumes; one that cannot keep up is replaced by a fresh resolve
// instead of buffering for minutes. See docs/ARCHITECTURE.md (Saved sources).

const (
	tsReuseStatTime = 10 * time.Second // metadata of a saved torrent TorrServer no longer holds
	tsAdoptRetry    = 10 * time.Minute // a hash no cached source names as TorBox is looked up again after this
)

// tsReuseWindow is the longest swarm measurement of a saved torrent (a var for tests).
var tsReuseWindow = 8 * time.Second

// tsReuseAnswer is GET /reuse: play Source, or resolve the title again (Reason says why).
type tsReuseAnswer struct {
	Action string           `json:"action"` // "play" or "resolve"
	Reason string           `json:"reason,omitempty"`
	Source *tsResolveResult `json:"source,omitempty"`
}

// tsSaved is the source a player saved with its progress.
type tsSaved struct {
	hash   string
	index  int
	magnet string
	at     float64 // resume position, seconds
}

// reuse decides how a saved source plays (GET /torrent-stream/reuse).
func (h handler) reuse(c *gin.Context) {
	s := h.s
	q, ok := h.parseQuery(c)
	if !ok {
		return
	}
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	sv := tsSaved{hash: hash, index: index}
	if m := strings.TrimSpace(c.Query("magnet")); strings.HasPrefix(m, "magnet:?") && tsHashFromMagnet(m) == hash {
		sv.magnet = m
	}
	if t, err := strconv.ParseFloat(c.Query("t"), 64); err == nil && t > 0 && t < 86400 {
		sv.at = t
	}
	if s.offline(c.Request.Context()) {
		c.JSON(errOffline.status, errOffline.body())
		return
	}
	key := tsResolveKey(q)
	w := s.holdWarm(key)
	s.markIntent(w) // a player asks: the title never gives way to another one's playback
	s.touch(hash)
	c.JSON(http.StatusOK, s.reuseSaved(c.Request.Context(), key, q, sv, w))
}

// reuseSaved: TorBox when it has the release cached, else the torrent when its swarm keeps
// up (or could not be measured), else "resolve".
func (s *Service) reuseSaved(ctx context.Context, key string, q tsResolveQuery, sv tsSaved, w *tsWarm) tsReuseAnswer {
	started := time.Now()
	// The title's cached source (registered again as a TorBox one after a restart)
	cached, _ := s.cachedSource(key)
	if alt := s.stepDownAlternative(key, q, sv, cached); alt != nil {
		log.Printf("reuse %q: %s was a step-down pick, resuming the title's own source %q", q.title, tsShortHash(sv.hash), alt.TorrentTitle)
		sv = tsSaved{hash: strings.ToLower(alt.Hash), index: alt.Index, magnet: alt.Magnet, at: sv.at}
	}
	if cached != nil && !strings.EqualFold(cached.Hash, sv.hash) {
		cached = nil
	}
	if s.debrid.enabled() {
		res, why := s.reuseDebrid(ctx, q, sv, cached)
		if res != nil {
			s.useSource(w, res)
			s.debridRewarm(w, res) // link and header ahead of the player's probe
			log.Printf("reuse %q: %s:%d is cached on TorBox, playing it from TorBox (%s)", q.title, tsShortHash(sv.hash), res.Index,
				time.Since(started).Round(time.Millisecond))
			return tsReuseAnswer{Action: "play", Source: res}
		}
		log.Printf("reuse %q: %s:%d not from TorBox: %s", q.title, tsShortHash(sv.hash), sv.index, why)
	}
	ans := s.reuseTorrent(ctx, q, sv, cached, w)
	if ans.Action == "play" {
		log.Printf("reuse %q: %s:%d plays from its swarm: %s (%s)", q.title, tsShortHash(sv.hash), sv.index, ans.Source.Health,
			time.Since(started).Round(time.Millisecond))
	} else {
		log.Printf("reuse %q: %s:%d: %s, resolving again (%s)", q.title, tsShortHash(sv.hash), sv.index, ans.Reason,
			time.Since(started).Round(time.Millisecond))
	}
	return ans
}

// stepDownAlternative: the saved source was picked by a step down (a resolve capped with maxq
// after a slow swarm stalled) while the title has an uncapped source for these caps; a fresh
// open without a cap resumes that one (GoT S01E01: the 1080p of the step down stayed the saved
// source and every resume played it instead of the TorBox 4K).
func (s *Service) stepDownAlternative(key string, q tsResolveQuery, sv tsSaved, cached *tsResolveResult) *tsResolveResult {
	if q.maxQ > 0 || cached == nil || strings.EqualFold(cached.Hash, sv.hash) || s.db == nil {
		return nil
	}
	rows, err := loadSourcesByHash(s.db, sv.hash)
	if err != nil {
		return nil
	}
	// The cap sits before |cinema in a key (tsResolveCaps)
	for _, row := range rows {
		if k := tsMaxCapPattern.ReplaceAllString(row.SourceKey, ""); k != row.SourceKey && k == key {
			return cached
		}
	}
	return nil
}

var tsMaxCapPattern = regexp.MustCompile(`\|max\d+`)

// reuseDebrid plays the saved release from TorBox when TorBox has it cached; why says why not.
func (s *Service) reuseDebrid(ctx context.Context, q tsResolveQuery, sv tsSaved, cached *tsResolveResult) (*tsResolveResult, string) {
	d := s.debrid
	if !d.usable() {
		return nil, "TorBox is paused"
	}
	it := d.item(ctx, sv.hash)
	if it == nil {
		d.forgetUncached(sv.hash)
		return nil, "not cached on TorBox (or TorBox did not answer)"
	}
	files := tsCachedFiles(it)
	cand := tsReuseCandidate(it.Name, sv.hash, q)
	file, ok := tsReuseFile(files, q, sv, cand, cached)
	if !ok {
		return nil, "no matching video file in TorBox's list"
	}
	title := it.Name
	if cached != nil && cached.TorrentTitle != "" {
		title = cached.TorrentTitle
	}
	magnet := sv.magnet
	if magnet == "" {
		magnet = tsBuildMagnet(sv.hash, title, nil)
	}
	d.register(tsDebridSource{hash: sv.hash, index: file.ID, path: file.Path, size: file.Length, title: title, magnet: magnet})
	lctx, cancel := context.WithTimeout(ctx, tsDebridLinkTimeout)
	_, err := d.link(lctx, sv.hash, file.ID, false)
	cancel()
	if err != nil {
		d.unregister(sv.hash, file.ID)
		return nil, err.Error()
	}
	res := &tsResolveResult{
		Magnet: magnet, Hash: sv.hash, Index: file.ID, FileName: filepath.Base(file.Path), TorrentTitle: title,
		Quality: tsQualityLabel(cand.quality), Size: file.Length, HDR: cand.hdr, Source: tsSourceTorBox, Debrid: true,
		Cached: true,
	}
	if cached != nil && cached.Debrid && cached.Index == file.ID {
		res.Quality, res.HDR, res.Format, res.Cinema = cached.Quality, cached.HDR, cached.Format, cached.Cinema
		res.Duration, res.DurationVerified = cached.Duration, cached.DurationVerified
	}
	s.applyProbedQuality(res)
	return res, ""
}

// reuseTorrent measures the saved torrent's swarm at the resume position.
func (s *Service) reuseTorrent(ctx context.Context, q tsResolveQuery, sv tsSaved, cached *tsResolveResult, w *tsWarm) tsReuseAnswer {
	link := sv.magnet
	if link == "" && cached != nil && strings.HasPrefix(cached.Magnet, "magnet:?") {
		link = cached.Magnet
	}
	if link == "" {
		link = sv.hash
	}
	// A saved TorBox source's index is its place in TorBox's list: its file is found in the
	// torrent by path and size, and the hash plays as a torrent from now on
	var want *tsDebridSource
	if reg, ok := s.debrid.registered(sv.hash, sv.index); ok {
		want = &reg
	} else if cached != nil && cached.Debrid && cached.Index == sv.index {
		want = &tsDebridSource{path: cached.FileName, size: cached.Size}
	}
	if want != nil {
		s.debrid.unregisterHash(sv.hash)
	}
	s.trackTorrent(w, sv.hash)
	runtime := s.lookupRuntime(q)
	sctx, cancel := context.WithTimeout(ctx, tsReuseStatTime)
	var st tsTorrentStat
	err := s.torrentStat(sctx, link, &st)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return tsReuseAnswer{Action: "resolve", Reason: "cancelled"}
		}
		s.dropFrom(w, sv.hash)
		return tsReuseAnswer{Action: "resolve", Reason: "no torrent metadata: " + err.Error()}
	}
	file, ok := tsReuseTorrentFile(st.FileStats, sv.index, want)
	if !ok {
		s.dropFrom(w, sv.hash)
		return tsReuseAnswer{Action: "resolve", Reason: fmt.Sprintf("file %d is not in the torrent", sv.index)}
	}
	debridIndex := sv.index
	sv.index = file.ID
	res := &tsResolveResult{
		Magnet: link, Hash: sv.hash, Index: sv.index, FileName: filepath.Base(file.Path), TorrentTitle: st.Name,
		Size: file.Length, Cached: true,
	}
	if !strings.HasPrefix(res.Magnet, "magnet:?") {
		res.Magnet = tsBuildMagnet(sv.hash, st.Name, nil)
	}
	if cached != nil && cached.Index == debridIndex && (cached.Debrid == (want != nil)) {
		res.Quality, res.HDR, res.Format, res.Cinema = cached.Quality, cached.HDR, cached.Format, cached.Cinema
		res.Duration, res.DurationVerified, res.TorrentTitle = cached.Duration, cached.DurationVerified, cached.TorrentTitle
	} else {
		c := tsReuseCandidate(st.Name, sv.hash, q)
		res.Quality, res.HDR = tsQualityLabel(c.quality), c.hdr
	}
	s.applyProbedQuality(res)
	// The header the player reads next, while the swarm is measured
	go func() {
		pctx, cancel := context.WithTimeout(w.ctx, 40*time.Second)
		defer cancel()
		if _, err := s.probeFile(pctx, sv.hash, sv.index); err != nil {
			log.Printf("reuse %q: probe of %s: %v", q.title, tsShortHash(sv.hash), err)
		}
	}()
	rt, _, _ := runtime()
	need := tsNeedRate(file.Length, rt, q.mediaType)
	if need <= 0 {
		s.useSource(w, res)
		return tsReuseAnswer{Action: "play", Source: res}
	}
	health := s.swarmHealthAt(ctx, sv.hash, sv.index, s.reuseOffset(sv, file.Length, rt), file.Length, need)
	if ctx.Err() != nil {
		return tsReuseAnswer{Action: "resolve", Reason: "cancelled"}
	}
	res.Health = health
	if health == nil || health.KeepsUp {
		s.useSource(w, res)
		return tsReuseAnswer{Action: "play", Source: res}
	}
	s.dropFrom(w, sv.hash)
	return tsReuseAnswer{Action: "resolve", Reason: "its swarm does not keep up: " + health.String()}
}

// swarmHealthAt measures a torrent file's swarm where playback goes on (saved and cached
// sources). A read TorrServer served from its cache is measured once more half the file
// away; when that is cached too, the swarm counts as unmeasured (nil).
func (s *Service) swarmHealthAt(ctx context.Context, hash string, index int, offset, size, need int64) *tsHealth {
	h, _ := s.measureOnce(ctx, hash, index, offset, need, tsReuseWindow, &tsWarmHooks{})
	if h == nil || !h.cached || ctx.Err() != nil {
		return h
	}
	away := tsAwayOffset(size, offset)
	log.Printf("warm: %s:%d at %d MB came from TorrServer's cache (%s), measuring at %d MB", tsShortHash(hash), index,
		offset>>20, h, away>>20)
	if h, _ = s.measureOnce(ctx, hash, index, away, need, tsReuseWindow, &tsWarmHooks{}); h != nil && h.cached {
		return nil
	}
	return h
}

// tsAwayOffset is half the file away from offset (1 MiB aligned, room for one read).
func tsAwayOffset(size, offset int64) int64 {
	if size <= 0 {
		return 0
	}
	off := (offset + size/2) % size
	if last := size - tsWarmMaxBytes; off > last {
		off = max(last, 0)
	}
	return off &^ (1<<20 - 1)
}

// tsCachedFresh: a swarm verdict this recent still stands when resolve answers with a cached
// source; an older one is measured again first (verifyCached).
const tsCachedFresh = 3 * time.Minute

type tsSwarmSeen struct {
	at     time.Time
	health tsHealth
}

// noteSwarm records a swarm verdict of hash:index.
func (s *Service) noteSwarm(hash string, index int, h *tsHealth) {
	s.swarmSeen.Store(fmt.Sprintf("%s:%d", strings.ToLower(hash), index), tsSwarmSeen{at: time.Now(), health: *h})
}

// recentSwarm is the swarm verdict of hash:index from the last tsCachedFresh.
func (s *Service) recentSwarm(hash string, index int) (tsSwarmSeen, bool) {
	v, ok := s.swarmSeen.Load(fmt.Sprintf("%s:%d", strings.ToLower(hash), index))
	if !ok || time.Since(v.(tsSwarmSeen).at) >= tsCachedFresh {
		return tsSwarmSeen{}, false
	}
	return v.(tsSwarmSeen), true
}

// verifyCached checks a cached source's swarm before resolve answers with it: one that played
// fine hours ago may have lost its swarm (The Mentalist S02E10: the cached 52d6688e opened
// with no data for 11.6 s, then the player searched again). A file TorBox plays, or whose
// swarm was measured within tsCachedFresh, is not measured. at is the start position
// (seconds, 0 = unknown). "" when the source may play (res.Health is then the new
// measurement), else why not.
func (s *Service) verifyCached(ctx context.Context, q tsResolveQuery, res *tsResolveResult, at float64, w *tsWarm) string {
	if res.Debrid && s.debrid.usable() {
		return ""
	}
	short := fmt.Sprintf("%s:%d", tsShortHash(res.Hash), res.Index)
	if seen, ok := s.recentSwarm(res.Hash, res.Index); ok {
		log.Printf("resolve %q: the cached %s was measured %s ago: %s", q.title, short,
			time.Since(seen.at).Round(time.Second), &seen.health)
		return ""
	}
	started := time.Now()
	s.trackTorrent(w, res.Hash)
	link := res.Magnet
	if !strings.HasPrefix(link, "magnet:?") {
		link = res.Hash
	}
	sctx, cancel := context.WithTimeout(ctx, tsReuseStatTime)
	var st tsTorrentStat
	err := s.torrentStat(sctx, link, &st)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return "cancelled"
		}
		s.dropFrom(w, res.Hash)
		return "no torrent metadata: " + err.Error()
	}
	size := res.Size
	for _, f := range st.FileStats {
		if size <= 0 && f.ID == res.Index {
			size = f.Length
		}
	}
	rt := q.runtime
	if res.Health == nil || res.Health.Need <= 0 || at > 0 {
		rt, _, _ = s.lookupRuntime(q)()
	}
	need := tsNeedRate(size, rt, q.mediaType)
	if res.Health != nil && res.Health.Need > 0 {
		need = res.Health.Need
	}
	if need <= 0 {
		return ""
	}
	// The header the player reads next, while the swarm is measured
	go func() {
		pctx, cancel := context.WithTimeout(w.ctx, 40*time.Second)
		defer cancel()
		if _, err := s.probeFile(pctx, res.Hash, res.Index); err != nil {
			log.Printf("resolve %q: probe of %s: %v", q.title, short, err)
		}
	}()
	off := tsMidOffset(size, 0)
	if at > 0 {
		off = s.reuseOffset(tsSaved{hash: res.Hash, index: res.Index, at: at}, size, rt)
	}
	h := s.swarmHealthAt(ctx, res.Hash, res.Index, off, size, need)
	if ctx.Err() != nil {
		return "cancelled"
	}
	took := time.Since(started).Round(time.Millisecond)
	if h == nil || h.KeepsUp {
		if h != nil {
			res.Health = h
		}
		log.Printf("resolve %q: the cached %s was measured again: %s, playing it (%s)", q.title, short, h, took)
		return ""
	}
	s.dropFrom(w, res.Hash)
	return fmt.Sprintf("its swarm does not keep up: %s (%s)", h, took)
}

// tsReuseTorrentFile finds the saved file among TorrServer's files: by TorrServer's id, or
// for a TorBox index (want set) by path and size.
func tsReuseTorrentFile(files []tsFileStat, index int, want *tsDebridSource) (tsFileStat, bool) {
	if want == nil {
		for _, f := range files {
			if f.ID == index && f.ID != 0 {
				return f, true
			}
		}
		return tsFileStat{}, false
	}
	if want.path == "" && want.size <= 0 {
		return tsFileStat{}, false
	}
	list := make([]torbox.File, 0, len(files))
	for _, f := range files {
		list = append(list, torbox.File{ID: int64(f.ID), Name: f.Path, Size: f.Length})
	}
	id, ok := tsDebridMatchFile(list, tsDebridSource{path: want.path, size: want.size}, nil)
	if !ok {
		return tsFileStat{}, false
	}
	for _, f := range files {
		if int64(f.ID) == id {
			return f, true
		}
	}
	return tsFileStat{}, false
}

// reuseOffset is where playback resumes in the file (1 MiB aligned): the pieces measured
// there are the ones the player reads next.
func (s *Service) reuseOffset(sv tsSaved, size int64, runtimeMin int) int64 {
	if sv.at <= 0 || size <= 0 {
		return 0
	}
	dur := float64(runtimeMin * 60)
	if p, ok := s.probeCache.Load(fmt.Sprintf("%s:%d", sv.hash, sv.index)); ok {
		if d := p.(*tsProbe).duration(); d > 0 {
			dur = d
		}
	}
	if dur <= 0 || sv.at >= dur {
		return 0
	}
	off := int64(float64(size) * sv.at / dur)
	if last := size - tsWarmMaxBytes; off > last {
		off = max(last, 0)
	}
	return off &^ (1<<20 - 1)
}

// tsCachedFiles is TorBox's file list with the indexes debrid sources use (place + 1).
func tsCachedFiles(it *torbox.CachedItem) []tsFileStat {
	files := make([]tsFileStat, 0, len(it.Files))
	for i, f := range it.Files {
		files = append(files, tsFileStat{ID: i + 1, Path: f.Name, Length: f.Size})
	}
	return files
}

// tsReuseCandidate reads a saved torrent's name like a search result (episode info,
// resolution, HDR); a name ranking rejects gives an empty candidate. The hash makes it a
// result ranking takes (one without a magnet or link is rejected).
func tsReuseCandidate(name, hash string, q tsResolveQuery) tsCandidate {
	if cands := tsRankAll([]JackettResult{{Title: name, Seeders: 1, InfoHash: hash}}, q); len(cands) > 0 && cands[0].quality > 0 {
		return cands[0]
	}
	tokens := map[string]bool{}
	for _, t := range tsNormalize(name) {
		tokens[t] = true
	}
	return tsCandidate{raw: JackettResult{Title: name}, quality: tsResolution(tokens, q.hevc),
		hdr: tsHDRPattern.MatchString(strings.ToLower(name))}
}

// tsReuseFile finds the saved file in TorBox's list: the cached source's file, the episode
// / movie by name, else the saved index when it is a video.
func tsReuseFile(files []tsFileStat, q tsResolveQuery, sv tsSaved, c tsCandidate, cached *tsResolveResult) (tsFileStat, bool) {
	if cached != nil && cached.Debrid && cached.Index >= 1 && cached.Index <= len(files) {
		return files[cached.Index-1], true
	}
	if f, ok := tsPickFile(files, q, c); ok {
		return f, true
	}
	if sv.index >= 1 && sv.index <= len(files) {
		if f := files[sv.index-1]; tsVideoExts[strings.ToLower(filepath.Ext(f.Path))] {
			return f, true
		}
	}
	var videos []tsFileStat
	for _, f := range files {
		if tsVideoExts[strings.ToLower(filepath.Ext(f.Path))] {
			videos = append(videos, f)
		}
	}
	if len(videos) == 1 {
		return videos[0], true
	}
	return tsFileStat{}, false
}

// adoptSaved registers hash as a TorBox source when a cached source names it as one, so a
// saved source played after a restart (before anything resolved its title, or by a client
// that does not ask /reuse) reads from TorBox, not TorrServer.
func (s *Service) adoptSaved(hash string) {
	d := s.debrid
	if s.db == nil || !tsHashPattern.MatchString(hash) || !d.enabled() {
		return
	}
	hash = strings.ToLower(hash)
	if _, ok := d.hashSource(hash); ok {
		return
	}
	d.mu.Lock()
	if at, ok := d.adoptTried[hash]; ok && time.Since(at) < tsAdoptRetry {
		d.mu.Unlock()
		return
	}
	d.adoptTried[hash] = time.Now()
	d.mu.Unlock()
	rows, err := loadSourcesByHash(s.db, hash)
	if err != nil {
		log.Printf("source cache: sources of %s: %v", tsShortHash(hash), err)
		return
	}
	for _, row := range rows {
		var res tsResolveResult
		if json.Unmarshal([]byte(row.Result), &res) != nil || !strings.EqualFold(res.Hash, hash) || !res.Debrid {
			continue
		}
		if s.debridOwns(&res) {
			log.Printf("torbox: %s:%d is a cached TorBox source (%s), playing it from TorBox", tsShortHash(hash), res.Index, row.SourceKey)
		}
		return
	}
}
