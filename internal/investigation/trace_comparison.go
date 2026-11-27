package investigation

import (
	"errors"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

var (
	ErrBaselineNotFound   = errors.New("selected baseline trace is not retained")
	ErrBaselineIneligible = errors.New("select a different HTTP 2xx trace without failure evidence, matching the source entry's operation, service, application, namespace and environment")
	ErrComparisonSource   = errors.New("the source needs recorded failure evidence and a unique entry span")
)

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
