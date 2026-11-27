package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"go.yaml.in/yaml/v3"
)

const MaxLiveBudgetYAML = 128 << 10

var ErrBudgetInvalid = errors.New("use default limits, up to 128 distinct namespace/service rules with nonnegative limits, and cardinality_limit from 0 to 255; collection settings, YAML aliases and merges are not supported")

type BudgetSources struct {
	DefaultSpans SettingSource `json:"default_spans"`
	DefaultBytes SettingSource `json:"default_bytes"`
	Services     SettingSource `json:"services"`
	Cardinality  SettingSource `json:"cardinality_limit"`
}
type BudgetState struct {
	Revision  string                 `json:"revision"`
	Limits    budget.ReportingLimits `json:"limits"`
	Sources   BudgetSources          `json:"sources"`
	Editable  bool                   `json:"editable"`
	Reason    string                 `json:"reason,omitempty"`
	AppliedAt time.Time              `json:"applied_at"`
	Warning   string                 `json:"warning,omitempty"`
	YAML      string                 `json:"-"`
}

func (r *Runtime) BudgetPolicy() budget.Policy {
	s := r.state.Load()
	return s.budgetPolicy.WithHistoryDays(s.operations.BudgetHistoryDays)
}
func (r *Runtime) BudgetState() BudgetState { return budgetState(*r.state.Load()) }
func budgetState(s runtimeSnapshot) BudgetState {
	limits := s.budgetPolicy.Limits()
	data, _ := yaml.Marshal(limits)
	return BudgetState{Revision: s.Revision, Limits: limits, Sources: s.budgetSources, Editable: s.budgetEditable, Reason: s.budgetReason, AppliedAt: s.budgetAppliedAt, Warning: s.Warning, YAML: string(data)}
}
func initRuntimeBudget(s *runtimeSnapshot, cfg Config, data []byte, reason string) {
	s.budgetPolicy, _ = budget.NewPolicy(budget.ReportingLimits{Defaults: cfg.TelemetryBudget.Defaults, Services: cfg.TelemetryBudget.Services, CardinalityLimit: cfg.TelemetryBudget.CardinalityLimit})
	s.budgetSources = cfg.Source.Budget
	if s.budgetSources.DefaultSpans.Kind == "" {
		s.budgetSources = defaultBudgetSources()
	}
	s.budgetReason = reason
	if reason != "" {
		return
	}
	doc, err := runtimeRootDocument(data)
	if err != nil {
		s.budgetReason = "Live Budget edits require a configuration without root YAML merges."
		return
	}
	node := mappingValue(doc.Content[0], "telemetry_budget")
	if node != nil && (node.Kind != yaml.MappingNode || !plainOwnerNode(node)) {
		s.budgetReason = "Live Budget edits require a mapping without YAML aliases, anchors or merges in telemetry_budget."
		return
	}
	if !cfg.TelemetryBudget.Enabled {
		s.budgetReason = "Enable telemetry_budget in YAML and restart Geko before editing reporting limits."
		return
	}
	s.budgetEditable = true
}
func mappingValue(root *yaml.Node, key string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}
func ParseBudgetLimits(data []byte) (budget.ReportingLimits, error) {
	var values budget.ReportingLimits
	if len(data) > MaxLiveBudgetYAML {
		return values, ErrBudgetInvalid
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || !plainOwnerNode(doc.Content[0]) {
		return values, ErrBudgetInvalid
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if decoder.Decode(&values) != nil {
		return values, ErrBudgetInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return values, ErrBudgetInvalid
	}
	if _, err := budget.NewPolicy(values); err != nil {
		return values, ErrBudgetInvalid
	}
	return values, nil
}
func (r *Runtime) UpdateBudget(ctx context.Context, expected string, values budget.ReportingLimits) (BudgetState, error) {
	s, err := r.updateDocument(ctx, expected, liveBudget, func(s *runtimeSnapshot, doc *yaml.Node) error {
		policy, err := budget.NewPolicy(values)
		if err != nil {
			return ErrBudgetInvalid
		}
		node := mappingValue(doc.Content[0], "telemetry_budget")
		if node == nil || node.Kind != yaml.MappingNode || !plainOwnerNode(node) {
			return ErrSettingsConflict
		}
		var next yaml.Node
		if next.Encode(policy.Limits()) != nil {
			return ErrBudgetInvalid
		}
		// Update only reporting fields; collection policy and worker configuration
		// remain untouched. Keep existing nodes/comments wherever fields match.
		for i := 0; i < len(next.Content); i += 2 {
			if next.Content[i].Value == "services" {
				preserveBudgetServiceComments(mappingValue(node, "services"), next.Content[i+1])
			}
			mergeOwnerNode(node, next.Content[i].Value, next.Content[i+1])
		}
		s.budgetPolicy = policy
		source := SettingSource{Kind: "yaml"}
		s.budgetSources = BudgetSources{source, source, source, source}
		return nil
	})
	return budgetState(s), err
}

// Match bounded service rules by identity, preserving comments even if their
// order changes. Never reuse an old policy node in a published snapshot.
func preserveBudgetServiceComments(old, next *yaml.Node) {
	if old == nil || old.Kind != yaml.SequenceNode || next.Kind != yaml.SequenceNode {
		return
	}
	identity := func(n *yaml.Node) string {
		namespace, service := mappingValue(n, "namespace"), mappingValue(n, "service")
		ns, name := "", ""
		if namespace != nil {
			ns = namespace.Value
		}
		if service != nil {
			name = service.Value
		}
		return ns + "\x00" + name
	}
	for i, rule := range next.Content {
		for _, previous := range old.Content {
			if identity(rule) != identity(previous) {
				continue
			}
			holder := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "rule"}, previous}}
			mergeOwnerNode(holder, "rule", rule)
			next.Content[i] = holder.Content[1]
			break
		}
	}
}
