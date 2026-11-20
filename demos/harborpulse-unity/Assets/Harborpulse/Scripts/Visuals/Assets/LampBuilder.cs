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
            var column = new MeshKit().Cylinder(Vector3.zero, .23f, .85f, 8, .17f)
                .Cylinder(Vector3.up * .85f, .16f, 7.65f, 8, .085f)
                .Cylinder(Vector3.up * .75f, .21f, .12f, 8).Cylinder(Vector3.up * 7.95f, .13f, .16f, 8);
            for (var i = 0; i < 4; i++)
            {
                var a = i * Mathf.PI * .5f; var d = new Vector3(Mathf.Cos(a), 0, Mathf.Sin(a));
                column.Beam(d * .158f + Vector3.up * .9f, d * .10f + Vector3.up * 4.5f, .035f);
            }
            r.Part("Column", column, m.DarkSteel, Vector3.zero);
            var foot = new MeshKit().Box(new Vector3(0, .06f, 0), new Vector3(.58f, .12f, .58f))
                .Cylinder(Vector3.up * .12f, .28f, .16f, 8, .23f);
            for (var x = -1; x <= 1; x += 2) for (var z = -1; z <= 1; z += 2)
                foot.Cylinder(new Vector3(x * .22f, .12f, z * .22f), .035f, .055f, 4);
            r.Part("Base", foot, m.Steel, Vector3.zero);
            var bracket = new MeshKit().Beam(new Vector3(0, 8.4f, 0), new Vector3(0, 8.4f, 1.7f), .13f)
                .Beam(new Vector3(0, 7.8f, .04f), new Vector3(0, 8.4f, 1.25f), .07f);
            // Frame, roof and cap belong to the fixed bracket; only the glass changes with telemetry.
            bracket.Box(new Vector3(0, 8.02f, 1.7f), new Vector3(.64f, .10f, .86f))
                .Gable(new Vector3(0, 8.51f, 1.7f), .76f, 1, .23f)
                .Cylinder(new Vector3(0, 8.74f, 1.7f), .07f, .15f, 4, 0);
            for (var x = -1; x <= 1; x += 2) for (var z = -1; z <= 1; z += 2)
                bracket.Beam(new Vector3(x * .27f, 8.05f, 1.7f + z * .36f), new Vector3(x * .27f, 8.53f, 1.7f + z * .36f), .045f);
            r.Part("Bracket", bracket, m.DarkSteel, Vector3.zero);
            var head = r.Box("Lantern", new Vector3(0, 8.28f, 1.7f), new Vector3(.51f, .44f, .69f), m.LampUnknown);
            r.Part("Halo", new MeshKit().Ball(Vector3.zero, 1.6f), m.LampHalo, head.localPosition - Vector3.up * 0.15f, false);
            var light = AssetRig.Node("Light", head, Vector3.down * 0.3f).gameObject.AddComponent<Light>();
            light.type = LightType.Point; light.range = 42; light.color = new Color(1, 0.82f, 0.58f); light.shadows = LightShadows.None;
            r.Part("PlatformWindow", new MeshKit().Ring(Vector3.zero, 2.3f, 0.05f), m.StatusGreen, Vector3.up * 0.08f, false);
            r["Beacon"].localPosition = new Vector3(0, 9.1f, 1.7f);
            r["CardAnchor"].localPosition = new Vector3(0, 10, 1.7f);
            r.MergeFixed("Column", "Bracket");
            return r;
        }
    }
}
