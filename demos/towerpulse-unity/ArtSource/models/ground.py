# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
campus_ground.glb: the lawn, the plaza between the two buildings, the paths, planters, trees and
the bollard lamps. Origin at the campus origin (world 0, 0, 0): the file is placed unmoved.

  Root
    Lawn            the ground, top at y = -0.15 (so the buildings' floor level 0 stands proud of it)
    Plaza           paving, top at y = -0.03, with an inlay grid
    Paths           the spine out to the street, the entrance links and the service paths to the plant
    Planters, Trees
    Bollards        the posts
    BollardLamps    the emissive caps of the bollards, a node of their own so Unity can dim them at day

Everything is in `layout.py` (PLAZA, PATHS, TREES ...); the positions of the buildings, rooftop units and
chillers there are what this ground is drawn around.
"""
import towerpulse_kit as K
import layout as L

OVER = 0.004          # the plaza inlay, and each path, stand a hair proud of what they lie on


def _node(name, piece):
    K.node(name, "Root", (0, 0, 0))
    K.attach(name, piece)


def ground():
    K.node("Root", None, (0, 0, 0))
    x0, x1, z0, z1 = L.LAWN
    p = K.Piece(bevel=0.0)
    p.box((x0, -0.6, z0), (x1, L.LAWN_TOP, z1), "lawn")
    _node("Lawn", p)

    p = K.Piece(bevel=0.0)
    x0, x1, z0, z1 = L.PLAZA
    p.box((x0, -0.6, z0), (x1, L.PAVE_TOP, z1), "paving")
    with p.detail():
        for x in range(int(x0) + 3, int(x1), 3):                     # inlay grid every 3 m
            p.box((x, L.PAVE_TOP, z0 + 0.3), (x + 0.05, L.PAVE_TOP + OVER, z1 - 0.3), "paving_d", 0.0)
        for z in range(int(z0) + 3, int(z1), 3):
            p.box((x0 + 0.3, L.PAVE_TOP, z), (x1 - 0.3, L.PAVE_TOP + OVER, z + 0.05), "paving_d", 0.0)
    _node("Plaza", p)

    p = K.Piece(bevel=0.0)
    for i, (a0, a1, b0, b1) in enumerate(L.PATHS):
        p.box((a0, -0.6, b0), (a1, L.PAVE_TOP + OVER * (i % 3), b1), "paving_d")
    _node("Paths", p)

    p = K.Piece(bevel=0.02)
    for x, z in L.PLANTERS:
        p.box((x - 0.8, L.PAVE_TOP, z - 0.8), (x + 0.8, 0.5, z + 0.8), "concrete")
        p.box((x - 0.7, 0.5, z - 0.7), (x + 0.7, 0.52, z + 0.7), "trunk", 0.0)
        p.ellipsoid((x, 0.8, z), (0.55, 0.4, 0.55), "leaf_b", 1)
    _node("Planters", p)

    p = K.Piece(bevel=0.0)
    for i, (x, z) in enumerate(L.TREES):
        r = 1.5 + 0.3 * (i % 3)
        p.cyl((x, L.LAWN_TOP, z), (x, 2.4 + 0.2 * (i % 2), z), 0.17, "trunk", 6)
        p.ellipsoid((x, 3.5 + 0.2 * (i % 2), z), (r, r * 0.9, r), "leaf_a" if i % 2 == 0 else "leaf_b", 1)
        p.ellipsoid((x + 0.5, 4.3, z - 0.3), (r * 0.6, r * 0.55, r * 0.6), "leaf_b" if i % 2 == 0 else "leaf_a", 1)
    _node("Trees", p)

    p = K.Piece(bevel=0.0)
    q = K.Piece(bevel=0.0)
    for x, z in L.BOLLARDS:
        p.cyl((x, L.PAVE_TOP, z), (x, 0.9, z), 0.08, "steel_d", 8)
        q.cyl((x, 0.9, z), (x, 0.96, z), 0.085, "lamp", 8)
    _node("Bollards", p)
    _node("BollardLamps", q)
    return dict(expect_nodes=["Root", "Lawn", "Plaza", "Paths", "Planters", "Trees", "Bollards", "BollardLamps"])
