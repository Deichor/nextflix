package notifications

import "testing"

func TestApplyLocal(t *testing.T) {
	n := Notification{
		Type:       TypeTMDBTrending,
		Title:      "Inception",
		TMDBIDs:    []int{1, 2},
		TMDBTitles: []string{"Inception", "Unknown"},
		Overview:   "English",
		Genres:     []string{"Action"},
		MediaDetails: []NotificationMedia{
			{ID: 1, Title: "Inception", Overview: "English", Genres: []string{"Action"}},
			{ID: 2, Title: "Unknown", Overview: "Kept"},
		},
	}
	tr := map[int]localText{1: {title: "Başlangıç", overview: "Türkçe", genres: []string{"Aksiyon"}}}
	got := applyLocal(n, func(id int) (localText, bool) { t, ok := tr[id]; return t, ok })

	if got.Title != "Başlangıç" || got.Overview != "Türkçe" || got.Genres[0] != "Aksiyon" {
		t.Errorf("headline: %q / %q / %v", got.Title, got.Overview, got.Genres)
	}
	if got.TMDBTitles[0] != "Başlangıç" || got.TMDBTitles[1] != "Unknown" {
		t.Errorf("titles: %v", got.TMDBTitles)
	}
	if got.MediaDetails[0].Title != "Başlangıç" || got.MediaDetails[1].Overview != "Kept" {
		t.Errorf("media: %+v", got.MediaDetails)
	}
	// The stored notification is shared and must stay English
	if n.TMDBTitles[0] != "Inception" || n.MediaDetails[0].Title != "Inception" {
		t.Errorf("original modified: %+v", n)
	}

	// A bundle headline ("Coming Soon to Theaters") is not a title and stays
	b := applyLocal(Notification{Title: "Coming Soon to Theaters", TMDBIDs: []int{1}, TMDBTitles: []string{"Inception"}},
		func(id int) (localText, bool) { t, ok := tr[id]; return t, ok })
	if b.Title != "Coming Soon to Theaters" {
		t.Errorf("bundle headline changed: %q", b.Title)
	}
}
