import { appendFileSync, readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';

const unavailableSummary = '### Geko: telemetry report unavailable\n\nThe failure count is unknown. Check the Geko diagnostic output; this is not a zero-failure result.\n';
const validRunID = value => typeof value === 'string' && /^[0-9a-f]{32}$/.test(value) && !/^0+$/.test(value);

export function readActionReport(directory) {
  try {
    const status = JSON.parse(readFileSync(join(directory, 'report.json'), 'utf8'));
    if (status.telemetry_status !== 'available' || !validRunID(status.run_id) ||
        !Number.isInteger(status.limit) || status.limit < 1 ||
        !Number.isInteger(status.failures) || status.failures < 0 || status.failures > status.limit ||
        typeof status.truncated !== 'boolean' || (status.truncated && status.failures !== status.limit)) {
      throw new Error('Unavailable or invalid report');
    }
    const summary = readFileSync(join(directory, 'report.md'), 'utf8');
    if (!summary.trim()) throw new Error('Missing summary');
    return { telemetryStatus: 'available', failures: `${status.failures}${status.truncated ? '+' : ''}`, summary, runID: status.run_id };
  } catch {
    return { telemetryStatus: 'unavailable', failures: 'unknown', summary: unavailableSummary };
  }
}

export function readActionComparison(directory, requested, runID) {
  if (!requested) return 'not_requested';
  if (!validRunID(runID)) return 'unavailable';
  try {
    const value = JSON.parse(readFileSync(join(directory, 'comparison.json'), 'utf8'));
    if (value.status !== 'partial' || !validRunID(value.baseline?.run_id) ||
        value.candidate?.run_id !== runID || value.baseline.run_id === runID ||
        !Array.isArray(value.groups) || value.groups.length > 200) return 'unavailable';
    return 'partial';
  } catch { return 'unavailable'; }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (!process.env.REPORT || !process.env.GITHUB_OUTPUT || !process.env.GITHUB_STEP_SUMMARY) {
    throw new Error('REPORT, GITHUB_OUTPUT and GITHUB_STEP_SUMMARY are required');
  }
  const report = readActionReport(process.env.REPORT);
  const comparison = readActionComparison(process.env.REPORT, !!process.env.BASELINE, report.runID);
  appendFileSync(process.env.GITHUB_OUTPUT, `telemetry_status=${report.telemetryStatus}\nfailures=${report.failures}\ncomparison_status=${comparison}\nrun_id=${report.runID ?? ''}\n`);
  appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${report.summary}\n`);
  if (report.telemetryStatus !== 'available') {
    process.stdout.write('::warning::Geko telemetry report unavailable; failure count is unknown.\n');
  }
  if (comparison === 'unavailable') {
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, '\n### Geko run comparison unavailable\n\nNo fix can be inferred from missing comparison evidence.\n');
    process.stdout.write('::warning::Geko run comparison unavailable; no fix is inferred.\n');
  }
}
