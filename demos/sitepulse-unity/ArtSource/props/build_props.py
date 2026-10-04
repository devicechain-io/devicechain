# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Sitepulse site props and vegetation, built as code with the machine kit.

    blender --background --python props/build_props.py -- [name ...] [--out DIR]

With no names every prop is built. Each prop is written as <name>.glb and <name>_LOD1.glb
(same node tree, as for the machines) plus one props_check.json with the triangle counts.

Same conventions as the machines (see sitepulse_kit.py): Unity metres, X right, Y up, Z forward,
origin on the ground at the prop's footprint centre. Every prop has one node, Body, under Root;
it does not move. A building's long axis is X and its open or front side faces +Z.

  pine, broadleaf, shrub     trees for Unity Terrain tree instances (scaled per instance)
  rock_a, rock_b             boulders (terrain tree instances, no collider)
  site_office                9.6 m portable site cabin on skids
  workshop                   18 m x 12 m open-fronted steel service shelter
  container_blue/_red        20 ft shipping container
  fuel_tank                  bunded 8 m horizontal diesel tank with a dispenser
  light_tower                trailer-mounted light tower (mast 8.5 m), lamps face +Z
  cone, barrier, site_sign   small site furniture
"""
import math
import os
import random
import sys
import json

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
import sitepulse_kit as K                                   # noqa: E402

import bmesh                                                # noqa: E402
from mathutils import Vector                                # noqa: E402


# ==================================================================================
# helpers on a kit Piece
# ==================================================================================
def blob(p, c, r, key, rng, scale=(1.0, 1.0, 1.0), subdiv=2, jitter=0.15):
    """A lumpy icosphere: canopy clumps, shrubs, boulders. subdiv 1 = 20 faces, 2 = 80, 3 = 320."""
    b0 = p._op()
    res = bmesh.ops.create_icosphere(p.bm, subdivisions=subdiv, radius=1.0)
    verts = res["verts"]
    for v in verts:
        n = v.co.normalized()
        k = 1.0 + rng.uniform(-jitter, jitter)
        v.co = Vector((c[0] + n.x * r * scale[0] * k, c[1] + n.y * r * scale[1] * k, c[2] + n.z * r * scale[2] * k))
    faces = list({f for v in verts for f in v.link_faces})
    p._finish(verts, faces, key, 0)
    p._tag(b0)


def spike_cone(p, c, r, h, key, rng, n=7, jag=0.3, droop=0.3):
    """A pine tier: a star-shaped cone whose outer points droop, with a shallow hollow underside."""
    b0 = p._op()
    x, y, z = c
    ring = []
    a0 = rng.uniform(0, math.pi)
    for i in range(2 * n):
        a = a0 + math.pi * i / n
        rr = r if i % 2 == 0 else r * (1.0 - jag)
        dy = -droop if i % 2 == 0 else 0.0
        rr *= 1.0 + rng.uniform(-0.08, 0.08)
        ring.append(p.bm.verts.new((x + rr * math.cos(a), y + dy, z + rr * math.sin(a))))
    apex = p.bm.verts.new((x + rng.uniform(-0.05, 0.05), y + h, z + rng.uniform(-0.05, 0.05)))
    under = p.bm.verts.new((x, y + 0.22 * h, z))
    faces = []
    for i in range(2 * n):
        j = (i + 1) % (2 * n)
        faces.append(p.bm.faces.new((ring[i], ring[j], apex)))
        faces.append(p.bm.faces.new((ring[j], ring[i], under)))
    p._finish(ring + [apex, under], faces, key, 0)
    p._tag(b0)


def single(name, piece, smooth=35):
    K.node("Root", None, (0, 0, 0))
    K.node("Body", "Root", (0, 0, 0))
    K.attach("Body", piece, smooth_angle=smooth)


# ==================================================================================
# vegetation
# ==================================================================================
def pine(rng):
    p = K.Piece()
    p.cyl((0, 0, 0), (0, 9.0, 0), 0.24, "bark", 7, r2=0.07)
    tiers = 5
    for i in range(tiers):
        f = i / (tiers - 1)
        y0 = 1.7 + i * 1.6
        spike_cone(p, (rng.uniform(-0.1, 0.1), y0, rng.uniform(-0.1, 0.1)), 2.35 * (1 - 0.72 * f),
                   2.7 - 0.5 * f, "pine", rng, n=7 if i < 3 else 6, jag=0.28, droop=0.45 * (1 - 0.6 * f))
    single("pine", p, smooth=25)


def broadleaf(rng):
    p = K.Piece()
    p.cyl((0, 0, 0), (0.05, 3.6, 0.02), 0.26, "bark", 7, r2=0.17)
    with p.detail():
        for a in (0.4, 2.5, 4.4):
            tip = (1.1 * math.cos(a), 4.6, 1.1 * math.sin(a))
            p.cyl((0.05, 3.3, 0.02), tip, 0.1, "bark", 5, r2=0.06)
    for k in range(7):
        a = k * 2.4 + rng.uniform(-0.3, 0.3)
        d = 1.3 if k else 0.0
        blob(p, (d * math.cos(a), 5.3 + rng.uniform(-0.6, 0.9) + (0.6 if k == 0 else 0), d * math.sin(a)),
             rng.uniform(1.5, 2.0), "leaf", rng, scale=(1.0, 0.85, 1.0), subdiv=2, jitter=0.16)
    single("broadleaf", p, smooth=25)


def shrub(rng):
    p = K.Piece()
    for k in range(4):
        a = k * 1.9
        d = 0.0 if k == 0 else 0.55
        blob(p, (d * math.cos(a), 0.55 + rng.uniform(-0.1, 0.15), d * math.sin(a)), rng.uniform(0.6, 0.85),
             "shrub", rng, scale=(1.0, 0.75, 1.0), subdiv=2, jitter=0.18)
    single("shrub", p, smooth=25)


def rock_a(rng):
    p = K.Piece()
    blob(p, (0, 0.3, 0), 1.0, "rock", rng, scale=(1.25, 0.7, 0.95), subdiv=3, jitter=0.12)
    single("rock_a", p, smooth=12)


def rock_b(rng):
    p = K.Piece()
    blob(p, (0, 0.35, 0), 0.9, "rock", rng, scale=(1.0, 0.85, 1.15), subdiv=2, jitter=0.22)
    blob(p, (0.85, 0.15, 0.3), 0.45, "rock", rng, scale=(1.0, 0.7, 1.0), subdiv=2, jitter=0.22)
    single("rock_b", p, smooth=12)


# ==================================================================================
# site
# ==================================================================================
def site_office(rng):
    p = K.Piece(bevel=0.03)
    L, W, H, F = 4.8, 1.5, 3.0, 0.35
    for z in (-1.1, 1.1):
        p.box((-L, 0, z - 0.1), (L, F, z + 0.1), "steel", 0.0)
    p.box((-L, F, -W), (L, H, W), "cabin")
    p.box((-L - 0.1, H, -W - 0.1), (L + 0.1, H + 0.12, W + 0.1), "paint_dark", 0.01)
    p.box((-L - 0.02, F, -W - 0.02), (L + 0.02, F + 0.12, W + 0.02), "paint_dark", 0.0)      # base trim
    for x in (-3.3, -1.3, 0.8):                                                              # front windows
        p.box((x - 0.65, 1.35, W - 0.01), (x + 0.65, 2.35, W + 0.03), "glass", 0.0)
        p.strip((x - 0.7, 1.3, W), (x + 0.7, 1.35, W + 0.05), "paint_dark")
    for x in (-2.5, 2.0):                                                                    # rear windows
        p.box((x - 0.65, 1.35, -W - 0.03), (x + 0.65, 2.35, -W + 0.01), "glass", 0.0)
    p.box((2.9, F + 0.05, W - 0.01), (3.85, 2.45, W + 0.04), "paint_dark", 0.0)              # door
    for k in range(3):                                                                       # steps
        p.box((2.75, 0.0, W + 0.95 - 0.3 * k), (4.0, 0.12 + 0.12 * k, W + 1.0 - 0.3 * k + 0.3), "steel", 0.0)
    with p.detail():
        p.tube([(4.05, 0.0, W + 1.0), (4.05, 1.25, W + 1.0), (4.05, 1.25, W + 0.1)], 0.025, "worn", 6)
        for x in [-L + 0.6 * i for i in range(17)]:                                          # cladding seams
            p.strip((x - 0.015, F + 0.15, W), (x + 0.015, H - 0.02, W + 0.012), "paint_dark")
    p.box((-L - 0.45, 1.9, -0.4), (-L, 2.55, 0.4), "worn", 0.02)                             # air conditioner
    single("site_office", p)


def workshop(rng):
    p = K.Piece(bevel=0.02)
    X, Zh, E, R = 9.0, 6.0, 6.0, 7.3
    p.box((-X - 0.3, 0, -Zh - 0.3), (X + 0.3, 0.15, Zh + 0.3), "concrete", 0.02)
    for x in (-X, -3.0, 3.0, X):
        for z in (-Zh, Zh):
            p.box((x - 0.15, 0.15, z - 0.15), (x + 0.15, E, z + 0.15), "paint_dark", 0.0)
    p.box((-X, 0.15, -Zh - 0.12), (X, E, -Zh + 0.05), "cabin", 0.0)                         # back wall
    for sx in (-1, 1):                                                                       # side walls + gables
        p.box((sx * X - 0.08, 0.15, -Zh), (sx * X + 0.08, E, Zh), "cabin", 0.0)
        p.prism([(-Zh, E), (Zh, E), (0.0, R)], "x", sx * X - 0.08, sx * X + 0.08, "cabin", 0.0)
    roof = [(-Zh - 0.5, E - 0.08), (0.0, R), (Zh + 0.5, E - 0.08), (Zh + 0.5, E + 0.1), (0.0, R + 0.18), (-Zh - 0.5, E + 0.1)]
    p.prism(roof, "x", -X - 0.4, X + 0.4, "box_blue", 0.0)
    p.box((-X, E - 0.45, Zh - 0.12), (X, E, Zh + 0.12), "box_blue", 0.0)                     # front fascia
    with p.detail():
        for x in [-X + 0.5 * i for i in range(1, 36)]:                                       # wall ribs
            p.strip((x - 0.02, 0.2, -Zh + 0.05), (x + 0.02, E - 0.05, -Zh + 0.09), "worn")
        for z in [-Zh + 0.5 * i for i in range(1, 24)]:
            for sx in (-1, 1):
                p.strip((sx * (X + 0.08) - 0.02, 0.2, z - 0.02), (sx * (X + 0.08) + 0.02, E - 0.05, z + 0.02), "worn")
        for k, x in enumerate((-6.5, -5.6, -4.7)):                                           # drums
            p.cyl((x, 0.15, -4.8), (x, 1.05, -4.8), 0.3, "box_red" if k % 2 else "box_blue", 12)
        p.box((2.0, 0.15, -5.7), (6.5, 0.95, -4.9), "paint_dark", 0.02)                      # workbench
        p.lamp((0.0, E - 0.2, -Zh + 0.06), "+z", 0.6, 0.25, 0.1)
    single("workshop", p)


def container(color):
    def build(rng):
        p = K.Piece(bevel=0.02)
        L, W, H = 3.03, 1.22, 2.59
        p.box((-L, 0.0, -W), (L, H, W), color)
        with p.detail():
            n = 21
            for i in range(n):
                x = -L + 0.3 + (2 * L - 0.6) * i / (n - 1)
                for sz in (-1, 1):
                    p.strip((x - 0.06, 0.12, sz * W - 0.025), (x + 0.06, H - 0.12, sz * W + 0.025), color)
            for z in (-0.75, -0.25, 0.25, 0.75):                                             # door locking bars
                p.cyl((L + 0.03, 0.15, z), (L + 0.03, H - 0.15, z), 0.022, "worn", 6)
            for x in (-L, L):
                for y in (0.0, H - 0.14):
                    for z in (-W, W):
                        p.box((x - 0.09 * (1 if x > 0 else -1), y, z - 0.09 * (1 if z > 0 else -1)),
                              (x + 0.01 * (1 if x > 0 else -1), y + 0.14, z + 0.01 * (1 if z > 0 else -1)), "steel", 0.0)
        p.box((L - 0.005, 0.1, -W + 0.04), (L + 0.02, H - 0.1, W - 0.04), color, 0.0)        # door panel
        single("container", p)
    return build


def fuel_tank(rng):
    p = K.Piece(bevel=0.02)
    BX, BZ, BH = 5.2, 2.3, 0.7
    p.box((-BX, 0, -BZ), (BX, 0.1, BZ), "concrete", 0.0)                                     # bund floor
    for sx in (-1, 1):
        p.box((sx * BX - 0.1, 0, -BZ), (sx * BX + 0.1, BH, BZ), "concrete", 0.01)
        p.box((-BX, 0, sx * BZ - 0.1), (BX, BH, sx * BZ + 0.1), "concrete", 0.01)
    for x in (-3.0, 0.0, 3.0):                                                               # saddles
        p.box((x - 0.25, 0.1, -1.0), (x + 0.25, 0.85, 1.0), "steel", 0.0)
    y, r = 2.05, 1.3
    p.cyl((-4.0, y, 0), (4.0, y, 0), r, "cabin", 24)
    for sx in (-1, 1):
        p.cyl((sx * 4.0, y, 0), (sx * 4.25, y, 0), r, "cabin", 24, r2=r * 0.78)
    p.box((-1.2, y + r - 0.05, -0.45), (1.2, y + r + 0.05, 0.45), "steel", 0.0)              # top walkway
    p.box((-4.0, y - 0.18, r - 0.02), (4.0, y + 0.18, r + 0.03), "paint", 0.0)               # diesel band
    p.box((-4.0, y - 0.18, -r - 0.03), (4.0, y + 0.18, -r + 0.02), "paint", 0.0)
    with p.detail():
        p.tube([(-1.1, y + r, 0.45), (-1.1, y + r + 1.0, 0.45), (1.1, y + r + 1.0, 0.45), (1.1, y + r, 0.45)], 0.025, "paint", 6)
        for zz in (-0.25, 0.25):                                                             # ladder
            p.cyl((4.45, 0.1, zz), (4.45, y + r + 0.9, zz), 0.03, "worn", 6)
        for k in range(10):
            yy = 0.4 + k * 0.35
            p.cyl((4.45, yy, -0.25), (4.45, yy, 0.25), 0.018, "worn", 5)
        p.cyl((0.0, y + r, 0.0), (0.0, y + r + 0.35, 0.0), 0.12, "steel", 8)                 # vent and lid
    # dispenser outside the bund, towards the bay (+Z), with its pipe and hose
    p.box((-2.1, 0.0, BZ + 0.6), (-0.9, 1.9, BZ + 1.2), "box_red", 0.03)
    p.box((-2.05, 1.2, BZ + 1.18), (-0.95, 1.7, BZ + 1.24), "glass", 0.0)
    with p.detail():
        p.tube([(-1.5, y - 0.6, 0.0), (-1.5, 0.35, BZ - 0.2), (-1.5, 0.35, BZ + 0.6)], 0.05, "worn", 6)
        p.tube([(-0.9, 1.5, BZ + 0.9), (-0.55, 1.2, BZ + 1.0), (-0.45, 0.4, BZ + 1.0), (-0.6, 0.2, BZ + 0.9),
                (-0.85, 0.9, BZ + 0.9)], 0.03, "rubber", 6)
    single("fuel_tank", p)


def light_tower(rng):
    p = K.Piece(bevel=0.02)
    p.box((-1.35, 0.45, -0.65), (1.35, 1.55, 0.65), "paint")
    p.box((-1.38, 1.55, -0.68), (1.38, 1.62, 0.68), "paint_dark", 0.0)
    for sz in (-1, 1):
        p.cyl((0.0, 0.36, sz * 0.62), (0.0, 0.36, sz * 0.88), 0.36, "rubber", 14)
    p.box((1.35, 0.55, -0.06), (2.5, 0.68, 0.06), "steel", 0.0)                              # drawbar
    for sx in (-1, 1):                                                                       # outriggers + pads
        for sz in (-1, 1):
            p.box((sx * 1.1 - 0.06, 0.05, sz * 0.6), (sx * 1.1 + 0.06, 0.6, sz * 1.5), "steel", 0.0)
            p.box((sx * 1.1 - 0.15, 0.0, sz * 1.5 - 0.15), (sx * 1.1 + 0.15, 0.05, sz * 1.5 + 0.15), "steel", 0.0)
    p.cyl((0.0, 1.55, 0.0), (0.0, 8.5, 0.0), 0.09, "worn", 8)
    p.cyl((0.0, 1.55, 0.0), (0.0, 5.0, 0.0), 0.12, "worn", 8)
    p.box((-1.1, 8.35, -0.05), (1.1, 8.5, 0.05), "steel", 0.0)
    for x in (-0.8, -0.27, 0.27, 0.8):
        p.lamp((x, 8.7, 0.05), "+z", 0.45, 0.35, 0.16)
    single("light_tower", p)


def cone(rng):
    p = K.Piece(bevel=0.0)
    p.box((-0.2, 0.0, -0.2), (0.2, 0.035, 0.2), "rubber", 0.0)
    p.cyl((0, 0.035, 0), (0, 0.72, 0), 0.165, "cone", 12, r2=0.03)
    p.cyl((0, 0.33, 0), (0, 0.46, 0), 0.098, "reflect", 12, r2=0.083)
    single("cone", p)


def barrier(rng):
    p = K.Piece(bevel=0.0)
    prof = [(-0.3, 0.0), (0.3, 0.0), (0.3, 0.08), (0.22, 0.26), (0.08, 0.81), (-0.08, 0.81), (-0.22, 0.26), (-0.3, 0.08)]
    p.prism(prof, "x", -1.48, 1.48, "concrete", 0.012)
    with p.detail():
        for k in range(4):
            x = -1.2 + k * 0.8
            p.box((x - 0.2, 0.6, 0.12), (x + 0.2, 0.7, 0.135), "cone" if k % 2 else "reflect", 0.0)
    single("barrier", p)


def site_sign(rng):
    p = K.Piece(bevel=0.01)
    for x in (-1.3, 1.3):
        p.cyl((x, 0.0, 0.0), (x, 2.7, 0.0), 0.06, "worn", 8)
    p.box((-1.6, 1.2, -0.04), (1.6, 2.6, 0.04), "sign", 0.01)
    with p.detail():
        p.strip((-1.5, 2.48, 0.04), (1.5, 2.52, 0.055), "reflect")
        p.strip((-1.5, 1.28, 0.04), (1.5, 1.32, 0.055), "reflect")
        for k, w in enumerate((2.4, 1.8, 2.1, 1.2)):                                         # lines of lettering
            yy = 2.25 - k * 0.26
            p.strip((-1.35, yy, 0.04), (-1.35 + w, yy + 0.12, 0.055), "reflect")
    single("site_sign", p)


PROPS = {
    "pine": (pine, 0.5), "broadleaf": (broadleaf, 0.45), "shrub": (shrub, 0.45),
    "rock_a": (rock_a, 0.35), "rock_b": (rock_b, 0.5),
    "site_office": (site_office, 0.6), "workshop": (workshop, 0.6),
    "container_blue": (container("box_blue"), 0.6), "container_red": (container("box_red"), 0.6),
    "fuel_tank": (fuel_tank, 0.5), "light_tower": (light_tower, 0.5),
    "cone": (cone, 0.6), "barrier": (barrier, 0.8), "site_sign": (site_sign, 0.7),
}


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    out = os.path.join(os.path.dirname(HERE), "out", "props")
    if "--out" in argv:
        i = argv.index("--out")
        out = argv[i + 1]
        del argv[i:i + 2]
    names = argv or list(PROPS)
    summary = {}
    for name in names:
        fn, ratio = PROPS[name]
        K.reset(name, out)
        fn(random.Random("sitepulse-" + name))                 # deterministic per prop
        K.make_lod1(ratio)
        rep = K.report(budget_lod0=6000)
        K.export(name)
        summary[name] = dict(tris_LOD0=rep["tris_LOD0"], tris_LOD1=rep["tris_LOD1"], bbox=rep["bbox_unity_m"],
                             materials=rep["materials"], ok=rep["budget"]["ok"])
        print(f"{name:<16} LOD0 {rep['tris_LOD0']:>6}  LOD1 {rep['tris_LOD1']:>6}  {rep['bbox_unity_m']}", flush=True)
    with open(os.path.join(out, "props_check.json"), "w") as f:
        json.dump(summary, f, indent=1)
    print("ALL_OK", all(v["ok"] for v in summary.values()))


main()
