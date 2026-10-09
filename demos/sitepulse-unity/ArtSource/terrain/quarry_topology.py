# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The Sitepulse quarry as an interlocking needs it: directed lanes cut into cells, junction boxes,
stand places with their access lanes, a drive-through refuel bay and the timed stations, with the
checks that say the description is sound.

    python3 quarry_topology.py [generate|check|selftest] [--terrain DIR] [--data DIR] [--out FILE] [--verbose]

    generate (default) writes ../../Assets/Sitepulse/Data/quarry_topology.json (and exits 2, writing
    nothing, when a check fails); check builds it in memory and runs every check on it and on the
    committed file; selftest shows each check failing on a site made to break it.

Requires Python 3.8+ and numpy. Deterministic. Reads the four files the site is generated into
(quarry_features.json, quarry_height.bytes, quarry_fleet_live.json, quarry_fleet.json) and writes
nothing but its own file: the topology is downstream of the choreography, because the checks need
the fleets' sweeps, and it records the SHA-256 of what it was made from.

WHAT IT HOLDS (see Data/README.md)
  lanes      the haul loop (cut from track 0 of the live fleet, each cell with the track time its start
             is reached), the lane of each wide road the loop does not drive, and the refuel bay's three.
             A cell is CELL_M of the line a machine drives. Capacities are counted in slots (SLOT_M: a
             hauler's length and the air haul trucks keep), whatever the cell length.
  junctions  one box per cluster of conflicting cells nothing else covers: a convex polygon, its member
             cells, the approach cell where a requester waits and the room a granted machine needs.
             Two cells of different lanes conflict when their swept hauler footprints come within
             CONFLICT_M. A diverge (the first cells of an access lane, which share ground with the lane
             it leaves) and the lanes through one stand (within a slot of each other) are not boxes.
  stands     none in the zones (see ZONES); the refuel queue and bay, each with a lane to and from it.
  stations   the load point and the dump pad: core, exit buffer, capacity, window, headway.
  work_areas the hull of every other machine's footprint over its whole track.

THE CHECKS (V1-V9) run here, on the file as written, and again in C# (Scripts/Tasks/Traffic,
read back by the EditMode tests) with the same thresholds.

THIS DESCRIBES A SITE. NOTHING READS IT AT RUN TIME YET.
"""
import argparse
import hashlib
import json
import math
import os
import sys

import numpy as np

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import quarry_fleet as qf                                    # noqa: E402
from quarry_heightmap import catmull_rom                     # noqa: E402

CELL_M = 2.0                    # a lane is cut every CELL_M along the line a machine drives (its offset line, not the road's centreline)
# a hauler's length and the air two haul trucks keep apart make a SLOT: capacities (the cycle, a span's complement, a
# buffer) are counted in slots, whatever the cell length is
HAULER_LENGTH = qf.FOOT["Hauler"][1] + qf.FOOT["Hauler"][2]
SLOT_M = round(HAULER_LENGTH + qf.HAULER_CLEARANCE, 3)
CONFLICT_M = 0.3                # two cells of different lanes whose swept footprints come nearer than this conflict. Not
                                # HAULER_CLEARANCE: the two lanes of a 12.5 m road are 1.1 m apart at the least offset and
                                # must not conflict (an opposing machine never holds), while 0.3 m is the air the model checker's
                                # conflict table kept
STAND_CLEAR_M = qf.STAND_AIR    # a stand's footprint keeps this from every lane, sweep, box and obstacle
WORK_CLEAR_M = 1.5              # and a lane keeps it from another machine's work area
GRADE_MAX = qf.GRADE_MAX_PCT
TRAVEL_REACH = qf.TRAVEL_RADIUS["Hauler"] + qf.TRAVEL_CLEARANCE    # 3.2: a lane keeps this from an outline


# ==================================================================================
# inputs
# ==================================================================================
def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def terrain_files(terrain):
    return os.path.join(terrain, "quarry_features.json"), os.path.join(terrain, "quarry_height.bytes")


def fleet_files(data):
    return os.path.join(data, "quarry_fleet_live.json"), os.path.join(data, "quarry_fleet.json")


def load_fleet(path):
    """A fleet file as arrays: {dt, machines, tracks: [{kind, period, x, z, heading, p1}]}."""
    with open(path) as f:
        raw = json.load(f)
    tracks = []
    for t in raw["tracks"]:
        a = np.array(t["data"], float).reshape(-1, len(raw["channels"]))
        tracks.append(dict(kind=t["kind"], period=t["period"], x=a[:, 0], z=a[:, 1], heading=a[:, 2], p1=a[:, 4]))
    return dict(dt=raw["dt"], machines=raw["machines"], tracks=tracks)


def pose_at(track, dt, t):
    """The pose of a track at time t (it loops): x, z, heading, by linear interpolation of the frames."""
    n = len(track["x"])
    u = (t % track["period"]) / dt
    i = int(u) % n
    j = (i + 1) % n
    f = u - int(u)
    x = track["x"][i] + (track["x"][j] - track["x"][i]) * f
    z = track["z"][i] + (track["z"][j] - track["z"][i]) * f
    dh = qf.wrap(track["heading"][j] - track["heading"][i])
    return float(x), float(z), float((track["heading"][i] + dh * f) % 360.0)


def frame_at(track, dt, t):
    """The frame of a track at time t, as the choreography is generated and checked (no interpolation): x, z, heading, boom."""
    i = int((t % track["period"]) / dt) % len(track["x"])
    return float(track["x"][i]), float(track["z"][i]), float(track["heading"][i]), float(track["p1"][i])


class Inputs:
    """Everything the topology is built from and checked against."""

    def __init__(self, terrain, data):
        self.terrain, self.data = terrain, data
        self.features_path, self.heights_path = terrain_files(terrain)
        self.live_path, self.preview_path = fleet_files(data)
        self.ground = qf.Ground(terrain)
        self.feats = self.ground.f
        self.live = load_fleet(self.live_path)
        self.preview = load_fleet(self.preview_path)
        self.obstacles = qf.site_obstacles(self.feats)

    def hashes(self):
        return dict(features=sha256(self.features_path), heights=sha256(self.heights_path),
                    fleet=sha256(self.live_path), fleet_preview=sha256(self.preview_path))


# ==================================================================================
# geometry: convex polygons as lists of (x, z)
# ==================================================================================
def boxes(kind, x, z, heading):
    """The footprint of a machine at a pose: its boxes, each four (x, z) corners (quarry_fleet.corners)."""
    return [[(float(c[0]), float(c[1])) for c in b] for b in qf.corners(kind, x, z, heading)]


def sat_gap(a, b):
    """Separation along the best axis between two convex polygons (negative = overlapping): quarry_fleet.sat_gap for any corner count."""
    best = -1e9
    for poly in (a, b):
        n = len(poly)
        for i in range(n):
            ex, ez = poly[(i + 1) % n][0] - poly[i][0], poly[(i + 1) % n][1] - poly[i][1]
            ln = math.hypot(ex, ez) + 1e-9
            nx, nz = -ez / ln, ex / ln
            pa = [nx * p[0] + nz * p[1] for p in a]
            pb = [nx * p[0] + nz * p[1] for p in b]
            g = max(min(pb) - max(pa), min(pa) - max(pb))
            if g > best:
                best = g
    return best


def group_gap(ga, gb):
    """The smallest separation between two groups of convex polygons."""
    return min(sat_gap(p, q) for p in ga for q in gb)


def center_radius(group):
    pts = [p for poly in group for p in poly]
    cx = sum(p[0] for p in pts) / len(pts)
    cz = sum(p[1] for p in pts) / len(pts)
    return cx, cz, max(math.hypot(p[0] - cx, p[1] - cz) for p in pts)


def hull(points):
    """Convex hull (Andrew's monotone chain), counter-clockwise, as a list of (x, z)."""
    pts = sorted(set((round(float(p[0]), 3), round(float(p[1]), 3)) for p in points))
    if len(pts) < 3:
        return pts

    def cross(o, a, b):
        return (a[0] - o[0]) * (b[1] - o[1]) - (a[1] - o[1]) * (b[0] - o[0])
    lo, up = [], []
    for p in pts:
        while len(lo) >= 2 and cross(lo[-2], lo[-1], p) <= 0:
            lo.pop()
        lo.append(p)
    for p in reversed(pts):
        while len(up) >= 2 and cross(up[-2], up[-1], p) <= 0:
            up.pop()
        up.append(p)
    return lo[:-1] + up[:-1]


# ==================================================================================
# lanes
# ==================================================================================
class Lane:
    """A directed lane: its cells in travel order, [x0, z0, x1, z1] (the loop's also carry t0, the track time at the cell's start)."""

    def __init__(self, lid, kind, cells, cyclic=False, **meta):
        self.id, self.kind, self.cells, self.cyclic = lid, kind, cells, cyclic
        self.meta = meta
        self._boxes = {}

    def __len__(self):
        return len(self.cells)

    def length(self):
        return sum(math.hypot(c[2] - c[0], c[3] - c[1]) for c in self.cells)


def cut_cells(pts, cell=CELL_M):
    """A polyline (N x 2) cut every `cell` metres of arc: [[x0, z0, x1, z1], ...]; the last cell is the remainder."""
    pts = np.asarray(pts, float)
    seg = np.hypot(np.diff(pts[:, 0]), np.diff(pts[:, 1]))
    keep = np.concatenate([[True], seg > 1e-9])
    pts = pts[keep]
    s = np.r_[0.0, np.cumsum(np.hypot(np.diff(pts[:, 0]), np.diff(pts[:, 1])))]
    total = float(s[-1])
    n = max(1, int(math.ceil(total / cell - 1e-9)))
    edges = np.minimum(np.arange(n + 1) * cell, total)
    xs, zs = np.interp(edges, s, pts[:, 0]), np.interp(edges, s, pts[:, 1])
    return [[float(xs[k]), float(zs[k]), float(xs[k + 1]), float(zs[k + 1])] for k in range(n)]


def smooth(points, step=0.5):
    """The lane line through authored (x, z) points: the centripetal Catmull-Rom the choreography's routes are drawn with."""
    poly, _ = catmull_rom([tuple(p) for p in points], step=step)
    return poly


def loop_lane(inputs):
    """The haul loop as a cyclic lane, cut from track 0 of the live fleet: each cell carries the track time its start is reached."""
    fl = inputs.live
    tr = fl["tracks"][0]
    x, z, dt = tr["x"], tr["z"], fl["dt"]
    d = np.hypot(np.diff(x), np.diff(z))
    s = np.r_[0.0, np.cumsum(d)]
    total = float(s[-1])
    n = int(math.ceil(total / CELL_M - 1e-9))
    edges = np.minimum(np.arange(n + 1) * CELL_M, total)
    cells = []
    for k in range(n):
        a, b = edges[k], edges[k + 1]
        # the earliest time the track has travelled `a` (a stop is the end of the cell it stands at)
        i = int(np.searchsorted(s, a, side="left"))
        t0 = 0.0 if i == 0 else (i - 1 + (a - s[i - 1]) / (s[i] - s[i - 1])) * dt
        cells.append([float(np.interp(a, s, x)), float(np.interp(a, s, z)), float(np.interp(b, s, x)), float(np.interp(b, s, z)), float(t0)])
    return Lane("loop", "loop", cells, cyclic=True, track=0)


def road_lane_line(ground, name, reverse=False, fade_end=0.0):
    """The lane of one direction of a road, as the choreography draws it (quarry_fleet.road_lane) but for either end easing to the
    centreline: the lane keeps left of the way it is driven, never nearer than MIN_LANE, eased where it would climb past the grade
    limit. `fade_end` eases the offset out to the road's centreline over that many metres at the far end (a road that ends on open ground)."""
    rd = next(r for r in ground.f["roads"] if r["name"] == name)
    pts = np.array([(p[0], p[2]) for p in rd["points"]])
    if reverse:
        pts = pts[::-1]
    d = np.gradient(pts, axis=0)
    d /= np.linalg.norm(d, axis=1, keepdims=True)
    left = np.stack([-d[:, 1], d[:, 0]], 1)
    along = np.r_[0.0, np.cumsum(np.linalg.norm(np.diff(pts, axis=0), axis=1))]
    k = np.minimum(1.0, (along[-1] - along) / fade_end) if fade_end > 0 else np.ones(len(pts))
    off = qf.eased_offsets(ground, pts, left, qf.lane_offset(rd["width"]) * k)
    return qf._thin(pts + left * off[:, None])


# ==================================================================================
# the ground a cell sweeps
# ==================================================================================
def _dedupe(poses):
    seen, out = set(), []
    for p in poses:
        key = (round(p[0], 2), round(p[1], 2), round(p[2] / 1.0))
        if key not in seen:
            seen.add(key)
            out.append(p)
    return out


def cell_poses(lane, k, inputs):
    """The poses a hauler takes crossing cell k: on the loop, every frame of the track from the cell's start to the next cell's
    (the whole standing stop included, the heading it turns through too); on any other lane, its two ends facing the way the cell points."""
    c = lane.cells[k]
    if lane.kind == "loop":
        fl = inputs.live
        tr = fl["tracks"][0]
        dt = fl["dt"]
        t0 = c[4]
        t1 = lane.cells[k + 1][4] if k + 1 < len(lane.cells) else tr["period"]
        poses = [pose_at(tr, dt, t0)]
        i = int(math.floor(t0 / dt)) + 1
        while i * dt < t1 - 1e-9:
            poses.append(pose_at(tr, dt, i * dt))
            i += 1
        poses.append(pose_at(tr, dt, min(t1, tr["period"] - 1e-6)))
        return _dedupe(poses)
    h = qf.heading_of(c[2] - c[0], c[3] - c[1])
    return [(c[0], c[1], h), (c[2], c[3], h)]


def cell_group(lane, k, inputs):
    """The swept footprint of cell k: the hauler's boxes at each pose of cell_poses. Cached on the lane."""
    g = lane._boxes.get(k)
    if g is None:
        g = [b for p in cell_poses(lane, k, inputs) for b in boxes("Hauler", *p)]
        lane._boxes[k] = g
    return g


def conflicts(lanes, inputs, only=None, threshold=CONFLICT_M):
    """Every pair of cells of two different lanes whose swept footprints come nearer than `threshold`: {((lane, k), (lane, k)): gap}.
    `only` limits the pairs to those with at least one cell on a lane of that set."""
    items = []
    for lane in lanes:
        for k in range(len(lane)):
            g = cell_group(lane, k, inputs)
            items.append((lane.id, k, g) + center_radius(g))
    grid = {}
    for n, it in enumerate(items):
        grid.setdefault((int(it[3] // 30), int(it[4] // 30)), []).append(n)
    out = {}
    for n, it in enumerate(items):
        gx, gz = int(it[3] // 30), int(it[4] // 30)
        for dx in (-1, 0, 1):
            for dz in (-1, 0, 1):
                for m in grid.get((gx + dx, gz + dz), ()):
                    if m <= n:
                        continue
                    jt = items[m]
                    if it[0] == jt[0]:
                        continue
                    if only is not None and it[0] not in only and jt[0] not in only:
                        continue
                    if math.hypot(it[3] - jt[3], it[4] - jt[4]) > it[5] + jt[5] + threshold:
                        continue
                    gap = group_gap(it[2], jt[2])
                    if gap < threshold:
                        out[((it[0], it[1]), (jt[0], jt[1]))] = gap
    return out


# ==================================================================================
# authored lane geometry: a drive program, straights and turns of one radius
# ==================================================================================
TURN_M = 4.5                    # the radius every authored turn is driven at; the haul loop itself turns as tight as 3 m at the dump pad's cusp
END_TOLERANCE_M = 0.15          # a program ends this near the pose it was written to reach, and within END_TOLERANCE_DEG of its heading
END_TOLERANCE_DEG = 1.5


def drive(pose, program, step=0.25):
    """Where a machine goes driving `program` from `pose` (x, z, heading degrees clockwise from north): [("S", metres), ("L", degrees),
    ("R", degrees)], turns of TURN_M. Returns the line (N x 2) and the pose it ends in."""
    x, z, h = pose
    th = math.radians(90.0 - h)                                      # the maths angle: anticlockwise from east
    pts = [(x, z)]
    for op, v in program:
        if op == "S":
            n = max(1, int(math.ceil(v / step)))
            for _ in range(n):
                x += v / n * math.cos(th)
                z += v / n * math.sin(th)
                pts.append((x, z))
        elif op in ("L", "R"):
            sign = 1.0 if op == "L" else -1.0
            ang = math.radians(v)
            n = max(1, int(math.ceil(ang * TURN_M / step)))
            for _ in range(n):
                d = ang / n * sign
                x += TURN_M * (math.sin(th + d) - math.sin(th)) * sign
                z += -TURN_M * (math.cos(th + d) - math.cos(th)) * sign
                th += d
                pts.append((x, z))
        else:
            raise SystemExit("unknown drive op %r" % (op,))
    return np.array(pts), (x, z, (90.0 - math.degrees(th)) % 360.0)


def parent_pose(lane, cell):
    """Where a lane leaving or joining `lane` does so: the end of `cell`, facing the way the cell points."""
    c = lane.cells[cell]
    return (c[2], c[3], qf.heading_of(c[2] - c[0], c[3] - c[1]))


def nearest_cell(lane, x, z):
    """The cell of `lane` whose end is nearest (x, z)."""
    return min(range(len(lane)), key=lambda k: (lane.cells[k][2] - x) ** 2 + (lane.cells[k][3] - z) ** 2)


def program_lane(lane_id, start, program, target, what):
    """The lane driven by `program` from `start`; it must end at `target` (a pose), which is how a lane stays tied to what it joins."""
    line, end = drive(start, program)
    miss = math.hypot(end[0] - target[0], end[1] - target[1])
    turn = abs(qf.wrap(end[2] - target[2]))
    if miss > END_TOLERANCE_M or turn > END_TOLERANCE_DEG:
        raise SystemExit("%s: its drive program ends %.2f m and %.1f degrees from %s (%.1f, %.1f, %.0f); redraw it"
                         % (lane_id, miss, turn, what, target[0], target[1], target[2]))
    return Lane(lane_id, "access", cut_cells(line))


# ==================================================================================
# work areas: what every other machine sweeps
# ==================================================================================
def work_areas(inputs):
    """The ground each machine off the haul loop works: the hull of its footprint over a whole period of its track (live fleet,
    machine order), as [(machine id, polygon)]. A parked machine's is its own footprint."""
    fl = inputs.live
    out = []
    owner = {}
    for m in fl["machines"]:
        owner.setdefault(m["track"], m["id"])
    for ti, tr in enumerate(fl["tracks"]):
        if ti == 0:
            continue
        pts = []
        last = None
        for i in range(len(tr["x"])):
            x, z, hd = float(tr["x"][i]), float(tr["z"][i]), float(tr["heading"][i])
            if last and math.hypot(x - last[0], z - last[1]) < 0.2 and abs(qf.wrap(hd - last[2])) < 2.0:
                continue
            last = (x, z, hd)
            for b in qf.corners(tr["kind"], x, z, hd, float(tr["p1"][i])):
                pts.extend((float(c[0]), float(c[1])) for c in b)
        out.append((owner[ti], hull(pts)))
    return out


# ==================================================================================
# the site: what is authored
# ==================================================================================
# ZONES: the kinds of machine `goto-area` may send to each zone, and so the kinds each must have a stand for. A hauler cannot be
# sent to the cut: its only room to stand is where no lane reaches it (see the stands below).
# ZONES: the kinds of machine `goto-area` may send to each zone, and so the kinds each must have a stand for. None has one: the stand
# places the choreography leaves room for (ArtSource/terrain/quarry_fleet.py stand_room) are places to stand, and no lane reaches and
# leaves one by the reach and the clearance a hauler keeps (3.2 m from every outline, 1.5 m from every machine's work area), without
# the two lanes of one stand running over each other. The generator's own search over the free ground finds the refuel bay a way and
# finds no stand in the cut, the fill or the yard a way.
ZONES = [
    ("sp-zone-cut", ()),
    ("sp-zone-fill", ()),
    ("sp-zone-yard", ()),
]

# STANDS: where a machine may be left standing, and the lanes it takes to get there and back. Authored, because where a machine is
# put down is a decision about the site. `pose` is where it stands (x, z, heading). Its `in` lane leaves the haul loop at the cell whose
# end is nearest `exit` and is driven by `program_in`, which ends at the pose; its `out` lane starts at the pose, is driven by
# `program_out` and ends on the loop at the cell nearest `join`. A program is straights (metres) and turns (degrees) of TURN_M.
# A stand is driven through, never backed out of. (None today: see ZONES.)
STANDS = []

# BAY: the refuel bay is a drive-through: in from the loop to the queue stand, a short hop to the bay stand, out to the loop. Its lanes are
# drawn as a stand's are (see STANDS); the hop is a straight from the queue to the bay.
BAY = dict(queue=("refuel-queue", (-59.5, -64.0, 0.0)), bay=("refuel-bay", (-59.5, -56.5, 0.0)),
           exit=(-42.2, -65.6), program_in=[("R", 1.29), ("S", 0.28), ("R", 3.73), ("L", 45), ("R", 22.5), ("S", 2.5), ("R", 45), ("L", 22.5), ("R", 90)],
           program_hop=[("S", 7.5)],
           join=(-16.7, -26.9), program_out=[("S", 6), ("R", 22.5), ("S", 7.5), ("L", 22.5), ("S", 2.5), ("R", 22.5), ("S", 7.5), ("R", 67.5), ("S", 2.5), ("R", 22.5), ("L", 22.5), ("S", 25), ("R", 0.38), ("S", 0.6), ("L", 3.97)])

# EXEMPT: what a lane or a stand is held to is not everything standing: the refuel bay's own approach, tank and cones are what it
# is laid out between (quarry_fleet.WORKS_AT says the same of the scripted visit), and that visit's track is the bay's own.
EXEMPT = [
    dict(to=who, obstacle="refuel-approach", spot=None, radius_m=0.0)
    for who in ("access/bay/in", "access/bay/hop", "access/bay/out", "refuel-queue", "refuel-bay")
] + [
    dict(to=who, obstacle=ob, spot=(-59.5, -56.5), radius_m=14.0)
    for who, ob in (("refuel-queue", "fuel_tank"), ("refuel-queue", "cone"), ("refuel-bay", "fuel_tank"), ("refuel-bay", "cone"), ("access/bay/out", "cone"))
] + [dict(to=who, machine="SP-HL-0006") for who in ("refuel-queue", "refuel-bay")]
EXEMPT_ROWS = [dict(e, spot=list(e["spot"])) if e.get("spot") else e for e in EXEMPT]


# ==================================================================================
# building the topology
# ==================================================================================
ROAD_LANES = [                  # the lane the haul loop does NOT drive on each wide road: (id, road, against the file's order, fade-out m, trim m)
    ("road/fill-road/back", "fill-road", True, 0.0, 0.0),
    ("road/fill-return/east", "fill-return", True, 0.0, 8.0),       # it starts 8 m on, clear of the barrier line's end
    ("road/yard-road/west", "yard-road", True, 24.0, 0.0),
]
DIVERGE_WINDOW = 12             # cells either side of an exit that the lane leaving shares ground with
DIVERGE_MAX = 40                # and the most cells of that lane that may
ROOM_CELLS = int(math.ceil(SLOT_M / CELL_M))     # a granted machine needs a slot to stand in beyond a box
PLACES = [("ramp-top", (72.0, -13.0)), ("pad-gate", (91.0, -42.0)), ("pad-south", (81.0, -68.0)),
          ("yard-corner", (-46.0, -58.0)), ("yard-north", (-47.0, -36.0))]


def r3(v):
    return round(float(v), 3)


def round_lane(lane):
    """The lane as it is written: every number to a millimetre. Everything derived afterwards reads these, so the file and the checks agree."""
    lane.cells = [[r3(v) for v in c] for c in lane.cells]
    lane._boxes = {}
    return lane


def cyc_dist(a, b, n):
    d = abs(a - b)
    return min(d, n - d)


def runs_of(cells, n, cyclic):
    """The sorted cell indices as contiguous runs [a, b] (a run may wrap past the end of a cyclic lane: a > b)."""
    cs = sorted(set(cells))
    if not cs:
        return []
    runs, a, b = [], cs[0], cs[0]
    for c in cs[1:]:
        if c == b + 1:
            b = c
        else:
            runs.append([a, b])
            a = b = c
    runs.append([a, b])
    if cyclic and len(runs) > 1 and runs[0][0] == 0 and runs[-1][1] == n - 1:
        last = runs.pop()
        runs[0] = [last[0], runs[0][1]]
    return runs


def run_cells(run, n):
    a, b = run
    if a <= b:
        return list(range(a, b + 1))
    return list(range(a, n)) + list(range(0, b + 1))


def heading_of_cell(lane, k):
    c = lane.cells[k]
    return qf.heading_of(c[2] - c[0], c[3] - c[1])


def build_lanes(inputs):
    """Every lane of the site and the stands, exits and entries that tie the access lanes to the haul loop."""
    loop = round_lane(loop_lane(inputs))
    lanes = [loop]
    for lid, name, rev, fade, trim in ROAD_LANES:
        cells = cut_cells(smooth(road_lane_line(inputs.ground, name, rev, fade)))[int(round(trim / CELL_M)):]
        lanes.append(round_lane(Lane(lid, "road", cells, road=name)))
    stands, exits, entries = [], [], []
    pend = []
    for s in STANDS:
        e = nearest_cell(loop, *s["exit"])
        j = nearest_cell(loop, *s["join"])
        li = program_lane("access/%s/in" % s["id"], parent_pose(loop, e), s["program_in"], s["pose"], "its stand")
        lo = program_lane("access/%s/out" % s["id"], s["pose"], s["program_out"], parent_pose(loop, j), "the loop")
        lanes += [round_lane(li), round_lane(lo)]
        stands.append(dict(id=s["id"], zone=s["zone"], kinds=list(s["kinds"]), pose=[r3(v) for v in s["pose"]], **{"in": li.id, "out": lo.id}))
        exits.append(dict(id="exit-" + s["id"], lane="loop", cell=e, to=li.id))
        pend.append(dict(id="entry-" + s["id"], frm=lo.id, lane="loop", cell=j))
    bay = None
    if BAY is not None:
        e = nearest_cell(loop, *BAY["exit"])
        j = nearest_cell(loop, *BAY["join"])
        q, b = BAY["queue"], BAY["bay"]
        li = program_lane("access/bay/in", parent_pose(loop, e), BAY["program_in"], q[1], "the queue")
        hop = program_lane("access/bay/hop", q[1], BAY["program_hop"], b[1], "the bay")
        lo = program_lane("access/bay/out", b[1], BAY["program_out"], parent_pose(loop, j), "the loop")
        lanes += [round_lane(li), round_lane(hop), round_lane(lo)]
        stands.append(dict(id=q[0], zone="sp-zone-yard", kinds=["Hauler"], pose=[r3(v) for v in q[1]], **{"in": li.id, "out": hop.id}))
        stands.append(dict(id=b[0], zone="sp-zone-yard", kinds=["Hauler"], pose=[r3(v) for v in b[1]], **{"in": hop.id, "out": lo.id}))
        exits.append(dict(id="exit-bay", lane="loop", cell=e, to=li.id))
        pend.append(dict(id="entry-bay", frm=lo.id, lane="loop", cell=j))
        bay = {"queue": q[0], "bay": b[0], "in": li.id, "hop": hop.id, "out": lo.id}
    return lanes, stands, exits, pend, bay


def stand_chains(stands, bay):
    """The lanes a machine drives through a stand, in order: (in, out) for a stand, (in, hop, out) for the refuel bay."""
    chains = [[s["in"], s["out"]] for s in stands if not (bay and s["id"] in (bay["queue"], bay["bay"]))]
    if bay:
        chains.append([bay["in"], bay["hop"], bay["out"]])
    return chains


def chain_coords(lanes_by_id, chain):
    """{(lane, cell): arc length along the chain to the middle of the cell}."""
    out, s = {}, 0.0
    for lid in chain:
        lane = lanes_by_id[lid]
        for k, c in enumerate(lane.cells):
            ln = math.hypot(c[2] - c[0], c[3] - c[1])
            out[(lid, k)] = s + ln / 2.0
            s += ln
    return out


def classify_conflicts(lanes, inputs, exits, stands, bay):
    """Every conflict between two lanes, and what covers it. A conflict is covered when it is a diverge (the first cells of an access
    lane run with the lane it leaves) or the lanes through one stand, within a slot of each other along the way through it (one machine's way through, and the
    spacing a following truck keeps). Returns (uncovered, exits with `shared` filled in)."""
    by_id = {l.id: l for l in lanes}
    cf = conflicts(lanes, inputs)
    coords = {}
    for chain in stand_chains(stands, bay):
        c = chain_coords(by_id, chain)
        for key, v in c.items():
            coords.setdefault(key, []).append((tuple(chain), v))
    uncovered, diverge = {}, {}
    for (a, b), gap in cf.items():
        same_chain = False
        for ca, sa in coords.get(a, ()):
            for cb, sb in coords.get(b, ()):
                if ca == cb and abs(sa - sb) <= SLOT_M:
                    same_chain = True
        if same_chain:
            continue
        hit = None
        for ex in exits:
            for x, y in ((a, b), (b, a)):
                if x[0] == ex["to"] and y[0] == ex["lane"] and cyc_dist(y[1], ex["cell"], len(by_id[ex["lane"]])) <= DIVERGE_WINDOW:
                    hit = (ex["id"], x[1])
        if hit:
            diverge.setdefault(hit[0], []).append(hit[1])
            continue
        uncovered[(a, b)] = gap
    out = []
    for ex in exits:
        shared = 1 + max(diverge.get(ex["id"], [-1]))
        if shared > DIVERGE_MAX:
            raise SystemExit("%s: the lane leaving shares ground with the loop for %d cells (at most %d)" % (ex["id"], shared, DIVERGE_MAX))
        out.append(dict(ex, shared=shared))
    return uncovered, out


def in_polygon(poly, x, z):
    """Whether (x, z) lies in or on the convex polygon `poly` (counter-clockwise, as hull() gives it)."""
    n = len(poly)
    for i in range(n):
        a, b = poly[i], poly[(i + 1) % n]
        if (b[0] - a[0]) * (z - a[1]) - (b[1] - a[1]) * (x - a[0]) < -1e-9:
            return False
    return True


def box_polygon(points):
    """A convex polygon holding every point, with a little give so that two lanes side by side still enclose something."""
    pts = []
    for x, z in points:
        for dx, dz in ((0.25, 0.25), (-0.25, 0.25), (0.25, -0.25), (-0.25, -0.25)):
            pts.append((x + dx, z + dz))
    return hull(pts)


def members_of(poly, lanes):
    """{lane id: sorted cell indices} of the cells of every lane with an end inside `poly`."""
    out = {}
    for lane in lanes:
        got = [k for k, c in enumerate(lane.cells) if in_polygon(poly, c[0], c[1]) or in_polygon(poly, c[2], c[3])]
        if got:
            out[lane.id] = got
    return out


def build_junctions(lanes, uncovered):
    """The junction boxes: one for each cluster of conflicts nothing else covers (cells joined by the conflicts they share). Each is a convex
    polygon round the cells that conflict, with its members (the cells of every lane with an end in it), the approach cell of each run of
    members (where a machine waits for a grant) and the room a granted machine needs beyond it."""
    by_id = {l.id: l for l in lanes}
    parent = {}

    def find(a):
        parent.setdefault(a, a)
        while parent[a] != a:
            parent[a] = parent[parent[a]]
            a = parent[a]
        return a
    for a, b in uncovered:
        parent[find(a)] = find(b)
    comps = {}
    for node in sorted(parent):
        comps.setdefault(find(node), []).append(node)
    order = {l.id: i for i, l in enumerate(lanes)}
    out, used = [], {}
    for nodes in sorted(comps.values(), key=lambda ns: min((order[n[0]], n[1]) for n in ns)):
        pts = []
        for lid, k in nodes:
            c = by_id[lid].cells[k]
            pts += [(c[0], c[1]), (c[2], c[3])]
        poly = box_polygon(pts)
        members = members_of(poly, lanes)
        heads = [abs(qf.wrap(heading_of_cell(by_id[a[0]], a[1]) - heading_of_cell(by_id[b[0]], b[1]))) for a, b in uncovered if a in nodes]
        worst = max(heads)
        kind = "merge" if worst < 60.0 else "crossing" if worst < 135.0 else "oncoming"
        cx = sum(p[0] for p in poly) / len(poly)
        cz = sum(p[1] for p in poly) / len(poly)
        outs = sorted({lid for lid, _ in nodes if lid.startswith("access/") and lid.endswith("/out")})
        if outs:
            base = "merge-" + outs[0].split("/")[1]
        else:
            place = min(PLACES, key=lambda p: math.hypot(p[1][0] - cx, p[1][1] - cz))
            base = "%s-%s" % (kind, place[0])
        used[base] = used.get(base, 0) + 1
        jid = base if used[base] == 1 else "%s-%d" % (base, used[base])
        mem, appr, room = [], [], []
        for lid in sorted(members, key=lambda l: (by_id[l].kind != "loop", l)):
            lane = by_id[lid]
            n = len(lane)
            for run in runs_of(members[lid], n, lane.cyclic):
                mem.append(dict(lane=lid, cells=run))
                a, b = run
                before = (a - 1) % n if lane.cyclic else a - 1
                if before >= 0:
                    appr.append(dict(lane=lid, cell=before))
                if lane.cyclic:
                    room.append(dict(lane=lid, cells=[(b + 1) % n, (b + ROOM_CELLS) % n]))
                elif b + 1 < n:
                    room.append(dict(lane=lid, cells=[b + 1, min(n - 1, b + ROOM_CELLS)]))
        out.append(dict(id=jid, kind=kind, polygon=[[r3(x), r3(z)] for x, z in poly], members=mem, approach=appr, room=room))
    return out


# ==================================================================================
# stations: the load point and the dump pad
# ==================================================================================
LOAD_PARTNER = "SP-LD-0001"     # the loader that loads the haul trucks from the loading bench
LOAD_CAPACITY, PAD_CAPACITY = 1, 2


def slots(metres):
    return int(math.floor(metres / SLOT_M + 1e-9))


def range_len(lane, run):
    return sum(math.hypot(lane.cells[k][2] - lane.cells[k][0], lane.cells[k][3] - lane.cells[k][1]) for k in run_cells(run, len(lane)))


def cell_time(loop, k, period):
    """The track time cell k's start is reached; one past the last cell is the end of the period."""
    n = len(loop)
    return loop.cells[k][4] if k < n else period + loop.cells[k - n][4]


def partner_ready(inputs, machine):
    """When, in its own track, the machine's cycle ends standing at the pose it waits at, and that pose: the start of the last stretch of
    its track in which it does not move."""
    fl = inputs.live
    m = next(m for m in fl["machines"] if m["id"] == machine)
    tr = fl["tracks"][m["track"]]
    n = len(tr["x"])
    i = n - 1
    while i > 0 and abs(tr["x"][i - 1] - tr["x"][n - 1]) < 1e-3 and abs(tr["z"][i - 1] - tr["z"][n - 1]) < 1e-3:
        i -= 1
    return m, tr, i * fl["dt"], (r3(tr["x"][i]), r3(tr["z"][i]), r3(tr["heading"][i]))


def build_stations(inputs, loop):
    fl = inputs.live
    tr0 = fl["tracks"][0]
    T = tr0["period"]
    n_loop = sum(1 for m in fl["machines"] if m["track"] == 0)
    headway = T / n_loop
    n = len(loop)
    m, ptr, ready_s, ready_pose = partner_ready(inputs, LOAD_PARTNER)
    window = ptr["period"]
    first = next(k for k in range(n) if loop.cells[k][4] >= T - window)
    nbuf = int(math.ceil(LOAD_CAPACITY * SLOT_M / CELL_M))
    load = dict(id="load", lane="loop", core=[first, n - 1], buffer=[0, nbuf - 1], capacity=LOAD_CAPACITY, window_s=r3(window),
                headway_s=r3(headway), ride_through=True,
                partner=dict(machine=LOAD_PARTNER, track=m["track"], ready_s=r3(ready_s), ready_pose=list(ready_pose)))
    pad = next(p for p in inputs.feats["pads"] if p["name"] == "fill")
    r = pad["rect"]
    inside = [k for k in range(n) if r[0] <= loop.cells[k][0] <= r[1] and r[2] <= loop.cells[k][1] <= r[3]]
    lo, hi = min(inside), max(inside)
    nbuf = int(math.ceil(PAD_CAPACITY * SLOT_M / CELL_M))
    pad_st = dict(id="pad", lane="loop", core=[lo, hi], buffer=[hi + 1, hi + nbuf], capacity=PAD_CAPACITY,
                  window_s=r3(loop.cells[hi + 1][4] - loop.cells[lo][4]), headway_s=r3(headway), ride_through=False, partner=None)
    return [load, pad_st]


def work_area_rows(inputs):
    return [dict(machine=m, polygon=[[r3(x), r3(z)] for x, z in poly]) for m, poly in work_areas(inputs)]


# ==================================================================================
# a topology as written, read back
# ==================================================================================
class Topo:
    """The topology file as the checks (and the C# mirror) read it: lanes with their cells, and the plain data beside them."""

    def __init__(self, d):
        self.d = d
        self.lanes = []
        for l in d["lanes"]:
            self.lanes.append(Lane(l["id"], l["kind"], l["cells"], cyclic=l["cyclic"]))
        self.by = {l.id: l for l in self.lanes}
        self.loop = self.by["loop"]
        self.stands = {s["id"]: s for s in d["stands"]}
        self.junctions = d["junctions"]
        self.stations = d["stations"]
        self.bay = d.get("bay")

    def chains(self):
        return stand_chains(self.d["stands"], self.bay)

    def core_runs(self, lane_id):
        return [st["core"] for st in self.stations if st["lane"] == lane_id]


def lane_length(lane):
    return sum(math.hypot(c[2] - c[0], c[3] - c[1]) for c in lane.cells)


def exempt_for(d, who):
    return [e for e in d["exempt"] if e["to"] == who]


def is_exempt(d, who, o_name, x, z):
    """Whether `who` (a lane or stand id) is not held to the outline called `o_name` at (x, z): the table says so (EXEMPT)."""
    for e in exempt_for(d, who):
        if e.get("obstacle") != o_name:
            continue
        if e["spot"] is None or math.hypot(x - e["spot"][0], z - e["spot"][1]) <= e["radius_m"]:
            return True
    return False


# ==================================================================================
# the checks
# ==================================================================================
def obstacle_gap(o, group):
    """Clear distance between a footprint (a group of convex polygons) and an obstacle; negative when they overlap. A box prop is judged
    at the worse of its two possible turns, as quarry_fleet.footprint_gap does."""
    if o[0] == "capsule":
        return min(qf._poly_seg_gap(q, o[2], o[3], o[4], o[5]) - o[6] for q in group)
    cx, cz, hx, hz, hd = o[2], o[3], o[4], o[5], o[6]
    worst = 1e9
    for sign in (1.0, -1.0):
        h = math.radians(hd) * sign
        c, s = math.cos(h), math.sin(h)
        quad = [(cx + lx * c + lz * s, cz - lx * s + lz * c) for lx, lz in ((hx, hz), (hx, -hz), (-hx, -hz), (-hx, hz))]
        worst = min(worst, min(sat_gap(q, quad) for q in group))
    return worst


def stand_group(kind, pose):
    return boxes(kind, pose[0], pose[1], pose[2])


def track_sweeps(inputs):
    """Every distinct pose of every track of both fleets, parked machines included: [(fleet, track, kind, owners, [(x, z, heading, boom)])]."""
    if getattr(inputs, "_sweeps", None) is None:
        out = []
        for name, fl in (("live", inputs.live), ("preview", inputs.preview)):
            owners = {}
            for m in fl["machines"]:
                owners.setdefault(m["track"], []).append(m["id"])
            for ti, tr in enumerate(fl["tracks"]):
                poses, last = [], None
                for i in range(len(tr["x"])):
                    x, z, hd = float(tr["x"][i]), float(tr["z"][i]), float(tr["heading"][i])
                    if last and math.hypot(x - last[0], z - last[1]) < 0.2 and abs(qf.wrap(hd - last[2])) < 2.0:
                        continue
                    last = (x, z, hd)
                    poses.append((x, z, hd, float(tr["p1"][i])))
                out.append((name, ti, tr["kind"], owners.get(ti, []), poses))
        inputs._sweeps = out
    return inputs._sweeps


def sweep_group(kind, p):
    return [[(float(c[0]), float(c[1])) for c in b] for b in qf.corners(kind, p[0], p[1], p[2], p[3])]


def v1_cycle_capacity(t):
    """V1 (P1): on every cyclic lane the slots outside station cores hold every machine that can be on it, and one more."""
    out = []
    nmax = t.d["fleet"]["machines"]
    for lane in t.lanes:
        if not lane.cyclic:
            continue
        cores = sum(range_len(lane, run) for run in t.core_runs(lane.id))
        free = slots(lane_length(lane) - cores)
        if free < nmax + 1:
            out.append("V1 P1: %s has %d slots outside its station cores for %d machines (needs %d)" % (lane.id, free, nmax, nmax + 1))
    return out


def junction_span(t, junc, lane):
    """The cells of a cyclic lane a junction takes (its members and the room beyond them), and the largest stretch it leaves free: the
    span is everything but that gap, so a machine between two stretches counts as inside."""
    n = len(lane)
    taken = set()
    for m in junc["members"]:
        if m["lane"] == lane.id:
            taken.update(run_cells(m["cells"], n))
    for r in junc["room"]:
        if r["lane"] == lane.id:
            taken.update(run_cells(r["cells"], n))
    if not taken:
        return None, []
    free = [k for k in range(n) if k not in taken]
    if not free:
        return taken, []
    runs = runs_of(free, n, True)
    gap = max(runs, key=lambda r: len(run_cells(r, n)))
    return taken, run_cells(gap, n)


def v2_span_complement(t):
    """V2 (P2): the complement of every junction's span on a cyclic lane holds everyone who can be on the lane but the requester."""
    out = []
    nmax = t.d["fleet"]["machines"]
    for junc in t.junctions:
        for lane in t.lanes:
            if not lane.cyclic or not any(m["lane"] == lane.id for m in junc["members"]):
                continue
            _, gap = junction_span(t, junc, lane)
            gapset = set(gap)
            length = sum(math.hypot(lane.cells[k][2] - lane.cells[k][0], lane.cells[k][3] - lane.cells[k][1]) for k in gap)
            core = 0.0
            for run in t.core_runs(lane.id):
                core += sum(math.hypot(lane.cells[k][2] - lane.cells[k][0], lane.cells[k][3] - lane.cells[k][1]) for k in run_cells(run, len(lane)) if k in gapset)
            free = slots(length - core)
            if free < nmax - 1:
                out.append("V2 P2: %s leaves %d slots outside station cores on %s for the %d other machines (needs %d)"
                           % (junc["id"], free, lane.id, nmax - 1, nmax - 1))
    return out


def cell_index(t, inputs):
    """[(lane id, cell, swept footprint, centre x, centre z, radius)] for every cell of every lane, cached on the topology."""
    if getattr(t, "_index", None) is None:
        items = []
        for lane in t.lanes:
            for k in range(len(lane)):
                g = cell_group(lane, k, inputs)
                items.append((lane.id, k, g) + center_radius(g))
        t._index = items
    return t._index


def own_lanes(t, stand):
    """The lanes that belong to a stand: the ones it is reached and left by, and the others of the way through it."""
    mine = {stand["in"], stand["out"]}
    for chain in t.chains():
        if mine & set(chain):
            mine |= set(chain)
    return mine


def track_exempt(d, stand_id, owners):
    """Whether a track is held to be the stand's own: a track only an exempt machine plays (the scripted refuel visit)."""
    ms = [e["machine"] for e in exempt_for(d, stand_id) if "machine" in e]
    return len(owners) == 1 and owners[0] in ms


def stand_gaps(t, inputs, sid, kind):
    """How near a stand's footprint (for one kind) comes to each class of thing it must keep clear of: {class: (gap, where)}."""
    s = t.stands[sid]
    idx = cell_index(t, inputs)
    own = own_lanes(t, s)
    g = stand_group(kind, s["pose"])
    cx, cz, r = center_radius(g)
    worst = {}

    def note(what, gap, where):
        if what not in worst or gap < worst[what][0]:
            worst[what] = (gap, where)
    for lid, k, grp, x, z, rad in idx:
        if lid in own or math.hypot(cx - x, cz - z) > r + rad + STAND_CLEAR_M:
            continue
        note("lane", group_gap(g, grp), "%s cell %d" % (lid, k))
    for name, ti, tk, owners, poses in track_sweeps(inputs):
        if track_exempt(t.d, sid, owners):
            continue
        for p in poses:
            if math.hypot(cx - p[0], cz - p[1]) > r + 14.0:
                continue
            note("sweep", group_gap(g, sweep_group(tk, p)), "%s fleet track %d (%s)" % (name, ti, "/".join(owners)))
    for j in t.junctions:
        note("junction", min(sat_gap(b, j["polygon"]) for b in g), j["id"])
    for o in inputs.obstacles:
        ox, oz = ((o[2], o[3]) if o[0] == "box" else ((o[2] + o[4]) / 2.0, (o[3] + o[5]) / 2.0))
        span = math.hypot(o[4], o[5]) if o[0] == "box" else math.hypot(o[4] - o[2], o[5] - o[3]) / 2.0 + o[6]
        if math.hypot(cx - ox, cz - oz) > r + span + STAND_CLEAR_M or is_exempt(t.d, sid, o[1], ox, oz):
            continue
        note("obstacle", obstacle_gap(o, g), "%s at (%.1f, %.1f)" % (o[1], ox, oz))
    return worst


def v3_stands(t, inputs):
    """V3 (P3): every stand, queue and bay: its footprint keeps STAND_CLEAR_M from every lane it is not reached by, every track sweep of both
    fleets, every junction and every obstacle; it does not overlap another stand; it is not a cul-de-sac; and its pose is a slot past any
    junction on the lane it is reached by."""
    out = []
    idx = cell_index(t, inputs)
    for sid, s in t.stands.items():
        for kind in s["kinds"]:
            for what, (gap, where) in sorted(stand_gaps(t, inputs, sid, kind).items()):
                if gap < STAND_CLEAR_M:
                    out.append("V3 stand %s (%s): %.2f m from %s %s, %.1f m is kept" % (sid, kind, gap, what, where, STAND_CLEAR_M))
    ids = sorted(t.stands)
    for i, a in enumerate(ids):
        for b in ids[i + 1:]:
            if t.bay and {a, b} == {t.bay["queue"], t.bay["bay"]}:
                continue
            gap = group_gap(stand_group(max(t.stands[a]["kinds"], key=lambda k: qf.FOOT[k][1]), t.stands[a]["pose"]),
                            stand_group(max(t.stands[b]["kinds"], key=lambda k: qf.FOOT[k][1]), t.stands[b]["pose"]))
            if gap < 0.0:
                out.append("V3 stand %s: overlaps stand %s by %.2f m" % (a, b, -gap))
    # a stand is driven through: near it (within two slots of its pose) the lanes through it do not conflict beyond a slot of the way through
    # it. Further off two lanes of one stand may cross: that is a junction like any other, and V5 sees it held
    for chain in t.chains():
        coords = chain_coords(t.by, chain)
        poses, run = [], 0.0
        for lid in chain[:-1]:
            run += lane_length(t.by[lid])
            poses.append(run)
        near = lambda key: min(abs(coords[key] - p) for p in poses) <= 2.0 * SLOT_M
        seen = False
        cells = [(lid, k) for lid in chain for k in range(len(t.by[lid])) if near((lid, k))]
        pos = {(lid, k): (g, cx, cz, r) for lid, k, g, cx, cz, r in idx if lid in chain}
        for i, a in enumerate(cells):
            for b in cells[i + 1:]:
                if a[0] == b[0] or abs(coords[a] - coords[b]) <= SLOT_M:
                    continue
                ga, gb = pos[a], pos[b]
                if math.hypot(ga[1] - gb[1], ga[2] - gb[2]) > ga[3] + gb[3] + CONFLICT_M:
                    continue
                if group_gap(ga[0], gb[0]) < CONFLICT_M:
                    out.append("V3 cul-de-sac: the lanes %s conflict at %s cell %d and %s cell %d, %.0f m apart along the way through"
                               % (" -> ".join(chain), a[0], a[1], b[0], b[1], abs(coords[a] - coords[b])))
                    seen = True
                    break
            if seen:
                break
    # and its pose is a slot past any junction on the lane it is reached by (a machine waiting on the way in must not stand in a box or its room)
    for sid, s in t.stands.items():
        for lid, towards_end in ((s["in"], True),):
            lane = t.by[lid]
            n = len(lane)
            lens = [math.hypot(c[2] - c[0], c[3] - c[1]) for c in lane.cells]
            for j in t.junctions:
                cells = set()
                for m in j["members"] + j["room"]:
                    if m["lane"] == lid:
                        cells.update(run_cells(m.get("cells"), n))
                for k in cells:
                    dist = sum(lens[k:]) if towards_end else sum(lens[:k + 1])
                    if dist < SLOT_M:
                        out.append("V3 stand %s: junction %s is %.1f m from its pose along %s (a slot is %.1f m)" % (sid, j["id"], dist, lid, SLOT_M))
                        break
    return out


def station_times(t, st):
    """The track times a station's region (its core and its exit buffer) runs from and to, and its core's end: (t_in, t_core_out, t_buffer_out)."""
    loop = t.by[st["lane"]]
    n = len(loop)
    T = t.d["fleet"]["period_s"]
    a, b = st["core"]
    c, e = st["buffer"]
    t_in = loop.cells[a][4]
    t_core = cell_time(loop, b + 1, T) if b + 1 <= n else T
    t_buf = cell_time(loop, e + 1, T)
    if t_core < t_in:
        t_core += T
    if t_buf < t_core:
        t_buf += T
    return t_in, t_core, t_buf


def station_min_gap(inputs, t_in, t_out, headway, window):
    """The nearest two haul trucks come in a station's region, over every headway from the design spacing to the spacing plus the window: the
    leader at time u into the region, the follower the headway behind it, both read at the frames the fleet is generated and checked at.
    Negative would be a touch. Returns (gap, headway, u)."""
    fl = inputs.live
    tr, dt = fl["tracks"][0], fl["dt"]
    us = np.arange(0.0, (t_out - t_in) + 1e-9, dt)
    lead = []
    for u in us:
        p = frame_at(tr, dt, t_in + u)
        lead.append((p, sweep_group("Hauler", p)))
    best = (1e9, 0.0, 0.0)
    h = headway
    while h <= headway + window + 1e-9:
        for i, u in enumerate(us):
            pa, ga = lead[i]
            pb = frame_at(tr, dt, t_in + u - h)
            if math.hypot(pa[0] - pb[0], pa[1] - pb[1]) > 30.0:
                continue
            gap = group_gap(ga, sweep_group("Hauler", pb))
            if gap < best[0]:
                best = (gap, h, float(u))
        h += dt
    return best


def partner_min_gap(t, inputs, st):
    """The nearest the station's coupled loader comes to the truck in its core, over every truck and every moment the truck is in the core."""
    fl = inputs.live
    dt = fl["dt"]
    tr0 = fl["tracks"][0]
    T = tr0["period"]
    p = st["partner"]
    ltr = fl["tracks"][p["track"]]
    lm = next(m for m in fl["machines"] if m["id"] == p["machine"])
    t_in, t_core, _ = station_times(t, st)
    best = 1e9
    for m in fl["machines"]:
        if m["track"] != 0:
            continue
        s = 0.0
        while s < T:
            tt = (s - m["offset"]) % T
            if t_in <= tt <= min(t_core, T) or (t_core > T and tt <= t_core - T):
                a = frame_at(tr0, dt, tt)
                b = frame_at(ltr, dt, s - lm["offset"])
                if math.hypot(a[0] - b[0], a[1] - b[1]) < 30.0:
                    best = min(best, group_gap(sweep_group("Hauler", a), sweep_group("Loader", b)))
            s += dt
    return best


def v4_stations(t, inputs):
    """V4: every station is safe at every headway from the design spacing up (two members never closer than nothing), its coupled loader
    clears its truck, its exit buffer holds its capacity in slots, and its headway is the fleet's spacing."""
    out = []
    fleet = inputs.live
    T = fleet["tracks"][0]["period"]
    spacing = T / sum(1 for m in fleet["machines"] if m["track"] == 0)
    loop = t.loop
    for st in t.stations:
        if abs(st["headway_s"] - spacing) > 0.0011:
            out.append("V4 station %s: its headway is %.3f s and the fleet's spacing is %.3f s" % (st["id"], st["headway_s"], spacing))
        buf = slots(range_len(loop, st["buffer"]))
        if buf < st["capacity"]:
            out.append("V4 station %s: its exit buffer holds %d slots for a capacity of %d" % (st["id"], buf, st["capacity"]))
        t_in, _, t_out = station_times(t, st)
        gap, h, u = station_min_gap(inputs, t_in, t_out, st["headway_s"], st["window_s"])
        if gap < 0.0:
            out.append("V4 station %s: two trucks %.2f s apart overlap by %.2f m, %.1f s into it" % (st["id"], h, -gap, u))
        if abs(gap - st["min_gap_m"]) > 0.006:
            out.append("V4 station %s: it records a nearest approach of %.3f m and measures %.3f m" % (st["id"], st["min_gap_m"], gap))
        if st["partner"]:
            pg = partner_min_gap(t, inputs, st)
            if pg < 0.0:
                out.append("V4 station %s: its loader %s overlaps the truck by %.2f m" % (st["id"], st["partner"]["machine"], -pg))
            if abs(pg - st["partner"]["min_gap_m"]) > 0.006:
                out.append("V4 station %s: it records a loader gap of %.3f m and measures %.3f m" % (st["id"], st["partner"]["min_gap_m"], pg))
    return out


def junction_cells(junc, lane):
    n = len(lane)
    cells = set()
    for m in junc["members"]:
        if m["lane"] == lane.id:
            cells.update(run_cells(m["cells"], n))
    return cells


def v5_boxes(t, inputs):
    """V5: every conflict between two lanes lies inside one junction box (or is a diverge, or a stand's own way through); no box's members
    include the approach cell where its own requester waits (a box reaching back over its approach is how two machines come to wait on
    each other); and a box's members are exactly the cells with an end in its polygon."""
    out = []
    by_junc = {j["id"]: {lid: junction_cells(j, t.by[lid]) for lid in {m["lane"] for m in j["members"]}} for j in t.junctions}
    chains = {}
    for chain in t.chains():
        for key, v in chain_coords(t.by, chain).items():
            chains.setdefault(key, []).append((tuple(chain), v))
    cf = conflicts(t.lanes, inputs)
    seen = set()
    for (a, b), gap in sorted(cf.items()):
        if any(a[1] in cells.get(a[0], ()) and b[1] in cells.get(b[0], ()) for cells in by_junc.values()):
            continue
        if any(ca == cb and abs(sa - sb) <= SLOT_M for ca, sa in chains.get(a, ()) for cb, sb in chains.get(b, ())):
            continue
        covered = False
        for ex in t.d["exits"]:
            for x, y in ((a, b), (b, a)):
                if x[0] == ex["to"] and y[0] == ex["lane"] and x[1] < ex["shared"] and cyc_dist(y[1], ex["cell"], len(t.by[ex["lane"]])) <= DIVERGE_WINDOW:
                    covered = True
        if covered:
            continue
        key = (a[0], b[0])
        if key in seen:
            continue
        seen.add(key)
        c = t.by[a[0]].cells[a[1]]
        out.append("V5 conflict: %s cell %d and %s cell %d come within %.2f m of each other at (%.1f, %.1f) and no junction holds both"
                   % (a[0], a[1], b[0], b[1], gap, c[0], c[1]))
    for j in t.junctions:
        for ap in j["approach"]:
            lane = t.by[ap["lane"]]
            if ap["cell"] in junction_cells(j, lane):
                out.append("V5 approach: junction %s holds the approach cell %d of %s among its own members (it reaches back over its approach)"
                           % (j["id"], ap["cell"], ap["lane"]))
        got = members_of(j["polygon"], t.lanes)
        want = {}
        for m in j["members"]:
            want.setdefault(m["lane"], set()).update(run_cells(m["cells"], len(t.by[m["lane"]])))
        for lid in sorted(set(got) | set(want)):
            if set(got.get(lid, ())) != want.get(lid, set()):
                out.append("V5 members: junction %s lists cells of %s that are not the ones inside its polygon" % (j["id"], lid))
    return out


def lane_points(lane):
    """The line a lane is driven on, as arrays: each cell's start, and the last cell's end."""
    xs = [c[0] for c in lane.cells] + [lane.cells[-1][2]]
    zs = [c[1] for c in lane.cells] + [lane.cells[-1][3]]
    return np.array(xs), np.array(zs)


def v6_drivable(t, inputs):
    """V6: every lane's driven line is within the grade limit a planned route is held to (12 % over 10 m, and its step rule), and clear of every
    outline by the reach a driving hauler keeps (3.2 m), except what the table says a lane is laid out between."""
    out = []
    for lane in t.lanes:
        xs, zs = lane_points(lane)
        worst = qf.sustained_grade(inputs.ground, xs, zs)
        if worst[0] > GRADE_MAX + 1e-9:
            out.append("V6 grade: %s climbs %.2f %% sustained at (%.1f, %.1f); a planned route is held to %.0f %%" % (lane.id, worst[0], worst[2], worst[3], GRADE_MAX))
        near = {}
        for k, c in enumerate(lane.cells):
            for o in inputs.obstacles:
                d = qf.obstacle_distance(o, c[0], c[1])
                if d >= TRAVEL_REACH or is_exempt(t.d, lane.id, o[1], c[0], c[1]):
                    continue
                if o[1] not in near or d < near[o[1]][0]:
                    near[o[1]] = (d, k, c[0], c[1])
        for name, (d, k, x, z) in sorted(near.items()):
            out.append("V6 reach: %s cell %d comes %.2f m from %s at (%.1f, %.1f); a driving hauler keeps %.1f m" % (lane.id, k, d, name, x, z, TRAVEL_REACH))
    return out


def v7_fresh(t, inputs):
    """V7: the topology was made from the files that are committed, and its loop is the live fleet's track."""
    out = []
    want = inputs.hashes()
    for key, h in want.items():
        if t.d["source"].get(key) != h:
            out.append("V7 fresh: its %s hash is not the committed file's" % key)
    fl = inputs.live
    tr = fl["tracks"][0]
    n_loop = sum(1 for m in fl["machines"] if m["track"] == 0)
    f = t.d["fleet"]
    if f["loop_trucks"] != n_loop or f["machines"] != len(fl["machines"]) or abs(f["period_s"] - tr["period"]) > 1e-6:
        out.append("V7 fresh: its fleet block (%s) is not the live fleet's (%d on the loop of %d, %.3f s)" % (f, n_loop, len(fl["machines"]), tr["period"]))
    worst = (0.0, 0)
    for k, c in enumerate(t.loop.cells):
        p = pose_at(tr, fl["dt"], c[4])
        off = math.hypot(p[0] - c[0], p[1] - c[1])
        if off > worst[0]:
            worst = (off, k)
    if worst[0] > 0.05:
        out.append("V7 fresh: loop cell %d starts %.3f m from where the track is at its time (at most 0.05 m)" % (worst[1], worst[0]))
    return out


def v8_zones(t, inputs):
    """V8: every zone has a stand for each kind goto-area may send there, inside the zone."""
    out = []
    rects = {z["token"]: z for z in inputs.feats["zones"]}
    for z in t.d["zones"]:
        for kind in z["kinds"]:
            if not any(s["zone"] == z["token"] and kind in s["kinds"] for s in t.stands.values()):
                out.append("V8 zone: %s has no stand for a %s" % (z["token"], kind))
    for sid, s in t.stands.items():
        zr = rects[s["zone"]]
        for kind in s["kinds"]:
            g = stand_group(kind, s["pose"])
            xs = np.array([p[0] for b in g for p in b])
            zs = np.array([p[1] for b in g for p in b])
            if not qf._in_rounded(xs, zs, zr["rect"], zr.get("corner", qf.STAND_CORNER)).all():
                out.append("V8 zone: stand %s (%s) is not wholly inside %s" % (sid, kind, s["zone"]))
    return out


def v9_work_areas(t, inputs):
    """V9: no lane cell and no stand within WORK_CLEAR_M of another machine's work area, but a load station's coupled loader in its own core. The
    haul loop is the choreography's own line and passes SP-LD-0002's and SP-DZ-0003's work 1.27 m and 1.00 m off, which the fleet generator holds
    only to not touching: for it, V9 asks that it not enter a work area."""
    out = []
    idx = cell_index(t, inputs)
    exempt = set()
    for st in t.stations:
        if st["partner"]:
            lane = t.by[st["lane"]]
            for k in run_cells(st["core"], len(lane)):
                exempt.add((st["partner"]["machine"], st["lane"], k))
    for wa in t.d["work_areas"]:
        poly = wa["polygon"]
        near = {}
        for lid, k, g, cx, cz, r in idx:
            if (wa["machine"], lid, k) in exempt:
                continue
            gap = min(sat_gap(b, poly) for b in g)
            keep = 0.0 if lid == "loop" else WORK_CLEAR_M
            if gap < keep and (lid not in near or gap < near[lid][0]):
                near[lid] = (gap, k, keep)
        for lid, (gap, k, keep) in sorted(near.items()):
            out.append("V9 work area: %s cell %d is %.2f m from %s's work area (%.1f m is kept)" % (lid, k, gap, wa["machine"], keep))
        for sid, s in t.stands.items():
            for kind in s["kinds"]:
                gap = min(sat_gap(b, poly) for b in stand_group(kind, s["pose"]))
                if gap < WORK_CLEAR_M:
                    out.append("V9 work area: stand %s (%s) is %.2f m from %s's work area (%.1f m is kept)" % (sid, kind, gap, wa["machine"], WORK_CLEAR_M))
    return out


VALIDATORS = [v1_cycle_capacity, v2_span_complement, v3_stands, v4_stations, v5_boxes, v6_drivable, v7_fresh, v8_zones, v9_work_areas]
NEEDS_INPUTS = {v3_stands, v4_stations, v5_boxes, v6_drivable, v7_fresh, v8_zones, v9_work_areas}


def run_checks(t, inputs, only=None):
    out = []
    for v in VALIDATORS:
        if only is not None and v not in only:
            continue
        out += v(t, inputs) if v in NEEDS_INPUTS else v(t)
    return out


# ==================================================================================
# assembling the file
# ==================================================================================
def lane_row(lane):
    row = {"id": lane.id, "kind": lane.kind, "cyclic": lane.cyclic}
    for key in ("track", "road"):
        if key in lane.meta:
            row[key] = lane.meta[key]
    row["cells"] = lane.cells
    return row


def assemble(inputs):
    """The topology of the site as the file will hold it, with every check run on it: (dict, defects)."""
    lanes, stands, exits, pend, bay = build_lanes(inputs)
    loop = lanes[0]
    uncovered, exits = classify_conflicts(lanes, inputs, exits, stands, bay)
    junctions = build_junctions(lanes, uncovered)
    by_id = {l.id: l for l in lanes}
    entries = []
    for p in pend:
        last = len(by_id[p["frm"]]) - 1
        jid = next((j["id"] for j in junctions if any(m["lane"] == p["frm"] and last in run_cells(m["cells"], len(by_id[p["frm"]])) for m in j["members"])), None)
        entries.append(dict(id=p["id"], **{"from": p["frm"]}, lane=p["lane"], cell=p["cell"], junction=jid))
    fl = inputs.live
    fleet = dict(loop_trucks=sum(1 for m in fl["machines"] if m["track"] == 0), machines=len(fl["machines"]), period_s=r3(fl["tracks"][0]["period"]))
    d = dict(generator="ArtSource/terrain/quarry_topology.py", source=inputs.hashes(), cell_m=CELL_M, slot_m=SLOT_M, fleet=fleet,
             zones=[dict(token=tok, kinds=list(kinds)) for tok, kinds in ZONES],
             lanes=[lane_row(l) for l in lanes], exits=exits, entries=entries, junctions=junctions, stands=stands, bay=bay,
             stations=build_stations(inputs, loop), work_areas=work_area_rows(inputs), exempt=EXEMPT_ROWS)
    t = Topo(d)
    # what the checks measure and the file records beside it
    for st in d["stations"]:
        t_in, _, t_out = station_times(t, st)
        st["min_gap_m"] = r3(station_min_gap(inputs, t_in, t_out, st["headway_s"], st["window_s"])[0])
        if st["partner"]:
            st["partner"]["min_gap_m"] = r3(partner_min_gap(t, inputs, st))
    for s in d["stands"]:
        s["clearance_m"] = r3(min(gap for kind in s["kinds"] for gap, _ in stand_gaps(t, inputs, s["id"], kind).values()))
    d = json.loads(json.dumps(d))
    return d, run_checks(Topo(d), inputs)


def dump_json(d):
    """One line per lane, per junction and so on, so a change to the site reads as a change to a line; LF endings, the same bytes anywhere."""
    lines = ["{"]
    keys = list(d)
    for i, key in enumerate(keys):
        v = d[key]
        comma = "," if i < len(keys) - 1 else ""
        if isinstance(v, list) and v and isinstance(v[0], dict):
            lines.append('"%s":[' % key)
            for j, row in enumerate(v):
                lines.append(json.dumps(row, separators=(",", ":")) + ("," if j < len(v) - 1 else ""))
            lines.append("]" + comma)
        else:
            lines.append('"%s":%s%s' % (key, json.dumps(v, separators=(",", ":")), comma))
    lines.append("}")
    return "\n".join(lines) + "\n"


def exemptions_needed(t, inputs):
    """Every exemption in the table must be needed: without it the lane or stand it is for fails a check. A row nothing exercises is free to delete."""
    import copy
    out = []
    for i, e in enumerate(t.d["exempt"]):
        if "obstacle" not in e:
            continue
        d2 = copy.deepcopy(t.d)
        del d2["exempt"][i]
        got = [x for x in run_checks(Topo(d2), inputs, only=[v6_drivable, v3_stands]) if e["to"] in x and e["obstacle"] in x]
        if not got:
            out.append("exemption: %s is exempt from %s and is held to nothing it would fail (delete the row)" % (e["to"], e["obstacle"]))
    return out


# ==================================================================================
# the report, the selftest and the command line
# ==================================================================================
def report(t, inputs):
    """What each check measured on the real site, as lines."""
    d = t.d
    lines = []
    nmax = d["fleet"]["machines"]
    for lane in t.lanes:
        if lane.cyclic:
            cores = sum(range_len(lane, run) for run in t.core_runs(lane.id))
            lines.append("V1 %s: %d slots outside station cores for %d machines (needs %d)" % (lane.id, slots(lane_length(lane) - cores), nmax, nmax + 1))
    worst = None
    for junc in t.junctions:
        for lane in t.lanes:
            if lane.cyclic and any(m["lane"] == lane.id for m in junc["members"]):
                _, gap = junction_span(t, junc, lane)
                length = sum(math.hypot(lane.cells[k][2] - lane.cells[k][0], lane.cells[k][3] - lane.cells[k][1]) for k in gap)
                free = slots(length)
                if worst is None or free < worst[0]:
                    worst = (free, junc["id"])
    if worst:
        lines.append("V2 smallest complement of a junction's span: %d slots (%s), needs %d" % (worst[0], worst[1], nmax - 1))
    for s in d["stands"]:
        lines.append("V3 stand %s (%s) in %s: clearance %.2f m, lanes %s, %s" % (s["id"], "/".join(s["kinds"]), s["zone"], s["clearance_m"], s["in"], s["out"]))
    for st in d["stations"]:
        lines.append("V4 station %s: core %s, buffer %s, capacity %d, window %.3f s, headway %.3f s, nearest approach %.3f m%s"
                     % (st["id"], st["core"], st["buffer"], st["capacity"], st["window_s"], st["headway_s"], st["min_gap_m"],
                        (", loader %s %.3f m" % (st["partner"]["machine"], st["partner"]["min_gap_m"])) if st["partner"] else ""))
    lines.append("V5 %d junction boxes: %s" % (len(d["junctions"]), ", ".join("%s (%s)" % (j["id"], j["kind"]) for j in d["junctions"])))
    lines.append("V8 zones: %s" % "; ".join("%s %s" % (z["token"], "/".join(z["kinds"])) for z in d["zones"]))
    lines.append("lanes: %s" % ", ".join("%s %d" % (l.id, len(l)) for l in t.lanes))
    return lines


CONTROLS = 18

# The best stand the search for one found in the yard (the one zone with room): the pose stand_room finds in the yard's north-east patch and the two lanes
# that reach and leave it. selftest builds the topology with it through the generator's own path and the checks refuse it: its lanes run over each other
# and over the refuel bay's, and a box lies on its pose.
CANDIDATE_YARD_STAND = dict(
    id="yard-1", zone="sp-zone-yard", kinds=("Hauler", "Loader", "Dozer"), pose=(-60.0, -25.0, 270.0),
    exit=(-48.8, -39.0), program_in=[("R", 2.64), ("S", 0.16), ("R", 7.48), ("L", 45), ("R", 22.5), ("L", 22.5), ("R", 22.5), ("L", 90), ("S", 6)],
    join=(-24.7, -27.5), program_out=[("S", 6), ("R", 225), ("S", 7.5), ("L", 22.5), ("R", 22.5), ("L", 45), ("S", 5), ("R", 22.5), ("S", 2.5), ("L", 22.5), ("S", 15), ("L", 0.78), ("S", 0.48), ("L", 5.75)])


def selftest(inputs, verbose=False):
    """Each check must FAIL on a site made to break it, with its own message, and pass one that breaks nothing."""
    import copy
    d, base = assemble(inputs)
    cases = [("the real site", not base, base)]
    pre = len(cases)

    def case(name, mutate, validators, expect, passes=False):
        d2 = copy.deepcopy(d)
        mutate(d2)
        got = run_checks(Topo(d2), inputs, only=validators)
        ok = (not got) if passes else any(expect in x for x in got)
        cases.append((name, ok, [x for x in got if expect in x] + [x for x in got if expect not in x] if expect else got))

    def loop_of(d2):
        return next(l for l in d2["lanes"] if l["id"] == "loop")

    def ring(d2, slots_n, core_cells, machines):
        n = int(math.ceil(slots_n * SLOT_M / CELL_M))
        loop = loop_of(d2)
        loop["cells"] = [[0.0, 0.0, 2.0, 0.0, 0.0] for _ in range(n)]
        d2["fleet"]["machines"] = machines
        d2["stations"] = [dict(st, core=[0, core_cells - 1]) for st in d2["stations"][:1]]
        d2["junctions"] = []
    # V1: a 6-truck loop of 7 slots with a one-slot core is short; one of 9 is not
    case("V1: a 6-truck loop of 7 slots with a 1-slot core", lambda d2: ring(d2, 7, 6, 6), [v1_cycle_capacity], "V1 P1")
    case("V1: the same with 9 slots (control)", lambda d2: ring(d2, 9, 6, 6), [v1_cycle_capacity], None, passes=True)

    # V2: a junction span that leaves too little of the loop
    def widen(complement_cells):
        def go(d2):
            loop = loop_of(d2)
            n = len(loop["cells"])
            j = next(j for j in d2["junctions"] if any(m["lane"] == "loop" for m in j["members"]))
            j["members"] = [m for m in j["members"] if m["lane"] != "loop"] + [dict(lane="loop", cells=[0, n - 1 - complement_cells])]
            j["room"] = [r for r in j["room"] if r["lane"] != "loop"]
            d2["stations"] = []
            d2["junctions"] = [j]
        return go
    need = d["fleet"]["machines"] - 1
    cells_for = int(math.ceil(need * SLOT_M / CELL_M))
    case("V2: a box widened to the whole loop but one slot", widen(int(SLOT_M / CELL_M)), [v2_span_complement], "V2 P2")
    case("V2: a complement of exactly N-1 slots (control)", widen(cells_for), [v2_span_complement], None, passes=True)
    case("V2: one cell short of it", widen(cells_for - 2), [v2_span_complement], "V2 P2")

    # V3: a stand on a loop cell; the bay left back the way it came
    def stand_on_loop(d2):
        c = loop_of(d2)["cells"][60]
        d2["stands"][0]["pose"] = [(c[0] + c[2]) / 2.0, (c[1] + c[3]) / 2.0, qf.heading_of(c[2] - c[0], c[3] - c[1])]
    case("V3: a stand on a loop cell", stand_on_loop, [v3_stands], "V3 stand")

    def reverse_bay(d2):
        by = {l["id"]: l for l in d2["lanes"]}
        by[d2["bay"]["out"]]["cells"] = [[c[2], c[3], c[0], c[1]] for c in reversed(by[d2["bay"]["in"]]["cells"])]
    case("V3: the bay with out = the reverse of in", reverse_bay, [v3_stands], "V3 cul-de-sac")

    # V4: a headway under the fleet's, a buffer one slot for a capacity of two
    case("V4: a headway 0.9 of the fleet's", lambda d2: d2["stations"][0].update(headway_s=r3(d2["stations"][0]["headway_s"] * 0.9)), [v4_stations], "V4 station load: its headway")

    def short_buffer(d2):
        pad = next(s for s in d2["stations"] if s["id"] == "pad")
        pad["buffer"] = [pad["buffer"][0], pad["buffer"][0] + 6]
    case("V4: a buffer of one slot for a capacity of two", short_buffer, [v4_stations], "exit buffer holds")

    # V5: the ramp top's box deleted; an approach cell made a member
    case("V5: the ramp-top box deleted", lambda d2: d2.update(junctions=[j for j in d2["junctions"] if "ramp-top" not in j["id"]]), [v5_boxes], "V5 conflict")

    def reach_back(d2):
        j = next(j for j in d2["junctions"] if any(a["lane"] == "loop" for a in j["approach"]))
        ap = next(a for a in j["approach"] if a["lane"] == "loop")
        for m in j["members"]:
            if m["lane"] == "loop" and (m["cells"][0] - 1) % len(loop_of(d2)["cells"]) == ap["cell"]:
                m["cells"][0] = ap["cell"]
    case("V5: a box that includes the cell it is approached by", reach_back, [v5_boxes], "V5 approach")

    # V6: a lane cell on the light tower
    def onto_tower(d2):
        lane = next(l for l in d2["lanes"] if l["id"] == "road/fill-road/back")
        c = lane["cells"][5]
        tower = min((p for p in inputs.feats["props"] if p["p"] == "light_tower"), key=lambda p: math.hypot(p["x"] - c[0], p["z"] - c[1]))
        c[0], c[1] = tower["x"], tower["z"]
    case("V6: a lane cell moved onto a light tower", onto_tower, [v6_drivable], "V6 reach")

    def up_the_wall(d2):
        lane = next(l for l in d2["lanes"] if l["id"] == "road/fill-road/back")
        for k, c in enumerate(lane["cells"]):
            c[0], c[1], c[2], c[3] = 0.0, 56.0 + 2.0 * k, 0.0, 58.0 + 2.0 * k
    case("V6: a lane run straight up the pit's north wall", up_the_wall, [v6_drivable], "V6 grade")

    # V7: a byte of the fleet's hash
    def stale(d2):
        h = d2["source"]["fleet"]
        d2["source"]["fleet"] = h[:-1] + ("0" if h[-1] != "0" else "1")
    case("V7: one byte of the fleet hash changed", stale, [v7_fresh], "V7 fresh: its fleet hash")

    # V8: a zone that must have a stand for a kind and has none
    case("V8: a zone asked for a stand for a hauler it has none of", lambda d2: d2["zones"][0].update(kinds=["Hauler"]), [v8_zones], "V8 zone: sp-zone-cut has no stand")

    # V9: an access lane through SP-DZ-0002's rip area
    def through_rip(d2):
        wa = next(w for w in d2["work_areas"] if w["machine"] == "SP-DZ-0002")
        cx = sum(p[0] for p in wa["polygon"]) / len(wa["polygon"])
        cz = sum(p[1] for p in wa["polygon"]) / len(wa["polygon"])
        lane = next(l for l in d2["lanes"] if l["id"].startswith("access/"))
        lane["cells"][3][0], lane["cells"][3][1] = cx, cz
    case("V9: an access lane through SP-DZ-0002's rip area", through_rip, [v9_work_areas], "V9 work area")

    # a stand through the generator's own path: the yard's best candidate is refused
    saved = list(STANDS)
    STANDS[:] = [CANDIDATE_YARD_STAND]
    try:
        _, refused = assemble(inputs)
    finally:
        STANDS[:] = saved
    cases.append(("a stand in the yard, lanes drawn through the generator", any("V3 stand yard-1" in x for x in refused), [x for x in refused if "V3 stand yard-1" in x]))

    print("selftest on the real site: %s" % ("clean" if not base else "%d defects" % len(base)))
    for name, ok, got in cases:
        print("  %-62s %s%s" % (name, "ok" if ok else "NOT CAUGHT", "" if ok or not got else "  " + "; ".join(got[:2])))
        if verbose and ok and got:
            print("      %s" % got[0])
    caught = sum(1 for _, ok, _ in cases if ok)
    print("selftest: %d of %d controls behaved" % (caught, len(cases)))
    if len(cases) != CONTROLS or caught != CONTROLS:
        sys.exit(2)


def main():
    default_data = os.path.join(HERE, "..", "..", "Assets", "Sitepulse", "Data")
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", nargs="?", choices=("generate", "check", "selftest"), default="generate",
                    help="check: build the topology in memory, run every check on it and on the committed file, write nothing; "
                         "selftest: show each check failing on a site made to break it")
    ap.add_argument("--terrain", default=os.path.join(HERE, "..", "..", "Assets", "Sitepulse", "Art", "Terrain"))
    ap.add_argument("--data", default=default_data)
    ap.add_argument("--out", default=os.path.join(default_data, "quarry_topology.json"))
    ap.add_argument("--verbose", action="store_true", help="selftest: also print the message each control fails with")
    a = ap.parse_args()
    inputs = Inputs(a.terrain, a.data)
    if a.mode == "selftest":
        selftest(inputs, a.verbose)
        return
    d, defects = assemble(inputs)
    t = Topo(d)
    for line in report(t, inputs):
        print(line)
    committed = None
    if a.mode == "check" and os.path.exists(a.out):
        with open(a.out) as f:
            committed = json.load(f)
        if committed != d:
            defects.append("the committed %s is not what the generator makes of the committed inputs (regenerate it)" % os.path.basename(a.out))
        defects += ["committed file: " + x for x in run_checks(Topo(committed), inputs)]
        defects += exemptions_needed(Topo(committed), inputs)
    for x in defects:
        print("DEFECT: " + x)
    if defects:
        sys.exit(2)
    if a.mode == "generate":
        os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)
        with open(a.out, "w", newline="\n") as f:
            f.write(dump_json(d))


if __name__ == "__main__":
    main()
