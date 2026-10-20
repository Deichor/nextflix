package streaming

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"nextflix/internal/platform/torbox"
)

// Client caps on the probed file: a release name may say nothing about its resolution or
// codec ("big-buck-bunny-2008-...", a 4K HEVC file), so once the probe read the header a
// file taller than the client's maxq, or with video play has to re-encode for it (HEVC
// without hevc=1, AV1, VP9, MPEG-4, Hi10P ...), is unfit for that client. Resolve prefers
// any fitting candidate and checks more of them before it falls back to an unfit one; the
// fallback is then the cheapest one to play (a copy before a re-encode, then the lowest
// resolution).

const (
	// Res part of a name without a resolution tag for a capped client (maxq or no HEVC):
	// below every labeled fitting resolution until the probe tells
	tsUnknownResCapped = -40
	// A fallback source is kept this long in the source cache, then resolve looks for a
	// fitting one again
	tsFallbackTTL = 6 * time.Hour
)

// tsCapped: the client limits what it can take (a quality cap, or no HEVC decoding).
func tsCapped(q tsResolveQuery) bool { return q.maxQ > 0 || !q.hevc }

// tsUnknownRes: c's name has no resolution tag and q's client is capped, so it ranks after
// every labeled candidate until a probe tells its size.
func tsUnknownRes(c tsCandidate, q tsResolveQuery) bool { return c.quality == 0 && tsCapped(q) }

// tsCapsMiss says why a probed file does not fit q's client ("" = it fits, or nothing is
// known about its video).
func tsCapsMiss(p *tsProbe, q tsResolveQuery) string {
	if p == nil {
		return ""
	}
	v := p.video()
	if v == nil {
		return ""
	}
	var why []string
	if vq := tsVideoQuality(v.Width, v.Height); q.maxQ > 0 && vq > q.maxQ {
		why = append(why, fmt.Sprintf("%s above the %dp cap", tsQualityLabel(vq), q.maxQ))
	}
	if !p.copiesVideo(q.hevc) {
		why = append(why, fmt.Sprintf("%s %s video is re-encoded", v.CodecName, v.PixFmt))
	}
	return strings.Join(why, ", ")
}

// tsStoredCapsMiss is tsCapsMiss from what a stored result knows (its probed resolution and
// video codec), for a cached source whose probe is gone.
func tsStoredCapsMiss(r *tsResolveResult, q tsResolveQuery) string {
	var why []string
	if rq := tsQualityHeight(r.Quality); q.maxQ > 0 && rq > q.maxQ {
		why = append(why, fmt.Sprintf("%s above the %dp cap", r.Quality, q.maxQ))
	}
	if f := r.Format; f != nil && f.Video != "" && (f.Undecodable || !(f.Video == "h264" || (f.Video == "hevc" && q.hevc))) {
		why = append(why, f.Video+" video is re-encoded")
	}
	return strings.Join(why, ", ")
}

// cachedProbe is the probe of a file when its header was read (nil otherwise).
func (s *Service) cachedProbe(hash string, index int) *tsProbe {
	if v, ok := s.probeCache.Load(s.probeKey(hash, index)); ok {
		return v.(*tsProbe)
	}
	return nil
}

// capsMiss: why r does not fit q's client, from its probe when cached, else from the result.
func (s *Service) capsMiss(r *tsResolveResult, q tsResolveQuery) string {
	if p := s.cachedProbe(r.Hash, r.Index); p != nil && p.video() != nil {
		return tsCapsMiss(p, q)
	}
	return tsStoredCapsMiss(r, q)
}

// applyProbedCaps marks a checked result unfit (overCaps) when its probed video breaks the
// client's caps, or, without a probe, when its name is above the cap (fallback checks);
// tsChoose then takes it only when no fitting check is left.
func (s *Service) applyProbedCaps(r *tsResolveResult, q tsResolveQuery) {
	why := ""
	if p := s.cachedProbe(r.Hash, r.Index); p != nil && p.video() != nil {
		why = tsCapsMiss(p, q)
		r.reencode = !p.copiesVideo(q.hevc)
	} else if q.maxQ > 0 && r.nameQ > q.maxQ {
		why = fmt.Sprintf("%s above the %dp cap by its name", tsQualityLabel(r.nameQ), q.maxQ)
	}
	if why == "" {
		return
	}
	r.overCaps = true
	if r.unfit == "" {
		r.unfit = why
	}
}

// tsNameUnfit: the release name already says the file breaks q's caps (above maxq, or video
// play re-encodes for the client, see tsNameReencode).
func tsNameUnfit(c tsCandidate, q tsResolveQuery) bool {
	return (q.maxQ > 0 && c.quality > q.maxQ) || tsNameReencode(c, q)
}

// tsNameReencode: the name says play re-encodes the video for q's client: HEVC or a 4K
// without H.264 for a client without HEVC, AV1, Hi10P / 4:4:4.
func tsNameReencode(c tsCandidate, q tsResolveQuery) bool {
	lower := strings.ToLower(c.raw.Title)
	h264 := tsH264Pattern.MatchString(lower)
	switch {
	case c.tags.Undecodable, tsAV1Pattern.MatchString(lower):
		return true
	case !q.hevc && tsHEVCPattern.MatchString(lower) && !h264:
		return true
	}
	return !q.hevc && c.quality == 2160 && !h264
}

// tsReencodeCost puts any re-encode after every copy (a file that is only above the cap
// costs no transcode); within each the lower resolution is cheaper.
const tsReencodeCost = 10000

// tsFallbackCost orders unfit candidates by their names (an unknown resolution may be 4K).
func tsFallbackCost(c tsCandidate, q tsResolveQuery) int {
	cost := c.quality
	if cost == 0 {
		cost = 2160
	}
	if tsNameReencode(c, q) {
		cost += tsReencodeCost
	}
	return cost
}

// tsPlayCost is tsFallbackCost of a checked result (its probed resolution and video).
func tsPlayCost(r *tsResolveResult) int {
	cost := tsQualityHeight(r.Quality)
	if cost == 0 {
		cost = 2160
	}
	if r.reencode {
		cost += tsReencodeCost
	}
	return cost
}

// fallbackCandidates are the candidates resolve checks when the first checks found nothing
// that fits the client: the ranked ones not checked yet whose name may fit, then those whose
// name says they do not (names above maxq included), cheapest first. At most
// tsResolveMaxCheck.
func (s *Service) fallbackCandidates(q tsResolveQuery, raw []JackettResult, checked []tsCandidate,
	items map[string]*torbox.CachedItem) []tsCandidate {
	q.overCap = true
	all := tsRankAll(raw, q)
	if len(items) > 0 && !s.debrid.settings().PreferDisabled {
		cached := func(c tsCandidate) bool { return items[strings.ToLower(c.hash)] != nil && !tsAvoided(c, q) }
		s.debridLineFit(all, cached, q)
		tsDebridBoost(all, cached)
		tsSortCandidates(all, q)
	}
	var fit, unfit []tsCandidate
	for _, c := range all {
		if tsAvoided(c, q) || tsCheckedAlready(c, checked) {
			continue
		}
		if tsNameUnfit(c, q) {
			unfit = append(unfit, c)
		} else {
			fit = append(fit, c)
		}
	}
	sort.SliceStable(unfit, func(i, j int) bool { return tsFallbackCost(unfit[i], q) < tsFallbackCost(unfit[j], q) })
	out := append(fit, unfit...)
	if len(out) > tsResolveMaxCheck {
		out = out[:tsResolveMaxCheck]
	}
	return out
}

// tsCheckedAlready: c is one of the checked candidates (same hash, or the same name and size).
func tsCheckedAlready(c tsCandidate, checked []tsCandidate) bool {
	for _, k := range checked {
		if (c.hash != "" && c.hash == k.hash) || (c.raw.Title == k.raw.Title && c.raw.Size == k.raw.Size) ||
			(tsStem(c.raw.Title) == tsStem(k.raw.Title) && tsSameSize(c.raw.Size, k.raw.Size)) {
			return true
		}
	}
	return false
}

// tsCheaperFallback: unfit a is the better fallback than unfit b: a good one, then an alive
// one, then the cheaper one to play (tsPlayCost: a copy before a re-encode, then the lower
// resolution).
func tsCheaperFallback(a, b *tsResolveResult) bool {
	if a.good() != b.good() {
		return a.good()
	}
	if a.alive() != b.alive() {
		return a.alive()
	}
	return tsPlayCost(a) < tsPlayCost(b)
}

// tsFallbackWins: the pick of the extra checks (b) replaces the first pick (a, unfit or nil):
// a fitting one that is good, or, when a is not good either, the more playable one (a dead
// swarm never replaces one that delivers); else the cheaper fallback.
func tsFallbackWins(b, a *tsResolveResult) bool {
	switch {
	case b == nil:
		return false
	case a == nil:
		return true
	case b.unfit == "":
		return b.good() || (!a.good() && tsChoosePlayable([]*tsResolveResult{a, b}) == 1)
	case !b.overCaps:
		return false
	}
	return a.overCaps && tsCheaperFallback(b, a)
}

// tsCheapest hides the unfit results that cost more to play than the cheapest good one that
// breaks the caps (tsPlayCost), so the fallback is the cheapest one.
func tsCheapest(results []*tsResolveResult) []*tsResolveResult {
	low := math.MaxInt
	for _, r := range results {
		if r != nil && r.overCaps && r.good() {
			low = min(low, tsPlayCost(r))
		}
	}
	if low == math.MaxInt {
		return results
	}
	out := make([]*tsResolveResult, len(results))
	for i, r := range results {
		if r != nil && !(r.overCaps && tsPlayCost(r) > low) {
			out[i] = r
		}
	}
	return out
}

// capsFallback runs when the checks of findSource found nothing that fits the client (res is
// unfit or nil): it checks up to tsResolveMaxCheck more candidates (see fallbackCandidates)
// and returns the pick and seed to use, the first ones when the extra checks did no better.
func (s *Service) capsFallback(ctx context.Context, q tsResolveQuery, res *tsResolveResult, seed *tsUpgradeSeed, items map[string]*torbox.CachedItem) (*tsResolveResult, *tsUpgradeSeed) {
	more := s.fallbackCandidates(q, seed.raw, seed.cands, items)
	if len(more) == 0 {
		return res, seed
	}
	why := "no candidate could be played"
	if res != nil {
		why = fmt.Sprintf("%q does not fit (%s)", res.TorrentTitle, res.unfit)
	}
	log.Printf("resolve %q%s: %s, checking %d more candidates for one that fits", q.title, tsResolveCaps(q), why, len(more))
	tsLogRanking(q, more)
	pick, warmCtx, warmCancel := s.runChecks(ctx, q, more, items, seed.base)
	for j, err := range pick.errs {
		if err != nil && !pick.kept[j] {
			log.Printf("resolve %q: candidate %q rejected: %v", q.title, more[j].raw.Title, err)
		}
	}
	drop := func(p *tsPick, cancel context.CancelFunc) {
		cancel()
		for _, r := range p.results {
			if r != nil {
				s.dropFrom(q.warm, r.Hash)
			}
		}
	}
	if !tsFallbackWins(pick.res, res) {
		drop(pick, warmCancel)
		return res, seed
	}
	if seed.pick.res != nil {
		s.dropFrom(q.warm, seed.pick.res.Hash)
	}
	drop(seed.pick, seed.cancel)
	next := *seed
	next.cands, next.pick, next.ctx, next.cancel = more, pick, warmCtx, warmCancel
	return pick.res, &next
}

// cachedFor is cachedSource for q's client: a cached source that breaks its caps (stored
// before the probe told, or by an older version) is dropped and resolved again, unless it
// was stored as the fallback for exactly these caps (kept tsFallbackTTL).
func (s *Service) cachedFor(key string, q tsResolveQuery) (*tsResolveResult, bool) {
	res, ok := s.cachedSource(key)
	if !ok || res.CapsFallback != "" {
		return res, ok
	}
	if why := s.capsMiss(res, q); why != "" {
		log.Printf("resolve %q%s: cached source %q breaks the client's caps (%s), resolving again", q.title, tsResolveCaps(q),
			res.TorrentTitle, why)
		s.forgetSource(key)
		return nil, false
	}
	return res, true
}

// tsSourceTTLOf is how long a resolved source stays cached.
func tsSourceTTLOf(res *tsResolveResult) time.Duration {
	if res.CapsFallback != "" {
		return tsFallbackTTL
	}
	return tsSourceTTL
}
