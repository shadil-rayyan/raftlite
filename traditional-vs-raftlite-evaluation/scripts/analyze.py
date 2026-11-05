#!/usr/bin/env python3
"""Post-process the raw experiment CSVs into summary tables and graphs.

Reads every CSV in results/raw/, recomputes the aggregate statistics per
system / experiment / run (so the numbers are reproducible from the raw
rows alone), writes results/summary.csv, and — if matplotlib is
available — renders comparison graphs into results/graphs/.

Experiment CSV shapes handled:
  latency rows   (propagation, server_failure, partition,
                  gateway_recovery, http_performance)
                 -> run_id,gateway_id,ip,latency_ms (+ extras)
  consistency    -> run_id,gateway_id,correct,missing,incorrect,catchup_ms
  resource_usage -> run_id,ts_ns,component,cpu_delta_s,mem_bytes,disk_bytes

Usage:
    python scripts/analyze.py
"""

from __future__ import annotations

import csv
import math
import os
import statistics
import sys
from collections import defaultdict

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RAW = os.path.join(ROOT, "results", "raw")
SUMMARY = os.path.join(ROOT, "results", "summary.csv")
GRAPHS = os.path.join(ROOT, "results", "graphs")

# Experiments whose rows carry a propagation-style latency_ms column.
LATENCY_EXPERIMENTS = {
    "propagation",
    "server_failure",
    "partition",
    "gateway_recovery",
    "http_performance",
}


def percentile_nearest_rank(sorted_vals, p):
    """Nearest-rank percentile, matching the Go implementation."""
    if not sorted_vals:
        return 0.0
    rank = math.ceil(p / 100.0 * len(sorted_vals))
    rank = max(1, min(rank, len(sorted_vals)))
    return sorted_vals[rank - 1]


def stats(values):
    if not values:
        return None
    s = sorted(values)
    n = len(s)
    mean = statistics.fmean(s)
    stddev = statistics.stdev(s) if n > 1 else 0.0
    return {
        "n": n,
        "mean": round(mean, 2),
        "median": round(percentile_nearest_rank(s, 50), 2),
        "min": round(s[0], 2),
        "max": round(s[-1], 2),
        "stddev": round(stddev, 2),
        "p95": round(percentile_nearest_rank(s, 95), 2),
        "p99": round(percentile_nearest_rank(s, 99), 2),
    }


def parse_name(name):
    """(system, experiment, run_id) from a results/raw filename."""
    parts = name.split("-")
    system = parts[0]
    experiment = parts[1]
    run_id = None
    for p in parts:
        if p.startswith("run"):
            run_id = p[3:]
            break
    return system, experiment, run_id


def main():
    if not os.path.isdir(RAW):
        print(f"no raw results directory at {RAW}")
        return 1

    files = sorted(
        os.path.join(RAW, n)
        for n in os.listdir(RAW)
        # -summary.csv holds one row of key/value failure facts, not
        # measurement rows; its contents are preserved in extras/docs.
        if n.endswith(".csv") and not n.endswith("-summary.csv")
    )
    if not files:
        print("no CSVs found in results/raw/")
        return 1

    # (system, experiment, run_id, gateway) -> [latency...]
    per_gw = defaultdict(list)
    # (system, experiment, run_id) -> consistency rows
    consistency = defaultdict(list)
    # (system, experiment) -> [all latencies across runs]
    per_system = defaultdict(list)
    # (system, run_id, component) -> resource samples
    resource = defaultdict(list)
    # (system) -> [all cpu deltas / mem samples across runs]
    res_system = defaultdict(lambda: {"cpu": [], "mem": []})

    for path in files:
        name = os.path.basename(path)
        system, experiment, run_id = parse_name(name)

        if experiment in LATENCY_EXPERIMENTS:
            with open(path, newline="") as f:
                for row in csv.DictReader(f):
                    lat = float(row["latency_ms"])
                    per_gw[(system, experiment, run_id, row["gateway_id"])].append(lat)
                    per_system[(system, experiment)].append(lat)
        elif experiment == "consistency":
            with open(path, newline="") as f:
                for row in csv.DictReader(f):
                    consistency[(system, experiment, run_id)].append(row)
        elif experiment == "resource_usage":
            with open(path, newline="") as f:
                for row in csv.DictReader(f):
                    comp = row["component"]
                    resource[(system, run_id, comp)].append(row)
                    res_system[system]["cpu"].append(float(row["cpu_delta_s"]))
                    res_system[system]["mem"].append(float(row["mem_bytes"]))

    # ---- summary.csv ----
    os.makedirs(os.path.dirname(SUMMARY), exist_ok=True)
    stat_cols = ["n", "mean_ms", "median_ms", "min_ms", "max_ms", "stddev_ms", "p95_ms", "p99_ms"]
    fields = (
        ["system", "experiment", "run_id", "scope", "gateway"]
        + stat_cols
        + ["correct", "missing", "incorrect", "catchup_ms",
           "avg_cpu_cores", "peak_cpu_cores", "avg_mem_mb", "peak_mem_mb"]
    )
    blank_stats = {c: "" for c in stat_cols}
    blank_res = {"avg_cpu_cores": "", "peak_cpu_cores": "", "avg_mem_mb": "", "peak_mem_mb": ""}
    blank_verdict = {"correct": "", "missing": "", "incorrect": "", "catchup_ms": ""}

    def stat_row(system, experiment, run_id, scope, gw, st):
        out = {"system": system, "experiment": experiment, "run_id": run_id,
               "scope": scope, "gateway": gw, **blank_stats, **blank_verdict, **blank_res}
        for k, v in st.items():
            out["n" if k == "n" else f"{k}_ms"] = v
        return out

    rows_out = []
    for (system, experiment, run_id, gw), vals in sorted(per_gw.items()):
        st = stats(vals)
        if st:
            rows_out.append(stat_row(system, experiment, run_id, "run", gw, st))
    for (system, experiment, run_id), grows in sorted(consistency.items()):
        correct = sum(int(r["correct"]) for r in grows)
        missing = sum(int(r["missing"]) for r in grows)
        incorrect = sum(int(r["incorrect"]) for r in grows)
        catchups = [float(r["catchup_ms"]) for r in grows if r.get("catchup_ms")]
        rows_out.append({
            "system": system, "experiment": experiment, "run_id": run_id,
            "scope": "run", "gateway": "all",
            **blank_stats, **blank_res,
            "correct": correct, "missing": missing, "incorrect": incorrect,
            "catchup_ms": round(statistics.fmean(catchups), 2) if catchups else "",
        })
    for (system, run_id, comp), rows in sorted(resource.items()):
        cpus = [float(r["cpu_delta_s"]) for r in rows]
        mems = [float(r["mem_bytes"]) for r in rows]
        rows_out.append({
            "system": system, "experiment": "resource_usage", "run_id": run_id,
            "scope": "run", "gateway": comp,
            **blank_stats, **blank_verdict,
            "n": len(rows),
            "avg_cpu_cores": round(statistics.fmean(cpus), 3),
            "peak_cpu_cores": round(max(cpus), 3),
            "avg_mem_mb": round(statistics.fmean(mems) / 1024 / 1024, 2),
            "peak_mem_mb": round(max(mems) / 1024 / 1024, 2),
        })
    for (system, experiment), vals in sorted(per_system.items()):
        st = stats(vals)
        if st:
            rows_out.append(stat_row(system, experiment, "ALL", "all-runs", "all", st))
    for system, d in sorted(res_system.items()):
        if not d["cpu"]:
            continue
        rows_out.append({
            "system": system, "experiment": "resource_usage", "run_id": "ALL",
            "scope": "all-runs", "gateway": "all", **blank_stats, **blank_verdict,
            "n": len(d["cpu"]),
            "avg_cpu_cores": round(statistics.fmean(d["cpu"]), 3),
            "peak_cpu_cores": round(max(d["cpu"]), 3),
            "avg_mem_mb": round(statistics.fmean(d["mem"]) / 1024 / 1024, 2),
            "peak_mem_mb": round(max(d["mem"]) / 1024 / 1024, 2),
        })

    with open(SUMMARY, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fields)
        w.writeheader()
        for r in rows_out:
            w.writerow(r)
    print(f"wrote {SUMMARY} ({len(rows_out)} rows)")

    # ---- console table (per-system, all runs pooled) ----
    for experiment in sorted({e for (_s, e) in per_system}):
        print(f"\n{experiment} — all runs pooled per system (ms):")
        header = f"{'system':<12}{'n':>6}{'mean':>9}{'median':>9}{'min':>8}{'max':>9}{'stddev':>9}{'P95':>9}{'P99':>9}"
        print(header)
        print("-" * len(header))
        for (system, exp), vals in sorted(per_system.items()):
            if exp != experiment:
                continue
            st = stats(vals)
            if not st:
                continue
            print(f"{system:<12}{st['n']:>6}{st['mean']:>9}{st['median']:>9}"
                  f"{st['min']:>8}{st['max']:>9}{st['stddev']:>9}{st['p95']:>9}{st['p99']:>9}")

    if consistency:
        print("\nConsistency — totals per system:")
        totals = defaultdict(lambda: [0, 0, 0])
        for (system, _exp, _run), grows in consistency.items():
            t = totals[system]
            for r in grows:
                t[0] += int(r["correct"])
                t[1] += int(r["missing"])
                t[2] += int(r["incorrect"])
        for system, (c, m, i) in sorted(totals.items()):
            verdict = "OK" if m == 0 and i == 0 else "MISMATCH"
            print(f"  {system:<12} correct={c:<6} missing={m:<6} incorrect={i:<6} {verdict}")

    # ---- graphs (optional) ----
    try:
        import matplotlib
        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except Exception as exc:  # matplotlib not installed — skip graphs
        print(f"\nmatplotlib unavailable ({exc}); skipping graphs")
        return 0

    os.makedirs(GRAPHS, exist_ok=True)
    for experiment in sorted({e for (_s, e) in per_system}):
        systems = sorted({s for (s, e) in per_system if e == experiment})
        if not systems:
            continue
        metrics = ["mean", "median", "p95", "p99"]
        x = range(len(metrics))
        width = 0.8 / max(1, len(systems))
        fig, ax = plt.subplots(figsize=(8, 4.5))
        for i, system in enumerate(systems):
            st = stats(per_system.get((system, experiment), [])) or {}
            series = [st.get(m, 0) for m in metrics]
            ax.bar([xi + i * width for xi in x], series, width, label=system)
        ax.set_xticks([xi + width * (len(systems) - 1) / 2 for xi in x])
        ax.set_xticklabels(metrics)
        ax.set_ylabel("latency (ms)")
        ax.set_title(f"{experiment} latency by system (all runs pooled)")
        ax.legend()
        fig.tight_layout()
        out = os.path.join(GRAPHS, f"{experiment}-comparison.png")
        fig.savefig(out, dpi=140)
        plt.close(fig)
        print(f"wrote {out}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
