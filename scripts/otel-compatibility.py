"""Verify real official SDK exports against an isolated Geko, optionally via otelcol.

Install/build fixtures as documented in docs/otel-compatibility.md first.
No downloads, global configuration changes, or fixed application ports are used.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
MATRIX = {'node': ['http/protobuf', 'http/json', 'grpc'],
          'python': ['http/protobuf', 'grpc'], 'java': ['http/protobuf', 'grpc'],
          'dotnet': ['http/protobuf', 'grpc'], 'go': ['http/protobuf']}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


@contextlib.contextmanager
def process(command, directory, env, log_name):
    with (Path(directory) / log_name).open('w', encoding='utf-8') as log:
        child = subprocess.Popen(command, cwd=directory, env=env, stdout=log, stderr=log,
                                 creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
        try:
            yield child
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=5)


def wait_until(check, seconds=30):
    deadline = time.monotonic() + seconds
    while True:
        try:
            return check()
        except (OSError, urllib.error.HTTPError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--languages', nargs='+', choices=MATRIX, default=list(MATRIX))
    parser.add_argument('--protocols', nargs='+', choices=['http/protobuf', 'http/json', 'grpc'])
    parser.add_argument('--collector', type=Path, help='Official otelcol binary; omit for direct export')
    parser.add_argument('--collector-export', choices=['http/protobuf', 'grpc'], default='http/protobuf')
    parser.add_argument('--report', type=Path)
    for runtime in MATRIX:
        parser.add_argument('--' + runtime, default=runtime)
    args = parser.parse_args()
    commands = {
        'node': [args.node, str(ROOT / 'scripts/otel-sdk/node.mjs')],
        'python': [args.python, str(ROOT / 'scripts/otel-sdk/python.py')],
        'java': [args.java, '-jar', str(ROOT / 'examples/sdk/java/target/geko-sdk-example-1.0.0.jar')],
        'dotnet': [args.dotnet, str(ROOT / 'examples/sdk/dotnet/bin/Release/net10.0/GekoSdkExample.dll')],
        'go': [args.go, 'run', './scripts/otel-sdk/go'],
    }
    cells = [(language, protocol) for language in args.languages for protocol in MATRIX[language]
             if args.protocols is None or protocol in args.protocols]
    require(cells, 'No supported SDK/protocol cells selected')
    env = {key: value for key, value in os.environ.items() if not key.startswith(('GEKO_', 'OTEL_'))}
    env.update(DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1')
    token = secrets.token_hex(32)
    results = []
    metadata = {'geko_version': subprocess.check_output([str(args.binary.resolve()), 'version'], text=True).strip(),
                'geko_binary_sha256': hashlib.sha256(args.binary.read_bytes()).hexdigest()}
    if args.collector:
        metadata['collector_version'] = subprocess.check_output([str(args.collector.resolve()), '--version'], text=True).strip()
        metadata['collector_binary_sha256'] = hashlib.sha256(args.collector.read_bytes()).hexdigest()
    with tempfile.TemporaryDirectory(prefix='geko-otel-compatibility-') as directory, contextlib.ExitStack() as stack:
        http_port, grpc_port = free_port(), free_port()
        base = f'http://127.0.0.1:{http_port}'
        receiver_env = dict(env, GEKO_LISTEN_ADDR=f'127.0.0.1:{http_port}',
                            GEKO_GRPC_ADDR=f'127.0.0.1:{grpc_port}', GEKO_ACCESS_TOKEN=token,
                            GEKO_DB_PATH=str(Path(directory) / 'geko.db'), GEKO_REDACTION='best_effort')
        server = stack.enter_context(process([str(args.binary.resolve()), 'serve'], directory, receiver_env, 'geko.log'))

        def get(path):
            require(server.poll() is None, 'Isolated Geko exited')
            request = urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + token})
            with urllib.request.urlopen(request, timeout=5) as response:
                return json.load(response) if path.startswith('/api/') or path.endswith('/otlp.json') else response.read()

        wait_until(lambda: get('/readyz'))
        export_http, export_grpc = http_port, grpc_port
        if args.collector:
            export_http, export_grpc = free_port(), free_port()
            target = ('otlphttp', f'http://127.0.0.1:{http_port}') if args.collector_export == 'http/protobuf' else ('otlp', f'127.0.0.1:{grpc_port}')
            config = Path(directory) / 'collector.yaml'
            config.write_text(f'''receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{export_http}
      grpc:
        endpoint: 127.0.0.1:{export_grpc}
exporters:
  {target[0]}:
    endpoint: {target[1]}
    tls:
      insecure: true
    headers:
      authorization: Bearer {token}
service:
  telemetry:
    metrics:
      level: none
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [{target[0]}]
''', encoding='utf-8')
            collector = stack.enter_context(process([str(args.collector.resolve()), '--config', str(config)], directory, env, 'collector.log'))

            def collector_ready():
                require(collector.poll() is None, 'Collector exited before readiness')
                with socket.create_connection(('127.0.0.1', export_http), timeout=1):
                    pass
            wait_until(collector_ready)
        for language, protocol in cells:
            run_id = secrets.token_hex(16)
            service = 'compatibility-' + language
            endpoint = f'http://127.0.0.1:{export_grpc if protocol == "grpc" else export_http}'
            child_env = dict(env, OTEL_SERVICE_NAME=service,
                OTEL_RESOURCE_ATTRIBUTES=f'service.namespace=SDKCompatibility,deployment.environment.name=test,service.version=1.0.0,geko.run.id={run_id}',
                OTEL_EXPORTER_OTLP_ENDPOINT=endpoint, OTEL_EXPORTER_OTLP_PROTOCOL=protocol,
                OTEL_EXPORTER_OTLP_TIMEOUT='10000', OTEL_EXPORTER_OTLP_INSECURE='true',
                OTEL_EXPORTER_OTLP_HEADERS='' if args.collector else 'authorization=Bearer%20' + token,
                OTEL_METRICS_EXPORTER='none', OTEL_LOGS_EXPORTER='none', OTEL_TRACES_SAMPLER='always_on')
            completed = subprocess.run(commands[language], cwd=ROOT, env=child_env, capture_output=True, text=True, timeout=120)
            require(completed.returncode == 0, f'{language}/{protocol} failed: ' + completed.stderr.replace(token, '<redacted>'))
            records = [json.loads(line) for line in completed.stdout.splitlines() if line.startswith('{')]
            require(len(records) == 2 and {r['scenario'] for r in records} == {'success', 'failure'}, 'Expected two SDK trace records')
            for record in records:
                page = wait_until(lambda: get('/api/v1/traces/' + record['trace_id']), seconds=20)
                entry = page['trace']['entry_span']
                require(entry and entry['service'] == service and entry.get('application') == 'SDKCompatibility', 'Resource service/namespace lost')
                require(entry['http_status'] == record['status'], 'HTTP status lost')
                for span in page['spans']:
                    require(span.get('otel', {}).get('present') is True, 'Span OTLP capture provenance missing')
                    resource = span['resource_attributes']
                    require(resource.get('geko.run.id') == run_id and resource.get('service.version') == '1.0.0'
                            and resource.get('deployment.environment.name') == 'test', 'Resource context lost')
                evidence = page['trace']['evidence']
                if record['scenario'] == 'failure':
                    require(evidence and evidence['classification'] == 'exception', 'Exception evidence lost')
                    require(any(event['attributes'].get('exception.stacktrace') for span in page['spans']
                                for event in span['events'] if event['name'] == 'exception'), 'Exception stack trace lost')
                else:
                    require(evidence is None, 'Healthy trace marked failing')
                if language == 'go' and record['scenario'] == 'failure':
                    target = next(span for span in page['spans'] if span['span_id'] == record['span_id'])
                    links = target['otel'].get('links', [])
                    require(len(links) == 1 and links[0]['trace_id'] == record['linked_trace_id']
                            and links[0]['span_id'] == record['linked_span_id'], 'Cross-trace SDK span link lost')
                    retry = next((event for event in target['events'] if event['name'] == 'retry'), None)
                    require(retry is not None and retry['attributes'].get('retry.attempt') == 9007199254740993
                            and retry['attributes'].get('retry.delay_ms') == 1.0, 'Retry event numeric values lost')
                    downloaded = get('/traces/' + record['trace_id'] + '/otlp.json')
                    wire_spans = [span for resource in downloaded['resourceSpans'] for scope in resource['scopeSpans']
                                  for span in scope['spans']]
                    wire = next(span for span in wire_spans if span['name'] == 'GET /failure')
                    wire_links = wire.get('links', [])
                    require(len(wire_links) == 1 and wire_links[0]['traceId'] == record['linked_trace_id']
                            and wire_links[0]['spanId'] == record['linked_span_id'],
                            'Downloaded OTLP span link IDs lost')
                    wire_retry = next(event for event in wire['events'] if event['name'] == 'retry')
                    values = {item['key']: item['value'] for item in wire_retry['attributes']}
                    require(values.get('retry.attempt') == {'intValue': '9007199254740993'}, 'OTLP int64 event type/precision lost')
                    require(values.get('retry.delay_ms') == {'doubleValue': 1}, 'OTLP double event type lost')
            failures = get('/api/v1/failures?' + urllib.parse.urlencode({'run_id': run_id}))
            require(len(failures['items']) == 1, 'Run ID filter did not isolate one failure')
            result = dict(language=language, protocol=protocol,
                          route='collector/' + args.collector_export if args.collector else 'direct',
                          accepted_traces=2, exception_stacktrace=True, resource_context=True, run_id_filter=True)
            result['otel_capture_provenance'] = True
            if language == 'go':
                result.update(cross_trace_span_link=True, retry_event_int64=True, retry_event_double=True,
                              otlp_download_numeric_types=True)
            results.append(result)
            print(json.dumps(result), flush=True)
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps({'metadata': metadata, 'results': results}, indent=2) + '\n', encoding='utf-8')


if __name__ == '__main__':
    main()
