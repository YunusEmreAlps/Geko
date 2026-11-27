package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorDoctorTextExplainsValidationCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.yaml")
	if err := os.WriteFile(path, []byte("receivers:\n  otlp: {}\nexporters:\n  debug: {}\nservice:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [debug]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := collectorDoctorCommand(t.Context(), []string{"--collector-config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "static_wiring") || !strings.Contains(out.String(), "Excluded:") || !strings.Contains(out.String(), "end-to-end") {
		t.Fatalf("validation limitations absent: %s", out.String())
	}
}
