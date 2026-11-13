package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type spanWrite struct {
	ctx        context.Context
	observedNS int64
	spans      []telemetry.Span
	done       chan error
}

// WriteSpans waits for the commit result. The caller retains ownership of spans.
func (s *Store) WriteSpans(ctx context.Context, observed time.Time, spans []telemetry.Span) error {
	if len(spans) == 0 {
		return nil
	}
	select {
	case s.writeSlots <- struct{}{}:
		defer func() { <-s.writeSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	req := spanWrite{ctx: ctx, observedNS: observed.UnixNano(), spans: spans, done: make(chan error, 1)}
	s.writeMu.Lock()
	if s.closed {
		s.writeMu.Unlock()
		return sql.ErrConnDone
	}
	s.pending = append(s.pending, req)
	if !s.writing {
		s.writing = true
		s.writer.Add(1)
		go s.drainWrites()
	}
	s.writeMu.Unlock()
	return <-req.done
}

func (s *Store) drainWrites() {
	defer s.writer.Done()
	for {
		// Exports that arrive during a commit are merged into the next batch.
		s.writeMu.Lock()
		batch := s.pending
		s.pending = nil
		if len(batch) == 0 {
			s.writing = false
			s.writeMu.Unlock()
			return
		}
		s.writeMu.Unlock()
		s.writeBatch(batch)
	}
}

func (s *Store) writeBatch(batch []spanWrite) {
	// Answer every waiting export and keep the queue running if a write panics.
	answered := 0
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("write traces: internal error: %v", recovered)
			for _, req := range batch[answered:] {
				req.done <- err
			}
		}
	}()
	if len(batch) > 1 && s.commitExports(batch) == nil {
		for _, req := range batch {
			req.done <- nil
			answered++
		}
		s.changed(nil)
		return
	}
	// Keep validation and capacity eviction independent when a shared write fails.
	for _, req := range batch {
		err := req.ctx.Err()
		if err == nil {
			err = s.writeExport(req.ctx, req.observedNS, req.spans)
		}
		req.done <- err
		answered++
	}
}

func (s *Store) commitExports(batch []spanWrite) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, req := range batch {
		if err := req.ctx.Err(); err != nil {
			return err
		}
		stop := context.AfterFunc(req.ctx, cancel)
		defer stop()
	}
	if err := s.checkJournalCapacity(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.checkWriteCapacity(ctx, tx); err != nil {
		return err
	}
	for _, req := range batch {
		if err := insertSpanBatch(ctx, tx, req.observedNS, req.spans); err != nil {
			return err
		}
	}
	for _, req := range batch {
		if err := req.ctx.Err(); err != nil {
			return err
		}
	}
	return writeError(tx.Commit())
}
