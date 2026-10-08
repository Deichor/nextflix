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
// and were not checked yet (copies of a checked torrent included).
func tsLateBetter(raw []JackettResult, q tsResolveQuery, checked []tsCandidate, pick tsCandidate) []tsCandidate {
	var out []tsCandidate
	for _, c := range tsRankAll(raw, q) {
		if len(out) >= tsLateMax || c.score <= pick.score || tsAvoided(c, q) {
			break
		}
		seen := false
		for _, k := range checked {
			if (c.hash != "" && c.hash == k.hash) || (c.raw.Title == k.raw.Title && c.raw.Size == k.raw.Size) ||
				(tsStem(c.raw.Title) == tsStem(k.raw.Title) && tsSameSize(c.raw.Size, k.raw.Size)) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, c)
		}
	}
	return out
}

// mergeLate waits for the indexers still searching after a pick and checks better
// candidates among their results (see above).
func (s *Service) mergeLate(key string, q tsResolveQuery, pick *tsResolveResult, seed *tsUpgradeSeed) {
	if seed == nil || seed.late == nil || q.upgradeFrom > 0 {
		return
	}
	q.names, q.origLang = seed.q.names, seed.q.origLang // late results are matched against the other names too
	q.seasonEps, q.absEp = seed.q.seasonEps, seed.q.absEp
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
	better := tsLateBetter(raw, q, seed.cands, seed.cands[seed.pick.best])
	log.Printf("resolve %q: %d late results, %d better candidates", q.title, got, len(better))
	if len(better) == 0 {
		return
	}

	floor := tsQualityHeight(pick.Quality)
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
			r, err := s.checkCandidate(cctx, c, q, seed.base, tsWarmWindowTop, nil, nil)
			if err != nil {
				log.Printf("resolve %q: late candidate %q rejected: %v", q.title, c.raw.Title, err)
				return
			}
			used := false
			switch {
			case tsQualityHeight(r.Quality) > floor && upgradable:
				if up := s.keepWarming(cctx, q, *r, nil); up != nil && tsQualityHeight(up.Quality) > floor {
					used = s.offerUpgrade(key, q, up)
				}
			case r.good() && tsQualityHeight(r.Quality) >= floor:
				// Same (or, for a capped client, the only playable) resolution: next time's
				// source, unless something replaced the pick meanwhile
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
