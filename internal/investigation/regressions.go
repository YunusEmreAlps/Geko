package investigation

import "time"

const RegressionMinSamples = 10

type RegressionFilter struct {
	Baseline, Candidate, Service string
	Since                        time.Time
}

type VersionMetrics struct {
	Traces   int    `json:"traces"`
	Failures int    `json:"failures"`
	Timed    int    `json:"timed_traces"`
	MeanNS   *int64 `json:"mean_duration_ns"`
}

type VersionComparison struct {
	Application       string         `json:"application,omitempty"`
	Namespace         string         `json:"namespace,omitempty"`
	Service           string         `json:"service"`
	Environment       string         `json:"environment,omitempty"`
	Operation         string         `json:"operation"`
	Baseline          VersionMetrics `json:"baseline"`
	Candidate         VersionMetrics `json:"candidate"`
	FailureRegression bool           `json:"failure_regression"`
	LatencyRegression bool           `json:"latency_regression"`
}
