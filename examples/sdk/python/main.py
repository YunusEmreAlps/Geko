"""Two real loopback HTTP requests, recorded and exported by the official SDK."""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
import urllib.error
import urllib.request

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import SpanKind, Status, StatusCode


def main():
    protocol = os.getenv('OTEL_EXPORTER_OTLP_TRACES_PROTOCOL', os.getenv('OTEL_EXPORTER_OTLP_PROTOCOL', 'http/protobuf'))
    if protocol != 'http/protobuf':
        raise ValueError('This example requires OTLP http/protobuf')
    provider = TracerProvider(resource=Resource.create({'service.name': os.getenv('OTEL_SERVICE_NAME', 'geko-python-sdk')}))
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    tracer = provider.get_tracer('geko.sdk.python')

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            failing = self.path == '/failure'
            route = '/failure' if failing else '/success'
            with tracer.start_as_current_span('GET ' + route, kind=SpanKind.SERVER) as span:
                span.set_attribute('http.request.method', 'GET')
                span.set_attribute('http.route', route)
                if failing:
                    with tracer.start_as_current_span('payment.authorize') as child:
                        try:
                            raise RuntimeError('Demo payment gateway unavailable')
                        except RuntimeError as error:
                            child.record_exception(error)
                            child.set_status(Status(StatusCode.ERROR))
                status = 500 if failing else 200
                span.set_attribute('http.response.status_code', status)
                if failing:
                    span.set_status(Status(StatusCode.ERROR))
                data = json.dumps({'scenario': route[1:], 'status': status, 'trace_id': format(span.get_span_context().trace_id, '032x')}).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        def log_message(self, *args):
            pass

    server = HTTPServer(('127.0.0.1', 0), Handler)
    worker = Thread(target=server.serve_forever, daemon=True)
    worker.start()
    try:
        for scenario in ('success', 'failure'):
            try:
                response = urllib.request.urlopen(f'http://127.0.0.1:{server.server_port}/{scenario}', timeout=10)
            except urllib.error.HTTPError as error:
                response = error  # The intentional 500 still has a trace ID.
            with response:
                if response.status != (500 if scenario == 'failure' else 200):
                    raise RuntimeError('Unexpected example response')
                print(json.dumps(json.load(response)))
    finally:
        server.shutdown()
        server.server_close()
        worker.join(timeout=5)
        try:
            if not provider.force_flush(timeout_millis=10000):
                raise RuntimeError('Trace flush did not complete')
        finally:
            provider.shutdown()


if __name__ == '__main__':
    main()
