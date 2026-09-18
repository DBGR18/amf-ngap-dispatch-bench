#!/usr/bin/env python3
"""Walk a results tree and flatten every run into one CSV.

Usage:
    aggregate.py <results_root> [-o results/parsed/summary.csv]

Reads <results_root>/manifest.csv, parses each run directory listed there, and
writes one row per run. Runs whose status is not "ok" are still written, with
their status, so a failed cell in the matrix is visible rather than missing.
"""

import argparse
import csv
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from parse_run import parse  # noqa: E402

# Distribution fields are flattened as <name>_<stat>.
DIST_FIELDS = ["total_s", "reg_s", "pdu_s", "serial_us", "queue_us", "process_us", "starttime_us"]
STATS = ["mean", "p50", "p95", "p99", "max"]

SCALAR_FIELDS = [
    "completed", "completion_rate", "stalled",
    "retrans_total", "aborted_total",
    "mode", "workers", "ue_count", "storm_s", "msgs", "load_cv",
    "id_pairs", "worker_switches", "keys_multi_worker", "supi_fallbacks",
    "cpu_seconds", "cpu_peak_pct", "amf_sha256",
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("results_root")
    ap.add_argument("-o", "--out", default=None)
    args = ap.parse_args()

    manifest = os.path.join(args.results_root, "manifest.csv")
    if not os.path.exists(manifest):
        print(f"no manifest at {manifest}", file=sys.stderr)
        return 2

    out = args.out or os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "..", "..", "results", "parsed", "summary.csv")
    out = os.path.abspath(out)
    os.makedirs(os.path.dirname(out), exist_ok=True)

    fieldnames = ["run", "status"] + SCALAR_FIELDS + \
        [f"{d}_{s}" for d in DIST_FIELDS for s in STATS]

    rows = []
    with open(manifest) as f:
        for entry in csv.DictReader(f):
            rundir = entry["rundir"]
            if not os.path.isdir(rundir):
                print(f"skip missing {rundir}", file=sys.stderr)
                continue

            m = parse(rundir)
            row = {"run": entry.get("run"), "status": entry.get("status")}
            for k in SCALAR_FIELDS:
                row[k] = m.get(k)
            for d in DIST_FIELDS:
                dist = m.get(d) or {}
                for s in STATS:
                    row[f"{d}_{s}"] = dist.get(s)
            rows.append(row)

    with open(out, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fieldnames)
        w.writeheader()
        w.writerows(rows)

    print(f"{len(rows)} runs -> {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
