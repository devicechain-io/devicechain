// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class PumpStationBuilder
    {
        // wet_well_m is measured from the well FLOOR, independently of harbour datum.
        public const float WellFloor = -4.4f;
        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m, float wallZ)
        {
            var r = new AssetRig(p, parent);
            r.Box("Kiosk", new Vector3(-2, 2.2f, -1), new Vector3(8, 4.4f, 7), m.Brick);
            r.Part("Roof", new MeshKit().Gable(Vector3.zero, 9, 8, 1.5f), m.Slate, new Vector3(-2, 4.4f, -1));
            r.Box("Door", new Vector3(-3, 1.3f, 2.56f), new Vector3(1.7f, 2.6f, 0.12f), m.DarkSteel);
            r.Part("ControlCabinet", new MeshKit().Box(new Vector3(3.1f, 1.1f, 2), new Vector3(1.4f, 2.2f, .6f))
                .Box(new Vector3(3.1f,2.25f,2),new Vector3(1.55f,.12f,.75f)), m.Steel, Vector3.zero);
            r.Part("Ammeter", new MeshKit().Pipe(Vector3.zero, Vector3.forward, 0.38f, 0.04f, 16), m.GaugeBoard, new Vector3(3.1f, 1.55f, 2.33f));
            var needle = r.Add("CurrentNeedle", new Vector3(3.1f, 1.55f, 2.40f));
            new MeshKit().Box(new Vector3(0, 0.14f, 0), new Vector3(0.025f, 0.3f, 0.025f)).Spawn("Needle", needle, m.DarkSteel);
            var c = new Vector3((float)WellFootprint.LocalX, WellFloor, (float)WellFootprint.LocalZ);
            r.Part("ConcreteWell", new MeshKit().Shell(Vector3.zero, (float)WellFootprint.RadiusM, 4.5f, 0.22f, 180, 360), m.Coping, c);
            r.Part("GlassSection", new MeshKit().Shell(Vector3.zero, 2.43f, 4.5f, 0.025f, 0, 180), m.WellGlass, c, false);
            r.Part("WellFloor", new MeshKit().Cylinder(Vector3.zero, 2.4f, 0.12f, 24), m.Coping, c - Vector3.up * 0.12f);
            r.Part("WellSurface", new MeshKit().Cylinder(Vector3.zero, 2.19f, 0.025f, 48), m.WetWell, c, false);
            // the cut-away is the scene's one deliberate x-ray, so it carries its own cool work light: without it the well is unreadable at night
            var wellLight = r.Add("WellLight", c + new Vector3(0, 4.3f, 0.6f)).gameObject.AddComponent<Light>();
            wellLight.type = LightType.Point; wellLight.range = 7.5f; wellLight.intensity = 5f; wellLight.color = new Color(0.82f, 0.9f, 1f); wellLight.shadows = LightShadows.None;
            r.Part("Motor", new MeshKit().Cylinder(Vector3.zero, 0.55f, 0.9f, 16), m.Steel, c + new Vector3(0, 0.2f, -0.4f));
            var impeller = r.Add("Impeller", c + new Vector3(0, 0.35f, 0.85f));
            var blades = new MeshKit();
            for (var i = 0; i < 4; i++) blades.Box(Vector3.zero, new Vector3(1.55f, 0.10f, 0.25f), i * 45);
            blades.Spawn("Blades", impeller, m.Flow);
            // the three fixed levels are named for a viewer; the tenant's own rule level names its source and is an engineer's tag
            Marker(r, m, "LowCutoff", (float)PumpPresentation.LowCutoffM, m.Steel, WorldText.PumpStops, 0, new Color(0.62f, 0.68f, 0.74f));
            Marker(r, m, "HighMarker", (float)PumpPresentation.HighMarkerM, m.Major, WorldText.AlarmLevel, 0, new Color(1f, 0.62f, 0.2f));
            Marker(r, m, "FloatMarker", (float)PumpPresentation.FloatMarkerM, m.GaugeBoard, WorldText.FloatSwitch, 0, new Color(0.92f, 0.92f, 0.88f));
            Marker(r, m, "AttributeMarker", 0, m.Minor, WorldText.AttributeRule, -1.9f, new Color(1f, 0.85f, 0.35f), -AttributeTagDropM, true);
            r["AttributeMarker"].gameObject.SetActive(false);
            r.Part("Float", new MeshKit().Ball(Vector3.zero, 0.18f), m.WorldCue, c + new Vector3(1.3f, 3.2f, 0));
            r.Part("FloatChain", new MeshKit().Cylinder(Vector3.zero, 0.025f, 1.2f, 6), m.Steel, c + new Vector3(1.3f, 3.2f, 0));
            var fittings = new MeshKit(); var plumbing = new MeshKit();
            for (var i = 0; i < 5; i++)
                fittings.Box(new Vector3(3.1f,.4f+i*.13f,2.315f),new Vector3(.95f,.045f,.045f));
            fittings.Box(new Vector3(3.63f,1.25f,2.34f),new Vector3(.08f,.3f,.06f))
                .Box(new Vector3(-2.1f,1.4f,2.65f),new Vector3(.08f,.35f,.1f));
            // The open front and the level markers remain unobstructed. Rails and guide pipes occupy the rear half only.
            for (var s = -1; s <= 1; s += 2)
            {
                plumbing.Pipe(c+new Vector3(s*.8f,.45f,-.9f),Vector3.up,.16f,4.3f,8);
                for (var y = 1f; y <= 4; y += 1.5f)
                    plumbing.Pipe(c+new Vector3(s*.8f,y,-.9f),Vector3.up,.25f,.1f,8);
                fittings.Beam(c+new Vector3(s*1.1f,.2f,-1.25f),c+new Vector3(s*1.1f,4.5f,-1.25f),.07f);
                plumbing.Beam(c+new Vector3(s*1.9f,4.5f,-.6f),c+new Vector3(s*1.9f,5.4f,-.6f),.06f);
            }
            for (var y = .4f; y < 4.4f; y += .4f)
                fittings.Beam(c+new Vector3(-1.1f,y,-1.25f),c+new Vector3(1.1f,y,-1.25f),.045f);
            plumbing.Beam(c+new Vector3(-1.9f,5.4f,-.6f),c+new Vector3(1.9f,5.4f,-.6f),.065f)
                .Box(c+new Vector3(0,4.58f,-1.65f),new Vector3(2.4f,.13f,.8f));
            // (a spoked valve handwheel stood here: a ring with a cross in it, which at any distance read as a crosshair glyph drawn on the
            // picture. The valve keeps its stub and a plain cap.)
            plumbing.Pipe(new Vector3(4.35f,.9f,1.5f),Vector3.forward,.12f,.5f,8);
            fittings.Box(new Vector3(4.35f,.9f,2.02f),new Vector3(.3f,.3f,.06f));
            r.Part("WellPipework",plumbing,m.Steel,Vector3.zero);
            r.Part("CabinetLouvresAndValve",fittings,m.DarkSteel,Vector3.zero);
            var outZ = wallZ - r.Root.localPosition.z;
            r.Part("DischargePipe", new MeshKit().Rod(new Vector3(0, -0.6f, -0.4f), new Vector3(0, -0.6f, outZ), 0.4f), m.DarkSteel, Vector3.zero);
            r.Part("Outfall", new MeshKit().Pipe(Vector3.zero, Vector3.forward, 0.55f, 1.3f, 20), m.Steel, new Vector3(0, -1.0f, outZ));
            var flap = r.Add("OutfallFlap", new Vector3(0, -0.43f, outZ + 1.31f));
            new MeshKit().Pipe(new Vector3(0, -0.55f, 0), Vector3.forward, 0.52f, 0.06f, 20).Spawn("HingedFlap", flap, m.DarkSteel);
            r.Part("OutfallJet", new MeshKit().Pipe(Vector3.zero, new Vector3(0, -0.18f, 1), 0.28f, 1, 10), m.Jet, new Vector3(0, -1, outZ + 1.4f), false);
            // the alarm pin stands over the pump house's ridge
            r["Beacon"].localPosition = new Vector3(-2, 6.5f, -1);
            r["CardAnchor"].localPosition = new Vector3(8, 2.2f, 6);
            r.MergeFixed("ConcreteWell", "WellFloor");
            r.MergeFixed("ControlCabinet", "Motor", "WellPipework", "Outfall");
            r.MergeFixed("Door", "DischargePipe", "CabinetLouvresAndValve");
            return r;
        }
        /// <summary>
        /// The attribute rule's tag hangs this far under its own ring. The rule's level is the tenant's (it moves in the day) and stands at
        /// or near one of the three fixed levels, whose tags are written at their rings: at the same height the two tags were one smear.
        /// </summary>
        public const float AttributeTagDropM = .8f;
        static void Marker(AssetRig r, HarborMaterials m, string name, float level, Material material, string text, float tagX, Color tagColour, float tagY = 0, bool engineer = false)
        {
            var t = r.Part(name, new MeshKit().Ring(Vector3.zero, 2.24f, 0.06f), material, new Vector3((float)WellFootprint.LocalX, WellFloor + level, (float)WellFootprint.LocalZ));
            AssetRig.Tag(text, t, new Vector3(tagX, 0.08f + tagY, 2.5f), 0.10f, tagColour, engineer);
        }
    }
}
