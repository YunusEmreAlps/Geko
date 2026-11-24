"""Exercise the nightly comparator's failure paths without running a benchmark."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('bench-compare.py')


class RegressionGuardTests(unittest.TestCase):
    def compare(self, current, baseline=None, extra=()):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'current.json').write_text(json.dumps(current))
            args = [sys.executable, str(SCRIPT), '--current', str(root / 'current.json')]
            if baseline is not None:
                (root / 'baseline.json').write_text(json.dumps(baseline))
                args += ['--baseline', str(root / 'baseline.json')]
            return subprocess.run(args + list(extra), capture_output=True, text=True, timeout=10)

    def test_same_baseline_passes(self):
        self.assertEqual(self.compare({'median_export_ms': 10}, {'median_export_ms': 10}).returncode, 0)

    def test_deliberate_regression_fails(self):
        self.assertEqual(self.compare({'median_export_ms': 20}, {'median_export_ms': 10}).returncode, 1)

    def test_incomplete_measurement_fails(self):
        self.assertNotEqual(self.compare({'complete': False, 'median_export_ms': 1}, {'median_export_ms': 10}).returncode, 0)

    def test_refused_exports_fail_even_without_timing_regression(self):
        self.assertEqual(self.compare({'median_export_ms': 1, 'parallel_exports_rejected_503': 1}, {'median_export_ms': 10}).returncode, 1)

    def test_required_baseline_cannot_silently_disappear(self):
        self.assertNotEqual(self.compare({'median_export_ms': 10}, extra=['--require-baseline']).returncode, 0)

    def test_median_cannot_hide_one_failed_round(self):
        value = {'median_of_rounds': {'median_export_ms': 10, 'parallel_exports_rejected_503': 0},
                 'rounds': [{'parallel_exports_rejected_503': 0}, {'parallel_exports_rejected_503': 1}, {'parallel_exports_rejected_503': 0}]}
        self.assertEqual(self.compare(value, {'median_export_ms': 10}).returncode, 1)

    def test_ci_baseline_must_use_the_same_platform(self):
        self.assertNotEqual(self.compare({'host': {'platform': 'linux'}, 'median_export_ms': 10},
            {'host': {'platform': 'win32'}, 'median_export_ms': 10}, ['--require-baseline']).returncode, 0)

    def test_required_baseline_rejects_missing_metrics(self):
        self.assertNotEqual(self.compare({'median_export_ms': 10}, {'median_export_ms': 10}, ['--require-baseline']).returncode, 0)

    def test_nan_factor_cannot_disable_regressions(self):
        self.assertNotEqual(self.compare({'median_export_ms': 20}, {'median_export_ms': 10}, ['--factor', 'nan']).returncode, 0)


if __name__ == '__main__':
    unittest.main()
