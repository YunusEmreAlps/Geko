"""Compare benchmark results with a baseline and fail on a regression.

    python scripts/bench-compare.py --current native.json --baseline baseline.json
    python scripts/bench-compare.py --current native.json --ingest ingest.json

--current takes the JSON written by bench-native.py --out and --ingest the one
written by bench-ingest-concurrency.py --out. The baseline is a native result
from the same kind of host; without one, the table lists the current figures
and nothing fails. A metric fails when it is worse than the baseline by more
than --factor (1.5 by default, since shared CI hosts vary by a third between
runs), and any export refused with HTTP 503 always fails. The Markdown table
goes to standard output, for a job summary.
"""

import argparse
import json
import math
import sys
from pathlib import Path

# Metric, unit, and whether a lower value is better.
METRICS = [
    ("median_export_ms", "ms", True),
    ("p95_export_ms", "ms", True),
    ("median_new_connection_export_ms", "ms", True),
    ("sequential_spans_per_second", "spans/s", False),
    ("parallel_spans_per_second", "spans/s", False),
    ("mean_failures_api_ms", "ms", True),
    ("mean_search_api_ms", "ms", True),
    ("mean_trace_api_ms", "ms", True),
    ("mean_failures_page_ms", "ms", True),
    ("mean_groups_page_ms", "ms", True),
    ("mean_overview_page_ms", "ms", True),
    ("post_ingest_memory_mib", "MiB", True),
    ("bytes_per_span", "bytes", True),
]


def load(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def medians(result):
    if result.get("complete") is False:
        raise ValueError("benchmark run is incomplete")
    return result.get("median_of_rounds") or result.get("after_median") or result


def compare(current, baseline, factor):
    rows, failures = [], []
    for name, unit, lower in METRICS:
        value = current.get(name)
        if value is None:
            continue
        base = baseline.get(name) if baseline else None
        verdict, change = "", ""
        if base:
            ratio = value / base if lower else base / value if value else float("inf")
            change = f"{(value - base) / base * 100:+.0f}%"
            verdict = "regressed" if ratio > factor else "ok"
            if verdict == "regressed":
                failures.append(f"{name} {value:g} {unit} against {base:g} {unit}")
        rows.append((name, f"{value:g} {unit}", f"{base:g} {unit}" if base else "", change, verdict))
    refused = current.get("parallel_exports_rejected_503", 0)
    if refused:
        failures.append(f"{refused:g} parallel exports refused with HTTP 503")
    return rows, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--current", required=True)
    parser.add_argument("--baseline")
    parser.add_argument("--require-baseline", action="store_true", help="fail if a baseline is missing instead of reporting only")
    parser.add_argument("--ingest")
    parser.add_argument("--factor", type=float, default=1.5)
    arguments = parser.parse_args()
    if not math.isfinite(arguments.factor) or arguments.factor <= 1:
        parser.error("--factor must be above 1")
    if arguments.require_baseline and (not arguments.baseline or not Path(arguments.baseline).is_file()):
        parser.error("the required baseline is missing")
    try:
        current_record = load(arguments.current)
        current = medians(current_record)
        baseline = None
        if arguments.baseline and Path(arguments.baseline).exists():
            baseline_record = load(arguments.baseline)
            baseline = medians(baseline_record)
            if arguments.require_baseline:
                current_platform = current_record.get("host", {}).get("platform")
                baseline_platform = baseline_record.get("host", {}).get("platform")
                if current_platform and baseline_platform and current_platform != baseline_platform:
                    raise ValueError("current and baseline platforms do not match")
                for label, values in [("current", current), ("baseline", baseline)]:
                    for name, _, _ in METRICS:
                        value = values.get(name)
                        if type(value) not in (int, float) or not math.isfinite(value) or value <= 0:
                            raise ValueError(f"{label} measurement is missing a valid positive {name}")
    except ValueError as error:
        parser.error(str(error))
    rows, failures = compare(current, baseline, arguments.factor)
    for index, round_ in enumerate(current_record.get("rounds", []), 1):
        refused = round_.get("parallel_exports_rejected_503", 0)
        if refused:
            failures.append(f"round {index}: {refused:g} exports refused with HTTP 503 (a median cannot hide a refusal)")
    print("### Geko benchmarks\n")
    if baseline is None:
        print("No baseline yet: these figures are informational. Commit them as the baseline to start comparing.\n")
    print(f"| Metric | Current | Baseline | Change | Within {arguments.factor:g}x |")
    print("| --- | ---: | ---: | ---: | --- |")
    for row in rows:
        print("| " + " | ".join(row) + " |")
    if arguments.ingest:
        print("\n| Exporters | Accepted spans/s | Refused with 503 |\n| ---: | ---: | ---: |")
        for level in load(arguments.ingest)["levels"]:
            print(f"| {level['connections']} | {level['accepted_spans_per_second']} | {level['rejected_503_exports']} |")
            if level["rejected_503_exports"]:
                failures.append(f"{level['rejected_503_exports']} exports refused with HTTP 503 at {level['connections']} exporters")
    if failures:
        print("\n**Regressions:** " + "; ".join(failures) + ".")
        sys.exit(1)


if __name__ == "__main__":
    main()
