package investigation

import "time"

const ChoiceTraceLimit = 1000
const ChoiceSpanLimit = 10000
const ChoiceLimit = 100

type ComparisonChoice struct {
	Value, Service, Namespace, Environment string
	ObservedAt                             time.Time
}

// Suggestions are examples of recent retained contexts, not exhaustive lists.
type ComparisonChoices struct {
	Versions, Runs []ComparisonChoice
	Services       []string
	Truncated      bool
}
