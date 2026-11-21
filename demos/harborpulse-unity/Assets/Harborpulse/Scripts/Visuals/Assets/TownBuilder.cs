// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

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

        /// <summary>
        /// The trees' layer (code only, like the water's 30): the planar reflection leaves it out, so the water reflects the town and the
        /// hills' ground but not the hundreds of thousands of crown vertices. Trees cast no shadow either; under the high cloud their
        /// shadows would be faint, and they would cost a pass over every crown.
        /// </summary>
        public const int FoliageLayer = 29;
        const float Tile = 300;
        static float Deck => (float)SitePlan.DeckHeightM;

        readonly WorldPropPlan plan;
        readonly HarborMaterials m;
        readonly Transform root;
        readonly System.Random random = new System.Random(1789);
        readonly Dictionary<(Material, int, int, bool, bool), MeshKit> kits = new Dictionary<(Material, int, int, bool, bool), MeshKit>();
        readonly List<Street> streets = new List<Street>();
        readonly List<Transform> boats = new List<Transform>();
        readonly List<Vector2> boatRest = new List<Vector2>();
        readonly TreeBuilder treeBuilder = new TreeBuilder();
        readonly Dictionary<(int, int), SceneryMesh> details = new Dictionary<(int, int), SceneryMesh>();
        readonly Material scenery;
        DetailManager detail;

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

        TownBuilder(Transform parent, HarborMaterials mats, WorldPropPlan plan)
        {
            this.plan = plan; m = mats;
            scenery = SceneryMesh.Material();
            root = AssetRig.Node("Town and hills", parent, Vector3.zero);
        }

        /// <param name="plan">Shared pure-model placements, with fixed devices and every accepted prop reserved first.</param>
        /// <param name="detail">Where the buildings' near-detail sets register (see <see cref="DetailManager"/>); null draws the far level only.</param>
        public static TownBuilder Build(Transform parent, HarborMaterials mats, WorldPropPlan plan, DetailManager detail = null)
        {
            var b = new TownBuilder(parent, mats, plan);
            b.detail = detail;
            b.Streets();
            b.Terrain();
            b.Landmarks();
            b.Lots();
            b.Waterfront();
            b.Park();
            b.Trees();
            b.StreetLamps();
            b.Flush();
            return b;
        }

        // ---------------------------------------------------------------- ground

        static float Hash(int x, int z)
        {
            unchecked
            {
                var h = (uint)(x * 374761393 + z * 668265263);
                h = (h ^ (h >> 13)) * 1274126177u;
                return ((h ^ (h >> 16)) & 0xffffff) / 16777215f;
            }
        }

        /// <summary>The mapped deck, a low apron under the town, then gently rolling ground; the skyline belongs to the distant ridge mesh.</summary>
        public static float Height(float x, float z)
        {
            return (float)CoastalTerrain.Height(x, z);
        }

        void Terrain()
        {
            Backdrop = AssetRig.Node("Hills and forest", root, Vector3.zero);
            // the plan's own grid (10 m cells round the town, 20 m across the rolling ground, coarser out to the ridge's foot, flat far
            // flanks past the far clip), so every rock and tree stands on exactly the surface drawn here; quads inside the core are left out
            TerrainBuilder.Build(Backdrop, scenery, plan, Height);
            // the one distant hazy ridge that carries the skyline
            RidgeBuilder.Build(Backdrop, scenery);
            // the coast's face where the side lands meet the sea, and the strips that join the terrain to the quays' deck
            var reach = (float)CoastalTerrain.TerrainHalfWidthM;
            new MeshKit().Slab(-reach, -CoreHalfX, CoastZ - 1, CoastZ, HarborWorld.SeabedY, Deck)
                .Slab(CoreHalfX, reach, CoastZ - 1, CoastZ, HarborWorld.SeabedY, Deck).Spawn("Coast", Backdrop, m.Stone);
            // the streets, draped over the ground
            var road = new MeshKit();
            foreach (var s in streets) Ribbon(road, s);
            road.Spawn("Streets", root, m.Lane, false);
        }

        static void Ribbon(MeshKit kit, Street s)
        {
            var length = s.Length; Vector3 pl = default, pr = default;
            var steps = Mathf.Max(1, Mathf.CeilToInt(length / 4));
            for (var i = 0; i <= steps; i++)
            {
                var d = length * i / steps;
                s.At(d, out var p, out var t);
                var n = new Vector2(t.y, -t.x) * s.HalfWidth;
                var l = new Vector3(p.x - n.x, 0, p.y - n.y); l.y = Height(l.x, l.z) + 0.14f;
                var r = new Vector3(p.x + n.x, 0, p.y + n.y); r.y = Height(r.x, r.z) + 0.14f;
                if (i > 0) kit.Quad(pl, l, r, pr);
                pl = l; pr = r;
            }
        }

        // ---------------------------------------------------------------- batching

        MeshKit Kit(Material mat, Vector3 at, bool shadows = true, bool forest = false)
        {
            // the hills' forest is batched in tiles three times larger: it is many small crowns seen from far off
            var tile = forest ? Tile * 3 : Tile;
            var key = (mat, Mathf.FloorToInt(at.x / tile), Mathf.FloorToInt(at.z / tile), shadows, forest);
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
                var go = pair.Value.Spawn(pair.Key.Item1.name.Replace("Harbor/", "") + " " + pair.Key.Item2 + "," + pair.Key.Item3, pair.Key.Item5 ? forest : Town, pair.Key.Item1, pair.Key.Item4);
                if (IsFoliage(pair.Key.Item1)) go.layer = FoliageLayer;
            }
            kits.Clear();
            foreach (var tile in details) if (tile.Value.VertexCount > 0) tile.Value.Spawn("Town architectural detail " + tile.Key, Town, scenery);
            details.Clear();
            treeBuilder.Flush(Town, forest, scenery);
        }

        // ---------------------------------------------------------------- the plan

        void Streets()
        {
            foreach(var s in plan.Streets)
                streets.Add(new Street(s.Name,(float)s.Width,Array.ConvertAll(s.Points,p=>new Vector2((float)p.X,(float)p.Z))));
        }

        enum Kind { House, Shopfront, Commercial, Shed, Inn }

        void Lots()
        {
            foreach(var p in plan.Props)
            {
                if(p.Area != "town" || !Enum.TryParse<Kind>(p.Kind,out var kind))continue;
                Building(kind,new Vector3((float)p.At.X,0,(float)p.At.Z),(float)p.Yaw,(float)p.Width,(float)p.Depth);

            }
        }

        void Landmarks()
        {
            var church=plan.Find("church");
            if(church!=null)Church(new Vector3((float)church.At.X,0,(float)church.At.Z),(float)church.Yaw);
        }

        // ---------------------------------------------------------------- buildings

        bool IsFoliage(Material mat) => mat == m.Bark || mat == m.FoliagePine || mat == m.FoliageGreen || mat == m.FoliageOlive
            || mat == m.FoliageGold || mat == m.FoliageOrange || mat == m.FoliageRed;

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

        public void Dispose() => MeshKit.Release(scenery);

        SceneryMesh Detail(Vector3 at)
        {
            var key = (Mathf.FloorToInt(at.x / Tile), Mathf.FloorToInt(at.z / Tile));
            if (!details.TryGetValue(key, out var mesh)) details[key] = mesh = new SceneryMesh();
            return mesh;
        }

        static bool Rich(Vector3 at) => at.z < 290 && Mathf.Abs(at.x) < 360;

        /// <summary>Registers a building's near-detail set (built the first time a camera comes within range, see <see cref="DetailManager"/>).</summary>
        void NearSet(NearPlan plan, Vector3 at, float yaw, NearPalette palette)
        {
            if (detail == null) return;
            detail.Register(plan, root, at, Quaternion.Euler(0, yaw, 0), palette, scenery, WindowFor);
        }

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
            var count = WindowRow.Count(span, spacing);
            var start = WindowRow.Start(count, spacing);
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
            var style = Mathf.Min(5, (int)(Hash(Mathf.RoundToInt(at.x), Mathf.RoundToInt(at.z)) * 6));
            BuildingDetail.Roof(P(roof, at, yaw + (gableFront ? 90 : 0)), gableFront ? d + .8f : w + .8f,
                gableFront ? w + .7f : d + .7f, h, rise, style);
            BuildingDetail.Envelope(Detail(at), at, yaw, w, d, h, paint.GetColor("_BaseColor"), true, Rich(at), w*.18f);
            var farDormer = Rich(at) && style == 0 && !gableFront;
            if (farDormer)
            {
                var dormer = new Vector3(-w*.22f, h+rise*.48f, -d*.28f);
                P(paint, at, yaw).Box(dormer, new Vector3(2.0f,1.65f,1.8f));
                P(roof, at, yaw).Gable(dormer+Vector3.up*.83f,2.2f,2,1,90);
                P(WindowFor(at,42),at,yaw,false).Box(dormer+new Vector3(0,0,-.92f),new Vector3(.9f,1.05f,.1f));
            }
            // white corner boards and a chimney give the clapboard its read at a distance
            if (paint != m.ClapWhite && !Rich(at))
                foreach (var sx in new[] { -1, 1 }) foreach (var sz in new[] { -1, 1 })
                    P(m.Trim, at, yaw).Box(new Vector3(sx * (w / 2 - 0.1f), h / 2 - 0.2f, sz * (d / 2 - 0.1f)), new Vector3(0.35f, h + 0.4f, 0.35f));
            float chimneyX = 0, chimneyY = 0, chimneyZ = 0, chimneyTall = 0;
            if (random.NextDouble() < 0.7)
            {
                var chimney = new Vector3(w*.3f*(random.Next(2)*2-1), h+rise*.6f, d*.15f);
                var tall = 2.4f + (style % 3)*.4f;
                P(style%2 == 0 ? m.Brick : m.Stone,at,yaw).Box(chimney,new Vector3(.85f,tall,.85f));
                P(m.BrickDark,at,yaw).Box(chimney+Vector3.up*(tall*.5f),new Vector3(1.08f,.22f,1.08f));
                chimneyX = chimney.x; chimneyY = chimney.y; chimneyZ = chimney.z; chimneyTall = tall;
            }
            Material awning = null;
            if (shop)
            {
                // a shop window band and an awning in an accent colour
                awning = Accent();
                P(m.Glass, at, yaw, false).Box(new Vector3(0, 1.5f, -d / 2 - 0.05f), new Vector3(w - 2.5f, 2.0f, 0.1f));
                P(awning, at, yaw).Box(new Vector3(0, 3.0f, -d / 2 - 0.9f), new Vector3(w - 1.5f, 0.18f, 1.8f));
                Windows(at, yaw, new Vector3(0, 0, -d / 2), Vector3.right, Vector3.back, w, floors - 1, 3.1f, 4.0f, new Vector2(1.0f, 1.5f), 2.6f, 5);
                AllWindows(at, yaw, w, d, floors, 3.1f, 0.9f, new Vector2(1.0f, 1.4f), 2.8f, false);
            }
            else AllWindows(at, yaw, w, d, floors, 3.1f, 0.9f, new Vector2(1.0f, 1.4f), 2.8f);
            if (porch)
            {
                var pw = Mathf.Min(w - 1, 7);
                P(m.Trim, at, yaw).Box(new Vector3(0, 0.25f, -d / 2 - 1.3f), new Vector3(pw, 0.5f, 2.6f));
                P(roof, at, yaw).Box(new Vector3(0, 3.0f, -d / 2 - 1.3f), new Vector3(pw + 0.4f, 0.2f, 2.9f));
                // two end posts: the middle of the front is the stair, which the near level builds (steps, newels and rails)
                for (var x = -pw / 2 + 0.3f; x <= pw / 2 - 0.2f; x += (pw - 0.6f) / 2)
                    if (Mathf.Abs(x) > .05f) P(m.Trim, at, yaw).Box(new Vector3(x, 1.6f, -d / 2 - 2.4f), new Vector3(0.22f, 2.8f, 0.22f));
            }
            P(m.DoorDark, at, yaw, false).Box(new Vector3(w * 0.18f, 1.1f, -d / 2 - 0.05f), new Vector3(1.1f, 2.2f, 0.1f));
            // Small rear wash-house/wood shed, wholly inside the lot's already reserved extension.
            if (Rich(at) && style == 4)
            {
                var rear = at + Quaternion.Euler(0,yaw,0)*new Vector3(0,0,d/2+1.25f);
                {
                    P(m.Shingle,rear,yaw).Box(new Vector3(0,1.15f,0),new Vector3(3,2.3f,2.4f));
                    P(m.RoofBrown,rear,yaw).Gable(new Vector3(0,2.3f,0),3.3f,2.7f,.8f);
                    P(m.DoorDark,rear,yaw,false).Box(new Vector3(1.52f,1,0),new Vector3(.1f,2,1));
                }
            }
            if (detail != null)
            {
                var spec = new HouseSpec(at.x, at.z, w, d, floors, porch, shop, gableFront, style, rise, Rich(at), farDormer, chimneyX, chimneyY, chimneyZ, chimneyTall);
                var accent = shop ? awning.GetColor("_BaseColor") : NearPalette.DoorAccent(at);
                NearSet(NearBuildings.House(spec), at, yaw, NearPalette.Building(paint.GetColor("_BaseColor"), roof.GetColor("_BaseColor"), accent));
            }
        }

        Material Accent()
        {
            var r = random.Next(4);
            return r == 0 ? m.AwningGreen : r == 1 ? m.AwningBlue : r == 2 ? m.BarnRed : m.ClapYellow;
        }

        void Commercial(Vector3 at, float yaw, float w, float d)
        {
            var floors = random.NextDouble() < 0.6 ? 3 : 2; var h = floors * 3.5f + 0.5f;
            BuildingDetail.Envelope(Detail(at),at,yaw,w,d,h,Color.white,false,Rich(at),0);
            var wall = random.NextDouble() < 0.65 ? m.Brick : random.NextDouble() < 0.5 ? m.BrickDark : m.ClapCream;
            P(wall, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            var awning = Accent();
            P(m.Trim, at, yaw).Box(new Vector3(0, h + 0.25f, 0), new Vector3(w + 0.6f, 0.5f, d + 0.6f));
            P(m.Slate, at, yaw).Box(new Vector3(0, h + 0.55f, 0), new Vector3(w - 0.6f, 0.15f, d - 0.6f));
            P(m.Trim, at, yaw).Box(new Vector3(0, 3.55f, -d / 2 - 0.08f), new Vector3(w, 0.35f, 0.16f));
            P(m.Glass, at, yaw, false).Box(new Vector3(0, 1.6f, -d / 2 - 0.05f), new Vector3(w - 2.2f, 2.4f, 0.1f));
            P(awning, at, yaw).Box(new Vector3(0, 3.1f, -d / 2 - 1.0f), new Vector3(w - 1.2f, 0.18f, 2.0f));
            Windows(at, yaw, new Vector3(0, 0, -d / 2), Vector3.right, Vector3.back, w, floors - 1, 3.5f, 4.4f, new Vector2(1.1f, 1.8f), 2.7f, 6);
            Windows(at, yaw, new Vector3(0, 0, d / 2), Vector3.right, Vector3.forward, w, floors, 3.5f, 0.9f, new Vector2(1.1f, 1.8f), 2.9f, 7);
            Windows(at, yaw, new Vector3(-w / 2, 0, 0), Vector3.forward, Vector3.left, d, floors, 3.5f, 0.9f, new Vector2(1.1f, 1.8f), 3.4f, 8);
            Windows(at, yaw, new Vector3(w / 2, 0, 0), Vector3.forward, Vector3.right, d, floors, 3.5f, 0.9f, new Vector2(1.1f, 1.8f), 3.4f, 9);
            if (detail != null)
                NearSet(NearBuildings.Commercial(at.x, at.z, w, d, floors, wall != m.ClapCream, Rich(at)), at, yaw,
                    NearPalette.Building(wall.GetColor("_BaseColor"), m.Slate.GetColor("_BaseColor"), awning.GetColor("_BaseColor")));
        }

        void Inn(Vector3 at, float yaw, float w, float d)
        {
            var h = 3 * 3.1f + 0.6f;
            BuildingDetail.Envelope(Detail(at),at,yaw,w,d,h,m.ClapWhite.GetColor("_BaseColor"),true,Rich(at),0);
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
            if (detail != null)
                NearSet(NearBuildings.Inn(at.x, at.z, w, d, Rich(at)), at, yaw,
                    NearPalette.Building(m.ClapWhite.GetColor("_BaseColor"), m.RoofGreen.GetColor("_BaseColor"), NearPalette.DoorAccent(at)));
        }

        void Shed(Vector3 at, float yaw, float w, float d, bool yard = false)
        {
            var wall = random.NextDouble() < 0.55 ? m.Shingle : m.BarnRed; var h = 5.2f;
            BuildingDetail.Envelope(Detail(at),at,yaw,w,d,h,wall.GetColor("_BaseColor"),true,Rich(at),-w*.2f);
            P(wall, at, yaw).Box(new Vector3(0, (h - 3) / 2, 0), new Vector3(w, h + 3, d));
            var shedRoof = random.NextDouble() < 0.5 ? m.Slate : m.RoofBrown;
            P(shedRoof, at, yaw).Gable(new Vector3(0, h, 0), w + 0.8f, d + 0.8f, 2.6f);
            P(m.DoorDark, at, yaw, false).Box(new Vector3(-w * 0.2f, 1.8f, -d / 2 - 0.05f), new Vector3(4.0f, 3.6f, 0.1f));
            P(m.Trim, at, yaw).Box(new Vector3(-w * 0.2f, 3.75f, -d / 2 - 0.08f), new Vector3(4.4f, 0.25f, 0.16f));
            Windows(at, yaw, new Vector3(w * 0.2f, 0, -d / 2), Vector3.right, Vector3.back, w * 0.5f, 1, 3, 1.4f, new Vector2(1.0f, 1.0f), 2.6f, 10);
            Windows(at, yaw, new Vector3(-w / 2, 0, 0), Vector3.forward, Vector3.left, d, 1, 3, 1.4f, new Vector2(1.0f, 1.0f), 3.2f, 11);
            // the town's lots reserve 3.5 m front and back; a boatyard shed's apron is one metre all round
            if (detail != null)
                NearSet(NearBuildings.Shed(at.x, at.z, w, d, Rich(at), yard ? new NearReach(1f, 1f, 1f) : NearReach.Lot), at, yaw,
                    NearPalette.Building(wall.GetColor("_BaseColor"), shedRoof.GetColor("_BaseColor"), NearPalette.DoorAccent(at)));
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
            if (detail != null)
                NearSet(NearBuildings.Church(), at, yaw, NearPalette.Building(m.ClapWhite.GetColor("_BaseColor"), m.Slate.GetColor("_BaseColor"), NearPalette.DoorAccent(at)));
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
            foreach(var shed in plan.Props)
            {
                if(shed.Kind!="Shed" || shed.Area=="town")continue;
                Shed(new Vector3((float)shed.At.X,Deck,(float)shed.At.Z),(float)shed.Yaw,(float)shed.Width,(float)shed.Depth,true);
            }
            // a hauled-out boat on blocks, and stacked pots and crates
            var hauled=plan.Find("hauled");
            if(hauled!=null)HauledBoat(new Vector3((float)hauled.At.X,Deck,(float)hauled.At.Z),(float)hauled.Yaw);
            if(plan.Find("crates")!=null)for (var i = 0; i < 6; i++) P(i % 2 == 0 ? m.Timber : m.AwningBlue, yard, 0).Box(new Vector3(18 + (i % 3) * 1.4f, 0.55f + (i / 3) * 1.1f, -14), new Vector3(1.2f, 1.0f, 1.2f));
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
                foreach (var s in new[] { -1, 1 })
                {
                    var pos=at+Quaternion.Euler(0,yaw,0)*new Vector3(s*(width/2-.35f),0,z);
                    foreach(var p in plan.Props)if(p.Kind=="PierBollard" && Math.Abs(p.At.X-pos.x)<.01 && Math.Abs(p.At.Z-pos.z)<.01)
                        P(m.DarkSteel,at,yaw).Cylinder(new Vector3(s*(width/2-.35f),Deck-.2f,z),.18f,.5f,7,.24f);
                }
            // a floating landing at the end, a little lower
            P(m.Timber, at, yaw).Box(new Vector3(0, 0.5f, length + 2.5f), new Vector3(width + 4, 0.35f, 5));
        }

        void HauledBoat(Vector3 at, float yaw)
        {
            P(m.HullNavy, at, yaw).Hull(new Vector3(0, 1.4f, 0), 11, 3.6f, 1.6f, 0.9f);
            P(m.Trim, at, yaw).Box(new Vector3(0, 3.4f, -1.5f), new Vector3(2.4f, 1.6f, 3.4f));
            foreach (var z in new[] { -3f, 0f, 3f }) P(m.Timber, at, yaw).Box(new Vector3(0, 0.35f, z), new Vector3(2.6f, 0.7f, 0.6f));
            if (detail != null) detail.Register(NearVehicles.HauledBoat(), root, at, Quaternion.Euler(0, yaw, 0), NearPalette.Vehicle(m.HullNavy.GetColor("_BaseColor")), scenery);
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
            var pavilion=plan.Find("gazebo");
            if(pavilion!=null)
            {
            var g = new Vector3((float)pavilion.At.X, Deck + 0.05f, (float)pavilion.At.Z);
            var gazebo = AssetRig.Node("Gazebo", root, g);
            new MeshKit().Cylinder(Vector3.zero, 3.6f, 0.45f, 8).Spawn("Floor", gazebo, m.Trim);
            var posts = new MeshKit();
            for (var i = 0; i < 8; i++) { var a = i * Mathf.PI / 4; posts.Box(new Vector3(Mathf.Cos(a) * 3.2f, 1.85f, Mathf.Sin(a) * 3.2f), new Vector3(0.2f, 2.8f, 0.2f)); }
            posts.Cylinder(new Vector3(0, 0.45f, 0), 3.3f, 0.9f, 8, 3.3f);
            posts.Spawn("Posts", gazebo, m.ClapWhite);
            new MeshKit().Cylinder(new Vector3(0, 3.25f, 0), 4.1f, 2.6f, 8, 0.15f).Cylinder(new Vector3(0, 5.8f, 0), 0.2f, 0.9f, 6, 0.02f).Spawn("Roof", gazebo, m.RoofGreen);
            }
            foreach(var b in plan.Props)if(b.Kind=="Bench")
                P(m.Timber,new Vector3((float)b.At.X,Height((float)b.At.X,(float)b.At.Z),(float)b.At.Z),(float)b.Yaw)
                    .Box(new Vector3(0,.45f,0),new Vector3(2.2f,.12f,.6f)).Box(new Vector3(0,.8f,.28f),new Vector3(2.2f,.6f,.08f));
        }

        // ---------------------------------------------------------------- street lamps

        void StreetLamps()
        {
            foreach(var p in plan.Props)
            {
                if(p.Kind!="StreetLamp")continue;
                var at3=new Vector3((float)p.At.X,Height((float)p.At.X,(float)p.At.Z),(float)p.At.Z);var yaw=(float)p.Yaw;
                P(m.DarkSteel,at3,yaw).Cylinder(Vector3.zero,.09f,4.6f,6).Box(new Vector3(0,4.6f,-.35f),new Vector3(.12f,.12f,.9f));
                P(p.At.X>140?m.EastTownLamp:m.TownLamp,at3,yaw,false).Box(new Vector3(0,4.38f,-.75f),new Vector3(.7f,.36f,.7f));
            }
        }

        // ---------------------------------------------------------------- trees

        void Tree(Vector3 at, float height, TreeKind kind, bool town)
        {
            // on the drawn surface (the two-triangle mesh), not the smooth field it was sampled from
            at.y = (float)plan.Terrain.MeshHeight(at.x,at.z)-.15f;
            treeBuilder.Add(at,height,kind,town);
        }

        void Trees()
        {
            foreach(var p in plan.Props)if(p.Kind=="Tree")Tree(new Vector3((float)p.At.X,0,(float)p.At.Z),(float)p.Height,(TreeKind)p.Variant,p.Area=="town");
        }
    }
}
