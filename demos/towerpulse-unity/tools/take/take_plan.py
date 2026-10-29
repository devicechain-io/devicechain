#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
take_plan.py -- the 90-second cut's shot plan, cut from two RECORDED runs.

    take_plan.py --run-a RUNDIR_A --run-b RUNDIR_B [--shots shots.json] [--out take-plan.json]

Run A is the afternoon with the 14:00 meeting (the hot room, the platform's rule, the boost command); run B is the annex chiller going silent and its
restart. Each shot says which run it is filmed from and which span of that run's model clock it covers, and the spans are anchored on what the
recording says happened (the alarm, the command being sent, the room back at setpoint, the restart done), never on a wall-clock guess: re-cut a new
take and the beats follow it. The wait between two beats is CUT, not faked, and TakeRenderer labels every shot whose clock jumps on screen.

The camera moves are the storyboard's (Assets/Towerpulse/Data/shots.json), split across the sub-shots of a beat so the move is continuous.
The owner's view (beat 4) is filmed from the console, not from the scene; its slot is the gap in the show clock between the chiller and the pull-back.
"""

import argparse
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_SHOTS = os.path.join(HERE, "..", "..", "Assets", "Towerpulse", "Data", "shots.json")
OWNER_SLOT_S = 10.0  # show seconds the console's part takes between the chiller beat and the pull-back
RUN_A_START_S = 13.9 * 3600.0  # acceptance.sh starts segment A at 13:54 on the model clock


class PlanError(Exception):
    pass


def facts(rundir):
    """The story facts of a recording, in order."""
    out = []
    with open(os.path.join(rundir, "story.ndjson"), encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                out.append(json.loads(line))
    return out


def first(fs, kind, **want):
    for f in fs:
        if f.get("k") == kind and all(f.get(k) == v for k, v in want.items()):
            return f
    raise PlanError("the recording has no %s fact %s" % (kind, want))


def anchors(fs, dev, alarm_key, command_key):
    """Model seconds of the day of the alarm, the command being sent and the command being done."""
    return {
        "alarm": first(fs, "alarm", dev=dev, key=alarm_key, active=True)["sim"],
        "sent": first(fs, "command", dev=dev, key=command_key, status="SENT")["sim"],
        "done": first(fs, "command", dev=dev, key=command_key, status="SUCCESSFUL")["sim"],
    }


def smooth(t):
    return t * t * (3 - 2 * t)


def lerp_pose(a, b, t):
    def v(k):
        return [a[k][i] + (b[k][i] - a[k][i]) * t for i in range(3)]

    return {"pos": [round(x, 3) for x in v("pos")], "look": [round(x, 3) for x in v("look")], "fov": round(a["fov"] + (b["fov"] - a["fov"]) * t, 3)}


def storyboard(shots_path):
    with open(shots_path, encoding="utf-8") as f:
        data = json.load(f)
    return {s["id"]: s for s in data["shots"]}


def split_move(parent, durations):
    """Poses at the edges of consecutive sub-shots that together make the parent's eased move: [(from, to), ...]."""
    total = float(sum(durations))
    acc = 0.0
    poses = [lerp_pose(parent["from"], parent["to"], 0.0)]
    for d in durations:
        acc += d
        poses.append(lerp_pose(parent["from"], parent["to"], smooth(acc / total)))
    return [(poses[i], poses[i + 1]) for i in range(len(durations))]


def hours(sim_s):
    return round(sim_s / 3600.0, 6)


def build(a, b, sb):
    """The shots, in show order. a and b are the anchors of the two runs, sb the storyboard by id."""
    for need in ("dusk-establishing", "f4-east-push-in", "chiller-close-up", "pull-back"):
        if need not in sb:
            raise PlanError("the storyboard has no shot %r" % need)
    if not (a["alarm"] <= a["sent"] <= a["done"]) or not (b["alarm"] <= b["sent"] <= b["done"]):
        raise PlanError("the recorded events are out of order: %r %r" % (a, b))

    shots = []
    clock = [0.0]

    def add(run, sid, beat, caption, dur, m0, m1, cards, frm, to, ease="linear"):
        shots.append({"id": sid, "beat": beat, "caption": caption, "startS": round(clock[0], 3), "durationS": dur, "ease": ease, "cards": cards,
                      "from": frm, "to": to, "run": run, "modelFromHour": hours(m0), "modelToHour": hours(m1)})
        clock[0] += dur

    est = sb["dusk-establishing"]
    add("A", "establish", "1 Establish", "A working campus, an ordinary afternoon", 10.0, RUN_A_START_S, 14.0 * 3600 + 30, [], est["from"], est["to"], "smooth")

    f4 = sb["f4-east-push-in"]
    d_hot = [12.0, 10.0, 18.0]
    p1, p2, p3 = split_move(f4, d_hot)
    caption = "Tower F4 East: fourteen people, sun on the glass"
    cards = ["zone", "alarm", "command"]
    add("A", "hot-room-heats", "2 A hot room", caption, d_hot[0], 14.0 * 3600 + 30, a["alarm"] - 20, cards, *p1)
    add("A", "hot-room-alarm", "2 A hot room", caption, d_hot[1], a["alarm"] - 20, a["sent"] + 30, cards, *p2)
    add("A", "hot-room-cools", "2 A hot room", "SENT means delivered; done means back at setpoint", d_hot[2], a["sent"] + 30, a["done"] + 20, cards, *p3)

    ch = sb["chiller-close-up"]
    d_ch = [6.0, 10.0, 8.0]
    c1, c2, c3 = split_move(ch, d_ch)
    trip = b["alarm"] - 120  # the absence rule needs two minutes of silence: the chiller stopped about two minutes before the alarm
    cards = ["chiller", "alarm", "command"]
    add("B", "chiller-running", "3 A silent chiller", "Annex chiller: the platform notices the silence", d_ch[0], trip - 120, trip - 5, cards, *c1)
    add("B", "chiller-silent", "3 A silent chiller", "Annex chiller: the platform notices the silence", d_ch[1], trip - 5, b["sent"] + 25, cards, *c2)
    add("B", "chiller-restart", "3 A silent chiller", "The restart is done when the chiller reports again", d_ch[2], b["sent"] + 25, b["done"] + 10, cards, *c3)

    clock[0] += OWNER_SLOT_S  # the console's part
    pb = sb["pull-back"]
    add("B", "pull-back", "5 Pull back", "Thirty-one devices, one campus", 6.0, b["done"] + 10, b["done"] + 16, [], ch["to"], pb["to"], "smooth")
    return shots


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--run-a", required=True)
    ap.add_argument("--run-b", required=True)
    ap.add_argument("--shots", default=DEFAULT_SHOTS)
    ap.add_argument("--out", default=os.path.join(HERE, "take-plan.json"))
    a = ap.parse_args(argv)
    try:
        shots = build(anchors(facts(a.run_a), "TP-VAV-T-F4-E", "zone-hot", "boost-cooling"),
                      anchors(facts(a.run_b), "TP-CH-A", "chiller-silent", "restart"), storyboard(a.shots))
    except (PlanError, OSError, KeyError, ValueError) as e:
        print("take_plan: " + str(e), file=sys.stderr)
        return 1
    plan = {"version": 1, "note": "Cut from two recorded runs by tools/take/take_plan.py; regenerate it for a new take. Show seconds; the console's part fills the gap before pull-back.", "shots": shots}
    with open(a.out, "w", encoding="utf-8", newline="\n") as f:
        json.dump(plan, f, indent=2)
        f.write("\n")
    print("take_plan: %d shots, %.1f s -> %s" % (len(shots), shots[-1]["startS"] + shots[-1]["durationS"], a.out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
