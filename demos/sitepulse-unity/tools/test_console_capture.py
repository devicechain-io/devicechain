# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for the pure parts of console_capture.py (no browser needed). Run: python3 -m pytest -q test_console_capture.py (or unittest)."""

import json
import os
import tempfile
import unittest
from unittest import mock

import console_capture as cc

SENTINEL_PASSWORD = "Sentinel-Pa55w0rd-DO-NOT-LEAK"
SENTINEL_EMAIL = "sentinel.user@leak.example"

TYRE = "[sitepulse] video-run: 330 s · tyre alarm · the platform says tyre-pressure-low is ACTIVE; the truck keeps driving"
OPERATOR = "[sitepulse] video-run: 520 s · operator · send goto-area sp-zone-yard to SP-HL-0003 (the console's device page, Commands panel); the run waits for it to succeed"


class Triggers(unittest.TestCase):
    def test_the_tyre_alarm_line_starts_s17_and_no_other_line_does(self):
        self.assertTrue(cc.is_tyre_alarm_step(TYRE))
        self.assertFalse(cc.is_tyre_alarm_step("[sitepulse] video-run: 330 s · tyre alarm NOT seen · no tyre-pressure-low ACTIVE within 150 s"))
        self.assertFalse(cc.is_tyre_alarm_step("[sitepulse] video-run: 217 s · puncture · SP-HL-0003: the leak started"))
        self.assertFalse(cc.is_tyre_alarm_step(OPERATOR))

    def test_the_operator_line_is_the_one_the_sender_already_uses(self):
        self.assertTrue(cc.v.is_operator_step(OPERATOR))
        self.assertFalse(cc.v.is_operator_step(TYRE))

    def test_a_watcher_sees_each_line_once_even_when_it_arrives_in_pieces(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "player.log")
            w = cc.LogWatcher(path)
            self.assertEqual(set(), w.poll({"tyre": cc.is_tyre_alarm_step}), "a log that does not exist yet is nothing yet")
            with open(path, "w", encoding="utf-8") as f:
                f.write("noise\n" + TYRE[:40])
            self.assertEqual(set(), w.poll({"tyre": cc.is_tyre_alarm_step}), "half a line is not a line")
            with open(path, "a", encoding="utf-8") as f:
                f.write(TYRE[40:] + "\n")
            self.assertEqual({"tyre"}, w.poll({"tyre": cc.is_tyre_alarm_step}))

    def test_a_line_that_arrives_during_one_wait_is_still_there_for_the_next(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "player.log")
            with open(path, "w", encoding="utf-8") as f:
                f.write(TYRE + "\n" + OPERATOR + "\n")
            w = cc.LogWatcher(path, {"tyre": cc.is_tyre_alarm_step, "operator": cc.v.is_operator_step})
            self.assertTrue(w.wait("tyre", timeout=0))
            self.assertTrue(w.wait("operator", timeout=0), "the operator's line was read while waiting for the tyre's")

    def test_waiting_gives_up_after_the_timeout(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "player.log")
            open(path, "w").close()
            t = [0.0]
            ok = cc.LogWatcher(path).wait("tyre", cc.is_tyre_alarm_step, 5, poll=1, sleep=lambda s: t.__setitem__(0, t[0] + s), clock=lambda: t[0])
            self.assertFalse(ok)
            self.assertGreaterEqual(t[0], 5)


class Timing(unittest.TestCase):
    def test_each_frame_is_held_until_the_next_and_the_last_until_the_end(self):
        tl = cc.clip_timeline([(10.0, "a"), (10.5, "b"), (12.0, "c")], 10.0, 14.0)
        self.assertEqual([("a", 0.5), ("b", 1.5), ("c", 2.0)], tl)

    def test_frames_out_of_order_or_before_the_start_never_give_a_negative_duration(self):
        tl = cc.clip_timeline([(12.0, "c"), (9.0, "z"), (10.0, "a")], 10.0, 13.0)
        self.assertTrue(all(d > 0 for _, d in tl))
        self.assertAlmostEqual(3.0, sum(d for _, d in tl), places=2)

    def test_the_concat_script_names_the_last_file_twice_and_escapes_quotes(self):
        text = cc.concat_script([("/t/a.jpg", 0.5), ("/t/it's.jpg", 1.0)])
        lines = text.strip().split("\n")
        self.assertEqual(lines[-1], lines[-3], "the demuxer drops the last duration unless the last file repeats")
        self.assertIn("it'\\''s", text)

    def test_status_changes_records_only_new_statuses(self):
        t = cc.status_changes([], "QUEUED", 1.0)
        t = cc.status_changes(t, "QUEUED", 2.0)
        t = cc.status_changes(t, None, 2.5)
        t = cc.status_changes(t, "SENT", 3.0)
        self.assertEqual([(1.0, "QUEUED"), (3.0, "SENT")], t)


class Manifest(unittest.TestCase):
    def test_a_shot_entry_says_when_it_started_and_what_was_clicked(self):
        e = cc.shot_entry("s19", 1760000000.0, 1760000006.5, [(1760000002.25, "opened the Command picker")])
        self.assertEqual("s19-command-form.mp4", e["file"])
        self.assertEqual("2025-10-09T08:53:20.000Z", e["start_utc"])
        self.assertEqual(6.5, e["duration_s"])
        self.assertEqual("2025-10-09T08:53:22.250Z", e["actions"][0]["at_utc"])
        self.assertNotIn("command_status", e)

    def test_the_sent_shot_carries_the_commands_status_trail(self):
        e = cc.shot_entry("s20", 100.0, 120.0, [], [(101.0, "QUEUED"), (104.0, "SENT")])
        self.assertEqual(["QUEUED", "SENT"], [s["status"] for s in e["command_status"]])

    def test_every_shot_has_a_file_name(self):
        self.assertEqual({"s17", "s18", "s19", "s20"}, set(cc.SHOT_FILES))
        self.assertEqual(
            ["s17-board", "s18-alarm-drill", "s19-command-form", "s20-command-sent"], [cc.SHOT_FILES[s] for s in cc.SHOTS]
        )


class Credentials(unittest.TestCase):
    RECORD = {"name": "sitepulse", "tenant": {"simEmail": SENTINEL_EMAIL, "simPassword": SENTINEL_PASSWORD}, "other": [1, 2]}

    def test_the_tenant_user_is_found_wherever_it_nests(self):
        self.assertEqual((SENTINEL_EMAIL, SENTINEL_PASSWORD), cc.find_credentials(self.RECORD))
        self.assertEqual((SENTINEL_EMAIL, SENTINEL_PASSWORD), cc.find_credentials({"simEmail": SENTINEL_EMAIL, "simPassword": SENTINEL_PASSWORD}))

    def test_a_record_without_the_user_is_an_error_that_quotes_nothing(self):
        with self.assertRaises(cc.CaptureError) as cm:
            cc.find_credentials({"simEmail": SENTINEL_EMAIL})
        self.assertNotIn(SENTINEL_EMAIL, str(cm.exception))

    def test_an_unreadable_or_broken_record_names_no_content(self):
        with tempfile.TemporaryDirectory() as d:
            bad = os.path.join(d, "sitepulse.json")
            with open(bad, "w") as f:
                f.write('{"simPassword": "' + SENTINEL_PASSWORD + '" nope')
            with self.assertRaises(cc.CaptureError) as cm:
                cc.load_credentials(bad)
            self.assertNotIn(SENTINEL_PASSWORD, str(cm.exception))
            with self.assertRaises(cc.CaptureError):
                cc.load_credentials(os.path.join(d, "missing.json"))

    def test_the_record_path_is_under_the_isolated_home(self):
        self.assertEqual("/h/.devicechain/sims/sitepulse.json", cc.sim_record_path("/h"))


class NoCredentialEverLeaves(unittest.TestCase):
    """The recorder is fed a sentinel email and password; nothing it prints, raises or writes may contain either."""

    def leaks(self, text):
        return SENTINEL_PASSWORD in text or SENTINEL_EMAIL in text

    def test_scrub_removes_both_values_wherever_they_are(self):
        msg = f"fill failed for {SENTINEL_EMAIL} / {SENTINEL_PASSWORD} (again {SENTINEL_PASSWORD})"
        out = cc.scrub(msg, (SENTINEL_EMAIL, SENTINEL_PASSWORD))
        self.assertFalse(self.leaks(out))
        self.assertIn("***", out)
        self.assertEqual("plain", cc.scrub("plain", ("", None)))

    def make_console(self, out_dir, page):
        con = cc.Console(out_dir=out_dir, creds=(SENTINEL_EMAIL, SENTINEL_PASSWORD))
        con.page = page
        return con

    def test_a_failed_sign_in_raises_a_message_without_the_values(self):
        page = mock.Mock()
        page.fill.side_effect = RuntimeError(f"Timeout typing {SENTINEL_PASSWORD} into #password of {SENTINEL_EMAIL}")
        with tempfile.TemporaryDirectory() as d:
            con = self.make_console(d, page)
            with self.assertRaises(cc.CaptureError) as cm:
                con.sign_in()
        self.assertFalse(self.leaks(str(cm.exception)))

    def test_the_take_reports_a_failure_without_the_values_and_exits_before_send(self):
        said = []

        class Boom(cc.Console):
            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

            def sign_in(self):
                raise RuntimeError(f"cannot sign in as {SENTINEL_EMAIL} with {SENTINEL_PASSWORD}")

        with tempfile.TemporaryDirectory() as d:
            args = mock.Mock(out=d, log=os.path.join(d, "p.log"), shots="s17,s19", wait_timeout=1, no_send=True)
            with mock.patch.object(cc, "Console", lambda out_dir: Boom(out_dir=out_dir, creds=(SENTINEL_EMAIL, SENTINEL_PASSWORD))):
                rc = cc.run_take(args, say=said.append)
            written = []
            for root, _, files in os.walk(d):
                for name in files:
                    with open(os.path.join(root, name), encoding="utf-8", errors="replace") as f:
                        written.append(f.read())
        self.assertEqual(cc.EXIT_FAILED_BEFORE_SEND, rc)
        self.assertTrue(said)
        self.assertFalse(any(self.leaks(m) for m in said), said)
        self.assertFalse(any(self.leaks(w) for w in written))

    def test_the_manifest_json_never_holds_the_values(self):
        entries = [cc.shot_entry("s19", 1.0, 2.0, [(1.5, "chose areaToken sp-zone-yard")])]
        text = json.dumps(cc.manifest(entries, "/x/console"))
        self.assertFalse(self.leaks(text))

    def test_the_modules_own_source_never_prints_or_logs_a_credential_name(self):
        src = open(cc.__file__, encoding="utf-8").read()
        for line in src.splitlines():
            code = line.split("#")[0]
            if "print(" in code or "say(" in code:
                self.assertNotIn("self.password", code)
                self.assertNotIn("self.email", code)


if __name__ == "__main__":
    unittest.main()
