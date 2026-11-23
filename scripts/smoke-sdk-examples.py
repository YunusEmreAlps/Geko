"""Exercise real SDK examples against a temporary, token-protected Geko.

Build Java/.NET and install Node/Python dependencies first; see examples/sdk.
No external service is contacted by this script. Child SDKs export only to the
temporary loopback receiver, and the token is never written to a report.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--languages', nargs='+', choices=['node', 'python', 'java', 'dotnet'], default=['node', 'python', 'java', 'dotnet'])
    parser.add_argument('--node', default='node')
    parser.add_argument('--python', default='python')
    parser.add_argument('--java', default='java')
    parser.add_argument('--dotnet', default='dotnet')
    args = parser.parse_args()
    commands = {
        'node': [args.node, str(ROOT / 'examples/sdk/node/main.mjs')],
        'python': [args.python, str(ROOT / 'examples/sdk/python/main.py')],
        'java': [args.java, '-jar', str(ROOT / 'examples/sdk/java/target/geko-sdk-example-1.0.0.jar')],
        'dotnet': [args.dotnet, str(ROOT / 'examples/sdk/dotnet/bin/Release/net10.0/GekoSdkExample.dll')],
    }
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    token = secrets.token_hex(32)
    env = {key: value for key, value in os.environ.items() if not key.startswith(('GEKO_', 'OTEL_'))}
    env.update(DOTNET_NOLOGO='1', DOTNET_CLI_TELEMETRY_OPTOUT='1')
    with tempfile.TemporaryDirectory(prefix='geko-sdk-smoke-') as directory:
        server_env = dict(env, GEKO_LISTEN_ADDR=f'127.0.0.1:{port}', GEKO_DB_PATH=str(Path(directory) / 'geko.db'),
                          GEKO_ACCESS_TOKEN=token, GEKO_REDACTION='best_effort')
        with (Path(directory) / 'server.log').open('w', encoding='utf-8') as log:
            server = subprocess.Popen([str(args.binary.resolve()), 'serve'], cwd=directory, env=server_env,
                stdout=log, stderr=log, creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
            try:
                def get(path):
                    request = urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + token})
                    with urllib.request.urlopen(request, timeout=5) as response:
                        return json.load(response) if response.status == 200 else None

                deadline = time.monotonic() + 30
                while True:
                    require(server.poll() is None, 'Geko exited before readiness')
                    try:
                        get('/readyz')
                        break
                    except OSError:
                        require(time.monotonic() < deadline, 'Geko readiness timed out')
                        time.sleep(0.1)
                for language in args.languages:
                    run_id = secrets.token_hex(16)
                    service = 'sdk-' + language
                    child_env = dict(env, OTEL_SERVICE_NAME=service,
                        OTEL_RESOURCE_ATTRIBUTES=f'service.namespace=SDKExamples,deployment.environment.name=test,service.version=1.0.0,geko.run.id={run_id}',
                        OTEL_EXPORTER_OTLP_ENDPOINT=base, OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf',
                        OTEL_EXPORTER_OTLP_HEADERS='authorization=Bearer%20' + token,
                        OTEL_METRICS_EXPORTER='none', OTEL_LOGS_EXPORTER='none', OTEL_TRACES_SAMPLER='always_on')
                    result = subprocess.run(commands[language], cwd=ROOT, env=child_env, capture_output=True, text=True, timeout=60)
                    require(result.returncode == 0, f'{language} example failed: ' + result.stderr.replace(token, '<redacted>'))
                    records = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
                    require(len(records) == 2, f'{language}: expected two request results')
                    require({row['scenario'] for row in records} == {'success', 'failure'}, f'{language}: missing scenario')
                    for row in records:
                        trace_id = row.get('trace_id', '')
                        require(isinstance(trace_id, str) and re.fullmatch('[0-9a-f]{32}', trace_id), f'{language}: invalid trace ID')
                        page = get('/api/v1/traces/' + trace_id)
                        summary = page['trace']
                        require(summary['entry_span'] is not None, f'{language}: no parentless entry span')
                        entry = summary['entry_span']
                        require(entry['service'] == service and entry.get('application') == 'SDKExamples', f'{language}: resource overrides were lost')
                        require(entry['http_status'] == row['status'], f'{language}: incorrect HTTP outcome')
                        for span in page['spans']:
                            resource = span['resource_attributes']
                            require(resource.get('geko.run.id') == run_id, f'{language}: run ID was not exported')
                            require(resource.get('deployment.environment.name') == 'test' and resource.get('service.version') == '1.0.0', f'{language}: resource context missing')
                        if row['scenario'] == 'failure':
                            require(summary['evidence'] is not None and summary['evidence']['classification'] == 'exception', f'{language}: exception missing')
                            exceptions = [event for span in page['spans'] for event in span['events'] if event['name'] == 'exception']
                            require(any(event['attributes'].get('exception.stacktrace') for event in exceptions), f'{language}: stack trace missing')
                        else:
                            require(summary['evidence'] is None and row['status'] == 200, f'{language}: healthy request marked failing')
                    failures = get('/api/v1/failures?' + urllib.parse.urlencode({'run_id': run_id}))
                    require(len(failures['items']) == 1, f'{language}: run-filtered failures are incorrect')
                    print(json.dumps({'language': language, 'successful_trace': True, 'failure_evidence': True,
                                      'stack_trace': True, 'resource_context': True, 'run_id_filter': True, 'flushed_before_exit': True}), flush=True)
            finally:
                server.terminate()
                server.wait(timeout=15)


if __name__ == '__main__':
    main()
