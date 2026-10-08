package streaming

import "testing"

func TestMatchTitleRejectsLongerTitle(t *testing.T) {
	title := tsNormalize("The Pit")
	cases := map[string]bool{
		"Quatermass.and.the.Pit.S01.1080p":            false,
		"The.Pit.S01E01.1080p":                        true,
		"www.Site.com - The Pit S01E01":               true,
		"Rycerz Siedmiu Krolestw - The Pit S01 2160p": true,
		"Return.of.the.Pit.2020.1080p":                false,
	}
	for release, want := range cases {
		if _, ok := tsMatchTitle(tsNormalize(release), title); ok != want {
			t.Errorf("%q: got %v want %v", release, ok, want)
		}
	}
}

func TestClosestPrefersLighterWhenAllDead(t *testing.T) {
	dead := func(q string, need int64) *tsResolveResult {
		return &tsResolveResult{Quality: q, Health: &tsHealth{Dead: true, Need: need}}
	}
	// Ranked 4K first, every swarm dead: the 1080p (lower need) must be the fallback
	results := []*tsResolveResult{dead("2160p", 5_360_000), dead("1080p", 1_400_000)}
	best, _ := tsChooseFit(results, []bool{true, true})
	if best != 1 {
		t.Fatalf("picked #%d (%s), want the 1080p", best, results[best].Quality)
	}
}
