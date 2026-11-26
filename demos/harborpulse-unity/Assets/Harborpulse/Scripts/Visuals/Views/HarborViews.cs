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
        readonly List<(string Quay, Transform Spray, Transform Overtop)> sea = new List<(string, Transform, Transform)>();
        readonly HarborMaterials mats;
        readonly DeviceChain.Demos.Hud.PinAnchors pinAnchors = new DeviceChain.Demos.Hud.PinAnchors();
        readonly Transform feeder, rain;
        readonly Mesh rainMesh;
        readonly List<Vector3> rainVertices = new List<Vector3>(), rainNormals = new List<Vector3>();
        readonly List<int> rainTriangles = new List<int>();
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
            // The rain: RainField says where every drop is; this draws each as a ribbon turned to the eye, rebuilt every frame.
            rainMesh = new Mesh { name = "WORLD Rain" }; rainMesh.MarkDynamic();
            var rainObject = new GameObject("WORLD Rain"); rainObject.transform.SetParent(world.Root, false);
            rainObject.AddComponent<MeshFilter>().sharedMesh = rainMesh;
            var rainRenderer = rainObject.AddComponent<MeshRenderer>(); rainRenderer.sharedMaterial = mats.Rain;
            rainRenderer.shadowCastingMode = UnityEngine.Rendering.ShadowCastingMode.Off; rainRenderer.receiveShadows = false;
            rain = rainObject.transform;
            var eastLamp = world.Rigs.First(r => r.Placement.Device.ExternalId == "PAW-LIGHT-EAST-3").Placement;
            var cab = AssetRig.Node("FeederCabinet", world.Root, new Vector3((float)eastLamp.At.X + 5, (float)SitePlan.DeckHeightM, (float)eastLamp.At.Z + 1));
            new MeshKit().Box(Vector3.up * 0.8f, new Vector3(1.1f, 1.6f, 0.6f)).Spawn("Cabinet", cab, mats.Steel);
            feeder = new MeshKit().Ball(Vector3.zero, 0.12f).Spawn("FeederDown", cab, mats.WorldCue, false).transform; feeder.localPosition = new Vector3(0, 1.3f, 0.36f);
            AssetRig.Tag(WorldText.FeederDown, feeder, Vector3.up * 0.3f, 0.12f, null, true);
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
        /// <summary>
        /// Where a device's alarm pin stands: its tip half a metre over the device's highest point (each rig's "Beacon" node is authored
        /// there), and the spot on the deck under it for the pin's ground glow. False for a device the scene does not draw.
        /// </summary>
        public bool TryPinPlace(string externalId, out Vector3 top, out Vector3 ground)
        {
            top = ground = default;
            if (externalId == null || !rigs.TryGetValue(externalId, out var rig)) return false;
            // A device that does not move has one anchor, taken once (PinAnchors): its pin cannot wander. The truck is followed, and the
            // composition root has put it in its final place for the frame before the HUD asks.
            var now = rig["Beacon"].position; var moving = rig.Placement.Device.Kind == DeviceKind.Truck;
            var at = pinAnchors.Place(externalId, moving, now.x, now.y, now.z);
            top = new Vector3((float)at[0], (float)at[1], (float)at[2]); ground = new Vector3(top.x, rig.Root.position.y + 0.05f, top.z);
            return true;
        }
        /// <param name="weather">The weather drawn: the frame's own, unless a fair-weather look check of the preview has put the storm aside.</param>
        /// <summary>Decides every well's level tags for a camera (see <see cref="WellFootprint.TagsInView"/>).</summary>
        public void FrameWells(Vector3 camera, Vector3 forward, bool tallFrame)
        {
            foreach (var p in pumps) p.Value.FrameTags(camera, forward, tallFrame);
        }

        /// <param name="forward">The way the camera looks: a wet well's level tags are drawn only for a camera that frames the well.</param>
        /// <param name="tallFrame">A 9:16 frame: its view of a well stands further back, and the tags are drawn from there too.</param>
        void DrawRain(Weather weather, Vector3 eye, float time)
        {
            var count = RainField.Count(weather);
            rain.gameObject.SetActive(count > 0);
            if (count == 0) return;
            rainVertices.Clear(); rainNormals.Clear(); rainTriangles.Clear();
            for (var i = 0; i < count; i++)
            {
                if (!RainField.TryStreak(i, time, weather, eye.x, eye.y, eye.z, out var streak)) continue;
                var head = new Vector3((float)streak.X, (float)streak.Y, (float)streak.Z); var tail = new Vector3((float)streak.TailX, (float)streak.TailY, (float)streak.TailZ);
                var toEye = eye - head; var distance = toEye.magnitude;
                var side = Vector3.Cross(tail, toEye).normalized * Mathf.Max((float)RainField.MinWidthM, (float)RainField.WidthPerMetre * distance); // under a pixel wide at any range
                var normal = toEye / distance; var at = rainVertices.Count;
                rainVertices.Add(head - side); rainVertices.Add(head + side); rainVertices.Add(head + tail + side); rainVertices.Add(head + tail - side);
                for (var k = 0; k < 4; k++) rainNormals.Add(normal);
                rainTriangles.Add(at); rainTriangles.Add(at + 1); rainTriangles.Add(at + 2); rainTriangles.Add(at); rainTriangles.Add(at + 2); rainTriangles.Add(at + 3);
                rainTriangles.Add(at); rainTriangles.Add(at + 2); rainTriangles.Add(at + 1); rainTriangles.Add(at); rainTriangles.Add(at + 3); rainTriangles.Add(at + 2);
            }
            rain.SetPositionAndRotation(Vector3.zero, Quaternion.identity); rain.localScale = Vector3.one;
            rainMesh.Clear(); rainMesh.SetVertices(rainVertices); rainMesh.SetNormals(rainNormals); rainMesh.SetTriangles(rainTriangles, 0);
            rainMesh.bounds = new Bounds(eye, new Vector3((float)RainField.BoxX, (float)RainField.BoxY, (float)RainField.BoxZ) * 1.1f);
        }
        public void Apply(HarborFrame f, SkyState sky, Vector3 camera, float dt, float time, Weather weather, Vector3 forward, bool tallFrame = false)
        {
            foreach (var p in pumps) p.Value.Apply(f.Pumps[p.Key], dt, time, camera, forward, tallFrame);
            foreach (var p in gauges) p.Value.Apply(f);
            foreach (var p in chargers) p.Value.Apply(f.Chargers[p.Key], f.World);
            foreach (var p in lamps) p.Value.Apply(f.Lamps[p.Key], f, sky, camera);
            foreach (var p in linked) p.Value.Apply(f.LinkedChargers[p.Key]);
            foreach (var p in pullLamps) p.Value.Apply(f.PullLamps[p.Key], sky);
            truck?.Apply(f.Truck, dt, time);
            // an alarm is a pin on the story HUD, placed by TryPinPlace: no beam, no rings and no count box stand in the world
            DrawRain(weather, camera, time);
            WorldTags.Apply();
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
