#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Deterministic Go batches, applied AFTER ci-select's unchanged module/leg selection.

  python3 hack/ci-go-batch.py regenerate /path/to/jobs.json > hack/ci-go-durations.json
  python3 hack/ci-go-batch.py discover  # DISCOVERED_MODULES + WORKSPACE_MODULES -> partition
  python3 hack/ci-go-batch.py select    # GO_BATCHES + DISCOVERED_MODULES; selection JSON on stdin
  python3 hack/ci-go-batch.py estimate  # current full-run slot estimate
  python3 hack/ci-go-batch.py --self-test

Timings are successful CI PUSH jobs (full race coverage), never reader-only or narrowed PR
jobs. Pack by p90 into <=600s bins, within existing cache groups. Unknown timings, envtest,
split modules and p90 >300s stay alone. New modules therefore keep their own job until measured.
The duration table and sorted longest-first packing fix batch membership across runs;
selection only removes members, never repacks them. Single-member cache groups stay alone.
"""
import collections
import copy
import datetime
import importlib.util
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
TABLE = ROOT / "hack/ci-go-durations.json"
# Import the hyphenated selection script without dirtying the checkout with __pycache__.
sys.dont_write_bytecode = True
ENVTEST = {"backend/cli", "backend/k8s"}
LIMIT = 600


def selector():
    spec = importlib.util.spec_from_file_location("ci_select", ROOT / "hack/ci-select.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def check_partition(modules, batches, independent=None):
    """Fail closed on discovery/packing loss, duplication, substitution or zero work."""
    if not isinstance(modules, list) or not modules or any(not isinstance(m, str) or not m for m in modules):
        raise ValueError("module discovery is empty or malformed")
    if len(set(modules)) != len(modules):
        raise ValueError("module discovery contains duplicates")
    if independent is not None and (not independent or collections.Counter(modules) != collections.Counter(independent)):
        raise ValueError("module discovery disagrees with independent go.work reader")
    if not isinstance(batches, list) or not batches or any(not isinstance(b, list) or not b for b in batches):
        raise ValueError("batch partition is empty or contains an empty batch")
    flat = [m for b in batches for m in b]
    if collections.Counter(flat) != collections.Counter(modules):
        missing = sorted(set(modules) - set(flat))
        extra = sorted(set(flat) - set(modules))
        duplicate = sorted(m for m, n in collections.Counter(flat).items() if n > 1)
        raise ValueError("batch union != modules: missing=%s extra=%s duplicates=%s" % (missing, extra, duplicate))


def isolated(module, durations, splits):
    row = durations.get(module)
    return module in ENVTEST or module in splits or row is None or row["p90_seconds"] > 300


def pack(modules, durations, splits, cache_group):
    groups, batches = {}, []
    for m in sorted(modules):
        if isolated(m, durations, splits):
            batches.append([m])
        else:
            groups.setdefault(cache_group(m)[0], []).append(m)
    for group, members in sorted(groups.items()):
        # Start with the minimum bin count implied by the budget, then balance the longest
        # jobs first. This avoids first-fit's tiny last batch. If indivisible modules need
        # another bin to respect the hard ceiling, add it deterministically.
        count = math.ceil(sum(durations[m]["p90_seconds"] for m in members) / LIMIT)
        bins = [([], 0) for _ in range(count)]
        for m in sorted(members, key=lambda m: (-durations[m]["p90_seconds"], m)):
            weight = durations[m]["p90_seconds"]
            fits = [i for i, (_, total) in enumerate(bins) if total + weight <= LIMIT]
            if fits:
                i = min(fits, key=lambda i: (bins[i][1], i))
                b, total = bins[i]
                bins[i] = (b + [m], total + weight)
            else:
                bins.append(([m], weight))
        batches.extend(sorted(sorted(b) for b, _ in bins if b))
    check_partition(modules, batches)
    return sorted(batches)


def validate_layout(modules, batches, durations, splits, cache_group):
    check_partition(modules, batches)
    for batch in batches:
        if len(batch) > 1:
            if any(isolated(m, durations, splits) for m in batch):
                raise ValueError("special/slow/unmeasured module must be isolated: %s" % batch)
            if len({cache_group(m)[0] for m in batch}) != 1:
                raise ValueError("a batch mixes cache groups: %s" % batch)
            if sum(durations[m]["p90_seconds"] for m in batch) > LIMIT:
                raise ValueError("batch exceeds duration budget: %s" % batch)


def batch_selection(legs, modules, batches):
    check_partition(modules, batches)
    by_module = collections.defaultdict(list)
    for leg in legs:
        if leg["module"] not in modules:
            raise ValueError("selected an unknown module: %s" % leg["module"])
        by_module[leg["module"]].append(leg)
    out = []
    for i, batch in enumerate(batches, 1):
        selected = [leg for m in batch for leg in by_module[m]]
        if not selected:
            continue  # no job for an empty selection, including docs-only and draft runs
        if len(batch) == 1:
            out.extend(selected)  # all shards/readers/rest keep their existing independent jobs
            continue
        if len({g["module"] for g in selected}) != len(selected) or any(g["shard"] or g["exclude"] or g.get("tests") or g.get("skip_tests") for g in selected):
            raise ValueError("a split leg reached a small-module batch")
        if len({g["cache_group"] for g in selected}) != 1:
            raise ValueError("selected batch mixes cache groups")
        # static=false prevents the standalone workflow steps from acting on the batch label.
        out.append(dict(module="batch-%02d" % i, leg="", static=False, batch=selected,
                        cache_group=selected[0]["cache_group"], cache_save=any(g["cache_save"] for g in selected),
                        race_scope="all", pkgs="", exclude="", shard="", race_pkgs=""))
    # Compare COMPLETE leg payloads, including static, race scope, packages and shard flags.
    canonical = lambda xs: collections.Counter(json.dumps(g, sort_keys=True) for g in xs)
    flattened = [g for job in out for g in job.get("batch", [job])]
    if canonical(flattened) != canonical(legs):
        raise ValueError("batching dropped, duplicated or modified a selected leg")
    return out


def regenerate(path):
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    times, counts, starts = collections.defaultdict(list), collections.Counter(), []
    for j in data["jobs"]:
        if j["wf"] != "ci" or j["event"] != "push" or not j["name"].startswith("go ("):
            continue
        if j["concl"] != "skipped":
            counts[j["run"]] += 1
        if j["concl"] != "success" or not j["started"] or not j["completed"]:
            continue
        start = datetime.datetime.fromisoformat(j["started"].replace("Z", "+00:00"))
        end = datetime.datetime.fromisoformat(j["completed"].replace("Z", "+00:00"))
        seconds = (end - start).total_seconds()
        if seconds > 0:
            times[j["name"]].append(seconds)
            starts.append(j["started"])
    if not times:
        raise ValueError("no successful full-coverage push Go jobs in historical data")
    rows = {name: dict(samples=len(ts), median_seconds=math.ceil(statistics.median(ts)),
                       p90_seconds=math.ceil(sorted(ts)[min(len(ts)-1, int(.9 * len(ts)))]))
            for name, ts in sorted(times.items())}
    return dict(source="ci push jobs with success; started-to-completed seconds, including setup",
                regenerate="python3 hack/ci-go-batch.py regenerate /path/to/jobs.json > hack/ci-go-durations.json",
                first_started=min(starts), last_started=max(starts), push_runs=len(counts),
                observed_push_go_slots=sorted(set(counts.values())),
                modules={name[4:-1]: row for name, row in rows.items() if " / " not in name}, legs=rows)


def self_test():
    ci = selector()
    durations = json.loads(TABLE.read_text())["modules"]
    # Independent fixture enumeration, not pack's output used as its own expected list.
    modules = list(durations) + [m for m in ci.SPLITS if m not in durations] + ["backend/tools/new-module"]
    batches = pack(modules, durations, ci.SPLITS, ci.cache_group)
    check_partition(modules, batches, list(reversed(modules)))
    assert batches == pack(list(reversed(modules)), durations, ci.SPLITS, ci.cache_group)
    validate_layout(modules, batches, durations, ci.SPLITS, ci.cache_group)
    print("ok: union == modules, no duplicates; packing deterministic under reordered discovery")
    assert all(len(b) == 1 for b in batches if any(isolated(m, durations, ci.SPLITS) for m in b))
    print("ok: envtest, split, >5-minute and unmeasured modules isolated; cache groups preserved")
    legs = [g for m in modules for g in ci.legs_for(m)]
    for g in legs:
        g.update(race_scope="all", race_pkgs="")
    jobs = batch_selection(legs, modules, batches)
    assert sum(bool(g.get("cache_save")) for g in jobs) == sum(bool(g["cache_save"]) for g in legs)
    print("ok: full selection preserves every leg payload and each cache saver exactly once")
    batch = next(b for b in batches if len(b) > 1)
    leg = ci.legs_for(batch[0], ["test"])[0]
    leg.update(race_scope="changed", race_pkgs="./test/")
    narrowed = batch_selection([leg], modules, batches)
    assert len(narrowed) == 1 and narrowed[0]["batch"] == [leg]
    assert batch_selection([], modules, batches) == []
    print("ok: partial selection keeps only selected modules and reader/race flags; empty batches skipped")
    def refuses(label, mods, layout, independent=None):
        try:
            check_partition(mods, layout, independent)
        except ValueError as e:
            print("ok: %s rejected: %s" % (label, e))
        else:
            raise AssertionError("accepted " + label)
    broken = copy.deepcopy(batches)
    dropped = broken[0].pop()
    broken = [b for b in broken if b]
    refuses("NEGATIVE CONTROL planted module drop (%s)" % dropped, modules, broken)
    control = subprocess.run([sys.executable, str(Path(__file__)), "select"],
                             input='{"go":[]}', text=True, capture_output=True,
                             env=dict(os.environ, DISCOVERED_MODULES=json.dumps(modules), GO_BATCHES=json.dumps(broken)))
    assert control.returncode == 1 and "batch union != modules" in control.stderr, control.stderr
    print("ok: NEGATIVE CONTROL real select command exits 1 for the planted module drop")
    broken = copy.deepcopy(batches)
    broken[0].append(broken[-1][0])
    refuses("duplicate module", modules, broken)
    broken[0][-1] = "unknown/module"
    refuses("foreign module", modules, broken)
    refuses("empty discovery", [], [])
    refuses("truncated go-list discovery", modules[:-1], pack(modules[:-1], durations, ci.SPLITS, ci.cache_group), modules)
    # A same-count substitution passes the old count-only guard, but must fail the new guard.
    substituted = modules[:-1] + ["foreign/module"]
    refuses("same-count substitution", substituted, [[m] for m in substituted], modules)
    print("ci-go-batch self-test passed")


def main(argv):
    if argv == ["--self-test"]:
        self_test()
        return
    if len(argv) == 2 and argv[0] == "regenerate":
        print(json.dumps(regenerate(argv[1]), indent=2) + "\n", end="")
        return
    ci = selector()
    table = json.loads(TABLE.read_text())
    durations = table["modules"]
    modules = json.loads(os.environ["DISCOVERED_MODULES"])
    if argv == ["discover"]:
        batches = pack(modules, durations, ci.SPLITS, ci.cache_group)
        independent = [m.removeprefix("./") for m in os.environ["WORKSPACE_MODULES"].splitlines() if m]
        check_partition(modules, batches, independent)
        validate_layout(modules, batches, durations, ci.SPLITS, ci.cache_group)
        print(json.dumps(batches, separators=(",", ":")))
    elif argv == ["select"]:
        batches = json.loads(os.environ["GO_BATCHES"])
        validate_layout(modules, batches, durations, ci.SPLITS, ci.cache_group)
        selection = json.load(sys.stdin)
        before = len(selection["go"])
        selection["go"] = batch_selection(selection["go"], modules, batches)
        print("Go batching: %d selected legs -> %d jobs" % (before, len(selection["go"])), file=sys.stderr)
        for job in selection["go"]:
            if "batch" in job:
                print("  %s: %s" % (job["module"], ", ".join(g["module"] for g in job["batch"])), file=sys.stderr)
        print(json.dumps(selection, separators=(",", ":")))
    elif argv == ["estimate"]:
        batches = pack(modules, durations, ci.SPLITS, ci.cache_group)
        legs = [g for m in modules for g in ci.legs_for(m)]
        jobs = batch_selection(legs, modules, batches)
        print("Historical push runs: %d; observed Go slots/run: %s" % (table["push_runs"], table["observed_push_go_slots"]))
        print("Current full run: %d modules; %d -> %d Go job slots (%.1f%% fewer)" % (len(modules), len(legs), len(jobs), 100*(1-len(jobs)/len(legs))))
        for b in batches:
            if len(b) > 1:
                print("  batch p90 budget %ds: %s" % (sum(durations[m]["p90_seconds"] for m in b), ", ".join(b)))
    else:
        raise ValueError(__doc__)


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (ValueError, KeyError) as error:
        print("ci-go-batch: " + str(error), file=sys.stderr)
        sys.exit(1)
