// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class LampBuilder
    {
        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m)
        {
            var r = new AssetRig(p, parent);
            r.Part("Column", new MeshKit().Cylinder(Vector3.zero, 0.16f, 8.5f, 10, 0.09f), m.DarkSteel, Vector3.zero);
            r.Part("Base", new MeshKit().Cylinder(Vector3.zero, 0.3f, 0.25f, 12), m.Steel, Vector3.zero);
            r.Box("Bracket", new Vector3(0, 8.4f, 0.9f), new Vector3(0.12f, 0.12f, 1.9f), m.DarkSteel);
            var head = r.Box("Lantern", new Vector3(0, 8.25f, 1.7f), new Vector3(0.55f, 0.18f, 0.9f), m.LampUnknown);
            r.Part("Halo", new MeshKit().Ball(Vector3.zero, 1.6f), m.LampHalo, head.localPosition - Vector3.up * 0.15f, false);
            var light = AssetRig.Node("Light", head, Vector3.down * 0.3f).gameObject.AddComponent<Light>();
            light.type = LightType.Point; light.range = 42; light.color = new Color(1, 0.82f, 0.58f); light.shadows = LightShadows.None;
            r.Part("PlatformWindow", new MeshKit().Ring(Vector3.zero, 2.3f, 0.05f), m.StatusGreen, Vector3.up * 0.08f, false);
            r["Beacon"].localPosition = new Vector3(0, 9.1f, 1.7f);
            r["CardAnchor"].localPosition = new Vector3(0, 10, 1.7f);
            return r;
        }
    }
}
