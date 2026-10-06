#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The WSL half of the Sitepulse Phase A acceptance.

phase-a-acceptance.sh launches the player; this script talks to the PLATFORM (through the runner's
operator token, held in a variable and never printed, stored or put in a report) and judges what the
player's own probe wrote beside the player log:

    run      the normal acceptance: storage, commands, corroboration, fuel window
    control  one negative control, shown to fail the right way
    unknown-command   a control that needs no player: a command the profile does not define

Everything that decides a verdict is a pure function above the I/O (tested by test_phase_a_check.py).
Every string that reaches a file or the terminal passes redact(), the same rules as the player's Redactor.
"""

import argparse
import datetime
import hashlib
import json
import os
import re
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

# ---------------------------------------------------------------------------------------------
# redaction (mirrors Scripts/Platform/Redactor.cs: a JWT becomes a short hash, a 32+ hex run its tail)
# ---------------------------------------------------------------------------------------------

_JWT = re.compile(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*")
_HEX = re.compile(r"[0-9A-Fa-f]{32,}")


def redact(text):
    if not text:
        return text
    text = _JWT.sub(lambda m: "jwt:" + hashlib.sha256(m.group(0).encode("utf-8")).hexdigest()[:8], text)
    return _HEX.sub(lambda m: "cred:…" + m.group(0)[-4:], text)


_KEEP = re.compile(r"\[sitepulse\]|Exception|\bERROR\b|\bFAIL|ACCEPTANCE|probe:", re.IGNORECASE)


def filter_log(text):
    """The player log reduced to the lines that matter, each one redacted."""
    return "\n".join(redact(line) for line in text.splitlines() if _KEEP.search(line)) + "\n"


# ---------------------------------------------------------------------------------------------
# time
# ---------------------------------------------------------------------------------------------

_FRACTION = re.compile(r"(\.\d+)")


def parse_time(text):
    """An RFC 3339 instant (any fraction length, Z or an offset) as an aware UTC datetime."""
    if text is None:
        return None
    s = text.strip()
    if s.endswith("Z") or s.endswith("z"):
        s = s[:-1] + "+00:00"

    def trim(m):
        return (m.group(1) + "000000")[:7]

    s = _FRACTION.sub(trim, s, count=1)
    dt = datetime.datetime.fromisoformat(s)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=datetime.timezone.utc)
    return dt.astimezone(datetime.timezone.utc)


def to_ms(dt):
    return int(dt.timestamp()) * 1000 + dt.microsecond // 1000


def iso(dt):
    return dt.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


# ---------------------------------------------------------------------------------------------
# storage: every emitted sample is stored, matched by device and occurred time (never by count)
# ---------------------------------------------------------------------------------------------


def parse_samples(text):
    """The player's emitted-sample log: one JSON object per line. Returns (samples, bad_lines)."""
    samples, bad = [], 0
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            s = json.loads(line)
            s["_time"] = parse_time(s["occurredTime"])
            samples.append(s)
        except (ValueError, KeyError, TypeError):
            bad += 1
    return samples, bad


def _close(a, b, eps):
    return a is not None and b is not None and abs(a - b) <= eps * max(1.0, abs(a), abs(b))


def _index(rows, key_of):
    idx = {}
    for r in rows:
        idx.setdefault(key_of(r), []).append(r)
    return idx


def _near(idx, token, ms, name=None):
    for d in (0, -1, 1):
        for r in idx.get((token, name, ms + d), ()):
            yield r


def match_samples(emitted, stored_measurements, stored_locations, eps=1e-6):
    """
    Match each emitted sample to a stored row by device token and occurred time (within 1 ms; the SDK sends
    100 ns ticks and the platform stores microseconds), and for a measurement by key and value.

    stored_measurements: dicts with deviceToken, name, value, occurredTime
    stored_locations:    dicts with deviceToken, latitude, longitude, elevation, speed, heading, occurredTime

    Returns {"checked", "matched", "missing": [...], "wrong": [...], "location_fields_missing": [...]}.
    A "wrong" sample was found at its time but with a different value; "location_fields_missing" is a stored
    location row that lacks speed, heading or elevation.
    """
    meas = _index(stored_measurements, lambda r: (r["deviceToken"], r["name"], to_ms(parse_time(r["occurredTime"]))))
    locs = _index(stored_locations, lambda r: (r["deviceToken"], None, to_ms(parse_time(r["occurredTime"]))))
    out = {"checked": 0, "matched": 0, "missing": [], "wrong": [], "location_fields_missing": []}
    for s in emitted:
        token, t = s["deviceToken"], to_ms(s["_time"])
        if s["kind"] == "measurement":
            for name, value in s["values"].items():
                out["checked"] += 1
                rows = list(_near(meas, token, t, name))
                if not rows:
                    out["missing"].append(f"{s['device']} {name} @ {iso(s['_time'])}")
                elif not any(_close(value, r["value"], eps) for r in rows):
                    out["wrong"].append(f"{s['device']} {name} @ {iso(s['_time'])}: sent {value}, stored {rows[0]['value']}")
                else:
                    out["matched"] += 1
        else:
            out["checked"] += 1
            rows = list(_near(locs, token, t))
            if not rows:
                out["missing"].append(f"{s['device']} location @ {iso(s['_time'])}")
                continue
            hit = None
            for r in rows:
                if all(_close(s.get(k), r.get(k), 1e-6) for k in ("latitude", "longitude")):
                    hit = r
                    break
            if hit is None:
                out["wrong"].append(f"{s['device']} location @ {iso(s['_time'])}: stored position differs")
                continue
            absent = [k for k in ("speed", "heading", "elevation") if hit.get(k) is None]
            differs = [k for k in ("speed", "heading", "elevation") if hit.get(k) is not None and not _close(s.get(k), hit.get(k), 1e-4)]
            if absent:
                out["location_fields_missing"].append(f"{s['device']} @ {iso(s['_time'])}: no {', '.join(absent)}")
            elif differs:
                out["wrong"].append(f"{s['device']} location @ {iso(s['_time'])}: {', '.join(differs)} differ")
            else:
                out["matched"] += 1
    return out


# ---------------------------------------------------------------------------------------------
# commands
# ---------------------------------------------------------------------------------------------

TERMINAL = {"SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED"}


def judge_success(cmd):
    if cmd is None:
        return False, "no such command on the platform"
    if cmd.get("status") != "SUCCESSFUL":
        return False, f"status {cmd.get('status')}" + (f" ({redact(cmd['error'])})" if cmd.get("error") else "")
    if not cmd.get("respondedTime"):
        return False, "SUCCESSFUL but no respondedTime"
    if not cmd.get("sentTime"):
        return False, "SUCCESSFUL but no sentTime"
    return True, f"SUCCESSFUL, sent {cmd['sentTime']}, responded {cmd['respondedTime']}"


def judge_failed_with_reason(cmd, must_contain=None):
    if cmd is None:
        return False, "no such command on the platform"
    if cmd.get("status") != "FAILED":
        return False, f"status {cmd.get('status')}, expected FAILED"
    err = cmd.get("error") or ""
    if not err.strip():
        return False, "FAILED without a reason"
    if must_contain and must_contain not in err:
        return False, f"FAILED but the reason \"{redact(err)}\" does not say \"{must_contain}\""
    return True, f"FAILED: {redact(err)}"


def judge_rejection(created, code):
    """`created` is the data of createCommand: {command, rejection}. A rejection is data, not an error."""
    if created is None:
        return False, "no answer"
    rej = created.get("rejection")
    if not rej:
        return False, "the command was accepted at enqueue; it should have been rejected" + (
            f" ({created['command']['token']})" if created.get("command") else "")
    if rej.get("code") != code:
        return False, f"rejected with {rej.get('code')} ({redact(rej.get('reason', ''))}), expected {code}"
    return True, f"rejected at enqueue: {code} ({redact(rej.get('reason', ''))})"


def fuel_rises_outside(fuel_events, sent, responded, slack_s=3.0, epsilon=1e-9):
    """
    The stored fuel readings of one machine (dicts with value and occurredTime) may rise only inside the
    refuel command's sent..responded window, widened by slack_s for the two machines' clocks and the time
    the response takes to reach the platform. Returns (rises, outside): every rise as (time, from, to) and
    those outside the window.
    """
    evs = sorted(((parse_time(e["occurredTime"]), e["value"]) for e in fuel_events if e.get("value") is not None), key=lambda x: x[0])
    lo = sent - datetime.timedelta(seconds=slack_s)
    hi = responded + datetime.timedelta(seconds=slack_s)
    rises = [(b[0], a[1], b[1]) for a, b in zip(evs, evs[1:]) if b[1] > a[1] + epsilon]
    return rises, [r for r in rises if not (lo <= r[0] <= hi)]


# ---------------------------------------------------------------------------------------------
# corroboration: the platform's account of a command against the device's own
# ---------------------------------------------------------------------------------------------


def token_tail(token, n=10):
    return token if len(token) <= n else token[-n:]


def rows_of(device_log, machine):
    return device_log.get(machine, [])


def _find(rows, kind, start=0, startswith=None, contains=None):
    for i in range(start, len(rows)):
        r = rows[i]
        if r["kind"] != kind:
            continue
        if startswith is not None and not r["text"].startswith(startswith):
            continue
        if contains is not None and contains not in r["text"]:
            continue
        return i
    return -1


def corroborate(kind, token, rows):
    """
    Does the device's own timeline agree with the platform's outcome for this command?

      success    received, then an outcome row starting SUCCESS
      refuel     received, accepted, service started, service finished, SUCCESS
      superseded received, then a superseded row naming it (or ending its task) -- the platform said FAILED superseded
      refused    a refused row (plant, or a command the device turned away); it never became a task
      absent     no row names the token (rejected at enqueue: it never reached the device)

    Returns (ok, detail).
    """
    tail = token_tail(token)
    named = [i for i, r in enumerate(rows) if tail in r["text"] and r["kind"] == "received"]
    if kind == "absent":
        seen = [r for r in rows if tail in r["text"]]
        return (not seen), ("the device never heard of it" if not seen else f"the device log names it: {seen[0]['kind']} {redact(seen[0]['text'])}")
    if kind == "refused":
        hit = [r for r in rows if r["kind"] == "refused"]
        return (bool(hit)), (f"refused row: {redact(hit[-1]['text'])}" if hit else "no refused row")
    if not named:
        return False, f"no received row names ...{tail}"
    at = named[0]
    if kind == "success":
        j = _find(rows, "outcome", at + 1, startswith="SUCCESS")
        return (j >= 0), ("received, then outcome SUCCESS" if j >= 0 else "received, but no SUCCESS outcome after it")
    if kind == "refuel":
        steps = [("accepted", None, "accepted"), ("refuelling", "service started", "service started"),
                 ("refuelling", "service finished", "service finished"), ("outcome", "SUCCESS", "outcome SUCCESS")]
        for k, prefix, name in steps:
            j = _find(rows, k, at + 1, startswith=prefix)
            if j < 0:
                return False, f"received, but no \"{name}\" row after it"
            at = j
        return True, "received, accepted, service started, service finished, SUCCESS"
    if kind == "superseded":
        j = _find(rows, "superseded", named[0] + 1, contains=tail)
        if j < 0:
            j = _find(rows, "superseded", named[0] + 1)
        return (j >= 0), ("received, then a superseded row" if j >= 0 else "received, but no superseded row after it")
    return False, f"unknown corroboration kind {kind}"


# ---------------------------------------------------------------------------------------------
# the report
# ---------------------------------------------------------------------------------------------


class Item:
    def __init__(self, item_id, description, ok, detail=""):
        self.id, self.description, self.ok, self.detail = item_id, description, ok, redact(detail)

    def as_dict(self):
        return {"id": self.id, "description": self.description, "pass": self.ok, "detail": self.detail}


def overall(items):
    """Passes only when there is at least one item and every item with a verdict passed."""
    judged = [i for i in items if i.ok is not None]
    return bool(judged) and all(i.ok for i in judged)


def summary_md(title, items, context):
    lines = [f"# {title}", ""]
    for k, v in context:
        lines.append(f"- **{k}**: {redact(str(v))}")
    lines += ["", f"**Result: {'PASS' if overall(items) else 'FAIL'}**", "", "| | Item | Detail |", "|---|---|---|"]
    for i in items:
        mark = "PASS" if i.ok else ("FAIL" if i.ok is False else "info")
        lines.append(f"| {mark} | {i.description} (`{i.id}`) | {i.detail.replace('|', '/').replace(chr(10), ' ')} |")
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------------------------
# platform I/O
# ---------------------------------------------------------------------------------------------

MACHINE_PREFIXES = ("HL", "LD", "DZ")
DEVICE_IDS = [f"SP-{k}-{i:04d}" for k in MACHINE_PREFIXES for i in range(1, 7)] + ["SP-PL-0001"]
MACHINE_IDS = [d for d in DEVICE_IDS if d != "SP-PL-0001"]
REFUEL_MACHINE = "SP-HL-0006"


class PlatformError(Exception):
    pass


class Platform:
    """GraphQL over the runner's operator token. The token lives in self._tok and nowhere else."""

    def __init__(self, runner):
        self.runner = runner.rstrip("/")
        self._tok = None
        self._origin = None
        self.refresh()

    def refresh(self):
        try:
            cfg = json.load(urllib.request.urlopen(self.runner + "/config.json", timeout=10))
            self._tok, self._origin = cfg["token"], cfg["apiOrigin"].rstrip("/")
        except (urllib.error.URLError, KeyError, ValueError) as e:
            raise PlatformError("cannot read the runner's /config.json: " + redact(str(e)))

    def gql(self, area, query, variables=None, retry=True):
        req = urllib.request.Request(
            f"{self._origin}/api/{area}/graphql",
            json.dumps({"query": query, "variables": variables or {}}).encode(),
            {"Authorization": "Bearer " + self._tok, "Content-Type": "application/json"},
        )
        try:
            r = json.load(urllib.request.urlopen(req, timeout=60))
        except urllib.error.HTTPError as e:
            if e.code == 401 and retry:
                self.refresh()
                return self.gql(area, query, variables, retry=False)
            raise PlatformError(f"{area}: HTTP {e.code}")
        except (urllib.error.URLError, OSError) as e:
            raise PlatformError(f"{area}: " + redact(str(e)))
        if r.get("errors"):
            raise PlatformError(f"{area}: " + redact(json.dumps(r["errors"])[:400]))
        return r["data"]

    # ---- reads
    def device_tokens(self, external_ids=DEVICE_IDS):
        d = self.gql("device-management", "query($i:[String!]!){devicesByExternalId(externalIds:$i){token externalId}}",
                     {"i": list(external_ids)})["devicesByExternalId"]
        return {x["externalId"]: x["token"] for x in d}

    def events(self, kind, token, since, until=None):
        field = "measurementEvents" if kind == "measurement" else "locationEvents"
        sel = ("name value occurredTime deviceToken" if kind == "measurement"
               else "latitude longitude elevation speed heading occurredTime deviceToken")
        out = []
        for page in range(1, 200):
            crit = {"pageNumber": page, "pageSize": 1000, "deviceToken": token, "startTime": since}
            if until:
                crit["endTime"] = until
            res = self.gql("event-management",
                           "query($c:EventSearchCriteria!){%s(criteria:$c){results{%s}}}" % (field, sel), {"c": crit})[field]["results"]
            out += res
            if len(res) < 1000:
                break
        return out

    def count(self, kind, token, since):
        field = "measurementEvents" if kind == "measurement" else "locationEvents"
        return self.gql("event-management",
                        "query($c:EventSearchCriteria!){%s(criteria:$c){pagination{totalRecords}}}" % field,
                        {"c": {"pageNumber": 1, "pageSize": 1, "deviceToken": token, "startTime": since}})[field]["pagination"]["totalRecords"]

    def latest_locations(self, tokens):
        rows = self.gql("device-state",
                        "query($t:[String!]!){latestLocations(deviceTokens:$t){deviceToken latitude longitude elevation speed heading occurredTime}}",
                        {"t": list(tokens)})["latestLocations"]
        return {r["deviceToken"]: r for r in rows}

    CMD_FIELDS = "token name deviceToken status error queuedTime sentTime respondedTime"

    def create_command(self, device_token, name, payload=None, token=None):
        token = token or f"accept-{name}-{os.urandom(4).hex()}"
        data = self.gql("command-delivery",
                        "mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token} rejection{code reason}}}",
                        {"r": {"token": token, "deviceToken": device_token, "name": name,
                               "payload": None if payload is None else json.dumps(payload)}})["createCommand"]
        data["token"] = token
        return data

    def commands_by_token(self, tokens):
        if not tokens:
            return {}
        rows = self.gql("command-delivery", "query($t:[String!]!){commandsByToken(tokens:$t){%s}}" % self.CMD_FIELDS,
                        {"t": list(tokens)})["commandsByToken"]
        return {r["token"]: r for r in rows}

    def commands_of(self, device_token):
        return self.gql("command-delivery",
                        "query($c:CommandSearchCriteria!){commands(criteria:$c){results{%s}}}" % self.CMD_FIELDS,
                        {"c": {"pageNumber": 1, "pageSize": 20, "deviceToken": device_token}})["commands"]["results"]


# ---------------------------------------------------------------------------------------------
# the player's files
# ---------------------------------------------------------------------------------------------

RESULT = "phaseA-result.json"
SAMPLES = "phaseA-samples.jsonl"
FINISH = "phaseA-finish"


def read_result(d):
    try:
        with open(os.path.join(d, RESULT), encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def wait_result(d, ok, timeout, step=2.0):
    """Polls the result file until ok(result) or the result is final; returns the last result read (or None)."""
    end = time.time() + timeout
    last = None
    while time.time() < end:
        r = read_result(d)
        if r is not None:
            last = r
            if ok(r) or r.get("final"):
                return r
        time.sleep(step)
    return last


def say(msg):
    print(f"[phase-a {time.strftime('%H:%M:%S')}] {redact(msg)}", flush=True)


def wait_commands(platform, tokens, timeout):
    end = time.time() + timeout
    got = {}
    while time.time() < end:
        got = platform.commands_by_token(tokens)
        if all(t in got and got[t]["status"] in TERMINAL for t in tokens):
            break
        time.sleep(3)
    return got


def find_react_refuel(platform, token, since, own_prefix="accept-", timeout=480):
    """The goto-refuel the platform's low-fuel rule sent to the refuel machine: not one we created, queued after `since`."""
    end = time.time() + timeout
    while time.time() < end:
        for c in platform.commands_of(token):
            if c["name"] == "goto-refuel" and not c["token"].startswith(own_prefix) and c.get("queuedTime") \
                    and parse_time(c["queuedTime"]) >= since:
                return c
        time.sleep(5)
    return None


# ---------------------------------------------------------------------------------------------
# the bundle: a leak scan of what the player and the platform produced, and the human summary
# ---------------------------------------------------------------------------------------------


def leaks(text):
    """What in this text would be a credential or an operator token: a 32+ hex run or a JWT shape."""
    found = []
    if _JWT.search(text):
        found.append("a JWT-shaped string")
    if _HEX.search(text):
        found.append("a run of 32 or more hex characters")
    return found


def leakscan(paths):
    """An Item: no file holds a credential-shaped or token-shaped string."""
    bad = []
    for path in paths:
        try:
            with open(path, encoding="utf-8", errors="replace") as f:
                text = f.read()
        except OSError:
            continue
        for what in leaks(text):
            bad.append(f"{os.path.basename(path)}: {what}")
    return Item("evidence-no-secrets", "no file of the bundle holds a credential or token shape (32+ hex run, JWT)", not bad,
                f"{len(paths)} file(s) scanned" + (f"; FOUND {bad}" if bad else "; none found"))


SHORT_SHA = 12


def short_sha(commit):
    """A commit id as the bundle writes it: 12 characters. A full 40-hex SHA is a run the leak scan (rightly) takes
    for a credential, so no commit id is ever written in full."""
    commit = (commit or "").strip()
    return commit[:SHORT_SHA] if re.fullmatch(r"[0-9a-fA-F]{7,}", commit) else (commit or "unknown")


def stamp_build(info_path, commit, tracked_clean, sdk_commit):
    """Record, in build-info.json, what only the WSL side can read: the commit, whether the tracked tree was clean,
    and the SDK's last commit (the Editor runs on Windows and cannot resolve the worktree's git metadata). Unity's own
    fields (version, backend, stripping, build time) are kept as they are."""
    with open(info_path, encoding="utf-8") as f:
        info = json.load(f)
    info.pop("git", None)
    info["gitSha"] = short_sha(commit)
    info["trackedTreeClean"] = bool(tracked_clean)
    info["sdkCommit"] = short_sha(sdk_commit)
    info["gitStampedBy"] = "tools/phase-a-acceptance.sh"
    with open(info_path, "w", encoding="utf-8") as f:
        json.dump(info, f, indent=2)
    return info


def judge_build(info, head, allow_stale=False):
    """(verdict, detail): was the player built from this commit with a clean tracked tree? verdict is PASS, FAIL or info."""
    sha, clean = short_sha(info.get("gitSha")), info.get("trackedTreeClean")
    head = short_sha(head)
    if sha == head and clean is True:
        return "PASS", sha
    what = f"built at {sha} (tracked tree clean: {clean}), HEAD is {head}"
    if allow_stale:
        return "info", f"NOT: {what}; allowed by --allow-stale-build"
    return "FAIL", what


def read_script_items(path):
    """tab-separated: PASS|FAIL|info, id, description, detail"""
    items = []
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                parts = line.rstrip("\n").split("\t")
                if len(parts) < 3:
                    continue
                verdict = {"PASS": True, "FAIL": False}.get(parts[0])
                items.append(Item(parts[1], parts[2], verdict, parts[3] if len(parts) > 3 else ""))
    except OSError:
        pass
    return items


def bundle(d, header_path):
    sections = []
    script = read_script_items(os.path.join(d, "script-items.tsv"))
    if script:
        sections.append(("Build, environment and evidence", script))
    names = sorted(n for n in os.listdir(d) if n.startswith("checker-") and n.endswith(".json"))
    names.sort(key=lambda n: (n.startswith("checker-control"), n))
    for n in names:
        with open(os.path.join(d, n), encoding="utf-8") as f:
            doc = json.load(f)
        sections.append((doc["title"], [Item(i["id"], i["description"], i["pass"], i["detail"]) for i in doc["items"]]))
    for c_dir in sorted(os.listdir(os.path.join(d, "controls"))) if os.path.isdir(os.path.join(d, "controls")) else []:
        sub = os.path.join(d, "controls", c_dir)
        for n in sorted(x for x in os.listdir(sub) if x.startswith("checker-") and x.endswith(".json")):
            with open(os.path.join(sub, n), encoding="utf-8") as f:
                doc = json.load(f)
            sections.append((doc["title"], [Item(i["id"], i["description"], i["pass"], i["detail"]) for i in doc["items"]]))
    soak_dir = os.path.join(d, "soak")
    if os.path.isdir(soak_dir):
        for n in sorted(x for x in os.listdir(soak_dir) if x.startswith("checker-") and x.endswith(".json")):
            with open(os.path.join(soak_dir, n), encoding="utf-8") as f:
                doc = json.load(f)
            sections.append((doc["title"], [Item(i["id"], i["description"], i["pass"], i["detail"]) for i in doc["items"]]))
    every = [i for _, items in sections for i in items]
    ok = overall(every)
    header = ""
    if header_path and os.path.exists(header_path):
        with open(header_path, encoding="utf-8") as f:
            header = f.read()
    lines = [header.rstrip("\n"), "", f"## Overall: {'PASS' if ok else 'FAIL'}  ({sum(1 for i in every if i.ok)} passed, "
             f"{sum(1 for i in every if i.ok is False)} failed, {sum(1 for i in every if i.ok is None)} info)", ""]
    for title, items in sections:
        lines += [f"### {title}", "", "| | Item | Detail |", "|---|---|---|"]
        for i in items:
            mark = "PASS" if i.ok else ("FAIL" if i.ok is False else "info")
            lines.append(f"| {mark} | {i.description} (`{i.id}`) | {i.detail.replace('|', '/').replace(chr(10), ' ')} |")
        lines.append("")
    soak_json = os.path.join(soak_dir, "soak.json")
    if os.path.exists(soak_json):
        import phase_b_check as b
        with open(soak_json, encoding="utf-8") as f:
            lines += b.soak_section(json.load(f)) + [""]
    with open(os.path.join(d, "SUMMARY.md"), "w", encoding="utf-8") as f:
        f.write("\n".join(lines))
    with open(os.path.join(d, "report.json"), "w", encoding="utf-8") as f:
        json.dump({"pass": ok, "sections": [{"title": t, "items": [i.as_dict() for i in its]} for t, its in sections]}, f, indent=2)
    print("\n".join(f"{'PASS' if i.ok else ('FAIL' if i.ok is False else 'info')}  {i.id}: {i.detail}" for i in every))
    print("RESULT", "PASS" if ok else "FAIL")
    return 0 if ok else 1


# ---------------------------------------------------------------------------------------------
# the normal run
# ---------------------------------------------------------------------------------------------


def run_phase_a(args, platform, since_dt):
    items, report = [], {"commands": {}, "storage": {}}
    d = args.dir
    since = iso(since_dt)

    say("waiting for the fleet to be observed")
    first = wait_result(d, lambda r: (r.get("timing") or {}).get("observedSeconds") is not None, args.reach_timeout)
    if first is None or first.get("final") and (first.get("timing") or {}).get("observedSeconds") is None:
        items.append(Item("fleet-up", "the player's fleet came up", False,
                          "the player wrote no result" if first is None else f"the player finished early: {first.get('error') or 'fleet never observed'}"))
        return items, report

    tokens = platform.device_tokens()
    missing = [i for i in DEVICE_IDS if i not in tokens]
    items.append(Item("platform-devices", "all 19 scene devices exist on the platform", not missing,
                      f"{len(tokens)}/19" + (f"; missing {missing}" if missing else "")))
    if missing:
        return items, report

    # ---- commands -------------------------------------------------------------------------------
    say("creating commands")
    created = {}
    created["area"] = platform.create_command(tokens["SP-HL-0003"], "goto-area", {"areaToken": "sp-zone-yard"})
    created["older"] = platform.create_command(tokens["SP-HL-0001"], "goto-refuel")
    created["newer"] = platform.create_command(tokens["SP-HL-0001"], "goto-area", {"areaToken": "sp-zone-fill"})
    created["invalid"] = platform.create_command(tokens["SP-HL-0002"], "goto-area", {"areaToken": "sp-zone-moon"})
    created["plant"] = platform.create_command(tokens["SP-PL-0001"], "goto-refuel")
    own = {k: v["token"] for k, v in created.items() if v.get("command")}

    say("waiting for those commands, and for the platform's own goto-refuel on " + REFUEL_MACHINE)
    got = wait_commands(platform, list(own.values()), args.command_timeout)
    react = find_react_refuel(platform, tokens[REFUEL_MACHINE], since_dt, timeout=args.react_timeout)
    if react:
        got.update(wait_commands(platform, [react["token"]], args.command_timeout))
    c = {k: got.get(t) for k, t in own.items()}
    reactc = got.get(react["token"]) if react else None

    def cmd_item(item_id, desc, fn, *a):
        ok, detail = fn(*a)
        items.append(Item(item_id, desc, ok, detail))
        return ok

    cmd_item("cmd-goto-area", "console-style goto-area sp-zone-yard to SP-HL-0003 is SUCCESSFUL with respondedTime", judge_success, c.get("area"))
    cmd_item("cmd-superseded-older", "the older goto-refuel on SP-HL-0001 is FAILED \"superseded by ...\"", judge_failed_with_reason, c.get("older"), "superseded by")
    cmd_item("cmd-superseded-newer", "the newer goto-area sp-zone-fill on SP-HL-0001 is SUCCESSFUL", judge_success, c.get("newer"))
    cmd_item("cmd-invalid-area", "goto-area sp-zone-moon is rejected at enqueue with PAYLOAD_SCHEMA_VIOLATION", judge_rejection, created["invalid"], "PAYLOAD_SCHEMA_VIOLATION")
    cmd_item("cmd-plant", "a command to SP-PL-0001 is FAILED with a reason", judge_failed_with_reason, c.get("plant"), None)
    if react:
        cmd_item("cmd-react-refuel", "the platform rule's goto-refuel on SP-HL-0006 is SUCCESSFUL with respondedTime", judge_success, reactc)
    else:
        items.append(Item("cmd-react-refuel", "the platform rule's goto-refuel on SP-HL-0006 is SUCCESSFUL with respondedTime", False,
                          f"no goto-refuel from the rule appeared within {args.react_timeout} s of the tank being prepared"))
    report["commands"] = {k: v for k, v in {**c, "react": reactc}.items() if v}
    report["commands"]["invalid_rejection"] = created["invalid"].get("rejection")

    # ---- fuel window ----------------------------------------------------------------------------
    if reactc and reactc.get("sentTime") and reactc.get("respondedTime"):
        fuel = [e for e in platform.events("measurement", tokens[REFUEL_MACHINE], since) if e["name"] == "fuel_pct"]
        rises, outside = fuel_rises_outside(fuel, parse_time(reactc["sentTime"]), parse_time(reactc["respondedTime"]), args.slack)
        items.append(Item("fuel-window", "SP-HL-0006's stored fuel rises only inside the refuel command's sent..responded window",
                          bool(rises) and not outside,
                          f"{len(fuel)} fuel samples, {len(rises)} rise(s), {len(outside)} outside the window (+/-{args.slack:g} s)"
                          + (f"; first outside: {iso(outside[0][0])} {outside[0][1]} -> {outside[0][2]}" if outside else "")
                          + ("" if rises else "; it never rose at all")))
    else:
        items.append(Item("fuel-window", "SP-HL-0006's stored fuel rises only inside the refuel command's sent..responded window", False,
                          "there was no completed refuel command to bound the window"))

    # ---- let the player finish --------------------------------------------------------------------
    say("telling the player the platform-side checks are done")
    with open(os.path.join(d, FINISH), "w") as f:
        f.write("done\n")
    final = wait_result(d, lambda r: bool(r.get("final")), args.finish_timeout)
    if final is None or not final.get("final"):
        items.append(Item("probe-final", "the player's probe finished", False, "no final result within the timeout"))
        return items, report
    for pi in final.get("items", []):
        items.append(Item("probe:" + pi["id"], "player probe: " + pi["description"], pi["pass"], pi.get("detail", "")))
    report["probe"] = final

    # ---- corroboration --------------------------------------------------------------------------
    log = final.get("deviceLog", {})
    pairs = [("area", "SP-HL-0003", "success"), ("newer", "SP-HL-0001", "success"), ("older", "SP-HL-0001", "superseded")]
    for key, machine, kind in pairs:
        if c.get(key):
            ok, detail = corroborate(kind, c[key]["token"], rows_of(log, machine))
            items.append(Item(f"device-log-{key}", f"{machine}'s device log agrees with the platform for the {key} command", ok, detail))
    if created["invalid"].get("token"):
        ok, detail = corroborate("absent", created["invalid"]["token"], rows_of(log, "SP-HL-0002"))
        items.append(Item("device-log-invalid", "the rejected command never reached SP-HL-0002", ok, detail))
    if c.get("plant"):
        ok, detail = corroborate("refused", c["plant"]["token"], rows_of(log, "SP-PL-0001"))
        items.append(Item("device-log-plant", "SP-PL-0001's device log shows the refusal", ok, detail))
    if reactc:
        ok, detail = corroborate("refuel", reactc["token"], rows_of(log, REFUEL_MACHINE))
        items.append(Item("device-log-react", "SP-HL-0006's device log agrees with the platform for the rule's goto-refuel", ok, detail))

    # ---- storage ----------------------------------------------------------------------------------
    say("checking that every emitted sample is stored")
    try:
        with open(os.path.join(d, SAMPLES), encoding="utf-8") as f:
            emitted, bad = parse_samples(f.read())
    except OSError as e:
        items.append(Item("storage", "every emitted sample is stored", False, "no emitted-sample log: " + str(e)))
        return items, report
    time.sleep(args.ingest_grace)
    result = None
    deadline = time.time() + args.storage_timeout
    while True:
        stored_m, stored_l = [], []
        for ext, tok in tokens.items():
            stored_m += platform.events("measurement", tok, since)
            if ext != "SP-PL-0001":
                stored_l += platform.events("location", tok, since)
        result = match_samples(emitted, stored_m, stored_l)
        if (not result["missing"] and not result["wrong"]) or time.time() > deadline:
            break
        time.sleep(10)
    per_device = {}
    for s in emitted:
        per_device.setdefault(s["device"], {"measurement": 0, "location": 0})[s["kind"]] += 1
    no_meas = [i for i in DEVICE_IDS if per_device.get(i, {}).get("measurement", 0) == 0]
    no_loc = [i for i in MACHINE_IDS if per_device.get(i, {}).get("location", 0) == 0]
    report["storage"] = {"emitted_samples": len(emitted), "unparseable_lines": bad, "entries_checked": result["checked"],
                         "matched": result["matched"], "missing": result["missing"][:20], "wrong": result["wrong"][:20],
                         "per_device": per_device}
    items.append(Item("storage-every-sample", "every emitted sample is stored, matched by device and occurredTime (and key and value)",
                      bool(emitted) and not result["missing"] and not result["wrong"] and bad == 0 and not no_meas,
                      f"{len(emitted)} samples, {result['checked']} entries checked, {result['matched']} matched, {len(result['missing'])} missing, "
                      f"{len(result['wrong'])} wrong value, {bad} unreadable lines; devices with no measurement sample: {no_meas or 'none'}"
                      + (f"; first missing: {result['missing'][0]}" if result["missing"] else "")))
    locs = latest = None
    try:
        locs = platform.latest_locations([tokens[i] for i in MACHINE_IDS])
    except PlatformError as e:
        latest = str(e)
    lacking = [i for i in MACHINE_IDS if locs is None or any(locs.get(tokens[i], {}).get(k) is None for k in ("speed", "heading", "elevation"))]
    items.append(Item("storage-location-fields", "Location rows carry speed, heading and elevation (stored rows and latestLocations)",
                      not result["location_fields_missing"] and not lacking and not no_loc,
                      f"{len(result['location_fields_missing'])} stored rows lacking a field; latestLocations lacking one: {lacking or 'none'}; "
                      f"machines that emitted no location: {no_loc or 'none'}" + (f"; {latest}" if latest else "")))
    return items, report


# ---------------------------------------------------------------------------------------------
# negative controls
# ---------------------------------------------------------------------------------------------


def run_control(args, platform, since_dt):
    items, report = [], {}
    kind, _, target = args.control.partition(":")
    since = iso(since_dt)
    d = args.dir
    if kind == "runner-stop":
        return run_runner_stop(args, platform, since_dt)
    if kind in ("rule-disabled", "observer-outage"):
        import phase_b_check as b
        return (b.run_rule_disabled if kind == "rule-disabled" else b.run_observer_outage)(args, platform, since_dt)

    say(f"control {args.control}: waiting for the player's verdict")
    final = wait_result(d, lambda r: bool(r.get("final")), args.finish_timeout + 120)
    if final is None or not final.get("final"):
        items.append(Item("control-final", "the control run finished", False, "no final result"))
        return items, report
    report["probe"] = final
    items.append(Item("control-labelled", "the run is labelled as a control in its result file", final.get("control") is True and "CONTROL" in final.get("run", ""),
                      f"run = {final.get('run')}"))
    for pi in final.get("items", []):
        if pi["pass"] is not None:
            items.append(Item("probe:" + pi["id"], "player probe: " + pi["description"], pi["pass"], pi.get("detail", "")))

    # the platform's side of the same story: what it stored from the devices since the control started
    time.sleep(args.ingest_grace)
    tokens = platform.device_tokens()
    counts = {ext: platform.count("measurement", tok, since) for ext, tok in tokens.items()}
    report["measurement_events_since_start"] = counts
    if kind == "wrong-ca":
        stored = {e: n for e, n in counts.items() if n}
        items.append(Item("platform-nothing-stored", "the platform stored nothing from any device (TLS refused, nobody was connected and quiet)",
                          not stored, f"devices with stored events since start: {stored or 'none'}"))
    else:
        quiet = counts.get(target, -1)
        others = {e: n for e, n in counts.items() if e != target}
        silent = [e for e, n in others.items() if n == 0]
        items.append(Item("platform-target-silent", f"the platform stored nothing from {target}", quiet == 0, f"{target}: {quiet} events since start"))
        items.append(Item("platform-others-stored", "the other 18 devices' events were stored", not silent,
                          f"{len(others) - len(silent)}/{len(others)} others have events" + (f"; silent: {silent}" if silent else "")))
    return items, report


def run_runner_stop(args, platform, since_dt):
    items, report = [], {}
    d = args.dir
    since = iso(since_dt)
    say("control runner-stop: waiting for the fleet to be observed")
    first = wait_result(d, lambda r: (r.get("timing") or {}).get("observedSeconds") is not None, args.reach_timeout)
    if first is None or first.get("final"):
        items.append(Item("fleet-up", "the player's fleet came up before the runner was stopped", False, "it did not"))
        return items, report
    tokens = platform.device_tokens()
    time.sleep(5)
    before = {e: platform.count("measurement", t, since) for e, t in tokens.items()}
    say("stopping the runner")
    subprocess.run([args.live_env, "runner", "stop"], check=False, stdout=subprocess.DEVNULL)
    down = True
    try:
        urllib.request.urlopen(args.runner.rstrip("/") + "/status", timeout=3)
        down = False
    except (urllib.error.URLError, OSError):
        pass
    items.append(Item("runner-stopped", "the runner is really down during the window", down, "/status did not answer" if down else "/status still answered"))
    try:
        time.sleep(40)
        after = {e: platform.count("measurement", t, since) for e, t in tokens.items()}   # the token we hold is still valid for ~15 minutes
        stalled = [e for e in tokens if after[e] <= before[e]]
        items.append(Item("platform-still-receiving", "the devices kept publishing and the platform kept storing with the runner down", not stalled,
                          f"{19 - len(stalled)}/19 devices gained events in 40 s" + (f"; stalled: {stalled}" if stalled else "")))
        final = wait_result(d, lambda r: bool(r.get("final")), args.finish_timeout + 120)
        if final is None or not final.get("final"):
            items.append(Item("control-final", "the control run finished", False, "no final result"))
        else:
            report["probe"] = final
            items.append(Item("control-labelled", "the run is labelled as a control in its result file", final.get("control") is True and "CONTROL" in final.get("run", ""),
                              f"run = {final.get('run')}"))
            for pi in final.get("items", []):
                if pi["pass"] is not None:
                    items.append(Item("probe:" + pi["id"], "player probe: " + pi["description"], pi["pass"], pi.get("detail", "")))
    finally:
        say("starting the runner again")
        subprocess.run([args.live_env, "runner", "start"], check=False, stdout=subprocess.DEVNULL)
    ok = False
    try:
        ok = urllib.request.urlopen(args.runner.rstrip("/") + "/status", timeout=5).status == 200
    except (urllib.error.URLError, OSError):
        pass
    items.append(Item("runner-restarted", "the runner answers /status again", ok, ""))
    report["note"] = ("The observer talks to the platform, not the runner, so a stopped runner shows no banner within the operator token's "
                      "lifetime (15 minutes); this control shows observation continues, and does not exercise the token's expiry.")
    return items, report


def run_unknown_command(args, platform, since_dt):
    items = []
    tokens = platform.device_tokens(["SP-HL-0002"])
    created = platform.create_command(tokens["SP-HL-0002"], "goto-nowhere")
    ok, detail = judge_rejection(created, "COMMAND_NOT_IN_VOCABULARY")
    items.append(Item("control-unknown-command", "an unknown command name is rejected at enqueue with COMMAND_NOT_IN_VOCABULARY (data, not an error)", ok, detail))
    return items, {"rejection": created.get("rejection")}


# ---------------------------------------------------------------------------------------------


def restore_mode(args):
    """rule-restore / scale-restore: put back what a control changed. Silent when there is nothing to undo."""
    import phase_b_check as b
    if args.mode == "rule-restore":
        items = b.restore_rule(Platform(args.runner), args.state)
    else:
        items = b.restore_scale(args.live_env, args.state)
    if not items:
        print("nothing to restore")
        return 0
    ok = overall(items)
    out = args.out or os.path.dirname(args.state)
    doc = {"title": f"Sitepulse restore after a control ({args.mode})", "pass": ok, "since": iso(datetime.datetime.now(datetime.timezone.utc)),
           "items": [i.as_dict() for i in items], "report": {}}
    with open(os.path.join(out, f"checker-{args.mode}.json"), "w", encoding="utf-8") as f:
        f.write(redact(json.dumps(doc, indent=2, default=str)))
    for i in items:
        print(f"{'PASS' if i.ok else ('FAIL' if i.ok is False else 'info')}  {i.id}: {i.detail}")
    return 0 if ok else 1


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("mode", choices=["run", "control", "unknown-command", "leakscan", "bundle", "stamp-build", "build-current", "soak", "rule-restore", "scale-restore"])
    p.add_argument("--files", nargs="*", default=[], help="leakscan: the files to scan")
    p.add_argument("--info", help="stamp-build, build-current: the build-info.json")
    p.add_argument("--commit", help="stamp-build: the commit built from (written as 12 characters)")
    p.add_argument("--clean", choices=["true", "false"], help="stamp-build: whether the tracked tree was clean")
    p.add_argument("--sdk", help="stamp-build: the C# SDK's last commit")
    p.add_argument("--head", help="build-current: the current HEAD")
    p.add_argument("--allow-stale", action="store_true", help="build-current: a stale build is recorded, not failed")
    p.add_argument("--header", help="bundle: a file whose text opens SUMMARY.md")
    p.add_argument("--dir", help="the evidence directory the player writes its files into")
    p.add_argument("--runner", default="http://localhost:8090")
    p.add_argument("--since", help="RFC 3339 instant the run began (events before it are not this run's)")
    p.add_argument("--control", help="bogus-binding:<id> | bad-credential:<id> | wrong-ca | runner-stop | rule-disabled | observer-outage")
    p.add_argument("--state", help="rule-restore: the rule-state.json; scale-restore: the scale-pending.tsv")
    p.add_argument("--soak-minutes", type=int, default=30, help="soak: how long the Live run lasts")
    p.add_argument("--outage-seconds", type=float, default=40, help="observer-outage: how long the deployment stays at zero")
    p.add_argument("--outage-deploy", default="event-management", help="observer-outage: the deployment scaled to zero")
    p.add_argument("--live-env", help="path of live-env.sh (runner-stop only)")
    p.add_argument("--out", help="where to write report.json and SUMMARY.md (default --dir)")
    p.add_argument("--reach-timeout", type=float, default=240)
    p.add_argument("--command-timeout", type=float, default=300)
    p.add_argument("--react-timeout", type=float, default=420)
    p.add_argument("--finish-timeout", type=float, default=120)
    p.add_argument("--ingest-grace", type=float, default=10)
    p.add_argument("--storage-timeout", type=float, default=90)
    p.add_argument("--slack", type=float, default=3.0, help="seconds of clock slack on the refuel window")
    args = p.parse_args(argv)

    if args.mode == "bundle":
        return bundle(args.dir, args.header)
    if args.mode == "stamp-build":
        stamp_build(args.info, args.commit, args.clean == "true", args.sdk)
        return 0
    if args.mode == "build-current":
        with open(args.info, encoding="utf-8") as f:
            verdict, detail = judge_build(json.load(f), args.head, args.allow_stale)
        print(verdict + "\t" + detail)
        return 0
    if args.mode == "leakscan":
        item = leakscan(args.files)
        print(("PASS" if item.ok else "FAIL"), item.id + ":", item.detail)
        out = args.out or args.dir
        if out:
            with open(os.path.join(out, "script-items.tsv"), "a", encoding="utf-8") as f:
                f.write("\t".join(["PASS" if item.ok else "FAIL", item.id, item.description, item.detail]) + "\n")
        return 0 if item.ok else 1

    # a SIGTERM (the script stopping a checker whose player crashed) must still run the finally blocks that put the platform back
    signal.signal(signal.SIGTERM, lambda _sig, _frame: sys.exit(143))
    if args.mode in ("rule-restore", "scale-restore"):
        return restore_mode(args)

    since_dt = parse_time(args.since) if args.since else datetime.datetime.now(datetime.timezone.utc)
    out = args.out or args.dir
    try:
        platform = Platform(args.runner)
        if args.mode == "run":
            items, report = run_phase_a(args, platform, since_dt)
            title = "Sitepulse Phase A acceptance"
        elif args.mode == "control":
            items, report = run_control(args, platform, since_dt)
            title = f"Sitepulse Phase A control: {args.control}"
        elif args.mode == "soak":
            import phase_b_check as b
            items, report = b.run_soak(args, platform, since_dt)
            title = f"Sitepulse soak: {args.soak_minutes} minute(s) of Live"
        else:
            items, report = run_unknown_command(args, platform, since_dt)
            title = "Sitepulse Phase A control: unknown-command"
    except PlatformError as e:
        items, report, title = [Item("platform", "the platform answered", False, str(e))], {}, "Sitepulse Phase A acceptance"

    ok = overall(items)
    context = [("since", iso(since_dt)), ("mode", args.mode + (f" {args.control}" if args.control else ""))]
    doc = {"title": title, "pass": ok, "since": iso(since_dt), "items": [i.as_dict() for i in items], "report": report}
    if out:
        os.makedirs(out, exist_ok=True)
        name = args.mode if args.mode != "control" else "control-" + re.sub(r"[^A-Za-z0-9_.-]", "_", args.control)
        with open(os.path.join(out, f"checker-{name}.json"), "w", encoding="utf-8") as f:
            f.write(redact(json.dumps(doc, indent=2, default=str)))
        with open(os.path.join(out, f"SUMMARY-{name}.md"), "w", encoding="utf-8") as f:
            f.write(summary_md(title, items, context))
    for i in items:
        print(f"{'PASS' if i.ok else ('FAIL' if i.ok is False else 'info')}  {i.id}: {i.detail}")
    print("RESULT", "PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
