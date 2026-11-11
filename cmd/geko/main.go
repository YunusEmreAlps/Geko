package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"geko/internal/api"
	"geko/internal/config"
	"geko/internal/ingest/otlp"
	"geko/internal/storage/sqlite"
	"geko/web"
)

var version = "dev" // Set for release builds with -ldflags "-X main.version=v0.1.0".

const usage = `Geko — Find the failure. Follow the trace.

Usage: geko <command>

Commands:
  serve     Start the local dashboard and OTLP trace receiver
  doctor    Check SQLite and probe the listener
  compact   Reclaim free space in the SQLite file
  version   Print version and build information

Environment:
  GEKO_CONFIG       YAML file (default geko.yaml in the working directory, if present)
  GEKO_LISTEN_ADDR  Loopback address (default 127.0.0.1:4318)
  GEKO_DB_PATH      SQLite file (default geko/geko.db in the user configuration directory)
  GEKO_RETENTION_DAYS  Trace retention in days (default 7)
  GEKO_MAX_DB_MIB      Main database size limit in MiB (default 256)
  GEKO_ON_FULL         evict_oldest (default) or reject
  GEKO_REDACTION       best_effort (default) or strict
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "geko:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		_, err := io.WriteString(out, usage)
		return err
	}
	if len(args) != 1 {
		return errors.New("expected one command, run geko --help for usage")
	}
	switch args[0] {
	case "help", "--help", "-h":
		_, err := io.WriteString(out, usage)
		return err
	case "version":
		printVersion(out)
		return nil
	case "doctor", "serve", "compact":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if args[0] == "doctor" {
			return doctor(ctx, cfg, out)
		}
		if args[0] == "compact" {
			return compact(ctx, cfg.DBPath, out)
		}
		return serve(ctx, cfg, out)
	default:
		return fmt.Errorf("unknown command %q, run geko --help for usage", args[0])
	}
}

func printVersion(out io.Writer) {
	fmt.Fprintf(out, "Geko %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision", "vcs.time", "vcs.modified":
				fmt.Fprintf(out, "%s=%s\n", setting.Key, setting.Value)
			}
		}
	}
}

func doctor(ctx context.Context, cfg config.Config, out io.Writer) error {
	dbErr := checkDatabase(ctx, cfg.DBPath, out)
	listener, listenErr := listen(ctx, cfg.ListenAddr)
	if listenErr == nil {
		listenErr = listener.Close()
		if listenErr == nil {
			fmt.Fprintf(out, "Listener available at %s\n", cfg.ListenAddr)
		}
	}
	return errors.Join(dbErr, listenErr)
}

func checkDatabase(ctx context.Context, path string, out io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Keep first-run diagnostics usable before the database exists.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		db, err := sqlite.Open(ctx, path)
		if err != nil {
			return err
		}
		if err := db.Close(); err != nil {
			return err
		}
	}
	status, err := sqlite.Inspect(ctx, path)
	if err != nil {
		return fmt.Errorf("check database %q: %w", path, err)
	}
	_, err = fmt.Fprintf(out, "SQLite %s, schema %d at %q\nMain file: %d bytes\nWAL: %d bytes\nSHM: %d bytes\nReusable pages: %d bytes\n",
		status.SQLiteVersion, status.SchemaVersion, path, status.MainBytes, status.WALBytes, status.SHMBytes, status.FreeBytes)
	return err
}

func compact(ctx context.Context, path string, out io.Writer) (err error) {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("open database for compaction: %w", err)
	}
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if err := db.Compact(ctx); err != nil {
		return err
	}
	status, err := db.Status(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Compacted %q. Main file: %d bytes. WAL: %d bytes.\n", path, status.MainBytes, status.WALBytes)
	return err
}

func listen(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s (check listen_addr or GEKO_LISTEN_ADDR, or stop the process using the port): %w", addr, err)
	}
	return listener, nil
}

func serve(ctx context.Context, cfg config.Config, out io.Writer) (err error) {
	listener, err := listen(ctx, cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	maxBytes := int64(cfg.MaxDBMiB) << 20
	db, err := sqlite.OpenWithPolicy(ctx, cfg.DBPath, maxBytes, cfg.OnFull)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	if cfg.RetentionDays > 0 {
		retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
		if err := db.Prune(ctx, time.Now().Add(-retention)); err != nil {
			return err
		}
		maintenance, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			prunePeriodically(maintenance, db, retention)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
	}

	mux := http.NewServeMux()
	receiver := otlp.NewHandlerWithRedaction(db, cfg.Redaction == "strict")
	mux.Handle("POST /v1/traces", receiver)
	api.Register(mux, db)
	if err := web.Register(mux, db, web.Settings{
		ListenAddr: listener.Addr().String(), DBPath: cfg.DBPath, Version: version,
		ReceiverDiagnostics: receiver.Diagnostics,
		RetentionDays:       cfg.RetentionDays, MaxDBBytes: maxBytes,
		ConfigSource: cfg.Source,
		OnFull:       cfg.OnFull, Redaction: cfg.Redaction,
	}); err != nil {
		return err
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()

	fmt.Fprintf(out, "Geko listening on http://%s\n", listener.Addr())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return errors.Join(err, server.Close(), <-done)
		}
		return <-done
	}
}

func prunePeriodically(ctx context.Context, db *sqlite.Store, retention time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := db.Prune(ctx, time.Now().Add(-retention)); err != nil && ctx.Err() == nil {
				log.Printf("trace retention: %v", err)
			}
		}
	}
}
