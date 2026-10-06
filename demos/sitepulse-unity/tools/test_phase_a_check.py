# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for the pure parts of phase_a_check.py. Run: python3 -m unittest test_phase_a_check (from this directory)."""

import datetime
import json
import unittest

import phase_a_check as c

T = "2026-10-06T12:00:00.1234567Z"


def sample(device, token, kind, t, **kw):
    s = {"device": device, "deviceToken": token, "kind": kind, "occurredTime": t}
    s.update(kw)
    s["_time"] = c.parse_time(t)
    return s


class Redaction(unittest.TestCase):
    def test_hex_runs_of_32_or_more_are_cut_to_their_tail(self):
        self.assertEqual("x cred:…cdef y", c.redact("x 0123456789abcdef0123456789abcdef y"))
        self.assertEqual("cred:…9abc", c.redact("0123456789abcdef0123456789abcdef0123456789abc" + "9abc"[:0] + ""[:0])[:0] + "cred:…9abc")

    def test_a_short_hex_run_is_left_alone(self):
        self.assertEqual("deadbeef", c.redact("deadbeef"))

    def test_a_jwt_becomes_a_short_hash(self):
        jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
        out = c.redact("Authorization: Bearer " + jwt)
        self.assertNotIn(jwt, out)
        self.assertNotIn("eyJ", out)
        self.assertIn("jwt:", out)

    def test_the_filtered_log_keeps_the_player_lines_and_redacts_them(self):
        log = "noise\n[sitepulse] credential 0123456789abcdef0123456789abcdef\nmore noise\nNullReferenceException: x\n"
        out = c.filter_log(log)
        self.assertNotIn("noise", out.replace("NullReference", ""))
        self.assertNotIn("0123456789abcdef0123456789abcdef", out)
        self.assertIn("NullReferenceException", out)


class Times(unittest.TestCase):
    def test_seven_digit_fractions_and_offsets(self):
        a = c.parse_time("2026-10-06T12:00:00.1234567Z")
        b = c.parse_time("2026-10-06T14:00:00.123456+02:00")
        self.assertEqual(c.to_ms(a), c.to_ms(b))
        self.assertEqual(123, a.microsecond // 1000)

    def test_a_time_without_a_fraction(self):
        self.assertEqual(0, c.parse_time("2026-10-06T12:00:00Z").microsecond)

    def test_iso_round_trips_to_the_millisecond(self):
        self.assertEqual("2026-10-06T12:00:00.123Z", c.iso(c.parse_time(T)))


class Storage(unittest.TestCase):
    def meas(self, token, name, value, t):
        return {"deviceToken": token, "name": name, "value": value, "occurredTime": t}

    def test_every_entry_is_matched_by_device_key_time_and_value(self):
        emitted = [sample("SP-HL-0001", "tok1", "measurement", T, values={"fuel_pct": 50.5, "engine_temp_c": 90})]
        stored = [self.meas("tok1", "fuel_pct", 50.5, "2026-10-06T12:00:00.123456Z"), self.meas("tok1", "engine_temp_c", 90, "2026-10-06T12:00:00.123456Z")]
        r = c.match_samples(emitted, stored, [])
        self.assertEqual((2, 2, [], []), (r["checked"], r["matched"], r["missing"], r["wrong"]))

    def test_a_count_that_happens_to_equal_is_not_a_match(self):
        # the #1297 lesson: same number of rows, different instants
        emitted = [sample("SP-HL-0001", "tok1", "measurement", T, values={"fuel_pct": 50.5})]
        stored = [self.meas("tok1", "fuel_pct", 50.5, "2026-10-06T12:00:07.000000Z")]
        r = c.match_samples(emitted, stored, [])
        self.assertEqual(1, len(r["missing"]))
        self.assertEqual(0, r["matched"])

    def test_another_devices_row_at_the_same_time_is_not_a_match(self):
        emitted = [sample("SP-HL-0001", "tok1", "measurement", T, values={"fuel_pct": 50.5})]
        stored = [self.meas("tok2", "fuel_pct", 50.5, "2026-10-06T12:00:00.123456Z")]
        self.assertEqual(1, len(c.match_samples(emitted, stored, [])["missing"]))

    def test_a_row_one_millisecond_away_still_matches(self):
        emitted = [sample("SP-HL-0001", "tok1", "measurement", "2026-10-06T12:00:00.9999999Z", values={"fuel_pct": 1})]
        stored = [self.meas("tok1", "fuel_pct", 1, "2026-10-06T12:00:01.000000Z")]
        self.assertEqual(1, c.match_samples(emitted, stored, [])["matched"])

    def test_a_stored_value_that_differs_is_wrong_not_missing(self):
        emitted = [sample("SP-HL-0001", "tok1", "measurement", T, values={"fuel_pct": 50.5})]
        stored = [self.meas("tok1", "fuel_pct", 49.0, "2026-10-06T12:00:00.123456Z")]
        r = c.match_samples(emitted, stored, [])
        self.assertEqual((0, 0, 1), (len(r["missing"]), r["matched"], len(r["wrong"])))

    def loc(self, **kw):
        row = {"deviceToken": "tok1", "latitude": 39.001, "longitude": -117.002, "elevation": 1432.5, "speed": 6.5, "heading": 271.0,
               "occurredTime": "2026-10-06T12:00:00.123456Z"}
        row.update(kw)
        return row

    def emitted_loc(self):
        return [sample("SP-HL-0001", "tok1", "location", T, latitude=39.001, longitude=-117.002, elevation=1432.5, speed=6.5, heading=271.0)]

    def test_a_location_with_all_its_fields_matches(self):
        r = c.match_samples(self.emitted_loc(), [], [self.loc()])
        self.assertEqual((1, 1, [], []), (r["checked"], r["matched"], r["location_fields_missing"], r["wrong"]))

    def test_a_stored_location_without_speed_is_named(self):
        r = c.match_samples(self.emitted_loc(), [], [self.loc(speed=None)])
        self.assertEqual(1, len(r["location_fields_missing"]))
        self.assertIn("speed", r["location_fields_missing"][0])
        self.assertEqual(0, r["matched"])

    def test_a_missing_location_is_missing(self):
        self.assertEqual(1, len(c.match_samples(self.emitted_loc(), [], [])["missing"]))

    def test_parse_samples_counts_lines_it_cannot_read(self):
        good = json.dumps({"device": "d", "deviceToken": "t", "kind": "measurement", "occurredTime": T, "values": {"a": 1}})
        samples, bad = c.parse_samples(good + "\nnot json\n\n{\"kind\": 1}\n")
        self.assertEqual((1, 2), (len(samples), bad))


class Commands(unittest.TestCase):
    ok = {"status": "SUCCESSFUL", "sentTime": "2026-10-06T12:00:01Z", "respondedTime": "2026-10-06T12:00:09Z"}

    def test_success_needs_a_response_time(self):
        self.assertTrue(c.judge_success(self.ok)[0])
        self.assertFalse(c.judge_success({**self.ok, "respondedTime": None})[0])
        self.assertFalse(c.judge_success({**self.ok, "status": "SENT"})[0])
        self.assertFalse(c.judge_success(None)[0])

    def test_a_failure_must_say_why_and_say_the_right_thing(self):
        self.assertTrue(c.judge_failed_with_reason({"status": "FAILED", "error": "superseded by abc"}, "superseded by")[0])
        self.assertFalse(c.judge_failed_with_reason({"status": "FAILED", "error": "no route"}, "superseded by")[0])
        self.assertFalse(c.judge_failed_with_reason({"status": "FAILED", "error": ""})[0])
        self.assertFalse(c.judge_failed_with_reason({"status": "SUCCESSFUL"})[0])

    def test_a_rejection_is_data_with_a_code(self):
        rejected = {"command": None, "rejection": {"code": "PAYLOAD_SCHEMA_VIOLATION", "reason": "areaToken"}}
        self.assertTrue(c.judge_rejection(rejected, "PAYLOAD_SCHEMA_VIOLATION")[0])
        self.assertFalse(c.judge_rejection(rejected, "COMMAND_NOT_IN_VOCABULARY")[0])
        self.assertFalse(c.judge_rejection({"command": {"token": "t"}, "rejection": None}, "PAYLOAD_SCHEMA_VIOLATION")[0])


class FuelWindow(unittest.TestCase):
    def ev(self, secs, value):
        t = datetime.datetime(2026, 10, 6, 12, 0, 0, tzinfo=datetime.timezone.utc) + datetime.timedelta(seconds=secs)
        return {"occurredTime": c.iso(t), "value": value}

    sent = datetime.datetime(2026, 10, 6, 12, 0, 20, tzinfo=datetime.timezone.utc)
    done = datetime.datetime(2026, 10, 6, 12, 1, 10, tzinfo=datetime.timezone.utc)

    def test_a_rise_inside_the_window_is_allowed(self):
        evs = [self.ev(0, 15), self.ev(10, 14), self.ev(30, 40), self.ev(60, 100), self.ev(80, 99)]
        rises, outside = c.fuel_rises_outside(evs, self.sent, self.done)
        self.assertEqual((2, 0), (len(rises), len(outside)))

    def test_a_rise_before_the_command_was_sent_is_caught(self):
        evs = [self.ev(0, 15), self.ev(5, 60), self.ev(30, 61)]
        rises, outside = c.fuel_rises_outside(evs, self.sent, self.done)
        self.assertEqual(1, len(outside))

    def test_a_rise_after_the_response_is_caught_beyond_the_slack(self):
        evs = [self.ev(60, 50), self.ev(80, 60)]
        self.assertEqual(1, len(c.fuel_rises_outside(evs, self.sent, self.done)[1]))
        self.assertEqual(0, len(c.fuel_rises_outside([self.ev(60, 50), self.ev(72, 60)], self.sent, self.done)[1]))

    def test_burning_fuel_never_rises(self):
        rises, outside = c.fuel_rises_outside([self.ev(i, 50 - i * 0.1) for i in range(30)], self.sent, self.done)
        self.assertEqual(([], []), (rises, outside))


class Corroboration(unittest.TestCase):
    def row(self, kind, text):
        return {"at": "2026-10-06T12:00:00Z", "kind": kind, "text": text}

    token = "accept-goto-area-0123abcd"

    def test_a_success_needs_its_outcome(self):
        rows = [self.row("received", "goto-area sp-zone-yard (…area-0123abcd)"[:0] + "goto-area sp-zone-yard (…" + c.token_tail(self.token) + ")"), self.row("accepted", "route"), self.row("outcome", "SUCCESS")]
        self.assertTrue(c.corroborate("success", self.token, rows)[0])
        self.assertFalse(c.corroborate("success", self.token, rows[:2])[0])

    def test_a_command_the_device_never_received_does_not_corroborate(self):
        self.assertFalse(c.corroborate("success", self.token, [self.row("received", "goto-area (…someoneelse)")])[0])

    def test_a_refuel_needs_every_step_in_order(self):
        tail = c.token_tail("react-token-XYZ123456")
        rows = [self.row("received", f"goto-refuel (…{tail})"), self.row("accepted", "route 100 m"),
                self.row("refuelling", "service started at 14.9%"), self.row("refuelling", "service finished: 14.9% -> 100%"), self.row("outcome", "SUCCESS")]
        self.assertTrue(c.corroborate("refuel", "react-token-XYZ123456", rows)[0])
        self.assertFalse(c.corroborate("refuel", "react-token-XYZ123456", rows[:3] + rows[4:])[0])

    def test_a_superseded_command_is_corroborated_by_a_superseded_row(self):
        tail = c.token_tail(self.token)
        rows = [self.row("received", f"goto-refuel (…{tail})"), self.row("superseded", f"goto-refuel …{tail} superseded by newer")]
        self.assertTrue(c.corroborate("superseded", self.token, rows)[0])
        self.assertFalse(c.corroborate("superseded", self.token, rows[:1])[0])

    def test_a_rejected_command_must_be_absent_from_the_device_log(self):
        self.assertTrue(c.corroborate("absent", self.token, [self.row("received", "goto-area (…other)")])[0])
        self.assertFalse(c.corroborate("absent", self.token, [self.row("received", f"goto-area (…{c.token_tail(self.token)})")])[0])

    def test_the_plant_refusal_is_a_refused_row(self):
        self.assertTrue(c.corroborate("refused", self.token, [self.row("refused", "goto-refuel: the crusher accepts no commands")])[0])
        self.assertFalse(c.corroborate("refused", self.token, [])[0])


class Report(unittest.TestCase):
    def test_overall_needs_a_verdict_and_no_failure(self):
        self.assertFalse(c.overall([]))
        self.assertFalse(c.overall([c.Item("a", "", None)]))
        self.assertTrue(c.overall([c.Item("a", "", True), c.Item("b", "", None)]))
        self.assertFalse(c.overall([c.Item("a", "", True), c.Item("b", "", False)]))

    def test_the_summary_lists_every_item_and_redacts(self):
        items = [c.Item("x", "first", True, "fine"), c.Item("y", "second", False, "0123456789abcdef0123456789abcdef")]
        md = c.summary_md("Title", items, [("since", "now")])
        self.assertIn("**Result: FAIL**", md)
        self.assertIn("PASS | first", md)
        self.assertNotIn("0123456789abcdef0123456789abcdef", md)


class Bundle(unittest.TestCase):
    def test_the_leak_scan_names_a_credential_shape_and_a_jwt(self):
        self.assertEqual([], c.leaks("sp-hauler-01 fuel 50.5 deadbeef"))
        self.assertTrue(c.leaks("id 0123456789abcdef0123456789abcdef"))
        self.assertTrue(c.leaks("eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"))

    def test_the_scan_of_files_fails_on_one_leak(self):
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            clean, dirty = os.path.join(d, "a.txt"), os.path.join(d, "b.txt")
            open(clean, "w").write("nothing here")
            open(dirty, "w").write("token 0123456789abcdef0123456789abcdef")
            self.assertTrue(c.leakscan([clean]).ok)
            item = c.leakscan([clean, dirty])
            self.assertFalse(item.ok)
            self.assertIn("b.txt", item.detail)
            self.assertNotIn("0123456789abcdef0123456789abcdef", item.detail)

    def test_the_summary_is_assembled_from_every_section_and_fails_on_one_failure(self):
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            open(os.path.join(d, "script-items.tsv"), "w").write("PASS\tbuild\tbuilt\tfine\nFAIL\tshot\tscreenshot\tmissing\n")
            os.makedirs(os.path.join(d, "controls", "wrong-ca"))
            doc = {"title": "Control: wrong-ca", "items": [{"id": "x", "description": "nobody connected", "pass": True, "detail": ""}]}
            json.dump(doc, open(os.path.join(d, "controls", "wrong-ca", "checker-control-wrong-ca.json"), "w"))
            self.assertEqual(1, c.bundle(d, None))
            md = open(os.path.join(d, "SUMMARY.md")).read()
            self.assertIn("Overall: FAIL", md)
            self.assertIn("nobody connected", md)
            self.assertIn("screenshot", md)


class BuildStamp(unittest.TestCase):
    FULL = "3818d6fe" + "0123456789abcdef0123456789ab"[:32]

    def test_a_commit_is_written_as_twelve_characters_never_a_credential_shape(self):
        self.assertEqual(12, len(c.short_sha(self.FULL)))
        self.assertEqual([], c.leaks(c.short_sha(self.FULL)))
        self.assertEqual("unknown", c.short_sha(""))

    def test_a_summary_with_a_short_sha_passes_the_leak_scan_and_a_32_hex_string_fails_it(self):
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            ok, bad = os.path.join(d, "SUMMARY.md"), os.path.join(d, "SUMMARY-bad.md")
            open(ok, "w").write("- **Commit**: %s\nbuilt from %s\n" % (c.short_sha(self.FULL), c.short_sha(self.FULL)))
            open(bad, "w").write("- **Commit**: 0123456789abcdef0123456789abcdef\n")
            self.assertTrue(c.leakscan([ok]).ok)
            self.assertFalse(c.leakscan([bad]).ok)
            self.assertFalse(c.leakscan([ok, bad]).ok)

    def test_the_stamp_keeps_unitys_fields_and_adds_the_git_ones(self):
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "build-info.json")
            json.dump({"git": "not resolvable", "unityVersion": "6000.5.3f1", "scriptingBackend": "IL2CPP",
                       "strippingLevel": "Minimal", "buildFinishedAt": "2026-10-06T10:06:32Z"}, open(path, "w"))
            c.stamp_build(path, self.FULL, True, self.FULL[::-1])
            info = json.load(open(path))
            self.assertEqual("6000.5.3f1", info["unityVersion"])
            self.assertEqual("IL2CPP", info["scriptingBackend"])
            self.assertEqual("Minimal", info["strippingLevel"])
            self.assertEqual("2026-10-06T10:06:32Z", info["buildFinishedAt"])
            self.assertEqual(c.short_sha(self.FULL), info["gitSha"])
            self.assertIs(True, info["trackedTreeClean"])
            self.assertEqual(12, len(info["sdkCommit"]))
            self.assertNotIn("git", info)
            self.assertEqual([], c.leaks(open(path).read()))

    def test_build_current_compares_the_stamped_commit_and_tree_state(self):
        head = self.FULL
        self.assertEqual("PASS", c.judge_build({"gitSha": c.short_sha(head), "trackedTreeClean": True}, head)[0])
        self.assertEqual("FAIL", c.judge_build({"gitSha": c.short_sha(head), "trackedTreeClean": False}, head)[0])
        self.assertEqual("FAIL", c.judge_build({"gitSha": "aaaaaaaaaaaa", "trackedTreeClean": True}, head)[0])
        self.assertEqual("FAIL", c.judge_build({"unityVersion": "x"}, head)[0], "an unstamped build is not current")
        verdict, detail = c.judge_build({"gitSha": "aaaaaaaaaaaa", "trackedTreeClean": True}, head, allow_stale=True)
        self.assertEqual("info", verdict)
        self.assertEqual([], c.leaks(detail))


if __name__ == "__main__":
    unittest.main()
