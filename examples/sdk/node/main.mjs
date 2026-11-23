import { createServer } from 'node:http';
import { once } from 'node:events';
import { trace, SpanKind, SpanStatusCode } from '@opentelemetry/api';
import { NodeSDK } from '@opentelemetry/sdk-node';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto';

// This example deliberately uses OTLP/HTTP protobuf. Do not silently ignore a
// gRPC setting copied from a different exporter configuration.
const protocol = process.env.OTEL_EXPORTER_OTLP_TRACES_PROTOCOL ?? process.env.OTEL_EXPORTER_OTLP_PROTOCOL ?? 'http/protobuf';
if (protocol !== 'http/protobuf') throw new Error('This example requires OTLP http/protobuf');
process.env.OTEL_SERVICE_NAME ||= 'geko-node-sdk';
process.env.OTEL_METRICS_EXPORTER ??= 'none';
process.env.OTEL_LOGS_EXPORTER ??= 'none';
const sdk = new NodeSDK({ traceExporter: new OTLPTraceExporter() });
sdk.start();
const tracer = trace.getTracer('geko.sdk.node');
const server = createServer((request, response) => {
  const failing = request.url === '/failure';
  const route = failing ? '/failure' : '/success';
  tracer.startActiveSpan(`GET ${route}`, { kind: SpanKind.SERVER, attributes: { 'http.request.method': 'GET', 'http.route': route } }, span => {
    try {
      if (failing) tracer.startActiveSpan('payment.authorize', child => {
        try { throw new Error('Demo payment gateway unavailable'); }
        catch (error) { child.recordException(error); child.setStatus({ code: SpanStatusCode.ERROR }); }
        finally { child.end(); }
      });
      const status = failing ? 500 : 200;
      span.setAttribute('http.response.status_code', status);
      if (failing) span.setStatus({ code: SpanStatusCode.ERROR });
      response.writeHead(status, { 'content-type': 'application/json' });
      response.end(JSON.stringify({ scenario: route.slice(1), status, trace_id: span.spanContext().traceId }));
    } finally { span.end(); }
  });
});

try {
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  for (const scenario of ['success', 'failure']) {
    const response = await fetch(`http://127.0.0.1:${server.address().port}/${scenario}`, { signal: AbortSignal.timeout(10000) });
    if (response.status !== (scenario === 'failure' ? 500 : 200)) throw new Error('Unexpected example response');
    console.log(JSON.stringify(await response.json()));
  }
} finally {
  try {
    if (server.listening) await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  } finally { await sdk.shutdown(); }
}
