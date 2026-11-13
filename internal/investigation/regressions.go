package investigation

import "time"

const RegressionMinSamples = 10

type RegressionFilter struct {
	Baseline, Candidate, Service string
	Since                        time.Time
}

type VersionMetrics struct {
	Traces, Failures, Timed int
	MeanNS                  *int64
}

type VersionComparison struct {
	Application, Namespace, Service, Environment, Operation string
	Baseline, Candidate                                     VersionMetrics
	FailureRegression, LatencyRegression                    bool
}
