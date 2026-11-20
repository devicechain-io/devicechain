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
                for (var a = -1; a <= 1; a += 2) r.Part("Wheel" + s + a, new MeshKit().Pipe(Vector3.zero, Vector3.right * s, 0.53f, 0.28f, 10), m.DarkSteel, new Vector3(s * 1.08f, 0.55f, a * 1.9f));
            }
            r.Box("LightBar", new Vector3(0, 2.6f, 1.6f), new Vector3(1.5f, 0.18f, 0.3f), m.Beacon);
            var hardware = new MeshKit().Box(new Vector3(0,.75f,2.64f),new Vector3(2.45f,.22f,.22f))
                .Box(new Vector3(0,.7f,-2.65f),new Vector3(2.45f,.2f,.18f))
                .Box(new Vector3(0,1.2f,2.59f),new Vector3(1.3f,.35f,.05f))
                .Box(new Vector3(0,1.94f,2.59f),new Vector3(.065f,.84f,.04f));
            var bright = new MeshKit(); var glass = new MeshKit();
            for (var s = -1; s <= 1; s += 2)
            {
                hardware.Beam(new Vector3(s*1.15f,2,2.25f),new Vector3(s*1.4f,2,2.05f),.055f)
                    .Box(new Vector3(s*1.42f,2,2.05f),new Vector3(.12f,.27f,.22f))
                    .Box(new Vector3(s*1.19f,.82f,1.6f),new Vector3(.25f,.1f,1.3f));
                bright.Box(new Vector3(s*1.14f,1.45f,1.7f),new Vector3(.04f,.06f,.28f));
                for (var a = -1; a <= 1; a += 2)
                {
                    bright.Pipe(new Vector3(s*1.37f,.55f,a*1.9f),Vector3.right*s,.23f,.025f,8);
                    hardware.Box(new Vector3(s*1.15f,1.04f,a*1.9f),new Vector3(.36f,.12f,1.25f))
                        .Box(new Vector3(s*1.15f,.5f,a*1.9f-.57f),new Vector3(.35f,.55f,.07f));
                }
                // Rack and a low ladder stay inside the tool body's existing footprint.
                bright.Beam(new Vector3(s*.8f,1.8f,-2.4f),new Vector3(s*.8f,2.27f,-2.4f),.07f)
                    .Beam(new Vector3(s*.8f,1.8f,.3f),new Vector3(s*.8f,2.27f,.3f),.07f)
                    .Beam(new Vector3(s*.4f,2.3f,-2.45f),new Vector3(s*.4f,2.3f,.4f),.065f);
                for (var i = 0; i < 3; i++)
                {
                    bright.Box(new Vector3(s*1.17f,1.2f,-2+i*.9f),new Vector3(.03f,.8f,.025f));
                    hardware.Box(new Vector3(s*1.19f,1.45f,-1.8f+i*.9f),new Vector3(.03f,.05f,.22f));
                }
                glass.Box(new Vector3(s*.78f,1.32f,2.6f),new Vector3(.4f,.22f,.04f));
            }
            for (var z = -2.3f; z < .4f; z += .35f)
                bright.Beam(new Vector3(-.4f,2.3f,z),new Vector3(.4f,2.3f,z),.045f);
            r.Part("BumpersMudguards",hardware,m.DarkSteel,Vector3.zero);
            r.Part("HubsLadderRack",bright,m.Steel,Vector3.zero);
            r.Part("Headlamps",glass,m.Glass,Vector3.zero);
            r.MergeFixed("Chassis", "BumpersMudguards", "Wheel-1-1", "Wheel-11", "Wheel1-1", "Wheel11");
            r.MergeFixed("ToolBody", "HubsLadderRack");
            r.MergeFixed("Windscreen", "SideWindow-1", "SideWindow1", "Headlamps");
            r.MergeFixed("BlueStripe-1", "BlueStripe1");
            r.MergeFixed("LightStripe-1", "LightStripe1");
            return r;
        }
    }
}
