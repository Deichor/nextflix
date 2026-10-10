package streaming

import "testing"

// Late candidates are checked with what the search learned, the runtime included, so their
// duration is checked like the early ones'.
func TestLateQueryKeepsSearchState(t *testing.T) {
	w := &tsWarm{}
	job := tsResolveQuery{mediaType: "tv", title: "Cukur", season: 4, episode: 1, warm: w}
	searched := job
	searched.warm = nil
	searched.runtime = 130
	searched.names = []string{"Çukur"}
	searched.origLang = "tr"
	searched.imdbID = "tt6560040"
	searched.seasonEps = []int{33, 34, 23, 39}
	searched.absEp = 91
	got := tsLateQuery(job, &tsUpgradeSeed{q: searched})
	if got.runtime != 130 || len(got.names) != 1 || got.origLang != "tr" || got.imdbID != "tt6560040" ||
		got.absEp != 91 || len(got.seasonEps) != 4 || got.warm != w {
		t.Fatalf("late query = %+v", got)
	}
}
