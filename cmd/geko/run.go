package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

// exitError carries a child process's exit code through run without printing.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

const runFlushWait = 2 * time.Second

// runCommand runs a command with exporter settings pointing at Geko, then
// summarizes the failures Geko received while it ran. It starts a server for
// the duration when none is listening. Traces are matched by receipt time, so
// the summary can include failures from other clients of the same Geko.
func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("usage: geko run -- <command> [arguments]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	base := "http://" + cfg.ListenAddr
	embedded := !serverReady(ctx, base, cfg.AccessToken.Enabled())
	if embedded {
		stop, err := startEmbedded(ctx, cfg, base)
		if err != nil {
			return err
		}
		defer stop()
	}

	child := exec.Command(args[0], args[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, stdout, stderr
	child.Env = runEnvironment(os.Environ(), base)
	start := time.Now()
	runErr := child.Run()
	var exitCode *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitCode) {
		return fmt.Errorf("run %s: %w", args[0], runErr)
	}
	// Batch span processors export after the command's last request; wait so
	// those spans arrive before the window closes.
	select {
	case <-ctx.Done():
	case <-time.After(runFlushWait):
	}
	end := time.Now()
	if err := summarizeRun(ctx, cfg, start, end, embedded, stderr); err != nil {
		fmt.Fprintf(stderr, "geko: could not summarize the run: %v\n", err)
	}
	if exitCode != nil {
		return exitError(exitCode.ExitCode())
	}
	return nil
}

// runEnvironment adds exporter settings the caller hasn't set. The access
// token is passed through as an OTLP header when one is configured.
func runEnvironment(environ []string, base string) []string {
	defaults := []string{
		"OTEL_TRACES_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + base, "OTEL_METRICS_EXPORTER=none", "OTEL_LOGS_EXPORTER=none",
		"OTEL_BSP_SCHEDULE_DELAY=500",
	}
	if token := os.Getenv("GEKO_ACCESS_TOKEN"); token != "" {
		defaults = append(defaults, "OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer%20"+token)
	}
	set := make(map[string]bool)
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		set[strings.ToUpper(name)] = true
	}
	result := append([]string(nil), environ...)
	for _, entry := range defaults {
		name, _, _ := strings.Cut(entry, "=")
		if !set[name] {
			result = append(result, entry)
		}
	}
	return result
}

func serverReady(ctx context.Context, base string, token bool) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	response.Body.Close()
	// Older servers require the token for /healthz; any Geko answer will do.
	return response.StatusCode == http.StatusNoContent || token && response.StatusCode == http.StatusUnauthorized
}

func startEmbedded(ctx context.Context, cfg config.Config, base string) (func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, io.Discard) }()
	deadline := time.Now().Add(10 * time.Second)
	for !serverReady(ctx, base, cfg.AccessToken.Enabled()) {
		select {
		case err := <-done:
			cancel()
			return nil, fmt.Errorf("start Geko for the run: %w", err)
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			return nil, errors.New("start Geko for the run: the listener did not become ready")
		}
	}
	return func() { cancel(); <-done }, nil
}

func summarizeRun(ctx context.Context, cfg config.Config, start, end time.Time, embedded bool, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	store, err := sqlite.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	from, to := start.UTC(), end.UTC()
	page, err := store.Failures(ctx, investigation.FailureFilter{From: &from, To: &to, Limit: 10})
	if err != nil {
		return err
	}
	window := from.Format(time.RFC3339) + " to " + to.Format(time.RFC3339)
	if len(page.Items) == 0 {
		_, err = fmt.Fprintf(out, "\nGeko: no failure evidence received from %s.\n", window)
		return err
	}
	dashboard := cfg.PublicURL
	if dashboard == "" {
		dashboard = "http://" + cfg.ListenAddr
		if cfg.DashboardAddr != "" {
			dashboard = "http://" + cfg.DashboardAddr
		}
	}
	more := ""
	if page.Next != nil {
		more = " (first 10)"
	}
	fmt.Fprintf(out, "\nGeko: failure evidence received from %s%s. Matched by time, not by test:\n", window, more)
	for _, trace := range page.Items {
		operation, service := trace.TraceID, ""
		if trace.EntrySpan != nil {
			operation, service = trace.EntrySpan.Operation, trace.EntrySpan.Service
		} else if trace.Evidence != nil {
			operation, service = trace.Evidence.Operation, trace.Evidence.Service
		}
		evidence := ""
		if trace.Evidence != nil {
			evidence = " · " + trace.Evidence.Classification
			if trace.Evidence.ExceptionType != "" {
				evidence += " " + trace.Evidence.ExceptionType
			}
		}
		fmt.Fprintf(out, "  %s  %s%s\n    %s/traces/%s\n", operation, service, evidence, dashboard, trace.TraceID)
	}
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
	_, err = fmt.Fprintf(out, "  All: %s/failures?%s\n  Agents: list_failures with from=%s and to=%s\n",
		dashboard, query.Encode(), from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	if err == nil && embedded {
		_, err = fmt.Fprintln(out, "  Geko ran only for this command; start geko serve to open the links.")
	}
	return err
}
