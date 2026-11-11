package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"geko/internal/investigation"
	"geko/internal/telemetry"
)

// One unique, observed root server span can describe the request. The selected
// evidence span is independently chosen by rank, recorded start and span ID.
const traceSummaryQuery = traceSummarySelect + traceSummaryJoins
const traceSummaryJoins = traceSummaryJoinStart + evidenceForTrace + " ORDER BY " + evidenceOrder + traceSummaryEnd

const traceSummarySelect = `
SELECT t.trace_id, t.first_observed_ns, t.last_observed_ns,
       r.span_id, r.service_name, r.name, r.http_status, r.start_ns, r.end_ns, r.application,
       s.span_id, s.service_name, s.name, s.http_status, s.start_ns, s.end_ns, s.application,
       e.rank, e.exception_message, e.exception_type, e.exception_hash
`

const traceSummaryJoinStart = `
FROM traces t
LEFT JOIN grouped_spans r ON r.trace_id = t.trace_id AND r.span_id = (
    SELECT CASE WHEN count(*) = 1 THEN min(root.span_id) END
    FROM spans root
    WHERE root.trace_id = t.trace_id AND root.kind = 2 AND root.parent_span_id IS NULL
)
LEFT JOIN span_evidence e ON e.trace_id = t.trace_id AND e.span_id = (
    SELECT candidate.span_id FROM span_evidence candidate
`

const evidenceSourceJoin = " JOIN grouped_spans source ON source.trace_id = candidate.trace_id AND source.span_id = candidate.span_id "
const evidenceForTrace = " WHERE candidate.trace_id = t.trace_id"
const evidenceOrder = "candidate.rank, candidate.start_ns, candidate.span_id"
const traceSummaryEnd = `
    LIMIT 1
)
LEFT JOIN grouped_spans s ON s.trace_id = e.trace_id AND s.span_id = e.span_id
`

// HasTraces reports whether the local store contains any recorded traces.
func (s *Store) HasTraces(ctx context.Context) (bool, error) {
	var hasTraces bool
	err := s.read.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM traces)").Scan(&hasTraces)
	return hasTraces, err
}

func (s *Store) Failures(ctx context.Context, f investigation.FailureFilter) (investigation.Page[investigation.TraceSummary], error) {
	page := investigation.Page[investigation.TraceSummary]{Items: []investigation.TraceSummary{}}
	if f.Limit < 1 || f.Limit > investigation.MaxPageSize {
		return page, errors.New("invalid failure page size")
	}
	var query strings.Builder
	var args []any
	query.WriteString(traceSummarySelect + traceSummaryJoinStart)
	if f.EvidenceService != "" || f.Search != "" {
		query.WriteString(evidenceSourceJoin)
	}
	query.WriteString(evidenceForTrace)
	if f.EvidenceService != "" {
		query.WriteString(" AND source.service_name = ?")
		args = append(args, f.EvidenceService)
		if f.Application != nil {
			query.WriteString(" AND source.application = ?")
			args = append(args, *f.Application)
		}
	}
	query.WriteString(" ORDER BY ")
	if f.Search != "" {
		// Select a matching evidence span ahead of the normal ranking so links
		// from search results open the evidence that the user actually found.
		query.WriteString(`(instr(lower(source.name), lower(?)) > 0 OR
            instr(lower(source.service_name), lower(?)) > 0 OR
            instr(lower(candidate.exception_message), lower(?)) > 0 OR
            instr(lower(candidate.exception_type), lower(?)) > 0 OR
            candidate.exception_hash = lower(?)) IS TRUE DESC, `)
		args = append(args, f.Search, f.Search, f.Search, f.Search, f.Search)
	}
	query.WriteString(evidenceOrder + traceSummaryEnd + " WHERE t.has_evidence = 1")
	if f.Application != nil {
		query.WriteString(" AND EXISTS (SELECT 1 FROM grouped_spans member WHERE member.trace_id = t.trace_id AND member.application = ?)")
		args = append(args, *f.Application)
	}
	if f.EvidenceService != "" {
		query.WriteString(" AND e.span_id IS NOT NULL")
	}
	if f.Service != "" {
		// Keep the documented entry/primary evidence service semantics.
		query.WriteString(` AND coalesce(r.service_name, (
            SELECT primary_span.service_name FROM span_evidence candidate
            JOIN spans primary_span ON primary_span.trace_id = candidate.trace_id AND primary_span.span_id = candidate.span_id
            WHERE candidate.trace_id = t.trace_id
            ORDER BY ` + evidenceOrder + ` LIMIT 1
        )) = ?`)
		args = append(args, f.Service)
		if f.Application != nil {
			query.WriteString(` AND coalesce(r.application, (
                SELECT primary_span.application FROM span_evidence candidate
                JOIN grouped_spans primary_span ON primary_span.trace_id = candidate.trace_id AND primary_span.span_id = candidate.span_id
                WHERE candidate.trace_id = t.trace_id
                ORDER BY ` + evidenceOrder + ` LIMIT 1
            )) = ?`)
			args = append(args, *f.Application)
		}
	}
	if f.HTTPStatus != 0 {
		query.WriteString(" AND r.http_status = ?")
		args = append(args, f.HTTPStatus)
	}
	if f.From != nil {
		query.WriteString(" AND t.last_observed_ns >= ?")
		args = append(args, f.From.UnixNano())
	}
	if f.To != nil {
		query.WriteString(" AND t.last_observed_ns < ?")
		args = append(args, f.To.UnixNano())
	}
	if f.Search != "" {
		query.WriteString(` AND (
            instr(lower(hex(t.trace_id)), lower(?)) = 1 OR
            instr(lower(r.name), lower(?)) > 0 OR
            instr(lower(r.service_name), lower(?)) > 0 OR
            instr(lower(s.name), lower(?)) > 0 OR
            instr(lower(s.service_name), lower(?)) > 0 OR
            instr(lower(e.exception_message), lower(?)) > 0 OR
            instr(lower(e.exception_type), lower(?)) > 0 OR
            e.exception_hash = lower(?))`)
		args = append(args, f.Search, f.Search, f.Search, f.Search, f.Search, f.Search, f.Search, f.Search)
	}
	if f.After != nil {
		query.WriteString(" AND (t.last_observed_ns, t.trace_id) < (?, ?)")
		args = append(args, f.After.TimeNS, f.After.ID)
	}
	query.WriteString(" ORDER BY t.last_observed_ns DESC, t.trace_id DESC LIMIT ?")
	args = append(args, f.Limit+1)

	rows, err := s.read.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return page, fmt.Errorf("query failures: %w", err)
	}
	defer rows.Close()
	var last investigation.Cursor
	for rows.Next() {
		if len(page.Items) == f.Limit {
			page.Next = &last
			break
		}
		summary, cursor, err := scanTraceSummary(rows)
		if err != nil {
			return page, fmt.Errorf("read failure: %w", err)
		}
		last = cursor
		page.Items = append(page.Items, summary)
	}
	return page, rows.Err()
}

func (s *Store) Trace(ctx context.Context, id telemetry.TraceID, after *investigation.Cursor, limit int) (investigation.TraceSummary, investigation.Page[investigation.Span], error) {
	var summary investigation.TraceSummary
	page := investigation.Page[investigation.Span]{Items: []investigation.Span{}}
	if limit < 1 || limit > investigation.MaxPageSize {
		return summary, page, errors.New("invalid span page size")
	}
	// Keep the header and spans on the same SQLite snapshot.
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, page, err
	}
	defer tx.Rollback()
	summary, _, err = scanTraceSummary(tx.QueryRowContext(ctx, traceSummaryQuery+" WHERE t.trace_id = ?", id[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return summary, page, investigation.ErrTraceNotFound
	}
	if err != nil {
		return summary, page, fmt.Errorf("read trace summary: %w", err)
	}
	var start, end sql.NullInt64
	var count int
	err = tx.QueryRowContext(ctx, `SELECT min(start_ns), max(max(start_ns, end_ns)), count(*)
        FROM spans WHERE trace_id = ?`, id[:]).Scan(&start, &end, &count)
	if err != nil {
		return summary, page, fmt.Errorf("read trace window: %w", err)
	}
	if count != 0 {
		summary.Window = &investigation.TraceWindow{
			Start: time.Unix(0, start.Int64).UTC(), End: time.Unix(0, end.Int64).UTC(), SpanCount: count,
		}
	}
	page, err = traceSpans(ctx, tx, id, after, limit)
	if err != nil {
		return summary, page, err
	}
	return summary, page, tx.Commit()
}

const spanSelect = `SELECT span_id, service_name, name, http_status, start_ns, end_ns, application,
        parent_span_id, kind, status_code, status_description,
        resource_attributes, scope_name, scope_version, scope_attributes, attributes, events
        FROM grouped_spans WHERE trace_id = ?`

// TraceEvidence returns a page of evidence spans independently of the waterfall.
// The next anchor is the last displayed span ID in evidence order.
func (s *Store) TraceEvidence(ctx context.Context, id telemetry.TraceID, after *telemetry.SpanID, limit int) ([]investigation.Evidence, string, error) {
	if limit < 1 || limit > investigation.MaxPageSize {
		return nil, "", errors.New("invalid evidence page size")
	}
	query := `SELECT source.span_id, source.service_name, source.name, source.http_status,
        source.start_ns, source.end_ns, source.application, candidate.rank,
        candidate.exception_message, candidate.exception_type, candidate.exception_hash
        FROM span_evidence candidate
        ` + evidenceSourceJoin + `
        WHERE candidate.trace_id = ?`
	args := []any{id[:]}
	if after != nil {
		query += ` AND (candidate.rank, candidate.start_ns, candidate.span_id) > (
            SELECT rank, start_ns, span_id FROM span_evidence WHERE trace_id = ? AND span_id = ?
        )`
		args = append(args, id[:], after[:])
	}
	query += " ORDER BY " + evidenceOrder + " LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("query trace evidence: %w", err)
	}
	defer rows.Close()
	var items []investigation.Evidence
	for rows.Next() {
		if len(items) == limit {
			return items, items[len(items)-1].SpanID, nil
		}
		var span spanSummaryRow
		var rank sql.NullInt64
		var exception exceptionRow
		fields := append(span.fields(), &rank, &exception.message, &exception.kind, &exception.hash)
		if err := rows.Scan(fields...); err != nil {
			return nil, "", fmt.Errorf("read trace evidence: %w", err)
		}
		items = append(items, *span.evidence(rank, exception))
	}
	return items, "", rows.Err()
}

func (s *Store) Span(ctx context.Context, traceID telemetry.TraceID, spanID telemetry.SpanID) (investigation.Span, error) {
	span, _, err := scanSpan(s.read.QueryRowContext(ctx, spanSelect+" AND span_id = ?", traceID[:], spanID[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return span, investigation.ErrSpanNotFound
	}
	return span, err
}

func traceSpans(ctx context.Context, tx *sql.Tx, id telemetry.TraceID, after *investigation.Cursor, limit int) (investigation.Page[investigation.Span], error) {
	page := investigation.Page[investigation.Span]{Items: []investigation.Span{}}
	query := spanSelect
	args := []any{id[:]}
	if after != nil {
		query += " AND (start_ns, span_id) > (?, ?)"
		args = append(args, after.TimeNS, after.ID)
	}
	query += " ORDER BY start_ns, span_id LIMIT ?"
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return page, fmt.Errorf("query trace spans: %w", err)
	}
	defer rows.Close()
	var last investigation.Cursor
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = &last
			break
		}
		span, cursor, err := scanSpan(rows)
		if err != nil {
			return page, fmt.Errorf("read trace span: %w", err)
		}
		last = cursor
		page.Items = append(page.Items, span)
	}
	return page, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

type spanSummaryRow struct {
	application   sql.NullString
	id            []byte
	service, name sql.NullString
	httpStatus    sql.Null[int]
	start, end    sql.NullInt64
}

func (r *spanSummaryRow) fields() []any {
	return []any{&r.id, &r.service, &r.name, &r.httpStatus, &r.start, &r.end, &r.application}
}

func (r spanSummaryRow) summary() *investigation.SpanSummary {
	if r.id == nil {
		return nil
	}
	span := &investigation.SpanSummary{
		SpanID:      hex.EncodeToString(r.id),
		Service:     r.service.String,
		Application: r.application.String,
		Operation:   r.name.String,
		DurationNS:  investigation.DurationNS(r.start.Int64, r.end.Int64),
	}
	if r.httpStatus.Valid {
		span.HTTPStatus = &r.httpStatus.V
	}
	return span
}

type exceptionRow struct {
	message, kind, hash sql.NullString
}

func (r spanSummaryRow) evidence(rank sql.NullInt64, exception exceptionRow) *investigation.Evidence {
	span := r.summary()
	if span == nil {
		return nil
	}
	classification := "span_error"
	switch rank.Int64 {
	case 1:
		classification = "exception"
	case 2:
		classification = "http_5xx"
	}
	return &investigation.Evidence{
		SpanSummary: *span, Classification: classification, ExceptionMessage: exception.message.String,
		ExceptionType: exception.kind.String, ExceptionHash: exception.hash.String,
	}
}

func scanTraceSummary(row scanner) (investigation.TraceSummary, investigation.Cursor, error) {
	var summary investigation.TraceSummary
	var entry, evidence spanSummaryRow
	var id []byte
	var first, last int64
	var rank sql.NullInt64
	var exception exceptionRow
	fields := append([]any{&id, &first, &last}, entry.fields()...)
	fields = append(fields, evidence.fields()...)
	fields = append(fields, &rank, &exception.message, &exception.kind, &exception.hash)
	if err := row.Scan(fields...); err != nil {
		return summary, investigation.Cursor{}, err
	}

	summary.TraceID = hex.EncodeToString(id)
	summary.FirstObserved = time.Unix(0, first).UTC()
	summary.LastObserved = time.Unix(0, last).UTC()
	summary.EntrySpan = entry.summary()
	summary.HTTPOutcome = investigation.HTTPOutcome(summary.EntrySpan)
	summary.Evidence = evidence.evidence(rank, exception)
	return summary, investigation.Cursor{TimeNS: last, ID: id}, nil
}

func scanSpan(row scanner) (investigation.Span, investigation.Cursor, error) {
	var span investigation.Span
	var summary spanSummaryRow
	var parent []byte
	var resource, scope, attributes, events []byte
	fields := append(summary.fields(),
		&parent, &span.Kind, &span.StatusCode, &span.StatusDescription, &resource,
		&span.Scope.Name, &span.Scope.Version, &scope, &attributes, &events,
	)
	if err := row.Scan(fields...); err != nil {
		return span, investigation.Cursor{}, err
	}
	span.SpanSummary = *summary.summary()
	span.ParentID = hex.EncodeToString(parent)
	span.Start = time.Unix(0, summary.start.Int64).UTC()
	span.End = time.Unix(0, summary.end.Int64).UTC()
	for _, field := range []struct {
		data []byte
		dest any
	}{
		{resource, &span.ResourceAttributes}, {scope, &span.Scope.Attributes},
		{attributes, &span.Attributes}, {events, &span.Events},
	} {
		if err := json.Unmarshal(field.data, field.dest); err != nil {
			return span, investigation.Cursor{}, fmt.Errorf("decode span metadata: %w", err)
		}
	}
	return span, investigation.Cursor{TimeNS: summary.start.Int64, ID: summary.id}, nil
}
