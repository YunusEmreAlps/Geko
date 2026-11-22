"""Compare two Geko binaries in alternating, isolated native benchmark rounds.

    python scripts/bench-paired.py --baseline bin/before --binary bin/after --out paired.json

Uses the same workload and query counts as bench-native.py. Keep other tests,
builds and Docker startup out of the measurement window. Raw rounds, binary
and fixture hashes are saved so a repeat can distinguish code from host drift.
"""

import argparse
import hashlib
import json
import os
import platform
import runpy
import statistics
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def save(path, result):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--baseline", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--batches", type=int, default=128)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    binaries = {"before": Path(args.baseline).resolve(), "after": Path(args.binary).resolve()}
    if not 1 <= args.rounds <= 10 or not 1 <= args.batches <= 1000:
        parser.error("use 1-10 rounds and 1-1000 batches")
    if any(not path.is_file() for path in binaries.values()):
        parser.error("build both binaries before measuring")
    result = {
        "date": time.strftime("%Y-%m-%d"),
        "host": {"platform": sys.platform, "description": platform.platform(), "logical_cpus": os.cpu_count()},
        "method": "Alternating native rounds; fresh database per process; 100 requests per query; medians across rounds.",
        "batches": args.batches,
        "fixture_sha256": digest(REPO / "examples/otlp-json/trace.json"),
        "harness_sha256": digest(REPO / "scripts/bench-native.py"),
        "complete": False,
        "rounds": [],
    }
    for variant, binary in binaries.items():
        result[variant + "_binary_sha256"] = digest(binary)
    harness = runpy.run_path(str(REPO / "scripts/bench-native.py"))
    output = Path(args.out)
    save(output, result)
    for index in range(args.rounds):
        order = ["before", "after"] if index % 2 == 0 else ["after", "before"]
        for variant in order:
            print(f"Round {index + 1}/{args.rounds}: {variant}", file=sys.stderr, flush=True)
            row = harness["run_round"](binaries[variant], args.batches, f"{variant}-{index + 1}")
            result["rounds"].append(row)
            save(output, result)
            print(json.dumps({key: row[key] for key in (
                "label", "median_export_ms", "mean_groups_page_ms", "parallel_spans_per_second")}),
                file=sys.stderr, flush=True)
    for variant in binaries:
        rows = [row for row in result["rounds"] if row["label"].startswith(variant + "-")]
        result[variant + "_median"] = {
            key: round(statistics.median(row[key] for row in rows), 2)
            for key, value in rows[0].items() if isinstance(value, (int, float))
        }
    result["complete"] = True
    save(output, result)
    print(json.dumps({key: result[key] for key in ("before_median", "after_median")}, indent=2))


if __name__ == "__main__":
    main()
