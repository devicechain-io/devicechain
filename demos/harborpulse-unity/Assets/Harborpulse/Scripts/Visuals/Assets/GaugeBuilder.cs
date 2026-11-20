// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class GaugeBuilder
    {
        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m, HarborLayout layout)
        {
            var r = new AssetRig(p, parent);
            var lora = p.Device.Kind == DeviceKind.LoraGauge;
            var legacy = p.Device.Kind == DeviceKind.LegacyGauge;
            r.Part("Pile", new MeshKit().Cylinder(new Vector3(0, -8, 0), legacy ? 0.4f : 0.12f, 10.5f, 10), legacy ? m.Timber : m.Steel, Vector3.zero);
            r.Part("Housing", new MeshKit().Box(new Vector3(0, 2.1f, 0), new Vector3(.6f, .7f, .4f))
                .Box(new Vector3(0, 2.49f, 0), new Vector3(.72f, .08f, .52f)), m.Render, Vector3.zero);
            var mount = new MeshKit().Box(new Vector3(0, 2.65f, .4f), new Vector3(.35f, .25f, .6f))
                .Beam(new Vector3(0, 1.6f, 0), new Vector3(0, 2.55f, .63f), .09f)
                .Box(new Vector3(0, 1.85f, -.24f), new Vector3(.72f, .08f, .12f))
                .Box(new Vector3(0, 2.36f, -.24f), new Vector3(.72f, .08f, .12f));
            for (var i = 0; i < 3; i++) mount.Box(new Vector3(0, 2.05f + i * .1f, .212f), new Vector3(.36f, .025f, .025f));
            r.Part(lora ? "BatteryBox" : legacy ? "LegacyHead" : "RadarHead", mount, m.DarkSteel, Vector3.zero);
            if (lora) r.Part("Antenna", new MeshKit().Cylinder(Vector3.zero, 0.025f, 1.2f, 6), m.DarkSteel, new Vector3(0.2f, 2.4f, 0));
            else if (!legacy)
            {
                var solar = r.Box("SolarPanel", new Vector3(0, 3.0f, 0), new Vector3(0.9f, 0.06f, 0.65f), m.Charger);
                solar.localRotation = Quaternion.Euler(25, 0, 0);
            }
            var staff = new MeshKit().Box(new Vector3(0, -2, 0.62f), new Vector3(0.55f, 6, 0.09f));
            r.Part("TideStaff", staff, m.GaugeBoard, Vector3.zero);
            var values = new[] { layout.Levels.FloodMinorM, layout.Levels.FloodMajorM, layout.Levels.FloodCriticalM };
            var colours = new[] { m.Minor, m.Major, m.Critical };
            for (var i = 0; i < 3; i++) r.Box("Tier" + i, new Vector3(0, (float)values[i] - (float)SitePlan.DeckHeightM, 0.69f), new Vector3(0.53f, 0.12f, 0.03f), colours[i]);
            var ticks = new MeshKit();
            // Decimetres, alternating half-metre strokes; omit portions beyond the actual board.
            for (var i = -50; i <= 10; i++)
            {
                var y = i * .1f; var width = i % 10 == 0 ? .34f : i % 5 == 0 ? .25f : .12f;
                ticks.Quad(new Vector3(-.24f+width, y-.012f, .671f), new Vector3(-.24f+width, y+.012f, .671f),
                    new Vector3(-.24f, y+.012f, .671f), new Vector3(-.24f, y-.012f, .671f));
            }
            r.Part("StaffTicks", ticks, m.DarkSteel, Vector3.zero);
            r.Box("Plate", new Vector3(0, 1.2f, 0.26f), new Vector3(0.6f, 0.25f, 0.035f), m.Steel);
            r["Beacon"].localPosition = Vector3.up * 3.6f;
            return r;
        }
    }
}
