package streaming

import (
	"context"
	"errors"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/torbox"
)

// Mislabeled home releases: the "WEB-DL"s of a film still in cinemas can be another video
// of the right name (The Odyssey 2026: Kitsune, QxR, LAMA and YTS "WEB-DL"s were all 86 min
// for a 173 min film, the making-of). When their probed length is wrong, the home-release
// verdict turns back to "not found" (the film is in cinemas again: the 425 gate, the theater
// banner) and their hashes never count as a home release again.

const (
	tsHomeRejectTTL  = 7 * 24 * time.Hour // a rejected hash does not count as a home release for this long
	tsHomeDurMin     = 2                  // wrong-length home releases needed to turn the verdict
	tsHomeProbeMax   = 3                  // TorBox-cached home releases the home check probes
	tsHomeProbeTime  = 20 * time.Second
	tsHomeProbeItems = 10 // hashes asked from TorBox for them
)

// tsHomeMislabeled: no probed home release had the title's length and at least tsHomeDurMin
// had another one (whatever their release groups).
func tsHomeMislabeled(wrong int, fits bool) bool { return !fits && wrong >= tsHomeDurMin }

// rejectedHome returns the hashes of the movie that do not count as a home release.
func (h *tsHome) rejectedHome(tmdbID int) map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	now := time.Now()
	for hash, until := range h.rejected[tmdbID] {
		if now.Before(until) {
			out[hash] = true
		}
	}
	return out
}

// homeFound reports whether the movie's cached verdict says a home release is out.
func (h *tsHome) homeFound(tmdbID int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.cache[tmdbID]
	return ok && v.found && time.Now().Before(v.expires)
}

// demoteHome turns the movie's verdict to "not found" for tsHomeFoundTTL and rejects hashes.
func (s *Service) demoteHome(tmdbID int, title string, hashes []string, why string) {
	h := s.home
	now := time.Now()
	h.mu.Lock()
	h.cache[tmdbID] = tsHomeVerdict{expires: now.Add(tsHomeFoundTTL)}
	if h.rejected[tmdbID] == nil {
		h.rejected[tmdbID] = map[string]time.Time{}
	}
	for _, hash := range hashes {
		h.rejected[tmdbID][hash] = now.Add(tsHomeRejectTTL)
	}
	for id, m := range h.rejected {
		for hash, until := range m {
			if now.After(until) {
				delete(m, hash)
			}
		}
		if len(m) == 0 {
			delete(h.rejected, id)
		}
	}
	h.mu.Unlock()
	log.Printf("home release %q (movie %d): %s; it counts as in cinemas again for %s (%d releases rejected)", title, tmdbID, why,
		tsHomeFoundTTL, len(hashes))
}

// withoutRejectedHome drops the results a length check already rejected for the movie: they
// are another video under its name.
func (s *Service) withoutRejectedHome(q tsResolveQuery, raw []JackettResult) []JackettResult {
	if q.mediaType != "movie" || q.tmdbID <= 0 {
		return raw
	}
	rejected := s.home.rejectedHome(q.tmdbID)
	if len(rejected) == 0 {
		return raw
	}
	out := make([]JackettResult, 0, len(raw))
	for _, r := range raw {
		if !rejected[tsResultHash(r)] {
			out = append(out, r)
		}
	}
	return out
}

// noteHomeDurations feeds the duration checks of a resolve back into the home-release
// verdict: when the movie counts as out only because of its home releases and the checked
// home releases have the wrong length (tsHomeMislabeled), it is in cinemas again. True when
// the verdict turned.
func (s *Service) noteHomeDurations(q tsResolveQuery, cands []tsCandidate, p *tsPick) bool {
	if q.mediaType != "movie" || q.tmdbID <= 0 || q.upgradeFrom > 0 || p == nil || !s.home.homeFound(q.tmdbID) {
		return false
	}
	var wrong []string
	fits := false
	for i, c := range cands {
		if src := tsSourceOf(c.rest); src == "" || src == "cinema" {
			continue
		}
		var de *tsDurationError
		switch {
		case i < len(p.errs) && errors.As(p.errs[i], &de):
			wrong = append(wrong, strings.ToLower(c.hash))
		case i < len(p.results) && p.results[i] != nil && p.results[i].DurationVerified:
			fits = true
		}
	}
	if !tsHomeMislabeled(len(wrong), fits) {
		return false
	}
	s.demoteHome(q.tmdbID, q.title, wrong, "every checked home release has the wrong length")
	return true
}

// probeHomeReleases is the home check's own length check, run in the background after a
// "found" verdict: up to tsHomeProbeMax home releases TorBox has cached are probed through
// the debrid proxy (only their header is read) and compared with the runtime.
func (s *Service) probeHomeReleases(q tsResolveQuery, cands []tsCandidate) {
	if q.runtime <= 0 || !s.debrid.usable() {
		return
	}
	select {
	case s.home.probing <- struct{}{}:
		defer func() { <-s.home.probing }()
	default:
		return // another movie is probed; its own resolve still checks the lengths
	}
	ctx, cancel := context.WithTimeout(context.Background(), tsHomeProbeTime)
	defer cancel()
	var hashes []string
	for _, c := range cands {
		if c.hash != "" && len(hashes) < tsHomeProbeItems {
			hashes = append(hashes, strings.ToLower(c.hash))
		}
	}
	items := s.debrid.cachedItems(ctx, hashes)
	var picked []tsCandidate
	for _, c := range cands {
		if items[strings.ToLower(c.hash)] != nil && len(picked) < tsHomeProbeMax {
			picked = append(picked, c)
		}
	}
	if len(picked) == 0 {
		return
	}
	type outcome struct {
		hash  string
		wrong bool
		fits  bool
	}
	outs := make([]outcome, len(picked))
	var wg sync.WaitGroup
	for i, c := range picked {
		wg.Add(1)
		go func(i int, c tsCandidate) {
			defer wg.Done()
			defer tsCatch("home release length check", func(error) {})
			hash := strings.ToLower(c.hash)
			dur, err := s.probeDebridHeader(ctx, c, q, items[hash])
			if err != nil || dur <= 0 {
				log.Printf("home release %q: length of %q unknown: %v", q.title, c.raw.Title, err)
				return
			}
			wrong := tsDurationMismatch(dur, q.runtime, "movie", tsLongerCut(c.raw.Title))
			log.Printf("home release %q: %q is %dm for a runtime of %dm", q.title, c.raw.Title, int(math.Round(dur/60)), q.runtime)
			outs[i] = outcome{hash: hash, wrong: wrong, fits: !wrong}
		}(i, c)
	}
	wg.Wait()
	var wrong []string
	fits := false
	for _, o := range outs {
		if o.wrong {
			wrong = append(wrong, o.hash)
		}
		fits = fits || o.fits
	}
	if tsHomeMislabeled(len(wrong), fits) && s.home.homeFound(q.tmdbID) {
		s.demoteHome(q.tmdbID, q.title, wrong, "the TorBox-cached home releases have the wrong length")
	}
}

// probeDebridHeader probes the file of a TorBox-cached candidate (its header only, through
// the debrid proxy) and returns its duration in seconds.
func (s *Service) probeDebridHeader(ctx context.Context, c tsCandidate, q tsResolveQuery, it *torbox.CachedItem) (float64, error) {
	if it == nil {
		return 0, errors.New("not cached")
	}
	hash := strings.ToLower(c.hash)
	files := make([]tsFileStat, 0, len(it.Files))
	for i, f := range it.Files {
		files = append(files, tsFileStat{ID: i + 1, Path: f.Name, Length: f.Size})
	}
	file, ok := tsPickFile(files, q, c)
	if !ok {
		return 0, errors.New("no matching video file")
	}
	magnet := c.magnet
	if !strings.HasPrefix(magnet, "magnet:?") {
		magnet = tsBuildMagnet(hash, c.raw.Title, nil)
	}
	s.debrid.register(tsDebridSource{hash: hash, index: file.ID, path: file.Path, size: file.Length, title: c.raw.Title, magnet: magnet})
	if _, err := s.debrid.link(ctx, hash, file.ID, false); err != nil {
		s.debrid.unregister(hash, file.ID)
		return 0, err
	}
	p, err := s.probeFileTimeout(ctx, hash, file.ID, tsHomeProbeTime)
	if err != nil {
		return 0, err
	}
	return p.duration(), nil
}
