// Package collectordoctor inspects Collector configuration and internal metrics.
package collectordoctor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Finding struct {
	Severity       string `json:"severity"`
	Code           string `json:"code"`
	Subject        string `json:"subject"`
	Reason         string `json:"reason"`
	Evidence       string `json:"evidence"`
	Recommendation string `json:"recommendation"`
}

var identifier = regexp.MustCompile(`^[[:alnum:]_.-]{1,64}(/[[:alnum:]_.-]{1,64})?$`)

func label(value string) string {
	if identifier.MatchString(value) {
		return value
	}
	return "(unavailable identifier)"
}

type collectorConfig struct {
	Receivers  map[string]yaml.Node
	Processors map[string]yaml.Node
	Exporters  map[string]yaml.Node
	Connectors map[string]yaml.Node
	Service    struct {
		Pipelines map[string]struct {
			Receivers  []string
			Processors []string
			Exporters  []string
		}
	}
}

func configFindings(data []byte) ([]Finding, error) {
	var cfg collectorConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&cfg); err != nil {
		return nil, errors.New("expected a resolved Collector YAML mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected one Collector YAML document")
	}
	var findings []Finding
	var names []string
	for name := range cfg.Service.Pipelines {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		findings = append(findings, Finding{"error", "no_pipelines", "service.pipelines",
			"no telemetry pipeline is enabled", "no pipeline definitions found",
			"configure at least one pipeline with receivers and exporters"})
	}
	incoming, outgoing := make(map[string]bool), make(map[string]bool)
	used := map[string]map[string]bool{
		"receivers": {}, "processors": {}, "exporters": {},
	}
	for _, name := range names {
		pipeline := cfg.Service.Pipelines[name]
		signal, _, _ := strings.Cut(name, "/")
		if !slices.Contains([]string{"traces", "metrics", "logs", "profiles"}, signal) {
			findings = append(findings, Finding{"error", "pipeline_signal", label(name),
				"the pipeline signal is unrecognized", "pipeline ID prefix is outside the supported signal names",
				"use a traces, metrics, logs or profiles pipeline supported by your Collector version"})
		}
		for _, stage := range []struct {
			name       string
			refs       []string
			defined    map[string]yaml.Node
			connectors map[string]bool
		}{
			{"receivers", pipeline.Receivers, cfg.Receivers, outgoing},
			{"processors", pipeline.Processors, cfg.Processors, nil},
			{"exporters", pipeline.Exporters, cfg.Exporters, incoming},
		} {
			subject := label(name) + "." + stage.name
			if len(stage.refs) == 0 && stage.connectors != nil {
				findings = append(findings, Finding{"error", "empty_stage", subject,
					"the pipeline has no component on this side", "empty component list",
					"add a configured component or connector"})
			}
			seen := make(map[string]bool, len(stage.refs))
			for index, ref := range stage.refs {
				if seen[ref] {
					findings = append(findings, Finding{"error", "duplicate_reference", subject,
						"a component is repeated in this stage", label(ref), "list the component once"})
				}
				seen[ref] = true
				_, defined := stage.defined[ref]
				if defined {
					used[stage.name][ref] = true
				}
				_, connector := cfg.Connectors[ref]
				if !defined && connector && stage.connectors != nil {
					stage.connectors[ref] = true
				} else if !defined {
					findings = append(findings, Finding{"error", "missing_component", subject,
						"the referenced component is not configured", label(ref),
						"define it in the matching top-level section or correct its ID"})
				}
				if stage.name == "processors" && defined && index > 0 && (ref == "memory_limiter" || strings.HasPrefix(ref, "memory_limiter/")) {
					findings = append(findings, Finding{"warning", "memory_limiter_order", subject,
						"earlier processors can allocate before the memory limiter runs", fmt.Sprintf("%s is processor %d", label(ref), index+1),
						"consider putting the memory_limiter first"})
				}
			}
		}
	}
	for _, section := range []struct {
		name       string
		components map[string]yaml.Node
	}{
		{"receivers", cfg.Receivers}, {"processors", cfg.Processors}, {"exporters", cfg.Exporters},
	} {
		var ids []string
		for id := range section.components {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if !used[section.name][id] {
				findings = append(findings, Finding{"info", "unused_component", label(id),
					"the configured component is inactive", "no pipeline references this " + section.name + " entry",
					"attach it to a pipeline if it should handle telemetry"})
				continue
			}
			if section.name == "exporters" {
				var options struct {
					Queue struct{ Enabled *bool } `yaml:"sending_queue"`
					Retry struct{ Enabled *bool } `yaml:"retry_on_failure"`
				}
				node := section.components[id]
				if node.Decode(&options) != nil {
					continue
				}
				if options.Queue.Enabled != nil && !*options.Queue.Enabled {
					findings = append(findings, Finding{"warning", "queue_disabled", label(id),
						"the exporter has no buffer for downstream backpressure", "sending_queue.enabled is false",
						"consider enabling a bounded sending queue if the exporter supports it"})
				}
				if options.Retry.Enabled != nil && !*options.Retry.Enabled {
					findings = append(findings, Finding{"warning", "retry_disabled", label(id),
						"the exporter does not retry transient errors", "retry_on_failure.enabled is false",
						"consider bounded retries for retryable downstream failures"})
				}
			}
		}
	}
	var connectors []string
	for name := range cfg.Connectors {
		connectors = append(connectors, name)
	}
	slices.Sort(connectors)
	for _, name := range connectors {
		if incoming[name] != outgoing[name] {
			findings = append(findings, Finding{"error", "unconnected_connector", label(name),
				"a connector is attached on only one side", "it appears as either a receiver or an exporter",
				"connect an upstream pipeline's exporters to a downstream pipeline's receivers"})
		}
	}
	return findings, nil
}
