# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Sitepulse machine kit -- shared code for every animated machine (dozer, loader, hauler ...).

    blender --background --python build.py -- <machine> [--no-render] [--only a,b] [--out DIR]

AUTHORING FRAME (every machine script): UNITY terms -- metres, X right, Y up, Z forward,
origin on the ground under the machine's footprint centre. U() converts to Blender
(model faces Blender -Y, model-right is Blender -X); the glTF exporter (+Y up) and glTFast
turn that back into Unity +Z forward / +X right with zero root rotation (verified in Unity
2026-10-04). For an axis-aligned pivot node, Unity localEulerAngles.x == Blender
rotation_euler.x (same number, same sign; verified on import into Unity).

HIERARCHY CONVENTION
  * every functional part is an EMPTY at its pivot, axis-aligned at rest (identity rotation),
  * geometry hangs under it as <Node>_LOD0 / <Node>_LOD1 with identity local transform,
  * LOD0 and LOD1 are exported as two .glb files with the same node tree (Unity has no
    auto-LODGroup for glTF; a small importer script pairs <Node>_LOD0/<Node>_LOD1 by path),
  * hydraulic cylinders: <P>Cylinder<S> (barrel, on the base part) and <P>Rod<S> (rod, on the
    moving part) each aim their local +Z (Blender) == local +Y (Unity) at the OTHER end's mount
    marker (<P>RodMount<S> / <P>CylinderMount<S>). In Unity drive them with an AimConstraint
    (aim axis +Y, world-up +Y) or a LookAt in LateUpdate; glTF does not carry constraints.

FACE TAGGING FOR LOD1: geometry built inside `with piece.detail():` is LOD0-only (seams,
grousers, bolts, rails, mirrors ...). LOD1 = LOD0 minus detail faces, then decimated.

The module imports outside Blender too (bpy missing) so the PIL compare helpers can run
under the system python3.
"""
import math, os, sys, json, contextlib

try:
    import bpy, bmesh
    from mathutils import Vector, Matrix, Quaternion
except ImportError:          # system python: only the image helpers are usable
    bpy = None

# ==================================================================================
# coordinates
# ==================================================================================
def U(x, y, z):
    """Unity (x right, y up, z fwd) -> Blender world vector."""
    return Vector((-x, -z, y))


def to_unity(v, nd=4):
    return (round(-v.x, nd), round(v.z, nd), round(-v.y, nd))


# ==================================================================================
# palette + shared material library (one set for every machine -> SRP batching friendly)
# values are LINEAR RGB (Blender / glTF baseColorFactor)
# ==================================================================================
PALETTE = {
    #  key          material name           colour                 rough metal emission
    "paint":      ("M_Paint_Yellow",     (0.86, 0.46, 0.035),    0.55, 0.0, None),   # 0.42 clipped to white under URP sun
    "paint_dark": ("M_Paint_Charcoal",   (0.050, 0.052, 0.058),  0.55, 0.0, None),
    "steel":      ("M_Steel_Black",      (0.016, 0.016, 0.018),  0.50, 0.2, None),
    "worn":       ("M_Steel_Worn",       (0.55, 0.56, 0.58),     0.32, 0.85, None),
    "rubber":     ("M_Rubber",           (0.020, 0.020, 0.021),  0.90, 0.0, None),
    "glass":      ("M_Glass",            (0.07, 0.11, 0.15),     0.06, 0.0, None),
    "beacon":     ("M_Light_Amber",      (1.00, 0.42, 0.02),     0.30, 0.0, 4.0),
    "lamp":       ("M_Light_White",      (1.00, 0.92, 0.75),     0.20, 0.0, 2.0),
    "tail":       ("M_Light_Red",        (0.80, 0.02, 0.01),     0.30, 0.0, 2.0),
    "track":      ("M_Track",            (0.05, 0.05, 0.05),     0.85, 0.1, None),   # + tread texture
}

S = dict(coll=None, nodes={}, mats={}, out=None, name=None)
NODES = S["nodes"]


def reset(name, out):
    """Empty scene + a collection for the machine. Call first."""
    bpy.ops.wm.read_factory_settings(use_empty=True)
    sc = bpy.context.scene
    sc.unit_settings.system = "METRIC"
    S["coll"] = bpy.data.collections.new(name.capitalize())
    sc.collection.children.link(S["coll"])
    S["nodes"].clear(); S["mats"].clear()
    S["out"], S["name"] = out, name
    os.makedirs(out, exist_ok=True)


def _tread_image(pitch_px=32):
    """Tileable one-link tread strip: U = one link (grouser bar | gap | shoe | gap), V = across shoe."""
    w, h = pitch_px, 16
    img = bpy.data.images.new("T_Tread", w, h)
    px = []
    for j in range(h):
        for i in range(w):
            u = i / w
            if u < 0.22:
                c = 0.20            # grouser bar face (catches light)
            elif u < 0.30 or u > 0.92:
                c = 0.010           # gap between links
            else:
                c = 0.055           # shoe plate
            if j in (0, h - 1):
                c *= 0.6
            px += [c, c, c * 1.02, 1.0]
    img.pixels = px
    img.filepath_raw = os.path.join(S["out"], "T_Tread.png")
    img.file_format = "PNG"
    img.save(); img.pack()
    return img


def mat(key):
    """Shared material by palette key (created on first use)."""
    if key in S["mats"]:
        return S["mats"][key]
    name, col, rough, metal, emit = PALETTE[key]
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    nt = m.node_tree
    b = nt.nodes["Principled BSDF"]
    b.inputs["Base Color"].default_value = (*col, 1)
    b.inputs["Roughness"].default_value = rough
    b.inputs["Metallic"].default_value = metal
    if emit:
        b.inputs["Emission Color"].default_value = (*col, 1)
        b.inputs["Emission Strength"].default_value = emit
    if key == "track":
        tex = nt.nodes.new("ShaderNodeTexImage"); tex.image = _tread_image(); tex.name = "Tread"
        tex.interpolation = "Closest"
        mp = nt.nodes.new("ShaderNodeMapping"); mp.name = "TreadScroll"
        tc = nt.nodes.new("ShaderNodeTexCoord")
        nt.links.new(tc.outputs["UV"], mp.inputs["Vector"])
        nt.links.new(mp.outputs["Vector"], tex.inputs["Vector"])
        nt.links.new(tex.outputs["Color"], b.inputs["Base Color"])
    S["mats"][key] = m
    return m


# ==================================================================================
# geometry: a Piece is a bmesh authored in Unity coordinates
# ==================================================================================
class Piece:
    def __init__(self, bevel=0.03, seg=12):
        self.bm = bmesh.new()
        self.mats = []
        self.uv = self.bm.loops.layers.uv.new("UVMap")
        self.dl = self.bm.faces.layers.int.new("detail")
        self._detail = 0
        self.bevel = bevel
        self.seg = seg

    # ---- bookkeeping ------------------------------------------------------------
    def mi(self, key):
        m = mat(key)
        if m not in self.mats:
            self.mats.append(m)
        return self.mats.index(m)

    @contextlib.contextmanager
    def detail(self):
        """Faces made inside this block are LOD0-only."""
        old, self._detail = self._detail, 1
        try:
            yield self
        finally:
            self._detail = old

    def _op(self):
        return set(self.bm.faces)

    def _tag(self, before):
        if self._detail:
            for f in set(self.bm.faces) - before:
                f[self.dl] = 1

    def _finish(self, verts, faces, key, bevel, segs=1):
        idx = self.mi(key)
        for f in faces:
            f.material_index = idx
        if bevel > 0:
            edges = list({e for v in verts for e in v.link_edges})
            bmesh.ops.bevel(self.bm, geom=edges + list(verts), offset=bevel, segments=segs,
                            profile=0.5, affect="EDGES", clamp_overlap=True, material=idx)

    # ---- primitives ---------------------------------------------------------------
    def prism(self, profile, axis, lo, hi, key, bevel=None):
        """Extrude a closed 2D profile along axis between lo..hi.
        axis 'x': profile pts are (z,y); 'z': (x,y); 'y': (x,z)."""
        b0 = self._op()
        bevel = self.bevel if bevel is None else bevel
        def pt(a, b, c):
            return {"x": (c, b, a), "z": (a, b, c), "y": (a, c, b)}[axis]
        v0 = [self.bm.verts.new(pt(a, b, lo)) for a, b in profile]
        v1 = [self.bm.verts.new(pt(a, b, hi)) for a, b in profile]
        n = len(profile)
        faces = [self.bm.faces.new(v0), self.bm.faces.new(list(reversed(v1)))]
        for i in range(n):
            j = (i + 1) % n
            faces.append(self.bm.faces.new((v0[i], v0[j], v1[j], v1[i])))
        self._finish(v0 + v1, faces, key, bevel)
        self._tag(b0)

    def box(self, mn, mx, key, bevel=None):
        (x0, y0, z0), (x1, y1, z1) = mn, mx
        x0, x1 = sorted((x0, x1)); y0, y1 = sorted((y0, y1)); z0, z1 = sorted((z0, z1))
        self.prism([(z0, y0), (z1, y0), (z1, y1), (z0, y1)], "x", x0, x1, key, bevel)

    def strip(self, mn, mx, key="steel"):
        """Un-bevelled thin box, LOD0-only: panel seams, louvres, tread plates, trims."""
        with self.detail():
            self.box(mn, mx, key, 0.0)

    def obox(self, c, axis_u, axis_v, su, sv, sw, key, open_w_neg=False):
        """Oriented box: centre c, unit axes u,v (w = u x v), full sizes su,sv,sw. No bevel.
        open_w_neg=True omits the -w face (a face buried in another surface, e.g. a tyre lug's
        base): saves 2 tris per box."""
        b0 = self._op()
        c, u, v = Vector(c), Vector(axis_u).normalized(), Vector(axis_v).normalized()
        w = u.cross(v)
        vs = [self.bm.verts.new(c + u * (su / 2 * i) + v * (sv / 2 * j) + w * (sw / 2 * k))
              for i in (-1, 1) for j in (-1, 1) for k in (-1, 1)]
        # index = 4*(i>0) + 2*(j>0) + (k>0)
        quads = [(0, 1, 3, 2), (4, 6, 7, 5), (0, 4, 5, 1), (2, 3, 7, 6), (0, 2, 6, 4), (1, 5, 7, 3)]
        if open_w_neg:
            quads.remove((0, 2, 6, 4))         # the k<0 (-w) face
        faces = [self.bm.faces.new([vs[q] for q in quad]) for quad in quads]
        self._finish(vs, faces, key, 0)
        self._tag(b0)

    def cyl(self, p0, p1, r, key, seg=None, bevel=0.0, r2=None):
        """Capped cylinder / cone frustum from p0 to p1."""
        b0 = self._op()
        seg = seg or self.seg
        p0, p1 = Vector(p0), Vector(p1)
        d = p1 - p0
        rot = Vector((0, 0, 1)).rotation_difference(d.normalized()).to_matrix().to_4x4()
        M = Matrix.Translation((p0 + p1) / 2) @ rot
        res = bmesh.ops.create_cone(self.bm, cap_ends=True, cap_tris=False, segments=seg,
                                    radius1=r, radius2=r if r2 is None else r2,
                                    depth=d.length, matrix=M)
        verts = res["verts"]
        faces = list({f for v in verts for f in v.link_faces})
        self._finish(verts, faces, key, bevel)
        self._tag(b0)

    def tube(self, pts, r, key="steel", seg=6):
        """Handrail / hose polyline: a cylinder per segment plus a small knuckle at each bend."""
        for a, b in zip(pts, pts[1:]):
            self.cyl(a, b, r, key, seg)
        for p in pts[1:-1]:
            self.cyl((p[0], p[1] - r, p[2]), (p[0], p[1] + r, p[2]), r * 1.25, key, seg)

    def lathe_x(self, c, profile, key, seg=20):
        """Surface of revolution about the X axis through c. profile = closed list of
        (radius, x_offset) points; consecutive points are joined, last back to first."""
        b0 = self._op()
        x, y, z = c
        rings = []
        for r, dx in profile:
            rings.append([self.bm.verts.new((x + dx, y + r * math.sin(2 * math.pi * i / seg),
                                             z + r * math.cos(2 * math.pi * i / seg))) for i in range(seg)])
        faces = []
        n = len(rings)
        for k in range(n):
            A, B = rings[k], rings[(k + 1) % n]
            for i in range(seg):
                j = (i + 1) % seg
                faces.append(self.bm.faces.new((A[i], A[j], B[j], B[i])))
        self._finish([v for r in rings for v in r], faces, key, 0)
        self._tag(b0)

    def gear(self, c, r_root, r_tip, teeth, half_w, key):
        prof = []
        for i in range(teeth * 2):
            a = math.pi * 2 * i / (teeth * 2)
            r = r_tip if i % 2 == 0 else r_root
            prof.append((c[2] + r * math.cos(a), c[1] + r * math.sin(a)))
        self.prism(prof, "x", c[0] - half_w, c[0] + half_w, key, bevel=0)

    def lamp(self, c, facing, w, h, depth=0.08, lens="lamp", housing="steel"):
        """Work/tail light mounted on a surface point `c`: a housing box that sticks out of the
        surface along `facing` ('+z','-z','+x','-x') by `depth`, with an emissive lens on its
        outer face. w = width across, h = height."""
        x, y, z = c
        ax = facing[1]; sg = 1 if facing[0] == "+" else -1
        if ax == "z":
            self.box((x - w / 2, y - h / 2, z), (x + w / 2, y + h / 2, z + sg * depth), housing, 0.012)
            self.box((x - w / 2 + 0.022, y - h / 2 + 0.022, z + sg * (depth - 0.01)),
                     (x + w / 2 - 0.022, y + h / 2 - 0.022, z + sg * (depth + 0.008)), lens, 0.0)
        else:
            self.box((x, y - h / 2, z - w / 2), (x + sg * depth, y + h / 2, z + w / 2), housing, 0.012)
            self.box((x + sg * (depth - 0.01), y - h / 2 + 0.022, z - w / 2 + 0.022),
                     (x + sg * (depth + 0.008), y + h / 2 - 0.022, z + w / 2 - 0.022), lens, 0.0)

    def seam_profile(self, pts, x, width=0.018, out=0.006, key="steel"):
        """A weld/panel seam following a (z,y) polyline on a curved surface (offset +z by `out`)."""
        prof = [(z + out, y) for z, y in pts] + [(z - 0.01, y) for z, y in reversed(pts)]
        with self.detail():
            self.prism(prof, "x", x - width / 2, x + width / 2, key, 0.0)


# ==================================================================================
# hierarchy
# ==================================================================================
def node(name, parent, pos_unity, rot_quat=None, size=0.25):
    o = bpy.data.objects.new(name, None)
    o.empty_display_size = size
    o.empty_display_type = "PLAIN_AXES"
    S["coll"].objects.link(o)
    w = U(*pos_unity)
    if parent:
        p = NODES[parent]
        o.parent = p
        o.location = p.matrix_world.inverted() @ w      # parents are at rest (identity) when built
    else:
        o.location = w
    if rot_quat is not None:
        o.rotation_mode = "QUATERNION"
        o.rotation_quaternion = rot_quat
    bpy.context.view_layer.update()
    NODES[name] = o
    return o


def attach(name, piece, local_blender=False, smooth_angle=35):
    """Turn a Piece into <name>_LOD0 under node <name> (vertices in the node's local frame)."""
    n = NODES[name]
    bm = piece.bm
    if not local_blender:
        inv = n.matrix_world.inverted()
        for v in bm.verts:
            v.co = inv @ U(*v.co)
    bmesh.ops.remove_doubles(bm, verts=bm.verts, dist=1e-5)
    bmesh.ops.recalc_face_normals(bm, faces=bm.faces)
    me = bpy.data.meshes.new(name + "_LOD0_Mesh")
    bm.to_mesh(me); bm.free()
    for m in piece.mats:
        me.materials.append(m)
    for p in me.polygons:
        p.use_smooth = True
    me.set_sharp_from_angle(angle=math.radians(smooth_angle))
    o = bpy.data.objects.new(name + "_LOD0", me)
    S["coll"].objects.link(o)
    o.parent = n
    return o


def rot_x(name, rad):
    """Set a pivot's local X rotation (Blender value == Unity localEulerAngles.x in radians)."""
    if name in NODES:                  # tolerate poses applied while the rig is half-built
        NODES[name].rotation_euler = (rad, 0, 0)


def update():
    bpy.context.view_layer.update()


def world_unity(name, local_unity_point=None):
    """World position (Unity coords) of a node, or of a point given in Unity world coords AT REST
    that is carried along by the node (pass `local_unity_point` captured with `capture`)."""
    o = NODES[name]
    if local_unity_point is None:
        return Vector(to_unity(o.matrix_world.translation))
    return Vector(to_unity(o.matrix_world @ local_unity_point))


def capture(name, pos_unity):
    """Express a rest-pose Unity world point in node-local Blender coords (for tracking it)."""
    return NODES[name].matrix_world.inverted() @ U(*pos_unity)


def node_pitch_deg(name):
    """World pitch of a node about Unity X, in degrees (from its local +Y in Blender = Unity -Z...)."""
    m = NODES[name].matrix_world.to_3x3()
    fwd = m @ Vector((0, -1, 0))                       # Blender -Y == Unity +Z (forward)
    return math.degrees(math.atan2(fwd.z, -fwd.y))     # + = nose up


def aim_quat(src, dst):
    d = NODES[dst].matrix_world.translation - NODES[src].matrix_world.translation
    pr = NODES[src].parent.matrix_world.to_quaternion()
    return Vector((0, 0, 1)).rotation_difference(pr.inverted() @ d.normalized())


def solve(fn, target, lo, hi, iters=60):
    """Bisection: find x in [lo,hi] with fn(x) == target (fn monotonic on the bracket)."""
    flo = fn(lo) - target; fhi = fn(hi) - target
    if flo * fhi > 0:
        raise ValueError(f"solve: target {target} not bracketed by [{lo},{hi}] ({flo+target},{fhi+target})")
    for _ in range(iters):
        mid = (lo + hi) / 2
        fm = fn(mid) - target
        if (fm > 0) == (flo > 0):
            lo, flo = mid, fm
        else:
            hi = mid
    return (lo + hi) / 2


# ==================================================================================
# builders
# ==================================================================================
def hydraulic(prefix, sfx, base, A, moving, B, r_barrel, r_rod, set_poses, reset_pose,
              clearance=0.06, seg=10, barrel_key="steel", rod_key="worn", hoses=True):
    """Aimed two-piece hydraulic cylinder between base-part point A and moving-part point B.

    Creates <prefix>Cylinder<sfx> (barrel, child of `base`, pivot at A) and <prefix>Rod<sfx>
    (rod, child of `moving`, pivot at B) plus the aim targets <prefix>CylinderMount<sfx> (on base
    at A) and <prefix>RodMount<sfx> (on moving at B). Barrel and rod length are derived from the
    pin-to-pin distance over every pose in `set_poses` (callables that pose the rig):
    barrel = rod = d_min - clearance, so the rod is never shorter than needed and never pokes
    out of the barrel's far end at d_min; `overlap_at_max` > 0 means it never pulls out."""
    cyl, rod = f"{prefix}Cylinder{sfx}", f"{prefix}Rod{sfx}"
    cm, rm = f"{prefix}CylinderMount{sfx}", f"{prefix}RodMount{sfx}"
    node(rod, moving, B, size=0.1)
    node(cyl, base, A, size=0.1)
    node(cm, base, A, size=0.05)
    node(rm, moving, B, size=0.05)
    ds = []
    for sp in set_poses:
        sp(); update()
        ds.append((NODES[cyl].matrix_world.translation - NODES[rod].matrix_world.translation).length)
    reset_pose(); update()
    dmin, dmax = min(ds), max(ds)
    L = dmin - clearance
    for nm, tgt, r, key, lead in ((cyl, rm, r_barrel, barrel_key, True), (rod, cm, r_rod, rod_key, False)):
        o = NODES[nm]
        o.rotation_mode = "QUATERNION"
        o.rotation_quaternion = aim_quat(nm, tgt)
        con = o.constraints.new("DAMPED_TRACK"); con.target = NODES[tgt]; con.track_axis = "TRACK_Z"
        pc = Piece()
        pc.cyl((0, 0, 0), (0, 0, L), r, key, seg)
        pc.cyl((-r * 0.9, 0, 0), (r * 0.9, 0, 0), r * 0.85, "steel", 8)          # eye / pin
        if lead:
            pc.cyl((0, 0, L - 0.02), (0, 0, L + 0.03), r * 1.12, "steel", seg)  # gland nut
            pc.cyl((0, 0, 0.02), (0, 0, 0.10), r * 1.12, "steel", seg)           # base collar
            if hoses:   # two short hose stubs along the barrel (LOD0 only)
                with pc.detail():
                    pc.tube([(r * 0.9, 0, 0.18), (r * 1.6, 0, 0.30), (r * 1.6, 0, L - 0.12),
                             (r * 0.9, 0, L - 0.06)], 0.014, "rubber", 5)
        attach(nm, pc, local_blender=True)
    return dict(cylinder=cyl, rod=rod, dmin=round(dmin, 4), dmax=round(dmax, 4),
                barrel_len=round(L, 4), rod_len=round(L, 4),
                overlap_at_max=round(2 * L - dmax, 4), stroke_ratio=round(dmax / dmin, 3))


def hydraulic_check(h):
    """Aim residual (deg) of barrel/rod at the current pose and current pin distance."""
    cyl, rod = NODES[h["cylinder"]], NODES[h["rod"]]
    a, b = cyl.matrix_world.translation, rod.matrix_world.translation
    res = []
    for o, src, dst in ((cyl, a, b), (rod, b, a)):
        z = (o.matrix_world.to_3x3() @ Vector((0, 0, 1))).normalized()
        res.append(math.degrees(z.angle((dst - src).normalized())))
    return dict(aim_err_deg=round(max(res), 4), pin_dist=round((a - b).length, 4))


def track_assembly(side, x, prm, parent="Root", frame_key="paint_dark", hub_key="paint", grouser_key="paint_dark"):
    """Oval crawler track at centre-line x: node <side>Track with belt (+ silhouette grousers,
    LOD0-only), frame and bottom rollers; child nodes <side>Sprocket (rear, z<0) and
    <side>Idler (front), both rotating about local X.

    prm: wheel_r, belt_t, grouser_h, shoe_w, ground_len, pitch, arc_seg, rollers
    Returns the speed constants Unity needs:
      sprocket_rad_per_m = 1 / wheel_r       (localEulerAngles.x += deg(d * that), + = forward)
      tread_u_per_m      = links / loop_len  (mainTextureOffset.x = -d * that,   + d = forward)"""
    R0 = prm["wheel_r"]; R1 = R0 + prm["belt_t"]; gh = prm["grouser_h"]
    cy = R1 + gh; wz = prm["ground_len"] / 2; hw = prm["shoe_w"] / 2; n = prm["arc_seg"]
    tn = side + "Track"
    node(tn, parent, (x, 0, 0))
    t = Piece()
    # --- belt: racetrack loop, U = distance along the outer surface / effective pitch
    pts = [(-wz, -math.pi / 2 - math.pi * i / n) for i in range(n + 1)] + \
          [(wz, math.pi / 2 - math.pi * i / n) for i in range(n + 1)]
    outer = [(cz + math.cos(a) * R1, cy + math.sin(a) * R1) for cz, a in pts]
    Ls = [0.0]
    for i in range(1, len(outer) + 1):
        Ls.append(Ls[-1] + math.dist(outer[i - 1], outer[i % len(outer)]))
    total = Ls[-1]
    links = max(1, round(total / prm["pitch"]))
    scale = links / total
    bm, uvl = t.bm, t.uv
    idx = t.mi("track")
    V = [[bm.verts.new((x + s * hw, cy + math.sin(a) * r, cz + math.cos(a) * r))
          for s in (-1, 1) for r in (R0, R1)] for cz, a in pts]
    m = len(V)
    for i in range(m):
        j = (i + 1) % m
        u0, u1 = Ls[i] * scale, Ls[i + 1] * scale
        for vs, uvs in (((V[i][1], V[j][1], V[j][3], V[i][3]), [(u0, 0), (u1, 0), (u1, 1), (u0, 1)]),
                        ((V[i][0], V[i][2], V[j][2], V[j][0]), [(u0, 0), (u0, 1), (u1, 1), (u1, 0)]),
                        ((V[i][0], V[j][0], V[j][1], V[i][1]), [(u0, 0), (u1, 0), (u1, .1), (u0, .1)]),
                        ((V[i][2], V[i][3], V[j][3], V[j][2]), [(u0, 0), (u0, .1), (u1, .1), (u1, 0)])):
            f = bm.faces.new(vs)
            f.material_index = idx
            for loop, uv in zip(f.loops, uvs):
                loop[uvl].uv = uv
    # --- grousers: one bar per link, centred where the texture's bar is at offset 0 (u = k+0.11).
    #     They are STATIC geometry with their own (non-scrolling) material: only the texture
    #     scrolls, so the silhouette teeth do not travel. Period == link pitch, so the outline is
    #     the same at every whole-link offset; at demo distances this reads as a moving track.
    def at(s):
        s %= total
        for i in range(len(outer)):
            if Ls[i + 1] >= s:
                a, b = outer[i], outer[(i + 1) % len(outer)]
                f = (s - Ls[i]) / max(1e-9, Ls[i + 1] - Ls[i])
                p = (a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f)
                tz, ty = (b[0] - a[0]), (b[1] - a[1]); l = math.hypot(tz, ty)
                return p, (tz / l, ty / l)
    with t.detail():
        for k in range(links):
            (pz, py), (tz, ty) = at((k + 0.11) / scale)
            nz, ny = -ty, tz                                   # outward normal (clockwise loop)
            c = (x, py + ny * gh / 2, pz + nz * gh / 2)
            t.obox(c, (1, 0, 0), (0, ty, tz), prm["shoe_w"] - 0.02, 0.22 / scale, gh, grouser_key)
    # --- frame + rollers
    t.prism([(-wz + 0.05, 0.16 + gh), (wz - 0.05, 0.16 + gh), (wz + 0.05, cy), (wz - 0.25, cy + 0.24),
             (-wz + 0.25, cy + 0.24), (-wz - 0.05, cy)], "x", x - hw + 0.10, x + hw - 0.10, frame_key, 0.02)
    with t.detail():   # frame side seam + guard strip
        for s in (-1, 1):
            t.strip((x + s * (hw - 0.10), cy - 0.02, -wz + 0.30), (x + s * (hw - 0.094), cy + 0.0, wz - 0.30), "steel")
    nr = prm.get("rollers", 6)
    ry = cy - R0 + 0.11
    for i in range(nr):
        z = -wz + 0.45 + i * (2 * wz - 0.9) / (nr - 1)
        t.cyl((x - hw + 0.06, ry, z), (x + hw - 0.06, ry, z), 0.11, "steel", 8)
    attach(tn, t)
    # --- sprocket (rear)
    node(side + "Sprocket", tn, (x, cy, -wz))
    g = Piece()
    g.gear((x, cy, -wz), R0 - 0.10, R0 + 0.02, 11, hw - 0.05, "steel")
    g.cyl((x - hw + 0.02, cy, -wz), (x + hw - 0.02, cy, -wz), 0.16, hub_key, 10, 0.01)
    for i in range(6):
        a = math.pi * 2 * i / 6
        bz, by = -wz + math.cos(a) * 0.21, cy + math.sin(a) * 0.21
        g.box((x - hw + 0.01, by - 0.035, bz - 0.035), (x + hw - 0.01, by + 0.035, bz + 0.035), "worn", 0)
    attach(side + "Sprocket", g)
    # --- idler (front)
    node(side + "Idler", tn, (x, cy, wz))
    d = Piece()
    d.cyl((x - hw + 0.08, cy, wz), (x + hw - 0.08, cy, wz), R0 - 0.01, "steel", 14)
    d.cyl((x - hw + 0.05, cy, wz), (x + hw - 0.05, cy, wz), 0.15, hub_key, 10, 0.01)
    for i in range(4):
        a = math.pi * 2 * i / 4 + 0.4
        bz, by = wz + math.cos(a) * 0.24, cy + math.sin(a) * 0.24
        d.box((x - hw + 0.07, by - 0.04, bz - 0.04), (x + hw - 0.07, by + 0.04, bz + 0.04), "worn", 0)
    attach(side + "Idler", d)
    return dict(node=tn, loop_len_m=round(total, 4), links=links, pitch_eff_m=round(total / links, 5),
                tread_u_per_m=round(scale, 5), sprocket_rad_per_m=round(1 / R0, 5),
                centre_y=cy, half_len=wz, outer_r=R1)


def wheel_piece(p, c, radius, width, side, rim_r=None, blocks=18, block_h=0.05,
                rim_key="paint", tire_key="rubber", bolts=8):
    """Tyre with chevron tread blocks (LOD0-only) + dished rim, hub and lug bolts, axle along X.
    `side` = +1 if the rim face looks to +X (right-hand wheel), -1 for left. Add to piece `p`
    (normally the wheel's own node piece, so it spins about the node's local X)."""
    x, y, z = c
    hw = width / 2
    rim_r = rim_r or radius * 0.62
    r_body = radius - block_h
    # tyre carcass: a closed lathe ring (tread face, rounded shoulders, sidewalls down to the rim)
    p.lathe_x(c, [(rim_r, -hw + 0.03), (r_body - 0.07, -hw), (r_body, -hw + 0.07), (r_body, hw - 0.07),
                  (r_body - 0.07, hw), (rim_r, hw - 0.03)], tire_key, 20)
    with p.detail():
        for i in range(blocks):
            a = 2 * math.pi * i / blocks
            for s in (-1, 1):            # chevron: two staggered blocks per pitch
                aa = a + (math.pi / blocks) * (0.5 if s > 0 else 0)
                ca, sa = math.cos(aa), math.sin(aa)
                cc = (x + s * hw * 0.45, y + sa * (r_body + block_h / 2 - 0.005), z + ca * (r_body + block_h / 2 - 0.005))
                p.obox(cc, (1, 0, 0), (0, ca, -sa), hw * 0.85, 2 * math.pi * radius / blocks * 0.45,
                       block_h + 0.01, tire_key)
    # rim recessed 6 cm inside the sidewall: barrel, dark dish ring, hub boss, lug bolts
    face = x + side * (hw - 0.06)
    p.cyl((x - side * (hw - 0.03), y, z), (face, y, z), rim_r + 0.005, rim_key, 16)
    p.cyl((face, y, z), (face + side * 0.005, y, z), rim_r * 0.82, "steel", 16)
    p.cyl((face - side * 0.02, y, z), (face + side * 0.06, y, z), rim_r * 0.38, rim_key, 12, 0.01)
    with p.detail():
        for i in range(bolts):
            a = 2 * math.pi * i / bolts
            by, bz = y + math.sin(a) * rim_r * 0.55, z + math.cos(a) * rim_r * 0.55
            p.cyl((face, by, bz), (face + side * 0.03, by, bz), 0.022, "worn", 6)


# ==================================================================================
# LOD1, report, export
# ==================================================================================
def tris(me):
    return sum(len(p.vertices) - 2 for p in me.polygons)


def lod_objects(lod):
    return [o for o in S["coll"].objects if o.type == "MESH" and o.name.endswith("_" + lod)]


def make_lod1(ratio=0.45, keep_full=()):
    """LOD1 = LOD0 minus detail-tagged faces, collapse-decimated to `ratio` (nodes in keep_full
    keep their stripped mesh undecimated -- e.g. tiny parts decimation would destroy)."""
    for o in lod_objects("LOD0"):
        bm = bmesh.new(); bm.from_mesh(o.data)
        dl = bm.faces.layers.int.get("detail")
        if dl is not None:
            bmesh.ops.delete(bm, geom=[f for f in bm.faces if f[dl]], context="FACES")
            bm.faces.layers.int.remove(dl)
        me = bpy.data.meshes.new(o.name[:-1] + "1_Mesh")
        bm.to_mesh(me); bm.free()
        for m in o.data.materials:
            me.materials.append(m)
        o1 = bpy.data.objects.new(o.name[:-1] + "1", me)
        S["coll"].objects.link(o1)
        o1.parent = o.parent
        if len(me.polygons) and o.parent.name not in keep_full:
            mod = o1.modifiers.new("dec", "DECIMATE")
            mod.ratio = ratio
            mod.use_collapse_triangulate = True
            dg = bpy.context.evaluated_depsgraph_get()
            nm = bpy.data.meshes.new_from_object(o1.evaluated_get(dg))
            o1.modifiers.clear(); o1.data = nm; nm.name = o1.name + "_Mesh"
    for o in lod_objects("LOD0"):                       # strip the tag from what gets exported
        a = o.data.attributes.get("detail")
        if a is not None:
            o.data.attributes.remove(a)
    # drop LOD1 objects that ended up empty (pure-detail nodes), keep hierarchy symmetric:
    for o in list(lod_objects("LOD1")):
        if len(o.data.polygons) == 0:
            bpy.data.objects.remove(o)
    update()


def hierarchy(root="Root"):
    out = []
    def walk(o, depth):
        e = o.matrix_world.to_euler()
        out.append(dict(name=o.name, depth=depth, type=o.type, origin_unity=to_unity(o.matrix_world.translation),
                        rot_deg_blender=[round(math.degrees(v), 2) for v in e],
                        tris=tris(o.data) if o.type == "MESH" else None))
        for ch in sorted(o.children, key=lambda c: c.name):
            walk(ch, depth + 1)
    walk(NODES[root], 0)
    return out


def node_paths(lod):
    """Paths of every exported object for one LOD file, with the _LODn suffix stripped."""
    keep = set(o.name for o in S["coll"].objects if o.type == "EMPTY") | set(o.name for o in lod_objects(lod))
    paths = []
    def walk(o, pre):
        if o.name not in keep:
            return
        nm = o.name[:-5] if o.name.endswith("_" + lod) else o.name
        paths.append(pre + nm)
        for ch in sorted(o.children, key=lambda c: c.name):
            walk(ch, pre + nm + "/")
    walk(NODES["Root"], "")
    return paths


def report(budget_lod0=10000):
    lod0 = lod_objects("LOD0")
    r = dict(machine=S["name"])
    r["hierarchy"] = hierarchy()
    for lod in ("LOD0", "LOD1"):
        r[f"tris_{lod}"] = sum(tris(o.data) for o in lod_objects(lod))
    pts = [o.matrix_world @ v.co for o in lod0 for v in o.data.vertices]
    mn = Vector((min(p[i] for p in pts) for i in range(3)))
    mx = Vector((max(p[i] for p in pts) for i in range(3)))
    sz = mx - mn
    r["bbox_unity_m"] = dict(width_x=round(sz.x, 3), height_y=round(sz.z, 3), length_z=round(sz.y, 3),
                             min_y=round(mn.z, 3), front_z=round(-mn.y, 3), rear_z=round(-mx.y, 3))
    used = sorted({m.name for o in lod0 for m in o.data.materials})
    r["materials"] = used
    r["per_node_tris_LOD0"] = {o.parent.name: tris(o.data) for o in lod0}
    p0, p1 = node_paths("LOD0"), node_paths("LOD1")
    only0 = sorted(set(p0) - set(p1))
    # a node whose geometry is entirely LOD0-only detail legitimately has no LOD1 mesh:
    r["lod_hierarchy"] = dict(lod0_paths=len(p0), lod1_paths=len(p1), only_in_lod0=only0,
                              only_in_lod1=sorted(set(p1) - set(p0)))
    # functional pivots must be axis-aligned at rest (aimed cylinders are the documented exception)
    rotated = [h["name"] for h in r["hierarchy"] if h["type"] == "EMPTY"
               and any(abs(v) > 1e-3 for v in h["rot_deg_blender"])]
    r["rotated_nodes_at_rest"] = rotated
    r["budget"] = dict(lod0_limit=budget_lod0, ok=r["tris_LOD0"] <= budget_lod0)
    return r


def _select_only(objs):
    for o in bpy.data.objects:
        o.select_set(False)
    for o in objs:
        o.select_set(True)
    bpy.context.view_layer.objects.active = objs[0]


def export(name=None):
    """<out>/<name>.glb (LOD0), <name>_LOD1.glb, <name>.blend. glTF only (FBX dropped: it left
    a 90 deg root rotation and rotated pivot frames in Unity)."""
    name = name or S["name"]
    out = S["out"]
    empties = [o for o in S["coll"].objects if o.type == "EMPTY"]
    files = {}
    for lod in ("LOD0", "LOD1"):
        _select_only([NODES["Root"]] + empties + lod_objects(lod))
        fn = os.path.join(out, f"{name}.glb" if lod == "LOD0" else f"{name}_LOD1.glb")
        bpy.ops.export_scene.gltf(filepath=fn, export_format="GLB", use_selection=True,
                                  export_yup=True, export_apply=True, export_animations=False,
                                  export_cameras=False, export_lights=False)
        files[lod] = fn
    bpy.ops.wm.save_as_mainfile(filepath=os.path.join(out, f"{name}.blend"))
    return files


def glb_summary(fn):
    """Read back a .glb: node count, mesh count, materials, root rotation, emissive strength ext."""
    import struct
    with open(fn, "rb") as f:
        data = f.read()
    ln = struct.unpack_from("<I", data, 12)[0]
    j = json.loads(data[20:20 + ln])
    roots = [j["nodes"][i] for i in j["scenes"][0]["nodes"]]
    return dict(file=os.path.basename(fn), bytes=len(data), nodes=len(j["nodes"]), meshes=len(j.get("meshes", [])),
                materials=[m["name"] for m in j.get("materials", [])],
                extensions=j.get("extensionsUsed", []),
                root_rotations=[r.get("rotation", [0, 0, 0, 1]) for r in roots],
                root_names=[r["name"] for r in roots])


# ==================================================================================
# rendering (Cycles CPU: EEVEE/Workbench need a GPU context, which a headless Linux/WSL host may not have)
# ==================================================================================
R = dict(cam=None)


def render_setup(samples=48, res=(1280, 720), ground=True):
    sc = bpy.context.scene
    sc.render.engine = "CYCLES"
    sc.cycles.device = "CPU"
    sc.cycles.samples = samples
    sc.cycles.use_denoising = True
    sc.render.resolution_x, sc.render.resolution_y = res
    sc.render.film_transparent = False
    sc.view_settings.view_transform = "Standard"     # closer to URP without tonemapping
    sc.view_settings.look = "None"
    w = bpy.data.worlds.new("W"); sc.world = w; w.use_nodes = True
    w.node_tree.nodes["Background"].inputs[0].default_value = (0.55, 0.65, 0.78, 1)
    w.node_tree.nodes["Background"].inputs[1].default_value = 0.9
    sun = bpy.data.objects.new("Sun", bpy.data.lights.new("Sun", "SUN"))
    sun.data.energy = 3.5; sun.data.angle = math.radians(3)
    sun.rotation_euler = (math.radians(50), 0, math.radians(35))
    sc.collection.objects.link(sun)
    if ground:
        bpy.ops.mesh.primitive_plane_add(size=600)
        g = bpy.context.active_object; g.name = "RenderGround"
        gm = bpy.data.materials.new("Ground"); gm.use_nodes = True
        gm.node_tree.nodes["Principled BSDF"].inputs["Base Color"].default_value = (0.42, 0.33, 0.24, 1)
        gm.node_tree.nodes["Principled BSDF"].inputs["Roughness"].default_value = 0.95
        g.data.materials.append(gm)
    cam = bpy.data.objects.new("Cam", bpy.data.cameras.new("Cam"))
    sc.collection.objects.link(cam); sc.camera = cam
    R["cam"] = cam
    show_lod(0)


def show_lod(n):
    for o in S["coll"].objects if S["coll"] else bpy.data.objects:
        if o.type == "MESH":
            o.hide_render = not o.name.endswith(f"_LOD{n}")


def look(pos_u, tgt_u, lens=50, ortho=None, roll_top=False):
    cam = R["cam"]
    cam.location = U(*pos_u)
    d = U(*tgt_u) - cam.location
    cam.rotation_euler = d.to_track_quat("-Z", "Y").to_euler()
    if ortho:
        cam.data.type = "ORTHO"; cam.data.ortho_scale = ortho
    else:
        cam.data.type = "PERSP"; cam.data.lens = lens
    cam.data.clip_end = 2000


def shot(path, res=None):
    sc = bpy.context.scene
    if res:
        sc.render.resolution_x, sc.render.resolution_y = res
    sc.render.filepath = path
    bpy.ops.render.render(write_still=True)
    print("rendered", path, flush=True)


# ==================================================================================
# image helpers (system python3 + PIL; bpy not needed)
# ==================================================================================
def side_by_side(left, right, out, labels=("before", "after"), height=720):
    from PIL import Image, ImageDraw
    ims = [Image.open(p).convert("RGB") for p in (left, right)]
    ims = [im.resize((round(im.width * height / im.height), height)) for im in ims]
    sheet = Image.new("RGB", (sum(i.width for i in ims) + 10, height), "white")
    x = 0
    for im, lb in zip(ims, labels):
        sheet.paste(im, (x, 0))
        d = ImageDraw.Draw(sheet)
        d.rectangle((x + 8, 8, x + 8 + 9 * len(lb), 30), fill=(255, 255, 255))
        d.text((x + 12, 12), lb, fill=(0, 0, 0))
        x += im.width + 10
    sheet.save(out)
    return out


def overlay_reference(render_png, ref_png, out, height_m, px_per_m=200.0, ground_px=720, x_px=None):
    """50% overlay of an orthographic reference drawing scaled to the render's px/m."""
    from PIL import Image, ImageDraw
    r = Image.open(render_png).convert("RGB")
    ref = Image.open(ref_png).convert("RGB")
    g = ref.convert("L").point(lambda v: 255 if v < 235 else 0)
    ref = ref.crop(g.getbbox())
    s = height_m * px_per_m / ref.height
    ref = ref.resize((round(ref.width * s), round(ref.height * s)))
    canvas = Image.new("RGB", r.size, "white")
    x = (r.width - ref.width) // 2 if x_px is None else x_px
    canvas.paste(ref, (x, ground_px - ref.height))
    o = Image.blend(r, canvas, 0.5)
    ImageDraw.Draw(o).line((0, ground_px, r.width, ground_px), fill=(255, 0, 0), width=1)
    o.save(out)
    return out


if __name__ == "__main__" and bpy is None:
    # python3 sitepulse_kit.py sbs left.png right.png out.png "label a" "label b"
    a = sys.argv[1:]
    if a and a[0] == "sbs":
        print(side_by_side(a[1], a[2], a[3], tuple(a[4:6]) if len(a) >= 6 else ("before", "after")))
    elif a and a[0] == "overlay":
        print(overlay_reference(a[1], a[2], a[3], float(a[4]), ground_px=int(a[5]),
                                x_px=int(a[6]) if len(a) > 6 else None))
