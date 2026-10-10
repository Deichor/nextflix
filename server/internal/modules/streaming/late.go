package streaming

import (
	"context"
	"log"
	"sync"
	"time"
)

// Results of slow indexers (1337x through FlareSolverr) arrive after resolve has picked. A
// better candidate among them is checked in the background: a higher resolution feeds the
// upgrade path (the player switches to it), one of the same resolution becomes the cached
// source for the next play and a spare for a sidegrade.

const (
	tsLateMax       = 2                // late candidates checked per resolve
	tsLateCheckTime = 45 * time.Second // ... and how long they may take
)

// tsLateBetter returns up to tsLateMax candidates of all results that rank above the pick
// and were not checked yet (copies of a checked torrent included). After a pick that breaks
// the client's caps (anyFit) every unchecked one whose name may fit counts, whatever its rank.
func tsLateBetter(raw []JackettResult, q tsResolveQuery, checked []tsCandidate, pick tsCandidate, anyFit bool) []tsCandidate {
	var out []tsCandidate
	for _, c := range tsRankAll(raw, q) {
		if len(out) >= tsLateMax || (c.score <= pick.score && !anyFit) || tsAvoided(c, q) {
			break
		}
		if !tsCheckedAlready(c, checked) && !(anyFit && tsNameUnfit(c, q)) {
			out = append(out, c)
		}
	}
	return out
}

// tsLateQuery is the query late candidates are ranked and checked with: the one the search
// left in the seed (other names, episode numbers, runtime for the duration check), with the
// job's warm state.
func tsLateQuery(q tsResolveQuery, seed *tsUpgradeSeed) tsResolveQuery {
	late := seed.q
	late.warm = q.warm
	return late
}

// mergeLate waits for the indexers still searching after a pick and checks better
// candidates among their results (see above).
func (s *Service) mergeLate(key string, q tsResolveQuery, pick *tsResolveResult, seed *tsUpgradeSeed) {
	if seed == nil || seed.late == nil || q.upgradeFrom > 0 {
		return
	}
	q = tsLateQuery(q, seed)
	ctx := q.warmCtx()
	raw := append([]JackettResult(nil), seed.raw...)
	got := 0
	for open := true; open; {
		select {
		case batch, ok := <-seed.late:
			if !ok {
				open = false
				break
			}
			raw = append(raw, batch...)
			got += len(batch)
		case <-ctx.Done():
			return
		}
	}
	if got == 0 || seed.pick.best < 0 {
		return
	}
	better := tsLateBetter(raw, q, seed.cands, seed.cands[seed.pick.best], pick.overCaps)
	log.Printf("resolve %q: %d late results, %d better candidates", q.title, got, len(better))
	if len(better) == 0 {
		return
	}

	floor := tsQualityHeight(pick.Quality)
	if pick.overCaps && q.maxQ > 0 {
		floor = min(floor, q.maxQ) // the fallback is above the cap: one at the cap replaces it
	}
	upgradable := q.hevc && q.maxQ == 0
	// They are background work: they wait while a stream starts
	bg := tsBackground(ctx)
	if !s.waitQuiet(bg, "", "late candidate checks") {
		return
	}
	cctx, cancel := context.WithTimeout(bg, tsLateCheckTime)
	defer cancel()
	var wg sync.WaitGroup
	for _, c := range better {
		wg.Add(1)
		go func(c tsCandidate) {
			defer wg.Done()
			defer tsCatch("late candidate check", func(error) {})
			r, err := s.checkCandidate(cctx, c, q, seed.base, tsWarmWindowTop, nil, nil)
			if err != nil {
				log.Printf("resolve %q: late candidate %q rejected: %v", q.title, c.raw.Title, err)
				return
			}
			used := false
			switch {
			case r.unfit != "":
				log.Printf("resolve %q: late candidate %q does not fit the client: %s", q.title, c.raw.Title, r.unfit)
			case tsQualityHeight(r.Quality) > floor && upgradable:
				if up := s.keepWarming(cctx, q, *r, nil); up != nil && tsQualityHeight(up.Quality) > floor {
					used = s.offerUpgrade(key, q, up)
				}
			case r.good() && (tsQualityHeight(r.Quality) >= floor || pick.overCaps):
				// Same (or, for a capped client, the only playable) resolution, or one that fits after
				// a fallback: next time's source, unless something replaced the pick meanwhile
				if cached, ok := s.cachedSource(key); ok && cached.Hash == pick.Hash {
					s.storeSource(key, *r)
					log.Printf("resolve %q: late candidate %q is the source from now on: %s", q.title, c.raw.Title, r.Health)
				}
				used = tsSpareWorthy(r, floor, q) && s.addSpare(q.warm, r)
			}
			if !used {
				s.dropFrom(q.warm, r.Hash)
			}
		}(c)
	}
	wg.Wait()
}
