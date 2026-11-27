package budget

import "slices"

// ReportingLimits changes interpretation of measurements, never collection,
// retention, checkpoint workers or the fingerprint seed.
type ReportingLimits struct {
	Defaults         Limits         `yaml:"default" json:"default"`
	Services         []ServiceLimit `yaml:"services" json:"services"`
	CardinalityLimit int            `yaml:"cardinality_limit" json:"cardinality_limit"`
}

// Policy is immutable; its compiled map is never exposed to callers.
// The zero value uses unlimited volume and a cardinality threshold of 100.
type Policy struct {
	historyDays int
	values      ReportingLimits
	services    map[identity]Limits
}

func NewPolicy(values ReportingLimits) (Policy, error) {
	if err := (Config{Defaults: values.Defaults, Services: values.Services, CardinalityLimit: values.CardinalityLimit}).Validate(); err != nil {
		return Policy{}, err
	}
	values.Services = slices.Clone(values.Services)
	p := Policy{values: values, services: make(map[identity]Limits, len(values.Services))}
	for _, s := range values.Services {
		p.services[identity{s.Namespace, s.Service}] = s.Limits
	}
	return p, nil
}

func (p Policy) Limits() ReportingLimits {
	values := p.values
	values.Services = slices.Clone(values.Services)
	return values
}
func (p Policy) threshold() int {
	if p.values.CardinalityLimit == 0 {
		return 100
	}
	return p.values.CardinalityLimit
}

// WithPolicySource installs the server's immutable settings supplier. Call once
// during startup, before the tracker is shared with concurrent consumers.
func (t *Tracker) WithPolicySource(source func() Policy) *Tracker {
	if t != nil {
		t.policySource = source
	}
	return t
}
func (t *Tracker) policy() Policy {
	if t.policySource != nil {
		return t.policySource()
	}
	return t.basePolicy
}

// WithHistoryDays changes persistence/report retention, never the counters.
func (p Policy) WithHistoryDays(days int) Policy {
	if days >= 0 && days <= 30 {
		p.historyDays = days
	}
	return p
}
