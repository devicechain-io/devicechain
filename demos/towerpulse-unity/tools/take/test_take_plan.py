# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Tests for take_plan.py (the cut follows what the recording says) and the pure part of frame_check.py."""

import json
import os
import tempfile
import unittest

import frame_check
import take_plan

POSE = {"pos": [0.0, 10.0, -30.0], "look": [0.0, 5.0, 0.0], "fov": 40.0}
POSE2 = {"pos": [10.0, 20.0, -10.0], "look": [4.0, 5.0, 0.0], "fov": 30.0}


def storyboard():
    return {sid: {"id": sid, "from": POSE, "to": POSE2} for sid in ("dusk-establishing", "f4-east-push-in", "chiller-close-up", "pull-back")}


A = {"alarm": 51140, "sent": 51140, "done": 51705}
B = {"alarm": 59530, "sent": 59535, "done": 60030}


class BuildTests(unittest.TestCase):
    def setUp(self):
        self.shots = take_plan.build(A, B, storyboard())
        self.by = {s["id"]: s for s in self.shots}

    def test_it_is_a_ninety_second_film_with_the_console_gap(self):
        end = self.shots[-1]["startS"] + self.shots[-1]["durationS"]
        self.assertAlmostEqual(end, 90.0)
        gap = self.by["pull-back"]["startS"] - (self.by["chiller-restart"]["startS"] + self.by["chiller-restart"]["durationS"])
        self.assertAlmostEqual(gap, take_plan.OWNER_SLOT_S)

    def test_shots_follow_one_another_with_no_overlap(self):
        for prev, nxt in zip(self.shots, self.shots[1:]):
            self.assertGreaterEqual(nxt["startS"] + 1e-9, prev["startS"] + prev["durationS"])

    def test_the_beats_are_anchored_on_the_recorded_events(self):
        h = lambda s: s / 3600.0
        # the alarm shot starts shortly before the alarm and ends after the command was sent
        self.assertLess(self.by["hot-room-alarm"]["modelFromHour"], h(A["alarm"]))
        self.assertGreater(self.by["hot-room-alarm"]["modelToHour"], h(A["sent"]))
        # the last hot-room shot ends after the command was DONE, not merely sent
        self.assertGreater(self.by["hot-room-cools"]["modelToHour"], h(A["done"]))
        self.assertGreater(self.by["chiller-restart"]["modelToHour"], h(B["done"]))
        self.assertLess(self.by["chiller-silent"]["modelFromHour"], h(B["alarm"]))

    def test_the_model_clock_never_goes_backwards_within_a_run(self):
        for run in ("A", "B"):
            mine = [s for s in self.shots if s["run"] == run]
            for prev, nxt in zip(mine, mine[1:]):
                self.assertGreaterEqual(nxt["modelFromHour"] + 1e-9, prev["modelToHour"])

    def test_a_split_move_is_continuous(self):
        parts = [s for s in self.shots if s["id"].startswith("hot-room")]
        for prev, nxt in zip(parts, parts[1:]):
            self.assertEqual(prev["to"], nxt["from"])
        self.assertEqual(parts[0]["from"], take_plan.lerp_pose(POSE, POSE2, 0.0))
        self.assertEqual(parts[-1]["to"], take_plan.lerp_pose(POSE, POSE2, 1.0))

    def test_a_different_recording_moves_the_beats(self):
        later = take_plan.build(dict(A, alarm=A["alarm"] + 600, sent=A["sent"] + 600, done=A["done"] + 600), B, storyboard())
        a0 = {s["id"]: s for s in later}
        self.assertGreater(a0["hot-room-cools"]["modelToHour"], self.by["hot-room-cools"]["modelToHour"])

    def test_events_out_of_order_are_refused(self):
        with self.assertRaises(take_plan.PlanError):
            take_plan.build({"alarm": 10, "sent": 5, "done": 20}, B, storyboard())
        with self.assertRaises(take_plan.PlanError):
            take_plan.build(A, {"alarm": 10, "sent": 20, "done": 15}, storyboard())

    def test_a_storyboard_without_a_beat_is_refused(self):
        sb = storyboard()
        del sb["pull-back"]
        with self.assertRaises(take_plan.PlanError):
            take_plan.build(A, B, sb)


class FactTests(unittest.TestCase):
    def write(self, lines):
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "story.ndjson"), "w", encoding="utf-8") as f:
            for line in lines:
                f.write(json.dumps(line) + "\n")
        return d

    def test_anchors_are_read_from_the_facts(self):
        d = self.write([
            {"sim": 100, "k": "alarm", "dev": "X", "key": "hot", "active": True},
            {"sim": 101, "k": "command", "dev": "X", "key": "go", "status": "SENT"},
            {"sim": 150, "k": "alarm", "dev": "X", "key": "hot", "active": False},
            {"sim": 400, "k": "command", "dev": "X", "key": "go", "status": "SUCCESSFUL"},
        ])
        self.assertEqual(take_plan.anchors(take_plan.facts(d), "X", "hot", "go"), {"alarm": 100, "sent": 101, "done": 400})

    def test_a_recording_that_never_finished_the_command_is_refused(self):
        d = self.write([
            {"sim": 100, "k": "alarm", "dev": "X", "key": "hot", "active": True},
            {"sim": 101, "k": "command", "dev": "X", "key": "go", "status": "SENT"},
        ])
        with self.assertRaises(take_plan.PlanError):
            take_plan.anchors(take_plan.facts(d), "X", "hot", "go")

    def test_another_device_does_not_count(self):
        d = self.write([{"sim": 100, "k": "alarm", "dev": "Y", "key": "hot", "active": True}])
        with self.assertRaises(take_plan.PlanError):
            take_plan.anchors(take_plan.facts(d), "X", "hot", "go")


class ChipTests(unittest.TestCase):
    def test_orange_is_the_chip_and_the_strip_is_not(self):
        self.assertTrue(frame_check.is_orange((245, 120, 70)))
        self.assertFalse(frame_check.is_orange((40, 48, 62)))   # the status strip
        self.assertFalse(frame_check.is_orange((120, 150, 190)))  # sky
        self.assertFalse(frame_check.is_orange((255, 255, 255)))


if __name__ == "__main__":
    unittest.main()
