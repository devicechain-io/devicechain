# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Builds the DeviceChain mark as a 3D object, and the wordmark as flat geometry, from the official
brand files, and exports them as one glTF binary for the intro package.

    blender --background --python build_mark.py -- [--logos DIR] [--out FILE]

--logos defaults to the repository's branding/logos directory; --out defaults to
../Runtime/Models/devicechain_mark.glb.

THE MARK. branding/logos/symbol.svg is drawn as an object seen straight on: a hexagonal frame
cut into six bevelled facets around a cube seen corner-on. This script reads the file's own
polygons and colours and lifts them into 3D without moving any point in the picture plane:

  * the frame: each facet keeps its SVG outline; points on the outer hexagon stay in the picture
    plane, points on the inner hexagon come forward by BEVEL, so each facet is a slanted face.
    The frame has outer and inner walls and a back, coloured with the base shape's colour.
  * the cube: an isometric cube whose projection is exactly the drawn one (edge length
    25.98 * sqrt(3/2) SVG units), with its corner nearest the viewer at the hexagon's centre.
    The three drawn faces (the top split in two tones, as drawn) keep their colours; the three
    faces the drawing cannot show use the colour of the cube's underlay hexagon.

Seen by an orthographic camera looking along the frame's axis, with flat (unlit) shading, the
frame and cube reproduce symbol.svg polygon for polygon. Face colours are written as vertex
colours (linear, as glTF requires); the intro's shader crossfades from lit shading to exactly
these colours at the lock.

THE WORDMARK is imported from branding/logos/logo.svg with Blender's SVG importer and placed
where that file places it relative to the mark, so the lockup is the file's own lockup.

AUTHORING FRAME (Unity terms): 1 unit = 100 SVG units; X right, Y up, the viewer on -Z looking
+Z; origin at the centre of the mark's hexagon (SVG 175, 118.285). U() converts to Blender; the
glTF exporter (+Y up) and glTFast turn it back with no root rotation.

NODES: DeviceChainMark (root) > Frame, Cube, Wordmark. Each mesh's pivot is the origin; the Cube
spins about it.
"""
import math, os, re, sys, json

import bpy, bmesh
from mathutils import Vector

HERE = os.path.dirname(os.path.abspath(__file__))
SCALE = 0.01                      # Unity units per SVG unit
CX, CY = 175.0, 118.285           # the hexagon's centre in symbol.svg
BEVEL = 8.0                       # how far the frame's inner edge stands forward (SVG units)
DEPTH = 12.0                      # how far the frame's back sits behind the picture plane
R_OUT, R_IN, R_CUBE = 51.965, 30.565, 25.98   # circumradii of the three hexagons
CURVE_RES = 24                    # curve resolution for the wordmark's round letters
CHAMFER = 0.55                    # the moving mark's chamfer, SVG units (about 0.6 % of its width)


def args():
    a = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    o = {"logos": os.path.normpath(os.path.join(HERE, "..", "..", "..", "..", "branding", "logos")),
         "out": os.path.normpath(os.path.join(HERE, "..", "Runtime", "Models", "devicechain_mark.glb"))}
    for i in range(0, len(a) - 1, 2):
        o[a[i].lstrip("-")] = a[i + 1]
    return o


def U(x, y, z):
    """Unity (x right, y up, z away from the viewer) -> Blender."""
    return Vector((-x, -z, y))


def lin(hexstr):
    """sRGB hex -> linear RGBA, the space glTF vertex colours are in."""
    h = hexstr.lstrip("#")
    c = [int(h[i:i + 2], 16) / 255.0 for i in (0, 2, 4)]
    return tuple(v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4 for v in c) + (1.0,)


def svg_shapes(path):
    """The symbol's shapes in paint order: (fill, [(x, y), ...]) for each polygon, and the base
    path's outer outline. Only the commands symbol.svg uses are understood; anything else fails."""
    text = open(path, encoding="utf-8").read()
    shapes = []
    for m in re.finditer(r'<(polygon|path)\s+fill="(#[0-9A-Fa-f]{6})"\s+(points|d)="([^"]+)"', text):
        kind, fill, _, data = m.groups()
        if kind == "polygon":
            nums = [float(v) for v in re.split(r"[\s,]+", data.strip())]
            pts = list(zip(nums[0::2], nums[1::2]))
        else:
            pts = path_outline(data)
        clean = []
        for p in pts:                         # the file repeats points; keep each corner once
            if not clean or math.dist(clean[-1], p) > 1e-6:
                clean.append(p)
        if math.dist(clean[0], clean[-1]) < 1e-6:
            clean.pop()
        if kind == "polygon":                 # drop points that lie on a straight edge
            clean = [p for i, p in enumerate(clean)
                     if abs(cross2(clean[i - 1], p, clean[(i + 1) % len(clean)])) > 1e-3]
        shapes.append((fill.upper(), clean))
    if len(shapes) != 12:
        raise SystemExit(f"expected 12 shapes in symbol.svg, found {len(shapes)}")
    return shapes


def cross2(a, b, c):
    return (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0])


def path_outline(d):
    """First subpath of an SVG path built from M/m, L/l, H/h, V/v and Z/z."""
    toks = re.findall(r"[MmLlHhVvZz]|-?\d*\.?\d+(?:e-?\d+)?", d)
    pts, cur, cmd, i = [], (0.0, 0.0), None, 0
    while i < len(toks):
        t = toks[i]
        if t.isalpha():
            cmd = t
            i += 1
            if cmd in "Zz":
                return pts
            continue
        if cmd in "MLml":
            x, y = float(toks[i]), float(toks[i + 1])
            i += 2
            cur = (cur[0] + x, cur[1] + y) if cmd.islower() else (x, y)
        elif cmd in "Hh":
            x = float(toks[i]); i += 1
            cur = (cur[0] + x if cmd == "h" else x, cur[1])
        elif cmd in "Vv":
            y = float(toks[i]); i += 1
            cur = (cur[0], cur[1] + y if cmd == "v" else y)
        else:
            raise SystemExit(f"unsupported path command {cmd}")
        pts.append(cur)
    return pts


def radius(p):
    return math.hypot(p[0] - CX, p[1] - CY)


def plane(p, z):
    """SVG point -> Unity point in the picture plane, z forward (negative = toward the viewer)."""
    return ((p[0] - CX) * SCALE, -(p[1] - CY) * SCALE, z * SCALE)


class MeshBuilder:
    def __init__(self):
        self.verts, self.faces, self.colors = [], [], []
        self.deltas = []

    def face(self, pts, color, deltas=None):
        base = len(self.verts)
        self.verts.extend(pts)
        self.deltas.extend(deltas or [(0.0, 0.0, 0.0)] * len(pts))
        self.faces.append(list(range(base, base + len(pts))))
        self.colors.append(color)

    def build(self, name, parent, material):
        me = bpy.data.meshes.new(name)
        me.from_pydata([U(*p) for p in self.verts], [], self.faces)
        me.update()
        attr = me.color_attributes.new("Col", "FLOAT_COLOR", "CORNER")
        for poly, col in zip(me.polygons, self.colors):
            for li in poly.loop_indices:
                attr.data[li].color = col
        me.color_attributes.active_color = attr
        # the chamfer (see chamfer()): each vertex's offset in the authoring frame, in two UV
        # sets, (dx, dy) and (dz, 0); the glTF exporter flips V and glTFast flips it back
        uv0 = me.uv_layers.new(name="UVMap")
        uvxy = me.uv_layers.new(name="ChamferXY")
        uvz = me.uv_layers.new(name="ChamferZ")
        for poly in me.polygons:
            for li in poly.loop_indices:
                d = self.deltas[me.loops[li].vertex_index]
                uv0.data[li].uv = (0.0, 0.0)
                uvxy.data[li].uv = (d[0], d[1])
                uvz.data[li].uv = (d[2], 0.0)
        # flat faces; the chamfer's strips and corners have no area in the drawing's shape, so
        # their normals here mean nothing: the intro's shader takes every normal from the
        # surface it draws (screen-space derivatives), chamfered or not
        for poly in me.polygons:
            poly.use_smooth = False
        me.materials.append(material)
        ob = bpy.data.objects.new(name, me)
        bpy.context.collection.objects.link(ob)
        ob.parent = parent
        return ob


def newell(pts):
    """The right-handed normal of a planar polygon in the authoring frame (unnormalised)."""
    n = Vector()
    for i, a in enumerate(pts):
        b = pts[(i + 1) % len(pts)]
        n += Vector(((a[1] - b[1]) * (a[2] + b[2]), (a[2] - b[2]) * (a[0] + b[0]), (a[0] - b[0]) * (a[1] + b[1])))
    return n


def chamfer(mb, size, sharp_degrees=5.0):
    """A chamfer that exists only while the mark moves. Every sharp edge (faces meeting at more
    than sharp_degrees) gets a strip of width about `size`, every corner where sharp edges meet a
    small polygon, and each face is inset to make room. The geometry is written in the drawing's
    shape: the strips and corners have zero area and every face is exactly where it was; the
    inset lives in each vertex's offset (MeshBuilder.deltas), which the intro's shader adds
    times _Chamfer. At 0, at the lock, the mark is the drawing; at 1 its edges catch the light.

    Faces are wound so that the authoring frame's right-handed normal points INTO the solid
    (Unity is left-handed); outward normals are the negated Newell normals."""
    reps = []

    def key(p):
        # the drawing's points and the computed hexagons differ by up to 0.01 SVG units, so
        # points are welded within a tolerance, never by exact equality
        for r in reps:
            if (r - p).length < 5e-4:
                return tuple(r)
        reps.append(Vector(p))
        return tuple(p)

    faces = []
    for f, col in zip(mb.faces, mb.colors):
        pts = [Vector(mb.verts[i]) for i in f]
        faces.append({"pts": pts, "keys": [key(p) for p in pts], "col": col, "n": -newell(pts).normalized()})
    edges = {}
    for fi, f in enumerate(faces):
        k = f["keys"]
        for i in range(len(k)):
            a, b = k[i], k[(i + 1) % len(k)]
            edges.setdefault((min(a, b), max(a, b)), []).append(fi)
    cos_sharp = math.cos(math.radians(sharp_degrees))

    def sharp(a, b):
        fs = edges.get((min(a, b), max(a, b)), [])
        return len(fs) == 2 and faces[fs[0]]["n"].dot(faces[fs[1]]["n"]) < cos_sharp

    inset = {}                                     # (face, vertex key) -> chamfered position
    own = {}                                       # (face, vertex key) -> the face's own point
    for fi, f in enumerate(faces):
        pts, k = f["pts"], f["keys"]
        m = len(pts)
        for i in range(m):
            own[(fi, k[i])] = pts[i]
            v, prev, nxt = pts[i], pts[i - 1], pts[(i + 1) % m]
            u, w = (prev - v).normalized(), (nxt - v).normalized()
            sin = u.cross(w).length
            o_prev = size if sharp(k[i - 1], k[i]) else 0.0
            o_next = size if sharp(k[i], k[(i + 1) % m]) else 0.0
            inset[(fi, k[i])] = v + u * (o_next / sin) + w * (o_prev / sin)

    lum = lambda c: 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]
    out = MeshBuilder()

    def emit(base, moved, col, n_out):
        loop = list(zip(base, moved))
        # wind so the (chamfered) polygon's outward normal agrees with n_out
        if (-newell([p for _, p in loop])).dot(n_out) < 0:
            loop.reverse()
        out.face([tuple(b) for b, _ in loop], col, [tuple(p - b) for b, p in loop])

    for fi, f in enumerate(faces):
        emit(f["pts"], [inset[(fi, k)] for k in f["keys"]], f["col"], f["n"])
    for (a, b), fs in edges.items():
        if not sharp(a, b):
            continue
        f, g = fs
        n = (faces[f]["n"] + faces[g]["n"]).normalized()
        col = max(faces[f]["col"], faces[g]["col"], key=lum)
        emit([own[(f, a)], own[(f, b)], own[(g, b)], own[(g, a)]],
             [inset[(f, a)], inset[(f, b)], inset[(g, b)], inset[(g, a)]], col, n)
    corners = {}
    for fi, f in enumerate(faces):
        for k in f["keys"]:
            corners.setdefault(k, []).append(fi)
    sharp_at = set()
    for a, b in edges:
        if sharp(a, b):
            sharp_at.update((a, b))
    for k, fs in corners.items():
        if k not in sharp_at:
            continue
        n = sum((faces[fi]["n"] for fi in fs), Vector()).normalized()
        pts, base = [], []
        for fi in fs:
            p = inset[(fi, k)]
            if all((p - q).length > 1e-7 for q in pts):
                pts.append(p)
                base.append(own[(fi, k)])
        if len(pts) < 3:
            continue
        # order the corner's points round its normal
        c = sum(pts, Vector()) / len(pts)
        ref = (pts[0] - c).normalized()
        side = n.cross(ref)
        order = sorted(range(len(pts)), key=lambda i: math.atan2((pts[i] - c).dot(side), (pts[i] - c).dot(ref)))
        col = max((faces[fi]["col"] for fi in fs), key=lum)
        emit([base[i] for i in order], [pts[i] for i in order], col, n)
    return out


def facing_viewer(pts):
    """Order a planar polygon's points so its normal points at the viewer (Unity -Z)."""
    a, b, c = (Vector(p) for p in pts[:3])
    n = (b - a).cross(c - a)
    if abs(n.length) < 1e-12:
        return pts
    # Unity is left-handed, Blender right-handed: after U() a counter-clockwise loop seen from
    # the viewer in Blender is what glTF and Unity treat as front-facing
    return pts if n.z > 0 else list(reversed(pts))


def outward(pts, centre):
    """Order a planar polygon so its normal points away from centre."""
    a, b, c = (Vector(p) for p in pts[:3])
    n = (b - a).cross(c - a)
    mid = sum((Vector(p) for p in pts), Vector()) / len(pts)
    return pts if n.dot(mid - Vector(centre)) < 0 else list(reversed(pts))


def build_frame(shapes, base_fill):
    mb = MeshBuilder()
    facets = [(f, pts) for f, pts in shapes[1:] if max(radius(p) for p in pts) > R_OUT - 0.6]
    if len(facets) != 6:
        raise SystemExit(f"expected 6 frame facets, found {len(facets)}")
    for fill, pts in facets:
        q = []
        for p in pts:
            r = radius(p)
            if abs(r - R_OUT) < 0.6:
                q.append(plane(p, 0.0))
            elif abs(r - R_IN) < 0.6:
                q.append(plane(p, -BEVEL))
            else:
                raise SystemExit(f"frame point {p} is on neither hexagon (r={r:.2f})")
        mb.face(facing_viewer(q), lin(fill))
    hexo = [(CX + R_OUT * math.sin(math.radians(60 * k)), CY - R_OUT * math.cos(math.radians(60 * k))) for k in range(6)]
    hexi = [(CX + R_IN * math.sin(math.radians(60 * k)), CY - R_IN * math.cos(math.radians(60 * k))) for k in range(6)]
    base = lin(base_fill)
    for k in range(6):
        a, b = hexo[k], hexo[(k + 1) % 6]
        wall = [plane(a, 0.0), plane(b, 0.0), plane(b, DEPTH), plane(a, DEPTH)]
        mb.face(outward(wall, (0, 0, DEPTH * SCALE / 2)), base)
        a, b = hexi[k], hexi[(k + 1) % 6]
        wall = [plane(a, -BEVEL), plane(b, -BEVEL), plane(b, DEPTH), plane(a, DEPTH)]
        mb.face(list(reversed(outward(wall, (0, 0, 0)))), base)
        back = [plane(hexo[k], DEPTH), plane(hexo[(k + 1) % 6], DEPTH), plane(hexi[(k + 1) % 6], DEPTH), plane(hexi[k], DEPTH)]
        mb.face(list(reversed(facing_viewer(back))), base)
    return mb


def build_cube(shapes):
    """An isometric cube whose projection is the drawn one. Edges leave the nearest corner F
    toward the drawn up-left, up-right and down corners, each going back by the same depth."""
    proj = R_CUBE                                       # projected edge length (SVG units)
    edge = proj * math.sqrt(1.5)
    back = math.sqrt(edge * edge - proj * proj)         # depth each edge recedes by
    dirs = [(-proj * math.cos(math.radians(30)), proj * 0.5), (proj * math.cos(math.radians(30)), proj * 0.5), (0.0, -proj)]
    e = [Vector((dx * SCALE, dy * SCALE, back * SCALE)) for dx, dy in dirs]
    F = Vector((0.0, 0.0, -1.5 * back * SCALE))          # cube centre at the origin
    corner = {}
    for i in (0, 1):
        for j in (0, 1):
            for k in (0, 1):
                corner[(i, j, k)] = F + e[0] * i + e[1] * j + e[2] * k

    def svg_of(v):
        return (CX + v.x / SCALE, CY - v.y / SCALE)

    visible = {key: v for key, v in corner.items() if key != (1, 1, 1)}   # (1,1,1) is hidden behind F

    def lift(p):
        best = min(visible.items(), key=lambda kv: math.dist(svg_of(kv[1]), p))
        if math.dist(svg_of(best[1]), p) > 0.05:
            raise SystemExit(f"cube point {p} is not a corner of the isometric cube")
        return tuple(best[1])

    mb = MeshBuilder()
    cube_shapes = [(f, pts) for f, pts in shapes if max(radius(p) for p in pts) < R_CUBE + 0.5]
    underlay = [s for s in cube_shapes if min(radius(p) for p in s[1]) > R_CUBE - 0.5]
    drawn = [s for s in cube_shapes if s not in underlay]
    if len(underlay) != 1 or len(drawn) != 4:
        raise SystemExit(f"expected the cube underlay and 4 drawn cube faces, found {len(underlay)} and {len(drawn)}")
    for fill, pts in drawn:
        mb.face(facing_viewer([lift(p) for p in pts]), lin(fill))
    hidden = lin(underlay[0][0])
    centre = (0.0, 0.0, 0.0)
    for axis in range(3):                               # the three faces that touch the far corner
        others = [a for a in range(3) if a != axis]
        loop = [tuple(1 if a == axis else 0 for a in range(3))]
        for step in ((others[0],), (others[0], others[1]), (others[1],)):
            loop.append(tuple(1 if (a == axis or a in step) else 0 for a in range(3)))
        mb.face(outward([tuple(corner[k]) for k in loop], centre), hidden)
    return mb


def build_wordmark(logo_svg, material):
    """Imports logo.svg, keeps the wordmark (everything below the mark) and maps it back into the
    authoring frame using the importer's own scale (90 dpi user units, y measured from the
    bottom of the viewBox)."""
    text = open(logo_svg, encoding="utf-8").read()
    vb = [float(v) for v in re.search(r'viewBox="([^"]+)"', text).group(1).split()]
    k = 0.0254 / 90.0
    before = set(bpy.data.objects)
    bpy.ops.import_curve.svg(filepath=logo_svg)
    imported = [o for o in bpy.data.objects if o not in before]
    keep = []
    fills = re.findall(r'fill="(#[0-9A-Fa-f]{6})"', text)
    for o in imported:
        ys = [(o.matrix_world @ Vector(c)).y for c in o.bound_box]
        svg_top = vb[3] - max(ys) / k
        if svg_top > CY + R_OUT + 1.0:                  # below the mark: a wordmark letter
            keep.append(o)
        else:
            bpy.data.objects.remove(o)
    if not keep:
        raise SystemExit("no wordmark found in logo.svg")
    mb = MeshBuilder()
    for o in keep:
        o.data.resolution_u = CURVE_RES
        o.data.fill_mode = "BOTH"
        lin_imported = tuple(o.active_material.diffuse_color[:3])
        # the importer converts the file's hex to linear floats; snap back to the exact hex
        fill = min(set(fills), key=lambda h: sum(abs(a - b) for a, b in zip(lin(h)[:3], lin_imported)))
        dg = bpy.context.evaluated_depsgraph_get()
        me = bpy.data.meshes.new_from_object(o.evaluated_get(dg))
        bm = bmesh.new()
        bm.from_mesh(me)
        bmesh.ops.triangulate(bm, faces=bm.faces[:])
        for f in bm.faces:
            pts = []
            for v in f.verts:
                w = o.matrix_world @ v.co
                sx, sy = w.x / k, vb[3] - w.y / k
                pts.append(plane((sx, sy), 0.0))
            mb.face(facing_viewer(pts), lin(fill))
        bm.free()
        bpy.data.meshes.remove(me)
        bpy.data.objects.remove(o)
    return mb


def main():
    o = args()
    bpy.ops.wm.read_factory_settings(use_empty=True)
    symbol = os.path.join(o["logos"], "symbol.svg")
    logo = os.path.join(o["logos"], "logo.svg")
    shapes = svg_shapes(symbol)
    base_fill = shapes[0][0]

    mat = bpy.data.materials.new("DeviceChainMark")
    mat.use_nodes = True
    root = bpy.data.objects.new("DeviceChainMark", None)
    bpy.context.collection.objects.link(root)

    frame = chamfer(build_frame(shapes, base_fill), CHAMFER * SCALE).build("Frame", root, mat)
    cube = chamfer(build_cube(shapes), CHAMFER * SCALE).build("Cube", root, mat)
    word = build_wordmark(logo, mat).build("Wordmark", root, mat)

    report = {}
    for ob in (frame, cube, word):
        xs = [-(v.co.x) for v in ob.data.vertices]
        ys = [v.co.z for v in ob.data.vertices]
        report[ob.name] = {"triangles": sum(len(p.vertices) - 2 for p in ob.data.polygons),
                           "x": [round(min(xs), 4), round(max(xs), 4)], "y": [round(min(ys), 4), round(max(ys), 4)]}

    os.makedirs(os.path.dirname(o["out"]), exist_ok=True)
    for ob in bpy.data.objects:
        ob.select_set(True)
    bpy.ops.export_scene.gltf(filepath=o["out"], export_format="GLB", use_selection=True,
                              export_yup=True, export_apply=True, export_animations=False,
                              export_cameras=False, export_lights=False, export_normals=True,
                              export_vertex_color="ACTIVE", export_materials="PLACEHOLDER")
    print("MARK " + json.dumps(report))
    print("MARK wrote " + o["out"])


main()
