package collectordoctor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const maxInput = 2 << 20

type Sources struct {
	ConfigFile string `yaml:"config_file"`
	MetricsURL string `yaml:"metrics_url"`
}

func (s Sources) Enabled() bool { return s.ConfigFile != "" || s.MetricsURL != "" }

type Options struct {
	Sources
	MetricsFile    string
	BaselineFile   string
	SampleInterval time.Duration
}

type Report struct {
	ConfigChecked  bool      `json:"config_checked"`
	MetricsChecked bool      `json:"metrics_checked"`
	Sampled        bool      `json:"compared_samples"`
	Findings       []Finding `json:"findings"`
}

func (r Report) HasErrors() bool {
	for _, finding := range r.Findings {
		if finding.Severity == "error" {
			return true
		}
	}
	return false
}

func Inspect(ctx context.Context, opts Options) Report {
	report := Report{Findings: []Finding{}}
	if opts.ConfigFile != "" {
		data, err := readFile(opts.ConfigFile)
		if err == nil {
			report.Findings, err = configFindings(data)
			report.ConfigChecked = err == nil
		}
		if err != nil {
			report.Findings = append(report.Findings, Finding{"error", "config_unavailable", "Collector configuration",
				"the configuration could not be inspected", err.Error(), "check the readable file and provide a resolved Collector YAML"})
		}
	}
	if opts.MetricsURL != "" || opts.MetricsFile != "" {
		metrics, baseline, err := snapshots(ctx, opts)
		if err == nil {
			var findings []Finding
			findings, err = metricFindings(metrics, baseline)
			report.Findings = append(report.Findings, findings...)
			report.MetricsChecked, report.Sampled = err == nil, err == nil && baseline != nil
		}
		if err != nil {
			report.Findings = append(report.Findings, Finding{"error", "metrics_unavailable", "Collector metrics",
				"the metrics could not be inspected", err.Error(), "check the endpoint or provide a Prometheus text snapshot"})
		}
	}
	if report.Findings == nil {
		report.Findings = []Finding{}
	}
	return report
}

func snapshots(ctx context.Context, opts Options) (metrics, baseline []byte, err error) {
	if opts.MetricsFile != "" {
		metrics, err = readFile(opts.MetricsFile)
	} else {
		metrics, err = scrape(ctx, opts.MetricsURL)
	}
	if err != nil {
		return nil, nil, err
	}
	if opts.BaselineFile != "" {
		baseline, err = readFile(opts.BaselineFile)
	}
	if opts.SampleInterval > 0 {
		timer := time.NewTimer(opts.SampleInterval)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, nil, errors.New("metrics sampling was interrupted")
		case <-timer.C:
		}
		baseline = metrics
		metrics, err = scrape(ctx, opts.MetricsURL)
	}
	return metrics, baseline, err
}

func readFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("input must be a readable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open the input file")
	}
	defer file.Close()
	return readBounded(file)
}

func ValidateMetricsURL(raw string) error {
	address, err := url.Parse(raw)
	if err != nil || address.Hostname() == "" || address.Scheme != "http" && address.Scheme != "https" ||
		address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return errors.New("use an HTTP or HTTPS metrics URL without credentials, query or fragment")
	}
	return nil
}

var metricsTransport = &http.Transport{DisableKeepAlives: true}
var metricsClient = http.Client{
	Timeout: 2 * time.Second, Transport: metricsTransport,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func scrape(ctx context.Context, raw string) ([]byte, error) {
	if err := ValidateMetricsURL(raw); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("could not create the metrics request")
	}
	request.Header.Set("Accept", "text/plain")
	response, err := metricsClient.Do(request)
	if err != nil {
		return nil, errors.New("metrics endpoint is unreachable or timed out")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("metrics endpoint did not return HTTP 200")
	}
	return readBounded(response.Body)
}

func readBounded(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxInput+1))
	if err != nil {
		return nil, errors.New("could not read diagnostic input")
	}
	if len(data) > maxInput {
		return nil, errors.New("diagnostic input exceeds 2 MiB")
	}
	return data, nil
}
