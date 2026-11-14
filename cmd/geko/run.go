package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// exitError carries a child process's exit code through run without printing.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

const runFlushWait = 2 * time.Second

// runCommand runs a command with exporter settings pointing at Geko, then
// summarizes the failures Geko received while it ran. It starts a server for
// the duration when none is listening. Traces are matched by receipt time, so
// the summary can include failures from other clients of the same Geko. The
// summary comes from the listening server, which may not use cfg.DBPath.
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
	token := os.Getenv("GEKO_ACCESS_TOKEN")
	// The query API and metrics move to the dashboard listener when it is set.
	queryBase := base
	if cfg.DashboardAddr != "" {
		queryBase = "http://" + cfg.DashboardAddr
	}
	before, countErr := readReceiverCounts(ctx, queryBase, token)
	if errors.Is(countErr, errTokenRequired) {
		fmt.Fprintf(stderr, "geko: %v; the command's exports will be rejected\n", countErr)
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
	var counts *receiverCounts
	if countErr == nil {
		counts = &before
	}
	if err := summarizeRun(ctx, cfg, queryBase, token, counts, start, end, embedded, stderr); err != nil {
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

var errTokenRequired = errors.New("set GEKO_ACCESS_TOKEN to the access token required by Geko")

// receiverCounts are receiver counters from Geko's /metrics. They count all
// clients of the server since it started.
type receiverCounts struct {
	accepted, rejected int64
}

func readReceiverCounts(ctx context.Context, base, token string) (receiverCounts, error) {
	var counts receiverCounts
	body, err := gekoGet(ctx, base+"/metrics", token)
	if err != nil {
		return counts, err
	}
	found := false
	for line := range strings.Lines(string(body)) {
		line = strings.TrimSpace(line)
		cut := strings.LastIndexByte(line, ' ')
		if cut < 0 || strings.HasPrefix(line, "#") {
			continue
		}
		name := line[:cut]
		value, err := strconv.ParseInt(line[cut+1:], 10, 64)
		if err != nil {
			continue
		}
		switch {
		case name == "geko_receiver_accepted_exports_total":
			counts.accepted, found = value, true
		case strings.HasPrefix(name, "geko_receiver_rejected_requests_total{"):
			counts.rejected += value
		}
	}
	if !found {
		return counts, errors.New("missing receiver counters in the Geko metrics response")
	}
	return counts, nil
}

func gekoGet(ctx context.Context, endpoint, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	switch {
	case err != nil:
		return nil, err
	case response.StatusCode == http.StatusUnauthorized:
		return nil, errTokenRequired
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s: %s", req.URL.Path, response.Status)
	}
	return body, nil
}

// summarizeRun reports the failures received during the run. With counters
// from before the run, it also tells an idle or rejected exporter apart from
// a run without failures.
func summarizeRun(ctx context.Context, cfg config.Config, base, token string, before *receiverCounts, start, end time.Time, embedded bool, out io.Writer) error {
	from, to := start.UTC(), end.UTC()
	window := from.Format(time.RFC3339) + " to " + to.Format(time.RFC3339)
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
	body, err := gekoGet(ctx, base+"/api/v1/failures?"+query.Encode()+"&limit=10", token)
	if err != nil {
		return err
	}
	var page queryPage[investigation.TraceSummary]
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decode failures: %w", err)
	}
	if before != nil {
		if after, err := readReceiverCounts(ctx, base, token); err == nil {
			if rejected := after.rejected - before.rejected; rejected > 0 {
				fmt.Fprintf(out, "\nGeko: rejected %d export requests from %s. Settings lists the reasons.\n", rejected, window)
			} else if after.accepted == before.accepted && len(page.Items) == 0 {
				_, err = fmt.Fprintf(out, "\nGeko: received no exports from %s. Check that the command exports OTLP traces to http://%s.\n", window, cfg.ListenAddr)
				return err
			}
		}
	}
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
	if page.NextCursor != "" {
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
	_, err = fmt.Fprintf(out, "  All: %s/failures?%s\n  Agents: list_failures with from=%s and to=%s\n",
		dashboard, query.Encode(), from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	if err == nil && embedded {
		_, err = fmt.Fprintln(out, "  Geko ran only for this command; start geko serve to open the links.")
	}
	return err
}
