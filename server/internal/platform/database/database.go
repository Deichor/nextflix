package database

import (
	"log"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DSN options are applied by the driver to every pooled connection.
const dsnOptions = "_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_cache_size=1000000"

// pragmas tune the connection; WAL and the vacuum mode persist in the file.
var pragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA cache_size = 1000000",
	"PRAGMA temp_store = memory",
	"PRAGMA mmap_size = 268435456",
	"PRAGMA page_size = 4096",
	"PRAGMA auto_vacuum = INCREMENTAL",
	"PRAGMA busy_timeout = 5000",
	"PRAGMA wal_autocheckpoint = 1000",
	"PRAGMA optimize",
}

// Open opens the SQLite database with the performance pragmas and pool settings.
func Open(url string) (*gorm.DB, error) {
	dsn := url
	if strings.Contains(dsn, "?") {
		dsn += "&" + dsnOptions
	} else {
		dsn += "?" + dsnOptions
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Silent),
		PrepareStmt:                              true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetConnMaxLifetime(time.Hour)

	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			log.Printf("sqlite: %s failed: %v", p, err)
		}
	}
	return db, nil
}

// Migrate auto-migrates the given models. Existing tables and columns are never dropped.
func Migrate(db *gorm.DB, models ...any) error {
	if len(models) == 0 {
		return nil
	}
	return db.AutoMigrate(models...)
}
