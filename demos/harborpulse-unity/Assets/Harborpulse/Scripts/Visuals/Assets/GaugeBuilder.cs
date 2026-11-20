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
            r.Box("Housing", new Vector3(0, 2.1f, 0), new Vector3(0.6f, 0.7f, 0.4f), m.Render);
            r.Box(lora ? "BatteryBox" : legacy ? "LegacyHead" : "RadarHead", new Vector3(0, 2.65f, 0.4f), new Vector3(0.35f, 0.25f, 0.6f), m.DarkSteel);
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
            for (var i = -4; i <= 3; i++) ticks.Box(new Vector3(-0.10f, i - (float)SitePlan.DeckHeightM, 0.7f), new Vector3(0.32f, 0.04f, 0.04f));
            r.Part("StaffTicks", ticks, m.DarkSteel, Vector3.zero);
            r.Box("Plate", new Vector3(0, 1.2f, 0.26f), new Vector3(0.6f, 0.25f, 0.035f), m.Steel);
            r["Beacon"].localPosition = Vector3.up * 3.6f;
            return r;
        }
    }
}
