# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Towerpulse art kit -- shared code for every model (tower, annex, rooftop units, chillers, furniture, people, ground).

    blender --background --python build.py -- <model> [--out DIR]

This is the Sitepulse kit with the machine-only parts (tracks, wheels, hydraulics) taken out. The
frame, the Piece builders, the LOD1 pass, the export and the report are the same.

AUTHORING FRAME (every model script): UNITY terms -- metres, X right, Y up, Z forward, origin on
the ground under the model's footprint centre. U() converts to Blender (a model faces Blender -Y,
model-right is Blender -X); the glTF exporter (+Y up) and glTFast turn that back into Unity +Z
forward / +X right with zero root rotation. For an axis-aligned pivot node, Unity
localEulerAngles equals the Blender rotation_euler (same number, same sign).

HIERARCHY CONVENTION
  * every functional part is an EMPTY at its pivot, axis-aligned at rest (identity rotation),
  * geometry hangs under it as <Node>_LOD0 / <Node>_LOD1 with identity local transform,
  * LOD0 and LOD1 are exported as two .glb files with the same node tree (glTF has no LOD
    groups; Unity pairs <Node>_LOD0 / <Node>_LOD1 by path).

FACE TAGGING FOR LOD1: geometry built inside `with piece.detail():` is LOD0-only (mullions,
handles, trims ...). LOD1 = LOD0 minus detail faces, then decimated.

GLASS under URP: a material whose palette entry carries an alpha below 1 exports as glTF
alphaMode BLEND, which glTFast turns into a transparent URP material. The glass is therefore a
plain alpha-blended dielectric (no transmission extension, which URP would need an opaque-texture
pass for). The zone tint volumes use the same mechanism.

The module imports outside Blender too (bpy missing) so layout/check helpers can run under the
system python.
"""
import math, os, sys, json, contextlib

try:
    import bpy, bmesh
    from mathutils import Vector, Matrix, Quaternion
except ImportError:          # system python: only the pure helpers are usable
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
# palette + shared material library (one set for every model -> SRP batching friendly)
# values are LINEAR RGB (Blender / glTF baseColorFactor); an optional 7th entry is alpha
# ==================================================================================
PALETTE = {
    #  key          material name           colour                 rough metal emission alpha
    "glass":      ("M_Glass",            (0.10, 0.16, 0.20),     0.05, 0.0, None, 0.30),
    "frame":      ("M_Frame_Graphite",   (0.040, 0.044, 0.050),  0.45, 0.6, None),
    "spandrel":   ("M_Spandrel",         (0.060, 0.075, 0.090),  0.40, 0.3, None),
    "concrete":   ("M_Concrete",         (0.40, 0.39, 0.36),     0.92, 0.0, None),
    "concrete_d": ("M_Concrete_Dark",    (0.17, 0.17, 0.165),    0.92, 0.0, None),
    "plaster":    ("M_Plaster_Warm",     (0.62, 0.60, 0.55),     0.85, 0.0, None),
    "roofing":    ("M_Roofing",          (0.075, 0.078, 0.082),  0.90, 0.0, None),
    "steel":      ("M_Steel_Brushed",    (0.55, 0.56, 0.58),     0.38, 0.85, None),
    "steel_d":    ("M_Steel_Dark",       (0.030, 0.032, 0.036),  0.50, 0.4, None),
    "unit":       ("M_Unit_Grey",        (0.36, 0.38, 0.40),     0.55, 0.2, None),
    "unit_blue":  ("M_Unit_Teal",        (0.040, 0.250, 0.300),  0.50, 0.1, None),
    "louvre":     ("M_Louvre",           (0.020, 0.022, 0.026),  0.60, 0.3, None),
    "blade":      ("M_Fan_Blade",        (0.70, 0.72, 0.74),     0.40, 0.5, None),
    "pad":        ("M_Pad_Concrete",     (0.28, 0.28, 0.27),     0.95, 0.0, None),
    # the zone tint volume: pale, alpha-blended; Unity tints it per zone with a property block
    "zone":       ("M_ZoneTint",         (0.85, 0.90, 0.95),     0.90, 0.0, None, 0.22),
    # furniture
    "wood":       ("M_Wood_Light",       (0.42, 0.27, 0.14),     0.70, 0.0, None),
    "wood_d":     ("M_Wood_Dark",        (0.12, 0.075, 0.045),   0.70, 0.0, None),
    "fabric":     ("M_Fabric_Charcoal",  (0.045, 0.050, 0.060),  0.90, 0.0, None),
    "white":      ("M_Laminate_White",   (0.70, 0.70, 0.68),     0.55, 0.0, None),
    "screen":     ("M_Screen",           (0.010, 0.014, 0.020),  0.20, 0.0, None),
    # people
    "skin_a":     ("M_Skin_A",           (0.42, 0.26, 0.18),     0.70, 0.0, None),
    "skin_b":     ("M_Skin_B",           (0.62, 0.42, 0.30),     0.70, 0.0, None),
    "shirt_a":    ("M_Shirt_Teal",       (0.030, 0.230, 0.260),  0.85, 0.0, None),
    "shirt_b":    ("M_Shirt_Sand",       (0.560, 0.420, 0.250),  0.85, 0.0, None),
    "trousers_a": ("M_Trousers_Navy",    (0.018, 0.024, 0.050),  0.85, 0.0, None),
    "trousers_b": ("M_Trousers_Grey",    (0.090, 0.092, 0.098),  0.85, 0.0, None),
    "hair_a":     ("M_Hair_Dark",        (0.012, 0.009, 0.007),  0.80, 0.0, None),
    "hair_b":     ("M_Hair_Auburn",      (0.130, 0.040, 0.016),  0.80, 0.0, None),
    "shoe":       ("M_Shoe",             (0.020, 0.020, 0.022),  0.70, 0.0, None),
    # campus ground
    "lawn":       ("M_Lawn",             (0.060, 0.115, 0.050),  0.95, 0.0, None),
    "paving":     ("M_Paving_Light",     (0.50, 0.49, 0.46),     0.90, 0.0, None),
    "paving_d":   ("M_Paving_Dark",      (0.22, 0.22, 0.215),    0.90, 0.0, None),
    "asphalt":    ("M_Asphalt",          (0.045, 0.047, 0.050),  0.95, 0.0, None),
    "trunk":      ("M_Trunk",            (0.075, 0.052, 0.036),  0.90, 0.0, None),
    "leaf_a":     ("M_Foliage_A",        (0.050, 0.110, 0.045),  0.85, 0.0, None),
    "leaf_b":     ("M_Foliage_B",        (0.080, 0.130, 0.050),  0.85, 0.0, None),
    "lamp":       ("M_Light_Warm",       (1.00, 0.78, 0.45),     0.30, 0.0, 3.0),
}

S = dict(coll=None, nodes={}, mats={}, out=None, name=None)
NODES = S["nodes"]


def reset(name, out):
    """Empty scene + a collection for the model. Call first."""
    bpy.ops.wm.read_factory_settings(use_empty=True)
    sc = bpy.context.scene
    sc.unit_settings.system = "METRIC"
    S["coll"] = bpy.data.collections.new(name.capitalize())
    sc.collection.children.link(S["coll"])
    S["nodes"].clear(); S["mats"].clear()
    S["out"], S["name"] = out, name
    os.makedirs(out, exist_ok=True)


def mat(key):
    """Shared material by palette key (created on first use)."""
    if key in S["mats"]:
        return S["mats"][key]
    name, col, rough, metal, emit, *rest = PALETTE[key]
    alpha = rest[0] if rest else 1.0
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
    if alpha < 1.0:
        b.inputs["Alpha"].default_value = alpha          # exports as alphaMode BLEND
        m.surface_render_method = "BLENDED"
        m.use_backface_culling = True
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
        self.vcol = None          # optional fn(vertex (Unity frame), face centre) -> (r, g, b)
        self.one_material = None  # optional palette key: bake each face's colour into its vertex
                                  # colours and draw the whole piece with this one material

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
            edges = list(dict.fromkeys(e for v in verts for e in v.link_edges))      # ordered, not a set
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
        faces = list(dict.fromkeys(f for v in verts for f in v.link_faces))
        self._finish(verts, faces, key, bevel)
        self._tag(b0)

    def ellipsoid(self, c, radii, key, subdiv=1):
        """A faceted ellipsoid (subdiv 1 = 80 triangles, 2 = 320 -- an icosphere): heads, foliage."""
        b0 = self._op()
        res = bmesh.ops.create_icosphere(self.bm, subdivisions=subdiv, radius=1.0)
        verts = res["verts"]
        for v in verts:
            v.co = Vector((c[0] + v.co.x * radii[0], c[1] + v.co.y * radii[1], c[2] + v.co.z * radii[2]))
        faces = list(dict.fromkeys(f for v in verts for f in v.link_faces))
        self._finish(verts, faces, key, 0)
        self._tag(b0)

    def ring(self, c, r_out, r_in, h, key, seg=None):
        """A vertical annulus (a fan shroud, a planter): outer wall, top and bottom faces, inner wall.
        c is the centre of the base."""
        b0 = self._op()
        seg = seg or self.seg
        x, y, z = c

        def rim(r, yy):
            return [self.bm.verts.new((x + r * math.cos(2 * math.pi * i / seg), yy, z + r * math.sin(2 * math.pi * i / seg)))
                    for i in range(seg)]
        ob, ot, it, ib = rim(r_out, y), rim(r_out, y + h), rim(r_in, y + h), rim(r_in, y)
        faces = []
        for i in range(seg):
            j = (i + 1) % seg
            for a, b in ((ob, ot), (ot, it), (it, ib), (ib, ob)):
                faces.append(self.bm.faces.new((a[i], a[j], b[j], b[i])))
        self._finish(ob + ot + it + ib, faces, key, 0)
        self._tag(b0)

    def tube(self, pts, r, key="steel", seg=6):
        """Handrail / hose polyline: a cylinder per segment plus a small knuckle at each bend."""
        for a, b in zip(pts, pts[1:]):
            self.cyl(a, b, r, key, seg)
        for p in pts[1:-1]:
            self.cyl((p[0], p[1] - r, p[2]), (p[0], p[1] + r, p[2]), r * 1.25, key, seg)

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


def _activate_colors(me):
    """The glTF exporter writes the ACTIVE colour attribute as COLOR_0 (and any other as COLOR_1):
    make the piece's vertex colours the active ones."""
    if "Color" in me.color_attributes:
        me.color_attributes.active_color_name = "Color"
        me.color_attributes.render_color_index = me.color_attributes.find("Color")


def _srgb(c):
    c = max(0.0, min(1.0, c))
    return 12.92 * c if c <= 0.0031308 else 1.055 * c ** (1 / 2.4) - 0.055


def canonical_order(bm):
    """Put the faces and vertices in an order that depends only on the geometry. Without it the
    same model came out of two builds with its faces in a different order and some polygons
    started at a different corner (so cut into different triangles): the same surfaces, the same
    look, but a different file. With it a rebuild is byte-identical."""
    def r(v):
        return tuple(round(c, 5) for c in v)
    # each face starts at its lowest corner, so a polygon is cut into the same triangles: a
    # rebuilt face may start anywhere round its loop, and the triangles follow the start
    color_layers = [lay for coll in (bm.loops.layers.color, bm.loops.layers.float_color) for lay in coll.values()]
    uv_layers = list(bm.loops.layers.uv.values())
    face_layers = [lay for coll in (bm.faces.layers.int, bm.faces.layers.float) for lay in coll.values()]
    for f in list(bm.faces):
        loops = list(f.loops)
        k = min(range(len(loops)), key=lambda i: r(loops[i].vert.co))
        if k == 0:
            continue
        verts = [lp.vert for lp in loops[k:] + loops[:k]]
        data = {lp.vert: ([tuple(lp[lay]) for lay in color_layers], [tuple(lp[lay].uv) for lay in uv_layers])
                for lp in loops}
        fdata = [f[lay] for lay in face_layers]
        mat, smooth = f.material_index, f.smooth
        bm.faces.remove(f)
        g = bm.faces.new(verts)
        g.material_index, g.smooth = mat, smooth
        for lay, val in zip(face_layers, fdata):
            g[lay] = val
        for lp in g.loops:
            cols, uvs = data[lp.vert]
            for lay, val in zip(color_layers, cols):
                lp[lay] = val
            for lay, val in zip(uv_layers, uvs):
                lp[lay].uv = val
    bm.normal_update()
    # BMesh sorts by a number, so rank the elements first
    vrank = {v: i for i, v in enumerate(sorted(bm.verts, key=lambda v: r(v.co)))}
    bm.verts.sort(key=lambda v: vrank[v])
    frank = {f: i for i, f in enumerate(sorted(bm.faces, key=lambda f: (
        f.material_index, r(f.calc_center_median()), r(f.normal), tuple(sorted(r(v.co) for v in f.verts)))))}
    bm.faces.sort(key=lambda f: frank[f])
    bm.verts.index_update()
    bm.faces.index_update()


def attach(name, piece, local_blender=False, smooth_angle=35):
    """Turn a Piece into <name>_LOD0 under node <name> (vertices in the node's local frame)."""
    n = NODES[name]
    bm = piece.bm
    if piece.vcol is not None:
        # vertex colours (corner colours), evaluated in the authoring frame before it is converted
        cl = bm.loops.layers.color.new("Color")
        tint = {}
        if piece.one_material:
            # the face's own palette colour goes into the vertex colour (linear -> the sRGB a byte
            # colour attribute stores), and every face moves to the one material
            for i, m in enumerate(piece.mats):
                key = next(k for k, v in PALETTE.items() if v[0] == m.name)
                tint[i] = PALETTE[key][1]
        for f in bm.faces:
            fc = f.calc_center_median()
            base = tint.get(f.material_index, (1.0, 1.0, 1.0))
            for lp in f.loops:
                r, g, b = piece.vcol(lp.vert.co, fc)
                lin = (base[0] * r, base[1] * g, base[2] * b)
                # a byte colour attribute stores sRGB; the glTF exporter writes it back as linear
                lp[cl] = (*(_srgb(c) for c in (lin if piece.one_material else (r, g, b))), 1.0)
        if piece.one_material:
            one = piece.mi(piece.one_material)
            for f in bm.faces:
                f.material_index = one
            piece.mats = [piece.mats[one]]
            for f in bm.faces:
                f.material_index = 0
    if not local_blender:
        inv = n.matrix_world.inverted()
        for v in bm.verts:
            v.co = inv @ U(*v.co)
    bmesh.ops.remove_doubles(bm, verts=bm.verts, dist=1e-5)
    bmesh.ops.recalc_face_normals(bm, faces=bm.faces)
    canonical_order(bm)
    me = bpy.data.meshes.new(name + "_LOD0_Mesh")
    bm.to_mesh(me); bm.free()
    _activate_colors(me)
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

# ==================================================================================
# LOD1, report, export
# ==================================================================================
def tris(me):
    return sum(len(p.vertices) - 2 for p in me.polygons)


def lod_objects(lod):
    return [o for o in S["coll"].objects if o.type == "MESH" and o.name.endswith("_" + lod)]


def make_lod1(ratio=0.45, keep_full=(), planar=False):
    """LOD1 = LOD0 minus detail-tagged faces, collapse-decimated to `ratio` (nodes in keep_full
    keep their stripped mesh undecimated -- e.g. tiny parts decimation would destroy). With
    planar, faces that lie in one plane are merged instead (a planar dissolve of 5 degrees), which
    keeps every edge of a boxy structure. Collapse decimation gave the plant (many repeated
    railings, ladders and trusses) a different LOD1 from one build to the next, even in a fresh,
    single-threaded Blender; a planar dissolve gives the same one every time."""
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
            if planar:
                mod.decimate_type = "DISSOLVE"
                mod.angle_limit = math.radians(5.0)
            else:
                mod.ratio = ratio
                mod.use_collapse_triangulate = True
            dg = bpy.context.evaluated_depsgraph_get()
            nm = bpy.data.meshes.new_from_object(o1.evaluated_get(dg))
            o1.modifiers.clear(); o1.data = nm; nm.name = o1.name + "_Mesh"
        _activate_colors(o1.data)
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
        has_colors = any(len(o.data.color_attributes) for o in lod_objects(lod))
        bpy.ops.export_scene.gltf(filepath=fn, export_format="GLB", use_selection=True,
                                  export_yup=True, export_apply=True, export_animations=False,
                                  export_cameras=False, export_lights=False,
                                  export_vertex_color="ACTIVE" if has_colors else "MATERIAL")
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
