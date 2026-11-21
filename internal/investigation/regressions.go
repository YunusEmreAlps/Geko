package investigation

import "time"

const RegressionMinSamples = 10

// RegressionSampleLimit bounds how many of the newest traces of each version
// a comparison reads.
const RegressionSampleLimit = 10000

type RegressionFilter struct {
	Baseline, Candidate, Service string
	Since                        time.Time
}

type VersionMetrics struct {
	Traces   int    `json:"traces"`
	Failures int    `json:"failures"`
	Timed    int    `json:"timed_traces"`
	MeanNS   *int64 `json:"mean_duration_ns"`
	// Sampled reports that the version had more traces in the window than
	// RegressionSampleLimit and these figures cover the newest of them.
	Sampled bool `json:"sampled,omitempty"`
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
