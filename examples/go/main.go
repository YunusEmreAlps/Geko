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
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return fmt.Errorf("create trace exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "geko-go-checkout"))),
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
	log.Print("Go example listening at http://127.0.0.1:8080 — /success, /failure, /retry")
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

func newHandler(tracer trace.Tracer) http.Handler {
	mux := http.NewServeMux()
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
