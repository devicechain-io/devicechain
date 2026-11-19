// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class TruckBuilder
    {
        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m)
        {
            var r = new AssetRig(p, parent);
            r.Box("Chassis", new Vector3(0, 0.65f, 0), new Vector3(2.3f, 0.45f, 5.5f), m.DarkSteel);
            r.Box("Cab", new Vector3(0, 1.6f, 1.6f), new Vector3(2.25f, 1.85f, 1.9f), m.Render);
            r.Box("ToolBody", new Vector3(0, 1.2f, -1), new Vector3(2.3f, 1.2f, 3.1f), m.Steel);
            r.Box("Windscreen", new Vector3(0, 1.95f, 2.56f), new Vector3(1.95f, 0.8f, 0.04f), m.Glass);
            for (var s = -1; s <= 1; s += 2)
            {
                r.Box("SideWindow" + s, new Vector3(s * 1.14f, 1.95f, 1.6f), new Vector3(0.04f, 0.8f, 1.25f), m.Glass);
                r.Box("BlueStripe" + s, new Vector3(s * 1.17f, 1.2f, 0), new Vector3(0.025f, 0.20f, 5), m.Charger);
                r.Box("LightStripe" + s, new Vector3(s * 1.18f, 1.42f, 0), new Vector3(0.025f, 0.12f, 5), m.GaugeBoard);
                for (var a = -1; a <= 1; a += 2) r.Part("Wheel" + s + a, new MeshKit().Pipe(Vector3.zero, Vector3.right * s, 0.53f, 0.28f, 14), m.DarkSteel, new Vector3(s * 1.08f, 0.55f, a * 1.9f));
            }
            r.Box("LightBar", new Vector3(0, 2.6f, 1.6f), new Vector3(1.5f, 0.18f, 0.3f), m.Beacon);
            return r;
        }
    }
}
