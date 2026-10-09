# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Preview choreography for the 18 Sitepulse machines on the quarry terrain.

    python3 quarry_fleet.py [generate|check|selftest] [--terrain DIR] [--out FILE] [--dt S] [--preview PNG] [--live]

    generate (default) writes the fleet; check builds both fleets in memory and runs every check without writing
    (exit status 2 on any defect); selftest shows each site check failing on a site made to break it.

Requires Python 3.8+ and numpy (Pillow only for --preview). Run quarry_heightmap.py first: this
reads its heightmap and feature file to grade speeds by slope. Deterministic.

This is LOCAL VISUAL SIMULATION ONLY. It gives the scene something believable to show and the
benchmark something realistic to draw; it is not the site simulation, which drives machines
from commands, and none of it is platform data.

WHAT IT MAKES
  * 5 haulers on one closed haul loop, spaced a fifth of the loop apart in time: load at the
    muck pile in the pit, up the ramp, out to the dump pad, reverse to the tipping edge and
    dump, back along the return road, through the yard past the refuel bay, and down the ramp.
    Traffic keeps LEFT on every road wide enough for two trucks, each direction on its own lane (see
    `lane_offset`): the loop runs in the lane of the way it goes, so a truck sent along such a road
    the other way by a command meets the loop's trucks passing, never sharing their line. A road
    narrower than TWO_LANE_WIDTH (two lanes at the least offset, a truck's width each) is single-lane: the loop drives its
    centreline. Where a lane would climb more than the grade limit (the inside of the ramp's curve, which is shorter than
    its centreline) it eases toward the centreline, never nearer than MIN_LANE (`eased_offsets`).
  * a 6th hauler out of the loop: from its parking place to the refuel bay (a lay-by beside the
    yard's through lane), a stop while it is fuelled, and back to park.
  * 6 loaders: one loads the haulers from the loading bench, PASSES buckets per truck (its
    cycle is exactly the haulers' spacing, timed so the buckets tip while a truck is standing
    at the load point);
    one rehandles the pit stockpile; at the plant one feeds the crusher's hopper from the feed
    stockpile and one works the product stockpile; two are parked (one in the workshop).
  * 6 dozers: one pushing up the muck pile at the toe of the north face, one ripping the pit
    floor, one spreading on the dump pad, and three parked in the yard.
  --live writes the LIVE-mode variant (default ../../Assets/Sitepulse/Data/quarry_fleet_live.json):
  the same 18 machines and ids, but the 6th hauler joins the loop (6 trucks, a sixth of the loop
  apart in time), the loader's cycle is retimed to that spacing, and there is no scripted
  refuel visit: in Live mode the platform's goto-refuel command sends a truck to the bay.
  Parked machines do not move: idle is a real state for a fleet, and the scene shows it.
  Speeds depend on grade and load (a loaded truck climbs the 10 % ramp at about 13 km/h), on
  path curvature and on acceleration limits; machines pause at every change of direction.

OUTPUT (default ../../Assets/Sitepulse/Data/quarry_fleet.json)
  tracks[]   one per distinct motion: period (s), dt (s) and `data`, a flat array of frames of
             CHANNELS values each. A machine plays a track from time `offset` and loops it.
  machines[] id, kind (Dozer | Loader | Hauler), track index, offset (s).
  CHANNELS   x, z (Unity metres), heading (deg clockwise from north = +Z), travel (signed
             metres along the ground, for wheel and track animation), p1, p2 (dozer: blade arm,
             ripper; loader: boom, bucket; hauler: dump raise, unused), steer (deg, + right),
             flag (hauler: 1 = carrying a load).
  The script also checks every pair of machines for footprint overlap over two full loops and
  prints the closest approach; it exits non-zero if any two machines touch, or if two haul
  trucks come within HAULER_CLEARANCE metres of each other. It also checks the site (`site_checks`):
  every driving track clear of what stands by the reach the task layer plans routes with, the haul
  loop's driven line within the grade limit, and room in each zone for a hauler, a loader and a
  dozer to stand. A haul truck's footprint is two
  boxes (its wide front deck, its narrower body and rear tyres); a loader with its boom raised
  is checked to its front tyres, since its bucket is then over the body it is tipping into.
"""
import argparse
import gzip
import json
import math
import os
import sys

import numpy as np

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from quarry_heightmap import catmull_rom, stations   # noqa: E402

CHANNELS = ["x", "z", "heading", "travel", "p1", "p2", "steer", "flag"]
# footprint half-sizes (m): half width, half length front, half length rear, from the models
FOOT = {"Dozer": (1.75, 3.7, 3.4), "Loader": (1.55, 5.2, 3.6), "Hauler": (2.85, 5.7, 4.6)}
# a haul truck is narrower at its body and rear tyres than at its front deck: two boxes
FOOT_PARTS = {"Hauler": [(2.85, 5.7, -1.2), (2.25, 1.2, 4.6)]}
WHEELBASE = {"Dozer": 2.5, "Loader": 3.3, "Hauler": 4.2}
STEER_LIMIT = {"Dozer": 0.0, "Loader": 40.0, "Hauler": 35.0}

# rig angles (degrees; see Scripts/Visuals/FleetRig.cs)
BLADE_RAISED, BLADE_CARRY, BLADE_DIG = -16.0, -6.0, 5.0
BOOM_GROUND, BOOM_CARRY, BOOM_HIGH = 0.0, -6.92, -72.0
BUCKET_FLAT, BUCKET_RACK, BUCKET_DUMP = -4.0, -38.08, 70.0


class Ground:
    """Bilinear elevation lookup on the packed heightmap."""

    def __init__(self, terrain_dir):
        with open(os.path.join(terrain_dir, "quarry_features.json")) as f:
            self.f = json.load(f)
        t = self.f["terrain"]
        n = t["height_res"]
        with open(os.path.join(terrain_dir, "quarry_height.bytes"), "rb") as f:
            r = np.frombuffer(gzip.decompress(f.read()), "<u2").reshape(n, n).astype(np.int64)
        s = np.cumsum(np.cumsum(r, 0), 1) % 65536                # undo the plane predictor
        self.h = t["elev_min"] + s / 65535.0 * t["elev_range"] - t["datum"]
        self.n, self.size, self.x0, self.z0 = n, t["size_m"], t["position"][0], t["position"][2]
        self.cell = self.size / (n - 1)

    def y(self, x, z):
        c = (np.asarray(x) - self.x0) / self.cell
        r = (np.asarray(z) - self.z0) / self.cell
        c0 = np.clip(np.floor(c).astype(int), 0, self.n - 2)
        r0 = np.clip(np.floor(r).astype(int), 0, self.n - 2)
        fc, fr = c - c0, r - r0
        h = self.h
        return (h[r0, c0] * (1 - fc) * (1 - fr) + h[r0, c0 + 1] * fc * (1 - fr)
                + h[r0 + 1, c0] * (1 - fc) * fr + h[r0 + 1, c0 + 1] * fc * fr)


def heading_of(dx, dz):
    return (math.degrees(math.atan2(dx, dz)) + 360.0) % 360.0


def wrap(a):
    return (a + 180.0) % 360.0 - 180.0


class Run:
    """One stretch of motion in one direction, from standstill to standstill."""

    def __init__(self, pts, reverse=False, loaded=False, vmax=None, pose=None, stop=0.0, stop_pose=None, tag=""):
        self.pts = pts
        self.reverse = reverse
        self.loaded = loaded
        self.vmax = vmax
        self.pose = pose or (lambda u: (0.0, 0.0))          # u = 0..1 along the run -> (p1, p2)
        self.stop = stop                                      # seconds standing at the end
        self.stop_pose = stop_pose or (lambda t: None)        # t = seconds into the stop -> (p1, p2) or None
        self.tag = tag


def speed_limit(kind, grade, loaded, reverse):
    if reverse:
        return 2.0
    if kind == "Hauler":
        if loaded:
            return max(3.0, 7.5 - 40.0 * grade) if grade > 0 else max(4.5, 7.5 + 30.0 * grade)
        return max(5.0, 9.0 - 30.0 * grade) if grade > 0 else max(6.0, 9.0 + 25.0 * grade)
    if kind == "Loader":
        return 3.2
    return 1.8                                               # dozer pushing or backing


ACCEL = {"Dozer": (0.7, 1.1), "Loader": (1.3, 1.6), "Hauler": (0.7, 1.1)}   # m/s2 speeding up, braking


def plan(kind, runs, ground, lat=1.6, step=0.5):
    """Time the runs. Returns per-sample arrays t, x, z, heading, travel, p1, p2, flag."""
    accel, decel = ACCEL[kind]
    out = {k: [] for k in ("t", "x", "z", "heading", "travel", "p1", "p2", "flag")}
    t, travel = 0.0, 0.0
    last_pose = (0.0, 0.0)
    for k, run in enumerate(runs):
        poly, _ = catmull_rom(run.pts, step=step) if len(run.pts) > 2 else (np.array(run.pts, float), None)
        if len(run.pts) == 2:                                 # straight run: resample evenly
            a, b = np.array(run.pts[0], float), np.array(run.pts[1], float)
            n = max(2, int(np.linalg.norm(b - a) / step) + 1)
            poly = np.array([a + (b - a) * i / (n - 1) for i in range(n)])
        s = stations(poly)
        d = np.gradient(poly, axis=0)
        hd = np.array([heading_of(dx, dz) for dx, dz in d])
        if run.reverse:
            hd = (hd + 180.0) % 360.0
        y = ground.y(poly[:, 0], poly[:, 1])
        grade = np.gradient(y) / np.maximum(np.gradient(s), 1e-6)
        if run.reverse:
            grade = -grade
        dh = np.radians(np.array([wrap(b - a) for a, b in zip(hd[:-1], hd[1:])] + [0.0]))
        kappa = np.abs(dh) / np.maximum(np.gradient(s), 1e-6)
        kw = min(9, len(kappa))
        kappa = np.convolve(kappa, np.ones(kw) / kw, mode="same")
        v = np.array([speed_limit(kind, g, run.loaded, run.reverse) for g in grade])
        if run.vmax:
            v = np.minimum(v, run.vmax)
        v = np.minimum(v, np.sqrt(lat / np.maximum(kappa, 1e-4)))
        v[0] = v[-1] = 0.0
        for i in range(1, len(v)):                            # acceleration limit
            v[i] = min(v[i], math.sqrt(v[i - 1] ** 2 + 2 * accel * (s[i] - s[i - 1])))
        for i in range(len(v) - 2, -1, -1):                   # braking limit
            v[i] = min(v[i], math.sqrt(v[i + 1] ** 2 + 2 * decel * (s[i + 1] - s[i])))
        sign = -1.0 if run.reverse else 1.0
        for i in range(len(poly)):
            if i:
                ds = s[i] - s[i - 1]
                t += ds / max((v[i] + v[i - 1]) / 2, 0.05)
                travel += sign * ds
            u = s[i] / max(s[-1], 1e-6)
            p1, p2 = run.pose(u)
            last_pose = (p1, p2)
            for key, val in (("t", t), ("x", poly[i, 0]), ("z", poly[i, 1]), ("heading", hd[i]),
                             ("travel", travel), ("p1", p1), ("p2", p2), ("flag", 1.0 if run.loaded else 0.0)):
                out[key].append(float(val))
        # stand still at the end, turning on the spot toward the next run's start heading if needed
        nxt = runs[(k + 1) % len(runs)]
        hd_next = heading_of(*(np.array(nxt.pts[1], float) - np.array(nxt.pts[0], float)))
        if nxt.reverse:
            hd_next = (hd_next + 180.0) % 360.0
        stop = max(run.stop, 0.6)
        n = max(2, int(stop / 0.1))
        h0 = hd[-1]
        dturn = wrap(hd_next - h0)
        for j in range(1, n + 1):
            ts = stop * j / n
            pose = run.stop_pose(ts)
            if pose is not None:
                last_pose = pose
            turn = min(1.0, ts / min(stop, 1.5))
            for key, val in (("t", t + ts), ("x", poly[-1, 0]), ("z", poly[-1, 1]),
                             ("heading", (h0 + dturn * (0.5 - 0.5 * math.cos(math.pi * turn))) % 360.0),
                             ("travel", travel), ("p1", last_pose[0]), ("p2", last_pose[1]),
                             ("flag", 1.0 if run.loaded else 0.0)):
                out[key].append(float(val))
        t += stop
    return {k: np.array(v) for k, v in out.items()}


def lerp(a, b, u):
    return a + (b - a) * min(max(u, 0.0), 1.0)


def ease(u):
    u = min(max(u, 0.0), 1.0)
    return 0.5 - 0.5 * math.cos(math.pi * u)


def resample(kind, tr, dt, period):
    """Frames on a fixed time grid over one period, with steering from the path curvature."""
    tg = np.arange(0.0, period, dt)
    t = tr["t"]
    keep = np.concatenate([[True], np.diff(t) > 1e-6])
    t = t[keep]
    cols = {k: tr[k][keep] for k in tr}
    hd_un = np.degrees(np.unwrap(np.radians(cols["heading"])))
    frame = {
        "x": np.interp(tg, t, cols["x"]), "z": np.interp(tg, t, cols["z"]),
        "heading": np.interp(tg, t, hd_un) % 360.0, "travel": np.interp(tg, t, cols["travel"]),
        "p1": np.interp(tg, t, cols["p1"]), "p2": np.interp(tg, t, cols["p2"]),
        "flag": np.interp(tg, t, cols["flag"]) > 0.5,
    }
    # steering: heading change per metre of travel through the wheelbase
    trav = np.interp(tg, t, cols["travel"])
    hh = np.interp(tg, t, hd_un)
    dtr = np.gradient(trav)
    dh = np.gradient(hh)
    moving = np.abs(dtr) > 1e-3
    kap = np.where(moving, np.radians(dh) / np.where(moving, dtr, 1.0), 0.0)
    kap = np.convolve(kap, np.ones(5) / 5, mode="same")
    lim = STEER_LIMIT[kind]
    steer = np.clip(np.degrees(np.arctan(WHEELBASE[kind] * kap)) * (1.6 if kind == "Loader" else 1.0), -lim, lim)
    frame["steer"] = np.where(moving, steer, 0.0)
    # hold steering through stops (a parked wheel does not snap straight)
    for i in range(1, len(tg)):
        if not moving[i]:
            frame["steer"][i] = frame["steer"][i - 1] * 0.97
    return frame


# ==================================================================================
# the haul loop
# ==================================================================================
HAULERS_ON_LOOP = 5
PASSES = 2                      # loader buckets per truck
LOAD_STOP, DUMP_STOP, CUSP_STOP = 32.0, 14.0, 1.2
DUMP_UP, DUMP_HOLD = 5.0, 3.5
# --live: six trucks a sixth of the loop apart (~38.7 s) cannot keep the five-truck loop timing.
# A 32 s load stop leaves the follower on top of the truck still pulling away from the load point,
# and the dump pad's turn-round, where a truck stops before it reverses, is swept by the truck
# that has just dumped. Three constants change, and nothing else in the loop: the load stop is
# shorter, the turn-round stop is long enough for the truck ahead to clear the pad (a short queue
# at the pad, as on a real haul road) and the turn-round is 2 m further from the tipping edge.
LIVE_LOAD_STOP, LIVE_CUSP_STOP, LIVE_TURN_X = 28.0, 10.0, 78.0
TURN_X = 80.0                   # x of the turn-round on the dump pad, where a truck stops before reversing
MIN_LANE = 3.4                  # two 5.7 m haul trucks pass with a metre of air between them (SiteGeometry.MinLaneOffset)
TWO_LANE_WIDTH = 2 * (MIN_LANE + 5.7 / 2)  # the narrowest road whose two lanes keep a haul truck's outer edge on it (RoadLine.TwoLaneWidth)


def lane_offset(width):
    """How far from a road's centreline the lane of each direction runs: a quarter of the width, and never
    less than MIN_LANE; none on a road narrower than TWO_LANE_WIDTH, which is single-lane (its centreline is
    driven). Traffic keeps left. Tasks/SiteGeometry.cs (`RoadLine.LaneOffset`) applies the same rule to the
    routes machines drive on command; the two must agree (LaneTests checks the loop against it)."""
    if width < TWO_LANE_WIDTH:
        return 0.0
    return max(width / 4.0, MIN_LANE)


LANE_STEP = 0.1                # how much nearer the centreline one pass of the easing takes a steep stretch
LANE_SLEW = 0.25                # a lane eases sideways no faster than this many metres per metre forward (LaneLine.Slope)
LANE_EASE_AT = 11.8           # a stretch of lane steeper than this (percent) is eased toward MIN_LANE: the limit less what
                               # the 8 m thinning and the spline through it can add (GRADE_MAX_PCT is 12)


def _thin_index(lane):
    keep = [0]
    for i in range(1, len(lane)):
        if np.linalg.norm(lane[i] - lane[keep[-1]]) >= 8.0 or i == len(lane) - 1:
            keep.append(i)
    return keep


def _steep_points(ground, lane, limit):
    """Which of the lane's points lie within a GRADE_WINDOW_M stretch of it steeper than `limit` percent, read on the
    line the loop will drive: the lane thinned to 8 m and the spline through that (see `plan`)."""
    poly, _ = catmull_rom([tuple(lane[k]) for k in _thin_index(lane)], step=0.5)
    cum = stations(poly)
    n = int(math.ceil(cum[-1] / GRADE_SAMPLE_M))
    s = np.linspace(0.0, cum[-1], n + 1)
    step = s[1] - s[0]
    px, pz = np.interp(s, cum, poly[:, 0]), np.interp(s, cum, poly[:, 1])
    y = ground.y(px, pz)
    w = max(1, int(round(GRADE_WINDOW_M / step)))
    at = cum[np.argmin(np.hypot(lane[:, 0:1] - poly[None, :, 0], lane[:, 1:2] - poly[None, :, 1]), axis=1)]
    bad = np.zeros(len(lane), bool)
    for k in range(0, n + 1 - w):
        if abs(y[k + w] - y[k]) / (w * step) * 100.0 > limit:
            bad |= (at >= s[k] - 1e-9) & (at <= s[k + w] + 1e-9)
    return bad


def eased_offsets(ground, pts, left, base):
    """The lane's distance from the road's centreline at each point: `base` (the width rule), eased toward MIN_LANE
    over the stretches where the lane itself would climb more than GRADE_MAX_PCT. The inside of a curve is shorter
    than the centreline, so the same rise is steeper there; a lane nearer the centreline is longer and gentler. It
    eases in and out at LANE_SLEW and never goes below MIN_LANE (two trucks keep a metre of air between them in
    opposite lanes)."""
    along = np.r_[0.0, np.cumsum(np.linalg.norm(np.diff(pts, axis=0), axis=1))]
    off = np.array(base, float)
    for _ in range(40):
        bad = _steep_points(ground, pts + left * off[:, None], LANE_EASE_AT)
        if not bad.any():
            break
        # a step nearer the centreline for every point of a steep stretch, until it is not steep or is at MIN_LANE
        target = np.where(bad, np.maximum(np.minimum(off, base) - LANE_STEP, np.minimum(off, MIN_LANE)), np.array(base, float))
        # the highest profile that is nowhere above `target` and never changes faster than LANE_SLEW per metre
        eased = np.array([np.min(target + LANE_SLEW * np.abs(along - along[i])) for i in range(len(along))])
        eased = np.minimum(eased, base)
        if np.allclose(eased, off):
            break                                            # at MIN_LANE and still steep: the check reports it
        off = eased
    return off


def _thin(lane):
    return [tuple(map(float, lane[i])) for i in _thin_index(lane)]


def road_lane(ground, name, reverse=False, x_from=None, i0=0, i1=None, fade_in=0.0):
    """The road's centreline, offset to the left of the way the loop travels it (reverse: against the order
    the file lists its points), as (x, z) points at least 8 m apart (plus the last). `fade_in` eases the
    offset in from the road's centreline over that many metres (a road that starts at a gate or a sign). Where the
    lane would climb more than the grade limit it is eased toward MIN_LANE (see `eased_offsets`)."""
    rd = next(r for r in ground.f["roads"] if r["name"] == name)
    pts = np.array([(p[0], p[2]) for p in rd["points"]])[i0:i1]
    if reverse:
        pts = pts[::-1]
    if x_from is not None:
        pts = pts[int(np.argmax(pts[:, 0] >= x_from)):]
    d = np.gradient(pts, axis=0)
    d /= np.linalg.norm(d, axis=1, keepdims=True)
    left = np.stack([-d[:, 1], d[:, 0]], 1)
    along = np.r_[0.0, np.cumsum(np.linalg.norm(np.diff(pts, axis=0), axis=1))]
    k = np.minimum(1.0, along / fade_in) if fade_in > 0 else np.ones(len(pts))
    off = eased_offsets(ground, pts, left, lane_offset(rd["width"]) * k)
    return _thin(pts + left * off[:, None])


def ramp_lane(ground, side, x_from=-14.0):
    """The ramp centreline from x_from to the top, offset to the left (side=+1) or right (side=-1) of the
    uphill direction by the ramp's lane offset, as (x, z) points about 8 m apart. Where the lane would climb
    more than the grade limit (the inside of the curve at the top) it is eased toward MIN_LANE."""
    rd = next(r for r in ground.f["roads"] if r["name"] == "pit-ramp")
    pts = np.array([(p[0], p[2]) for p in rd["points"]])
    i0 = int(np.argmax(pts[:, 0] >= x_from))
    pts = pts[i0:]
    d = np.gradient(pts, axis=0)
    d /= np.linalg.norm(d, axis=1, keepdims=True)
    left = np.stack([-d[:, 1], d[:, 0]], 1)
    off = eased_offsets(ground, pts, left * side, np.full(len(pts), lane_offset(rd["width"])))
    return _thin(pts + left * side * off[:, None])


def haul_runs(ground):
    def dump_pose(ts):
        if ts < 1.0:
            return (0.0, 0.0)
        ts -= 1.0
        if ts < DUMP_UP:
            return (50.0 * ease(ts / DUMP_UP), 0.0)
        ts -= DUMP_UP
        if ts < DUMP_HOLD:
            return (50.0, 0.0)
        ts -= DUMP_HOLD
        return (50.0 * (1 - ease(ts / DUMP_UP)), 0.0)
    up = ramp_lane(ground, +1)                 # loaded, uphill, north/east lane
    down = list(reversed(ramp_lane(ground, -1)))
    # 1: loaded, from the load point round the pit floor, up the ramp, to the turn-round on the dump pad
    out = ([(4.0, 35.5), (16.0, 35.5), (27.0, 36.0), (33.5, 31.5), (29.0, 27.0), (16.0, 26.5), (-4.0, 26.5),
            (-14.0, 26.5), (-22.0, 24.0), (-24.0, 15.0)] + up[1:]
           + road_lane(ground, "fill-road")[1:] + [(90.0, -45.0), (86.0, -49.5), (TURN_X, -51.0)])
    # 2: reverse to the tipping edge
    back = [(TURN_X, -51.0), (101.0, -51.0)]
    # 3: empty, back along the return road, through the yard past the refuel bay, down the ramp,
    #    round to the load point
    home = ([(101.0, -51.0), (94.0, -53.0), (89.0, -58.0), (84.0, -62.0)]
            + road_lane(ground, "fill-return") + [(-47.5, -62.0), (-49.0, -52.0), (-49.0, -40.0)]
            + road_lane(ground, "yard-road", fade_in=24.0)[:-1]
            + down[1:-1] + [(-17.0, 6.0), (-31.0, 21.0), (-29.0, 31.0), (-24.0, 36.5), (-16.0, 36.0),
                           (-6.0, 35.5), (4.0, 35.5)])
    return [
        Run(out, loaded=True, stop=CUSP_STOP, tag="haul-loaded"),
        Run(back, reverse=True, loaded=True, stop=DUMP_STOP, stop_pose=dump_pose, tag="dump"),
        Run(home, loaded=False, stop=LOAD_STOP, tag="load"),
    ]


def yard_hauler_runs():
    """A truck out of the loop for its service: from its parking place round to the refuel bay
    (a lay-by beside the through lane), a stop while it is fuelled, and back to park."""
    to_bay = [(-82.0, -57.0), (-82.0, -63.0), (-77.0, -68.0), (-68.0, -68.0), (-61.5, -64.0), (-59.5, -59.0),
              (-59.5, -56.5)]
    to_park = [(-59.5, -56.5), (-59.5, -51.0), (-62.5, -48.0), (-68.0, -48.0), (-76.0, -48.0), (-80.5, -51.0),
               (-82.0, -54.0), (-82.0, -57.0)]
    return [
        Run(to_bay, stop=40.0, tag="refuel"),
        Run(to_park, stop=50.0, tag="parked"),
    ]


# ==================================================================================
# loaders and dozers
# ==================================================================================
def loader_v(dig, rev, dump, dig_stop=1.6, dump_stop=3.6, via=()):
    """Load-and-carry V: into the pile, back out, turn to the truck, tip, back out, return.
    `via` bends the run to the truck so that the loader arrives square to the truck's side."""
    def at_dig(ts):
        return (BOOM_GROUND, lerp(BUCKET_FLAT, BUCKET_RACK, ts / 1.3))
    def at_dump(ts):
        if ts < 1.6:
            return (BOOM_HIGH, lerp(BUCKET_RACK, BUCKET_DUMP, ease(ts / 1.6)))
        return (BOOM_HIGH, lerp(BUCKET_DUMP, BUCKET_RACK, ease((ts - 2.2) / 1.4)))
    return [
        Run([dig, rev], reverse=True, pose=lambda u: (lerp(BOOM_GROUND, BOOM_CARRY, u), BUCKET_RACK),
            stop=0.8, tag="back-out"),
        # the boom is fully up before the bucket reaches the truck's side
        Run([rev, *via, dump], pose=lambda u: (lerp(BOOM_CARRY, BOOM_HIGH, ease(u / 0.6)), BUCKET_RACK),
            stop=dump_stop, stop_pose=at_dump, tag="to-truck"),
        Run([dump, *reversed(via), rev], reverse=True, pose=lambda u: (lerp(BOOM_HIGH, BOOM_CARRY, ease((u - 0.4) / 0.6)), BUCKET_RACK),
            stop=0.8, tag="back-off"),
        Run([rev, dig], pose=lambda u: (lerp(BOOM_CARRY, BOOM_GROUND, u), lerp(BUCKET_RACK, BUCKET_FLAT, u)),
            stop=dig_stop, stop_pose=at_dig, tag="to-pile"),
    ]


def shuttle(kind, a, b, work_stop=2.0, back_stop=1.0, push_pose=None, back_pose=None, stop_pose=None):
    """Forward a -> b (working), stop, reverse b -> a, stop."""
    return [
        Run([a, b], pose=push_pose, stop=work_stop, stop_pose=stop_pose, tag="work"),
        Run([b, a], reverse=True, pose=back_pose, stop=back_stop, tag="back"),
    ]


def dozer_push(a, b, rip=False):
    return shuttle("Dozer", a, b, work_stop=1.5, back_stop=1.0,
                   push_pose=lambda u: (lerp(BLADE_CARRY, BLADE_DIG, u * 4) if u < 0.9 else lerp(BLADE_DIG, BLADE_RAISED, (u - 0.9) * 10),
                                        -30.0 if rip else 0.0),
                   back_pose=lambda u: (BLADE_RAISED, 0.0),
                   stop_pose=lambda ts: (BLADE_RAISED, 0.0))


def loader_rehandle(a, b):
    return shuttle("Loader", a, b, work_stop=2.5, back_stop=1.5,
                   push_pose=lambda u: (BOOM_GROUND, BUCKET_FLAT),
                   back_pose=lambda u: (lerp(BOOM_GROUND, -30.0, ease(u)), BUCKET_RACK),
                   stop_pose=lambda ts: (BOOM_GROUND, lerp(BUCKET_FLAT, BUCKET_RACK, ts / 2.0)))


def parked(kind, x, z, heading, pose):
    """A machine standing parked with its engine off: it does not move at all (two points a
    couple of centimetres apart give it its heading)."""
    hx, hz = math.sin(math.radians(heading)), math.cos(math.radians(heading))
    a, b = (x - hx * 0.01, z - hz * 0.01), (x + hx * 0.01, z + hz * 0.01)
    return [Run([a, b], vmax=0.05, pose=lambda u: pose, stop=30.0, stop_pose=lambda ts: pose, tag="parked")]


PARKED_LOADER = (BOOM_GROUND, BUCKET_FLAT)       # bucket flat on the ground
PARKED_DOZER = (0.0, 0.0)                        # blade resting on the ground


def fleet(ground, live=False):
    global LOAD_STOP, CUSP_STOP, TURN_X
    if live:
        LOAD_STOP, CUSP_STOP, TURN_X = LIVE_LOAD_STOP, LIVE_CUSP_STOP, LIVE_TURN_X
    tracks, machines = [], []
    n_loop = 6 if live else HAULERS_ON_LOOP

    def add_track(kind, runs, period=None):
        tr = plan(kind, runs, ground)
        T = period or tr["t"][-1]
        tracks.append(dict(kind=kind, raw=tr, period=float(T)))
        return len(tracks) - 1

    # haulers on the loop
    hl = add_track("Hauler", haul_runs(ground))
    T = tracks[hl]["period"]
    load_start = T - LOAD_STOP                        # the load stop ends the loop
    gap = T / n_loop
    for k in range(n_loop):
        machines.append(dict(id=f"SP-HL-{k + 1:04d}", kind="Hauler", track=hl, offset=round(-k * gap, 3)))
    if not live:
        # the sixth truck is out of the loop, going to the refuel bay and back to park
        yh = add_track("Hauler", yard_hauler_runs())
        machines.append(dict(id=f"SP-HL-{HAULERS_ON_LOOP + 1:04d}", kind="Hauler", track=yh, offset=0.0))

    # the pit loader: PASSES buckets per truck, then waits at the pile for the next one
    # a V on the loading bench: it backs out of the pile and comes in to the truck's side at
    # about 20 degrees off square, its front tyres at the bench's edge, so that its raised
    # bucket tips over the body
    pit_v = dict(dig=(-1.7, 46.3), rev=(-1.37, 44.56), dump=(0.0, 40.8), via=())
    one = loader_v(pit_v["dig"], pit_v["rev"], pit_v["dump"], via=pit_v["via"])
    probe = plan("Loader", one, ground)
    cycle = probe["t"][-1]
    if PASSES * cycle > gap - 0.5 or (PASSES - 1) * cycle + 6.0 > LOAD_STOP:
        raise SystemExit(f"{PASSES} loader passes of {cycle:.1f} s do not fit a {LOAD_STOP:.0f} s load stop "
                         f"and a {gap:.1f} s truck spacing")
    lv = []
    for _ in range(PASSES):
        lv += loader_v(pit_v["dig"], pit_v["rev"], pit_v["dump"], via=pit_v["via"])
    lv[-1].stop += gap - PASSES * cycle               # wait at the pile for the next truck
    li = add_track("Loader", lv, period=gap)
    tr = tracks[li]["raw"]
    # time within the loader cycle when the first bucket is fully tipped (end of run 2 + 1.6 s)
    t_tip = _run_end(tr, lv, 1) + 1.6
    t_want = load_start + 4.0                         # hauler 0 has been standing 4 s
    # a machine plays its track at (time - offset): tip at loader time t_tip when the scene is at t_want
    machines.append(dict(id="SP-LD-0001", kind="Loader", track=li, offset=round((t_want - t_tip) % gap, 3)))
    # the hauler shows its load once the first bucket has tipped
    tracks[hl]["load_from"] = t_want + 0.6

    # loader rehandling the pit stockpile
    li2 = add_track("Loader", loader_rehandle((46.0, 47.0), (49.0, 48.5)))
    machines.append(dict(id="SP-LD-0002", kind="Loader", track=li2, offset=3.0))
    # the plant: one loader feeds the crusher's hopper from the feed stockpile...
    feed = ground.f["spots"]["plant-feed"]
    li3 = add_track("Loader", loader_v((-51.5, -90.0), (-53.0, -99.0), (feed["x"], feed["z"])))
    machines.append(dict(id="SP-LD-0003", kind="Loader", track=li3, offset=0.0))
    # ...and one works the product stockpile under the stacker
    li4 = add_track("Loader", loader_rehandle((8.6, -112.8), (12.8, -108.6)))
    machines.append(dict(id="SP-LD-0004", kind="Loader", track=li4, offset=5.0))
    # two loaders are not needed today: one is in the workshop, one is parked
    li5 = add_track("Loader", parked("Loader", -70.0, -35.5, 180.0, PARKED_LOADER))
    machines.append(dict(id="SP-LD-0005", kind="Loader", track=li5, offset=0.0))
    li6 = add_track("Loader", parked("Loader", -94.5, -53.0, 68.0, PARKED_LOADER))
    machines.append(dict(id="SP-LD-0006", kind="Loader", track=li6, offset=0.0))

    # dozers: one pushing up the muck pile at the toe of the north face, one ripping the pit
    # floor, one spreading on the dump pad; the other three are parked in the yard
    for k, (a, b, rip) in enumerate([
        ((-30.0, 51.0), (-16.5, 51.0), False),
        ((-30.0, 44.0), (-20.0, 44.0), True),
        ((102.0, -81.0), (102.0, -70.0), False),
    ]):
        di = add_track("Dozer", dozer_push(a, b, rip))
        machines.append(dict(id=f"SP-DZ-{k + 1:04d}", kind="Dozer", track=di, offset=round(k * 2.7, 3)))
    # parked where their drivers left them, not lined up for display
    for k, (x, z, hd) in enumerate(((-92.0, -45.5, 78.0), (-90.2, -38.6, 103.0), (-93.4, -31.0, 86.0))):
        di = add_track("Dozer", parked("Dozer", x, z, hd, PARKED_DOZER))
        machines.append(dict(id=f"SP-DZ-{k + 4:04d}", kind="Dozer", track=di, offset=0.0))
    return tracks, machines


def _run_end(tr, runs, idx):
    """Time at which run `idx` stops moving (its stop starts)."""
    t = tr["t"]
    # runs end where position stays fixed; walk run boundaries by counting stops
    still = np.concatenate([[False], (np.diff(tr["x"]) == 0) & (np.diff(tr["z"]) == 0)])
    starts = [i for i in range(1, len(still)) if still[i] and not still[i - 1]]
    return float(t[starts[idx] - 1])


# ==================================================================================
# overlap check
# ==================================================================================
def corners(kind, x, z, hd, p1=0.0):
    """The machine's footprint as one or more boxes (each a list of four corners)."""
    s, c = math.sin(math.radians(hd)), math.cos(math.radians(hd))
    fwd, right = np.array([s, c]), np.array([c, -s])
    p = np.array([x, z])
    boxes = []
    for w, f, r in FOOT_PARTS.get(kind, [FOOT[kind]]):
        if kind == "Loader" and p1 < -40.0:
            f = LOADER_RAISED_FRONT      # boom up: the bucket is over whatever it is tipping into
        boxes.append([p + fwd * f + right * w, p + fwd * f - right * w, p - fwd * r - right * w, p - fwd * r + right * w])
    return boxes


def sat_gap(A, B):
    """Separation along the best axis between two convex quads (negative = overlapping)."""
    best = -1e9
    for poly in (A, B):
        for i in range(4):
            e = poly[(i + 1) % 4] - poly[i]
            n = np.array([-e[1], e[0]]) / (np.linalg.norm(e) + 1e-9)
            pa = [float(n @ v) for v in A]
            pb = [float(n @ v) for v in B]
            best = max(best, max(min(pb) - max(pa), min(pa) - max(pb)))
    return best


HAULER_CLEARANCE = 2.5          # metres two haul trucks must keep apart, passing or queueing. The tightest place on the
                                # loop is the head of the ramp, where the loaded truck in the outer lane meets the empty
                                # one in the inside lane, which is eased toward the centreline to stay within the grade
                                # limit (see `eased_offsets`): SP-HL-0001 and SP-HL-0004 come to 2.63 m there in the live
                                # loop (preview 3.19 m). With the lane at its full 5 m the pair is 2.78 m apart, with it eased
                                # all the way to 3.4 m 2.40 m, so the lane is eased only as far as the grade needs.
LOADER_RAISED_FRONT = 2.6       # a loader's footprint front (m) with its boom raised: its front tyres


def check(tracks, machines, frames, dt, horizon):
    """Closest approach over the horizon: (gap, ids, time) for any two machines, and the same
    for any two haul trucks."""
    worst = (1e9, None)
    worst_hl = (1e9, None)
    times = np.arange(0.0, horizon, 0.5)
    pos = []
    for m in machines:
        tr, fr = tracks[m["track"]], frames[m["track"]]
        idx = (((times - m["offset"]) % tr["period"]) / dt).astype(int) % len(fr["x"])
        pos.append([corners(m["kind"], fr["x"][i], fr["z"][i], fr["heading"][i], fr["p1"][i]) for i in idx])
    for a in range(len(machines)):
        for b in range(a + 1, len(machines)):
            for k in range(len(times)):
                ca, cb = pos[a][k], pos[b][k]
                if np.linalg.norm(ca[0][0] - cb[0][0]) > 30:
                    continue
                g = min(sat_gap(qa, qb) for qa in ca for qb in cb)
                if g < worst[0]:
                    worst = (g, (machines[a]["id"], machines[b]["id"], float(times[k])))
                if machines[a]["kind"] == machines[b]["kind"] == "Hauler" and g < worst_hl[0]:
                    worst_hl = (g, (machines[a]["id"], machines[b]["id"], float(times[k])))
    return worst, worst_hl


# ==================================================================================
# the site checks: what a machine drives past and over
# ==================================================================================
# Each of these mirrors a rule the task layer plans by, so that the loop is held to the line the planner holds a
# commanded route to: Tasks/Reservations.cs (`ParkingLot.TravelRadius`, `TravelClearance`), Tasks/TerrainHeights.cs
# (`Grade`) and App/SiteGeometryReader.cs (`PropHalfExtents`, the piles and the refuel approach). The EditMode tests
# in SiteClearanceTests read the committed fleet files against the same rules.
TRAVEL_RADIUS = {"Hauler": 3.0, "Loader": 2.0, "Dozer": 2.0}   # a DRIVING machine's reach from its point
TRAVEL_CLEARANCE = 0.2          # air kept between a driving machine and anything standing
GRADE_MAX_PCT, GRADE_WINDOW_M, GRADE_STEP_M, GRADE_STEP_MAX_PCT, GRADE_SAMPLE_M = 12.0, 10.0, 4.0, 25.0, 1.0
PROP_HALF = {                   # half extents along a prop's own X and Z (SiteGeometryReader.PropHalfExtents)
    "site_office": (4.85, 2.5), "workshop": (9.3, 6.3), "container_blue": (3.03, 1.22), "container_red": (3.03, 1.22),
    "fuel_tank": (5.2, 3.5), "light_tower": (2.5, 1.7), "cone": (0.2, 0.2), "barrier": (1.48, 0.3),
    "site_sign": (1.65, 0.3), "crusher_plant": (22.0, 16.0),
}
APPROACH_RADIUS = 6.4           # the refuel approach: queue to bay, a hauler's footprint wide


def site_obstacles(feats):
    """What stands on the site, as the task layer reads it: props (oriented boxes), piles and the refuel approach
    (capsules). ("box", name, x, z, hx, hz, heading deg) | ("capsule", name, ax, az, bx, bz, radius)."""
    out = []
    for p in feats["props"]:
        hx, hz = PROP_HALF[p["p"]]
        out.append(("box", p["p"], p["x"], p["z"], hx, hz, p.get("heading", 0.0)))
    for p in feats["piles"]:
        hd = math.radians(p.get("heading", 0.0))
        ux, uz = math.sin(hd) * p.get("len", 0.0) / 2.0, math.cos(hd) * p.get("len", 0.0) / 2.0
        r = p["h"] / math.tan(math.radians(37.0)) * 1.3
        out.append(("capsule", p["name"], p["x"] - ux, p["z"] - uz, p["x"] + ux, p["z"] + uz, r))
    sp = feats["spots"]
    out.append(("capsule", "refuel-approach", sp["refuel-queue"]["x"], sp["refuel-queue"]["z"],
                sp["refuel-bay"]["x"], sp["refuel-bay"]["z"], APPROACH_RADIUS))
    return out


def _box_distance(o, x, z, h):
    cx, cz, hx, hz = o[2], o[3], o[4], o[5]
    dx, dz = x - cx, z - cz
    c, s = math.cos(h), math.sin(h)
    lx, lz = abs(dx * c - dz * s) - hx, abs(dx * s + dz * c) - hz
    if lx <= 0 and lz <= 0:
        return max(lx, lz)
    return math.hypot(max(lx, 0.0), max(lz, 0.0))


def _seg_distance(x, z, ax, az, bx, bz):
    sx, sz = bx - ax, bz - az
    l2 = sx * sx + sz * sz
    t = 0.0 if l2 <= 0 else max(0.0, min(1.0, ((x - ax) * sx + (z - az) * sz) / l2))
    return math.hypot(x - (ax + t * sx), z - (az + t * sz))


def obstacle_distance(o, x, z):
    """How far a point is from an obstacle's outline (negative inside): Obstacle.Distance. A box answers with the
    nearer of its two possible turns (the file does not state its heading's sign)."""
    if o[0] == "capsule":
        return _seg_distance(x, z, o[2], o[3], o[4], o[5]) - o[6]
    h = math.radians(o[6])
    return min(_box_distance(o, x, z, h), _box_distance(o, x, z, -h))


def _inside_convex(quad, p):
    side = [(quad[(i + 1) % 4][0] - quad[i][0]) * (p[1] - quad[i][1]) - (quad[(i + 1) % 4][1] - quad[i][1]) * (p[0] - quad[i][0])
            for i in range(4)]
    return all(v >= 0 for v in side) or all(v <= 0 for v in side)


def _poly_seg_gap(quad, ax, az, bx, bz):
    """Distance between a convex quad and a segment (negative when the segment crosses or lies in it)."""
    if _inside_convex(quad, (ax, az)) or _inside_convex(quad, (bx, bz)):
        return -0.001
    best = 1e9
    for i in range(4):
        p, q = quad[i], quad[(i + 1) % 4]
        for u in ((ax, az), (bx, bz)):
            best = min(best, _seg_distance(u[0], u[1], p[0], p[1], q[0], q[1]))
        for u in (p, q):
            best = min(best, _seg_distance(u[0], u[1], ax, az, bx, bz))
    if best < 1e-9:
        return -0.001                                        # the segment crosses an edge
    return best


def footprint_gap(o, boxes):
    """Clear distance between a machine's footprint (corners() boxes) and an obstacle: positive is air, negative
    overlap. A box prop is judged at the worse of its two possible turns, as obstacle_distance is."""
    if o[0] == "capsule":
        return min(_poly_seg_gap(q, o[2], o[3], o[4], o[5]) - o[6] for q in boxes)
    cx, cz, hx, hz, hd = o[2], o[3], o[4], o[5], o[6]
    worst = 1e9
    for sign in (1.0, -1.0):
        h = math.radians(hd) * sign
        c, s = math.cos(h), math.sin(h)
        # BoxDistance's convention: local x = dx*c - dz*s, local z = dx*s + dz*c
        quad = [np.array([cx + lx * c + lz * s, cz - lx * s + lz * c]) for lx, lz in ((hx, hz), (hx, -hz), (-hx, -hz), (-hx, hz))]
        worst = min(worst, min(sat_gap(q, quad) for q in boxes))
    return worst


def clearance_report(kind, fr, obstacles, exempt=None):
    """How near a track passes each thing that stands, over its frames. Two readings, both against EVERY obstacle (props,
    piles and the refuel approach alike): the planner's (the machine's point at least TRAVEL_RADIUS + TRAVEL_CLEARANCE from
    the outline) and the machine's own footprint (at least TRAVEL_CLEARANCE of air). `exempt(obstacle, x, z)` says a frame
    works AT an obstacle and is not held to it (WORKS_AT). Returns {obstacle index: (point margin, frame, footprint margin,
    frame)} for every obstacle the track comes near; a margin is the distance minus what is required (negative = short)."""
    need = TRAVEL_RADIUS[kind] + TRAVEL_CLEARANCE
    found = {}
    for i in range(len(fr["x"])):
        x, z = float(fr["x"][i]), float(fr["z"][i])
        boxes = None
        for k, o in enumerate(obstacles):
            if o[0] == "box":
                cx, cz, span = o[2], o[3], math.hypot(o[4], o[5])
            else:
                cx, cz, span = (o[2] + o[4]) / 2.0, (o[3] + o[5]) / 2.0, math.hypot(o[4] - o[2], o[5] - o[3]) / 2.0 + o[6]
            if math.hypot(x - cx, z - cz) > span + 14.0:
                continue
            if exempt is not None and exempt(o, x, z):
                continue
            d = obstacle_distance(o, x, z) - need
            if boxes is None:
                boxes = corners(kind, x, z, float(fr["heading"][i]), float(fr["p1"][i]))
            g = footprint_gap(o, boxes) - TRAVEL_CLEARANCE
            cur = found.get(k, (1e9, 0, 1e9, 0))
            found[k] = (min(d, cur[0]), i if d < cur[0] else cur[1], min(g, cur[2]), i if g < cur[2] else cur[3])
    return found


def sustained_grade(ground, xs, zs):
    """The steepest grade (percent) along a path as a truck feels it: Grade.MaxSustained. The ground is read every
    GRADE_SAMPLE_M along the path; a rise over GRADE_WINDOW_M is a slope, and a rise over GRADE_STEP_M is a step,
    scaled so GRADE_STEP_MAX_PCT of step counts as GRADE_MAX_PCT. Returns (worst, arc length there, x, z)."""
    xs, zs = np.asarray(xs, float), np.asarray(zs, float)
    keep = np.concatenate([[True], np.hypot(np.diff(xs), np.diff(zs)) > 1e-6])
    xs, zs = xs[keep], zs[keep]
    cum = np.r_[0.0, np.cumsum(np.hypot(np.diff(xs), np.diff(zs)))]
    total = float(cum[-1])
    n = int(math.ceil(total / GRADE_SAMPLE_M))
    step = total / n
    s = np.arange(n + 1) * step
    px, pz = np.interp(s, cum, xs), np.interp(s, cum, zs)
    y = ground.y(px, pz)
    worst = (0.0, 0.0, float(px[0]), float(pz[0]))
    for length, scale in ((GRADE_WINDOW_M, 1.0), (GRADE_STEP_M, GRADE_MAX_PCT / GRADE_STEP_MAX_PCT)):
        w = min(n, max(1, int(round(length / step))))
        run = length if total < length else w * step
        g = np.abs(y[w:] - y[:-w]) / run * 100.0 * scale
        k = int(np.argmax(g))
        if g[k] > worst[0]:
            worst = (float(g[k]), float(s[k]), float(px[k]), float(pz[k]))
    return worst


# What a track WORKS AT on purpose is not kept clear of by the travel reach: a dozer pushes up a pile, a loader tips into the hopper,
# the scripted refuel visit drives into the bay's lay-by. The whole exempt set is this table, and nothing else is exempt: each entry is
# (obstacle name, where). `where` is None (the whole track: its own workface) or (spot name, radius): only while the machine is within
# that radius of the spot. A pile is a loader's or a dozer's workface only for the machine that works it.
WORKS_AT = {
    "SP-HL-0006": [("refuel-approach", None),                  # the strip queue to bay is this machine's own (preview fleet only)
                   ("fuel_tank", ("refuel-bay", 12.0)),         # the visit's way in and the stop, not its way back to park
                   ("cone", ("refuel-bay", 12.0))],
    "SP-LD-0001": [("muck-pile", None)],                       # loads the haul trucks from the muck pile's toe
    "SP-LD-0002": [("pit-stockpile", None)],                   # rehandles it
    "SP-LD-0003": [("feed-stockpile", None),                   # digs it ...
                   ("crusher_plant", ("plant-feed", 16.0))],   # ... and tips into the hopper, inside the plant's bounding box
    "SP-LD-0004": [("product-coarse", None)],                  # works the product stockpile under the stacker
    "SP-DZ-0001": [("muck-pile", None)],                       # pushes up the muck pile
    "SP-DZ-0003": [("fill-heap-3", None)],                     # spreads the dump pad's heap
}


def _exempt_for(name, feats):
    """The `exempt` callback of clearance_report for one machine, from WORKS_AT."""
    entries = WORKS_AT.get(name, [])
    spots = feats["spots"]

    def exempt(o, x, z):
        for oname, where in entries:
            if o[1] != oname:
                continue
            if where is None:
                return True
            sp = spots[where[0]]
            if math.hypot(x - sp["x"], z - sp["z"]) <= where[1]:
                return True
        return False
    return exempt


def _margin(m):
    return "clear" if m > 1e8 else "%+.2f m" % m


def site_checks(ground, tracks, frames, machines, dt, with_room=True):
    """What the machines drive past and over, as a list of defects (empty = clean) and report lines. Every moving
    track is read against everything that stands, except what it WORKS at (WORKS_AT). The haul loop (track 0) is also
    held to the grade a planned route is, on the line it drives."""
    obstacles = site_obstacles(ground.f)
    defects, lines = [], []
    owner = {}
    for m in machines:
        owner.setdefault(m["track"], m["id"])
    for ti, tr in enumerate(tracks):
        fr = frames[ti]
        if np.ptp(fr["x"]) < 1.0 and np.ptp(fr["z"]) < 1.0:
            continue                                         # parked: it does not drive
        name = owner.get(ti, "track %d" % ti)
        kind = tr["kind"]
        works = [e[0] if e[1] is None else "%s (within %.0f m of %s)" % (e[0], e[1][1], e[1][0]) for e in WORKS_AT.get(name, [])]
        found = clearance_report(kind, fr, obstacles, _exempt_for(name, ground.f))
        pm = min([v[0] for v in found.values()] + [1e9])
        fg = min([v[2] for v in found.values()] + [1e9])
        lines.append("site %-9s tightest: reach %s, air %s%s"
                     % (name, _margin(pm), _margin(fg), ("; works at " + ", ".join(works)) if works else ""))
        for k, (d, di, g, gi) in sorted(found.items()):
            o = obstacles[k]
            at = "%s at (%.1f, %.1f)" % (o[1], o[2], o[3])
            if d < -1e-9:
                defects.append("%s passes %s %.2f m inside the %.1f m a driving %s keeps (t = %.2f s)"
                               % (name, at, -d, TRAVEL_RADIUS[kind] + TRAVEL_CLEARANCE, kind, di * dt))
            if g < -1e-9:
                defects.append("%s's footprint is %.2f m nearer %s than the %.1f m of air kept (t = %.2f s)"
                               % (name, -g, at, TRAVEL_CLEARANCE, gi * dt))
        if ti == 0:
            g = sustained_grade(ground, fr["x"], fr["z"])
            lines.append("site %-9s grade %.2f %% at (%.1f, %.1f), the limit is %.0f %%" % (name, g[0], g[2], g[3], GRADE_MAX_PCT))
            if g[0] > GRADE_MAX_PCT:
                defects.append("the haul loop climbs %.2f %% sustained at (%.1f, %.1f); a planned route is held to %.0f %%"
                               % (g[0], g[2], g[3], GRADE_MAX_PCT))
    if with_room:
        for token, found in stand_room(ground, tracks, frames).items():
            lines.append("room %-13s %s" % (token, ", ".join("%s %s" % (k, ("(%.0f, %.0f) facing %.0f" % v) if v else "NONE")
                                                         for k, v in found.items())))
            for kind, pose in found.items():
                if pose is None:
                    defects.append("%s has no room for a %s to stand %.1f m clear of every track sweep, road, prop and pile"
                                   % (token, kind, STAND_AIR))
    return defects, lines


# ==================================================================================
# room to stand: a hauler, a loader and a dozer in each zone
# ==================================================================================
STAND_AIR = 1.5                 # metres a standing machine keeps from every track sweep, road, prop and pile
STAND_CELL = 0.25               # the raster the sweeps are drawn on
STAND_STEP = 1.0                # metres between the poses tried, and degrees between their headings:
STAND_HEADINGS = range(0, 360, 15)   # a pose found is a proof of room; the search is not exhaustive
STAND_ORDER = ("Hauler", "Loader", "Dozer")
STAND_CORNER = 6.0              # a zone's corners are rounded by this radius (a pad's `corner`)
STAND_FLAT = 0.6                # the most the ground may rise or fall under a standing machine (m)


def _in_rounded(X, Z, rect, r):
    """Which points lie inside the rect (x0, x1, z0, z1) with its corners rounded by radius r."""
    cx, cz = np.clip(X, rect[0] + r, rect[1] - r), np.clip(Z, rect[2] + r, rect[3] - r)
    return (np.hypot(X - cx, Z - cz) <= r) & (X >= rect[0]) & (X <= rect[1]) & (Z >= rect[2]) & (Z <= rect[3])


def _footprint_mask(X, Z, kind, x, z, hd, grow=0.0):
    """Which cells of the grid (X, Z) the machine at (x, z, hd) covers, grown by `grow` metres all round."""
    s, c = math.sin(math.radians(hd)), math.cos(math.radians(hd))
    mask = np.zeros(X.shape, bool)
    parts = FOOT_PARTS.get(kind, [FOOT[kind]])
    for w, f, r in parts:
        along = (X - x) * s + (Z - z) * c
        across = (X - x) * c - (Z - z) * s
        mask |= (along <= f + grow) & (along >= -r - grow) & (np.abs(across) <= w + grow)
    return mask


def _occupancy(ground, rect, tracks, frames, margin):
    """A raster over the zone (grown by `margin`) of everything a stand must keep clear of: every machine's whole sweep
    (every frame of every track, parked machines included), the roads, the props, the piles and the refuel approach."""
    x0, x1, z0, z1 = rect[0] - margin, rect[1] + margin, rect[2] - margin, rect[3] + margin
    xs, zs = np.arange(x0, x1, STAND_CELL), np.arange(z0, z1, STAND_CELL)
    X, Z = np.meshgrid(xs, zs)
    occ = np.zeros(X.shape, bool)
    for tr, fr in zip(tracks, frames):
        last = None
        for i in range(len(fr["x"])):
            x, z, hd = float(fr["x"][i]), float(fr["z"][i]), float(fr["heading"][i])
            if x < x0 - 14 or x > x1 + 14 or z < z0 - 14 or z > z1 + 14:
                continue
            if last and math.hypot(x - last[0], z - last[1]) < 0.4 and abs(wrap(hd - last[2])) < 3.0:
                continue                                     # standing still: the same footprint again
            last = (x, z, hd)
            for q_ in _crop(X, Z, x, z, 12.0):
                occ[q_[0]] |= _footprint_mask(q_[1], q_[2], tr["kind"], x, z, hd)
    for rd in ground.f["roads"]:
        P = [(p[0], p[2]) for p in rd["points"]]
        for (ax, az), (bx, bz) in zip(P[:-1], P[1:]):
            for q_ in _crop(X, Z, (ax + bx) / 2, (az + bz) / 2, math.hypot(bx - ax, bz - az) / 2 + rd["width"] / 2 + 1):
                dx, dz = bx - ax, bz - az
                t = np.clip(((q_[1] - ax) * dx + (q_[2] - az) * dz) / (dx * dx + dz * dz), 0.0, 1.0)
                occ[q_[0]] |= np.hypot(q_[1] - (ax + t * dx), q_[2] - (az + t * dz)) <= rd["width"] / 2
    for o in site_obstacles(ground.f):
        cx, cz = ((o[2], o[3]) if o[0] == "box" else ((o[2] + o[4]) / 2, (o[3] + o[5]) / 2))
        reach = (math.hypot(o[4], o[5]) if o[0] == "box" else math.hypot(o[4] - o[2], o[5] - o[3]) / 2 + o[6]) + 2.0
        for sl, gx, gz in _crop(X, Z, cx, cz, reach):
            d = np.minimum.reduce([_box_distance_grid(o, gx, gz, h) for h in (math.radians(o[6]), -math.radians(o[6]))]) \
                if o[0] == "box" else _seg_distance_grid(gx, gz, o[2], o[3], o[4], o[5]) - o[6]
            occ[sl] |= d <= 0.0
    return X, Z, occ


def _crop(X, Z, cx, cz, r):
    """The grid cut to the square of half side r about (cx, cz): [(slice, X, Z)] (empty when it misses)."""
    c0 = max(0, int((cx - r - X[0, 0]) / STAND_CELL)); c1 = min(X.shape[1], int((cx + r - X[0, 0]) / STAND_CELL) + 2)
    r0 = max(0, int((cz - r - Z[0, 0]) / STAND_CELL)); r1 = min(X.shape[0], int((cz + r - Z[0, 0]) / STAND_CELL) + 2)
    if c1 <= c0 or r1 <= r0:
        return []
    sl = (slice(r0, r1), slice(c0, c1))
    return [(sl, X[sl], Z[sl])]


def _box_distance_grid(o, X, Z, h):
    dx, dz = X - o[2], Z - o[3]
    c, s = math.cos(h), math.sin(h)
    lx, lz = np.abs(dx * c - dz * s) - o[4], np.abs(dx * s + dz * c) - o[5]
    return np.where((lx <= 0) & (lz <= 0), np.maximum(lx, lz), np.hypot(np.maximum(lx, 0), np.maximum(lz, 0)))


def _seg_distance_grid(X, Z, ax, az, bx, bz):
    sx, sz = bx - ax, bz - az
    l2 = sx * sx + sz * sz
    t = np.zeros(X.shape) if l2 <= 0 else np.clip(((X - ax) * sx + (Z - az) * sz) / l2, 0.0, 1.0)
    return np.hypot(X - (ax + t * sx), Z - (az + t * sz))


def stand_room(ground, tracks, frames, zones=None, kinds=STAND_ORDER):
    """For each zone: can a hauler, a loader and a dozer all stand in it, each at least STAND_AIR from every track
    sweep, road, prop and pile (and from the others), inside the zone's rect? Greedy, in STAND_ORDER, scanning the zone
    west to east and south to north, every STAND_HEADINGS. Returns {zone token: {kind: (x, z, heading) or None}}."""
    out = {}
    for zone in (ground.f["zones"] if zones is None else zones):
        rect = zone["rect"]
        X, Z, occ = _occupancy(ground, rect, tracks, frames, STAND_AIR + 1.0)
        found = {}
        for kind in kinds:
            found[kind] = None
            for z in np.arange(rect[2], rect[3] + 1e-9, STAND_STEP):
                for x in np.arange(rect[0], rect[1] + 1e-9, STAND_STEP):
                    for hd in STAND_HEADINGS:
                        sl, gx, gz = _crop(X, Z, x, z, 9.0)[0]
                        body = _footprint_mask(gx, gz, kind, x, z, hd)
                        if not (~body | _in_rounded(gx, gz, rect, zone.get("corner", STAND_CORNER))).all():
                            continue                         # the body leaves the zone (its corners are rounded, like a pad's)
                        if np.ptp(ground.y(gx[body], gz[body])) > STAND_FLAT:
                            continue                         # not level ground (a wall, a batter)
                        if (_footprint_mask(gx, gz, kind, x, z, hd, STAND_AIR + STAND_CELL) & occ[sl]).any():   # a cell more: the raster is drawn on whole cells
                            continue
                        found[kind] = (float(x), float(z), float(hd))
                        occ[sl] |= _footprint_mask(gx, gz, kind, x, z, hd)
                        break
                    if found[kind]:
                        break
                if found[kind]:
                    break
        out[zone["token"]] = found
    return out


def build(ground, live, dt):
    """The fleet's tracks, machines and resampled frames; the preview fleet when not `live`."""
    tracks, machines = fleet(ground, live)
    frames = []
    for tr in tracks:
        fr = resample(tr["kind"], tr["raw"], dt, tr["period"])
        if tr["kind"] == "Hauler" and "load_from" in tr:
            tg = np.arange(0.0, tr["period"], dt)
            # carrying from the moment the loader's bucket tips until the dump: the load stop is
            # the end of the loop, so the tipped load shows for its last few seconds too
            fr["flag"] = fr["flag"] | (tg >= tr["load_from"])
        frames.append(fr)
    return tracks, machines, frames


CONTROLS = 10                   # how many controls `selftest` runs: it fails if it ran fewer, whatever it concluded about them


def selftest(terrain, dt):
    """Each site check must FAIL on a site made to break it, and pass a change that breaks nothing. Builds the live fleet
    on the real site, then on copies of it, one control per reading: a light tower on the loop; one 3.1 m off its line
    (short of the 3.2 m reach by the point alone) and one 3.3 m off (passes); one 2.95 m off (the truck's side 0.1 m from
    it: short of the air); one ahead on the front deck's corner where the point is far off; a pile beside the loop; a
    climb just over the grade limit and one just under it; and a zone with a gap too narrow for a hauler and 1.5 m of air
    round it, and a wider one. Every one is named, and counted: a verdict that skips controls fails."""
    import copy
    ground = Ground(terrain)
    tracks, machines, frames = build(ground, True, dt)
    base, _ = site_checks(ground, tracks, frames, machines, dt)
    fr = frames[0]
    on_loop = next(i for i in range(len(fr["x"])) if abs(fr["z"][i] - 36.0) < 0.5 and fr["x"][i] > 5.0)
    # the straight leg along z = 26.5 heading west, level ground: a prop d metres off it north is d from the truck's line
    leg = [i for i in range(len(fr["x"])) if abs(fr["x"][i] - 5.0) <= 2.5 and abs(fr["z"][i] - 26.5) < 0.5]
    z_near = max(float(fr["z"][i]) for i in leg)
    leg_wide = [i for i in range(len(fr["x"])) if abs(fr["x"][i] - 5.0) < 12.0 and abs(fr["z"][i] - 26.5) < 0.5]
    cases = []

    def case(name, edit, expect, fr_edit=None):
        g = copy.deepcopy(ground)
        edit(g)
        f2 = frames
        if fr_edit is not None:
            f2 = [dict(f) for f in frames]
            f2[0] = fr_edit(frames[0])
        d, _ = site_checks(g, tracks, f2, machines, dt, with_room=False)
        new = [x for x in d if x not in base]
        ok = any(expect in x for x in new) if expect else not new
        cases.append((name, ok, new))

    def tower(g, x, z):
        g.f["props"].append(dict(p="light_tower", x=x, z=z, heading=0.0))

    def tower_beside(d):
        return lambda g: tower(g, 5.0, z_near + d + 1.7)

    def hill(slope):
        # a hill along the leg: climbs at `slope` for 14 m from x = -12 and falls over the next 14 m, over the rows round z = 26.5
        def edit(g):
            xs = g.x0 + np.arange(g.n) * g.cell
            zs = g.z0 + np.arange(g.n) * g.cell
            u = xs - (-12.0)
            prof = np.where((u > 0) & (u < 28.0), np.where(u <= 14.0, slope * u, slope * (28.0 - u)), 0.0)
            rows = (zs > 22.5) & (zs < 30.5)
            g.h[np.ix_(rows, np.ones(g.n, bool))] += prof[None, :]
        return edit

    def pile_beside(g):
        g.f["piles"].append(dict(name="a-pile", x=5.0, z=z_near + 3.0 + 4.0, h=4.0 * math.tan(math.radians(37.0)) / 1.3, heading=90.0, len=20.0))

    def leg_only(f):
        # the loop's straight leg alone: the return leg 9 m north of it would be within reach of a prop standing between them
        return {k: v[min(leg_wide):max(leg_wide) + 1] for k, v in f.items()}

    def ahead_only(f):
        # the loop up to a frame on the leg, and a tower 5.0 m beyond its front: the point is 1.8 m clear of the reach, the front deck 0.7 m in
        i = leg[len(leg) // 2]
        return {k: v[:i + 1] for k, v in f.items()}

    at = leg[len(leg) // 2]
    ax, az = float(fr["x"][at]), float(fr["z"][at])
    case("a light tower on the loop", lambda g: tower(g, float(fr["x"][on_loop]), float(fr["z"][on_loop])), "passes light_tower")
    case("a tower 3.1 m off the line (the reach, 3.2 m)", tower_beside(3.1), "passes light_tower", leg_only)
    case("a tower 3.3 m off the line (control)", tower_beside(3.3), None, leg_only)
    case("a tower 2.95 m off the line (the air kept)", tower_beside(2.95), "footprint is", leg_only)
    case("a tower ahead on the front deck's corner", lambda g: tower(g, ax - 5.0 - 2.5, az), "footprint is", fr_edit=ahead_only)
    case("a pile beside the loop", pile_beside, "a-pile", leg_only)
    case("a climb of 12.6 % (over the limit)", hill(0.126), "the haul loop climbs")
    case("a climb of about 11.4 % (under it, control)", hill(0.105), None)
    # the stand-room control: a zone 16 m by 8 m in the fill pad's southern end, between two piles' edges: 7.7 m apart is a hauler
    # and 1.0 m each side, short of 1.5 m of air; 9.8 m apart it fits
    rect = [84.0, 100.0, -86.0, -78.0]

    def room(gap):
        g = copy.deepcopy(ground)
        for zc in (-82.2 - gap / 2.0 - 1.0, -82.2 + gap / 2.0 + 1.0):         # two piles 1 m in radius, their edges `gap` apart
            g.f["piles"].append(dict(name="edge", x=92.0, z=zc, h=1.0 * math.tan(math.radians(37.0)) / 1.3, heading=90.0, len=24.0))
        r = stand_room(g, tracks, frames, zones=[dict(token="control", rect=rect, corner=0.0)], kinds=("Hauler",))["control"]["Hauler"]
        return r

    cases.append(("a gap 1.0 m each side of a hauler", room(7.7) is None, [] if room(7.7) is None else ["found a stand"]))
    cases.append(("a gap 2.05 m each side of it (control)", room(9.8) is not None, []))
    bad = False
    print("site checks on the real site: %s" % ("clean" if not base else "%d defects" % len(base)))
    for name, ok, new in cases:
        print("  %-48s %s%s" % (name, "ok" if ok else "NOT CAUGHT", "" if ok or not new else " " + "; ".join(new)))
        bad |= not ok
    caught = sum(1 for _, ok, _ in cases if ok)
    print("selftest: %d of %d controls behaved" % (caught, len(cases)))
    if base or bad or len(cases) != CONTROLS or caught != CONTROLS:
        sys.exit(2)


def main():
    default_out = os.path.join(HERE, "..", "..", "Assets", "Sitepulse", "Data", "quarry_fleet.json")
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", nargs="?", choices=("generate", "check", "selftest"), default="generate",
                    help="check: build both fleets in memory, run every check, write nothing; "
                         "selftest: show each check failing on a site made to break it")
    ap.add_argument("--terrain", default=os.path.join(HERE, "..", "..", "Assets", "Sitepulse", "Art", "Terrain"))
    ap.add_argument("--out", default=default_out)
    ap.add_argument("--live", action="store_true",
                    help="six trucks on the loop, no scripted refuel visit (writes quarry_fleet_live.json)")
    ap.add_argument("--dt", type=float, default=0.25)
    ap.add_argument("--preview", default=None)
    a = ap.parse_args()
    if a.live and a.out == default_out:
        a.out = os.path.join(os.path.dirname(default_out), "quarry_fleet_live.json")
    if a.mode == "selftest":
        selftest(a.terrain, a.dt)
        return
    ground = Ground(a.terrain)
    modes = (False, True) if a.mode == "check" else (a.live,)
    failed = False
    for live in modes:
        tracks, machines, frames = build(ground, live, a.dt)
        n_loop = 6 if live else HAULERS_ON_LOOP
        horizon = max(tr["period"] for tr in tracks) * 2
        worst, worst_hl = check(tracks, machines, frames, a.dt, horizon)
        defects, report = site_checks(ground, tracks, frames, machines, a.dt, with_room=True)
        out = dict(generator="ArtSource/terrain/quarry_fleet.py", dt=a.dt, channels=CHANNELS, tracks=[], machines=machines)
        for tr, fr in zip(tracks, frames):
            data = np.stack([fr["x"], fr["z"], fr["heading"], fr["travel"], fr["p1"], fr["p2"], fr["steer"],
                             fr["flag"].astype(float)], 1)
            out["tracks"].append(dict(kind=tr["kind"], period=round(tr["period"], 3),
                                      data=[round(float(v), 3) for v in data.ravel()]))
        if a.mode != "check":
            os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)
            with open(a.out, "w", newline="\n") as f:          # LF on every platform: the committed file is the same bytes
                json.dump(out, f, separators=(",", ":"))
                f.write("\n")
        print("== %s fleet" % ("live" if live else "preview"))
        print("haul loop %.3f s, truck spacing %.3f s" % (tracks[0]["period"], tracks[0]["period"] / n_loop))
        for i, tr in enumerate(tracks):
            print("track %2d %-6s period %6.1f s" % (i, tr["kind"], tr["period"]))
        print("closest approach %.2f m (%s)" % worst)
        print("closest haul trucks %.2f m (%s)" % worst_hl)
        for line in report:
            print(line)
        for d in defects:
            print("DEFECT: " + d)
        if a.preview and a.mode != "check":
            preview(ground, tracks, frames, a.preview)
        if worst[0] < 0.0 or worst_hl[0] < HAULER_CLEARANCE or defects:
            failed = True
    if failed:
        sys.exit(2)


def preview(ground, tracks, frames, path):
    from PIL import Image, ImageDraw
    W, S = 1600, 6.0
    img = Image.new("RGB", (W, int(W * 0.75)), (40, 40, 40))
    d = ImageDraw.Draw(img)
    def px(x, z):
        return (W / 2 + x * S, img.height / 2 - z * S + 60)
    for rd in ground.f["roads"]:
        d.line([px(p[0], p[2]) for p in rd["points"]], fill=(90, 90, 90), width=int(rd["width"] * S))
    cols = {"Hauler": (255, 200, 0), "Loader": (0, 200, 255), "Dozer": (255, 90, 60)}
    for tr, fr in zip(tracks, frames):
        d.line([px(x, z) for x, z in zip(fr["x"], fr["z"])], fill=cols[tr["kind"]], width=1)
    img.save(path)


if __name__ == "__main__":
    main()
