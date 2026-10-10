package database

import (
	"database/sql"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DSN options are applied by the driver to every pooled connection. _txlock=immediate makes
// every transaction take the write lock at BEGIN, so a read-then-write transaction waits
// (busy_timeout) instead of failing with SQLITE_BUSY_SNAPSHOT under WAL.
const dsnOptions = "_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_cache_size=1000000&_txlock=immediate"

// connPragmas have no DSN option and are set by the connect hook on every new connection.
var connPragmas = []string{
	"PRAGMA temp_store = memory",
	"PRAGMA mmap_size = 268435456",
}

// filePragmas are database-wide; page_size and auto_vacuum only matter for a new file.
var filePragmas = []string{
	"PRAGMA page_size = 4096",
	"PRAGMA auto_vacuum = INCREMENTAL",
	"PRAGMA optimize",
}

const driverName = "sqlite3_nextflix"

var registerOnce sync.Once

func registerDriver() {
	registerOnce.Do(func() {
		sql.Register(driverName, &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				for _, p := range connPragmas {
					if _, err := conn.Exec(p, nil); err != nil {
						return err
					}
				}
				return nil
			},
		})
	})
}

// Open opens the SQLite database with the performance pragmas and pool settings.
func Open(url string) (*gorm.DB, error) {
	registerDriver()
	dsn := url
	if strings.Contains(dsn, "?") {
		dsn += "&" + dsnOptions
	} else {
		dsn += "?" + dsnOptions
	}

	db, err := gorm.Open(sqlite.New(sqlite.Config{DriverName: driverName, DSN: dsn}), &gorm.Config{
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

	for _, p := range filePragmas {
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
