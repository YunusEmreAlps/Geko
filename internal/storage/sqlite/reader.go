package sqlite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func (r *Reader) RunSnapshot(ctx context.Context, id string) (investigation.RunSnapshot, error) {
	return r.store.RunSnapshot(ctx, id)
}

func (r *Reader) CompareRuns(ctx context.Context, baseline, candidate string) (investigation.RunComparison, error) {
	return r.store.CompareRuns(ctx, baseline, candidate)
}

// Reader opens an existing Geko database without running migrations.
type Reader struct{ store *Store }

func (r *Reader) ComparisonChoices(ctx context.Context, since time.Time, service string) (investigation.ComparisonChoices, error) {
	return r.store.ComparisonChoices(ctx, since, service)
}

func OpenReadOnly(ctx context.Context, path string) (*Reader, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("database path must be absolute")
	}
	db, err := openReader(path)
	if err != nil {
		return nil, err
	}
	var id, version int
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err == nil {
		if id != applicationID {
			err = errors.New("file is not a Geko database")
		} else if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err == nil && version != len(migrations) {
			err = fmt.Errorf("database schema %d does not match required schema %d; open it with a matching Geko serve", version, len(migrations))
		}
	}
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Reader{store: &Store{read: db, path: path}}, nil
}

func (r *Reader) Close() error { return r.store.read.Close() }

func (r *Reader) Failures(ctx context.Context, filter investigation.FailureFilter) (investigation.Page[investigation.TraceSummary], error) {
	return r.store.Failures(ctx, filter)
}

func (r *Reader) FailureGroups(ctx context.Context, filter investigation.FailureFilter, after *investigation.GroupCursor) (investigation.FailureGroups, error) {
	return r.store.FailureGroups(ctx, filter, after)
}

func (r *Reader) Trace(ctx context.Context, id telemetry.TraceID, after *investigation.Cursor, limit int) (investigation.TraceSummary, investigation.Page[investigation.Span], error) {
	return r.store.Trace(ctx, id, after, limit)
}

func (r *Reader) EvidenceDetails(ctx context.Context, id telemetry.TraceID, selected telemetry.SpanID, limit int) ([]investigation.Span, bool, error) {
	return r.store.EvidenceDetails(ctx, id, selected, limit)
}

func (r *Reader) CompareTrace(ctx context.Context, id telemetry.TraceID) (investigation.TraceSummary, *investigation.TraceSummary, error) {
	return r.store.CompareTrace(ctx, id)
}

func (r *Reader) TraceComparison(ctx context.Context, id telemetry.TraceID) (investigation.TraceComparison, error) {
	return r.store.TraceComparison(ctx, id)
}

func (r *Reader) GroupVersions(ctx context.Context, key string) ([]investigation.GroupVersion, int, error) {
	return r.store.GroupVersions(ctx, key)
}

func (r *Reader) VersionComparisons(ctx context.Context, filter investigation.RegressionFilter) ([]investigation.VersionComparison, error) {
	return r.store.VersionComparisons(ctx, filter)
}

// Backup writes a consistent snapshot to a new file while Geko keeps serving.
func (r *Reader) Backup(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("backup path must be absolute")
	}
	// query_only blocks VACUUM INTO, so use a dedicated connection that still
	// opens the source read-only.
	db, err := openConnection(r.store.path, url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)"}}, 1)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("back up database to %q: %w", path, err)
	}
	return nil
}
