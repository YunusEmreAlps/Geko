"""Regression checks for balanced, repeated ingest measurements."""
import importlib.util
from pathlib import Path
import unittest
import threading
from unittest import mock

SPEC = importlib.util.spec_from_file_location("ingest_bench", Path(__file__).with_name("bench-ingest-concurrency.py"))
BENCH = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BENCH)


class IngestMeasurementTests(unittest.TestCase):
    def fake_connection(self, accepted=True, connect_error=False):
        test = self
        class Response:
            status = 200 if accepted else 503
            def read(self):
                return b'{}' if accepted else b'{"code":14,"message":"trace receiver is busy"}'
        class Connection:
            lock = threading.Lock()
            connected = 0
            seen = []
            def __init__(self, *args, **kwargs): pass
            def connect(self):
                if connect_error: raise OSError('synthetic connect failure')
                with Connection.lock:
                    Connection.connected += 1
            def request(self, method, path, payload, headers):
                with Connection.lock:
                    test.assertEqual(Connection.connected, 2, 'requests started before all clients connected')
                    Connection.seen.append(payload)
            def getresponse(self): return Response()
            def close(self): pass
        return Connection

    def test_timed_gate_waits_for_every_connected_client(self):
        connection = self.fake_connection()
        with mock.patch.object(BENCH.http.client, 'HTTPConnection', connection):
            result = BENCH.measure(2, list(range(5)), 1, {})
        self.assertEqual(list(range(5)), sorted(connection.seen))
        self.assertEqual(5, result['accepted_exports'])
        self.assertGreaterEqual(result['p95_export_ms'], 0)

    def test_busy_reason_is_counted_without_claiming_success_latency(self):
        connection = self.fake_connection(accepted=False)
        with mock.patch.object(BENCH.http.client, 'HTTPConnection', connection):
            result = BENCH.measure(2, list(range(4)), 1, {})
        self.assertEqual(4, result['rejected_503_exports'])
        self.assertEqual({'receiver_busy': 4}, result['rejection_reasons'])
        self.assertIsNone(result['p95_export_ms'])

    def test_setup_failure_aborts_readiness_gate(self):
        with mock.patch.object(BENCH.http.client, 'HTTPConnection', self.fake_connection(connect_error=True)):
            with self.assertRaisesRegex(OSError, 'synthetic connect failure'):
                BENCH.measure(2, [1, 2], 1, {})

    def test_every_payload_is_distributed_once_even_for_uneven_counts(self):
        payloads = list(range(10))
        chunks = BENCH.split_payloads(payloads, 3)
        self.assertEqual([4, 3, 3], list(map(len, chunks)))
        self.assertEqual(payloads, [value for chunk in chunks for value in chunk])

    def test_exporter_without_work_is_rejected(self):
        with self.assertRaises(ValueError):
            BENCH.split_payloads([1], 2)

    def test_rotating_order_balances_each_position(self):
        orders = [BENCH.level_order([1, 4, 16], index) for index in range(3)]
        self.assertEqual([[1, 4, 16], [4, 16, 1], [16, 1, 4]], orders)

    def test_summary_uses_median_rate_and_never_hides_refusals(self):
        samples = [dict(connections=4, exports_sent=10, accepted_exports=accepted,
                        rejected_503_exports=10-accepted, accepted_spans=128*accepted,
                        wall_seconds=1, accepted_spans_per_second=rate,
                        ms_per_accepted_export=100, p50_export_ms=10, p95_export_ms=20,
                        client_cpu_seconds=0.2)
                   for accepted, rate in [(10, 1000), (9, 900), (10, 100)]]
        result = BENCH.summarize_levels([4], samples)[0]
        self.assertEqual(900, result['accepted_spans_per_second'])
        self.assertEqual(1, result['rejected_503_exports'])
        self.assertEqual(29, result['accepted_exports'])
        self.assertEqual(100, result['accepted_spans_per_second_min'])
        self.assertEqual(1000, result['accepted_spans_per_second_max'])


if __name__ == '__main__':
    unittest.main()
