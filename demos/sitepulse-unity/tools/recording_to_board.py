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
import stat
import subprocess
import sys

CONVERTER_VERSION = 2
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


_REPARSE_POINT = 0x400  # FILE_ATTRIBUTE_REPARSE_POINT: a Windows symlink or junction


def read_allowed(run_dir, name):
    """Read one allowlisted input file as bytes. Any other name is refused, and so is an
    allowlisted name that is a link (symlink or Windows junction) or not a regular file:
    a link could point at one of the files that must never be read."""
    if name not in INPUT_ALLOWLIST:
        raise Refusal("refusing to read %r: not an allowlisted input" % name)
    path = os.path.join(run_dir, name)
    st = os.lstat(path)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode) or getattr(st, "st_file_attributes", 0) & _REPARSE_POINT:
        raise Refusal("refusing to read %r: not a plain regular file" % name)
    with open(path, "rb") as f:
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


# Every text field that reaches the output has a grammar, checked where it is read and
# again on the finished document. Anything that does not fit is refused, so a field that
# later carries a structure or a free-form value cannot slip through as a "string".
GRAMMAR = {
    "token": r"[a-z0-9][a-z0-9-]{0,63}",  # device token, instance, tenant
    "deviceId": r"[A-Z0-9][A-Z0-9-]{0,31}",
    "kind": r"[A-Z][A-Za-z]{0,31}",
    "metric": r"[a-z][a-z0-9_]{0,63}",
    "alarmKey": r"[a-z][a-z0-9-]{0,63}",
    "semver": r"\d{1,4}\.\d{1,4}\.\d{1,4}(-[0-9A-Za-z.-]{1,32})?",
    "gitSha": r"[0-9a-f]{7,40}",
    "runId": r"run-\d{8}T\d{6}Z",
    "timestamp": r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{1,7})?Z",
    "chapterName": r"[A-Za-z][A-Za-z0-9 -]{0,63}",
    "chapterNote": r"[A-Za-z0-9 .,:;%()'>/+_-]{0,240}",
    "alarmId": r"alarm-\d{1,6}",
    "path": r"[A-Za-z0-9_./-]{1,200}",
}
CLOCK_MODES = ("real", "accelerated")


def _g(name, value, where):
    """`value` must be a string that matches grammar `name` in full."""
    if not isinstance(value, str) or not re.fullmatch(GRAMMAR[name], value):
        raise Refusal("%s does not fit the %s grammar" % (where, name))
    return value


def _int(value, where, lo=0, hi=10**9):
    if not isinstance(value, int) or isinstance(value, bool) or not lo <= value <= hi:
        raise Refusal("%s is not an integer in [%d, %d]" % (where, lo, hi))
    return value


def _number(value, where, lo=-1e12, hi=1e12):
    if not isinstance(value, (int, float)) or isinstance(value, bool) or not math.isfinite(value) or not lo <= value <= hi:
        raise Refusal("%s is not a number in range" % where)
    return value


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
    presenter = _ndjson(raw["presenter.ndjson"], "presenter.ndjson")  # only action rows are used, and only to find the chapter clock

    # --- run integrity
    _require(run.get("formatVersion") == 1, "unsupported run.json formatVersion %r" % (run.get("formatVersion"),))
    _g("runId", run.get("runId"), "runId")
    _require((run.get("build") or {}).get("trackedTreeClean") is True, "the recording build's tree was not clean")
    end = run.get("end") or {}
    _require(end.get("cleanly") is True, "the run did not end cleanly")
    _require(video.get("complete") is True, "video-run.json says the take is incomplete")
    start_utc = _g("timestamp", run.get("startedAtUtc"), "startedAtUtc")
    start_us = _us(start_utc)
    duration_ms = _ms(end.get("durationSeconds"))

    devices = []
    for d in run["devices"]:
        devices.append(
            {"id": _g("deviceId", d.get("id"), "device id"), "token": _g("token", d.get("token"), "device token"), "kind": _g("kind", d.get("kind"), "device kind")}
        )
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
        if not isinstance(r.get("ack"), str) or not re.fullmatch(GRAMMAR["timestamp"], r["ack"]):
            continue  # published but never acknowledged by the platform: it vouches for nothing
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
    seed_phase = True  # the platform's initial snapshot: over at the first measurement that is not a seed
    seeded = set()
    located = set()  # devices that have had a position matched to a device sample

    def alarm_obj(a, snapshot):
        for k in ("dev", "token", "key", "metric", "state", "sev", "occ"):
            _require(isinstance(a.get(k), str) and a[k], "alarm row missing %r" % k)
        _require(a["dev"] in index_of_token, "alarm for unknown device %r" % a["dev"])
        _g("alarmKey", a["key"], "alarm key")
        _g("metric", a["metric"], "alarm metric")
        _g("timestamp", a["occ"], "alarm occ")
        _require(a["state"] in ALARM_STATES, "alarm state %r not allowed" % a["state"])
        _require(a["sev"] in ALARM_SEVERITIES, "alarm severity %r not allowed" % a["sev"])
        if snapshot:
            # the alarms standing when the viewer subscribed were raised before the run began
            _require(_us(a["occ"]) < start_us, "fabricated row: snapshot alarm %s was raised at %s, not before the run start" % (a["key"], a["occ"]))
        else:
            # a live alarm is the platform's verdict on a reading: that reading must be one the device published
            _require(
                (a["dev"], a["metric"], _us(a["occ"])) in pub_meas,
                "fabricated row: alarm %s on %s at %s has no device-published %s sample" % (a["key"], a["dev"], a["occ"], a["metric"]),
            )
        aid = alarm_ids.setdefault(a["token"], "alarm-%d" % (len(alarm_ids) + 1))
        o = {"id": aid, "dev": a["dev"], "key": a["key"], "metric": a["metric"], "state": a["state"], "sev": a["sev"], "occ": a["occ"]}
        if a.get("ack") is True:
            o["ack"] = True
        return o

    for r in observed:
        kind = r.get("k")
        _require("t" in r, "an observed %s row has no time" % kind)
        tm = _ms(r["t"])
        _require(tm >= last_t, "observed.ndjson is not in time order")
        last_t = tm
        if kind == "measurement":
            counts["measurementRows"] += 1
            dev, n, occ = r["dev"], r["n"], _us(r["occ"])
            _require(dev in index_of_token, "measurement for unknown device %r" % dev)
            _g("metric", n, "measurement name")
            is_seed = r.get("snap") is True
            if is_seed:
                _require(seed_phase, "fabricated row: a seed row for %s %s at t=%s comes after the initial snapshot" % (dev, n, r["t"]))
                _require((dev, n) not in seeded, "fabricated row: a second seed row for %s %s" % (dev, n))
                seeded.add((dev, n))
            else:
                seed_phase = False
            pub = pub_meas.get((dev, n, occ), None)
            if (dev, n, occ) in pub_meas:
                if pub != r["v"]:
                    raise Refusal("observed %s %s at %s is %r but the device published %r" % (dev, n, r["occ"], r["v"], pub))
                counts["measurementMatched"] += 1
            elif is_seed and occ < start_us:
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
            cols["s"].append(1 if is_seed else 0)
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
                located.add(dev)
            elif occ < start_us and dev not in located:
                # the poll's answer for a machine that has not reported yet: the platform's last position from before the run
                counts["locationSeed"] += 1
            else:
                raise Refusal("fabricated row: observed location for %s at %s has no device-published sample" % (dev, r["occ"]))
        elif kind == "alarm":
            counts["alarmEvents"] += 1
            o = alarm_obj(r, snapshot=False)
            o["tMs"] = tm
            events.append(o)
        elif kind == "alarmSnapshot":
            snapshots.append({"tMs": tm, "total": _int(r.get("total"), "alarm snapshot total"), "alarms": [alarm_obj(a, snapshot=True) for a in r.get("alarms", [])]})
        elif kind == "command":
            counts["commandRowsDropped"] += 1
        else:
            counts["otherRowsDropped"] += 1

    # --- header
    chapters = build_chapters(video, presenter, events)
    doc = {
        "formatVersion": FORMAT_VERSION,
        "kind": FORMAT_KIND,
        "runId": run["runId"],
        "startedAtUtc": start_utc,
        "durationMs": duration_ms,
        "platformVersion": _g("semver", run.get("platformVersion"), "platformVersion"),
        "instance": _g("token", run.get("instance"), "instance"),
        "tenant": _g("token", run.get("tenant"), "tenant"),
        "build": {"gitSha": _g("gitSha", run["build"].get("gitSha"), "build.gitSha"), "sdkCommit": _g("gitSha", run["build"].get("sdkCommit"), "build.sdkCommit")},
        "clock": [clock_segment(c) for c in run["clock"]],
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


PRESENTER_SPREAD_S = 0.15  # video-run.json rounds its times to 0.1 s, so two offsets may differ by about that
FLEET_WAIT_S = 300.0  # the most the take waits for the fleet before its own clock starts
ALARM_CLAIM = re.compile(r"the platform says (\S+) is (ACTIVE|CLEARED)")


def clock_segment(c):
    _require(c.get("mode") in CLOCK_MODES, "clock mode %r not allowed" % (c.get("mode"),))
    return {"fromMs": _ms(c.get("from")), "mode": c["mode"], "scale": _number(c.get("scale"), "clock scale", 1e-6, 1e6)}


def build_chapters(video, presenter, events):
    """Chapter times in ms from the run start.

    video-run.json counts seconds from the moment the fleet was observed, which is after
    the run started (by up to the fleet wait), while everything else here counts from the
    run start. The presenter's own action rows carry run-start times for the steps that
    are presenter actions (same text as the step note), so the offset between the two
    clocks is measured from those, and the conversion is refused if it cannot be.

    A step whose note says the platform reported an alarm state is moved to no earlier
    than that alarm event (its time is only known to 0.1 s), and refused if there is no
    such event or it is more than 2 s away: the note must not be true only before its
    own chapter time."""
    actions = {}
    for r in presenter:
        if r.get("k") == "action" and isinstance(r.get("text"), str):
            actions[r["text"]] = r.get("t")
    steps = video.get("steps")
    _require(isinstance(steps, list) and steps, "video-run.json holds no steps")
    offsets = []
    for s in steps:
        if s.get("note") in actions:
            offsets.append(_number(actions[s["note"]], "presenter time", 0, 1e6) - _number(s.get("atSeconds"), "step atSeconds", 0, 1e6))
    _require(offsets, "cannot establish the chapter clock: no step note matches a presenter action")
    _require(max(offsets) - min(offsets) <= PRESENTER_SPREAD_S, "the chapter clock offsets disagree: %s" % ["%.3f" % o for o in offsets])
    offset = sum(offsets) / len(offsets)
    _require(0 <= offset <= FLEET_WAIT_S, "chapter clock offset %.3f s is outside [0, %d]" % (offset, FLEET_WAIT_S))
    chapters = []
    for s in steps:
        name = _g("chapterName", s.get("name"), "chapter name")
        note = _g("chapterNote", s.get("note"), "chapter note")
        t = _ms(_number(s.get("atSeconds"), "step atSeconds", 0, 1e6) + offset)
        m = ALARM_CLAIM.search(note)
        if m:
            at = [e["tMs"] for e in events if e["key"] == m.group(1) and e["state"] == m.group(2)]
            _require(at, "chapter %r says %s is %s but the recording has no such alarm event" % (name, m.group(1), m.group(2)))
            _require(abs(t - min(at)) <= 2000, "chapter %r is %d ms from the alarm it reports" % (name, abs(t - min(at))))
            t = max(t, min(at))
        chapters.append({"tMs": t, "name": name, "note": note})
    return chapters


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
    """Schema-strict: an unknown or missing key anywhere means refuse, and every value
    must fit the grammar of its field."""
    _keys(doc, _SCHEMA_TOP, "document", _SCHEMA_REQUIRED)
    _require(doc["kind"] == FORMAT_KIND and doc["formatVersion"] == FORMAT_VERSION, "wrong format identity")
    _g("runId", doc["runId"], "runId")
    _g("timestamp", doc["startedAtUtc"], "startedAtUtc")
    _int(doc["durationMs"], "durationMs")
    _g("semver", doc["platformVersion"], "platformVersion")
    _g("token", doc["instance"], "instance")
    _g("token", doc["tenant"], "tenant")
    _keys(doc["build"], {"gitSha", "sdkCommit"}, "build")
    _g("gitSha", doc["build"]["gitSha"], "build.gitSha")
    _g("gitSha", doc["build"]["sdkCommit"], "build.sdkCommit")
    _keys(doc["channels"], {"measurements", "alarms", "locations", "commands"}, "channels")
    _require(isinstance(doc["channels"]["measurements"], list), "channels.measurements is not a list")
    for m in doc["channels"]["measurements"]:
        _g("metric", m, "channel metric")
    _require(all(isinstance(doc["channels"][k], bool) for k in ("alarms", "locations", "commands")), "channel flags must be booleans")
    _keys(doc["board"], {"path", "sourceCommit", "sha256"}, "board")
    _g("path", doc["board"]["path"], "board.path")
    _require(".." not in doc["board"]["path"].split("/"), "board.path climbs out of the repository")
    _require(isinstance(doc["board"]["sourceCommit"], str) and _HEX40.fullmatch(doc["board"]["sourceCommit"]), "bad board commit")
    _require(isinstance(doc["board"]["sha256"], str) and _HEX64.fullmatch(doc["board"]["sha256"]), "bad board hash")
    _keys(doc["converter"], {"version"}, "converter")
    _int(doc["converter"]["version"], "converter.version", 1, 1000)
    _keys(doc["sourceHashes"], set(INPUT_ALLOWLIST), "sourceHashes")
    for n, h in doc["sourceHashes"].items():
        _require(isinstance(h, str) and _HEX64.fullmatch(h), "bad source hash for %s" % n)
    _require(isinstance(doc["clock"], list) and doc["clock"], "clock is empty")
    for c in doc["clock"]:
        _keys(c, {"fromMs", "mode", "scale"}, "clock entry")
        _int(c["fromMs"], "clock fromMs")
        _require(c["mode"] in CLOCK_MODES, "clock mode %r not allowed" % (c["mode"],))
        _number(c["scale"], "clock scale", 1e-6, 1e6)
    _require(isinstance(doc["devices"], list), "devices is not a list")
    for d in doc["devices"]:
        _keys(d, {"id", "token", "kind"}, "device")
        _g("deviceId", d["id"], "device id")
        _g("token", d["token"], "device token")
        _g("kind", d["kind"], "device kind")
    for c in doc["chapters"]:
        _keys(c, {"tMs", "name", "note"}, "chapter")
        _int(c["tMs"], "chapter tMs")
        _g("chapterName", c["name"], "chapter name")
        _g("chapterNote", c["note"], "chapter note")
    if "excerpt" in doc:
        _keys(doc["excerpt"], {"fromMs", "toMs"}, "excerpt")
        _int(doc["excerpt"]["fromMs"], "excerpt fromMs")
        _int(doc["excerpt"]["toMs"], "excerpt toMs")
    cols = doc["measurements"]
    _keys(cols, {"d", "n", "t", "v", "s"}, "measurements")
    n = len(cols["t"])
    _require(all(isinstance(c, list) and len(c) == n for c in cols.values()), "measurement columns differ in length")
    _require(all(isinstance(i, int) and 0 <= i < len(doc["devices"]) for i in cols["d"]), "measurement device index out of range")
    _require(all(isinstance(i, int) and 0 <= i < len(doc["channels"]["measurements"]) for i in cols["n"]), "measurement name index out of range")
    _require(all(isinstance(t, int) and t >= 0 for t in cols["t"]), "measurement time is not a non-negative integer")
    for v in cols["v"]:
        _number(v, "measurement value")
    _require(all(isinstance(x, int) and x in (0, 1) for x in cols["s"]), "bad seed flag")
    _keys(doc["alarms"], {"snapshots", "events"}, "alarms")
    alarm_keys = {"id", "dev", "key", "metric", "state", "sev", "occ", "ack"}
    alarm_req = alarm_keys - {"ack"}
    tokens = {d["token"] for d in doc["devices"]}

    def check_alarm(a, where):
        _g("alarmId", a["id"], where + " id")  # re-minted: a platform uuid does not fit
        _require(a["dev"] in tokens, where + " is for a device not in the run")
        _g("alarmKey", a["key"], where + " key")
        _g("metric", a["metric"], where + " metric")
        _g("timestamp", a["occ"], where + " occ")
        _require(a["state"] in ALARM_STATES and a["sev"] in ALARM_SEVERITIES, where + " has a state or severity outside the vocabulary")
        _require(a.get("ack", True) is True, where + " ack must be true when present")

    for sn in doc["alarms"]["snapshots"]:
        _keys(sn, {"tMs", "total", "alarms"}, "alarm snapshot")
        _int(sn["tMs"], "snapshot tMs")
        _int(sn["total"], "snapshot total")
        for a in sn["alarms"]:
            _keys(a, alarm_keys, "snapshot alarm", alarm_req)
            check_alarm(a, "snapshot alarm")
    for e in doc["alarms"]["events"]:
        _keys(e, alarm_keys | {"tMs"}, "alarm event", alarm_req | {"tMs"})
        _int(e["tMs"], "event tMs")
        check_alarm(e, "alarm event")
    _require(doc["locations"] == [], "this converter emits no positions")


# --- credential scan -------------------------------------------------------

_SCAN_PATTERNS = (
    ("jwt", re.compile(r"eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}(\.[A-Za-z0-9_-]*)?")),
    ("pem", re.compile(r"-----BEGIN|-----END|PRIVATE KEY", re.I)),
    # No boundary on either side: the simulator's device credential is 32 hex characters
    # and nothing says a letter or digit cannot sit beside it (x<hex>, 0x<hex>, key<hex>).
    ("hex-run", re.compile(r"[0-9a-fA-F]{32,}")),
    ("uuid", re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")),
    ("base64-run", re.compile(r"[A-Za-z0-9+/_-]{40,}={0,2}")),
    ("keyword", re.compile(r"password|passwd|secret|bearer|credential|api[_-]?key|authorization|client[_-]?secret", re.I)),
    ("email", re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")),
    ("url", re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://")),
    ("ipv4", re.compile(r"(?<![\d.])\d{1,3}(\.\d{1,3}){3}(?![\d.])")),
    # a "::" address; a clock time such as 02:01:36 has no "::" and is not one
    ("ipv6", re.compile(r"(?<![\w:])(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}(?![\w:])|(?<![\w:])(?:[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{1,4}){0,6})?::(?:[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{1,4}){0,6})?(?![\w:])")),
    ("hostname", re.compile(r"\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|net|org|io|dev|local|internal|cloud|app|edu|gov|info|svc|lan|corp|home|test|example|invalid|cluster)\b", re.I)),
    # in-cluster names and host:port, which carry no public TLD
    ("cluster-name", re.compile(r"\.svc\b|\.cluster\b|\bgke-|\bnode-pool|-pool-\d|\b[a-z][a-z0-9-]*\.[a-z0-9.-]+:\d{2,5}\b|\b[a-z][a-z0-9.-]*:\d{4,5}\b")),
)

# The only strings allowed to look like a hex run: exactly these fields, and only when
# they are exactly a SHA-256 / a full git commit.
_HASH_FIELDS = (("board", "sha256", _HEX64), ("board", "sourceCommit", _HEX40))
_HEX32 = re.compile(r"[0-9a-fA-F]{32,}")


def _is_hit(name, text):
    """A long base64-alphabet run is only a credential if it is mixed: a repo path or a
    kebab-case phrase of the same length has no digit and no capital."""
    if name == "base64-run":
        return bool(re.search(r"\d", text) and re.search(r"[A-Z]", text) and re.search(r"[a-z]", text))
    return True


def _walk(obj, path=()):
    if isinstance(obj, dict):
        for k, v in obj.items():
            yield path + (k,), k, True  # a key is text too
            yield from _walk(v, path + (k,))
    elif isinstance(obj, list):
        for i, v in enumerate(obj):
            yield from _walk(v, path + (i,))
    elif isinstance(obj, str):
        yield path, obj, False


def _exempt(path, s):
    if len(path) == 2 and path[0] == "sourceHashes" and _HEX64.fullmatch(s):
        return True
    return len(path) == 2 and any(path == (a, b) and rx.fullmatch(s) for a, b, rx in _HASH_FIELDS)


def scan_for_credentials(doc_or_text):
    """Return a list of findings (empty means clean) for a parsed document or a raw
    string. Every string and every key is checked. The sha256 fields of the board pin
    and sourceHashes are exempt only when they are exactly a full hash. A secret split
    across separators or across neighbouring strings is also found: every string is
    checked with its non-alphanumerics removed, and so is the concatenation of all of
    them in document order."""
    if isinstance(doc_or_text, str):
        items = [((), doc_or_text, False)]
    else:
        items = list(_walk(doc_or_text))
    findings = []
    kept = []
    for path, s, is_key in items:
        if not is_key and _exempt(path, s):
            continue
        if not is_key:
            kept.append(s)
        where = "/".join(str(p) for p in path) or "text"
        for name, rx in _SCAN_PATTERNS:
            if any(_is_hit(name, m.group(0)) for m in rx.finditer(s)):
                findings.append("%s at %s" % (name, where))
        if _HEX32.search(re.sub(r"[^0-9A-Za-z]", "", s)):
            findings.append("hex-run (split by separators) at %s" % where)
    if len(kept) > 1 and _HEX32.search("".join(re.sub(r"[^0-9A-Za-z]", "", s) for s in kept)):
        findings.append("hex-run across neighbouring strings")
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
