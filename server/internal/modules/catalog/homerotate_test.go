package catalog

import (
	"testing"
	"time"

	"nextflix/internal/platform/tmdb"
)

func moviePage(from int) []tmdb.Movie {
	out := make([]tmdb.Movie, 20)
	for i := range out {
		out[i] = tmdb.Movie{ID: from + i}
	}
	return out
}

func TestRotateHomeRow(t *testing.T) {
	pages := func() [][]tmdb.Movie { return [][]tmdb.Movie{moviePage(0), moviePage(20), moviePage(40)} }
	day := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	a := rotateHomeRow("comedies", day, pages())
	if len(a) != homeRowSize {
		t.Fatalf("row size %d", len(a))
	}
	seen := map[int]bool{}
	for _, m := range a {
		if seen[m.ID] {
			t.Fatalf("title %d twice", m.ID)
		}
		seen[m.ID] = true
	}
	same := rotateHomeRow("comedies", day.Add(10*time.Hour), pages())
	for i := range a {
		if a[i].ID != same[i].ID {
			t.Fatalf("the row changed within a day at %d", i)
		}
	}
	next := rotateHomeRow("comedies", day.AddDate(0, 0, 1), pages())
	overlap := 0
	for _, m := range next {
		if seen[m.ID] {
			overlap++
		}
	}
	if overlap > 14 {
		t.Fatalf("%d of %d cards stayed the next day", overlap, homeRowSize)
	}
	// Another page failed: what arrived still makes a row, duplicates across pages count once
	few := rotateHomeRow("comedies", day, [][]tmdb.Movie{moviePage(0), moviePage(10)})
	if len(few) != homeRowSize {
		t.Fatalf("row of two overlapping pages: %d", len(few))
	}
	if got := rotateHomeRow("comedies", day, nil); len(got) != 0 {
		t.Fatalf("no pages: %d", len(got))
	}
}
