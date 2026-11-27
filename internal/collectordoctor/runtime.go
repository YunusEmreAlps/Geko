package collectordoctor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// validateCollector runs only an explicitly selected executable, never a shell
// or PATH lookup. Sources deliberately has no binary setting, so the dashboard
// cannot opt into executing a Collector or resolving configuration providers.
func validateCollector(ctx context.Context, opts Options, report *Report) {
	report.Coverage.Config = "runtime_validation_failed"
	failed := func() {
		report.Findings = append(report.Findings, Finding{"error", "runtime_validation_failed", "Collector configuration",
			"the explicitly selected Collector could not validate the configuration",
			"validation failed, was interrupted, or exceeded the 5s limit; process output omitted",
			"run the trusted Collector's validate command locally to inspect sensitive errors; verify binary, distribution and component options"})
	}
	if opts.ConfigFile == "" || strings.HasPrefix(strings.ReplaceAll(opts.CollectorBinary, `\`, "/"), "//") {
		failed()
		return
	}
	binary, err := filepath.Abs(opts.CollectorBinary)
	if err != nil {
		failed()
		return
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		failed()
		return
	}
	config, err := filepath.Abs(opts.ConfigFile)
	if err != nil {
		failed()
		return
	}
	if _, err := readFile(config); err != nil {
		failed()
		return
	}
	validationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(validationCtx, binary, "validate", "--config", config)
	// Capture zero output bytes. Collector errors and provider substitutions can
	// contain credentials; output remains bounded regardless of child verbosity.
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil || validationCtx.Err() != nil {
		failed()
		return
	}
	report.ConfigChecked = true
	report.Coverage.Config = "runtime_validated"
	report.Coverage.Excluded = []string{"running pipeline behavior and destination connectivity", "end-to-end delivery and confirmed loss"}
}
