# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
Fetch the CC0 terrain materials Sitepulse uses and prepare them for Unity terrain layers.

    python3 prepare_textures.py [--out DIR] [--cache DIR]

Requires Python 3.8+ and Pillow. Each material is downloaded from ambientCG (CC0 1.0, see
THIRD_PARTY.md) as its 1K JPG set and turned into three files:

  T_<Layer>_Albedo.jpg   base colour, re-encoded at quality 90
  T_<Layer>_Normal.jpg   OpenGL-convention normal map (what Unity expects), quality 92
  T_<Layer>_Mask.png     Unity terrain mask map: R metallic (0), G ambient occlusion,
                         B height (the displacement map, used for height-based blending),
                         A smoothness (1 - roughness); 512 px, which is plenty for blending

The re-encoding only shrinks the files for the repository; nothing is painted or altered.
"""
import argparse
import io
import os
import urllib.request
import zipfile

from PIL import Image, ImageChops

MATERIALS = {               # layer -> ambientCG asset id
    "Rock": "Rock028",
    "Gravel": "Gravel040",
    "Dirt": "Ground081",
    "Grass": "Ground037",
}
URL = "https://ambientcg.com/get?file={id}_1K-JPG.zip"


def fetch(asset, cache):
    os.makedirs(cache, exist_ok=True)
    path = os.path.join(cache, asset + "_1K-JPG.zip")
    if not os.path.exists(path):
        req = urllib.request.Request(URL.format(id=asset), headers={"User-Agent": "sitepulse-prepare-textures"})
        with urllib.request.urlopen(req, timeout=120) as r, open(path, "wb") as f:
            f.write(r.read())
    return zipfile.ZipFile(path)


def member(z, suffix):
    for n in z.namelist():
        if n.endswith(suffix):
            return Image.open(io.BytesIO(z.read(n)))
    raise KeyError(suffix)


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=os.path.join(here, "..", "..", "Assets", "Sitepulse", "Art", "Textures", "Terrain"))
    ap.add_argument("--cache", default=os.path.join(here, "cache"))
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    for layer, asset in MATERIALS.items():
        z = fetch(asset, a.cache)
        col = member(z, "_Color.jpg").convert("RGB")
        nrm = member(z, "_NormalGL.jpg").convert("RGB")
        col.save(os.path.join(a.out, f"T_{layer}_Albedo.jpg"), quality=90, optimize=True)
        nrm.save(os.path.join(a.out, f"T_{layer}_Normal.jpg"), quality=92, optimize=True)
        size = (512, 512)
        ao = member(z, "_AmbientOcclusion.jpg").convert("L").resize(size, Image.LANCZOS)
        height = member(z, "_Displacement.jpg").convert("L").resize(size, Image.LANCZOS)
        smooth = ImageChops.invert(member(z, "_Roughness.jpg").convert("L").resize(size, Image.LANCZOS))
        metal = Image.new("L", size, 0)
        Image.merge("RGBA", (metal, ao, height, smooth)).save(os.path.join(a.out, f"T_{layer}_Mask.png"), optimize=True)
        print(layer, asset, "ok")


if __name__ == "__main__":
    main()
