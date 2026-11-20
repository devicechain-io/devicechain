// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>WORLD crew props use only the cool scene palette.</summary>
    public static class CrewBuilder
    {
        public static Transform Build(Transform parent, HarborMaterials m, bool lamp, Vector3 at)
        {
            var t = AssetRig.Node(lamp ? "LampCrew" : "ReplacementCrew", parent, at);
            new MeshKit().Box(new Vector3(0, 1, 0), new Vector3(2.2f, 1.6f, 4.6f)).Box(new Vector3(0, 1.9f, 0.4f), new Vector3(2, 1, 3.4f)).Spawn("TechnicianVan", t, m.Render);
            var glass = new MeshKit().Box(new Vector3(0, 2, 2.12f), new Vector3(1.7f, .7f, .04f));
            for (var s = -1; s <= 1; s += 2) glass.Box(new Vector3(s*1.01f,2,1.2f),new Vector3(.03f,.65f,1.1f));
            glass.Spawn("VanWindows", t, m.Glass);
            var wheels = new MeshKit();
            for (var s = -1; s <= 1; s += 2) for (var a = -1; a <= 1; a += 2) wheels.Pipe(new Vector3(s, 0.4f, a * 1.4f), Vector3.right * s, 0.4f, 0.2f, 8);
            wheels.Spawn("VanWheels", t, m.DarkSteel);
            var cones = new MeshKit();
            for (var i = 0; i < 5; i++) cones.Cylinder(new Vector3(-3 + i * 1.5f, 0, 3), 0.25f, 0.65f, 6, 0.04f).Box(new Vector3(-3 + i * 1.5f, 0.04f, 3), new Vector3(0.55f, 0.08f, 0.55f));
            cones.Spawn("TrafficCones", t, m.WorldCue);
            new MeshKit().Box(new Vector3(-3, 0.9f, 0), new Vector3(0.12f, 0.6f, 3)).Beam(new Vector3(-3, 0, -1.4f), new Vector3(-3, 1.3f, -1.4f), .08f).Beam(new Vector3(-3, 0, 1.4f), new Vector3(-3, 1.3f, 1.4f), .08f).Spawn("WorkBarrier", t, m.WorldCue);
            if (lamp)
            {
                var boom = new MeshKit().Beam(new Vector3(0, 1, 0), new Vector3(-1, 4, 0), .25f)
                    .Beam(new Vector3(-1, 4, 0), new Vector3(-3, 7.6f, 2), .21f)
                    .Box(new Vector3(-3, 7.5f, 2), new Vector3(1.5f, .15f, 1.3f));
                for (var s = -1; s <= 1; s += 2)
                {
                    boom.Beam(new Vector3(-3+s*.65f,7.5f,1.45f),new Vector3(-3+s*.65f,8.4f,1.45f),.055f)
                        .Beam(new Vector3(-3+s*.65f,7.5f,2.55f),new Vector3(-3+s*.65f,8.4f,2.55f),.055f)
                        .Beam(new Vector3(-3+s*.65f,8.4f,1.45f),new Vector3(-3+s*.65f,8.4f,2.55f),.055f)
                        .Beam(new Vector3(-3.65f,8.4f,2+s*.55f),new Vector3(-2.35f,8.4f,2+s*.55f),.055f);
                }
                boom.Spawn("CherryPicker", t, m.WorldCue);
                var ladder = new MeshKit();
                for (var s = -1; s <= 1; s += 2) ladder.Beam(new Vector3(-2 + s * 0.25f, 0, 1), new Vector3(-2 + s * 0.25f, 4, 0), .08f);
                for (var y = 0.3f; y < 4; y += 0.3f) ladder.Beam(new Vector3(-2.25f, y, 1 - y / 4), new Vector3(-1.75f, y, 1 - y / 4), .06f);
                ladder.Spawn("Ladder", t, m.Steel);
            }
            AssetRig.MergeFixed(t, t.GetComponentsInChildren<MeshFilter>());
            t.gameObject.SetActive(false); return t;
        }
    }
}
