package investigation

import "github.com/YunusEmreAlps/geko/internal/telemetry"

// ComparisonSpan excludes attributes and events: comparisons need identities,
// durations and evidence presence, not another copy of large stack traces.
type ComparisonSpan struct {
	SpanSummary
	Namespace, Environment string
	Kind                   telemetry.SpanKind
	HasEvidence            bool
}

type TraceComparison struct {
	Source                     TraceSummary
	Baseline                   *TraceSummary
	SourceSpans, BaselineSpans []ComparisonSpan
	Truncated                  bool
}
