# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for phase_b_check.py. Run: python3 -m unittest test_phase_b_check (from this directory)."""

import datetime
import json
import os
import stat
import tempfile
import unittest
from unittest import mock

import phase_a_check as a
import phase_b_check as b

T0 = a.parse_time("2026-10-06T12:00:00Z")


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def after(seconds):
    return a.iso(T0 + datetime.timedelta(seconds=seconds))


def cmd(name, status, t, token="t1", device="SP-HL-0006-token"):
    return {"token": token, "name": name, "status": status, "queuedTime": t, "deviceToken": device, "error": None}


class FakePlatform:
    """Just enough of the device-management / event-processing / command-delivery GraphQL to run the rule control against."""

    CMD_FIELDS = a.Platform.CMD_FIELDS

    def __init__(self, active=1):
        self.rules = {b.RULE_TOKEN: True, "sp-rule-overheat": True, "sp-rule-tyre-low": True}
        self.definition = '{"type": "threshold"}'
        self.active = active
        self.snapshots = {active: dict(self.rules)}
        self.calls = []
        self.commands = []
        self.fail_health = False

    def device_tokens(self, ids):
        return {i: i + "-token" for i in ids}

    def gql(self, area, query, variables=None, retry=True):
        v = variables or {}
        if "deviceProfilesByToken" in query:
            return {"deviceProfilesByToken": [{"token": b.PROFILE_TOKEN, "activeVersion": self.active,
                                              "detectionRules": [{"token": t, "enabled": e, "definition": self.definition} for t, e in self.rules.items()]}]}
        if "updateDetectionRule" in query:
            self.calls.append(("update", v["t"], v["r"]["enabled"]))
            self.rules[v["t"]] = v["r"]["enabled"]
            return {"updateDetectionRule": {"token": v["t"], "enabled": v["r"]["enabled"]}}
        if "publishDeviceProfile" in query:
            self.active = max(self.snapshots) + 1
            self.snapshots[self.active] = dict(self.rules)
            self.calls.append(("publish", self.active))
            return {"publishDeviceProfile": {"version": self.active}}
        if "rollbackDeviceProfile" in query:
            self.calls.append(("rollback", v["v"]))
            self.active = v["v"]
            return {"rollbackDeviceProfile": {"token": b.PROFILE_TOKEN, "activeVersion": self.active}}
        if "ruleHealth" in query:
            if self.fail_health:
                return {"ruleHealth": []}
            return {"ruleHealth": [{"ruleToken": t, "name": t, "status": "ACTIVE", "fireCount": 0} for t, e in self.snapshots[self.active].items() if e]}
        if "commands(criteria" in query:
            return {"commands": {"results": list(self.commands)}}
        if "cancelCommand" in query:
            self.calls.append(("cancel", v["t"]))
            return {"cancelCommand": {"token": v["t"], "status": "CANCELLED"}}
        raise AssertionError("unexpected query " + query[:60])


class Judges(unittest.TestCase):
    def test_a_command_in_the_window_fails_the_no_command_judge(self):
        ok, detail = b.judge_no_commands([cmd("goto-refuel", "SENT", after(30))], T0)
        self.assertFalse(ok)
        self.assertIn("goto-refuel", detail)

    def test_a_command_before_the_window_does_not(self):
        self.assertTrue(b.judge_no_commands([cmd("goto-refuel", "SUCCESSFUL", after(-30))], T0)[0])
        self.assertTrue(b.judge_no_commands([], T0)[0])

    def test_an_active_alarm_or_one_raised_in_the_window_fails(self):
        self.assertFalse(b.judge_no_alarm([{"alarmKey": "low-fuel", "state": "ACTIVE", "raisedTime": after(-500)}], "low-fuel", T0)[0])
        self.assertFalse(b.judge_no_alarm([{"alarmKey": "low-fuel", "state": "CLEARED", "raisedTime": after(10)}], "low-fuel", T0)[0])
        self.assertTrue(b.judge_no_alarm([{"alarmKey": "low-fuel", "state": "CLEARED", "raisedTime": after(-10)}], "low-fuel", T0)[0])
        self.assertTrue(b.judge_no_alarm([{"alarmKey": "engine-overheat", "state": "ACTIVE", "raisedTime": after(10)}], "low-fuel", T0)[0])

    def test_fuel_series_crossing_and_rises(self):
        ev = [{"value": 16.0, "occurredTime": after(0)}, {"value": 14.0, "occurredTime": after(1)}, {"value": 13.0, "occurredTime": after(2)}]
        self.assertEqual(T0 + datetime.timedelta(seconds=1), b.fuel_crossed(ev))
        self.assertEqual([], b.fuel_rises(ev))
        ev.append({"value": 90.0, "occurredTime": after(3)})
        rises = b.fuel_rises(ev)
        self.assertEqual(1, len(rises))
        self.assertEqual((13.0, 90.0), rises[0][1:])
        self.assertIsNone(b.fuel_crossed([{"value": 50.0, "occurredTime": after(0)}]))

    def test_rule_gone_needs_the_siblings_to_remain(self):
        sib = [{"ruleToken": t, "status": "ACTIVE"} for t in b.OTHER_RULE_TOKENS]
        self.assertTrue(b.judge_rule_gone(sib)[0])
        self.assertFalse(b.judge_rule_gone(sib + [{"ruleToken": b.RULE_TOKEN, "status": "ACTIVE"}])[0])
        ok, detail = b.judge_rule_gone(sib[:1])
        self.assertFalse(ok)
        self.assertIn("more than the one rule", detail)
        self.assertFalse(b.judge_rule_gone([])[0])

    def test_rule_running(self):
        self.assertTrue(b.judge_rule_running([{"ruleToken": b.RULE_TOKEN, "status": "ACTIVE"}])[0])
        self.assertFalse(b.judge_rule_running([{"ruleToken": b.RULE_TOKEN, "status": "COMPILE_ERROR"}])[0])
        self.assertFalse(b.judge_rule_running([])[0])

    def state(self):
        return {"enabled": True, "definitionHash": b.definition_hash('{"type": "threshold"}'), "others": {"sp-rule-overheat": True, "sp-rule-tyre-low": True},
                "activeVersion": 1}

    def profile(self, enabled=True, active=1, definition='{"type": "threshold"}'):
        return {"activeVersion": active, "detectionRules": [{"token": b.RULE_TOKEN, "enabled": enabled, "definition": definition},
                                                           {"token": "sp-rule-overheat", "enabled": True, "definition": "x"},
                                                           {"token": "sp-rule-tyre-low", "enabled": True, "definition": "x"}]}

    HEALTH = [{"ruleToken": b.RULE_TOKEN, "status": "ACTIVE"}]

    def test_restored_means_draft_version_and_engine_all_back(self):
        self.assertTrue(b.judge_rule_restored(self.state(), self.profile(), self.HEALTH)[0])

    def test_each_way_of_not_being_restored_is_named(self):
        for profile, health, word in [
            (self.profile(enabled=False), self.HEALTH, "enabled"),
            (self.profile(active=2), self.HEALTH, "active version"),
            (self.profile(definition="{}"), self.HEALTH, "definition"),
            (self.profile(), [], "ruleHealth"),
        ]:
            ok, detail = b.judge_rule_restored(self.state(), profile, health)
            self.assertFalse(ok, word)
            self.assertIn(word, detail)

    def test_replicas(self):
        self.assertTrue(b.judge_replicas("event-management", 1, "event-management 1 1")[0])
        self.assertFalse(b.judge_replicas("event-management", 1, "event-management 1")[0])   # ready absent: not ready
        self.assertTrue(b.judge_replicas("event-management", 0, "event-management 0")[0])
        self.assertFalse(b.judge_replicas("event-management", 1, "")[0])
        self.assertFalse(b.judge_replicas("event-management", 1, "device-state 1 1")[0])

    def test_outage_bound(self):
        self.assertTrue(b.judge_outage_bound(T0, after(30))[0])
        self.assertFalse(b.judge_outage_bound(T0, after(300))[0])
        self.assertFalse(b.judge_outage_bound(T0, None)[0])


class SoakJudges(unittest.TestCase):
    def test_every_refuel_must_be_successful_and_there_must_be_one_per_cycle(self):
        good = [cmd("goto-refuel", "SUCCESSFUL", after(i), token=f"c{i}") for i in range(5)]
        items = {i.id: i for i in b.judge_soak_commands(good, 5)}
        self.assertTrue(all(i.ok for i in items.values()))
        self.assertFalse(b.judge_soak_commands(good[:4], 5)[0].ok, "a cycle with no command at all is not a pass")
        self.assertFalse(b.judge_soak_commands([], 0)[0].ok, "no cycle prepared means nothing was measured")

    def test_an_open_or_failed_command_is_named(self):
        cmds = [cmd("goto-refuel", "SUCCESSFUL", after(1), token="a"), cmd("goto-refuel", "SENT", after(2), token="b"), cmd("goto-area", "FAILED", after(3), token="c")]
        items = {i.id: i for i in b.judge_soak_commands(cmds, 2)}
        self.assertFalse(items["soak-refuels-successful"].ok)
        self.assertFalse(items["soak-no-open-commands"].ok)
        self.assertFalse(items["soak-no-failed-commands"].ok)

    def test_cadence_is_the_rate_actually_achieved(self):
        ts = [T0 + datetime.timedelta(seconds=i) for i in range(11)]
        samples = [{"device": "SP-HL-0001", "kind": "measurement", "_time": t} for t in ts] + [{"device": "SP-HL-0001", "kind": "location", "_time": ts[0]}]
        out = b.cadence(samples)
        self.assertEqual(11, out["SP-HL-0001"]["measurement"]["count"])
        self.assertEqual(1.0, out["SP-HL-0001"]["measurement"]["hz"])
        self.assertIsNone(out["SP-HL-0001"]["location"]["hz"])

    def test_memory_rows_and_summary(self):
        rows = b.parse_memory("1000\t500\n1060\t520\nnot a row\n1120\t560\n")
        self.assertEqual(3, len(rows))
        s = b.memory_summary(rows)
        self.assertEqual((500, 560, 560), (s["firstMiB"], s["lastMiB"], s["maxMiB"]))
        self.assertEqual(30.0, s["growthMiBPerMin"])
        self.assertEqual({"samples": 0}, b.memory_summary([]))

    def test_the_soak_section_carries_every_number_it_was_given(self):
        soak = {"minutes": 30, "referenceHardware": "Some CPU, Some GPU", "frame": {"n": 1000, "p50Ms": 8.1, "p95Ms": 12.2, "p99Ms": 20.5, "maxMs": 90.0},
                "lag": {"n": 500, "p50Ms": 120.0, "p95Ms": 300.0, "maxMs": 900.0}, "memory": {"samples": 2, "firstMiB": 500, "lastMiB": 520, "maxMiB": 530, "growthMiBPerMin": 0.5},
                "sessions": {"expected": 19, "min": 19, "max": 19}, "gaps": {"maxSeconds": 4.5, "device": "SP-LD-0001", "over15s": []}, "dropped": 0, "sendErrors": 0,
                "reconnectEpisodes": 0, "commands": {"total": 5, "refuels": 5, "failed": 0},
                "cadence": {"SP-HL-0001": {"measurement": {"count": 1700, "hz": 0.94}}}, "cycles": [{"index": 1, "machine": "SP-HL-0006", "preparedAt": "t", "outcome": "SUCCESS: ok"}],
                "perMinute": [{"minute": 1, "sessions": 19, "observed": 19, "presenceActive": 19, "reconnecting": 0, "dropped": 0, "frameP95Ms": 12.0, "bayHolder": None, "bayWaiting": 0}]}
        text = "\n".join(b.soak_section(soak))
        for needle in ("Some CPU, Some GPU", "8.1 ms / 12.2 ms / 20.5 ms / 90.0 ms", "120.0 ms / 300.0 ms / 900.0 ms", "500 / 520 / 530 MiB", "SP-HL-0001 | 1700 (0.94 Hz)", "SP-LD-0001"):
            self.assertIn(needle, text)


class RuleRestore(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.path = os.path.join(self.dir, b.RULE_STATE)

    def test_the_state_is_recorded_before_anything_changes(self):
        p = FakePlatform()
        state, problem = b.record_rule_state(p, self.path)
        self.assertIsNone(problem)
        self.assertEqual([], p.calls, "recording must not mutate")
        self.assertEqual(1, json.loads(read(self.path))["activeVersion"])
        self.assertTrue(state["enabled"])

    def test_a_rule_already_disabled_is_not_a_baseline_to_restore_to(self):
        p = FakePlatform()
        p.rules[b.RULE_TOKEN] = False
        state, problem = b.record_rule_state(p, self.path)
        self.assertIsNone(state)
        self.assertIn("already disabled", problem)
        self.assertFalse(os.path.exists(self.path))

    def test_a_disabled_and_published_rule_is_rolled_back_and_verified(self):
        p = FakePlatform()
        b.record_rule_state(p, self.path)
        b.set_rule_enabled(p, b.RULE_TOKEN, False)
        b.publish_profile(p, b.PROFILE_TOKEN, "l", "d")
        self.assertEqual(2, p.active)
        with mock.patch("time.sleep"):
            items = b.restore_rule(p, self.path, timeout=0, quiet_seconds=0)
        self.assertTrue(items[0].ok, items[0].detail)
        self.assertTrue(p.rules[b.RULE_TOKEN])
        self.assertEqual(1, p.active)
        self.assertIn(("rollback", 1), p.calls)
        self.assertTrue(json.loads(read(self.path))["restored"])
        self.assertTrue(any(i.id == "rule-restore-quiet" and i.ok for i in items))

    def test_a_crash_between_disable_and_publish_needs_only_the_draft_back(self):
        p = FakePlatform()
        b.record_rule_state(p, self.path)
        b.set_rule_enabled(p, b.RULE_TOKEN, False)
        with mock.patch("time.sleep"):
            items = b.restore_rule(p, self.path, timeout=0, quiet_seconds=0)
        self.assertTrue(items[0].ok, items[0].detail)
        self.assertNotIn("rollback", [c[0] for c in p.calls], "the active version was never moved: nothing to roll back")

    def test_restoring_twice_does_nothing_the_second_time(self):
        p = FakePlatform()
        b.record_rule_state(p, self.path)
        b.set_rule_enabled(p, b.RULE_TOKEN, False)
        with mock.patch("time.sleep"):
            b.restore_rule(p, self.path, timeout=0, quiet_seconds=0)
            before = list(p.calls)
            self.assertEqual([], b.restore_rule(p, self.path, timeout=0, quiet_seconds=0))
        self.assertEqual(before, p.calls)

    def test_a_restore_that_does_not_verify_keeps_its_record_and_fails(self):
        p = FakePlatform()
        b.record_rule_state(p, self.path)
        b.set_rule_enabled(p, b.RULE_TOKEN, False)
        p.fail_health = True   # the engine never shows the rule running again
        with mock.patch("time.sleep"):
            items = b.restore_rule(p, self.path, timeout=0, quiet_seconds=0)
        self.assertFalse(items[0].ok)
        self.assertFalse(json.loads(read(self.path))["restored"], "an unverified restore must stay retryable")

    def test_a_command_after_the_restore_is_named_and_cancelled(self):
        p = FakePlatform()
        b.record_rule_state(p, self.path)
        b.set_rule_enabled(p, b.RULE_TOKEN, False)
        p.commands = [cmd("goto-refuel", "QUEUED", a.iso(datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=5)), token="stray")]
        with mock.patch("time.sleep"):
            items = b.restore_rule(p, self.path, timeout=0, quiet_seconds=0)
        quiet = [i for i in items if i.id == "rule-restore-quiet"][0]
        self.assertFalse(quiet.ok)
        self.assertIn(("cancel", "stray"), p.calls)

    def test_the_control_restores_even_when_the_disable_did_not_take_effect(self):
        p = FakePlatform()
        p.fail_health = False
        # the engine keeps reporting the low-fuel rule: pretend publish did not remove it
        real = p.gql

        def stubborn(area, query, variables=None, retry=True):
            if "ruleHealth" in query:
                return {"ruleHealth": [{"ruleToken": t, "name": t, "status": "ACTIVE", "fireCount": 0} for t in p.rules]}
            return real(area, query, variables, retry)

        p.gql = stubborn
        args = mock.Mock(dir=self.dir, reach_timeout=1, finish_timeout=1, ingest_grace=0)
        awaiting = {"phase": "awaiting-go", "final": False}
        with mock.patch.object(a, "wait_result", return_value=awaiting), mock.patch("time.sleep"), mock.patch("time.time", side_effect=[0, 0, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000]):
            items, _ = b.run_rule_disabled(args, p, T0)
        ids = {i.id: i for i in items}
        self.assertFalse(ids["rule-disabled-effective"].ok)
        self.assertTrue(p.rules[b.RULE_TOKEN], "the draft is enabled again")
        self.assertEqual(1, p.active, "the active version is the original again")
        self.assertTrue(ids["rule-restored"].ok, ids["rule-restored"].detail)
        self.assertFalse(os.path.exists(os.path.join(self.dir, b.GO)), "the player was never told to go over a rule that is still running")


class ScaleRestore(unittest.TestCase):
    """A stand-in live-env.sh: `replicas <d>` and `scale <d> <n>` over a file, so restore_scale runs for real."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.store = os.path.join(self.dir, "replicas")
        self.script = os.path.join(self.dir, "live-env.sh")
        with open(self.script, "w") as f:
            f.write('#!/usr/bin/env bash\nstore="%s"\ncase "$1" in\nreplicas) echo "$2 $(cat "$store") $(cat "$store")" ;;\n'
                    'scale) echo "$3" >"$store" ;;\nesac\n' % self.store)
        os.chmod(self.script, os.stat(self.script).st_mode | stat.S_IEXEC)
        self.pending = os.path.join(self.dir, b.SCALE_PENDING)

    def test_a_pending_scale_is_put_back_verified_and_its_record_removed(self):
        with open(self.store, "w") as f:
            f.write("0\n")
        with open(self.pending, "w") as f:
            f.write("event-management\t1\n")
        items = b.restore_scale(self.script, self.pending, timeout=5)
        self.assertTrue(items[0].ok, items[0].detail)
        self.assertEqual("1", read(self.store).strip())
        self.assertFalse(os.path.exists(self.pending))

    def test_a_restore_that_never_verifies_keeps_its_record(self):
        # a deployment that stays at zero whatever it is told
        with open(self.script, "w") as f:
            f.write('#!/usr/bin/env bash\ncase "$1" in\nreplicas) echo "$2 0" ;;\nesac\n')
        with open(self.pending, "w") as f:
            f.write("event-management\t1\n")
        with mock.patch("time.sleep"):
            items = b.restore_scale(self.script, self.pending, timeout=0)
        self.assertFalse(items[0].ok)
        self.assertTrue(os.path.exists(self.pending))
        self.assertEqual("event-management\t1", read(self.pending).strip())

    def test_the_outage_control_puts_the_deployment_back_even_when_it_blows_up_midway(self):
        with open(self.store, "w") as f:
            f.write("1\n")
        args = mock.Mock(dir=self.dir, live_env=self.script, outage_deploy="event-management", outage_seconds=0, reach_timeout=1, finish_timeout=1,
                         ingest_grace=0, storage_timeout=0)
        steady = {"final": False, "observerOutage": {"steadyAt": "2026-10-06T12:00:00Z"}}
        with mock.patch.object(a, "wait_result", side_effect=[steady, RuntimeError("the checker died")]), mock.patch("time.sleep"):
            with self.assertRaises(RuntimeError):
                b.run_observer_outage(args, None, T0)
        self.assertEqual("1", read(self.store).strip(), "event-management is back at one replica")
        self.assertFalse(os.path.exists(self.pending), "and the record of the change is gone only because the restore verified")

    def test_the_outage_control_scales_to_zero_and_records_before_it_does(self):
        with open(self.store, "w") as f:
            f.write("1\n")
        seen = []
        real = b._live_env

        def spy(live_env, *argv):
            if argv[0] == "scale" and argv[2] == "0":
                seen.append(os.path.exists(self.pending))
            return real(live_env, *argv)

        args = mock.Mock(dir=self.dir, live_env=self.script, outage_deploy="event-management", outage_seconds=0, reach_timeout=1, finish_timeout=1,
                         ingest_grace=0, storage_timeout=0)
        steady = {"final": False, "observerOutage": {"steadyAt": "2026-10-06T12:00:00Z"}}
        with mock.patch.object(a, "wait_result", side_effect=[steady, None]), mock.patch("time.sleep"), mock.patch.object(b, "_live_env", side_effect=spy):
            items, _ = b.run_observer_outage(args, None, T0)
        self.assertEqual([True], seen, "the undo record exists before the deployment is scaled down")
        self.assertEqual("1", read(self.store).strip())
        self.assertTrue(any(i.id == "replicas-restored" and i.ok for i in items))

    def test_no_pending_record_means_nothing_to_do(self):
        self.assertEqual([], b.restore_scale(self.script, self.pending))


class Protocol(unittest.TestCase):
    def test_the_redelivery_note_points_at_real_tests(self):
        for needle in ("MqttRealBrokerTests.cs", "ARealRedeliveryIsAnsweredAgainWithoutRerunningTheHandler", "MqttDeviceSessionTests.cs", "LwM2M", "MaxConcurrentCommands = 4"):
            self.assertIn(needle, b.REDELIVERY_INFO)

    def test_the_real_sdk_tests_named_exist(self):
        root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
        for path, name in [("sdks/csharp/tests/DeviceChain.Sdk.Tests/MqttRealBrokerTests.cs", "ARealRedeliveryIsAnsweredAgainWithoutRerunningTheHandler"),
                           ("sdks/csharp/tests/DeviceChain.Sdk.Tests/MqttDeviceSessionTests.cs", "ARedeliveredCommandIsAnsweredAgainWithoutRerunningTheHandler")]:
            with open(os.path.join(root, path), encoding="utf-8") as f:
                self.assertIn(name, f.read(), path)


if __name__ == "__main__":
    unittest.main()
