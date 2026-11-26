package web

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

type operationIdentity struct {
	serviceKey
	operation string
	kind      telemetry.SpanKind
}

type operationTotal struct {
	Calls, Errors, Timed int
	DurationNS           *int64
	SpanID               string
	overflow             bool
}

type spanOperationRow struct {
	Operation, Application, Namespace, Service, Environment, Kind string
	Source, Baseline                                              operationTotal
	Difference                                                    string
	SourceURL, BaselineURL                                        string
}

func comparisonRows(source, baseline []investigation.ComparisonSpan) []spanOperationRow {
	rows := make(map[operationIdentity]*spanOperationRow)
	for side, spans := range [][]investigation.ComparisonSpan{source, baseline} {
		for _, span := range spans {
			key := operationIdentity{serviceKey{span.Application, span.Namespace, span.Service, span.Environment}, span.Operation, span.Kind}
			row := rows[key]
			if row == nil {
				row = &spanOperationRow{Operation: span.Operation, Application: span.Application, Namespace: span.Namespace, Service: span.Service, Environment: span.Environment, Kind: comparisonKind(span.Kind)}
				rows[key] = row
			}
			total := &row.Source
			if side == 1 {
				total = &row.Baseline
			}
			if total.Calls == 0 || span.HasEvidence && total.Errors == 0 {
				total.SpanID = span.SpanID
			}
			total.Calls++
			if span.HasEvidence {
				total.Errors++
			}
			if span.DurationNS != nil {
				total.Timed++
				if total.DurationNS == nil {
					total.DurationNS = new(int64)
				}
				if *span.DurationNS < 0 || *total.DurationNS > math.MaxInt64-*span.DurationNS {
					total.overflow = true
				} else {
					*total.DurationNS += *span.DurationNS
				}
			}
		}
	}
	result := make([]spanOperationRow, 0, len(rows))
	for _, row := range rows {
		for _, total := range []*operationTotal{&row.Source, &row.Baseline} {
			if total.Timed != total.Calls || total.overflow {
				total.DurationNS = nil
			}
		}
		switch {
		case row.Source.Calls == 0:
			row.Difference = "Not observed in trace with evidence"
		case row.Baseline.Calls == 0:
			row.Difference = "Not observed in successful trace"
		case row.Source.Calls != row.Baseline.Calls:
			row.Difference = "Call count differs"
		case row.Source.DurationNS == nil || row.Baseline.DurationNS == nil:
			row.Difference = "Duration unavailable"
		default:
			delta := *row.Source.DurationNS - *row.Baseline.DurationNS
			if delta < 0 {
				delta = -delta
				row.Difference = formatDuration(&delta) + " less total span time"
			} else if delta > 0 {
				row.Difference = formatDuration(&delta) + " more total span time"
			} else {
				row.Difference = "Same observed total span time"
			}
		}
		if row.Source.Errors != row.Baseline.Errors {
			row.Difference = fmt.Sprintf("%d vs %d spans with evidence; %s", row.Source.Errors, row.Baseline.Errors, row.Difference)
		}
		result = append(result, *row)
	}
	slices.SortFunc(result, func(a, b spanOperationRow) int {
		return cmp.Or(cmp.Compare(b.Source.Errors, a.Source.Errors), cmp.Compare(a.Application, b.Application), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Service, b.Service), cmp.Compare(a.Environment, b.Environment), cmp.Compare(a.Operation, b.Operation), cmp.Compare(a.Kind, b.Kind))
	})
	return result
}

func comparisonKind(kind telemetry.SpanKind) string {
	switch kind {
	case telemetry.KindServer:
		return "server"
	case telemetry.KindClient:
		return "client"
	case telemetry.KindInternal:
		return "internal"
	case telemetry.KindProducer:
		return "producer"
	case telemetry.KindConsumer:
		return "consumer"
	default:
		return "other"
	}
}
