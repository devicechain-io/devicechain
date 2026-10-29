# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tests of the judgements in acceptance_check.py. Each has a counterpart that must FAIL: a judge that cannot say no is not one."""

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import acceptance_check as a  # noqa: E402


def ev(name, value, t):
    return {"name": name, "value": value, "occurredTime": t}


# F4-East: 25.5 -> hot at 14:08, boost sent 14:10, back at setpoint (22.0) by 14:21
def hot_room():
    out = []
    for i in range(0, 60):  # a reading every 30 s from 14:00
        minute = i * 0.5
        temp = 22.0 + min(minute, 8) * 0.55 if minute <= 10 else max(22.0, 26.4 - (minute - 10) * 0.4)
        t = f"2026-10-29T14:{int(minute):02d}:{int((minute % 1) * 60):02d}Z"
        out.append(ev("temp_c", round(temp, 2), t))
        out.append(ev("setpoint_c", 22.0, t))
    return out


BOOST = {"name": "boost-cooling", "status": "SUCCESSFUL", "queuedTime": "2026-10-29T14:09:30Z", "sentTime": "2026-10-29T14:10:00Z", "respondedTime": "2026-10-29T14:21:00Z"}


class Judges(unittest.TestCase):
    def test_boost_successful_only_at_setpoint(self):
        ok, detail = a.judge_boost_at_setpoint(BOOST, hot_room())
        self.assertTrue(ok, detail)

    def test_boost_answered_while_the_room_is_still_hot_fails(self):
        early = dict(BOOST, respondedTime="2026-10-29T14:12:00Z")
        ok, detail = a.judge_boost_at_setpoint(early, hot_room())
        self.assertFalse(ok)
        self.assertIn("NOT at setpoint", detail)

    def test_boost_sent_when_the_room_was_fine_fails(self):
        cold = [ev(n, 22.0, "2026-10-29T14:%02d:00Z" % m) for m in range(0, 30) for n in ("temp_c", "setpoint_c")]
        ok, detail = a.judge_boost_at_setpoint(BOOST, cold)
        self.assertFalse(ok)
        self.assertIn("already at setpoint", detail)

    def test_boost_answered_at_once_fails_even_if_the_room_reads_fine(self):
        evs = [ev("temp_c", 26.4, "2026-10-29T14:09:50Z"), ev("temp_c", 22.0, "2026-10-29T14:10:06Z")] + [ev("setpoint_c", 22.0, "2026-10-29T14:09:50Z")]
        quick = dict(BOOST, respondedTime="2026-10-29T14:10:08Z")
        ok, detail = a.judge_boost_at_setpoint(quick, evs)
        self.assertFalse(ok)
        self.assertIn("almost at once", detail)

    def test_boost_not_successful_or_missing_fails(self):
        self.assertFalse(a.judge_boost_at_setpoint(None, [])[0])
        self.assertFalse(a.judge_boost_at_setpoint(dict(BOOST, status="FAILED"), hot_room())[0])
        self.assertFalse(a.judge_boost_at_setpoint(dict(BOOST, sentTime=None), hot_room())[0])

    def test_alarm_cycle(self):
        al = {"alarmKey": "zone-hot", "severity": "MAJOR", "raisedTime": "2026-10-29T14:08:00Z", "state": "CLEARED", "clearedTime": "2026-10-29T14:22:00Z"}
        self.assertEqual(a.judge_alarm_cycle(al, "MAJOR")[:2], (True, True))
        self.assertEqual(a.judge_alarm_cycle(dict(al, state="ACTIVE", clearedTime=None), "MAJOR")[:2], (True, False))
        self.assertEqual(a.judge_alarm_cycle(al, "CRITICAL")[0], False)
        self.assertEqual(a.judge_alarm_cycle(None)[:2], (False, False))

    def test_rolling_average_needs_a_window_and_a_threshold(self):
        self.assertIsNotNone(a.rolling_average_exceeds(hot_room(), "temp_c", 26.0 - 0.9))
        self.assertIsNone(a.rolling_average_exceeds(hot_room(), "temp_c", 30.0))
        # a single spike is not an average
        spike = [ev("temp_c", 40.0, "2026-10-29T14:00:00Z")]
        self.assertIsNone(a.rolling_average_exceeds(spike, "temp_c", 26.0))

    def test_control_passes_only_when_nothing_reacted_and_the_room_overheated(self):
        hot = [ev("temp_c", 27.5, "2026-10-29T14:%02d:%02dZ" % (m, s)) for m in range(0, 20) for s in (0, 30)]
        items = a.judge_control([], [], hot, True)
        self.assertTrue(a.overall(items), [i.detail for i in items])
        # an alarm, a boost, a room that never got hot, or a rule that was never off each fail the control
        self.assertFalse(a.overall(a.judge_control([{"alarmKey": "zone-hot"}], [], hot, True)))
        self.assertFalse(a.overall(a.judge_control([], [{"name": "boost-cooling"}], hot, True)))
        cool = [ev("temp_c", 22.0, e["occurredTime"]) for e in hot]
        self.assertFalse(a.overall(a.judge_control([], [], cool, True)))
        self.assertFalse(a.overall(a.judge_control([], [], hot, False)))

    def test_sessions_counts_distinct_clients_of_this_tenant_only(self):
        conns = [{"mqtt_client": f"tp:sim-x:{d}"} for d in a.DEVICES] + [{"mqtt_client": "tp:other:dev"}, {"cid": 3}]
        ok, n, _ = a.judge_sessions(json.dumps({"connections": conns}), "tp", "sim-x")
        self.assertTrue(ok)
        self.assertEqual(n, 31)
        ok, n, _ = a.judge_sessions(json.dumps({"connections": conns[:-3]}), "tp", "sim-x")
        self.assertFalse(ok)
        self.assertFalse(a.judge_sessions("not json", "tp", "sim-x")[0])
        # the same device twice is one session
        dup = conns + [conns[0]]
        self.assertEqual(a.judge_sessions(json.dumps({"connections": dup}), "tp", "sim-x")[1], 31)

    def test_telemetry_needs_every_metric_of_every_type(self):
        full = {d: set(a.METRICS[a.kind_of(d)]) for d in a.DEVICES}
        self.assertTrue(a.judge_telemetry(full)[0])
        broken = dict(full)
        broken["TP-CH-A"] = {"chw_supply_c"}
        ok, detail = a.judge_telemetry(broken)
        self.assertFalse(ok)
        self.assertIn("TP-CH-A", detail)
        short = {k: v for k, v in full.items() if k != "TP-EM-T"}
        self.assertFalse(a.judge_telemetry(short)[0])

    def test_calculated_missing_input_must_be_zero_and_non_finite_must_be_explained(self):
        before = {"evaluations": 100, "missing_input": 0, "non_finite": 1, "div_zero": 0}
        after = {"evaluations": 900, "missing_input": 0, "non_finite": 2, "div_zero": 0}
        calc = {"cop", "kwh_per_m2"}
        self.assertTrue(a.judge_calculated(before, after, 1, calc)[0])
        self.assertFalse(a.judge_calculated(before, after, 0, calc)[0], "a skip nothing sent can explain")
        self.assertFalse(a.judge_calculated(before, dict(after, missing_input=3), 1, calc)[0])
        self.assertFalse(a.judge_calculated(before, dict(after, evaluations=100), 1, calc)[0])
        self.assertFalse(a.judge_calculated(before, dict(after, div_zero=1), 1, calc)[0])
        self.assertFalse(a.judge_calculated(before, after, 1, {"cop"})[0])

    def test_power_zero_counts_only_chiller_readings_sending_both_zero(self):
        s = [
            {"k": "sample", "values": {"power_kw": 0, "cooling_kw": 0}},
            {"k": "sample", "values": {"power_kw": 0.3, "cooling_kw": 0}},
            {"k": "sample", "values": {"temp_c": 22}},
            {"k": "sample", "values": {"power_kw": 0, "cooling_kw": 4}},
        ]
        self.assertEqual(a.count_power_zero(s), 1)

    def test_owner_scope(self):
        op = {d: "t" for d in a.DEVICES}
        ok, _ = a.judge_owner_scope(a.ANNEX_DEVICES, op, {"tp-annex-dashboard"}, 0, 0)
        self.assertTrue(ok)
        self.assertEqual(len(a.ANNEX_DEVICES), 7)
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES + ["TP-CH-T"], op, {"tp-annex-dashboard"}, 0, 0)[0])
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES[:-1], op, {"tp-annex-dashboard"}, 0, 0)[0])
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES, op, {"tp-annex-dashboard", "tp-dashboard"}, 0, 0)[0])
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES, op, set(), 0, 0)[0])
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES, op, {"tp-annex-dashboard"}, 5, 0)[0])
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES, op, {"tp-annex-dashboard"}, 0, 1)[0])
        self.assertFalse(a.judge_owner_scope(a.ANNEX_DEVICES, {"x": "t"}, {"tp-annex-dashboard"}, 0, 0)[0], "the control: the operator must see everything")

    def test_silence(self):
        reported = [ev("chw_supply_c", 6.5, "2026-10-29T16:%02d:00Z" % m) for m in range(20, 31)]
        reported += [ev("chw_supply_c", 6.5, "2026-10-29T16:40:00Z")]
        ok, last, gap = a.judge_silence(reported, None)
        self.assertTrue(ok)
        self.assertEqual(gap, 600.0)
        self.assertEqual(last.minute, 30)
        self.assertEqual(a.judge_silence([], None)[0], False)

    def test_an_open_gap_is_measured_to_the_alarm(self):
        live = [ev("chw_supply_c", 6.5, "2026-10-29T16:%02d:%02dZ" % (m, s)) for m in (20, 21) for s in (0, 30)]
        until = a.parse_time("2026-10-29T16:24:00Z")
        ok, last, gap = a.judge_silence(live, until)
        self.assertTrue(ok)
        self.assertEqual(gap, 150.0)
        self.assertEqual(a.judge_silence(live, None)[2], 30.0, "without the alarm's time the open gap is invisible")


class Safety(unittest.TestCase):
    def test_redact_and_leaks(self):
        jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
        self.assertNotIn("eyJ", a.redact("x " + jwt))
        self.assertNotIn("a" * 40, a.redact("k=" + "a" * 40))
        self.assertTrue(a.leaks(jwt))
        self.assertTrue(a.leaks('{"simPassword": "hunter2hunter2hunter2"}'))
        self.assertFalse(a.leaks("31/31 sessions"))

    def test_items_never_carry_a_token(self):
        i = a.Item("x", "d", True, "got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl")
        self.assertNotIn("eyJ", json.dumps(i.as_dict()))

    def test_overall_needs_a_verdict(self):
        self.assertFalse(a.overall([]))
        self.assertFalse(a.overall([a.Item("a", "d", None)]))
        self.assertTrue(a.overall([a.Item("a", "d", True), a.Item("b", "d", None)]))
        self.assertFalse(a.overall([a.Item("a", "d", True), a.Item("b", "d", False)]))

    def test_the_device_table_is_the_scenarios(self):
        self.assertEqual(len(a.DEVICES), 31)
        self.assertEqual(len(set(a.DEVICES)), 31)
        self.assertIn(a.HOT_DEVICE, a.DEVICES)
        self.assertIn(a.ANNEX_CHILLER, a.ANNEX_DEVICES)


if __name__ == "__main__":
    unittest.main()
