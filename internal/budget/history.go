package budget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"time"
)

const MaxHistoryServices = 256
const CheckpointInterval = time.Minute

type Volume struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Spans     int64  `json:"spans"`
	Bytes     int64  `json:"bytes"`
}

// Checkpoint stores cumulative volume for one session/hour. No attribute
// values, fingerprints, cardinality sets or credentials cross this boundary.
type Checkpoint struct {
	Session    string    `json:"session"`
	Start      time.Time `json:"start"`
	Since      time.Time `json:"since"`
	Until      time.Time `json:"until"`
	Spans      int64     `json:"spans"`
	Bytes      int64     `json:"bytes"`
	OtherSpans int64     `json:"other_spans"`
	OtherBytes int64     `json:"other_bytes"`
	Services   []Volume  `json:"services"`
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
		c := Checkpoint{Session: t.session, Start: w.start, Since: since, Until: minTime(now, w.start.Add(time.Hour)), Spans: w.total.Spans, Bytes: w.total.Bytes, OtherSpans: w.other.Spans, OtherBytes: w.other.Bytes, Services: []Volume{}}
		for key, v := range w.services {
			c.Services = append(c.Services, Volume{key.namespace, key.service, v.Spans, v.Bytes})
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
	merge := func(c Checkpoint) error {
		if err := c.Validate(); err != nil {
			return err
		}
		if !c.Start.Equal(hour) {
			return errors.New("budget checkpoint hour mismatch")
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
	// Fingerprints stay process-local. A historical report can show current
	// session cardinality only, with an independent observation start.
	for _, active := range []window{t.current, t.previous} {
		if !active.start.Equal(hour) {
			continue
		}
		current := t.report(active, now, policy)
		report.CardinalitySince = current.ObservedSince
		for i := range report.Services {
			for _, v := range current.Services {
				if report.Services[i].Namespace == v.Namespace && report.Services[i].Name == v.Name {
					report.Services[i].HighCardinality = v.HighCardinality
				}
			}
		}
		for _, advice := range current.Advice {
			if !slices.Contains(report.Advice, advice) {
				report.Advice = append(report.Advice, advice)
			}
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
