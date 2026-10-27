#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tabulate the calculated-metrics cost gate (hack/calc-cost-gate.sh) and give its verdict.

Input: results.jsonl, one JSON object per `run`. Besides the fields the harness writes itself
(arm, run, formulas, raws, readings_per_s, ok, rows_ok, ...), a row carries these metrics under
--prom / --metric. Names are the contract; a missing one is NO DATA, and NO DATA never passes.

  throughput          readings/s device-management resolved over the window   (higher is better)
  p99_ms              device-management resolve p99                           (lower is better)
  em_cpu_per_row      event-management CPU cores per 1000 stored rows/s
  persist_p99_ms      event-management persist p99
  lag_growing         1 if any resolved-event consumer's lag grew over the window, else 0
  dead_letters        dead-lettered messages over the window
  dm_cpu_per_reading  device-management CPU cores per 1000 readings/s         (reported)
  em_rows_per_s, hypertable_bytes_per_h, ds_upserts_per_s, stream_bytes_per_s (reported, C4)

Criteria (arms are compared on the median of their runs):
  C0      noise between runs and, when a B0 arm (the same build, no calculated profile) exists,
          throughput and p99 within +-1% of it
  C1      throughput cost vs C0 under 3%
  C4      reported only
  C8-eq   stored rows/s within 5% of C0's (else the arm does not test what it claims), then
          em_cpu_per_row and persist_p99_ms within +-10% of C0, lag_growing 0, dead_letters 0
  C4-eq   the fallback: the same checks as C8-eq at 4 formulas
  C8-knee reported: the lowest rate at which lag_growing is 1

Any row with ok=false, or an arm whose rows are not rows_ok=true (the stored-row count was not
verified against accepted x (raws+N)), fails that arm outright.

Verdict: default 8 if C0, C1 and C8-eq pass; else 4 if C0, C1 and C4-eq pass; else 0 (re-plan).
"""
import json
import statistics
import sys

PASS, FAIL, NODATA, REPORT = "PASS", "FAIL", "NO DATA", "REPORT"


def load(path):
    rows = []
    with open(path, encoding="utf-8") as f:
        for i, line in enumerate(f, 1):
            line = line.strip()
            if line:
                try:
                    rows.append(json.loads(line))
                except ValueError as e:
                    sys.exit("tabulate: %s line %d is not JSON: %s" % (path, i, e))
    return rows


def med(rows, key):
    vals = [r[key] for r in rows if isinstance(r.get(key), (int, float))]
    return statistics.median(vals) if len(vals) == len(rows) and vals else None


def rel(a, b):
    """(a - b) / b, or None when it cannot be said."""
    if a is None or b is None or b == 0:
        return None
    return (a - b) / b


def quality(rows):
    """FAIL with a reason when the arm's rows cannot be believed, else None."""
    for r in rows:
        if r.get("ok") is not True:
            return "run %s failed its checks: %s" % (r.get("run"), r.get("why") or "ok=false")
        if r.get("rows_ok") is not True:
            return "run %s: stored rows were not verified against accepted x (raws+N) (rows_ok=%s)" % (
                r.get("run"), r.get("rows_ok"))
    return None


def rows_per_s(rows):
    vals = [r["readings_per_s"] * (r["raws"] + r["formulas"]) for r in rows
            if isinstance(r.get("readings_per_s"), (int, float))]
    return statistics.median(vals) if len(vals) == len(rows) and vals else None


def within(name, got, base, tol, out, lower_is_better=None):
    d = rel(got, base)
    if d is None:
        out.append((NODATA, "%s: missing for the arm or its baseline" % name))
        return
    ok = abs(d) <= tol
    out.append((PASS if ok else FAIL, "%s %+.2f%% (limit +-%.0f%%)" % (name, d * 100, tol * 100)))


def judge(by_arm):
    """Return {arm: (status, [(status, text)...])}, and the verdict string."""
    res = {}
    c0 = by_arm.get("C0", [])

    def settle(arm, checks, extra=None):
        rows = by_arm.get(arm)
        if not rows:
            res[arm] = (NODATA, [(NODATA, "no runs")])
            return
        q = quality(rows)
        if q:
            res[arm] = (FAIL, [(FAIL, q)])
            return
        statuses = [s for s, _ in checks]
        if FAIL in statuses:
            st = FAIL
        elif NODATA in statuses or not checks:
            st = NODATA if NODATA in statuses else REPORT
        elif all(s == REPORT for s in statuses):
            st = REPORT
        else:
            st = PASS
        res[arm] = (st, checks)

    # C0
    checks = []
    if len(c0) >= 2:
        t = [r.get("throughput") for r in c0]
        if all(isinstance(x, (int, float)) for x in t):
            d = (max(t) - min(t)) / max(t) if max(t) else None
            checks.append((REPORT, "run-to-run throughput spread %.2f%%" % (d * 100) if d is not None else "spread n/a"))
        else:
            checks.append((NODATA, "throughput missing"))
    else:
        checks.append((NODATA, "needs two runs for the noise floor"))
    b0 = by_arm.get("B0")
    if b0:
        within("throughput vs B0", med(c0, "throughput"), med(b0, "throughput"), 0.01, checks)
        within("p99_ms vs B0", med(c0, "p99_ms"), med(b0, "p99_ms"), 0.01, checks)
    else:
        checks.append((NODATA, "no B0 arm (same build, no calculated profile): the +-1% claim is unmeasured"))
    settle("C0", checks)

    # C1
    checks = []
    d = rel(med(by_arm.get("C1", []), "throughput"), med(c0, "throughput"))
    if d is None:
        checks.append((NODATA, "throughput missing for C1 or C0"))
    else:
        checks.append((PASS if -d < 0.03 else FAIL, "throughput cost %.2f%% (limit < 3%%)" % (-d * 100)))
    settle("C1", checks)

    # C4
    keys = ("em_rows_per_s", "hypertable_bytes_per_h", "ds_upserts_per_s", "stream_bytes_per_s",
            "dm_cpu_per_reading", "em_cpu_per_row")
    c4 = by_arm.get("C4", [])
    settle("C4", [(REPORT, "%s %s" % (k, med(c4, k))) for k in keys if med(c4, k) is not None]
           or [(NODATA, "no storage metrics supplied")])

    # C8-eq / C4-eq
    for arm in ("C8-eq", "C4-eq"):
        rows = by_arm.get(arm, [])
        checks = []
        a, b = rows_per_s(rows), rows_per_s(c0)
        d = rel(a, b)
        if d is None:
            checks.append((NODATA, "readings_per_s missing, so rows/s cannot be compared with C0"))
        else:
            checks.append((PASS if abs(d) <= 0.05 else FAIL,
                           "stored rows/s %+.1f%% vs C0 (must be within +-5%% or the arm is not row-equal)" % (d * 100)))
        within("em_cpu_per_row", med(rows, "em_cpu_per_row"), med(c0, "em_cpu_per_row"), 0.10, checks)
        within("persist_p99_ms", med(rows, "persist_p99_ms"), med(c0, "persist_p99_ms"), 0.10, checks)
        for k in ("lag_growing", "dead_letters"):
            v = med(rows, k)
            checks.append((NODATA, "%s missing" % k) if v is None
                          else (PASS if v == 0 else FAIL, "%s = %s (must be 0)" % (k, v)))
        dm = med(rows, "dm_cpu_per_reading")
        if dm is not None:
            checks.append((REPORT, "dm_cpu_per_reading %s" % dm))
        settle(arm, checks)

    # knee
    knee = sorted(by_arm.get("C8-knee", []), key=lambda r: r.get("readings_per_s") or 0)
    checks = []
    if not knee:
        checks.append((NODATA, "no runs"))
    else:
        grew = [r["readings_per_s"] for r in knee if r.get("lag_growing") == 1]
        checks.append((REPORT, "lag first grows at %s readings/s (x%d rows)" % (min(grew), 8 + knee[0]["raws"])
                       if grew else "lag did not grow at any rate tried: the knee is above %s readings/s" %
                       knee[-1].get("readings_per_s")))
    settle("C8-knee", checks)

    g = lambda a: res.get(a, (NODATA, []))[0]
    if g("C0") == PASS and g("C1") == PASS and g("C8-eq") == PASS:
        verdict = "default 8"
    elif g("C0") == PASS and g("C1") == PASS and g("C4-eq") == PASS:
        verdict = "default 4 (C8-eq did not pass; record the cause)"
    else:
        verdict = "default stays 0: re-plan G21 (this is a result, not a failure to hand-wave)"
    return res, verdict


def render(by_arm):
    res, verdict = judge(by_arm)
    lines = []
    for arm in ("B0", "C0", "C1", "C4", "C8-eq", "C4-eq", "C8-knee"):
        if arm == "B0":
            continue
        if arm not in res or (res[arm][0] == NODATA and arm not in by_arm):
            continue
        st, checks = res[arm]
        lines.append("%-8s %-8s runs=%d" % (arm, st, len(by_arm.get(arm, []))))
        for s, text in checks:
            lines.append("           %-8s %s" % (s, text))
    lines.append("")
    lines.append("VERDICT: " + verdict)
    return "\n".join(lines), verdict


def group(rows):
    by = {}
    for r in rows:
        by.setdefault(r["arm"], []).append(r)
    return by


def self_test():
    def row(arm, run, n, rate, **kw):
        r = {"arm": arm, "run": run, "formulas": n, "raws": 1, "readings_per_s": rate,
             "ok": True, "rows_ok": True, "throughput": rate, "p99_ms": 10.0,
             "em_cpu_per_row": 1.0, "persist_p99_ms": 20.0, "lag_growing": 0, "dead_letters": 0}
        r.update(kw)
        return r

    base = [row("C0", 1, 0, 10000), row("C0", 2, 0, 10000, throughput=10050),
            row("B0", 1, 0, 10000), row("C1", 1, 1, 10000, throughput=9900),
            row("C8-eq", 1, 8, 10000 / 9)]
    failures = []

    def expect(name, rows, want_arm, want_status, want_verdict=None):
        res, verdict = judge(group(rows))
        got = res.get(want_arm, (None,))[0]
        if got != want_status:
            failures.append("%s: %s is %s, want %s" % (name, want_arm, got, want_status))
        if want_verdict and not verdict.startswith(want_verdict):
            failures.append("%s: verdict %r, want %r" % (name, verdict, want_verdict))

    expect("good", base, "C8-eq", PASS, "default 8")
    # twins: each must flip exactly the thing it breaks
    expect("C1 costs 5%", [r for r in base if r["arm"] != "C1"] + [row("C1", 1, 1, 10000, throughput=9500)],
           "C1", FAIL, "default stays 0")
    expect("C8-eq not row-equal", [r for r in base if r["arm"] != "C8-eq"] + [row("C8-eq", 1, 8, 5000)],
           "C8-eq", FAIL)
    expect("C8-eq lag grows", [r for r in base if r["arm"] != "C8-eq"] + [row("C8-eq", 1, 8, 10000 / 9, lag_growing=1)],
           "C8-eq", FAIL)
    expect("C8-eq em cost +20%", [r for r in base if r["arm"] != "C8-eq"] + [row("C8-eq", 1, 8, 10000 / 9, em_cpu_per_row=1.2)],
           "C8-eq", FAIL)
    expect("unverified rows", [r for r in base if r["arm"] != "C8-eq"] + [row("C8-eq", 1, 8, 10000 / 9, rows_ok=None)],
           "C8-eq", FAIL)
    expect("a failed harness check", [r for r in base if r["arm"] != "C1"] + [row("C1", 1, 1, 10000, ok=False, why="x")],
           "C1", FAIL)
    expect("no B0 leaves C0 unproven", [r for r in base if r["arm"] != "B0"], "C0", NODATA, "default stays 0")
    expect("missing metric is not a pass", [r for r in base if r["arm"] != "C8-eq"] +
           [{k: v for k, v in row("C8-eq", 1, 8, 10000 / 9).items() if k != "persist_p99_ms"}], "C8-eq", NODATA)
    fallback = [r for r in base if r["arm"] != "C8-eq"] + [row("C8-eq", 1, 8, 10000 / 9, lag_growing=1),
                                                            row("C4-eq", 1, 4, 2000)]
    expect("fallback to 4", fallback, "C4-eq", PASS, "default 4")
    if failures:
        for f in failures:
            print("tabulate self-test: " + f, file=sys.stderr)
        return 1
    print("tabulate self-test: ok")
    return 0


def main(argv):
    if len(argv) == 2 and argv[1] == "--self-test":
        return self_test()
    if len(argv) != 2:
        print("usage: tabulate.py results.jsonl | --self-test", file=sys.stderr)
        return 2
    text, verdict = render(group(load(argv[1])))
    print(text)
    return 0 if not verdict.startswith("default stays 0") else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
