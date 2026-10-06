# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Kit smoke test: one wheel node (tyre + tread blocks + rim) and one hydraulic, to exercise the
builders the dozer does not use. blender -b --python build.py -- kit_selftest"""
NAME = "kit_selftest"
LOD1_RATIO = 0.5
P = dict(wheel_r=0.80, wheel_w=0.55)


def pose(K, spin=0.0, arm=0.0):
    K.rot_x("WheelRight", spin)
    K.rot_x("Arm", arm)
    K.update()


def build(K):
    K.node("Root", None, (0, 0, 0))
    K.node("Base", "Root", (0, 0, 0))
    b = K.Piece(); b.box((-0.2, 0.0, -1.5), (0.6, 0.4, 1.5), "paint_dark"); K.attach("Base", b)
    K.node("WheelRight", "Base", (1.0, P["wheel_r"], 0))
    w = K.Piece(); K.wheel_piece(w, (1.0, P["wheel_r"], 0), P["wheel_r"], P["wheel_w"], +1); K.attach("WheelRight", w)
    K.node("Arm", "Base", (0, 0.4, -1.2))
    a = K.Piece(); a.box((-0.1, 0.35, -1.25), (0.1, 0.5, 0.8), "paint"); K.attach("Arm", a)
    h = K.hydraulic("Arm", "", "Base", (0, 0.4, 1.2), "Arm", (0, 0.5, 0.6), 0.07, 0.04,
                    [lambda: pose(K, arm=-0.5), lambda: pose(K)], lambda: pose(K))
    return dict(hydraulics={"Arm": h}, keep_full_lod1=["ArmCylinder", "ArmRod"])


def checks(K, info):
    pose(K, arm=-0.5); c = K.hydraulic_check(info["hydraulics"]["Arm"]); pose(K)
    return {"hyd_aim": dict(c, ok=c["aim_err_deg"] < 0.5)}


def shots(K, info):
    return [("kit_selftest_wheel", dict(arm=-0.4), ((4.5, 2.0, 2.5), (0.8, 0.8, 0), 45), 0)]
