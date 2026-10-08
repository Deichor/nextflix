package ratings

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE profiles (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO profiles (id, name) VALUES (1, 'Home'), (2, 'Kids')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	return db
}

func str(s string) *string { return &s }
func boolp(b bool) *bool   { return &b }

func TestPutPartialUpdateAndDeleteRule(t *testing.T) {
	s := NewService(testDB(t), nil)
	key := Update{ProfileID: 1, MediaType: "movie", TmdbID: 27205}

	u := key
	u.Title, u.PosterPath, u.RatingSet, u.Rating = "Inception", "/p.jpg", true, str(Love)
	r, err := s.Put(u)
	if err != nil || r.Rating == nil || *r.Rating != Love || r.Favorite {
		t.Fatalf("love: %+v %v", r, err)
	}

	// Only favorite present: rating and title stay
	u = key
	u.Favorite = boolp(true)
	r, err = s.Put(u)
	if err != nil || r.Rating == nil || *r.Rating != Love || !r.Favorite || r.Title != "Inception" || r.PosterPath != "/p.jpg" {
		t.Fatalf("favorite: %+v %v", r, err)
	}

	// Explicit null clears the rating, favorite keeps the row
	u = key
	u.RatingSet = true
	r, err = s.Put(u)
	if err != nil || r.Rating != nil || !r.Favorite {
		t.Fatalf("clear rating: %+v %v", r, err)
	}
	if _, err := s.Item(1, "movie", 27205); err != nil {
		t.Fatalf("row should still exist: %v", err)
	}

	// No rating and not a favorite: the row is deleted
	u = key
	u.Favorite = boolp(false)
	r, err = s.Put(u)
	if err != nil || r.Rating != nil || r.Favorite {
		t.Fatalf("unfavorite: %+v %v", r, err)
	}
	if _, err := s.Item(1, "movie", 27205); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row should be gone, got %v", err)
	}

	// An empty update on a missing title creates nothing
	u = key
	u.Favorite = boolp(false)
	if _, err := s.Put(u); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.List(1, Filter{}); len(rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(rows))
	}
}

func TestPutUnknownProfileAndHook(t *testing.T) {
	s := NewService(testDB(t), nil)
	var changed []uint
	s.OnChange(func(id uint) { changed = append(changed, id) })

	if _, err := s.Put(Update{ProfileID: 9, MediaType: "tv", TmdbID: 1, Favorite: boolp(true)}); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("want ErrUnknownProfile, got %v", err)
	}
	if _, err := s.Put(Update{ProfileID: 2, MediaType: "tv", TmdbID: 1, Favorite: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != 2 {
		t.Fatalf("hook calls: %v", changed)
	}
}

func TestListFiltersAndFavoriteOrder(t *testing.T) {
	db := testDB(t)
	s := NewService(db, nil)
	put := func(id int, rating *string, fav bool) {
		t.Helper()
		u := Update{ProfileID: 1, MediaType: "movie", TmdbID: id, RatingSet: rating != nil, Rating: rating, Favorite: boolp(fav)}
		if _, err := s.Put(u); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	put(1, nil, true)
	put(2, str(Love), false)
	put(3, str(Like), true)
	// Rating a favorite later must not move it in the Favorites order
	put(1, str(Love), true)

	favs, _ := s.List(1, Filter{FavoriteOnly: true})
	if len(favs) != 2 || favs[0].TmdbID != 3 || favs[1].TmdbID != 1 {
		t.Fatalf("favorites order: %+v", favs)
	}
	loved, _ := s.List(1, Filter{Rating: Love})
	if len(loved) != 2 || loved[0].TmdbID != 1 {
		t.Fatalf("loved: %+v", loved)
	}
	if other, _ := s.List(2, Filter{}); len(other) != 0 {
		t.Fatalf("profile 2 should be empty")
	}
}

func TestOptionalRatingJSON(t *testing.T) {
	cases := []struct {
		body  string
		set   bool
		value string
	}{
		{`{"tmdb_id":1}`, false, ""},
		{`{"rating":null}`, true, ""},
		{`{"rating":""}`, true, ""},
		{`{"rating":"like"}`, true, "like"},
	}
	for _, c := range cases {
		var in putInput
		if err := json.Unmarshal([]byte(c.body), &in); err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		got := ""
		if in.Rating.Value != nil {
			got = *in.Rating.Value
		}
		if in.Rating.Set != c.set || got != c.value {
			t.Errorf("%s: set=%v value=%q", c.body, in.Rating.Set, got)
		}
	}
}

func TestMapMyListItem(t *testing.T) {
	cases := []struct {
		id        uint64
		mediaType string
		wantType  string
		wantID    int
		ok        bool
	}{
		{927205, "movie", "movie", 27205, true},
		{91399, "tv", "tv", 1399, true},
		{9550, "", "movie", 550, true},
		{999, "TV", "tv", 99, true},
		{12345, "movie", "", 0, false}, // no "9" prefix
		{9, "movie", "", 0, false},
		{90, "movie", "", 0, false},
		{9550, "episode", "", 0, false},
	}
	for _, c := range cases {
		mt, id, ok := mapMyListItem(c.id, c.mediaType)
		if mt != c.wantType || id != c.wantID || ok != c.ok {
			t.Errorf("mapMyListItem(%d, %q) = %q, %d, %v", c.id, c.mediaType, mt, id, ok)
		}
	}
}

func TestMigrateMyList(t *testing.T) {
	db := testDB(t)
	err := db.Exec(`CREATE TABLE my_list_items (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, media_id INTEGER NOT NULL,
		media_type TEXT, notes TEXT, priority INTEGER DEFAULT 0, added_at DATETIME)`).Error
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []struct {
		user    uint
		media   uint64
		kind    string
		addedAt time.Time
	}{
		{1, 927205, "movie", old},
		{1, 91399, "tv", old.Add(time.Hour)},
		{1, 927205, "movie", old.Add(2 * time.Hour)}, // duplicate
		{1, 12345, "movie", old},                     // not a TMDB list id
		{2, 9550, "movie", old},
		{99, 9550, "movie", old}, // profile no longer exists
	}
	for _, r := range rows {
		if err := db.Exec("INSERT INTO my_list_items (user_id, media_id, media_type, added_at) VALUES (?, ?, ?, ?)",
			r.user, r.media, r.kind, r.addedAt).Error; err != nil {
			t.Fatal(err)
		}
	}

	s := NewService(db, nil)
	res, err := s.MigrateMyList()
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyDone || res.Read != 6 || res.Migrated != 3 || res.Unmapped != 1 || res.Orphaned != 1 {
		t.Fatalf("result: %+v", res)
	}
	favs, _ := s.List(1, Filter{FavoriteOnly: true})
	if len(favs) != 2 || favs[0].MediaType != "tv" || favs[0].TmdbID != 1399 || favs[1].TmdbID != 27205 {
		t.Fatalf("profile 1 favorites: %+v", favs)
	}
	if favs[1].Rating != nil || !favs[1].UpdatedAt.Equal(old) {
		t.Fatalf("migrated row: %+v", favs[1])
	}

	// Removing a migrated favorite sticks: the migration never runs twice
	if _, err := s.Put(Update{ProfileID: 1, MediaType: "movie", TmdbID: 27205, Favorite: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	res, err = s.MigrateMyList()
	if err != nil || !res.AlreadyDone {
		t.Fatalf("second run: %+v %v", res, err)
	}
	if favs, _ := s.List(1, Filter{FavoriteOnly: true}); len(favs) != 1 {
		t.Fatalf("favorite came back: %+v", favs)
	}
	var n int64
	db.Table("my_list_items").Count(&n)
	if n != 6 {
		t.Fatalf("my_list_items must stay untouched, has %d rows", n)
	}
}

func TestMigrateWithoutMyListTable(t *testing.T) {
	s := NewService(testDB(t), nil)
	res, err := s.MigrateMyList()
	if err != nil || res.AlreadyDone || res.Read != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, _ = s.MigrateMyList(); !res.AlreadyDone {
		t.Fatal("marker not written")
	}
}

type fakeLookup map[int]TitleMeta

func (f fakeLookup) Lookup(_ string, id int) (TitleMeta, error) {
	m, ok := f[id]
	if !ok {
		return TitleMeta{}, errors.New("not found")
	}
	return m, nil
}

func TestBackfillKeepsOrderAndValues(t *testing.T) {
	db := testDB(t)
	s := NewService(db, fakeLookup{550: {"Fight Club", "/fc.jpg", "/fcb.jpg"}, 13: {"Forrest Gump", "/fg.jpg", ""}})
	if _, err := s.Put(Update{ProfileID: 1, MediaType: "movie", TmdbID: 550, Favorite: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(Update{ProfileID: 1, MediaType: "movie", TmdbID: 13, Title: "Mine", Favorite: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Item(1, "movie", 550)
	s.Backfill(t.Context())
	fc, _ := s.Item(1, "movie", 550)
	fg, _ := s.Item(1, "movie", 13)
	if fc.Title != "Fight Club" || fc.PosterPath != "/fc.jpg" || !fc.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("fight club: %+v", fc)
	}
	if fg.Title != "Mine" || fg.PosterPath != "/fg.jpg" {
		t.Fatalf("stored title must win: %+v", fg)
	}
}
