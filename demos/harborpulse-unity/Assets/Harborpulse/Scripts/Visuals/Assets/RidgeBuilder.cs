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
            }
            else
            {
                // nearer woods: spruce green low down, lighter mixed wood above, and here and there a face turned to rust or gold
                var near = layer.FootFadeM < 26;
                var low = near ? new Color(.18f, .27f, .23f) : new Color(.17f, .27f, .26f);
                var high = near ? new Color(.25f, .33f, .25f) : new Color(.21f, .31f, .28f);
                c = Color.Lerp(low, high, Mathf.SmoothStep(.15f, .85f, t));
                if (autumn > .80f) c = Color.Lerp(c, new Color(.40f, .27f, .15f), near ? .55f : .38f);
                else if (autumn < .10f) c = Color.Lerp(c, new Color(.46f, .40f, .17f), near ? .45f : .30f);
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
                        mesh.Facing(a, c, d, b, Vector3.up, Band(layer, t, .97f + .06f * (float)CoastalTerrain.Hash(j + (layer == CoastalTerrain.Ridge ? 0 : (l + 1) * 977), r), (float)CoastalTerrain.Hash(j * 7 + 3 + l * 131, r * 5 + 1)));
                    }
                if (mesh.VertexCount > 0) mesh.Spawn(layer.Name, parent, material);
            }
        }
    }
}
