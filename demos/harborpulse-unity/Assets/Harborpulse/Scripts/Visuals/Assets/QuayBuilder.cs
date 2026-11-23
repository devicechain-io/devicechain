// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class QuayBuilder
    {
        public static void Build(DeviceChain.Harborpulse.Model.Director.PresentationMap map, Transform root, HarborMaterials m, WorldPropPlan plan)
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
                    for (var x = (float)q.XMin - (row % 2) * 1.6f; x < q.XMax; x += 3.2f)
                    {
                        var i = (row + (int)Math.Abs(x / 3.2f)) % (q.Token == "hp-east-quay" ? 3 : 6);
                        var lo = Mathf.Max(x, q.XMin); var hi = Mathf.Min(x + 3.1f, q.XMax);
                        if (hi > lo) kits[i].Ashlar(new Vector3((lo+hi)*.5f, y, wall + .015f), hi-lo, .48f,
                            .07f + ((row * 7 + i) % 3) * .025f);
                    }
                }
                for (var i = 0; i < kits.Length; i++) kits[i].Spawn("StoneCourse" + i, t, m.Courses[i]);
                var coping = new MeshKit().Slab((float)q.XMin, (float)q.XMax, wall - 1.8f, wall + 0.2f, deck + 0.35f, deck + 0.55f);
                var copingObject=coping.Spawn("Coping", t, m.Surfaces);
                SurfaceDetail.Colour(copingObject,new Color(.62f,.60f,.55f,.4f));
                var steel = new MeshKit(); var timber = new MeshKit();
                var paint = new SceneryMesh();
                for (var x = (float)q.XMin + 12; x < q.XMax - 6; x += 28)
                {
                    timber.Box(new Vector3(x + 14, deck - 1.6f, wall + 0.35f), new Vector3(0.45f, 4.6f, 0.4f));
                    // Tyre fenders and rope coils share the existing two material batches.
                    steel.Hoop(new Vector3(x+14,deck-.4f,wall+.66f),Vector3.forward,.42f,.13f);
                    steel.Beam(new Vector3(x+14,deck+.35f,wall+.45f),new Vector3(x+14,deck,wall+.65f),.045f);
                    if(plan.Find("bollard:"+token+":"+x)!=null)timber.Hoop(new Vector3(x+1.2f,deck+.59f,wall-1),Vector3.up,.39f,.07f)
                        .Hoop(new Vector3(x+1.2f,deck+.59f,wall-1),Vector3.up,.26f,.07f);
                }
                foreach(var prop in plan.Props)
                {
                    if(prop.Area!=token || (prop.Kind!="Bollard" && prop.Kind!="Cleat"))continue;
                    var at=frame.Local(new Vector3((float)prop.At.X,0,(float)prop.At.Z));var x=at.x;var z=at.z;
                    if(prop.Kind=="Bollard")
                    {
                        steel.Box(new Vector3(x,deck+.58f,z),new Vector3(.9f,.16f,.75f))
                            .Cylinder(new Vector3(x,deck+.6f,z),.28f,.55f,8,.35f)
                            .Beam(new Vector3(x-.5f,deck+1,z),new Vector3(x+.5f,deck+1,z),.22f);
                        paint.Branch(new Vector3(x,deck+.85f,z),new Vector3(x,deck+1.05f,z),.34f,.35f,new Color(.61f,.49f,.27f),8);
                    }
                    else
                    {
                        steel.Box(new Vector3(x,deck+.6f,z),new Vector3(.7f,.1f,.45f))
                            .Beam(new Vector3(x-.55f,deck+.85f,z),new Vector3(x+.55f,deck+.85f,z),.16f);
                        paint.Branch(new Vector3(x-.35f,deck+.85f,z),new Vector3(x+.35f,deck+.85f,z),.11f,.11f,new Color(.53f,.46f,.30f),6);
                    }
                }
                paint.Spawn("Worn bollard paint",t,m.Surfaces);
                var jx = (float)q.CenterX + 30; var end = wall + (float)18;
                for (var z = wall; z < end + 2; z += .65f)
                    timber.Slab(jx - 2.2f, jx + 2.2f, z, Mathf.Min(z+.60f,end+2), deck-.1f, deck+.15f);
                for (var z = wall + 6; z < end + 2; z += 7)
                    for (var s = -1; s <= 1; s += 2) timber.Cylinder(new Vector3(jx + s * 1.9f, -6.5f, z), 0.22f, deck + 7.4f, 8);
                for (var z = wall+6; z < end-5; z += 7)
                    for (var s = -1; s <= 1; s += 2)
                    {
                        timber.Beam(new Vector3(jx+s*1.9f,-1.8f,z),new Vector3(jx+s*1.9f,deck-.3f,z+7),.18f)
                            .Beam(new Vector3(jx+s*1.9f,deck-.3f,z),new Vector3(jx+s*1.9f,-1.8f,z+7),.18f);
                    }
                for (var s = -1; s <= 1; s += 2)
                {
                    for (var z = wall + 2; z < end; z += 4) steel.Cylinder(new Vector3(jx + s * 2.1f, deck, z), 0.04f, 1.1f, 6);
                    steel.Rod(new Vector3(jx + s * 2.1f, deck + 1.1f, wall), new Vector3(jx + s * 2.1f, deck + 1.1f, end));
                }
                // A ladder head on this wall (QuayFittings) opens the railing: the rail stops at a post either side of it. With no ladder
                // the railing is the one run it always was.
                const float opening = (float)QuayFittings.RailOpeningHalfM;
                var ladders = plan.Fittings.Landings.Where(l => l.Quay == token)
                    .Select(l => frame.Local(new Vector3((float)l.Ladder.X, 0, (float)l.Ladder.Z)).x).OrderBy(x => x).ToArray();
                foreach (var edge in new[] { (float)q.XMin, (float)q.XMax - 35 })
                {
                    for (var x = edge; x <= edge + 35; x += 5)
                        if (!ladders.Any(l => Mathf.Abs(x - l) < opening)) steel.Cylinder(new Vector3(x, deck + 0.55f, wall - 0.8f), 0.04f, 1.0f, 6);
                    var from = edge;
                    foreach (var l in ladders.Where(l => l - opening > edge && l + opening < edge + 35))
                    {
                        steel.Rod(new Vector3(from, deck + 1.55f, wall - 0.8f), new Vector3(l - opening, deck + 1.55f, wall - 0.8f));
                        foreach (var x in new[] { l - opening, l + opening }) steel.Cylinder(new Vector3(x, deck + 0.55f, wall - 0.8f), 0.04f, 1.0f, 6);
                        from = l + opening;
                    }
                    steel.Rod(new Vector3(from, deck + 1.55f, wall - 0.8f), new Vector3(edge + 35, deck + 1.55f, wall - 0.8f));
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
                AssetRig.MergeFixed(t,t.GetComponentsInChildren<MeshFilter>());
            }
        }
    }
}
