// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The backdrop behind the last trees: wooded foothills, a middle range and the far ridge that carries the skyline, and the uplands
    /// east and west of the town (<see cref="CoastalTerrain.HillLayers"/>), each a coarse height field of a few hundred triangles with no trees on it. The nearer a layer
    /// the darker and greener it is drawn, with a few autumn-tinted faces; the haze then takes more of each with distance, so they read as
    /// successive ranges. A layer's vertex alpha is its surface code, which the scenery shader reads to give the backdrop its own lighter fog
    /// and to dissolve each layer's foot into the ground haze over that layer's own height.
    /// </summary>
    internal static class RidgeBuilder
    {
        /// <summary>The scenery shader's codes for the backdrop (scenery is 1, rock .9, the surface codes are 0.1 to 0.4): far ridge, middle range, foothills.</summary>
        public const float SurfaceCode = .6f, MidCode = .66f, FoothillCode = .72f;

        /// <summary>A layer's code is chosen by the height its foot fades over (50 m, 26 m or 14 m): the shader knows those three.</summary>
        public static float Code(CoastalTerrain.HillLayer layer) => layer.FootFadeM >= 50 ? SurfaceCode : layer.FootFadeM >= 26 ? MidCode : FoothillCode;

        static Color Band(CoastalTerrain.HillLayer layer, float t, float variation, float autumn)
        {
            Color c;
            if (layer.FootFadeM >= 50)
            {
                // pale valley haze at the foot, then two forest greens, then a slightly lighter crest
                if (t < .18f) c = new Color(.30f, .40f, .43f);
                else if (t < .45f) c = new Color(.21f, .33f, .34f);
                else if (t < .72f) c = new Color(.16f, .28f, .28f);
                else c = new Color(.18f, .30f, .30f);
                // and a breath of the season in broad reaches of the far wood
                if (t >= .18f && autumn > .78f) c = Color.Lerp(c, new Color(.36f, .27f, .17f), .30f);
                else if (t >= .18f && autumn < .16f) c = Color.Lerp(c, new Color(.40f, .37f, .18f), .26f);
            }
            else
            {
                // nearer woods: spruce green low down, lighter mixed wood above, and here and there a face turned to rust or gold
                var near = layer.FootFadeM < 26;
                var low = near ? new Color(.18f, .27f, .23f) : new Color(.17f, .27f, .26f);
                var high = near ? new Color(.25f, .33f, .25f) : new Color(.21f, .31f, .28f);
                c = Color.Lerp(low, high, Mathf.SmoothStep(0, 1, Mathf.InverseLerp(.15f, .85f, t)));
                // rust and gold in drifts along the range (a slow noise now, where a per-face hash used to scatter single faces)
                if (autumn > .70f) c = Color.Lerp(c, new Color(.44f, .27f, .14f), (near ? .62f : .46f) * Mathf.InverseLerp(.70f, .80f, autumn));
                else if (autumn < .30f) c = Color.Lerp(c, new Color(.50f, .42f, .16f), (near ? .52f : .38f) * Mathf.InverseLerp(.30f, .20f, autumn));
            }
            return new Color(c.r * variation, c.g * variation, c.b * variation, Code(layer));
        }

        public static void Build(Transform parent, Material material)
        {
            var deck = (float)SitePlan.DeckHeightM;
            for (var l = 0; l < CoastalTerrain.HillLayers.Length; l++)
            {
                var layer = CoastalTerrain.HillLayers[l];
                var mesh = new SceneryMesh();
                var step = (float)layer.ColumnStep;
                var columns = (int)Math.Round(2 * layer.HalfWidth / step);
                var peak = 1f;
                foreach (var h in layer.Hills) peak = Mathf.Max(peak, (float)h[2]);
                // a corner at an along and a back coordinate, in WORLD space (a northern layer runs along x, an eastern or western one along z)
                Vector3 V(float along, float back)
                {
                    layer.World(along, back, out var wx, out var wz);
                    return new Vector3((float)wx, (float)layer.Height(wx, wz), (float)wz);
                }
                for (var r = 0; r + 1 < layer.RowZ.Length; r++)
                    for (var j = 0; j < columns; j++)
                    {
                        float x0 = (float)(layer.AlongCentre - layer.HalfWidth) + j * step, x1 = x0 + step;
                        float z0 = (float)layer.RowZ[r], z1 = (float)layer.RowZ[r + 1];
                        var a = V(x0, z0); var b = V(x1, z0); var c = V(x0, z1); var d = V(x1, z1);
                        var mean = (a.y + b.y + c.y + d.y) * .25f;
                        if (Mathf.Max(Mathf.Max(a.y, b.y), Mathf.Max(c.y, d.y)) - deck < .3f) continue;
                        var t = Mathf.Clamp01((mean - deck) / peak);
                        // a height field's faces all look up, whichever way the layer runs
                        // the season's colour comes in drifts a few hundred metres long, sharpened so most of a range stays green
                        var drift = (float)CoastalTerrain.Noise((x0 + step * .5f) * .0042 + l * 13.7, (z0 + z1) * .5f * .006 + l * 5.1) * .7f
                            + (float)CoastalTerrain.Noise((x0 + step * .5f) * .013 + l * 3.3, (z0 + z1) * .5f * .017) * .3f;
                        drift = Mathf.Clamp01((drift - .5f) * 2.1f + .5f);
                        mesh.Facing(a, c, d, b, Vector3.up, Band(layer, t, .97f + .06f * (float)CoastalTerrain.Hash(j + (layer == CoastalTerrain.Ridge ? 0 : (l + 1) * 977), r), drift));
                    }
                // the wood itself: tree tops along the crest and down the face, as flat cut-outs towards the harbour (CoastalTerrain.CrestTrees)
                var alongAxis = layer.Side == 0 ? Vector3.right : Vector3.forward;
                var toward = layer.Side == 0 ? Vector3.back : layer.Side > 0 ? Vector3.left : Vector3.right;
                var code = Code(layer);
                foreach (var tree in layer.CrestTrees())
                {
                    layer.World(tree.Along, tree.Back, out var tx, out var tz);
                    var foot = new Vector3((float)tx, (float)tree.Foot, (float)tz);
                    float w = (float)tree.HalfWidth, h = (float)tree.Height, v = (float)tree.Variation;
                    var lean = alongAxis * ((v - .5f) * .25f * w);
                    Color tone;
                    if (tree.Conifer) tone = Color.Lerp(new Color(.11f, .20f, .15f), new Color(.17f, .27f, .19f), v);
                    else
                    {
                        var d = (float)tree.Drift;
                        tone = d > .66f ? new Color(.56f, .26f, .13f) : d > .52f ? new Color(.62f, .40f, .14f) : d > .36f ? new Color(.60f, .50f, .17f) : new Color(.30f, .36f, .18f);
                        tone = Color.Lerp(tone, new Color(.46f, .17f, .13f), d > .8f ? .6f : 0);
                        tone *= .9f + .2f * v;
                    }
                    if (layer.FootFadeM >= 50) tone = Color.Lerp(tone, new Color(.17f, .29f, .29f), .45f);
                    tone.a = code;
                    if (tree.Conifer)
                        mesh.Facing(foot - alongAxis * w, foot + Vector3.up * h + lean, foot + alongAxis * w, toward, tone);
                    else
                    {
                        // a round crown: five corners, three triangles
                        Vector3 p0 = foot - alongAxis * (w * .5f), p1 = foot + alongAxis * (w * .5f), p2 = foot + alongAxis * w + Vector3.up * (h * .55f),
                            p3 = foot + Vector3.up * h + lean, p4 = foot - alongAxis * w + Vector3.up * (h * .6f);
                        mesh.Facing(p0, p2, p1, toward, tone);
                        mesh.Facing(p0, p3, p2, toward, tone);
                        mesh.Facing(p0, p4, p3, toward, tone);
                    }
                }
                if (mesh.VertexCount > 0) mesh.Spawn(layer.Name, parent, material);
            }
        }
    }
}
