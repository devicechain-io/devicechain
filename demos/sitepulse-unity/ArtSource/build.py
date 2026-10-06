# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Build one Sitepulse machine: geometry -> rig checks -> LOD1 -> glTF export -> renders.

  blender --background --python build.py -- <machine> [--no-render] [--only stem1,stem2] [--out DIR]

A machine module (machines/<name>.py) provides:
  NAME, P (params), LOD1_RATIO
  build(K)          -> info dict (rig numbers; may set info["keep_full_lod1"])
  pose(K, **state)  -> poses the rig (render + checks use it)
  checks(K, info)   -> {check_name: {..., "ok": bool}}
  shots(K, info)    -> [(stem, pose_kwargs, (cam_pos, cam_target, lens[, ortho_scale_m]), lod)]
"""
import sys, os, json, importlib, time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import sitepulse_kit as K

argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
name = argv[0]
opts = argv[1:]
out = os.path.join(HERE, "out")
only = None
if "--out" in opts:
    out = opts[opts.index("--out") + 1]
if "--only" in opts:
    only = opts[opts.index("--only") + 1].split(",")
render = "--no-render" not in opts

t0 = time.time()
M = importlib.import_module("machines." + name)
K.reset(name, out)
info = M.build(K)
checks = M.checks(K, info)
K.make_lod1(M.LOD1_RATIO, keep_full=tuple(info.get("keep_full_lod1", ())))
rep = K.report()
files = K.export(name)
rep["params"] = M.P
rep["rig"] = {k: v for k, v in info.items() if k != "keep_full_lod1"}
rep["checks"] = checks
rep["checks"]["lod_hierarchy_match"] = dict(ok=not rep["lod_hierarchy"]["only_in_lod1"],
                                            lod0_only_nodes=rep["lod_hierarchy"]["only_in_lod0"])
rep["checks"]["budget"] = dict(tris_LOD0=rep["tris_LOD0"], limit=rep["budget"]["lod0_limit"], ok=rep["budget"]["ok"])
# aimed parts (cylinders, rods, links) carry an aim constraint; anything else rotated at rest is a defect
aimed = [n for n in rep["rotated_nodes_at_rest"] if not (K.NODES[n].constraints or "Cylinder" in n or "Rod" in n
                                                       or n == "RipperUpperLink")]
rep["checks"]["pivots_axis_aligned"] = dict(rotated=rep["rotated_nodes_at_rest"], unexpected=aimed, ok=not aimed)
rep["glb"] = {k: K.glb_summary(v) for k, v in files.items()}
rep["checks"]["glb_root_identity"] = dict(ok=all(r == [0, 0, 0, 1] for g in rep["glb"].values() for r in g["root_rotations"]))
rep["all_ok"] = all(v.get("ok", False) for v in checks.values())
rep["build_s"] = round(time.time() - t0, 2)

def jsonable(o):
    if isinstance(o, dict):
        return {k: jsonable(v) for k, v in o.items()}
    if isinstance(o, (list, tuple)):
        return [jsonable(v) for v in o]
    if hasattr(o, "to_tuple"):
        return [round(x, 4) for x in o]
    return o

# <name>_check.json per machine; the dozer also keeps its original out/check.json name
for fn in [f"{name}_check.json"] + (["check.json"] if name == "dozer" else []):
    with open(os.path.join(out, fn), "w") as f:
        json.dump(jsonable(rep), f, indent=1)
print("==== CHECKS ====")
for k, v in rep["checks"].items():
    print(f"  {k:<28} {'PASS' if v.get('ok') else 'FAIL'}")
print("tris", rep["tris_LOD0"], rep["tris_LOD1"], "bbox", rep["bbox_unity_m"], "mats", rep["materials"])
print("ALL_OK", rep["all_ok"], "build_s", rep["build_s"], flush=True)

if render:
    rd = os.path.join(HERE, "renders")
    os.makedirs(rd, exist_ok=True)
    K.render_setup()
    for stem, pz, view, lod in M.shots(K, info):     # view = (pos, tgt, lens[, ortho_scale_m])
        if only and stem not in only:
            continue
        M.pose(K, **pz)
        K.show_lod(lod)
        K.look(*view)
        K.shot(os.path.join(rd, stem + ".png"))
    M.pose(K)
