package streaming

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Subtitle alternatives: the playing release may lack the profile language's subtitle while
// another release of the title carries it inside (muxed platform subtitles, subtitle files).
// Every finished check records its result with the subtitle languages it found (subs.go), and
// resolve remembers its ranking. GET /subalt offers a recorded release with the subtitle that
// kept up; without one it probes the headers of a few untried candidates in the background
// (quiet gate aware, at most once per tsSubAltScanEvery per title) and measures a hit's swarm.

const (
	tsSubAltKeep      = 2 * time.Hour    // checked results and the ranking of a title stay this long
	tsSubAltFresh     = 30 * time.Minute // a swarm measurement this old still counts for an offer
	tsSubAltProbes    = 3                // headers probed per scan
	tsSubAltRanked    = 12               // ranked candidates remembered per title
	tsSubAltProbeTime = 40 * time.Second // one header probe of a scan
	tsSubAltScanTime  = 3 * time.Minute  // a whole scan
	tsSubAltRetry     = 30               // seconds a player waits before asking again while a scan runs
)

// errNoMatchingFile: no video file of the torrent is the title / episode.
var errNoMatchingFile = errors.New("no matching video file")

// Var so tests can shorten it.
var tsSubAltScanEvery = 10 * time.Minute // background scans per title

// tsSubAltEntry is a finished check of one torrent.
type tsSubAltEntry struct {
	res  tsResolveResult
	caps string // tsSubAltCaps of the check: the client it was ranked and measured for
	at   time.Time
}

// tsSubAlt is what the server knows about the releases of one title (resolve base key).
type tsSubAlt struct {
	mu       sync.Mutex
	known    map[string]tsSubAltEntry // hash -> its latest finished check
	ranked   []tsCandidate            // the latest resolve's ranking (best first)
	rankedQ  tsResolveQuery
	rankedAt time.Time
	seen     time.Time // last change or question
	scanAt   time.Time // last scan started
	scanning bool
}

// tsSubAltCaps is the client capability of q without the quality cap (a capped re-resolve
// checks for the same client).
func tsSubAltCaps(q tsResolveQuery) string {
	q.maxQ = 0
	return tsResolveCaps(q)
}

// subAltOf returns the record of the title base (nil when unknown and !create). Records
// untouched for tsSubAltKeep are dropped on the way.
func (s *Service) subAltOf(base string, create bool) *tsSubAlt {
	if v, ok := s.subAlts.Load(base); ok {
		return v.(*tsSubAlt)
	}
	if !create {
		return nil
	}
	now := time.Now()
	s.subAlts.Range(func(k, v any) bool {
		a := v.(*tsSubAlt)
		a.mu.Lock()
		stale := !a.scanning && now.Sub(a.seen) > tsSubAltKeep
		a.mu.Unlock()
		if stale {
			s.subAlts.Delete(k)
		}
		return true
	})
	v, _ := s.subAlts.LoadOrStore(base, &tsSubAlt{known: map[string]tsSubAltEntry{}, seen: now})
	return v.(*tsSubAlt)
}

// noteChecked records a finished check (see checkCandidate).
func (s *Service) noteChecked(q tsResolveQuery, r *tsResolveResult) {
	if r == nil || r.partial || !tsHashPattern.MatchString(r.Hash) {
		return
	}
	a := s.subAltOf(tsResolveBaseKey(q), true)
	now := time.Now()
	a.mu.Lock()
	a.known[r.Hash] = tsSubAltEntry{res: *r, caps: tsSubAltCaps(q), at: now}
	a.seen = now
	a.mu.Unlock()
}

// noteRanked remembers the best tsSubAltRanked candidates of a resolve's results, the
// torrents a scan may try. Upgrade searches (higher resolutions only) are left out.
func (s *Service) noteRanked(q tsResolveQuery, raw []JackettResult) {
	if q.upgradeFrom > 0 {
		return
	}
	cands := tsRankAll(raw, q)
	if len(cands) > tsSubAltRanked {
		cands = cands[:tsSubAltRanked]
	}
	a := s.subAltOf(tsResolveBaseKey(q), true)
	now := time.Now()
	a.mu.Lock()
	a.ranked, a.rankedQ, a.rankedAt, a.seen = cands, q, now, now
	a.rankedQ.warm = nil
	a.mu.Unlock()
}

// tsSubAltFloor is the lowest resolution offered instead of a playing curQ: one step down,
// never below 720p unless the playing one is.
func tsSubAltFloor(curQ int) int {
	switch {
	case curQ >= 2160:
		return 1080
	case curQ >= 1080:
		return 720
	case curQ > 0:
		return curQ
	}
	return 720
}

// tsSubAltFits: a release of resolution rq is acceptable for q playing at curQ.
func tsSubAltFits(rq, curQ int, q tsResolveQuery) bool {
	if rq < tsSubAltFloor(curQ) || (q.maxQ > 0 && rq > q.maxQ) {
		return false
	}
	return rq <= 1080 || q.hevc
}

// tsSubAltPick chooses among the recorded checks the one to offer: checked for this client
// (caps), not current or avoided, subtitles in lang, of a fitting length and fit for the
// client, kept up when it was measured (within tsSubAltFresh), of an acceptable resolution
// (tsSubAltFits). The playing resolution first, then higher, then lower; then the swarm with
// the most headroom, then the rank.
func tsSubAltPick(known map[string]tsSubAltEntry, caps string, q tsResolveQuery, current string, curQ int, lang string,
	now time.Time) *tsResolveResult {
	var picks []tsResolveResult
	for h, e := range known {
		r := e.res
		if h == current || e.caps != caps || now.Sub(e.at) > tsSubAltFresh || r.Format == nil || !tsHasLang(r.Format.Subs, lang) {
			continue
		}
		if r.unfit != "" || !r.durationOK() || r.Health == nil || !r.Health.KeepsUp || r.Health.Dead {
			continue
		}
		if tsAvoided(tsCandidate{hash: h}, q) || !tsSubAltFits(tsQualityHeight(r.Quality), curQ, q) {
			continue
		}
		picks = append(picks, r)
	}
	if len(picks) == 0 {
		return nil
	}
	tier := func(r tsResolveResult) int {
		switch rq := tsQualityHeight(r.Quality); {
		case rq == curQ:
			return 0
		case rq > curQ:
			return 1
		}
		return 2
	}
	sort.Slice(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if tier(a) != tier(b) {
			return tier(a) < tier(b)
		}
		if ra, rb := a.Health.ratio(), b.Health.ratio(); ra != rb {
			return ra > rb
		}
		if a.rank != b.rank {
			return a.rank > b.rank
		}
		return a.Hash < b.Hash
	})
	r := picks[0]
	return &r
}

// tsSubAltScanList chooses the candidates a scan probes: torrents with a magnet that were not
// checked yet, not current or avoided, of an acceptable resolution; the ones whose name hints
// at the subtitle first, then by rank. At most tsSubAltProbes.
func tsSubAltScanList(ranked []tsCandidate, known map[string]tsSubAltEntry, q tsResolveQuery, current string, curQ int,
	lang string) []tsCandidate {
	type scored struct {
		c    tsCandidate
		hint float64
	}
	var list []scored
	for _, c := range ranked {
		if c.hash == "" || c.magnet == "" || c.hash == current || tsAvoided(c, q) || c.quality == 0 {
			continue
		}
		if _, checked := known[c.hash]; checked || !tsSubAltFits(c.quality, curQ, q) {
			continue
		}
		list = append(list, scored{c, tsSubNameScore(c.rest, lang, q.origLang)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].hint != list[j].hint {
			return list[i].hint > list[j].hint
		}
		return list[i].c.score > list[j].c.score
	})
	out := make([]tsCandidate, 0, tsSubAltProbes)
	for _, x := range list {
		if len(out) == tsSubAltProbes {
			break
		}
		out = append(out, x.c)
	}
	return out
}

// subaltFor returns a release of q's title other than current whose subtitles include lang
// (see tsSubAltPick), or nil; then it starts a background scan unless one ran for the title
// within tsSubAltScanEvery or another title's scan runs. scanning reports a running scan.
func (s *Service) subaltFor(q tsResolveQuery, current string, curQ int, lang string) (res *tsResolveResult, scanning bool) {
	if lang == "" {
		return nil, false // no language to look for: a scan could never match
	}
	base := tsResolveBaseKey(q)
	a := s.subAltOf(base, true)
	now := time.Now()
	a.mu.Lock()
	a.seen = now
	res = tsSubAltPick(a.known, tsSubAltCaps(q), q, current, curQ, lang, now)
	start := res == nil && !a.scanning && now.Sub(a.scanAt) >= tsSubAltScanEvery
	if start {
		select {
		case s.subAltSlot <- struct{}{}: // one scan server-wide
			a.scanning, a.scanAt = true, now
		default:
			start = false
		}
	}
	scanning = a.scanning
	a.mu.Unlock()
	if res != nil {
		log.Printf("resolve %q: subtitle alternative (%s) for %s: %q (%s, subs %s): %s", q.title, lang, tsShortHash(current),
			res.TorrentTitle, res.Quality, strings.Join(res.Format.Subs, "+"), res.Health)
		return res, false
	}
	if start {
		scan := s.subAltScanFn
		if scan == nil {
			scan = s.scanSubAlt
		}
		go func() {
			defer func() {
				<-s.subAltSlot
				a.mu.Lock()
				a.scanning, a.seen = false, time.Now()
				a.mu.Unlock()
			}()
			scan(a, q, current, curQ, lang)
		}()
	}
	return nil, scanning
}

// scanSubAlt probes the headers of a few untried candidates of the title (tsSubAltScanList; a
// title played from the cache is searched first) for a subtitle in lang. The first hit is
// checked like a resolve candidate (swarm health, length), which records it for subaltFor.
// Its work is background work (quiet gate, playback priority); the probed torrents are
// dropped again, a hit is kept in TorrServer's DB so switching to it opens fast.
func (s *Service) scanSubAlt(a *tsSubAlt, q tsResolveQuery, current string, curQ int, lang string) {
	w := q.warm
	if w == nil {
		return
	}
	ctx, cancel := context.WithTimeout(tsBackground(w.ctx), tsSubAltScanTime)
	defer cancel()
	a.mu.Lock()
	ranked, rq := a.ranked, a.rankedQ
	fresh := time.Since(a.rankedAt) < tsSubAltKeep
	a.mu.Unlock()
	if len(ranked) == 0 || !fresh {
		names := s.titleNamesLater(q)
		runtime := s.lookupRuntime(q)
		sr, nq, _, err := s.searchTitle(ctx, q, time.Now(), names)
		if err != nil {
			log.Printf("resolve %q: subtitle scan: search failed: %v", q.title, err)
			return
		}
		nq.runtime, _, _ = runtime()
		s.noteRanked(nq, sr.raw)
		a.mu.Lock()
		ranked, rq = a.ranked, a.rankedQ
		a.mu.Unlock()
	}
	rq.warm, rq.avoid, rq.lang, rq.maxQ, rq.hevc = w, q.avoid, lang, q.maxQ, q.hevc
	a.mu.Lock()
	list := tsSubAltScanList(ranked, a.known, rq, current, curQ, lang)
	a.mu.Unlock()
	log.Printf("resolve %q: no %s subtitle in %s, scanning %d other releases", q.title, lang, tsShortHash(current), len(list))
	for _, c := range list {
		langs, err := s.probeSubLangs(ctx, c, rq)
		if err != nil {
			log.Printf("resolve %q: subtitle scan of %q: %v", q.title, c.raw.Title, err)
			s.dropFrom(w, c.hash)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		log.Printf("resolve %q: subtitle scan: %q has subtitles %v", q.title, c.raw.Title, langs)
		if !tsHasLang(langs, lang) {
			s.dropFrom(w, c.hash)
			continue
		}
		if !s.warmSlot(ctx, c.hash, "subtitle alternative") {
			s.dropFrom(w, c.hash)
			return
		}
		res, err := s.checkCandidate(ctx, c, rq, "", tsWarmWindow, nil, nil)
		<-s.warmRounds
		if err != nil {
			log.Printf("resolve %q: subtitle alternative %q rejected: %v", q.title, c.raw.Title, err)
			continue
		}
		log.Printf("resolve %q: subtitle alternative %q (%s, subs %v): %s", q.title, res.TorrentTitle, res.Quality,
			res.Format.Subs, res.Health)
		s.saveTorrent(res.Hash)
		s.dropFrom(w, res.Hash)
		if res.good() && res.unfit == "" {
			return
		}
	}
}

// probeSubLangs adds a candidate to TorrServer, picks its file and returns the subtitle
// languages of its header (text tracks) and of the subtitle files next to it.
func (s *Service) probeSubLangs(ctx context.Context, c tsCandidate, q tsResolveQuery) ([]string, error) {
	if !s.waitQuiet(ctx, c.hash, "subtitle scan") {
		return nil, ctx.Err()
	}
	s.trackTorrent(q.warm, c.hash)
	sctx, cancel := context.WithTimeout(ctx, tsResolveValidateTime)
	var stat tsTorrentStat
	err := s.torrentStat(sctx, c.magnet, &stat)
	cancel()
	if err != nil {
		return nil, err
	}
	file, ok := tsPickFile(stat.FileStats, q, c)
	if !ok {
		return nil, errNoMatchingFile
	}
	langs := tsTorrentSubLangs(stat.FileStats, file)
	p, err := s.probeFileTimeout(ctx, c.hash, file.ID, tsSubAltProbeTime)
	if err != nil {
		if len(langs) > 0 {
			return langs, nil
		}
		return nil, err
	}
	for _, l := range tsProbedSubLangs(p) {
		langs = tsAddLang(langs, l)
	}
	return langs, nil
}

// subalt (GET /subalt) answers 200 with a release of the title whose subtitles include ?lang=
// (default: the profile's, X-Lang) when the playing current lacks it, else 204, with
// Retry-After while a background scan runs. Same query as /sidegrade.
func (h handler) subalt(c *gin.Context) {
	q, ok := h.parseQuery(c)
	if !ok {
		return
	}
	current := strings.ToLower(c.Query("current"))
	if !tsHashPattern.MatchString(current) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current must be the playing torrent's hash"})
		return
	}
	lang := tsLangCode(c.Query("lang"))
	if lang == "" {
		lang = q.lang
	}
	key := tsResolveKey(q)
	curQ, _ := strconv.Atoi(c.Query("current_q"))
	if curQ <= 0 {
		if cached, ok := h.s.cachedSource(key); ok && cached.Hash == current {
			curQ = tsQualityHeight(cached.Quality)
		}
	}
	q.warm = h.s.holdWarm(key)
	h.s.markIntent(q.warm)
	res, scanning := h.s.subaltFor(q, current, curQ, lang)
	if res != nil {
		c.JSON(http.StatusOK, res)
		return
	}
	if scanning {
		c.Header("Retry-After", strconv.Itoa(tsSubAltRetry))
	}
	c.Status(http.StatusNoContent)
}
