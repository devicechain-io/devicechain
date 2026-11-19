// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>One street light in the scene: the lamp head whose material shows the platform's report, and its light.</summary>
    public sealed class LampRig
    {
        public Placement Placement { get; internal set; }
        public MeshRenderer Head { get; internal set; }

        /// <summary>A soft glow drawn round the head only while the lamp is lit.</summary>
        public MeshRenderer Halo { get; internal set; }
        public Light Light { get; internal set; }
        public Vector3 LabelAt { get; internal set; }
    }

    /// <summary>One tide gauge in the scene, and where its readout label hangs.</summary>
    public sealed class GaugeRig
    {
        public Placement Placement { get; internal set; }
        public Vector3 LabelAt { get; internal set; }
    }

    /// <summary>
    /// Builds the waterfront from the <see cref="SitePlan"/> at start-up, entirely in code: land, quay walls and copings, the promenade, jetties,
    /// the seabed, every device's model, the two metered buildings, the depot and a deterministic town behind the promenade. The static parts are
    /// merged into one mesh per material; the lamps keep their own heads and lights so the platform's reports can switch them.
    /// </summary>
    public sealed class HarborWorld
    {
        public const float SeabedY = -6.5f;

        readonly SitePlan plan;
        readonly HarborLayout layout;
        readonly HarborMaterials mats;
        readonly Dictionary<Material, MeshKit> statics = new Dictionary<Material, MeshKit>();
        readonly List<LampRig> lamps = new List<LampRig>();
        readonly List<GaugeRig> gauges = new List<GaugeRig>();
        readonly List<Transform> buoys = new List<Transform>();
        readonly List<Vector2> buoyRest = new List<Vector2>();

        public Transform Root { get; private set; }
        public IReadOnlyList<LampRig> Lamps => lamps;
        public IReadOnlyList<GaugeRig> Gauges => gauges;

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
            w.BuildQuays();
            w.BuildTown();
            foreach (var p in plan.Placements) w.BuildDevice(p);
            w.BuildDepot();
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

        void BuildGround()
        {
            float x0 = (float)plan.MinX - 12000, x1 = (float)plan.MaxX + 12000;
            // the town's land, its north face the sea wall
            Kit(mats.Ground).Slab(x0, x1, -16000, WallZ - 26, SeabedY, Deck);
            Kit(mats.Stone).Slab(x0, x1, WallZ - 1.6f, WallZ, SeabedY, Deck + 0.35f);
            Kit(mats.Coping).Slab(x0, x1, WallZ - 1.8f, WallZ + 0.15f, Deck + 0.35f, Deck + 0.55f);
            // the promenade the lights stand on, and the road behind it
            Kit(mats.Paving).Slab(x0, x1, WallZ - 26, WallZ - 1.6f, SeabedY, Deck + 0.02f);
            Kit(mats.Asphalt).Slab(x0, x1, -56, -46, Deck + 0.02f, Deck + 0.05f);
            // the harbour floor
            Kit(mats.Seabed).Slab(x0, x1, WallZ, WallZ + 4000, SeabedY - 1, SeabedY);
        }

        void BuildQuays()
        {
            foreach (var q in plan.Quays)
            {
                float x0 = (float)q.XMin, x1 = (float)q.XMax;
                // a working quay: a lighter apron, bollards along the edge and timber fenders on the wall face
                Kit(mats.Coping).Slab(x0, x1, WallZ - 24, WallZ - 1.8f, Deck + 0.02f, Deck + 0.06f);
                for (var x = x0 + 12; x < x1 - 6; x += 28)
                {
                    Kit(mats.DarkSteel).Cylinder(new Vector3(x, Deck + 0.55f, WallZ - 0.9f), 0.28f, 0.7f, 8, 0.34f);
                    Kit(mats.Timber).Box(new Vector3(x + 14, Deck - 1.6f, WallZ + 0.25f), new Vector3(0.45f, 4.6f, 0.4f));
                }

                // the jetty that carries the quay's LoRaWAN gauge
                var jx = (float)q.CenterX + 30;
                var jz1 = WallZ + (float)SitePlan.JettyLengthM;
                Kit(mats.Timber).Slab(jx - 2.2f, jx + 2.2f, WallZ, jz1 + 2, Deck - 0.1f, Deck + 0.15f);
                for (var z = WallZ + 6; z <= jz1 + 1; z += 7)
                {
                    Kit(mats.Timber).Cylinder(new Vector3(jx - 1.9f, SeabedY, z), 0.22f, Deck - SeabedY + 0.9f, 6);
                    Kit(mats.Timber).Cylinder(new Vector3(jx + 1.9f, SeabedY, z), 0.22f, Deck - SeabedY + 0.9f, 6);
                }

                Kit(mats.Steel).Box(new Vector3(jx - 2.1f, Deck + 0.65f, (WallZ + jz1) / 2), new Vector3(0.06f, 0.06f, jz1 - WallZ));
                Kit(mats.Steel).Box(new Vector3(jx + 2.1f, Deck + 0.65f, (WallZ + jz1) / 2), new Vector3(0.06f, 0.06f, jz1 - WallZ));
            }
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

        void House(Vector3 baseCenter, float width, float depth, float height, int floors, Material wall, bool pitched)
        {
            Kit(wall).Box(baseCenter + Vector3.up * height / 2, new Vector3(width, height, depth));
            if (pitched) Kit(mats.Slate).Gable(baseCenter + Vector3.up * height, width + 0.6f, depth + 0.6f, Mathf.Min(5, depth * 0.3f));
            else Kit(mats.Slate).Box(baseCenter + Vector3.up * (height + 0.25f), new Vector3(width + 0.3f, 0.5f, depth + 0.3f));
            // a band of windows per floor on the harbour-facing (north) side
            for (var f = 0; f < floors; f++)
            {
                var y = baseCenter.y + 1.7f + f * 3.2f;
                for (var wx = -width / 2 + 2.2f; wx < width / 2 - 1.5f; wx += 3.4f)
                    Kit(Lit(baseCenter.x + wx, y, baseCenter.z) ? mats.Windows : mats.WindowsDark).Box(new Vector3(baseCenter.x + wx + 0.7f, y, baseCenter.z + depth / 2 + 0.03f), new Vector3(1.4f, 1.5f, 0.08f));
            }
        }

        // about two windows in three are lit at night, fixed per window so the town does not flicker between frames or runs
        static bool Lit(float x, float y, float z)
        {
            var h = Mathf.Sin(x * 12.9898f + y * 78.233f + z * 37.719f) * 43758.5453f;
            return h - Mathf.Floor(h) < 0.66f;
        }

        void BuildDevice(Placement p)
        {
            var at = V(p.At, Deck);
            switch (p.Device.Kind)
            {
                case DeviceKind.Light:
                    Lamp(p, at);
                    break;
                case DeviceKind.Gauge:
                    WallGauge(p, at);
                    break;
                case DeviceKind.LoraGauge:
                    PostGauge(p, at, Deck + 0.15f, true);
                    break;
                case DeviceKind.LegacyGauge:
                    // its own pile in the water
                    Kit(mats.Timber).Cylinder(new Vector3(at.x, SeabedY, at.z), 0.45f, Deck - SeabedY + 0.8f, 8);
                    PostGauge(p, at, Deck + 0.8f, false);
                    break;
                case DeviceKind.Pump:
                    PumpStation(p, at);
                    break;
                case DeviceKind.Charger:
                    ChargerPost(at, (float)p.YawDeg);
                    break;
                case DeviceKind.Meter:
                    if (p.Device.Index == 1) Lofts(at);
                    else FerryTerminal(at);
                    break;
                case DeviceKind.Truck:
                    Truck(at, (float)p.YawDeg);
                    break;
            }
        }

        void Lamp(Placement p, Vector3 at)
        {
            const float h = 8.5f;
            Kit(mats.DarkSteel).Cylinder(at, 0.16f, h, 8, 0.09f);
            Kit(mats.DarkSteel).Box(at + new Vector3(0, h - 0.1f, 0.9f), new Vector3(0.1f, 0.1f, 1.9f));
            var headKit = new MeshKit().Box(new Vector3(0, 0, 0), new Vector3(0.55f, 0.18f, 0.9f));
            var head = headKit.Spawn("lamp " + p.Device.ExternalId, Root, mats.LampUnknown, false);
            head.transform.position = at + new Vector3(0, h - 0.25f, 1.7f);
            var lightGo = new GameObject("light " + p.Device.ExternalId);
            lightGo.transform.SetParent(head.transform, false);
            lightGo.transform.localPosition = Vector3.down * 0.3f;
            var halo = new MeshKit().Ball(Vector3.zero, 1.6f).Spawn("halo " + p.Device.ExternalId, head.transform, mats.LampHalo, false);
            halo.transform.localPosition = Vector3.down * 0.15f;
            var haloRenderer = halo.GetComponent<MeshRenderer>();
            haloRenderer.enabled = false;
            var light = lightGo.AddComponent<Light>();
            light.type = LightType.Point;
            light.range = 42;
            light.color = new Color(1.0f, 0.82f, 0.58f);
            light.intensity = 0;
            light.shadows = LightShadows.None;
            light.enabled = false;
            lamps.Add(new LampRig { Placement = p, Head = head.GetComponent<MeshRenderer>(), Halo = haloRenderer, Light = light, LabelAt = at + Vector3.up * (h + 1.5f) });
        }

        void WallGauge(Placement p, Vector3 at)
        {
            // a staff board on the wall face, datum at 0 m, with the flood tiers marked, and the gauge housing on the coping above it
            var z = WallZ + 0.35f;
            Kit(mats.GaugeBoard).Box(new Vector3(at.x, (SeabedY + 1 + Deck) / 2, z), new Vector3(0.5f, Deck - SeabedY - 1, 0.08f));
            for (var m = -4; m <= 2; m++) Kit(mats.DarkSteel).Box(new Vector3(at.x - 0.1f, m, z + 0.05f), new Vector3(0.3f, 0.05f, 0.03f));
            Band(at.x, z, layout.Levels.FloodMinorM, mats.Minor);
            Band(at.x, z, layout.Levels.FloodMajorM, mats.Major);
            Band(at.x, z, layout.Levels.FloodCriticalM, mats.Critical);
            Kit(mats.Steel).Box(new Vector3(at.x, Deck + 0.9f, WallZ - 0.8f), new Vector3(0.45f, 0.7f, 0.35f));
            Kit(mats.Steel).Box(new Vector3(at.x, Deck + 1.35f, WallZ - 0.2f), new Vector3(0.08f, 0.08f, 1.2f));
            Kit(mats.StatusGreen).Box(new Vector3(at.x, Deck + 1.1f, WallZ - 0.62f), new Vector3(0.12f, 0.06f, 0.02f));
            gauges.Add(new GaugeRig { Placement = p, LabelAt = new Vector3(at.x, Deck + 3.5f, WallZ) });
        }

        void Band(float x, float z, double level, Material m) =>
            Kit(m).Box(new Vector3(x + 0.12f, (float)level, z + 0.05f), new Vector3(0.28f, 0.1f, 0.03f));

        void PostGauge(Placement p, Vector3 at, float baseY, bool lora)
        {
            Kit(mats.Steel).Cylinder(new Vector3(at.x, baseY, at.z), 0.07f, 2.6f, 6);
            Kit(mats.Render).Box(new Vector3(at.x, baseY + 2.0f, at.z), new Vector3(0.5f, 0.6f, 0.35f));
            if (lora) Kit(mats.DarkSteel).Cylinder(new Vector3(at.x + 0.15f, baseY + 2.3f, at.z), 0.02f, 1.2f, 4);
            Kit(mats.Safety).Box(new Vector3(at.x, baseY + 1.0f, at.z), new Vector3(0.18f, 0.25f, 0.18f));
            // the ultrasonic head looks down at the water
            Kit(mats.DarkSteel).Box(new Vector3(at.x, baseY + 2.45f, at.z + 0.35f), new Vector3(0.2f, 0.12f, 0.5f));
            gauges.Add(new GaugeRig { Placement = p, LabelAt = new Vector3(at.x, baseY + 4.5f, at.z) });
        }

        void PumpStation(Placement p, Vector3 at)
        {
            House(at, 12, 9, 5, 1, mats.Brick, true);
            Kit(mats.DarkSteel).Box(at + new Vector3(0, 1.2f, 4.55f), new Vector3(2.2f, 2.4f, 0.1f));
            // the rising main runs under the promenade to an outfall through the wall
            Kit(mats.DarkSteel).Pipe(new Vector3(at.x, -0.6f, WallZ - 2f), Vector3.forward, 0.55f, 2.4f);
            Kit(mats.Stone).Box(new Vector3(at.x, Deck + 0.2f, (at.z + WallZ) / 2), new Vector3(1.6f, 0.15f, WallZ - at.z - 4));
            Kit(mats.Beacon).Box(at + new Vector3(4.5f, 5.6f, 3.5f), new Vector3(0.35f, 0.35f, 0.35f));
        }

        void ChargerPost(Vector3 at, float yaw)
        {
            Kit(mats.Charger).Box(at + Vector3.up * 0.8f, new Vector3(0.55f, 1.6f, 0.38f), yaw);
            Kit(mats.DarkSteel).Box(at + new Vector3(0, 1.25f, -0.2f), new Vector3(0.36f, 0.3f, 0.02f), yaw);
            Kit(mats.StatusGreen).Box(at + new Vector3(0, 1.52f, -0.2f), new Vector3(0.3f, 0.04f, 0.02f), yaw);
            // its parking bay
            Kit(mats.Paving).Box(at + new Vector3(0, 0.04f, -3.4f), new Vector3(2.6f, 0.04f, 5.2f), yaw);
        }

        void Lofts(Vector3 at)
        {
            House(at, 38, 18, 17, 5, mats.Render, false);
            Kit(mats.Glass).Box(at + new Vector3(0, 9, 9.05f), new Vector3(30, 14, 0.1f));
        }

        void FerryTerminal(Vector3 at)
        {
            House(at, 56, 22, 8, 2, mats.Render, false);
            Kit(mats.Glass).Box(at + new Vector3(0, 3.5f, 11.05f), new Vector3(48, 5, 0.1f));
            Kit(mats.Steel).Box(at + new Vector3(0, 8.4f, 15f), new Vector3(60, 0.3f, 10));
            // the ferry berth: a pontoon off the wall in front of it
            Kit(mats.Timber).Slab(at.x - 18, at.x + 18, WallZ + 6, WallZ + 16, Deck - 1.4f, Deck - 0.9f);
            Kit(mats.Steel).Slab(at.x - 1.2f, at.x + 1.2f, WallZ, WallZ + 6, Deck - 0.9f, Deck - 0.7f);
        }

        void Truck(Vector3 at, float yaw)
        {
            Kit(mats.Safety).Box(at + new Vector3(0, 1.6f, 0), new Vector3(2.4f, 2.4f, 5.2f), yaw);
            Kit(mats.Render).Box(at + Quaternion.Euler(0, yaw, 0) * new Vector3(0, 1.4f, 3.5f), new Vector3(2.4f, 2.0f, 1.8f), yaw);
            Kit(mats.Beacon).Box(at + Quaternion.Euler(0, yaw, 0) * new Vector3(0, 2.55f, 3.6f), new Vector3(1.4f, 0.15f, 0.25f), yaw);
            for (var s = -1; s <= 1; s += 2)
            for (var a = -1; a <= 1; a += 2)
                Kit(mats.DarkSteel).Pipe(at + Quaternion.Euler(0, yaw, 0) * new Vector3(s * 1.25f, 0.5f, a * 1.9f), Quaternion.Euler(0, yaw, 0) * Vector3.right * s, 0.5f, 0.3f, 10);
        }

        void BuildDepot()
        {
            var d = V(plan.Depot, Deck);
            Kit(mats.Asphalt).Box(d + new Vector3(0, 0.06f, 0), new Vector3(70, 0.06f, 50));
            House(d + new Vector3(-18, 0, -14), 28, 16, 7, 1, mats.Steel, true);
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
