package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"syscall"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/notify"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/web"
)

var version = "dev" // Set for release builds with -ldflags "-X main.version=v0.1.0".

const usage = `Geko — Find the failure. Follow the trace.

Usage: geko <command>

Commands:
  demo      Serve a temporary synthetic dataset and open the dashboard
  serve [--open] [--read-only]  Start the local dashboard and OTLP trace receiver
  doctor    Check SQLite and listeners, or inspect an OpenTelemetry Collector
  budget    Report hourly trace volume, limits and high-cardinality attributes
  compact   Reclaim free space in the SQLite file
  backup <file>     Write a consistent copy of the database while Geko runs
  run [--server URL] -- <command>  Run a command and list failures tagged with its run ID
  failures --json   Query failures in the local database, or a server with --server
  compare-runs --baseline ID_OR_FILE --candidate ID_OR_FILE [--json]  Compare observed run evidence
  mcp       Serve read-only investigation tools over stdio (servers also serve /mcp)
  mcp setup claude-code|cursor|vscode [--server URL]  Register Geko with an MCP client
  version   Print version and build information

Environment:
  GEKO_CONFIG       YAML file (default geko.yaml in the working directory, if present)
  GEKO_LISTEN_ADDR  OTLP/HTTP address (default 127.0.0.1:4318)
  GEKO_DASHBOARD_ADDR  Optional separate loopback dashboard address
  GEKO_ACCESS_TOKEN    Protect all HTTP and gRPC listeners with an access token
  GEKO_INGEST_TOKENS   Export-only tokens as name=token pairs, comma-separated
  GEKO_READ_TOKENS     Read-only tokens (dashboard, API, /mcp) as name=token pairs
  GEKO_TRIAGE_TOKENS   Read tokens that also resolve, mute and reopen error groups
  GEKO_TOKENS_FILE     YAML file with access, ingest, read and triage tokens, reloaded on change
  GEKO_PROXY_USER_HEADER  Trust the user an authenticating proxy names in this header
  GEKO_PROXY_TRUSTED_IPS  Addresses of that proxy (IPs or CIDR prefixes)
  GEKO_PROXY_ROLES        Proxy users' roles as user=viewer|triager|admin pairs
  GEKO_PROXY_DEFAULT_ROLE Role of unlisted proxy users (default viewer, or none)
  GEKO_AUDIT_LOG       Append a JSON line per change and sign-in to this file (- for stderr)
  GEKO_TOKEN           Read token for remote failures and budget queries
  GEKO_WEBHOOK_URL     Notify on new and reopened error groups
  GEKO_WEBHOOK_FORMAT  json, slack, discord or ntfy (inferred from the URL)
  GEKO_MCP_TRIAGE      Let MCP clients with the triager or admin role resolve and mute groups
  GEKO_ALLOW_PLAINTEXT Allow non-loopback listeners behind a TLS proxy (true/false)
  GEKO_PUBLIC_URL      External URL of the OTLP/HTTP listener, e.g. https://geko.example.com
  GEKO_GRPC_ADDR       Optional OTLP/gRPC address (e.g. 127.0.0.1:4317)
  GEKO_DB_PATH      SQLite file (default geko/geko.db in the user configuration directory)
  GEKO_RETENTION_DAYS  Trace retention in days (default 7)
  GEKO_MAX_DB_MIB      Main database size limit in MiB (default 256)
  GEKO_ON_FULL         evict_oldest (default), evict_fair or reject
  GEKO_REDACTION       best_effort, or strict (default once GEKO_ACCESS_TOKEN is set)
  GEKO_INGEST_CONCURRENCY  Exports decoded at once (default 4, up to 64)
  GEKO_OWNERS          Owning teams as service:<name>=<team> or application:<name>=<team> pairs
  GEKO_DIGEST          Weekly digest to every webhook, such as "monday 09:00" (UTC)
  GEKO_READ_ONLY       Refuse exports, triage, imports and other changes (true/false)
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
	if args[0] == "compare-runs" {
		return compareRunsCommand(ctx, args[1:], out)
	}
	if args[0] == "mcp" && len(args) > 1 {
		return mcpSetupCommand(args[1:], out)
	}
	if args[0] == "doctor" && len(args) > 1 {
		return collectorDoctorCommand(ctx, args[1:], out)
	}
	if args[0] == "budget" {
		return budgetCommand(ctx, args[1:], out)
	}
	if args[0] == "serve" {
		return serveCommand(ctx, args[1:], out)
	}
	if args[0] == "demo" {
		return demoCommand(ctx, args[1:], out)
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
	case "doctor", "compact":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if args[0] == "doctor" {
			return doctor(ctx, cfg, out)
		}
		return compact(ctx, cfg.DBPath, out)
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

func serveCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(out)
	open := flags.Bool("open", false, "open the dashboard in a browser once Geko is listening")
	readOnly := flags.Bool("read-only", false, "refuse exports, triage, imports and other changes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: geko serve [--open] [--read-only]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.ReadOnly = cfg.ReadOnly || *readOnly
	return serve(ctx, cfg, out, serveOptions{open: *open})
}

// serveOptions are what the demo adds to an ordinary server.
type serveOptions struct {
	// open starts a browser on the dashboard once the listeners are bound.
	open bool
	// public admits callers from any Host without a token, at a per-client
	// rate. Only the synthetic demo dataset is served this way.
	public bool
	// trustedProxies name the client in X-Forwarded-For for the public rate.
	trustedProxies []netip.Prefix
	// prepare runs on the open database before the listeners are bound.
	prepare func(context.Context, *sqlite.Store) error
	// background runs until the server stops.
	background func(context.Context, *sqlite.Store)
}

func serve(ctx context.Context, cfg config.Config, out io.Writer, options serveOptions) (err error) {
	if cfg.ReadOnly && cfg.GRPCAddr != "" {
		return errors.New("a read-only instance refuses exports; remove grpc_addr (GEKO_GRPC_ADDR)")
	}
	if cfg.ReadOnly {
		cfg.MCPTriage = false
	}
	maxBytes := int64(cfg.MaxDBMiB) << 20
	db, err := sqlite.OpenWithPolicy(ctx, cfg.DBPath, maxBytes, cfg.OnFull)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if options.prepare != nil {
		if err := options.prepare(ctx, db); err != nil {
			return err
		}
	}
	// Migrations and earlier larger limits can leave the file above max_db_mib.
	if status, err := db.Status(ctx); err == nil {
		warnStorageLimit(out, cfg, status)
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

	if options.background != nil {
		running, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			options.background(running, db)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
	}

	mux := http.NewServeMux()
	tracker := budget.New(cfg.TelemetryBudget, time.Now())
	receiver := otlp.NewHandlerWithRedaction(db, cfg.Redaction == "strict").
		WithAttributeSearch(cfg.AttributeSearch).WithBudget(tracker)
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
	if cfg.WebhookURL != "" || len(cfg.ApplicationWebhooks) > 0 {
		baseURL := cfg.PublicURL
		if baseURL == "" {
			baseURL = "http://" + dashboardAddr
		}
		notifier := notify.New(cfg.WebhookURL, cfg.WebhookFormat, baseURL, db, time.Now()).WithApplications(cfg.ApplicationWebhooks).WithOwners(cfg.Owners).WithDigest(cfg.Digest)
		notifications = &web.Notifications{Format: string(notifier.Format()), Host: notifier.Host(), Test: notifier.Test}
		if cfg.Digest.Enabled {
			notifications.Digest = cfg.Digest.String()
		}
		for _, application := range slices.Sorted(maps.Keys(cfg.ApplicationWebhooks)) {
			webhook := cfg.ApplicationWebhooks[application]
			notifications.Applications = append(notifications.Applications, web.ApplicationWebhook{
				Application: application, Format: string(webhook.Format), Host: webhook.Host(),
			})
		}
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
	var auditLog *audit.Log
	if cfg.AuditLog != "" {
		var closeAudit func() error
		if auditLog, closeAudit, err = audit.Open(cfg.AuditLog); err != nil {
			return err
		}
		defer func() { err = errors.Join(err, closeAudit()) }()
	}
	var signIn func(http.ResponseWriter, *http.Request, string) (access.Principal, bool)
	var signOut func(http.ResponseWriter, *http.Request) (access.Principal, bool)
	proxyHeader := ""
	if cfg.AccessToken.Enabled() {
		signIn, signOut = cfg.AccessToken.SignIn, cfg.AccessToken.SignOut
		if proxy := cfg.AccessToken.Proxy(); proxy != nil {
			proxyHeader = proxy.Header
		}
	}
	api.Register(dashboardMux, db)
	var triage groupTriage
	if cfg.MCPTriage {
		triage = db
	}
	dashboardMux.Handle("/mcp", mcpHandler(db, triage, cfg.Owners, cfg.AccessToken.Enabled(), auditLog))
	restore := receiver.RestoreTraceJSON
	if cfg.ReadOnly {
		restore = nil
	}
	if err := web.Register(dashboardMux, db, web.Settings{
		Shutdown:      ctx.Done(),
		DashboardAddr: dashboardAddr, GRPCAddr: grpcAddr,
		ListenAddr: listener.Addr().String(), DBPath: cfg.DBPath, Version: version,
		PublicURL: cfg.PublicURL, TraceLinkURL: cfg.TraceLinkURL, TraceLinkName: cfg.TraceLinkName, AccessToken: cfg.AccessToken.Enabled(), IngestTokens: cfg.AccessToken.Names(access.ScopeIngest),
		ReadTokens:   cfg.AccessToken.Names(access.ScopeRead),
		TriageTokens: cfg.AccessToken.Names(access.ScopeTriage),
		ProxyHeader:  proxyHeader, Audit: auditLog,
		Notifications: notifications,
		SignIn:        signIn, SignOut: signOut, Tokens: cfg.AccessToken,
		ReceiverDiagnostics: receiver.Diagnostics,
		ReceiverConnections: receiver.Connections,
		RestoreTrace:        restore,
		AttributeSearch:     cfg.AttributeSearch,
		CollectorDoctor:     cfg.CollectorDoctor,
		TelemetryBudget:     tracker,
		RetentionDays:       cfg.RetentionDays, MaxDBBytes: maxBytes,
		IngestConcurrency: cfg.IngestSlots,
		ConfigSource:      cfg.Source,
		OnFull:            cfg.OnFull, Redaction: cfg.Redaction,
		ReadOnly: cfg.ReadOnly,
		Owners:   cfg.Owners,
	}); err != nil {
		return err
	}
	registerProbes(mux, db)
	guard := func(handler http.Handler) http.Handler {
		handler = cfg.AccessToken.Protect(handler)
		if options.public {
			handler = limitPerClient(access.Anyone(handler), publicRequestRate, publicRequestBurst, options.trustedProxies)
		}
		if cfg.ReadOnly {
			handler = access.ReadOnly(handler)
		}
		return handler
	}
	endpoints := []httpEndpoint{{listener, newHTTPServer(guard(mux))}}
	if dashboardListener != nil {
		registerProbes(dashboardMux, db)
		endpoints = append(endpoints, httpEndpoint{dashboardListener, newHTTPServer(guard(dashboardMux))})
	}
	if options.open {
		openDashboard(out, dashboardAddr)
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
