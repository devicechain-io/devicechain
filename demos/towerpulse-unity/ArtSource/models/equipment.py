# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Plant on the roofs and on the ground: the rooftop air-handling unit (ahu.glb, instanced three times)
and the air-cooled chiller (chiller.glb, instanced twice).

Both stand with their origin on the ground (or the roof) at the centre of their pad and face +Z.

  ahu.glb      Root / Body, Fan, Anchor
               Fan: pivot at the hub of the exhaust fan on top. Spin it about its local Y axis. Its blades
               are pitched, so a turning fan reads as one; the shroud and guard under it do not move.
  chiller.glb  Root / Body, Fan_1..Fan_4, StatusLamp, Anchor
               Fan_1..4: the four condenser fans along the top, hubs on the unit's centre line from -X
               to +X; spin each about its local Y. StatusLamp: a small emissive lamp on a post, for the
               running / tripped indication (swap its emission colour in Unity).
"""
import math

import towerpulse_kit as K


def _piece():
    return K.Piece(bevel=0.0)


def fan_blades(p, hub, r_blade, n, key="blade", pitch_deg=22, chord=0.2):
    """A hub and n pitched blades about the Y axis through `hub` (Unity coordinates)."""
    hx, hy, hz = hub
    p.cyl((hx, hy - 0.05, hz), (hx, hy + 0.07, hz), 0.12, "steel_d", 10)
    pitch = math.radians(pitch_deg)
    for k in range(n):
        a = 2 * math.pi * k / n
        d = (math.cos(a), 0.0, math.sin(a))                 # radial
        t = (-math.sin(a), 0.0, math.cos(a))                # tangent
        v = (t[0] * math.cos(pitch), -math.sin(pitch), t[2] * math.cos(pitch))      # tilted tangent
        c = (hx + d[0] * (0.1 + r_blade / 2), hy, hz + d[2] * (0.1 + r_blade / 2))
        p.obox(c, d, v, r_blade, chord, 0.02, key)


def shroud(p, c, r_out, r_in, h, seg):
    """Fan shroud (ring) with a dark opening and a two-bar guard across it."""
    x, y, z = c
    p.ring((x, y, z), r_out, r_in, h, "steel", seg)
    p.cyl((x, y, z), (x, y + h - 0.04, z), r_in, "louvre", seg)
    with p.detail():
        p.box((x - r_in, y + h - 0.01, z - 0.015), (x + r_in, y + h + 0.015, z + 0.015), "frame", 0.0)
        p.box((x - 0.015, y + h - 0.01, z - r_in), (x + 0.015, y + h + 0.015, z + r_in), "frame", 0.0)


def ahu():
    K.node("Root", None, (0, 0, 0))
    K.node("Body", "Root", (0, 0, 0))
    p = K.Piece(bevel=0.03)
    p.box((-2.45, 0.0, -1.15), (2.45, 0.25, 1.15), "pad", 0.0)                           # curb
    p.box((-2.3, 0.25, -1.0), (2.3, 2.15, 1.0), "unit")                                   # casing
    p.box((2.3, 0.65, -0.75), (2.36, 1.85, 0.75), "louvre", 0.0)                          # intake louvre, +X end
    p.box((-2.9, 0.75, -0.6), (-2.3, 1.75, 0.6), "steel", 0.0)                            # supply duct stub, -X end
    for zs in (-1, 1):
        p.box((-2.2, 1.05, zs * 1.0 - (0.01 if zs > 0 else 0.02)), (2.2, 1.35, zs * 1.0 + (0.02 if zs > 0 else 0.01)), "unit_blue", 0.0)
    with p.detail():
        for x in (-1.5, -0.3, 0.9):                                                       # access panel seams
            for zs in (-1, 1):
                p.box((x, 0.35, zs * 1.0 - 0.012), (x + 0.03, 1.95, zs * 1.0 + 0.012), "steel_d", 0.0)
        p.box((-0.6, 0.55, -1.08), (-0.1, 0.9, -1.0), "frame", 0.0)                       # controller box
    shroud(p, (1.3, 2.15, 0.0), 0.78, 0.70, 0.28, 20)
    K.attach("Body", p)
    K.node("Fan", "Root", (1.3, 2.3, 0.0), size=0.3)
    q = _piece()
    fan_blades(q, (1.3, 2.3, 0.0), 0.62, 5)
    K.attach("Fan", q)
    K.node("Anchor", "Root", (0, 2.9, 0), size=0.2)
    return dict(expect_nodes=["Root", "Body", "Fan", "Anchor"], fans=["Fan"])


def chiller():
    K.node("Root", None, (0, 0, 0))
    K.node("Body", "Root", (0, 0, 0))
    p = K.Piece(bevel=0.03)
    p.box((-3.2, 0.0, -1.5), (3.2, 0.15, 1.5), "pad", 0.0)
    p.box((-2.9, 0.15, -1.05), (2.9, 0.4, 1.05), "steel_d", 0.0)                          # base rails
    p.box((-2.9, 0.4, -1.1), (2.9, 2.0, 1.1), "unit")                                      # casing
    for zs in (-1, 1):                                                                    # coil guards, both long sides
        z0, z1 = (zs * 1.1, zs * 1.13)
        p.box((-2.6, 0.6, min(z0, z1)), (2.6, 1.85, max(z0, z1)), "louvre", 0.0)
    p.box((2.9, 0.7, -0.5), (3.0, 1.8, 0.5), "steel", 0.0)                                 # control cabinet, +X end
    for z in (-0.4, 0.4):                                                                  # chilled-water connections, -X end
        p.cyl((-2.9, 0.75, z), (-3.5, 0.75, z), 0.1, "steel", 10)
        p.cyl((-3.45, 0.75, z), (-3.5, 0.75, z), 0.15, "steel_d", 10)
    p.box((2.45, 2.0, 0.7), (2.65, 2.2, 0.9), "steel_d", 0.0)                              # lamp post
    with p.detail():
        for x in (-1.45, 0.0, 1.45):
            for zs in (-1, 1):
                p.box((x, 0.45, zs * 1.1 - 0.012), (x + 0.03, 1.95, zs * 1.1 + 0.012), "steel_d", 0.0)
        p.box((2.99, 1.0, 0.35), (3.03, 1.3, 0.42), "frame", 0.0)                          # handle
    fan_x = (-2.1, -0.7, 0.7, 2.1)
    for x in fan_x:
        shroud(p, (x, 2.0, 0.0), 0.62, 0.55, 0.22, 16)
    K.attach("Body", p)
    for i, x in enumerate(fan_x, 1):
        K.node("Fan_%d" % i, "Root", (x, 2.18, 0.0), size=0.3)
        q = _piece()
        fan_blades(q, (x, 2.18, 0.0), 0.44, 4, chord=0.17)
        K.attach("Fan_%d" % i, q)
    K.node("StatusLamp", "Root", (2.55, 2.28, 0.8), size=0.2)
    q = _piece()
    q.cyl((2.55, 2.2, 0.8), (2.55, 2.36, 0.8), 0.07, "lamp", 10)
    K.attach("StatusLamp", q)
    K.node("Anchor", "Root", (0, 3.0, 0), size=0.2)
    return dict(expect_nodes=["Root", "Body", "Fan_1", "Fan_2", "Fan_3", "Fan_4", "StatusLamp", "Anchor"],
                fans=["Fan_1", "Fan_2", "Fan_3", "Fan_4"])
