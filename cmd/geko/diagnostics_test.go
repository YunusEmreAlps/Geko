package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorDoctorCommandReportsErrorsAsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.yaml")
	if err := os.WriteFile(path, []byte("service: {pipelines: {traces: {receivers: [missing], exporters: [absent]}}}"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := run(t.Context(), []string{"doctor", "--collector-config", path, "--json"}, &output)
	if err == nil || !strings.Contains(output.String(), `"code":"missing_component"`) ||
		!strings.Contains(output.String(), `"config_checked":true`) {
		t.Fatalf("report = %s, %v", &output, err)
	}
}

func TestBudgetCommandReadsRemoteDashboard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/v1/budget" || req.URL.Query().Get("previous") != "true" ||
			req.Header.Get("Authorization") != "Bearer read-token" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"available":true,"total":{"spans":7,"bytes":120},"services":[]}`)
	}))
	defer server.Close()
	t.Setenv("GEKO_TOKEN", "read-token")
	var output bytes.Buffer
	err := run(t.Context(), []string{"budget", "--server", server.URL, "--previous", "--json"}, &output)
	if err != nil || !strings.Contains(output.String(), `"total":{"spans":7,"bytes":120}`) {
		t.Fatalf("report = %s, %v", &output, err)
	}
}
