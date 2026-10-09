# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Convert a Sitepulse recording run into a board recording: the data file a
recorded-source dashboard plays back.

    python3 recording_to_board.py RUN_DIR --board-path frontend/testdata/sim-dashboards/sp-dashboard.json \
        --board-commit <sha> --out board-replay.<runId>.json [--excerpt FROM_S TO_S]

RUN_DIR is a recording run folder (the one holding run.json). The take folder that
contains it also holds files that must never be read: the CA bundle, the operator
credential, the filtered player log and the raw sim stream. This converter therefore
opens exactly five named files (INPUT_ALLOWLIST) and nothing else; it never lists or
globs the folder. Within those files it picks named fields only (the *_FIELDS tables),
so a field a later recorder version adds is ignored rather than copied through.

What it checks, and refuses on (a Refusal, never a silent drop):

  * the run is clean: the build's tracked tree was clean and the run ended cleanly.
  * every observed measurement row matches a sample the device itself published and
    the platform acknowledged, by (device, metric, occurredTime to the microsecond)
    with an identical value. The only rows allowed without a match are the platform's
    own seed rows ("snap") whose occurredTime precedes the run start. A row with no
    match is a fabricated row and fails the whole conversion.
  * the same for observed location rows (existence, speed, elevation). Locations carry
    no latitude/longitude in this recording, so none are emitted (channels.locations
    is false and a player reports them as "not in this recording").
  * the output has exactly the keys of the format below, and nothing in it looks like
    a credential (scan_for_credentials, importable by tests and by other repos).

What it drops: command rows, presence rows, status rows, engine_hours, and every
free-text field except chapter notes. Alarm ids are re-minted (alarm-1, alarm-2, ...)
so no platform identifier is published.

Output format, formatVersion 1, kind "sitepulse-board-recording" (all times in ms
from the start of the run, as the recording viewer applied them):

  header   runId, startedAtUtc, durationMs, platformVersion, instance, tenant,
           build{gitSha, sdkCommit}, clock[{fromMs, mode, scale}],
           devices[{id, token, kind}], channels{measurements[names], alarms,
           locations, commands}, board{path, sourceCommit, sha256},
           sourceHashes{file: sha256}, converter{version}, chapters[{tMs, name, note}],
           excerpt{fromMs, toMs} (only on an excerpt)
  measurements  columns, one entry per row, sorted by t:
           d (index into devices), n (index into channels.measurements),
           t (ms), v (value), s (1 for a platform seed row, else 0)
  alarms   snapshots[{tMs, total, alarms[alarm]}], events[{tMs, ...alarm}]
           alarm = {id, dev, key, metric, state, sev, occ, ack?}; ack appears only when true
  locations []   (always empty in this converter)

An excerpt keeps a contiguous window of measurement rows. Alarm snapshots and events
are small and the alarm table is a fold over history, so an excerpt keeps all of them
up to the end of its window.
"""

import argparse
import hashlib
import json
import math
import os
import re
import subprocess
import sys

CONVERTER_VERSION = 1
FORMAT_VERSION = 1
FORMAT_KIND = "sitepulse-board-recording"

# The only files ever opened, by exact name. See the module docstring.
INPUT_ALLOWLIST = ("run.json", "video-run.json", "observed.ndjson", "device.ndjson", "presenter.ndjson")

# The metrics the board can show. engine_hours is deliberately absent.
BOARD_MEASUREMENTS = (
    "engine_temp_c",
    "fuel_pct",
    "payload_t",
    "plant_running",
    "throughput_tph",
    "tyre_pressure_kpa",
)
ALARM_STATES = ("ACTIVE", "CLEARED")
ALARM_SEVERITIES = ("CRITICAL", "MAJOR", "MINOR", "WARNING")

SPEED_TOLERANCE = 1e-3  # observed locations are rounded to 4 decimals


class Refusal(Exception):
    """The conversion must not proceed. Never caught and continued past."""


# --- input -----------------------------------------------------------------


def read_allowed(run_dir, name):
    """Read one allowlisted input file as bytes. Any other name is refused."""
    if name not in INPUT_ALLOWLIST:
        raise Refusal("refusing to read %r: not an allowlisted input" % name)
    with open(os.path.join(run_dir, name), "rb") as f:
        return f.read()


def _ndjson(data, name):
    out = []
    for i, line in enumerate(data.decode("utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            out.append(json.loads(line))
        except ValueError as e:
            raise Refusal("%s line %d is not JSON: %s" % (name, i, e))
    return out


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def _us(ts):
    """An ISO UTC time cut to microseconds. The platform stores microseconds while the
    recorder writes 100 ns ticks, so the same instant can differ in the last digit."""
    m = re.fullmatch(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d+))?Z", ts or "")
    if not m:
        raise Refusal("not a UTC timestamp: %r" % (ts,))
    return "%s.%sZ" % (m.group(1), (m.group(2) or "").ljust(6, "0")[:6])


def _ms(seconds):
    if not isinstance(seconds, (int, float)) or isinstance(seconds, bool) or not math.isfinite(seconds) or seconds < 0:
        raise Refusal("bad time offset %r" % (seconds,))
    return int(round(seconds * 1000))


def _require(cond, msg):
    if not cond:
        raise Refusal(msg)


# --- conversion ------------------------------------------------------------


def convert(run_dir, board, excerpt=None):
    """Return (document, notes). `board` is {"path", "sourceCommit", "sha256"}.
    `excerpt` is (from_seconds, to_seconds) or None. `notes` holds the cross-check
    counts for the caller to print."""
    raw = {n: read_allowed(run_dir, n) for n in INPUT_ALLOWLIST}
    run = json.loads(raw["run.json"].decode("utf-8"))
    video = json.loads(raw["video-run.json"].decode("utf-8"))
    observed = _ndjson(raw["observed.ndjson"], "observed.ndjson")
    device = _ndjson(raw["device.ndjson"], "device.ndjson")
    _ndjson(raw["presenter.ndjson"], "presenter.ndjson")  # parsed to be sure it is well formed; nothing is copied from it

    # --- run integrity
    _require(run.get("formatVersion") == 1, "unsupported run.json formatVersion %r" % (run.get("formatVersion"),))
    _require(re.fullmatch(r"run-\d{8}T\d{6}Z", str(run.get("runId"))), "bad runId")
    _require((run.get("build") or {}).get("trackedTreeClean") is True, "the recording build's tree was not clean")
    end = run.get("end") or {}
    _require(end.get("cleanly") is True, "the run did not end cleanly")
    _require(video.get("complete") is True, "video-run.json says the take is incomplete")
    start_utc = run["startedAtUtc"]
    start_us = _us(start_utc)
    duration_ms = _ms(end.get("durationSeconds"))

    devices = []
    for d in run["devices"]:
        devices.append({"id": d["id"], "token": d["token"], "kind": d["kind"]})
    token_of_id = {d["id"]: d["token"] for d in devices}
    index_of_token = {d["token"]: i for i, d in enumerate(devices)}
    _require(len(token_of_id) == len(devices) and len(index_of_token) == len(devices), "duplicate device in run.json")

    # --- device-side published and acknowledged samples
    pub_meas = {}
    pub_loc = {}
    for r in device:
        if r.get("k") != "sample":
            continue
        tok = token_of_id.get(r.get("dev"))
        _require(tok is not None, "device sample for a device not in run.json: %r" % (r.get("dev"),))
        if r.get("sk") == "measurement":
            for n, v in r["values"].items():
                pub_meas[(tok, n, _us(r["occ"]))] = v
        elif r.get("sk") == "location":
            pub_loc[(tok, _us(r["occ"]))] = (r.get("speed"), r.get("elev"))

    # --- observed rows
    cols = {"d": [], "n": [], "t": [], "v": [], "s": []}
    names = list(BOARD_MEASUREMENTS)
    counts = {
        "measurementRows": 0,
        "measurementMatched": 0,
        "measurementSeed": 0,
        "measurementOnBoard": 0,
        "locationRows": 0,
        "locationMatched": 0,
        "locationSeed": 0,
        "alarmEvents": 0,
        "commandRowsDropped": 0,
        "otherRowsDropped": 0,
    }
    alarm_ids = {}
    snapshots, events = [], []
    last_t = -1

    def alarm_obj(a):
        for k in ("dev", "token", "key", "metric", "state", "sev", "occ"):
            _require(isinstance(a.get(k), str) and a[k], "alarm row missing %r" % k)
        _require(a["dev"] in index_of_token, "alarm for unknown device %r" % a["dev"])
        _require(a["state"] in ALARM_STATES, "alarm state %r not allowed" % a["state"])
        _require(a["sev"] in ALARM_SEVERITIES, "alarm severity %r not allowed" % a["sev"])
        aid = alarm_ids.setdefault(a["token"], "alarm-%d" % (len(alarm_ids) + 1))
        o = {"id": aid, "dev": a["dev"], "key": a["key"], "metric": a["metric"], "state": a["state"], "sev": a["sev"], "occ": a["occ"]}
        if a.get("ack") is True:
            o["ack"] = True
        return o

    for r in observed:
        kind = r.get("k")
        if "t" in r:
            tm = _ms(r["t"])
            _require(tm >= last_t, "observed.ndjson is not in time order")
            last_t = tm
        if kind == "measurement":
            counts["measurementRows"] += 1
            dev, n, occ = r["dev"], r["n"], _us(r["occ"])
            _require(dev in index_of_token, "measurement for unknown device %r" % dev)
            pub = pub_meas.get((dev, n, occ), None)
            if (dev, n, occ) in pub_meas:
                if pub != r["v"]:
                    raise Refusal("observed %s %s at %s is %r but the device published %r" % (dev, n, r["occ"], r["v"], pub))
                counts["measurementMatched"] += 1
            elif r.get("snap") is True and occ < start_us:
                counts["measurementSeed"] += 1
            else:
                raise Refusal("fabricated row: observed %s %s at %s (t=%s) has no device-published sample" % (dev, n, r["occ"], r["t"]))
            if n not in BOARD_MEASUREMENTS:
                continue
            _require(isinstance(r["v"], (int, float)) and not isinstance(r["v"], bool) and math.isfinite(r["v"]), "non-numeric value")
            counts["measurementOnBoard"] += 1
            cols["d"].append(index_of_token[dev])
            cols["n"].append(names.index(n))
            cols["t"].append(tm)
            cols["v"].append(r["v"])
            cols["s"].append(1 if r.get("snap") is True else 0)
        elif kind == "location":
            counts["locationRows"] += 1
            dev, occ = r["dev"], _us(r["occ"])
            _require(dev in index_of_token, "location for unknown device %r" % dev)
            if (dev, occ) in pub_loc:
                sp, el = pub_loc[(dev, occ)]
                for what, a, b in (("speed", r.get("speed"), sp), ("elev", r.get("elev"), el)):
                    if a is None or b is None or abs(a - b) > SPEED_TOLERANCE:
                        raise Refusal("observed location %s %s at %s is %r but the device published %r" % (dev, what, r["occ"], a, b))
                counts["locationMatched"] += 1
            elif occ < start_us:
                counts["locationSeed"] += 1
            else:
                raise Refusal("fabricated row: observed location for %s at %s has no device-published sample" % (dev, r["occ"]))
        elif kind == "alarm":
            counts["alarmEvents"] += 1
            o = alarm_obj(r)
            o["tMs"] = tm
            events.append(o)
        elif kind == "alarmSnapshot":
            snapshots.append({"tMs": tm, "total": r["total"], "alarms": [alarm_obj(a) for a in r.get("alarms", [])]})
        elif kind == "command":
            counts["commandRowsDropped"] += 1
        else:
            counts["otherRowsDropped"] += 1

    # --- header
    chapters = [{"tMs": _ms(s["atSeconds"]), "name": s["name"], "note": s["note"]} for s in video.get("steps", [])]
    doc = {
        "formatVersion": FORMAT_VERSION,
        "kind": FORMAT_KIND,
        "runId": run["runId"],
        "startedAtUtc": start_utc,
        "durationMs": duration_ms,
        "platformVersion": run["platformVersion"],
        "instance": run["instance"],
        "tenant": run["tenant"],
        "build": {"gitSha": run["build"]["gitSha"], "sdkCommit": run["build"]["sdkCommit"]},
        "clock": [{"fromMs": _ms(c["from"]), "mode": c["mode"], "scale": c["scale"]} for c in run["clock"]],
        "devices": devices,
        "channels": {"measurements": names, "alarms": True, "locations": False, "commands": False},
        "board": dict(board),
        "sourceHashes": {n: _sha256(raw[n]) for n in INPUT_ALLOWLIST},
        "converter": {"version": CONVERTER_VERSION},
        "chapters": chapters,
        "measurements": cols,
        "alarms": {"snapshots": snapshots, "events": events},
        "locations": [],
    }

    if excerpt is not None:
        lo, hi = _ms(excerpt[0]), _ms(excerpt[1])
        _require(lo < hi <= duration_ms, "excerpt window %r is empty or past the end of the run" % (excerpt,))
        keep = [i for i, t in enumerate(cols["t"]) if lo <= t <= hi]
        _require(keep, "excerpt window holds no measurement rows")
        doc["measurements"] = {k: [v[i] for i in keep] for k, v in cols.items()}
        doc["alarms"]["snapshots"] = [s for s in snapshots if s["tMs"] <= hi]
        doc["alarms"]["events"] = [e for e in events if e["tMs"] <= hi]
        doc["chapters"] = [c for c in chapters if lo <= c["tMs"] <= hi]
        doc["excerpt"] = {"fromMs": lo, "toMs": hi}
        counts["excerptRows"] = len(keep)

    validate_output(doc)
    findings = scan_for_credentials(doc)
    if findings:
        raise Refusal("output looks like it holds a credential: " + "; ".join(findings[:5]))
    return doc, counts


# --- output validation -----------------------------------------------------

_HEX64 = re.compile(r"[0-9a-f]{64}")
_HEX40 = re.compile(r"[0-9a-f]{40}")

_SCHEMA_TOP = {
    "formatVersion", "kind", "runId", "startedAtUtc", "durationMs", "platformVersion", "instance", "tenant",
    "build", "clock", "devices", "channels", "board", "sourceHashes", "converter", "chapters",
    "measurements", "alarms", "locations", "excerpt",
}
_SCHEMA_REQUIRED = _SCHEMA_TOP - {"excerpt"}


def _keys(obj, allowed, where, required=None):
    _require(isinstance(obj, dict), "%s is not an object" % where)
    extra = set(obj) - set(allowed)
    _require(not extra, "%s has unexpected keys %s" % (where, sorted(extra)))
    missing = set(required if required is not None else allowed) - set(obj)
    _require(not missing, "%s is missing keys %s" % (where, sorted(missing)))


def validate_output(doc):
    """Schema-strict: an unknown or missing key anywhere means refuse."""
    _keys(doc, _SCHEMA_TOP, "document", _SCHEMA_REQUIRED)
    _require(doc["kind"] == FORMAT_KIND and doc["formatVersion"] == FORMAT_VERSION, "wrong format identity")
    _keys(doc["build"], {"gitSha", "sdkCommit"}, "build")
    _keys(doc["channels"], {"measurements", "alarms", "locations", "commands"}, "channels")
    _keys(doc["board"], {"path", "sourceCommit", "sha256"}, "board")
    _require(_HEX40.fullmatch(doc["board"]["sourceCommit"]) and _HEX64.fullmatch(doc["board"]["sha256"]), "bad board pin")
    _keys(doc["converter"], {"version"}, "converter")
    _keys(doc["sourceHashes"], set(INPUT_ALLOWLIST), "sourceHashes")
    for n, h in doc["sourceHashes"].items():
        _require(_HEX64.fullmatch(h), "bad source hash for %s" % n)
    for c in doc["clock"]:
        _keys(c, {"fromMs", "mode", "scale"}, "clock entry")
    for d in doc["devices"]:
        _keys(d, {"id", "token", "kind"}, "device")
    for c in doc["chapters"]:
        _keys(c, {"tMs", "name", "note"}, "chapter")
    if "excerpt" in doc:
        _keys(doc["excerpt"], {"fromMs", "toMs"}, "excerpt")
    cols = doc["measurements"]
    _keys(cols, {"d", "n", "t", "v", "s"}, "measurements")
    n = len(cols["t"])
    _require(all(len(c) == n for c in cols.values()), "measurement columns differ in length")
    _require(all(0 <= i < len(doc["devices"]) for i in cols["d"]), "measurement device index out of range")
    _require(all(0 <= i < len(doc["channels"]["measurements"]) for i in cols["n"]), "measurement name index out of range")
    _require(all(isinstance(v, (int, float)) and not isinstance(v, bool) for v in cols["v"]), "measurement value not numeric")
    _require(all(s in (0, 1) for s in cols["s"]), "bad seed flag")
    _keys(doc["alarms"], {"snapshots", "events"}, "alarms")
    alarm_keys = {"id", "dev", "key", "metric", "state", "sev", "occ", "ack"}
    alarm_req = alarm_keys - {"ack"}
    for s in doc["alarms"]["snapshots"]:
        _keys(s, {"tMs", "total", "alarms"}, "alarm snapshot")
        for a in s["alarms"]:
            _keys(a, alarm_keys, "snapshot alarm", alarm_req)
    for e in doc["alarms"]["events"]:
        _keys(e, alarm_keys | {"tMs"}, "alarm event", alarm_req | {"tMs"})
        _require(re.fullmatch(r"alarm-\d+", e["id"]), "alarm id was not re-minted")
    _require(doc["locations"] == [], "this converter emits no positions")


# --- credential scan -------------------------------------------------------

_SCAN_PATTERNS = (
    ("jwt", re.compile(r"eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}(\.[A-Za-z0-9_-]*)?")),
    ("pem", re.compile(r"-----BEGIN|-----END|PRIVATE KEY", re.I)),
    ("hex-run", re.compile(r"(?<![0-9A-Za-z])[0-9a-fA-F]{32,}(?![0-9A-Za-z])")),
    ("base64-run", re.compile(r"[A-Za-z0-9+/_-]{40,}={0,2}")),
    ("keyword", re.compile(r"password|passwd|secret|bearer|credential|api[_-]?key|authorization|client[_-]?secret", re.I)),
    ("email", re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")),
    ("url", re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://")),
    ("ipv4", re.compile(r"(?<![\d.])\d{1,3}(\.\d{1,3}){3}(?![\d.])")),
    ("hostname", re.compile(r"\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|net|org|io|dev|local|internal|cloud|app|edu|gov|info)\b", re.I)),
)

# The only strings allowed to look like a hex run: exactly these fields, and only when
# they are exactly a SHA-256 / a full git commit.
_HASH_FIELDS = (("board", "sha256", _HEX64), ("board", "sourceCommit", _HEX40))


def _is_hit(name, text):
    """A long base64-alphabet run is only a credential if it is mixed: a repo path or a
    kebab-case phrase of the same length has no digit and no capital."""
    if name == "base64-run":
        return bool(re.search(r"\d", text) and re.search(r"[A-Z]", text) and re.search(r"[a-z]", text))
    return True


def _walk(obj, path=()):
    if isinstance(obj, dict):
        for k, v in obj.items():
            yield path + (k,), k  # a key is text too
            yield from _walk(v, path + (k,))
    elif isinstance(obj, list):
        for i, v in enumerate(obj):
            yield from _walk(v, path + (i,))
    elif isinstance(obj, str):
        yield path, obj


def scan_for_credentials(doc_or_text):
    """Return a list of findings (empty means clean) for a parsed document or a raw
    string. Every string and every key is checked. The sha256 fields of the board pin
    and sourceHashes are exempt only when they are exactly a full hash."""
    if isinstance(doc_or_text, str):
        items = [((), doc_or_text)]
    else:
        items = list(_walk(doc_or_text))
    findings = []
    for path, s in items:
        if len(path) == 2 and path[0] == "sourceHashes" and _HEX64.fullmatch(s):
            continue
        if len(path) == 2 and any(path == (a, b) and rx.fullmatch(s) for a, b, rx in _HASH_FIELDS):
            continue
        for name, rx in _SCAN_PATTERNS:
            if any(_is_hit(name, m.group(0)) for m in rx.finditer(s)):
                where = "/".join(str(p) for p in path) or "text"
                findings.append("%s at %s" % (name, where))
    return findings


# --- command line ----------------------------------------------------------


def resolve_board(path, commit, repo_root):
    """Pin the board definition from git, so the hash is of the committed bytes."""
    full = subprocess.check_output(["git", "-C", repo_root, "rev-parse", commit + "^{commit}"], text=True).strip()
    blob = subprocess.check_output(["git", "-C", repo_root, "show", "%s:%s" % (full, path)])
    return {"path": path, "sourceCommit": full, "sha256": _sha256(blob)}


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("run_dir")
    ap.add_argument("--board-path", required=True, help="repo-relative path of the board definition")
    ap.add_argument("--board-commit", required=True, help="commit that holds the board as it was at the run")
    ap.add_argument("--repo-root", default=".")
    ap.add_argument("--out", required=True)
    ap.add_argument("--excerpt", nargs=2, type=float, metavar=("FROM_S", "TO_S"))
    a = ap.parse_args(argv)
    board = resolve_board(a.board_path, a.board_commit, a.repo_root)
    try:
        doc, counts = convert(a.run_dir, board, tuple(a.excerpt) if a.excerpt else None)
    except Refusal as e:
        print("refused: %s" % e, file=sys.stderr)
        return 1
    body = json.dumps(doc, separators=(",", ":"), ensure_ascii=True) + "\n"
    with open(a.out, "w", encoding="utf-8", newline="\n") as f:
        f.write(body)
    digest = _sha256(body.encode("utf-8"))
    with open(a.out + ".sha256", "w", encoding="utf-8", newline="\n") as f:
        f.write("%s  %s\n" % (digest, os.path.basename(a.out)))
    print(json.dumps(counts, indent=2, sort_keys=True))
    print("wrote %s (%d bytes) sha256 %s" % (a.out, len(body), digest))
    return 0


if __name__ == "__main__":
    sys.exit(main())
