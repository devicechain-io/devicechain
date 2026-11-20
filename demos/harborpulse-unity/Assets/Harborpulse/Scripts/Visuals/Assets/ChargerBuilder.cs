// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class ChargerBuilder
    {
        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m)
        {
            var r = new AssetRig(p, parent);
            r.Box("Pedestal", Vector3.up * 0.95f, new Vector3(0.7f, 1.9f, 0.5f), m.Charger);
            r.Box("Screen", new Vector3(0, 1.45f, 0.26f), new Vector3(0.48f, 0.36f, 0.035f), m.LampUnknown);
            r.Box("Holster", new Vector3(0.42f, 1.0f, 0.2f), new Vector3(0.18f, 0.35f, 0.22f), m.DarkSteel);
            r.Box("Connector", new Vector3(0.45f, 1.2f, 0.4f), new Vector3(0.12f, 0.25f, 0.22f), m.Flow);
            var car = r.Add("Car", new Vector3(0, 0, 4));
            new MeshKit().Box(new Vector3(0, 0.65f, 0), new Vector3(1.8f, 0.65f, 3.6f)).Box(new Vector3(0, 1.16f, -0.2f), new Vector3(1.6f, 0.65f, 1.9f)).Spawn("UnbrandedHatchback", car, m.Render);
            new MeshKit().Box(new Vector3(0, 1.2f, 0.79f), new Vector3(1.5f, 0.45f, 0.04f)).Spawn("CarWindows", car, m.Glass);
            var wheels = new MeshKit();
            for (var side = -1; side <= 1; side += 2) for (var axle = -1; axle <= 1; axle += 2)
                wheels.Pipe(new Vector3(side * 0.78f, 0.4f, axle * 1.1f), Vector3.right * side, 0.34f, 0.22f, 12);
            wheels.Spawn("CarWheels", car, m.DarkSteel);
            var cable = new MeshKit(); var last = new Vector3(0.45f, 1.1f, 0.4f);
            for (var i = 1; i <= 16; i++) { var t = i / 16f; var next = Vector3.Lerp(new Vector3(0.45f, 1.1f, 0.4f), new Vector3(0.8f, 0.8f, 3), t) - Vector3.up * Mathf.Sin(t * Mathf.PI) * 0.75f; cable.Rod(last, next, 0.035f); last = next; }
            r.Part("Cable", cable, m.DarkSteel, Vector3.zero);
            var bay = new MeshKit();
            for (var side = -1; side <= 1; side += 2) bay.Box(new Vector3(side * 1.35f, 0.05f, 3.1f), new Vector3(0.08f, 0.03f, 5.8f));
            bay.Box(new Vector3(0, 0.05f, 6), new Vector3(2.7f, 0.03f, 0.08f));
            r.Part("BayMarkings", bay, m.GaugeBoard, Vector3.zero);
            r.Part("MaintenanceRing", new MeshKit().Ring(Vector3.zero, 3.4f, 0.06f), m.WorldCue, Vector3.up * 0.08f, false);
            r["CardAnchor"].localPosition = Vector3.up * 2.8f;
            return r;
        }
    }
}
