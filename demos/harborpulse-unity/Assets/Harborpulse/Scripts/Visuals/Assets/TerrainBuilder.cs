// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    internal static class TerrainBuilder
    {
        static Color Mix(Color a, Color b, float t) => Color.Lerp(a, b, Mathf.Clamp01(t));
        // The vertex alpha is the shader's surface code (1 for scenery): scale the colour only, never the code.
        static Color Scale(Color c, float s) => new Color(c.r * s, c.g * s, c.b * s, 1);

        /// <summary>
        /// The ground: one grid (the plan's own, so every rock and tree stands on the surface drawn here), coloured by what grows there, and the
        /// granite outcrops fitted into it. Everything shares the terrain tiles: no renderer per rock.
        /// </summary>
        public static void Build(Transform parent, Material material, WorldPropPlan plan, Func<float, float, float> height)
        {
            var xs = plan.Terrain.Xs; var zs = plan.Terrain.Zs;
            var tiles = new Dictionary<(int, int), SceneryMesh>();
            SceneryMesh Tile(float x, float z)
            {
                var key = (Mathf.FloorToInt(Mathf.Clamp(x, -3799, 3799) / 900), Mathf.FloorToInt(Mathf.Min(z, 2399) / 900));
                if (!tiles.TryGetValue(key, out var mesh)) tiles[key] = mesh = new SceneryMesh();
                return mesh;
            }
            Vector3 P(float x, float z) => new Vector3(x, height(x, z), z);

            // Yards: the ground within a few metres of a house keeps the lawn green; a spatial hash keeps the lookup cheap.
            var yards = new Dictionary<(int, int), List<Ground>>();
            foreach (var lot in plan.Props)
            {
                if (lot.Kind != "House") continue;
                var key = (Mathf.FloorToInt((float)lot.At.X / 40), Mathf.FloorToInt((float)lot.At.Z / 40));
                if (!yards.TryGetValue(key, out var list)) yards[key] = list = new List<Ground>();
                list.Add(lot.At);
            }
            float YardWeight(float x, float z)
            {
                var best = 99f;
                int cx = Mathf.FloorToInt(x / 40), cz = Mathf.FloorToInt(z / 40);
                for (var i = -1; i <= 1; i++)
                    for (var j = -1; j <= 1; j++)
                        if (yards.TryGetValue((cx + i, cz + j), out var list))
                            foreach (var g in list) best = Mathf.Min(best, Mathf.Sqrt((float)((g.X - x) * (g.X - x) + (g.Z - z) * (g.Z - z))));
                return 1 - Mathf.SmoothStep(0, 1, (best - 8) / 12);
            }
            // Bedrock skirts: darkened ground round each outcrop.
            var skirts = new Dictionary<(int, int), List<Outcrop>>();
            foreach (var o in plan.Outcrops)
            {
                var key = (Mathf.FloorToInt((float)o.Center.X / 60), Mathf.FloorToInt((float)o.Center.Z / 60));
                if (!skirts.TryGetValue(key, out var list)) skirts[key] = list = new List<Outcrop>();
                list.Add(o);
            }
            float Skirt(float x, float z)
            {
                var best = 0f;
                int cx = Mathf.FloorToInt(x / 60), cz = Mathf.FloorToInt(z / 60);
                for (var i = -1; i <= 1; i++)
                    for (var j = -1; j <= 1; j++)
                        if (skirts.TryGetValue((cx + i, cz + j), out var list))
                            foreach (var o in list) best = Mathf.Max(best, (float)o.Skirt(x, z));
                return best;
            }

            var forestFloor = new Color(.20f, .23f, .14f);
            var forestWarm = new Color(.27f, .24f, .15f);
            var meadowGreen = new Color(.46f, .52f, .33f);
            var meadowStraw = new Color(.60f, .56f, .36f);
            var meadowWarm = new Color(.64f, .52f, .32f);
            var lawn = new Color(.40f, .51f, .28f);
            var heath = new Color(.47f, .44f, .35f);
            var worn = new Color(.37f, .32f, .23f);

            for (var j = 0; j < zs.Count - 1; j++)
                for (var i = 0; i < xs.Count - 1; i++)
                {
                    var x = (xs[i] + xs[i + 1]) * .5f; var z = (zs[j] + zs[j + 1]) * .5f;
                    if (Mathf.Abs(x) < TownBuilder.CoreHalfX && z < TownBuilder.CoreNorthZ) continue;
                    var a = P(xs[i], zs[j]); var b = P(xs[i + 1], zs[j]);
                    var c = P(xs[i], zs[j + 1]); var d = P(xs[i + 1], zs[j + 1]);
                    var n = Vector3.Cross(c - a, d - a).normalized;
                    // what grows here: forest floor under the wood, dry autumn grass between, in warmer and greener drifts
                    var cover = (float)CoastalTerrain.ForestCover(x, z);
                    var drift = (float)CoastalTerrain.Noise(x * .006 + 4, z * .006);
                    var patch = (float)CoastalTerrain.Noise(x * .02 + 9, z * .02 - 3);
                    var meadow = Mix(Mix(meadowGreen, meadowStraw, Mathf.SmoothStep(.25f, .7f, drift)), meadowWarm, Mathf.SmoothStep(.62f, .9f, patch) * .6f);
                    var floor = Mix(forestFloor, forestWarm, Mathf.SmoothStep(.3f, .8f, patch));
                    var color = Mix(meadow, floor, cover);
                    var inland = (float)CoastalTerrain.CoreDistance(x, z);
                    // lawn green only on the park and the yards
                    color = Mix(color, lawn, YardWeight(x, z) * (1 - cover * .7f));
                    // small worn-earth aprons, with the meadow remaining between the streets and houses
                    if (inland < 320)
                    {
                        var edge = float.MaxValue;
                        foreach (var street in plan.Streets) edge = Mathf.Min(edge, (float)(street.DistanceTo(new Ground(x, z)) - street.Width / 2));
                        color = Mix(color, worn, 1 - Mathf.SmoothStep(0, 1, edge / 3));
                    }
                    // the open coast's edge is turf and bare heath in a strip of uneven width, not forest floor ruled off at the water
                    if (Mathf.Abs(x) > TownBuilder.CoreHalfX)
                    {
                        var strip = 22 + 60 * (float)CoastalTerrain.Noise(x * .011 + 17, 3.3);
                        var shore = 1 - Mathf.SmoothStep(0, 1, (z - TownBuilder.CoastZ) / strip);
                        color = Mix(color, Mix(heath, meadowStraw, Mathf.SmoothStep(0, 1, Mathf.InverseLerp(.3f, .8f, patch))), shore * .85f);
                    }
                    // rocky, heath-toned ground where the slope steepens, and the bedrock skirt round every outcrop
                    var bare = Mathf.Clamp01((.985f - n.y) * 9);
                    color = Mix(color, heath, bare * .5f);
                    var skirt = Skirt(x, z);
                    color = Mix(color, Scale(heath, .72f), skirt * .75f);
                    // Flat facets show the knolls; a whisper of per-facet variation, no alternating checkerboard
                    var shade = .985f + .03f * (float)CoastalTerrain.Noise(x * .05, z * .05) + .008f * (float)CoastalTerrain.Hash(i, j);
                    Tile(x, z).Quad(a, c, d, b, Scale(color, shade));
                }
            // Verges share the terrain tiles: widening Main Street adds no renderer or shadow caster.
            foreach (var street in plan.Streets)
            {
                var steps = Math.Max(1, (int)Math.Ceiling(street.Length / 8));
                foreach (var side in new[] { -1, 1 })
                {
                    Vector3 inner = default, outer = default;
                    for (var i = 0; i <= steps; i++)
                    {
                        street.At(street.Length * i / steps, out var p, out var t);
                        var width = street.Name == "Main Street" ? 2.0 : 1.1;
                        var offset = street.Width * .5;
                        var a = P((float)(p.X + t.Z * side * offset), (float)(p.Z - t.X * side * offset)) + Vector3.up * .07f;
                        var b = P((float)(p.X + t.Z * side * (offset + width)), (float)(p.Z - t.X * side * (offset + width))) + Vector3.up * .07f;
                        if (i > 0)
                        {
                            var kit = Tile((float)p.X, (float)p.Z);
                            var color = new Color(.43f, .46f, .34f);
                            if (side > 0) kit.Quad(inner, a, b, outer, color); else kit.Quad(outer, b, a, inner, color);
                        }
                        inner = a; outer = b;
                    }
                }
            }
            // Granite outcrops: stepped, faceted ledges sunk into the slope (the model fits and checks every ring), sharing the terrain tiles.
            foreach (var o in plan.Outcrops) DrawOutcrop(Tile((float)o.Center.X, (float)o.Center.Z), o);
            foreach (var tile in tiles) tile.Value.Spawn("Coastal terrain and granite " + tile.Key, parent, material);
        }

        static Vector3 V(DeviceChain.Harborpulse.Model.Director.Point3 p) => new Vector3((float)p.X, (float)p.Y, (float)p.Z);

        /// <summary>The scenery shader's code for outcrop rock (vertex alpha): it adds the grain, the bedding cracks and the lichen.</summary>
        public const float RockCode = .9f;

        /// <summary>
        /// Granite that reads as rock from HOME as well as close: three values of warm grey chosen facet by facet (so one outcrop is never one
        /// pale patch), ledge tops lighter than the walls, every wall darkening to a shadowed foot where it meets the ground or the ledge
        /// below, the walls that face away from the sun's arc darker again, and a few facets taken by lichen or moss. The scenery mesh casts
        /// and takes no shadow, so the shading that makes a ledge a ledge is in the colours.
        /// </summary>
        static void DrawOutcrop(SceneryMesh kit, Outcrop o)
        {
            // warm greys: the scenery's light is mostly sky, which turns a neutral grey blue
            var light = new Color(.76f, .69f, .57f); var mid = new Color(.61f, .54f, .45f); var dark = new Color(.40f, .36f, .31f);
            var lichen = new Color(.55f, .59f, .38f); var rust = new Color(.58f, .42f, .25f); var moss = new Color(.25f, .31f, .20f);
            Color Rock(Color c, float k) => new Color(c.r * k, c.g * k, c.b * k, RockCode);
            Color Granite(int ring, int i, bool top)
            {
                var h = (float)CoastalTerrain.Hash(o.Seed * 31 + ring * 7, i * 13 + 5);
                var v = (float)CoastalTerrain.Hash(o.Seed + ring, i * 3 + 1);
                // most facets mid grey, a third light, a fifth dark and wet
                var tone = v < .2f ? Color.Lerp(dark, mid, v * 2.5f) : v < .66f ? Color.Lerp(mid, light, (v - .2f) * .9f) : Color.Lerp(mid, light, .55f + (v - .66f) * 1.3f);
                if (top)
                {
                    if (h > .80f) tone = Color.Lerp(tone, lichen, .55f);
                    else if (h < .09f) tone = Color.Lerp(tone, rust, .45f);
                }
                else if (h > .86f) tone = Color.Lerp(tone, moss, .6f);
                return tone;
            }
            var rings = o.Rings;
            var centre = new Vector3((float)o.Center.X, 0, (float)o.Center.Z);
            for (var r = 0; r + 1 < rings.Length; r++)
                for (var i = 0; i < rings[r].Length; i++)
                {
                    var k = (i + 1) % rings[r].Length;
                    Vector3 a = V(rings[r][i]), b = V(rings[r + 1][i]), c = V(rings[r + 1][k]), d = V(rings[r][k]);
                    // an outward-facing wall between this ring and the next one up, wound like the terrain's quads
                    var outward = (a + d) * .5f - centre; outward.y = 0; outward.Normalize();
                    // the sun stays in the southern half of the sky: a wall that faces north is in shade all day
                    var shade = Mathf.Lerp(1f, .68f, Mathf.Clamp01(outward.z * 1.4f));
                    var tone = Granite(r, i, false);
                    var foot = Rock(tone, .58f * shade); var lip = Rock(tone, .96f * shade);
                    kit.Quad(a, b, c, d, foot, lip, lip, foot);
                }
            var top = rings[rings.Length - 1];
            var crown = V(o.Crown);
            for (var i = 0; i < top.Length; i++)
                kit.Tri(V(top[i]), crown, V(top[(i + 1) % top.Length]), Rock(Granite(rings.Length, i, true), 1.12f));
            // the ledges between the tiers: the flat shelf from each ring's lip in to the foot of the wall above is drawn by the walls'
            // own slope here, so nothing more is needed; a dark fissure across the crown breaks the top into two blocks
            if (top.Length >= 6)
            {
                var i0 = (int)(CoastalTerrain.Hash(o.Seed, 91) * top.Length); var i1 = (i0 + top.Length / 2) % top.Length;
                Vector3 p0 = Vector3.Lerp(V(top[i0]), crown, .08f), p1 = Vector3.Lerp(V(top[i1]), crown, .08f);
                var across = Vector3.Cross(Vector3.up, (p1 - p0).normalized) * Mathf.Max(.12f, .035f * (float)o.Radius);
                var lift = Vector3.up * .04f;
                var fissure = Rock(dark, .55f);
                kit.Facing(p0 + lift, crown - across + lift, crown + across + lift, Vector3.up, fissure);
                kit.Facing(p1 + lift, crown + across + lift, crown - across + lift, Vector3.up, fissure);
            }
        }
    }
}
