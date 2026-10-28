# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Cycles (CPU) preview renders of the campus, assembled from the built .glb files.

  blender --background --python preview.py -- [hero] [closeup] [roof] [--models DIR] [--out DIR]
                                              [--samples N] [--res WxH]

Imports the LOD0 models from --models (default ./out, where build.py writes them), places them as
`campus_layout.json` says (Unity frame -> Blender frame through the kit's U()), cuts the south and
east facades away, tints the zones by a made-up temperature (the hot zone is Tower F4-East), seats
people and lights the scene as dusk. Writes <shot>.png into --out (default ./out), which git ignores.

This is a look check for the art, not the Unity scene: the tint here is one colour ramp and the
light is Cycles'. What it does prove is that the committed models import, line up, and read.
"""
import sys, os, math, random, re

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import bpy                                  # noqa: E402
from mathutils import Matrix, Vector, Euler  # noqa: E402

import towerpulse_kit as K                  # noqa: E402
import layout as L                          # noqa: E402

CUT = ("South", "East")


def place_matrix(x, y, z, yaw):
    return Matrix.Translation(K.U(x, y, z)) @ Matrix.Rotation(-math.radians(yaw), 4, "Z")


def import_glb(models, name):
    before = set(bpy.data.objects)
    bpy.ops.import_scene.gltf(filepath=os.path.join(models, name + ".glb"))
    new = [o for o in bpy.data.objects if o not in before]
    return {re.sub(r"\.\d+$", "", o.name): o for o in new}, new


def import_root(models, name, matrix):
    objs, new = import_glb(models, name)
    root = next(o for o in new if o.name.startswith("Root") and o.parent is None)
    root.matrix_world = matrix @ root.matrix_world
    return objs, new


def tint_colour(t):
    """Made-up ramp: cool teal at 21 C, neutral at 24, amber at 27, red-orange beyond."""
    stops = [(21.0, (0.05, 0.35, 0.60)), (24.0, (0.30, 0.60, 0.40)), (26.0, (0.95, 0.60, 0.12)), (28.0, (0.95, 0.20, 0.06))]
    if t <= stops[0][0]:
        return stops[0][1]
    for (t0, c0), (t1, c1) in zip(stops, stops[1:]):
        if t <= t1:
            f = (t - t0) / (t1 - t0)
            return tuple(c0[i] + (c1[i] - c0[i]) * f for i in range(3))
    return stops[-1][1]


def zone_temp(zid, rng):
    if zid == "tower-f4-east":
        return 27.4
    return 22.0 + 2.6 * rng.random()


def set_tint(obj, rgb, alpha=0.20, glow=2.2):
    m = bpy.data.materials.new("Tint_" + obj.name)
    m.use_nodes = True
    b = m.node_tree.nodes["Principled BSDF"]
    b.inputs["Base Color"].default_value = (*rgb, 1)
    b.inputs["Alpha"].default_value = alpha
    b.inputs["Roughness"].default_value = 0.9
    b.inputs["Emission Color"].default_value = (*rgb, 1)
    b.inputs["Emission Strength"].default_value = glow
    obj.data = obj.data.copy()
    obj.data.materials.clear()
    obj.data.materials.append(m)


def dup_mesh(src, matrix, name):
    o = bpy.data.objects.new(name, src.data)
    bpy.context.scene.collection.objects.link(o)
    o.matrix_world = matrix @ src.matrix_world
    return o


def build_scene(models, seed=3):
    rng = random.Random(seed)
    lay = L.campus_layout()
    bpy.ops.wm.read_factory_settings(use_empty=True)
    sc = bpy.context.scene
    zones = {z["id"]: z for z in lay["zones"]}
    bpos = {b["id"]: b["position"] for b in lay["buildings"]}

    imported = {}
    for b in lay["buildings"]:
        objs, new = import_root(models, b["model"], place_matrix(*b["position"], 0))
        imported[b["id"]] = objs
        for s in CUT:
            for o in new:
                if o.name.startswith("Facade_" + s):
                    o.hide_render = True
        for o in new:
            if o.type == "MESH" and o.name.startswith("Zone_"):
                zid = next(z["id"] for z in lay["zones"] if z["building"] == b["id"] and o.name.startswith(z["node"]))
                set_tint(o, tint_colour(zone_temp(zid, rng)))
    for a in lay["rooftopUnits"]:
        objs, new = import_root(models, "ahu", place_matrix(*a["position"], a["yaw"]))
        fan = objs["Fan"]
        fan.rotation_mode = "XYZ"
        fan.rotation_euler = (0, 0, rng.uniform(0, 1.2))
    for c in lay["chillers"]:
        objs, new = import_root(models, "chiller", place_matrix(*c["position"], c["yaw"]))
        for i in range(1, 5):
            objs["Fan_%d" % i].rotation_mode = "XYZ"
            objs["Fan_%d" % i].rotation_euler = (0, 0, rng.uniform(0, 1.2))
    import_root(models, "campus_ground", Matrix.Identity(4))

    # furniture and people: linked copies of the one imported mesh
    src = {}
    for name in ("desk", "meeting_table", "chair", "person_seated_a", "person_seated_b", "person_standing_a", "person_standing_b"):
        objs, new = import_glb(models, name)
        mesh = next(o for o in new if o.type == "MESH")
        mesh.hide_render = True
        src[name] = mesh
    n_people = 0
    for z in lay["zones"]:
        bx, _, bz = bpos[z["building"]]
        for it in z["furniture"]:
            if it["prop"] != "chair":
                dup_mesh(src[it["prop"]], place_matrix(bx + it["x"], z["floorY"], bz + it["z"], it["yaw"]), it["prop"])
            else:
                dup_mesh(src["chair"], place_matrix(bx + it["x"], z["floorY"], bz + it["z"], it["yaw"]), "chair")
        full = z["id"] == "tower-f4-east"
        for k, s in enumerate(z["seats"]):
            if full or rng.random() < 0.55:
                dup_mesh(src["person_seated_" + "ab"[(k + n_people) % 2]],
                         place_matrix(bx + s["x"], z["floorY"], bz + s["z"], s["yaw"]), "person")
                n_people += 1
    # the meeting's presenter, and arrivals on the plaza
    z4 = zones["tower-f4-east"]
    bx, _, bz = bpos["tower"]
    dup_mesh(src["person_standing_a"], place_matrix(bx + z4["centre"][0] - 0.5, z4["floorY"], bz + z4["centre"][1] + 2.2, 200), "person")
    for k, (x, z, yaw) in enumerate([(-1.0, -9.0, 10), (1.0, -7.0, 350), (-0.5, -3.5, 20), (0.8, -1.0, 340), (-3.0, 4.0, 90),
                                     (3.5, 6.0, 270), (4.8, 0.5, 90), (-5.0, -0.5, 270)]):
        dup_mesh(src["person_standing_" + "ab"[k % 2]], place_matrix(x, L.PAVE_TOP, z, yaw), "person")
    return lay


def setup_render(res, samples):
    sc = bpy.context.scene
    sc.render.engine = "CYCLES"
    sc.cycles.device = "CPU"
    sc.cycles.samples = samples
    sc.cycles.use_denoising = True
    sc.cycles.max_bounces = 6
    sc.cycles.transparent_max_bounces = 10
    sc.render.resolution_x, sc.render.resolution_y = res
    sc.view_settings.view_transform = "AgX"
    sc.view_settings.look = "AgX - Base Contrast"
    sc.view_settings.exposure = 0.0
    w = bpy.data.worlds.new("Dusk")
    sc.world = w
    w.use_nodes = True
    nt = w.node_tree
    tc = nt.nodes.new("ShaderNodeTexCoord")
    sep = nt.nodes.new("ShaderNodeSeparateXYZ")
    rng_ = nt.nodes.new("ShaderNodeMapRange")
    rng_.inputs["From Min"].default_value = -0.05
    rng_.inputs["From Max"].default_value = 0.75
    ramp = nt.nodes.new("ShaderNodeValToRGB")
    cr = ramp.color_ramp
    cr.elements[0].position, cr.elements[0].color = 0.0, (1.0, 0.36, 0.10, 1)       # the horizon glow
    cr.elements[1].position, cr.elements[1].color = 1.0, (0.012, 0.03, 0.10, 1)     # the zenith
    mid = cr.elements.new(0.25)
    mid.color = (0.30, 0.10, 0.18, 1)
    bg = nt.nodes["Background"]
    bg.inputs["Strength"].default_value = 0.9
    nt.links.new(tc.outputs["Generated"], sep.inputs["Vector"])
    nt.links.new(sep.outputs["Z"], rng_.inputs["Value"])
    nt.links.new(rng_.outputs["Result"], ramp.inputs["Fac"])
    nt.links.new(ramp.outputs["Color"], bg.inputs["Color"])
    # the low sun: in the west-south-west, so the light crosses the campus toward the east
    sun = bpy.data.objects.new("Sun", bpy.data.lights.new("Sun", "SUN"))
    sun.data.energy = 2.2
    sun.data.color = (1.0, 0.55, 0.28)
    sun.data.angle = math.radians(2.0)
    sun.rotation_euler = K.U(0.9, -0.16, 0.38).to_track_quat("-Z", "Y").to_euler()
    sc.collection.objects.link(sun)
    bpy.ops.mesh.primitive_plane_add(size=3000, location=(0, 0, -0.2))
    g = bpy.context.active_object
    gm = bpy.data.materials.new("Horizon")
    gm.use_nodes = True
    gm.node_tree.nodes["Principled BSDF"].inputs["Base Color"].default_value = (0.05, 0.06, 0.07, 1)
    g.data.materials.append(gm)
    cam = bpy.data.objects.new("Cam", bpy.data.cameras.new("Cam"))
    sc.collection.objects.link(cam)
    sc.camera = cam
    cam.data.clip_end = 3000
    return cam


def aim(cam, pos, tgt, lens):
    cam.location = K.U(*pos)
    d = K.U(*tgt) - cam.location
    cam.rotation_euler = d.to_track_quat("-Z", "Y").to_euler()
    cam.data.lens = lens


def add_room_light(cx, y, cz, watts=400, colour=(1.0, 0.82, 0.58)):
    ld = bpy.data.lights.new("RoomLight", "AREA")
    ld.energy = watts
    ld.size = 3.0
    ld.color = colour
    o = bpy.data.objects.new("RoomLight", ld)
    bpy.context.scene.collection.objects.link(o)
    o.location = K.U(cx, y, cz)
    return o


SHOTS = {
    # name: (camera, target, lens)
    "hero": ((46.0, 25.0, -50.0), (3.0, 8.0, 0.0), 30),
    "closeup": ((34.5, 13.4, 1.0), (23.2, 11.5, 5.5), 30),
    "roof": ((40.0, 31.0, -18.0), (17.0, 19.6, 0.0), 38),
}


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []

    def opt(name, default):
        if name in argv:
            i = argv.index(name)
            v = argv[i + 1]
            del argv[i:i + 2]
            return v
        return default

    models = os.path.abspath(opt("--models", os.path.join(HERE, "out")))
    out = os.path.abspath(opt("--out", os.path.join(HERE, "out")))
    samples = int(opt("--samples", "48"))
    w, h = (int(v) for v in opt("--res", "1280x720").split("x"))
    shots = argv or list(SHOTS)
    os.makedirs(out, exist_ok=True)
    build_scene(models)
    cam = setup_render((w, h), samples)
    lay = L.campus_layout()
    z4 = next(z for z in lay["zones"] if z["id"] == "tower-f4-east")
    bx, _, bz = lay["buildings"][0]["position"]
    for s in shots:
        extra = []
        if s == "closeup":
            extra.append(add_room_light(bx + z4["centre"][0], z4["floorY"] + 3.2, bz + z4["centre"][1], 600))
        pos, tgt, lens = SHOTS[s]
        aim(cam, pos, tgt, lens)
        bpy.context.scene.render.filepath = os.path.join(out, "towerpulse_" + s + ".png")
        bpy.ops.render.render(write_still=True)
        print("rendered", bpy.context.scene.render.filepath, flush=True)
        for o in extra:
            bpy.data.objects.remove(o)


main()
