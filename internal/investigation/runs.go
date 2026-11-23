package investigation

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

const RunSnapshotLimit = 100
const RunSnapshotVersion = 1

// Bound arrays while decoding portable/untrusted reports, before allocating
// a slice proportional to a malicious array of empty objects.
type boundedRunList[T any] []T

func (list *boundedRunList[T]) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return errors.New("run snapshot requires arrays")
	}
	values := boundedRunList[T]{}
	for decoder.More() {
		if len(values) >= RunSnapshotLimit {
			return errors.New("run snapshot array exceeds limit")
		}
		var value T
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		values = append(values, value)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	*list = values
	return nil
}

func ValidRunID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 16 && strings.Trim(id, "0") != "" && id == strings.ToLower(id)
}

type RunContext struct {
	Application string `json:"application"`
	Namespace   string `json:"namespace"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Traces      int64  `json:"traces"`
}

type RunGroup struct {
	ID             string `json:"group_id"`
	Application    string `json:"application"`
	Namespace      string `json:"namespace"`
	Service        string `json:"service"`
	Environment    string `json:"environment"`
	Operation      string `json:"operation"`
	Classification string `json:"classification"`
	ExceptionType  string `json:"exception_type,omitempty"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	Count          int64  `json:"trace_count"`
	TraceID        string `json:"trace_id"`
	SpanID         string `json:"span_id"`
}

type RunSnapshot struct {
	SchemaVersion    int       `json:"schema_version"`
	RunID            string    `json:"run_id"`
	CapturedAt       time.Time `json:"captured_at"`
	CollectionStatus string    `json:"collection_status"`
	// Counts describe retained traces at capture, never exporter coverage.
	Traces        int64        `json:"traces"`
	TaggedSpans   int64        `json:"tagged_spans"`
	FailingTraces int64        `json:"failing_traces"`
	Truncated     bool         `json:"truncated"`
	Contexts      []RunContext `json:"contexts"`
	Groups        []RunGroup   `json:"groups"`
}

func (s *RunSnapshot) UnmarshalJSON(data []byte) error {
	type snapshot RunSnapshot
	var value snapshot
	wire := struct {
		*snapshot
		Contexts boundedRunList[RunContext] `json:"contexts"`
		Groups   boundedRunList[RunGroup]   `json:"groups"`
	}{snapshot: &value}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	value.Contexts, value.Groups = wire.Contexts, wire.Groups
	*s = RunSnapshot(value)
	return nil
}

type RunGroupChange struct {
	State          string   `json:"state"`
	Group          RunGroup `json:"group"`
	BaselineCount  int64    `json:"baseline_count"`
	CandidateCount int64    `json:"candidate_count"`
}

type RunComparison struct {
	Status    string           `json:"status"`
	Reason    string           `json:"reason"`
	Baseline  RunSnapshot      `json:"baseline"`
	Candidate RunSnapshot      `json:"candidate"`
	Groups    []RunGroupChange `json:"groups"`
}

func UnavailableRun(id string) RunSnapshot {
	return RunSnapshot{SchemaVersion: RunSnapshotVersion, RunID: id, CapturedAt: time.Now().UTC(), CollectionStatus: "unavailable", Groups: []RunGroup{}, Contexts: []RunContext{}}
}

// ValidateRunSnapshot treats portable reports as untrusted input. Group IDs
// must agree with their displayed identity; notes, messages and stacks are not
// part of this bounded comparison artifact.
func ValidateRunSnapshot(s RunSnapshot) error {
	bad := errors.New("invalid or unsupported run snapshot")
	if s.SchemaVersion != RunSnapshotVersion || !ValidRunID(s.RunID) || s.CapturedAt.IsZero() ||
		(s.CollectionStatus != "available" && s.CollectionStatus != "unavailable") ||
		s.Traces < 0 || s.Traces > 1<<40 || s.TaggedSpans < s.Traces || s.TaggedSpans > 1<<48 ||
		s.FailingTraces < 0 || s.FailingTraces > s.Traces || len(s.Groups) > RunSnapshotLimit || len(s.Contexts) > RunSnapshotLimit {
		return bad
	}
	seen := map[string]bool{}
	var failures, traces int64
	for _, g := range s.Groups {
		rank := map[string]int{"exception": 1, "http_5xx": 2, "span_error": 3}[g.Classification]
		identity := runGroupIdentity(g)
		if rank == 0 || g.Count <= 0 || g.Count > s.FailingTraces || seen[identity] || !ValidRunID(g.TraceID) {
			return bad
		}
		span, err := hex.DecodeString(g.SpanID)
		if err != nil || len(span) != 8 || strings.Trim(g.SpanID, "0") == "" {
			return bad
		}
		key, err := DecodeGroupKey(g.ID)
		if err != nil {
			return bad
		}
		var fields []any
		if json.Unmarshal([]byte(key), &fields) != nil {
			return bad
		}
		canonical, _ := json.Marshal(fields)
		if string(canonical) != runGroupIdentity(g) {
			return bad
		}
		seen[identity] = true
		failures += g.Count
	}
	for _, c := range s.Contexts {
		if c.Traces <= 0 || c.Traces > s.Traces {
			return bad
		}
		for _, v := range []string{c.Application, c.Namespace, c.Service, c.Environment, c.Version} {
			if len(v) > 1024 {
				return bad
			}
		}
		traces += c.Traces
	}
	if failures > s.FailingTraces || traces > s.Traces ||
		(s.CollectionStatus == "available" && !s.Truncated && (failures != s.FailingTraces || traces != s.Traces)) {
		return bad
	}
	return nil
}

func CompareRunSnapshots(baseline, candidate RunSnapshot) (RunComparison, error) {
	result := RunComparison{Status: "unavailable", Baseline: baseline, Candidate: candidate, Groups: []RunGroupChange{}}
	if err := ValidateRunSnapshot(baseline); err != nil {
		return result, err
	}
	if err := ValidateRunSnapshot(candidate); err != nil {
		return result, err
	}
	if baseline.RunID == candidate.RunID {
		return result, errors.New("choose two different run IDs")
	}
	if baseline.CollectionStatus != "available" || candidate.CollectionStatus != "available" || baseline.Traces == 0 || candidate.Traces == 0 {
		result.Reason = "No retained tagged traces or a complete report is available for one run. Missing run IDs, no exports, retention or eviction can explain this; the failure count is unknown."
		return result, nil
	}
	if baseline.Truncated || candidate.Truncated {
		result.Reason = "A snapshot exceeds the bounded group/context limit. Absence cannot be inferred from truncated evidence."
		return result, nil
	}
	scope := func(s RunSnapshot) []string {
		keys := []string{}
		for _, c := range s.Contexts {
			if c.Service == "" {
				return nil
			}
			key, _ := json.Marshal([]string{c.Application, c.Namespace, c.Service, c.Environment})
			keys = append(keys, string(key))
		}
		slices.Sort(keys)
		return slices.Compact(keys)
	}
	left, right := scope(baseline), scope(candidate)
	if len(left) == 0 || !slices.Equal(left, right) {
		result.Reason = "The runs have different or unknown application/service/environment scope. Compare matching workloads instead. Versions may differ."
		return result, nil
	}
	result.Status = "partial"
	result.Reason = "Compared only observed, retained evidence at each capture time. Sampling, missing run tags, late spans, retention and eviction can hide failures. Not observed does not mean fixed; no group state is changed."
	old := map[string]RunGroup{}
	for _, g := range baseline.Groups {
		old[runGroupIdentity(g)] = g
	}
	for _, g := range candidate.Groups {
		change := RunGroupChange{State: "newly_observed", Group: g, CandidateCount: g.Count}
		if previous, ok := old[runGroupIdentity(g)]; ok {
			change.State = "recurring"
			change.BaselineCount = previous.Count
			delete(old, runGroupIdentity(g))
		}
		result.Groups = append(result.Groups, change)
	}
	for _, g := range old {
		result.Groups = append(result.Groups, RunGroupChange{State: "not_observed", Group: g, BaselineCount: g.Count})
	}
	slices.SortFunc(result.Groups, func(a, b RunGroupChange) int { return strings.Compare(a.Group.ID, b.Group.ID) })
	return result, nil
}

func runGroupIdentity(g RunGroup) string {
	rank := map[string]int{"exception": 1, "http_5xx": 2, "span_error": 3}[g.Classification]
	key, _ := json.Marshal([]any{g.Application, g.Namespace, g.Service, g.Environment, g.Operation, rank, g.ExceptionType, g.HTTPStatus})
	return string(key)
}
