"""Finite official SDK exporter fixture; JSON is deliberately not offered."""
import json
import os

from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import SpanKind, Status, StatusCode

protocol = os.environ['OTEL_EXPORTER_OTLP_PROTOCOL']
if protocol == 'grpc':
    from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
elif protocol == 'http/protobuf':
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
else:
    raise ValueError('Unsupported protocol')

provider = TracerProvider(resource=Resource.create({}))
provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
try:
    for scenario in ('success', 'failure'):
        with provider.get_tracer('geko.compatibility.python').start_as_current_span(
                'GET /' + scenario, kind=SpanKind.SERVER) as span:
            status = 500 if scenario == 'failure' else 200
            span.set_attribute('http.response.status_code', status)
            if status == 500:
                try:
                    raise RuntimeError('Synthetic compatibility failure')
                except RuntimeError as error:
                    span.record_exception(error)
                    span.set_status(Status(StatusCode.ERROR))
            print(json.dumps(dict(scenario=scenario, status=status,
                                 trace_id=format(span.get_span_context().trace_id, '032x'))))
    if not provider.force_flush(timeout_millis=10000):
        raise RuntimeError('Trace flush timed out')
finally:
    provider.shutdown()
