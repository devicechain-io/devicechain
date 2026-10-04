# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Stylized, unbranded mid-size ARTICULATED WHEEL LOADER (19 t / 3.1 m3 class) for Sitepulse.

Coordinates: Unity metres (X right, Y up, Z forward). Origin on the ground at the ARTICULATION
axis, which sits exactly midway between the axles ([GC:9] rear axle -> hitch 1.650 = half of
the 3.300 wheelbase [GC:7]). [GC:n] = numbered dimension n on the reference side drawing
(a published manufacturer spec sheet for this class; not committed), [GC:tbl] = the bucket table on the same spec
sheet, [WA:x] = the second spec sheet (front view), [meas] = measured off the side drawing at
153.9 px/m, [style] = a choice. Proportions only: nothing traced, no logos or liveries.

RIG -- Unity localEulerAngles (degrees); every pivot is axis-aligned at rest (identity):
  node           axis  range                 sign / rule
  FrontFrame     Y     -40 .. +40            ARTICULATION (vertical hitch pin). + = front frame
                                             yaws RIGHT (machine turns right driving forward).
  Boom           X     0 (bucket on ground)  - = raise. -6.92 = carry (B-pin 0.655 m [GC:8]),
                       .. -84.69             -84.69 = max lift (B-pin 4.188 m [GC:5]).
  Bucket         X     -40 .. +136.7         relative to Boom. - = rack back (curl), + = dump.
                                             WORLD bucket pitch = Boom.x + Bucket.x, so
                                             Bucket.x = worldPitch - Boom.x. Rest (0) = floor
                                             flat on the ground. Spec poses:
                                               ground rack-back 40      Bucket.x = -40
                                               carry rack-back 45       Bucket.x = -38.08
                                               max lift rack-back 60    Bucket.x = +24.69
                                               max lift dump 52         Bucket.x = +136.69
  Bellcrank      X     follows Bucket        Z-BAR RULE: a function of Bucket.x ONLY (both hang
                                             off the Boom). Exact closed form = circle-circle
                                             intersection, see bellcrank_x(); the quintic in
                                             info["zbar"]["fit"] reproduces it to the stated
                                             max error. BucketLink is aimed, so a small fit
                                             error only changes the link's visual length.
  WheelFrontLeft/Right, WheelRearLeft/Right
                 X     continuous            spin: x += degrees(d / WHEEL_R) for distance d
                                             rolled by THAT wheel (+ = forward).
                                             WHEEL_R = 0.765 m -> 74.90 deg per metre.
  aimed (AimConstraint aim +Y, up +Y, or LookAt in LateUpdate; glTF carries no constraints):
    LiftCylinderLeft/Right -> LiftRodMountLeft/Right,  LiftRodLeft/Right -> LiftCylinderMount*
    TiltCylinder -> TiltRodMount,  TiltRod -> TiltCylinderMount
    SteerCylinderLeft/Right -> SteerRodMount*, SteerRod* -> SteerCylinderMount*
    BucketLink -> BucketLinkPin
  ExhaustTip     particle emitter point (exhaust smoke).

Linkage: Z-bar (single tilt cylinder pushes the bellcrank top forward -> the bellcrank bottom
pulls the link back -> the bucket racks back; extending the tilt cylinder = rack back). Pins
were placed by a small numeric search (design/opt3.py) so that ONE branch of the four-bar
covers the whole Bucket.x range with no toggle, transmission angles >= 30 deg, and the tilt
cylinder stroke ratio stays well under 2.
"""
import math

NAME = "loader"
LOD1_RATIO = 0.45
WHEEL_R = 0.765

P = dict(
    wheelbase=3.300,            # [GC:7] (ref B: 3.300 too)
    hitch_from_rear_axle=1.650, # [GC:9]
    axle_h=0.750,               # [GC:14]; tyre modelled at the 0.760 rolling radius [GC p1] -> 0.765
    wheel_r=WHEEL_R,
    tread=2.150,                # [WA:E] 2.160, ref A tread 2.140
    tyre_w=0.620,               # 23.5R25 section 0.597 + bulge -> width over tyres 2.77 [WA:D 2.765, GC 2.84]
    ground_clearance=0.460,     # [GC:4]
    rear_axle_to_cw=2.055,      # [GC:6]
    hood_top=2.673,             # [GC:3]
    rops_h=3.458,               # [GC:1]
    exhaust_top=3.416,          # [GC:2]
    bpin_carry=0.655,           # [GC:8]
    bpin_max=4.188,             # [GC:5]
    rack_back_ground=40, rack_back_carry=45, rack_back_max=60, dump_max=52,   # [GC:13,12,10,11] deg
    bucket_w=2.994,             # [GC:tbl] teeth; 2.927 with bolt-on edge; [WA:C] 2.990
    overall_len_teeth=8.466,    # [GC:tbl] 3.1 m3 GP teeth (target; 8.29 edge, ref B 8.68)
    dump_clear_45=2.933,        # [GC:tbl] dump clearance at max lift & 45 deg discharge (teeth)
    reach_45=1.372,             # [GC:tbl]
    steer_max_deg=40,           # design (ref A is quoted at 38 deg)
    # linkage (Unity z, y) -- side-view pins [meas] + numeric search
    A=(0.85, 2.00),             # boom pivot on the front-frame tower [meas ~(0.86, 2.05)]
    B=(3.20, 0.36),             # bucket hinge, bucket flat on the ground [meas]
    E=(2.825, 1.480),           # bellcrank pivot on the boom cross-tube
    T=(3.088, 2.164),           # bellcrank top (tilt-cylinder rod pin)
    D=(2.366, 0.954),           # bellcrank bottom (link pin)
    Kp=(3.224, 0.729),          # bucket link pin
    C=(1.128, 1.851),           # tilt-cylinder base on the tower
    lift_base=(0.45, 0.62),     # lift-cylinder base, low on the front frame
    lift_rod=(1.555, 1.508),    # lift-cylinder rod pin under the boom
    boom_x=0.58,                # boom arm / lift cylinder centre-line x
    steer_cyl=(0.48, 0.80, -0.78, 0.42),   # x, y, rear-frame z, front-frame z [style]
    cab=dict(z0=-1.20, z1=0.12, hw=0.80, floor=1.70, roof=3.36),   # [meas] roof -1.26..0.28, 3.40
)

_I = {}


# ==================================================================================
# 2-D linkage helpers in the side plane (z, y); ccw = nose UP = -Unity localEulerAngles.x
# ==================================================================================
def _R(a, v):
    c, s = math.cos(a), math.sin(a)
    return (c * v[0] - s * v[1], s * v[0] + c * v[1])


def _sub(a, b):
    return (a[0] - b[0], a[1] - b[1])


def _add(a, b):
    return (a[0] + b[0], a[1] + b[1])


def _cross(a, b):
    return a[0] * b[1] - a[1] * b[0]


def _dist(a, b):
    return math.hypot(a[0] - b[0], a[1] - b[1])


def _ang(v):
    return math.atan2(v[1], v[0])


def zbar_solve(bucket_x):
    """Boom-local Z-bar closure for a Bucket.x (radians, Unity sign). Returns (bellcrank_x, D, K).
    Exact: D = intersection of circle(E, |ED|) and circle(K, |DK|) on the rest-pose branch."""
    E, D0, K0, B = P["E"], P["D"], P["Kp"], P["B"]
    Kp = _add(B, _R(-bucket_x, _sub(K0, B)))
    r0, r1 = _dist(E, D0), _dist(D0, K0)
    d = _dist(E, Kp)
    if not abs(r0 - r1) <= d <= r0 + r1:
        raise ValueError(f"Z-bar cannot close at Bucket.x={math.degrees(bucket_x):.1f}")
    a = (r0 * r0 - r1 * r1 + d * d) / (2 * d)
    h = math.sqrt(max(0.0, r0 * r0 - a * a))
    ex = ((Kp[0] - E[0]) / d, (Kp[1] - E[1]) / d)
    m = (E[0] + a * ex[0], E[1] + a * ex[1])
    s0 = _cross(_sub(K0, E), _sub(D0, E))
    for D in ((m[0] - h * ex[1], m[1] + h * ex[0]), (m[0] + h * ex[1], m[1] - h * ex[0])):
        if _cross(_sub(Kp, E), _sub(D, E)) * s0 > 0:
            th = _ang(_sub(D, E)) - _ang(_sub(D0, E))
            th = (th + math.pi) % (2 * math.pi) - math.pi
            return -th, D, Kp
    raise ValueError("Z-bar branch lost")


def bellcrank_x(bucket_x):
    return zbar_solve(bucket_x)[0]


# ==================================================================================
# pose
# ==================================================================================
def pose(K, boom=0.0, bucket=0.0, steer=0.0, wheels=(0.0, 0.0, 0.0, 0.0)):
    """boom / bucket / steer in radians (Unity localEulerAngles sign, see header);
    wheels = distance rolled (m) by FrontLeft, FrontRight, RearLeft, RearRight."""
    K.rot_x("Boom", boom)
    K.rot_x("Bucket", bucket)
    K.rot_x("Bellcrank", bellcrank_x(bucket))
    if "FrontFrame" in K.NODES:
        K.NODES["FrontFrame"].rotation_euler = (0, 0, -steer)     # Unity +Y yaw == Blender -Z
    for nm, d in zip(("WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight"), wheels):
        K.rot_x(nm, d / WHEEL_R)
    K.update()


def world_pose(K, boom, world_pitch_deg):
    """kwargs for a boom angle + a WORLD bucket pitch (deg, + = rack back / lip up)."""
    return dict(boom=boom, bucket=-math.radians(world_pitch_deg) - boom)


# ==================================================================================
# geometry helpers
# ==================================================================================
def ribbon(center, hw):
    """Closed (z,y) outline of a thick polyline: centre points + half-widths (mitred)."""
    n = len(center)
    segs = []
    for i in range(n - 1):
        dz, dy = center[i + 1][0] - center[i][0], center[i + 1][1] - center[i][1]
        l = math.hypot(dz, dy)
        segs.append((-dy / l, dz / l))
    left, right = [], []
    for i, (c, w) in enumerate(zip(center, hw)):
        if i == 0:
            nz, ny = segs[0]; k = 1.0
        elif i == n - 1:
            nz, ny = segs[-1]; k = 1.0
        else:
            a, b = segs[i - 1], segs[i]
            nz, ny = a[0] + b[0], a[1] + b[1]
            l = math.hypot(nz, ny); nz, ny = nz / l, ny / l
            k = 1.0 / max(0.4, nz * a[0] + ny * a[1])
        left.append((c[0] + nz * w * k, c[1] + ny * w * k))
        right.append((c[0] - nz * w * k, c[1] - ny * w * k))
    return left + list(reversed(right))


def arc(cz, cy, r, a0, a1, n):
    return [(cz + r * math.cos(math.radians(a0 + (a1 - a0) * i / n)),
             cy + r * math.sin(math.radians(a0 + (a1 - a0) * i / n))) for i in range(n + 1)]


def fender(p, x0, x1, cz, cy, r_in, r_out, a0, a1, n, key="paint", lip_key="paint_dark"):
    """Arched mudguard over a wheel (angles in the (z,y) plane, 0 = forward)."""
    prof = arc(cz, cy, r_out, a0, a1, n) + list(reversed(arc(cz, cy, r_in, a0, a1, n)))
    p.prism(prof, "x", x0, x1, key, 0.0)
    with p.detail():   # charcoal lip on the outer edge
        s = 1 if x1 > 0 else -1
        xo = x1 if abs(x1) > abs(x0) else x0
        prof2 = arc(cz, cy, r_out + 0.012, a0, a1, n) + list(reversed(arc(cz, cy, r_in - 0.012, a0, a1, n)))
        p.prism(prof2, "x", xo - s * 0.03, xo + s * 0.006, lip_key, 0.0)


# ==================================================================================
# build
# ==================================================================================
def build(K):
    Piece, node, attach = K.Piece, K.node, K.attach
    R = WHEEL_R
    WB = P["wheelbase"]
    zf, zr = WB / 2, -WB / 2
    TX = P["tread"] / 2
    TW = P["tyre_w"]
    tin = TX - TW / 2                                   # tyre inner face x (0.765)
    zcw = zr - P["rear_axle_to_cw"]                     # counterweight rear face (-3.705)
    cab = P["cab"]
    cz0, cz1, CW, fy, ry = cab["z0"], cab["z1"], cab["hw"], cab["floor"], cab["roof"]
    Az, Ay = P["A"]; Bz, By = P["B"]; BX = P["boom_x"]

    node("Root", None, (0, 0, 0))
    # ============================================================ REAR FRAME (engine, cab)
    node("RearFrame", "Root", (0, 0, 0))
    r = Piece()
    r.box((-0.55, 0.50, zcw + 0.30), (0.55, 1.18, -0.48), "paint_dark", 0.0)             # main rails
    for y0, y1 in ((0.52, 0.62), (1.10, 1.20)):                                         # hitch plates
        r.prism([(-0.50, y0), (0.16, y0), (0.16, y1), (-0.50, y1)], "x", -0.30, 0.30, "paint_dark", 0.0)
        r.cyl((0, y0 - 0.02, 0), (0, y1 + 0.02, 0), 0.11, "paint_dark", 10)
    r.cyl((0, 0.48, 0), (0, 1.24, 0), 0.06, "worn", 8)                                  # hitch pin
    sc = P["steer_cyl"]
    for s in (-1, 1):                                                                    # steer-cyl base lugs
        r.box((s * 0.34, sc[1] - 0.08, sc[2] - 0.09), (s * 0.56, sc[1] + 0.08, sc[2] + 0.09), "paint_dark", 0.0)
    r.cyl((-tin + 0.02, R, zr), (tin - 0.02, R, zr), 0.17, "paint_dark", 10)             # rear axle
    r.box((-0.34, 0.42, zr - 0.30), (0.34, 1.05, zr + 0.30), "paint_dark", 0.0)           # diff housing
    # engine hood: profile in (z, y), rounded rear
    hz0, hz1 = zcw + 0.28, cz0 - 0.06
    hood = [(hz1, 1.16), (hz1, P["hood_top"] - 0.03), (hz1 - 0.35, P["hood_top"] - 0.05),
            (hz0 + 0.45, 2.42), (hz0 + 0.12, 2.33), (hz0, 2.16), (hz0, 1.16)]
    r.prism(hood, "x", -0.70, 0.70, "paint", 0.05)
    r.box((-0.62, 1.30, hz0 - 0.03), (0.62, 2.10, hz0 + 0.02), "steel", 0.0)             # rear grille core
    for k in range(5):                                                                   # grille bars
        y = 1.38 + k * 0.155
        r.box((-0.64, y, hz0 - 0.06), (0.64, y + 0.045, hz0 - 0.01), "paint", 0.0)
    # counterweight: yellow block with a charcoal bumper
    cw = [(zcw, 0.62), (zcw, 1.22), (zcw + 0.14, 1.34), (zcw + 0.42, 1.34), (zcw + 0.42, 0.56)]
    r.prism(cw, "x", -1.10, 1.10, "paint", 0.04)
    r.box((-1.06, 0.50, zcw - 0.02), (1.06, 0.66, zcw + 0.40), "paint_dark", 0.02)
    # rear fenders + platform decks beside the cab
    for s in (-1, 1):
        fender(r, s * (tin - 0.03), s * (TX + TW / 2 + 0.06), zr, R, 0.86, 0.92, 12, 168, 7)
        r.box((s * 0.55, fy - 0.08, cz0 - 0.10), (s * 1.30, fy, zr + 0.95), "paint", 0.015)   # side deck
    r.box((-0.40, fy - 0.10, cz0 - 0.05), (0.40, fy, cz1 - 0.05), "paint_dark", 0.0)          # cab floor plate
    # exhaust stack + precleaner on the hood
    sx, sz = -0.32, cz0 - 0.70
    r.cyl((sx, P["hood_top"] - 0.20, sz), (sx, P["exhaust_top"] - 0.05, sz), 0.075, "steel", 10)
    r.cyl((sx, P["exhaust_top"] - 0.06, sz), (sx, P["exhaust_top"], sz + 0.02), 0.09, "steel", 10)
    r.cyl((0.30, P["hood_top"] - 0.15, sz + 0.35), (0.30, 2.86, sz + 0.35), 0.09, "paint_dark", 10)
    r.cyl((0.30, 2.86, sz + 0.35), (0.30, 2.95, sz + 0.35), 0.15, "paint_dark", 10, r2=0.10)
    with r.detail():
        for s in (-1, 1):
            xo = s * 0.70
            # hood side: louvre block near the rear, door split + seams
            for k in range(4):
                y = 1.66 + k * 0.10
                r.strip((xo, y, hz0 + 0.28), (xo + s * 0.014, y + 0.04, hz0 + 0.95))
            for z in (hz0 + 1.15, hz1 - 0.45):
                r.strip((xo, 1.22, z - 0.009), (xo + s * 0.006, 2.30, z + 0.009))
            r.strip((xo, 2.32, hz0 + 0.20), (xo + s * 0.006, 2.338, hz1 - 0.10))           # hood shoulder seam
            r.strip((s * 1.10, 1.10, zcw + 0.05), (s * 1.106, 1.12, zcw + 0.40), "paint_dark")
            # tail cluster in the counterweight: red over white
            r.lamp((s * 0.78, 1.12, zcw), "-z", 0.20, 0.10, 0.05, "tail")
            r.lamp((s * 0.78, 0.98, zcw), "-z", 0.20, 0.10, 0.05, "lamp")
            # handrails on the platform beside the cab (front + rear posts, mid rail)
            x = s * 1.26
            r.tube([(x, fy, cz0 + 0.05), (x, fy + 0.95, cz0 + 0.05), (x, fy + 0.95, zr + 0.95 - 0.05),
                    (x, fy, zr + 0.95 - 0.05)], 0.022)
            r.tube([(x, fy + 0.50, cz0 + 0.05), (x, fy + 0.50, zr + 0.95 - 0.05)], 0.016)
            # side deck tread plate
            r.strip((s * 0.82, fy, cz0 - 0.05), (s * 1.24, fy + 0.008, zr + 0.90), "paint_dark")
        # access ladder (left), between the rear tyre and the hitch
        lz0, lz1 = -0.70, -0.42
        for z in (lz0, lz1):
            r.box((-1.22, 0.48, z - 0.02), (-1.18, fy, z + 0.02), "paint_dark", 0.0)
        for k in range(4):
            y = 0.62 + k * 0.29
            r.box((-1.24, y, lz0), (-1.16, y + 0.03, lz1), "steel", 0.0)
        r.tube([(-1.24, fy, -0.72), (-1.24, fy + 0.95, -0.72)], 0.02)
        r.cyl((0.30, 2.74, sz + 0.35), (0.30, 2.80, sz + 0.35), 0.096, "worn", 10)        # precleaner band
        r.cyl((sx, 2.95, sz), (sx, 3.18, sz), 0.088, "worn", 10)                         # stack heat shield
        r.cyl((0.95, 1.62, -2.10), (0.95, 1.67, -2.10), 0.07, "worn", 10)                # fuel cap
    attach("RearFrame", r)
    node("ExhaustTip", "RearFrame", (sx, P["exhaust_top"], sz + 0.02))

    # ------------------------------------------------------------ Cab
    node("Cab", "RearFrame", (0, fy, (cz0 + cz1) / 2))
    k = Piece()
    k.box((-CW, fy, cz0), (CW, fy + 0.40, cz1), "paint_dark", 0.03)                         # lower body
    k.prism([(-CW + 0.03, fy + 0.40), (CW - 0.03, fy + 0.40), (CW - 0.07, ry - 0.12), (-CW + 0.07, ry - 0.12)],
            "z", cz0 + 0.03, cz1 - 0.03, "glass", 0.0)                                       # glass volume
    for sx_ in (-1, 1):
        for z in (cz0, cz1):
            k.box((sx_ * (CW - 0.02) - 0.055, fy + 0.35, z - 0.055), (sx_ * (CW - 0.02) + 0.055, ry - 0.08, z + 0.055),
                  "steel", 0.012)                                                             # ROPS posts
        k.box((sx_ * (CW - 0.10), fy + 0.40, -0.68), (sx_ * (CW + 0.005), ry - 0.12, -0.61), "steel", 0.0)  # door mullion
    k.box((-CW - 0.01, fy + 0.37, cz0 - 0.01), (CW + 0.01, fy + 0.45, cz1 + 0.01), "steel", 0.0)  # sill band
    k.box((-CW + 0.03, ry - 0.22, cz0), (CW - 0.03, ry - 0.12, cz1), "steel", 0.0)              # header band
    k.box((-CW - 0.03, ry - 0.15, cz0 - 0.06), (CW + 0.03, ry - 0.10, cz1 + 0.10), "steel", 0.0)  # gutter
    k.box((-CW - 0.06, ry - 0.10, cz0 - 0.08), (CW + 0.06, ry, cz1 + 0.14), "paint", 0.03)      # roof
    with k.detail():
        for sx_ in (-1, 1):
            xm = sx_ * (CW + 0.25)
            k.tube([(sx_ * (CW + 0.03), ry - 0.40, cz1 + 0.04), (xm, ry - 0.38, cz1 + 0.06)], 0.014)
            k.box((xm - 0.06, ry - 0.62, cz1 + 0.035), (xm + 0.06, ry - 0.34, cz1 + 0.09), "steel", 0.01)
            k.box((xm - 0.05, ry - 0.61, cz1 + 0.028), (xm + 0.05, ry - 0.35, cz1 + 0.036), "worn", 0)
            for zz, f in ((cz1 + 0.10, "+z"), (cz0 - 0.04, "-z")):   # roof work lights front + rear
                k.lamp((sx_ * 0.52, ry + 0.035, zz), f, 0.17, 0.11, 0.08, "lamp")
            k.box((sx_ * (CW + 0.003), fy + 0.55, -0.78), (sx_ * (CW + 0.02), fy + 0.58, -0.66), "worn", 0)
        k.tube([(0.05, fy + 0.48, cz1 - 0.015), (-0.34, fy + 1.05, cz1 - 0.015)], 0.011)        # wiper
        k.tube([(-CW - 0.02, fy + 0.15, -0.95), (-CW - 0.02, fy + 1.15, -0.95)], 0.018)       # grab handle
    attach("Cab", k)
    bz = cz0 + 0.22
    node("Beacon", "Cab", (0, ry, bz))
    b = Piece()
    b.cyl((0, ry, bz), (0, ry + 0.03, bz), 0.075, "steel", 10)
    b.cyl((0, ry + 0.03, bz), (0, ry + 0.085, bz), 0.06, "beacon", 10, r2=0.055)
    b.cyl((0, ry + 0.085, bz), (0, ry + 0.10, bz), 0.055, "beacon", 10, r2=0.03)   # top 3.46 ~ ROPS 3.458 [GC:1]
    attach("Beacon", b)

    # ------------------------------------------------------------ rear wheels
    def wheel(name, parent, x, z, side):
        node(name, parent, (x, R, z))
        w = Piece()
        K.wheel_piece(w, (x, R, z), R, TW, side, rim_r=0.40, blocks=12, block_h=0.055, bolts=5)
        attach(name, w)
    wheel("WheelRearLeft", "RearFrame", -TX, zr, -1)
    wheel("WheelRearRight", "RearFrame", TX, zr, 1)

    # ============================================================ FRONT FRAME (articulates)
    node("FrontFrame", "RearFrame", (0, 0.86, 0))
    f = Piece()
    f.cyl((0, 0.63, 0), (0, 1.08, 0), 0.20, "paint_dark", 12)                              # hitch boss
    f.box((-0.40, 0.50, 0.36), (0.40, 1.16, zf + 0.42), "paint", 0.02)                      # main frame
    f.box((-0.18, 0.66, 0.0), (0.18, 1.05, 0.40), "paint_dark", 0.0)                         # boss neck
    f.box((-0.42, 1.10, 0.62), (0.42, 2.12, 1.30), "paint", 0.03)                           # tower
    f.prism([(0.62, 1.10), (1.30, 1.10), (1.30, 1.62), (0.62, 2.12)], "x", -0.44, 0.44, "paint", 0.03)
    for s in (-1, 1):
        f.cyl((s * 0.40, Ay, Az), (s * 0.68, Ay, Az), 0.12, "paint_dark", 10)               # A-pin boss
        f.box((s * 0.36, Ay - 0.16, Az - 0.18), (s * 0.48, Ay + 0.14, Az + 0.16), "paint", 0.0)
        lb = P["lift_base"]
        f.box((s * 0.42, lb[1] - 0.10, lb[0] - 0.12), (s * 0.70, lb[1] + 0.10, lb[0] + 0.12), "paint_dark", 0.0)
        sc = P["steer_cyl"]
        f.box((s * 0.36, sc[1] - 0.08, sc[3] - 0.08), (s * 0.55, sc[1] + 0.08, sc[3] + 0.08), "paint_dark", 0.0)
    f.box((-0.14, P["C"][1] - 0.10, P["C"][0] - 0.10), (0.14, P["C"][1] + 0.10, P["C"][0] + 0.12), "paint_dark", 0.01)
    f.cyl((-tin + 0.02, R, zf), (tin - 0.02, R, zf), 0.17, "paint_dark", 10)               # front axle
    f.box((-0.34, 0.42, zf - 0.30), (0.34, 0.80, zf + 0.30), "paint_dark", 0.0)              # diff housing
    for s in (-1, 1):   # short roading fenders over the front tyres
        fender(f, s * (tin - 0.03), s * (TX + TW / 2 + 0.06), zf, R, 0.86, 0.92, 70, 150, 6)
    with f.detail():
        for s in (-1, 1):
            # headlights + indicator on the front fenders
            fz = zf + 0.92 * math.cos(math.radians(70)); fyy = R + 0.92 * math.sin(math.radians(70))
            f.box((s * TX - 0.03, fyy, fz - 0.03), (s * TX + 0.03, fyy + 0.08, fz + 0.03), "steel", 0)
            f.lamp((s * TX, fyy + 0.14, fz), "+z", 0.18, 0.12, 0.08, "lamp")
            # tower seams
            f.strip((s * 0.42, 1.20, 0.86), (s * 0.426, 2.02, 0.875))
            f.strip((s * 0.40, 1.16, 0.30), (s * 0.406, 1.17, zf + 0.30), "steel")
        f.box((-0.30, 2.12, 0.80), (0.30, 2.14, 1.20), "paint_dark", 0)                      # tower top plate
    attach("FrontFrame", f)
    wheel("WheelFrontLeft", "FrontFrame", -TX, zf, -1)
    wheel("WheelFrontRight", "FrontFrame", TX, zf, 1)

    # ------------------------------------------------------------ Boom (twin arms + cross-tube)
    node("Boom", "FrontFrame", (0, Ay, Az))
    bm = Piece()
    E, T, D, Kp, C = P["E"], P["T"], P["D"], P["Kp"], P["C"]
    centre = [(Az, Ay), (1.85, 1.86), (2.62, 1.62), (2.98, 1.22), (Bz, By)]
    outline = ribbon(centre, [0.17, 0.18, 0.18, 0.15, 0.12])
    for s in (-1, 1):
        bm.prism(outline, "x", s * BX - 0.08, s * BX + 0.08, "paint", 0.025)
        bm.cyl((s * BX - 0.11, Ay, Az), (s * BX + 0.11, Ay, Az), 0.11, "steel", 10)       # A eye
        bm.cyl((s * BX - 0.10, By, Bz), (s * BX + 0.10, By, Bz), 0.09, "steel", 10)       # B eye
        lr = P["lift_rod"]
        bm.prism([(lr[0] - 0.13, lr[1] + 0.25), (lr[0] + 0.15, lr[1] + 0.22), (lr[0] + 0.06, lr[1] - 0.07),
                  (lr[0] - 0.08, lr[1] - 0.07)], "x", s * BX - 0.07, s * BX + 0.07, "paint_dark", 0.01)
    bm.cyl((-BX - 0.02, E[1], E[0]), (BX + 0.02, E[1], E[0]), 0.13, "paint", 12)           # cross-tube
    bm.cyl((-0.20, E[1], E[0]), (0.20, E[1], E[0]), 0.16, "paint_dark", 12)                 # bellcrank hub
    with bm.detail():
        stripe = ribbon([(1.05, 1.965), (1.85, 1.86), (2.55, 1.645)], [0.035] * 3)
        for s in (-1, 1):   # charcoal stripe along the outer face of each arm
            bm.prism(stripe, "x", s * (BX + 0.080), s * (BX + 0.087), "paint_dark", 0.0)
    attach("Boom", bm)

    # ------------------------------------------------------------ Bellcrank (Z-bar)
    node("Bellcrank", "Boom", (0, E[1], E[0]))
    bc = Piece()
    out = ribbon([T, E, D], [0.10, 0.16, 0.10])
    for s in (-1, 1):
        bc.prism(out, "x", s * 0.10 - 0.035, s * 0.10 + 0.035, "paint", 0.01)
    bc.cyl((-0.15, T[1], T[0]), (0.15, T[1], T[0]), 0.07, "steel", 8)
    bc.cyl((-0.15, D[1], D[0]), (0.15, D[1], D[0]), 0.07, "steel", 8)
    attach("Bellcrank", bc)

    # ------------------------------------------------------------ Bucket
    node("Bucket", "Boom", (0, By, Bz))
    bk = Piece()
    BW = P["bucket_w"] / 2 - 0.03            # side plate outer x (teeth add the last 3 cm a side)
    lip = 4.60
    outer = [(lip, 0.0), (3.80, 0.0), (3.52, 0.06), (3.34, 0.24), (3.26, 0.52), (3.27, 0.92), (3.33, 1.22), (3.42, 1.36)]
    inner = [(lip - 0.06, 0.05), (3.82, 0.05), (3.56, 0.10), (3.39, 0.27), (3.31, 0.53), (3.32, 0.92), (3.38, 1.20),
             (3.47, 1.32)]
    bk.prism(outer + list(reversed(inner)), "x", -BW + 0.03, BW - 0.03, "paint", 0.0)       # wrapper / floor / back
    side = [(3.27, 0.30), (3.55, -0.0), (lip + 0.02, 0.0), (lip + 0.02, 0.10), (4.42, 0.62), (4.12, 1.18),
            (3.90, 1.36), (3.44, 1.42), (3.25, 1.00)]
    for s in (-1, 1):
        bk.prism(side, "x", s * BW - 0.035, s * BW, "paint", 0.008)                          # side plates
        bk.prism([(lip + 0.02, 0.0), (lip + 0.10, 0.0), (lip + 0.10, 0.12), (4.30, 0.62), (4.20, 0.58)], "x",
                 s * BW - 0.045, s * BW + 0.012, "worn", 0.0)                                 # side cutter
        x = s * BX
        bk.prism([(3.28, 0.16), (3.28, 0.78), (3.20, 0.54), (3.09, 0.36), (3.20, 0.22)], "x",
                 x - 0.11, x + 0.11, "paint_dark", 0.01)                                     # boom-pin lugs
        bk.prism([(3.28, 0.56), (3.28, 0.98), (3.24, 0.86), (3.13, 0.73), (3.24, 0.62)], "x",
                 s * 0.20 - 0.04, s * 0.20 + 0.04, "paint_dark", 0.01)                       # link lugs
    bk.cyl((-0.26, Kp[1], Kp[0]), (0.26, Kp[1], Kp[0]), 0.055, "worn", 8)                     # link pin
    bk.box((-BW + 0.03, 1.32, 3.40), (BW - 0.03, 1.42, 3.62), "paint", 0.01)                 # top rim / spill guard
    bk.box((-BW + 0.03, 0.0, lip - 0.30), (BW - 0.03, 0.045, lip + 0.02), "worn", 0.0)        # cutting edge
    nt = 8
    for i in range(nt):                                                                      # teeth + adapters
        x = -BW + 0.10 + i * (2 * BW - 0.20) / (nt - 1)
        bk.prism([(lip - 0.08, 0.0), (lip + 0.08, 0.0), (lip + 0.08, 0.10), (lip - 0.08, 0.075)], "x",
                 x - 0.065, x + 0.065, "steel", 0.0)
        bk.prism([(lip + 0.08, 0.005), (lip + 0.15, 0.025), (lip + 0.15, 0.05), (lip + 0.08, 0.095)], "x",
                 x - 0.05, x + 0.05, "worn", 0.0)
    with bk.detail():
        for x in (-1.05, 1.05):                                                              # back stiffeners
            bk.prism([(3.27, 0.40), (3.18, 0.50), (3.20, 1.10), (3.31, 1.18)], "x", x - 0.04, x + 0.04, "paint", 0)
        bk.strip((-BW + 0.05, 1.42, 3.45), (BW - 0.05, 1.425, 3.58), "paint_dark")
    attach("Bucket", bk)
    node("BucketLinkPin", "Bucket", (0, Kp[1], Kp[0]), size=0.05)

    # ------------------------------------------------------------ Bucket link (aimed, on the bellcrank)
    node("BucketLink", "Bellcrank", (0, D[1], D[0]))
    lk_len = _dist(D, Kp)
    bl = K.NODES["BucketLink"]
    bl.rotation_mode = "QUATERNION"; bl.rotation_quaternion = K.aim_quat("BucketLink", "BucketLinkPin")
    con = bl.constraints.new("DAMPED_TRACK"); con.target = K.NODES["BucketLinkPin"]; con.track_axis = "TRACK_Z"
    lk = Piece()
    for s in (-1, 1):
        lk.box((s * 0.16 - 0.03, -0.06, -0.07), (s * 0.16 + 0.03, 0.06, lk_len + 0.07), "paint", 0.012)   # Blender-local
    attach("BucketLink", lk, local_blender=True)
    K.update()

    # ------------------------------------------------------------ solve the spec poses
    bp_local = K.capture("Boom", (BX, By, Bz))
    def bpin_y(a):
        pose(K, boom=a)
        return K.world_unity("Boom", bp_local).y
    CARRY = K.solve(bpin_y, P["bpin_carry"], -0.6, 0.2)
    MAXL = K.solve(bpin_y, P["bpin_max"], -2.0, -0.6)
    pose(K)
    poses = dict(
        ground=dict(),
        ground_rb40=world_pose(K, 0.0, P["rack_back_ground"]),
        dig5=world_pose(K, 0.0, -5),
        carry=world_pose(K, CARRY, P["rack_back_carry"]),
        mid=world_pose(K, MAXL / 2, 10),
        max_rb60=world_pose(K, MAXL, P["rack_back_max"]),
        max_dump52=world_pose(K, MAXL, -P["dump_max"]),
    )
    # envelope sweep for the tilt cylinder: every boom angle x its bucket range
    env = []
    for i in range(7):
        fb = i / 6
        a = MAXL * fb
        lo, hi = -5 + (-P["dump_max"] + 5) * fb, P["rack_back_ground"] + (P["rack_back_max"] - P["rack_back_ground"]) * fb
        for j in range(7):
            env.append(world_pose(K, a, lo + (hi - lo) * j / 6))
    _I.update(CARRY=CARRY, MAXL=MAXL, poses=poses, env=env, bp_local=bp_local)

    hyd = {}
    boom_poses = [lambda kw=kw: pose(K, **kw) for kw in poses.values()]
    lb, lr = P["lift_base"], P["lift_rod"]
    for side, s in (("Left", -1), ("Right", 1)):
        hyd["Lift" + side] = K.hydraulic("Lift", side, "FrontFrame", (s * BX, lb[1], lb[0]), "Boom",
                                         (s * BX, lr[1], lr[0]), 0.095, 0.052, boom_poses, lambda: pose(K))
    hyd["Tilt"] = K.hydraulic("Tilt", "", "FrontFrame", (0, C[1], C[0]), "Bellcrank", (0, T[1], T[0]), 0.11, 0.06,
                              [lambda kw=kw: pose(K, **kw) for kw in env + list(poses.values())], lambda: pose(K))
    sc = P["steer_cyl"]
    smax = math.radians(P["steer_max_deg"])
    steer_poses = [lambda a=a: pose(K, steer=a) for a in (-smax, -smax / 2, 0.0, smax / 2, smax)]
    for side, s in (("Left", -1), ("Right", 1)):
        hyd["Steer" + side] = K.hydraulic("Steer", side, "RearFrame", (s * sc[0], sc[1], sc[2]), "FrontFrame",
                                          (s * sc[0], sc[1], sc[3]), 0.075, 0.04, steer_poses, lambda: pose(K),
                                          clearance=0.04, seg=8, hoses=False)

    # ------------------------------------------------------------ Z-bar fit for Unity
    xs = [math.radians(-45 + i * 0.5) for i in range(int((140 + 45) / 0.5) + 1)]
    ys = [bellcrank_x(x) for x in xs]
    fit = _polyfit(xs, ys, 5)
    err = max(abs(_polyval(fit, x) - y) for x, y in zip(xs, ys))
    _I["fit"] = fit
    deg = math.degrees
    return dict(
        wheel=dict(radius_m=R, deg_per_m=round(deg(1 / R), 3), rule="Wheel*.x += degrees(d / 0.765)"),
        boom=dict(carry_deg=round(deg(CARRY), 2), max_lift_deg=round(deg(MAXL), 2), ground_deg=0.0),
        bucket_rel_deg={k: round(deg(v.get("bucket", 0.0)), 2) for k, v in poses.items()},
        boom_deg={k: round(deg(v.get("boom", 0.0)), 2) for k, v in poses.items()},
        bellcrank_deg={k: round(deg(bellcrank_x(v.get("bucket", 0.0))), 2) for k, v in poses.items()},
        steer_deg=[-P["steer_max_deg"], P["steer_max_deg"]],
        zbar=dict(fit=dict(form="Bellcrank.x[rad] = sum(c[i] * b^i, i=0..5), b = Bucket.x[rad]",
                           coeffs=[round(c, 6) for c in fit], range_deg=[-45, 140],
                           max_err_deg=round(deg(err), 3)),
                  link_len_m=round(lk_len, 4)),
        hydraulics=hyd,
        keep_full_lod1=[n for n in K.NODES if "Cylinder" in n or "Rod" in n] + ["Beacon", "Bucket", "BucketLink",
                                                                               "Bellcrank"],
    )


def _polyfit(xs, ys, deg):
    n = deg + 1
    A = [[sum(x ** (i + j) for x in xs) for j in range(n)] for i in range(n)]
    b = [sum(y * x ** i for x, y in zip(xs, ys)) for i in range(n)]
    for c in range(n):                                   # gaussian elimination
        p = max(range(c, n), key=lambda r: abs(A[r][c]))
        A[c], A[p] = A[p], A[c]; b[c], b[p] = b[p], b[c]
        for r in range(c + 1, n):
            f = A[r][c] / A[c][c]
            for k in range(c, n):
                A[r][k] -= f * A[c][k]
            b[r] -= f * b[c]
    x = [0.0] * n
    for r in reversed(range(n)):
        x[r] = (b[r] - sum(A[r][k] * x[k] for k in range(r + 1, n))) / A[r][r]
    return x


def _polyval(c, x):
    return sum(ci * x ** i for i, ci in enumerate(c))


# ==================================================================================
# checks
# ==================================================================================
def _world_tris(K, names, lod="LOD0"):
    verts, polys = [], []
    for nm in names:
        for ch in K.NODES[nm].children:
            if ch.type != "MESH" or not ch.name.endswith("_" + lod):
                continue
            mw = ch.matrix_world
            base = len(verts)
            verts += [mw @ v.co for v in ch.data.vertices]
            polys += [[base + i for i in p.vertices] for p in ch.data.polygons]
    return verts, polys


def _overlap(K, ga, gb, ignore=None):
    from mathutils.bvhtree import BVHTree
    va, pa = _world_tris(K, ga)
    vb, pb = _world_tris(K, gb)
    ta, tb = BVHTree.FromPolygons(va, pa), BVHTree.FromPolygons(vb, pb)
    pairs = ta.overlap(tb)
    if ignore:
        def cen(v, p):
            c = sum((v[i] for i in p), v[p[0]] * 0) / len(p)
            return c
        pairs = [(i, j) for i, j in pairs if not ignore(K.to_unity(cen(va, pa[i])))]
    return len(pairs)


def _subtree(K, root, stop=()):
    out = []
    def walk(o):
        if o.type == "EMPTY" and o.name not in stop:
            out.append(o.name)
            for c in o.children:
                walk(c)
    walk(K.NODES[root])
    return out


def checks(K, info):
    out = {}
    deg = math.degrees
    poses = _I["poses"]
    # --- spec B-pin heights
    res = {}
    E, Kp, D = P["E"], P["Kp"], P["D"]
    BX = P["boom_x"]
    b_on_boom = _I["bp_local"]
    b_on_bucket = K.capture("Bucket", (BX, P["B"][1], P["B"][0]))
    e_on_boom = K.capture("Boom", (0, E[1], E[0]))
    e_on_bc = K.capture("Bellcrank", (0, E[1], E[0]))
    d_on_bc = K.capture("Bellcrank", (0, D[1], D[0]))
    k_on_bk = K.capture("Bucket", (0, Kp[1], Kp[0]))
    edge_on_bk = K.capture("Bucket", (0, 0.0, 4.60))
    floor_a = K.capture("Bucket", (0, 0.0, 3.80)); floor_b = K.capture("Bucket", (0, 0.0, 4.60))
    hitch_r = K.capture("RearFrame", (0, 0.86, 0)); hitch_f = K.capture("FrontFrame", (0, 0.86, 0))
    hitch_f_top = K.capture("FrontFrame", (0, 1.20, 0))
    link0 = _dist(D, Kp)
    for nm, kw in poses.items():
        pose(K, **kw)
        fa, fb = K.world_unity("Bucket", floor_a), K.world_unity("Bucket", floor_b)
        res[nm] = dict(
            bpin_y=round(K.world_unity("Boom", b_on_boom).y, 4),
            bucket_hinge_drift=round((K.world_unity("Boom", b_on_boom) - K.world_unity("Bucket", b_on_bucket)).length, 6),
            bellcrank_pivot_drift=round((K.world_unity("Boom", e_on_boom) - K.world_unity("Bellcrank", e_on_bc)).length, 6),
            link_len_err=round(abs((K.world_unity("Bellcrank", d_on_bc) - K.world_unity("Bucket", k_on_bk)).length - link0), 6),
            floor_pitch_deg=round(deg(math.atan2(fb.y - fa.y, fb.z - fa.z)), 2),
            edge=K.world_unity("Bucket", edge_on_bk),
            hyd={h: K.hydraulic_check(v) for h, v in info["hydraulics"].items() if not h.startswith("Steer")},
        )
    pose(K)
    out["bpin_heights_spec"] = dict(carry=res["carry"]["bpin_y"], spec_carry=P["bpin_carry"], max=res["max_rb60"]["bpin_y"],
                                    spec_max=P["bpin_max"], ground=res["ground"]["bpin_y"],
                                    ok=abs(res["carry"]["bpin_y"] - P["bpin_carry"]) < 0.005
                                    and abs(res["max_rb60"]["bpin_y"] - P["bpin_max"]) < 0.005)
    want = dict(ground=0, ground_rb40=P["rack_back_ground"], carry=P["rack_back_carry"], max_rb60=P["rack_back_max"],
                max_dump52=-P["dump_max"], dig5=-5, mid=10)
    out["bucket_angles_spec"] = dict(per_pose={n: dict(floor_pitch_deg=res[n]["floor_pitch_deg"], spec=want[n]) for n in want},
                                     ok=all(abs(res[n]["floor_pitch_deg"] - want[n]) < 0.2 for n in want))
    drift = max(max(v["bucket_hinge_drift"], v["bellcrank_pivot_drift"]) for v in res.values())
    lerr = max(v["link_len_err"] for v in res.values())
    out["linkage_pins_fixed"] = dict(bucket_hinge_and_bellcrank_pivot_max_drift_m=drift, link_closure_max_err_m=lerr,
                                     ok=drift < 1e-4 and lerr < 1e-4)
    # dump clearance / reach at max lift, 45 deg discharge (the spec-sheet definition)
    pose(K, **world_pose(K, _I["MAXL"], -45))
    edge45 = K.world_unity("Bucket", edge_on_bk)
    pose(K)
    front_tyre = P["wheelbase"] / 2 + WHEEL_R
    out["dump_clearance_reach_45"] = dict(clearance_m=round(edge45.y, 3), spec=P["dump_clear_45"],
                                          reach_m=round(edge45.z - (P["wheelbase"] / 2 + 0.0), 3),
                                          note="reach measured from the front axle; spec measures from the front of "
                                               "the tyres/frame, so only the clearance is compared",
                                          ok=abs(edge45.y - P["dump_clear_45"]) < 0.30)
    # hydraulics
    hy_ok = True; hy = {}
    for nm in res:
        for h, v in res[nm]["hyd"].items():
            hy.setdefault(h, {})[nm] = v
            hy_ok &= v["aim_err_deg"] < 0.5
    smax = math.radians(P["steer_max_deg"])
    for a, nm in ((-smax, "steer_left"), (0.0, "straight"), (smax, "steer_right")):
        pose(K, steer=a)
        for h in ("SteerLeft", "SteerRight"):
            c = K.hydraulic_check(info["hydraulics"][h]); hy.setdefault(h, {})[nm] = c
            hy_ok &= c["aim_err_deg"] < 0.5
    pose(K)
    ratios = {h: v["stroke_ratio"] for h, v in info["hydraulics"].items()}
    overl = {h: v["overlap_at_max"] for h, v in info["hydraulics"].items()}
    out["hydraulics_feasible"] = dict(stroke_ratio=ratios, overlap_at_max_m=overl, per_pose=hy,
                                      ok=bool(hy_ok) and all(r < 2.0 for r in ratios.values())
                                      and all(o > 0.05 for o in overl.values()))
    # z-bar: one branch over the whole range, monotonic, fit error
    xs = [math.radians(-45 + i) for i in range(186)]
    bc = [bellcrank_x(x) for x in xs]
    mono = all((b1 - b0) * (bc[1] - bc[0]) > 0 for b0, b1 in zip(bc, bc[1:]))
    tr = []
    for x in xs:
        _, Dd, Kk = zbar_solve(x)
        u, w = _sub(Kk, Dd), _sub(Dd, E)
        tr.append(deg(math.acos(min(1, abs(u[0] * w[0] + u[1] * w[1]) / (math.hypot(*u) * math.hypot(*w))))))
    out["zbar_single_branch"] = dict(range_deg=[-45, 140], monotonic=mono, min_transmission_deg=round(min(tr), 2),
                                     fit_max_err_deg=info["zbar"]["fit"]["max_err_deg"],
                                     ok=mono and min(tr) > 20 and info["zbar"]["fit"]["max_err_deg"] < 1.0)
    # articulation: the hitch axis stays put, steer sign
    hd = []
    for a in (-smax, 0.0, smax):
        pose(K, steer=a)
        hd.append(max((K.world_unity("RearFrame", hitch_r) - K.world_unity("FrontFrame", hitch_f)).length,
                      abs(K.world_unity("FrontFrame", hitch_f_top).x) + abs(K.world_unity("FrontFrame", hitch_f_top).z)))
    pose(K, steer=smax)
    fax = K.world_unity("WheelFrontRight")
    pose(K)
    out["articulation_axis_fixed"] = dict(hitch_drift_m=round(max(hd), 6), steer_plus_front_axle_x=round(fax.x, 3),
                                          ok=max(hd) < 1e-4 and fax.x > 0.5,
                                          note="+steer must move the front axle to +X (turn right)")
    # wheels touch the ground (LOD0 tyre lowest point), in rest / carry / full steer
    gc = {}
    for nm, kw in (("rest", {}), ("carry", poses["carry"]), ("steer_left", dict(steer=-smax)),
                   ("steer_right", dict(steer=smax)), ("rolled", dict(wheels=(0.37, 0.91, 1.3, 0.05)))):
        pose(K, **kw)
        for w in ("WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight"):
            v, _ = _world_tris(K, [w])
            gc.setdefault(w, {})[nm] = round(min(K.to_unity(p)[1] for p in v), 4)
    pose(K)
    lo = min(min(d.values()) for d in gc.values()); hi = max(max(d.values()) for d in gc.values())
    static = [v for d in gc.values() for k, v in d.items() if k != "rolled"]
    rolled = [d["rolled"] for d in gc.values()]
    out["wheels_on_ground"] = dict(min_y=gc, worst_low=lo, worst_high=hi,
                                   ok=lo > -0.01 and max(static) < 0.01 and max(rolled) < 0.03,
                                   note="rolled: lowest point is a tread block or the carcass between blocks")
    # interference: front assembly vs rear assembly at full steer; bucket/boom vs front wheels/frame
    front = [n for n in _subtree(K, "FrontFrame") if not n.startswith("Steer")]
    rear = [n for n in _subtree(K, "RearFrame", stop=("FrontFrame",)) if not n.startswith("Steer")]
    near_hitch = lambda c: math.hypot(c[0], c[2]) < 0.30
    col = {}
    for nm, kw in (("steer_left", dict(steer=-smax)), ("steer_right", dict(steer=smax)),
                   ("steer_right_carry", dict(poses["carry"], steer=smax)),
                   ("steer_left_carry", dict(poses["carry"], steer=-smax))):
        pose(K, **kw)
        col[nm] = _overlap(K, front, rear, near_hitch)
    for nm in ("ground_rb40", "carry", "max_dump52", "max_rb60", "dig5"):
        pose(K, **poses[nm])
        col["bucket_vs_frame_wheels_" + nm] = _overlap(K, ["Bucket", "BucketLink"],
                                                     ["FrontFrame", "WheelFrontLeft", "WheelFrontRight"])
        col["bellcrank_vs_frame_" + nm] = _overlap(K, ["Bellcrank"], ["FrontFrame", "WheelFrontLeft", "WheelFrontRight"])
    pose(K)
    out["no_interference"] = dict(overlapping_triangle_pairs=col, ok=all(v == 0 for v in col.values()),
                                  note="hitch pin/boss region (r < 0.30 m) and steering cylinders excluded")
    # bbox vs spec (rest pose, bucket on the ground)
    from mathutils import Vector
    pts = []
    for o in K.lod_objects("LOD0"):
        pts += [Vector(K.to_unity(o.matrix_world @ v.co)) for v in o.data.vertices]
    mn = [min(p[i] for p in pts) for i in range(3)]; mx = [max(p[i] for p in pts) for i in range(3)]
    L, W, H = mx[2] - mn[2], mx[0] - mn[0], mx[1] - mn[1]
    out["bbox_vs_spec"] = dict(length=round(L, 3), spec_length=P["overall_len_teeth"], width=round(W, 3),
                               spec_width=P["bucket_w"], height=round(H, 3), spec_height=P["rops_h"],
                               rear_z=round(mn[2], 3), front_z=round(mx[2], 3), min_y=round(mn[1], 4),
                               ok=abs(L - P["overall_len_teeth"]) < 0.15 and abs(W - P["bucket_w"]) < 0.06
                               and abs(H - P["rops_h"]) < 0.08 and abs(mn[1]) < 0.01)
    return out


def shots(K, info):
    p = _I["poses"]
    c, md, mr = p["carry"], p["max_dump52"], p["max_rb60"]
    smax = math.radians(P["steer_max_deg"])
    side_ortho = ((40, 2.6, 0.55), (0, 2.6, 0.55), 50, 12.8)     # 100 px/m at 1280 x 720, from +X
    front_ortho = ((0, 2.0, 40), (0, 2.0, 0), 50, 12.8)
    return [
        ("loader_01_3q_front_travel", dict(c, wheels=(0.3, 0.3, 0.3, 0.3)), ((8.5, 4.2, 10.5), (0, 1.4, 0.6), 45), 0),
        ("loader_02_side_ortho", {}, side_ortho, 0),
        ("loader_03_front_ortho", {}, front_ortho, 0),
        ("loader_04_raised_dump", md, ((9.5, 4.5, 9.0), (0, 2.4, 1.2), 42), 0),
        ("loader_05_side_dump_ortho", md, side_ortho, 0),
        ("loader_06_steered_top", dict(c, steer=smax), ((7.0, 13.0, 4.0), (0, 0.8, 0.3), 40), 0),
        ("loader_07_steered_3q", dict(c, steer=-smax), ((-8.5, 4.0, 9.5), (0, 1.3, 0.5), 45), 0),
        ("loader_08_3q_rear", {}, ((-8.5, 3.8, -9.0), (0, 1.4, -0.6), 45), 0),
        ("loader_09_raised_rackback", mr, ((-12.5, 4.0, 10.0), (0, 2.9, 1.2), 38), 0),
        ("loader_10_demo_distance", c, ((70, 45, 80), (0, 0, 0), 35), 0),
        ("loader_11_demo_mid", c, ((35, 22, 40), (0, 0, 0), 35), 0),
        ("loader_12_lod1_3q", c, ((8.5, 4.2, 10.5), (0, 1.4, 0.6), 45), 1),
    ]
