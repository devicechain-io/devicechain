# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Sitepulse quarry terrain: a procedural heightmap, terrain-layer masks and a feature file.

    python3 quarry_heightmap.py [--seed N] [--out DIR] [--raw DIR] [--preview DIR]

Requires Python 3.8+, numpy and Pillow. Deterministic: the same seed and parameters give
byte-identical output.

WHAT IT BUILDS
  A 1024 m x 1024 m terrain with the ~220 m x 160 m work site in the middle and gentle hills
  out to the horizon. The site, in site-local metres (X east, Z north, origin at the centre of
  the terrain; elevations in metres above an arbitrary datum, with the yard pad at 100 m):

    * CUT     an open pit, floor at 90 m, cut into the south slope of a hill. Its walls are
              benched: 9 m faces at ~72 degrees with 7 m berms, as many benches as the hill is
              high (four on the north wall, one or two elsewhere).
    * RAMP    a 16 m two-way haul ramp from the pit floor up to the yard level at about 9-10 %
              grade, cut into the pit's south wall, with a safety windrow on its drop side.
    * FILL    a dump pad at 98 m pushed out over a gully to the east, with a tipping face at the
              angle of repose and a windrow along its crest.
    * YARD    a level pad at 100 m (parking, workshop, site office, containers) with the refuel
              bay at its south-east corner.
    * ROADS   haul and service roads joining them into one loop (cut -> fill -> yard -> cut).
              Every road is a centreline with a design elevation profile; cuts and fills
              meet the natural ground with batters.
    * PILES   a muck pile at the toe of the north face, stockpiles in the yard and tipped heaps
              on the dump pad.

OUTPUT (into --out, default ../../Assets/Sitepulse/Art/Terrain)
  quarry_height.bytes        the 16-bit heightmap, packed for the repository: gzip of the
                             plane-predicted samples (each stored as s - (west + south - southwest),
                             mod 65536, little-endian uint16). Unpacked it is the RAW below.
  quarry_height.raw          (only with --raw DIR, not committed) the plain 16-bit unsigned RAW,
                             little-endian, HEIGHT_RES x HEIGHT_RES, row-major, which Unity's
                             terrain "Import Raw" also reads (byte order Windows, flip vertically).
                             Row 0 is the SOUTH edge (z = -512), column 0 the WEST edge. A sample
                             s maps to elevation ELEV_MIN + s / 65535 * ELEV_RANGE.
  quarry_splat_<layer>.png   8-bit masks for the four terrain layers (rock, gravel, dirt,
                             grass), SPLAT_RES square. The image's top row is NORTH. At each
                             texel the four masks sum to 255.
  quarry_features.json       terrain size and elevation mapping, zones, the road network
                             (centrelines with per-segment grade, for later route graphs), named
                             spots, vegetation instances and prop placements. Positions are in
                             Unity metres: x east, z north, y = elevation - DATUM (the yard pad
                             is y = 0).

  --preview DIR also writes a shaded relief map with the layers and roads drawn on it.
"""
import argparse
import gzip
import json
import math
import os

import numpy as np
from PIL import Image

# ==================================================================================
# parameters
# ==================================================================================
SEED = 20261004
WORLD = 1024.0            # terrain edge length (m); the site is centred
HEIGHT_RES = 2049         # heightmap samples per edge (Unity needs 2^n + 1): 0.5 m spacing
SPLAT_RES = 1024          # terrain-layer mask texels per edge: 1 m per texel
ELEV_MIN = 60.0           # elevation of heightmap sample 0 (m)
ELEV_RANGE = 160.0        # elevation span of the heightmap (m) -> Unity TerrainData.size.y
DATUM = 100.0             # elevation that maps to Unity y = 0 (the yard pad)
CELL = WORLD / (HEIGHT_RES - 1)

PIT = dict(                # the cut
    rect=(-35.0, 55.0, 20.0, 52.0),   # floor x0, x1, z0, z1
    corner=8.0,                         # floor corner radius
    floor=90.0,
    bench_h=9.0,                        # face height
    face_deg=72.0,                      # face angle
    berm=7.0,                           # berm width
    edge_noise=0.9,                     # +- metres of irregularity along the faces
)

PADS = [                   # level platforms: name, (x0, x1, z0, z1), corner, elevation, cut and fill batters (rise per run)
    dict(name="yard", rect=(-108.0, -46.0, -76.0, -14.0), corner=6.0, elev=100.0, cut=0.67, fill=0.5),
    dict(name="fill", rect=(78.0, 108.0, -74.0, -36.0), corner=6.0, elev=98.0, cut=0.67, fill=0.73),
]

# Roads: control points (x, z, elevation). The centreline is a Catmull-Rom spline through the
# points; the design elevation is linear in arc length between control points (None: graded
# evenly between its neighbours, so a ramp has one constant grade). width is the
# full running width; cut/fill are batter steepness (rise per metre of run) where the road
# meets the existing surface; windrow raises a safety bund on any edge that drops > 1 m.
ROADS = [
    dict(name="pit-ramp", kind="haul-ramp", width=16.0, cut=2.5, fill=2.5, windrow=True,
         pts=[(-24.0, 30.0, 90.0), (-27.0, 20.0, 90.0), (-14.0, 11.0, None), (20.0, 10.0, None),
              (54.0, 10.5, None), (68.0, 2.0, 100.0), (71.0, -12.0, 100.0)]),
    dict(name="fill-road", kind="haul-road", width=14.0, cut=0.67, fill=0.6, windrow=True,
         pts=[(71.0, -12.0, 100.0), (80.0, -22.0, 99.2), (88.0, -32.0, 98.2), (92.0, -40.0, 98.0)]),
    dict(name="fill-return", kind="haul-road", width=12.0, cut=0.67, fill=0.5, windrow=True,
         pts=[(80.0, -66.0, 98.0), (60.0, -70.0, 98.8), (30.0, -68.0, 99.6), (0.0, -64.0, 100.0),
              (-30.0, -62.0, 100.0), (-44.0, -62.0, 100.0)]),
    dict(name="yard-road", kind="haul-road", width=12.0, cut=0.67, fill=0.5, windrow=False,
         pts=[(-44.0, -30.0, 100.0), (-10.0, -30.0, 100.0), (30.0, -26.0, 100.0), (55.0, -20.0, 100.0),
              (71.0, -12.0, 100.0)]),
]

PILES = [                  # cones at the angle of repose: name, centre, peak height above base, base elevation
    dict(name="muck-pile", x=-2.0, z=57.0, h=7.0, base=90.0, kind="muck"),
    dict(name="pit-stockpile", x=47.0, z=44.0, h=4.5, base=90.0, kind="stockpile"),
    dict(name="yard-stockpile-1", x=-98.0, z=-22.0, h=5.0, base=100.0, kind="stockpile"),
    dict(name="yard-stockpile-2", x=-84.0, z=-21.0, h=4.0, base=100.0, kind="stockpile"),
    dict(name="fill-heap-1", x=97.0, z=-66.0, h=2.2, base=98.0, kind="heap"),
    dict(name="fill-heap-2", x=102.0, z=-58.0, h=2.0, base=98.0, kind="heap"),
    dict(name="fill-heap-3", x=89.0, z=-70.0, h=1.8, base=98.0, kind="heap"),
]
REPOSE_DEG = 37.0

ZONES = [                  # labelled areas (Unity x/z bounds), matching the platform's area tokens
    dict(token="sp-zone-cut", label="Cut", rect=(-35.0, 55.0, 20.0, 52.0)),
    dict(token="sp-zone-fill", label="Fill", rect=(78.0, 108.0, -74.0, -36.0)),
    dict(token="sp-zone-yard", label="Yard", rect=(-108.0, -46.0, -76.0, -14.0)),
]

SPOTS = {                  # named places the scene and the simulation refer to (x, z, heading deg from north)
    "refuel-bay": (-56.0, -66.0, 90.0),
    "refuel-queue": (-72.0, -66.0, 90.0),
    "workshop": (-70.0, -36.0, 0.0),
    "site-office": (-96.0, -64.0, 0.0),
    "parking": (-96.0, -45.0, 90.0),
    "load-point": (4.0, 33.0, 90.0),
    "dump-point": (101.0, -51.0, 270.0),
}

VEGETATION = dict(
    spacing=6.5,           # jittered grid pitch (m)
    clearance=14.0,        # keep this far from any worked ground
    max_slope_deg=30.0,
    forest_cover=0.10,     # fBm threshold for forest patches (lower = more forest)
    sparse_keep=0.05,      # chance a candidate outside the forest patches still gets a tree
)

# ==================================================================================
# noise
# ==================================================================================
class ValueNoise:
    """Smooth 2D value noise on a seeded 256 x 256 lattice (wraps every 256 lattice cells)."""

    def __init__(self, seed):
        self.lat = np.random.default_rng(seed).random((256, 256)) * 2.0 - 1.0

    def __call__(self, x, z):
        xi, zi = np.floor(x), np.floor(z)
        fx, fz = x - xi, z - zi
        xi = xi.astype(np.int64) & 255
        zi = zi.astype(np.int64) & 255
        ux = fx * fx * fx * (fx * (fx * 6 - 15) + 10)       # quintic fade
        uz = fz * fz * fz * (fz * (fz * 6 - 15) + 10)
        L = self.lat
        a, b = L[zi, xi], L[zi, (xi + 1) & 255]
        c, d = L[(zi + 1) & 255, xi], L[(zi + 1) & 255, (xi + 1) & 255]
        return (a + (b - a) * ux) * (1 - uz) + (c + (d - c) * ux) * uz


def fbm(noise, x, z, wavelength, octaves=4, gain=0.5, offset=0.0):
    """Fractal sum of value noise, roughly in -1..1."""
    total, amp, norm, f = 0.0, 1.0, 0.0, 1.0 / wavelength
    for o in range(octaves):
        total = total + amp * noise(x * f + offset + 17.3 * o, z * f - offset + 31.7 * o)
        norm += amp
        amp *= gain
        f *= 2.0
    return total / norm


def smoothstep(e0, e1, v):
    t = np.clip((v - e0) / (e1 - e0), 0.0, 1.0)
    return t * t * (3 - 2 * t)

# ==================================================================================
# geometry helpers
# ==================================================================================
def sd_round_rect(X, Z, rect, r):
    """Signed distance to a rounded rectangle (negative inside)."""
    x0, x1, z0, z1 = rect
    cx, cz = (x0 + x1) / 2, (z0 + z1) / 2
    hx, hz = (x1 - x0) / 2 - r, (z1 - z0) / 2 - r
    qx, qz = np.abs(X - cx) - hx, np.abs(Z - cz) - hz
    out = np.hypot(np.maximum(qx, 0), np.maximum(qz, 0))
    return out + np.minimum(np.maximum(qx, qz), 0) - r


def catmull_rom(pts, step=1.0):
    """Centripetal Catmull-Rom through (x, z) points, sampled about every `step` metres.
    Returns the dense polyline and the index of each control point in it."""
    P = [np.array(p, float) for p in pts]
    P = [P[0] * 2 - P[1]] + P + [P[-1] * 2 - P[-2]]
    out, idx = [], []
    for i in range(1, len(P) - 2):
        p0, p1, p2, p3 = P[i - 1], P[i], P[i + 1], P[i + 2]
        t0 = 0.0
        t1 = t0 + max(np.linalg.norm(p1 - p0), 1e-6) ** 0.5
        t2 = t1 + max(np.linalg.norm(p2 - p1), 1e-6) ** 0.5
        t3 = t2 + max(np.linalg.norm(p3 - p2), 1e-6) ** 0.5
        n = max(2, int(math.ceil(np.linalg.norm(p2 - p1) / step)))
        idx.append(len(out))
        for k in range(n):
            t = t1 + (t2 - t1) * k / n
            a1 = (t1 - t) / (t1 - t0) * p0 + (t - t0) / (t1 - t0) * p1
            a2 = (t2 - t) / (t2 - t1) * p1 + (t - t1) / (t2 - t1) * p2
            a3 = (t3 - t) / (t3 - t2) * p2 + (t - t2) / (t3 - t2) * p3
            b1 = (t2 - t) / (t2 - t0) * a1 + (t - t0) / (t2 - t0) * a2
            b2 = (t3 - t) / (t3 - t1) * a2 + (t - t1) / (t3 - t1) * a3
            out.append((t2 - t) / (t2 - t1) * b1 + (t - t1) / (t2 - t1) * b2)
    idx.append(len(out))
    out.append(P[-2])
    return np.array(out), idx


def stations(poly):
    seg = np.hypot(np.diff(poly[:, 0]), np.diff(poly[:, 1]))
    return np.concatenate([[0.0], np.cumsum(seg)])

# ==================================================================================
# the terrain
# ==================================================================================
class Quarry:
    def __init__(self, seed):
        self.seed = seed
        self.noise = [ValueNoise(seed + k) for k in range(6)]
        c = (np.arange(HEIGHT_RES) * CELL - WORLD / 2)
        self.X, self.Z = np.meshgrid(c, c)            # [row = z, col = x]
        self.masks = {}
        self.roads = []

    # ---- natural ground ----------------------------------------------------------
    def natural(self):
        X, Z, n = self.X, self.Z, self.noise
        r = np.hypot(X, Z)
        h = DATUM + 2.2 * fbm(n[0], X, Z, 140.0, 4)                     # gentle rolling ground
        # the quarried hill: a broad ridge north of the site
        ridge = 50.0 * smoothstep(-10.0, 140.0, Z) * (0.8 + 0.2 * fbm(n[1], X, Z, 220.0, 3))
        ridge *= 1.0 - 0.55 * smoothstep(150.0, 330.0, np.abs(X - 30.0))
        h = h + ridge
        # a gully east of the site, for the fill to push out over
        gully = np.exp(-((X - 128.0) / 34.0) ** 2) * (1.0 - 0.6 * smoothstep(0.0, 160.0, Z))
        h = h - 15.0 * gully * (1.0 - smoothstep(170.0, 320.0, np.abs(Z + 40.0)))
        # hills out to the horizon, kept low near the site
        far = smoothstep(230.0, 470.0, r)
        h = h + far * (38.0 + 34.0 * fbm(n[2], X, Z, 260.0, 4, offset=5.0))
        h = h + 0.7 * fbm(n[3], X, Z, 22.0, 3)                          # small surface detail
        self.N = h
        self.H = h.copy()

    # ---- the pit -----------------------------------------------------------------
    def pit(self):
        p = PIT
        X, Z = self.X, self.Z
        d = sd_round_rect(X, Z, p["rect"], p["corner"])
        d = d + p["edge_noise"] * fbm(self.noise[4], X, Z, 9.0, 2)      # blast-irregular faces
        fw = p["bench_h"] / math.tan(math.radians(p["face_deg"]))
        period = fw + p["berm"]
        dd = np.maximum(d, 0.0)
        k = np.floor(dd / period)
        rr = dd - k * period
        rise = np.clip(rr / fw, 0.0, 1.0)
        rise = rise * rise * (3 - 2 * rise) * 0.25 + rise * 0.75         # soften crest and toe slightly
        stair = p["floor"] + (k + rise) * p["bench_h"]
        stair = stair + 0.12 * fbm(self.noise[5], X, Z, 6.0, 2) * (d < 0) # rough floor
        cut = stair < self.H
        self.H = np.where(cut, stair, self.H)
        self.masks["pit"] = cut
        self.masks["pit_floor"] = cut & (d <= 0.0)
        self.masks["berm"] = cut & (rr >= fw) & (d > 0.0)
        self.pit_fw = fw

    # ---- level pads --------------------------------------------------------------
    def pad(self, pd):
        X, Z = self.X, self.Z
        x0, x1, z0, z1 = pd["rect"]
        m = 60.0
        sl = self._window(x0 - m, x1 + m, z0 - m, z1 + m)
        Xs, Zs, H = X[sl], Z[sl], self.H[sl]
        d = np.maximum(sd_round_rect(Xs, Zs, pd["rect"], pd["corner"]), 0.0)
        e = pd["elev"]
        cut = np.minimum(H, e + d * pd["cut"])
        fill = np.maximum(H, e - d * pd["fill"])
        out = np.where(H > e, cut, fill)
        self.H[sl] = out
        mk = np.zeros_like(self.H, bool)
        mk[sl] = d <= 0.0
        self.masks["pad_" + pd["name"]] = mk

    def _window(self, x0, x1, z0, z1):
        c0 = max(0, int((x0 + WORLD / 2) / CELL))
        c1 = min(HEIGHT_RES, int((x1 + WORLD / 2) / CELL) + 2)
        r0 = max(0, int((z0 + WORLD / 2) / CELL))
        r1 = min(HEIGHT_RES, int((z1 + WORLD / 2) / CELL) + 2)
        return (slice(r0, r1), slice(c0, c1))

    # ---- piles -------------------------------------------------------------------
    def pile(self, pl):
        rad = pl["h"] / math.tan(math.radians(REPOSE_DEG))
        sl = self._window(pl["x"] - rad - 2, pl["x"] + rad + 2, pl["z"] - rad - 2, pl["z"] + rad + 2)
        Xs, Zs = self.X[sl], self.Z[sl]
        dist = np.hypot(Xs - pl["x"], Zs - pl["z"])
        ang = np.arctan2(Zs - pl["z"], Xs - pl["x"])
        wob = 1.0 + 0.08 * np.sin(3 * ang + pl["x"]) + 0.05 * np.sin(5 * ang + pl["z"])
        cone = pl["h"] - dist / wob * math.tan(math.radians(REPOSE_DEG))
        cone = np.where(cone > 0.6, cone, 0.6 * smoothstep(-0.6, 0.6, cone))   # soft toe
        top = pl["base"] + cone
        top = top + 0.15 * fbm(self.noise[3], Xs, Zs, 2.5, 2) * (cone > 0.2)
        cur = self.H[sl]
        self.H[sl] = np.maximum(cur, top)
        mk = self.masks.setdefault("pile", np.zeros_like(self.H, bool))
        mk[sl] |= top > cur + 0.05

    # ---- roads -------------------------------------------------------------------
    def road(self, rd):
        poly, ctrl = catmull_rom([(x, z) for x, z, _ in rd["pts"]], step=1.0)
        s = stations(poly)
        ctrl_s = [s[i] for i in ctrl]
        known = [(cs, e) for cs, (_, _, e) in zip(ctrl_s, rd["pts"]) if e is not None]
        elev = np.interp(s, [k[0] for k in known], [k[1] for k in known])
        w = rd["width"] / 2
        m = w + 40.0
        sl = self._window(poly[:, 0].min() - m, poly[:, 0].max() + m, poly[:, 1].min() - m, poly[:, 1].max() + m)
        Xs, Zs = self.X[sl], self.Z[sl]
        best = np.full(Xs.shape, np.inf)
        best_s = np.zeros(Xs.shape)
        for i in range(len(poly) - 1):
            ax, az = poly[i]
            bx, bz = poly[i + 1]
            vx, vz = bx - ax, bz - az
            L2 = vx * vx + vz * vz
            t = np.clip(((Xs - ax) * vx + (Zs - az) * vz) / L2, 0.0, 1.0)
            dx, dz = Xs - (ax + t * vx), Zs - (az + t * vz)
            dist = np.hypot(dx, dz)
            closer = dist < best
            best = np.where(closer, dist, best)
            best_s = np.where(closer, s[i] + t * (s[i + 1] - s[i]), best_s)
        er = np.interp(best_s, s, elev)
        H = self.H[sl]
        before = H.copy()
        dd = np.maximum(best - w, 0.0)
        cut = np.minimum(H, er + dd * rd["cut"])
        fill = np.maximum(H, er - dd * rd["fill"])
        H = np.where(H > er, cut, fill)
        core = best <= w
        if rd["windrow"]:
            # a bund on any edge that drops more than a metre: about 1.2 m high, 3 m wide
            band = (best > w - 3.0) & (best <= w)
            drop = band & (before < er - 1.0)
            prof = np.sin(np.clip((best - (w - 3.0)) / 3.0, 0, 1) * math.pi)
            H = np.where(drop, H + 1.2 * prof, H)
            wm = self.masks.setdefault("windrow", np.zeros_like(self.H, bool))
            wm[sl] |= drop & (prof > 0.2)
            core = core & ~(drop & (prof > 0.2))
        self.H[sl] = H
        rm = self.masks.setdefault("road", np.zeros_like(self.H, bool))
        rm[sl] |= core
        seg_grade = np.diff(elev) / np.maximum(np.diff(s), 1e-6)
        self.roads.append(dict(rd, poly=poly, s=s, elev=elev, grade=seg_grade))

    # ---- build ---------------------------------------------------------------------
    def build(self):
        self.natural()
        self.pit()
        for pd in PADS:
            self.pad(pd)
        for rd in ROADS:
            self.road(rd)
        for pl in PILES:
            self.pile(pl)
        self.H = np.clip(self.H, ELEV_MIN + 0.5, ELEV_MIN + ELEV_RANGE - 0.5)

    # ---- analysis ------------------------------------------------------------------
    def slope_deg(self, H=None):
        H = self.H if H is None else H
        gz, gx = np.gradient(H, CELL)
        return np.degrees(np.arctan(np.hypot(gx, gz)))

    def splat(self):
        """Weights for rock, gravel, dirt, grass at heightmap resolution, then box-downsampled."""
        H, N, mk = self.H, self.N, self.masks
        X, Z, n = self.X, self.Z, self.noise
        slope = self.slope_deg()
        disturbed = np.abs(H - N) > 0.25
        worked = mk["road"] | mk["pad_yard"] | mk["pad_fill"] | mk["pit_floor"]
        rock = smoothstep(38.0, 50.0, slope)
        rock = np.maximum(rock, mk["berm"] * 0.35)                       # rubble on the berms
        grav = worked * 1.0 + mk["berm"] * 0.65
        grav = np.where(mk["pit_floor"], 0.75 + 0.25 * fbm(n[5], X, Z, 14.0, 2), grav)
        dirt = (disturbed & ~worked) * 1.0 + mk.get("pile", 0) * 1.0 + mk.get("windrow", 0) * 1.0
        dirt = dirt + mk["pit_floor"] * 0.3 * (fbm(n[4], X, Z, 11.0, 3) > 0.1)
        # natural ground: grass, thinning to dirt on steeper ground and in patches
        patch = smoothstep(0.25, 0.55, fbm(n[1], X, Z, 35.0, 4, offset=9.0))
        natural = ~disturbed
        grass = natural * (1.0 - smoothstep(24.0, 36.0, slope)) * (1.0 - 0.85 * patch)
        dirt = dirt + natural * (1.0 - grass) * 0.9
        # vehicle wear: dirt fringes either side of the roads
        grass = grass * (1.0 - 0.6 * self._near(mk["road"] | mk["pad_yard"], 4.0))
        w = np.stack([rock, grav * (1 - rock), dirt * (1 - rock), grass * (1 - rock)])
        w = np.maximum(w, 0.0)
        w[2] = np.where(w.sum(0) < 1e-3, 1.0, w[2])
        w = w / w.sum(0)
        # 2049 -> 2048 -> SPLAT_RES box filter
        f = (HEIGHT_RES - 1) // SPLAT_RES
        w = w[:, :-1, :-1].reshape(4, SPLAT_RES, f, SPLAT_RES, f).mean((2, 4))
        self.weights = w
        self.slope = slope
        self.disturbed = disturbed
        return w

    def _near(self, mask, radius):
        """Soft 0..1 'within radius metres of mask' using a box blur via summed-area table."""
        k = max(1, int(radius / CELL))
        a = mask.astype(np.float64)
        S = np.pad(a, ((1, 0), (1, 0))).cumsum(0).cumsum(1)
        r = np.arange(a.shape[0])
        lo, hi = np.clip(r - k, 0, a.shape[0]), np.clip(r + k + 1, 0, a.shape[0])
        box = S[hi][:, hi] - S[lo][:, hi] - S[hi][:, lo] + S[lo][:, lo]
        cnt = (hi - lo)[:, None] * (hi - lo)[None, :]
        return np.clip(box / cnt * 3.0, 0.0, 1.0)

    # ---- vegetation and props -------------------------------------------------------
    def vegetation(self):
        v = VEGETATION
        rng = np.random.default_rng(self.seed + 101)
        near = self._near(self.disturbed, v["clearance"]) > 0.0
        sp = v["spacing"]
        g = np.arange(-WORLD / 2 + sp / 2, WORLD / 2 - 2, sp)
        gx, gz = np.meshgrid(g, g)
        gx = gx + rng.uniform(-0.45, 0.45, gx.shape) * sp
        gz = gz + rng.uniform(-0.45, 0.45, gz.shape) * sp
        gx, gz = gx.ravel(), gz.ravel()
        ci = np.clip(np.round((gx + WORLD / 2) / CELL).astype(int), 0, HEIGHT_RES - 1)
        ri = np.clip(np.round((gz + WORLD / 2) / CELL).astype(int), 0, HEIGHT_RES - 1)
        ok = (~near[ri, ci]) & (self.slope[ri, ci] < v["max_slope_deg"])
        ok &= (np.abs(gx) < WORLD / 2 - 6) & (np.abs(gz) < WORLD / 2 - 6)
        forest = fbm(self.noise[2], gx, gz, 90.0, 4, offset=3.0)
        dense = forest > v["forest_cover"]
        keep = ok & (dense | (rng.random(gx.shape) < v["sparse_keep"]))
        # thin the forest a little at its edges
        keep &= ~(dense & (forest < v["forest_cover"] + 0.06) & (rng.random(gx.shape) < 0.5))
        out = []
        r = rng.random(gx.shape)
        for i in np.nonzero(keep)[0]:
            elevation = self.H[ri[i], ci[i]]
            hi_ground = elevation > DATUM + 18.0
            if dense[i]:
                kind = "pine" if (r[i] < 0.62 or hi_ground) else ("broadleaf" if r[i] < 0.9 else "shrub")
            else:
                kind = "shrub" if r[i] < 0.45 else ("broadleaf" if r[i] < 0.75 else "pine")
            s = float(rng.uniform(0.8, 1.25))
            out.append(dict(p=kind, x=round(float(gx[i]), 2), z=round(float(gz[i]), 2),
                            s=round(s, 3), h=round(s * float(rng.uniform(0.9, 1.15)), 3),
                            r=round(float(rng.uniform(0, 360)), 1)))
        # loose boulders: at the toes of the faces, on the berms and around the fill
        cand = np.argwhere((self.masks["berm"] | self.masks["pad_fill"] | self.masks["pit_floor"])
                           & (self._near(self.slope > 45.0, 2.5) > 0) & ~self.masks["road"]
                           & ~self.masks.get("pile", np.zeros_like(self.H, bool)))
        if len(cand):
            pick = cand[rng.choice(len(cand), size=min(170, len(cand)), replace=False)]
            for ri_, ci_ in pick:
                x, z = float(self.X[ri_, ci_]), float(self.Z[ri_, ci_])
                if _in_rect(x, z, PADS[1]["rect"], -6) and x < 104:                   # keep the pad's working area clear
                    continue
                s = float(rng.uniform(0.5, 1.4))
                out.append(dict(p="rock_a" if rng.random() < 0.5 else "rock_b", x=round(x, 2), z=round(z, 2),
                                s=round(s, 3), h=round(s * float(rng.uniform(0.7, 1.0)), 3),
                                r=round(float(rng.uniform(0, 360)), 1)))
        # boulders scattered in the countryside
        for _ in range(260):
            x, z = rng.uniform(-WORLD / 2 + 20, WORLD / 2 - 20, 2)
            c = int((x + WORLD / 2) / CELL); rr_ = int((z + WORLD / 2) / CELL)
            if near[rr_, c] or self.slope[rr_, c] > 32:
                continue
            s = float(rng.uniform(0.6, 1.8))
            out.append(dict(p="rock_a" if rng.random() < 0.5 else "rock_b", x=round(float(x), 2),
                            z=round(float(z), 2), s=round(s, 3), h=round(s * 0.8, 3),
                            r=round(float(rng.uniform(0, 360)), 1)))
        return out


def _in_rect(x, z, rect, inset=0.0):
    x0, x1, z0, z1 = rect
    return x0 + inset <= x <= x1 - inset and z0 + inset <= z <= z1 - inset


# props: prefab name, x, z, heading (deg, clockwise from north)
PROPS = [
    # yard: office, workshop, containers, parking, sign, light
    ("site_office", -96.0, -66.0, 0.0),
    ("site_office", -96.0, -56.5, 0.0),
    ("workshop", -70.0, -36.0, 180.0),
    ("container_blue", -104.0, -40.0, 90.0),
    ("container_red", -100.5, -40.0, 90.0),
    ("container_blue", -104.0, -48.0, 90.0),
    ("container_red", -86.0, -74.0, 0.0),
    ("fuel_tank", -56.0, -73.5, 0.0),
    ("light_tower", -60.0, -58.0, 225.0),
    ("light_tower", -48.0, -20.0, 200.0),
    ("light_tower", 30.0, 26.0, 330.0),
    ("light_tower", 90.0, -34.0, 160.0),
    ("site_sign", -40.0, -24.0, 270.0),
] + [("cone", -64.0 + 3.0 * i, -60.0, 0.0) for i in range(6)] \
  + [("cone", -50.0, -61.0 - 2.5 * i, 0.0) for i in range(4)] \
  + [("barrier", -46.0, -40.0 - 3.2 * i, 90.0) for i in range(6)] \
  + [("barrier", -108.0 + 3.2 * i, -78.5, 0.0) for i in range(5)]


def elevation_at(q, x, z):
    c = (x + WORLD / 2) / CELL
    r = (z + WORLD / 2) / CELL
    c0, r0 = int(c), int(r)
    fc, fr = c - c0, r - r0
    H = q.H
    return float(H[r0, c0] * (1 - fc) * (1 - fr) + H[r0, c0 + 1] * fc * (1 - fr)
                 + H[r0 + 1, c0] * (1 - fc) * fr + H[r0 + 1, c0 + 1] * fc * fr)


def features(q, veg):
    roads = []
    for rd in q.roads:
        s, poly, el = rd["s"], rd["poly"], rd["elev"]
        keep = [0]
        for i in range(1, len(s)):                          # thin the polyline to ~4 m
            if s[i] - s[keep[-1]] >= 4.0 or i == len(s) - 1:
                keep.append(i)
        pts = [[round(float(poly[i, 0]), 2), round(float(el[i] - DATUM), 3), round(float(poly[i, 1]), 2)] for i in keep]
        grades = [round(float((el[b] - el[a]) / max(s[b] - s[a], 1e-6) * 100.0), 2) for a, b in zip(keep, keep[1:])]
        roads.append(dict(name=rd["name"], kind=rd["kind"], width=rd["width"], length_m=round(float(s[-1]), 1),
                          max_grade_pct=round(max(abs(g) for g in grades), 2),
                          points=pts, grade_pct=grades))
    return dict(
        generator="ArtSource/terrain/quarry_heightmap.py", seed=q.seed,
        terrain=dict(size_m=WORLD, height_res=HEIGHT_RES, splat_res=SPLAT_RES, elev_min=ELEV_MIN,
                     elev_range=ELEV_RANGE, datum=DATUM,
                     position=[-WORLD / 2, ELEV_MIN - DATUM, -WORLD / 2],
                     layers=["rock", "gravel", "dirt", "grass"]),
        pit=dict(PIT, floor_y=PIT["floor"] - DATUM, face_width_m=round(q.pit_fw, 3)),
        pads=[dict(name=p["name"], rect=p["rect"], y=p["elev"] - DATUM) for p in PADS],
        zones=ZONES,
        roads=roads,
        piles=[dict(p, y=p["base"] - DATUM) for p in PILES],
        spots={k: dict(x=v[0], z=v[1], y=round(elevation_at(q, v[0], v[1]) - DATUM, 3), heading=v[2])
               for k, v in SPOTS.items()},
        props=[dict(p=n, x=x, z=z, heading=hd) for n, x, z, hd in PROPS],
        vegetation=veg,
    )


def write(q, w, feats, out, raw_dir=None):
    os.makedirs(out, exist_ok=True)
    s = np.round((q.H - ELEV_MIN) / ELEV_RANGE * 65535.0)
    s = np.clip(s, 0, 65535).astype(np.int64)            # row 0 = south
    if raw_dir:
        os.makedirs(raw_dir, exist_ok=True)
        s.astype("<u2").tofile(os.path.join(raw_dir, "quarry_height.raw"))
    pred = np.zeros_like(s)
    pred[:, 1:] += s[:, :-1]
    pred[1:, :] += s[:-1, :]
    pred[1:, 1:] -= s[:-1, :-1]
    resid = ((s - pred) % 65536).astype("<u2")
    # mtime=0 keeps the gzip header, and so the file, identical from run to run
    with open(os.path.join(out, "quarry_height.bytes"), "wb") as f:
        f.write(gzip.compress(resid.tobytes(), compresslevel=9, mtime=0))
    q8 = np.floor(w * 255.0).astype(np.int32)
    rem = 255 - q8.sum(0)                                 # give the rounding remainder to the strongest layer
    top = np.argmax(w, 0)
    for k in range(4):
        q8[k] += np.where(top == k, rem, 0)
    for k, name in enumerate(["rock", "gravel", "dirt", "grass"]):
        Image.fromarray(np.flipud(q8[k]).astype(np.uint8), "L").save(
            os.path.join(out, f"quarry_splat_{name}.png"), optimize=True)
    with open(os.path.join(out, "quarry_features.json"), "w") as f:
        json.dump(feats, f, separators=(",", ":"))
        f.write("\n")


def preview(q, w, feats, out, crop=180.0):
    """Shaded relief of the site (top = north) with layer colours, roads and vegetation."""
    os.makedirs(out, exist_ok=True)
    H = q.H
    gz, gx = np.gradient(H, CELL)
    lx, ly, lz = -0.6, 0.55, 0.6                           # light from the north-west
    nrm = np.sqrt(gx * gx + gz * gz + 1)
    shade = np.clip((-gx * lx - gz * lz + ly) / nrm / 0.85, 0.15, 1.2)
    f = (HEIGHT_RES - 1) // SPLAT_RES
    shade = shade[:-1, :-1].reshape(SPLAT_RES, f, SPLAT_RES, f).mean((1, 3))
    cols = np.array([[0.55, 0.53, 0.50], [0.68, 0.66, 0.62], [0.55, 0.42, 0.30], [0.36, 0.48, 0.22]])
    rgb = np.einsum("kyx,kc->yxc", w, cols) * shade[..., None]
    img = Image.fromarray((np.clip(np.flipud(rgb), 0, 1) * 255).astype(np.uint8))
    from PIL import ImageDraw
    big = img.resize((SPLAT_RES * 2, SPLAT_RES * 2))
    dr = ImageDraw.Draw(big)
    ppm = SPLAT_RES * 2 / WORLD
    def px(x, z):
        return ((x + WORLD / 2) * ppm, (WORLD / 2 - z) * ppm)
    for rd in feats["roads"]:
        dr.line([px(p[0], p[2]) for p in rd["points"]], fill=(255, 230, 0), width=1)
    for v in feats["vegetation"]:
        c = {"pine": (10, 60, 20), "broadleaf": (40, 110, 30), "shrub": (90, 120, 40)}.get(v["p"], (120, 120, 120))
        x, y = px(v["x"], v["z"])
        dr.point((x, y), fill=c)
    for p in feats["props"]:
        x, y = px(p["x"], p["z"])
        dr.rectangle((x - 2, y - 2, x + 2, y + 2), outline=(255, 0, 255))
    big.save(os.path.join(out, "quarry_preview_full.png"))
    a, b = px(-crop * 1.3, crop), px(crop * 1.3, -crop)
    big.crop((int(a[0]), int(a[1]), int(b[0]), int(b[1]))).resize((1600, int(1600 * crop / (crop * 1.3)))).save(
        os.path.join(out, "quarry_preview_site.png"))


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--seed", type=int, default=SEED)
    ap.add_argument("--out", default=os.path.join(here, "..", "..", "Assets", "Sitepulse", "Art", "Terrain"))
    ap.add_argument("--raw", default=None, help="also write the plain RAW heightmap into this directory")
    ap.add_argument("--preview", default=None)
    a = ap.parse_args()
    q = Quarry(a.seed)
    q.build()
    w = q.splat()
    veg = q.vegetation()
    feats = features(q, veg)
    write(q, w, feats, a.out, a.raw)
    if a.preview:
        preview(q, w, feats, a.preview)
    counts = {}
    for v in veg:
        counts[v["p"]] = counts.get(v["p"], 0) + 1
    print("elevation range %.1f .. %.1f m" % (q.H.min(), q.H.max()))
    for rd in feats["roads"]:
        print("road %-12s %6.1f m  max grade %5.2f %%" % (rd["name"], rd["length_m"], rd["max_grade_pct"]))
    print("vegetation", counts)


if __name__ == "__main__":
    main()
