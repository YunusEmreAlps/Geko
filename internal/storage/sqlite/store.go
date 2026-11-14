// Package sqlite owns the local SQLite database and its schema.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/YunusEmreAlps/geko/internal/storage"

	_ "modernc.org/sqlite"
)

const applicationID = 0x47454b4f // GEKO

//go:embed migrations/001_initial.sql
var initialSchema string

//go:embed migrations/002_evidence.sql
var evidenceSchema string

//go:embed migrations/003_applications.sql
var applicationSchema string

//go:embed migrations/004_maintenance.sql
var maintenanceSchema string

//go:embed migrations/005_exception_search.sql
var exceptionSearchSchema string

//go:embed migrations/006_evidence_insert.sql
var evidenceInsertSchema string

//go:embed migrations/007_trace_projection.sql
var traceProjectionSchema string

//go:embed migrations/008_service_projection_batch.sql
var serviceProjectionBatchSchema string

//go:embed migrations/009_group_states.sql
var groupStateSchema string

//go:embed migrations/010_group_state_actor.sql
var groupStateActorSchema string

var migrations = []string{initialSchema, evidenceSchema, applicationSchema, maintenanceSchema, exceptionSearchSchema, evidenceInsertSchema, traceProjectionSchema, serviceProjectionBatchSchema, groupStateSchema, groupStateActorSchema}

type Store struct {
	revision   atomic.Uint64
	db         *sql.DB
	read       *sql.DB
	path       string
	maxBytes   int64
	onFull     storage.FullPolicy
	assignment sync.Mutex
	writeMu    sync.Mutex
	pending    []spanWrite
	writing    bool
	closed     bool
	writer     sync.WaitGroup
	writeSlots chan struct{}
}

// Revision changes after this instance commits data affecting investigation.
// It is an invalidation hint, not a persistent count or a cross-process feed.
func (s *Store) Revision() uint64 { return s.revision.Load() }
func (s *Store) changed(err error) error {
	if err == nil {
		s.revision.Add(1)
	}
	return err
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithLimit(ctx, path, 0)
}

// OpenWithLimit limits ingestion growth of the main database file.
func OpenWithLimit(ctx context.Context, path string, maxBytes int64) (*Store, error) {
	return OpenWithPolicy(ctx, path, maxBytes, storage.EvictOldest)
}

func OpenWithPolicy(ctx context.Context, path string, maxBytes int64, onFull storage.FullPolicy) (*Store, error) {
	if onFull == "" {
		onFull = storage.EvictOldest
	}
	if !onFull.Evicts() && onFull != storage.Reject {
		return nil, fmt.Errorf("invalid capacity policy %q", onFull)
	}
	if err := prepareFile(path); err != nil {
		return nil, fmt.Errorf("prepare database %q: %w", path, err)
	}

	params := url.Values{
		"mode":    {"rw"},
		"_pragma": {"busy_timeout(5000)", "foreign_keys(ON)", "journal_size_limit(0)"},
		"_txlock": {"immediate"},
	}
	db, err := openConnection(path, params, 1)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	if err := initialize(ctx, db); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize database %q: %w", path, err), db.Close())
	}
	read, err := openReader(path)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db, read: read, path: path, maxBytes: maxBytes, onFull: onFull, writeSlots: make(chan struct{}, 4)}, nil
}

func openConnection(path string, params url.Values, maxConnections int) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && uriPath[0] != '/' {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: params.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err == nil {
		db.SetMaxOpenConns(maxConnections)
	}
	return db, err
}

func openReader(path string) (*sql.DB, error) {
	return openConnection(path, url.Values{
		"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "query_only(ON)"},
	}, 4)
}

// Inspect checks an existing database without acquiring the writer or migrating it.
func Inspect(ctx context.Context, path string) (status storage.Status, err error) {
	read, err := openReader(path)
	if err != nil {
		return status, err
	}
	defer func() { err = errors.Join(err, read.Close()) }()
	var id int
	if err := read.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return status, err
	}
	if id != applicationID {
		return status, errors.New("file is not a Geko database")
	}
	var version int
	if err := read.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return status, err
	}
	if version < 1 || version > len(migrations) {
		return status, fmt.Errorf("unsupported schema version %d, this build supports versions 1 through %d", version, len(migrations))
	}
	return (&Store{read: read, path: path}).Check(ctx)
}

func prepareFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		return file.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("database path must be a regular file")
	}
	return nil
}

func initialize(ctx context.Context, db *sql.DB) error {
	if err := migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("SQLite returned journal mode %q, expected WAL on local storage", mode)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id, version int
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if id != applicationID {
		var objects int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
			return err
		}
		if id != 0 || version != 0 || objects != 0 {
			return errors.New("file belongs to another application, set GEKO_DB_PATH to a Geko database or a new file")
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return err
		}
	}
	if version < 0 || version > len(migrations) {
		return fmt.Errorf("unsupported schema version %d, this build supports versions 0 through %d", version, len(migrations))
	}
	for index := version; index < len(migrations); index++ {
		if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
			return fmt.Errorf("apply migration %d: %w", index+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", index+1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Check(ctx context.Context) (storage.Status, error) {
	var result string
	if err := s.read.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result); err != nil {
		return storage.Status{}, fmt.Errorf("check SQLite integrity: %w", err)
	}
	if result != "ok" {
		return storage.Status{}, errors.New("SQLite integrity check failed, inspect or restore the database before use")
	}
	return s.Status(ctx)
}

func (s *Store) Status(ctx context.Context) (storage.Status, error) {
	var status storage.Status
	if err := s.read.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&status.SQLiteVersion); err != nil {
		return storage.Status{}, err
	}
	if err := s.read.QueryRowContext(ctx, "PRAGMA user_version").Scan(&status.SchemaVersion); err != nil {
		return storage.Status{}, err
	}
	if err := s.read.QueryRowContext(ctx, "SELECT freelist_count * page_size FROM pragma_freelist_count(), pragma_page_size()").Scan(&status.FreeBytes); err != nil {
		return storage.Status{}, err
	}
	if status.SchemaVersion >= 4 {
		if err := s.read.QueryRowContext(ctx, "SELECT evicted_traces, rejected_exports FROM storage_counters WHERE id = 1").
			Scan(&status.EvictedTraces, &status.RejectedExports); err != nil {
			return storage.Status{}, err
		}
	}
	for _, file := range []struct {
		suffix string
		size   *int64
	}{{"", &status.MainBytes}, {"-wal", &status.WALBytes}, {"-shm", &status.SHMBytes}} {
		info, err := os.Stat(s.path + file.suffix)
		if file.suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue // SQLite can remove its sidecar files when connections close.
		}
		if err != nil {
			return storage.Status{}, fmt.Errorf("measure database size: %w", err)
		}
		*file.size = info.Size()
		status.Bytes += *file.size
	}
	return status, nil
}

func (s *Store) Close() error {
	s.writeMu.Lock()
	s.closed = true
	s.writeMu.Unlock()
	s.writer.Wait()
	if err := errors.Join(s.read.Close(), s.db.Close()); err != nil {
		return fmt.Errorf("close database %q: %w", s.path, err)
	}
	return nil
}

// Ready reports whether the database answers a read.
func (s *Store) Ready(ctx context.Context) error {
	var version int
	return s.read.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
}
