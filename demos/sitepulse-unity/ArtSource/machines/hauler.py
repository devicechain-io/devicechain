# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Stylized, unbranded rigid-frame off-highway HAUL TRUCK (~55 t class) for Sitepulse.

Coordinates: Unity metres (X right, Y up, Z forward, origin on the ground at the wheelbase
centre). [n] = numbered dimension n of the reference spec drawing (a published manufacturer spec sheet for this class,
not committed), [meas] = measured off that drawing (122.4 px/m), [style] = choice.

RIG  (all pivots axis-aligned at rest; Unity localEulerAngles == the values below)
  node                     axis  range                  sign / rule
  SteerFrontLeft/Right     Y     -35 .. +35 deg         + = wheels steer RIGHT (toward +X).
                                                        Unity y == -Blender rotation_euler.z.
                                                        Pivot = kingpin (front suspension strut),
                                                        0.18 m inboard of the tyre's inner face, so
                                                        the wheel swings on a short arc (no scrub).
                                                        Both sides take the same angle; for Ackermann
                                                        use inner=atan(WB/(Rt-KP)), outer=atan(WB/(Rt+KP))
                                                        with WB=4.215, KP=1.06, Rt = turn radius at
                                                        the rear-axle centre line.
  WheelFrontLeft/Right     X     unbounded              x += deg(d / WHEEL_R) for distance d travelled
  WheelRearLeft/Right      X     unbounded              (+d = forward). WHEEL_R = 1.06 m (lug face,
                                                        24.00R35 class) -> 54.05 deg per metre.
                                                        Each rear node carries BOTH dual tyres.
  DumpBody                 X     0 .. -50 deg           x < 0 raises the body (front goes up) about
                                                        the rear hinge pin.
  Canopy                   -     fixed child of DumpBody (cab guard rises with the body).
  Load                     -     child of DumpBody; heaped rock mesh, show/hide only.
                                 LOAD RULE (hysteresis on DumpBody.localEulerAngles.x, signed, deg):
                                   x <  -2.0       -> Load hidden   (Load.SetActive(false))
                                   x >= -0.05      -> Load shown    (body back at rest; 0.05 = float slack)
                                   -2.0 <= x < -0.05 -> keep the previous state
                                 i.e. the heap vanishes in the first 2 deg of a raise (the body has not
                                 visibly moved yet) and returns only once the body is fully down. The
                                 heap mesh pokes through the canopy/tail if left on above ~5 deg.
                                 Unity: read the angle as Mathf.DeltaAngle(0, localEulerAngles.x)
                                 (localEulerAngles wraps -2 to 358). The constants are exported in
                                 hauler_check.json -> rig.load_rule; the runtime component reads them
                                 from there. A payload flag (empty return trip) may AND with it.
  HoistCylinderLeft/Right  aim   local +Y at HoistRodMount<S>     AimConstraint / LookAt
  HoistRodLeft/Right       aim   local +Y at HoistCylinderMount<S>   (glTF carries no constraints)
  Beacon (on the front-right rail post), ExhaustTip (particle point): fixed markers.

SPEED CONSTANTS: wheel_rad_per_m = 1/1.06 = 0.943396 rad/m (54.0526 deg/m), same for front and
rear (one tyre size). No scrolling textures on this machine.
"""
import math, os, json, random

NAME = "hauler"
LOD1_RATIO = 0.45
DUMP_DEG = 50.0          # design: ~50 deg (the reference drawing shows 60 with 2-stage hoists)
STEER_DEG = 35.0         # design: +-35 (reference 31; another truck of the class 39)
LOAD_HIDE_BELOW_DEG = -2.0    # LOAD RULE (see header): DumpBody.x < this -> hide Load
LOAD_SHOW_AT_DEG = -0.05      # DumpBody.x >= this (rest, with float slack) -> show Load
_LOAD = dict(visible=True)    # rule state (hysteresis band) for pose()


def load_rule(dump_x_deg, was_visible):
    """The LOAD RULE on the signed Unity DumpBody.localEulerAngles.x (deg). Single source for
    pose() and the check; Unity's runtime component implements the same three lines."""
    if dump_x_deg < LOAD_HIDE_BELOW_DEG:
        return False
    if dump_x_deg >= LOAD_SHOW_AT_DEG:
        return True
    return was_visible

P = dict(
    wheelbase=4.215,            # [5]
    wheel_r=1.06,               # 24.00R35: OD 83 in = 2.108 m; drawing 2.12 m
    tyre_w=0.72,                # [style] with dual spacing -> overall tyre width [21] 4.40 (4.411)
    lug_h=0.045,                # [style] shallow rock tread (was 0.07: read as tractor lugs)
    lug_pitches=20,             # [style] per row, two staggered rows (was 12)
    lug_w_factor=0.36,          # [style] lug length along the tread / pitch length (was 0.46)
    lug_chevron_deg=12.0,       # [style] each row tilts opposite -> shallow chevron
    front_track=3.205,          # [13]
    rear_dual_centres=2.929,    # [20]
    dual_spacing=0.75,          # [style] -> [21]
    kingpin_x=1.06,             # [style] 0.18 m inboard of the front tyre inner face
    front_z=5.06,               # [4] overall length 10.07 with rear_z
    rear_z=-5.0325,             # [6] rear axle to tail 2.925
    hinge=(0.0, 2.255, -2.664), # [meas] body hinge pin
    hoist_A=(0.70, 0.86, 0.0),  # [style] frame lug (bottom 0.76 ~ ground clearance [7] 0.759)
    hoist_B=(0.70, 1.86, -1.15),# [style] body lug; single-stage geometry, stroke ratio < 2
    body_half_out=3.922 / 2,    # [16]
    body_half_in=3.654 / 2,     # [17]
    body_top=3.771,             # [9] loading height
    body_front_z=1.18, body_tail_z=-5.0325,
    canopy_half=4.886 / 2,      # [15]
    canopy_front_y=4.459,       # [18]
    canopy_front_z=4.22,        # [meas] 2.12 ahead of the front axle
    cab_top=4.108,              # [1]
    cab_x=(-1.95, -0.42), cab_z=(1.60, 3.85),   # [meas] operator on the left
    deck_y=2.44,                # [meas]
    mirror_half=5.673 / 2,      # [12]
    axle_clear=0.56,            # [19]
)
_I = {}

FLOOR_TOP = [(1.06, 2.17), (-0.90, 2.05), (-3.30, 2.62), (-5.0325, 3.24)]   # (z, y) inside floor


def U_floor(z):
    """Underside of the body floor at z (rest pose)."""
    pts = [(z0, y0 - 0.12) for z0, y0 in FLOOR_TOP]
    pts[0] = (P["body_front_z"], pts[0][1])
    for (z0, y0), (z1, y1) in zip(pts, pts[1:]):
        if z1 <= z <= z0:
            return y0 + (y1 - y0) * (z - z0) / (z1 - z0)
    return pts[-1][1] if z < pts[-1][0] else pts[0][1]


# ------------------------------------------------------------------------------------------
def pose(K, dump=0.0, steer=0.0, roll_m=0.0, load=None):
    """dump: body raise in deg (0..50, applied as DumpBody.x = -dump); steer: Unity yaw deg
    (+ = right); roll_m: distance travelled; load: None = apply the LOAD RULE (stateful, like the
    Unity component), False = empty truck (payload flag off; ANDed with the rule)."""
    K.rot_x("DumpBody", -math.radians(dump))
    for s in ("Left", "Right"):
        if "SteerFront" + s in K.NODES:
            K.NODES["SteerFront" + s].rotation_euler = (0, 0, -math.radians(steer))
        for w in ("WheelFront", "WheelRear"):
            K.rot_x(w + s, roll_m / P["wheel_r"])
    _LOAD["visible"] = load_rule(-dump, _LOAD["visible"])
    if "Load" in K.NODES:
        vis = _LOAD["visible"] and load is not False
        K.NODES["Load"].scale = (1, 1, 1) if vis else (1e-4, 1e-4, 1e-4)
    K.update()


# ------------------------------------------------------------------------------------------
def tyre(K, p, c, side, rim_face=True, lugs=True, seg=14):
    """OTR rock tyre (dense shallow staggered chevron lugs, LOD0-only) + deep yellow rim. Axle
    along X. side = -1 for a rim face looking to -X (left side), +1 right. lugs=False (the
    barely visible inner duals) gives a lug-less carcass at the full rolling radius, so it still
    meets the ground; it pays for the denser tread on the four visible tyres."""
    R, W, lh = P["wheel_r"], P["tyre_w"], P["lug_h"]
    pitches = P["lug_pitches"]
    x, y, z = c
    hw = W / 2
    rb = R - lh if lugs else R
    rim_r = 0.50
    p.lathe_x(c, [(rim_r, -hw + 0.06), (rb * 0.78, -hw - 0.015), (rb - 0.09, -hw + 0.01), (rb, -hw + 0.10),
                  (rb, hw - 0.10), (rb - 0.09, hw - 0.01), (rb * 0.78, hw + 0.015), (rim_r, hw - 0.06)], "rubber", seg)
    tang = 2 * math.pi * R / pitches * P["lug_w_factor"]
    ch = math.radians(P["lug_chevron_deg"])
    with p.detail():
        for i in range(pitches if lugs else 0):
            a = -math.pi / 2 + 2 * math.pi * i / pitches
            for s in (-1, 1):
                aa = a + (math.pi / pitches if s > 0 else 0.0)
                ca, sa = math.cos(aa), math.sin(aa)
                sw = lh + 0.075                    # rooted 0.075 into the carcass so the lug end
                rr = R - sw / 2                    # still meets the shoulder roll-off (no floating tab)
                cc = (x + s * hw * 0.42, y + sa * rr, z + ca * rr)
                t = (0, ca, -sa)                                   # tread tangent
                cs, sn = math.cos(ch), s * math.sin(ch)             # rows tilt opposite -> chevron
                u = (cs, sn * t[1], sn * t[2])
                v = (-sn, cs * t[1], cs * t[2])
                p.obox(cc, u, v, hw * 0.92, tang, sw, "rubber", open_w_neg=True)
    inner = x - side * (hw - 0.05)
    face = x + side * (hw - 0.12)
    p.cyl((inner, y, z), (face, y, z), rim_r + 0.005, "paint", 14)
    if rim_face:
        p.cyl((face, y, z), (face + side * 0.01, y, z), rim_r * 0.86, "paint_dark", 14)
        p.cyl((face - side * 0.02, y, z), (face + side * 0.09, y, z), 0.21, "paint", 10)
        with p.detail():
            for i in range(6):
                a = 2 * math.pi * i / 6
                by, bz = y + math.sin(a) * 0.32, z + math.cos(a) * 0.32
                f2 = face + side * 0.01
                p.box((f2, by - 0.025, bz - 0.025), (f2 + side * 0.04, by + 0.025, bz + 0.025), "worn", 0)


def build(K):
    Piece, node, attach = K.Piece, K.node, K.attach
    random.seed(7)
    K.PALETTE.setdefault("load", ("M_Load_Rock", (0.17, 0.145, 0.12), 0.95, 0.0, None))
    R, WB = P["wheel_r"], P["wheelbase"]
    ZF, ZR = WB / 2, -WB / 2
    FX = P["front_track"] / 2
    RX = P["rear_dual_centres"] / 2
    KP = P["kingpin_x"]
    DY = P["deck_y"]
    hx, hy, hz = P["hinge"]
    BO, BI, BT = P["body_half_out"], P["body_half_in"], P["body_top"]
    BF, BTZ = P["body_front_z"], P["body_tail_z"]
    keepout = []      # chassis boxes the steered front tyres must stay out of

    node("Root", None, (0, 0, 0))
    node("Chassis", "Root", (0, 0, 0))
    c = Piece(bevel=0.02)

    def kbox(mn, mx, key, bevel=None):
        c.box(mn, mx, key, bevel)
        keepout.append((mn, mx))

    # --- frame rails (rise over the rear axle to the hinge brackets), cross members
    for s in (-1, 1):
        c.prism([(4.55, 1.00), (4.55, 1.50), (-1.55, 1.50), (-2.05, 1.95), (-2.95, 1.95), (-2.95, 1.50),
                 (-2.10, 1.42), (-1.60, 1.00)], "x", s * 0.36, s * 0.50, "paint_dark", 0.0)
        keepout.append(((s * 0.36, 1.00, -1.6), (s * 0.50, 1.50, 4.55)))
        c.box((s * 0.36, 1.90, hz - 0.08), (s * 0.50, hy, hz + 0.08), "paint_dark", 0.0)       # hinge bracket
        c.cyl((s * 0.30, hy, hz), (s * 0.50, hy, hz), 0.08, "steel", 10)                       # hinge pin boss
        c.box((s * 0.50, 0.76, -0.15), (s * 0.82, 1.06, 0.15), "paint_dark", 0.01)             # hoist base lug
    c.box((-0.50, 1.55, -3.05), (0.50, 1.95, -2.95), "paint_dark", 0.0)                       # rear crossmember
    c.box((-0.50, 1.05, 1.15), (0.50, 1.40, 1.30), "paint_dark", 0.0)                          # mid crossmember
    # --- engine / radiator housing under the deck (narrow between the steered wheels)
    kbox((-0.50, 1.10, 1.40), (0.50, 1.75, 3.90), "paint_dark", 0.0)
    kbox((-0.66, 1.75, 1.40), (0.66, DY - 0.14, 4.00), "paint_dark", 0.0)
    kbox((-0.90, 1.30, 3.90), (0.90, DY - 0.14, 4.56), "paint_dark", 0.02)
    c.cyl((0, 1.25, 1.40), (0, 1.12, -1.85), 0.08, "steel", 8)                                # drive shaft
    # --- rear axle housing + differential (bottom = rear axle clearance [19])
    c.cyl((-0.73, R, ZR), (0.73, R, ZR), 0.30, "paint_dark", 12)
    c.box((-0.38, P["axle_clear"], ZR - 0.36), (0.38, 1.48, ZR + 0.36), "paint_dark", 0.10)
    # --- hood + grille + bumper
    c.prism([(3.90, DY - 0.14), (4.62, DY - 0.14), (4.62, 2.80), (4.50, 2.93), (3.90, 2.93)], "x", -0.90, 0.90,
            "paint", 0.04)
    c.box((-0.86, 1.30, 4.55), (0.86, 2.78, 4.645), "steel", 0.015)                            # grille core
    c.box((-1.70, 0.95, 4.62), (1.70, 1.30, 4.92), "paint", 0.03)                              # bumper
    c.box((-1.70, 0.95, 4.60), (1.70, 1.02, 4.93), "paint_dark", 0.0)                          # bumper lower edge
    for s in (-1, 1):   # frame horns: rail ends -> bumper. Structural (LOD1 too): without them the
        kbox((s * 0.50, 0.95, 4.20), (s * 0.82, 1.30, 4.64), "paint_dark", 0.0)   # LOD1 bumper floats
    # grille guard: 5 vertical bars on the bumper, up past the hood face, + top bar (LOD1 too)
    for xg in (-0.66, -0.33, 0.0, 0.33, 0.66):
        c.box((xg - 0.035, 1.30, 4.655), (xg + 0.035, 2.90, 4.725), "paint_dark", 0.0)
    c.box((-0.80, 2.84, 4.645), (0.80, 2.93, 4.735), "paint_dark", 0.0)
    # --- deck, front valance (fender), strut towers
    c.box((-2.35, DY - 0.14, 1.32), (2.35, DY, 4.50), "paint", 0.02)
    for s in (-1, 1):
        c.box((s * 0.92, 2.05, 4.40), (s * 2.35, DY - 0.14, 4.50), "paint", 0.015)
        c.box((s * 0.90, 2.16, ZF - 0.16), (s * 1.25, DY - 0.14, ZF + 0.16), "paint_dark", 0.0)
    # --- air-cleaner housing (smaller, chamfered) + exhaust stack (right side of the deck)
    c.box((0.62, DY, 3.10), (1.42, 2.90, 3.85), "paint", 0.07)
    c.box((-0.72, 2.93, 3.98), (0.72, 2.965, 4.42), "paint_dark", 0.0)                         # radiator top guard
    c.box((1.72, DY, 1.50), (2.25, DY + 0.42, 2.40), "paint_dark", 0.02)                       # battery/tool box
    c.box((-0.30, DY, 1.55), (0.42, DY + 0.025, 2.85), "paint_dark", 0.0)                      # service hatch

    c.cyl((0.80, DY, 2.55), (0.80, 3.85, 2.55), 0.09, "steel", 10)
    c.cyl((0.80, 3.78, 2.55), (0.80, 3.88, 2.55), 0.11, "worn", 10)
    # --- tanks between the axles: fuel (left), hydraulic (right)
    for s in (-1, 1):
        kbox((s * 1.00, 0.95 if s < 0 else 1.05, -0.95), (s * 1.75, 1.95, 0.55), "paint", 0.06)
        c.box((s * 0.50, 1.20, -0.60), (s * 1.00, 1.40, 0.20), "paint_dark", 0.0)              # tank bracket
    with c.detail():
        # grille slats (behind the guard), headlights, tow hooks
        for k in range(7):
            yb = 1.40 + k * 0.19
            c.strip((-0.80, yb, 4.645), (0.80, yb + 0.05, 4.655), "worn")
        for s in (-1, 1):
            # ONE large headlight per side, recessed in the deck valance under a visor
            # (were 4 small lamps on the grille: read as cartoon eyes)
            lx, ly, lw, lh_ = s * 1.36, 2.175, 0.36, 0.17
            c.box((lx - lw / 2 + 0.02, ly - lh_ / 2 + 0.02, 4.50), (lx + lw / 2 - 0.02, ly + lh_ / 2 - 0.02, 4.515), "lamp", 0)
            c.box((lx - lw / 2 - 0.03, ly + lh_ / 2 - 0.02, 4.50), (lx + lw / 2 + 0.03, ly + lh_ / 2 + 0.03, 4.60), "paint_dark", 0)  # visor
            c.box((lx - lw / 2 - 0.03, ly - lh_ / 2 - 0.03, 4.50), (lx + lw / 2 + 0.03, ly - lh_ / 2 + 0.02, 4.58), "paint_dark", 0)  # sill
            for e in (-1, 1):
                ex = lx + e * (lw / 2 + 0.005)
                c.box((ex - 0.025, ly - lh_ / 2 - 0.03, 4.50), (ex + 0.025, ly + lh_ / 2 + 0.03, 4.59), "paint_dark", 0)
            c.box((s * 1.05, 1.00, 4.92), (s * 1.25, 1.10, 5.00), "worn", 0)
            # deck edge band + tread plates
            c.strip((s * 2.35, DY - 0.13, 1.34), (s * 2.358, DY - 0.01, 4.48), "paint_dark")
            # tank straps + caps
            for zz in (-0.55, 0.15):
                c.strip((s * 0.99, 0.95, zz - 0.04), (s * 1.765, 1.965, zz + 0.04), "steel")
        c.strip((-2.30, DY, 3.90), (-0.45, DY + 0.008, 4.46), "steel")                         # tread plate L
        c.strip((1.60, DY, 1.40), (2.30, DY + 0.008, 4.46), "steel")                           # tread plate R
        c.cyl((-1.40, 1.95, -0.20), (-1.40, 2.01, -0.20), 0.08, "worn", 8)                     # fuel cap
        c.box((1.755, 1.40, -0.30), (1.765, 1.80, -0.24), "glass", 0)                          # sight glass
        c.strip((0.55, 2.70, 2.995), (1.55, 2.72, 3.005), "paint_dark")                       # housing seam
        for xx in (0.80, 1.30):                                                                # pre-cleaners
            c.cyl((xx, 3.00, 3.70), (xx, 3.22, 3.70), 0.08, "paint_dark", 8)
        # rear crossmember lights
        for s in (-1, 1):
            c.lamp((s * 0.32, 1.78, -3.05), "-z", 0.16, 0.12, 0.05, "tail")
            c.lamp((s * 0.13, 1.78, -3.05), "-z", 0.12, 0.12, 0.05, "lamp")
        # handrails: front edge (both sides of the hood), right side, left walkway beside the cab
        yt = DY + 1.0
        c.tube([(0.95, DY, 4.47), (0.95, yt, 4.47), (2.33, yt, 4.47), (2.33, yt, 1.40), (2.33, DY, 1.40)], 0.025, "steel", 5)
        c.tube([(0.95, DY + 0.5, 4.47), (2.33, DY + 0.5, 4.47), (2.33, DY + 0.5, 1.40)], 0.018, "steel", 5)
        c.tube([(2.33, DY, 3.0), (2.33, yt, 3.0)], 0.022, "steel", 5)
        c.tube([(-0.95, DY, 4.47), (-0.95, yt, 4.47), (-1.95, yt, 4.47)], 0.025, "steel", 5)
        c.tube([(-2.33, yt, 3.90), (-2.33, yt, 1.40), (-2.33, DY, 1.40)], 0.025, "steel", 5)
        c.tube([(-2.33, DY + 0.5, 3.90), (-2.33, DY + 0.5, 1.40)], 0.018, "steel", 5)
        # front diagonal stairs, both sides (bumper -> deck), 4 treads + stringer + rail. The right
        # side used to carry a ground ladder at x 1.78-2.12, outside the 1.70 bumper, hung by its top.
        for s in (-1, 1):
            for k in range(4):
                t = (k + 1) / 5
                xs, ys = s * (1.0 + 1.2 * t), 1.30 + (DY - 1.30) * t
                c.box((xs - 0.14, ys - 0.03, 4.55), (xs + 0.14, ys, 4.90), "steel", 0)
            c.obox((s * 1.6, (1.30 + DY) / 2 - 0.05, 4.92), (s * 1.2, DY - 1.30, 0), (0, 0, 1),
                   math.hypot(1.2, DY - 1.30) + 0.1, 0.03, 0.12, "paint")
            c.tube([(s * 1.0, 1.30, 4.92), (s * 1.0, 2.25, 4.92), (s * 2.2, DY + 0.95, 4.92), (s * 2.2, DY, 4.92)],
                   0.022, "steel", 5)
        # right mirror on an arm from the front rail
        mh = P["mirror_half"]
        c.tube([(2.33, yt - 0.1, 4.30), (mh - 0.12, yt - 0.05, 4.32)], 0.02)
        c.box((mh - 0.20, 3.05, 4.30), (mh, 3.50, 4.37), "steel", 0.01)
        c.box((mh - 0.19, 3.06, 4.293), (mh - 0.01, 3.49, 4.30), "worn", 0)
        c.tube([(mh - 0.10, 3.50, 4.33), (mh - 0.10, yt - 0.05, 4.32)], 0.016)
        # deck work lights on the front rail posts
        c.lamp((0.95, yt + 0.07, 4.47), "+z", 0.16, 0.11, 0.08, "lamp")
        c.lamp((-0.95, yt + 0.07, 4.47), "+z", 0.16, 0.11, 0.08, "lamp")
    attach("Chassis", c)
    node("ExhaustTip", "Chassis", (0.80, 3.88, 2.55))
    node("Beacon", "Chassis", (2.33, DY + 1.03, 4.47))
    b = Piece()
    by_ = DY + 1.03
    b.cyl((2.33, by_, 4.47), (2.33, by_ + 0.035, 4.47), 0.085, "steel", 10)
    b.cyl((2.33, by_ + 0.035, 4.47), (2.33, by_ + 0.122, 4.47), 0.068, "beacon", 10, r2=0.06)
    b.cyl((2.33, by_ + 0.122, 4.47), (2.33, by_ + 0.142, 4.47), 0.06, "beacon", 10, r2=0.035)
    attach("Beacon", b)

    # ---------------------------------------------------------------- Cab (operator left)
    (cx0, cx1), (cz0, cz1), ry = P["cab_x"], P["cab_z"], P["cab_top"]
    fy = DY
    cxm = (cx0 + cx1) / 2
    node("Cab", "Chassis", (cxm, fy, (cz0 + cz1) / 2))
    k = Piece()
    k.box((cx0, fy, cz0), (cx1, fy + 0.55, cz1), "paint", 0.03)
    k.prism([(cz0 + 0.03, fy + 0.55), (cz1 - 0.03, fy + 0.55), (cz1 - 0.12, ry - 0.12), (cz0 + 0.03, ry - 0.12)],
            "x", cx0 + 0.03, cx1 - 0.03, "glass", 0.0)
    for sx in (cx0, cx1):
        for zz, top_z in ((cz0, cz0), (cz1, cz1 - 0.09)):
            k.obox((sx, (fy + 0.5 + ry - 0.08) / 2, (zz + top_z) / 2), (0, ry - 0.08 - fy - 0.5, top_z - zz), (1, 0, 0),
                   math.hypot(ry - 0.08 - fy - 0.5, top_z - zz), 0.11, 0.11, "steel")
    k.box((cx0 - 0.01, fy + 0.52, cz0 - 0.01), (cx1 + 0.01, fy + 0.60, cz1 + 0.01), "steel", 0.01)    # sill band
    k.box((cx0 + 0.03, ry - 0.22, cz0), (cx1 - 0.03, ry - 0.12, cz1 - 0.12), "steel", 0.0)            # header
    k.box((cx0 - 0.06, ry - 0.10, cz0 - 0.08), (cx1 + 0.06, ry, cz1 + 0.06), "paint", 0.03)           # roof
    k.box((cx0 - 0.005, fy + 0.60, 2.55), (cx0 + 0.06, ry - 0.12, 2.62), "steel", 0.0)               # door mullion
    with k.detail():
        k.box((cx0 - 0.03, ry - 0.15, cz0 - 0.06), (cx1 + 0.03, ry - 0.10, cz1 + 0.04), "steel", 0.0)  # gutter
        mh = P["mirror_half"]
        k.tube([(cx0, ry - 0.45, cz1 - 0.05), (-mh + 0.12, ry - 0.55, cz1 + 0.02)], 0.018, "steel", 5)
        k.box((-mh, 3.05, cz1 - 0.02), (-mh + 0.20, 3.50, cz1 + 0.05), "steel", 0.01)
        k.box((-mh + 0.01, 3.06, cz1 - 0.027), (-mh + 0.19, 3.49, cz1 - 0.02), "worn", 0)
        k.tube([(-mh + 0.10, 3.50, cz1 + 0.01), (-mh + 0.10, ry - 0.55, cz1 + 0.02)], 0.016)
        k.box((cx0 - 0.02, fy + 0.75, 2.30), (cx0 - 0.003, fy + 0.78, 2.45), "worn", 0)               # door handle
        k.tube([(cx0 - 0.06, fy + 0.15, 2.0), (cx0 - 0.06, fy + 1.25, 2.0)], 0.018)                  # grab handle
        k.tube([(cxm + 0.25, fy + 0.62, cz1 - 0.03), (cxm - 0.25, fy + 1.15, cz1 - 0.10)], 0.011)     # wiper
        for sx in (-1, 1):                                                                             # roof lamps
            k.lamp((cxm + sx * 0.45, ry - 0.05, cz1 + 0.06), "+z", 0.15, 0.09, 0.07, "lamp")
            k.lamp((cxm + sx * 0.45, ry - 0.05, cz0 - 0.08), "-z", 0.15, 0.09, 0.07, "lamp")
    attach("Cab", k)

    # ---------------------------------------------------------------- front wheels + steering
    for side, s in (("Left", -1), ("Right", 1)):
        kx = s * KP
        node("SteerFront" + side, "Chassis", (kx, R, ZF))
        st = Piece()
        st.cyl((kx, R - 0.25, ZF), (kx, 2.18, ZF), 0.13, "paint_dark", 10)        # suspension strut = kingpin
        st.cyl((kx, 1.55, ZF), (kx, 1.95, ZF), 0.095, "worn", 10)                 # chrome band
        st.cyl((kx, R, ZF), (s * (FX - 0.12), R, ZF), 0.17, "steel", 10)         # spindle into the hub
        attach("SteerFront" + side, st)
        node("WheelFront" + side, "SteerFront" + side, (s * FX, R, ZF))
        w = Piece()
        tyre(K, w, (s * FX, R, ZF), s)
        attach("WheelFront" + side, w)
    # ---------------------------------------------------------------- rear duals
    for side, s in (("Left", -1), ("Right", 1)):
        node("WheelRear" + side, "Chassis", (s * RX, R, ZR))
        w = Piece()
        tyre(K, w, (s * (RX - P["dual_spacing"] / 2), R, ZR), s, rim_face=False, lugs=False)
        tyre(K, w, (s * (RX + P["dual_spacing"] / 2), R, ZR), s)
        w.cyl((s * (RX - 0.40), R, ZR), (s * (RX + 0.40), R, ZR), 0.30, "steel", 10)       # hub/final drive
        attach("WheelRear" + side, w)

    # ---------------------------------------------------------------- dump body
    node("DumpBody", "Chassis", (hx, hy, hz))
    d = Piece(bevel=0.02)
    ft = FLOOR_TOP
    fb = [(z, y - 0.12) for z, y in ft]
    fb[0] = (BF, U_floor(BF))
    d.prism(ft + list(reversed(fb)), "x", -BI, BI, "paint", 0.0)                               # floor
    lip = (BTZ, ft[-1][1] + 0.06)
    for s in (-1, 1):
        prof = [(BF, U_floor(BF))] + fb[1:] + [lip, (-3.95, BT - 0.07), (BF, BT - 0.07)]
        d.prism(prof, "x", s * (BI - 0.005), s * BO, "paint", 0.015)                          # side wall
        d.box((s * (BI - 0.03), BT - 0.11, -3.95), (s * (BO + 0.05), BT, BF), "paint", 0.02)    # top rail
        tl = math.hypot(3.95 + BTZ, BT - 0.06 - lip[1])
        d.obox((s * (BO + 0.01), (BT - 0.06 + lip[1]) / 2, (-3.95 + BTZ) / 2 + 0.03),
               (0, lip[1] - (BT - 0.06), BTZ + 3.95), (1, 0, 0), tl - 0.06, 0.13, 0.11, "paint")  # tail rail
        for zr in (0.40, -0.80, -2.00, -3.20):                                                  # side ribs
            d.box((s * BO, U_floor(zr) + 0.03, zr - 0.07), (s * (BO + 0.07), BT - 0.11, zr + 0.07), "paint", 0.015)
        # bottom edge band (two-tone) along each underside segment
        pts = [(BF, U_floor(BF))] + fb[1:] + [lip]
        for (z0, y0), (z1, y1) in zip(pts, pts[1:]):
            if abs(z1 - z0) < 1e-6:          # the vertical tail edge gets the wear lip instead
                continue
            L = math.hypot(z1 - z0, y1 - y0)
            d.obox((s * (BO - 0.04), (y0 + y1) / 2 + 0.05, (z0 + z1) / 2), (0, y1 - y0, z1 - z0), (1, 0, 0),
                   L - (0.08 if z1 == BTZ else -0.02), 0.10, 0.11, "paint_dark")
        # longitudinal beams + hinge lug + hoist lug
        bx0, bx1 = s * 0.50, s * 0.64
        bpts = [(1.0, U_floor(1.0)), (-0.90, U_floor(-0.90)), (-2.85, U_floor(-2.85))]
        d.prism(bpts + [(z, y - 0.17) for z, y in reversed(bpts)], "x", bx0, bx1, "paint_dark", 0.01)
        d.box((bx0, hy - 0.02, hz - 0.12), (bx1, U_floor(hz), hz + 0.12), "paint_dark", 0.0)
        d.cyl((bx0, hy, hz), (bx1, hy, hz), 0.095, "paint_dark", 10)
        ax, ay, az = P["hoist_B"]
        d.box((s * 0.60, ay - 0.06, az - 0.10), (s * 0.80, U_floor(az) - 0.12, az + 0.10), "paint_dark", 0.01)
    for zc in (0.60, -0.30):                                                                    # cross members
        d.box((-BI, U_floor(zc) - 0.10, zc - 0.06), (BI, U_floor(zc), zc + 0.06), "paint_dark", 0.0)
    # headboard up to the canopy
    d.box((-BO, U_floor(BF), BF - 0.12), (BO, 4.24, BF), "paint", 0.02)
    d.box((-BO - 0.05, BT - 0.11, BF - 0.14), (BO + 0.05, BT, BF + 0.02), "paint", 0.02)          # front top rail
    d.box((-BI + 0.05, ft[-1][1] - 0.02, BTZ), (BI - 0.05, ft[-1][1] + 0.09, BTZ + 0.14), "worn", 0.0)  # tail wear lip
    with d.detail():
        for yy in (2.75, 3.30):                                                                 # headboard ribs (inside)
            d.strip((-BI, yy, BF - 0.16), (BI, yy + 0.07, BF - 0.12), "paint")
        for s in (-1, 1):                                                                       # rib caps seam
            d.strip((s * (BO + 0.07), BT - 0.13, -3.95), (s * (BO + 0.076), BT - 0.115, BF), "paint_dark")
    attach("DumpBody", d)

    # ---------------------------------------------------------------- canopy (rises with the body)
    czf, cyf, CH = P["canopy_front_z"], P["canopy_front_y"], P["canopy_half"]
    node("Canopy", "DumpBody", (0, 4.24, BF))
    cp = Piece(bevel=0.02)
    cp.prism([(BF - 0.12, 4.20), (czf, 4.24), (czf, cyf), (czf - 0.10, cyf), (BF - 0.12, 4.33)], "x",
             -(CH - 0.20), CH - 0.20, "paint", 0.02)
    cp.box((-(CH - 0.20), 4.21, czf - 0.05), (CH - 0.20, 4.31, czf + 0.02), "paint_dark", 0.01)  # front lip (lower band)
    for s in (-1, 1):
        cp.prism([(s * (CH - 0.22), 4.36), (s * CH, 4.26), (s * CH, 4.21), (s * (CH - 0.22), 4.29)], "z",
                 BF, czf - 0.06, "paint", 0.0)                                                   # side flare
        cp.obox((s * (BO - 0.20), 4.00, BF + 0.20), (0, 0.6, 0.5), (1, 0, 0), 0.55, 0.10, 0.10, "paint")  # brace
    with cp.detail():
        for zz in (2.0, 3.0, 3.8):
            cp.strip((-(CH - 0.25), 4.16, zz - 0.05), (CH - 0.25, 4.215, zz + 0.05), "paint")
    attach("Canopy", cp)

    # ---------------------------------------------------------------- heaped load
    node("Load", "DumpBody", (0, BT, -1.3))
    ld = Piece()
    nx, nz = 8, 11
    z0, z1 = BF - 0.16, -4.35
    def floor_top(zz):
        for (za, ya), (zb, yb) in zip(FLOOR_TOP, FLOOR_TOP[1:]):
            if zb <= zz <= za:
                return ya + (yb - ya) * (zz - za) / (zb - za)
        return FLOOR_TOP[0][1]
    def hgt(u, v):           # u in [-1,1] across, v in [0,1] front->rear; rear edge slumps to the floor
        zc = 0.36
        fz = max(0.0, 1 - ((v - zc) / (0.64 if v > zc else 0.5)) ** 2)
        fx = max(0.0, 1 - u * u)
        t = min(1.0, max(0.0, (v - 0.62) / 0.38))
        base = 3.60 + (floor_top(z0 + (z1 - z0) * v) + 0.06 - 3.60) * t * t * (3 - 2 * t)
        return base + 0.85 * (fx ** 0.75) * (fz ** 0.7)
    V = []
    for i in range(nz + 1):
        row = []
        for j in range(nx + 1):
            u = -1 + 2 * j / nx; v = i / nz
            x = u * (BI - 0.04); zz = z0 + (z1 - z0) * v
            jit = 0.0 if (j in (0, nx) or i in (0, nz)) else random.uniform(-0.06, 0.06)
            row.append(ld.bm.verts.new((x + (0 if j in (0, nx) else random.uniform(-0.05, 0.05)),
                                        hgt(u, v) + jit, zz)))
        V.append(row)
    li = ld.mi("load")
    for i in range(nz):
        for j in range(nx):
            f = ld.bm.faces.new((V[i][j], V[i][j + 1], V[i + 1][j + 1], V[i + 1][j]))
            f.material_index = li
    # skirt down into the body (hidden while loaded)
    ring = [V[0][j] for j in range(nx + 1)] + [V[i][nx] for i in range(1, nz + 1)] + \
           [V[nz][j] for j in range(nx - 1, -1, -1)] + [V[i][0] for i in range(nz - 1, 0, -1)]
    low = [ld.bm.verts.new((v.co.x, 2.9, v.co.z)) for v in ring]
    for a in range(len(ring)):
        b2 = (a + 1) % len(ring)
        f = ld.bm.faces.new((ring[a], ring[b2], low[b2], low[a]))
        f.material_index = li
    with ld.detail():
        for _ in range(9):                         # a few boulders poking out of the heap
            u, v = random.uniform(-0.75, 0.75), random.uniform(0.1, 0.85)
            x = u * (BI - 0.04); zz = z0 + (z1 - z0) * v
            sz = random.uniform(0.20, 0.34)
            a = random.uniform(0, math.pi); t = random.uniform(-0.5, 0.5)
            ux, uz = math.cos(a), math.sin(a)
            ld.obox((x, hgt(u, v) - sz * 0.10, zz), (ux, t, uz), (-uz * t, 1, ux * 0.3 + random.uniform(-0.4, 0.4)),
                    sz * 1.25, sz * 0.8, sz * 0.9, "load")
    attach("Load", ld, smooth_angle=8)

    # ---------------------------------------------------------------- hoists
    hp = [lambda: pose(K), lambda: pose(K, dump=DUMP_DEG / 2), lambda: pose(K, dump=DUMP_DEG)]
    hyd = {}
    for side, s in (("Left", -1), ("Right", 1)):
        ax, ay, az = P["hoist_A"]; bx, by2, bz2 = P["hoist_B"]
        hyd["Hoist" + side] = K.hydraulic("Hoist", side, "Chassis", (s * ax, ay, az), "DumpBody",
                                          (s * bx, by2, bz2), 0.12, 0.075, hp, lambda: pose(K), seg=10)
    pose(K)
    _I.update(keepout=keepout)
    rad_per_m = 1 / R
    return dict(
        unity_localEuler_deg=dict(DumpBody_x=[0.0, -DUMP_DEG], SteerFront_y=[-STEER_DEG, STEER_DEG],
                                  Wheel_x="x += deg(d / 1.06)"),
        wheel_r_m=R, wheel_rad_per_m=round(rad_per_m, 6), wheel_deg_per_m=round(math.degrees(rad_per_m), 4),
        hinge_world_unity=list(P["hinge"]), kingpin_x=KP,
        load_rule=dict(node="Load", driver="DumpBody", driver_property="localEulerAngles.x",
                       angle="signed deg, Mathf.DeltaAngle(0, x); rest 0, full raise -%g" % DUMP_DEG,
                       hide_below_deg=LOAD_HIDE_BELOW_DEG, show_at_or_above_deg=LOAD_SHOW_AT_DEG,
                       between="keep previous visibility (hysteresis)", action="Load.SetActive(visible)"),
        hydraulics=hyd,
        keep_full_lod1=[n for n in K.NODES if "Cylinder" in n or "Rod" in n] +
                       ["Beacon", "WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight",
                        "SteerFrontLeft", "SteerFrontRight",
                        # hard-surface boxes: collapse-decimation melts them into blobs (bumper,
                        # grille, deck); their detail-stripped meshes are already cheap enough
                        "Chassis", "Cab", "Canopy", "DumpBody"],
    )


# ------------------------------------------------------------------------------------------
def _mesh_world_verts(K, name, lod="LOD0"):
    o = [c for c in K.NODES[name].children if c.name.endswith("_" + lod)]
    if not o:
        return []
    o = o[0]
    mw = o.matrix_world
    return [K.to_unity(mw @ v.co) for v in o.data.vertices]


def _all_lod0_verts(K):
    out = []
    for o in K.lod_objects("LOD0"):
        mw = o.matrix_world
        out += [K.to_unity(mw @ v.co) for v in o.data.vertices]
    return out


def _seg_point_dist(p, a, b):
    ax, ay = a; bx, by = b; px, py = p
    dx, dy = bx - ax, by - ay
    L2 = dx * dx + dy * dy
    t = 0 if L2 == 0 else max(0, min(1, ((px - ax) * dx + (py - ay) * dy) / L2))
    return math.hypot(px - ax - t * dx, py - ay - t * dy)


def checks(K, info):
    out = {}
    R = P["wheel_r"]
    hx, hy, hz = P["hinge"]
    # 1. dump hinge fixed + body raise angle measured on the node
    pin_local = K.capture("DumpBody", (0.6, hy, hz))
    res = {}
    for nm, dg in (("rest", 0.0), ("half", DUMP_DEG / 2), ("raised", DUMP_DEG)):
        pose(K, dump=dg, load=False)
        res[nm] = dict(pin=K.world_unity("DumpBody", pin_local), pitch=K.node_pitch_deg("DumpBody"),
                       hyd={h: K.hydraulic_check(v) for h, v in info["hydraulics"].items()})
    drift = max((res[n]["pin"] - res["rest"]["pin"]).length for n in res)
    out["dump_hinge_fixed"] = dict(pin_max_drift_m=round(drift, 6),
                                   body_pitch_deg={n: round(res[n]["pitch"], 3) for n in res},
                                   ok=drift < 1e-4 and abs(res["raised"]["pitch"] - DUMP_DEG) < 0.01)
    # 2. hoists: aim, overlap, stroke ratio
    hy_ok = True; rep = {}
    for h, v in info["hydraulics"].items():
        rep[h] = dict(stroke_ratio=v["stroke_ratio"], overlap_at_max=v["overlap_at_max"], dmin=v["dmin"],
                      dmax=v["dmax"], barrel_len=v["barrel_len"],
                      aim_err_deg={n: res[n]["hyd"][h]["aim_err_deg"] for n in res})
        hy_ok &= v["stroke_ratio"] < 2.0 and v["overlap_at_max"] > 0.05 and \
            all(res[n]["hyd"][h]["aim_err_deg"] < 0.5 for n in res)
    out["hoist_feasible"] = dict(per_cylinder=rep, ok=bool(hy_ok))
    # 3. raised body clears the ground and the rear tyres at every angle (mesh EDGES, z-y plane)
    movers = [c for n in ("DumpBody", "HoistCylinderLeft", "HoistRodLeft", "HoistCylinderRight", "HoistRodRight")
              for c in K.NODES[n].children if c.name.endswith("_LOD0")]
    lo_x, hi_x = P["rear_dual_centres"] / 2 - P["dual_spacing"] / 2 - P["tyre_w"] / 2, 2.25
    axle = (-P["wheelbase"] / 2, R)
    clr = {}
    gmin = {}
    for dg in range(0, int(DUMP_DEG) + 1, 5):
        pose(K, dump=dg, load=False)
        m = 99.0; gm = 99.0
        for o in movers:
            mw = o.matrix_world
            vs = [K.to_unity(mw @ v.co) for v in o.data.vertices]
            gm = min(gm, min(v[1] for v in vs))
            for e in o.data.edges:
                a, b = vs[e.vertices[0]], vs[e.vertices[1]]
                if not (lo_x <= abs(a[0]) <= hi_x or lo_x <= abs(b[0]) <= hi_x):
                    continue
                m = min(m, _seg_point_dist(axle, (a[2], a[1]), (b[2], b[1])) - R)
        clr[dg] = round(m, 4)
        gmin[dg] = round(gm, 4)
    pose(K)
    out["body_clears_tyres_and_ground"] = dict(min_gap_to_rear_tyre_m=clr, body_min_y_m=gmin,
                                               ok=min(clr.values()) > 0.03 and min(gmin.values()) > 0.3)
    # 4. wheels touch the ground (rest, rolled, steered)
    gt = {}
    for nm, kw in (("rest", {}), ("rolled_0.37m", dict(roll_m=0.37)), ("steer_+35", dict(steer=STEER_DEG)),
                   ("steer_-35", dict(steer=-STEER_DEG))):
        pose(K, **kw)
        gt[nm] = {w: round(min(v[1] for v in _mesh_world_verts(K, w)), 4)
                  for w in ("WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight")}
    pose(K)
    out["wheels_touch_ground"] = dict(min_y=gt, ok=all(abs(y) < 0.015 for d_ in gt.values() for y in d_.values()))
    # 5. steering: kingpin fixed, wheel centre on its arc, tyres stay out of the chassis keep-out boxes
    st = {}
    hits = 0
    for side in ("Left", "Right"):
        k0 = K.world_unity("SteerFront" + side)
        c0 = K.world_unity("WheelFront" + side)
        for dg in (-STEER_DEG, STEER_DEG):
            pose(K, steer=dg)
            k1 = K.world_unity("SteerFront" + side); c1 = K.world_unity("WheelFront" + side)
            for v in _mesh_world_verts(K, "WheelFront" + side):
                for (mn, mx) in _I["keepout"]:
                    lo = [min(mn[i], mx[i]) for i in range(3)]; hi = [max(mn[i], mx[i]) for i in range(3)]
                    if all(lo[i] + 0.005 < v[i] < hi[i] - 0.005 for i in range(3)):
                        hits += 1
            st[f"{side}_{dg:+.0f}"] = dict(kingpin_drift_m=round((k1 - k0).length, 6),
                                          wheel_arc_r_m=round(math.hypot(c1[0] - k1[0], c1[2] - k1[2]), 4),
                                          wheel_centre_z=round(c1[2], 4))
        pose(K)
    out["steering"] = dict(per_pose=st, tyre_verts_inside_chassis=hits,
                           ok=hits == 0 and all(v["kingpin_drift_m"] < 1e-5 for v in st.values()))
    # 6. steer + dump SIGNS as Unity will read them from the glb (glTFast: x-mirror ->
    #    q_unity = (qx, -qy, -qz, qw)); proves 'Unity y = -Blender z' and 'Unity x = Blender x'.
    import bpy, struct
    pose(K, steer=20.0, dump=30.0)
    tmp = os.path.join(K.S["out"], "_signcheck.glb")
    for o in bpy.data.objects:
        o.select_set(o.type == "EMPTY")
    bpy.context.view_layer.objects.active = K.NODES["Root"]
    bpy.ops.export_scene.gltf(filepath=tmp, export_format="GLB", use_selection=True, export_yup=True,
                              export_animations=False, export_cameras=False, export_lights=False)
    with open(tmp, "rb") as f:
        data = f.read()
    j = json.loads(data[20:20 + struct.unpack_from("<I", data, 12)[0]])
    os.remove(tmp)
    byname = {n["name"]: n for n in j["nodes"]}
    def unity_euler(nm):
        qx, qy, qz, qw = byname[nm].get("rotation", [0, 0, 0, 1])
        ux, uy, uz = qx, -qy, -qz
        return round(math.degrees(2 * math.atan2(ux, qw)), 3), round(math.degrees(2 * math.atan2(uy, qw)), 3)
    sl = unity_euler("SteerFrontLeft")[1]; dbx = unity_euler("DumpBody")[0]
    # world direction of the steered wheel's forward axis (Unity): + x component == steering right
    fwd = K.NODES["WheelFrontLeft"].matrix_world.to_3x3() @ K.Vector((0, -1, 0))
    pose(K)
    out["unity_signs_from_glb"] = dict(steer_cmd_deg=20.0, SteerFrontLeft_unity_y=sl, wheel_forward_unity_x=round(-fwd.x, 4),
                                       dump_cmd_deg=30.0, DumpBody_unity_x=dbx,
                                       ok=abs(sl - 20.0) < 0.01 and -fwd.x > 0 and abs(dbx + 30.0) < 0.01)
    # 7. bbox + key heights vs spec
    pose(K)
    vs = _all_lod0_verts(K)
    xs = [v[0] for v in vs]; ys = [v[1] for v in vs]; zs = [v[2] for v in vs]
    cab = _mesh_world_verts(K, "Cab"); can = _mesh_world_verts(K, "Canopy"); body = _mesh_world_verts(K, "DumpBody")
    rw = _mesh_world_verts(K, "WheelRearLeft") + _mesh_world_verts(K, "WheelRearRight")
    fw = _mesh_world_verts(K, "WheelFrontLeft")
    meas = dict(
        overall_length=(round(max(zs) - min(zs), 3), 10.070, 0.10),
        operating_width_mirrors=(round(max(xs) - min(xs), 3), 5.673, 0.05),
        front_canopy_height=(round(max(v[1] for v in can), 3), 4.459, 0.03),
        cab_rops_height=(round(max(v[1] for v in cab), 3), 4.108, 0.03),
        canopy_width=(round(max(v[0] for v in can) - min(v[0] for v in can), 3), 4.886, 0.03),
        body_outside_width=(round(2 * P["body_half_out"], 3), 3.922, 0.01),
        overall_rear_tyre_width=(round(max(v[0] for v in rw) - min(v[0] for v in rw), 3), 4.411, 0.08),
        front_tyre_dia=(round(max(v[1] for v in fw) - min(v[1] for v in fw), 3), 2.12, 0.02),
        rear_axle_to_tail=(round(-P["wheelbase"] / 2 - min(zs), 3), 2.925, 0.03),
        loading_height=(round(max(v[1] for v in body if v[2] < -2.0), 3), 3.771, 0.03),
        min_ground_clearance_frame=(round(min(v[1] for v in _mesh_world_verts(K, "Chassis") if abs(v[0]) < 0.9 and abs(v[2]) < 1.0), 3), 0.759, 0.02),
    )
    out["bbox_vs_spec"] = dict(measured_spec_tol={k: v for k, v in meas.items()},
                               max_height=round(max(ys), 3),
                               ok=all(abs(m - s) <= t for m, s, t in meas.values()))
    # 8. canopy over cab clearance; raised overall height (reference: 9.284 m at 60 deg)
    (cx0, cx1), (cz0, cz1) = P["cab_x"], P["cab_z"]
    over = [v[1] for v in can if cx0 - 0.1 <= v[0] <= cx1 + 0.1 and cz0 - 0.1 <= v[2] <= cz1 + 0.1]
    gap = min(over) - max(v[1] for v in cab)
    pose(K, dump=DUMP_DEG, load=False)
    raised_h = max(v[1] for v in _all_lod0_verts(K))
    pose(K)
    out["canopy_clears_cab"] = dict(gap_m=round(gap, 4), raised_overall_height_m=round(raised_h, 3),
                                    spec_raised_height_60deg=9.284, ok=gap > 0.02)
    # 9. load sits inside the body
    ldv = _mesh_world_verts(K, "Load")
    out["load_inside_body"] = dict(max_abs_x=round(max(abs(v[0]) for v in ldv), 3), inner_half=round(P["body_half_in"], 3),
                                   z_range=[round(min(v[2] for v in ldv), 3), round(max(v[2] for v in ldv), 3)],
                                   heap_top_y=round(max(v[1] for v in ldv), 3),
                                   ok=max(abs(v[0]) for v in ldv) < P["body_half_in"] and max(v[2] for v in ldv) < P["body_front_z"] - 0.1)
    # 9b. LOAD RULE: drive a full dump cycle through pose() (same code path as the renders) and
    #     read the Load node's effective visibility back off the scene
    seq = [0.0, 1.0, 1.99, 2.5, 25.0, DUMP_DEG, 25.0, 2.5, 1.0, 0.04, 0.0, 1.0]   # raise deg (x = -deg)
    want = [True, True, True, False, False, False, False, False, False, True, True, True]
    got = []
    _LOAD["visible"] = True
    for dg in seq:
        pose(K, dump=dg)
        got.append(K.NODES["Load"].matrix_world.to_scale().x > 0.5)
    pose(K)
    pose(K, dump=DUMP_DEG, load=False); empty_hidden = K.NODES["Load"].matrix_world.to_scale().x < 0.5
    pose(K)
    rest_shown = K.NODES["Load"].matrix_world.to_scale().x > 0.5
    out["load_rig_rule"] = dict(dump_x_deg_sequence=[-d for d in seq], visible=got, expected=want,
                                hide_below_deg=LOAD_HIDE_BELOW_DEG, show_at_or_above_deg=LOAD_SHOW_AT_DEG,
                                payload_flag_false_hides=empty_hidden, rest_shown=rest_shown,
                                ok=got == want and empty_hidden and rest_shown)
    # 10. rolling constant from the mesh: lug-face radius about the spin axis
    o = [c for c in K.NODES["WheelFrontLeft"].children if c.name.endswith("_LOD0")][0]
    rr = max(math.hypot(v.co.y, v.co.z) for v in o.data.vertices)
    out["wheel_speed_constant"] = dict(mesh_rolling_r=round(rr, 4), wheel_rad_per_m=info["wheel_rad_per_m"],
                                       surface_speed_per_m=round(rr * info["wheel_rad_per_m"], 5),
                                       ok=abs(rr * info["wheel_rad_per_m"] - 1) < 0.01)
    return out


def shots(K, info):
    D, S = DUMP_DEG, STEER_DEG
    return [
        ("hauler_01_3q_front", {}, ((10.5, 6.0, 15.0), (0, 2.0, 0.3), 40), 0),
        ("hauler_02_side", {}, ((-30, 2.6, 0.0), (0, 2.4, 0.0), 60), 0),
        ("hauler_03_3q_rear", dict(roll_m=0.4), ((-11.5, 6.0, -13.5), (0, 2.0, -0.6), 40), 0),
        ("hauler_04_dumping", dict(dump=D), ((12.5, 6.5, -12.5), (0, 3.2, -1.2), 40), 0),
        ("hauler_05_side_dumping", dict(dump=D), ((-32, 4.0, -0.5), (0, 4.0, -0.5), 55), 0),
        ("hauler_06_steered", dict(steer=S), ((5.5, 2.2, 14.5), (0, 1.6, 1.8), 32), 0),
        ("hauler_07_front", {}, ((0, 2.4, 30), (0, 2.3, 0), 60), 0),
        ("hauler_08_demo_distance", {}, ((70, 45, 80), (0, 0, 0), 35), 0),
        ("hauler_09_demo_mid", {}, ((35, 22, 40), (0, 0, 0), 35), 0),
        ("hauler_10_lod1_3q", {}, ((10.5, 6.0, 15.0), (0, 2.0, 0.3), 40), 1),
        ("hauler_11_empty_3q", dict(load=False), ((-9.0, 8.5, 12.0), (0, 2.4, -0.5), 40), 0),
        ("hauler_12_front_low", {}, ((4.2, 0.9, 10.5), (0, 1.3, 4.4), 40), 0),
        ("hauler_13_lod1_front_low", {}, ((4.2, 0.9, 10.5), (0, 1.3, 4.4), 40), 1),
        ("hauler_14_tyre_close", {}, ((5.5, 1.4, 6.5), (1.6, 1.0, 2.1), 45), 0),
    ]
