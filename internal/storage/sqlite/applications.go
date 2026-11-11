package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"geko/internal/investigation"
)

func (s *Store) ApplicationNames(ctx context.Context) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `
        SELECT service_namespace AS name FROM spans WHERE service_namespace <> ''
        UNION SELECT application FROM service_applications
        UNION SELECT application FROM trace_applications
        ORDER BY name LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *Store) ServiceApplications(ctx context.Context, after string) ([]investigation.ServiceApplication, error) {
	rows, err := s.read.QueryContext(ctx, `
        SELECT service_name, application FROM service_applications
        WHERE service_name > ? ORDER BY service_name LIMIT 26`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []investigation.ServiceApplication
	for rows.Next() {
		var rule investigation.ServiceApplication
		if err := rows.Scan(&rule.Service, &rule.Application); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) SetServiceApplication(ctx context.Context, service, application string) error {
	if application == "" {
		_, err := s.db.ExecContext(ctx, "DELETE FROM service_applications WHERE service_name = ?", service)
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
	_, err = tx.ExecContext(ctx, `
        INSERT INTO service_applications (service_name, application) VALUES (?, ?)
        ON CONFLICT (service_name) DO UPDATE SET application = excluded.application`, service, application)
	if err != nil {
		return writeError(err)
	}
	return writeError(tx.Commit())
}

// AssignTraceApplication assigns or clears a label for unnamespaced spans in
// traces last received in [from, to). It does not change stored telemetry.
func (s *Store) AssignTraceApplication(ctx context.Context, from, to time.Time, service, application string) (int64, error) {
	if !s.assignment.TryLock() {
		return 0, investigation.ErrAssignmentInProgress
	}
	defer s.assignment.Unlock()
	scope := assignmentScope{from.UnixNano(), to.UnixNano(), service, application}
	for {
		changed, done, err := s.assignTraceBatch(ctx, scope)
		if err != nil || done {
			return changed, err
		}
	}
}

type assignmentScope struct {
	from, to             int64
	service, application string
}

const assignmentBatchSize = 225

func (s *Store) assignTraceBatch(ctx context.Context, scope assignmentScope) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var saved assignmentScope
	after := []byte{}
	var total int64
	err = tx.QueryRowContext(ctx, `SELECT from_ns, to_ns, service, application, last_trace_id, changed
        FROM assignment_progress WHERE id = 1`).Scan(&saved.from, &saved.to, &saved.service, &saved.application, &after, &total)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return 0, false, err
	case scope != saved:
		return 0, false, investigation.ErrAssignmentInProgress
	}

	ids, err := assignmentCandidates(ctx, tx, scope, after)
	if err != nil {
		return total, false, err
	}
	if len(ids) != 0 {
		changed, err := s.applyAssignment(ctx, tx, scope.application, ids)
		if err != nil {
			return total, false, err
		}
		total += changed
	}

	done := len(ids) < assignmentBatchSize
	if done {
		_, err = tx.ExecContext(ctx, "DELETE FROM assignment_progress WHERE id = 1")
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO assignment_progress
            (id, from_ns, to_ns, service, application, last_trace_id, changed)
            VALUES (1, ?, ?, ?, ?, ?, ?)
            ON CONFLICT (id) DO UPDATE SET last_trace_id = excluded.last_trace_id, changed = excluded.changed`,
			scope.from, scope.to, scope.service, scope.application, ids[len(ids)-1], total)
	}
	if err != nil {
		return 0, false, writeError(err)
	}
	return total, done, writeError(tx.Commit())
}

func assignmentCandidates(ctx context.Context, tx *sql.Tx, scope assignmentScope, after []byte) ([][]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.trace_id FROM traces t
        WHERE t.trace_id > ? AND t.last_observed_ns >= ? AND t.last_observed_ns < ?
        AND EXISTS (SELECT 1 FROM spans s WHERE s.trace_id = t.trace_id
            AND s.service_namespace = '' AND (? = '' OR s.service_name = ?))
        ORDER BY t.trace_id LIMIT ?`, after, scope.from, scope.to, scope.service, scope.service, assignmentBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) applyAssignment(ctx context.Context, tx *sql.Tx, application string, ids [][]byte) (int64, error) {
	matches := "trace_id IN (?" + strings.Repeat(",?", len(ids)-1) + ")"
	query := "DELETE FROM trace_applications WHERE " + matches
	args := make([]any, 0, len(ids)+1)
	if application != "" {
		if err := s.checkWriteCapacity(ctx, tx); err != nil {
			return 0, err
		}
		query = `INSERT INTO trace_applications (trace_id, application)
            SELECT trace_id, ? FROM traces WHERE ` + matches + `
            ON CONFLICT (trace_id) DO UPDATE SET application = excluded.application
            WHERE trace_applications.application <> excluded.application`
		args = append(args, application)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, writeError(err)
	}
	return result.RowsAffected()
}

// DiscardAssignmentProgress leaves changes from completed batches in place.
func (s *Store) DiscardAssignmentProgress(ctx context.Context) error {
	if !s.assignment.TryLock() {
		return investigation.ErrAssignmentInProgress
	}
	defer s.assignment.Unlock()
	_, err := s.db.ExecContext(ctx, "DELETE FROM assignment_progress WHERE id = 1")
	return err
}
