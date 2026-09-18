#!/usr/bin/env python3
"""Turn the run summary into the comparison tables and figures.

Usage:
    analyze.py [results/parsed/summary.csv] [-o docs/results_generated.md]
               [--figures analysis/figures]

Aggregates the 10 runs of each cell (policy x workers x UE count) and answers
the question the benchmark exists for: how the dispatch point and the dispatch
key show up in the StartTime phase and the phases after it.

Tables always print. Figures are written only if matplotlib is importable, so
the analysis is useful on a machine without it.
"""

import argparse
import csv
import os
import statistics as st
import sys
from collections import defaultdict

# Cells are keyed by (mode, workers, ue_count).
MODES = ["hash", "supi"]


def load(path):
    rows = []
    with open(path) as f:
        for r in csv.DictReader(f):
            def num(k):
                v = r.get(k, "")
                if v in ("", "None", None):
                    return None
                try:
                    return float(v)
                except ValueError:
                    return None

            rows.append({
                "mode": r.get("mode"),
                "workers": int(float(r["workers"])) if r.get("workers") else None,
                "ues": int(float(r["ue_count"])) if r.get("ue_count") else None,
                "status": r.get("status"),
                "completed": num("completed"),
                "completion_rate": num("completion_rate"),
                "storm_s": num("storm_s"),
                "total_mean": num("total_s_mean"),
                "total_p95": num("total_s_p95"),
                "serial_mean": num("serial_us_mean"),
                "serial_p95": num("serial_us_p95"),
                "queue_mean": num("queue_us_mean"),
                "process_mean": num("process_us_mean"),
                "starttime_mean": num("starttime_us_mean"),
                "starttime_p95": num("starttime_us_p95"),
                "switches": num("worker_switches"),
                "keys_multi_worker": num("keys_multi_worker"),
                "fallbacks": num("supi_fallbacks"),
                "retrans": num("retrans_total"),
                "aborted": num("aborted_total"),
                "cpu_seconds": num("cpu_seconds"),
                "load_cv": num("load_cv"),
            })
    return rows


def cells(rows):
    out = defaultdict(list)
    for r in rows:
        if r["mode"] and r["workers"] is not None and r["ues"] is not None:
            out[(r["mode"], r["workers"], r["ues"])].append(r)
    return out


def agg(runs, field):
    vals = [r[field] for r in runs if r[field] is not None]
    if not vals:
        return None, None
    return st.mean(vals), (st.stdev(vals) if len(vals) > 1 else 0.0)


def fmt(v, digits=2):
    return "-" if v is None else f"{v:.{digits}f}"


def table_completion(c, ue_counts, worker_counts, out):
    out.append("## Completion rate\n")
    out.append("Read this before any latency number. A cell below 1.00 collapsed:")
    out.append("NAS timers expired and the AMF abandoned UEs, so its latency")
    out.append("distribution describes the survivors, not the workload.\n")
    out.append("| UEs | workers | hash | supi |")
    out.append("|---:|---:|---:|---:|")
    for u in ue_counts:
        for w in worker_counts:
            vals = []
            for m in MODES:
                mean, _ = agg(c.get((m, w, u), []), "completion_rate")
                vals.append(fmt(mean, 2))
            out.append(f"| {u} | {w} | {vals[0]} | {vals[1]} |")
    out.append("")


def table_metric(c, ue_counts, worker_counts, field, title, unit, note, out, digits=2):
    out.append(f"## {title}\n")
    if note:
        out.append(note + "\n")
    out.append(f"| UEs | workers | hash ({unit}) | supi ({unit}) | supi - hash | supi / hash |")
    out.append("|---:|---:|---:|---:|---:|---:|")
    for u in ue_counts:
        for w in worker_counts:
            h, hsd = agg(c.get(("hash", w, u), []), field)
            s, ssd = agg(c.get(("supi", w, u), []), field)
            diff = ratio = None
            if h is not None and s is not None:
                diff = s - h
                ratio = (s / h) if h else None
            hs = f"{fmt(h, digits)} ±{fmt(hsd, digits)}" if h is not None else "-"
            ss = f"{fmt(s, digits)} ±{fmt(ssd, digits)}" if s is not None else "-"
            out.append(f"| {u} | {w} | {hs} | {ss} | {fmt(diff, digits)} | {fmt(ratio, 3)} |")
    out.append("")


def table_mechanism(c, ue_counts, worker_counts, out):
    out.append("## Mechanism differences\n")
    out.append("`switches` counts UEs whose messages moved between workers")
    out.append("mid-procedure - structurally impossible under `supi`.")
    out.append("`keys_multi_worker` is the invariant check: one dispatch key must")
    out.append("reach exactly one worker, so it must be 0 everywhere.")
    out.append("`fallbacks` counts messages where `supi` could not resolve the")
    out.append("subscriber key; a large value would mean the arms were not")
    out.append("actually running different policies.\n")
    out.append("| UEs | workers | hash switches | supi switches | keys_multi_worker | supi fallbacks | hash load CV | supi load CV |")
    out.append("|---:|---:|---:|---:|---:|---:|---:|---:|")
    for u in ue_counts:
        for w in worker_counts:
            hsw, _ = agg(c.get(("hash", w, u), []), "switches")
            ssw, _ = agg(c.get(("supi", w, u), []), "switches")
            kmw = max(
                [x for x in
                 [agg(c.get((m, w, u), []), "keys_multi_worker")[0] for m in MODES]
                 if x is not None] or [0])
            fb, _ = agg(c.get(("supi", w, u), []), "fallbacks")
            hcv, _ = agg(c.get(("hash", w, u), []), "load_cv")
            scv, _ = agg(c.get(("supi", w, u), []), "load_cv")
            out.append(
                f"| {u} | {w} | {fmt(hsw, 1)} | {fmt(ssw, 1)} | {fmt(kmw, 0)} | "
                f"{fmt(fb, 1)} | {fmt(hcv, 4)} | {fmt(scv, 4)} |")
    out.append("")


def table_phase_split(c, ue_counts, worker_counts, out):
    out.append("## Where the time goes\n")
    out.append("Per NGAP message, averaged over the runs of each cell. The serial")
    out.append("section is the part no number of workers can shrink; the two")
    out.append("policies differ there by one NAS decode.\n")
    out.append("| UEs | workers | mode | serial (us) | queue (us) | process (us) | serial share |")
    out.append("|---:|---:|:--|---:|---:|---:|---:|")
    for u in ue_counts:
        for w in worker_counts:
            for m in MODES:
                runs = c.get((m, w, u), [])
                se, _ = agg(runs, "serial_mean")
                q, _ = agg(runs, "queue_mean")
                p, _ = agg(runs, "process_mean")
                share = None
                if None not in (se, q, p) and (se + q + p):
                    share = se / (se + q + p) * 100
                out.append(
                    f"| {u} | {w} | {m} | {fmt(se, 1)} | {fmt(q, 1)} | {fmt(p, 1)} | "
                    f"{fmt(share, 4)}% |")
    out.append("")


def figures(c, ue_counts, worker_counts, outdir):
    try:
        import matplotlib
        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError:
        return ["(figures skipped: matplotlib not installed)"]

    os.makedirs(outdir, exist_ok=True)
    written = []
    colours = {"hash": "#1f77b4", "supi": "#d62728"}

    # Storm completion vs worker count, one line per mode, one panel per UE count.
    fig, axes = plt.subplots(1, len(ue_counts), figsize=(4.2 * len(ue_counts), 3.6),
                             squeeze=False)
    for ax, u in zip(axes[0], ue_counts):
        for m in MODES:
            xs, ys, es = [], [], []
            for w in worker_counts:
                mean, sd = agg(c.get((m, w, u), []), "storm_s")
                if mean is not None:
                    xs.append(w)
                    ys.append(mean)
                    es.append(sd or 0)
            if xs:
                ax.errorbar(xs, ys, yerr=es, marker="o", label=m, color=colours[m],
                            capsize=3)
        ax.set_title(f"{u} UEs")
        ax.set_xlabel("workers")
        ax.set_xscale("log", base=2)
        ax.set_xticks(worker_counts)
        ax.set_xticklabels([str(w) for w in worker_counts])
        ax.grid(alpha=0.3)
    axes[0][0].set_ylabel("storm completion (s)")
    axes[0][0].legend()
    fig.tight_layout()
    p = os.path.join(outdir, "storm_completion.png")
    fig.savefig(p, dpi=150)
    plt.close(fig)
    written.append(p)

    # Serial section: the mechanism cost, isolated.
    fig, ax = plt.subplots(figsize=(6, 3.6))
    width = 0.35
    labels, hvals, svals = [], [], []
    for u in ue_counts:
        for w in worker_counts:
            labels.append(f"{u}/{w}")
            hvals.append(agg(c.get(("hash", w, u), []), "serial_mean")[0] or 0)
            svals.append(agg(c.get(("supi", w, u), []), "serial_mean")[0] or 0)
    idx = range(len(labels))
    ax.bar([i - width / 2 for i in idx], hvals, width, label="hash", color=colours["hash"])
    ax.bar([i + width / 2 for i in idx], svals, width, label="supi", color=colours["supi"])
    ax.set_xticks(list(idx))
    ax.set_xticklabels(labels, rotation=90, fontsize=7)
    ax.set_ylabel("serial section per message (us)")
    ax.set_xlabel("UEs / workers")
    ax.legend()
    ax.grid(alpha=0.3, axis="y")
    fig.tight_layout()
    p = os.path.join(outdir, "serial_section.png")
    fig.savefig(p, dpi=150)
    plt.close(fig)
    written.append(p)

    # Completion rate: where each policy collapses.
    fig, axes = plt.subplots(1, len(ue_counts), figsize=(4.2 * len(ue_counts), 3.2),
                             squeeze=False)
    for ax, u in zip(axes[0], ue_counts):
        for m in MODES:
            xs = [w for w in worker_counts if c.get((m, w, u))]
            ys = [agg(c[(m, w, u)], "completion_rate")[0] for w in xs]
            if xs:
                ax.plot(xs, ys, marker="s", label=m, color=colours[m])
        ax.set_ylim(0, 1.05)
        ax.set_title(f"{u} UEs")
        ax.set_xlabel("workers")
        ax.set_xscale("log", base=2)
        ax.set_xticks(worker_counts)
        ax.set_xticklabels([str(w) for w in worker_counts])
        ax.grid(alpha=0.3)
    axes[0][0].set_ylabel("completion rate")
    axes[0][0].legend()
    fig.tight_layout()
    p = os.path.join(outdir, "completion_rate.png")
    fig.savefig(p, dpi=150)
    plt.close(fig)
    written.append(p)

    return written


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("summary", nargs="?", default="results/parsed/summary.csv")
    ap.add_argument("-o", "--out", default="docs/results_generated.md")
    ap.add_argument("--figures", default="analysis/figures")
    args = ap.parse_args()

    if not os.path.exists(args.summary):
        print(f"no summary at {args.summary}; run bench/collect/aggregate.py first",
              file=sys.stderr)
        return 2

    rows = load(args.summary)
    c = cells(rows)
    ue_counts = sorted({k[2] for k in c})
    worker_counts = sorted({k[1] for k in c})

    out = [
        "# Generated results",
        "",
        f"From `{args.summary}`: {len(rows)} runs, "
        f"{len(c)} cells, {len(ue_counts)} UE counts, {len(worker_counts)} worker counts.",
        "",
        "Every number is the mean over that cell's runs, +/- the standard",
        "deviation across them. Definitions and the deviations from the paper are",
        "in [design.md](design.md).",
        "",
    ]

    table_completion(c, ue_counts, worker_counts, out)
    table_metric(c, ue_counts, worker_counts, "storm_s",
                 "Storm completion time", "s",
                 "Wall clock from the first Registration Request to the last "
                 "completed PDU session.", out, 2)
    table_metric(c, ue_counts, worker_counts, "total_mean",
                 "Per-UE connection processing time", "s",
                 "The paper's headline metric: Registration Request to PDU session "
                 "complete, per UE.", out, 3)
    table_metric(c, ue_counts, worker_counts, "starttime_mean",
                 "StartTime phase", "us",
                 "InitialUEMessage arrival to the end of its handling, which is "
                 "where the AMF has just sent the Authentication Request.", out, 1)
    table_metric(c, ue_counts, worker_counts, "serial_mean",
                 "Serial section per message", "us",
                 "Time on the single SCTP reader goroutine. This is where the two "
                 "policies actually differ: `supi` decodes NAS here, `hash` does "
                 "not.", out, 2)
    table_metric(c, ue_counts, worker_counts, "retrans",
                 "NAS retransmissions", "count",
                 "The paper's Table III. Rises sharply once a configuration cannot "
                 "keep up.", out, 1)
    table_metric(c, ue_counts, worker_counts, "cpu_seconds",
                 "CPU-seconds", "core-s",
                 "Total computational effort across all NFs, the paper's Table II.",
                 out, 2)
    table_phase_split(c, ue_counts, worker_counts, out)
    table_mechanism(c, ue_counts, worker_counts, out)

    figs = figures(c, ue_counts, worker_counts, args.figures)
    out.append("## Figures\n")
    for f in figs:
        if f.startswith("("):
            out.append(f)
        else:
            rel = os.path.relpath(f, os.path.dirname(os.path.abspath(args.out)))
            out.append(f"![{os.path.basename(f)}]({rel})")
    out.append("")

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w") as f:
        f.write("\n".join(out))
    print(f"{len(rows)} runs -> {args.out}")
    for f in figs:
        print(f"  {f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
