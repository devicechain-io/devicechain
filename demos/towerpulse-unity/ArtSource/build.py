# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Build the Towerpulse models: geometry -> checks -> LOD1 -> glTF export.

  blender --background --python build.py -- [all | <model> ...] [--out DIR] [--install]

With no names, or `all`, every model is built, each in a Blender of its own, so a model's files do
not depend on what was built before it (a rebuild is byte-identical). Writes, per model, to --out
(default ./out, ignored by git):

  <model>.glb, <model>_LOD1.glb   the full and lower-detail models (same node tree)
  <model>.blend                    the generated scene, for inspection
  models_check.json                every check's result, the polycounts, the hierarchy summary

and exits non-zero if any check fails. --install also copies the .glb files into
../Assets/Towerpulse/Art/Models/ and writes ../Assets/Towerpulse/Data/campus_layout.json.

The checks (all must pass; see README.md):
  nodes_in_glb          every node the model promises exists in the exported file, LOD0 and LOD1
  node_names_unique     no Blender-renamed duplicates
  no_open_geometry      no boundary edges, degenerate faces or loose vertices in any mesh
  budget                LOD0 triangles under the model's limit
  lod_hierarchy_match   LOD1 has no node LOD0 lacks
  pivots_axis_aligned   no pivot is rotated at rest
  glb_root_identity     no root rotation in the glTF (it imports into Unity facing +Z)
  root_at_origin        Root sits at the origin; the model stands on or above its ground
  blend_materials       the glass and zone-tint materials export as alphaMode BLEND
  and, per building: one_node_per_zone, zones_disjoint, zones_inside_slab, zone_areas_plausible,
  floor_to_floor_3p8
"""
import sys, os, json, time, shutil, subprocess

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import towerpulse_kit as K            # noqa: E402
import layout as L                    # noqa: E402
from models import buildings, equipment, furniture, people, ground     # noqa: E402

import bpy, bmesh                     # noqa: E402

BLEND_MATERIALS = {"M_Glass", "M_ZoneTint"}

#            name                     builder                                  LOD1       LOD0 triangle budget
MODELS = {
    "tower": (buildings.tower, dict(planar=True, budget=7000, extra=lambda info: buildings.checks(info, "tower"))),
    "annex": (buildings.annex, dict(planar=True, budget=3000, extra=lambda info: buildings.checks(info, "annex"))),
    "ahu": (equipment.ahu, dict(ratio=0.6, budget=1200)),
    "chiller": (equipment.chiller, dict(ratio=0.6, budget=2000)),
    "desk": (furniture.desk, dict(ratio=0.8, budget=300)),
    "meeting_table": (furniture.meeting_table, dict(ratio=0.8, budget=200)),
    "chair": (furniture.chair, dict(ratio=0.8, budget=300)),
    "person_standing_a": (lambda: people.standing("a"), dict(ratio=0.6, budget=600)),
    "person_standing_b": (lambda: people.standing("b"), dict(ratio=0.6, budget=600)),
    "person_seated_a": (lambda: people.seated("a"), dict(ratio=0.6, budget=600)),
    "person_seated_b": (lambda: people.seated("b"), dict(ratio=0.6, budget=600)),
    "campus_ground": (ground.ground, dict(planar=True, budget=7000)),
}


def glb_json(fn):
    import struct
    with open(fn, "rb") as f:
        data = f.read()
    ln = struct.unpack_from("<I", data, 12)[0]
    return json.loads(data[20:20 + ln])


def mesh_health():
    """Open geometry in any LOD0 mesh. Boundary edges (one face) mean a hole; multi-face edges are
    only counted, because two boxes that touch share an edge once their coincident vertices merge."""
    res = {}
    bad = 0
    for o in K.lod_objects("LOD0"):
        bm = bmesh.new()
        bm.from_mesh(o.data)
        boundary = sum(1 for e in bm.edges if len(e.link_faces) == 1)
        multi = sum(1 for e in bm.edges if len(e.link_faces) > 2)
        degenerate = sum(1 for f in bm.faces if f.calc_area() < 1e-8)
        loose = sum(1 for v in bm.verts if not v.link_edges)
        bm.free()
        res[o.name] = dict(boundary_edges=boundary, multi_face_edges=multi, degenerate_faces=degenerate, loose_verts=loose)
        bad += boundary + degenerate + loose
    return dict(ok=bad == 0, meshes=res)


def build_one(name, out):
    fn, opt = MODELS[name]
    t0 = time.time()
    K.reset(name, out)
    info = fn()
    extra = opt["extra"](info) if "extra" in opt else {}
    K.make_lod1(opt.get("ratio", 0.5), planar=opt.get("planar", False))
    rep = K.report(budget_lod0=opt["budget"])
    files = K.export(name)
    checks = dict(extra)
    # the nodes the model promises, in the exported files themselves
    promised = set(info["expect_nodes"])
    missing = {}
    for lod, fnm in files.items():
        names = {n["name"] for n in glb_json(fnm)["nodes"]}
        miss = sorted(promised - names)
        if miss:
            missing[lod] = miss
    checks["nodes_in_glb"] = dict(ok=not missing, promised=len(promised), missing=missing)
    renamed = [k for k, o in K.NODES.items() if o.name != k]
    checks["node_names_unique"] = dict(ok=not renamed, renamed=renamed)
    checks["no_open_geometry"] = mesh_health()
    checks["budget"] = dict(tris_LOD0=rep["tris_LOD0"], limit=rep["budget"]["lod0_limit"], ok=rep["budget"]["ok"])
    checks["lod_hierarchy_match"] = dict(ok=not rep["lod_hierarchy"]["only_in_lod1"], lod0_only_nodes=rep["lod_hierarchy"]["only_in_lod0"])
    checks["pivots_axis_aligned"] = dict(rotated=rep["rotated_nodes_at_rest"], ok=not rep["rotated_nodes_at_rest"])
    glb = {k: K.glb_summary(v) for k, v in files.items()}
    checks["glb_root_identity"] = dict(ok=all(r == [0, 0, 0, 1] for g in glb.values() for r in g["root_rotations"]))
    root = K.NODES["Root"].matrix_world.translation
    bb = rep["bbox_unity_m"]
    checks["root_at_origin"] = dict(ok=root.length < 1e-6 and bb["min_y"] > -1.0, min_y=bb["min_y"])
    mats = {m["name"]: m.get("alphaMode", "OPAQUE") for m in glb_json(files["LOD0"]).get("materials", [])}
    want = {m: "BLEND" for m in BLEND_MATERIALS if m in mats}
    checks["blend_materials"] = dict(ok=all(mats[m] == "BLEND" for m in want) and all(v == "OPAQUE" for k, v in mats.items() if k not in BLEND_MATERIALS),
                                     alpha_modes=mats)
    ok = all(v.get("ok", False) for v in checks.values())
    print(f"{name:<18} LOD0 {rep['tris_LOD0']:>6}  LOD1 {rep['tris_LOD1']:>6}  nodes {len(rep['hierarchy']):>3}  "
          f"{'OK' if ok else 'FAILED'}  {rep['bbox_unity_m']}", flush=True)
    for k, v in checks.items():
        if not v.get("ok", False):
            print("   FAIL", k, json.dumps(v)[:400], flush=True)
    return dict(tris_LOD0=rep["tris_LOD0"], tris_LOD1=rep["tris_LOD1"], bbox=rep["bbox_unity_m"], materials=rep["materials"],
                nodes=len(rep["hierarchy"]), bytes={k: v["bytes"] for k, v in glb.items()},
                build_s=round(time.time() - t0, 2), checks=checks, all_ok=ok)


def main():
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    out = os.path.join(HERE, "out")
    if "--out" in argv:
        i = argv.index("--out")
        out = os.path.abspath(argv[i + 1])
        del argv[i:i + 2]
    install = "--install" in argv
    argv = [a for a in argv if a != "--install"]
    if "--one" in argv:
        name = argv[argv.index("--one") + 1]
        with open(os.path.join(out, f".check_{name}.json"), "w") as f:
            json.dump(build_one(name, out), f)
        return
    names = [a for a in argv if a != "all"] or list(MODELS)
    os.makedirs(out, exist_ok=True)
    summary = {}
    for name in names:
        if name not in MODELS:
            raise SystemExit("unknown model: " + name)
        subprocess.run([bpy.app.binary_path, "--background", "--factory-startup", "--python", os.path.abspath(__file__),
                        "--", "--one", name, "--out", out], check=True)
        chk = os.path.join(out, f".check_{name}.json")
        with open(chk) as f:
            summary[name] = json.load(f)
        os.remove(chk)
    with open(os.path.join(out, "models_check.json"), "w", newline="\n") as f:
        json.dump(summary, f, indent=1)
        f.write("\n")
    with open(os.path.join(out, "campus_layout.json"), "w", encoding="utf8", newline="\n") as f:
        f.write(json.dumps(L.campus_layout(), indent=1) + "\n")
    all_ok = all(v["all_ok"] for v in summary.values())
    print("ALL_OK", all_ok, flush=True)
    if install and all_ok:
        art = os.path.abspath(os.path.join(HERE, "..", "Assets", "Towerpulse"))
        models = os.path.join(art, "Art", "Models")
        os.makedirs(models, exist_ok=True)
        os.makedirs(os.path.join(art, "Data"), exist_ok=True)
        for name in names:
            for suffix in (".glb", "_LOD1.glb"):
                shutil.copyfile(os.path.join(out, name + suffix), os.path.join(models, name + suffix))
        shutil.copyfile(os.path.join(out, "campus_layout.json"), os.path.join(art, "Data", "campus_layout.json"))
        print("installed into", art, flush=True)
    if not all_ok:
        sys.exit(1)


main()
