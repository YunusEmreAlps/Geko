package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"geko/internal/storage"

	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	pruneBatchSize          = 128
	maintenanceWALThreshold = 4 << 20
)

const deleteOldest = `DELETE FROM traces WHERE trace_id IN (
    SELECT trace_id FROM traces WHERE last_observed_ns < ? %s
    ORDER BY last_observed_ns, trace_id LIMIT ?
)`

// Prune removes whole traces last observed before cutoff, in short transactions.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) error {
	removed := false
	for {
		result, err := s.db.ExecContext(ctx, fmt.Sprintf(deleteOldest, ""), cutoff.UnixNano(), pruneBatchSize)
		if err != nil {
			return fmt.Errorf("expire traces: %w", err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count expired traces: %w", err)
		}
		removed = removed || deleted > 0
		if deleted < pruneBatchSize {
			break
		}
	}

	if !removed {
		size, err := s.journalSize()
		if err != nil {
			return err
		}
		if size < maintenanceWALThreshold {
			return nil
		}
	}
	_, err := s.truncateJournal(ctx)
	if err != nil {
		return fmt.Errorf("checkpoint trace storage: %w", err)
	}
	// An external reader can defer truncation until the next maintenance run.
	return nil
}

// Compact reclaims free pages in the main file on explicit request.
func (s *Store) Compact(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("compact database: %w", err)
	}
	drained, err := s.truncateJournal(ctx)
	if err == nil && !drained {
		return errors.New("database compacted, but a concurrent connection prevented WAL truncation")
	}
	return err
}

func (s *Store) truncateJournal(ctx context.Context) (drained bool, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// Do not hold the writer waiting on a dashboard snapshot or another reader.
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return false, err
	}
	defer func() {
		_, resetErr := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA busy_timeout = 5000")
		err = errors.Join(err, resetErr)
	}()
	var busy, frames, checkpointed int
	err = conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed)
	return busy == 0 && frames == checkpointed, err
}

func (s *Store) checkWriteCapacity(ctx context.Context, tx *sql.Tx) error {
	if s.maxBytes == 0 {
		return nil
	}
	var pageSize int64
	if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return fmt.Errorf("read storage page size: %w", err)
	}
	// Apply on the writer connection even if database/sql has reopened it.
	maxPages := s.maxBytes / pageSize
	if maxPages == 0 {
		return storage.ErrCapacity
	}
	var effective int64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", maxPages)).Scan(&effective); err != nil {
		return fmt.Errorf("set trace storage limit: %w", err)
	}
	// SQLite floors the limit at the existing page count. Evict mode can
	// continue to reuse that space without growing the main database.
	if effective > maxPages && s.onFull != storage.EvictOldest {
		return storage.ErrCapacity
	}

	return nil
}

func (s *Store) checkJournalCapacity(ctx context.Context) error {
	if s.maxBytes == 0 {
		return nil
	}
	size, err := s.journalSize()
	if err != nil {
		return err
	}
	if size >= s.maxBytes {
		drained, err := s.truncateJournal(ctx)
		if err != nil {
			return err
		}
		if !drained {
			return storage.ErrCapacity
		}
	}
	return nil
}

func (s *Store) journalSize() (int64, error) {
	info, err := os.Stat(s.path + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("measure trace journal: %w", err)
	}
	return info.Size(), nil
}

func writeError(err error) error {
	var sqliteErr *driver.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_FULL {
		return storage.ErrCapacity
	}
	return err
}
