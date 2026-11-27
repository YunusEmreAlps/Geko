import { trace, SpanKind, SpanStatusCode } from '@opentelemetry/api';
import { NodeSDK } from '@opentelemetry/sdk-node';

const exporters = {
  'http/protobuf': '@opentelemetry/exporter-trace-otlp-proto',
  'http/json': '@opentelemetry/exporter-trace-otlp-http',
  grpc: '@opentelemetry/exporter-trace-otlp-grpc',
};
const name = exporters[process.env.OTEL_EXPORTER_OTLP_PROTOCOL];
if (!name) throw new Error('Unsupported protocol');
const { OTLPTraceExporter } = await import(name);
const sdk = new NodeSDK({ traceExporter: new OTLPTraceExporter() });
sdk.start();
try {
  const tracer = trace.getTracer('geko.compatibility.node');
  for (const scenario of ['success', 'failure']) {
    tracer.startActiveSpan(`GET /${scenario}`, { kind: SpanKind.SERVER }, span => {
      const status = scenario === 'failure' ? 500 : 200;
      span.setAttribute('http.response.status_code', status);
      if (status === 500) {
        span.recordException(new Error('Synthetic compatibility failure'));
        span.setStatus({ code: SpanStatusCode.ERROR });
      }
      span.end();
      console.log(JSON.stringify({ scenario, status, trace_id: span.spanContext().traceId }));
    });
  }
} finally { await sdk.shutdown(); }
