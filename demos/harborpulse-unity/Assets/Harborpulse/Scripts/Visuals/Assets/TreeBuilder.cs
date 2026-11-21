// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The forest, merged by tile and level of detail into shared, shadow-free surfaces (never one object per tree). Species, colours and the
    /// per-band vertex costs come from <see cref="TreeStyle"/>; the geometry below must emit exactly the vertices its table counts
    /// (flat shading: three per triangle, four per quad), so the plan's budget test is the budget the player pays.
    /// </summary>
    internal sealed class TreeBuilder
    {
        readonly Dictionary<(int, int, int, bool), SceneryMesh> tiles = new Dictionary<(int, int, int, bool), SceneryMesh>();

        // The vertex alpha is the shader's surface code (1 for scenery): colours scale in RGB only.
        static Color C(TreeStyle.Colour c, float scale = 1) => new Color((float)c.R * scale, (float)c.G * scale, (float)c.B * scale, 1);
        static Color Scale(Color c, float s) => new Color(c.r * s, c.g * s, c.b * s, 1);

        public void Add(Vector3 at, float height, TreeKind kind, bool town)
        {
            var d = (float)CoastalTerrain.CoreDistance(at.x, at.z);
            var lod = TreeStyle.Lod(d);
            var tileSize = lod == TreeLod.Near ? 300 : lod == TreeLod.Mid ? 450 : 900;
            var key = (Mathf.FloorToInt(at.x / tileSize), Mathf.FloorToInt(at.z / tileSize), (int)lod, town);
            if (!tiles.TryGetValue(key, out var mesh)) tiles[key] = mesh = new SceneryMesh();
            var roll = (float)CoastalTerrain.Hash(Mathf.RoundToInt(at.x * 7), Mathf.RoundToInt(at.z * 7));
            var roll2 = (float)CoastalTerrain.Hash(Mathf.RoundToInt(at.z * 5) + 17, Mathf.RoundToInt(at.x * 5) - 9);
            var yaw = roll * 360;
            var lean = new Vector3(Mathf.Sin(yaw * Mathf.Deg2Rad) * .045f, 0, Mathf.Cos(yaw * Mathf.Deg2Rad) * .035f) * height;
            var up = Vector3.up;
            var bark = C(TreeStyle.Bark(kind));
            var tr = height * (.030f + roll * .010f);
            if (TreeStyle.IsConifer(kind)) { Conifer(mesh, at, height, kind, lod, roll2, lean, bark, tr); return; }

            var leaf = C(TreeStyle.Broadleaf(kind, at.x, at.z, roll2));
            // far crowns are brighter and a little more saturated, so the autumn colour survives the haze
            if (lod == TreeLod.Far) { var grey = (leaf.r + leaf.g + leaf.b) / 3; leaf = new Color(Mathf.Clamp01((grey + (leaf.r - grey) * 1.25f) * 1.1f), Mathf.Clamp01((grey + (leaf.g - grey) * 1.25f) * 1.1f), Mathf.Clamp01((grey + (leaf.b - grey) * 1.25f) * 1.1f), 1); }
            // round maple, broad oak, tall oval birch
            Vector3 radii; float cy;
            if (kind == TreeKind.Oak) { radii = new Vector3(.40f, .30f, .38f) * height; cy = .60f; }
            else if (kind == TreeKind.Birch) { radii = new Vector3(.21f, .33f, .20f) * height; cy = .68f; }
            else { var r = .32f * height; radii = new Vector3(r, r * .95f, r * .92f); cy = .66f; }
            var centre = at + lean + up * (height * cy);
            if (lod == TreeLod.Far) { mesh.Crown(at + lean + up * (height * .50f), radii, leaf, 0, yaw); return; }
            if (lod == TreeLod.Mid)
            {
                mesh.Branch(at - up * .3f, at + lean + up * (height * .45f), tr * 1.2f, tr * .7f, bark, 3);   // 21
                mesh.Crown(centre, radii, leaf, 0, yaw);                                                       // 24
                return;
            }
            var trunkTop = at + lean + up * (height * .56f);
            mesh.Branch(at - up * .4f, trunkTop, tr * 1.15f, tr * .55f, bark, 5);                              // 35
            mesh.Crown(centre, radii, leaf, 1, yaw);                                                           // 96
            if (kind != TreeKind.Birch)
            {
                // one side lobe on a short branch: the crown is not a perfect ball
                var dir = Quaternion.Euler(0, yaw + 155, 0) * new Vector3(radii.x * .65f, 0, radii.x * .20f);
                var end = centre + dir - up * (height * .10f);
                mesh.Branch(trunkTop - up * (height * .12f), end, tr * .55f, tr * .18f, bark, 4);              // 28
                mesh.Crown(end, radii * .62f, Scale(leaf, .78f), 0, yaw + 47);                                 // 24
            }
        }

        void Conifer(SceneryMesh mesh, Vector3 at, float height, TreeKind kind, TreeLod lod, float roll, Vector3 lean, Color bark, float tr)
        {
            var up = Vector3.up;
            Vector3 P(float y) => at + lean * (y / height) + up * y;
            Color Body(float u) => C(TreeStyle.Conifer(kind, roll, u, at.x, at.z));
            if (lod == TreeLod.Far)
            {
                if (kind == TreeKind.Spruce)
                {
                    mesh.Branch(P(height * .08f), P(height * .62f), height * .26f, 0, Body(.2f), 4);          // 12
                    mesh.Branch(P(height * .40f), P(height), height * .18f, 0, Body(.8f), 4);                  // 12
                }
                else mesh.Branch(P(height * (kind == TreeKind.Pine ? .30f : .06f)), P(height * (kind == TreeKind.Pine ? .95f : 1f)),
                    height * (kind == TreeKind.Pine ? .34f : .30f), 0, Body(.5f), 4);                          // 12
                return;
            }
            if (lod == TreeLod.Mid)
            {
                var wide = kind == TreeKind.Pine ? 1.2f : kind == TreeKind.Fir ? 1.08f : 1f;
                mesh.Branch(at - up * .3f, P(height * .2f), tr * 1.2f, tr * .8f, bark, 3);                     // 21
                mesh.Branch(P(height * .16f), P(height * .66f), height * .30f * wide, 0, Body(.2f), 5);        // 15
                mesh.Branch(P(height * .44f), P(height), height * .20f * wide, 0, Body(.8f), 5);              // 15
                return;
            }
            if (kind == TreeKind.Spruce)
            {
                mesh.Branch(at - up * .4f, P(height * .2f), tr * 1.2f, tr * .6f, bark, 4);                     // 28
                for (var i = 0; i < 5; i++)                                                                     // 5 x 21
                {
                    var u = i / 4f; var baseY = height * (.14f + u * .52f);
                    mesh.Branch(P(baseY), P(baseY + height * .34f), height * .27f * (1 - u * .72f), 0, Body(u), 7);
                }
            }
            else if (kind == TreeKind.Fir)
            {
                mesh.Branch(at - up * .4f, P(height * .2f), tr * 1.2f, tr * .6f, bark, 4);                     // 28
                for (var i = 0; i < 4; i++)                                                                     // 4 x 21
                {
                    var u = i / 3f; var baseY = height * (.12f + u * .50f);
                    mesh.Branch(P(baseY), P(baseY + height * .40f), height * .30f * (1 - u * .62f), 0, Body(u), 7);
                }
            }
            else
            {
                // pine: a bare upper trunk under broad, flatter tiers
                mesh.Branch(at - up * .4f, P(height * .62f), tr * 1.15f, tr * .55f, bark, 5);                  // 35
                for (var i = 0; i < 4; i++)                                                                     // 4 x 21
                {
                    var u = i / 3f; var baseY = height * (.36f + u * .20f);
                    mesh.Branch(P(baseY), P(baseY + height * (.28f + u * .16f)), height * .36f * (1 - u * .60f), 0, Body(u), 7);
                }
            }
        }

        public void Flush(Transform town, Transform forest, Material material)
        {
            foreach (var tile in tiles)
                tile.Value.Spawn("Woodland stands " + (TreeLod)tile.Key.Item3 + " " + tile.Key.Item1 + "," + tile.Key.Item2, tile.Key.Item4 ? town : forest, material, TownBuilder.FoliageLayer);
            tiles.Clear();
        }
    }
}
