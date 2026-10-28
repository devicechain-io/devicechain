# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Office workers: simple faceted figures, our own design, built from boxes and two ellipsoids.

  person_standing_a / _b   1.75 m, standing, arms by the sides. Origin on the floor between the feet.
  person_seated_a / _b     the same figure seated: origin on the floor under the pelvis, so it drops onto
                           a chair placed at the same X, Z (the chair's seat top is at 0.50 m). Thighs
                           forward, forearms resting forward at desk height.

Both face +Z. Variant a and b differ in clothes, hair and skin tone (separate materials, so a
property block or a material swap can recolour them). Each is Root / Body.
"""
import towerpulse_kit as K

VARIANTS = {
    "a": dict(skin="skin_a", hair="hair_a", shirt="shirt_a", trousers="trousers_a"),
    "b": dict(skin="skin_b", hair="hair_b", shirt="shirt_b", trousers="trousers_b"),
}


def _single(piece):
    K.node("Root", None, (0, 0, 0))
    K.node("Body", "Root", (0, 0, 0))
    K.attach("Body", piece)
    return dict(expect_nodes=["Root", "Body"])


def _head(p, v, y_neck, hair_dz=0.0):
    p.cyl((0, y_neck, 0), (0, y_neck + 0.07, 0), 0.05, v["skin"], 6)
    p.ellipsoid((0, y_neck + 0.17, 0.01), (0.095, 0.115, 0.105), v["skin"], 1)
    p.ellipsoid((0, y_neck + 0.215, -0.02), (0.10, 0.11, 0.105), v["hair"], 1)


def standing(variant):
    v = VARIANTS[variant]
    p = K.Piece(bevel=0.012)
    for sx in (-1, 1):
        p.box((sx * 0.09 - 0.055, 0.0, -0.07), (sx * 0.09 + 0.055, 0.07, 0.19), "shoe", 0.0)
        p.box((sx * 0.09 - 0.065, 0.07, -0.06), (sx * 0.09 + 0.065, 0.86, 0.07), v["trousers"], 0.0)
    p.box((-0.17, 0.80, -0.1), (0.17, 0.96, 0.1), v["trousers"], 0.0)
    p.box((-0.19, 0.94, -0.1), (0.19, 1.46, 0.1), v["shirt"], 0.03)
    p.box((-0.22, 1.36, -0.09), (0.22, 1.46, 0.09), v["shirt"], 0.0)
    for sx in (-1, 1):
        x0, x1 = sorted((sx * 0.205, sx * 0.295))
        p.box((x0, 1.0, -0.05), (x1, 1.44, 0.05), v["shirt"], 0.0)
        p.box((x0 + 0.005, 0.80, -0.04), (x1 - 0.005, 1.0, 0.04), v["skin"], 0.0)
    _head(p, v, 1.46)
    return _single(p)


def seated(variant):
    v = VARIANTS[variant]
    p = K.Piece(bevel=0.012)
    p.box((-0.17, 0.50, -0.12), (0.17, 0.66, 0.1), v["trousers"], 0.0)
    for sx in (-1, 1):
        p.box((sx * 0.09 - 0.065, 0.50, 0.05), (sx * 0.09 + 0.065, 0.64, 0.50), v["trousers"], 0.0)   # thighs
        p.box((sx * 0.09 - 0.06, 0.07, 0.44), (sx * 0.09 + 0.06, 0.52, 0.57), v["trousers"], 0.0)     # shins
        p.box((sx * 0.09 - 0.055, 0.0, 0.42), (sx * 0.09 + 0.055, 0.07, 0.68), "shoe", 0.0)
    p.box((-0.19, 0.64, -0.12), (0.19, 1.16, 0.08), v["shirt"], 0.03)
    p.box((-0.22, 1.06, -0.11), (0.22, 1.16, 0.07), v["shirt"], 0.0)
    for sx in (-1, 1):
        x0, x1 = sorted((sx * 0.205, sx * 0.295))
        p.box((x0, 0.78, -0.07), (x1, 1.14, 0.03), v["shirt"], 0.0)            # upper arm
        p.box((x0, 0.76, -0.07), (x1, 0.84, 0.40), v["shirt"], 0.0)            # forearm, forward
        p.box((x0 + 0.005, 0.76, 0.40), (x1 - 0.005, 0.83, 0.50), v["skin"], 0.0)
    _head(p, v, 1.16)
    return _single(p)
