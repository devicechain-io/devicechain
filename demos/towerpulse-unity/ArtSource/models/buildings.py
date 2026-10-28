# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The two buildings: tower.glb (5 floors, 4 zones a floor round a lift-and-stair core) and annex.glb
(2 floors, 2 labs a floor, a stair tower on its north face).

Origin: on the ground at the centre of the footprint; the building is placed unrotated at its
`layout.BUILDINGS[..]["pos"]`. Floor level of floor n is y = (n - 1) * 3.8 (floor to floor 3.8 m).

NODE TREE (the names are the contract with Unity; see ArtSource/README.md)

  Root
    Floor_F<n>                 pivot at the floor level, footprint centre        n = 1..floors
      Slab_F<n>                the floor slab (its top face is the floor)
      Zone_F<n>-<Wing|LabX>    the zone's room volume: an alpha-blended box, one material (M_ZoneTint)
                               that Unity tints per zone. Pivot at the room's floor centre.
      Anchor_F<n>-<Wing|LabX>  empty 2.6 m above the room's floor centre, for a card or a label
    Roof                       pivot at the roof level: slab, membrane, parapet
      Mount_<device id>        empty where a rooftop unit stands (an `ahu` instance goes here)
    Facade_North/East/South/West   the curtain wall, one node a side. THE CUTAWAY: disable the
                               GameObjects of the sides that face the camera and the interiors show.
    Core, CoreLifts, CoreStairs    (the annex has Core only: its stair tower is outside the footprint)
    Entrance                   canopy, posts and door frame; does not belong to a facade, so it stays
                               when the facade it stands on is cut away

Zone volumes sit 0.04 m inside their room and 0.03..3.45 m above the floor, so neighbours never
share a face and the slab edges stay visible. Furniture and people are not in these files: the
campus layout (campus_layout.json) lists where they go.
"""
import math

import towerpulse_kit as K
import layout as L

H = L.FLOOR_H
T = L.SLAB_T
SIDES = ("North", "East", "South", "West")


def side_box(b, side, u0, u1, y0, y1, t0, t1):
    """A box on a facade plane: u runs along the wall, t outward from the slab edge."""
    hx, hz = b["hx"], b["hz"]
    if side == "North":
        return (u0, y0, hz + t0), (u1, y1, hz + t1)
    if side == "South":
        return (u0, y0, -hz - t1), (u1, y1, -hz - t0)
    if side == "East":
        return (hx + t0, y0, u0), (hx + t1, y1, u1)
    return (-hx - t1, y0, u0), (-hx - t0, y1, u1)


def _piece():
    return K.Piece(bevel=0.0)


def facade(b, side):
    """Curtain wall for one side: spandrel band over every slab edge, one glass pane a floor, and
    a mullion at every bay line the full height."""
    p = _piece()
    fl = b["floors"]
    length = 2 * (b["hx"] if side in ("North", "South") else b["hz"])
    ext = 0.16 if side in ("North", "South") else 0.0      # north and south wrap the corners
    lo, hi = -length / 2 - ext, length / 2 + ext
    roof = L.roof_y(b)
    p.box(*side_box(b, side, lo, hi, -0.15, 0.45, 0.0, 0.10), "concrete_d")        # ground plinth
    for f in range(1, fl + 1):
        F = L.floor_y(f)
        g0 = 0.45 if f == 1 else F + 0.6
        p.box(*side_box(b, side, -length / 2, length / 2, g0, F + H - T, 0.03, 0.07), "glass")
        if f > 1:
            p.box(*side_box(b, side, lo, hi, F - T, F + 0.6, 0.0, 0.10), "spandrel")
    p.box(*side_box(b, side, lo, hi, roof - T, roof, 0.0, 0.10), "spandrel")       # roof-slab edge
    n = max(1, round(length / 1.9))
    bay = length / n
    with p.detail():
        for k in range(n + 1):
            u = -length / 2 + k * bay
            p.box(*side_box(b, side, u - 0.045, u + 0.045, 0.0, roof, 0.0, 0.16), "frame", 0.0)
    name = "Facade_" + side
    centre = {"North": (0, 0, b["hz"]), "South": (0, 0, -b["hz"]), "East": (b["hx"], 0, 0), "West": (-b["hx"], 0, 0)}[side]
    K.node(name, "Root", centre)
    K.attach(name, p)


def slab(b, f):
    F = L.floor_y(f)
    p = _piece()
    depth = 0.6 if f == 1 else T
    p.box((-b["hx"], F - depth, -b["hz"]), (b["hx"], F, b["hz"]), "concrete")
    p.box((-b["hx"] + 0.02, F, -b["hz"] + 0.02), (b["hx"] - 0.02, F + 0.02, b["hz"] - 0.02), "concrete_d")   # floor finish
    return p


def zone_volume(z):
    x0, x1, z0, z1 = L.volume_rect(z)
    F = L.floor_y(z["floor"])
    p = _piece()
    p.box((x0, F + L.ZONE_Y0, z0), (x1, F + L.ZONE_Y1, z1), "zone")
    return p


def roof(b, mounts):
    Hh = L.roof_y(b)
    K.node("Roof", "Root", (0, Hh, 0))
    p = _piece()
    hx, hz = b["hx"], b["hz"]
    p.box((-hx, Hh - T, -hz), (hx, Hh, hz), "concrete")
    p.box((-hx + 0.2, Hh, -hz + 0.2), (hx - 0.2, Hh + 0.03, hz - 0.2), "roofing")
    pw, ph = 0.2, 0.9
    p.box((-hx, Hh, hz - pw), (hx, Hh + ph, hz), "concrete_d")
    p.box((-hx, Hh, -hz), (hx, Hh + ph, -hz + pw), "concrete_d")
    p.box((hx - pw, Hh, -hz + pw), (hx, Hh + ph, hz - pw), "concrete_d")
    p.box((-hx, Hh, -hz + pw), (-hx + pw, Hh + ph, hz - pw), "concrete_d")
    K.attach("Roof", p)
    for m in mounts:
        K.node("Mount_" + m["id"], "Roof", (m["x"], Hh, m["z"]), size=0.5)


def entrance(b):
    e = b["entrance"]
    side, at, w = e["side"], e["at"], e["width"]
    p = _piece()
    p.box(*side_box(b, side, at - w / 2, at + w / 2, 3.05, 3.25, 0.0, 2.6), "frame")                 # canopy
    for du in (-w / 2 + 0.1, w / 2 - 0.1):
        p.box(*side_box(b, side, at + du - 0.08, at + du + 0.08, 0.0, 3.05, 2.45, 2.61), "steel")      # posts
    d = 0.9
    p.box(*side_box(b, side, at - d - 0.08, at - d, 0.0, 2.5, 0.0, 0.18), "frame")                    # door frame
    p.box(*side_box(b, side, at + d, at + d + 0.08, 0.0, 2.5, 0.0, 0.18), "frame")
    p.box(*side_box(b, side, at - d, at + d, 2.42, 2.5, 0.0, 0.18), "frame")
    p.box(*side_box(b, side, at - 0.02, at + 0.02, 0.0, 2.42, 0.07, 0.15), "frame")                  # leaf split
    pos = {"north": (at, 0, b["hz"]), "south": (at, 0, -b["hz"]), "east": (b["hx"], 0, at), "west": (-b["hx"], 0, at)}[side.lower()]
    K.node("Entrance", "Root", pos)
    K.attach("Entrance", p)


# ---- the tower's core -----------------------------------------------------------------------------
def tower_core(b):
    c = b["core"]
    x0, x1, z0, z1 = c["x0"], c["x1"], c["z0"], c["z1"]
    roof_h = L.roof_y(b)
    wall = 0.25
    K.node("Core", "Root", ((x0 + x1) / 2, 0, (z0 + z1) / 2))
    p = _piece()
    top = roof_h
    p.box((x0, -0.3, z0), (x0 + wall, top, z1), "plaster")                  # west
    p.box((x1 - wall, -0.3, z0), (x1, top, z1), "plaster")                  # east
    p.box((x0 + wall, -0.3, z0), (x1 - wall, top, z0 + wall), "plaster")    # south (the lift wall)
    # north: a glazed stair wall -- a pane, a post at each end and a transom at every floor
    p.box((x0 + wall, 0.0, z1 - 0.12), (x1 - wall, top, z1 - 0.08), "glass")
    with p.detail():
        for f in range(0, b["floors"] + 1):
            y = L.floor_y(f + 1)
            p.box((x0 + wall, y - 0.04, z1 - 0.16), (x1 - wall, y + 0.04, z1 - 0.04), "frame", 0.0)
        for u in (x0 + wall, 0.0, x1 - wall - 0.08):
            p.box((u, 0.0, z1 - 0.16), (u + 0.08, top, z1 - 0.04), "frame", 0.0)
    p.box((x0, top, z0), (x1, top + 3.0, z1), "concrete")                   # lift overrun
    p.box((x0 - 0.1, top + 3.0, z0 - 0.1), (x1 + 0.1, top + 3.1, z1 + 0.1), "concrete_d")
    K.attach("Core", p)

    # lift doors on the south wall, every floor, two lifts
    K.node("CoreLifts", "Root", (0, 0, z0))
    p = _piece()
    for f in range(1, b["floors"] + 1):
        F = L.floor_y(f)
        for xc in (-0.95, 0.95):
            p.box((xc - 0.75, F, z0 - 0.02), (xc + 0.75, F + 2.3, z0), "frame")
            p.box((xc - 0.56, F, z0 - 0.05), (xc - 0.02, F + 2.15, z0 - 0.02), "steel")
            p.box((xc + 0.02, F, z0 - 0.05), (xc + 0.56, F + 2.15, z0 - 0.02), "steel")
    K.attach("CoreLifts", p)

    # a dog-leg stair behind the glazed wall: two flights and a landing a floor
    K.node("CoreStairs", "Root", (0, 0, (z0 + z1) / 2 + 1.0))
    p = _piece()
    xa, xb = x0 + wall, x1 - wall
    for f in range(1, b["floors"] + 1):
        F = L.floor_y(f)
        _flight(p, F, xa, 0.45, 0.2, 1.15, F + 0.0, 10, 0.19)               # up along +x
        p.box((0.45, F + 1.78, 0.2), (xb, F + 1.9, z1 - 0.2), "concrete")  # landing
        _flight(p, F, 0.45, xa, 1.3, z1 - 0.2, F + 1.9, 10, 0.19)           # back along -x
    K.attach("CoreStairs", p)


def _flight(p, floor_f, x_from, x_to, za, zb, y_from, steps, rise):
    """A solid stepped flight climbing from x_from to x_to. Its underside is flat at y_from - 1.0
    (never below the floor), so it reads as a concrete stair rather than a floating ramp."""
    s = 1.0 if x_to > x_from else -1.0
    run = abs(x_to - x_from) / steps
    base = max(floor_f, y_from - 1.0)
    pts = [(x_from, base), (x_to, base), (x_to, y_from + steps * rise)]
    for i in range(steps - 1, -1, -1):
        pts.append((x_from + s * i * run, y_from + (i + 1) * rise))
        if i > 0:
            pts.append((x_from + s * i * run, y_from + i * rise))
    p.prism(pts, "z", za, zb, "concrete", 0.0)


def annex_core(b):
    c = b["core"]
    x0, x1, z0, z1 = c["x0"], c["x1"], c["z0"], c["z1"]
    top = L.roof_y(b) + 0.9
    K.node("Core", "Root", ((x0 + x1) / 2, 0, (z0 + z1) / 2))
    p = _piece()
    p.box((x0, -0.3, z0), (x1, top, z1), "plaster")
    p.box((x0 - 0.1, top, z0 - 0.1), (x1 + 0.1, top + 0.12, z1 + 0.1), "roofing")
    p.box((-0.55, 0.5, z1), (0.55, top - 0.4, z1 + 0.04), "glass")          # tall stair window on the north face
    p.box((-0.9, 0.0, z1), (0.9, 2.4, z1 + 0.03), "frame")                   # door surround, ground floor
    with p.detail():
        for y in (L.floor_y(2),):
            p.box((-0.6, y - 0.05, z1), (0.6, y + 0.05, z1 + 0.08), "frame", 0.0)
        for u in (-0.6, 0.55):
            p.box((u, 0.5, z1), (u + 0.05, top - 0.4, z1 + 0.08), "frame", 0.0)
    K.attach("Core", p)


# ---- assemble ---------------------------------------------------------------------------------------
def build_building(b, mounts):
    name = b["name"]
    zs = [z for z in L.zones() if z["building"] == name]
    K.node("Root", None, (0, 0, 0))
    for f in range(1, b["floors"] + 1):
        F = L.floor_y(f)
        K.node("Floor_F%d" % f, "Root", (0, F, 0), size=0.6)
        K.node("Slab_F%d" % f, "Floor_F%d" % f, (0, F, 0))
        K.attach("Slab_F%d" % f, slab(b, f))
        for z in (z for z in zs if z["floor"] == f):
            K.node(z["node"], "Floor_F%d" % f, (z["cx"], F, z["cz"]), size=0.4)
            K.attach(z["node"], zone_volume(z))
            K.node(z["anchor"], "Floor_F%d" % f, (z["cx"], F + 2.6, z["cz"]), size=0.3)
    roof(b, mounts)
    for side in SIDES:
        facade(b, side)
    if b["core"]["kind"] == "interior":
        tower_core(b)
    else:
        annex_core(b)
    entrance(b)
    return dict(zones=[z["node"] for z in zs],
                expect_nodes=["Root", "Roof", "Entrance", "Core"] + ["Facade_" + s for s in SIDES]
                + [z["node"] for z in zs] + [z["anchor"] for z in zs]
                + ["Floor_F%d" % f for f in range(1, b["floors"] + 1)] + ["Slab_F%d" % f for f in range(1, b["floors"] + 1)]
                + ["Mount_" + m["id"] for m in mounts]
                + (["CoreLifts", "CoreStairs"] if b["core"]["kind"] == "interior" else []))


def tower(_K=None):
    return build_building(L.TOWER, [a for a in L.AHUS if a["building"] == "tower"])


def annex(_K=None):
    return build_building(L.ANNEX, [a for a in L.AHUS if a["building"] == "annex"])


def checks(info, building):
    """Geometry facts the generic checks cannot know: zone rectangles tile the footprint without
    overlap, a floor is 3.8 m, and the zone volumes stay inside the slab."""
    b = L.BUILDINGS[building]
    zs = [z for z in L.zones() if z["building"] == building]
    out = {}
    per_floor = {}
    for z in zs:
        per_floor.setdefault(z["floor"], []).append(z)
    overlap = []
    for f, row in per_floor.items():
        for i, a in enumerate(row):
            ra = L.volume_rect(a)
            for c in row[i + 1:]:
                rc = L.volume_rect(c)
                if ra[0] < rc[1] and rc[0] < ra[1] and ra[2] < rc[3] and rc[2] < ra[3]:
                    overlap.append((a["id"], c["id"]))
    out["zones_disjoint"] = dict(ok=not overlap, overlapping=overlap)
    out["zones_inside_slab"] = dict(ok=all(L.volume_rect(z)[0] >= -b["hx"] and L.volume_rect(z)[1] <= b["hx"]
                                           and L.volume_rect(z)[2] >= -b["hz"] and L.volume_rect(z)[3] <= b["hz"] for z in zs))
    area = {z["id"]: round(z["w"] * z["d"], 1) for z in zs}
    out["zone_areas_plausible"] = dict(ok=all(70 <= a <= 100 for a in area.values()), areas=area)
    ys = [K.NODES["Floor_F%d" % f].matrix_world.translation.z for f in range(1, b["floors"] + 1)]
    gaps = [round(ys[i + 1] - ys[i], 4) for i in range(len(ys) - 1)]
    out["floor_to_floor_3p8"] = dict(ok=all(abs(g - 3.8) < 1e-3 for g in gaps), gaps=gaps)
    expect = len(zs)
    found = [n for n in K.NODES if n.startswith("Zone_")]
    out["one_node_per_zone"] = dict(ok=len(found) == expect and len(set(found)) == expect
                                    and sorted(found) == sorted(z["node"] for z in zs), expected=expect, found=len(found))
    return out
