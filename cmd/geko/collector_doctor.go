package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/collectordoctor"
	"github.com/YunusEmreAlps/geko/internal/config"
)

func collectorDoctorCommand(ctx context.Context, args []string, out io.Writer) error {
	var opts collectordoctor.Options
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(out)
	configured := flags.Bool("collector", false, "use collector_doctor from Geko's configuration")
	asJSON := flags.Bool("json", false, "output the Collector report as JSON")
	flags.StringVar(&opts.CollectorBinary, "collector-binary", "", "explicit trusted local Collector executable for validate (5s limit)")
	flags.StringVar(&opts.ConfigFile, "collector-config", "", "resolved Collector YAML file")
	flags.StringVar(&opts.MetricsURL, "collector-metrics", "", "Collector internal Prometheus metrics URL")
	flags.StringVar(&opts.MetricsFile, "metrics-file", "", "saved Collector metrics")
	flags.StringVar(&opts.BaselineFile, "baseline", "", "earlier metrics snapshot from the same Collector")
	flags.DurationVar(&opts.SampleInterval, "sample", 0, "time between two live scrapes, up to 30s")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *configured {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if opts.ConfigFile == "" {
			opts.ConfigFile = cfg.CollectorDoctor.ConfigFile
		}
		if opts.MetricsURL == "" && opts.MetricsFile == "" {
			opts.MetricsURL = cfg.CollectorDoctor.MetricsURL
		}
	}
	if flags.NArg() != 0 || !opts.Sources.Enabled() && opts.MetricsFile == "" {
		return errors.New("select a Collector source with --collector, --collector-config, --collector-metrics or --metrics-file")
	}
	if opts.CollectorBinary != "" && opts.ConfigFile == "" {
		return errors.New("--collector-binary requires --collector-config or a configured Collector file")
	}
	if opts.MetricsFile != "" && opts.MetricsURL != "" {
		return errors.New("select --collector-metrics or --metrics-file")
	}
	if opts.SampleInterval < 0 || opts.SampleInterval > 30*time.Second ||
		opts.SampleInterval > 0 && (opts.MetricsURL == "" || opts.BaselineFile != "") {
		return errors.New("--sample requires live metrics, no --baseline, and a duration up to 30s")
	}
	if opts.BaselineFile != "" && opts.MetricsFile == "" && opts.MetricsURL == "" {
		return errors.New("--baseline requires current metrics")
	}
	report := collectordoctor.Inspect(ctx, opts)
	if *asJSON {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return err
		}
	} else {
		var body strings.Builder
		fmt.Fprintln(&body, "OpenTelemetry Collector Doctor")
		fmt.Fprintf(&body, "Validation coverage: config=%s; metrics=%s\n", report.Coverage.Config, report.Coverage.Metrics)
		fmt.Fprintf(&body, "Excluded: %s\n", strings.Join(report.Coverage.Excluded, "; "))
		if len(report.Findings) == 0 {
			fmt.Fprintln(&body, "No findings within the validation coverage shown above.")
		}
		for _, finding := range report.Findings {
			fmt.Fprintf(&body, "\n[%s] %s (%s)\n  Evidence: %s\n  Action: %s\n",
				finding.Severity, finding.Reason, finding.Subject, finding.Evidence, finding.Recommendation)
		}
		if _, err := io.WriteString(out, body.String()); err != nil {
			return err
		}
	}
	if report.HasErrors() {
		return errors.New("errors reported by Collector Doctor")
	}
	return nil
}
