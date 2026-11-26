// Command go-example serves deterministic checkout scenarios for Geko.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (result error) {
	options := exampleExporterOptions()
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return fmt.Errorf("create trace exporter: %w", err)
	}
	res, err := exampleResource(ctx)
	if err != nil {
		return fmt.Errorf("create resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result = errors.Join(result, provider.Shutdown(shutdown))
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		return fmt.Errorf("listen on example port: %w", err)
	}
	server := &http.Server{Handler: newHandler(provider.Tracer("github.com/YunusEmreAlps/geko/examples/go")), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Printf("Go example listening at http://%s/", listener.Addr())
	if len(options) != 0 {
		log.Print("Trace exporter defaults to http://127.0.0.1:4318/v1/traces; set OTEL_EXPORTER_OTLP_TRACES_ENDPOINT for another receiver.")
	} else {
		log.Print("Trace exporter uses your explicit OTEL endpoint/TLS settings.")
	}
	log.Print("Open the page and select a checkout scenario to generate a trace. Export can take about five seconds.")
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return errors.Join(err, server.Close())
		}
		return nil
	}
}

// The generic SDK default is HTTPS, while a default local Geko serves HTTP.
// Apply an example-only loopback default; never override explicit transport
// configuration, including TLS settings with the SDK's default endpoint.
func exampleExporterOptions() []otlptracehttp.Option {
	for _, variable := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_INSECURE", "OTEL_EXPORTER_OTLP_TRACES_INSECURE",
		"OTEL_EXPORTER_OTLP_CERTIFICATE", "OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", "OTEL_EXPORTER_OTLP_CLIENT_KEY",
		"OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE", "OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY",
	} {
		if os.Getenv(variable) != "" {
			return nil
		}
	}
	return []otlptracehttp.Option{otlptracehttp.WithEndpointURL("http://127.0.0.1:4318/v1/traces")}
}

func exampleResource(ctx context.Context) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", "geko-go-checkout")),
		resource.WithFromEnv())
}

func newHandler(tracer trace.Tracer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, checkoutPage)
	})
	for _, scenario := range []string{"success", "failure", "retry"} {
		mux.HandleFunc("GET /"+scenario, func(w http.ResponseWriter, r *http.Request) {
			ctx, request := tracer.Start(r.Context(), "GET /"+scenario,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String("http.request.method", "GET"), attribute.String("http.route", "/"+scenario)))
			defer request.End()
			// Each request deliberately starts a new trace so it has an observable entry span.
			status := http.StatusOK
			if scenario != "success" {
				_, attempt := tracer.Start(ctx, "payment.authorize")
				attempt.RecordError(errors.New("demo payment gateway unavailable"), trace.WithStackTrace(true))
				attempt.SetStatus(codes.Error, "payment authorization failed")
				attempt.End()
				if scenario == "failure" {
					status = http.StatusInternalServerError
					request.SetStatus(codes.Error, "checkout failed")
				} else {
					_, retry := tracer.Start(ctx, "payment.authorize retry")
					retry.End()
				}
			}
			request.SetAttributes(attribute.Int("http.response.status_code", status))
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Trace-ID", request.SpanContext().TraceID().String())
			w.WriteHeader(status)
			fmt.Fprintf(w, "%s: HTTP %d\ntrace_id=%s\n", scenario, status, request.SpanContext().TraceID())
		})
	}
	return mux
}

const checkoutPage = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Go checkout example</title>
<main>
  <h1>Go checkout example</h1>
  <p>Choose a scenario to generate a trace.</p>
  <ul>
    <li><a href="/success">Successful checkout (HTTP 200)</a></li>
    <li><a href="/failure">Failed checkout (HTTP 500)</a></li>
    <li><a href="/retry">Successful retry (HTTP 200)</a></li>
  </ul>
  <p>Copy the trace ID from the response. After about five seconds, paste it into <strong>Go to trace ID</strong> in Geko.</p>
</main>
</html>
`
