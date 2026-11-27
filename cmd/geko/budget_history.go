package main

import (
	"context"
	"log"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func checkpointBudget(ctx context.Context, db *sqlite.Store, tracker *budget.Tracker) {
	days := tracker.HistoryDays()
	if days == 0 {
		return
	}
	now := time.Now()
	err := db.SaveBudgetCheckpoints(ctx, now, days, tracker.Checkpoints(now))
	tracker.CheckpointResult(now, err)
	if err != nil && ctx.Err() == nil {
		log.Printf("budget history checkpoint skipped: %v", err)
	}
}

func startBudgetHistory(ctx context.Context, db *sqlite.Store, tracker *budget.Tracker) func() {
	running, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(budget.CheckpointInterval)
		defer ticker.Stop()
		for {
			select {
			case <-running.Done():
				return
			case <-ticker.C:
				checkpointBudget(running, db, tracker)
			}
		}
	}()
	return func() {
		cancel()
		<-done
		// The caller defers this until after HTTP/gRPC graceful shutdown, so
		// accepted in-flight exports are included. Capacity/busy failures remain
		// best effort; they do not change the trace commit acknowledgement.
		final, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		checkpointBudget(final, db, tracker)
	}
}
