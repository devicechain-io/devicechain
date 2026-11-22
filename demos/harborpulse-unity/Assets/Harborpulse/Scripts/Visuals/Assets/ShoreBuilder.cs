// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The rocky foreshore along the open coast and the skerries off it (<see cref="CoastalTerrain.ShoreReach"/>): faceted granite ledges
    /// that run out from the coast's straight edge to an irregular line of points and coves, dipping under the water at their seaward edge
    /// so the tide draws the waterline. Two meshes on the scenery material, no shadow; nothing here is near the harbour's own walls.
    /// </summary>
    internal static class ShoreBuilder
    {
        const int Rows = 4;

        static Color Rock(int column, int row, float height)
        {
            var v = (float)CoastalTerrain.Hash(column * 13 + 7, row * 29 + 3);
            var tone = v < .25f ? Color.Lerp(new Color(.40f, .36f, .31f), new Color(.61f, .54f, .45f), v * 4) : Color.Lerp(new Color(.61f, .54f, .45f), new Color(.76f, .69f, .57f), (v - .25f) * 1.2f);
            // wet and weed-dark near the water, a pale dry band above the tide's reach, lichen on a few of the high facets
            var wet = 1 - Mathf.SmoothStep(0, 1, Mathf.InverseLerp(.2f, 1.9f, height));
            tone = Color.Lerp(tone, new Color(.17f, .19f, .15f), wet * .75f);
            if (height > 2f && v > .86f) tone = Color.Lerp(tone, new Color(.55f, .59f, .38f), .5f);
            tone.a = TerrainBuilder.RockCode;
            return tone;
        }

        public static void Build(Transform parent, Material material)
        {
            var coast = (float)CoastalTerrain.CoastZ;
            foreach (var side in new[] { -1, 1 })
            {
                var mesh = new SceneryMesh();
                var column = 0;
                var x = (float)CoastalTerrain.ShoreStartX;
                var reachX = (float)CoastalTerrain.TerrainHalfWidthM;
                Vector3 P(float px, int row, int col)
                {
                    var t = row / (float)Rows;
                    var reach = (float)CoastalTerrain.ShoreReach(side * px);
                    // the inner edge is the coast itself, exactly; further out each corner is nudged so no two facets lie in one plane
                    var jitter = row == 0 ? 0 : ((float)CoastalTerrain.Hash(col * 5 + side * 3, row * 11 + 2) - .5f);
                    var y = (float)CoastalTerrain.ShoreHeight(t) + (row == 0 ? 0 : jitter * 1.1f * Mathf.Min(1, reach / 20));
                    return new Vector3(side * px, y, coast - reach * (t + jitter * .12f * (row < Rows ? 1 : 0)));
                }
                while (x < reachX)
                {
                    var step = x < 2400 ? 14f : 70f;
                    var x1 = Mathf.Min(reachX, x + step);
                    if (CoastalTerrain.ShoreReach(side * x) > .5 || CoastalTerrain.ShoreReach(side * x1) > .5)
                        for (var r = 0; r < Rows; r++)
                        {
                            var a = P(x, r, column); var b = P(x1, r, column + 1); var c = P(x, r + 1, column); var d = P(x1, r + 1, column + 1);
                            var colour = Rock(column * side, r, (a.y + b.y + c.y + d.y) * .25f);
                            // two triangles, each with its own facet normal, facing up whichever side of the harbour this is
                            mesh.Facing(a, c, d, Vector3.up, colour);
                            mesh.Facing(a, d, b, Vector3.up, new Color(colour.r * .93f, colour.g * .93f, colour.b * .93f, colour.a));
                        }
                    x = x1; column++;
                }
                if (mesh.VertexCount > 0) mesh.Spawn(side < 0 ? "Foreshore west" : "Foreshore east", parent, material);
            }

            var rocks = new SceneryMesh();
            for (var i = 0; i < CoastalTerrain.Skerries.Length; i++)
            {
                var s = CoastalTerrain.Skerries[i];
                var centre = new Vector3((float)s[0], 0, (float)s[1]); float radius = (float)s[2], height = (float)s[3];
                const int sides = 7;
                var foot = new Vector3[sides]; var shoulder = new Vector3[sides];
                for (var k = 0; k < sides; k++)
                {
                    var a = (k + ((float)CoastalTerrain.Hash(i * 17 + k, 5) - .5f) * .5f) * Mathf.PI * 2 / sides;
                    var r = radius * (.75f + .35f * (float)CoastalTerrain.Hash(i * 31 + k, 9));
                    var dir = new Vector3(Mathf.Cos(a), 0, Mathf.Sin(a) * .7f);
                    foot[k] = centre + dir * r + Vector3.down * 3f;
                    shoulder[k] = centre + dir * r * .5f + Vector3.up * height * (.45f + .4f * (float)CoastalTerrain.Hash(i * 7 + k, 13));
                }
                var crown = centre + Vector3.up * height + new Vector3(radius * .12f, 0, 0);
                for (var k = 0; k < sides; k++)
                {
                    var n = (k + 1) % sides;
                    var outward = (foot[k] + foot[n]) * .5f - centre;
                    var wall = Rock(i * 41 + k, 1, .6f); var top = Rock(i * 41 + k, 2, 2.6f);
                    rocks.Facing(foot[k], shoulder[k], shoulder[n], foot[n], outward, wall);
                    rocks.Facing(shoulder[k], crown, shoulder[n], outward + Vector3.up * radius, top);
                }
            }
            if (rocks.VertexCount > 0) rocks.Spawn("Skerries", parent, material);
        }
    }
}
