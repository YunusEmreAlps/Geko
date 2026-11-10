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
	"strings"

	_ "modernc.org/sqlite"
)

const applicationID = 0x47454b4f // GEKO

//go:embed migrations/001_initial.sql
var initialSchema string

var migrations = []string{initialSchema}

type Store struct {
	db   *sql.DB
	path string
}

type Status struct {
	SQLiteVersion string
	SchemaVersion int
	Bytes         int64
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := prepareFile(path); err != nil {
		return nil, fmt.Errorf("prepare database %q: %w", path, err)
	}

	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // SQLite file URI for a Windows drive path.
	}
	params := url.Values{
		"mode":    {"rw"},
		"_pragma": {"busy_timeout(5000)", "foreign_keys(ON)"},
		"_txlock": {"immediate"},
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: params.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if err := initialize(ctx, db); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize database %q: %w", path, err), db.Close())
	}
	return &Store{db: db, path: path}, nil
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

func (s *Store) Check(ctx context.Context) (Status, error) {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result); err != nil {
		return Status{}, fmt.Errorf("check SQLite integrity: %w", err)
	}
	if result != "ok" {
		return Status{}, errors.New("SQLite integrity check failed, inspect or restore the database before use")
	}

	var status Status
	if err := s.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&status.SQLiteVersion); err != nil {
		return Status{}, err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&status.SchemaVersion); err != nil {
		return Status{}, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue // SQLite can remove its sidecar files when connections close.
		}
		if err != nil {
			return Status{}, fmt.Errorf("measure database size: %w", err)
		}
		status.Bytes += info.Size()
	}
	return status, nil
}

func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close database %q: %w", s.path, err)
	}
	return nil
}
