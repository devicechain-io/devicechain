# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Office furniture, kept very low-poly: desk.glb (a workstation desk with a monitor), meeting_table.glb
and chair.glb. Each is Root / Body with its origin on the floor at the centre of its footprint.

Facing. A chair faces +Z (its back is at -Z). A desk's user sits at -Z and faces +Z, the monitor
faces -Z. The meeting table is symmetric. `layout.zone_furniture` turns these into placements.
"""
import towerpulse_kit as K


def _single(piece):
    K.node("Root", None, (0, 0, 0))
    K.node("Body", "Root", (0, 0, 0))
    K.attach("Body", piece)
    return dict(expect_nodes=["Root", "Body"])


def desk():
    p = K.Piece(bevel=0.01)
    p.box((-0.8, 0.70, -0.4), (0.8, 0.74, 0.4), "wood")                                   # top
    for sx in (-1, 1):
        p.box((sx * 0.76 - 0.02, 0.0, -0.34), (sx * 0.76 + 0.02, 0.70, 0.34), "white", 0.0)   # panel legs
    p.box((-0.74, 0.28, 0.34), (0.74, 0.70, 0.36), "white", 0.0)                          # modesty panel
    p.box((-0.05, 0.74, 0.17), (0.05, 0.94, 0.23), "steel_d", 0.0)                        # monitor stand
    p.box((-0.28, 0.94, 0.19), (0.28, 1.26, 0.25), "steel_d", 0.0)                        # monitor shell
    p.box((-0.26, 0.96, 0.178), (0.26, 1.24, 0.19), "screen", 0.0)                        # screen, faces -Z
    p.box((-0.2, 0.74, -0.2), (0.2, 0.755, -0.07), "steel_d", 0.0)                        # keyboard
    return _single(p)


def meeting_table():
    p = K.Piece(bevel=0.02)
    p.box((-1.4, 0.71, -0.6), (1.4, 0.76, 0.6), "wood")
    for sx in (-1, 1):
        p.box((sx * 1.0 - 0.1, 0.0, -0.4), (sx * 1.0 + 0.1, 0.71, 0.4), "wood_d", 0.0)
    return _single(p)


def chair():
    p = K.Piece(bevel=0.012)
    p.box((-0.23, 0.44, -0.23), (0.23, 0.50, 0.23), "fabric")                             # seat
    p.box((-0.22, 0.53, -0.26), (0.22, 0.95, -0.21), "fabric")                            # back
    p.cyl((0, 0.05, 0), (0, 0.44, 0), 0.03, "steel_d", 6)                                  # post
    p.box((-0.28, 0.0, -0.03), (0.28, 0.05, 0.03), "steel_d", 0.0)                        # base
    p.box((-0.03, 0.0, -0.28), (0.03, 0.05, 0.28), "steel_d", 0.0)
    return _single(p)
