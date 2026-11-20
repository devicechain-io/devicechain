// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The land behind the waterfront: a rising ground with forested hills, a small town on an organic main street and a few side lanes,
    /// a park lawn with a gazebo, and the working waterfront (piers on pilings, moored boats). Everything is WORLD scenery: no device stands
    /// here, nothing reads the platform, and the core round the quays (the water, the quays, the mapped road, plain and depot) is left
    /// exactly as <see cref="HarborWorld"/> draws it. Seeded, so the same town comes out every time.
    /// </summary>
    public sealed class TownBuilder
    {
        /// <summary>The ground is the deck everywhere inside this rectangle (|x| below it, z below the north edge), and rises outside it.</summary>
        public const float CoreHalfX = 240, CoreNorthZ = 140, CoastZ = -140;
        const float Tile = 300;
        static float Deck => (float)SitePlan.DeckHeightM;

        readonly HarborMaterials m;
        readonly Transform root;
        readonly System.Random random = new System.Random(1789);
        readonly Dictionary<(Material, int, int, bool, bool), MeshKit> kits = new Dictionary<(Material, int, int, bool, bool), MeshKit>();
        readonly List<Street> streets = new List<Street>();
        readonly List<Vector3> footprints = new List<Vector3>(); // x, z, radius
        readonly List<Vector2> keepClear;
        readonly Func<Vector2, bool> reserved;
        readonly List<Transform> boats = new List<Transform>();
        readonly List<Vector2> boatRest = new List<Vector2>();

        public IReadOnlyList<Transform> Boats => boats;
        public IReadOnlyList<Vector2> BoatRest => boatRest;
        /// <summary>The terrain and the forest on it: the HOME check's backdrop ("ridge").</summary>
        public Transform Backdrop { get; private set; }
        public Transform Town { get; private set; }

        sealed class Street
        {
            public readonly Vector2[] Points; public readonly float HalfWidth; public readonly string Name;
            public Street(string name, float width, params Vector2[] points) { Name = name; HalfWidth = width / 2; Points = points; }
            public float Length { get { var l = 0f; for (var i = 1; i < Points.Length; i++) l += Vector2.Distance(Points[i - 1], Points[i]); return l; } }
            /// <summary>The point and unit tangent at distance <paramref name="s"/> along the street.</summary>
            public void At(float s, out Vector2 p, out Vector2 t)
            {
                for (var i = 1; i < Points.Length; i++)
                {
                    var seg = Vector2.Distance(Points[i - 1], Points[i]);
                    if (s <= seg || i == Points.Length - 1)
                    {
                        t = (Points[i] - Points[i - 1]).normalized;
                        p = Points[i - 1] + t * Mathf.Min(s, seg);
                        return;
                    }
                    s -= seg;
                }
                p = Points[0]; t = Vector2.up;
            }
            public float Distance(Vector2 p)
            {
                var best = float.MaxValue;
                for (var i = 1; i < Points.Length; i++)
                {
                    var a = Points[i - 1]; var ab = Points[i] - a;
                    var u = Mathf.Clamp01(Vector2.Dot(p - a, ab) / Mathf.Max(1e-4f, ab.sqrMagnitude));
                    best = Mathf.Min(best, Vector2.Distance(p, a + ab * u));
                }
                return best;
            }
        }

        TownBuilder(Transform parent, HarborMaterials mats, List<Vector2> keepClear, Func<Vector2, bool> reserved)
        {
            m = mats; this.keepClear = keepClear; this.reserved = reserved;
            root = AssetRig.Node("Town and hills", parent, Vector3.zero);
        }

        /// <param name="keepClear">Points (device anchors, the depot) nothing may be built within 30 m of.</param>
        /// <param name="reserved">Ground the core owns (the mapped fences and road): no tree or building there.</param>
        public static TownBuilder Build(Transform parent, HarborMaterials mats, List<Vector2> keepClear, Func<Vector2, bool> reserved)
        {
            var b = new TownBuilder(parent, mats, keepClear, reserved);
            b.Streets();
            b.Terrain();
            b.Lots();
            b.Landmarks();
            b.Waterfront();
            b.Park();
            b.Trees();
            b.Flush();
            return b;
        }

        // ---------------------------------------------------------------- ground

        static float Smooth(float a, float b, float x) { var t = Mathf.Clamp01((x - a) / (b - a)); return t * t * (3 - 2 * t); }

        static float Hash(int x, int z)
        {
            unchecked
            {
                var h = (uint)(x * 374761393 + z * 668265263);
                h = (h ^ (h >> 13)) * 1274126177u;
                return ((h ^ (h >> 16)) & 0xffffff) / 16777215f;
            }
        }

        static float Noise(float x, float z)
        {
            int ix = Mathf.FloorToInt(x), iz = Mathf.FloorToInt(z);
            float fx = x - ix, fz = z - iz;
            fx = fx * fx * (3 - 2 * fx); fz = fz * fz * (3 - 2 * fz);
            return Mathf.Lerp(Mathf.Lerp(Hash(ix, iz), Hash(ix + 1, iz), fx), Mathf.Lerp(Hash(ix, iz + 1), Hash(ix + 1, iz + 1), fx), fz);
        }

        static float Fbm(float x, float z) => (Noise(x, z) * 0.57f + Noise(x * 2.03f + 11, z * 2.03f - 5) * 0.29f + Noise(x * 4.1f - 3, z * 4.1f + 9) * 0.14f);

        static float CoreDistance(float x, float z)
        {
            float dx = Mathf.Max(0, Mathf.Abs(x) - CoreHalfX), dz = Mathf.Max(0, z - CoreNorthZ);
            return Mathf.Sqrt(dx * dx + dz * dz);
        }

        /// <summary>The ground's height: the deck in the core, a gentle rise for the town, then forested hills and, far off, higher ridges.</summary>
        public static float Height(float x, float z)
        {
            var d = CoreDistance(x, z);
            if (d <= 0) return Deck;
            var coast = Smooth(CoastZ, CoastZ + 120, z);
            var slope = 30f * Smooth(0, 400, d);
            var hills = Smooth(300, 720, d) * (55 + 175 * Fbm(x * 0.0024f + 3.1f, z * 0.0024f - 7.3f));
            var far = Smooth(1300, 2300, d) * (120 + 120 * Noise(x * 0.0011f, z * 0.0011f + 4));
            return Deck + (slope + hills + far) * coast;
        }

        static List<float> Axis(float from, float to, float denseUntil, float denseStep)
        {
            var v = new List<float>(); var step = denseStep; var x = from;
            while (x < to)
            {
                v.Add(x);
                if (Mathf.Abs(x) >= denseUntil || x >= denseUntil) step = Mathf.Min(step * 1.13f, 150);
                x += step;
            }
            v.Add(to);
            return v;
        }

        void Terrain()
        {
            Backdrop = AssetRig.Node("Hills and forest", root, Vector3.zero);
            // a rectilinear grid, dense (10 m) round the town and coarser outward, with no T-junction; quads inside the core are left out
            var right = Axis(0, 3400, 720, 10);
            var xs = new List<float>(); for (var i = right.Count - 1; i > 0; i--) xs.Add(-right[i]); xs.AddRange(right);
            var zs = Axis(CoastZ, 3400, 820, 10);
            var nx = xs.Count; var nz = zs.Count;
            var vertices = new Vector3[nx * nz];
            for (var j = 0; j < nz; j++) for (var i = 0; i < nx; i++) vertices[j * nx + i] = new Vector3(xs[i], Height(xs[i], zs[j]), zs[j]);
            var tris = new List<int>();
            for (var j = 0; j < nz - 1; j++)
                for (var i = 0; i < nx - 1; i++)
                {
                    float cx = (xs[i] + xs[i + 1]) / 2, cz = (zs[j] + zs[j + 1]) / 2;
                    if (Mathf.Abs(cx) < CoreHalfX && cz < CoreNorthZ) continue;
                    int a = j * nx + i, b = a + 1, c = a + nx, d = c + 1;
                    tris.Add(a); tris.Add(c); tris.Add(d); tris.Add(a); tris.Add(d); tris.Add(b);
                }
            var mesh = new Mesh { name = "terrain", indexFormat = IndexFormat.UInt32 };
            mesh.vertices = vertices; mesh.SetTriangles(tris, 0); mesh.RecalculateNormals(); mesh.RecalculateBounds();
            var go = new GameObject("Terrain");
            go.transform.SetParent(Backdrop, false);
            go.AddComponent<MeshFilter>().sharedMesh = mesh;
            var mr = go.AddComponent<MeshRenderer>(); mr.sharedMaterial = m.Ground; mr.shadowCastingMode = ShadowCastingMode.Off;
            // the coast's face where the side lands meet the sea, and the strips that join the terrain to the quays' deck
            new MeshKit().Slab(-3400, -CoreHalfX, CoastZ - 1, CoastZ, HarborWorld.SeabedY, Deck)
                .Slab(CoreHalfX, 3400, CoastZ - 1, CoastZ, HarborWorld.SeabedY, Deck).Spawn("Coast", Backdrop, m.Stone);
            // the streets, draped over the ground
            var road = new MeshKit();
            foreach (var s in streets) Ribbon(road, s);
            road.Spawn("Streets", root, m.Lane, false);
        }

        static void Ribbon(MeshKit kit, Street s)
        {
            var length = s.Length; Vector3 pl = default, pr = default;
            for (var d = 0f; d <= length + 0.01f; d += 4)
            {
                s.At(Mathf.Min(d, length), out var p, out var t);
                var n = new Vector2(t.y, -t.x) * s.HalfWidth;
                var l = new Vector3(p.x - n.x, 0, p.y - n.y); l.y = Height(l.x, l.z) + 0.14f;
                var r = new Vector3(p.x + n.x, 0, p.y + n.y); r.y = Height(r.x, r.z) + 0.14f;
                if (d > 0) kit.Quad(pl, l, r, pr);
                pl = l; pr = r;
            }
        }

        // ---------------------------------------------------------------- batching

        MeshKit Kit(Material mat, Vector3 at, bool shadows = true, bool forest = false)
        {
            var key = (mat, Mathf.FloorToInt(at.x / Tile), Mathf.FloorToInt(at.z / Tile), shadows, forest);
            if (!kits.TryGetValue(key, out var kit)) kits[key] = kit = new MeshKit();
            return kit;
        }

        void Flush()
        {
            Town = AssetRig.Node("Town", root, Vector3.zero);
            var forest = AssetRig.Node("Forest", Backdrop, Vector3.zero);
            foreach (var pair in kits)
            {
                if (pair.Value.VertexCount == 0) continue;
                pair.Value.Spawn(pair.Key.Item1.name.Replace("Harbor/", "") + " " + pair.Key.Item2 + "," + pair.Key.Item3, pair.Key.Item5 ? forest : Town, pair.Key.Item1, pair.Key.Item4);
            }
            kits.Clear();
        }

        // ---------------------------------------------------------------- the plan

        void Streets()
        {
            // Main Street climbs inland from the North Quay road between the lofts and the north pump, bending as it goes
            streets.Add(new Street("Main Street", 9, new Vector2(-46, 124), new Vector2(-44, 158), new Vector2(-34, 198), new Vector2(-16, 238),
                new Vector2(-2, 280), new Vector2(-8, 322), new Vector2(-30, 366), new Vector2(-62, 410)));
            streets.Add(new Street("Elm Street", 7, new Vector2(-30, 192), new Vector2(-78, 202), new Vector2(-132, 222), new Vector2(-190, 246), new Vector2(-262, 262), new Vector2(-330, 260)));
            streets.Add(new Street("Church Street", 7, new Vector2(-12, 250), new Vector2(42, 252), new Vector2(98, 266), new Vector2(150, 286), new Vector2(214, 306), new Vector2(286, 312)));
            streets.Add(new Street("Shore Road", 7, new Vector2(108, 124), new Vector2(170, 132), new Vector2(236, 146), new Vector2(292, 160), new Vector2(330, 196), new Vector2(350, 250)));
            streets.Add(new Street("Wharf Street", 7, new Vector2(-41, 166), new Vector2(18, 170), new Vector2(78, 164), new Vector2(138, 174), new Vector2(204, 186), new Vector2(262, 204)));
            streets.Add(new Street("School Street", 6, new Vector2(-43, 172), new Vector2(-98, 168), new Vector2(-122, 186), new Vector2(-168, 198)));
            streets.Add(new Street("Hill Lane", 6, new Vector2(-7, 322), new Vector2(46, 344), new Vector2(102, 352), new Vector2(150, 372)));
            streets.Add(new Street("West Lane", 6, new Vector2(-236, 168), new Vector2(-292, 150), new Vector2(-340, 110), new Vector2(-380, 52), new Vector2(-430, 4)));
        }

        float Clearance(Vector2 p)
        {
            var best = float.MaxValue;
            foreach (var s in streets) best = Mathf.Min(best, s.Distance(p) - s.HalfWidth);
            return best;
        }

        bool Free(Vector2 c, float radius, bool allowCore = false)
        {
            if (!allowCore && Mathf.Abs(c.x) < CoreHalfX + radius * 0.5f && c.y < CoreNorthZ + radius * 0.5f) return false;
            if (reserved(c)) return false;
            foreach (var k in keepClear) if (Vector2.Distance(k, c) < 30 + radius) return false;
            foreach (var f in footprints) if (Vector2.Distance(new Vector2(f.x, f.y), c) < f.z + radius) return false;
            if (InPark(c, radius)) return false;
            return true;
        }

        static bool InPark(Vector2 p, float margin) => p.x > -240 - margin && p.x < -120 + margin && p.y > 72 - margin && p.y < 180 + margin;

        enum Kind { House, Shopfront, Commercial, Shed, Inn }

        void Lots()
        {
            foreach (var s in streets)
            {
                var length = s.Length;
                foreach (var side in new[] { -1, 1 })
                {
                    var at = 6f + (float)random.NextDouble() * 6;
                    var innPlaced = false;
                    while (at < length - 8)
                    {
                        Kind kind;
                        if (s.Name == "Main Street" && at < 120) kind = random.NextDouble() < 0.6 ? Kind.Commercial : Kind.Shopfront;
                        else if (s.Name == "Main Street" && !innPlaced && side < 0 && at > 130) { kind = Kind.Inn; innPlaced = true; }
                        else if (s.Name == "Shore Road" && at < 140) kind = Kind.Shed;
                        else kind = Kind.House;
                        float w, d;
                        switch (kind)
                        {
                            case Kind.Commercial: w = 15 + random.Next(6); d = 13; break;
                            case Kind.Shopfront: w = 10 + random.Next(3); d = 11; break;
                            case Kind.Inn: w = 24; d = 14; break;
                            case Kind.Shed: w = 18 + random.Next(6); d = 11; break;
                            default: w = 9 + random.Next(4); d = 8 + random.Next(3); break;
                        }
                        s.At(at + w / 2, out var p, out var t);
                        var n = new Vector2(t.y, -t.x) * side;
                        var setback = kind == Kind.Commercial || kind == Kind.Shopfront ? 1.5f : 3 + (float)random.NextDouble() * 4;
                        var c = p + n * (s.HalfWidth + setback + d / 2);
                        var yaw = Mathf.Atan2(-n.x, -n.y) * Mathf.Rad2Deg;
                        var radius = 0.5f * Mathf.Sqrt(w * w + d * d);
                        var ok = Free(c, radius * 0.9f) && CornersClear(c, w, d, yaw, s.HalfWidth);
                        if (ok && random.NextDouble() < (kind == Kind.House ? 0.92 : 1))
                        {
                            Building(kind, new Vector3(c.x, 0, c.y), yaw, w, d);
                            footprints.Add(new Vector3(c.x, c.y, radius * 0.9f));
                        }
                        at += w + (kind == Kind.Commercial || kind == Kind.Shopfront ? 1.5f + (float)random.NextDouble() * 3 : 3.5f + (float)random.NextDouble() * 7);
                    }
                }
            }
        }

        bool CornersClear(Vector2 c, float w, float d, float yaw, float street)
        {
            var r = Quaternion.Euler(0, yaw, 0);
            foreach (var sx in new[] { -1, 1 }) foreach (var sz in new[] { -1, 1 })
            {
                var v = r * new Vector3(sx * w / 2, 0, sz * d / 2);
                if (Clearance(new Vector2(c.x + v.x, c.y + v.z)) < 0.8f) return false;
            }
            return true;
        }

        void Landmarks()
        {
            // a white clapboard church with its steeple, where Church Street leaves Main Street
            var church = new Vector2(30, 274);
            Church(new Vector3(church.x, 0, church.y), 180);
            footprints.Add(new Vector3(church.x, church.y, 18));
        }

        // ---------------------------------------------------------------- buildings

        Material Paint()
        {
            var r = random.NextDouble();
            return r < 0.38 ? m.ClapWhite : r < 0.55 ? m.ClapCream : r < 0.68 ? m.ClapGrey : r < 0.78 ? m.ClapBlue : r < 0.86 ? m.ClapSage : r < 0.93 ? m.ClapYellow : m.BarnRed;
        }

        Material Roof()
        {
            var r = random.NextDouble();
            return r < 0.5 ? m.Slate : r < 0.75 ? m.RoofBrown : r < 0.9 ? m.RoofGreen : m.RoofRed;
        }

        /// <summary>The ground under a footprint's highest corner, so no wall floats on the slope.</summary>
        static float Ground(Vector3 at, float w, float d, float yaw)
        {
            var r = Quaternion.Euler(0, yaw, 0); var g = Height(at.x, at.z);
            foreach (var sx in new[] { -1, 1 }) foreach (var sz in new[] { -1, 1 })
            { var v = at + r * new Vector3(sx * w / 2, 0, sz * d / 2); g = Mathf.Max(g, Height(v.x, v.z)); }
            return g;
        }

        void Building(Kind kind, Vector3 at, float yaw, float w, float d)
        {
            at.y = Ground(at, w, d, yaw);
            switch (kind)
            {
                case Kind.Commercial: Commercial(at, yaw, w, d); break;
                case Kind.Shopfront: House(at, yaw, w, d, 2, Paint(), Roof(), true, true); break;
                case Kind.Inn: Inn(at, yaw, w, d); break;
                case Kind.Shed: Shed(at, yaw, w, d); break;
                default: House(at, yaw, w, d, random.NextDouble() < 0.7 ? 2 : 1, Paint(), Roof(), random.NextDouble() < 0.4, false); break;
            }
        }

        MeshKit P(Material mat, Vector3 at, float yaw, bool shadows = true) => Kit(mat, at, shadows).Place(at, yaw);

        Material WindowFor(Vector3 at, int seed)
        {
            // about half the windows are lit at night; the east of the town is on the east feeder
            var h = Hash(Mathf.RoundToInt(at.x * 7) + seed * 31, Mathf.RoundToInt(at.z * 7) - seed * 17);
            if (h > 0.55f) return m.WindowsDark;
            return at.x > 140 ? m.EastWindows : m.Windows;
        }

        /// <summary>Windows in a row on one facade: <paramref name="face"/> is the facade's outward local axis.</summary>
        void Windows(Vector3 at, float yaw, Vector3 origin, Vector3 along, Vector3 face, float span, int floors, float floorHeight, float sill, Vector2 size, float spacing, int seed)
        {
            var count = Mathf.Max(1, Mathf.FloorToInt((span - 1.5f) / spacing));
            var start = -(count - 1) * spacing / 2;
            for (var f = 0; f < floors; f++)
                for (var i = 0; i < count; i++)
                {
                    var local = origin + along * (start + i * spacing) + Vector3.up * (sill + f * floorHeight + size.y / 2) + face * 0.06f;
                    var world = at + Quaternion.Euler(0, yaw, 0) * local;
                    var dims = new Vector3(Mathf.Abs(along.x) * size.x + Mathf.Abs(face.x) * 0.12f, size.y, Mathf.Abs(along.z) * size.x + Mathf.Abs(face.z) * 0.12f);
                    P(WindowFor(world, seed + f * 13 + i), at, yaw, false).Box(local, dims);
                }
        }

        void AllWindows(Vector3 at, float yaw, float w, float d, int floors, float floorHeight, float sill, Vector2 size, float spacing, bool front = true)
        {
            if (front) Windows(at, yaw, new Vector3(0, 0, -d / 2), Vector3.right, Vector3.back, w, floors, floorHeight, sill, size, spacing, 1);
            Windows(at, yaw, new Vector3(0, 0, d / 2), Vector3.right, Vector3.forward, w, floors, floorHeight, sill, size, spacing, 2);
            Windows(at, yaw, new Vector3(-w / 2, 0, 0), Vector3.forward, Vector3.left, d, floors, floorHeight, sill, size, spacing + 0.8f, 3);
            Windows(at, yaw, new Vector3(w / 2, 0, 0), Vector3.forward, Vector3.right, d, floors, floorHeight, sill, size, spacing + 0.8f, 4);
        }

        void House(Vector3 at, float yaw, float w, float d, int floors, Material paint, Material roof, bool porch, bool shop)
        {
            var h = floors * 3.1f + 0.6f;
            P(paint, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            var gableFront = random.NextDouble() < 0.45;
            var rise = Mathf.Min(w, d) * (gableFront ? 0.42f : 0.36f);
            if (gableFront) P(roof, at, yaw).Gable(new Vector3(0, h, 0), d + 0.8f, w + 0.7f, rise, 90);
            else P(roof, at, yaw).Gable(new Vector3(0, h, 0), w + 0.8f, d + 0.7f, rise);
            // white corner boards and a chimney give the clapboard its read at a distance
            if (paint != m.ClapWhite)
                foreach (var sx in new[] { -1, 1 }) foreach (var sz in new[] { -1, 1 })
                    P(m.Trim, at, yaw).Box(new Vector3(sx * (w / 2 - 0.1f), h / 2 - 0.2f, sz * (d / 2 - 0.1f)), new Vector3(0.35f, h + 0.4f, 0.35f));
            if (random.NextDouble() < 0.7) P(m.Brick, at, yaw).Box(new Vector3(w * 0.3f * (random.Next(2) * 2 - 1), h + rise * 0.6f, d * 0.15f), new Vector3(0.9f, 2.6f, 0.9f));
            if (shop)
            {
                // a shop window band and an awning in an accent colour
                P(m.Glass, at, yaw, false).Box(new Vector3(0, 1.5f, -d / 2 - 0.05f), new Vector3(w - 2.5f, 2.0f, 0.1f));
                P(Accent(), at, yaw).Box(new Vector3(0, 3.0f, -d / 2 - 0.9f), new Vector3(w - 1.5f, 0.18f, 1.8f));
                Windows(at, yaw, new Vector3(0, 0, -d / 2), Vector3.right, Vector3.back, w, floors - 1, 3.1f, 4.0f, new Vector2(1.0f, 1.5f), 2.6f, 5);
                AllWindows(at, yaw, w, d, floors, 3.1f, 0.9f, new Vector2(1.0f, 1.4f), 2.8f, false);
            }
            else AllWindows(at, yaw, w, d, floors, 3.1f, 0.9f, new Vector2(1.0f, 1.4f), 2.8f);
            if (porch)
            {
                var pw = Mathf.Min(w - 1, 7);
                P(m.Trim, at, yaw).Box(new Vector3(0, 0.25f, -d / 2 - 1.3f), new Vector3(pw, 0.5f, 2.6f));
                P(roof, at, yaw).Box(new Vector3(0, 3.0f, -d / 2 - 1.3f), new Vector3(pw + 0.4f, 0.2f, 2.9f));
                for (var x = -pw / 2 + 0.3f; x <= pw / 2 - 0.2f; x += (pw - 0.6f) / 2)
                    P(m.Trim, at, yaw).Box(new Vector3(x, 1.6f, -d / 2 - 2.4f), new Vector3(0.22f, 2.8f, 0.22f));
            }
            P(m.DoorDark, at, yaw, false).Box(new Vector3(w * 0.18f, 1.1f, -d / 2 - 0.05f), new Vector3(1.1f, 2.2f, 0.1f));
        }

        Material Accent()
        {
            var r = random.Next(4);
            return r == 0 ? m.AwningGreen : r == 1 ? m.AwningBlue : r == 2 ? m.BarnRed : m.ClapYellow;
        }

        void Commercial(Vector3 at, float yaw, float w, float d)
        {
            var floors = random.NextDouble() < 0.6 ? 3 : 2; var h = floors * 3.5f + 0.5f;
            var wall = random.NextDouble() < 0.65 ? m.Brick : random.NextDouble() < 0.5 ? m.BrickDark : m.ClapCream;
            P(wall, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            P(m.Trim, at, yaw).Box(new Vector3(0, h + 0.25f, 0), new Vector3(w + 0.6f, 0.5f, d + 0.6f));
            P(m.Slate, at, yaw).Box(new Vector3(0, h + 0.55f, 0), new Vector3(w - 0.6f, 0.15f, d - 0.6f));
            P(m.Trim, at, yaw).Box(new Vector3(0, 3.55f, -d / 2 - 0.08f), new Vector3(w, 0.35f, 0.16f));
            P(m.Glass, at, yaw, false).Box(new Vector3(0, 1.6f, -d / 2 - 0.05f), new Vector3(w - 2.2f, 2.4f, 0.1f));
            P(Accent(), at, yaw).Box(new Vector3(0, 3.1f, -d / 2 - 1.0f), new Vector3(w - 1.2f, 0.18f, 2.0f));
            Windows(at, yaw, new Vector3(0, 0, -d / 2), Vector3.right, Vector3.back, w, floors - 1, 3.5f, 4.4f, new Vector2(1.1f, 1.8f), 2.7f, 6);
            Windows(at, yaw, new Vector3(0, 0, d / 2), Vector3.right, Vector3.forward, w, floors, 3.5f, 0.9f, new Vector2(1.1f, 1.8f), 2.9f, 7);
            Windows(at, yaw, new Vector3(-w / 2, 0, 0), Vector3.forward, Vector3.left, d, floors, 3.5f, 0.9f, new Vector2(1.1f, 1.8f), 3.4f, 8);
            Windows(at, yaw, new Vector3(w / 2, 0, 0), Vector3.forward, Vector3.right, d, floors, 3.5f, 0.9f, new Vector2(1.1f, 1.8f), 3.4f, 9);
        }

        void Inn(Vector3 at, float yaw, float w, float d)
        {
            var h = 3 * 3.1f + 0.6f;
            P(m.ClapWhite, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            P(m.RoofGreen, at, yaw).Gable(new Vector3(0, h, 0), w + 1, d + 1, 6.2f);
            for (var x = -w / 2 + 4; x <= w / 2 - 4; x += (w - 8) / 2)
            {
                P(m.ClapWhite, at, yaw).Box(new Vector3(x, h + 1.6f, -d / 2 + 2.6f), new Vector3(2.2f, 2.2f, 2.0f));
                P(m.RoofGreen, at, yaw).Gable(new Vector3(x, h + 2.7f, -d / 2 + 2.6f), 2.6f, 2.4f, 1.0f, 90);
                P(WindowFor(at + new Vector3(x, 0, 0), 21), at, yaw, false).Box(new Vector3(x, h + 1.5f, -d / 2 + 1.55f), new Vector3(1.0f, 1.2f, 0.1f));
            }
            foreach (var sx in new[] { -1, 1 }) P(m.Brick, at, yaw).Box(new Vector3(sx * (w / 2 - 1.2f), h + 4.5f, 0), new Vector3(1.4f, 4, 1.4f));
            // the veranda across the front
            P(m.Trim, at, yaw).Box(new Vector3(0, 0.3f, -d / 2 - 1.6f), new Vector3(w, 0.6f, 3.2f));
            P(m.RoofGreen, at, yaw).Box(new Vector3(0, 3.2f, -d / 2 - 1.6f), new Vector3(w + 0.4f, 0.22f, 3.5f));
            for (var x = -w / 2 + 0.4f; x <= w / 2; x += w / 6) P(m.Trim, at, yaw).Box(new Vector3(x, 1.7f, -d / 2 - 3.0f), new Vector3(0.25f, 2.9f, 0.25f));
            AllWindows(at, yaw, w, d, 3, 3.1f, 0.9f, new Vector2(1.0f, 1.5f), 2.6f);
            P(m.DoorDark, at, yaw, false).Box(new Vector3(0, 1.2f, -d / 2 - 0.05f), new Vector3(1.6f, 2.4f, 0.1f));
        }

        void Shed(Vector3 at, float yaw, float w, float d)
        {
            var wall = random.NextDouble() < 0.55 ? m.Shingle : m.BarnRed; var h = 5.2f;
            P(wall, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            P(random.NextDouble() < 0.5 ? m.Slate : m.RoofBrown, at, yaw).Gable(new Vector3(0, h, 0), w + 0.8f, d + 0.8f, 2.6f);
            P(m.DoorDark, at, yaw, false).Box(new Vector3(-w * 0.2f, 1.8f, -d / 2 - 0.05f), new Vector3(4.0f, 3.6f, 0.1f));
            P(m.Trim, at, yaw).Box(new Vector3(-w * 0.2f, 3.75f, -d / 2 - 0.08f), new Vector3(4.4f, 0.25f, 0.16f));
            Windows(at, yaw, new Vector3(w * 0.2f, 0, -d / 2), Vector3.right, Vector3.back, w * 0.5f, 1, 3, 1.4f, new Vector2(1.0f, 1.0f), 2.6f, 10);
            Windows(at, yaw, new Vector3(-w / 2, 0, 0), Vector3.forward, Vector3.left, d, 1, 3, 1.4f, new Vector2(1.0f, 1.0f), 3.2f, 11);
        }

        void Church(Vector3 at, float yaw)
        {
            const float w = 12, d = 24, h = 9;
            at.y = Ground(at, w + 6, d + 6, yaw);
            P(m.ClapWhite, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            P(m.Slate, at, yaw).Gable(new Vector3(0, h, 0), d + 0.8f, w + 0.8f, 5.5f, 90);
            // the tower stands at the front (local -z), then a belfry, then an octagonal spire
            P(m.ClapWhite, at, yaw).Box(new Vector3(0, 7.5f, -d / 2 - 2.0f), new Vector3(5, 18, 5));
            P(m.Trim, at, yaw).Box(new Vector3(0, 16.7f, -d / 2 - 2.0f), new Vector3(5.6f, 0.5f, 5.6f));
            P(m.ClapWhite, at, yaw).Box(new Vector3(0, 18.6f, -d / 2 - 2.0f), new Vector3(3.8f, 3.4f, 3.8f));
            P(m.DoorDark, at, yaw, false).Box(new Vector3(0, 18.6f, -d / 2 - 2.0f), new Vector3(3.9f, 1.8f, 1.4f)).Box(new Vector3(0, 18.6f, -d / 2 - 2.0f), new Vector3(1.4f, 1.8f, 3.9f));
            P(m.ClapWhite, at, yaw).Cylinder(new Vector3(0, 20.3f, -d / 2 - 2.0f), 2.1f, 13, 8, 0.05f);
            P(m.DoorDark, at, yaw, false).Box(new Vector3(0, 1.6f, -d / 2 - 4.55f), new Vector3(1.8f, 3.2f, 0.1f));
            foreach (var sx in new[] { -1, 1 })
                for (var z = -d / 2 + 4; z < d / 2 - 2; z += 4.5f)
                    P(WindowFor(at + new Vector3(sx, 0, z), 31), at, yaw, false).Box(new Vector3(sx * (w / 2 + 0.06f), 4.2f, z), new Vector3(0.12f, 3.6f, 1.3f));
        }

        // ---------------------------------------------------------------- waterfront and park

        void Waterfront()
        {
            // two timber piers on pilings, clear of every device: off the south end of East Quay and inside the Basin's southern dock
            Pier(new Vector3(140, 0, -70), new Vector3(-1, 0, 0), 46, 4.2f);
            Pier(new Vector3(-155, 0, -102), new Vector3(1, 0, 0), 42, 4.2f);
            // boats: alongside the piers, and a few on swing moorings clear of the channel
            Boat(new Vector2(112, -64.5f), 90, 13, m.HullWhite, true);
            Boat(new Vector2(104, -76), 90, 10, m.HullNavy, false);
            Boat(new Vector2(-128, -96.5f), 270, 11, m.HullRed, false);
            Boat(new Vector2(-136, -108), 270, 14, m.HullWhite, true);
            Boat(new Vector2(-104, -18), 200, 10, m.HullWhite, false);
            Boat(new Vector2(-92, 22), 160, 12, m.HullNavy, true);
            Boat(new Vector2(92, -12), 20, 11, m.HullWhite, false);
            Boat(new Vector2(108, 30), 340, 9, m.HullRed, false);
            // the working waterfront behind East Quay: sheds round a small boatyard, on the deck
            var yard = new Vector3(168, Deck, 100);
            P(m.Paving, yard, 0, false).Box(new Vector3(0, 0.03f, 0), new Vector3(110, 0.06f, 40));
            Shed(new Vector3(132, Deck, 106), 180, 20, 11); footprints.Add(new Vector3(132, 106, 11));
            Shed(new Vector3(162, Deck, 108), 180, 18, 10); footprints.Add(new Vector3(162, 108, 10));
            Shed(new Vector3(198, Deck, 102), 200, 22, 12); footprints.Add(new Vector3(198, 102, 12));
            // a hauled-out boat on blocks, and stacked pots and crates
            HauledBoat(new Vector3(150, Deck, 86), 70);
            for (var i = 0; i < 6; i++) P(i % 2 == 0 ? m.Timber : m.AwningBlue, yard, 0).Box(new Vector3(18 + (i % 3) * 1.4f, 0.55f + (i / 3) * 1.1f, -14), new Vector3(1.2f, 1.0f, 1.2f));
        }

        void Pier(Vector3 wall, Vector3 dir, float length, float width)
        {
            var yaw = Mathf.Atan2(dir.x, dir.z) * Mathf.Rad2Deg;
            var at = new Vector3(wall.x, 0, wall.z);
            P(m.Timber, at, yaw).Box(new Vector3(0, Deck - 0.35f, length / 2), new Vector3(width, 0.3f, length));
            for (var z = 3f; z <= length; z += 5)
                foreach (var s in new[] { -1, 1 })
                    P(m.Piling, at, yaw).Cylinder(new Vector3(s * (width / 2 - 0.1f), HarborWorld.SeabedY, z), 0.24f, Deck - HarborWorld.SeabedY + (z % 10 < 5 ? 0.6f : -0.5f), 7);
            for (var z = 6f; z < length; z += 10)
                foreach (var s in new[] { -1, 1 }) P(m.DarkSteel, at, yaw).Cylinder(new Vector3(s * (width / 2 - 0.35f), Deck - 0.2f, z), 0.18f, 0.5f, 7, 0.24f);
            // a floating landing at the end, a little lower
            P(m.Timber, at, yaw).Box(new Vector3(0, 0.5f, length + 2.5f), new Vector3(width + 4, 0.35f, 5));
        }

        void HauledBoat(Vector3 at, float yaw)
        {
            P(m.HullNavy, at, yaw).Hull(new Vector3(0, 1.4f, 0), 11, 3.6f, 1.6f, 0.9f);
            P(m.Trim, at, yaw).Box(new Vector3(0, 3.4f, -1.5f), new Vector3(2.4f, 1.6f, 3.4f));
            foreach (var z in new[] { -3f, 0f, 3f }) P(m.Timber, at, yaw).Box(new Vector3(0, 0.35f, z), new Vector3(2.6f, 0.7f, 0.6f));
        }

        void Boat(Vector2 rest, float yaw, float length, Material hull, bool sail)
        {
            // built round its own origin with the yaw baked in: the composition root bobs it on the water every frame
            var t = AssetRig.Node("Moored boat", root, new Vector3(rest.x, 0, rest.y));
            var beam = length * 0.32f;
            var kitHull = new MeshKit().Place(Vector3.zero, yaw).Hull(new Vector3(0, -0.5f, 0), length, beam, 1.7f, 0.8f);
            kitHull.Spawn("Hull", t, hull);
            var top = new MeshKit().Place(Vector3.zero, yaw)
                .Box(new Vector3(0, 1.25f, -length * 0.12f), new Vector3(beam * 0.62f, 1.3f, length * 0.32f))
                .Box(new Vector3(0, 1.95f, -length * 0.12f), new Vector3(beam * 0.7f, 0.12f, length * 0.36f));
            top.Spawn("Cabin", t, m.Trim);
            new MeshKit().Place(Vector3.zero, yaw).Box(new Vector3(0, 1.3f, -length * 0.12f - 0.02f), new Vector3(beam * 0.64f, 0.45f, length * 0.26f)).Spawn("CabinGlass", t, m.Glass, false);
            new MeshKit().Place(Vector3.zero, yaw).Slab(-beam * 0.42f, beam * 0.42f, -length * 0.45f, length * 0.3f, 0.6f, 0.68f).Spawn("Deck", t, m.Timber, false);
            if (sail) new MeshKit().Place(Vector3.zero, yaw).Cylinder(new Vector3(0, 0.6f, length * 0.12f), 0.09f, length * 0.55f, 6).Spawn("Mast", t, m.Trim);
            boats.Add(t); boatRest.Add(rest);
        }

        void Park()
        {
            // a lawn at the harbour's north-west corner, from the water's edge up to the town, with a gazebo by the water
            // the lawn is draped over the ground, which starts to rise at its northern end
            var lawn = new MeshKit().Slab(-152, -122, 75.5f, 92, Deck - 0.4f, Deck + 0.06f);
            for (var x = -238f; x < -122; x += 4)
                for (var z = 92f; z < 178; z += 4)
                {
                    float x1 = Mathf.Min(x + 4, -122), z1 = Mathf.Min(z + 4, 178);
                    lawn.Quad(new Vector3(x, Height(x, z) + 0.06f, z), new Vector3(x, Height(x, z1) + 0.06f, z1),
                        new Vector3(x1, Height(x1, z1) + 0.06f, z1), new Vector3(x1, Height(x1, z) + 0.06f, z));
                }
            lawn.Spawn("Park lawn", root, m.Lawn, false);
            var path = new MeshKit();
            Vector2 prev = new Vector2(-135, 80);
            foreach (var p in new[] { new Vector2(-150, 104), new Vector2(-172, 122), new Vector2(-198, 132), new Vector2(-222, 156), new Vector2(-236, 168) })
            {
                var dir = p - prev; var mid = (p + prev) / 2;
                path.Box(new Vector3(mid.x, Height(mid.x, mid.y) + 0.1f, mid.y), new Vector3(2.4f, 0.06f, dir.magnitude + 2.2f), Mathf.Atan2(dir.x, dir.y) * Mathf.Rad2Deg);
                prev = p;
            }
            path.Spawn("Park path", root, m.Paving, false);
            var g = new Vector3(-140, Deck + 0.05f, 86);
            var gazebo = AssetRig.Node("Gazebo", root, g);
            new MeshKit().Cylinder(Vector3.zero, 3.6f, 0.45f, 8).Spawn("Floor", gazebo, m.Trim);
            var posts = new MeshKit();
            for (var i = 0; i < 8; i++) { var a = i * Mathf.PI / 4; posts.Box(new Vector3(Mathf.Cos(a) * 3.2f, 1.85f, Mathf.Sin(a) * 3.2f), new Vector3(0.2f, 2.8f, 0.2f)); }
            posts.Cylinder(new Vector3(0, 0.45f, 0), 3.3f, 0.9f, 8, 3.3f);
            posts.Spawn("Posts", gazebo, m.ClapWhite);
            new MeshKit().Cylinder(new Vector3(0, 3.25f, 0), 4.1f, 2.6f, 8, 0.15f).Cylinder(new Vector3(0, 5.8f, 0), 0.2f, 0.9f, 6, 0.02f).Spawn("Roof", gazebo, m.RoofGreen);
            // benches along the path
            foreach (var b in new[] { new Vector3(-160, 0, 116), new Vector3(-208, 0, 140) })
                P(m.Timber, new Vector3(b.x, Height(b.x, b.z), b.z), 45).Box(new Vector3(0, 0.45f, 0), new Vector3(2.2f, 0.12f, 0.6f)).Box(new Vector3(0, 0.8f, 0.28f), new Vector3(2.2f, 0.6f, 0.08f));
            // a ring of trees round the lawn's edge, and a few on it
            for (var i = 0; i < 26; i++)
            {
                var a = i / 26f;
                Vector2 p = a < 0.5f ? new Vector2(-236 + a * 2 * 110, 176 - (float)random.NextDouble() * 4) : new Vector2(-236 + (float)random.NextDouble() * 4, 172 - (a - 0.5f) * 2 * 70);
                if (Clearance(p) < 3) continue;
                Tree(new Vector3(p.x, Deck, p.y), 9 + (float)random.NextDouble() * 5, true);
            }
            foreach (var p in new[] { new Vector2(-196, 110), new Vector2(-174, 150), new Vector2(-214, 118), new Vector2(-150, 140) })
                Tree(new Vector3(p.x, Deck, p.y), 11 + (float)random.NextDouble() * 4, true);
        }

        // ---------------------------------------------------------------- trees

        Material Foliage(float x, float z)
        {
            // autumn: mostly greens with patches of gold, orange and red; conifers darker
            var patch = Noise(x * 0.012f + 40, z * 0.012f - 13);
            var r = (float)random.NextDouble() * 0.6f + patch * 0.4f;
            if (r < 0.30f) return m.FoliageGreen;
            if (r < 0.46f) return m.FoliageOlive;
            if (r < 0.62f) return m.FoliageGold;
            if (r < 0.76f) return m.FoliageOrange;
            if (r < 0.84f) return m.FoliageRed;
            return m.FoliageGreen;
        }

        void Tree(Vector3 at, float height, bool town)
        {
            at.y = Height(at.x, at.z) - 0.3f;
            var conifer = random.NextDouble() < (town ? 0.18 : 0.3);
            P(m.Bark, at, 0).Cylinder(Vector3.zero, height * 0.035f + 0.12f, height * 0.45f, 5, height * 0.025f);
            if (conifer)
            {
                var r = height * 0.24f;
                P(m.FoliagePine, at, 0).Cylinder(new Vector3(0, height * 0.22f, 0), r, height * 0.5f, 7, 0).Cylinder(new Vector3(0, height * 0.48f, 0), r * 0.75f, height * 0.52f, 7, 0);
            }
            else
            {
                var r = height * 0.3f; var leaf = Foliage(at.x, at.z);
                var crown = P(leaf, at, (float)random.NextDouble() * 360).Ellipsoid(new Vector3(0, height * 0.62f, 0), new Vector3(r, r * 0.95f, r), 1);
                if (height > 11) crown.Ellipsoid(new Vector3(r * 0.45f, height * 0.5f, r * 0.2f), new Vector3(r * 0.7f, r * 0.65f, r * 0.7f), 1);
            }
        }

        void Trees()
        {
            // in town: yards and verges, kept off streets, buildings and the core; the side lands beside the Basin and East Quay get lighter woods
            for (var z = -120f; z < 520; z += 10)
                for (var x = -470f; x < 470; x += 10)
                {
                    var p = new Vector2(x + (float)random.NextDouble() * 8 - 4, z + (float)random.NextDouble() * 8 - 4);
                    var side = p.y < 146;
                    if (side && Mathf.Abs(p.x) < CoreHalfX + 4) continue;
                    var density = side ? 0.2f + 0.5f * Noise(p.x * 0.017f + 7, p.y * 0.017f) * Smooth(CoastZ, CoastZ + 60, p.y)
                        : 0.12f + 0.4f * Noise(p.x * 0.02f, p.y * 0.02f) + Smooth(300, 500, p.y) * 0.5f + Smooth(260, 460, Mathf.Abs(p.x)) * 0.3f;
                    if (random.NextDouble() > density) continue;
                    if (Clearance(p) < 3.5f || !Free(p, 2)) continue;
                    Tree(new Vector3(p.x, 0, p.y), 8 + (float)random.NextDouble() * 7, true);
                }
            // the hills and the far side lands: forest in big low-poly crowns, sparser and larger with distance; far crowns are coarser and cast no shadow
            const float Base = 10;
            for (var z = CoastZ + 20; z < 3000; z += Base)
                for (var x = -3000f; x < 3000; x += Base)
                {
                    var d = CoreDistance(x, z);
                    if (Mathf.Abs(x) < 470 && z < 520) continue; // the town and the near side lands have their own trees
                    var step = Base + Mathf.Max(0, d - 470) * 0.045f;
                    if (random.NextDouble() > (Base / step) * (Base / step)) continue;
                    var p = new Vector2(x + ((float)random.NextDouble() - 0.5f) * step, z + ((float)random.NextDouble() - 0.5f) * step);
                    if (Noise(p.x * 0.006f + 2, p.y * 0.006f + 9) < 0.2f) continue; // clearings
                    var crown = step * (0.55f + (float)random.NextDouble() * 0.25f);
                    var at = new Vector3(p.x, Height(p.x, p.y) - crown * 0.3f, p.y);
                    var near = d < 620;
                    var mat = random.NextDouble() < 0.28 ? m.FoliagePine : Foliage(p.x, p.y);
                    Kit(mat, at, near, true).Place(at, (float)random.NextDouble() * 360)
                        .Ellipsoid(new Vector3(0, crown * 0.9f, 0), new Vector3(crown, crown * 1.15f, crown), near ? 1 : 0);
                }
        }
    }
}
