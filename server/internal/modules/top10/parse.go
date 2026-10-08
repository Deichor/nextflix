package top10

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// entry is one ranked row of a Netflix Top 10 file.
type entry struct {
	Rank        int
	Title       string // show_title (the series name for TV)
	SeasonTitle string // "Show: Season 2", "N/A" for films
	Hours       int64  // weekly_hours_viewed (global file only)
	Views       int64  // weekly_views (global file only)
}

// weekData is the latest week of one list source.
type weekData struct {
	Week  string
	Films []entry
	TV    []entry
}

type columns map[string]int

const bom = string(rune(0xFEFF))

func readHeader(sc *bufio.Scanner, required ...string) (columns, error) {
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("empty file")
	}
	cols := columns{}
	for i, name := range strings.Split(strings.TrimPrefix(trimLine(sc.Text()), bom), "\t") {
		cols[strings.TrimSpace(name)] = i
	}
	for _, r := range required {
		if _, ok := cols[r]; !ok {
			return nil, fmt.Errorf("missing column %q", r)
		}
	}
	return cols, nil
}

func (c columns) get(fields []string, name string) string {
	i, ok := c[name]
	if !ok || i >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[i])
}

func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	return sc
}

func trimLine(s string) string { return strings.TrimRight(s, "\r") }

func toEntry(cols columns, f []string) (entry, bool) {
	rank, err := strconv.Atoi(cols.get(f, "weekly_rank"))
	if err != nil || rank <= 0 {
		return entry{}, false
	}
	e := entry{Rank: rank, Title: cols.get(f, "show_title"), SeasonTitle: cols.get(f, "season_title")}
	e.Hours, _ = strconv.ParseInt(cols.get(f, "weekly_hours_viewed"), 10, 64)
	e.Views, _ = strconv.ParseInt(cols.get(f, "weekly_views"), 10, 64)
	return e, e.Title != ""
}

// parseCountries streams all-weeks-countries.tsv and keeps only the latest week of each wanted
// country (ISO2 codes, upper case). Weeks are ISO dates, so string order is date order.
func parseCountries(r io.Reader, wanted []string) (map[string]*weekData, error) {
	sc := newScanner(r)
	cols, err := readHeader(sc, "country_iso2", "week", "category", "weekly_rank", "show_title")
	if err != nil {
		return nil, err
	}
	out := map[string]*weekData{}
	marks := make([]string, 0, len(wanted))
	for _, iso := range wanted {
		out[iso] = &weekData{}
		marks = append(marks, "\t"+iso+"\t")
	}
	for sc.Scan() {
		line := sc.Text()
		hit := false
		for _, m := range marks {
			if strings.Contains(line, m) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		f := strings.Split(trimLine(line), "\t")
		wd := out[cols.get(f, "country_iso2")]
		if wd == nil {
			continue
		}
		addRow(wd, cols, f, cols.get(f, "category"))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, wd := range out {
		sortByRank(wd.Films)
		sortByRank(wd.TV)
	}
	return out, nil
}

// globalData keeps the English and non-English lists of the latest global week apart.
type globalData struct {
	Week                string
	FilmsEN, FilmsNonEN []entry
	TVEN, TVNonEN       []entry
}

// parseGlobal streams all-weeks-global.tsv and keeps the latest week.
func parseGlobal(r io.Reader) (*globalData, error) {
	sc := newScanner(r)
	cols, err := readHeader(sc, "week", "category", "weekly_rank", "show_title")
	if err != nil {
		return nil, err
	}
	g := &globalData{}
	for sc.Scan() {
		f := strings.Split(trimLine(sc.Text()), "\t")
		week := cols.get(f, "week")
		if week == "" || week < g.Week {
			continue
		}
		if week > g.Week {
			*g = globalData{Week: week}
		}
		e, ok := toEntry(cols, f)
		if !ok {
			continue
		}
		switch cols.get(f, "category") {
		case "Films (English)":
			g.FilmsEN = append(g.FilmsEN, e)
		case "Films (Non-English)":
			g.FilmsNonEN = append(g.FilmsNonEN, e)
		case "TV (English)":
			g.TVEN = append(g.TVEN, e)
		case "TV (Non-English)":
			g.TVNonEN = append(g.TVNonEN, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return g, nil
}

// addRow adds a row to wd, starting over when it belongs to a newer week.
func addRow(wd *weekData, cols columns, f []string, category string) {
	week := cols.get(f, "week")
	if week == "" || week < wd.Week {
		return
	}
	if week > wd.Week {
		*wd = weekData{Week: week}
	}
	e, ok := toEntry(cols, f)
	if !ok {
		return
	}
	switch category {
	case "Films":
		wd.Films = append(wd.Films, e)
	case "TV":
		wd.TV = append(wd.TV, e)
	}
}

func sortByRank(es []entry) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].Rank < es[j].Rank })
}

// mergeGlobal combines the English and non-English lists into one top 10, ranked by weekly
// views when every row has them, else by hours viewed, else the English list alone.
func mergeGlobal(english, nonEnglish []entry) []entry {
	sortByRank(english)
	sortByRank(nonEnglish)
	all := append(append([]entry{}, english...), nonEnglish...)
	var metric func(e entry) int64
	switch {
	case len(nonEnglish) > 0 && every(all, func(e entry) bool { return e.Views > 0 }):
		metric = func(e entry) int64 { return e.Views }
	case len(nonEnglish) > 0 && every(all, func(e entry) bool { return e.Hours > 0 }):
		metric = func(e entry) int64 { return e.Hours }
	default:
		all = append([]entry{}, english...)
	}
	if metric != nil {
		sort.SliceStable(all, func(i, j int) bool { return metric(all[i]) > metric(all[j]) })
	}
	if len(all) > 10 {
		all = all[:10]
	}
	for i := range all {
		all[i].Rank = i + 1
	}
	return all
}

func every(es []entry, ok func(entry) bool) bool {
	for _, e := range es {
		if !ok(e) {
			return false
		}
	}
	return len(es) > 0
}
