# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
A small, deterministic Towerpulse run folder (the files the recorder writes), for the board-replay profile's tests and for the committed board-recording
fixture. It is synthetic: invented numbers shaped like a real run (a zone heating until the platform's rule fires, a chiller going silent), nothing recorded.

    python towerpulse_fixture.py OUT_DIR              write the run folder
    python towerpulse_fixture.py --board-recording F  convert it with the Towerpulse profile and write the board recording to F (placeholder board pin)
"""

import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
START = "2026-10-29T14:00:00.0000000Z"
BOARD_REL = "frontend/testdata/sim-dashboards/tp-dashboard.json"
PLACEHOLDER_BOARD = {"path": BOARD_REL, "sourceCommit": "a" * 40, "sha256": "b" * 64}
PROFILE = os.path.join(HERE, "board-replay.profile.json")

sys.path.insert(0, HERE)
import acceptance_check as ac  # noqa: E402


def token_of(ext):
    """The platform token the scenario gives a device (backend/sims/dc-simulator/sim/towerpulse.go, buildTowerpulseLayout)."""
    kind, parts = ac.kind_of(ext), ext.split("-")
    building = {"T": "tower", "A": "annex"}[parts[2]]
    if kind == "vav":
        return "tp-vav-%s-f%s-%s" % (building, parts[3][1:], {"N": "north", "E": "east", "S": "south", "W": "west"}[parts[4]])
    if kind == "ahu":
        return "tp-ahu-%s-%s" % (building, parts[3])
    return "tp-%s-%s" % ("chiller" if kind == "chiller" else "meter", building)


DEVICES = [(e, token_of(e), ac.kind_of(e)) for e in ac.DEVICES]
HOT = next(d for d in DEVICES if d[0] == ac.HOT_DEVICE)
STEP_S = 30
STEPS = 21  # ten minutes


def stamp(i):
    m, s = divmod(i * STEP_S, 60)
    return "2026-10-29T14:%02d:%02d.0000000Z" % (m, s)


def hot_temp(i):
    return round(23.0 + min(i, 12) * 0.35 - max(0, i - 14) * 0.5, 2)


def make_run(root, patch_observed=None, patch_device=None, patch_run=None):
    """Write the run folder under `root`; the patches receive the row lists to alter before they are written."""
    run = {
        "formatVersion": 1,
        "runId": "run-20261029T140000Z",
        "startedAtUtc": START,
        "build": {"gitSha": "0123456789ab", "trackedTreeClean": True, "sdkCommit": "ba9876543210"},
        "platformVersion": "0.26.0",
        "instance": "tp",
        "tenant": "sim-tp-fixture",
        "manifest": "towerpulse",
        "seed": "1",
        "devices": [{"id": e, "token": t, "kind": k} for e, t, k in DEVICES],
        "clock": [{"from": 0.0, "mode": "real", "scale": 1}],
        "end": {"cleanly": True, "durationSeconds": float(STEPS * STEP_S)},
    }
    device, observed = [], [{"t": 0.9, "k": "alarmSnapshot", "requested": "2026-10-29T14:00:00.5000000Z", "total": 0, "alarms": []}]
    silent_from = 14  # the annex chiller stops reporting at step 14
    for i in range(STEPS):
        occ, t = stamp(i), 1.0 + i * STEP_S
        for ext, tok, kind in DEVICES:
            if kind == "vav":
                temp = hot_temp(i) if ext == HOT[0] else 22.0
                values = {"temp_c": temp, "setpoint_c": 22.0, "co2_ppm": 600.0 + i, "occupancy": 14 if ext == HOT[0] else 2, "damper_pct": 30.0}
            elif kind == "ahu":
                values = {"supply_air_c": 13.0, "fan_pct": 60.0, "filter_dp_pa": 180.0, "mode": 0}
            elif kind == "chiller":
                if ext == "TP-CH-A" and i >= silent_from:
                    continue  # not reporting
                values = {"chw_supply_c": 6.5, "power_kw": 50.0, "cooling_kw": 175.0, "running": 1}
            else:
                values = {"kw": 120.0 + i, "kwh": 1000.0 + i * 1.0}
            device.append({"t": t, "k": "sample", "dev": ext, "sk": "measurement", "occ": occ, "ack": occ, "values": values})
            for n, v in sorted(values.items()):
                observed.append({"t": t + 0.2, "k": "measurement", "dev": tok, "n": n, "v": v, "occ": occ, "obs": occ})
            if kind == "chiller":
                observed.append({"t": t + 0.2, "k": "measurement", "dev": tok, "n": "cop", "v": round(values["cooling_kw"] / values["power_kw"], 4), "occ": occ, "obs": occ})
            if kind == "meter":
                observed.append({"t": t + 0.2, "k": "measurement", "dev": tok, "n": "kwh_per_m2", "v": round(values["kwh"] / 5000.0, 4), "occ": occ, "obs": occ})
    # R1: the five-minute average crosses; the alarm is the platform's verdict on the reading at step 10 and clears at step 18
    observed.append({"t": 1.0 + 10 * STEP_S + 0.4, "k": "alarm", "dev": HOT[1], "token": "a-1", "key": "zone-hot", "metric": "temp_c", "state": "ACTIVE", "sev": "MAJOR", "occ": stamp(10)})
    observed.append({"t": 1.0 + 11 * STEP_S, "k": "command", "dev": HOT[1], "token": "c-1", "n": "boost-cooling", "state": "QUEUED"})
    observed.append({"t": 1.0 + 18 * STEP_S + 0.4, "k": "alarm", "dev": HOT[1], "token": "a-1", "key": "zone-hot", "metric": "temp_c", "state": "CLEARED", "sev": "MAJOR", "occ": stamp(18)})
    # R4: the platform raised it because readings stopped; no reading triggered it (the chiller published nothing at that instant)
    observed.append({"t": 1.0 + 19 * STEP_S + 0.4, "k": "alarm", "dev": "tp-chiller-annex", "token": "a-2", "key": "chiller-silent", "metric": "chw_supply_c", "state": "ACTIVE", "sev": "CRITICAL", "occ": stamp(19)})
    if patch_observed:
        patch_observed(observed)
    if patch_device:
        patch_device(device)
    if patch_run:
        patch_run(run)
    observed.sort(key=lambda r: r["t"])
    os.makedirs(root, exist_ok=True)
    for name, rows in (("observed.ndjson", observed), ("device.ndjson", device)):
        with open(os.path.join(root, name), "w", encoding="utf-8", newline="\n") as f:
            f.write("".join(json.dumps(r) + "\n" for r in rows))
    with open(os.path.join(root, "run.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(run, f)
    return root


def board_recording(root, board=None):
    sys.path.insert(0, os.path.join(HERE, "..", "..", "sitepulse-unity", "tools"))
    import recording_to_board as c

    doc, _ = c.convert(root, board or PLACEHOLDER_BOARD, None, c.load_profile(PROFILE))
    return doc


def main(argv):
    if len(argv) == 3 and argv[1] == "--board-recording":
        import tempfile

        with tempfile.TemporaryDirectory() as d:
            doc = board_recording(make_run(os.path.join(d, "run")))
        with open(argv[2], "w", encoding="utf-8", newline="\n") as f:
            f.write(json.dumps(doc, separators=(",", ":"), ensure_ascii=True) + "\n")
        return 0
    if len(argv) == 2:
        make_run(argv[1])
        return 0
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
