package collectordoctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		if len(os.Args) != 4 || os.Args[2] != "--config" {
			os.Exit(21)
		}
		data, _ := os.ReadFile(os.Args[3])
		switch string(data) {
		case "invalid":
			fmt.Fprint(os.Stderr, "secret-provider-output")
			os.Exit(2)
		case "timeout":
			time.Sleep(30 * time.Second)
			os.Exit(0)
		default:
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestExplicitCollectorValidationCoverageAndRedaction(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input, want string
		failed            bool
	}{
		{"valid", "receivers: {}\n", "runtime_validated", false},
		{"invalid", "invalid", "runtime_validation_failed", true},
		{"timeout", "timeout", "runtime_validation_failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "collector.yaml")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(map[string]string{"ConfigFile": path, "CollectorBinary": executable})
			var opts Options
			if err := json.Unmarshal(encoded, &opts); err != nil {
				t.Fatal(err)
			}
			began := time.Now()
			report := Inspect(t.Context(), opts)
			if time.Since(began) > 10*time.Second {
				t.Fatal("validation timeout not bounded")
			}
			if report.Coverage.Config != tc.want {
				t.Fatalf("coverage = %+v", report)
			}
			found := false
			for _, f := range report.Findings {
				if f.Code == "runtime_validation_failed" {
					found = true
				}
				if strings.Contains(f.Evidence, "secret") {
					t.Fatal("validator output leaked")
				}
			}
			if found != tc.failed {
				t.Fatalf("validation error status: %+v", report)
			}
		})
	}
}

// Optional distribution check; ordinary unit tests use the local helper above.
func TestOfficialCollectorConfigValidation(t *testing.T) {
	binary := os.Getenv("GEKO_TEST_COLLECTOR_BINARY")
	if binary == "" {
		t.Skip("set GEKO_TEST_COLLECTOR_BINARY to an explicitly trusted local Collector")
	}
	valid := "receivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: 127.0.0.1:0\nexporters:\n  debug:\n    verbosity: basic\nservice:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [debug]\n"
	for _, invalid := range []bool{false, true} {
		input := valid
		if invalid {
			input = strings.Replace(valid, "verbosity: basic", "unknown_doctor_test_option: true", 1)
		}
		path := filepath.Join(t.TempDir(), "collector.yaml")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		report := Inspect(t.Context(), Options{Sources: Sources{ConfigFile: path}, CollectorBinary: binary})
		want := "runtime_validated"
		if invalid {
			want = "runtime_validation_failed"
		}
		if report.Coverage.Config != want || report.HasErrors() != invalid {
			t.Fatalf("invalid=%v: %+v", invalid, report)
		}
	}
}
