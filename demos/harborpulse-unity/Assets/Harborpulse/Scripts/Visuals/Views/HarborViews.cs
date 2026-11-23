// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Linq;
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
        readonly Dictionary<string, LinkedChargerView> linked = new Dictionary<string, LinkedChargerView>();
        readonly Dictionary<string, PullLampView> pullLamps = new Dictionary<string, PullLampView>();
        readonly Dictionary<string, BeaconView> beacons = new Dictionary<string, BeaconView>();
        readonly Dictionary<string, int> slots = new Dictionary<string, int>();
        readonly List<(string Quay, Transform Spray, Transform Overtop)> sea = new List<(string, Transform, Transform)>();
        readonly HarborMaterials mats;
        readonly Transform feeder, rain;
        const float RainPeriod = 8;
        TruckView truck;
        public HarborViews(HarborWorld world, HarborMaterials mats)
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
                    case DeviceKind.MqttCharger: case DeviceKind.CertCharger: linked.Add(id, new LinkedChargerView(r, mats)); break;
                    case DeviceKind.PullLight: pullLamps.Add(id, new PullLampView(r, mats)); break;
                    case DeviceKind.Truck: truck = new TruckView(r); break;
                    default: if (DeviceKind.IsGauge(r.Placement.Device.Kind)) gauges.Add(id, new GaugeView(r, mats, 0)); break;
                }
            }
            var rainMesh = new MeshKit();
            var random = new System.Random(712);
            // Streaks slanted by the wind in a box that follows the camera. The same streaks repeat every RainPeriod metres of height, so
            // sliding the box down by the fall and wrapping it at one period never shows a seam; none is planted right at the lens.
            for (var i = 0; i < 320; i++)
            {
                var x = (float)random.NextDouble() * 70 - 35; var y = (float)random.NextDouble() * RainPeriod; var z = (float)random.NextDouble() * 70 - 35;
                var length = 0.5f + (float)random.NextDouble() * 0.5f;
                if (x * x + z * z < 36) continue;
                for (var layer = -1; layer < 3; layer++)
                {
                    var a = new Vector3(x, y + layer * RainPeriod, z);
                    rainMesh.Rod(a, a + new Vector3(-0.28f, -1f, 0.06f) * length, 0.004f);
                }
            }
            rain = rainMesh.Spawn("WORLD Rain", world.Root, mats.Rain, false).transform;
            var eastLamp = world.Rigs.First(r => r.Placement.Device.ExternalId == "PAW-LIGHT-EAST-3").Placement;
            var cab = AssetRig.Node("FeederCabinet", world.Root, new Vector3((float)eastLamp.At.X + 5, (float)SitePlan.DeckHeightM, (float)eastLamp.At.Z + 1));
            new MeshKit().Box(Vector3.up * 0.8f, new Vector3(1.1f, 1.6f, 0.6f)).Spawn("Cabinet", cab, mats.Steel);
            feeder = new MeshKit().Ball(Vector3.zero, 0.12f).Spawn("FeederDown", cab, mats.WorldCue, false).transform; feeder.localPosition = new Vector3(0, 1.3f, 0.36f);
            AssetRig.Tag("feeder down (WORLD)", feeder, Vector3.up * 0.3f);
            foreach (var rig in world.Rigs)
            {
                var placement = rig.Placement;
            {
                if (placement.Device.Kind != DeviceKind.Gauge) continue;
                var localFrame = world.Frames[placement.Device.Quay];
                var anchor = localFrame.Local(rig.Root.position);
                var x = (float)placement.At.X;
                var spray = new MeshKit();
                for (var i = 0; i < 30; i++) spray.Rod(new Vector3(i * 2, 0, 0), new Vector3(i * 2 - 0.5f, 1 + i % 4 * 0.3f, -0.6f), 0.035f);
                var t = spray.Spawn("SeawallSpray", world.Root, mats.WorldCue, false).transform;
                t.position = localFrame.World(new Vector3(anchor.x - 10, 2.8f, .6f));
                t.rotation = localFrame.Rotation;
                var over = new MeshKit().Box(Vector3.zero, new Vector3(65, 0.04f, 4)).Spawn("ObservedOvertopping", world.Root, mats.Flow, false).transform;
                over.position = localFrame.World(new Vector3(anchor.x, (float)SitePlan.DeckHeightM + .08f, -2));
                over.rotation = localFrame.Rotation;
                sea.Add((placement.Device.Quay, t, over));
            }
            }
        }
        /// <param name="weather">The weather drawn: the frame's own, unless a fair-weather look check of the preview has put the storm aside.</param>
        /// <summary>Decides every well's level tags for a camera (see <see cref="WellFootprint.TagsInView"/>).</summary>
        public void FrameWells(Vector3 camera, Vector3 forward, bool tallFrame)
        {
            foreach (var p in pumps) p.Value.FrameTags(camera, forward, tallFrame);
        }

        /// <param name="forward">The way the camera looks: a wet well's level tags are drawn only for a camera that frames the well.</param>
        /// <param name="tallFrame">A 9:16 frame: its view of a well stands further back, and the tags are drawn from there too.</param>
        public void Apply(HarborFrame f, SkyState sky, Vector3 camera, float dt, float time, Weather weather, Vector3 forward, bool tallFrame = false)
        {
            foreach (var p in pumps) p.Value.Apply(f.Pumps[p.Key], dt, time, camera, forward, tallFrame);
            foreach (var p in gauges) p.Value.Apply(f);
            foreach (var p in chargers) p.Value.Apply(f.Chargers[p.Key], f.World);
            foreach (var p in lamps) p.Value.Apply(f.Lamps[p.Key], f, sky, camera);
            foreach (var p in linked) p.Value.Apply(f.LinkedChargers[p.Key]);
            foreach (var p in pullLamps) p.Value.Apply(f.PullLamps[p.Key], sky);
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
            rain.gameObject.SetActive(weather.Rain > 0.01);
            // light rain is the same streaks squeezed shorter and closer; the wrap follows the squeezed period
            var squeeze = Mathf.Max(0.05f, (float)weather.Rain);
            rain.position = camera + Vector3.down * (time * 17 % (RainPeriod * squeeze));
            rain.localScale = new Vector3(1, squeeze, 1);
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
