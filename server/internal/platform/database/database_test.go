package database

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"gorm.io/gorm"
)

func TestPragmasOnEveryConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	ctx := context.Background()
	// Hold several connections at once so the pool has to open new ones
	for i := 0; i < 4; i++ {
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var temp, mmap int
		var mode string
		if err := conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&temp); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&mmap); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if temp != 2 || mmap != 268435456 || mode != "wal" {
			t.Fatalf("conn %d: temp_store=%d mmap_size=%d journal_mode=%s", i, temp, mmap, mode)
		}
	}
}

type counter struct {
	ID int `gorm:"primaryKey"`
	N  int
}

// Concurrent read-then-write transactions must neither fail nor lose updates.
func TestReadThenWriteTransactions(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, &counter{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&counter{ID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				var c counter
				if err := tx.First(&c, 1).Error; err != nil {
					return err
				}
				return tx.Model(&counter{}).Where("id = 1").Update("n", c.N+1).Error
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("transaction failed: %v", err)
		}
	}
	var c counter
	db.First(&c, 1)
	if c.N != workers {
		t.Fatalf("n = %d, want %d (lost updates)", c.N, workers)
	}
}
