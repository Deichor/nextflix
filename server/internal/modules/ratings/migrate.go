package ratings

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const myListMigration = "mylist_to_favorites_v1"

// MigrationResult summarizes the My List -> Favorites copy.
type MigrationResult struct {
	AlreadyDone bool
	Read        int // rows in my_list_items
	Migrated    int // distinct titles written as favorites
	Unmapped    int // media ids without the "9"+tmdbId convention or unknown media types
	Orphaned    int // rows of profiles that no longer exist
}

type myListRow struct {
	UserID    uint
	MediaID   uint64
	MediaType string
	AddedAt   time.Time
}

// mapMyListItem turns the old My List key ("9"+tmdbId, media_type) into a TMDB key.
func mapMyListItem(mediaID uint64, mediaType string) (string, int, bool) {
	s := strconv.FormatUint(mediaID, 10)
	if len(s) < 2 || s[0] != '9' {
		return "", 0, false
	}
	id, err := strconv.Atoi(s[1:])
	if err != nil || id <= 0 {
		return "", 0, false
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "", "movie":
		return "movie", id, true
	case "tv", "series", "show":
		return "tv", id, true
	}
	return "", 0, false
}

// MigrateMyList copies my_list_items into title_ratings as favorites once.
// my_list_items is left untouched; a marker row makes reruns a no-op.
func (s *Service) MigrateMyList() (MigrationResult, error) {
	var res MigrationResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var marker dataMigration
		err := tx.Where("name = ?", myListMigration).First(&marker).Error
		if err == nil {
			res.AlreadyDone = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if tx.Migrator().HasTable("my_list_items") {
			if err := copyMyList(tx, &res); err != nil {
				return err
			}
		}
		return tx.Create(&dataMigration{Name: myListMigration, DoneAt: time.Now()}).Error
	})
	return res, err
}

func copyMyList(tx *gorm.DB, res *MigrationResult) error {
	var rows []myListRow
	if err := tx.Table("my_list_items").Select("user_id, media_id, media_type, added_at").
		Order("added_at ASC").Scan(&rows).Error; err != nil {
		return err
	}
	res.Read = len(rows)

	var ids []uint
	if err := tx.Table("profiles").Pluck("id", &ids).Error; err != nil {
		return err
	}
	profiles := make(map[uint]bool, len(ids))
	for _, id := range ids {
		profiles[id] = true
	}

	seen := map[string]bool{}
	for _, r := range rows {
		if !profiles[r.UserID] {
			res.Orphaned++
			continue
		}
		mediaType, tmdbID, ok := mapMyListItem(r.MediaID, r.MediaType)
		if !ok {
			res.Unmapped++
			continue
		}
		key := strconv.FormatUint(uint64(r.UserID), 10) + ":" + mediaType + ":" + strconv.Itoa(tmdbID)
		if seen[key] {
			continue
		}
		seen[key] = true
		added := r.AddedAt
		if added.IsZero() {
			added = time.Now()
		}
		row := TitleRating{
			ProfileID: r.UserID, MediaType: mediaType, TmdbID: tmdbID,
			Favorite: true, FavoritedAt: &added, CreatedAt: added, UpdatedAt: added,
		}
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "profile_id"}, {Name: "media_type"}, {Name: "tmdb_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"favorite":     true,
				"favorited_at": gorm.Expr("COALESCE(title_ratings.favorited_at, excluded.favorited_at)"),
			}),
		}).Create(&row).Error
		if err != nil {
			return err
		}
		res.Migrated++
	}
	return nil
}
