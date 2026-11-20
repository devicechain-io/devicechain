// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class HarborViews
    {
        readonly Dictionary<string, AssetRig> rigs = new Dictionary<string, AssetRig>();
        readonly Dictionary<string, PumpStationView> pumps = new Dictionary<string, PumpStationView>();
        readonly Dictionary<string, GaugeView> gauges = new Dictionary<string, GaugeView>();
        readonly Dictionary<string, ChargerView> chargers = new Dictionary<string, ChargerView>();
        readonly Dictionary<string, LampView> lamps = new Dictionary<string, LampView>();
        readonly Dictionary<string, BeaconView> beacons = new Dictionary<string, BeaconView>();
        readonly Dictionary<string, int> slots = new Dictionary<string, int>();
        readonly List<(string Quay, Transform Spray, Transform Overtop)> sea = new List<(string, Transform, Transform)>();
        readonly HarborMaterials mats;
        readonly Transform feeder, rain;
        TruckView truck;
        public HarborViews(HarborWorld world, SitePlan plan, HarborMaterials mats)
        {
            this.mats = mats;
            foreach (var r in world.Rigs)
            {
                var id = r.Placement.Device.ExternalId; rigs.Add(id, r);
                switch (r.Placement.Device.Kind)
                {
                    case DeviceKind.Pump: pumps.Add(id, new PumpStationView(r)); break;
                    case DeviceKind.Charger: chargers.Add(id, new ChargerView(r, mats)); break;
                    case DeviceKind.Light: lamps.Add(id, new LampView(r, mats)); break;
                    case DeviceKind.Truck: truck = new TruckView(r); break;
                    default: if (DeviceKind.IsGauge(r.Placement.Device.Kind)) gauges.Add(id, new GaugeView(r, mats, plan.WallZ)); break;
                }
            }
            var rainMesh = new MeshKit();
            var random = new System.Random(712);
            for (var i = 0; i < 240; i++)
            {
                var a = new Vector3((float)random.NextDouble() * 50 - 25, (float)random.NextDouble() * 28, (float)random.NextDouble() * 50 - 25);
                rainMesh.Rod(a, a + new Vector3(-0.10f, -0.6f, 0), 0.008f);
            }
            rain = rainMesh.Spawn("WORLD Rain", world.Root, mats.WorldCue, false).transform;
            var eastLamp = plan.Find("PAW-LIGHT-EAST-3");
            var cab = AssetRig.Node("FeederCabinet", world.Root, new Vector3((float)eastLamp.At.X + 5, (float)SitePlan.DeckHeightM, (float)eastLamp.At.Z + 1));
            new MeshKit().Box(Vector3.up * 0.8f, new Vector3(1.1f, 1.6f, 0.6f)).Spawn("Cabinet", cab, mats.Steel);
            feeder = new MeshKit().Ball(Vector3.zero, 0.12f).Spawn("FeederDown", cab, mats.WorldCue, false).transform; feeder.localPosition = new Vector3(0, 1.3f, 0.36f);
            AssetRig.Tag("feeder down (WORLD)", feeder, Vector3.up * 0.3f);
            foreach (var placement in plan.Placements)
            {
                if (placement.Device.Kind != DeviceKind.Gauge) continue;
                var q = plan.Quay(placement.Device.Quay);
                var x = (float)placement.At.X;
                var spray = new MeshKit();
                for (var i = 0; i < 30; i++) spray.Rod(new Vector3(i * 2, 0, 0), new Vector3(i * 2 - 0.5f, 1 + i % 4 * 0.3f, -0.6f), 0.035f);
                var t = spray.Spawn("SeawallSpray", world.Root, mats.WorldCue, false).transform;
                t.position = new Vector3(x - 30, 2.8f, (float)plan.WallZ + 0.6f);
                var over = new MeshKit().Box(Vector3.zero, new Vector3(65, 0.04f, 4)).Spawn("ObservedOvertopping", world.Root, mats.Flow, false).transform;
                over.position = new Vector3(x, (float)SitePlan.DeckHeightM + 0.08f, (float)plan.WallZ - 2);
                sea.Add((q.Token, t, over));
            }
        }
        public void Apply(HarborFrame f, SkyState sky, Vector3 camera, float dt, float time)
        {
            foreach (var p in pumps) p.Value.Apply(f.Pumps[p.Key], dt, time);
            foreach (var p in gauges) p.Value.Apply(f);
            foreach (var p in chargers) p.Value.Apply(f.Chargers[p.Key], f.World);
            foreach (var p in lamps) p.Value.Apply(f.Lamps[p.Key], f, sky, camera);
            truck?.Apply(f.Truck, dt, time);
            foreach (var b in beacons.Values) b.Hide();
            foreach (var alarm in f.Beacons)
            {
                if (!rigs.TryGetValue(alarm.Device, out var rig)) continue;
                if (!beacons.TryGetValue(alarm.Identity, out var b))
                {
                    slots.TryGetValue(alarm.Device, out var slot); slots[alarm.Device] = slot + 1;
                    b = new BeaconView(rig["Beacon"], mats, slot); beacons.Add(alarm.Identity, b);
                }
                b.Apply(alarm, f.ObservedAt);
            }
            rain.gameObject.SetActive(f.World.Weather.Rain > 0.01);
            rain.position = camera + Vector3.down * (time * 15 % 8);
            rain.localScale = new Vector3(1, (float)f.World.Weather.Rain, 1);
            feeder.gameObject.SetActive(f.World.EastFeederOutage > 0);
            feeder.localScale = Vector3.one * (float)f.World.EastFeederOutage;
            foreach (var s in sea)
            {
                var intensity = (float)f.Spray[s.Quay]; s.Spray.gameObject.SetActive(intensity > 0.01f);
                s.Spray.localScale = new Vector3(1, intensity * (0.75f + 0.25f * Mathf.Sin(time * 7)), 1);
                var overflow = (float)f.Overtopping[s.Quay];
                s.Overtop.gameObject.SetActive(overflow > 0);
                s.Overtop.localScale = new Vector3(1, 1, overflow * 2);
            }
        }
    }
}
