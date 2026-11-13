package web

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// snippetName keeps generated shell and YAML snippets safe to paste unquoted
// and OTEL_RESOURCE_ATTRIBUTES free of characters that need percent-encoding.
var snippetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type envVar struct{ Name, Value string }

type connectView struct {
	Application, Service, Protocol, Token string
	Tokens                                []string
	AccessToken, GRPC                     bool
	Error                                 string
	Endpoint                              string
	Bash, PowerShell, Compose             string
	TracesURL                             string
}

func (d *dashboard) connect(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	view := connectView{
		Application: strings.TrimSpace(query.Get("application")), Service: strings.TrimSpace(query.Get("service")),
		Protocol: query.Get("protocol"), Token: query.Get("token"),
		Tokens: d.currentSettings().IngestTokens, AccessToken: d.settings.AccessToken, GRPC: d.settings.GRPCAddr != "" || d.settings.PublicURL != "",
	}
	if view.Service == "" {
		view.Service = "my-service"
	}
	if view.Protocol == "" {
		view.Protocol = "http/protobuf"
	}
	if view.Token == "" && len(view.Tokens) > 0 {
		view.Token = view.Tokens[0]
	}
	switch {
	case !snippetName.MatchString(view.Service) || view.Application != "" && !snippetName.MatchString(view.Application):
		view.Error = "Use up to 64 letters, digits, dots, hyphens or underscores for names."
	case view.Protocol != "http/protobuf" && view.Protocol != "grpc":
		view.Error = "Choose OTLP/HTTP or OTLP/gRPC."
	case view.Protocol == "grpc" && !view.GRPC:
		view.Error = "OTLP/gRPC is disabled. Set grpc_addr, or use OTLP/HTTP."
	case view.Token != "" && !slices.Contains(view.Tokens, view.Token):
		view.Error = "Choose a configured ingest token."
	}
	if view.Error != "" {
		d.render(w, http.StatusBadRequest, "connect", "Connect an application", view)
		return
	}

	view.Endpoint = d.otlpBase(view.Protocol, r.Host)
	vars := []envVar{{"OTEL_SERVICE_NAME", view.Service}}
	if view.Application != "" {
		vars = append(vars, envVar{"OTEL_RESOURCE_ATTRIBUTES", "service.namespace=" + view.Application})
	}
	vars = append(vars, envVar{"OTEL_EXPORTER_OTLP_PROTOCOL", view.Protocol}, envVar{"OTEL_EXPORTER_OTLP_ENDPOINT", view.Endpoint})
	var bash, powershell, compose strings.Builder
	compose.WriteString("environment:\n")
	for _, v := range vars {
		bash.WriteString("export " + v.Name + "=" + v.Value + "\n")
		powershell.WriteString("$env:" + v.Name + " = '" + v.Value + "'\n")
		compose.WriteString("  " + v.Name + ": " + v.Value + "\n")
	}
	if view.AccessToken {
		bash.WriteString(`export OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer%20${GEKO_INGEST_TOKEN}"` + "\n")
		powershell.WriteString(`$env:OTEL_EXPORTER_OTLP_HEADERS = "authorization=Bearer%20$env:GEKO_INGEST_TOKEN"` + "\n")
		compose.WriteString(`  OTEL_EXPORTER_OTLP_HEADERS: "authorization=Bearer%20${GEKO_INGEST_TOKEN}"` + "\n")
	}
	// Geko stores traces only; zero-code agents otherwise export metrics and logs too.
	for _, name := range []string{"OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER"} {
		bash.WriteString("export " + name + "=none\n")
		powershell.WriteString("$env:" + name + " = 'none'\n")
		compose.WriteString("  " + name + ": none\n")
	}
	view.Bash, view.PowerShell, view.Compose = bash.String(), powershell.String(), compose.String()
	view.TracesURL = route("/traces", url.Values{"service": {view.Service}})
	d.render(w, http.StatusOK, "connect", "Connect an application", view)
}

// otlpBase returns the exporter base URL; SDKs append /v1/traces for HTTP.
func (d *dashboard) otlpBase(protocol, requestHost string) string {
	if d.settings.PublicURL != "" {
		return d.settings.PublicURL
	}
	addr := d.settings.ListenAddr
	if protocol == "grpc" {
		addr = d.settings.GRPCAddr
	}
	return strings.TrimSuffix(traceEndpoint(addr, requestHost), "/v1/traces")
}
