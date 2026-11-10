// Command service-map exports one synthetic multi-service trace for UI exploration.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (result error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, exporter.Shutdown(ctx)) }()
	start := time.Now().Add(-time.Second)
	var providers []*sdktrace.TracerProvider
	tracerFor := func(service string) trace.Tracer {
		provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "demo-"+service))))
		providers = append(providers, provider)
		return provider.Tracer("geko/synthetic-service-map")
	}
	frontend, checkout, catalog, payments, inventory := tracerFor("frontend"), tracerFor("checkout"), tracerFor("catalog"), tracerFor("payments"), tracerFor("inventory")
	rootCtx, root := frontend.Start(ctx, "POST /checkout", trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(start), trace.WithAttributes(attribute.Int("http.response.status_code", 500)))
	checkoutCtx, order := checkout.Start(rootCtx, "checkout.place", trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(start.Add(20*time.Millisecond)), trace.WithAttributes(attribute.Int("http.response.status_code", 500)))
	_, products := catalog.Start(checkoutCtx, "catalog.lookup", trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(start.Add(40*time.Millisecond)))
	products.End(trace.WithTimestamp(start.Add(110 * time.Millisecond)))
	_, stock := inventory.Start(checkoutCtx, "inventory.reserve", trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(start.Add(120*time.Millisecond)))
	stock.End(trace.WithTimestamp(start.Add(190 * time.Millisecond)))
	_, payment := payments.Start(checkoutCtx, "payment.authorize", trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(start.Add(200*time.Millisecond)))
	payment.RecordError(errors.New("synthetic demo: payment gateway timed out"), trace.WithTimestamp(start.Add(440*time.Millisecond)))
	payment.SetStatus(codes.Error, "synthetic payment timeout")
	payment.End(trace.WithTimestamp(start.Add(450 * time.Millisecond)))
	order.SetStatus(codes.Error, "checkout failed")
	order.End(trace.WithTimestamp(start.Add(470 * time.Millisecond)))
	root.SetStatus(codes.Error, "checkout failed")
	root.End(trace.WithTimestamp(start.Add(490 * time.Millisecond)))
	// Flush every service before shutting down the shared exporter.
	for _, provider := range providers {
		if err := provider.ForceFlush(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	for _, provider := range providers {
		if err := provider.Shutdown(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result == nil {
		fmt.Printf("Synthetic demo trace: %s\n", root.SpanContext().TraceID())
	}
	return result
}
