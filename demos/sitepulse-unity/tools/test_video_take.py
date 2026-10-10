# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for the pure parts of video_take.py. Run: python3 -m unittest test_video_take (from this directory)."""

import os
import contextlib
import io
import tempfile
import unittest
from unittest import mock

import phase_a_check as c
import video_take as v

STEP = "[sitepulse] video-run: 520 s · operator · send goto-area sp-zone-yard to SP-HL-0003 (the console's device page, Commands panel); the run waits for it to succeed"


class FakePlatform:
    def __init__(self, statuses=("QUEUED", "SENT", "SUCCESSFUL"), rejection=None, devices=None):
        self.statuses = list(statuses)
        self.rejection = rejection
        self.created = []
        self.tokens = []
        self.devices = {"SP-HL-0003": "tok-3"} if devices is None else devices

    def device_tokens(self, ids):
        return {i: self.devices[i] for i in ids if i in self.devices}

    def create_command(self, device_token, name, payload=None, token=None):
        self.created.append((device_token, name, payload))
        self.tokens.append(token)
        return {"token": token or "op-1", "rejection": self.rejection}

    def commands_by_token(self, tokens):
        s = self.statuses.pop(0) if len(self.statuses) > 1 else self.statuses[0]
        return {t: {"token": t, "status": s} for t in tokens}


class Clock:
    def __init__(self):
        self.t = 0.0

    def __call__(self):
        return self.t

    def sleep(self, s):
        self.t += s


class OperatorStep(unittest.TestCase):
    def test_the_players_own_line_is_the_signal_and_no_other_line_is(self):
        self.assertTrue(v.is_operator_step(STEP))
        self.assertFalse(v.is_operator_step("[sitepulse] video-run: 150 s · low fuel · SP-HL-0006: prepared"))
        self.assertFalse(v.is_operator_step("[sitepulse] video-run: 1000 s · operator command NOT seen"))
        self.assertFalse(v.is_operator_step("send goto-area sp-zone-yard to SP-HL-0003"))

    def test_the_payload_is_the_consoles_typed_form(self):
        self.assertEqual({"areaToken": "sp-zone-yard"}, v.operator_payload())

    def test_a_log_that_grows_is_read_from_where_it_was(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "player.log")
            self.assertEqual(("", 0), v.new_text(p, 0), "no file yet is not an error")
            with open(p, "wb") as f:
                f.write(b"hello\n")
            text, off = v.new_text(p, 0)
            self.assertEqual("hello\n", text)
            with open(p, "ab") as f:
                f.write(b"world\n")
            text2, off2 = v.new_text(p, off)
            self.assertEqual("world\n", text2)
            self.assertEqual(off + 6, off2)

    def test_waiting_finds_the_line_once_it_appears(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "player.log")
            clock = Clock()
            writes = [b"noise\n", ("[sitepulse] video-run: 150 s · low fuel\n").encode(), (STEP + "\n").encode()]

            def sleep(s):
                clock.sleep(s)
                if writes:
                    with open(p, "ab") as f:
                        f.write(writes.pop(0))

            self.assertTrue(v.wait_for_operator_step(p, 100, poll=2.0, sleep=sleep, clock=clock))
            self.assertLess(clock.t, 10)

    def test_a_line_cut_in_half_by_the_read_is_not_missed(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "player.log")
            clock = Clock()
            data = (STEP + "\n").encode()
            halves = [data[:20], data[20:]]

            def sleep(s):
                clock.sleep(s)
                if halves:
                    with open(p, "ab") as f:
                        f.write(halves.pop(0))

            with open(p, "wb"):
                pass
            self.assertTrue(v.wait_for_operator_step(p, 100, poll=1.0, sleep=sleep, clock=clock))

    def test_waiting_gives_up_when_the_time_runs_out(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "player.log")
            clock = Clock()
            self.assertFalse(v.wait_for_operator_step(p, 10, poll=2.0, sleep=clock.sleep, clock=clock))
            self.assertGreaterEqual(clock.t, 10)


class Sending(unittest.TestCase):
    def test_it_creates_exactly_the_consoles_command_for_exactly_one_device(self):
        p = FakePlatform()
        token, rejection = v.send_operator_command(p)
        self.assertRegex(token, r"^video-goto-area-[0-9a-f]{8}$")
        self.assertEqual([token], p.tokens)
        self.assertFalse(token.startswith("accept-"), "a take's command is not named like an acceptance run's")
        self.assertIsNone(rejection)
        self.assertEqual([("tok-3", "goto-area", {"areaToken": "sp-zone-yard"})], p.created)

    def test_a_device_the_platform_does_not_have_is_an_error_not_a_guess(self):
        with self.assertRaises(c.PlatformError):
            v.send_operator_command(FakePlatform(devices={}))

    def test_the_platforms_rejection_is_passed_back_not_swallowed(self):
        p = FakePlatform(rejection={"code": "PAYLOAD_SCHEMA_VIOLATION", "reason": "areaToken"})
        _, rejection = v.send_operator_command(p)
        self.assertEqual("PAYLOAD_SCHEMA_VIOLATION", rejection["code"])
        verdict, detail = v.judge(None, rejection)
        self.assertEqual("FAIL", verdict)
        self.assertIn("rejected", detail)

    def test_waiting_for_the_platform_returns_its_terminal_state(self):
        clock = Clock()
        status = v.wait_finished(FakePlatform(), "op-1", 60, poll=1.0, sleep=clock.sleep, clock=clock)
        self.assertEqual("SUCCESSFUL", status)
        self.assertLess(clock.t, 10)

    def test_a_command_the_platform_never_finishes_is_reported_at_its_last_state(self):
        clock = Clock()
        status = v.wait_finished(FakePlatform(statuses=("SENT",)), "op-1", 5, poll=1.0, sleep=clock.sleep, clock=clock)
        self.assertEqual("SENT", status)
        self.assertEqual("FAIL", v.judge(status, None)[0])

    def test_only_successful_passes(self):
        self.assertEqual("PASS", v.judge("SUCCESSFUL", None)[0])
        for s in ("FAILED", "TIMEOUT", "EXPIRED", "CANCELLED", "SENT", None):
            self.assertEqual("FAIL", v.judge(s, None)[0], s)


class Main(unittest.TestCase):
    """What the command line does: its exit code is what the unattended take reads, and it sends nothing unless the player said it was time."""

    def run_main(self, argv, platform=None, waited=True, recorder=None, console_rc=0):
        """<recorder> is the recorder's interpreter (None: not installed, so the tests do not depend on this machine's setup)."""
        out = io.StringIO()
        made = []

        def make_platform(url):
            made.append(url)
            return platform

        with mock.patch.object(v, "wait_for_operator_step", return_value=waited) as wait, mock.patch.object(c, "Platform", make_platform), \
                mock.patch.object(v, "recorder_python", return_value=recorder), \
                mock.patch.object(v.subprocess, "call", return_value=console_rc) as call, \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            rc = v.main(argv)
        self.call = call
        return rc, out.getvalue(), made, wait

    def test_when_the_wait_for_the_players_signal_times_out_nothing_is_sent_and_it_exits_1(self):
        p = FakePlatform()
        rc, out, made, wait = self.run_main(["operator", "--log", "player.log", "--wait-timeout", "5"], p, waited=False)
        self.assertEqual(1, rc)
        self.assertEqual([], p.created, "no command without the player's say-so")
        self.assertEqual([], made, "and the platform was not even contacted")
        wait.assert_called_once_with("player.log", 5.0)
        self.assertIn("nothing was sent", out)

    def test_a_successful_command_exits_0_after_sending_exactly_one(self):
        p = FakePlatform()
        rc, out, _, _ = self.run_main(["operator", "--now"], p)
        self.assertEqual(0, rc)
        self.assertEqual(1, len(p.created))
        self.assertIn("PASS", out)

    def test_a_command_the_platform_finishes_as_failed_exits_non_zero(self):
        rc, out, _, _ = self.run_main(["operator", "--now"], FakePlatform(statuses=("FAILED",)))
        self.assertNotEqual(0, rc)
        self.assertIn("FAIL", out)

    def test_a_command_the_platform_rejects_exits_non_zero_and_says_so(self):
        p = FakePlatform(rejection={"code": "PAYLOAD_SCHEMA_VIOLATION", "reason": "areaToken"})
        rc, out, _, _ = self.run_main(["operator", "--now"], p)
        self.assertNotEqual(0, rc)
        self.assertIn("rejected", out)

    def test_a_platform_that_does_not_answer_exits_non_zero(self):
        class Down(FakePlatform):
            def device_tokens(self, ids):
                raise c.PlatformError("connection refused")

        rc, out, _, _ = self.run_main(["operator", "--now"], Down())
        self.assertNotEqual(0, rc)
        self.assertIn("did not answer", out)


class OperatorMode(unittest.TestCase):
    """--operator: who sends the command. The console records and sends; the API is the fallback, never a second sender."""

    def test_auto_is_console_when_the_recorder_is_installed_and_api_when_it_is_not(self):
        self.assertEqual("console", v.choose_operator("auto", True))
        self.assertEqual("api", v.choose_operator("auto", False))

    def test_an_explicit_choice_is_kept_and_console_without_the_recorder_is_refused(self):
        self.assertEqual("api", v.choose_operator("api", True))
        self.assertEqual("console", v.choose_operator("console", True))
        with self.assertRaises(ValueError):
            v.choose_operator("console", False)
        with self.assertRaises(ValueError):
            v.choose_operator("telepathy", True)

    def test_the_consoles_exit_status_decides_whether_the_api_may_send(self):
        self.assertEqual("done", v.console_outcome(0))
        self.assertEqual("fallback", v.console_outcome(3))
        self.assertEqual("fallback", v.console_outcome(1), "a crash of the recorder itself sent nothing")
        self.assertEqual("sent-clip-lost", v.console_outcome(4), "after the console sent it, the API must never send a second one")


class MainConsole(unittest.TestCase):
    """The command line with the console path."""

    run_main = Main.run_main

    def test_console_mode_hands_the_log_and_out_to_the_recorder_and_sends_nothing_itself(self):
        p = FakePlatform()
        rc, out, made, _ = self.run_main(["operator", "--log", "player.log", "--out", "o", "--operator", "console"], p, recorder="/py")
        self.assertEqual(0, rc)
        argv = self.call.call_args[0][0]
        self.assertEqual("/py", argv[0])
        self.assertIn("take", argv)
        self.assertIn("player.log", argv)
        self.assertIn("o", argv)
        self.assertEqual([], p.created)
        self.assertEqual([], made)
        self.assertIn("PASS", out)

    def test_a_console_that_failed_before_sending_falls_back_to_the_api_exactly_once(self):
        p = FakePlatform()
        rc, out, _, wait = self.run_main(["operator", "--log", "player.log", "--out", "o"], p, recorder="/py", console_rc=3)
        self.assertEqual(0, rc)
        self.assertEqual(1, len(p.created))
        wait.assert_called_once()
        self.assertIn("falling back", out)

    def test_a_console_that_failed_after_sending_never_sends_again(self):
        p = FakePlatform()
        rc, out, _, _ = self.run_main(["operator", "--log", "player.log", "--out", "o"], p, recorder="/py", console_rc=4)
        self.assertEqual(1, rc)
        self.assertEqual([], p.created)
        self.assertIn("clip was lost", out)

    def test_without_the_recorder_auto_is_the_api_path_and_the_recorder_is_never_run(self):
        p = FakePlatform()
        rc, _, _, _ = self.run_main(["operator", "--log", "player.log", "--out", "o"], p, recorder=None)
        self.assertEqual(0, rc)
        self.call.assert_not_called()
        self.assertEqual(1, len(p.created))

    def test_console_mode_needs_the_take_out_directory(self):
        with self.assertRaises(SystemExit):
            self.run_main(["operator", "--log", "player.log", "--operator", "console"], FakePlatform(), recorder="/py")


if __name__ == "__main__":
    unittest.main()
