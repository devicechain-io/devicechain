// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The one distant, hazy ridge that gives the skyline its shape: a coarse height field 2.35-3.05 km behind the harbour (a few hundred
    /// triangles, no trees), banded blue-green-grey like far forest. Its vertex alpha is <see cref="SurfaceCode"/>, which the scenery shader
    /// reads to give it its own lighter fog and to dissolve its foot into the haze.
    /// </summary>
    internal static class RidgeBuilder
    {
        /// <summary>The scenery shader's code for the ridge: a vertex alpha of 0.6 (scenery is 1, the surface codes are 0.1 to 0.4).</summary>
        public const float SurfaceCode = .6f;

        static Color Band(float t, float variation)
        {
            // pale valley haze at the foot, then two forest greens, then a slightly lighter crest
            Color c;
            if (t < .18f) c = new Color(.36f, .45f, .47f);
            else if (t < .45f) c = new Color(.28f, .41f, .41f);
            else if (t < .72f) c = new Color(.23f, .36f, .34f);
            else c = new Color(.25f, .38f, .35f);
            return new Color(c.r * variation, c.g * variation, c.b * variation, SurfaceCode);
        }

        public static void Build(Transform parent, Material material)
        {
            var mesh = new SceneryMesh();
            var step = (float)CoastalTerrain.RidgeColumnStep;
            var columns = (int)Math.Round(2 * CoastalTerrain.RidgeHalfWidth / step);
            var deck = (float)SitePlan.DeckHeightM;
            Vector3 V(float x, float z) => new Vector3(x, (float)CoastalTerrain.RidgeHeight(x, z), z);
            for (var r = 0; r + 1 < CoastalTerrain.RidgeRowZ.Length; r++)
                for (var j = 0; j < columns; j++)
                {
                    float x0 = (float)-CoastalTerrain.RidgeHalfWidth + j * step, x1 = x0 + step;
                    float z0 = (float)CoastalTerrain.RidgeRowZ[r], z1 = (float)CoastalTerrain.RidgeRowZ[r + 1];
                    // same winding as the terrain's quads
                    var a = V(x0, z0); var b = V(x1, z0); var c = V(x0, z1); var d = V(x1, z1);
                    var mean = (a.y + b.y + c.y + d.y) * .25f;
                    if (Mathf.Max(Mathf.Max(a.y, b.y), Mathf.Max(c.y, d.y)) - deck < .3f) continue;
                    var t = Mathf.Clamp01((mean - deck) / 124f);
                    mesh.Quad(a, c, d, b, Band(t, .97f + .06f * (float)CoastalTerrain.Hash(j, r)));
                }
            if (mesh.VertexCount > 0) mesh.Spawn("Distant ridge", parent, material);
        }
    }
}
