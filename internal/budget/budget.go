// Package budget measures ingested trace volume without retaining attribute values.
package budget

import (
	"cmp"
	"errors"
	"hash/maphash"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"

	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	maxServices = 128
	maxDistinct = 256
)

type Limits struct {
	Spans int64 `yaml:"spans_per_hour" json:"spans_per_hour"`
	Bytes int64 `yaml:"bytes_per_hour" json:"bytes_per_hour"`
}

type ServiceLimit struct {
	Namespace string `yaml:"namespace"`
	Service   string `yaml:"service"`
	Limits    `yaml:",inline"`
}

type Config struct {
	Enabled          bool           `yaml:"enabled"`
	Defaults         Limits         `yaml:"default"`
	Services         []ServiceLimit `yaml:"services"`
	Attributes       []string       `yaml:"attributes"`
	CardinalityLimit int            `yaml:"cardinality_limit"`
	HistoryDays      int            `yaml:"history_days"`
}

var attributeKey = regexp.MustCompile(`^[[:alnum:]_.-]{1,128}$`)

func (c Config) Validate() error {
	if c.HistoryDays < 0 || c.HistoryDays > 30 || c.HistoryDays > 0 && !c.Enabled {
		return errors.New("telemetry_budget history_days must be 0–30 and requires enabled: true")
	}
	if len(c.Services) > maxServices || len(c.Attributes) > 8 || c.CardinalityLimit < 0 || c.CardinalityLimit >= maxDistinct {
		return errors.New("telemetry_budget supports up to 128 service limits, eight attributes and a cardinality_limit from 0 to 255")
	}
	if c.Defaults.Spans < 0 || c.Defaults.Bytes < 0 {
		return errors.New("telemetry_budget default limits must be nonnegative")
	}
	keys := make(map[string]bool)
	for _, key := range c.Attributes {
		if !attributeKey.MatchString(key) || keys[key] {
			return errors.New("telemetry_budget attributes must be distinct attribute names of up to 128 characters")
		}
		keys[key] = true
	}
	seen := make(map[identity]bool)
	for _, service := range c.Services {
		key := identity{service.Namespace, service.Service}
		if key.service == "" || len(key.service) > 256 || len(key.namespace) > 256 || seen[key] {
			return errors.New("telemetry_budget service limits require distinct namespace and service pairs")
		}
		if service.Spans < 0 || service.Bytes < 0 {
			return errors.New("telemetry_budget service limits must be nonnegative")
		}
		seen[key] = true
	}
	return nil
}

type identity struct{ namespace, service string }
type totals struct {
	Spans int64 `json:"spans"`
	Bytes int64 `json:"bytes"`
}
type usage struct {
	totals
	values map[string]map[uint64]struct{}
}
type window struct {
	start        time.Time
	total, other totals
	services     map[identity]*usage
}

type Tracker struct {
	cfg               Config
	seed              maphash.Seed
	limits            map[identity]Limits
	keys              map[string]bool
	started           time.Time
	mu                sync.Mutex
	current, previous window
	session           string
	checkpointAt      time.Time
	checkpointFailed  bool
}

func New(cfg Config, now time.Time) *Tracker {
	if !cfg.Enabled {
		return nil
	}
	if cfg.CardinalityLimit == 0 {
		cfg.CardinalityLimit = 100
	}
	if cfg.Attributes == nil {
		cfg.Attributes = []string{"http.route", "url.path", "db.query.summary", "service.instance.id"}
	}
	t := &Tracker{cfg: cfg, seed: maphash.MakeSeed(), started: now.UTC(), limits: make(map[identity]Limits), keys: make(map[string]bool)}
	t.session = newSessionID()
	for _, rule := range cfg.Services {
		t.limits[identity{rule.Namespace, rule.Service}] = rule.Limits
	}
	for _, key := range cfg.Attributes {
		t.keys[key] = true
	}
	t.advance(now)
	return t
}

// Observation carries the raw fields of a normalized span until the write
// succeeds. Attribute fingerprints and counters are the only retained data.
type Observation struct {
	Span     *trace.Span
	Resource *resource.Resource
}

type fingerprint struct {
	key  string
	hash uint64
}

func (t *Tracker) fingerprints(input []*common.KeyValue, add func(fingerprint)) {
	for _, attribute := range input {
		key := attribute.GetKey()
		if !t.keys[key] {
			continue
		}
		var value string
		var kind byte
		switch typed := attribute.GetValue().GetValue().(type) {
		case *common.AnyValue_StringValue:
			value, kind = typed.StringValue, 's'
		case *common.AnyValue_IntValue:
			value, kind = strconv.FormatInt(typed.IntValue, 10), 'i'
		case *common.AnyValue_BoolValue:
			value, kind = strconv.FormatBool(typed.BoolValue), 'b'
		case *common.AnyValue_DoubleValue:
			value, kind = strconv.FormatFloat(typed.DoubleValue, 'g', -1, 64), 'd'
		default:
			continue
		}
		var hash maphash.Hash
		hash.SetSeed(t.seed)
		hash.WriteByte(kind)
		hash.WriteString(value)
		add(fingerprint{key, hash.Sum64()})
	}
}

func (u *usage) add(f fingerprint) {
	if u.values == nil {
		u.values = make(map[string]map[uint64]struct{})
	}
	values := u.values[f.key]
	if values == nil {
		values = make(map[uint64]struct{})
		u.values[f.key] = values
	}
	if len(values) < maxDistinct {
		values[f.hash] = struct{}{}
	}
}

func (w *window) service(key identity, configured bool) *usage {
	if existing := w.services[key]; existing != nil {
		return existing
	}
	// Configured services always retain a dedicated row even during a burst
	// of new service names. Other services share a separate tracking allowance.
	if !configured && len(w.services) >= maxServices {
		return nil
	}
	created := &usage{}
	w.services[key] = created
	return created
}

func (t *Tracker) Record(at time.Time, spans []telemetry.Span, observations []Observation) {
	batch := window{services: make(map[identity]*usage)}
	resources := make(map[*resource.Resource][]fingerprint)
	for i, span := range spans {
		size := int64(proto.Size(observations[i].Span))
		batch.total.Spans++
		batch.total.Bytes += size
		namespace, _ := span.ResourceAttributes["service.namespace"].(string)
		key := identity{namespace, span.ServiceName}
		_, configured := t.limits[key]
		entry := batch.service(key, configured)
		if entry == nil {
			batch.other.Spans++
			batch.other.Bytes += size
			continue
		}
		entry.Spans++
		entry.Bytes += size
		if len(t.keys) == 0 {
			continue
		}
		resource := observations[i].Resource
		values, cached := resources[resource]
		if !cached {
			t.fingerprints(resource.GetAttributes(), func(value fingerprint) { values = append(values, value) })
			resources[resource] = values
		}
		for _, value := range values {
			entry.add(value)
		}
		t.fingerprints(observations[i].Span.GetAttributes(), entry.add)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advance(at)
	target := &t.current
	if at.Before(target.start) {
		target = &t.previous
		if target.start.IsZero() || at.Before(target.start) {
			return
		}
	}
	target.total.Spans += batch.total.Spans
	target.total.Bytes += batch.total.Bytes
	target.other.Spans += batch.other.Spans
	target.other.Bytes += batch.other.Bytes
	for key, entry := range batch.services {
		_, configured := t.limits[key]
		destination := target.service(key, configured)
		if destination == nil {
			target.other.Spans += entry.Spans
			target.other.Bytes += entry.Bytes
			continue
		}
		destination.Spans += entry.Spans
		destination.Bytes += entry.Bytes
		for key, values := range entry.values {
			for hash := range values {
				destination.add(fingerprint{key, hash})
			}
		}
	}
}

func (t *Tracker) advance(now time.Time) {
	start := now.UTC().Truncate(time.Hour)
	if !start.After(t.current.start) {
		return
	}
	if t.current.start.Add(time.Hour).Equal(start) {
		t.previous = t.current
	} else if previous := start.Add(-time.Hour); !previous.Before(t.started) {
		t.previous = window{start: previous, services: make(map[identity]*usage)}
	} else {
		t.previous = window{}
	}
	t.current = window{start: start, services: make(map[identity]*usage)}
}

type Cardinality struct {
	Key      string `json:"attribute"`
	Distinct int    `json:"distinct_values"`
	AtLeast  bool   `json:"at_least"`
}

type Service struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"service"`
	totals
	Limits          Limits        `json:"limits"`
	SpanOverage     int64         `json:"span_overage"`
	ByteOverage     int64         `json:"byte_overage"`
	HighCardinality []Cardinality `json:"high_cardinality,omitempty"`
	SpanDelta       *int64        `json:"span_delta,omitempty"`
	ByteDelta       *int64        `json:"byte_delta,omitempty"`
}

type Report struct {
	Start                time.Time `json:"window_start"`
	End                  time.Time `json:"window_end"`
	ObservedSince        time.Time `json:"observed_since"`
	Available            bool      `json:"available"`
	Total                totals    `json:"total"`
	Other                totals    `json:"unattributed_services"`
	Services             []Service `json:"services"`
	CardinalityLimit     int       `json:"cardinality_limit"`
	Advice               []string  `json:"recommendations"`
	ObservedUntil        time.Time `json:"observed_until"`
	Partial              bool      `json:"partial"`
	CardinalitySince     time.Time `json:"cardinality_since"`
	HistoryDays          int       `json:"history_days"`
	CheckpointedAt       time.Time `json:"checkpointed_at"`
	CheckpointFailed     bool      `json:"checkpoint_failed"`
	DiscardedCheckpoints int64     `json:"discarded_checkpoints"`
	ChangesTruncated     bool      `json:"changes_truncated"`
	SpanDelta            *int64    `json:"span_delta,omitempty"`
	ByteDelta            *int64    `json:"byte_delta,omitempty"`
}

func (t *Tracker) Snapshot(now time.Time, previous bool) Report {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advance(now)
	active := t.current
	if previous {
		active = t.previous
	}
	return t.report(active, now)
}

// report is called with the tracker locked; the history path supplies a
// separate aggregated window and replaces observation/cardinality metadata.
func (t *Tracker) report(active window, now time.Time) Report {
	report := Report{Available: !active.start.IsZero(), Start: active.start, Total: active.total,
		Other: active.other, CardinalityLimit: t.cfg.CardinalityLimit, Services: []Service{}, Advice: []string{}}
	if !report.Available {
		return report
	}
	report.End = active.start.Add(time.Hour)
	report.ObservedSince = active.start
	if t.started.After(report.ObservedSince) {
		report.ObservedSince = t.started
	}
	report.ObservedUntil = minTime(now, report.End)
	report.Partial = report.ObservedSince.After(report.Start) || report.ObservedUntil.Before(report.End)
	report.CardinalitySince = report.ObservedSince
	report.HistoryDays = t.cfg.HistoryDays
	report.CheckpointedAt = t.checkpointAt
	report.CheckpointFailed = t.checkpointFailed
	var volume, cardinality bool
	for key, entry := range active.services {
		limits, configured := t.limits[key]
		if !configured {
			limits = t.cfg.Defaults
		}
		service := Service{Namespace: key.namespace, Name: key.service, totals: entry.totals, Limits: limits}
		if limits.Spans > 0 && entry.Spans > limits.Spans {
			service.SpanOverage = entry.Spans - limits.Spans
		}
		if limits.Bytes > 0 && entry.Bytes > limits.Bytes {
			service.ByteOverage = entry.Bytes - limits.Bytes
		}
		volume = volume || service.SpanOverage > 0 || service.ByteOverage > 0
		for key, set := range entry.values {
			if len(set) > t.cfg.CardinalityLimit {
				service.HighCardinality = append(service.HighCardinality, Cardinality{key, len(set), len(set) == maxDistinct})
			}
		}
		slices.SortFunc(service.HighCardinality, func(a, b Cardinality) int {
			return cmp.Compare(a.Key, b.Key)
		})
		cardinality = cardinality || len(service.HighCardinality) > 0
		report.Services = append(report.Services, service)
	}
	slices.SortFunc(report.Services, func(a, b Service) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	if volume {
		report.Advice = append(report.Advice, "Check repetitive requests and verbose spans in services over budget. Filter health checks or adjust sampling while retaining error traces.")
	}
	if cardinality {
		report.Advice = append(report.Advice, "Use bounded categories or route templates for high-cardinality attributes. Remove identifiers from telemetry dimensions when they are not needed.")
	}
	if report.Other.Spans > 0 {
		report.Advice = append(report.Advice, "Some services share the overflow row. Check for dynamic service names or configure limits for services that need individual rows.")
	}
	return report
}
