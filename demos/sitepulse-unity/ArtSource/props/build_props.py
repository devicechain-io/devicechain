# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Sitepulse site props and vegetation, built as code with the machine kit.

    blender --background --python props/build_props.py -- [name ...] [--out DIR]

With no names every prop is built, each in a Blender of its own, so the files are the same
byte for byte on every build. Each prop is written as <name>.glb and <name>_LOD1.glb (same node
tree, as for the machines) plus one props_check.json with the triangle counts.

Same conventions as the machines (see sitepulse_kit.py): Unity metres, X right, Y up, Z forward,
origin on the ground at the prop's footprint centre. Every prop has one node, Body, under Root;
it does not move. A building's long axis is X and its open or front side faces +Z. The plant
also has empty marker nodes under Root where material leaves it (StackerHead, SideHeadRight,
SideHeadLeft, CrusherDischarge) and at its Hopper and Screen, for the scene's effects; its two
flywheels (FlywheelLeft, FlywheelRight, turning about X) and its screen box (ScreenBox) are nodes
of their own, so the scene can turn and shake them.

  conifer_a/_b/_c            stylized spruce, pine and young fir for Unity Terrain tree instances
  shrub_a, shrub_b           a low bush and a taller scrub clump
  rock_a/_b/_c               faceted boulders (terrain tree instances, no collider)
  site_office                9.6 m portable site cabin on skids
  workshop                   18 m x 12 m open-fronted steel service shelter
  container_blue/_red        20 ft shipping container
  fuel_tank                  bunded 8 m horizontal diesel tank with a dispenser
  light_tower                trailer-mounted light tower (mast 8.5 m), lamps face +Z
  cone, barrier, site_sign   small site furniture
  worker                     a site worker in a high-visibility vest and hard hat
  crusher_plant              primary jaw crusher in a pocket below the pad, conveyor, screen tower,
                             radial stacker and two side conveyors; origin at the hopper centre,
                             material flows along +Z (see PLANT_SCALE)

Vegetation and rocks carry vertex colours (a soft gradient and a little per-face variation),
which multiply their material's colour.
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
import bpy                                                  # noqa: E402
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
    faces = list(dict.fromkeys(f for v in verts for f in v.link_faces))
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
# vegetation and rocks: stylized, to sit with the machines. Crisp faceted shapes; each face gets
# a soft gradient (darker low down, lighter on top) and a small random lift or drop in its
# vertex colour, so a tree reads as a crisp shape with soft light rather than as flat colour.
# ==================================================================================
def shade(height, low=0.62, high=1.12, jitter=0.07, seed=0):
    """Vertex-colour function: brightness from `low` at the ground to `high` at `height`,
    with a per-face random variation of +-jitter."""
    def fn(co, fc):
        t = max(0.0, min(1.0, co[1] / height))
        k = low + (high - low) * t
        h = math.sin(fc[0] * 12.9898 + fc[1] * 78.233 + fc[2] * 37.719 + seed) * 43758.5453
        k *= 1.0 + jitter * (2.0 * (h - math.floor(h)) - 1.0)
        return (k, k, k)
    return fn


def tiered(p, rng, key, base, top, r0, tiers, n=8, jag=0.22, droop=0.35, tier_h=None):
    """A stack of star-shaped cone tiers from `base` up to `top` metres, the lowest r0 wide."""
    span = top - base
    for i in range(tiers):
        f = i / max(1, tiers - 1)
        y0 = base + span * 0.78 * f
        h = tier_h or span * (0.42 - 0.12 * f)
        spike_cone(p, (rng.uniform(-0.08, 0.08), y0, rng.uniform(-0.08, 0.08)), r0 * (1.0 - 0.78 * f),
                   h, key, rng, n=n if i < tiers - 1 else max(5, n - 2), jag=jag, droop=droop * (1.0 - 0.6 * f))


def conifer_a(rng):
    """Spruce, about 12 m: a narrow spire of drooping tiers over a short bare trunk."""
    p = K.Piece()
    p.cyl((0, 0, 0), (0, 10.5, 0), 0.26, "bark", 7, r2=0.06)
    tiered(p, rng, "spruce", 1.4, 12.0, 2.9, 6, n=8, jag=0.26, droop=0.55)
    p.vcol = shade(12.0, seed=1)
    p.one_material = "vegetation"
    single("conifer_a", p, smooth=20)


def conifer_b(rng):
    """Pine, about 10 m: a tall bare trunk under a broad crown of a few flat tiers."""
    p = K.Piece()
    p.cyl((0, 0, 0), (0.1, 9.0, 0.05), 0.24, "bark", 7, r2=0.1)
    for k, (y, r, h) in enumerate(((5.2, 2.6, 1.6), (6.6, 2.3, 1.5), (7.9, 1.7, 1.5), (9.0, 1.0, 1.3))):
        spike_cone(p, (rng.uniform(-0.3, 0.3), y, rng.uniform(-0.3, 0.3)), r, h, "pine", rng,
                   n=7, jag=0.3, droop=0.25)
    p.vcol = shade(10.5, low=0.7, seed=2)
    p.one_material = "vegetation"
    single("conifer_b", p, smooth=20)


def conifer_c(rng):
    """Young fir, about 5 m: a short full cone to the ground, lighter green."""
    p = K.Piece()
    p.cyl((0, 0, 0), (0, 4.0, 0), 0.12, "bark", 6, r2=0.04)
    tiered(p, rng, "fir", 0.5, 5.2, 1.7, 4, n=7, jag=0.22, droop=0.3)
    p.vcol = shade(5.2, low=0.7, seed=3)
    p.one_material = "vegetation"
    single("conifer_c", p, smooth=20)


def shrub_a(rng):
    """Low rounded bush, about 1.3 m."""
    p = K.Piece()
    for k in range(4):
        a = k * 1.9
        d = 0.0 if k == 0 else 0.6
        blob(p, (d * math.cos(a), 0.4 + rng.uniform(-0.1, 0.15), d * math.sin(a)), rng.uniform(0.6, 0.85),
             "shrub", rng, scale=(1.0, 0.75, 1.0), subdiv=1, jitter=0.16)
    p.vcol = shade(1.4, low=0.6, high=1.1, seed=4)
    p.one_material = "vegetation"
    single("shrub_a", p, smooth=20)


def shrub_b(rng):
    """Taller scrub, about 2.4 m, in a loose clump."""
    p = K.Piece()
    for k in range(6):
        a = k * 1.3 + rng.uniform(-0.2, 0.2)
        d = 0.0 if k == 0 else rng.uniform(0.7, 1.0)
        blob(p, (d * math.cos(a), 0.9 + rng.uniform(0.0, 0.8) + (0.4 if k == 0 else 0.0), d * math.sin(a)),
             rng.uniform(0.6, 0.9), "scrub", rng, scale=(1.0, 1.1, 1.0), subdiv=1, jitter=0.18)
    p.vcol = shade(2.5, low=0.62, high=1.1, seed=5)
    p.one_material = "vegetation"
    single("shrub_b", p, smooth=20)


def rock(name, shape, seed):
    """A faceted boulder: an icosahedron, stretched to `shape`, pushed about at random and
    sunk a little so that it sits in the ground."""
    def build(rng):
        p = K.Piece()
        sx, sy, sz = shape
        b0 = p._op()
        res = bmesh.ops.create_icosphere(p.bm, subdivisions=1, radius=1.0)
        for v in res["verts"]:
            n = v.co.normalized()
            k = 1.0 + rng.uniform(-0.22, 0.22)
            y = n.y * sy * k
            v.co = Vector((n.x * sx * k, max(y, -0.25 * sy) + 0.12 * sy, n.z * sz * k))
        faces = list(dict.fromkeys(f for v in res["verts"] for f in v.link_faces))
        p._finish(res["verts"], faces, "rock", 0)
        p._tag(b0)
        p.vcol = shade(sy * 1.3, low=0.72, high=1.08, jitter=0.09, seed=seed)
        single(name, p, smooth=18)
    return build


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
        p.box((x - 0.25, 0.1, -1.0), (x + 0.25, 0.85, 1.0), "paint_dark", 0.0)
    y, r = 2.05, 1.3
    # the shell in a muted sage, not white, so it sits in the site's palette; stiffener rings
    # and the dished ends in bare plate, and a band of road dirt along its lower side
    p.cyl((-4.0, y, 0), (4.0, y, 0), r, "tank", 24)
    for sx in (-1, 1):
        p.cyl((sx * 4.0, y, 0), (sx * 4.25, y, 0), r, "tank", 24, r2=r * 0.78)
        p.cyl((sx * 4.0, y, 0), (sx * 4.08, y, 0), r + 0.04, "plate", 24)                    # end ring
        p.cyl((sx * 4.25, y, 0), (sx * 4.28, y, 0), r * 0.42, "plate", 16)                   # manway
    for x in (-2.0, 2.0):
        p.cyl((x - 0.05, y, 0), (x + 0.05, y, 0), r + 0.035, "plate", 24)                    # stiffener ring
    # the grime: a thin curved skin over the lower part of the shell, from low on one side,
    # under the tank, to low on the other ((z, y) profile, extruded along the tank)
    arc = [math.radians(a) for a in range(196, 345, 8)]
    skin = [((r + 0.015) * math.cos(a), y + (r + 0.015) * math.sin(a)) for a in arc]
    skin += [((r - 0.04) * math.cos(a), y + (r - 0.04) * math.sin(a)) for a in reversed(arc)]
    p.prism(skin, "x", -4.0, 4.0, "grime", 0.0)
    p.box((-1.2, y + r - 0.05, -0.45), (1.2, y + r + 0.05, 0.45), "steel", 0.0)              # top walkway
    p.box((-3.6, y - 0.16, r - 0.02), (3.6, y + 0.16, r + 0.04), "paint", 0.0)               # diesel band
    p.box((-3.6, y - 0.16, -r - 0.04), (3.6, y + 0.16, -r + 0.02), "paint", 0.0)
    p.box((0.9, y + 0.25, r - 0.03), (2.3, y + 0.75, r + 0.05), "reflect", 0.0)              # product placard
    p.box((-2.3, y + 0.25, -r - 0.05), (-0.9, y + 0.75, -r + 0.03), "reflect", 0.0)
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


# ==================================================================================
# processing plant
# ==================================================================================
def frustum(p, y0, y1, bot, top, key, open_top=False, inner_key=None):
    """A closed box whose horizontal section goes from rectangle `bot` at y0 to `top` at y1;
    each rectangle is (x0, x1, z0, z1). A hopper, a chute. open_top leaves the top off and adds
    the inside of the walls (a hopper you can see into), in inner_key if given."""
    b0 = p._op()
    def ring(y, r):
        x0, x1, z0, z1 = r
        return [p.bm.verts.new(v) for v in ((x0, y, z0), (x1, y, z0), (x1, y, z1), (x0, y, z1))]
    a, b = ring(y0, bot), ring(y1, top)
    faces = [p.bm.faces.new(list(reversed(a)))]
    if not open_top:
        faces.append(p.bm.faces.new(b))
    for i in range(4):
        j = (i + 1) % 4
        faces.append(p.bm.faces.new((a[i], a[j], b[j], b[i])))
    verts = a + b
    if open_top:
        # the inside: the same walls a little in, facing inwards, and a floor
        def inset(r, d):
            x0, x1, z0, z1 = r
            return (x0 + d, x1 - d, z0 + d, z1 - d)
        ai, bi = ring(y0 + 0.06, inset(bot, 0.06)), ring(y1, inset(top, 0.06))
        faces.append(p.bm.faces.new(ai))
        for i in range(4):
            j = (i + 1) % 4
            faces.append(p.bm.faces.new((bi[i], bi[j], ai[j], ai[i])))
            faces.append(p.bm.faces.new((b[i], b[j], bi[j], bi[i])))                 # the rim's top
        verts += ai + bi
        if inner_key is not None:
            inner = faces[-8::2] + [faces[-9]]                                              # inside walls, floor
            p._finish(verts, [f for f in faces if f not in inner], key, 0)
            p._finish([], inner, inner_key, 0)
            p._tag(b0)
            return
    p._finish(verts, faces, key, 0)
    p._tag(b0)


def _frame(a, b):
    """Unit axes along a -> b: u along, v the 'up' normal to it, w across."""
    a, b = Vector(a), Vector(b)
    u = (b - a).normalized()
    up = Vector((0.0, 1.0, 0.0))
    v = (up - u * up.dot(u)).normalized()
    return a, b, u, v, u.cross(v)


def belt_load(p, c, u, v, L, width, period=2.0):
    """The layer of crushed rock riding on a belt: a flat box whose texture coordinates run
    along the belt (U, one repeat every `period` metres), so that scrolling the texture in Unity
    moves the rock along."""
    before = set(p.bm.faces)
    p.obox(c, u, v, L, 0.12, width, "ore_belt")
    w = u.cross(v)
    for f in set(p.bm.faces) - before:
        for lp in f.loops:
            d = lp.vert.co - c
            lp[p.uv].uv = (d.dot(u) / period, d.dot(w) / width + 0.5)


def conveyor(p, a, b, width=0.9, legs=(), truss=False, walkway=True):
    """A troughed belt conveyor from tail a to head b (Unity metres): stringers, belt with a
    stream of rock on it, idlers, pulleys, a walkway with handrail on one side, and legs down to
    the ground at the given fractions along it. truss=True hangs a lattice truss under it."""
    a, b, u, v, w = _frame(a, b)
    L = (b - a).length
    mid = (a + b) / 2
    hw = width / 2
    for s in (-1, 1):
        p.obox(mid + w * s * (hw + 0.12) - v * 0.12, u, v, L, 0.32, 0.08, "paint_dark")      # stringers
    p.obox(mid + v * 0.06, u, v, L - 0.4, 0.05, width, "rubber")                           # belt
    belt_load(p, mid + v * 0.13, u, v, L - 0.9, width * 0.55)                              # material on it
    for t, r in ((0.0, 0.28), (1.0, 0.32)):                                                 # tail and head pulleys
        c = a + (b - a) * t
        p.cyl(c - w * (hw + 0.05), c + w * (hw + 0.05), r, "steel", 12)
    with p.detail():
        n = max(2, int(L / 1.6))
        for i in range(1, n):                                                               # idlers
            c = a + (b - a) * (i / n) - v * 0.02
            p.cyl(c - w * hw, c + w * hw, 0.07, "steel", 6)
        if walkway:
            p.obox(mid + w * (hw + 0.6) - v * 0.25, u, v, L, 0.05, 0.75, "worn")              # grating
            for t in (1.0, 0.9):
                off = w * (hw + 0.95) + v * t
                p.cyl(a + off, b + off, 0.025, "paint", 6)
            k = max(2, int(L / 2.5))
            for i in range(k + 1):
                c = a + (b - a) * (i / k) + w * (hw + 0.95)
                p.cyl(c - v * 0.25, c + v * 1.0, 0.025, "paint", 6)
    for t in legs:                                                                          # support legs
        c = a + (b - a) * t - v * 0.28
        for s in (-1, 1):
            foot = Vector((c.x, 0.0, c.z)) + w * s * (hw + 0.55)
            p.cyl(foot, c + w * s * (hw + 0.1), 0.09, "paint_dark", 8)
        with p.detail():
            p.cyl(c + w * (hw + 0.1) + Vector((0, -1.2, 0)), c - w * (hw + 0.1) + Vector((0, -1.2, 0)), 0.05, "paint_dark", 6)
    if truss:
        depth = 1.1
        bot = [a + (b - a) * t - v * depth for t in (0.05, 0.95)]
        for s in (-1, 1):
            p.cyl(bot[0] + w * s * hw, bot[1] + w * s * hw, 0.07, "paint_dark", 6)          # bottom chords
        with p.detail():
            k = max(3, int(L / 1.8))
            for i in range(k):
                t0, t1 = 0.05 + 0.9 * i / k, 0.05 + 0.9 * (i + 1) / k
                for s in (-1, 1):
                    top = a + (b - a) * t0 - v * 0.28 + w * s * (hw + 0.1)
                    low = a + (b - a) * t1 - v * depth + w * s * hw
                    p.cyl(top, low, 0.04, "paint_dark", 5)


# The plant is built at PLANT_SCALE (1.4 x the dimensions written below) so it stands right next
# to the haul trucks, and its primary crusher sits PRIMARY_DROP metres (after scaling) down in a
# concrete-lined pocket so that a loader on the pad tips over the hopper's rim with its bucket's
# lip half a metre clear of it. Both, and the
# discharge points, are mirrored in ArtSource/terrain/quarry_heightmap.py (PLANT_*): keep them in step.
PLANT_SCALE = 1.4
PRIMARY_DROP = 2.9
POCKET = (-3.6, 3.6, -4.4, 8.8)              # x0, x1, z0, z1 of the pocket, metres after scaling


def flywheel(p, c, r, width, spokes=6):
    """A crusher flywheel on the X axis at c: rim, hub and straight spokes."""
    x, y, z = c
    p.lathe_x(c, [(r, -width / 2), (r, width / 2), (r * 0.82, width / 2), (r * 0.82, -width / 2)], "steel", 24)
    p.cyl((x - width * 0.7, y, z), (x + width * 0.7, y, z), r * 0.18, "paint_dark", 12)
    for k in range(spokes):
        a = 2 * math.pi * k / spokes + 0.3
        p.cyl((x, y + r * 0.15 * math.sin(a), z + r * 0.15 * math.cos(a)),
              (x, y + r * 0.84 * math.sin(a), z + r * 0.84 * math.cos(a)), width * 0.28, "steel", 6)


def crusher_plant(rng):
    """Primary jaw crusher in a pocket below the pad, fed by a loader over the hopper's rim; a
    conveyor up to a vibrating screen on a steel tower; three product conveyors from it: the
    main radial stacker straight on, and a side conveyor to the right and one to the left.
    Origin: centre of the hopper, on the pad; material flows along +Z. Named nodes mark where
    material leaves each head and the crusher (for the scene's falling streams and dust)."""
    p = K.Piece(bevel=0.02)
    drop = PRIMARY_DROP / PLANT_SCALE            # unscaled
    x0, x1, z0, z1 = (q / PLANT_SCALE for q in POCKET)
    # -- the pocket's retaining walls (a slot in the far wall lets the conveyor out)
    t = 0.22
    p.box((x0 - t, -drop, z0 - t), (x1 + t, 0.12, z0), "concrete", 0.02)
    for sx, xx in ((-1, x0), (1, x1)):
        p.box((xx - t if sx < 0 else xx, -drop, z0 - t), (xx if sx < 0 else xx + t, 0.12, z1 + t), "concrete", 0.02)
    p.box((x0 - t, -drop, z1), (-0.75, 0.12, z1 + t), "concrete", 0.02)
    p.box((0.75, -drop, z1), (x1 + t, 0.12, z1 + t), "concrete", 0.02)
    p.box((x0, -drop - 0.05, z0), (x1, -drop + 0.05, z1), "concrete", 0.0)                 # floor slab
    primary = set(p.bm.faces)
    # -- primary: skid, columns, hopper, jaw crusher, flywheels, discharge chute
    p.box((-1.9, 0.0, -3.0), (1.9, 0.4, 6.4), "paint_dark")                                # skid
    for x in (-1.6, 1.6):
        for z in (-2.4, 0.9):
            p.box((x - 0.15, 0.4, z - 0.15), (x + 0.15, 2.5, z + 0.15), "paint_dark", 0.0)
    p.box((-2.0, 2.4, -2.7), (2.0, 2.6, 1.2), "paint_dark", 0.0)                            # feeder deck
    # the feed hopper: splayed steel plate walls, lined with dark wear plates inside,
    # widest towards the loader so the bucket tips well inside the rim, which is a yellow frame
    frustum(p, 2.55, 4.0, (-1.0, 1.0, -1.2, 1.0), (-2.6, 2.6, -3.3, 2.5), "plate", open_top=True, inner_key="liner")
    for (ax0, ax1, az0, az1) in ((-2.7, 2.7, -3.4, -3.2), (-2.7, 2.7, 2.4, 2.6),          # rim: a frame, open
                                 (-2.7, -2.5, -3.4, 2.6), (2.5, 2.7, -3.4, 2.6)):          # in the middle
        p.box((ax0, 4.0, az0), (ax1, 4.14, az1), "paint", 0.0)
    with p.detail():
        for sx in (-1, 1):                                                                  # stiffeners outside,
            for z in (-1.9, -0.5, 0.9):                                                     # along the splayed walls
                p.cyl((sx * 1.08, 2.62, z * 0.45), (sx * 2.68, 3.98, z), 0.07, "paint_dark", 6)
    p.box((-1.4, 3.2, -1.9), (1.4, 3.35, 1.6), "ore", 0.0)                                  # rock in it
    for k in range(18):
        blob(p, (rng.uniform(-1.6, 1.6), 3.5 + rng.uniform(0.0, 0.3), rng.uniform(-2.2, 1.5)), rng.uniform(0.25, 0.55),
             "ore", rng, scale=(1.0, 0.7, 1.0), subdiv=1, jitter=0.25)
    # jaw crusher: two heavy side frames, sloped at the feed, with the swing-jaw housing between
    side = [(2.3, 0.4), (5.3, 0.4), (5.3, 2.2), (4.6, 3.5), (2.3, 3.5)]                     # (z, y)
    for sx in (-1, 1):
        p.prism(side, "x", sx * 1.05 - 0.14, sx * 1.05 + 0.14, "plant", 0.03)
        with p.detail():
            for z in (3.0, 3.8, 4.6):                                                       # stiffening ribs
                p.box((sx * 1.2 - 0.05, 0.5, z - 0.08), (sx * 1.2 + 0.05, 3.2, z + 0.08), "plant", 0.0)
    p.box((-0.92, 0.4, 2.4), (0.92, 3.3, 5.1), "paint_dark", 0.02)                          # jaw housing
    p.prism([(2.3, 3.5), (4.6, 3.5), (3.4, 4.2)], "x", -1.15, 1.15, "plant", 0.02)        # pitman cover
    p.cyl((-1.8, 2.25, 3.9), (1.8, 2.25, 3.9), 0.16, "steel", 12)                          # shaft
    frustum(p, 0.4, 1.0, (-0.55, 0.55, 4.9, 5.8), (-0.95, 0.95, 4.4, 5.6), "paint_dark")  # discharge chute
    with p.detail():                                                                        # access platform
        p.box((-3.0, 3.2, -2.8), (-2.3, 3.28, 2.4), "worn", 0.0)
        for z in (-2.7, -0.2, 2.3):
            p.cyl((-3.0, 3.25, z), (-3.0, 4.3, z), 0.025, "paint", 6)
        p.cyl((-3.0, 4.3, -2.7), (-3.0, 4.3, 2.3), 0.025, "paint", 6)
        p.cyl((-3.0, 3.75, -2.7), (-3.0, 3.75, 2.3), 0.025, "paint", 6)
    primary = set(p.bm.faces) - primary
    for v in {v for f in primary for v in f.verts}:                                         # down into the pocket
        v.co.y -= drop
    # the drive: an electric motor on the pad beside the pocket, belted to the right flywheel
    p.box((2.95, 0.0, 3.1), (4.4, 0.35, 4.8), "paint_dark", 0.02)                           # motor base
    p.cyl((3.0, 0.95, 3.95), (4.3, 0.95, 3.95), 0.55, "plant", 16)                          # motor
    for k in range(5):
        p.cyl((3.1 + 0.25 * k, 0.95, 3.95), (3.15 + 0.25 * k, 0.95, 3.95), 0.6, "plant", 16)   # cooling fins
    _, _, gu, gv, gw = _frame((3.0, 0.95, 3.95), (1.75, 2.25 - drop, 3.9))
    p.obox(Vector((2.35, (0.95 + 2.25 - drop) / 2, 3.92)), gu, Vector((0, 0, 1)), 1.9, 0.3, 1.1, "paint")  # belt guard
    # -- conveyor up to the screen, out of the pocket through the slot in its far wall
    conveyor(p, (0.0, 0.95 - drop, 5.4), (0.0, 7.0, 17.6), width=0.9, legs=(0.45, 0.8))
    # -- screen on its tower
    for x in (-2.1, 2.1):
        for z in (16.8, 22.2):
            p.box((x - 0.14, 0.0, z - 0.14), (x + 0.14, 5.9, z + 0.14), "paint_dark", 0.0)
    p.box((-2.6, 5.9, 16.4), (2.6, 6.05, 22.6), "worn", 0.0)                                # deck
    with p.detail():
        for x in (-2.1, 2.1):                                                               # bracing
            p.cyl((x, 0.3, 16.8), (x, 5.6, 22.2), 0.05, "paint_dark", 5)
            p.cyl((x, 0.3, 22.2), (x, 5.6, 16.8), 0.05, "paint_dark", 5)
        for z in (16.4, 22.6):
            p.cyl((-2.6, 7.0, z), (2.6, 7.0, z), 0.025, "paint", 6)
        for x in (-2.6, 2.6):
            p.cyl((x, 7.0, 16.4), (x, 7.0, 22.6), 0.025, "paint", 6)
            for z in (16.4, 19.5, 22.6):
                p.cyl((x, 6.05, z), (x, 7.0, z), 0.025, "paint", 6)
        for k in range(16):                                                                 # stair to the deck
            p.box((2.3, 0.35 * k, 15.2 - 0.3 * k + 4.8), (3.1, 0.35 * k + 0.05, 15.5 - 0.3 * k + 4.8), "worn", 0.0)
    _, _, su, sv, sw = _frame((0.0, 7.5, 17.4), (0.0, 6.5, 22.0))
    # the screen box shakes and the flywheels turn: each is its own node (see below)
    screen = K.Piece(bevel=0.02)
    screen.obox(Vector((0.0, 7.05, 19.7)) + sv * 0.55, su, sv, 4.9, 1.1, 2.6, "plant")        # screen box
    screen.obox(Vector((0.0, 7.05, 19.7)) + sv * 1.15, su, sv, 4.6, 0.1, 2.2, "ore")         # material on the top deck
    p.box((1.35, 6.05, 18.4), (2.1, 6.75, 19.4), "plant", 0.02)                              # vibrator motor
    frustum(p, 3.2, 5.9, (-0.6, 0.6, 20.2, 21.2), (-1.4, 1.4, 18.0, 22.0), "paint_dark")   # under-screen chute
    # -- the main radial stacker, straight on
    conveyor(p, (0.0, 1.5, 20.6), (0.0, 9.0, 38.5), width=0.8, legs=(), truss=True, walkway=False)
    p.box((-0.9, 0.0, 19.8), (0.9, 1.2, 21.4), "paint_dark", 0.02)                           # tail pivot
    _, _, su, sv, sw = _frame((0.0, 1.5, 20.6), (0.0, 9.0, 38.5))
    knee = Vector((0.0, 1.5, 20.6)) + (Vector((0.0, 9.0, 38.5)) - Vector((0.0, 1.5, 20.6))) * 0.62 - sv * 1.1
    for sx in (-1, 1):                                                                      # A-frame to the bogie
        p.cyl((sx * 1.7, 1.0, knee.z), (sx * 0.45, knee.y, knee.z), 0.11, "paint_dark", 8)
    p.box((-2.1, 0.75, knee.z - 0.35), (2.1, 1.15, knee.z + 0.35), "paint_dark", 0.02)       # axle beam
    for sx in (-1, 1):
        p.cyl((sx * 1.85, 0.55, knee.z - 0.32), (sx * 1.85, 0.55, knee.z + 0.32), 0.55, "rubber", 16)
    p.box((-0.75, 8.8, 38.3), (0.75, 9.8, 39.4), "paint_dark", 0.02)                         # discharge hood
    # -- side conveyors under the screen: the middle grade to the right, the fines to the left
    for sx, reach, top, legs in ((1, 12.0, 6.0, (0.35, 0.7)), (-1, 9.5, 4.6, (0.45,))):
        a, b = (sx * 1.0, 1.1, 19.7), (sx * reach, top, 19.7)
        conveyor(p, a, b, width=0.65, legs=legs, walkway=False)
        p.box((b[0] - 0.55 + 0.35 * sx, top - 0.25, 19.15), (b[0] + 0.55 + 0.35 * sx, top + 0.55, 20.25), "paint_dark", 0.02)
    # -- electrical room beside the crusher
    p.box((-7.4, 0.0, 1.0), (-4.6, 0.35, 6.2), "steel", 0.0)
    p.box((-7.3, 0.35, 1.1), (-4.7, 2.95, 6.1), "mcc")
    p.box((-7.4, 2.95, 1.0), (-4.6, 3.07, 6.2), "paint_dark", 0.01)
    p.box((-4.72, 0.45, 4.6), (-4.66, 2.4, 5.5), "paint_dark", 0.0)                          # door
    p.box((-4.70, 1.75, 3.7), (-4.64, 2.15, 4.1), "paint", 0.0)                              # warning plate
    with p.detail():
        for k in range(5):                                                                  # louvres
            y = 1.0 + 0.16 * k
            p.box((-4.72, y, 1.6), (-4.62, y + 0.07, 3.0), "paint_dark", 0.0)
        for z in (2.2, 3.6, 4.4):                                                           # wall seams
            p.box((-7.33, 0.4, z - 0.02), (-7.27, 2.9, z + 0.02), "paint_dark", 0.0)
    with p.detail():
        p.box((-7.75, 1.9, 2.0), (-7.3, 2.5, 2.8), "worn", 0.02)                             # air conditioner
        p.tube([(-4.6, 2.6, 2.0), (-3.0, 2.6, 2.0), (-2.6, 0.2, 2.4)], 0.05, "rubber", 6)  # cable
    p.lamp((0.0, 4.1 - drop, -2.65), "-z", 0.5, 0.3, 0.12)
    p.lamp((2.6, 7.0, 22.6), "+z", 0.5, 0.3, 0.12)
    for v in p.bm.verts:                                                                     # to scale
        v.co *= PLANT_SCALE
    S = PLANT_SCALE
    K.node("Root", None, (0, 0, 0))
    K.node("Body", "Root", (0, 0, 0))
    # the flywheels turn about their shaft (the node's X axis) and the screen box shakes
    for node, sx in (("FlywheelLeft", -1), ("FlywheelRight", 1)):
        fw = K.Piece(bevel=0.02)
        flywheel(fw, (sx * 1.55, 2.25 - drop, 3.9), 1.25, 0.34)
        for v in fw.bm.verts:
            v.co *= S
        K.node(node, "Root", (sx * 1.55 * S, (2.25 - drop) * S, 3.9 * S))
        K.attach(node, fw, smooth_angle=35)
    for v in screen.bm.verts:
        v.co *= S
    K.node("ScreenBox", "Root", (0.0, 7.05 * S, 19.7 * S))
    K.attach("ScreenBox", screen, smooth_angle=35)
    for name, pos in (("StackerHead", (0.0, 9.0, 39.2)), ("SideHeadRight", (12.35, 5.9, 19.7)),
                      ("SideHeadLeft", (-9.85, 4.5, 19.7)), ("CrusherDischarge", (0.0, 0.4 - drop, 5.4)),
                      ("Hopper", (0.0, 4.1 - drop, -0.2)), ("Screen", (0.0, 7.6, 19.7))):
        K.node(name, "Root", (pos[0] * S, pos[1] * S, pos[2] * S))
    K.attach("Body", p, smooth_angle=35)


def worker(rng):
    """A site worker, 1.78 m, standing: dark work clothes, a high-visibility vest with reflective
    bands, a white hard hat. Faces +Z, right arm reaching forward (to a fuel nozzle)."""
    p = K.Piece(bevel=0.01)
    for sx in (-1, 1):                                                                      # legs and boots
        p.box((sx * 0.13 - 0.07, 0.0, -0.06), (sx * 0.13 + 0.07, 0.1, 0.16), "rubber", 0.0)
        p.box((sx * 0.13 - 0.075, 0.1, -0.07), (sx * 0.13 + 0.075, 0.86, 0.08), "workwear", 0.0)
    p.box((-0.2, 0.84, -0.1), (0.2, 1.0, 0.1), "workwear", 0.0)                             # hips
    p.box((-0.22, 1.0, -0.12), (0.22, 1.48, 0.12), "hivis", 0.02)                           # vest
    for y in (1.12, 1.3):
        p.box((-0.225, y, -0.125), (0.225, y + 0.05, 0.125), "reflect", 0.0)                # reflective bands
    p.cyl((0.0, 1.48, 0.0), (0.0, 1.54, 0.0), 0.06, "skin", 8)                              # neck
    p.cyl((0.0, 1.54, 0.01), (0.0, 1.7, 0.01), 0.1, "skin", 10, r2=0.09)                   # head
    p.cyl((0.0, 1.68, 0.01), (0.0, 1.78, 0.01), 0.125, "reflect", 12, r2=0.09)             # hard hat
    p.cyl((0.0, 1.68, 0.03), (0.0, 1.70, 0.03), 0.145, "reflect", 12)                       # its brim
    p.cyl((-0.27, 1.44, 0.0), (-0.29, 0.98, 0.04), 0.055, "workwear", 6)                    # left arm, down
    p.cyl((0.27, 1.44, 0.0), (0.3, 1.32, 0.36), 0.055, "workwear", 6)                       # right arm, forward
    p.cyl((0.3, 1.32, 0.36), (0.3, 1.36, 0.55), 0.05, "workwear", 6)
    p.box((0.26, 1.31, 0.55), (0.34, 1.41, 0.63), "skin", 0.0)                              # hand
    single("worker", p)


PROPS = {
    "conifer_a": (conifer_a, 0.3), "conifer_b": (conifer_b, 0.35), "conifer_c": (conifer_c, 0.35),
    "shrub_a": (shrub_a, 0.55), "shrub_b": (shrub_b, 0.5),
    "rock_a": (rock("rock_a", (1.3, 0.7, 1.0), 6), 0.6), "rock_b": (rock("rock_b", (1.0, 0.9, 1.1), 7), 0.6),
    "rock_c": (rock("rock_c", (0.9, 0.6, 0.8), 8), 0.6),
    "site_office": (site_office, 0.6), "workshop": (workshop, 0.6),
    "container_blue": (container("box_blue"), 0.6), "container_red": (container("box_red"), 0.6),
    "fuel_tank": (fuel_tank, 0.5), "light_tower": (light_tower, 0.5),
    "cone": (cone, 0.6), "barrier": (barrier, 0.8), "site_sign": (site_sign, 0.7),
    "crusher_plant": (crusher_plant, 0.5), "worker": (worker, 0.6),
}
BUDGET = {"crusher_plant": 22000}            # triangles at LOD0; everything else 6000
PLANAR_LOD1 = {"crusher_plant"}              # LOD1 by planar dissolve (see sitepulse_kit.make_lod1)


def build(name, out):
    fn, ratio = PROPS[name]
    K.reset(name, out)
    fn(random.Random("sitepulse-" + name))                     # deterministic per prop
    K.make_lod1(ratio, planar=name in PLANAR_LOD1)
    rep = K.report(budget_lod0=BUDGET.get(name, 6000))
    K.export(name)
    print(f"{name:<16} LOD0 {rep['tris_LOD0']:>6}  LOD1 {rep['tris_LOD1']:>6}  {rep['bbox_unity_m']}", flush=True)
    return dict(tris_LOD0=rep["tris_LOD0"], tris_LOD1=rep["tris_LOD1"], bbox=rep["bbox_unity_m"],
                materials=rep["materials"], ok=rep["budget"]["ok"])


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    out = os.path.join(os.path.dirname(HERE), "out", "props")
    if "--out" in argv:
        i = argv.index("--out")
        out = argv[i + 1]
        del argv[i:i + 2]
    if "--one" in argv:
        # a child: build one prop and leave its summary for the parent
        name = argv[argv.index("--one") + 1]
        with open(os.path.join(out, f".check_{name}.json"), "w") as f:
            json.dump(build(name, out), f)
        return
    # Every prop is built in a fresh Blender, so no prop's files depend on what was built
    # before it in the same session or on which props were asked for.
    import subprocess
    names = argv or list(PROPS)
    os.makedirs(out, exist_ok=True)
    summary = {}
    for name in names:
        if name not in PROPS:
            raise SystemExit(f"unknown prop: {name}")
        subprocess.run([bpy.app.binary_path, "--background", "--factory-startup", "--python", os.path.abspath(__file__),
                        "--", "--one", name, "--out", out], check=True)
        check = os.path.join(out, f".check_{name}.json")
        with open(check) as f:
            summary[name] = json.load(f)
        os.remove(check)
    with open(os.path.join(out, "props_check.json"), "w") as f:
        json.dump(summary, f, indent=1)
    print("ALL_OK", all(v["ok"] for v in summary.values()))


main()
