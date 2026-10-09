# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tests for recording_to_board.py. Run: python3 -m unittest test_recording_to_board (from this directory).

The committed 40 s excerpt is always checked. The golden values are also checked against
the FULL take2 recording when it is on disk (set SITEPULSE_TAKE2_RUN to its run folder, or
keep it at the default path under the demo's untracked Build/ folder); that class skips
otherwise, which is why CI, which has no recording, runs the excerpt tests only.
"""

import builtins
import contextlib
import hashlib
import io
import json
import os
import subprocess
import tempfile
import unittest
from unittest import mock

import recording_to_board as c

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
EXCERPT = os.path.join(REPO, "frontend", "testdata", "board-replay", "take2-tyre-excerpt.json")
BOARD_REL = "frontend/testdata/sim-dashboards/sp-dashboard.json"
FULL_RUN = os.environ.get(
    "SITEPULSE_TAKE2_RUN",
    os.path.join(HERE, "..", "Build", "video-take", "take2", "recordings", "run-20261007T020136Z"),
)

BOARD = {"path": BOARD_REL, "sourceCommit": "a" * 40, "sha256": "b" * 64}
START = "2026-10-07T02:01:36.0000000Z"
JWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"


# --- helpers that read a board recording the way a player would ---------------


def value_at(doc, device_id, metric, t_ms):
    d = next(i for i, x in enumerate(doc["devices"]) if x["id"] == device_id)
    n = doc["channels"]["measurements"].index(metric)
    m = doc["measurements"]
    best = None
    for i, t in enumerate(m["t"]):
        if t <= t_ms and m["d"][i] == d and m["n"][i] == n:
            best = m["v"][i]  # rows are in time order
    return best


def active_alarms(doc, t_ms):
    state = {}
    for s in doc["alarms"]["snapshots"]:
        if s["tMs"] <= t_ms:
            state = {a["id"]: a for a in s["alarms"]}
    for e in doc["alarms"]["events"]:
        if e["tMs"] <= t_ms:
            state[e["id"]] = e
    return [a for a in state.values() if a["state"] == "ACTIVE"]


def count_sev(doc, t_ms, sev):
    return sum(1 for a in active_alarms(doc, t_ms) if a["sev"] == sev)


def assert_alarm_claims_hold(tc, doc):
    """Every chapter whose note says the platform reported an alarm state is at or after that event."""
    import re as _re

    for ch in doc["chapters"]:
        m = _re.search(r"the platform says (\S+) is (ACTIVE|CLEARED)", ch["note"])
        if m:
            at = [e["tMs"] for e in doc["alarms"]["events"] if e["key"] == m.group(1) and e["state"] == m.group(2)]
            tc.assertTrue(at, ch)
            tc.assertGreaterEqual(ch["tMs"], min(at), ch)


# --- a synthetic run folder ----------------------------------------------------

T1 = "2026-10-07T02:01:37.0000001Z"
T2 = "2026-10-07T02:01:38.0000000Z"


def alarm_row(**kw):
    r = {"t": 5.0, "k": "alarm", "dev": "sp-hauler-01", "token": "tok-x", "key": "low-fuel", "metric": "fuel_pct", "state": "ACTIVE", "sev": "MAJOR", "occ": T2}
    r.update(kw)
    return r


STEP_NOTE = "SP-HL-0001: prepare low-fuel cycle"


def snapshot_row(t=6.0, requested="2026-10-07T02:01:40.0000000Z", **kw):
    r = {"t": t, "k": "alarmSnapshot", "requested": requested, "total": len(kw.get("alarms", [])), "alarms": []}
    r.update(kw)
    return r


def make_run(root, observed_extra=(), run_patch=None, video_patch=None, extra_files=None, presenter_rows=None, device_extra=(), drop_ack=False, observed_drop=()):
    run = {
        "formatVersion": 1,
        "runId": "run-20261007T020136Z",
        "startedAtUtc": START,
        "build": {"gitSha": "19cccec06725", "trackedTreeClean": True, "sdkCommit": "69446d47b09f"},
        "platformVersion": "0.19.0",
        "instance": "sitepulse",
        "tenant": "sim-sitepulse",
        "devices": [
            {"id": "SP-HL-0001", "token": "sp-hauler-01", "kind": "Hauler"},
            {"id": "SP-HL-0002", "token": "sp-hauler-02", "kind": "Hauler"},
        ],
        "clock": [{"from": 0.01, "mode": "real", "scale": 1}],
        "end": {"cleanly": True, "durationSeconds": 60.0},
    }
    run.update(run_patch or {})
    video = {
        "complete": True,
        "steps": [
            {"name": "fleet observed", "atSeconds": 0.0, "note": "the fleet works"},
            {"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE},
        ],
    }
    video.update(video_patch or {})

    def sample(occ, **values):
        return {"t": 1.0, "k": "sample", "dev": "SP-HL-0001", "sk": "measurement", "occ": occ, "ack": occ, "values": values}

    def meas(n, v, occ, t, **kw):
        r = {"t": t, "k": "measurement", "dev": "sp-hauler-01", "n": n, "v": v, "occ": occ, "obs": occ}
        r.update(kw)
        return r

    device = list(device_extra) + [
        sample(T1, fuel_pct=90.5, tyre_pressure_kpa=700.0, engine_hours=5.0),
        sample(T2, fuel_pct=90.0),
        {"t": 2.0, "k": "sample", "dev": "SP-HL-0001", "sk": "location", "occ": T2, "ack": T2, "speed": 1.5, "elev": 1800.0, "lat": 39.0, "lon": -117.0},
        {"t": 2.0, "k": "linkState", "dev": "SP-HL-0001", "state": "Live"},
    ]
    observed = [
        {"t": 0.1, "k": "status", "source": "alarms", "state": "Connecting"},
        meas("fuel_pct", 77.0, "2026-10-07T02:01:30.0000000Z", 0.4, snap=True),  # a seed row from before the start
        {"t": 0.5, "k": "command", "dev": "sp-hauler-01", "token": "hex:eacd5c2d8d50", "n": "goto-refuel", "state": "SENT"},
        {"t": 1.0, "k": "alarmSnapshot", "requested": "2026-10-07T02:01:36.9000000Z", "total": 0, "alarms": []},
        meas("fuel_pct", 90.5, "2026-10-07T02:01:37.0000009Z", 1.2),  # the device's microsecond, a later 100 ns digit
        meas("tyre_pressure_kpa", 700.0, T1, 1.2),
        meas("engine_hours", 5.0, T1, 1.2),  # real, but not a board metric: dropped from the output
        {"t": 1.5, "k": "presence", "dev": "sp-hauler-01", "active": True},
        {"t": 2.0, "k": "location", "dev": "sp-hauler-01", "speed": 1.5, "heading": 90, "elev": 1800.0, "occ": T2},
        alarm_row(t=3.0, token="de35f6eb-1070-466a-8bf3-5dac6162fa6a", key="low-fuel", metric="fuel_pct", occ=T2),
    ] + list(observed_extra)

    observed = [r for r in observed if not any(r.get(k) == v for k, v in observed_drop)]
    if drop_ack:
        for r in device:
            r.pop("ack", None)
    os.makedirs(root, exist_ok=True)
    files = {
        "run.json": json.dumps(run),
        "video-run.json": json.dumps(video),
        "observed.ndjson": "\n".join(json.dumps(r) for r in observed) + "\n",
        "device.ndjson": "\n".join(json.dumps(r) for r in device) + "\n",
        "presenter.ndjson": "\n".join(
            json.dumps(r)
            for r in (
                presenter_rows
                if presenter_rows is not None
                else [{"t": 0.0, "k": "clock", "text": "clock: real time", "scale": 1}, {"t": 2.5, "k": "action", "text": STEP_NOTE}]
            )
        )
        + "\n",
    }
    files.update(extra_files or {})
    for n, body in files.items():
        with open(os.path.join(root, n), "w", encoding="utf-8") as f:
            f.write(body)
    return root


class Base(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.run_dir = os.path.join(self._tmp.name, "run")

    def convert(self, **kw):
        make_run(self.run_dir, **kw)
        return c.convert(self.run_dir, BOARD)


# --- the committed excerpt -----------------------------------------------------


class Excerpt(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(EXCERPT, "rb") as f:
            cls.raw = f.read()
        cls.doc = json.loads(cls.raw)

    def test_golden_tyre_pressure_at_441_2s(self):
        self.assertEqual(599.6, value_at(self.doc, "SP-HL-0003", "tyre_pressure_kpa", 441200))

    def test_golden_alarm_counts_at_441_2s(self):
        self.assertEqual(1, count_sev(self.doc, 441200, "MAJOR"))
        self.assertEqual(0, count_sev(self.doc, 441200, "CRITICAL"))
        (a,) = active_alarms(self.doc, 441200)
        self.assertEqual(("sp-hauler-03", "tyre-pressure-low"), (a["dev"], a["key"]))

    def test_the_earlier_low_fuel_alarm_history_is_carried_into_the_window(self):
        self.assertEqual(1, count_sev(self.doc, 221200, "MAJOR"))
        self.assertEqual(0, count_sev(self.doc, 300000, "MAJOR"))

    def test_the_excerpt_is_a_forty_second_window_and_small(self):
        self.assertEqual({"fromMs": 420000, "toMs": 460000}, self.doc["excerpt"])
        self.assertTrue(all(420000 <= t <= 460000 for t in self.doc["measurements"]["t"]))
        self.assertLess(len(self.raw), 100_000)

    def test_it_is_schema_valid_and_credential_clean(self):
        c.validate_output(self.doc)
        self.assertEqual([], c.scan_for_credentials(self.doc))

    def test_no_positions_no_commands_and_no_platform_ids(self):
        self.assertEqual([], self.doc["locations"])
        self.assertFalse(self.doc["channels"]["locations"] or self.doc["channels"]["commands"])
        self.assertNotIn(b"de35f6eb", self.raw)  # a platform alarm uuid
        self.assertNotIn(b"hex:", self.raw)  # a hashed command token
        self.assertTrue(all(e["id"].startswith("alarm-") for e in self.doc["alarms"]["events"]))

    def test_a_chapter_that_reports_an_alarm_is_not_before_it(self):
        assert_alarm_claims_hold(self, self.doc)
        self.assertEqual(441124, {x["name"]: x["tMs"] for x in self.doc["chapters"]}["tyre alarm"])

    def test_the_sidecar_hash_matches(self):
        with open(EXCERPT + ".sha256", encoding="utf-8") as f:
            want = f.read().split()[0]
        self.assertEqual(want, hashlib.sha256(self.raw).hexdigest())

    def test_the_pinned_board_hash_is_the_board_at_the_pinned_commit(self):
        # the board at the commit it was pinned from, not the working tree: a later board edit is legitimate
        commit, path = self.doc["board"]["sourceCommit"], self.doc["board"]["path"]
        try:
            body = subprocess.check_output(["git", "-C", REPO, "show", "%s:%s" % (commit, path)], stderr=subprocess.DEVNULL)
        except (OSError, subprocess.CalledProcessError):
            if os.environ.get("CI"):
                self.fail("commit %s is not available: the checkout must fetch full history" % commit)
            self.skipTest("commit %s is not in this clone (fetch full history to run this check)" % commit)
        self.assertEqual(self.doc["board"]["sha256"], hashlib.sha256(body).hexdigest())

    def test_the_board_has_no_command_button(self):
        with open(os.path.join(REPO, BOARD_REL), encoding="utf-8") as f:
            self.assertNotIn("command-button", f.read())  # the recording carries no command channel


# --- negative controls -----------------------------------------------------------


class Refusals(Base):
    def test_a_clean_synthetic_run_converts(self):
        doc, counts = self.convert()
        self.assertEqual(1, counts["measurementSeed"])
        self.assertEqual(3, counts["measurementMatched"])  # the fuel, tyre and engine_hours rows
        self.assertEqual(1, counts["locationMatched"])
        self.assertEqual(1, counts["commandRowsDropped"])
        self.assertEqual(3, len(doc["measurements"]["t"]))  # seed + fuel + tyre; engine_hours is not a board metric
        self.assertEqual([1, 0, 0], doc["measurements"]["s"])  # the seed flag is carried, and only on the seed
        self.assertEqual([], doc["locations"])

    def test_a_fabricated_measurement_row_is_refused(self):
        row = {"t": 4.0, "k": "measurement", "dev": "sp-hauler-01", "n": "fuel_pct", "v": 12.0, "occ": "2026-10-07T02:01:40.0000000Z"}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_a_row_with_a_matching_time_but_another_value_is_refused(self):
        row = {"t": 4.0, "k": "measurement", "dev": "sp-hauler-01", "n": "fuel_pct", "v": 12.0, "occ": T2}
        with self.assertRaisesRegex(c.Refusal, "device published"):
            self.convert(observed_extra=[row])

    def test_a_seed_row_after_the_start_with_no_sample_is_refused(self):
        row = {"t": 4.0, "k": "measurement", "dev": "sp-hauler-01", "n": "fuel_pct", "v": 12.0, "occ": "2026-10-07T02:01:40.0000000Z", "snap": True}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_a_fabricated_location_row_is_refused(self):
        row = {"t": 4.0, "k": "location", "dev": "sp-hauler-01", "speed": 1.0, "elev": 1.0, "occ": "2026-10-07T02:01:41.0000000Z"}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_an_unmatched_row_for_a_metric_the_board_does_not_use_is_still_refused(self):
        row = {"t": 4.0, "k": "measurement", "dev": "sp-hauler-01", "n": "engine_hours", "v": 1.0, "occ": "2026-10-07T02:01:40.0000000Z"}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_a_planted_jwt_in_an_allowed_alarm_field_is_refused(self):
        for field in ("key", "metric"):
            with self.subTest(field=field), self.assertRaises(c.Refusal):
                self.convert(observed_extra=[alarm_row(token="u-2", state="CLEARED", **{field: JWT})])

    def test_a_planted_credential_in_a_chapter_note_is_refused(self):
        for secret in (
            JWT,
            "-----BEGIN CERTIFICATE-----",
            "0123456789abcdef0123456789abcdef",
            "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5",
            "password: hunter2",
            "ops@example.com",
            "https://example.com/x",
            "10.0.0.7",
        ):
            video = {"steps": [{"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE}, {"name": "s", "atSeconds": 2.0, "note": "the operator typed " + secret}]}
            with self.subTest(secret=secret[:12]), self.assertRaisesRegex(c.Refusal, "credential|grammar"):
                self.convert(video_patch=video)

    def test_a_planted_credential_in_a_device_field_is_refused(self):
        devices = [{"id": "SP-HL-0001", "token": "sp-hauler-01", "kind": JWT}, {"id": "SP-HL-0002", "token": "sp-hauler-02", "kind": "Hauler"}]
        with self.assertRaises(c.Refusal):
            self.convert(run_patch={"devices": devices})

    def test_a_dirty_build_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "not clean"):
            self.convert(run_patch={"build": {"gitSha": "19cccec06725", "trackedTreeClean": False, "sdkCommit": "69446d47b09f"}})

    def test_a_run_that_did_not_end_cleanly_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "end cleanly"):
            self.convert(run_patch={"end": {"cleanly": False, "durationSeconds": 60.0}})

    def test_an_incomplete_take_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "incomplete"):
            self.convert(video_patch={"complete": False})

    def test_an_unknown_alarm_severity_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "severity"):
            self.convert(observed_extra=[alarm_row(sev="PANIC")])


class InputAllowlist(Base):
    SECRETS = {"ca.pem": "-----BEGIN CERTIFICATE-----\nSECRETSECRET\n", "operator.txt": "hunter2-operator", "player.filtered.log": "x"}

    def test_a_non_allowlisted_file_is_refused(self):
        make_run(self.run_dir, extra_files=self.SECRETS)
        for name in ("ca.pem", "operator.txt", "player.filtered.log", "sim.bin", "../ca.pem", ".raw/x"):
            with self.subTest(name=name), self.assertRaisesRegex(c.Refusal, "not an allowlisted"):
                c.read_allowed(self.run_dir, name)

    def test_conversion_opens_only_the_five_allowlisted_files(self):
        make_run(self.run_dir, extra_files=self.SECRETS)
        opened = []
        real_open = builtins.open

        def spy(file, *a, **k):
            opened.append(os.path.basename(str(file)))
            return real_open(file, *a, **k)

        with mock.patch.object(builtins, "open", spy), mock.patch("os.listdir", side_effect=AssertionError("listed the folder")), mock.patch(
            "glob.glob", side_effect=AssertionError("globbed the folder")
        ):
            doc, _ = c.convert(self.run_dir, BOARD)
        self.assertEqual(sorted(c.INPUT_ALLOWLIST), sorted(opened))
        text = json.dumps(doc)
        self.assertNotIn("SECRETSECRET", text)
        self.assertNotIn("hunter2", text)


class Output(Base):
    def test_commands_presence_and_platform_ids_are_dropped(self):
        doc, counts = self.convert()
        text = json.dumps(doc)
        self.assertNotIn("goto-refuel", text)
        self.assertNotIn("de35f6eb", text)
        self.assertNotIn("presence", text)
        self.assertEqual("alarm-1", doc["alarms"]["events"][0]["id"])
        self.assertEqual(1, counts["commandRowsDropped"])

    def test_alarm_ids_are_stable_per_platform_token(self):
        doc, _ = self.convert(observed_extra=[alarm_row(t=5.0), alarm_row(t=6.0, state="CLEARED")])
        self.assertEqual(["alarm-1", "alarm-2", "alarm-2"], [e["id"] for e in doc["alarms"]["events"]])

    def test_the_ack_flag_survives_only_when_true(self):
        doc, _ = self.convert(observed_extra=[alarm_row(ack=True)])
        self.assertTrue(doc["alarms"]["events"][-1]["ack"])
        self.assertNotIn("ack", doc["alarms"]["events"][0])

    def test_an_unknown_key_in_the_output_is_refused(self):
        doc, _ = self.convert()
        doc["extra"] = 1
        with self.assertRaisesRegex(c.Refusal, "unexpected keys"):
            c.validate_output(doc)
        doc, _ = self.convert()
        doc["alarms"]["events"][0]["lastValue"] = 3
        with self.assertRaisesRegex(c.Refusal, "unexpected keys"):
            c.validate_output(doc)

    def test_a_position_cannot_be_smuggled_into_locations(self):
        doc, _ = self.convert()
        doc["locations"] = [{"d": 0, "t": 1, "lat": 39.0, "lon": -117.0}]
        with self.assertRaisesRegex(c.Refusal, "no positions"):
            c.validate_output(doc)

    def test_an_excerpt_keeps_a_contiguous_window(self):
        make_run(self.run_dir)
        doc, _ = c.convert(self.run_dir, BOARD, excerpt=(1.1, 1.3))
        self.assertEqual(2, len(doc["measurements"]["t"]))
        self.assertTrue(all(1100 <= t <= 1300 for t in doc["measurements"]["t"]))
        self.assertEqual({"fromMs": 1100, "toMs": 1300}, doc["excerpt"])
        with self.assertRaisesRegex(c.Refusal, "excerpt"):
            c.convert(self.run_dir, BOARD, excerpt=(10.0, 20.0))

    def test_the_command_line_exits_1_on_a_refusal_and_writes_nothing(self):
        make_run(self.run_dir, run_patch={"build": {"gitSha": "x", "trackedTreeClean": False, "sdkCommit": "y"}})
        out = os.path.join(self._tmp.name, "out.json")
        with mock.patch.object(c, "resolve_board", return_value=BOARD), contextlib.redirect_stderr(io.StringIO()):
            rc = c.main([self.run_dir, "--board-path", BOARD_REL, "--board-commit", "HEAD", "--out", out])
        self.assertEqual(1, rc)
        self.assertFalse(os.path.exists(out))


class FabricationPaths(Base):
    """Rows the cross-check must not let through, each of which once converted cleanly."""

    def test_an_alarm_with_no_device_sample_behind_it_is_refused(self):
        for kw in (
            {"occ": "2026-10-07T02:01:50.0000000Z"},  # no sample at that instant
            {"metric": "tyre_pressure_kpa"},  # a sample at that instant, but of another metric
            {"dev": "sp-hauler-02"},  # a sample at that instant, but on another device
        ):
            with self.subTest(**kw), self.assertRaisesRegex(c.Refusal, "fabricated row"):
                self.convert(observed_extra=[alarm_row(token="made-up", key="engine-overheat", sev="CRITICAL", **kw)])

    def test_an_alarm_time_that_is_not_a_timestamp_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "timestamp"):
            self.convert(observed_extra=[alarm_row(occ="yesterday")])

    def test_a_snapshot_alarm_must_predate_the_run_and_is_reminted(self):
        old = {"dev": "sp-hauler-01", "token": "11111111-2222-3333-4444-555555555555", "key": "low-fuel", "metric": "fuel_pct", "state": "ACTIVE", "sev": "MAJOR"}
        snap = lambda occ: snapshot_row(alarms=[dict(old, occ=occ)])
        make_run(self.run_dir, observed_extra=[snap("2026-10-07T02:01:20.0000000Z")])
        doc, _ = c.convert(self.run_dir, BOARD)
        ids = [a["id"] for sn in doc["alarms"]["snapshots"] for a in sn["alarms"]]
        self.assertEqual(["alarm-2"], ids)  # alarm-1 is the live alarm already in the run
        self.assertNotIn("11111111", json.dumps(doc))
        make_run(self.run_dir, observed_extra=[snap("2026-10-07T02:01:40.0000000Z")])
        with self.assertRaisesRegex(c.Refusal, "not before the run start"):
            c.convert(self.run_dir, BOARD)

    def test_a_seed_row_in_the_middle_of_the_run_is_refused(self):
        row = {"t": 40.0, "k": "measurement", "dev": "sp-hauler-02", "n": "fuel_pct", "v": 3.0, "occ": "2026-10-07T02:01:30.0000000Z", "snap": True}
        with self.assertRaisesRegex(c.Refusal, "after the initial snapshot"):
            self.convert(observed_extra=[row])

    def test_a_second_seed_row_for_the_same_device_and_metric_is_refused(self):
        dup = {"t": 0.4, "k": "measurement", "dev": "sp-hauler-01", "n": "fuel_pct", "v": 5.0, "occ": "2026-10-07T02:01:29.0000000Z", "snap": True}
        make_run(self.run_dir)
        # the helper's first row is the seed; put a second one beside it, still inside the initial snapshot
        with open(os.path.join(self.run_dir, "observed.ndjson"), encoding="utf-8") as f:
            lines = f.read().splitlines()
        lines.insert(2, json.dumps(dup))
        with open(os.path.join(self.run_dir, "observed.ndjson"), "w", encoding="utf-8", newline="\n") as f:
            f.write("\n".join(lines) + "\n")
        with self.assertRaisesRegex(c.Refusal, "second seed"):
            c.convert(self.run_dir, BOARD)

    def test_a_row_with_no_time_is_refused_rather_than_given_the_previous_one(self):
        row = {"k": "measurement", "dev": "sp-hauler-01", "n": "fuel_pct", "v": 90.0, "occ": T2}
        with self.assertRaisesRegex(c.Refusal, "no time"):
            self.convert(observed_extra=[row])

    def test_a_time_one_microsecond_off_is_not_a_match(self):
        # the platform keeps microseconds; a reading 1 us away from the device's is another reading
        row = {"t": 4.0, "k": "measurement", "dev": "sp-hauler-01", "n": "fuel_pct", "v": 90.5, "occ": "2026-10-07T02:01:37.0000011Z"}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_a_pre_start_row_without_the_seed_flag_is_refused(self):
        row = {"t": 4.0, "k": "measurement", "dev": "sp-hauler-02", "n": "fuel_pct", "v": 3.0, "occ": "2026-10-07T02:01:20.0000000Z"}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_a_location_value_that_differs_from_the_device_sample_is_refused(self):
        for field, value in (("speed", 9.5), ("elev", 1900.0)):
            row = {"t": 4.0, "k": "location", "dev": "sp-hauler-01", "speed": 1.5, "heading": 90, "elev": 1800.0, "occ": T2}
            row[field] = value
            with self.subTest(field=field), self.assertRaisesRegex(c.Refusal, "device published"):
                self.convert(observed_extra=[row])

    def test_a_location_seed_is_refused_once_the_device_has_reported(self):
        row = {"t": 4.0, "k": "location", "dev": "sp-hauler-01", "speed": 1.0, "elev": 1.0, "occ": "2026-10-07T02:01:20.0000000Z"}
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(observed_extra=[row])

    def test_a_device_sample_the_platform_never_acknowledged_vouches_for_nothing(self):
        with self.assertRaisesRegex(c.Refusal, "fabricated row"):
            self.convert(drop_ack=True)


class Chapters(Base):
    def test_the_chapter_clock_is_the_presenters_not_the_video_runs(self):
        doc, _ = self.convert()  # the step is at 1.0 s on the video clock and the presenter says 2.5 s
        by_name = {ch["name"]: ch["tMs"] for ch in doc["chapters"]}
        self.assertEqual(2500, by_name["low fuel"])
        self.assertEqual(1500, by_name["fleet observed"])

    def test_no_presenter_action_to_measure_from_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "chapter clock"):
            self.convert(presenter_rows=[{"t": 0.0, "k": "clock", "text": "clock: real time", "scale": 1}])

    def test_offsets_that_disagree_are_refused(self):
        video = {"steps": [{"name": "a", "atSeconds": 1.0, "note": STEP_NOTE}, {"name": "b", "atSeconds": 2.0, "note": "second"}]}
        rows = [{"t": 2.5, "k": "action", "text": STEP_NOTE}, {"t": 9.0, "k": "action", "text": "second"}]
        with self.assertRaisesRegex(c.Refusal, "disagree"):
            self.convert(video_patch=video, presenter_rows=rows)

    def test_an_offset_beyond_the_fleet_wait_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "outside"):
            self.convert(presenter_rows=[{"t": 500.0, "k": "action", "text": STEP_NOTE}])

    def test_a_chapter_that_reports_an_alarm_is_never_before_the_alarm(self):
        # the alarm event is at t=3.0 s; the step reports it at 2.96 s on the corrected clock
        video = {
            "steps": [
                {"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE},
                {"name": "alarm", "atSeconds": 1.46, "note": "the platform says low-fuel is ACTIVE; the truck keeps driving"},
            ]
        }
        doc, _ = self.convert(video_patch=video)
        alarm_t = doc["alarms"]["events"][-1]["tMs"]
        ch = {x["name"]: x["tMs"] for x in doc["chapters"]}
        self.assertEqual(3000, alarm_t)
        self.assertEqual(3000, ch["alarm"])  # 1.46 s + 1.5 s offset = 2.96 s, moved up to the alarm itself

    def test_a_chapter_that_reports_an_alarm_nobody_raised_is_refused(self):
        video = {"steps": [{"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE}, {"name": "x", "atSeconds": 3.0, "note": "the platform says engine-overheat is ACTIVE"}]}
        with self.assertRaisesRegex(c.Refusal, "no such alarm"):
            self.convert(video_patch=video)

    def test_a_chapter_far_from_the_alarm_it_reports_is_refused(self):
        video = {"steps": [{"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE}, {"name": "x", "atSeconds": 40.0, "note": "the platform says low-fuel is ACTIVE"}]}
        with self.assertRaisesRegex(c.Refusal, "from the alarm"):
            self.convert(video_patch=video)


class FieldGrammars(Base):
    def test_a_structured_or_free_form_header_value_is_refused(self):
        bad = {
            "instance": {"id": "sitepulse", "mqtt": "dc-nats.devicechain.svc:4222"},
            "tenant": "Sim Tenant",
            "platformVersion": "0.19.0 build 4",
            "build": {"gitSha": "19cccec06725", "trackedTreeClean": True, "sdkCommit": {"x": 1}},
            "clock": [{"from": 0.01, "mode": "turbo", "scale": 1}],
        }
        for key, value in bad.items():
            with self.subTest(key=key), self.assertRaises(c.Refusal):
                self.convert(run_patch={key: value})

    def test_clock_scale_must_be_a_number(self):
        with self.assertRaises(c.Refusal):
            self.convert(run_patch={"clock": [{"from": 0.01, "mode": "real", "scale": "1; DROP"}]})

    def test_chapter_text_outside_its_grammar_is_refused(self):
        for note in ("see {\"a\": 1}", "x" * 300, "mail me @ home", "a=b", "tab\there"):
            video = {"steps": [{"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE}, {"name": "x", "atSeconds": 3.0, "note": note}]}
            with self.subTest(note=note[:20]), self.assertRaises(c.Refusal):
                self.convert(video_patch=video)
        video = {"steps": [{"name": "low fuel", "atSeconds": 1.0, "note": STEP_NOTE}, {"name": "has/slash", "atSeconds": 3.0, "note": "ok"}]}
        with self.assertRaises(c.Refusal):
            self.convert(video_patch=video)

    def test_the_finished_document_is_checked_too(self):
        doc, _ = self.convert()
        for mutate in (
            lambda d: d.update(instance={"id": "x"}),
            lambda d: d.update(tenant="has.dot"),
            lambda d: d["alarms"]["snapshots"].append({"tMs": 1, "total": 1, "alarms": [dict(d["alarms"]["events"][0], id="de35f6eb-1070-466a-8bf3-5dac6162fa6a")]}),
            lambda d: d["devices"][0].update(kind="Hauler9"),
            lambda d: d["clock"][0].update(scale="1"),
        ):
            bad = json.loads(json.dumps(doc))
            mutate(bad)
            with self.assertRaises(c.Refusal):
                c.validate_output(bad)

    def test_a_missing_required_key_is_refused(self):
        doc, _ = self.convert()
        paths = [("runId",), ("board", "sha256"), ("build", "gitSha"), ("measurements", "s"), ("alarms", "events"), ("devices", 0, "token"), ("channels", "alarms"), ("converter", "version")]
        for path in paths:
            bad = json.loads(json.dumps(doc))
            node = bad
            for k in path[:-1]:
                node = node[k]
            del node[path[-1]]
            with self.subTest(path=path), self.assertRaises(c.Refusal):
                c.validate_output(bad)


class Links(Base):
    def test_a_link_in_place_of_an_allowlisted_file_is_refused(self):
        make_run(self.run_dir)
        target = os.path.join(self._tmp.name, "elsewhere.json")
        with open(target, "w", encoding="utf-8") as f:
            f.write("{}")
        link = os.path.join(self.run_dir, "run.json")
        os.remove(link)
        try:
            os.symlink(target, link)
        except (OSError, NotImplementedError):
            self.skipTest("this machine cannot create symlinks")
        with self.assertRaisesRegex(c.Refusal, "plain regular file"):
            c.read_allowed(self.run_dir, "run.json")

    def test_a_windows_reparse_point_is_refused(self):
        make_run(self.run_dir)
        real = os.lstat(os.path.join(self.run_dir, "run.json"))

        class Junction:
            st_mode = real.st_mode
            st_file_attributes = 0x400

        with mock.patch("os.lstat", return_value=Junction()), self.assertRaisesRegex(c.Refusal, "plain regular file"):
            c.read_allowed(self.run_dir, "run.json")

    def test_a_directory_in_place_of_an_allowlisted_file_is_refused(self):
        make_run(self.run_dir)
        os.remove(os.path.join(self.run_dir, "run.json"))
        os.mkdir(os.path.join(self.run_dir, "run.json"))
        with self.assertRaisesRegex(c.Refusal, "plain regular file"):
            c.read_allowed(self.run_dir, "run.json")


class Snapshots(Base):
    OLD = {"dev": "sp-hauler-01", "key": "low-fuel", "metric": "fuel_pct", "state": "ACTIVE", "sev": "MAJOR", "occ": "2026-10-07T02:01:20.0000000Z"}

    def alarm(self, token="u-1", **kw):
        return dict(self.OLD, token=token, **kw)

    def test_the_query_send_time_is_carried_and_is_not_after_arrival(self):
        doc, _ = self.convert()
        sn = doc["alarms"]["snapshots"][0]
        self.assertEqual({"tMs", "requestedAtMs", "total", "alarms"}, set(sn))
        self.assertEqual(900, sn["requestedAtMs"])  # 02:01:36.9 against a start of 02:01:36.0
        self.assertLessEqual(sn["requestedAtMs"], sn["tMs"])

    def test_a_snapshot_with_no_send_time_is_refused(self):
        row = snapshot_row()
        del row["requested"]
        with self.assertRaisesRegex(c.Refusal, "when its query was sent"):
            self.convert(observed_extra=[row])

    def test_a_query_sent_after_the_snapshot_arrived_is_refused(self):
        with self.assertRaisesRegex(c.Refusal, "after it arrived"):
            self.convert(observed_extra=[snapshot_row(t=6.0, requested="2026-10-07T02:01:50.0000000Z")])

    def test_a_truncated_snapshot_is_refused(self):
        row = snapshot_row(alarms=[self.alarm()], truncated=True)
        with self.assertRaisesRegex(c.Refusal, "truncated"):
            self.convert(observed_extra=[row])

    def test_a_total_that_differs_from_the_list_is_refused(self):
        for total in (3, 0):
            row = snapshot_row(alarms=[self.alarm()], total=total)
            with self.subTest(total=total), self.assertRaisesRegex(c.Refusal, "lists 1"):
                self.convert(observed_extra=[row])

    def test_a_snapshot_with_no_total_is_refused(self):
        row = snapshot_row(alarms=[self.alarm()])
        del row["total"]
        with self.assertRaisesRegex(c.Refusal, "total"):
            self.convert(observed_extra=[row])

    def test_the_same_alarm_twice_in_a_snapshot_is_refused(self):
        row = snapshot_row(alarms=[self.alarm(), self.alarm()])
        with self.assertRaisesRegex(c.Refusal, "twice"):
            self.convert(observed_extra=[row])

    def test_an_alarm_id_cannot_move_to_another_device(self):
        snap = snapshot_row(t=4.0, requested="2026-10-07T02:01:39.0000000Z", alarms=[self.alarm(token="tok-y", dev="sp-hauler-02")])
        make_run(self.run_dir, observed_extra=[snap, alarm_row(t=5.0, token="tok-y", dev="sp-hauler-01")])
        with self.assertRaisesRegex(c.Refusal, "moves from device"):
            c.convert(self.run_dir, BOARD)

    def test_the_finished_document_rejects_the_same_shapes(self):
        doc, _ = self.convert(observed_extra=[snapshot_row(alarms=[self.alarm()])])
        for name, mutate in (
            ("total", lambda d: d["alarms"]["snapshots"][-1].update(total=5)),
            ("send time after arrival", lambda d: d["alarms"]["snapshots"][-1].update(requestedAtMs=10**6)),
            ("missing send time", lambda d: d["alarms"]["snapshots"][-1].pop("requestedAtMs")),
            ("duplicate", lambda d: d["alarms"]["snapshots"][-1].update(alarms=d["alarms"]["snapshots"][-1]["alarms"] * 2, total=2)),
            ("device move", lambda d: d["alarms"]["events"][0].update(dev="sp-hauler-02", id=d["alarms"]["snapshots"][-1]["alarms"][0]["id"])),
            ("chapter order", lambda d: d["chapters"].reverse()),
        ):
            bad = json.loads(json.dumps(doc))
            mutate(bad)
            with self.subTest(name), self.assertRaises(c.Refusal):
                c.validate_output(bad)


class Dates(Base):
    def test_impossible_dates_are_refused_not_rolled_over(self):
        for ts in ("2026-02-30T02:01:36.0000000Z", "2026-13-01T00:00:00Z", "2026-10-07T24:00:00Z", "2026-10-07T02:61:00Z"):
            with self.subTest(ts=ts):
                with self.assertRaises(c.Refusal):
                    c._us(ts)
                with self.assertRaises(c.Refusal):
                    c._g("timestamp", ts, "ts")
        with self.assertRaises(c.Refusal):
            self.convert(run_patch={"startedAtUtc": "2026-02-30T02:01:36.0000000Z"})
        with self.assertRaises(c.Refusal):
            self.convert(observed_extra=[alarm_row(occ="2026-02-30T02:01:36.0000000Z")])


class ChapterOrder(Base):
    def test_chapters_out_of_time_order_are_refused(self):
        video = {"steps": [{"name": "low fuel", "atSeconds": 5.0, "note": STEP_NOTE}, {"name": "earlier", "atSeconds": 2.0, "note": "ok"}]}
        with self.assertRaisesRegex(c.Refusal, "earlier than the chapter before"):
            self.convert(video_patch=video, presenter_rows=[{"t": 6.5, "k": "action", "text": STEP_NOTE}])

    def test_a_chapter_outside_an_excerpt_window_is_refused(self):
        doc, _ = c.convert(make_run(self.run_dir), BOARD, excerpt=(1.1, 3.5))
        doc["chapters"].append({"tMs": 59000, "name": "late", "note": "ok"})
        with self.assertRaisesRegex(c.Refusal, "outside the excerpt"):
            c.validate_output(doc)


class Scan(unittest.TestCase):
    def test_each_kind_is_found(self):
        cases = {
            "jwt": JWT,
            "pem": "-----BEGIN PRIVATE KEY-----",
            "hex-run": "x " + "ab" * 16 + " y",
            "base64-run": "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5",
            "keyword": "Bearer abc",
            "email": "a@b.co",
            "url": "wss://x",
            "ipv4": "192.168.1.20",
            "hostname": "broker.example.com",
        }
        for kind, text in cases.items():
            with self.subTest(kind=kind):
                self.assertTrue(any(f.startswith(kind) for f in c.scan_for_credentials(text)), c.scan_for_credentials(text))

    def test_benign_text_is_clean(self):
        for text in (
            "sp-hauler-03",
            "tyre-pressure-low",
            "2026-10-07T02:08:54.4239371Z",
            "fuel 94.8% -> 15.5%, crosses 15% in about 60 s",
            "frontend/testdata/sim-dashboards/sp-dashboard.json",
            "SP-HL-0006: prepare low-fuel cycle",
            "19cccec06725",
        ):
            with self.subTest(text=text):
                self.assertEqual([], c.scan_for_credentials(text))

    def test_pinned_hashes_pass_only_in_their_fields(self):
        h = "ab" * 32
        ok = {"board": {"sha256": h, "sourceCommit": "cd" * 20}, "sourceHashes": {"run.json": h}}
        self.assertEqual([], c.scan_for_credentials(ok))
        self.assertTrue(c.scan_for_credentials({"chapters": [{"note": h}]}))  # the same string anywhere else is a finding
        self.assertTrue(c.scan_for_credentials({"board": {"sha256": h + "ab"}}))  # a longer run in a hash field is not a hash

    def test_the_simulators_32_hex_credential_is_found_beside_other_characters(self):
        h = "0123456789abcdef0123456789abcdef"
        for text in (h, "x" + h, "0x" + h * 2, "key" + h * 2, "k=" + h, "g" + h + "g"):
            with self.subTest(text=text[:12]):
                self.assertTrue(any(f.startswith("hex-run") for f in c.scan_for_credentials(text)), text)

    def test_a_uuid_is_found(self):
        self.assertTrue(any(f.startswith("uuid") for f in c.scan_for_credentials("de35f6eb-1070-466a-8bf3-5dac6162fa6a")))

    def test_a_secret_split_in_two_is_found(self):
        h = "0123456789abcdef"
        self.assertTrue(c.scan_for_credentials(h + " " + h))
        self.assertTrue(c.scan_for_credentials(h + "-" + h))
        self.assertTrue(c.scan_for_credentials({"chapters": [{"tMs": 1, "name": h, "note": h}]}))
        self.assertTrue(c.scan_for_credentials({"a": [h, h]}))

    def test_in_cluster_names_and_ipv6_are_found(self):
        for text in ("dc-nats.devicechain.svc:4222", "nats.devicechain.svc", "gke-prod-default-pool-1", "broker:1883", "fe80::1", "2001:db8::8a2e:370:7334", "::1", "1:2:3:4:5:6:7:8"):
            with self.subTest(text=text):
                self.assertTrue(c.scan_for_credentials(text), text)
        for text in ("02:01:36", "T02:01:36.0842212Z", "SP-HL-0006: prepare", "tyre-pressure-low is ACTIVE; ok"):
            with self.subTest(text=text):
                self.assertEqual([], c.scan_for_credentials(text), text)

    def test_the_real_excerpt_and_full_sized_hash_pins_stay_clean(self):
        with open(EXCERPT, encoding="utf-8") as f:
            self.assertEqual([], c.scan_for_credentials(json.load(f)))

    def test_keys_are_scanned_too(self):
        self.assertTrue(c.scan_for_credentials({JWT: 1}))


# --- the full take2 run ------------------------------------------------------------


def _excerpt_board():
    with open(EXCERPT, encoding="utf-8") as f:
        return json.load(f)["board"]


@unittest.skipUnless(
    os.path.isfile(os.path.join(FULL_RUN, "run.json")),
    "SKIPPED: the full take2 recording is not at %s (set SITEPULSE_TAKE2_RUN); the golden values are NOT checked against it in this run" % os.path.normpath(FULL_RUN),
)
class FullTake2(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.doc, cls.counts = c.convert(FULL_RUN, _excerpt_board())

    def test_cross_check_counts(self):
        k = self.counts
        self.assertEqual(55410, k["measurementMatched"])
        self.assertEqual(70, k["measurementSeed"])
        self.assertEqual(55480, k["measurementRows"])
        self.assertEqual(12410, k["locationMatched"])
        self.assertEqual(28, k["locationSeed"])
        self.assertEqual(916, k["commandRowsDropped"])

    def test_golden_tyre_pressure_and_alarms_at_441_2s(self):
        self.assertEqual(599.6, value_at(self.doc, "SP-HL-0003", "tyre_pressure_kpa", 441200))
        self.assertEqual(1, count_sev(self.doc, 441200, "MAJOR"))
        self.assertEqual(0, count_sev(self.doc, 441200, "CRITICAL"))

    def test_golden_low_fuel_at_221_2s(self):
        self.assertEqual(14.9, value_at(self.doc, "SP-HL-0006", "fuel_pct", 221200))
        (a,) = active_alarms(self.doc, 221200)
        self.assertEqual("low-fuel", a["key"])

    def test_the_committed_excerpt_is_this_runs_window(self):
        ex, _ = c.convert(FULL_RUN, _excerpt_board(), excerpt=(420, 460))
        with open(EXCERPT, encoding="utf-8") as f:
            self.assertEqual(json.load(f), ex)

    def test_the_whole_output_is_clean(self):
        self.assertEqual([], c.scan_for_credentials(self.doc))

    def test_every_chapter_that_reports_an_alarm_is_not_before_it(self):
        assert_alarm_claims_hold(self, self.doc)
        self.assertEqual(8, len(self.doc["chapters"]))


if __name__ == "__main__":
    unittest.main()
