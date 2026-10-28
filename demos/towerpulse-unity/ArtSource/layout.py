# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The Towerpulse campus on paper: building footprints, the 24 zones, where the rooftop units, chillers,
furniture and paths go. Pure Python (no Blender), so the model scripts, the checks, the preview and
the committed `campus_layout.json` all read one source.

Frame: Unity metres, X east, Y up, Z north. Every building is authored with its origin on the ground at
the centre of its footprint and is placed unrotated, so a point local to a building is the world point
minus the building's position.

Zone naming. The scenario's zone ids are `tower-f4-east` and `annex-f1-laba`; the node that carries a
zone's tint volume is named from them by `node_name`:

    tower-f4-east  ->  Zone_F4-East          (in tower.glb)
    annex-f1-laba  ->  Zone_F1-LabA          (in annex.glb)

Floor, then the wing or lab, each capitalised. A zone id is the same string with the building first, so
Unity looks a node up as `node_name(id)` in the building named by the id's first segment.
"""
import json
import math

FLOOR_H = 3.8           # floor to floor, m
SLAB_T = 0.30           # floor slab thickness, m
ZONE_INSET = 0.04       # a zone volume sits this far inside its room, so neighbours never share a face
ZONE_Y0, ZONE_Y1 = 0.03, 3.45     # zone volume bottom and top above the floor level

WINGS = ("north", "east", "south", "west")


def cap(s):
    return s[:1].upper() + s[1:]


def node_name(zone_id):
    """Zone id -> the tint-volume node's name."""
    parts = zone_id.split("-")
    if len(parts) != 3:
        raise ValueError("not a zone id: " + zone_id)
    return "Zone_" + parts[1].upper() + "-" + (cap(parts[2][:-1]) + parts[2][-1].upper() if parts[2].startswith("lab") else cap(parts[2]))


# ---- building footprints -------------------------------------------------------------------------
TOWER = dict(name="tower", floors=5, hx=9.5, hz=9.5, pos=(17.0, 0.0, 2.0),
             core=dict(x0=-2.5, x1=2.5, z0=-2.5, z1=2.5, kind="interior"),
             entrance=dict(side="west", at=0.0, width=4.5))
ANNEX = dict(name="annex", floors=2, hx=9.0, hz=5.0, pos=(-17.0, 0.0, 0.0),
             core=dict(x0=-1.75, x1=1.75, z0=5.16, z1=8.16, kind="exterior"),
             entrance=dict(side="east", at=0.0, width=3.6))
BUILDINGS = {"tower": TOWER, "annex": ANNEX}


def roof_y(b):
    return b["floors"] * FLOOR_H


def floor_y(floor):
    """Finished floor level of floor n (1-based), m above the ground."""
    return (floor - 1) * FLOOR_H


def _tower_wing_rect(wing):
    """The four wings pinwheel round the 5 m core: each is 12 m x 7 m."""
    h, c = TOWER["hx"], TOWER["core"]["x1"]
    return {
        "north": (-h, c, c, h),
        "east": (c, h, -c, h),
        "south": (-c, h, -h, -c),
        "west": (-h, -c, -h, c),
    }[wing]


def zones():
    """Every zone: id, node, anchor node, building, floor, rectangle (building-local), capacity."""
    out = []
    for floor in range(1, TOWER["floors"] + 1):
        for wing in WINGS:
            x0, x1, z0, z1 = _tower_wing_rect(wing)
            zid = "tower-f%d-%s" % (floor, wing)
            out.append(_zone(zid, "tower", floor, x0, x1, z0, z1, 6 if (floor == 4 and wing == "east") else 8))
    for floor in range(1, ANNEX["floors"] + 1):
        for lab, (x0, x1) in (("a", (0.0, ANNEX["hx"])), ("b", (-ANNEX["hx"], 0.0))):
            zid = "annex-f%d-lab%s" % (floor, lab)
            out.append(_zone(zid, "annex", floor, x0, x1, -ANNEX["hz"], ANNEX["hz"], 6))
    return out


def _zone(zid, building, floor, x0, x1, z0, z1, capacity):
    n = node_name(zid)
    return dict(id=zid, node=n, anchor="Anchor_" + n[len("Zone_"):], building=building, floor=floor,
                x0=x0, x1=x1, z0=z0, z1=z1, cx=(x0 + x1) / 2, cz=(z0 + z1) / 2,
                w=x1 - x0, d=z1 - z0, capacity=capacity)


def volume_rect(z):
    """The tint volume's rectangle: the room less the inset (and the facade skin on outer walls)."""
    i = ZONE_INSET
    return (z["x0"] + i, z["x1"] - i, z["z0"] + i, z["z1"] - i)


# ---- furniture -----------------------------------------------------------------------------------
def _rot(yaw_deg, x, z):
    """Unity yaw about +Y (positive turns +Z toward +X) applied to an offset."""
    a = math.radians(yaw_deg)
    return (x * math.cos(a) + z * math.sin(a), -x * math.sin(a) + z * math.cos(a))


def zone_furniture(z):
    """Desks, chairs and (in the hot zone) the meeting table, in building-local coordinates.
    Returns (items, seats): items are dicts prop/x/z/yaw; seats are the chairs, in order."""
    items, seats = [], []
    cx, cz = z["cx"], z["cz"]
    long_x = z["w"] >= z["d"]
    L, D = (z["w"], z["d"]) if long_x else (z["d"], z["w"])      # along and across the long axis
    rot = 0.0 if long_x else 90.0                                # the whole arrangement turns with the room

    def put(prop, u, v, yaw, bucket):
        dx, dz = _rot(rot, u, v)
        item = dict(prop=prop, x=round(cx + dx, 3), z=round(cz + dz, 3), yaw=(yaw + rot) % 360)
        bucket.append(item)

    if z["id"] == "tower-f4-east":
        put("meeting_table", 0.0, 0.0, 0, items)
        for k, u in enumerate((-0.9, 0.0, 0.9)):
            put("chair", u, -0.95, 0, seats)           # south side, facing the table
            put("chair", u, 0.95, 180, seats)          # north side
        items.extend(seats)
        return items, seats
    n = z["capacity"] // 2                                       # desks per row
    step = 2.8
    us = [(k - (n - 1) / 2) * step for k in range(n)]
    for u in us:
        put("desk", u, -D / 2 + 1.0, 180, items)       # row against the one long wall; user faces it
        put("chair", u, -D / 2 + 1.0 + 0.72, 180, seats)
        put("desk", u, D / 2 - 1.0, 0, items)
        put("chair", u, D / 2 - 1.0 - 0.72, 0, seats)
    items.extend(seats)
    return items, seats


# ---- campus --------------------------------------------------------------------------------------
# Rooftop units and chillers, by device id. Positions are building-local for rooftop units (y is the
# roof level) and world for chillers. yaw is degrees about +Y.
AHUS = [
    dict(id="ahu-tower-low", building="tower", x=-5.5, z=6.0, yaw=0, serves="floors 1-3"),
    dict(id="ahu-tower-high", building="tower", x=5.5, z=-6.0, yaw=0, serves="floors 4-5"),
    dict(id="ahu-annex", building="annex", x=-3.5, z=-1.0, yaw=0, serves="annex"),
]
CHILLERS = [
    dict(id="chiller-tower", x=31.0, z=4.0, yaw=90),
    dict(id="chiller-annex", x=-17.0, z=-10.0, yaw=0),
]

# Ground: rectangles (x0, x1, z0, z1, material key, top y). The lawn is the lowest; kerbs stand
# 0.12 m proud of it.
LAWN = (-50.0, 52.0, -42.0, 38.0)
LAWN_TOP = -0.15
PAVE_TOP = -0.03
PLAZA = (-7.2, 6.8, -12.0, 12.0)
PATHS = [
    (-1.4, 1.4, -42.0, -12.0),          # the spine out to the street
    (-1.4, 1.4, 12.0, 20.0),            # north link
    (6.8, 7.5, 0.4, 3.6),               # tower entrance step across the gap to the facade
    (-8.0, -7.2, -1.8, 1.8),            # annex entrance
    (7.5, 29.0, 15.5, 17.5),            # service path behind the tower
    (28.8, 33.2, -2.5, 17.5),           # to the tower chiller
    (-23.0, -11.0, -13.0, -11.5),       # beside the annex chiller
    (-17.9, -16.1, -11.5, -5.2),        # annex chiller to the annex
]
TREES = [(-9.0, 18.0), (-14.0, 22.0), (-22.0, 17.0), (-30.0, 12.0), (-31.0, -6.0), (-29.0, -18.0),
         (-12.0, -24.0), (-5.0, -30.0), (6.0, -27.0), (14.0, -22.0), (24.0, -20.0), (36.0, -14.0),
         (38.0, 14.0), (22.0, 28.0), (8.0, 27.0), (-4.0, 24.0), (40.0, 0.0), (-36.0, 4.0)]
PLANTERS = [(-5.2, 9.5), (4.8, 9.5), (-5.2, -9.5), (4.8, -9.5)]
BOLLARDS = [(-3.0, y) for y in (-9.0, -4.5, 0.0, 4.5, 9.0)] + [(3.0, y) for y in (-9.0, -4.5, 0.0, 4.5, 9.0)]


def campus_layout():
    zs = zones()
    for z in zs:
        z["furniture"], z["seats"] = zone_furniture(z)
    return dict(
        note="Generated by ArtSource/layout.py; do not edit. Unity metres, X east, Y up, Z north.",
        floorHeight=FLOOR_H,
        buildings=[dict(id=b["name"], model=b["name"], position=list(b["pos"]), floors=b["floors"],
                        roofY=roof_y(b), halfX=b["hx"], halfZ=b["hz"]) for b in BUILDINGS.values()],
        zones=[dict(id=z["id"], node=z["node"], anchor=z["anchor"], building=z["building"], floor=z["floor"],
                    floorY=floor_y(z["floor"]), capacity=z["capacity"], centre=[z["cx"], z["cz"]],
                    furniture=z["furniture"], seats=z["seats"]) for z in zs],
        rooftopUnits=[dict(id=a["id"], model="ahu", building=a["building"], mount="Mount_" + a["id"],
                           position=[a["x"] + BUILDINGS[a["building"]]["pos"][0],
                                     roof_y(BUILDINGS[a["building"]]),
                                     a["z"] + BUILDINGS[a["building"]]["pos"][2]],
                           yaw=a["yaw"], fanNode="Fan") for a in AHUS],
        chillers=[dict(id=c["id"], model="chiller", position=[c["x"], 0.0, c["z"]], yaw=c["yaw"],
                       fanNodes=["Fan_1", "Fan_2", "Fan_3", "Fan_4"], lampNode="StatusLamp") for c in CHILLERS],
        ground=dict(model="campus_ground", position=[0, 0, 0]),
    )


if __name__ == "__main__":
    import sys
    path = sys.argv[1] if len(sys.argv) > 1 else None
    text = json.dumps(campus_layout(), indent=1) + "\n"
    if path:
        with open(path, "w", encoding="utf8", newline="\n") as f:
            f.write(text)
    else:
        sys.stdout.write(text)
