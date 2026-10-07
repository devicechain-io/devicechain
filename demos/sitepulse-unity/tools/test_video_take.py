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
        self.devices = {"SP-HL-0003": "tok-3"} if devices is None else devices

    def device_tokens(self, ids):
        return {i: self.devices[i] for i in ids if i in self.devices}

    def create_command(self, device_token, name, payload=None, token=None):
        self.created.append((device_token, name, payload))
        return {"token": "op-1", "rejection": self.rejection}

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
        self.assertEqual("op-1", token)
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

    def run_main(self, argv, platform=None, waited=True):
        out = io.StringIO()
        made = []

        def make_platform(url):
            made.append(url)
            return platform

        with mock.patch.object(v, "wait_for_operator_step", return_value=waited) as wait, mock.patch.object(c, "Platform", make_platform), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            rc = v.main(argv)
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


if __name__ == "__main__":
    unittest.main()
