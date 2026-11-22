package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Keep these budgets aligned with migration 019. Ordinary project labels stay
// exact; excess labels are visibly aggregated rather than exhausting storage.
const (
	maxEvictionApplications = 512
	maxEvictionLabelBytes   = 512
)

// Read attribution before cascading deletes, then persist counts after deletion
// frees pages. The same write transaction owns both selections, the counters and
// the incoming export. Retention deliberately does not call this function.
func evictSelected(ctx context.Context, tx *sql.Tx, selection string, args ...any) (int64, error) {
	labels, err := tx.QueryContext(ctx, "SELECT application FROM application_evictions")
	if err != nil {
		return 0, err
	}
	tracked := make(map[string]bool)
	for labels.Next() {
		var application string
		if err := labels.Scan(&application); err != nil {
			labels.Close()
			return 0, err
		}
		tracked[application] = true
	}
	err = labels.Err()
	if closeErr := labels.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `WITH victims AS (`+selection+`)
SELECT coalesce(nullif(ts.namespace,''),ta.application,sa.application,''),count(DISTINCT t.trace_id)
FROM victims v JOIN traces t ON t.trace_id=v.trace_id
LEFT JOIN trace_services ts ON ts.trace_id=t.trace_id
LEFT JOIN trace_applications ta ON ta.trace_id=t.trace_id
LEFT JOIN service_applications sa ON sa.service_name=ts.service_name GROUP BY 1`, args...)
	if err != nil {
		return 0, fmt.Errorf("attribute evicted traces: %w", writeError(err))
	}
	type loss struct {
		application string
		traces      int64
	}
	var losses []loss
	var overflow int64
	for rows.Next() {
		var value loss
		if err := rows.Scan(&value.application, &value.traces); err != nil {
			rows.Close()
			return 0, err
		}
		if len(value.application) > maxEvictionLabelBytes || !tracked[value.application] && len(tracked) >= maxEvictionApplications {
			overflow += value.traces
		} else {
			tracked[value.application] = true
			losses = append(losses, value)
		}
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	if len(losses) == 0 && overflow == 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM traces WHERE trace_id IN ("+selection+")", args...)
	if err != nil {
		return 0, fmt.Errorf("evict traces: %w", writeError(err))
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	for _, value := range losses {
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_evictions(application,evicted_traces) VALUES(?,?)
 ON CONFLICT(application) DO UPDATE SET evicted_traces=evicted_traces+excluded.evicted_traces`, value.application, value.traces); err != nil {
			return deleted, fmt.Errorf("record application evictions: %w", writeError(err))
		}
	}
	if overflow > 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE storage_counters SET application_eviction_overflow=application_eviction_overflow+? WHERE id=1", overflow); err != nil {
			return deleted, fmt.Errorf("record application eviction overflow: %w", writeError(err))
		}
	}
	return deleted, nil
}
