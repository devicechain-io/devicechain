#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tabulate the calculated-metrics cost gate (hack/calc-cost-gate.sh) and give its verdict.

Input: results.jsonl, one JSON object per `run`. Besides the fields the harness writes itself
(arm, run, formulas, raws, target_rate_ps, readings_per_s (the measured PUBACK rate),
stored_rows_per_s (settled stored rows over the hold), dm_image, em_image, ok, rows_ok, ...), a row
carries these metrics under --prom / --metric. Names are the contract; a missing one is NO DATA,
and NO DATA never passes.

  dm_cpu_per_reading  device-management CPU cores per 1000 readings/s   (lower is better)
  p99_ms              device-management resolve p99                     (lower is better)
  em_cpu_per_row      event-management CPU cores per 1000 stored rows/s
  persist_p99_ms      event-management persist p99
  lag_growing         1 if any resolved-event consumer's lag grew over the window, else 0 (max)
  dead_letters        dead-lettered messages over the window                           (max)
  em_rows_per_s, hypertable_bytes_per_h, ds_upserts_per_s, stream_bytes_per_s   (reported, C4)

Rules that apply to every arm:
  - every run must have ok=true and rows_ok=true (the harness's own checks, including the exact
    stored-row count), and must have achieved its offered rate (measured PUBACK rate >= 95% of
    the target), or the arm FAILS: a throughput that was not offered says nothing about cost;
  - every run of every non-B0 arm must be on the same device-management and event-management
    images, or every gating arm FAILS (B0 is the previous release, so it is exempt);
  - a gating arm (C0, C1, C8-eq, C4-eq) needs at least TWO runs, or it FAILS.

Criteria (arms are compared on the median of their runs):
  C0      passes on its verified runs; the run-to-run spread is recorded and the Calc0 benchmark is
          the other half of the evidence. An optional B0 arm (the previous release image) adds
          +-1% checks on dm_cpu_per_reading and p99_ms.
  C1      dm_cpu_per_reading under +3% vs C0
  C4      reported only
  C8-eq   stored_rows_per_s within 5% of C0's (else the arm is not row-equal), then em_cpu_per_row
          and persist_p99_ms within +-10% of C0, max lag_growing 0, max dead_letters 0
  C4-eq   the fallback: the same checks as C8-eq at 4 formulas
  C8-knee reported: the lowest measured rate at which lag_growing is 1

Verdict: default 8 if C0, C1 and C8-eq pass; default 4 only if C0 and C1 pass, C8-eq FAILED, and
C4-eq passes; otherwise the default stays 0 and G21 is re-planned.
"""
import json
import statistics
import sys

PASS, FAIL, NODATA, REPORT = "PASS", "FAIL", "NO DATA", "REPORT"
GATING = ("C0", "C1", "C8-eq", "C4-eq")


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


def _vals(rows, key):
    vals = [r[key] for r in rows if isinstance(r.get(key), (int, float))]
    return vals if rows and len(vals) == len(rows) else None


def med(rows, key):
    v = _vals(rows, key)
    return statistics.median(v) if v else None


def mx(rows, key):
    v = _vals(rows, key)
    return max(v) if v else None


def rel(a, b):
    """(a - b) / b, or None when it cannot be said."""
    if a is None or b is None or b == 0:
        return None
    return (a - b) / b


def quality(rows):
    """A reason the arm's rows cannot be believed, else None."""
    for r in rows:
        if r.get("ok") is not True:
            return "run %s failed its checks: %s" % (r.get("run"), r.get("why") or "ok=false")
        if r.get("rows_ok") is not True:
            return "run %s: stored rows were not verified against promised x (raws+N)" % r.get("run")
        t, a = r.get("target_rate_ps"), r.get("readings_per_s")
        if not isinstance(t, (int, float)) or not isinstance(a, (int, float)):
            return "run %s: no measured rate in the load report" % r.get("run")
        if t > 0 and a < 0.95 * t:
            return "run %s: offered %.0f/s but achieved %.0f/s (under 95%%): the cost per reading is not comparable" % (
                r.get("run"), t, a)
    return None


def within(name, got, base, tol, out):
    d = rel(got, base)
    if d is None:
        out.append((NODATA, "%s: missing for the arm or its baseline" % name))
        return
    out.append((PASS if abs(d) <= tol else FAIL, "%s %+.2f%% (limit +-%.0f%%)" % (name, d * 100, tol * 100)))


def builds(by_arm):
    """(ok, text): every non-B0 run is on one dm/em image pair."""
    seen = set()
    for arm, rows in by_arm.items():
        if arm == "B0":
            continue
        for r in rows:
            seen.add((r.get("dm_image") or "", r.get("em_image") or ""))
    if not seen:
        return True, ""
    if any(not a or not b for a, b in seen):
        return False, "a run carries no device-management / event-management image id"
    if len(seen) > 1:
        return False, "runs span %d image pairs; arms from different builds cannot be compared" % len(seen)
    return True, ""


def judge(by_arm):
    """Return ({arm: (status, [(status, text)...])}, verdict)."""
    res = {}
    c0 = by_arm.get("C0", [])
    same_build, build_text = builds(by_arm)

    def settle(arm, checks):
        rows = by_arm.get(arm)
        if not rows:
            res[arm] = (NODATA, [(NODATA, "no runs")])
            return
        q = quality(rows)
        if q:
            res[arm] = (FAIL, [(FAIL, q)])
            return
        if arm in GATING and len(rows) < 2:
            res[arm] = (FAIL, [(FAIL, "needs at least two runs (has %d)" % len(rows))])
            return
        if arm in GATING and not same_build:
            res[arm] = (FAIL, [(FAIL, build_text)])
            return
        st = [s for s, _ in checks]
        if FAIL in st:
            out = FAIL
        elif NODATA in st:
            out = NODATA
        elif PASS in st:
            out = PASS
        else:
            out = REPORT
        res[arm] = (out, checks)

    # C0
    checks = []
    t = _vals(c0, "readings_per_s")
    if t and len(t) >= 2 and max(t):
        spread = (max(t) - min(t)) / max(t)
        checks.append((REPORT, "run-to-run rate spread %.2f%% (record it next to the Calc0 benchmark)" % (spread * 100)))
    checks.append((PASS, "%d verified run(s)" % len(c0)))
    b0 = by_arm.get("B0")
    if b0:
        within("dm_cpu_per_reading vs B0", med(c0, "dm_cpu_per_reading"), med(b0, "dm_cpu_per_reading"), 0.01, checks)
        within("p99_ms vs B0", med(c0, "p99_ms"), med(b0, "p99_ms"), 0.01, checks)
    settle("C0", checks)

    # C1
    checks = []
    d = rel(med(by_arm.get("C1", []), "dm_cpu_per_reading"), med(c0, "dm_cpu_per_reading"))
    if d is None:
        checks.append((NODATA, "dm_cpu_per_reading missing for C1 or C0"))
    else:
        checks.append((PASS if d < 0.03 else FAIL, "dm_cpu_per_reading %+.2f%% vs C0 (limit < +3%%)" % (d * 100)))
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
        d = rel(med(rows, "stored_rows_per_s"), med(c0, "stored_rows_per_s"))
        if d is None:
            checks.append((NODATA, "stored_rows_per_s missing, so rows/s cannot be compared with C0"))
        else:
            checks.append((PASS if abs(d) <= 0.05 else FAIL,
                           "measured stored rows/s %+.1f%% vs C0 (must be within +-5%% or the arm is not row-equal)" % (d * 100)))
        within("em_cpu_per_row", med(rows, "em_cpu_per_row"), med(c0, "em_cpu_per_row"), 0.10, checks)
        within("persist_p99_ms", med(rows, "persist_p99_ms"), med(c0, "persist_p99_ms"), 0.10, checks)
        for k in ("lag_growing", "dead_letters"):
            v = mx(rows, k)
            checks.append((NODATA, "%s missing" % k) if v is None
                          else (PASS if v == 0 else FAIL, "max %s = %s (must be 0)" % (k, v)))
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
        checks.append((REPORT, "lag first grows at %.0f readings/s (x%d rows)" % (min(grew), 8 + knee[0]["raws"])
                       if grew else "lag did not grow at any rate tried: the knee is above %.0f readings/s" %
                       knee[-1].get("readings_per_s", 0)))
    settle("C8-knee", checks)

    def g(a):
        return res.get(a, (NODATA, []))[0]

    if g("C0") == PASS and g("C1") == PASS and g("C8-eq") == PASS:
        verdict = "default 8"
    elif g("C0") == PASS and g("C1") == PASS and g("C8-eq") == FAIL and g("C4-eq") == PASS:
        verdict = "default 4 (C8-eq failed; record the cause)"
    else:
        verdict = "default stays 0: re-plan G21 (this is a result, not a failure to hand-wave)"
    return res, verdict


def render(by_arm):
    res, verdict = judge(by_arm)
    lines = []
    for arm in ("C0", "C1", "C4", "C8-eq", "C4-eq", "C8-knee"):
        if arm not in by_arm:
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
        r = {"arm": arm, "run": run, "formulas": n, "raws": 1, "target_rate_ps": rate, "readings_per_s": rate,
             "stored_rows_per_s": rate * (1 + n), "ok": True, "rows_ok": True,
             "dm_image": "dm@sha256:a", "em_image": "em@sha256:b", "dm_cpu_per_reading": 1.0,
             "p99_ms": 10.0, "em_cpu_per_row": 1.0, "persist_p99_ms": 20.0, "lag_growing": 0, "dead_letters": 0}
        r.update(kw)
        return r

    eq = 10000 / 9

    def two(arm, n, rate, **kw):
        return [row(arm, 1, n, rate, **kw), row(arm, 2, n, rate, **kw)]

    base = two("C0", 0, 10000) + two("C1", 1, 10000, dm_cpu_per_reading=1.02) + two("C8-eq", 8, eq)
    failures = []

    def without(rows, arm):
        return [r for r in rows if r["arm"] != arm]

    def expect(name, rows, want_arm, want_status, want_verdict=None):
        res, verdict = judge(group(rows))
        got = res.get(want_arm, (None,))[0]
        if got != want_status:
            failures.append("%s: %s is %s, want %s" % (name, want_arm, got, want_status))
        if want_verdict and not verdict.startswith(want_verdict):
            failures.append("%s: verdict %r, want %r" % (name, verdict, want_verdict))

    expect("good", base, "C8-eq", PASS, "default 8")
    expect("good C1", base, "C1", PASS)
    expect("C1 costs 5% DM CPU", without(base, "C1") + two("C1", 1, 10000, dm_cpu_per_reading=1.05),
           "C1", FAIL, "default stays 0")
    expect("C8-eq not row-equal (measured rows/s)", without(base, "C8-eq") + two("C8-eq", 8, eq, stored_rows_per_s=5000),
           "C8-eq", FAIL)
    expect("C8-eq lag grows in ONE run (max, not median)",
           without(base, "C8-eq") + [row("C8-eq", 1, 8, eq), row("C8-eq", 2, 8, eq, lag_growing=1), row("C8-eq", 3, 8, eq)],
           "C8-eq", FAIL)
    expect("C8-eq dead letters in one run",
           without(base, "C8-eq") + [row("C8-eq", 1, 8, eq), row("C8-eq", 2, 8, eq, dead_letters=2)], "C8-eq", FAIL)
    expect("C8-eq em cost +20%", without(base, "C8-eq") + two("C8-eq", 8, eq, em_cpu_per_row=1.2), "C8-eq", FAIL)
    expect("unverified rows", without(base, "C8-eq") + two("C8-eq", 8, eq, rows_ok=None), "C8-eq", FAIL)
    expect("a failed harness check",
           without(base, "C1") + [row("C1", 1, 1, 10000, ok=False, why="x"), row("C1", 2, 1, 10000)], "C1", FAIL)
    expect("one run is not enough", without(base, "C1") + [row("C1", 1, 1, 10000)], "C1", FAIL, "default stays 0")
    expect("offered rate not achieved", without(base, "C1") + two("C1", 1, 10000, readings_per_s=8000), "C1", FAIL)
    expect("missing metric is not a pass",
           without(base, "C8-eq") + [{k: v for k, v in r.items() if k != "persist_p99_ms"} for r in two("C8-eq", 8, eq)],
           "C8-eq", NODATA)
    mixed = without(base, "C1") + two("C1", 1, 10000, dm_cpu_per_reading=1.02, dm_image="dm@sha256:other")
    expect("mixed builds", mixed, "C0", FAIL, "default stays 0")
    prev = {"dm_image": "dm@sha256:prev", "em_image": "em@sha256:prev"}
    expect("B0 on another image is exempt, and within 1% passes", base + two("B0", 0, 10000, **prev),
           "C0", PASS, "default 8")
    expect("B0 5% off fails C0", base + two("B0", 0, 10000, dm_cpu_per_reading=1.05, **prev),
           "C0", FAIL, "default stays 0")
    fallback = without(base, "C8-eq") + two("C8-eq", 8, eq, lag_growing=1) + two("C4-eq", 4, 2000)
    expect("fallback to 4", fallback, "C4-eq", PASS, "default 4")
    # 4 needs C8-eq to have FAILED: no C8-eq rows at all must not buy a 4
    expect("no C8-eq rows is not a C8-eq failure", without(base, "C8-eq") + two("C4-eq", 4, 2000),
           "C4-eq", PASS, "default stays 0")
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
