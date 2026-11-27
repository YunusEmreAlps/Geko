package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/YunusEmreAlps/geko/internal/notify"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"go.yaml.in/yaml/v3"
)

var ErrOperationsInvalid = errors.New("use retention_days 1–3650, max_db_mib 8–65536, ingest_concurrency 1–64, a supported full/redaction policy, history_days 0–30, and a weekday/whole UTC hour for digest")
var ErrOperationsConfirm = errors.New("confirm the previewed retention/capacity or privacy effects before applying")

type OperationsPolicy struct {
	RetentionDays     int                `json:"retention_days"`
	MaxDBMiB          int                `json:"max_db_mib"`
	IngestConcurrency int                `json:"ingest_concurrency"`
	OnFull            storage.FullPolicy `json:"on_full"`
	Redaction         string             `json:"redaction"`
	BudgetHistoryDays int                `json:"budget_history_days"`
	Digest            string             `json:"digest"`
}
type OperationsState struct {
	Revision  string                   `json:"revision"`
	Policy    OperationsPolicy         `json:"policy"`
	Sources   map[string]SettingSource `json:"sources"`
	Editable  bool                     `json:"editable"`
	Reason    string                   `json:"reason,omitempty"`
	AppliedAt time.Time                `json:"applied_at"`
	Warning   string                   `json:"warning,omitempty"`
}

func (r *Runtime) Operations() OperationsPolicy     { return r.state.Load().operations }
func (r *Runtime) OperationsState() OperationsState { return operationsState(*r.state.Load()) }
func operationsState(s runtimeSnapshot) OperationsState {
	return OperationsState{Revision: s.Revision, Policy: s.operations, Sources: maps.Clone(s.opsSources), Editable: s.opsEditable, Reason: s.opsReason, AppliedAt: s.opsAppliedAt, Warning: s.Warning}
}
func (r *Runtime) DigestPolicy() notify.Digest {
	raw := r.Operations().Digest
	if raw == "" {
		return notify.Digest{}
	}
	d, _ := notify.ParseDigest(raw)
	return d
}
func initRuntimeOperations(s *runtimeSnapshot, cfg Config, data []byte, reason string) {
	digest := ""
	if cfg.Digest.Enabled {
		digest = fmt.Sprintf("%s %02d:00", cfg.Digest.Weekday, cfg.Digest.Hour)
	}
	s.operations = OperationsPolicy{cfg.RetentionDays, cfg.MaxDBMiB, cfg.IngestSlots, cfg.OnFull, cfg.Redaction, cfg.TelemetryBudget.HistoryDays, digest}
	s.hasBudget = cfg.TelemetryBudget.Enabled
	s.hasNotifications = cfg.WebhookURL != "" || len(cfg.ApplicationWebhooks) > 0
	s.opsSources = map[string]SettingSource{"retention_days": cfg.Source.RetentionDays, "max_db_mib": cfg.Source.MaxDBMiB, "ingest_concurrency": cfg.Source.IngestSlots, "on_full": cfg.Source.OnFull, "redaction": cfg.Source.Redaction, "budget_history_days": cfg.Source.BudgetHistory, "digest": cfg.Source.Digest}
	for key, source := range s.opsSources {
		if source.Kind == "" {
			s.opsSources[key] = SettingSource{Kind: "default"}
		}
	}
	s.opsReason = reason
	if reason != "" {
		return
	}
	doc, err := runtimeRootDocument(data)
	if err != nil {
		s.opsReason = "Operational edits require explicit YAML keys without root merges."
		return
	}
	for key := range s.opsSources {
		node := mappingValue(doc.Content[0], key)
		if key == "budget_history_days" {
			node = mappingValue(doc.Content[0], "telemetry_budget")
		}
		if node != nil && !plainOwnerNode(node) {
			s.opsReason = "Operational edits require unaliased YAML fields and Budget configuration."
			return
		}
	}
	s.opsEditable = true
}
func (p OperationsPolicy) values() map[string]any {
	return map[string]any{"retention_days": p.RetentionDays, "max_db_mib": p.MaxDBMiB, "ingest_concurrency": p.IngestConcurrency, "on_full": string(p.OnFull), "redaction": p.Redaction, "budget_history_days": p.BudgetHistoryDays, "digest": p.Digest}
}
func (p OperationsPolicy) NeedsConfirmation(old OperationsPolicy) bool {
	return p.RetentionDays < old.RetentionDays || p.MaxDBMiB < old.MaxDBMiB || p.OnFull.Evicts() && !old.OnFull.Evicts() || p.Redaction == "best_effort" && old.Redaction == "strict" || p.BudgetHistoryDays > 0 && (old.BudgetHistoryDays == 0 || p.BudgetHistoryDays < old.BudgetHistoryDays)
}
func validateOperations(s runtimeSnapshot, next OperationsPolicy) error {
	if next.RetentionDays < 1 || next.RetentionDays > 3650 || next.MaxDBMiB < 8 || next.MaxDBMiB > 65536 || next.IngestConcurrency < 1 || next.IngestConcurrency > 64 || !next.OnFull.Evicts() && next.OnFull != storage.Reject || next.Redaction != "strict" && next.Redaction != "best_effort" || next.BudgetHistoryDays < 0 || next.BudgetHistoryDays > 30 || next.BudgetHistoryDays > 0 && !s.hasBudget {
		return ErrOperationsInvalid
	}
	if next.Digest != "" {
		if !s.hasNotifications {
			return ErrOperationsInvalid
		}
		if _, err := notify.ParseDigest(next.Digest); err != nil {
			return ErrOperationsInvalid
		}
	}
	old := s.operations.values()
	for key, value := range next.values() {
		if s.opsSources[key].Kind == "env" && value != old[key] {
			return ErrSettingsLocked
		}
	}
	return nil
}
func (r *Runtime) PreviewOperations(expected string, next OperationsPolicy) (OperationsState, error) {
	s := *r.state.Load()
	if !s.opsEditable {
		return operationsState(s), ErrSettingsLocked
	}
	if s.Revision != expected {
		return operationsState(s), ErrSettingsConflict
	}
	return operationsState(s), validateOperations(s, next)
}
func (r *Runtime) UpdateOperations(ctx context.Context, expected string, next OperationsPolicy, confirmed bool) (OperationsState, error) {
	s, err := r.updateDocument(ctx, expected, liveOperations, func(s *runtimeSnapshot, doc *yaml.Node) error {
		if err := validateOperations(*s, next); err != nil {
			return err
		}
		if next.NeedsConfirmation(s.operations) && !confirmed {
			return ErrOperationsConfirm
		}
		s.opsSources = maps.Clone(s.opsSources)
		values := next.values()
		for _, key := range []string{"retention_days", "max_db_mib", "ingest_concurrency", "on_full", "redaction", "budget_history_days", "digest"} {
			value := values[key]
			if s.opsSources[key].Kind == "env" {
				continue
			}
			root := doc.Content[0]
			target := key
			if key == "budget_history_days" {
				root = mappingValue(root, "telemetry_budget")
				target = "history_days"
				if root == nil || root.Tag == "!!null" {
					if next.BudgetHistoryDays == 0 {
						continue
					}
					return ErrOperationsInvalid
				}
			}
			if root.Kind != yaml.MappingNode || !plainOwnerNode(root) && target == "history_days" {
				return ErrSettingsConflict
			}
			var node yaml.Node
			if node.Encode(value) != nil {
				return ErrOperationsInvalid
			}
			mergeOwnerNode(root, target, &node)
			s.opsSources[key] = SettingSource{Kind: "yaml"}
		}
		s.operations = next
		return nil
	})
	return operationsState(s), err
}
