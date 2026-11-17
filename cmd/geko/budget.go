package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/config"
)

func budgetCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("budget", flag.ContinueOnError)
	flags.SetOutput(out)
	server := flags.String("server", "", "Geko server URL; set GEKO_TOKEN for read access")
	previous := flags.Bool("previous", false, "report the previous UTC hour")
	asJSON := flags.Bool("json", false, "output the report as JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: geko budget [--server URL] [--previous] [--json]")
	}
	token := os.Getenv("GEKO_TOKEN")
	if *server == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		addr := cfg.ListenAddr
		if cfg.DashboardAddr != "" {
			addr = cfg.DashboardAddr
		}
		*server = "http://" + addr
		if token == "" {
			token = os.Getenv("GEKO_ACCESS_TOKEN")
		}
	}
	base, err := remoteServerURL(*server)
	if err != nil {
		return err
	}
	endpoint := base.JoinPath("api", "v1", "budget").String()
	if *previous {
		endpoint += "?previous=true"
	}
	body, err := gekoGet(ctx, endpoint, token)
	if errors.Is(err, errTokenRequired) {
		return errors.New("set GEKO_TOKEN to a read token or the access token")
	}
	if err != nil {
		return fmt.Errorf("read telemetry budget (enable telemetry_budget on the Geko server): %w", err)
	}
	var report budget.Report
	if err := json.Unmarshal(body, &report); err != nil {
		return fmt.Errorf("decode telemetry budget: %w", err)
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(report)
	}
	if !report.Available {
		_, err = fmt.Fprintln(out, "No measurements are available for the requested hour.")
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Telemetry budget %s to %s\nObserved since %s: %d spans, %d protobuf span bytes\n",
		report.Start.Format(time.RFC3339), report.End.Format(time.RFC3339),
		report.ObservedSince.Format(time.RFC3339), report.Total.Spans, report.Total.Bytes)
	for _, service := range report.Services {
		name := service.Name
		if service.Namespace != "" {
			name = service.Namespace + "/" + name
		}
		fmt.Fprintf(&text, "  %s: %d spans, %d bytes", name, service.Spans, service.Bytes)
		if service.SpanOverage > 0 || service.ByteOverage > 0 {
			fmt.Fprintf(&text, " (over budget by %d spans and %d bytes)", service.SpanOverage, service.ByteOverage)
		}
		fmt.Fprintln(&text)
		for _, attribute := range service.HighCardinality {
			minimum := ""
			if attribute.AtLeast {
				minimum = "at least "
			}
			fmt.Fprintf(&text, "    %s: %s%d distinct values\n", attribute.Key, minimum, attribute.Distinct)
		}
	}
	if report.Other.Spans > 0 {
		fmt.Fprintf(&text, "  Other services: %d spans, %d bytes\n", report.Other.Spans, report.Other.Bytes)
	}
	for _, advice := range report.Advice {
		fmt.Fprintln(&text, "  "+advice)
	}
	_, err = io.WriteString(out, text.String())
	return err
}
