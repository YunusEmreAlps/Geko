package collectordoctor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var metricLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{((?:[^"{}]|"(?:\\.|[^"\\])*")*)\})?\s+(\S+)(?:\s|$)`)
var metricLabel = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*("(?:\\.|[^"\\])*")`)

type metricRule struct {
	code, reason, recommendation string
}

var counterRules = map[string]metricRule{
	"receiver_refused":        {"receiver_refused", "the receiver could not pass items to the pipeline", "check downstream backpressure and exporter retry behavior"},
	"processor_refused":       {"processor_refused", "a processor refused telemetry", "check the processor limits and verify that clients can retry"},
	"exporter_enqueue_failed": {"enqueue_failed", "items could not enter the sending queue", "check queue saturation, downstream throughput and persistent queue storage"},
	"exporter_send_failed":    {"send_failed", "export attempts failed", "check the exporter logs, destination availability, credentials and retry settings"},
}

type sample struct {
	name, subject, identity string
	value                   float64
	rule                    metricRule
}

func readMetrics(data []byte) (map[string]sample, error) {
	rows := make(map[string]sample)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := metricLine.FindStringSubmatch(scanner.Text())
		if line == nil {
			continue
		}
		name := strings.TrimSuffix(line[1], "_total")
		short, internal := strings.CutPrefix(name, "otelcol_")
		if !internal {
			continue
		}
		var rule metricRule
		for _, suffix := range []string{"_spans", "_metric_points", "_log_records", "_profiles"} {
			if base, ok := strings.CutSuffix(short, suffix); ok {
				rule = counterRules[base]
				break
			}
		}
		if rule.code == "" && short != "exporter_queue_size" && short != "exporter_queue_capacity" {
			continue
		}
		value, err := strconv.ParseFloat(line[3], 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		if len(rows) >= 8192 {
			return nil, errors.New("too many Collector diagnostic metrics")
		}
		subject := "Collector component"
		var labels []string
		for _, pair := range metricLabel.FindAllStringSubmatch(line[2], -1) {
			labels = append(labels, pair[1]+"="+pair[2])
			switch pair[1] {
			case "exporter", "receiver", "processor":
				if strings.HasPrefix(short, pair[1]+"_") {
					subject = label(strings.Trim(pair[2], `"`))
				}
			}
		}
		slices.Sort(labels)
		identity := strings.Join(labels, "\x00")
		rows[name+"\x00"+identity] = sample{name, subject, identity, value, rule}
	}
	if scanner.Err() != nil {
		return nil, errors.New("oversized line in Collector metrics")
	}
	return rows, nil
}

func metricFindings(current, baseline []byte) ([]Finding, error) {
	samples, err := readMetrics(current)
	if err != nil {
		return nil, err
	}
	var before map[string]sample
	if baseline != nil {
		before, err = readMetrics(baseline)
		if err != nil {
			return nil, err
		}
	}
	var keys []string
	for key := range samples {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out []Finding
	var skipped int
	for _, key := range keys {
		row := samples[key]
		switch {
		case row.name == "otelcol_exporter_queue_size":
			capacity := samples["otelcol_exporter_queue_capacity\x00"+row.identity].value
			if capacity > 0 && row.value/capacity >= .8 {
				out = append(out, Finding{"warning", "queue_pressure", row.subject,
					"the exporter queue is close to capacity", fmt.Sprintf("queue size %g / %g (%.0f%%)", row.value, capacity, row.value/capacity*100),
					"check exporter throughput and destination health before increasing queue capacity"})
			}
		case row.rule.code != "":
			scope := "cumulative sample"
			value := row.value
			if before != nil {
				previous, ok := before[key]
				if !ok || previous.value > value {
					skipped++
					continue
				}
				value -= previous.value
				scope = "increase between samples"
			}
			if value > 0 {
				severity := "warning"
				if row.rule.code == "enqueue_failed" && before != nil {
					severity = "error"
				}
				out = append(out, Finding{severity, row.rule.code, row.subject, row.rule.reason,
					fmt.Sprintf("%s = %g (%s)", row.name, value, scope), row.rule.recommendation})
			}
		}
	}
	if len(samples) == 0 {
		out = append(out, Finding{"info", "metrics_missing", "Collector metrics", "diagnostic series are unavailable",
			"no supported failure or queue series were found", "verify the internal Prometheus endpoint and metrics level"})
	}
	if skipped > 0 {
		out = append(out, Finding{"info", "unmatched_counters", "Collector metrics", "a counter reset or its series changed",
			fmt.Sprintf("%d counters could not be compared", skipped), "sample the same running Collector again"})
	}
	return out, nil
}
