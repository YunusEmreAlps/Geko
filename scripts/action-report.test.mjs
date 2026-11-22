import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { readActionReport } from './action-report.mjs';

const available = { run_id: 'a'.repeat(32), telemetry_status: 'available', failures: 0, truncated: false };
test('only a complete, valid manifest can report a known count', async t => {
  for (const [name, manifest, summary, expected] of [
    ['missing manifest with stale success', undefined, 'No failures', 'unknown'],
    ['unavailable', { ...available, telemetry_status: 'unavailable' }, 'No failures', 'unknown'],
    ['missing summary', available, undefined, 'unknown'],
    ['invalid count', { ...available, failures: '0\nstatus=available' }, 'No failures', 'unknown'],
    ['inconsistent truncation', { ...available, truncated: true }, 'No failures', 'unknown'],
    ['zero', available, 'No failures', '0'],
    ['bounded', { ...available, failures: 25, truncated: true }, 'More failures', '25+'],
  ]) await t.test(name, () => {
    const dir = mkdtempSync(join(tmpdir(), 'geko-action-'));
    try {
      writeFileSync(join(dir, 'failures'), '0\n');
      if (manifest) writeFileSync(join(dir, 'report.json'), JSON.stringify(manifest));
      if (summary) writeFileSync(join(dir, 'report.md'), summary);
      const result = readActionReport(dir);
      assert.equal(result.failures, expected);
      assert.equal(result.telemetryStatus, expected === 'unknown' ? 'unavailable' : 'available');
      if (expected === 'unknown') assert.match(result.summary, /not a zero-failure result/);
    } finally { rmSync(dir, { recursive: true }); }
  });
});

test('unavailable report emits a warning and unknown outputs without failing the command', () => {
  const dir = mkdtempSync(join(tmpdir(), 'geko-action-'));
  try {
    const output = join(dir, 'output'), summary = join(dir, 'summary');
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('./action-report.mjs', import.meta.url))], {
      env: { ...process.env, REPORT: dir, GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary }, encoding: 'utf8',
    });
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /::warning::/);
    assert.equal(readFileSync(output, 'utf8'), 'telemetry_status=unavailable\nfailures=unknown\n');
    assert.match(readFileSync(summary, 'utf8'), /failure count is unknown/);
  } finally { rmSync(dir, { recursive: true }); }
});
