package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

const maxBudgetCheckpoints = 1024

// SaveBudgetCheckpoints is best effort metadata work, never trace eviction.
// It waits at most 50ms for the serialized writer; a busy/full store leaves
// volume in memory and the caller marks history persistence unavailable.
func (s *Store) SaveBudgetCheckpoints(ctx context.Context, now time.Time, days int, checkpoints []budget.Checkpoint) error {
	if days < 1 || days > 30 || len(checkpoints) > 2 {
		return errors.New("invalid budget history bounds")
	}
	data := make([][]byte, len(checkpoints))
	for i, c := range checkpoints {
		if err := c.Validate(); err != nil {
			return err
		}
		encoded, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if len(encoded) > 262144 {
			return errors.New("budget checkpoint exceeds size limit")
		}
		data[i] = encoded
	}
	s.writeMu.Lock()
	busy := s.writing || s.closed
	s.writeMu.Unlock()
	if busy {
		return errors.New("budget checkpoint deferred while ingest is busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	var estimate int64
	for _, payload := range data {
		estimate += int64(len(payload))*2 + 8192
	}
	if s.maxBytes > 0 {
		size, err := s.journalSize()
		if err != nil {
			return err
		}
		if size+estimate >= s.maxBytes-s.maxBytes/10 {
			return storage.ErrCapacity
		}
	}
	return s.withinCapacity(ctx, func(tx *sql.Tx) error {
		if s.maxBytes > 0 {
			var used int64
			if err := tx.QueryRowContext(ctx, "SELECT (page_count-freelist_count)*page_size FROM pragma_page_count(),pragma_freelist_count(),pragma_page_size()").Scan(&used); err != nil {
				return err
			}
			if used+estimate > s.maxBytes-s.maxBytes/10 {
				return storage.ErrCapacity
			}
		}
		cutoff := now.UTC().Truncate(time.Hour).Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
		if _, err := tx.ExecContext(ctx, "DELETE FROM budget_checkpoints WHERE window_ns < ?", cutoff); err != nil {
			return err
		}
		for i, c := range checkpoints {
			if c.Start.UnixNano() < cutoff {
				continue
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO budget_checkpoints(session_id,window_ns,saved_ns,payload) VALUES(?,?,?,?) ON CONFLICT(session_id,window_ns) DO UPDATE SET saved_ns=excluded.saved_ns,payload=excluded.payload`, c.Session, c.Start.UnixNano(), now.UnixNano(), string(data[i]))
			if err != nil {
				return err
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM budget_checkpoints").Scan(&count); err != nil {
			return err
		}
		if excess := count - maxBudgetCheckpoints; excess > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM budget_checkpoints WHERE (session_id,window_ns) IN (SELECT session_id,window_ns FROM budget_checkpoints ORDER BY window_ns,saved_ns,session_id LIMIT ?)`, excess); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE budget_history_meta SET discarded_checkpoints=discarded_checkpoints+? WHERE id=1", excess); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ReadBudgetHistory(ctx context.Context, hour time.Time, excludeSession string, visit func(budget.Checkpoint) error) (int64, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var discarded int64
	if err := tx.QueryRowContext(ctx, "SELECT discarded_checkpoints FROM budget_history_meta WHERE id=1").Scan(&discarded); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM budget_checkpoints WHERE window_ns=? AND session_id<>? ORDER BY saved_ns,session_id", hour.UnixNano(), excludeSession)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return 0, err
		}
		var c budget.Checkpoint
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			rows.Close()
			return 0, err
		}
		if err := visit(c); err != nil {
			rows.Close()
			return 0, err
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	return discarded, tx.Commit()
}
