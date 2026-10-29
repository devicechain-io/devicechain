#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The Towerpulse acceptance checker: judges the PLATFORM's side of a live run, from the platform's own records.

acceptance.sh provisions a fresh sim, launches the player (the 31 devices), and calls this tool for what only the platform can say:

    ready      --runner URL                      wait for the sim to be converged and its config served
    rules      --runner URL --out DIR disable    disable rule R1 (zone-hot) on the VAV profile, publish, prove the engine dropped it
    annotate   --runner URL --recording DIR      write the platform's rule and profile versions into the recording's towerpulse.json
    counters   --out DIR --name before|after     snapshot the calculated-metric counters (kubectl, no credentials)
    segment-a  --runner URL --out DIR --mode control|rule-disabled --since ISO --quit-file F
               the F4-East beat: alarm, boost SENT, SUCCESSFUL only at setpoint, clear (control); or no alarm, no boost, and an overheated room (rule-disabled)
    segment-b  --runner URL --out DIR --since ISO --quit-file F
               the annex chiller beat: silent, rule R4 alarm, `restart` through the API, SUCCESSFUL, back
    common     --runner URL --out DIR --since ISO   31 sessions, telemetry of every type, calculated metrics (needs counters before/after)
    owner      --sim NAME --out DIR                 the annex owner signs in and sees only the annex
    summary    --out DIR --title T                  SUMMARY.md from items.jsonl

Credentials. The runner's /config.json serves a tenant-admin token, and the sim's own record holds the owner's password. Both are read here, into
variables, and used; nothing prints, logs or stores them, every message passes redact(), and the evidence is scanned (leaks()) before it is called done.
Every judgement is a pure function of data (tested by test_acceptance_check.py); only the small shell around them does I/O.
"""

import argparse
import datetime
import json
import os
import re
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

# ---------------------------------------------------------------------------------------------
# the scenario's names (backend/sims/dc-simulator/sim/towerpulse.go, Assets/Towerpulse/Scripts/Plane/Vocabulary.cs)
# ---------------------------------------------------------------------------------------------

HOT_DEVICE = "TP-VAV-T-F4-E"
ANNEX_CHILLER = "TP-CH-A"
ZONE_HOT_RULE = "tp-rule-zone-hot"
VAV_PROFILE = "tp-vav-profile"
PROFILES = ("tp-vav-profile", "tp-ahu-profile", "tp-chiller-profile", "tp-meter-profile")
ZONE_HOT_KEY = "zone-hot"
CHILLER_KEY = "chiller-silent"
BOOST = "boost-cooling"
RESTART = "restart"
HOT_C = 26.0
ABSENCE_S = 120.0
OWNER_BASE = "owner@halden-labs.example"
ANNEX_BOARD = "tp-annex-dashboard"
OPERATOR_BOARD = "tp-dashboard"

DEVICES = (
    [f"TP-VAV-T-F{f}-{d}" for f in range(1, 6) for d in "NESW"][:20]
    + [f"TP-VAV-A-F{f}-{d}" for f in (1, 2) for d in "NS"]
    + ["TP-AHU-T-1", "TP-AHU-T-2", "TP-AHU-A-1", "TP-CH-T", "TP-CH-A", "TP-EM-T", "TP-EM-A"]
)
ANNEX_DEVICES = [d for d in DEVICES if re.match(r"TP-(VAV|AHU|CH|EM)-A\b", d)]
METRICS = {
    "vav": {"temp_c", "setpoint_c", "co2_ppm", "occupancy", "damper_pct"},
    "ahu": {"supply_air_c", "fan_pct", "filter_dp_pa", "mode"},
    "chiller": {"chw_supply_c", "power_kw", "cooling_kw", "running"},
    "meter": {"kw", "kwh"},
}
CALCULATED = {"chiller": "cop", "meter": "kwh_per_m2"}


def kind_of(ext):
    return {"VAV": "vav", "AHU": "ahu", "CH": "chiller", "EM": "meter"}[ext.split("-")[1]]


# ---------------------------------------------------------------------------------------------
# text safety
# ---------------------------------------------------------------------------------------------

_JWT = re.compile(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*")
_HEX = re.compile(r"[0-9A-Fa-f]{32,}")


def redact(text):
    text = _JWT.sub("jwt:…", str(text))
    return _HEX.sub("cred:…", text)


def leaks(text):
    """Credential-shaped content in `text` (a JWT, a long hex run, a password field)."""
    out = []
    if _JWT.search(text):
        out.append("a JWT")
    if _HEX.search(text):
        out.append("a long hex run")
    if re.search(r'"(password|simPassword|token)"\s*:\s*"[^"]{12,}"', text):
        out.append("a credential field")
    return out


# ---------------------------------------------------------------------------------------------
# time
# ---------------------------------------------------------------------------------------------

_FRACTION = re.compile(r"(\.\d+)")


def parse_time(text):
    if text is None:
        return None
    s = text.strip()
    if s[-1] in "Zz":
        s = s[:-1] + "+00:00"
    s = _FRACTION.sub(lambda m: (m.group(1) + "000000")[:7], s, count=1)
    dt = datetime.datetime.fromisoformat(s)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=datetime.timezone.utc)
    return dt.astimezone(datetime.timezone.utc)


def iso(dt):
    return dt.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc)


def say(msg):
    print("[acceptance] " + redact(msg), file=sys.stderr, flush=True)


# ---------------------------------------------------------------------------------------------
# items
# ---------------------------------------------------------------------------------------------


class Item:
    def __init__(self, item_id, description, ok, detail=""):
        self.id, self.description, self.ok, self.detail = item_id, description, ok, redact(detail)

    def as_dict(self):
        return {"id": self.id, "description": self.description, "pass": self.ok, "detail": self.detail}


def overall(items):
    judged = [i for i in items if i.ok is not None]
    return bool(judged) and all(i.ok for i in judged)


def append_items(out, items):
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "items.jsonl"), "a", encoding="utf-8") as f:
        for i in items:
            f.write(json.dumps(i.as_dict()) + "\n")
            say(("PASS " if i.ok else "FAIL " if i.ok is False else "info ") + i.id + ": " + i.detail)


def read_items(out):
    path = os.path.join(out, "items.jsonl")
    if not os.path.exists(path):
        return []
    with open(path, encoding="utf-8") as f:
        return [Item(d["id"], d["description"], d["pass"], d["detail"]) for d in map(json.loads, f) if d]


def summary_md(title, items, context):
    lines = [f"# {title}", ""]
    for k, v in context:
        lines.append(f"- **{k}**: {redact(v)}")
    lines += ["", f"**Result: {'PASS' if overall(items) else 'FAIL'}**", "", "| | Check | Evidence |", "|---|---|---|"]
    for i in items:
        mark = "PASS" if i.ok else ("FAIL" if i.ok is False else "info")
        lines.append(f"| {mark} | {i.description} (`{i.id}`) | {i.detail.replace('|', '/').replace(chr(10), ' ')} |")
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------------------------
# the judgements: pure functions of data
# ---------------------------------------------------------------------------------------------


def alarm_for(alarms, key, device=None):
    """The newest alarm of a key (on a device when given)."""
    rows = [a for a in alarms if a.get("alarmKey") == key and (device is None or a.get("device") == device)]
    return max(rows, key=lambda a: a.get("raisedTime") or "") if rows else None


def judge_alarm_cycle(alarm, severity=None):
    """(raised, cleared, detail) of an alarm row."""
    if alarm is None:
        return False, False, "no alarm"
    sev_ok = severity is None or alarm.get("severity") == severity
    raised = bool(alarm.get("raisedTime")) and sev_ok
    cleared = alarm.get("state") == "CLEARED" and bool(alarm.get("clearedTime"))
    return raised, cleared, f"{alarm.get('alarmKey')} {alarm.get('severity')} raised {alarm.get('raisedTime')}, state {alarm.get('state')}, cleared {alarm.get('clearedTime')}"


def nearest_before(events, name, when):
    """The last stored value of `name` at or before `when` (events: dicts with name, value, occurredTime)."""
    best = None
    for e in events:
        if e["name"] != name:
            continue
        t = parse_time(e["occurredTime"])
        if t <= when and (best is None or t > best[0]):
            best = (t, e["value"])
    return best


def judge_boost_at_setpoint(cmd, temp_events, setpoint_tol=0.4, hot_margin=0.3):
    """
    The command went QUEUED -> SENT -> SUCCESSFUL, took real time between SENT and SUCCESSFUL, and the room was NOT at setpoint when it was
    sent but WAS at setpoint (within `setpoint_tol`) when it was answered. temp_events holds the stored temp_c and setpoint_c events.
    """
    if cmd is None:
        return False, "the platform has no boost command"
    for field in ("queuedTime", "sentTime", "respondedTime"):
        if not cmd.get(field):
            return False, f"status {cmd.get('status')}: {field} is empty"
    if cmd.get("status") != "SUCCESSFUL":
        return False, f"status is {cmd.get('status')}, not SUCCESSFUL"
    q, s, r = (parse_time(cmd[f]) for f in ("queuedTime", "sentTime", "respondedTime"))
    if not q <= s <= r:
        return False, "the times are not in order QUEUED <= SENT <= SUCCESSFUL"
    took = (r - s).total_seconds()
    sp = nearest_before(temp_events, "setpoint_c", r)
    t_sent = nearest_before(temp_events, "temp_c", s)
    t_done = nearest_before(temp_events, "temp_c", r)
    if sp is None or t_sent is None or t_done is None:
        return False, "the stored temperature or setpoint around the command is missing"
    detail = f"SENT {s:%H:%M:%S}Z at {t_sent[1]:.2f} C, SUCCESSFUL {r:%H:%M:%S}Z at {t_done[1]:.2f} C (setpoint {sp[1]:.1f}), {took:.0f} s apart"
    if t_sent[1] <= sp[1] + hot_margin:
        return False, detail + ": the room was already at setpoint when it was sent"
    if t_done[1] > sp[1] + setpoint_tol:
        return False, detail + ": the room was NOT at setpoint when it was answered"
    if took < 20:
        return False, detail + ": answered almost at once, so not by the room reaching setpoint"
    return True, detail


def rolling_average_exceeds(events, name, threshold, window_s=300.0):
    """First time the trailing `window_s` mean of `name` exceeds `threshold` (needs a mostly full window), or None."""
    pts = sorted((parse_time(e["occurredTime"]), e["value"]) for e in events if e["name"] == name)
    for i, (t, _) in enumerate(pts):
        win = [v for (u, v) in pts[: i + 1] if (t - u).total_seconds() <= window_s]
        span = (t - pts[max(0, i - len(win) + 1)][0]).total_seconds() if win else 0
        if span >= window_s * 0.8 and sum(win) / len(win) > threshold:
            return t
    return None


def judge_silence(events, until=None, gap_s=ABSENCE_S):
    """
    The chiller reported before, then stopped: (reported_before, last_before, longest_gap_s). A gap that is still OPEN is measured to `until` (the
    instant the absence alarm was raised, or now): the readings that would close it have not happened yet.
    """
    ts = sorted(parse_time(e["occurredTime"]) for e in events if e["name"] == "chw_supply_c")
    if not ts:
        return False, None, 0.0
    longest, last = 0.0, ts[0]
    for a, b in zip(ts, ts[1:]):
        if (b - a).total_seconds() > longest:
            longest, last = (b - a).total_seconds(), a
    if until is not None and (until - ts[-1]).total_seconds() > longest:
        longest, last = (until - ts[-1]).total_seconds(), ts[-1]
    return True, last, longest


def judge_sessions(connz_text, instance, tenant, want=31):
    """Count the broker's MQTT clients named `<instance>:<tenant>:<device>`."""
    try:
        doc = json.loads(connz_text)
    except ValueError:
        return False, 0, "the broker's connz is not JSON"
    prefix = f"{instance}:{tenant}:"
    ids = {c.get("mqtt_client", "") for c in doc.get("connections", []) if c.get("mqtt_client", "").startswith(prefix)}
    return len(ids) == want, len(ids), f"{len(ids)}/{want} MQTT sessions named {prefix}*"


def judge_telemetry(by_device):
    """by_device: {externalId: set(metric names)}. Every device of every type reported all its metrics."""
    missing = {}
    for ext in DEVICES:
        want = METRICS[kind_of(ext)]
        have = by_device.get(ext, set())
        if not want <= have:
            missing[ext] = sorted(want - have)
    kinds = sorted({kind_of(d) for d in by_device if by_device[d]})
    return not missing and len(by_device) == len(DEVICES), (
        f"{sum(1 for d in DEVICES if METRICS[kind_of(d)] <= by_device.get(d, set()))}/{len(DEVICES)} devices reported every metric of their type; types: {', '.join(kinds)}"
        + (f"; missing: {json.dumps(missing)[:300]}" if missing else "")
    )


def judge_calculated(before, after, power_zero_samples, calc_events):
    """
    The calculated-metric counters moved by `after - before`. missing_input must be 0 (a formula that cannot find its input is a modelling error);
    evaluations must be > 0; every non_finite skip must be explained by a chiller reading with power_kw 0 and cooling_kw 0 that the plane really sent
    (an honest gap: 0/0 is not a number, and the platform stores no cop for it). calc_events: names of the calculated metrics found stored.
    """
    d = {k: after.get(k, 0) - before.get(k, 0) for k in after}
    problems = []
    if d.get("missing_input", 0) != 0:
        problems.append(f"missing_input moved by {d['missing_input']}")
    if d.get("evaluations", 0) <= 0:
        problems.append("no calculated metric was evaluated")
    others = {k: v for k, v in d.items() if k not in ("evaluations", "missing_input", "non_finite") and v}
    if others:
        problems.append(f"other skips: {others}")
    nf = d.get("non_finite", 0)
    if nf > power_zero_samples:
        problems.append(f"{nf} non_finite skips but only {power_zero_samples} chiller reading(s) sent power_kw 0 and cooling_kw 0")
    for name in ("cop", "kwh_per_m2"):
        if name not in calc_events:
            problems.append(f"no stored {name}")
    return not problems, (
        f"evaluations +{d.get('evaluations', 0)}, missing_input +{d.get('missing_input', 0)}, non_finite +{nf} "
        f"(explained by {power_zero_samples} honest 0/0 chiller reading(s)); stored: {', '.join(sorted(calc_events))}" + ("; PROBLEMS: " + "; ".join(problems) if problems else "")
    )


def count_power_zero(samples):
    """Chiller readings the plane sent with power_kw 0 and cooling_kw 0 (device.ndjson sample lines)."""
    n = 0
    for s in samples:
        v = s.get("values") or {}
        if s.get("k") == "sample" and "power_kw" in v and v["power_kw"] == 0 and v.get("cooling_kw") == 0:
            n += 1
    return n


def judge_owner_scope(owner_devices, all_devices_seen_by_operator, boards, tower_events_visible, tower_alarms_visible):
    """The annex owner's view: exactly the annex devices, the annex board and not the operator's, and nothing of the tower."""
    problems = []
    got = set(owner_devices)
    if got != set(ANNEX_DEVICES):
        extra, lack = sorted(got - set(ANNEX_DEVICES)), sorted(set(ANNEX_DEVICES) - got)
        problems.append(f"devices differ from the annex's {len(ANNEX_DEVICES)}: extra {extra}, missing {lack}")
    if len(all_devices_seen_by_operator) != len(DEVICES):
        problems.append(f"the operator sees {len(all_devices_seen_by_operator)} devices, not {len(DEVICES)} (the control for the scope)")
    if ANNEX_BOARD not in boards:
        problems.append(f"{ANNEX_BOARD} is not listed for the owner")
    if OPERATOR_BOARD in boards:
        problems.append(f"{OPERATOR_BOARD} is listed for the owner")
    if tower_events_visible:
        problems.append(f"{tower_events_visible} tower telemetry rows are visible")
    if tower_alarms_visible:
        problems.append(f"{tower_alarms_visible} tower alarms are visible")
    return not problems, (
        f"owner sees {len(got)} devices ({'annex only' if got <= set(ANNEX_DEVICES) else 'MORE THAN THE ANNEX'}); operator sees {len(all_devices_seen_by_operator)}; "
        f"owner's boards: {sorted(boards)}" + ("; PROBLEMS: " + "; ".join(problems) if problems else "")
    )


def judge_control(alarms, commands, temp_events, rule_was_off):
    """
    The rule-disabled control: with R1 off the same day gives NO zone-hot alarm and NO boost, and the room still overheats. The last is what makes
    the absence mean something: the condition the rule exists for was really there.
    """
    items = []
    zone = [a for a in alarms if a.get("alarmKey") == ZONE_HOT_KEY]
    items.append(Item("c-no-alarm", "no zone-hot alarm was raised anywhere", not zone, f"{len(zone)} zone-hot alarm(s)"))
    boosts = [c for c in commands if c.get("name") == BOOST]
    items.append(Item("c-no-boost", "the platform sent no boost-cooling command", not boosts, f"{len(boosts)} boost-cooling command(s)"))
    first = rolling_average_exceeds(temp_events, "temp_c", HOT_C)
    peak = max((e["value"] for e in temp_events if e["name"] == "temp_c"), default=None)
    items.append(Item("c-zone-overheated", f"the room still overheated (stored temperature's 5-minute average above {HOT_C} C)", first is not None,
                      f"first at {iso(first) if first else 'never'}; peak {peak if peak is None else round(peak, 2)} C over {sum(1 for e in temp_events if e['name'] == 'temp_c')} stored readings"))
    items.append(Item("c-rule-was-off", "the rule was off for the whole run (the control is only a control if it was)", rule_was_off, "ruleHealth lacked the rule" if rule_was_off else "ruleHealth still ran the rule"))
    return items


# ---------------------------------------------------------------------------------------------
# platform I/O
# ---------------------------------------------------------------------------------------------


class PlatformError(Exception):
    pass


def _ipv4_only():
    """`localhost` resolves to ::1 first on this machine and the kind ingress listens on IPv4 only, so every request waited ~21 s for the fallback. The
    Host header must stay `localhost` (the ingress routes on it), so it is the resolution that is narrowed, not the URL."""
    original = socket.getaddrinfo

    def v4(host, port, family=0, *args, **kwargs):
        return original(host, port, socket.AF_INET, *args, **kwargs)

    socket.getaddrinfo = v4


_ipv4_only()


def _post(url, token, query, variables):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(url, json.dumps({"query": query, "variables": variables or {}}).encode(), headers)
    try:
        r = json.load(urllib.request.urlopen(req, timeout=60))
    except urllib.error.HTTPError as e:
        raise PlatformError(f"HTTP {e.code}")
    except (urllib.error.URLError, OSError, ValueError) as e:
        raise PlatformError(redact(str(e)))
    if r.get("errors"):
        raise PlatformError(redact(json.dumps(r["errors"])[:400]))
    return r["data"]


class Platform:
    """GraphQL under the runner's tenant-admin token, or (with `session`) a token this object was handed. The token lives in self._tok."""

    def __init__(self, runner=None, session=None):
        self.runner = runner.rstrip("/") if runner else None
        self._tok, self._origin = None, None
        if session:
            self._tok, self._origin = session
        else:
            self.refresh()

    def refresh(self):
        try:
            cfg = json.load(urllib.request.urlopen(self.runner + "/config.json", timeout=10))
            self._tok, self._origin = cfg["token"], cfg["apiOrigin"].rstrip("/")
            self.tenant, self.instance = cfg.get("tenant"), cfg.get("instanceId")
        except (urllib.error.URLError, KeyError, ValueError, OSError) as e:
            raise PlatformError("cannot read the runner's /config.json: " + redact(str(e)))

    def gql(self, area, query, variables=None, retry=True):
        try:
            return _post(f"{self._origin}/api/{area}/graphql", self._tok, query, variables)
        except PlatformError as e:
            if retry and self.runner and "401" in str(e):
                self.refresh()
                return self.gql(area, query, variables, retry=False)
            raise PlatformError(f"{area}: {e}")

    def devices(self):
        out, page = {}, 1
        while True:
            res = self.gql("device-management", "query($c:DeviceSearchCriteria!){devices(criteria:$c){results{token externalId}}}",
                           {"c": {"pageNumber": page, "pageSize": 100}})["devices"]["results"]
            for d in res:
                out[d["externalId"]] = d["token"]
            if len(res) < 100:
                return out
            page += 1

    def events(self, token, since, until=None, size=1000):
        out = []
        for page in range(1, 200):
            crit = {"pageNumber": page, "pageSize": size, "deviceToken": token, "startTime": since}
            if until:
                crit["endTime"] = until
            res = self.gql("event-management", "query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name value occurredTime}}}", {"c": crit})["measurementEvents"]["results"]
            out += res
            if len(res) < size:
                break
        return out

    def alarms(self, since=None):
        out, page = [], 1
        while True:
            crit = {"pageNumber": page, "pageSize": 100}
            if since:
                crit["startTime"] = since
            res = self.gql("device-management",
                           "query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{token alarmKey state severity raisedTime clearedTime originatorToken}}}", {"c": crit})["alarms"]["results"]
            out += res
            if len(res) < 100:
                return out
            page += 1

    CMD_FIELDS = "token name deviceToken status error queuedTime sentTime respondedTime"

    def commands(self, device_token):
        return self.gql("command-delivery", "query($c:CommandSearchCriteria!){commands(criteria:$c){results{%s}}}" % self.CMD_FIELDS,
                        {"c": {"pageNumber": 1, "pageSize": 50, "deviceToken": device_token}})["commands"]["results"]

    def command(self, token):
        rows = self.gql("command-delivery", "query($t:[String!]!){commandsByToken(tokens:$t){%s}}" % self.CMD_FIELDS, {"t": [token]})["commandsByToken"]
        return rows[0] if rows else None

    def create_command(self, device_token, name, token):
        return self.gql("command-delivery", "mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token} rejection{code reason}}}",
                        {"r": {"token": token, "deviceToken": device_token, "name": name}})["createCommand"]

    def profile(self, token):
        rows = self.gql("device-management", "query($t:[String!]!){deviceProfilesByToken(tokens:$t){token activeVersion detectionRules{token enabled}}}", {"t": [token]})["deviceProfilesByToken"]
        return rows[0] if rows else None

    def rule_health(self, profile):
        return self.gql("event-processing", "query($t:String!){ruleHealth(profileToken:$t){ruleToken name status fireCount}}", {"t": profile})["ruleHealth"]


def with_devices(platform, rows):
    """Annotate alarm rows with the external id of their device."""
    by_token = {t: e for e, t in platform.devices().items()}
    return [dict(a, device=by_token.get(a.get("originatorToken"), a.get("originatorToken"))) for a in rows]


# ---------------------------------------------------------------------------------------------
# the cluster (no credentials involved)
# ---------------------------------------------------------------------------------------------


def kube(args, namespace):
    p = subprocess.run(["kubectl", "-n", namespace] + args, capture_output=True, text=True, timeout=60)
    if p.returncode != 0:
        raise PlatformError("kubectl " + " ".join(args[:3]) + ": " + redact(p.stderr[:200]))
    return p.stdout


def counters(namespace):
    text = kube(["exec", "deploy/dc-nats-box", "--", "wget", "-qO-", "http://device-management:8080/metrics"], namespace)
    out = {}
    for line in text.splitlines():
        m = re.match(r'devicechain_devicemanagement_calcmetric_skipped_total\{reason="(\w+)"\}\s+([0-9.e+]+)', line)
        if m:
            out[m.group(1)] = float(m.group(2))
        m = re.match(r"devicechain_devicemanagement_calcmetric_evaluations_total\s+([0-9.e+]+)", line)
        if m:
            out["evaluations"] = float(m.group(1))
    return out


def connz(namespace):
    return kube(["exec", "deploy/dc-nats-box", "--", "wget", "-qO-", "http://dc-nats-headless:8222/connz?limit=10000"], namespace)


# ---------------------------------------------------------------------------------------------
# subcommands
# ---------------------------------------------------------------------------------------------


def cmd_ready(a):
    end = time.time() + a.timeout
    last = ""
    while time.time() < end:
        try:
            p = Platform(a.runner)
            n = len(p.devices())
            if n >= len(DEVICES):
                print(json.dumps({"tenant": p.tenant, "instance": p.instance, "devices": n}))
                return 0
            last = f"{n} devices so far"
        except PlatformError as e:
            last = str(e)
        time.sleep(5)
    say("the sim did not converge: " + last)
    return 1


def cmd_rules(a):
    p = Platform(a.runner)
    state_path = os.path.join(a.out, "rule-state.json")
    os.makedirs(a.out, exist_ok=True)
    prof = p.profile(VAV_PROFILE)
    rule = next((r for r in prof["detectionRules"] if r["token"] == ZONE_HOT_RULE), None) if prof else None
    if rule is None:
        append_items(a.out, [Item("rule-found", "R1 is a rule of the VAV profile", False, "not found")])
        return 1
    with open(state_path, "w") as f:
        json.dump({"profile": VAV_PROFILE, "rule": ZONE_HOT_RULE, "enabled": rule["enabled"], "activeVersion": prof["activeVersion"]}, f)
    h = p.rule_health(VAV_PROFILE)
    before = any(r["ruleToken"] == ZONE_HOT_RULE for r in h)
    p.gql("device-management", "mutation($t:String!,$r:DetectionRuleUpdateRequest!){updateDetectionRule(token:$t,request:$r){token enabled}}", {"t": ZONE_HOT_RULE, "r": {"enabled": False}})
    pub = p.gql("device-management", "mutation($t:String!,$l:String,$d:String){publishDeviceProfile(token:$t,label:$l,description:$d){version}}",
                {"t": VAV_PROFILE, "l": "acceptance rule-disabled control", "d": "disables the zone-hot rule for one control run; the tenant is destroyed afterwards"})["publishDeviceProfile"]
    end, gone = time.time() + 90, False
    while time.time() < end and not gone:
        gone = not any(r["ruleToken"] == ZONE_HOT_RULE for r in p.rule_health(VAV_PROFILE))
        if not gone:
            time.sleep(3)
    others = [r["ruleToken"] for r in p.rule_health(VAV_PROFILE)]
    append_items(a.out, [
        Item("rule-ran-before", "R1 was running before the control disabled it (the positive baseline)", before, f"ruleHealth {'listed' if before else 'did not list'} {ZONE_HOT_RULE}"),
        Item("rule-disabled-effective", "R1 is disabled on the profile and the engine no longer runs it", gone, f"published profile version {pub['version']}; rules still running: {others}"),
    ])
    return 0 if gone else 1


def platform_version(namespace):
    """The platform's version as a semver, from the image tag of the running device-management (a non-release tag becomes 0.0.0-<tag>)."""
    image = kube(["get", "deploy", "device-management", "-o", "jsonpath={.spec.template.spec.containers[0].image}"], namespace).strip()
    tag = image.rsplit(":", 1)[-1].split("@")[0].lstrip("v")
    if re.fullmatch(r"\d+\.\d+\.\d+(-[0-9A-Za-z.-]{1,32})?", tag):
        return tag
    return "0.0.0-" + re.sub(r"[^0-9A-Za-z.-]", "-", tag)[:30]


def stamp_platform_version(recording, namespace):
    """run.json has no field the player can fill for this (it does not know the platform's version); the checker learns it from the cluster."""
    path = os.path.join(recording, "run.json")
    with open(path, encoding="utf-8") as f:
        doc = json.load(f)
    doc["platformVersion"] = platform_version(namespace)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")


def cmd_annotate(a):
    p = Platform(a.runner)
    path = os.path.join(a.recording, "towerpulse.json")
    with open(path, encoding="utf-8") as f:
        doc = json.load(f)
    doc.setdefault("ruleVersions", {})
    doc.setdefault("ruleEnabled", {})
    doc.setdefault("profileVersions", {})
    for tok in PROFILES:
        prof = p.profile(tok)
        if not prof:
            continue
        doc["profileVersions"][tok] = prof["activeVersion"]
        for r in prof["detectionRules"]:
            doc["ruleVersions"][r["token"]] = prof["activeVersion"]  # rules are versioned with the profile that carries them
            doc["ruleEnabled"][r["token"]] = r["enabled"]
    doc["annotatedBy"] = "acceptance_check.py annotate, from the platform's own records, after the run"
    stamp_platform_version(a.recording, a.namespace)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")
    say(f"annotated {a.recording}: {len(doc['ruleVersions'])} rules, {len(doc['profileVersions'])} profiles")
    return 0


def cmd_counters(a):
    os.makedirs(a.out, exist_ok=True)
    c = counters(a.namespace)
    with open(os.path.join(a.out, f"counters-{a.name}.json"), "w") as f:
        json.dump(c, f)
    return 0


def quit_player(path):
    with open(path, "w") as f:
        f.write("done\n")


def gather(p, ext, since):
    return p.events(p.devices()[ext], since)


def cmd_segment_a(a):
    p = Platform(a.runner)
    since = a.since
    hot_tok = p.devices()[HOT_DEVICE]
    deadline = time.time() + a.timeout
    items = []
    rule_off = a.mode == "rule-disabled"
    ok_state = None
    settle_until = None
    while time.time() < deadline:
        alarms = with_devices(p, p.alarms(since))
        cmds = [c for c in p.commands(hot_tok) if c.get("name") == BOOST and c.get("queuedTime") and parse_time(c["queuedTime"]) >= parse_time(since)]
        events = p.events(hot_tok, since)
        if not rule_off:
            al = alarm_for(alarms, ZONE_HOT_KEY, HOT_DEVICE)
            _, cleared, _ = judge_alarm_cycle(al)
            done = [c for c in cmds if c.get("status") == "SUCCESSFUL"]
            if cleared and done:
                break
        else:
            first = rolling_average_exceeds(events, "temp_c", HOT_C)
            if first is not None:
                settle_until = settle_until or (utc_now() + datetime.timedelta(minutes=8))
                if utc_now() >= settle_until:
                    break
        time.sleep(15)
    alarms = with_devices(p, p.alarms(since))
    cmds = [c for c in p.commands(hot_tok) if c.get("name") == BOOST and c.get("queuedTime") and parse_time(c["queuedTime"]) >= parse_time(since)]
    events = p.events(hot_tok, since)
    if not rule_off:
        al = alarm_for(alarms, ZONE_HOT_KEY, HOT_DEVICE)
        raised, cleared, detail = judge_alarm_cycle(al, "MAJOR")
        items.append(Item("a-alarm", "F4-East: R1 raised a MAJOR zone-hot alarm", raised, detail))
        boost = sorted(cmds, key=lambda c: c["queuedTime"])[0] if cmds else None
        items.append(Item("a-boost-sent", "R1 sent boost-cooling to the zone's controller and the platform marked it SENT", bool(boost and boost.get("sentTime")),
                          f"{len(cmds)} boost command(s); first {boost['status'] if boost else '-'} queued {boost and boost['queuedTime']} sent {boost and boost['sentTime']}"))
        ok, detail = judge_boost_at_setpoint(boost, events)
        items.append(Item("a-boost-successful-at-setpoint", "the boost went SUCCESSFUL only when the room reached setpoint", ok, detail))
        items.append(Item("a-alarm-cleared", "the alarm cleared itself", cleared, detail))
    else:
        items += judge_control(alarms, [c for c in cmds], events, rule_off)
    append_items(a.out, items)
    return 0 if overall(items) else 1


def cmd_segment_b(a):
    p = Platform(a.runner)
    since = a.since
    ch_tok = p.devices()[ANNEX_CHILLER]
    deadline = time.time() + a.timeout
    alarm = None
    while time.time() < deadline:
        alarms = with_devices(p, p.alarms(since))
        alarm = alarm_for(alarms, CHILLER_KEY, ANNEX_CHILLER)
        if alarm and alarm.get("state") == "ACTIVE":
            break
        time.sleep(10)
    items = []
    events = p.events(ch_tok, since)
    reported, last, gap = judge_silence(events, parse_time(alarm["raisedTime"]) if alarm and alarm.get("raisedTime") else None)
    raised, _, detail = judge_alarm_cycle(alarm, "CRITICAL")
    items.append(Item("b-silent", "the annex chiller reported, then went silent for at least the rule's two minutes",
                      reported and gap >= ABSENCE_S, f"last reading before the gap {last and iso(last)}, longest gap {gap:.0f} s"))
    items.append(Item("b-r4-alarm", "R4 raised a CRITICAL chiller-silent alarm", raised, detail))
    cmd_token = "accept-restart-" + os.urandom(4).hex()
    cmd = None
    if raised:
        res = p.create_command(ch_tok, RESTART, cmd_token)
        items.append(Item("b-restart-created", "restart was sent through the API (the operator's console path)", bool(res.get("command")), f"rejection: {res.get('rejection')}"))
        end = time.time() + 600
        while time.time() < end:
            cmd = p.command(cmd_token)
            if cmd and cmd.get("status") in ("SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED"):
                break
            time.sleep(10)
        ok = bool(cmd and cmd.get("status") == "SUCCESSFUL" and cmd.get("sentTime") and cmd.get("respondedTime"))
        items.append(Item("b-restart-successful", "restart went QUEUED, SENT, SUCCESSFUL", ok,
                          f"status {cmd and cmd.get('status')}, queued {cmd and cmd.get('queuedTime')}, sent {cmd and cmd.get('sentTime')}, answered {cmd and cmd.get('respondedTime')}"))
        end = time.time() + 240
        back, cleared = False, False
        while time.time() < end:
            events = p.events(ch_tok, since)
            if cmd and cmd.get("respondedTime"):
                back = any(parse_time(e["occurredTime"]) > parse_time(cmd["respondedTime"]) for e in events if e["name"] == "chw_supply_c")
            alarm = alarm_for(with_devices(p, p.alarms(since)), CHILLER_KEY, ANNEX_CHILLER)
            cleared = bool(alarm and alarm.get("state") == "CLEARED")
            if back and cleared:
                break
            time.sleep(10)
        items.append(Item("b-reporting-again", "the chiller reports again after the restart", back, ""))
        items.append(Item("b-alarm-cleared", "the absence alarm cleared when it did", cleared, judge_alarm_cycle(alarm)[2]))
    append_items(a.out, items)
    return 0 if overall(items) else 1


def cmd_common(a):
    p = Platform(a.runner)
    devs = p.devices()
    items = []
    ok, n, detail = judge_sessions(connz(a.namespace), p.instance, p.tenant)
    items.append(Item("sessions", "31/31 devices hold their own MQTT session at the broker", ok, detail))
    by_device, calc_found = {}, set()
    for ext in DEVICES:
        tok = devs.get(ext)
        names = set()
        if tok:
            crit = {"pageNumber": 1, "pageSize": 200, "deviceToken": tok, "startTime": a.since}
            rows = p.gql("event-management", "query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name}}}", {"c": crit})["measurementEvents"]["results"]
            names = {r["name"] for r in rows}
        by_device[ext] = names
        if CALCULATED.get(kind_of(ext)) in names:
            calc_found.add(CALCULATED[kind_of(ext)])
    ok, detail = judge_telemetry(by_device)
    items.append(Item("telemetry", "the platform stored telemetry for every device of every type", ok, detail))
    before = json.load(open(os.path.join(a.out, "counters-before.json")))
    after = counters(a.namespace)
    samples = []
    if a.recording and os.path.exists(os.path.join(a.recording, "device.ndjson")):
        samples = [json.loads(line) for line in open(os.path.join(a.recording, "device.ndjson"), encoding="utf-8") if line.strip()]
    ok, detail = judge_calculated(before, after, count_power_zero(samples), calc_found)
    items.append(Item("calculated", "calculated metrics (cop, kwh_per_m2) are stored and none was skipped as missing_input", ok, detail))
    append_items(a.out, items)
    return 0 if overall(items) else 1


def cmd_calc(a):
    """The calculated-metric check on its own, for a run whose recording holds the readings that explain a skip (the chiller segment)."""
    p = Platform(a.runner)
    d = p.devices()
    found = set()
    for ext in ("TP-CH-A", "TP-EM-A"):
        crit = {"pageNumber": 1, "pageSize": 200, "deviceToken": d[ext], "startTime": a.since}
        rows = p.gql("event-management", "query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name}}}", {"c": crit})["measurementEvents"]["results"]
        found |= {r["name"] for r in rows} & {"cop", "kwh_per_m2"}
    before = json.load(open(os.path.join(a.out, "counters-before.json")))
    samples = [json.loads(line) for line in open(os.path.join(a.recording, "device.ndjson"), encoding="utf-8") if line.strip()]
    ok, detail = judge_calculated(before, counters(a.namespace), count_power_zero(samples), found)
    append_items(a.out, [Item("calculated", "calculated metrics are stored and none was skipped as missing_input; the restart's honest 0/0 reading is the only non_finite", ok, detail)])
    return 0 if ok else 1


def read_sim_record(name):
    path = os.path.join(os.path.expanduser("~"), ".devicechain", "sims", name + ".json")
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def cmd_owner(a):
    rec = read_sim_record(a.sim)
    tenant = rec["tenant"]
    email = OWNER_BASE.replace("@", "+" + tenant + "@")
    user_url = rec["endpoints"]["userGraphQL"]
    pw = rec["simPassword"]
    items = []
    try:
        ident = _post(user_url, None, "mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}", {"e": email, "p": pw})["login"]["identityToken"]
        tok = _post(user_url, None, "mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}", {"i": ident, "t": tenant})["selectTenant"]["accessToken"]
    except PlatformError as e:
        append_items(a.out, [Item("o-signin", "the annex owner signs in as a customer user", False, str(e).replace(pw, "…"))])
        return 1
    items.append(Item("o-signin", "the annex owner signs in as a customer user (login, then selectTenant)", True, f"signed in as {email} to {tenant}"))
    origin = rec["endpoints"]["deviceMgmtGraphQL"].rsplit("/api/", 1)[0]
    owner = Platform(session=(tok, origin))
    operator = Platform(a.runner)
    owner_devs = set(owner.devices())
    op_devs = operator.devices()
    boards_rows = owner.gql("dashboard-management", "query($c:DashboardSearchCriteria!){dashboards(criteria:$c){results{token}}}", {"c": {"pageNumber": 1, "pageSize": 50}})["dashboards"]["results"]
    boards = {r["token"] for r in boards_rows}
    tower_ev = 0
    for ext in ("TP-VAV-T-F4-E", "TP-CH-T", "TP-EM-T"):
        try:
            tower_ev += len(owner.gql("event-management", "query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name}}}",
                                      {"c": {"pageNumber": 1, "pageSize": 5, "deviceToken": op_devs[ext], "startTime": a.since}})["measurementEvents"]["results"])
        except PlatformError:
            pass  # refused is the right answer
    tower_alarms = [x for x in owner.alarms(a.since) if x.get("originatorToken") in {t for e, t in op_devs.items() if e not in ANNEX_DEVICES}]
    ok, detail = judge_owner_scope(owner_devs, op_devs, boards, tower_ev, len(tower_alarms))
    items.append(Item("o-annex-only", "the annex owner sees only the annex: its devices, its board, its alarms", ok, detail))
    append_items(a.out, items)
    return 0 if overall(items) else 1


def cmd_summary(a):
    items = read_items(a.out)
    ctx = []
    cpath = os.path.join(a.out, "context.json")
    if os.path.exists(cpath):
        ctx = list(json.load(open(cpath)).items())
    missing = [r for r in (a.require.split(",") if a.require else []) if r not in {i.id for i in items}]
    if missing:
        # a run that never reached a check has not passed it: the absence is a failure, recorded as one
        items += [Item(m, "required check", False, "never ran") for m in missing]
    text = summary_md(a.title, items, ctx)
    with open(os.path.join(a.out, "SUMMARY.md"), "w", encoding="utf-8") as f:
        f.write(text)
    found = leaks(text)
    if found:
        say("the summary holds " + ", ".join(found))
        return 2
    print("PASS" if overall(items) else "FAIL")
    return 0 if overall(items) else 1


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    def add(cmd_name, fn, **flags):
        sp = sub.add_parser(cmd_name)
        sp.set_defaults(fn=fn)
        for flag, kw in flags.items():
            sp.add_argument("--" + flag.replace("_", "-"), **kw)
        return sp

    runner = {"default": "http://127.0.0.1:8090"}
    out = {"required": True}
    ns = {"default": "dci-tp"}
    add("ready", cmd_ready, runner=runner, timeout=dict(type=float, default=600))
    add("rules", cmd_rules, runner=runner, out=out).add_argument("action", choices=["disable"])
    add("annotate", cmd_annotate, runner=runner, recording=dict(required=True), namespace=ns)
    add("counters", cmd_counters, out=out, name=dict(required=True), namespace=ns)
    add("segment-a", cmd_segment_a, runner=runner, out=out, mode=dict(choices=["control", "rule-disabled"], required=True), since=dict(required=True),
        quit_file=dict(required=False), timeout=dict(type=float, default=2400))
    add("segment-b", cmd_segment_b, runner=runner, out=out, since=dict(required=True), quit_file=dict(required=False), timeout=dict(type=float, default=1500))
    add("common", cmd_common, runner=runner, out=out, since=dict(required=True), recording=dict(default=None), namespace=ns)
    add("calc", cmd_calc, runner=runner, out=out, since=dict(required=True), recording=dict(required=True), namespace=ns)
    add("owner", cmd_owner, runner=runner, sim=dict(required=True), out=out, since=dict(required=True))
    add("summary", cmd_summary, out=out, title=dict(required=True), require=dict(default=""))
    a = ap.parse_args(argv)
    try:
        return a.fn(a)
    except PlatformError as e:
        say("platform error: " + str(e))
        return 3


if __name__ == "__main__":
    sys.exit(main())
