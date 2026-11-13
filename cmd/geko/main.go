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
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/notify"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/web"
)

var version = "dev" // Set for release builds with -ldflags "-X main.version=v0.1.0".

const usage = `Geko — Find the failure. Follow the trace.

Usage: geko <command>

Commands:
  serve     Start the local dashboard and OTLP trace receiver
  doctor    Check SQLite and probe the listener
  compact   Reclaim free space in the SQLite file
  backup <file>     Write a consistent copy of the database while Geko runs
  run -- <command>  Run a command with exporters set to Geko, then list the failures received meanwhile
  failures --json   Query failures in the local database, or a server with --server
  mcp       Serve read-only investigation tools over stdio (servers also serve /mcp)
  version   Print version and build information

Environment:
  GEKO_CONFIG       YAML file (default geko.yaml in the working directory, if present)
  GEKO_LISTEN_ADDR  OTLP/HTTP address (default 127.0.0.1:4318)
  GEKO_DASHBOARD_ADDR  Optional separate loopback dashboard address
  GEKO_ACCESS_TOKEN    Protect all HTTP and gRPC listeners with an access token
  GEKO_INGEST_TOKENS   Export-only tokens as name=token pairs, comma-separated
  GEKO_READ_TOKENS     Read-only tokens (dashboard, API, /mcp) as name=token pairs
  GEKO_TOKENS_FILE     YAML file with access, ingest and read tokens, reloaded on change
  GEKO_TOKEN           Token that failures --server sends
  GEKO_WEBHOOK_URL     Notify on new and reopened error groups
  GEKO_WEBHOOK_FORMAT  json, slack, discord or ntfy (inferred from the URL)
  GEKO_MCP_TRIAGE      Let MCP clients with full access resolve and mute groups
  GEKO_ALLOW_PLAINTEXT Allow non-loopback listeners behind a TLS proxy (true/false)
  GEKO_PUBLIC_URL      External URL of the OTLP/HTTP listener, e.g. https://geko.example.com
  GEKO_GRPC_ADDR       Optional OTLP/gRPC address (e.g. 127.0.0.1:4317)
  GEKO_DB_PATH      SQLite file (default geko/geko.db in the user configuration directory)
  GEKO_RETENTION_DAYS  Trace retention in days (default 7)
  GEKO_MAX_DB_MIB      Main database size limit in MiB (default 256)
  GEKO_ON_FULL         evict_oldest (default), evict_fair or reject
  GEKO_REDACTION       best_effort (default) or strict
  GEKO_INGEST_CONCURRENCY  Exports decoded at once (default 4, up to 64)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		var code exitError
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "geko:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		_, err := io.WriteString(out, usage)
		return err
	}
	if args[0] == "failures" {
		return failuresCommand(ctx, args[1:], out)
	}
	if args[0] == "backup" {
		return backupCommand(ctx, args[1:], out)
	}
	if args[0] == "run" {
		return runCommand(ctx, args[1:], out, os.Stderr)
	}
	if len(args) != 1 {
		return errors.New("expected one command, run geko --help for usage")
	}
	switch args[0] {
	case "mcp":
		return mcpCommand(ctx, out)
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
	var listenerErrors []error
	for _, addr := range []string{cfg.ListenAddr, cfg.DashboardAddr, cfg.GRPCAddr} {
		if addr == "" {
			continue
		}
		listener, err := listen(ctx, addr)
		if err == nil {
			err = listener.Close()
			if err == nil {
				fmt.Fprintf(out, "Listener available at %s\n", addr)
			}
		}
		listenerErrors = append(listenerErrors, err)
	}
	return errors.Join(append(listenerErrors, dbErr)...)
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
		return nil, fmt.Errorf("bind %s (check GEKO_LISTEN_ADDR, GEKO_DASHBOARD_ADDR or GEKO_GRPC_ADDR, or stop the process using the port): %w", addr, err)
	}
	return listener, nil
}

func serve(ctx context.Context, cfg config.Config, out io.Writer) (err error) {
	maxBytes := int64(cfg.MaxDBMiB) << 20
	db, err := sqlite.OpenWithPolicy(ctx, cfg.DBPath, maxBytes, cfg.OnFull)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	// Migrations and earlier larger limits can leave the file above max_db_mib.
	if status, err := db.Status(ctx); err == nil && status.MainBytes-status.FreeBytes > maxBytes {
		advice := "Geko evicts the oldest traces as exports arrive"
		if cfg.OnFull == storage.Reject {
			advice = "every export is rejected until space is freed; raise max_db_mib or set on_full to evict_oldest or evict_fair"
		}
		fmt.Fprintf(out, "Warning: the database uses %d MiB, above max_db_mib (%d MiB); %s.\n",
			(status.MainBytes-status.FreeBytes)>>20, cfg.MaxDBMiB, advice)
	}

	listener, err := listen(ctx, cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	var dashboardListener, grpcListener net.Listener
	if cfg.DashboardAddr != "" {
		dashboardListener, err = listen(ctx, cfg.DashboardAddr)
		if err != nil {
			return err
		}
		defer dashboardListener.Close()
	}
	if cfg.GRPCAddr != "" {
		grpcListener, err = listen(ctx, cfg.GRPCAddr)
		if err != nil {
			return err
		}
		defer grpcListener.Close()
	}

	if cfg.RetentionDays > 0 {
		retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
		pruneExpired(ctx, db, retention)
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
	if cfg.IngestSlots > 0 {
		receiver.WithConcurrency(cfg.IngestSlots)
	}
	mux.Handle("POST /v1/traces", receiver)
	dashboardMux := mux
	dashboardAddr := listener.Addr().String()
	grpcAddr := ""
	if grpcListener != nil {
		grpcAddr = grpcListener.Addr().String()
	}
	if dashboardListener != nil {
		dashboardMux = http.NewServeMux()
		dashboardAddr = dashboardListener.Addr().String()
	}
	if cfg.TokensFile != "" {
		watching, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			access.WatchTokenFile(watching, cfg.TokensFile, cfg.AccessToken, 5*time.Second)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
	}
	var notifications *web.Notifications
	if cfg.WebhookURL != "" {
		baseURL := cfg.PublicURL
		if baseURL == "" {
			baseURL = "http://" + dashboardAddr
		}
		notifier := notify.New(cfg.WebhookURL, cfg.WebhookFormat, baseURL, db, time.Now())
		notifications = &web.Notifications{Format: string(notifier.Format()), Host: notifier.Host(), Test: notifier.Test}
		running, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			notifier.Run(running, 30*time.Second)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
	}
	var signIn func(http.ResponseWriter, *http.Request, string) bool
	var signOut func(http.ResponseWriter, *http.Request)
	if cfg.AccessToken.Enabled() {
		signIn, signOut = cfg.AccessToken.SignIn, cfg.AccessToken.SignOut
	}
	api.Register(dashboardMux, db)
	var triage groupTriage
	if cfg.MCPTriage {
		triage = db
	}
	dashboardMux.Handle("/mcp", mcpHandler(db, triage, cfg.AccessToken.Enabled()))
	if err := web.Register(dashboardMux, db, web.Settings{
		Shutdown:      ctx.Done(),
		DashboardAddr: dashboardAddr, GRPCAddr: grpcAddr,
		ListenAddr: listener.Addr().String(), DBPath: cfg.DBPath, Version: version,
		PublicURL: cfg.PublicURL, AccessToken: cfg.AccessToken.Enabled(), IngestTokens: cfg.AccessToken.Names(access.ScopeIngest),
		ReadTokens:    cfg.AccessToken.Names(access.ScopeRead),
		Notifications: notifications,
		SignIn:        signIn, SignOut: signOut, Tokens: cfg.AccessToken,
		ReceiverDiagnostics: receiver.Diagnostics,
		RestoreTrace:        receiver.RestoreTraceJSON,
		RetentionDays:       cfg.RetentionDays, MaxDBBytes: maxBytes,
		IngestConcurrency: cfg.IngestSlots,
		ConfigSource:      cfg.Source,
		OnFull:            cfg.OnFull, Redaction: cfg.Redaction,
	}); err != nil {
		return err
	}
	registerProbes(mux, db)
	endpoints := []httpEndpoint{{listener, newHTTPServer(cfg.AccessToken.Protect(mux))}}
	if dashboardListener != nil {
		registerProbes(dashboardMux, db)
		endpoints = append(endpoints, httpEndpoint{dashboardListener, newHTTPServer(cfg.AccessToken.Protect(dashboardMux))})
	}
	return runServers(ctx, out, endpoints, grpcListener, receiver, cfg.AccessToken)
}

// registerProbes serves unauthenticated liveness and readiness checks.
func registerProbes(mux *http.ServeMux, db *sqlite.Store) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ready(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func backupCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: geko backup <new-file.db>")
	}
	target, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists; choose a new file", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	store, err := openInvestigation(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Backup(ctx, target); err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Backed up to %s (%d bytes)\n", target, info.Size())
	return err
}

func prunePeriodically(ctx context.Context, db *sqlite.Store, retention time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pruneExpired(ctx, db, retention)
		}
	}
}

func pruneExpired(ctx context.Context, db *sqlite.Store, retention time.Duration) {
	if err := db.Prune(ctx, time.Now().Add(-retention)); err != nil && ctx.Err() == nil {
		log.Printf("trace retention: %v", err)
	}
}
