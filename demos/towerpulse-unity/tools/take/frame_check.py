#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
frame_check.py -- the machine half of a take's frame check, over the frames render-take.sh sampled (Build/take/samples/<shot>/frame_*.jpg).

    frame_check.py --plan take-plan.json --samples Build/take/samples

It checks what a pixel can say: every shot has sampled frames, every frame is the size the cut is made at and is neither black nor blown out, and the
orange chip that labels a jump in the clock is on every frame of a shot whose clock jumps and on no frame of a shot that runs in real time. What a pixel
cannot say (a card clipped by the frame, a wrong label, a preview tag in a take) is for the eyes, on the contact sheet and on the frames themselves.

Needs ffmpeg and ffprobe on PATH. Exits 0 when every check holds, 1 when one does not, 2 when it could not run.
"""

import argparse
import glob
import json
import os
import subprocess
import sys

WIDTH, HEIGHT = 1920, 1080
JUMP_RATE = 1.05  # the same threshold StoryText.Jump uses
# the chip sits in the status strip at (418, 27) in the 1600x900 layout, 180x26 (502, 32, 216x31 at 1920x1080); this is a crop of its plain fill, left of the text
CHIP = (506, 36, 24, 22)


def average_rgb(path, crop=None):
    vf = "scale=1:1" if crop is None else "crop=%d:%d:%d:%d,scale=1:1" % (crop[2], crop[3], crop[0], crop[1])
    p = subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-i", path, "-vf", vf, "-f", "rawvideo", "-pix_fmt", "rgb24", "-"], capture_output=True)
    if p.returncode != 0 or len(p.stdout) != 3:
        raise RuntimeError("ffmpeg could not read " + path)
    return tuple(p.stdout)


def size(path):
    p = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0", path], capture_output=True, text=True)
    if p.returncode != 0:
        raise RuntimeError("ffprobe could not read " + path)
    w, h = p.stdout.strip().split(",")
    return int(w), int(h)


def is_orange(rgb):
    r, g, b = rgb
    return r > 170 and 60 < g < 150 and b < 90 and r - b > 110


def check(plan_path, samples):
    problems = []
    with open(plan_path, encoding="utf-8") as f:
        plan = json.load(f)
    seen = 0
    for shot in plan["shots"]:
        frames = sorted(glob.glob(os.path.join(samples, shot["id"], "frame_*.jpg")))
        if not frames:
            problems.append("%s: no sampled frames" % shot["id"])
            continue
        rate = (shot["modelToHour"] - shot["modelFromHour"]) * 3600.0 / shot["durationS"]
        jumps = rate > JUMP_RATE
        for fr in frames:
            seen += 1
            name = "%s/%s" % (shot["id"], os.path.basename(fr))
            if size(fr) != (WIDTH, HEIGHT):
                problems.append("%s: %dx%d, not %dx%d" % (name, *size(fr), WIDTH, HEIGHT))
                continue
            mean = sum(average_rgb(fr)) / 3.0
            if mean < 15 or mean > 240:
                problems.append("%s: mean brightness %.0f (black or blown out)" % (name, mean))
            chip = is_orange(average_rgb(fr, CHIP))
            if jumps and not chip:
                problems.append("%s: the clock jumps x%.0f here but there is no jump chip" % (name, rate))
            if not jumps and chip:
                problems.append("%s: a real-time shot shows a jump chip" % name)
    return seen, problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--plan", required=True)
    ap.add_argument("--samples", required=True)
    a = ap.parse_args(argv)
    try:
        seen, problems = check(a.plan, a.samples)
    except (OSError, RuntimeError, ValueError, KeyError) as e:
        print("frame_check: " + str(e), file=sys.stderr)
        return 2
    for p in problems:
        print("FAIL " + p)
    print("frame_check: %d frames, %d problem(s)" % (seen, len(problems)))
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
