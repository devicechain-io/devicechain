// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// Builds the waterfront from the <see cref="SitePlan"/> at start-up, entirely in code: land, quay walls and copings, the promenade, jetties,
    /// the seabed, every device's model, the two metered buildings, the depot and a deterministic town behind the promenade. Quay courses and town windows are
    /// batched by material; each device keeps its own named rig so snapshot views can apply platform evidence.
    /// </summary>
    public sealed class HarborWorld
    {
        public const float SeabedY = -6.5f;

        readonly SitePlan plan;
        readonly HarborLayout layout;
        readonly HarborMaterials mats;
        readonly Dictionary<Material, MeshKit> statics = new Dictionary<Material, MeshKit>();
        readonly List<AssetRig> rigs = new List<AssetRig>();
        readonly List<Transform> buoys = new List<Transform>();
        readonly List<Vector2> buoyRest = new List<Vector2>();

        public Transform Root { get; private set; }
        public IReadOnlyList<AssetRig> Rigs => rigs;

        /// <summary>The objects that float: each frame they are put on the water's surface.</summary>
        public IReadOnlyList<Transform> Buoys => buoys;

        /// <summary>Where each buoy is moored (x, z): the rest position its waves are computed at.</summary>
        public IReadOnlyList<Vector2> BuoyRest => buoyRest;

        float Deck => (float)SitePlan.DeckHeightM;
        float WallZ => (float)plan.WallZ;

        HarborWorld(SitePlan plan, HarborLayout layout, HarborMaterials mats)
        {
            this.plan = plan;
            this.layout = layout;
            this.mats = mats;
        }

        MeshKit Kit(Material m)
        {
            if (!statics.TryGetValue(m, out var k)) statics[m] = k = new MeshKit();
            return k;
        }

        static Vector3 V(Ground g, float y = 0) => new Vector3((float)g.X, y, (float)g.Z);

        public static HarborWorld Build(SitePlan plan, HarborLayout layout, HarborMaterials mats, Transform parent)
        {
            var w = new HarborWorld(plan, layout, mats);
            var root = new GameObject("Waterfront");
            root.transform.SetParent(parent, false);
            w.Root = root.transform;
            w.BuildGround();
            QuayBuilder.Build(plan, w.Root, mats);
            w.BuildTown();
            foreach (var p in plan.Placements) w.BuildDevice(p);
            BuildingBuilder.Depot(w.Root, V(plan.Depot, w.Deck), mats);
            w.BuildBuoys();
            foreach (var pair in w.statics)
            {
                var castShadows = pair.Key != mats.Seabed && pair.Key != mats.Ground;
                pair.Value.Spawn("static " + pair.Key.name.Replace("Harbor/", ""), w.Root, pair.Key, castShadows);
            }

            return w;
        }

        /// <summary>Destroys the waterfront: every mesh it built and every object under it.</summary>
        public void Dispose()
        {
            if (Root == null) return;
            foreach (var f in Root.GetComponentsInChildren<MeshFilter>(true)) MeshKit.Release(f.sharedMesh);
            MeshKit.Release(Root.gameObject);
            Root = null;
        }

        void GroundWithWells(Material material, float x0, float x1, float z0, float z1, float top)
        {
            var openings = new List<Placement>();
            foreach (var p in plan.Placements) if (p.Device.Kind == DeviceKind.Pump) openings.Add(p);
            openings.Sort((a, b) => a.At.X.CompareTo(b.At.X));
            var cursor = x0;
            foreach (var p in openings)
            {
                var left = (float)p.At.X + 6.2f; var right = left + 5.6f;
                Kit(material).Slab(cursor, left, z0, z1, SeabedY, top);
                var back = Mathf.Clamp((float)p.At.Z + 3.1f, z0, z1);
                var front = Mathf.Clamp((float)p.At.Z + 14, z0, z1);
                if (back > z0) Kit(material).Slab(left, right, z0, back, SeabedY, top);
                if (front < z1) Kit(material).Slab(left, right, front, z1, SeabedY, top);
                cursor = right;
            }
            Kit(material).Slab(cursor, x1, z0, z1, SeabedY, top);
        }

        void BuildGround()
        {
            float x0 = (float)plan.MinX - 12000, x1 = (float)plan.MaxX + 12000;
            // the town's land, its north face the sea wall
            GroundWithWells(mats.Ground, x0, x1, -16000, WallZ - 26, Deck);
            Kit(mats.Stone).Slab(x0, x1, WallZ - 1.6f, WallZ, SeabedY, Deck + 0.35f);
            Kit(mats.Coping).Slab(x0, x1, WallZ - 1.8f, WallZ + 0.15f, Deck + 0.35f, Deck + 0.55f);
            // the promenade the lights stand on, and the road behind it
            GroundWithWells(mats.Paving, x0, x1, WallZ - 26, WallZ - 1.6f, Deck + 0.02f);
            Kit(mats.Asphalt).Slab(x0, x1, -56, -46, Deck + 0.02f, Deck + 0.05f);
            // the harbour floor
            Kit(mats.Seabed).Slab(x0, x1, WallZ, WallZ + 4000, SeabedY - 1, SeabedY);
        }

        // A deterministic town behind the promenade: rows of blocks of varied height and width, windows lit at night, kept clear of the
        // devices' own buildings.
        void BuildTown()
        {
            var rng = new System.Random(4242);
            var keepClear = new List<Vector3>();
            foreach (var p in plan.Placements)
                if (p.Device.Kind == DeviceKind.Pump || p.Device.Kind == DeviceKind.Meter || p.Device.Kind == DeviceKind.Charger)
                    keepClear.Add(V(p.At));
            keepClear.Add(V(plan.Depot));
            for (var row = 0; row < 3; row++)
            {
                var z = -60f - row * 70f;
                for (var x = (float)plan.MinX - 600; x < plan.MaxX + 600;)
                {
                    var width = 22 + (float)rng.NextDouble() * 30;
                    var depth = 18 + (float)rng.NextDouble() * 20;
                    var floors = 2 + rng.Next(row == 0 ? 4 : 3);
                    var height = floors * 3.2f;
                    var c = new Vector3(x + width / 2, Deck, z - depth / 2);
                    var clear = true;
                    foreach (var k in keepClear)
                        if (Mathf.Abs(k.x - c.x) < width / 2 + 45 && Mathf.Abs(k.z - c.z) < depth / 2 + 45) clear = false;
                    if (clear && rng.NextDouble() > 0.12)
                    {
                        var wall = rng.NextDouble() < 0.5 ? mats.Brick : mats.Render;
                        House(c, width, depth, height, floors, wall, rng.NextDouble() < 0.6);
                    }

                    x += width + 4 + (float)rng.NextDouble() * 10;
                }
            }
        }

        void House(Vector3 at, float width, float depth, float height, int floors, Material wall, bool pitched)
        {
            var east = plan.Quay("hp-east-quay");
            var windows = at.x >= east.XMin && at.x <= east.XMax ? mats.EastWindows : mats.Windows;
            BuildingBuilder.House(Root, at, width, depth, height, floors, mats, wall, windows, pitched);
        }

        void BuildDevice(Placement p)
        {
            AssetRig rig = null;
            switch (p.Device.Kind)
            {
                case DeviceKind.Pump: rig = PumpStationBuilder.Build(p, Root, mats, WallZ); break;
                case DeviceKind.Gauge: case DeviceKind.LoraGauge: case DeviceKind.LegacyGauge: rig = GaugeBuilder.Build(p, Root, mats, layout); break;
                case DeviceKind.Light: rig = LampBuilder.Build(p, Root, mats); break;
                case DeviceKind.Charger: rig = ChargerBuilder.Build(p, Root, mats); break;
                case DeviceKind.Truck: rig = TruckBuilder.Build(p, Root, mats); break;
                case DeviceKind.Meter: rig = new AssetRig(p, Root); rig["Beacon"].localPosition = Vector3.up * 20; rig["CardAnchor"].localPosition = Vector3.up * 18; BuildingBuilder.Build(Root, V(p.At, Deck), p.Device.Index == 1, mats, WallZ); break;
                default: throw new System.InvalidOperationException("no asset builder for " + p.Device.Kind);
            }
            if (rig != null) rigs.Add(rig);
        }

        void BuildBuoys()
        {
            // the dredged channel's port and starboard marks, off the Basin; they ride the water, so each is its own object
            var red = new MeshKit().Cylinder(new Vector3(0, -0.9f, 0), 0.7f, 2.6f, 10).Build("buoy can");
            var green = new MeshKit().Cylinder(new Vector3(0, -0.9f, 0), 0.8f, 2.8f, 10, 0.05f).Build("buoy cone");
            var basin = plan.Quay(layout.Quays[layout.Quays.Count - 1].Token);
            for (var i = 0; i < 6; i++)
            {
                var z = WallZ + 260 + i * 160;
                var x = (float)basin.CenterX + 200 - i * 40;
                buoys.Add(Buoy("buoy port " + i, red, mats.BuoyRed, new Vector3(x - 70, 0, z)));
                buoys.Add(Buoy("buoy starboard " + i, green, mats.BuoyGreen, new Vector3(x + 70, 0, z)));
            }
        }

        Transform Buoy(string name, Mesh mesh, Material m, Vector3 at)
        {
            var go = new GameObject(name);
            go.transform.SetParent(Root, false);
            go.transform.position = at;
            go.AddComponent<MeshFilter>().sharedMesh = mesh;
            go.AddComponent<MeshRenderer>().sharedMaterial = m;
            buoyRest.Add(new Vector2(at.x, at.z));
            return go.transform;
        }
    }
}
