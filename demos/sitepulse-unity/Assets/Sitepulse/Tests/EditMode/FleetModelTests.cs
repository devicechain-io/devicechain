// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEditor;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// The imported models keep the conventions every rig rule depends on: no root rotation,
    /// +Z forward, identity pivots at rest (aimed parts excepted: they rest pointing at their
    /// target), and the named nodes the rigs drive.
    /// </summary>
    public class FleetModelTests
    {
        const string Models = "Assets/Sitepulse/Art/Models/";

        // name, the node that must sit ahead of the origin, then every pivot the rig drives
        static readonly object[] Cases =
        {
            new object[] { "dozer", "Blade", new[] { "BladeArmPivot", "Blade", "Ripper", "RipperCarriage", "LeftSprocket", "RightSprocket", "LeftIdler", "RightIdler", "LeftTrack", "RightTrack" } },
            new object[] { "loader", "Bucket", new[] { "FrontFrame", "Boom", "Bucket", "Bellcrank", "BucketLink", "WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight" } },
            new object[] { "hauler", "Cab", new[] { "DumpBody", "Load", "SteerFrontLeft", "SteerFrontRight", "WheelFrontLeft", "WheelFrontRight", "WheelRearLeft", "WheelRearRight" } },
        };

        static GameObject Load(string file)
        {
            var go = AssetDatabase.LoadAssetAtPath<GameObject>(Models + file);
            Assert.IsNotNull(go, $"{Models}{file} did not import as a model");
            return go;
        }

        [TestCaseSource(nameof(Cases))]
        public void ModelKeepsTheRigConventions(string name, string frontNode, string[] pivots)
        {
            foreach (var file in new[] { name + ".glb", name + "_LOD1.glb" })
            {
                var root = Load(file).transform;
                Assert.That(Quaternion.Angle(root.localRotation, Quaternion.identity), Is.LessThan(0.01f), $"{file}: root is rotated");
                Assert.That(root.localScale, Is.EqualTo(Vector3.one), $"{file}: root is scaled");

                foreach (var p in pivots)
                {
                    var t = FleetRig.Find(root, p);
                    Assert.IsNotNull(t, $"{file}: no pivot named {p}");
                    // an aimed part rests pointing at its target, so its rest rotation is not identity
                    if (FleetRig.AimTarget(p) != null) continue;
                    Assert.That(Quaternion.Angle(t.localRotation, Quaternion.identity), Is.LessThan(0.01f), $"{file}: pivot {p} is not at rest");
                }

                var front = FleetRig.Find(root, frontNode);
                Assert.That(root.InverseTransformPoint(front.position).z, Is.GreaterThan(0.5f), $"{file}: {frontNode} is not ahead of the origin, so the model does not face +Z");
            }
        }

        [TestCaseSource(nameof(Cases))]
        public void LodLevelsShareOneRig(string name, string frontNode, string[] pivots)
        {
            var merged = FleetRig.BuildMergedLod(Load(name + ".glb"), Load(name + "_LOD1.glb"), name, out var error);
            try
            {
                Assert.IsNotNull(merged, error);
                var lods = merged.GetComponent<LODGroup>().GetLODs();
                Assert.That(lods.Length, Is.EqualTo(2));
                Assert.That(lods[0].renderers, Is.Not.Empty);
                Assert.That(lods[1].renderers, Is.Not.Empty);
                foreach (var r in lods[1].renderers)
                    Assert.That(r.name, Does.EndWith("_LOD1"), "a LOD0 mesh ended up in the LOD1 level");
            }
            finally
            {
                if (merged != null) Object.DestroyImmediate(merged);
            }
        }

        [Test]
        public void HaulerHidesItsLoadAsTheBodyRisesAndShowsItOnlyWhenDown()
        {
            var merged = FleetRig.BuildMergedLod(Load("hauler.glb"), Load("hauler_LOD1.glb"), "hauler", out var error);
            try
            {
                Assert.IsNotNull(merged, error);
                var rig = merged.AddComponent<MachineRig>();
                rig.Bind(MachineKind.Hauler);
                var load = FleetRig.Find(merged.transform, "Load").gameObject;

                rig.dump = 0f; rig.Apply();
                Assert.IsTrue(load.activeSelf, "load hidden at rest");
                rig.dump = 3f; rig.Apply();
                Assert.IsFalse(load.activeSelf, "load still shown with the body 3 degrees up");
                rig.dump = 1f; rig.Apply();
                Assert.IsFalse(load.activeSelf, "load came back before the body was down");
                rig.dump = 0f; rig.Apply();
                Assert.IsTrue(load.activeSelf, "load did not come back with the body down");
                rig.loaded = false; rig.Apply();
                Assert.IsFalse(load.activeSelf, "an empty hauler shows a load");
            }
            finally
            {
                if (merged != null) Object.DestroyImmediate(merged);
            }
        }

        [Test]
        public void HydraulicsFollowTheirMounts()
        {
            var merged = FleetRig.BuildMergedLod(Load("loader.glb"), Load("loader_LOD1.glb"), "loader", out var error);
            try
            {
                Assert.IsNotNull(merged, error);
                var rig = merged.AddComponent<MachineRig>();
                rig.Bind(MachineKind.Loader);
                rig.boom = FleetRig.LoaderBoomMaxLift; rig.bucket = 24.69f; rig.Apply();
                var pairs = FleetRig.AimPairs(merged.transform);
                Assert.That(pairs.Count, Is.GreaterThanOrEqualTo(9), "aimed parts missing");
                foreach (var (part, target) in pairs)
                {
                    var toTarget = (target.position - part.position).normalized;
                    Assert.That(Vector3.Angle(part.up, toTarget), Is.LessThan(0.5f), $"{part.name} does not point at {target.name}");
                }
            }
            finally
            {
                if (merged != null) Object.DestroyImmediate(merged);
            }
        }
    }
}
