# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Preview choreography for the 18 Sitepulse machines on the quarry terrain.

    python3 quarry_fleet.py [--terrain DIR] [--out FILE] [--dt S] [--preview PNG]

Requires Python 3.8+ and numpy (Pillow only for --preview). Run quarry_heightmap.py first: this
reads its heightmap and feature file to grade speeds by slope. Deterministic.

This is LOCAL VISUAL SIMULATION ONLY. It gives the scene something believable to show and the
benchmark something realistic to draw; it is not the site simulation, which drives machines
from commands, and none of it is platform data.

WHAT IT MAKES
  * 5 haulers on one closed haul loop, spaced a fifth of the loop apart in time: load at the
    muck pile in the pit, up the ramp, out to the dump pad, reverse to the tipping edge and
    dump, back along the return road, through the yard past the refuel bay, and down the ramp.
    Traffic keeps LEFT, each direction on its own lane of the 20 m ramp, which lets the loop
    run without crossing itself.
  * a 6th hauler out of the loop: from its parking place to the refuel bay (a lay-by beside the
    yard's through lane), a stop while it is fuelled, and back to park.
  * 6 loaders: one loads the haulers from the loading bench, PASSES buckets per truck (its
    cycle is exactly the haulers' spacing, timed so the buckets tip while a truck is standing
    at the load point);
    one rehandles the pit stockpile; at the plant one feeds the crusher's hopper from the feed
    stockpile and one works the product stockpile; two are parked (one in the workshop).
  * 6 dozers: one pushing up the muck pile at the toe of the north face, one ripping the pit
    floor, one spreading on the dump pad, and three parked in the yard.
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
  trucks come within HAULER_CLEARANCE metres of each other. A haul truck's footprint is two
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
LANE = 5.0                      # lane centre offset from the ramp's centreline (20 m ramp, keep left)


def ramp_lane(ground, side, x_from=-14.0):
    """The ramp centreline from x_from to the top, offset LANE metres to the left (side=+1) or
    right (side=-1) of the uphill direction, as (x, z) points about 8 m apart."""
    rd = next(r for r in ground.f["roads"] if r["name"] == "pit-ramp")
    pts = np.array([(p[0], p[2]) for p in rd["points"]])
    i0 = int(np.argmax(pts[:, 0] >= x_from))
    pts = pts[i0:]
    d = np.gradient(pts, axis=0)
    d /= np.linalg.norm(d, axis=1, keepdims=True)
    left = np.stack([-d[:, 1], d[:, 0]], 1)
    lane = pts + left * LANE * side
    keep = [0]
    for i in range(1, len(lane)):
        if np.linalg.norm(lane[i] - lane[keep[-1]]) >= 8.0 or i == len(lane) - 1:
            keep.append(i)
    return [tuple(map(float, lane[i])) for i in keep]


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
            (-14.0, 26.5), (-19.5, 22.5), (-18.0, 17.0)] + up[1:]
           + [(80.5, -20.0), (88.0, -31.0), (93.5, -39.0), (90.0, -45.0), (86.0, -49.5), (80.0, -51.0)])
    # 2: reverse to the tipping edge
    back = [(80.0, -51.0), (101.0, -51.0)]
    # 3: empty, back along the return road, through the yard past the refuel bay, down the ramp,
    #    round to the load point
    home = ([(101.0, -51.0), (94.0, -53.0), (89.0, -58.0), (84.0, -62.0), (79.0, -67.0), (60.0, -70.0),
             (30.0, -68.0), (0.0, -64.0), (-30.0, -62.0), (-44.0, -61.5), (-47.5, -58.0), (-49.0, -52.0),
             (-49.0, -40.0), (-44.0, -30.5), (-10.0, -30.0), (30.0, -26.0), (52.0, -21.5), (61.0, -14.0)]
            + down[:-1] + [(-17.0, 6.0), (-31.0, 21.0), (-29.0, 31.0), (-24.0, 36.5), (-16.0, 36.0),
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
    to_park = [(-59.5, -56.5), (-59.5, -51.0), (-62.5, -47.0), (-68.0, -46.8), (-76.0, -47.0), (-80.5, -50.0),
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


def fleet(ground):
    tracks, machines = [], []

    def add_track(kind, runs, period=None):
        tr = plan(kind, runs, ground)
        T = period or tr["t"][-1]
        tracks.append(dict(kind=kind, raw=tr, period=float(T)))
        return len(tracks) - 1

    # haulers on the loop
    hl = add_track("Hauler", haul_runs(ground))
    T = tracks[hl]["period"]
    load_start = T - LOAD_STOP                        # the load stop ends the loop
    gap = T / HAULERS_ON_LOOP
    for k in range(HAULERS_ON_LOOP):
        machines.append(dict(id=f"SP-HL-{k + 1:04d}", kind="Hauler", track=hl, offset=round(-k * gap, 3)))
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
    li2 = add_track("Loader", loader_rehandle((38.0, 40.5), (42.0, 42.5)))
    machines.append(dict(id="SP-LD-0002", kind="Loader", track=li2, offset=3.0))
    # the plant: one loader feeds the crusher's hopper from the feed stockpile...
    feed = ground.f["spots"]["plant-feed"]
    li3 = add_track("Loader", loader_v((-51.5, -90.0), (-53.0, -99.0), (feed["x"], feed["z"])))
    machines.append(dict(id="SP-LD-0003", kind="Loader", track=li3, offset=0.0))
    # ...and one works the product stockpile under the stacker
    li4 = add_track("Loader", loader_rehandle((6.9, -114.5), (11.1, -110.3)))
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
        ((45.0, 28.0), (57.0, 28.0), True),
        ((90.0, -70.0), (100.0, -69.0), False),
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


HAULER_CLEARANCE = 3.0          # metres two haul trucks must keep apart, passing or queueing
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--terrain", default=os.path.join(HERE, "..", "..", "Assets", "Sitepulse", "Art", "Terrain"))
    ap.add_argument("--out", default=os.path.join(HERE, "..", "..", "Assets", "Sitepulse", "Data", "quarry_fleet.json"))
    ap.add_argument("--dt", type=float, default=0.25)
    ap.add_argument("--preview", default=None)
    a = ap.parse_args()
    ground = Ground(a.terrain)
    tracks, machines = fleet(ground)
    frames = []
    for tr in tracks:
        fr = resample(tr["kind"], tr["raw"], a.dt, tr["period"])
        if tr["kind"] == "Hauler" and "load_from" in tr:
            tg = np.arange(0.0, tr["period"], a.dt)
            # carrying from the moment the loader's bucket tips until the dump: the load stop is
            # the end of the loop, so the tipped load shows for its last few seconds too
            fr["flag"] = fr["flag"] | (tg >= tr["load_from"])
        frames.append(fr)
    horizon = max(tr["period"] for tr in tracks) * 2
    worst, worst_hl = check(tracks, machines, frames, a.dt, horizon)
    out = dict(generator="ArtSource/terrain/quarry_fleet.py", dt=a.dt, channels=CHANNELS, tracks=[], machines=machines)
    for tr, fr in zip(tracks, frames):
        data = np.stack([fr["x"], fr["z"], fr["heading"], fr["travel"], fr["p1"], fr["p2"], fr["steer"],
                         fr["flag"].astype(float)], 1)
        out["tracks"].append(dict(kind=tr["kind"], period=round(tr["period"], 3),
                                  data=[round(float(v), 3) for v in data.ravel()]))
    os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)
    with open(a.out, "w") as f:
        json.dump(out, f, separators=(",", ":"))
        f.write("\n")
    print("haul loop %.1f s, truck spacing %.1f s" % (tracks[0]["period"], tracks[0]["period"] / HAULERS_ON_LOOP))
    for i, tr in enumerate(tracks):
        print("track %2d %-6s period %6.1f s" % (i, tr["kind"], tr["period"]))
    print("closest approach %.2f m (%s)" % worst)
    print("closest haul trucks %.2f m (%s)" % worst_hl)
    if a.preview:
        preview(ground, tracks, frames, a.preview)
    if worst[0] < 0.0 or worst_hl[0] < HAULER_CLEARANCE:
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
