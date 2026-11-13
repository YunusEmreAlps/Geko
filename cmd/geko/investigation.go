package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type failureArgs struct {
	Search          string `json:"q,omitempty" jsonschema:"Trace ID, operation, service or exception search text"`
	Service         string `json:"service,omitempty" jsonschema:"Request service"`
	EvidenceService string `json:"evidence_service,omitempty" jsonschema:"Service that recorded the failure evidence"`
	Application     string `json:"application,omitempty" jsonschema:"ungrouped or group:<name>"`
	Status          int    `json:"status,omitempty" jsonschema:"Request HTTP status"`
	From            string `json:"from,omitempty" jsonschema:"Inclusive last receipt time in RFC3339"`
	To              string `json:"to,omitempty" jsonschema:"Exclusive last receipt time in RFC3339"`
	Limit           int    `json:"limit,omitempty" jsonschema:"Page size, 1 to 100"`
	Cursor          string `json:"cursor,omitempty" jsonschema:"Next cursor from a previous page"`
}

type queryPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (args failureArgs) filter() (investigation.FailureFilter, error) {
	params := url.Values{
		"q": {args.Search}, "service": {args.Service}, "evidence_service": {args.EvidenceService},
		"application": {args.Application}, "from": {args.From}, "to": {args.To}, "cursor": {args.Cursor},
	}
	if args.Status != 0 {
		params.Set("status", strconv.Itoa(args.Status))
	}
	if args.Limit != 0 {
		params.Set("limit", strconv.Itoa(args.Limit))
	}
	return api.ParseFailureFilter(params)
}

func readFailures(ctx context.Context, store api.Reader, filter investigation.FailureFilter) (queryPage[investigation.TraceSummary], error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	page, err := store.Failures(ctx, filter)
	return queryPage[investigation.TraceSummary]{page.Items, api.EncodeCursor(page.Next)}, err
}

func openInvestigation(ctx context.Context) (*sqlite.Reader, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.DBPath); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no Geko database at %s; start geko serve once or set GEKO_DB_PATH", cfg.DBPath)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	store, err := sqlite.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cfg.DBPath, err)
	}
	return store, nil
}

func failuresCommand(ctx context.Context, argv []string, out io.Writer) error {
	var args failureArgs
	flags := flag.NewFlagSet("failures", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "write JSON to stdout")
	flags.StringVar(&args.Search, "q", "", "search text")
	flags.StringVar(&args.Service, "service", "", "request service")
	flags.StringVar(&args.EvidenceService, "evidence-service", "", "service that recorded the evidence")
	flags.StringVar(&args.Application, "application", "", "ungrouped or group:<name>")
	flags.IntVar(&args.Status, "status", 0, "request HTTP status")
	flags.StringVar(&args.From, "from", "", "inclusive last receipt time (RFC3339)")
	flags.StringVar(&args.To, "to", "", "exclusive last receipt time (RFC3339)")
	flags.IntVar(&args.Limit, "limit", investigation.DefaultPageSize, "page size")
	flags.StringVar(&args.Cursor, "cursor", "", "next cursor")
	if err := flags.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(out)
			flags.Usage()
			return nil
		}
		return err
	}
	if !*jsonOutput || flags.NArg() != 0 {
		return errors.New("usage: geko failures --json [filters]")
	}
	if args.Limit < 1 || args.Limit > investigation.MaxPageSize {
		return fmt.Errorf("--limit must be between 1 and %d", investigation.MaxPageSize)
	}
	filter, err := args.filter()
	if err != nil {
		return err
	}
	store, err := openInvestigation(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := readFailures(ctx, store, filter)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

type traceArgs struct {
	TraceID string `json:"trace_id" jsonschema:"32-character hexadecimal trace ID"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Page size, 1 to 100"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"Next cursor from a previous page"`
}

type traceResult struct {
	Trace      investigation.TraceSummary `json:"trace"`
	Spans      []spanTiming               `json:"spans"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

type spanTiming struct {
	investigation.SpanSummary
	ParentID string               `json:"parent_span_id,omitempty"`
	Start    time.Time            `json:"start"`
	End      time.Time            `json:"end"`
	Status   telemetry.StatusCode `json:"status_code" jsonschema:"OpenTelemetry span status: 0 unset, 1 ok, 2 error"`
}

func readTrace(ctx context.Context, store api.Reader, args traceArgs) (traceResult, error) {
	id, err := api.ParseTraceID(args.TraceID)
	if err != nil {
		return traceResult{}, err
	}
	params := url.Values{"cursor": {args.Cursor}}
	if args.Limit != 0 {
		params.Set("limit", strconv.Itoa(args.Limit))
	}
	limit, after, err := api.ParsePagination(params, len(telemetry.SpanID{}))
	if err != nil {
		return traceResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	summary, page, err := store.Trace(ctx, id, after, limit)
	if err != nil {
		return traceResult{}, err
	}
	spans := make([]spanTiming, 0, len(page.Items))
	for _, span := range page.Items {
		spans = append(spans, spanTiming{span.SpanSummary, span.ParentID, span.Start, span.End, span.StatusCode})
	}
	return traceResult{summary, spans, api.EncodeCursor(page.Next)}, nil
}
