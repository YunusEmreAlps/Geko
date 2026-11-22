import { appendFileSync, readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';

const unavailableSummary = '### Geko: telemetry report unavailable\n\nThe failure count is unknown. Check the Geko diagnostic output; this is not a zero-failure result.\n';

export function readActionReport(directory) {
  try {
    const status = JSON.parse(readFileSync(join(directory, 'report.json'), 'utf8'));
    if (status.telemetry_status !== 'available' || !/^[0-9a-f]{32}$/.test(status.run_id) ||
        !Number.isInteger(status.failures) || status.failures < 0 || status.failures > 25 ||
        typeof status.truncated !== 'boolean' || (status.truncated && status.failures !== 25)) {
      throw new Error('Unavailable or invalid report');
    }
    const summary = readFileSync(join(directory, 'report.md'), 'utf8');
    if (!summary.trim()) throw new Error('Missing summary');
    return { telemetryStatus: 'available', failures: `${status.failures}${status.truncated ? '+' : ''}`, summary };
  } catch {
    return { telemetryStatus: 'unavailable', failures: 'unknown', summary: unavailableSummary };
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (!process.env.REPORT || !process.env.GITHUB_OUTPUT || !process.env.GITHUB_STEP_SUMMARY) {
    throw new Error('REPORT, GITHUB_OUTPUT and GITHUB_STEP_SUMMARY are required');
  }
  const report = readActionReport(process.env.REPORT);
  appendFileSync(process.env.GITHUB_OUTPUT, `telemetry_status=${report.telemetryStatus}\nfailures=${report.failures}\n`);
  appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${report.summary}\n`);
  if (report.telemetryStatus !== 'available') {
    process.stdout.write('::warning::Geko telemetry report unavailable; failure count is unknown.\n');
  }
}
