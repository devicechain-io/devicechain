#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The WSL half of an unattended Sitepulse video take (video-take.sh launches the player with -sitepulse-video-run).

The take's one human part is the operator's `goto-area sp-zone-yard` to SP-HL-0003 (script shots S19-S21: the maintainer sends it from the
console's device page, Commands panel, and records that screen). For a take with nobody at the console, `operator` does what the console
does, through the same platform call: it waits for the player to say it is time (the run's own log line), creates the command through the
operator plane (so the platform checks it against the device profile like any other), and waits for the platform to finish with it.
It is the operator's hand and nothing else: it creates no alarm and writes no value, and the command is not "the demo's".

    video_take.py operator --runner http://127.0.0.1:8090 --log <player log> [--wait-timeout 2700]
    video_take.py operator --runner http://127.0.0.1:8090 --now      send at once (no log to wait on)

Everything that decides something is a pure function above the I/O (tested by test_video_take.py).
"""

import argparse
import os
import re
import sys
import time

import phase_a_check as c

DEVICE = "SP-HL-0003"
AREA = "sp-zone-yard"
COMMAND = "goto-area"

# the player says: "[sitepulse] video-run: 520 s · operator · send goto-area sp-zone-yard to SP-HL-0003 ..."
OPERATOR_STEP = re.compile(r"video-run: \d+ s \W+ operator \W+ send goto-area " + re.escape(AREA) + " to " + DEVICE)

TERMINAL = {"SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED"}


def operator_payload(area=AREA):
    """The command's payload, as the console's typed form sends it."""
    return {"areaToken": area}


def is_operator_step(line):
    return OPERATOR_STEP.search(line) is not None


def new_text(path, offset):
    """What was appended to a file after byte <offset>, decoded leniently, and the new offset. A missing file is nothing yet."""
    try:
        with open(path, "rb") as f:
            f.seek(offset)
            data = f.read()
    except OSError:
        return "", offset
    return data.decode("utf-8", errors="replace"), offset + len(data)


def wait_for_operator_step(path, timeout, poll=2.0, sleep=time.sleep, clock=time.monotonic):
    """True when the player's log says it is time for the operator's command; False when <timeout> seconds pass first."""
    deadline = clock() + timeout
    offset = 0
    carry = ""
    while True:
        text, offset = new_text(path, offset)
        carry += text
        *lines, carry = carry.split("\n")
        for line in lines:
            if is_operator_step(line):
                return True
        if clock() >= deadline:
            return False
        sleep(poll)


def judge(status, rejection):
    """(verdict, detail) for what the platform did with the command: PASS only for SUCCESSFUL."""
    if rejection:
        return "FAIL", "the platform rejected the command: " + c.redact(str(rejection))
    if status == "SUCCESSFUL":
        return "PASS", "the platform says the command is SUCCESSFUL"
    return "FAIL", "the platform's last word on the command was " + str(status)


def send_operator_command(platform, area=AREA):
    """Creates the command through the operator plane. Returns (token, rejection-or-None)."""
    tokens = platform.device_tokens([DEVICE])
    if DEVICE not in tokens:
        raise c.PlatformError(DEVICE + " does not exist on the platform")
    # a take's own token prefix: the unattended operator is the take's, not an acceptance run's
    created = platform.create_command(tokens[DEVICE], COMMAND, operator_payload(area), token=f"video-{COMMAND}-{os.urandom(4).hex()}")
    return created["token"], created.get("rejection")


def wait_finished(platform, token, timeout, poll=2.0, sleep=time.sleep, clock=time.monotonic):
    """The command's status once the platform has finished with it, or the last one seen when <timeout> seconds pass."""
    deadline = clock() + timeout
    status = None
    while True:
        rows = platform.commands_by_token([token])
        status = (rows.get(token) or {}).get("status", status)
        if status in TERMINAL or clock() >= deadline:
            return status
        sleep(poll)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("mode", choices=["operator"])
    p.add_argument("--runner", default="http://127.0.0.1:8090")
    p.add_argument("--log", help="the player's log file, which says when it is time")
    p.add_argument("--now", action="store_true", help="send at once, without waiting for the player's log")
    p.add_argument("--wait-timeout", type=float, default=2700, help="seconds to wait for the player to say it is time")
    p.add_argument("--finish-timeout", type=float, default=600, help="seconds to wait for the platform to finish with the command")
    args = p.parse_args(argv)

    if not args.now:
        if not args.log:
            p.error("operator needs --log (the player's log) or --now")
        c.say(f"waiting for the player's log ({os.path.basename(args.log)}) to say it is time for the operator's command")
        if not wait_for_operator_step(args.log, args.wait_timeout):
            c.say("the player never said it was time: nothing was sent")
            return 1
    try:
        platform = c.Platform(args.runner)
        token, rejection = send_operator_command(platform)
        if rejection:
            print("FAIL", judge(None, rejection)[1])
            return 1
        c.say(f"sent {COMMAND} {AREA} to {DEVICE} ({token}); waiting for the platform to finish with it")
        status = wait_finished(platform, token, args.finish_timeout)
    except c.PlatformError as e:
        print("FAIL the platform did not answer:", c.redact(str(e)))
        return 1
    verdict, detail = judge(status, None)
    print(verdict, detail)
    return 0 if verdict == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
