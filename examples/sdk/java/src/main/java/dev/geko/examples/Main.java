package dev.geko.examples;

import com.sun.net.httpserver.HttpServer;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.StatusCode;
import io.opentelemetry.context.Scope;
import io.opentelemetry.sdk.OpenTelemetrySdk;
import io.opentelemetry.sdk.autoconfigure.AutoConfiguredOpenTelemetrySdk;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Map;
import java.util.concurrent.TimeUnit;

public class Main {
    public static void main(String[] args) throws Exception {
        OpenTelemetrySdk sdk = AutoConfiguredOpenTelemetrySdk.builder().addPropertiesSupplier(() -> Map.of(
            "otel.service.name", "geko-java-sdk", "otel.exporter.otlp.protocol", "http/protobuf",
            "otel.metrics.exporter", "none", "otel.logs.exporter", "none")).build().getOpenTelemetrySdk();
        var tracer = sdk.getTracer("geko.sdk.java");
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            boolean failing = exchange.getRequestURI().getPath().equals("/failure");
            String route = failing ? "/failure" : "/success";
            Span span = tracer.spanBuilder("GET " + route).setSpanKind(SpanKind.SERVER).startSpan();
            try (Scope scope = span.makeCurrent()) {
                span.setAttribute("http.request.method", "GET");
                span.setAttribute("http.route", route);
                if (failing) {
                    Span child = tracer.spanBuilder("payment.authorize").startSpan();
                    try { throw new IllegalStateException("Demo payment gateway unavailable"); }
                    catch (IllegalStateException error) { child.recordException(error); child.setStatus(StatusCode.ERROR); }
                    finally { child.end(); }
                }
                int status = failing ? 500 : 200;
                span.setAttribute("http.response.status_code", status);
                if (failing) span.setStatus(StatusCode.ERROR);
                byte[] data = String.format("{\"scenario\":\"%s\",\"status\":%d,\"trace_id\":\"%s\"}", route.substring(1), status, span.getSpanContext().getTraceId()).getBytes(StandardCharsets.UTF_8);
                exchange.getResponseHeaders().set("Content-Type", "application/json");
                exchange.sendResponseHeaders(status, data.length);
                exchange.getResponseBody().write(data);
            } finally { exchange.close(); span.end(); }
        });
        try {
            server.start();
            try (HttpClient client = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build()) {
                for (String scenario : new String[]{"success", "failure"}) {
                    var response = client.send(HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + server.getAddress().getPort() + "/" + scenario)).timeout(Duration.ofSeconds(10)).build(), HttpResponse.BodyHandlers.ofString());
                    if (response.statusCode() != (scenario.equals("failure") ? 500 : 200)) throw new IllegalStateException("Unexpected example response");
                    System.out.println(response.body());
                }
            }
        } finally {
            server.stop(0);
            try {
                if (!sdk.getSdkTracerProvider().forceFlush().join(10, TimeUnit.SECONDS).isSuccess()) throw new IllegalStateException("Trace flush did not complete");
            } finally { sdk.close(); }
        }
    }
}
