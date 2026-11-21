package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

const observeTraces = `
INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns)
`
const updateObservation = `
ON CONFLICT (trace_id) DO UPDATE SET
    first_observed_ns = min(traces.first_observed_ns, excluded.first_observed_ns),
    last_observed_ns = max(traces.last_observed_ns, excluded.last_observed_ns)
`

const insertSpan = `
INSERT INTO spans (
    trace_id, span_id, parent_span_id, service_name, name, kind, start_ns, end_ns,
    status_code, status_description, resource_attributes, scope_name, scope_version,
    scope_attributes, attributes, events
)
`

// Use the stored namespace so it matches the generated column. WHERE true
// separates the SELECT from the UPSERT's ON CONFLICT clause.
const projectServices = `
INSERT INTO trace_services (trace_id, namespace, service_name, span_count)
SELECT spans.trace_id, spans.service_namespace, spans.service_name, batch.span_count
FROM batch JOIN spans ON spans.trace_id = batch.trace_id AND spans.span_id = batch.span_id
WHERE true
ON CONFLICT (trace_id, namespace, service_name) DO UPDATE SET span_count = span_count + excluded.span_count
`

type serviceProjection struct {
	trace     telemetry.TraceID
	namespace string
	service   string
}

type projectedSpans struct {
	sample telemetry.SpanID
	count  int
}

const writeChunkSize = 128

func observeSpanTraces(ctx context.Context, tx *sql.Tx, observedNS int64, spans []telemetry.Span) error {
	seen := make(map[telemetry.TraceID]struct{})
	ids := make([]telemetry.TraceID, 0, len(spans))
	for _, span := range spans {
		if _, exists := seen[span.TraceID]; !exists {
			seen[span.TraceID] = struct{}{}
			ids = append(ids, span.TraceID)
		}
	}
	for chunk := range slices.Chunk(ids, writeChunkSize) {
		args := make([]any, 1, len(chunk)+1)
		args[0] = observedNS
		for i := range chunk {
			args = append(args, chunk[i][:])
		}
		// Reuse parameter ?1 for both timestamps in every row.
		values := "(?2,?1,?1)" + strings.Repeat(",(?,?1,?1)", len(chunk)-1)
		if _, err := tx.ExecContext(ctx, observeTraces+"VALUES "+values+updateObservation, args...); err != nil {
			return fmt.Errorf("update trace observations: %w", writeError(err))
		}
	}
	return nil
}

func updateServiceProjections(ctx context.Context, tx *sql.Tx, services map[serviceProjection]projectedSpans) error {
	args := make([]any, 0, min(len(services), writeChunkSize)*3)
	write := func() error {
		values := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(args)/3), ",")
		query := "WITH batch(trace_id,span_id,span_count) AS (VALUES " + values + ") " + projectServices
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("update trace services: %w", writeError(err))
		}
		return nil
	}
	for key, projection := range services {
		args = append(args, key.trace[:], projection.sample[:], projection.count)
		if len(args) == writeChunkSize*3 {
			if err := write(); err != nil {
				return err
			}
			args = args[:0]
		}
	}
	if len(args) > 0 {
		return write()
	}
	return nil
}

func (s *Store) writeExport(ctx context.Context, observedNS int64, spans []telemetry.Span) error {
	err := s.checkJournalCapacity(ctx)
	if err == nil {
		err = s.writeSpans(ctx, observedNS, spans)
	}
	if errors.Is(err, storage.ErrCapacity) {
		_, countErr := s.db.ExecContext(ctx, "UPDATE storage_counters SET rejected_exports = rejected_exports + 1 WHERE id = 1")
		if countErr != nil {
			return errors.Join(err, fmt.Errorf("record rejected export: %w", countErr))
		}
	}
	return s.changed(err)
}

// spanBytesEstimate is below the measured size of a stored span with its
// indexes, so free pages short of it cannot hold an export.
const spanBytesEstimate = 512

// mustEvict reports whether an export of n spans cannot fit without evicting:
// the last export had to evict and the free pages are too few for this one.
func (s *Store) mustEvict(ctx context.Context, n int) bool {
	if !s.onFull.Evicts() || !s.full.Load() {
		return false
	}
	var free int64
	err := s.db.QueryRowContext(ctx, "SELECT freelist_count * page_size FROM pragma_freelist_count(), pragma_page_size()").Scan(&free)
	return err == nil && free < int64(n)*spanBytesEstimate
}

func (s *Store) writeSpans(ctx context.Context, observedNS int64, spans []telemetry.Span) error {
	if !s.mustEvict(ctx, len(spans)) {
		_, err := s.writeSpanBatch(ctx, observedNS, spans, "", 0)
		if err == nil {
			s.full.Store(false)
		}
		if !errors.Is(err, storage.ErrCapacity) || !s.onFull.Evicts() {
			return err
		}
	}
	s.full.Store(true)

	seen := make(map[telemetry.TraceID]struct{}, len(spans))
	ids := make([]string, 0, len(spans))
	for _, span := range spans {
		if _, ok := seen[span.TraceID]; !ok {
			seen[span.TraceID] = struct{}{}
			ids = append(ids, hex.EncodeToString(span.TraceID[:]))
		}
	}
	protected, err := json.Marshal(ids)
	if err != nil {
		return err
	}

	// SQLITE_FULL can roll back the whole transaction. Retry from scratch so
	// eviction and ingestion only commit together, even for an oversized export.
	// Each attempt rewrites the whole export, so start by evicting as many
	// traces as arrive: in steady state at the limit that frees enough at once.
	for limit := int64(len(ids)); ; limit *= 2 {
		deleted, err := s.writeSpanBatch(ctx, observedNS, spans, string(protected), limit)
		if !errors.Is(err, storage.ErrCapacity) || deleted < limit {
			return err
		}
	}
}

func (s *Store) writeSpanBatch(ctx context.Context, observedNS int64, spans []telemetry.Span, protected string, limit int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin trace batch: %w", err)
	}
	defer tx.Rollback()

	if err := s.checkWriteCapacity(ctx, tx); err != nil {
		return 0, err
	}
	var deleted int64
	if limit > 0 {
		var err error
		if s.onFull == storage.EvictFair {
			deleted, err = s.evictFairly(ctx, tx, observedNS, protected, limit)
		}
		// The largest owner may only hold traces in this export; fall back.
		if err == nil && deleted == 0 {
			deleted, err = evict(ctx, tx, fmt.Sprintf(deleteOldest, unprotected), observedNS, protected, limit)
		}
		if err != nil {
			return 0, err
		}
		if deleted == 0 {
			return 0, storage.ErrCapacity
		}
		if _, err := tx.ExecContext(ctx, "UPDATE storage_counters SET evicted_traces = evicted_traces + ? WHERE id = 1", deleted); err != nil {
			return 0, fmt.Errorf("record evicted traces: %w", writeError(err))
		}
	}
	if err := insertSpanBatch(ctx, tx, observedNS, spans); err != nil {
		return deleted, err
	}
	return deleted, writeError(tx.Commit())
}

const unprotected = "AND lower(hex(trace_id)) NOT IN (SELECT value FROM json_each(?))"

const ownerKey = "CASE WHEN namespace <> '' THEN 'n:' || namespace ELSE 's:' || service_name END"

// largestOwnerQuery finds the owner with the most stored spans. An owner is a
// service namespace, or a service name for spans without one.
const largestOwnerQuery = `SELECT ` + ownerKey + ` AS owner FROM trace_services
    GROUP BY owner ORDER BY sum(span_count) DESC, owner LIMIT 1`

// evictOwner deletes the oldest traces containing spans of one owner.
const evictOwner = `DELETE FROM traces WHERE trace_id IN (
    SELECT t.trace_id FROM traces t
    WHERE t.last_observed_ns < ? ` + unpinned + ` ` + unprotected + `
    AND EXISTS (SELECT 1 FROM trace_services ts WHERE ts.trace_id = t.trace_id AND ` + ownerKey + ` = ?)
    ORDER BY t.last_observed_ns, t.trace_id LIMIT ?
)`

// ownerRefresh bounds how stale the largest owner may be. Finding it reads
// every row of trace_services, which at the default size limit costs more
// than the export being stored.
const ownerRefresh = time.Second

// largestOwner returns the owner to evict from, reusing a recent answer
// unless refresh is set, and whether it was just looked up. Only the writer
// goroutine calls it.
func (s *Store) largestOwner(ctx context.Context, tx *sql.Tx, now time.Time, refresh bool) (string, bool, error) {
	if !refresh && s.owner != "" && now.Sub(s.ownerAt) < ownerRefresh {
		return s.owner, false, nil
	}
	var owner string
	err := tx.QueryRowContext(ctx, largestOwnerQuery).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return "", true, fmt.Errorf("find largest owner: %w", writeError(err))
	}
	s.owner, s.ownerAt = owner, now
	return owner, true, nil
}

// evictFairly evicts from the largest owner. A cached owner with nothing left
// to evict may no longer be the largest, so it is looked up again.
func (s *Store) evictFairly(ctx context.Context, tx *sql.Tx, observedNS int64, protected string, limit int64) (int64, error) {
	for refresh := false; ; refresh = true {
		owner, fresh, err := s.largestOwner(ctx, tx, time.Now(), refresh)
		if err != nil || owner == "" {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, evictOwner, observedNS, protected, owner, limit)
		if err != nil {
			return 0, fmt.Errorf("evict traces: %w", writeError(err))
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted > 0 || fresh {
			return deleted, err
		}
	}
}

func evict(ctx context.Context, tx *sql.Tx, query string, observedNS int64, protected string, limit int64) (int64, error) {
	result, err := tx.ExecContext(ctx, query, observedNS, protected, limit)
	if err != nil {
		return 0, fmt.Errorf("evict traces: %w", writeError(err))
	}
	return result.RowsAffected()
}

func insertSpanBatch(ctx context.Context, tx *sql.Tx, observedNS int64, spans []telemetry.Span) error {
	if err := observeSpanTraces(ctx, tx, observedNS, spans); err != nil {
		return err
	}
	services := make(map[serviceProjection]projectedSpans)
	for chunk := range slices.Chunk(spans, writeChunkSize) {
		if err := insertSpanChunk(ctx, tx, chunk, services); err != nil {
			return err
		}
	}
	return updateServiceProjections(ctx, tx, services)
}

func insertSpanChunk(ctx context.Context, tx *sql.Tx, spans []telemetry.Span, services map[serviceProjection]projectedSpans) error {
	const columns = 16
	args := make([]any, 0, len(spans)*columns)
	for i := range spans {
		span := &spans[i]
		fields, err := encodeSpanFields(span)
		if err != nil {
			return err
		}
		var parent any
		if span.ParentID != (telemetry.SpanID{}) {
			parent = span.ParentID[:]
		}
		args = append(args,
			span.TraceID[:], span.ID[:], parent, span.ServiceName, span.Name, span.Kind,
			span.StartTime.UnixNano(), span.EndTime.UnixNano(), span.StatusCode, span.StatusDescription,
			fields[0], span.Scope.Name, span.Scope.Version, fields[1], fields[2], fields[3],
		)
	}
	values := strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),", len(spans)), ",")
	// RETURNING reports only newly inserted rows. SQLite may return them in
	// any order, so derive membership from each returned identity.
	rows, err := tx.QueryContext(ctx, insertSpan+" VALUES "+values+`
ON CONFLICT (trace_id, span_id) DO NOTHING
RETURNING trace_id, span_id, service_namespace, service_name`, args...)
	if err != nil {
		return fmt.Errorf("insert span chunk: %w", writeError(err))
	}
	defer rows.Close()
	for rows.Next() {
		var traceID, spanID []byte
		var namespace, service string
		if err := rows.Scan(&traceID, &spanID, &namespace, &service); err != nil {
			return fmt.Errorf("read inserted span: %w", writeError(err))
		}
		key := serviceProjection{namespace: namespace, service: service}
		copy(key.trace[:], traceID)
		projection, exists := services[key]
		if !exists {
			copy(projection.sample[:], spanID)
		}
		projection.count++
		services[key] = projection
	}
	return writeError(rows.Err())
}

func encodeSpanFields(span *telemetry.Span) ([4]string, error) {
	var fields [4]string
	values := [...]any{span.ResourceAttributes, span.Scope.Attributes, span.Attributes, span.Events}
	for i, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fields, fmt.Errorf("encode span metadata: %w", err)
		}
		fields[i] = string(encoded)
	}
	return fields, nil
}
