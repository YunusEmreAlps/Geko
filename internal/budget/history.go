package budget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
)

const MaxHistoryServices = 256
const CheckpointInterval = time.Minute

type Volume struct {
	Namespace   string        `json:"namespace"`
	Service     string        `json:"service"`
	Spans       int64         `json:"spans"`
	Bytes       int64         `json:"bytes"`
	Cardinality []Cardinality `json:"cardinality,omitempty"`
}

// Checkpoint stores cumulative volume and bounded counts for one session/hour. No attribute
// values, fingerprints, cardinality sets or credentials cross this boundary.
// Only bounded per-attribute counts are persisted; cross-session union is unknown.
type Checkpoint struct {
	CardinalityMeasured bool      `json:"cardinality_measured,omitempty"`
	Session             string    `json:"session"`
	Start               time.Time `json:"start"`
	Since               time.Time `json:"since"`
	Until               time.Time `json:"until"`
	Spans               int64     `json:"spans"`
	Bytes               int64     `json:"bytes"`
	OtherSpans          int64     `json:"other_spans"`
	OtherBytes          int64     `json:"other_bytes"`
	Services            []Volume  `json:"services"`
}

type HistoryReader interface {
	ReadBudgetHistory(context.Context, time.Time, string, func(Checkpoint) error) (int64, error)
}

func newSessionID() string { var id [16]byte; rand.Read(id[:]); return hex.EncodeToString(id[:]) }
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (t *Tracker) HistoryDays() int { return t.policy().historyDays }
func (t *Tracker) Session() string  { return t.session }

func ParseHour(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04"} {
		value, err := time.Parse(layout, raw)
		if err == nil && value.Equal(value.UTC().Truncate(time.Hour)) {
			return value.UTC(), nil
		}
	}
	return time.Time{}, errors.New("choose a whole UTC hour, such as 2026-11-24T08:00:00Z")
}

func (c Checkpoint) Validate() error {
	bad := errors.New("invalid budget checkpoint")
	id, err := hex.DecodeString(c.Session)
	if err != nil || len(id) != 16 || c.Start.IsZero() || !c.Start.Equal(c.Start.UTC().Truncate(time.Hour)) || c.Since.Before(c.Start) || c.Until.Before(c.Since) || c.Until.After(c.Start.Add(time.Hour)) || len(c.Services) > MaxHistoryServices {
		return bad
	}
	spans, bytes := c.OtherSpans, c.OtherBytes
	if spans < 0 || bytes < 0 || c.Spans < 0 || c.Spans > 1<<40 || c.Bytes < 0 || c.Bytes > 1<<50 {
		return bad
	}
	seen := map[identity]bool{}
	for _, v := range c.Services {
		key := identity{v.Namespace, v.Service}
		if len(v.Namespace) > 256 || len(v.Service) > 256 || v.Service == "" || seen[key] || v.Spans < 0 || v.Bytes < 0 || v.Spans > c.Spans || v.Bytes > c.Bytes {
			return bad
		}
		if spans > c.Spans-v.Spans || bytes > c.Bytes-v.Bytes {
			return bad
		}
		if len(v.Cardinality) > 8 || !c.CardinalityMeasured && len(v.Cardinality) > 0 {
			return bad
		}
		keys := map[string]bool{}
		for _, count := range v.Cardinality {
			if !attributeKey.MatchString(count.Key) || keys[count.Key] || count.Distinct < 1 || count.Distinct > maxDistinct {
				return bad
			}
			keys[count.Key] = true
		}
		seen[key] = true
		spans += v.Spans
		bytes += v.Bytes
	}
	if spans != c.Spans || bytes != c.Bytes {
		return bad
	}
	return nil
}

func (t *Tracker) Checkpoints(now time.Time) []Checkpoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advance(now)
	result := []Checkpoint{}
	for _, w := range []window{t.previous, t.current} {
		if w.start.IsZero() {
			continue
		}
		since := w.start
		if t.started.After(since) {
			since = t.started
		}
		c := Checkpoint{CardinalityMeasured: true, Session: t.session, Start: w.start, Since: since, Until: minTime(now, w.start.Add(time.Hour)), Spans: w.total.Spans, Bytes: w.total.Bytes, OtherSpans: w.other.Spans, OtherBytes: w.other.Bytes, Services: []Volume{}}
		for key, v := range w.services {
			volume := Volume{Namespace: key.namespace, Service: key.service, Spans: v.Spans, Bytes: v.Bytes}
			for key, set := range v.values {
				volume.Cardinality = append(volume.Cardinality, Cardinality{key, len(set), len(set) == maxDistinct})
			}
			slices.SortFunc(volume.Cardinality, func(a, b Cardinality) int { return strings.Compare(a.Key, b.Key) })
			c.Services = append(c.Services, volume)
		}
		slices.SortFunc(c.Services, func(a, b Volume) int {
			if a.Namespace < b.Namespace {
				return -1
			}
			if a.Namespace > b.Namespace {
				return 1
			}
			if a.Service < b.Service {
				return -1
			}
			if a.Service > b.Service {
				return 1
			}
			return 0
		})
		result = append(result, c)
	}
	return result
}

func (t *Tracker) CheckpointResult(at time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.checkpointFailed = err != nil
	if err == nil {
		t.checkpointAt = at.UTC()
	}
}

// HistoryReport streams persisted sessions, excluding our own cumulative
// checkpoints, then overlays live counters exactly once. Restarts never add
// old totals to Record, and checkpoint retries cannot double count them.
func (t *Tracker) HistoryReport(ctx context.Context, reader HistoryReader, now, hour time.Time) (Report, error) {
	return t.historyReport(ctx, reader, now, hour, t.policy())
}
func (t *Tracker) historyReport(ctx context.Context, reader HistoryReader, now, hour time.Time, policy Policy) (Report, error) {
	live := t.Checkpoints(now)
	w := window{services: make(map[identity]*usage)}
	intervals := [][2]time.Time{}
	counts := map[identity]map[string]int{}
	measured, missing := false, false
	var cardinalitySince time.Time
	merge := func(c Checkpoint) error {
		if err := c.Validate(); err != nil {
			return err
		}
		if !c.Start.Equal(hour) {
			return errors.New("budget checkpoint hour mismatch")
		}
		if c.CardinalityMeasured {
			measured = true
			if cardinalitySince.IsZero() || c.Since.Before(cardinalitySince) {
				cardinalitySince = c.Since
			}
		} else {
			missing = true
		}
		w.start = hour
		w.total.Spans += c.Spans
		w.total.Bytes += c.Bytes
		w.other.Spans += c.OtherSpans
		w.other.Bytes += c.OtherBytes
		intervals = append(intervals, [2]time.Time{c.Since, c.Until})
		for _, v := range c.Services {
			key := identity{v.Namespace, v.Service}
			_, configured := policy.services[key]
			if u := w.service(key, configured); u != nil {
				if counts[key] == nil {
					counts[key] = map[string]int{}
				}
				for _, count := range v.Cardinality {
					if _, present := counts[key][count.Key]; !present && len(counts[key]) >= 8 {
						missing = true
						continue
					}
					counts[key][count.Key] = max(counts[key][count.Key], count.Distinct)
				}
				u.Spans += v.Spans
				u.Bytes += v.Bytes
			} else {
				w.other.Spans += v.Spans
				w.other.Bytes += v.Bytes
			}
		}
		return nil
	}
	discarded, err := reader.ReadBudgetHistory(ctx, hour, t.session, merge)
	if err != nil {
		return Report{}, err
	}
	for _, c := range live {
		if c.Start.Equal(hour) {
			if err := merge(c); err != nil {
				return Report{}, err
			}
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	report := t.report(w, now, policy)
	report.HistoryDays = policy.historyDays
	report.CheckpointedAt = t.checkpointAt
	report.CheckpointFailed = t.checkpointFailed
	report.Start, report.End = hour, hour.Add(time.Hour)
	report.DiscardedCheckpoints = discarded
	report.CardinalitySince = time.Time{}
	if len(intervals) == 0 {
		return report, nil
	}
	slices.SortFunc(intervals, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })
	since, until := intervals[0][0], intervals[0][1]
	gap := since.After(hour)
	for _, period := range intervals[1:] {
		if !period[0].Equal(until) {
			gap = true
		}
		if period[1].After(until) {
			until = period[1]
		}
	}
	report.ObservedSince, report.ObservedUntil = since, until
	report.Partial = gap || until.Before(report.End)
	report.CardinalityCoverage = "unavailable"
	if measured {
		report.CardinalitySince = cardinalitySince
		report.CardinalityCoverage = "historical_session_max_lower_bound"
		hasHighCardinality := false
		if missing {
			report.CardinalityCoverage = "partial_historical_session_max_lower_bound"
		}
		for i := range report.Services {
			service := &report.Services[i]
			for key, count := range counts[identity{service.Namespace, service.Name}] {
				if count > policy.threshold() {
					service.HighCardinality = append(service.HighCardinality, Cardinality{key, count, true})
					hasHighCardinality = true
				}
			}
			slices.SortFunc(service.HighCardinality, func(a, b Cardinality) int { return strings.Compare(a.Key, b.Key) })
		}
		if hasHighCardinality {
			report.Advice = append(report.Advice, "Use bounded categories or route templates for high-cardinality attributes. Historical counts are lower bounds across sessions.")
		}
	}

	return report, nil
}

func (t *Tracker) ReportAt(ctx context.Context, reader HistoryReader, now, hour time.Time) (Report, error) {
	policy := t.policy()
	current := now.UTC().Truncate(time.Hour)
	if policy.historyDays == 0 {
		if hour.Equal(current) {
			return t.snapshotWithPolicy(now, false, policy), nil
		}
		if hour.Equal(current.Add(-time.Hour)) {
			return t.snapshotWithPolicy(now, true, policy), nil
		}
		return Report{Start: hour, End: hour.Add(time.Hour), Services: []Service{}, Advice: []string{}}, nil
	}
	if hour.Before(current.Add(-time.Duration(policy.historyDays)*24*time.Hour)) || hour.After(current) {
		return Report{Start: hour, End: hour.Add(time.Hour), HistoryDays: policy.historyDays, Services: []Service{}, Advice: []string{}}, nil
	}
	report, err := t.historyReport(ctx, reader, now, hour, policy)
	if err != nil || !report.Available || report.Partial {
		return report, err
	}
	if hour.Add(-time.Hour).Before(current.Add(-time.Duration(policy.historyDays) * 24 * time.Hour)) {
		return report, nil
	}
	previous, err := t.historyReport(ctx, reader, now, hour.Add(-time.Hour), policy)
	if err != nil {
		return Report{}, err
	}
	if !previous.Available || previous.Partial {
		return report, nil
	}
	spans, bytes := report.Total.Spans-previous.Total.Spans, report.Total.Bytes-previous.Total.Bytes
	report.SpanDelta, report.ByteDelta = &spans, &bytes
	if report.Other.Spans == 0 && previous.Other.Spans == 0 {
		present := map[identity]bool{}
		for i, v := range report.Services {
			present[identity{v.Namespace, v.Name}] = true
			var beforeSpans, beforeBytes int64
			for _, old := range previous.Services {
				if v.Namespace == old.Namespace && v.Name == old.Name {
					beforeSpans, beforeBytes = old.Spans, old.Bytes
					break
				}
			}
			spans, bytes := v.Spans-beforeSpans, v.Bytes-beforeBytes
			report.Services[i].SpanDelta, report.Services[i].ByteDelta = &spans, &bytes
		}
		for _, old := range previous.Services {
			if present[identity{old.Namespace, old.Name}] {
				continue
			}
			if len(report.Services) >= MaxHistoryServices {
				report.ChangesTruncated = true
				continue
			}
			spans, bytes := -old.Spans, -old.Bytes
			report.Services = append(report.Services, Service{Namespace: old.Namespace, Name: old.Name, Limits: old.Limits, SpanDelta: &spans, ByteDelta: &bytes})
		}
	}
	return report, nil
}
