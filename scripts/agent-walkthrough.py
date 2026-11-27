"""Reproduce a synthetic Go defect through real Geko run reports and MCP stdio.

Baseline deliberately breaks a scratch copy; verify never edits that copy.
An investigator must inspect evidence and fix work-dir/source/quote.go between
the two phases. No coding-agent conversation is simulated by this harness.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import shutil
import socket
import subprocess
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n', encoding='utf-8')


class MCP:
    """Newline JSON-RPC transport to the actual `geko mcp` process."""
    def __init__(self, binary, cwd, env):
        self.log = (cwd / 'mcp-stderr.log').open('a', encoding='utf-8')
        self.process = subprocess.Popen([str(binary), 'mcp'], cwd=cwd, env=env,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.log,
            text=True, encoding='utf-8', creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
        self.messages = queue.Queue()
        self.counter = 0
        self.transcript = []
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self):
        for line in self.process.stdout:
            self.messages.put(line)
        self.messages.put(None)

    def request(self, method, params):
        self.counter += 1
        message = {'jsonrpc': '2.0', 'id': self.counter, 'method': method, 'params': params}
        self.transcript.append({'sent': message})
        self.process.stdin.write(json.dumps(message) + '\n')
        self.process.stdin.flush()
        while True:
            line = self.messages.get(timeout=30)
            require(line is not None, 'MCP process exited before responding')
            response = json.loads(line)
            self.transcript.append({'received': response})
            if response.get('id') == self.counter:
                require('error' not in response, f'MCP protocol error: {response}')
                return response['result']

    def initialize(self):
        self.request('initialize', {'protocolVersion': '2025-03-26', 'capabilities': {},
            'clientInfo': {'name': 'geko-agent-walkthrough', 'version': '1'}})
        self.process.stdin.write(json.dumps({'jsonrpc': '2.0', 'method': 'notifications/initialized'}) + '\n')
        self.process.stdin.flush()

    def call(self, name, arguments):
        result = self.request('tools/call', {'name': name, 'arguments': arguments})
        require(not result.get('isError'), f'MCP tool {name} failed: {result}')
        if 'structuredContent' in result:
            return result['structuredContent']
        return json.loads(next(block['text'] for block in result['content'] if block['type'] == 'text'))

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.process.stdout.close()
        self.log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--work-dir', required=True, type=Path)
    parser.add_argument('--phase', required=True, choices=['baseline', 'verify'])
    args = parser.parse_args()
    binary, work = args.binary.resolve(), args.work_dir.resolve()
    require(binary.is_file(), 'Build Geko first; --binary must exist')
    if args.phase == 'baseline':
        require(not work.exists(), 'Use a new --work-dir; existing evidence is never overwritten')
        work.mkdir(parents=True)
        (work / 'source').mkdir()
        for name in ('go.mod', 'go.sum'):
            shutil.copy2(ROOT / name, work / name)
        for name in ('quote.go', 'quote_test.go'):
            shutil.copy2(ROOT / 'examples/agent-investigation' / name, work / 'source' / name)
        path = work / 'source/quote.go'
        source = path.read_text(encoding='utf-8')
        # Deliberately introduce only the whitespace-normalization defect.
        source = source.replace('strconv.Atoi(strings.TrimSpace(quantity))', 'strconv.Atoi(quantity)')
        source = source.replace('\n\t"strings"', '')
        require('strconv.Atoi(quantity)' in source, 'Fixture mutation anchor changed')
        path.write_text(source, encoding='utf-8')
    require((work / 'source/quote.go').is_file(), 'Run baseline first')
    report = work / args.phase
    require(not report.exists(), 'Phase already recorded; use a new work directory')
    workload_hash = hashlib.sha256((work / 'source/quote_test.go').read_bytes()).hexdigest()
    if args.phase == 'verify':
        baseline_command = json.loads((work / 'baseline/command.json').read_text(encoding='utf-8'))
        require(baseline_command['workload_sha256'] == workload_hash,
                'The test workload changed; verify must use the unchanged baseline test')
    env = {key: value for key, value in os.environ.items() if not key.startswith(('GEKO_', 'OTEL_'))}
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    env.update(GEKO_LISTEN_ADDR=f'127.0.0.1:{port}', GEKO_DB_PATH=str(work / 'geko.db'))
    command = [str(binary), 'run', '--report', str(report)]
    if args.phase == 'verify':
        command += ['--baseline', str(work / 'baseline/run.json')]
    command += ['--', 'go', 'test', '-count=1', '-v', './source']
    # Await explicit readiness: cold first database initialization can exceed
    # `run`'s short embedded-server startup window on a busy development host.
    with (work / 'server.log').open('a', encoding='utf-8') as log:
        server = subprocess.Popen([str(binary), 'serve'], cwd=work, env=env,
            stdout=log, stderr=log, creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
        try:
            deadline = time.monotonic() + 60
            while True:
                require(server.poll() is None, 'Geko exited before readiness; inspect server.log')
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/readyz', timeout=2) as response:
                        require(response.status in (200, 204), 'Geko not ready')
                    break
                except OSError:
                    require(time.monotonic() < deadline, 'Geko readiness timed out')
                    time.sleep(0.1)
            result = subprocess.run(command, cwd=work, env=env, capture_output=True, text=True, timeout=180)
        finally:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait(timeout=5)
    report.mkdir(exist_ok=True)
    (report / 'command.stdout.txt').write_text(result.stdout, encoding='utf-8')
    (report / 'command.stderr.txt').write_text(result.stderr, encoding='utf-8')
    write_json(report / 'command.json', {'command': command, 'exit_code': result.returncode,
        'workload_sha256': workload_hash,
        'source_sha256': hashlib.sha256((work / 'source/quote.go').read_bytes()).hexdigest(),
        'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest()})
    expected = 1 if args.phase == 'baseline' else 0
    require(result.returncode == expected, f'{args.phase}: expected child test exit {expected}, got {result.returncode}; inspect {report}')
    snapshot = json.loads((report / 'run.json').read_text(encoding='utf-8'))
    require(snapshot['collection_status'] == 'available' and not snapshot['truncated'], 'Incomplete run evidence')
    require(snapshot['traces'] == 2 and snapshot['tagged_spans'] == 2, 'Expected both workload traces with run IDs')
    require(snapshot['failing_traces'] == (1 if args.phase == 'baseline' else 0), 'Unexpected failing trace count')
    client = MCP(binary, work, env)
    try:
        client.initialize()
        if args.phase == 'baseline':
            require(len(snapshot['groups']) == 1, 'Expected one baseline group')
            investigation = client.call('investigate_group', {'group_id': snapshot['groups'][0]['group_id']})
            write_json(report / 'investigation.json', investigation)
            require(investigation['evidence_spans'], 'MCP returned no exception evidence')
        else:
            baseline = json.loads((work / 'baseline/run.json').read_text(encoding='utf-8'))
            comparison = client.call('compare_runs', {'baseline_run_id': baseline['run_id'], 'candidate_run_id': snapshot['run_id']})
            write_json(report / 'mcp-comparison.json', comparison)
            require(comparison['status'] == 'partial' and
                    comparison['baseline']['collection_status'] == 'available' and
                    comparison['candidate']['collection_status'] == 'available',
                    'MCP comparison unavailable or unexpected coverage semantics')
            require(len(comparison['groups']) == 1 and comparison['groups'][0]['state'] == 'not_observed', 'Unexpected comparison classification')
    finally:
        try:
            write_json(report / 'mcp-transcript.json', client.transcript)
        finally:
            client.close()
    print(json.dumps({'phase': args.phase, 'exit_code': result.returncode, 'run_id': snapshot['run_id'],
        'traces': snapshot['traces'], 'failing_traces': snapshot['failing_traces'], 'report': str(report)}))


if __name__ == '__main__':
    main()
