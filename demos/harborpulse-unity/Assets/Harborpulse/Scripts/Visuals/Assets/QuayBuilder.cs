// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class QuayBuilder
    {
        public static void Build(DeviceChain.Harborpulse.Model.Director.PresentationMap map, Transform root, HarborMaterials m)
        {
            foreach (var token in map.Quays.Keys)
            {
                var frame = new QuayFrame(map, token);
                var q = new { Token = token, XMin = -frame.Length / 2, XMax = frame.Length / 2, CenterX = 0f };
                var t = frame.Parent(root, token);
                var wall = 0f; var deck = (float)SitePlan.DeckHeightM;
                var kits = new MeshKit[6]; for (var i = 0; i < kits.Length; i++) kits[i] = new MeshKit();
                for (var row = 0; row < 18; row++)
                {
                    var y = -6.5f + row * 0.52f;
                    for (var x = (float)q.XMin; x < q.XMax; x += 3.2f)
                    {
                        var i = (row + (int)Math.Abs(x / 3.2f)) % (q.Token == "hp-east-quay" ? 3 : 6);
                        kits[i].Box(new Vector3(x + 1.56f, y, wall + 0.04f), new Vector3(3.10f, 0.48f, 0.35f));
                    }
                }
                for (var i = 0; i < kits.Length; i++) kits[i].Spawn("StoneCourse" + i, t, m.Courses[i]);
                var coping = new MeshKit().Slab((float)q.XMin, (float)q.XMax, wall - 1.8f, wall + 0.2f, deck + 0.35f, deck + 0.55f);
                coping.Spawn("Coping", t, m.Coping);
                var steel = new MeshKit(); var timber = new MeshKit();
                for (var x = (float)q.XMin + 12; x < q.XMax - 6; x += 28)
                {
                    steel.Cylinder(new Vector3(x, deck + 0.5f, wall - 0.9f), 0.28f, 0.65f, 10, 0.35f);
                    timber.Box(new Vector3(x + 14, deck - 1.6f, wall + 0.35f), new Vector3(0.45f, 4.6f, 0.4f));
                }
                var jx = (float)q.CenterX + 30; var end = wall + (float)18;
                timber.Slab(jx - 2.2f, jx + 2.2f, wall, end + 2, deck - 0.1f, deck + 0.15f);
                for (var z = wall + 6; z < end + 2; z += 7)
                    for (var s = -1; s <= 1; s += 2) timber.Cylinder(new Vector3(jx + s * 1.9f, -6.5f, z), 0.22f, deck + 7.4f, 8);
                for (var s = -1; s <= 1; s += 2)
                {
                    for (var z = wall + 2; z < end; z += 4) steel.Cylinder(new Vector3(jx + s * 2.1f, deck, z), 0.04f, 1.1f, 6);
                    steel.Rod(new Vector3(jx + s * 2.1f, deck + 1.1f, wall), new Vector3(jx + s * 2.1f, deck + 1.1f, end));
                }
                foreach (var edge in new[] { (float)q.XMin, (float)q.XMax - 35 })
                {
                    for (var x = edge; x <= edge + 35; x += 5) steel.Cylinder(new Vector3(x, deck + 0.55f, wall - 0.8f), 0.04f, 1.0f, 6);
                    steel.Rod(new Vector3(edge, deck + 1.55f, wall - 0.8f), new Vector3(edge + 35, deck + 1.55f, wall - 0.8f));
                }
                var lx = (float)q.CenterX - 15;
                for (var s = -1; s <= 1; s += 2) steel.Rod(new Vector3(lx + s * 0.35f, -2, wall + 0.6f), new Vector3(lx + s * 0.35f, deck + 1, wall + 0.6f), 0.035f);
                for (var y = -2f; y < deck + 0.9f; y += 0.3f) steel.Rod(new Vector3(lx - 0.35f, y, wall + 0.6f), new Vector3(lx + 0.35f, y, wall + 0.6f), 0.025f);
                var steps = new MeshKit();
                for (var i = 0; i < 12; i++) steps.Box(new Vector3((float)q.CenterX - 35, deck - i * 0.25f, wall + i * 0.5f), new Vector3(3, 0.25f, 0.55f));
                steps.Spawn("WaterSteps", t, m.Coping);
                if (q.Token == "hp-basin")
                {
                    // Enclose the dock with two arms; the opening remains navigable.
                    foreach (var x in new[] { (float)q.XMin, (float)q.XMax })
                    {
                        new MeshKit().Box(new Vector3(x, -1, wall + 12), new Vector3(3, 7, 24)).Spawn("DockArm", t, m.Stone);
                        for (var z = wall + 3; z < wall + 24; z += 5) steel.Cylinder(new Vector3(x, deck, z), 0.05f, 1.1f, 6);
                        steel.Rod(new Vector3(x, deck + 1.1f, wall), new Vector3(x, deck + 1.1f, wall + 24));
                    }
                }
                steel.Spawn("BollardsLaddersRailings", t, m.DarkSteel); timber.Spawn("JettyFenders", t, m.Timber);
            }
        }
    }
}
