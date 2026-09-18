#!/usr/bin/env python3
"""Turn one run directory into a row of metrics.

Usage:
    parse_run.py <rundir> [--per-ue <out.csv>]

Reads the three artifacts a run leaves behind:

  ue_trace.csv   per-UE milestones, nanosecond, measured at the UE
  amf_trace.csv  per-NGAP-message timing, measured inside the AMF
  amf.log        RAN-UE-NGAP-ID <-> AMF-UE-NGAP-ID pairing, for worker switches
  pidstat.log    per-NF CPU samples at 1 s

and prints one JSON object of metrics on stdout.

Metric definitions (see docs/design.md; deviations from the paper are noted):

  total_s        per UE, pdu_done - reg_start. The paper's "connection
                 processing time".
  storm_s        max(pdu_done) - min(reg_start) across all UEs: how long the
                 whole storm took.
  serial_us      per message, submitted - recv. Time on the single SCTP reader
                 goroutine, before any worker can touch the message. This is
                 the Amdahl serial section the two dispatch policies differ in.
  queue_us       per message, worker_start - submitted.
  process_us     per message, handled - worker_start (includes SBI round trips,
                 which block the worker).
  starttime_us   per InitialUEMessage, handled - recv. Proxy for the paper's
                 "StartTime" phase: the AMF sends the Authentication Request
                 while handling InitialUEMessage, so this ends at roughly the
                 moment the paper's StartTime ends. DEVIATION: the paper starts
                 the clock at the gNB, this starts it when the AMF reads the
                 message off the socket.
  worker_switches  UEs whose messages moved between workers when the dispatch
                 key changed from RAN-UE-NGAP-ID to AMF-UE-NGAP-ID. Structurally
                 impossible under the supi policy; that is the point of it.
  load_cv        coefficient of variation of per-worker message counts.
"""

import argparse
import csv
import json
import os
import re
import statistics as st
import sys

# NGAP procedure codes we care about (TS 38.413).
PC_INITIAL_UE_MESSAGE = 15


def pct(values, q):
    if not values:
        return None
    s = sorted(values)
    idx = min(int(q * len(s)), len(s) - 1)
    return s[idx]


def summarise(values, scale=1.0):
    if not values:
        return None
    v = [x / scale for x in values]
    return {
        "n": len(v),
        "mean": st.mean(v),
        "p50": st.median(v),
        "p95": pct(v, 0.95),
        "p99": pct(v, 0.99),
        "max": max(v),
    }


def read_ue_trace(path):
    """-> {supi: {event: ts_ns}}"""
    ues = {}
    if not os.path.exists(path):
        return ues
    with open(path) as f:
        for row in csv.DictReader(f):
            try:
                ts = int(row["ts_ns"])
            except (KeyError, ValueError):
                continue
            ues.setdefault(row["supi"], {})[row["event"]] = ts
    return ues


def read_amf_trace(path):
    rows = []
    if not os.path.exists(path):
        return rows
    with open(path) as f:
        for row in csv.DictReader(f):
            try:
                rows.append({
                    "recv": int(row["recv_ns"]),
                    "submitted": int(row["submitted_ns"]) if row["submitted_ns"] else None,
                    "worker_start": int(row["worker_start_ns"]) if row["worker_start_ns"] else None,
                    "handled": int(row["handled_ns"]) if row["handled_ns"] else None,
                    "worker": int(row["worker_id"]),
                    "key": int(row["key"]),
                    "pc": int(row["procedure_code"]),
                    "fallback": row.get("fallback") == "true",
                })
            except (KeyError, ValueError):
                continue
    return rows


# The AMF tags UE-scoped log lines with both identifiers, e.g.
#   [amf_ue_ngap_id:RU:12,AU:7(NGPP)]
RU_AU_RE = re.compile(r"RU:(\d+),AU:(\d+)")

# NAS retransmissions, the paper's Table III. free5gc logs each individual
# retransmission in lower case and the final give-up in upper case, e.g.
#   internal/gmm/message/send.go:611  "T3550 expires, retransmit ... (retry: 1)"
#   internal/gmm/message/send.go:615  "T3550 Expires 4 times, abort ..."
# Counting only the second would undercount badly: it fires once per UE no
# matter how many retransmissions preceded it.
RETRANS_RE = re.compile(r"(T\d{4}) expires, retransmit")
ABORT_RE = re.compile(r"(T\d{4}) Expires \d+ times, abort")


def count_retransmissions(path):
    """-> {"retrans_total", "aborted_total", "retrans_by_timer", "aborted_by_timer"}"""
    by_timer, aborts = {}, {}
    if os.path.exists(path):
        with open(path, errors="replace") as f:
            for line in f:
                m = RETRANS_RE.search(line)
                if m:
                    by_timer[m.group(1)] = by_timer.get(m.group(1), 0) + 1
                    continue
                m = ABORT_RE.search(line)
                if m:
                    aborts[m.group(1)] = aborts.get(m.group(1), 0) + 1
    return {
        "retrans_total": sum(by_timer.values()),
        "aborted_total": sum(aborts.values()),
        "retrans_by_timer": dict(sorted(by_timer.items())),
        "aborted_by_timer": dict(sorted(aborts.items())),
    }


def read_id_pairs(path):
    """-> set of (ran_ue_ngap_id, amf_ue_ngap_id)"""
    pairs = set()
    if not os.path.exists(path):
        return pairs
    with open(path, errors="replace") as f:
        for line in f:
            m = RU_AU_RE.search(line)
            if m:
                ru, au = int(m.group(1)), int(m.group(2))
                # AU is only meaningful once assigned; free5gc prints -1 before.
                if au >= 0:
                    pairs.add((ru, au))
    return pairs


def read_pidstat(path):
    """-> (per-process mean %CPU, aggregate CPU-seconds, peak aggregate %CPU)

    pidstat -u -h prints one block of rows per sample instant. Summing %CPU
    within a timestamp gives the aggregate utilisation U(t) the paper reports;
    integrating U(t)/100 over the samples gives CPU-seconds.
    """
    if not os.path.exists(path):
        return {}, None, None

    per_proc = {}
    per_instant = {}
    with open(path, errors="replace") as f:
        for line in f:
            parts = line.split()
            # e.g. "10:34:44 PM 0 216234 0.00 0.00 0.00 0.00 0.00 0 upf"
            if len(parts) < 10 or parts[0].startswith("#") or parts[0] == "Linux":
                continue
            try:
                ts = parts[0] + " " + parts[1]
                cpu = float(parts[8])
                cmd = parts[-1]
            except (ValueError, IndexError):
                continue
            per_proc.setdefault(cmd, []).append(cpu)
            per_instant[ts] = per_instant.get(ts, 0.0) + cpu

    if not per_instant:
        return {}, None, None

    means = {cmd: st.mean(v) for cmd, v in per_proc.items()}
    # Samples are 1 s apart, so each instant contributes U/100 core-seconds.
    cpu_seconds = sum(per_instant.values()) / 100.0
    return means, cpu_seconds, max(per_instant.values())


def parse(rundir, per_ue_out=None):
    ue = read_ue_trace(os.path.join(rundir, "ue_trace.csv"))
    amf = read_amf_trace(os.path.join(rundir, "amf_trace.csv"))
    pairs = read_id_pairs(os.path.join(rundir, "amf.log"))
    cpu_means, cpu_seconds, cpu_peak = read_pidstat(os.path.join(rundir, "pidstat.log"))

    meta = {}
    prov = os.path.join(rundir, "provenance.txt")
    if os.path.exists(prov):
        for line in open(prov):
            # One key=value per line, but tolerate several on one line so that
            # runs recorded by an older run_core.sh still parse.
            for token in line.strip().split():
                if "=" in token:
                    k, v = token.split("=", 1)
                    meta[k] = v

    # --- UE-side latencies -------------------------------------------------
    totals, regs, pdus = [], [], []
    per_ue_rows = []
    for supi, ev in ue.items():
        row = {"supi": supi}
        if "reg_start" in ev and "pdu_done" in ev:
            t = ev["pdu_done"] - ev["reg_start"]
            totals.append(t)
            row["total_s"] = t / 1e9
        if "reg_start" in ev and "reg_done" in ev:
            r = ev["reg_done"] - ev["reg_start"]
            regs.append(r)
            row["reg_s"] = r / 1e9
        if "pdu_start" in ev and "pdu_done" in ev:
            p = ev["pdu_done"] - ev["pdu_start"]
            pdus.append(p)
            row["pdu_s"] = p / 1e9
        per_ue_rows.append(row)

    storm_s = None
    starts = [e["reg_start"] for e in ue.values() if "reg_start" in e]
    ends = [e["pdu_done"] for e in ue.values() if "pdu_done" in e]
    if starts and ends:
        storm_s = (max(ends) - min(starts)) / 1e9

    # --- AMF-side message timing -------------------------------------------
    complete = [r for r in amf if r["handled"] and r["submitted"] and r["worker_start"]]
    serial = [r["submitted"] - r["recv"] for r in complete]
    queue = [r["worker_start"] - r["submitted"] for r in complete]
    process = [r["handled"] - r["worker_start"] for r in complete]

    initial = [r for r in complete if r["pc"] == PC_INITIAL_UE_MESSAGE]
    starttime = [r["handled"] - r["recv"] for r in initial]

    per_worker = {}
    for r in complete:
        per_worker[r["worker"]] = per_worker.get(r["worker"], 0) + 1
    load_cv = None
    if len(per_worker) > 1:
        counts = list(per_worker.values())
        load_cv = st.pstdev(counts) / st.mean(counts) if st.mean(counts) else None

    # --- worker switches ----------------------------------------------------
    # A switch happens when a UE's messages move between workers partway
    # through its procedure, costing cache locality and ordering headroom.
    #
    # Under the hash policy the dispatch key changes from RAN-UE-NGAP-ID to
    # AMF-UE-NGAP-ID, so a switch happens exactly when those two hash to
    # different workers - computable from the ID pairs in the log.
    #
    # Under the supi policy the key is the subscriber identity and never
    # changes, so a switch is structurally impossible. The ID-pair formula does
    # NOT apply there (the NGAP IDs are not the dispatch key), and applying it
    # anyway would invent switches that never happened.
    workers = int(meta.get("workers", 0) or 0)
    mode = meta.get("mode")
    switches = None
    if mode == "supi":
        switches = 0
    elif workers > 1 and pairs:
        switches = sum(1 for ru, au in pairs if (ru % workers) != (au % workers))

    # Invariant check, independent of policy: one dispatch key must always
    # reach one worker, or per-UE message ordering is broken. Must be 0.
    key_workers = {}
    for r in complete:
        key_workers.setdefault(r["key"], set()).add(r["worker"])
    keys_multi_worker = sum(1 for w in key_workers.values() if len(w) > 1)

    fallbacks = sum(1 for r in amf if r["fallback"])
    retrans = count_retransmissions(os.path.join(rundir, "amf.log"))

    # How many UEs the configuration could actually serve. A collapsed arm
    # shows up here, not only in the latency distribution - and a latency mean
    # computed over the survivors of a collapse is not comparable to one from a
    # run where everybody finished, so this must be read alongside it.
    completed = sum(1 for e in ue.values() if "pdu_done" in e)
    stalled = None
    timing = os.path.join(rundir, "ran_timing.txt")
    if os.path.exists(timing):
        for line in open(timing):
            if line.startswith("ue_stalled="):
                stalled = line.strip().split("=", 1)[1] == "1"

    if per_ue_out:
        with open(per_ue_out, "w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=["supi", "total_s", "reg_s", "pdu_s"])
            w.writeheader()
            for row in sorted(per_ue_rows, key=lambda x: x["supi"]):
                w.writerow(row)

    return {
        "rundir": rundir,
        "mode": mode,
        "workers": workers,
        "ue_count": len(ue),
        "completed": completed,
        "completion_rate": (completed / len(ue)) if ue else None,
        "stalled": stalled,
        "storm_s": storm_s,
        "total_s": summarise(totals, 1e9),
        "reg_s": summarise(regs, 1e9),
        "pdu_s": summarise(pdus, 1e9),
        "serial_us": summarise(serial, 1e3),
        "queue_us": summarise(queue, 1e3),
        "process_us": summarise(process, 1e3),
        "starttime_us": summarise(starttime, 1e3),
        "msgs": len(complete),
        "per_worker": dict(sorted(per_worker.items())),
        "load_cv": load_cv,
        "id_pairs": len(pairs),
        "worker_switches": switches,
        "keys_multi_worker": keys_multi_worker,
        "supi_fallbacks": fallbacks,
        "cpu_seconds": cpu_seconds,
        "cpu_peak_pct": cpu_peak,
        "cpu_mean_by_nf": {k: round(v, 2) for k, v in sorted(cpu_means.items())},
        **retrans,
        "amf_sha256": meta.get("amf_sha256"),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("rundir")
    ap.add_argument("--per-ue")
    args = ap.parse_args()

    if not os.path.isdir(args.rundir):
        print(f"no such run directory: {args.rundir}", file=sys.stderr)
        return 2

    print(json.dumps(parse(args.rundir, args.per_ue), indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
