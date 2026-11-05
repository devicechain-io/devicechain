# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Helper for hack/perf-ab.sh and hack/perf-ab-compare.sh (Python 3 standard library only).

  perf_ab.py summarize --dir <arm dir>        raw snapshots -> summary.json + summary.md
  perf_ab.py noise OUT.json SUMMARY...        replicate summaries of ONE arm -> per-metric noise band
  perf_ab.py compare [--noise F] [--floor P] A B [C ...]   baseline A against each of B, C...
  perf_ab.py self-test                        runs the maths against a synthetic fixture

An "arm" argument to compare is one summary.json, or several joined by commas (replicates, which
are averaged).
"""
import glob
import json
import math
import os
import re
import sys
import tempfile

SCHEMA = 1
# Fewer table updates than this in the window make a HOT ratio meaningless.
HOT_MIN_UPDATES = 1000
# A relative band is meaningless for a metric that is ~0 in every arm.
TINY = 1e-9
# Components using less CPU than this are background, not per-event cost: not reported as metrics.
MIN_CORES = 0.1
# Floor for the band (percent): replicates of a deterministic count can agree to 3 digits by luck.
DEFAULT_FLOOR_PCT = 2.0
# CPU ratios come from cAdvisor counters sampled over the window and move with whatever else the
# machine is doing, so their band never goes below this, however still the replicates were.
CPU_FLOOR_PCT = 5.0
# The noise band is a prediction interval: with n replicates of one arm (mean m, standard deviation s),
# a NEW single run of the same arm falls within m +- t * s * sqrt(1 + 1/n) with the chosen confidence.
# A test arm averaged over k runs has sqrt(1/k + 1/n) in place of sqrt(1 + 1/n). Two-sided Student t,
# indexed by degrees of freedom n - 1; beyond the table the normal quantile is close enough.
T_TABLE = {
    95: [12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228],
    99: [63.657, 9.925, 5.841, 4.604, 4.032, 3.707, 3.499, 3.355, 3.250, 3.169],
}
T_LARGE = {95: 2.0, 99: 2.8}
DEFAULT_CONF = 99
# Set by --include-failed: keep runs whose harness verdict failed (an arm that is expected to fail one,
# such as a deliberately starved projection, still has per-event costs worth comparing).
INCLUDE_FAILED = False
# Diagnostics, shown but never flagged: JetStream API calls (mostly timed health and info calls, so per event they
# follow the rate, not the work), counts of rare events (full-page images follow checkpoints),
# the rate itself, and the loss counters (the harness verdict covers those).
INFO_ONLY = ("nats.js_api_calls", "db.tsdb.wal_fpi_bytes", "db.rdb.wal_fpi_bytes", "db.tsdb.wal_fpi", "db.rdb.wal_fpi", "db.tsdb.wal_bytes_per_event_with_ckpt", "db.rdb.wal_bytes_per_event_with_ckpt", "loss.", "rate.", "db.tsdb.txns_per_s", "db.rdb.txns_per_s",
             "db.tsdb.dead_tuples", "db.rdb.dead_tuples", "db.tsdb.autovacuum_runs", "db.rdb.autovacuum_runs",
             "db.tsdb.autoanalyze_runs", "db.rdb.autoanalyze_runs", "db.tsdb.checkpoints_", "db.rdb.checkpoints_")


def t_quantile(conf, n):
    tab = T_TABLE[conf]
    return tab[n - 2] if 2 <= n <= len(tab) + 1 else T_LARGE[conf]


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def num(x, default=0.0):
    try:
        return float(x)
    except (TypeError, ValueError):
        return default


# ---------------------------------------------------------------------------
# summarize
# ---------------------------------------------------------------------------
def read_snap(d):
    s = {"nats": {}, "db": {}, "cpu": {}, "pods": None, "epoch": None}
    for f in glob.glob(os.path.join(d, "nats-*-varz.json")):
        pod = os.path.basename(f)[len("nats-"):-len("-varz.json")]
        s["nats"][pod] = {
            "varz": load(f),
            "routez": load(os.path.join(d, "nats-%s-routez.json" % pod)),
            "connz": load(os.path.join(d, "nats-%s-connz.json" % pod)),
            "jsz": load(os.path.join(d, "nats-%s-jsz.json" % pod)) if os.path.exists(os.path.join(d, "nats-%s-jsz.json" % pod)) else None,
        }
    for name in ("tsdb", "rdb"):
        p = os.path.join(d, "db-%s.json" % name)
        if os.path.exists(p):
            with open(p, encoding="utf-8") as f:
                s["db"][name] = json.loads(f.read().strip())
    s["tables"], s["instances"] = {}, None
    for name in ("tsdb", "rdb"):
        p = os.path.join(d, "tables-%s.json" % name)
        if os.path.exists(p) and os.path.getsize(p) > 2:
            with open(p, encoding="utf-8") as f:
                s["tables"][name] = json.loads(f.read().strip())
    p = os.path.join(d, "db-instances.json")
    if os.path.exists(p) and os.path.getsize(p) > 2:
        s["instances"] = load(p)
    label = re.compile(r'(\w+)="([^"]*)"')
    for f in glob.glob(os.path.join(d, "cadvisor-*.prom")):
        with open(f, encoding="utf-8") as fh:
            for line in fh:
                name, _, rest = line.partition("{")
                labels, _, val = rest.rpartition("}")
                lab = dict(label.findall(labels))
                if lab.get("container") in (None, "", "POD") or "pod" not in lab:
                    continue
                v = val.split()
                if not v:
                    continue
                key = (name, lab.get("namespace", ""), lab["pod"], lab["container"])
                s["cpu"][key] = s["cpu"].get(key, 0.0) + float(v[0])
    p = os.path.join(d, "pods.json")
    if os.path.exists(p):
        s["pods"] = {x["u"]: x for x in load(p)}
    p = os.path.join(d, "start.epoch")
    if os.path.exists(p):
        with open(p, encoding="utf-8") as f:
            s["epoch"] = int(f.read().strip())
    return s


def stmt_key(s):
    return (s.get("db"), s.get("id"))


def norm_query(q):
    q = re.sub(r"\(\$\d+(?:,\s*\$\d+)*\)(?:,\s*\(\$\d+(?:,\s*\$\d+)*\))*", "(...)", q)
    q = re.sub(r"\$\d+(?:,\s*\$\d+)+", "$n", q)
    return re.sub(r"\s+", " ", q).strip()[:160]


def stmt_deltas(pre, post, warnings, who):
    """pg_stat_statements delta, grouped by normalised text. Refuses a reset or an eviction."""
    if pre.get("reset") != post.get("reset"):
        warnings.append("%s: pg_stat_statements was reset inside the window; statement metrics are invalid" % who)
        return None
    if num(pre.get("dealloc")) != num(post.get("dealloc")):
        warnings.append("%s: pg_stat_statements evicted entries inside the window (raise pg_stat_statements.max); statement metrics are invalid" % who)
        return None
    before = {stmt_key(s): s for s in pre.get("stmts", [])}
    groups = {}
    for s in post.get("stmts", []):
        b = before.get(stmt_key(s), {})
        dc = num(s.get("calls")) - num(b.get("calls"))
        if dc <= 0:
            continue
        g = groups.setdefault(norm_query(s.get("q", "")), {"calls": 0.0, "rows": 0.0, "ms": 0.0, "walb": 0.0})
        g["calls"] += dc
        g["rows"] += num(s.get("rows")) - num(b.get("rows"))
        g["ms"] += num(s.get("ms")) - num(b.get("ms"))
        g["walb"] += num(s.get("walb")) - num(b.get("walb"))
    return groups


def _insert_into(table):
    # INSERT INTO ["schema".]"table" [AS alias] ( ... ; the services qualify every table with their schema
    return re.compile(r'^insert into\s+(?:"[^"]+"\.)?"?%s"?\s*(?:as\s+\w+\s*)?\(' % table, re.I)


EVENTS_INSERT = _insert_into("events")
# The hot-path writes reported as rows per statement: name -> (pattern, which databases hold it).
HOT_STATEMENTS = {
    "events_insert": EVENTS_INSERT,
    "measurement_events_insert": _insert_into("measurement_events"),
    "device_states_upsert": _insert_into("device_states"),
    "latest_measurements_upsert": _insert_into("latest_measurements"),
}


# Statements the platform issues at a rate that does not follow the offered load (leader locks,
# periodic snapshots, audit housekeeping, tenant purges, this script's own reads). They are left out
# of the statements-per-second figures, which would otherwise carry their drift; transaction control
# (begin/commit/rollback) is counted on its own as txns_per_s.
TXN_CONTROL = ("begin", "commit", "rollback")
BACKGROUND = re.compile(r"pg_try_advisory_lock|pg_advisory_unlock|^delete from|detect_snapshots|audit_events|pg_stat_|jsonb_build_object|nspname|pg_catalog|information_schema", re.I)


def component_of(pod):
    pod = re.sub(r"-[0-9a-f]{8,10}-[a-z0-9]{5}$", "", pod)
    return re.sub(r"-\d+$", "", pod)


SHED_METRIC = re.compile(r'^devicechain_eventsources_total_(msg_rate_limited|msg_reading_limited|readings_rate_limited|msg_publish_failed|msg_backpressured)\{([^}]*)\}\s+([0-9.eE+-]+)')


def read_shed(d):
    """-> {(counter, source): value} summed over the event-sources pods, or None when not collected."""
    if not os.path.isdir(d):
        return None
    out = {}
    for f in glob.glob(os.path.join(d, "shed-*.prom")):
        with open(f, encoding="utf-8") as fh:
            for line in fh:
                mt = SHED_METRIC.match(line)
                if mt:
                    src = (re.search(r'source="([^"]*)"', mt.group(2)) or [None, ""])[1]
                    out[(mt.group(1), src)] = out.get((mt.group(1), src), 0.0) + float(mt.group(3))
    return out


STALL = re.compile(r"took too long|NO quorum|stalled|new (?:stream |consumer |metadata )?leader|leader (?:changed|lost)|[Ss]tep[- ]?down")
LOG_TIME = re.compile(r"(\d{4})/(\d\d)/(\d\d) (\d\d):(\d\d):(\d\d)")


def stall_episodes(path, w0, w1, gap=30):
    """Broker log lines that mark a stall or a leader change, grouped into episodes (lines within `gap`
    seconds), each placed against the measured window [w0, w1] in epoch seconds."""
    if not os.path.exists(path):
        return None
    import calendar
    times, kinds = [], []
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            mt = LOG_TIME.search(line)
            if not mt or not STALL.search(line):
                continue
            times.append(calendar.timegm(tuple(int(x) for x in mt.groups()) + (0, 0, 0)))
            kinds.append("leader-change" if "leader" in line and "quorum" not in line else "stall")
    order = sorted(range(len(times)), key=lambda i: times[i])
    eps = []
    for i in order:
        if eps and times[i] - eps[-1]["end"] <= gap:
            eps[-1]["end"] = times[i]
            eps[-1]["lines"] += 1
            if kinds[i] == "stall":
                eps[-1]["kind"] = "stall"
        else:
            eps.append({"start": times[i], "end": times[i], "lines": 1, "kind": kinds[i]})
    for e in eps:
        e["startRelS"] = e["start"] - w0
        e["endRelS"] = e["end"] - w0
        e["inWindow"] = e["end"] >= w0 and e["start"] <= w1
    return eps


def checkpoint_cause(timed, requested):
    """Why the checkpoints in the window ran. 'requested' is max_wal_size being reached (or an explicit
    CHECKPOINT); 'timed' is checkpoint_timeout. They have opposite fixes, so they are never summed."""
    if timed <= 0 and requested <= 0:
        return "none"
    if requested <= 0:
        return "timed (checkpoint_timeout)"
    if timed <= 0:
        return "requested (max_wal_size reached, or an explicit CHECKPOINT)"
    return "timed and requested"


def table_metrics(name, a, b, events, m, info, warnings):
    """Per-table write and vacuum behaviour from pg_stat_user_tables snapshots ({database: [rows]}):
    the HOT-update ratio (n_tup_hot_upd / n_tup_upd: a non-HOT update writes every index of the table),
    dead tuples (a gauge at the end of the window), and autovacuum / autoanalyze runs in the window."""
    if a is None or b is None:
        warnings.append("%s: no table statistics were collected, so the HOT ratio, dead tuples and autovacuum runs are missing" % name)
        return

    def flat(doc):
        return {(db, r["t"]): r for db, rows in doc.items() for r in rows}
    A, B = flat(a), flat(b)
    rows = []
    for key, r in B.items():
        o = A.get(key, {})
        d = {f: num(r.get(f)) - num(o.get(f)) for f in ("i", "u", "h", "d", "av", "aa", "vc")}
        if any(v < 0 for v in d.values()):
            warnings.append("%s: table statistics went backwards for %s (a crash or a stats reset); table metrics are invalid" % (name, key[1]))
            return
        d.update(t=key[1], db=key[0], dead=num(r.get("dead")), live=num(r.get("live")))
        rows.append(d)
    if not rows:
        warnings.append("%s: the table statistics are empty" % name)
        return
    multi = len({r["db"] for r in rows}) > 1
    for r in rows:
        r["name"] = ("%s:%s" % (r["db"], r["t"])) if multi else r["t"]
    upd, hot = sum(r["u"] for r in rows), sum(r["h"] for r in rows)
    m["db.%s.tuple_updates_per_event" % name] = upd / events
    # A ratio over a handful of updates says nothing; below this it is reported as absent.
    if upd >= HOT_MIN_UPDATES:
        m["db.%s.hot_ratio" % name] = hot / upd
    for r in rows:
        if r["u"] >= HOT_MIN_UPDATES:
            m["db.%s.hot_ratio.%s" % (name, r["name"])] = r["h"] / r["u"]
    m["db.%s.dead_tuples" % name] = sum(r["dead"] for r in rows)
    for r in sorted(rows, key=lambda x: -x["dead"])[:8]:
        if r["dead"] > 0:
            m["db.%s.dead_tuples.%s" % (name, r["name"])] = r["dead"]
    m["db.%s.autovacuum_runs" % name] = sum(r["av"] for r in rows)
    m["db.%s.autoanalyze_runs" % name] = sum(r["aa"] for r in rows)
    top = sorted(rows, key=lambda x: -(x["u"] + x["i"]))[:8]
    info.setdefault("tables", {})[name] = [
        {"table": r["name"], "inserts": int(r["i"]), "updates": int(r["u"]), "hot_updates": int(r["h"]),
         "hot_ratio": round(r["h"] / r["u"], 4) if r["u"] else None, "dead_tuples": int(r["dead"]), "live_tuples": int(r["live"]),
         "autovacuum_runs": int(r["av"]), "autoanalyze_runs": int(r["aa"])} for r in top]


def instance_cpu(pre, post, window, events, m, info, warnings):
    """DB CPU per INSTANCE: the postgres container of each CNPG pod, labelled by role (primary or
    standby) from the cluster labels read at both ends of the window. The component rows
    (cpu.dc-tsdb...) fold the primary and the standby into one number, which hides the standby replay
    cost and the primary alone; these rows keep them apart."""
    if not post.get("instances") or not pre.get("instances"):
        warnings.append("no db-instances snapshot: the DB CPU could not be split into primary and standby")
        return
    role_of = lambda inst: "primary" if inst.get("role") == "primary" else "standby"
    pre_by = {i["n"]: i for i in pre["instances"]}
    post_by = {i["n"]: i for i in post["instances"]}
    totals, cores, moved = {}, {}, set()
    for pod, i in post_by.items():
        before = pre_by.get(pod)
        if before is None or role_of(before) != role_of(i):
            moved.add(i["store"])
            warnings.append("%s changed role (or appeared) inside the window: a failover or switchover. The per-instance CPU rows for %s are invalid" % (pod, i["store"]))
    for key, v1 in post["cpu"].items():
        name, nsp, pod, ctr = key
        if name != "container_cpu_usage_seconds_total" or ctr != "postgres" or pod not in post_by:
            continue
        d1 = v1 - pre["cpu"].get(key, 0.0)
        i = post_by[pod]
        if d1 < 0:
            warnings.append("cAdvisor counter went backwards for %s/%s (container restarted?)" % (pod, ctr))
            moved.add(i["store"])
            continue
        totals[(i["store"], role_of(i))] = totals.get((i["store"], role_of(i)), 0.0) + d1
        cores.setdefault(i["store"], {})["%s (%s)" % (pod, role_of(i))] = round(d1 / window, 3)
    for store in sorted({i["store"] for i in post_by.values()}):
        if store in moved:
            continue
        for role in ("primary", "standby"):
            if (store, role) in totals:
                m["cpu.dbinst.%s.%s.s_per_1k_events" % (store, role)] = totals[(store, role)] / (events / 1000.0)
            elif any(role_of(i) == role and i["store"] == store for i in post_by.values()):
                warnings.append("no CPU counters for the %s %s instance" % (store, role))
        info.setdefault("db_instance_cores", {})[store] = cores.get(store, {})


def summarize(d):
    meta = load(os.path.join(d, "meta.json"))
    raw = os.path.join(d, "raw")
    pre, post = read_snap(os.path.join(raw, "pre")), read_snap(os.path.join(raw, "post"))
    warnings = []
    m = {}
    info = {}

    # --- the denominator: events persisted in the window (event-store INSERT INTO events rows)
    tp, tq = pre["db"]["tsdb"], post["db"]["tsdb"]
    window = num(tq["t"]) - num(tp["t"])
    sd = {}
    for name in ("tsdb", "rdb"):
        sd[name] = stmt_deltas(pre["db"][name], post["db"][name], warnings, name)
    events = 0.0
    if sd["tsdb"]:
        for q, g in sd["tsdb"].items():
            if EVENTS_INSERT.match(q):
                events += g["rows"]
    if events <= 0:
        raise SystemExit("perf-ab: no rows were inserted into events in the window (is pg_stat_statements on, and did the load reach the store?)")
    info["window_s"] = round(window, 2)
    info["events"] = int(events)
    m["rate.offered_eps"] = num(meta.get("offered"))
    m["rate.achieved_eps"] = events / window

    # --- NATS: varz totals, client share from /connz, route share from /routez
    tot = {"in_msgs": 0.0, "out_msgs": 0.0, "in_bytes": 0.0, "out_bytes": 0.0}
    cli = {"in_msgs": 0.0, "out_msgs": 0.0, "in_bytes": 0.0, "out_bytes": 0.0}
    rte = {"in_msgs": 0.0, "out_msgs": 0.0, "in_bytes": 0.0, "out_bytes": 0.0}
    api = 0.0
    for pod, a in pre["nats"].items():
        b = post["nats"].get(pod)
        if b is None:
            warnings.append("nats %s disappeared" % pod)
            continue
        if b["varz"].get("server_id") != a["varz"].get("server_id") or num(b["varz"].get("in_msgs")) < num(a["varz"].get("in_msgs")):
            warnings.append("nats %s restarted inside the window; NATS metrics are invalid" % pod)
        for key in tot:
            tot[key] += num(b["varz"].get(key)) - num(a["varz"].get(key))
        api += num(((b["varz"].get("jetstream") or {}).get("stats") or {}).get("api", {}).get("total")) \
            - num(((a["varz"].get("jetstream") or {}).get("stats") or {}).get("api", {}).get("total"))
        ca = {c["cid"]: c for c in (a["connz"].get("connections") or [])}
        for c in (b["connz"].get("connections") or []):
            p = ca.get(c["cid"], {})
            for key in cli:
                cli[key] += num(c.get(key)) - num(p.get(key))
        ra = {r["rid"]: r for r in (a["routez"].get("routes") or [])}
        for r in (b["routez"].get("routes") or []):
            p = ra.get(r["rid"])
            if p is None:
                warnings.append("nats %s: route %s appeared inside the window (counted from zero)" % (pod, r["rid"]))
                p = {}
            for key in rte:
                rte[key] += num(r.get(key)) - num(p.get(key))
    m["nats.client_in_msgs_per_event"] = cli["in_msgs"] / events
    m["nats.client_out_msgs_per_event"] = cli["out_msgs"] / events
    m["nats.nonclient_in_msgs_per_event"] = (tot["in_msgs"] - cli["in_msgs"]) / events
    m["nats.nonclient_out_msgs_per_event"] = (tot["out_msgs"] - cli["out_msgs"]) / events
    m["nats.route_in_msgs_per_event"] = rte["in_msgs"] / events
    m["nats.route_out_msgs_per_event"] = rte["out_msgs"] / events
    m["nats.total_msgs_per_event"] = (tot["in_msgs"] + tot["out_msgs"]) / events
    m["nats.in_bytes_per_event"] = tot["in_bytes"] / events
    m["nats.out_bytes_per_event"] = tot["out_bytes"] / events
    m["nats.route_bytes_per_event"] = (rte["in_bytes"] + rte["out_bytes"]) / events
    m["nats.js_api_calls_per_event"] = api / events
    info["nats_msgs_total"] = int(tot["in_msgs"] + tot["out_msgs"])
    if len(pre["nats"]) > 1:
        # a server that served almost none of the load shows a leader layout that is not comparable
        shares = {}
        for pod, a in pre["nats"].items():
            b = post["nats"][pod]
            shares[pod] = round((num(b["varz"].get("in_msgs")) + num(b["varz"].get("out_msgs"))
                                 - num(a["varz"].get("in_msgs")) - num(a["varz"].get("out_msgs"))) / max(tot["in_msgs"] + tot["out_msgs"], 1), 3)
        info["nats_server_share"] = shares

    # --- JetStream: appends per stream and deliveries per consumer, per event (info, not compared)
    def stream_state(snap):
        st, cs = {}, {}
        for pod, x in snap["nats"].items():
            j = x.get("jsz") or {}
            for acc in j.get("account_details") or []:
                for sd in acc.get("stream_detail") or []:
                    seq = num(((sd.get("state") or {}).get("last_seq")))
                    st[sd["name"]] = max(st.get(sd["name"], 0.0), seq)
                    for c in sd.get("consumer_detail") or []:
                        key = "%s/%s" % (sd["name"], c["name"])
                        dv = num((c.get("delivered") or {}).get("consumer_seq"))
                        rd = num(c.get("num_redelivered"))
                        o = cs.get(key, (0.0, 0.0))
                        cs[key] = (max(o[0], dv), max(o[1], rd))
        return st, cs
    s0, c0 = stream_state(pre)
    s1, c1 = stream_state(post)
    if s1:
        info["stream_appends_per_event"] = {k: round((v - s0.get(k, 0.0)) / events, 3) for k, v in sorted(s1.items()) if v - s0.get(k, 0.0) > 0.02 * events}
        info["consumer_deliveries_per_event"] = {k: round((v[0] - c0.get(k, (0.0, 0.0))[0]) / events, 3) for k, v in sorted(c1.items()) if v[0] - c0.get(k, (0.0, 0.0))[0] > 0.02 * events}
        redel = {k: int(v[1] - c0.get(k, (0.0, 0.0))[1]) for k, v in c1.items() if v[1] - c0.get(k, (0.0, 0.0))[1] > 0}
        if redel:
            info["consumer_redeliveries"] = redel
            warnings.append("JetStream redeliveries in the window: %s" % ", ".join("%s=%d" % kv for kv in sorted(redel.items())[:6]))

    # --- databases: WAL from the LSN, statements from pg_stat_statements
    for name in ("tsdb", "rdb"):
        a, b = pre["db"][name], post["db"][name]
        if a.get("inRecovery") or b.get("inRecovery"):
            warnings.append("%s: the pod is in recovery; it is not the primary" % name)
        dw = num(b["lsn"]) - num(a["lsn"])
        if dw < 0:
            warnings.append("%s: the WAL position went backwards (failover?)" % name)
        m["db.%s.wal_bytes_per_event" % name] = dw / events
        m["db.%s.wal_fpi_per_event" % name] = (num((b.get("wal") or {}).get("wal_fpi")) - num((a.get("wal") or {}).get("wal_fpi"))) / events
        cks = num(b.get("ckpt")) - num(a.get("ckpt"))
        ct, cr = num(b.get("ckptTimed")) - num(a.get("ckptTimed")), num(b.get("ckptReq")) - num(a.get("ckptReq"))
        if "ckptTimed" in b and "ckptReq" in b:
            m["db.%s.checkpoints_timed" % name], m["db.%s.checkpoints_requested" % name] = ct, cr
            info.setdefault("checkpoint_cause", {})[name] = checkpoint_cause(ct, cr)
        else:
            warnings.append("%s: the snapshot carries no timed/requested checkpoint split; the cause of a checkpoint is unknown" % name)
        if cks > 0:
            # Every page touched after a checkpoint writes a full-page image, so the byte count of a window
            # that holds one is not comparable. It is kept under another name, which is never compared.
            m["db.%s.wal_bytes_per_event_with_ckpt" % name] = m.pop("db.%s.wal_bytes_per_event" % name)
            warnings.append("%s: %d checkpoint(s) inside the window (cause: %s); WAL bytes per event are reported as wal_bytes_per_event_with_ckpt and not compared; wal_bytes_excl_fpi_per_event (exact, from pg_walinspect) and records per event still are" % (name, cks, info.get("checkpoint_cause", {}).get(name, "unknown")))
        ws_path = os.path.join(raw, "post", "walstats-%s.json" % name)
        if os.path.exists(ws_path):
            with open(ws_path, encoding="utf-8") as f:
                ws = json.loads(f.read().strip())
            # The record bytes alone (the images are what a checkpoint adds), exact from pg_walinspect.
            m["db.%s.wal_bytes_excl_fpi_per_event" % name] = (num(ws.get("combinedBytes")) - num(ws.get("fpiBytes"))) / events
            m["db.%s.wal_fpi_bytes_per_event" % name] = num(ws.get("fpiBytes")) / events
        wr = num((b.get("wal") or {}).get("wal_records")) - num((a.get("wal") or {}).get("wal_records"))
        m["db.%s.wal_records_per_event" % name] = wr / events
        table_metrics(name, pre["tables"].get(name), post["tables"].get(name), events, m, info, warnings)
        if sd[name] is not None:
            calls = sum(g["calls"] for q, g in sd[name].items() if not BACKGROUND.search(q) and q.lower() not in TXN_CONTROL)
            m["db.%s.stmts_per_s" % name] = calls / window
            m["db.%s.stmts_per_event" % name] = calls / events
            m["db.%s.txns_per_s" % name] = sum(g["calls"] for q, g in sd[name].items() if q.lower() == "begin") / window
    for hname, pat in HOT_STATEMENTS.items():
        grp = [g for name in ("tsdb", "rdb") for q, g in (sd[name] or {}).items() if pat.match(q)]
        if grp:
            m["stmt.%s.rows_per_call" % hname] = sum(g["rows"] for g in grp) / sum(g["calls"] for g in grp)
            m["stmt.%s.calls_per_s" % hname] = sum(g["calls"] for g in grp) / window
        elif hname in ("events_insert", "device_states_upsert"):
            warnings.append("no %s statement seen; check the pattern against the top statements" % hname)
    info["top_statements"] = {}
    for name in ("tsdb", "rdb"):
        if sd[name]:
            top = sorted(sd[name].items(), key=lambda kv: -kv[1]["ms"])[:8]
            info["top_statements"][name] = [{"q": q, "calls": int(g["calls"]), "rows_per_call": round(g["rows"] / max(g["calls"], 1), 2),
                                             "ms": round(g["ms"], 1), "wal_bytes": int(g["walb"])} for q, g in top]

    # --- CPU by component (cAdvisor), per 1,000 events; plus CFS throttling to flag saturation
    comp, thr = {}, {}
    skip = {meta.get("loadgenPod", "perf-ab-loadgen")}
    for key, v1 in post["cpu"].items():
        name, nsp, pod, ctr = key
        if pod in skip:
            continue
        d1 = v1 - pre["cpu"].get(key, 0.0)
        if d1 < 0:
            warnings.append("cAdvisor counter went backwards for %s/%s (container restarted?)" % (pod, ctr))
            continue
        c = component_of(pod)
        if "barman" in ctr and pod not in (meta.get("eventStorePrimary"), meta.get("relationalPrimary")):
            # The backup sidecar of a STANDBY runs the scheduled base backup, a timed job that has
            # nothing to do with the events in the window. (On the primary it archives WAL, which does.)
            c += ".standby-backup"
        tgt = comp if name == "container_cpu_usage_seconds_total" else thr
        tgt[c] = tgt.get(c, 0.0) + d1
    total_cpu = 0.0
    for c, v in comp.items():
        if c.endswith(".standby-backup"):
            if v / window >= 0.1:
                warnings.append("a base backup ran on a standby in the window (%s used %.2f cores); it is left out of the CPU metrics" % (c, v / window))
            continue
        if v / window < MIN_CORES:
            continue
        m["cpu.%s.s_per_1k_events" % c] = v / (events / 1000.0)
        total_cpu += v
    m["cpu.total.s_per_1k_events"] = total_cpu / (events / 1000.0)
    info["cpu_cores"] = {c: round(v / window, 3) for c, v in sorted(comp.items(), key=lambda kv: -kv[1]) if v / window >= MIN_CORES}
    instance_cpu(pre, post, window, events, m, info, warnings)
    info["throttled_s_per_s"] = {c: round(v / window, 4) for c, v in thr.items() if v / window >= 0.02}
    if info["throttled_s_per_s"]:
        warnings.append("CFS throttling in the window (%s): the arm may be CPU-bound, so a count may be inflated by backlog" %
                        ", ".join(sorted(info["throttled_s_per_s"])))

    # --- pod identity/restarts
    if pre["pods"] and post["pods"]:
        for u, p in post["pods"].items():
            q = pre["pods"].get(u)
            if q is not None and p["r"] > q["r"]:
                warnings.append("pod %s restarted inside the window" % p["n"])
        gone = [p["n"] for u, p in pre["pods"].items() if u not in post["pods"]]
        if gone:
            warnings.append("pods replaced inside the window: %s" % ", ".join(gone))

    # --- the harness verdicts
    verdict = {"passed": True, "reports": {}}
    accepted_total = 0.0
    for kind in ("mqtt", "http"):
        p = os.path.join(raw, "report-%s.json" % kind)
        if not os.path.exists(p):
            if (kind == "mqtt" and num(meta.get("offeredMqtt")) > 0) or (kind == "http" and num(meta.get("offeredHttp")) > 0):
                verdict["passed"] = False
                verdict["reports"][kind] = {"missing": True}
            continue
        r = load(p)
        drv = r.get("drive") or {}
        inv = r.get("invariants") or []
        failed = [i.get("name") for i in inv if not i.get("passed")]
        if kind == "mqtt":
            # dc-loadtest-mqtt: PUBACKed events are the promise; storage holds what was read back.
            st = r.get("storage") or {}
            accepted = num(drv.get("pubacked"))
            persisted = num(st.get("persisted"))
            lost, dup = num(st.get("lost")), num(st.get("excess"))
            failed_n = num(drv.get("connectFailures")) + num(drv.get("unconfirmed")) + num(drv.get("connectionLosses"))
        else:
            accepted = num(drv.get("accepted"))
            persisted = num(r.get("persistedEvents"))
            lost, dup = max(accepted - persisted, 0), max(persisted - accepted, 0)
            failed_n = num(drv.get("failed"))
        accepted_total += accepted
        rep = {"accepted": int(accepted), "persisted": int(persisted), "failed": int(failed_n), "shed": int(num(drv.get("shed"))),
               "lost": int(max(lost, accepted - persisted, 0)), "duplicated": int(max(dup, persisted - accepted, 0)),
               "achievedRatePerSec": round(num(drv.get("achievedRatePerSec") or drv.get("pubackedRatePerSec")), 1), "failedInvariants": failed}
        verdict["reports"][kind] = rep
        if failed or rep["lost"] or rep["duplicated"] or rep["failed"]:
            verdict["passed"] = False
    m["loss.lost_events"] = sum(r.get("lost", 0) for r in verdict["reports"].values())
    m["loss.duplicated_events"] = sum(r.get("duplicated", 0) for r in verdict["reports"].values())
    if num(meta.get("offered")) and m["rate.achieved_eps"] < 0.97 * num(meta["offered"]):
        warnings.append("achieved %.0f events/s is under 97%% of the offered %.0f: per-event counts are only comparable at the same achieved rate" %
                        (m["rate.achieved_eps"], num(meta["offered"])))

    # --- a failed verdict: counted shed, or an unexplained loss?
    shed0, shed1 = read_shed(os.path.join(raw, "shed-start")), read_shed(os.path.join(raw, "shed-end"))
    shed = None
    if shed0 is not None and shed1 is not None:
        shed = {}
        for key, v in shed1.items():
            dv = v - shed0.get(key, 0.0)
            if dv < 0:
                warnings.append("shed counter %s went backwards (event-sources restarted?)" % (key,))
                dv = 0.0
            shed.setdefault(key[0], {})[key[1]] = dv
        info["shed_counters"] = {k: {s_: int(x) for s_, x in v.items()} for k, v in shed.items()}
    loss_class, unexplained = None, False
    for kind, rep in verdict["reports"].items():
        lost = rep.get("lost", 0) if isinstance(rep, dict) else 0
        if not lost:
            continue
        if shed is None:
            rep["lossClass"] = "UNEXPLAINED LOSS: lost %d and no shed counters were collected" % lost
            unexplained = True
            continue
        counted = sum(x for per in shed.values() for src, x in per.items() if (("mqtt" in src) == (kind == "mqtt")))
        if counted >= lost:
            rep["lossClass"] = "counted shed: %d" % counted
        else:
            rep["lossClass"] = "UNEXPLAINED LOSS: lost %d, counted shed %d" % (lost, counted)
            unexplained = True
    classes = [r["lossClass"] for r in verdict["reports"].values() if isinstance(r, dict) and r.get("lossClass")]
    if classes:
        loss_class = "; ".join(classes)
    verdict["lossClass"] = loss_class
    verdict["unexplainedLoss"] = unexplained
    if unexplained:
        warnings.append("UNEXPLAINED LOSS (%s): PUBACKed events are missing and no shed counter accounts for them; this is a durability alarm, not noise" % loss_class)

    # --- broker stalls and leader changes, against the measured window
    w0 = int(pre["epoch"] or 0)
    w1 = int(open(os.path.join(raw, "post", "end.epoch")).read().strip()) if os.path.exists(os.path.join(raw, "post", "end.epoch")) else w0 + int(window)
    eps = stall_episodes(os.path.join(raw, "nats-events.log"), w0, w1)
    info["stalls"] = eps
    stall_in_window = bool(eps) and any(e["inWindow"] for e in eps)
    if eps:
        for e in eps:
            warnings.append("broker %s at %+d..%+d s relative to the window start (%d log lines)%s" % (
                e["kind"], e["startRelS"], e["endRelS"], e["lines"], ": INSIDE the window" if e["inWindow"] else ", outside the window"))
    excluded = None
    if stall_in_window:
        excluded = "a broker stall or leader change fell inside the measured window"
    if not verdict["passed"]:
        excluded = ((excluded + "; ") if excluded else "") + "harness verdict failed" + (" (%s)" % loss_class if loss_class else "")

    layout = layout_of(raw)
    summary = {"schema": SCHEMA, "meta": meta, "info": info, "metrics": m, "verdict": verdict, "layout": layout, "warnings": warnings, "excluded": excluded}
    with open(os.path.join(d, "summary.json"), "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2, sort_keys=True)
    with open(os.path.join(d, "summary.md"), "w", encoding="utf-8") as f:
        f.write(render_md(summary))
    sys.stdout.write(render_md(summary))
    if unexplained:
        return 4
    return 0 if verdict["passed"] and not stall_in_window else 1


def layout_of(raw):
    """Leaders per server, from /jsz of the run (after any rebalance)."""
    out = {}
    for label in ("run", "before-rebalance"):
        p = os.path.join(raw, "layout-%s.json" % label)
        if not os.path.exists(p):
            continue
        try:
            j = load(p)
        except (ValueError, OSError):
            continue
        streams, consumers = {}, {}
        for acc in j.get("account_details") or []:
            for s in acc.get("stream_detail") or []:
                lead = (s.get("cluster") or {}).get("leader") or "?"
                streams[lead] = streams.get(lead, 0) + 1
                for c in s.get("consumer_detail") or []:
                    cl = (c.get("cluster") or {}).get("leader") or "?"
                    consumers[cl] = consumers.get(cl, 0) + 1
        out[label] = {"stream_leaders": streams, "consumer_leaders": consumers}
    return out


def fmt(v):
    if abs(v) >= 1000:
        return "%.0f" % v
    if abs(v) >= 10:
        return "%.1f" % v
    return "%.3f" % v


def render_md(s):
    m, meta, info = s["metrics"], s["meta"], s["info"]
    L = ["# perf-ab: arm `%s`" % meta.get("arm"), ""]
    L.append("Window %.0f s, %d events persisted; offered %.0f events/s (mqtt %s + http %s), achieved %.0f events/s." % (
        info["window_s"], info["events"], num(meta.get("offered")), meta.get("offeredMqtt"), meta.get("offeredHttp"), m["rate.achieved_eps"]))
    L.append("")
    v = s["verdict"]
    L.append("Harness verdict: **%s**. lost=%d duplicated=%d. %s" % ("PASS" if v["passed"] else "FAIL", m["loss.lost_events"], m["loss.duplicated_events"],
                                                                     json.dumps(v["reports"], sort_keys=True)))
    if s.get("excluded"):
        L.append("")
        L.append("**EXCLUDED from replicates: %s**" % s["excluded"])
    if meta.get("tier"):
        L.append("")
        L.append("Sim tier `%s`: ingest ceiling %s/s, burst %s." % (meta["tier"], meta.get("tierIngestPerS"), meta.get("tierBurst")))
    L.append("")
    L += ["| metric | per event |", "|---|---:|"]
    for k in sorted(m):
        if k.startswith(("rate.", "loss.")):
            continue
        L.append("| %s | %s |" % (k, fmt(m[k])))
    L.append("")
    L.append("CPU cores (mean over the window): " + ", ".join("%s %.2f" % kv for kv in info.get("cpu_cores", {}).items()))
    if info.get("nats_server_share"):
        L.append("NATS message share by server: " + json.dumps(info["nats_server_share"], sort_keys=True))
    for key, label in (("stream_appends_per_event", "Stream appends per event"), ("consumer_deliveries_per_event", "Consumer deliveries per event")):
        if info.get(key):
            L.append("%s: %s" % (label, json.dumps(info[key], sort_keys=True)))
    if s.get("layout"):
        L.append("Leader layout: " + json.dumps(s["layout"], sort_keys=True))
    for name, rows in info.get("top_statements", {}).items():
        L += ["", "Top statements, %s (by time):" % name]
        for r in rows[:5]:
            L.append("- calls=%d rows/call=%s ms=%s wal=%s `%s`" % (r["calls"], r["rows_per_call"], r["ms"], r["wal_bytes"], r["q"][:110]))
    if s["warnings"]:
        L += ["", "Warnings:"] + ["- " + w for w in s["warnings"]]
    return "\n".join(L) + "\n"


# ---------------------------------------------------------------------------
# noise and compare
# ---------------------------------------------------------------------------
def mean(xs):
    return sum(xs) / len(xs)


def stdev(xs):
    if len(xs) < 2:
        return 0.0
    mu = mean(xs)
    return math.sqrt(sum((x - mu) ** 2 for x in xs) / (len(xs) - 1))


def arm_values(arg):
    """-> (label, [summary,...], {metric: [values]})"""
    paths = []
    for part in arg.split(","):
        part = part.strip()
        if os.path.isdir(part):
            part = os.path.join(part, "summary.json")
        paths.append(part)
    sums = []
    for p in paths:
        s = load(p)
        # A run whose harness verdict failed (a PUBACKed event lost, state not caught up) was not a
        # like-for-like window: a stall or a loss moves every per-event count. It is left out, loudly.
        bad = s.get("excluded") or (None if (s.get("verdict") or {}).get("passed", True) else "its harness verdict FAILED")
        if bad and not INCLUDE_FAILED:
            sys.stderr.write("perf-ab: leaving out %s: %s\n" % (p, bad))
            continue
        sums.append(s)
    if not sums:
        raise SystemExit("perf-ab: no run in %r has a passing harness verdict" % arg)
    vals = {}
    for s in sums:
        for k, v in s["metrics"].items():
            vals.setdefault(k, []).append(num(v))
    return sums[0]["meta"].get("arm", paths[0]), sums, vals


def noise_stats(vals, floor, conf=DEFAULT_CONF):
    out = {}
    for k, xs in vals.items():
        if len(xs) < 2:
            continue
        mu = mean(xs)
        n = len(xs)
        if abs(mu) < TINY:
            out[k] = {"mean": mu, "sd": stdev(xs), "min": min(xs), "max": max(xs), "rel_sd_pct": 0.0, "band_pct": None, "n": n}
            continue
        raw = 100.0 * t_quantile(conf, n) * stdev(xs) * math.sqrt(1.0 + 1.0 / n) / abs(mu)
        fl = max(floor, CPU_FLOOR_PCT) if k.startswith("cpu.") else floor
        out[k] = {"mean": mu, "sd": stdev(xs), "min": min(xs), "max": max(xs), "rel_sd_pct": 100.0 * stdev(xs) / abs(mu),
                  "raw_band_pct": raw, "floor_pct": fl, "band_pct": max(fl, raw), "n": n}
    return out


def cmd_noise(argv):
    floor, conf = DEFAULT_FLOOR_PCT, DEFAULT_CONF
    while argv and argv[0] in ("--floor", "--confidence"):
        if argv[0] == "--floor":
            floor = float(argv[1])
        else:
            conf = int(argv[1])
            if conf not in T_TABLE:
                sys.stderr.write("--confidence must be one of %s\n" % sorted(T_TABLE))
                return 2
        argv = argv[2:]
    if len(argv) < 3:
        sys.stderr.write("usage: noise [--floor PCT] [--confidence 95|99] OUT.json SUMMARY.json SUMMARY.json [...]  (at least two replicates; three or more to mean anything)\n")
        return 2
    out, paths = argv[0], argv[1:]
    _, sums, vals = arm_values(",".join(paths))
    if len(sums) < 3:
        sys.stderr.write("perf-ab: only %d passing replicate(s): the band from fewer than three is too wide to use or too lucky to trust\n" % len(sums))
    st = noise_stats(vals, floor, conf)
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"schema": SCHEMA, "replicates": len(sums), "floor_pct": floor, "confidence": conf, "metrics": st}, f, indent=2, sort_keys=True)
    sys.stdout.write("noise from %d replicates: band = max(%.1f%%, %d%% prediction interval of one new run)\n\n" % (len(sums), floor, conf))
    sys.stdout.write("| metric | mean | sd | min | max | sd % | band % |\n|---|---:|---:|---:|---:|---:|---:|\n")
    for k in sorted(st):
        s = st[k]
        sys.stdout.write("| %s | %s | %s | %s | %s | %s | %s |\n" % (
            k, fmt(s["mean"]), fmt(s["sd"]), fmt(s["min"]), fmt(s["max"]), "%.1f" % s["rel_sd_pct"], "-" if s["band_pct"] is None else "%.1f" % s["band_pct"]))
    return 0


def floor_of(k, floor):
    return max(floor, CPU_FLOOR_PCT) if k.startswith("cpu.") else floor


def cmd_compare(argv):
    global INCLUDE_FAILED
    noise_path, floor = None, DEFAULT_FLOOR_PCT
    args = []
    i = 0
    while i < len(argv):
        if argv[i] == "--include-failed":
            INCLUDE_FAILED, i = True, i + 1
        elif argv[i] == "--noise":
            noise_path, i = argv[i + 1], i + 2
        elif argv[i] == "--floor":
            floor, i = float(argv[i + 1]), i + 2
        else:
            args.append(argv[i])
            i += 1
    if len(args) < 2:
        sys.stderr.write("usage: compare [--noise noise.json] [--floor PCT] [--include-failed] BASELINE ARM [ARM...]\n")
        return 2
    base_label, base_sums, base_vals = arm_values(args[0])
    bands, raws = {}, {}
    n_base = len(base_sums)
    source = "floor %.1f%%" % floor
    if noise_path:
        nz = load(noise_path)
        bands = {k: v["band_pct"] for k, v in nz["metrics"].items() if v.get("band_pct") is not None}
        raws = {k: v.get("raw_band_pct", v["band_pct"]) for k, v in nz["metrics"].items() if v.get("band_pct") is not None}
        n_base = nz.get("replicates", n_base)
        source = "%s (%d replicates)" % (noise_path, nz.get("replicates", 0))
    elif len(base_sums) >= 2:
        st = noise_stats(base_vals, floor)
        bands = {k: v["band_pct"] for k, v in st.items() if v["band_pct"] is not None}
        raws = {k: v["raw_band_pct"] for k, v in st.items() if v["band_pct"] is not None}
        source = "the %d baseline replicates" % len(base_sums)
    flagged = 0
    layouts = {}
    for s in base_sums:
        layouts[base_label] = s.get("layout", {}).get("run")
    for arg in args[1:]:
        label, sums, vals = arm_values(arg)
        for s in sums:
            layouts[label] = s.get("layout", {}).get("run")
        L = ["", "## %s against %s   (noise band from %s)" % (label, base_label, source), "",
             "| metric | %s | %s | delta | band | |" % (base_label, label), "|---|---:|---:|---:|---:|---|"]
        for k in sorted(set(base_vals) & set(vals)):
            a, b = mean(base_vals[k]), mean(vals[k])
            if abs(a) < TINY and abs(b) < TINY:
                continue
            if k.startswith("rate.offered"):
                continue
            d = 100.0 * (b - a) / abs(a) if abs(a) >= TINY else float("inf")
            band = bands.get(k, floor_of(k, floor))
            if len(sums) > 1 and k in raws:
                # The noise is single-run noise. A test arm averaged over k runs against a baseline
                # of n runs has variance (1/k + 1/n) sigma^2, against (1 + 1/n) for one run.
                band = max(floor_of(k, floor), raws[k] * math.sqrt((1.0 / len(sums) + 1.0 / n_base) / (1.0 + 1.0 / n_base)))
            mark = ""
            if abs(d) > band and not k.startswith(INFO_ONLY):
                mark, flagged = "CHANGED", flagged + 1
            if k.startswith(INFO_ONLY):
                mark = "(info)"
            elif k not in bands:
                mark = (mark + " (no replicate band: floor)").strip()
            L.append("| %s | %s | %s | %+.1f%% | %.1f%% | %s |" % (k, fmt(a), fmt(b), d, band, mark))
        sys.stdout.write("\n".join(L) + "\n")
        for s in sums:
            for w in s.get("warnings", []):
                sys.stdout.write("warning (%s): %s\n" % (label, w))
            if not s["verdict"]["passed"]:
                sys.stdout.write("WARNING (%s): the harness verdict FAILED for a run in this arm\n" % label)
    ref = layouts.get(base_label)
    for lab, lo in layouts.items():
        if lo is not None and ref is not None and sorted((lo.get("stream_leaders") or {}).values()) != sorted((ref.get("stream_leaders") or {}).values()):
            sys.stdout.write("WARNING: the stream leader layout of %s differs from the baseline's; message counts depend on it\n" % lab)
    sys.stdout.write("\n%d metric(s) outside the noise band\n" % flagged)
    return 1 if flagged else 0


# ---------------------------------------------------------------------------
# self-test: the maths against a synthetic raw tree, including the refusals
# ---------------------------------------------------------------------------
def self_test():
    rc = 0
    with tempfile.TemporaryDirectory() as tmp:
        arm = os.path.join(tmp, "arm")
        for when, scale in (("pre", 0), ("post", 1)):
            d = os.path.join(arm, "raw", when)
            os.makedirs(d)
            for i in (0, 1):
                pod = "dc-nats-%d" % i
                base = scale * 1000
                with open(os.path.join(d, "nats-%s-varz.json" % pod), "w") as f:
                    json.dump({"server_id": "s%d" % i, "in_msgs": 500 + base * 10, "out_msgs": 500 + base * 10, "in_bytes": base * 1000, "out_bytes": base * 1000,
                               "jetstream": {"stats": {"api": {"total": base // 10}}}}, f)
                with open(os.path.join(d, "nats-%s-routez.json" % pod), "w") as f:
                    json.dump({"routes": [{"rid": 1, "in_msgs": base * 4, "out_msgs": base * 4, "in_bytes": base * 100, "out_bytes": base * 100}]}, f)
                with open(os.path.join(d, "nats-%s-connz.json" % pod), "w") as f:
                    json.dump({"connections": [{"cid": 7, "in_msgs": base * 2, "out_msgs": base * 3, "in_bytes": 0, "out_bytes": 0}]}, f)
            stmts = [{"q": "INSERT INTO events (a,b) VALUES ($1,$2),($3,$4)", "id": "1", "db": "x", "calls": 100 * scale, "rows": 3200 * scale, "ms": 5 * scale, "walb": 0, "walr": 0},
                     {"q": "INSERT INTO device_states (a) VALUES ($1) ON CONFLICT", "id": "2", "db": "x", "calls": 50 * scale, "rows": 1600 * scale, "ms": 1, "walb": 0, "walr": 0}]
            for name in ("tsdb", "rdb"):
                with open(os.path.join(d, "db-%s.json" % name), "w") as f:
                    json.dump({"t": 1000 + 100 * scale, "lsn": 5000 + 3200 * 2000 * scale, "inRecovery": False, "wal": {"wal_records": 10 + 6400 * scale},
                               "ckptTimed": 5 + scale, "ckptReq": 1 + 2 * scale,
                               "reset": "r", "dealloc": 0, "stmts": stmts if name == "tsdb" else []}, f)
            with open(os.path.join(d, "cadvisor-n1.prom"), "w") as f:
                f.write('container_cpu_usage_seconds_total{container="svc",namespace="dci",pod="device-state-abc12345-xyz12"} %d 1\n' % (10 + 32 * scale))
                f.write('container_cpu_usage_seconds_total{container="svc",namespace="dci",pod="perf-ab-loadgen"} %d 1\n' % (10 + 99 * scale))
            with open(os.path.join(d, "pods.json"), "w") as f:
                json.dump([], f)
            with open(os.path.join(d, "tables-tsdb.json"), "w") as f:
                json.dump({"app": [{"t": "event-management/events", "i": 3200 * scale, "u": 0, "h": 0, "d": 0, "live": 3200 * scale, "dead": 0, "av": 0, "aa": scale, "vc": 0}]}, f)
            with open(os.path.join(d, "tables-rdb.json"), "w") as f:
                json.dump({"app": [{"t": "device-state/device_states", "i": 100 * scale, "u": 4000 * scale, "h": 3000 * scale, "d": 0, "live": 1000, "dead": 120 * scale, "av": 2 * scale, "aa": scale, "vc": 0},
                                   {"t": "device-management/devices", "i": 0, "u": 10 * scale, "h": 0, "d": 0, "live": 50, "dead": 0, "av": 0, "aa": 0, "vc": 0}]}, f)
            with open(os.path.join(d, "db-instances.json"), "w") as f:
                json.dump([{"n": "dc-tsdb-1", "ns": "dci", "cluster": "dc-tsdb", "store": "tsdb", "role": "primary"},
                           {"n": "dc-tsdb-2", "ns": "dci", "cluster": "dc-tsdb", "store": "tsdb", "role": "replica"},
                           {"n": "dc-rdb-1", "ns": "dc-system", "cluster": "dc-rdb", "store": "rdb", "role": "primary"},
                           {"n": "dc-rdb-2", "ns": "dc-system", "cluster": "dc-rdb", "store": "rdb", "role": "replica"}], f)
            with open(os.path.join(d, "cadvisor-n2.prom"), "w") as f:
                for pod, cpu in (("dc-tsdb-1", 64), ("dc-tsdb-2", 16), ("dc-rdb-1", 8), ("dc-rdb-2", 4)):
                    ns_ = "dc-system" if "rdb" in pod else "dci"
                    f.write('container_cpu_usage_seconds_total{container="postgres",namespace="%s",pod="%s"} %d 1\n' % (ns_, pod, 10 + cpu * scale))
                    f.write('container_cpu_usage_seconds_total{container="plugin-barman-cloud",namespace="%s",pod="%s"} %d 1\n' % (ns_, pod, 5 + 40 * scale))
        with open(os.path.join(arm, "meta.json"), "w") as f:
            json.dump({"arm": "t", "offered": 32, "offeredMqtt": 32, "offeredHttp": 0}, f)
        with open(os.path.join(arm, "raw", "report-mqtt.json"), "w") as f:
            json.dump({"drive": {"accepted": 3200, "failed": 0}, "persistedEvents": 3200, "invariants": [{"name": "x", "passed": True}]}, f)
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            summarize(arm)
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        s = load(os.path.join(arm, "summary.json"))["metrics"]
        want = {"nats.total_msgs_per_event": (2 * 2 * 10000) / 3200.0, "nats.client_in_msgs_per_event": 2 * 2000 / 3200.0,
                "nats.nonclient_out_msgs_per_event": (2 * 10000 - 2 * 3000) / 3200.0, "nats.route_in_msgs_per_event": 2 * 4000 / 3200.0,
                "db.tsdb.wal_bytes_per_event": 2000.0, "stmt.events_insert.rows_per_call": 32.0, "stmt.device_states_upsert.rows_per_call": 32.0,
                "cpu.device-state.s_per_1k_events": 32 / 3.2, "rate.achieved_eps": 32.0, "nats.js_api_calls_per_event": 2 * 100 / 3200.0,
                # the S0 additions: checkpoint cause, HOT ratio, dead tuples, autovacuum, DB CPU by role (postgres container only)
                "db.tsdb.checkpoints_timed": 1.0, "db.tsdb.checkpoints_requested": 2.0,
                "db.rdb.hot_ratio": 3000 / 4010.0, "db.rdb.hot_ratio.device-state/device_states": 0.75, "db.rdb.dead_tuples": 120.0,
                "db.rdb.dead_tuples.device-state/device_states": 120.0, "db.rdb.autovacuum_runs": 2.0, "db.rdb.autoanalyze_runs": 1.0,
                "db.rdb.tuple_updates_per_event": 4010 / 3200.0, "db.tsdb.autoanalyze_runs": 1.0,
                "cpu.dbinst.tsdb.primary.s_per_1k_events": 64 / 3.2, "cpu.dbinst.tsdb.standby.s_per_1k_events": 16 / 3.2,
                "cpu.dbinst.rdb.primary.s_per_1k_events": 8 / 3.2, "cpu.dbinst.rdb.standby.s_per_1k_events": 4 / 3.2}
        for k, w in want.items():
            if abs(s.get(k, -1) - w) > 1e-6:
                sys.stderr.write("self-test: %s = %r, want %r\n" % (k, s.get(k), w))
                rc = 1
        if "cpu.perf-ab-loadgen.s_per_1k_events" in s:
            sys.stderr.write("self-test: the loadgen pod's CPU was counted\n")
            rc = 1
        summ = load(os.path.join(arm, "summary.json"))
        # a HOT ratio over a handful of updates is absent, not a number (devices: 10 updates)
        if "db.rdb.hot_ratio.device-management/devices" in s or "db.tsdb.hot_ratio" in s:
            sys.stderr.write("self-test: a HOT ratio was reported over fewer than %d updates\n" % HOT_MIN_UPDATES)
            rc = 1
        if summ["info"].get("checkpoint_cause", {}).get("tsdb") != "timed and requested":
            sys.stderr.write("self-test: checkpoint cause = %r\n" % summ["info"].get("checkpoint_cause"))
            rc = 1
        # the sidecar's CPU is not the database's: only the postgres container is a DB instance
        if abs(sum(v for k, v in s.items() if k.startswith("cpu.dbinst.tsdb.")) - (64 + 16) / 3.2) > 1e-6:
            sys.stderr.write("self-test: a sidecar container was counted as database CPU\n")
            rc = 1
        # NEGATIVE CONTROLS for the instance split and the table counters, each run on a copy of the
        # raw tree and judged by what it must REFUSE to say.
        raw_ = os.path.join(arm, "raw")
        inst_p = os.path.join(raw_, "post", "db-instances.json")
        keep_inst = open(inst_p).read()
        swapped = json.loads(keep_inst)
        swapped[0]["role"], swapped[1]["role"] = "replica", "primary"
        with open(inst_p, "w") as f:
            json.dump(swapped, f)
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            summarize(arm)
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        j2 = load(os.path.join(arm, "summary.json"))
        if any(k.startswith("cpu.dbinst.tsdb.") for k in j2["metrics"]) or not any("changed role" in w for w in j2["warnings"]):
            sys.stderr.write("self-test: a role change inside the window still produced per-instance CPU rows\n")
            rc = 1
        if not any(k.startswith("cpu.dbinst.rdb.") for k in j2["metrics"]):
            sys.stderr.write("self-test: a failover of one store invalidated the other store's rows\n")
            rc = 1
        with open(inst_p, "w") as f:
            f.write(keep_inst)
        tab_p = os.path.join(raw_, "post", "tables-rdb.json")
        keep_tab = open(tab_p).read()
        broken = json.loads(keep_tab)
        broken["app"][0]["h"] = -1  # below the earlier snapshot: the counter went backwards (a stats reset)
        with open(tab_p, "w") as f:
            json.dump(broken, f)
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            summarize(arm)
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        j2 = load(os.path.join(arm, "summary.json"))
        if "db.rdb.hot_ratio" in j2["metrics"] or not any("went backwards" in w for w in j2["warnings"]):
            sys.stderr.write("self-test: a stats reset still produced a HOT ratio\n")
            rc = 1
        os.remove(tab_p)
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            summarize(arm)
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        j2 = load(os.path.join(arm, "summary.json"))
        if "db.rdb.hot_ratio" in j2["metrics"] or not any("no table statistics" in w for w in j2["warnings"]):
            sys.stderr.write("self-test: missing table statistics were not reported as missing\n")
            rc = 1
        with open(tab_p, "w") as f:
            f.write(keep_tab)
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            summarize(arm)
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        # a failed verdict: classified as a counted shed, or as an unexplained loss; and stalls placed against the window
        raw0 = os.path.join(arm, "raw")
        for when, val in (("shed-start", 100), ("shed-end", 2100)):
            os.makedirs(os.path.join(raw0, when), exist_ok=True)
            with open(os.path.join(raw0, when, "shed-event-sources-x.prom"), "w") as f:
                f.write('devicechain_eventsources_total_msg_rate_limited{source="mqtt1",shed_class="a"} %d\n' % val)
                f.write('devicechain_eventsources_total_msg_reading_limited{source="mqtt1"} 0\n')
        with open(os.path.join(raw0, "report-mqtt.json"), "w") as f:
            json.dump({"drive": {"pubacked": 3200}, "storage": {"persisted": 1200, "lost": 2000, "excess": 0},
                       "invariants": [{"name": "puback-durability", "passed": False}]}, f)
        with open(os.path.join(raw0, "nats-events.log"), "w") as f:
            f.write("dc-nats-0 [7] 2026/07/03 23:00:00.5 [WRN] Internal subscription on x took too long: 3s\n")  # long before the window
        with open(os.path.join(raw0, "pre", "start.epoch"), "w") as f:
            f.write("1783123200")  # 2026-07-04 00:00:00 UTC: the window starts here
        with open(os.path.join(raw0, "post", "end.epoch"), "w") as f:
            f.write("1783123300")
        j = {}
        for shed_end, want_class, want_rc in ((2100, "counted shed: 2000", 1), (150, "UNEXPLAINED LOSS", 4)):
            with open(os.path.join(raw0, "shed-end", "shed-event-sources-x.prom"), "w") as f:
                f.write('devicechain_eventsources_total_msg_rate_limited{source="mqtt1",shed_class="a"} %d\n' % shed_end)
            stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
            try:
                got_rc = summarize(arm)
            finally:
                sys.stdout.close()
                sys.stdout = stdout
            j = load(os.path.join(arm, "summary.json"))
            if want_class not in (j["verdict"].get("lossClass") or "") or got_rc != want_rc:
                sys.stderr.write("self-test: loss class %r rc %r, want %r rc %r\n" % (j["verdict"].get("lossClass"), got_rc, want_class, want_rc))
                rc = 1
        if j["info"]["stalls"][0]["inWindow"] or not j.get("excluded"):
            sys.stderr.write("self-test: a stall before the window was placed inside it (or the failed arm was not excluded)\n")
            rc = 1
        with open(os.path.join(raw0, "nats-events.log"), "a") as f:
            f.write("dc-nats-2 [7] 2026/07/04 00:00:30.1 [WRN] JetStream cluster stream 'X' has NO quorum, stalled\n")  # inside
        with open(os.path.join(raw0, "report-mqtt.json"), "w") as f:
            json.dump({"drive": {"pubacked": 3200}, "storage": {"persisted": 3200, "lost": 0, "excess": 0}, "invariants": [{"name": "x", "passed": True}]}, f)
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            got_rc = summarize(arm)
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        j = load(os.path.join(arm, "summary.json"))
        if not any(e["inWindow"] for e in j["info"]["stalls"]) or "inside the measured window" not in (j.get("excluded") or "") or got_rc != 1:
            sys.stderr.write("self-test: a stall inside the window did not exclude the arm (excluded=%r rc=%r)\n" % (j.get("excluded"), got_rc))
            rc = 1
        # the refusal: a pg_stat_statements reset inside the window must invalidate the statement metrics
        for name in ("tsdb",):
            p = os.path.join(arm, "raw", "post", "db-%s.json" % name)
            j = load(p)
            j["reset"] = "other"
            with open(p, "w") as f:
                json.dump(j, f)
        try:
            stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
            try:
                summarize(arm)
                refused = False
            finally:
                sys.stdout.close()
                sys.stdout = stdout
        except SystemExit:
            refused = True
        if not refused:
            sys.stderr.write("self-test: a pg_stat_statements reset did not stop the summary\n")
            rc = 1
        # noise and compare: identical replicates must flag nothing, a 24% change must be flagged
        sums = []
        for i, f in enumerate((1.00, 1.01, 0.99)):
            p = os.path.join(tmp, "s%d.json" % i)
            with open(p, "w") as fh:
                json.dump({"meta": {"arm": "same"}, "metrics": {"nats.total_msgs_per_event": 50 * f, "db.tsdb.wal_bytes_per_event": 2000 * f}, "verdict": {"passed": True}, "warnings": []}, fh)
            sums.append(p)
        changed = os.path.join(tmp, "chg.json")
        with open(changed, "w") as fh:
            json.dump({"meta": {"arm": "chg"}, "metrics": {"nats.total_msgs_per_event": 38, "db.tsdb.wal_bytes_per_event": 2000}, "verdict": {"passed": True}, "warnings": []}, fh)
        same = os.path.join(tmp, "same.json")
        with open(same, "w") as fh:
            json.dump({"meta": {"arm": "same4"}, "metrics": {"nats.total_msgs_per_event": 50.2, "db.tsdb.wal_bytes_per_event": 1990}, "verdict": {"passed": True}, "warnings": []}, fh)
        nz = os.path.join(tmp, "noise.json")
        stdout, sys.stdout = sys.stdout, open(os.devnull, "w")
        try:
            r0 = cmd_noise([nz] + sums)
            r1 = cmd_compare(["--noise", nz, ",".join(sums), same])
            r2 = cmd_compare(["--noise", nz, ",".join(sums), changed])
        finally:
            sys.stdout.close()
            sys.stdout = stdout
        if r0 != 0 or r1 != 0:
            sys.stderr.write("self-test: identical arms were flagged (noise rc=%d compare rc=%d)\n" % (r0, r1))
            rc = 1
        if r2 != 1:
            sys.stderr.write("self-test: a 24%% change was not flagged (rc=%d)\n" % r2)
            rc = 1
    sys.stderr.write("self-test: %s\n" % ("ok" if rc == 0 else "FAILED"))
    return rc


def main(argv):
    if not argv:
        sys.stderr.write(__doc__)
        return 2
    cmd, rest = argv[0], argv[1:]
    if cmd == "summarize":
        if len(rest) != 2 or rest[0] != "--dir":
            sys.stderr.write("usage: summarize --dir <arm dir>\n")
            return 2
        return summarize(rest[1])
    if cmd == "noise":
        return cmd_noise(rest)
    if cmd == "compare":
        return cmd_compare(rest)
    if cmd == "self-test":
        return self_test()
    sys.stderr.write(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
