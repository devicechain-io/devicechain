# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""
The lock check: does the 3D mark, at its locked pose, reproduce symbol.svg?

    python3 lock_check.py <render.png> [--svg symbol.svg] [--diff out.png]

<render.png> is the intro's lock pose rendered orthographically over the SVG's viewBox exactly
(square, black background, nothing else drawn): the Unity Editor writes one with the package's
README recipe. This script rasterises symbol.svg itself (it is polygons and one path of straight
lines, painted in order) at the render's size, 4x supersampled, and reports:

  * silhouette IoU: the mark's coverage in the render against the SVG's (both thresholded at
    half coverage);
  * per face: every visible SVG face's interior (the pixels where it is the top-most shape, eroded
    by a few pixels so antialiased edges do not count), the median rendered colour against the
    face's hex colour, and the worst channel difference over the interior (0..255);
  * PASS when IoU >= 0.98 and every face's median is within 2/255 per channel.

Needs Python 3 with Pillow and numpy.
"""
import argparse, json, math, os, re, sys

import numpy as np
from PIL import Image, ImageDraw, ImageFilter

HERE = os.path.dirname(os.path.abspath(__file__))
SS = 4


def parse(svg):
    text = open(svg, encoding="utf-8").read()
    vb = [float(v) for v in re.search(r'viewBox="([^"]+)"', text).group(1).split()]
    shapes = []
    for m in re.finditer(r'<(polygon|path)\s+fill="(#[0-9A-Fa-f]{6})"\s+(?:points|d)="([^"]+)"', text):
        kind, fill, data = m.groups()
        if kind == "polygon":
            nums = [float(v) for v in re.split(r"[\s,]+", data.strip())]
            shapes.append((fill.upper(), [list(zip(nums[0::2], nums[1::2]))]))
        else:
            shapes.append((fill.upper(), subpaths(data)))
    return vb, shapes


def subpaths(d):
    toks = re.findall(r"[MmLlHhVvZz]|-?\d*\.?\d+(?:e-?\d+)?", d)
    out, pts, cur, start, cmd, i = [], [], (0.0, 0.0), (0.0, 0.0), None, 0
    while i < len(toks):
        t = toks[i]
        if t.isalpha():
            cmd = t
            i += 1
            if cmd in "Zz":
                out.append(pts)
                pts, cur = [], start
            continue
        if cmd in "MmLl":
            x, y = float(toks[i]), float(toks[i + 1]); i += 2
            cur = (cur[0] + x, cur[1] + y) if cmd.islower() else (x, y)
            if cmd in "Mm" and not pts:
                start = cur
        elif cmd in "Hh":
            x = float(toks[i]); i += 1
            cur = (cur[0] + x if cmd == "h" else x, cur[1])
        elif cmd in "Vv":
            y = float(toks[i]); i += 1
            cur = (cur[0], cur[1] + y if cmd == "v" else y)
        else:
            raise SystemExit(f"unsupported path command {cmd}")
        pts.append(cur)
    if pts:
        out.append(pts)
    return out


def mask(rings, vb, size, ss):
    """Coverage of a shape (outer ring, then holes: symbol.svg's only path is a ring whose inner
    subpath winds the other way) on a size*ss canvas."""
    n = size * ss
    sx = n / vb[2]
    img = Image.new("L", (n, n), 0)
    dr = ImageDraw.Draw(img)
    for k, ring in enumerate(rings):
        dr.polygon([((x - vb[0]) * sx, (y - vb[1]) * sx) for x, y in ring], fill=255 if k == 0 else 0)
    return img


def hexrgb(h):
    return np.array([int(h[i:i + 2], 16) for i in (1, 3, 5)], dtype=np.float64)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("render")
    ap.add_argument("--svg", default=os.path.normpath(os.path.join(HERE, "..", "..", "..", "..", "branding", "logos", "symbol.svg")))
    ap.add_argument("--diff")
    a = ap.parse_args()

    ren = np.asarray(Image.open(a.render).convert("RGB")).astype(np.float64)
    size = ren.shape[0]
    if ren.shape[1] != size:
        raise SystemExit("the render must be square")
    vb, shapes = parse(a.svg)

    # reference: paint in order at 4x, then box-filter down (an antialiased raster of the SVG)
    n = size * SS
    ref = np.zeros((n, n, 3))
    cov = np.zeros((n, n))
    top = np.full((n, n), -1, dtype=np.int32)       # index of the top-most shape, full resolution
    for idx, (fill, rings) in enumerate(shapes):
        m = np.asarray(mask(rings, vb, size, SS)) > 127
        ref[m] = hexrgb(fill)
        cov[m] = 1.0
        top[m] = idx
    ref = ref.reshape(size, SS, size, SS, 3).mean(axis=(1, 3))
    cov = cov.reshape(size, SS, size, SS).mean(axis=(1, 3))

    # silhouette: anything the render drew over its black background
    drawn = ren.max(axis=2) > 12.0
    want = cov >= 0.5
    inter = np.logical_and(drawn, want).sum()
    union = np.logical_or(drawn, want).sum()
    iou = inter / union

    # faces: where each shape is on top (sampled at pixel centres), eroded
    centre = top[SS // 2::SS, SS // 2::SS]
    faces, worst_median = [], 0.0
    for idx, (fill, _) in enumerate(shapes):
        region = Image.fromarray(((centre == idx) * 255).astype(np.uint8)).filter(ImageFilter.MinFilter(7))
        r = np.asarray(region) > 127
        if r.sum() == 0:
            continue                                # fully covered by later shapes (the underlays)
        px = ren[r]
        med = np.median(px, axis=0)
        d_med = np.abs(med - hexrgb(fill)).max()
        d_max = np.abs(px - hexrgb(fill)).max()
        worst_median = max(worst_median, d_med)
        faces.append({"shape": idx, "fill": fill, "pixels": int(r.sum()),
                      "median": [int(round(v)) for v in med], "median_diff": round(float(d_med), 2),
                      "max_diff": round(float(d_max), 2)})

    # whole-image colour error inside the mark, away from edges
    inner = np.asarray(Image.fromarray((want * 255).astype(np.uint8)).filter(ImageFilter.MinFilter(7))) > 127
    mean_err = float(np.abs(ren[inner] - ref[inner]).mean())

    ok = iou >= 0.98 and worst_median <= 2.0
    print(json.dumps({"render": a.render, "size": size, "silhouette_iou": round(float(iou), 5),
                      "faces": faces, "worst_face_median_diff": round(worst_median, 2),
                      "mean_abs_error_interior": round(mean_err, 3), "pass": bool(ok)}, indent=1))
    if a.diff:
        d = np.clip(np.abs(ren - ref) * 8.0, 0, 255).astype(np.uint8)
        Image.fromarray(d).save(a.diff)
    sys.exit(0 if ok else 1)


main()
