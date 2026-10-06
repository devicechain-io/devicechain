# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Stylized, unbranded crawler dozer (medium size class) for Sitepulse.

Coordinates: Unity metres (X right, Y up, Z forward, origin on the ground under the track
footprint centre). [spec:n] = numbered dimension n on the reference spec drawing
(a published manufacturer spec sheet for this size class; not committed), [spec:meas] = measured off it, [style] = choice.

RIG (Unity localEulerAngles.x == the Blender X values below, same sign):
  BladeArmPivot  push-arm trunnion. x < 0 raises the blade, x > 0 digs.
  Blade          blade pitch pivot at the push-arm/blade pin. COUNTER-ROTATION RULE:
                     Blade.x = -BLADE_PITCH_COMP * BladeArmPivot.x
                 With 0.85 the moldboard keeps 85 % of its rest attitude: it tips back only
                 15 % of the arm angle (about 2.7 deg at full raise) instead of the full ~18 deg.
                 The pitch cylinders (PitchCylinder*/PitchRod*) visibly take up the difference.
  Ripper         lower parallelogram link. x < 0 lowers the shank.
  RipperCarriage shank carriage. PARALLELOGRAM RULE: RipperCarriage.x = -Ripper.x (exact),
                 keeps the shank vertical; RipperUpperLink is aimed at RipperUpperPin.
  Left/RightSprocket, Left/RightIdler: x += degrees(d / wheel_r) for travelled distance d.
  M_Track tread:  mainTextureOffset.x = -d * tread_u_per_m (per side).
  Lift/Pitch/Ripper Cylinder+Rod: aim local +Y (Unity) at the matching *Mount node.
"""
import math

NAME = "dozer"
LOD1_RATIO = 0.45
BLADE_PITCH_COMP = 0.85

P = dict(
    # undercarriage
    track_gauge=2.080,          # [spec:1]
    shoe_width=0.610,           # [spec:2]  -> width over tracks 2.69 [spec:3]
    track_ground_len=3.247,     # [spec:4]
    wheel_r=0.36,               # [spec:meas] sprocket/idler pitch radius
    belt_t=0.05,                # [style] shoe plate thickness
    grouser_h=0.04,             # [style] grouser bar height (silhouette)  -> track top 0.90 [spec:meas 0.86-0.9]
    track_pitch=0.2028,         # [spec]
    ground_clearance=0.422,     # [spec]
    # body
    deck_y=1.00, hood_top_rear=2.30, hood_top_front=2.20, hood_half_w=0.62,
    grille_front_z=1.98, cab_front_z=0.30, cab_rear_z=-1.45, cab_half_w=0.75,   # 0.80 -> 0.75: front sheet cab/blade ~0.45
    cab_floor_y=1.45, cab_roof_y=3.08, rear_z=-2.11, stack_z=1.10, stack_top=3.05,
    # blade (SU class)
    blade_half_w=3.312 / 2,     # [spec: SU blade]
    blade_h=1.312,              # [spec:8]
    blade_front_z=3.55,         # overall length 5.662 [spec:12]
    blade_lift=1.131,           # [spec:10]
    blade_dig=0.698,            # [spec:9]
    hinge=(1.48, 0.55, -0.10),  # push-arm trunnion
    blade_pin=(0.0, 0.465, 3.10),   # push-arm -> blade pitch pin [style]
    # ripper: parallelogram, 3 shanks
    ripper_link=1.05, ripper_link_rest_deg=25.0, ripper_link_gap=0.50,
    ripper_tip_clear=0.664,     # [spec ripper:2] raised clearance under tip
    ripper_pen=0.35,            # [style] penetration when fully lowered
)

_I = {}   # rig numbers filled by build()


def pose(K, arm=0.0, ripper=0.0, track_m=(0.0, 0.0)):
    """arm / ripper in radians (Blender X == Unity localEulerAngles.x); track_m per side."""
    K.rot_x("BladeArmPivot", arm)
    K.rot_x("Blade", -BLADE_PITCH_COMP * arm)
    K.rot_x("Ripper", ripper)
    K.rot_x("RipperCarriage", -ripper)
    for side, d in zip(("Left", "Right"), track_m):
        K.rot_x(side + "Sprocket", d / P["wheel_r"])
        K.rot_x(side + "Idler", d / P["wheel_r"])
    try:
        m = K.mat("track").node_tree.nodes["TreadScroll"]
        m.inputs["Location"].default_value[0] = -track_m[0] * _I.get("tread_u_per_m", 1 / P["track_pitch"])
    except KeyError:
        pass
    K.update()


def build(K):
    Piece, node, attach = K.Piece, K.node, K.attach
    TX = P["track_gauge"] / 2
    D, HW, gz = P["deck_y"], P["hood_half_w"], P["grille_front_z"]
    cz0, cz1, CW = P["cab_rear_z"], P["cab_front_z"], P["cab_half_w"]
    fy, ry, rz = P["cab_floor_y"], P["cab_roof_y"], P["rear_z"]
    bz, H, BW = P["blade_front_z"], P["blade_h"], P["blade_half_w"]
    hx, hy, hz = P["hinge"]
    FX = TX + P["shoe_width"] / 2 + 0.04          # fender outer x

    # ---------------------------------------------------------------- Root / Chassis
    node("Root", None, (0, 0, 0))
    node("Chassis", "Root", (0, 0, 0))
    c = Piece()
    c.box((-0.72, P["ground_clearance"], -1.95), (0.72, D, 2.0), "paint_dark")               # belly frame
    for s in (-1, 1):                                                                          # fenders
        c.prism([(rz, D - 0.02), (1.70, D - 0.02), (1.95, D - 0.25), (1.95, D - 0.17),
                 (1.72, D + 0.06), (rz, D + 0.06)], "x", s * 0.70, s * FX, "paint", 0.02)
    c.prism([(cz1 - 0.05, D), (gz, D), (gz, P["hood_top_front"] - 0.12), (gz - 0.15, P["hood_top_front"]),
             (cz1 - 0.05, P["hood_top_rear"])], "x", -HW, HW, "paint", 0.05)                   # hood
    c.box((-HW - 0.10, D, 0.45), (HW + 0.10, 1.62, 1.90), "paint", 0.04)                       # engine side doors
    # radiator guard: dark core, yellow posts + bars
    c.box((-0.50, D + 0.08, gz - 0.02), (0.50, P["hood_top_front"] - 0.16, gz + 0.06), "steel", 0.015)
    for s in (-1, 1):
        c.box((s * 0.49, D + 0.02, gz - 0.02), (s * 0.60, 2.10, gz + 0.12), "paint", 0.02)
    for k in range(5):
        y = D + 0.20 + k * 0.17
        c.box((-0.52, y, gz + 0.04), (0.52, y + 0.055, gz + 0.10), "paint", 0.01)
    c.box((-0.70, 0.48, gz - 0.10), (0.70, D + 0.05, gz + 0.16), "paint_dark")                # pull frame / nose
    for s in (-1, 1):                                                                          # lift-cyl brackets
        c.box((s * 0.68, 1.80, 1.30), (s * 0.90, 2.06, 1.52), "paint_dark", 0.015)
    c.box((-1.22, D, rz + 0.08), (1.22, 2.02, cz0 + 0.05), "paint", 0.05)                      # rear tanks
    c.box((-0.95, D, cz0), (0.95, fy, cz1 - 0.02), "paint", 0.04)                              # cab plinth
    c.box((-0.62, 0.46, rz - 0.10), (0.62, 1.22, rz + 0.30), "paint_dark", 0.03)              # ripper mount frame
    c.box((-0.11, 1.68, rz - 0.10), (0.11, 1.92, rz + 0.10), "paint_dark", 0.015)             # ripper cyl bracket
    st = P["stack_z"]
    c.cyl((-0.32, P["hood_top_rear"] - 0.1, st), (-0.32, P["stack_top"], st), 0.075, "steel", 10)
    c.cyl((-0.32, P["stack_top"] - 0.02, st), (-0.32, P["stack_top"] + 0.03, st + 0.02), 0.095, "steel", 10)
    c.cyl((0.30, P["hood_top_rear"] - 0.1, st + 0.15), (0.30, P["hood_top_rear"] + 0.30, st + 0.15), 0.11, "paint_dark", 10)
    c.cyl((0.30, P["hood_top_rear"] + 0.30, st + 0.15), (0.30, P["hood_top_rear"] + 0.36, st + 0.15), 0.125, "steel", 10)
    with c.detail():
        # --- panel seams + louvres (engine doors), hood upper seams
        for s in (-1, 1):
            xo = s * (HW + 0.10)
            for z in (0.95, 1.33):
                c.strip((xo, D + 0.08, z - 0.009), (xo + s * 0.006, 1.57, z + 0.009))
            for k in range(4):
                y = 1.17 + k * 0.095
                c.strip((xo, y, 1.42), (xo + s * 0.014, y + 0.042, 1.80))
            for z in (0.85, 1.45):
                c.strip((s * HW, 1.64, z - 0.009), (s * (HW + 0.006), 2.16, z + 0.009))
            c.strip((s * FX, D - 0.015, rz + 0.02), (s * (FX + 0.008), D + 0.055, 1.70), "paint_dark")   # fender edge
            c.strip((s * 0.98, D + 0.06, -1.32), (s * 1.31, D + 0.072, 0.22), "paint_dark")             # walkway
            # rear tank side seam + rear face seams
            c.strip((s * 1.22, 1.62, rz + 0.14), (s * 1.226, 1.638, cz0 - 0.02))
            c.strip((s * 0.45 - 0.009, D + 0.10, rz + 0.074), (s * 0.45 + 0.009, 1.96, rz + 0.08))
            # rear light cluster: red tail over white reverse/work lamp
            c.lamp((s * 0.92, 1.84, rz + 0.08), "-z", 0.20, 0.11, 0.06, "tail")
            c.lamp((s * 0.92, 1.69, rz + 0.08), "-z", 0.20, 0.11, 0.06, "lamp")
            # front work lights on the radiator-guard posts
            c.lamp((s * 0.545, 2.17, gz + 0.02), "+z", 0.17, 0.12, 0.09, "lamp")
            c.box((s * 0.52, 2.08, gz + 0.03), (s * 0.57, 2.12, gz + 0.09), "steel", 0)
            # handrail beside the cab (+ mid rail), entry step on the track frame
            x = s * 1.30
            c.tube([(x, D + 0.07, -1.30), (x, 1.93, -1.30), (x, 1.93, 0.15), (x, D + 0.07, 0.15)], 0.022)
            c.tube([(x, D + 0.07, -0.55), (x, 1.93, -0.55)], 0.02)
            c.tube([(x, 1.50, -1.30), (x, 1.50, 0.15)], 0.016)
            c.box((s * 1.25, 0.57, -0.98), (s * 1.48, 0.61, -0.66), "steel", 0.01)
            c.box((s * 1.24, 0.42, -0.86), (s * 1.28, 0.61, -0.78), "steel", 0)
            c.tube([(s * (CW + 0.06), fy + 0.10, -1.20), (s * (CW + 0.06), fy + 1.05, -1.20)], 0.018)  # grab handle
        c.cyl((-0.32, 2.52, st), (-0.32, 2.80, st), 0.088, "worn", 10)                         # stack heat shield
        c.cyl((0.85, 2.02, -1.85), (0.85, 2.07, -1.85), 0.07, "worn", 10)                      # fuel cap
        c.strip((-0.50, 1.62, 0.52), (0.50, 1.625, 1.84), "paint_dark")                        # hood/door split line
    attach("Chassis", c)
    node("ExhaustTip", "Chassis", (-0.32, P["stack_top"] + 0.03, st + 0.02))                   # particle point

    # ---------------------------------------------------------------- Cab
    node("Cab", "Chassis", (0, fy, (cz0 + cz1) / 2))
    k = Piece()
    k.box((-CW, fy, cz0), (CW, fy + 0.45, cz1), "paint", 0.03)                                 # lower body
    k.prism([(-CW + 0.03, fy + 0.45), (CW - 0.03, fy + 0.45), (CW - 0.08, ry - 0.12), (-CW + 0.08, ry - 0.12)],
            "z", cz0 + 0.03, cz1 - 0.03, "glass", 0.0)                                          # glass volume
    for sx in (-1, 1):
        for z in (cz0, cz1):
            k.box((sx * (CW - 0.02) - 0.055, fy + 0.4, z - 0.055), (sx * (CW - 0.02) + 0.055, ry - 0.08, z + 0.055),
                  "steel", 0.012)                                                               # ROPS posts
        k.box((sx * (CW - 0.11), fy + 0.45, -0.42), (sx * (CW + 0.005), ry - 0.12, -0.35), "steel", 0.008)  # door mullion
    k.box((-CW - 0.01, fy + 0.42, cz0 - 0.01), (CW + 0.01, fy + 0.50, cz1 + 0.01), "steel", 0.01)  # sill band
    k.box((-CW + 0.03, ry - 0.22, cz0 + 0.0), (CW - 0.03, ry - 0.12, cz1 - 0.0), "steel", 0.0)  # header band
    k.box((-0.035, fy + 0.45, cz0 - 0.005), (0.035, ry - 0.12, cz0 + 0.06), "steel", 0.0)      # rear mullion
    k.box((-CW - 0.03, ry - 0.15, cz0 - 0.06), (CW + 0.03, ry - 0.10, cz1 + 0.08), "steel", 0.0)  # gutter
    k.box((-CW - 0.06, ry - 0.10, cz0 - 0.08), (CW + 0.06, ry, cz1 + 0.10), "paint", 0.03)    # roof
    with k.detail():
        for sx in (-1, 1):
            # mirrors on the front posts, looking back
            xm = sx * (CW + 0.25)
            k.tube([(sx * (CW + 0.03), ry - 0.42, cz1 + 0.04), (xm, ry - 0.40, cz1 + 0.06)], 0.014)
            k.box((xm - 0.06, ry - 0.62, cz1 + 0.035), (xm + 0.06, ry - 0.36, cz1 + 0.09), "steel", 0.01)
            k.box((xm - 0.05, ry - 0.61, cz1 + 0.028), (xm + 0.05, ry - 0.37, cz1 + 0.036), "worn", 0)
            # roof work lights front + rear on short brackets
            k.box((sx * 0.55 - 0.03, ry, cz1 - 0.04), (sx * 0.55 + 0.03, ry + 0.05, cz1 + 0.0), "steel", 0)
            k.lamp((sx * 0.55, ry + 0.10, cz1 - 0.04), "+z", 0.17, 0.11, 0.09, "lamp")
            k.box((sx * 0.55 - 0.03, ry, cz0 + 0.0), (sx * 0.55 + 0.03, ry + 0.05, cz0 + 0.04), "steel", 0)
            k.lamp((sx * 0.55, ry + 0.10, cz0 + 0.04), "-z", 0.17, 0.11, 0.09, "lamp")
            k.box((sx * (CW + 0.003), fy + 0.60, -0.62), (sx * (CW + 0.02), fy + 0.63, -0.48), "worn", 0)  # door handle
        k.tube([(0.05, fy + 0.53, cz1 - 0.015), (-0.32, fy + 1.06, cz1 - 0.015)], 0.011)           # wiper
    attach("Cab", k)
    bxz = cz0 + 0.25
    node("Beacon", "Cab", (0, ry, bxz))
    b = Piece()
    b.cyl((0, ry, bxz), (0, ry + 0.035, bxz), 0.085, "steel", 10)
    b.cyl((0, ry + 0.035, bxz), (0, ry + 0.122, bxz), 0.068, "beacon", 10, r2=0.06)
    b.cyl((0, ry + 0.122, bxz), (0, ry + 0.142, bxz), 0.06, "beacon", 10, r2=0.035)            # dome top -> 3.222 [spec:5]
    attach("Beacon", b)

    # ---------------------------------------------------------------- Tracks (kit builder)
    tp = dict(wheel_r=P["wheel_r"], belt_t=P["belt_t"], grouser_h=P["grouser_h"], shoe_w=P["shoe_width"],
              ground_len=P["track_ground_len"], pitch=P["track_pitch"], arc_seg=10, rollers=6)
    tracks = {side: K.track_assembly(side, s * TX, tp) for side, s in (("Left", -1), ("Right", 1))}
    _I["tread_u_per_m"] = tracks["Left"]["tread_u_per_m"]

    # ---------------------------------------------------------------- Blade
    node("BladeArmPivot", "Chassis", (0, hy, hz))
    a = Piece()
    pin = P["blade_pin"]
    for s in (-1, 1):
        x = s * hx
        a.prism([(hz - 0.10, hy - 0.10), (pin[2] + 0.05, pin[1] - 0.13), (pin[2] + 0.05, pin[1] + 0.13),
                 (hz + 0.05, hy + 0.12)], "x", x - 0.08, x + 0.08, "paint", 0.025)              # push arm
        a.cyl((x - 0.13, hy, hz), (x + 0.13, hy, hz), 0.12, "steel", 10)                       # trunnion
        a.box((x - 0.06, 0.60, 1.86), (x + 0.06, 0.80, 2.04), "paint_dark", 0.01)              # pitch-cyl lug
        with a.detail():
            a.cyl((x - 0.11, pin[1], pin[2]), (x + 0.11, pin[1], pin[2]), 0.06, "worn", 8)    # blade pin
            a.strip((x + s * 0.080, 0.42, 0.6), (x + s * 0.086, 0.47, 2.6), "paint_dark")       # arm stripe (outer face)
    attach("BladeArmPivot", a)

    node("Blade", "BladeArmPivot", pin)
    bl = Piece()
    front = [(bz, 0.0), (bz - 0.13, 0.25), (bz - 0.19, 0.55), (bz - 0.17, 0.85), (bz - 0.09, 1.10), (bz + 0.03, H)]
    mold = front + [(bz - 0.10, H + 0.02), (bz - 0.37, 1.10), (bz - 0.43, 0.60), (bz - 0.35, 0.05)]
    bl.prism(mold, "x", -BW, BW, "paint", 0.02)
    bl.box((-BW + 0.02, 0.0, bz - 0.10), (BW - 0.02, 0.15, bz + 0.03), "worn", 0.01)           # cutting edge
    for s in (-1, 1):
        bl.box((s * BW - 0.06, 0.0, bz - 0.12), (s * BW + 0.03, 0.20, bz + 0.05), "worn", 0.01)  # end bits
        bl.box((s * BW - 0.05, 0.05, bz - 0.45), (s * BW + 0.03, H - 0.05, bz - 0.12), "paint", 0.015)  # side plate
        bl.box((s * 0.78 - 0.09, 0.85, bz - 0.55), (s * 0.78 + 0.09, 1.15, bz - 0.38), "paint_dark", 0.015)  # lift lug
        bl.box((s * hx - 0.07, 0.95, bz - 0.58), (s * hx + 0.07, 1.15, bz - 0.38), "paint_dark", 0.015)     # pitch lug
        bl.box((s * hx - 0.10, 0.36, bz - 0.56), (s * hx + 0.10, 0.58, bz - 0.38), "paint_dark", 0.015)     # arm clevis
    bl.box((-BW + 0.15, 0.30, bz - 0.52), (BW - 0.15, 0.62, bz - 0.38), "paint", 0.02)         # back beam
    # moldboard top edge: a forward-rolled spill lip, proud of the face (catches a highlight line)
    bl.prism([(bz - 0.13, H - 0.07), (bz + 0.05, H - 0.04), (bz + 0.07, H + 0.01), (bz + 0.04, H + 0.045),
              (bz - 0.13, H + 0.04)], "x", -BW + 0.02, BW - 0.02, "paint", 0.01)
    # wear band: charcoal liner plate on the lower moldboard, following the face curve 0.17-0.38 m
    def face_z(y):
        for (za, ya), (zb, yb) in zip(front, front[1:]):
            if ya <= y <= yb:
                return za + (zb - za) * (y - ya) / (yb - ya)
    wy = (0.17, 0.25, 0.38)
    bl.prism([(face_z(y) + 0.014, y) for y in wy] + [(face_z(y) - 0.02, y) for y in reversed(wy)],
             "x", -BW + 0.07, BW - 0.07, "paint_dark", 0.0)
    with bl.detail():
        for x in (-1.12, -0.40, 0.40, 1.12):                                                   # back stiffeners
            bl.box((x - 0.03, 0.62, bz - 0.47), (x + 0.03, 1.12, bz - 0.36), "paint", 0.008)
        for k in range(7):                                                                     # liner bolt heads
            xb = -BW + 0.35 + k * (2 * BW - 0.70) / 6
            bl.box((xb - 0.025, 0.265, face_z(0.28) + 0.01), (xb + 0.025, 0.305, face_z(0.28) + 0.03), "worn", 0)
    attach("Blade", bl)

    # solve the arm angles that give the spec lift height / dig depth AT THE CUTTING EDGE
    edge_local = K.capture("Blade", (0, 0, bz))
    def edge_y(arm):
        pose(K, arm=arm)
        return K.world_unity("Blade", edge_local).y
    y0 = edge_y(0.0)
    RAISE = K.solve(edge_y, y0 + P["blade_lift"], -1.0, 0.0)
    DIG = K.solve(edge_y, y0 - P["blade_dig"], 0.0, 0.8)
    pose(K)
    blade_poses = [lambda: pose(K, arm=DIG), lambda: pose(K), lambda: pose(K, arm=RAISE)]
    hyd = {}
    for side, s in (("Left", -1), ("Right", 1)):
        hyd["Lift" + side] = K.hydraulic("Lift", side, "Chassis", (s * 0.80, 1.95, 1.40), "Blade",
                                         (s * 0.78, 1.00, bz - 0.47), 0.085, 0.045, blade_poses, lambda: pose(K))
        hyd["Pitch" + side] = K.hydraulic("Pitch", side, "BladeArmPivot", (s * hx, 0.72, 1.95), "Blade",
                                          (s * hx, 1.06, bz - 0.50), 0.07, 0.038, blade_poses, lambda: pose(K))

    # ---------------------------------------------------------------- Ripper (parallelogram)
    L, al, gap = P["ripper_link"], math.radians(P["ripper_link_rest_deg"]), P["ripper_link_gap"]
    P1 = (0, 0.62, rz - 0.05)
    Q1 = (0, P1[1] + L * math.sin(al), P1[2] - L * math.cos(al))
    P2, Q2 = (0, P1[1] + gap, P1[2]), (0, Q1[1] + gap, Q1[2])
    node("Ripper", "Chassis", P1)
    rp = Piece()
    dirv = (0, math.sin(al), -math.cos(al))
    for s in (-1, 1):
        mid = (s * 0.45, (P1[1] + Q1[1]) / 2, (P1[2] + Q1[2]) / 2)
        rp.obox(mid, (1, 0, 0), dirv, 0.12, L + 0.10, 0.17, "paint")
        rp.cyl((s * 0.45 - 0.09, P1[1], P1[2]), (s * 0.45 + 0.09, P1[1], P1[2]), 0.08, "steel", 8)
    cb = (0, P1[1] + 0.72 * L * math.sin(al), P1[2] - 0.72 * L * math.cos(al))                # cylinder lug point
    rp.cyl((-0.45, cb[1], cb[2]), (0.45, cb[1], cb[2]), 0.07, "paint", 8)
    rp.box((-0.07, cb[1] - 0.02, cb[2] - 0.08), (0.07, cb[1] + 0.13, cb[2] + 0.08), "paint_dark", 0.01)
    attach("Ripper", rp)
    node("RipperCarriage", "Ripper", Q1)
    rc = Piece()
    qy, qz, tip = Q1[1], Q1[2], P["ripper_tip_clear"]
    rc.box((-0.66, qy - 0.08, qz - 0.15), (0.66, qy + 0.58, qz + 0.10), "paint", 0.03)         # beam
    for s in (-1, 1):
        rc.cyl((s * 0.45 - 0.10, qy, qz), (s * 0.45 + 0.10, qy, qz), 0.07, "steel", 8)
        rc.box((s * 0.30 - 0.06, qy + gap - 0.08, qz - 0.02), (s * 0.30 + 0.06, qy + gap + 0.08, qz + 0.13), "paint_dark", 0.01)
    for x in (-0.50, 0.0, 0.50):                                                               # 3 shanks + teeth
        rc.prism([(qz + 0.07, qy + 0.10), (qz - 0.09, qy + 0.10), (qz - 0.09, tip + 0.32), (qz - 0.02, tip + 0.10),
                  (qz + 0.07, tip + 0.07), (qz + 0.07, tip + 0.30)], "x", x - 0.045, x + 0.045, "paint_dark", 0.01)
        rc.prism([(qz + 0.17, tip), (qz + 0.05, tip + 0.03), (qz - 0.02, tip + 0.13), (qz + 0.07, tip + 0.14)],
                 "x", x - 0.04, x + 0.04, "worn", 0.0)
    attach("RipperCarriage", rc)
    node("RipperUpperPin", "RipperCarriage", Q2, size=0.05)
    node("RipperUpperLink", "Chassis", P2)
    ul = K.NODES["RipperUpperLink"]
    ul.rotation_mode = "QUATERNION"; ul.rotation_quaternion = K.aim_quat("RipperUpperLink", "RipperUpperPin")
    con = ul.constraints.new("DAMPED_TRACK"); con.target = K.NODES["RipperUpperPin"]; con.track_axis = "TRACK_Z"
    lk = Piece()
    for s in (-1, 1):
        lk.box((s * 0.30 - 0.05, -0.06, -0.06), (s * 0.30 + 0.05, 0.06, L + 0.06), "paint", 0.015)  # Blender-local
        lk.cyl((s * 0.30 - 0.08, 0, 0), (s * 0.30 + 0.08, 0, 0), 0.065, "steel", 8)
    attach("RipperUpperLink", lk, local_blender=True)
    K.update()

    tip_local = K.capture("RipperCarriage", (0, tip, qz + 0.17))
    def tip_y(r):
        pose(K, ripper=r)
        return K.world_unity("RipperCarriage", tip_local).y
    LOWER = K.solve(tip_y, -P["ripper_pen"], -1.4, 0.0)
    pose(K)
    hyd["Ripper"] = K.hydraulic("Ripper", "", "Chassis", (0, 1.80, rz - 0.03), "Ripper", cb, 0.085, 0.045,
                                [lambda: pose(K), lambda: pose(K, ripper=LOWER)], lambda: pose(K))
    _I.update(RAISE=RAISE, DIG=DIG, LOWER=LOWER, edge_local=edge_local, tip_local=tip_local,
              pin_world=(hx, hy, hz), P2=P2, Q2=Q2)
    return dict(
        blade=dict(raise_rad=round(RAISE, 5), dig_rad=round(DIG, 5),
                   unity_localEulerX_deg=dict(BladeArmPivot=[round(math.degrees(RAISE), 2), 0, round(math.degrees(DIG), 2)],
                                              Blade_rule=f"Blade.x = -{BLADE_PITCH_COMP} * BladeArmPivot.x")),
        ripper=dict(lower_rad=round(LOWER, 5), unity_localEulerX_deg=[0, round(math.degrees(LOWER), 2)],
                    rule="RipperCarriage.x = -Ripper.x"),
        tracks=tracks, hydraulics=hyd,
        keep_full_lod1=[n for n in K.NODES if "Cylinder" in n or "Rod" in n] + ["Beacon", "RipperUpperLink"],
    )


def checks(K, info):
    """Re-verify the rig invariants on the built scene. Each entry has ok: bool."""
    out = {}
    RAISE, DIG, LOWER = _I["RAISE"], _I["DIG"], _I["LOWER"]
    hx, hy, hz = _I["pin_world"]
    pin_local = K.capture("BladeArmPivot", (hx, hy, hz))
    res = {}
    for nm, a in (("dig", DIG), ("rest", 0.0), ("raised", RAISE)):
        pose(K, arm=a)
        res[nm] = dict(pin=K.world_unity("BladeArmPivot", pin_local), edge=K.world_unity("Blade", _I["edge_local"]),
                       moldboard_pitch_deg=K.node_pitch_deg("Blade"),
                       hyd={h: K.hydraulic_check(v) for h, v in info["hydraulics"].items() if not h.startswith("Ripper")})
    pose(K)
    drift = max((res[n]["pin"] - res["rest"]["pin"]).length for n in res)
    out["hinge_fixed"] = dict(pin_max_drift_m=round(drift, 6), ok=drift < 1e-4)
    lift = res["raised"]["edge"].y - res["rest"]["edge"].y
    dig = res["rest"]["edge"].y - res["dig"]["edge"].y
    out["blade_spec_lift_dig"] = dict(lift_m=round(lift, 4), spec_lift=P["blade_lift"], dig_m=round(dig, 4),
                                      spec_dig=P["blade_dig"],
                                      ok=abs(lift - P["blade_lift"]) < 0.005 and abs(dig - P["blade_dig"]) < 0.005)
    pr, pz, pd = (res[n]["moldboard_pitch_deg"] for n in ("raised", "rest", "dig"))
    arm_deg = -math.degrees(RAISE)
    out["moldboard_near_upright"] = dict(arm_raise_deg=round(arm_deg, 2), pitch_change_raised_deg=round(pr - pz, 2),
                                         pitch_change_dig_deg=round(pd - pz, 2),
                                         would_be_without_comp_deg=round(arm_deg, 2),
                                         ok=abs(pr - pz) <= 5 and abs(pd - pz) <= 5)
    hy_ok = True; hy_rep = {}
    for nm in res:
        for h, v in res[nm]["hyd"].items():
            hy_rep.setdefault(h, {})[nm] = v
            hy_ok &= v["aim_err_deg"] < 0.5
    for h, v in info["hydraulics"].items():
        hy_ok &= v["overlap_at_max"] > 0.05
    # ripper
    rr = {}
    for nm, a in (("raised", 0.0), ("lowered", LOWER)):
        pose(K, ripper=a)
        q2 = K.world_unity("RipperUpperPin"); p2 = K.world_unity("RipperUpperLink")
        z = K.NODES["RipperUpperLink"].matrix_world.to_3x3()
        rr[nm] = dict(tip_y=round(K.world_unity("RipperCarriage", _I["tip_local"]).y, 4),
                      upper_link_len=round((q2 - p2).length, 6),
                      carriage_pitch_deg=round(K.node_pitch_deg("RipperCarriage"), 4),
                      cyl=K.hydraulic_check(info["hydraulics"]["Ripper"]))
        hy_rep.setdefault("Ripper", {})[nm] = rr[nm]["cyl"]
        hy_ok &= rr[nm]["cyl"]["aim_err_deg"] < 0.5
    pose(K)
    out["hydraulics_aim_and_overlap"] = dict(per_pose=hy_rep, ok=bool(hy_ok))
    dl = abs(rr["raised"]["upper_link_len"] - rr["lowered"]["upper_link_len"])
    out["ripper_parallelogram"] = dict(raised=rr["raised"], lowered=rr["lowered"], link_len_drift_m=round(dl, 6),
                                       ok=dl < 1e-4 and all(abs(r["carriage_pitch_deg"]) < 0.01 for r in rr.values())
                                       and abs(rr["raised"]["tip_y"] - P["ripper_tip_clear"]) < 0.005
                                       and abs(rr["lowered"]["tip_y"] + P["ripper_pen"]) < 0.005)
    # track speed constants, read back from the exported LOD0 belt UVs
    tr = {}
    tok = True
    for side, t in info["tracks"].items():
        o = K.NODES[t["node"]].children
        me = [c for c in o if c.name.endswith("_LOD0")][0]
        mw = me.matrix_world
        uv = me.data.uv_layers.active.data
        top = t["centre_y"] + t["outer_r"]
        samples = []
        for poly in me.data.polygons:
            if poly.material_index != [m.name for m in me.data.materials].index("M_Track"):
                continue
            for li in poly.loop_indices:
                v = K.to_unity(mw @ me.data.vertices[me.data.loops[li].vertex_index].co)
                if abs(v[1] - top) < 1e-3 and abs(v[2]) <= t["half_len"] + 1e-3:
                    samples.append((v[2], uv[li].uv[0]))
        samples.sort()
        (z0, u0), (z1, u1) = samples[0], samples[-1]
        slope = (u1 - u0) / (z1 - z0)
        tr[side] = dict(top_run_du_dz=round(slope, 5), tread_u_per_m=t["tread_u_per_m"],
                        pitch_eff_m=t["pitch_eff_m"], spec_pitch_m=P["track_pitch"],
                        sprocket_rad_per_m=t["sprocket_rad_per_m"],
                        sprocket_surface_speed_per_m=round(t["sprocket_rad_per_m"] * P["wheel_r"], 6),
                        links=t["links"], loop_len_m=t["loop_len_m"])
        tok &= abs(slope - t["tread_u_per_m"]) < 1e-3 and abs(t["pitch_eff_m"] - P["track_pitch"]) < 0.002
    out["track_speed_constants"] = dict(per_side=tr, ok=bool(tok),
                                        note="top run moves +Z at d/m when the machine moves +Z by d: U slope == tread_u_per_m")
    return out


def shots(K, info):
    """(file stem, pose kwargs, look args, lod) for build.py --render."""
    R, Dg, Lw = _I["RAISE"], _I["DIG"], _I["LOWER"]
    return [
        ("dozer_01_3q_front", {}, ((7.5, 4.2, 9.0), (0, 1.2, 0.4), 45), 0),
        ("dozer_02_side", {}, ((-17, 1.6, 0.15), (0, 1.5, 0.15), 70), 0),
        ("dozer_03_3q_rear", {}, ((-8.0, 3.6, -8.0), (0, 1.2, -0.4), 45), 0),
        ("dozer_04_blade_raised", dict(arm=R, track_m=(0.5, 0.5)), ((8.5, 3.0, 6.5), (0, 1.3, 0.8), 45), 0),
        ("dozer_05_side_raised", dict(arm=R), ((-17, 1.6, 0.4), (0, 1.4, 0.4), 70), 0),
        ("dozer_06_dig_ripper_down", dict(arm=Dg, ripper=Lw, track_m=(0.9, 0.2)), ((-8.5, 2.8, -7.0), (0, 0.9, -0.8), 45), 0),
        ("dozer_07_demo_distance", {}, ((70, 45, 80), (0, 0, 0), 35), 0),
        ("dozer_08_demo_mid", {}, ((35, 22, 40), (0, 0, 0), 35), 0),
        ("dozer_09_lod1_3q", {}, ((7.5, 4.2, 9.0), (0, 1.2, 0.4), 45), 1),
    ]
