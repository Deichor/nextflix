package streaming

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/torbox"
)

// Resolve with TorBox (see debrid.go): the best ranked hashes are checked against TorBox's
// cache; with "prefer TorBox" a cached release moves to the top of its resolution. A cached
// candidate is checked without TorrServer: its file comes from TorBox's file list, the
// duration probe reads the HTTPS link, and a short parallel read (debrid_rate.go) stands in
// for the swarm health.

const (
	tsDebridBonus    = 5.0             // above the best uncached release of the resolution
	tsDebridGateWait = 5 * time.Second // lower ranked checks wait this long for the TorBox one
)

var errDebridPicked = errors.New("a TorBox source of this resolution was picked")

// debridCandidates asks TorBox about the top ranked hashes and, with "prefer TorBox", lifts
// the cached ones within their resolution. It returns the candidates to check and the cached
// entries by hash (nil when TorBox is off or has none of them).
func (s *Service) debridCandidates(ctx context.Context, q tsResolveQuery, raw []JackettResult, cands []tsCandidate) ([]tsCandidate, map[string]*torbox.CachedItem) {
	d := s.debrid
	if !d.usable() || len(cands) == 0 {
		return cands, nil
	}
	all := tsRankAll(raw, q)
	var hashes []string
	seen := map[string]bool{}
	for _, c := range all {
		h := strings.ToLower(c.hash)
		if h == "" || seen[h] || tsAvoided(c, q) {
			continue
		}
		seen[h] = true
		hashes = append(hashes, h)
		if len(hashes) >= tsDebridCheckMax {
			break
		}
	}
	started := time.Now()
	items := d.cachedItems(ctx, hashes)
	if len(items) == 0 {
		log.Printf("resolve %q: torbox has none of %d candidates cached (%s)", q.title, len(hashes), time.Since(started).Round(time.Millisecond))
		return cands, nil
	}
	cached := func(c tsCandidate) bool { return items[strings.ToLower(c.hash)] != nil && !tsAvoided(c, q) }
	// Without "prefer" the order stays; the cached ones among the checked still play from TorBox
	if !d.settings().PreferDisabled {
		tsDebridBoost(all, cached)
		tsSortCandidates(all, q)
		cands = tsTopCandidates(all, q)
	}
	n := 0
	for _, c := range cands {
		if cached(c) {
			n++
		}
	}
	log.Printf("resolve %q: torbox has %d of %d candidates cached (%s), %d of the checked ones", q.title, len(items), len(hashes),
		time.Since(started).Round(time.Millisecond), n)
	return cands, items
}

// tsDebridBoost raises every cached candidate by the same amount within its resolution so the
// best cached one ranks tsDebridBonus above the best of that resolution; the order among
// the cached ones stays.
func tsDebridBoost(cands []tsCandidate, cached func(tsCandidate) bool) {
	tierMax, cachedMax := map[int]float64{}, map[int]float64{}
	for _, c := range cands {
		if v, ok := tierMax[c.quality]; !ok || c.score > v {
			tierMax[c.quality] = c.score
		}
		if !cached(c) {
			continue
		}
		if v, ok := cachedMax[c.quality]; !ok || c.score > v {
			cachedMax[c.quality] = c.score
		}
	}
	for i := range cands {
		if cached(cands[i]) {
			q := cands[i].quality
			cands[i].score += tierMax[q] - cachedMax[q] + tsDebridBonus
		}
	}
}

// tsSortCandidates is tsRankAll's order: avoided last, for a capped client names without a
// resolution tag after the labeled ones (see tsUnknownRes), then score, then name.
func tsSortCandidates(cands []tsCandidate, q tsResolveQuery) {
	sort.SliceStable(cands, func(i, j int) bool {
		ai, aj := tsAvoided(cands[i], q), tsAvoided(cands[j], q)
		if ai != aj {
			return aj
		}
		if ui, uj := tsUnknownRes(cands[i], q), tsUnknownRes(cands[j], q); ui != uj {
			return uj
		}
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].raw.Title < cands[j].raw.Title
	})
}

// tsDebridGate lets the best ranked cached candidate (lead) go first: checks ranked below it
// wait until it finished (at most tsDebridGateWait), and once it is a good pick that fits the
// client the ones of its resolution or lower stop, so no swarm competes with the TorBox stream for the line.
// Higher resolutions still run (upgrade path).
type tsDebridGate struct {
	s     *Service
	cands []tsCandidate
	items map[string]*torbox.CachedItem
	lead  int
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	pick  *tsResolveResult
}

func (s *Service) newDebridGate(cands []tsCandidate, items map[string]*torbox.CachedItem) *tsDebridGate {
	g := &tsDebridGate{s: s, cands: cands, items: items, lead: -1, done: make(chan struct{})}
	for i := range cands {
		if g.item(i) != nil {
			g.lead = i
			break
		}
	}
	return g
}

func (g *tsDebridGate) item(i int) *torbox.CachedItem {
	return g.items[strings.ToLower(g.cands[i].hash)]
}

func (g *tsDebridGate) finish(res *tsResolveResult, err error) {
	g.once.Do(func() {
		if err == nil && res != nil && res.good() && res.unfit == "" {
			g.mu.Lock()
			g.pick = res
			g.mu.Unlock()
		}
		close(g.done)
	})
}

// wait blocks a lower ranked check until the lead finished; errDebridPicked when it should
// not run at all.
func (g *tsDebridGate) wait(ctx context.Context, i int) error {
	t := time.NewTimer(tsDebridGateWait)
	defer t.Stop()
	select {
	case <-g.done:
	case <-t.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	pick := g.pick
	g.mu.Unlock()
	if pick != nil && g.cands[i].quality <= tsQualityHeight(pick.Quality) {
		return errDebridPicked
	}
	return nil
}

// check runs candidate i: through TorBox when it is cached (falling back to the swarm check
// when TorBox cannot give a link), else swarm.
func (g *tsDebridGate) check(ctx context.Context, i int, q tsResolveQuery, claim func(string) bool,
	live func(*tsResolveResult), swarm func(context.Context) (*tsResolveResult, error)) (res *tsResolveResult, err error) {
	c := g.cands[i]
	if g.lead < 0 {
		g.s.debrid.forgetUncached(c.hash)
		return swarm(ctx)
	}
	if i == g.lead {
		defer func() { g.finish(res, err) }()
	} else if i > g.lead {
		if err := g.wait(ctx, i); err != nil {
			return nil, err
		}
	}
	if it := g.item(i); it != nil {
		res, err = g.s.checkDebrid(ctx, c, q, it, claim, live)
		if !errors.Is(err, errDebridUnavailable) {
			return res, err
		}
		log.Printf("resolve %q: %q: %v, checking its swarm", q.title, c.raw.Title, err)
	}
	g.s.debrid.forgetUncached(c.hash)
	return swarm(ctx)
}

// checkDebrid is checkCandidate for a release TorBox has cached.
func (s *Service) checkDebrid(ctx context.Context, c tsCandidate, q tsResolveQuery, it *torbox.CachedItem,
	claim func(string) bool, progress func(*tsResolveResult)) (*tsResolveResult, error) {
	hash := strings.ToLower(c.hash)
	if claim != nil && !claim(hash) {
		return nil, errDuplicateTorrent
	}
	files := make([]tsFileStat, 0, len(it.Files))
	for i, f := range it.Files {
		files = append(files, tsFileStat{ID: i + 1, Path: f.Name, Length: f.Size})
	}
	file, ok := tsPickFile(files, q, c)
	if !ok {
		return nil, errors.New("no matching video file")
	}
	magnet := c.magnet
	if !strings.HasPrefix(magnet, "magnet:?") {
		magnet = tsBuildMagnet(hash, c.raw.Title, nil)
	}
	d := s.debrid
	d.register(tsDebridSource{hash: hash, index: file.ID, path: file.Path, size: file.Length, title: c.raw.Title, magnet: magnet})
	started := time.Now()
	lctx, cancel := context.WithTimeout(ctx, tsDebridLinkTimeout)
	_, err := d.link(lctx, hash, file.ID, false)
	cancel()
	if err != nil {
		d.unregister(hash, file.ID)
		if !errors.Is(err, errDebridUnavailable) {
			err = fmt.Errorf("%w: %v", errDebridUnavailable, err)
		}
		return nil, err
	}
	log.Printf("resolve %q: %q is cached on TorBox, link in %s", q.title, c.raw.Title, time.Since(started).Round(time.Millisecond))

	res := &tsResolveResult{
		Magnet:          magnet,
		Hash:            hash,
		Index:           file.ID,
		FileName:        filepath.Base(file.Path),
		TorrentTitle:    c.raw.Title,
		Quality:         tsQualityLabel(c.quality),
		Seeders:         c.raw.Seeders,
		Size:            file.Length,
		HDR:             c.hdr,
		Source:          tsSourceTorBox,
		Debrid:          true,
		durationChecked: q.runtime > 0,
		nameQ:           c.quality,
		rank:            c.score,
		avoided:         tsAvoided(c, q),
		Cinema:          c.cinema,
	}
	nameTags := c.tags
	res.Format = &nameTags
	if progress != nil {
		early := *res
		early.partial = true
		progress(&early)
	}
	// The header probe and a short read of the middle (the line speed TorBox gives) together
	need := tsNeedRate(file.Length, q.runtime, q.mediaType)
	healthCh := make(chan *tsHealth, 1)
	go func() {
		healthCh <- s.debridHealth(ctx, hash, file.ID, file.Length, tsMidOffset(file.Length, 0), need, c.raw.Seeders)
	}()
	duration, verified, err := s.checkDuration(ctx, c, q, hash, file.ID, nil, nil)
	health := <-healthCh
	if err != nil {
		d.unregister(hash, file.ID)
		return nil, err
	}
	res.Duration, res.DurationVerified = math.Round(duration), verified
	if s.applyProbedQuality(res) {
		log.Printf("resolve %q: %q is %s, not %s as its name says", q.title, c.raw.Title, res.Quality, tsQualityLabel(c.quality))
	}
	s.applyProbedLanguage(res, q)
	formatMoved := s.applyProbedFormat(res, c, q)
	subsMoved := s.applyProbedSubs(res, c, q, files, file)
	s.applyProbedCaps(res, q)
	if formatMoved || subsMoved || res.unfit != "" {
		log.Printf("resolve %q: %q probed as {%s}: rank %.1f -> %.1f%s", q.title, c.raw.Title, res.Format, c.score, res.rank,
			tsUnfitNote(res.unfit))
	}
	res.Health = health
	log.Printf("resolve %q: %q plays from TorBox: %s in %s%s", q.title, c.raw.Title, health, time.Since(started).Round(time.Millisecond),
		tsHealthSeries(health))
	s.noteChecked(q, res)
	return res, nil
}
