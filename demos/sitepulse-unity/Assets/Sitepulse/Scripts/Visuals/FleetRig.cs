// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>The three machine models the art kit builds.</summary>
    public enum MachineKind
    {
        Dozer,
        Loader,
        Hauler,
    }

    /// <summary>
    /// Rig rules for the Sitepulse machine models, kept in one place so that every caller poses
    /// a machine the same way.
    ///
    /// The models come from the code in <c>ArtSource/</c>. Each moving part is a node at its
    /// pivot with identity rotation at rest, so a pose is an absolute local Euler angle on that
    /// node. The numbers below are the ones each machine's builder documents in its header
    /// (<c>ArtSource/machines/*.py</c>); change them there first and rebuild the models.
    ///
    /// glTF carries no constraints, so the hydraulic cylinders and rods are re-aimed in code
    /// after every pose (<see cref="AimAll"/>).
    /// </summary>
    public static class FleetRig
    {
        // ---- dozer
        /// <summary>BladeArmPivot.x: negative raises the blade.</summary>
        public const float DozerBladeRaised = -20.13f;
        /// <summary>BladeArmPivot.x: positive digs.</summary>
        public const float DozerBladeDig = 12.37f;
        /// <summary>Blade.x = -BladePitchComp * BladeArmPivot.x keeps the moldboard near its rest attitude.</summary>
        public const float BladePitchComp = 0.85f;
        /// <summary>Ripper.x: 0 is stowed, negative lowers the shank.</summary>
        public const float DozerRipperLowered = -57.9f;
        /// <summary>Sprocket and idler radius in metres (2.7778 rad per metre of travel).</summary>
        public const float DozerSprocketRadius = 0.36f;
        /// <summary>Track texture U per metre of travel: the belt's top run moves with the ground speed.</summary>
        public const float DozerTreadUPerMetre = 4.96715f;

        // ---- loader
        /// <summary>Boom.x at full lift; 0 puts the bucket on the ground, -6.92 is carry.</summary>
        public const float LoaderBoomMaxLift = -84.69f;
        public const float LoaderBoomCarry = -6.92f;
        /// <summary>Bucket.x relative to the boom: negative racks back, positive dumps.</summary>
        public const float LoaderBucketRackBack = -40f;
        public const float LoaderBucketCarry = -38.08f;
        public const float LoaderBucketDump = 136.69f;
        /// <summary>FrontFrame.y articulation limit; positive turns right.</summary>
        public const float LoaderArticulationLimit = 40f;
        public const float LoaderWheelRadius = 0.765f;

        // ---- hauler
        /// <summary>Dump body raise in degrees; the node angle is DumpBody.x = -raise.</summary>
        public const float HaulerDumpMax = 50f;
        /// <summary>SteerFront*.y limit; positive steers right.</summary>
        public const float HaulerSteerLimit = 35f;
        public const float HaulerWheelRadius = 1.06f;
        /// <summary>Load rule: hide the heap once DumpBody.x drops below this (signed degrees).</summary>
        public const float HaulerLoadHideBelow = -2f;
        /// <summary>Load rule: show it again once DumpBody.x is back at or above this.</summary>
        public const float HaulerLoadShowAtOrAbove = -0.05f;

        /// <summary>Depth-first search for a node by exact name, including inactive nodes.</summary>
        public static Transform Find(Transform root, string name)
        {
            if (root == null) return null;
            if (root.name == name) return root;
            for (int i = 0; i < root.childCount; i++)
            {
                var hit = Find(root.GetChild(i), name);
                if (hit != null) return hit;
            }
            return null;
        }

        /// <summary>
        /// The node an aimed part points at, or null if <paramref name="name"/> is not an aimed
        /// part. <c>&lt;P&gt;Cylinder&lt;S&gt;</c> aims at <c>&lt;P&gt;RodMount&lt;S&gt;</c>, and
        /// <c>&lt;P&gt;Rod&lt;S&gt;</c> at <c>&lt;P&gt;CylinderMount&lt;S&gt;</c>.
        /// </summary>
        public static string AimTarget(string name)
        {
            if (name.Contains("_LOD") || name.Contains("Mount")) return null;
            if (name == "BucketLink") return "BucketLinkPin";
            if (name == "RipperUpperLink") return "RipperUpperPin";
            int i = name.IndexOf("Cylinder", System.StringComparison.Ordinal);
            if (i >= 0) return name.Substring(0, i) + "RodMount" + name.Substring(i + "Cylinder".Length);
            i = name.IndexOf("Rod", System.StringComparison.Ordinal);
            if (i >= 0) return name.Substring(0, i) + "CylinderMount" + name.Substring(i + "Rod".Length);
            return null;
        }

        /// <summary>Every aimed part under <paramref name="root"/> with the node it points at.</summary>
        public static List<(Transform part, Transform target)> AimPairs(Transform root)
        {
            var byName = new Dictionary<string, Transform>();
            foreach (var t in root.GetComponentsInChildren<Transform>(true)) byName[t.name] = t;
            var pairs = new List<(Transform, Transform)>();
            foreach (var kv in byName)
            {
                var targetName = AimTarget(kv.Key);
                if (targetName != null && byName.TryGetValue(targetName, out var target)) pairs.Add((kv.Value, target));
            }
            return pairs;
        }

        /// <summary>Turn each part's local +Y toward its target with the smallest rotation, which keeps its roll.</summary>
        public static void AimAll(List<(Transform part, Transform target)> pairs)
        {
            foreach (var (part, target) in pairs)
            {
                var d = target.position - part.position;
                if (d.sqrMagnitude < 1e-8f) continue;
                part.rotation = Quaternion.FromToRotation(part.up, d.normalized) * part.rotation;
            }
        }

        /// <summary>
        /// Bellcrank.x as a function of Bucket.x for the loader's Z-bar linkage: the builder's
        /// quintic fit of the exact four-bar solution. Degrees in and out; the fit itself is in radians.
        /// </summary>
        public static float LoaderBellcrankDeg(float bucketDeg)
        {
            double b = bucketDeg * Mathf.Deg2Rad, sum = 0, p = 1;
            foreach (var c in ZBarFit) { sum += c * p; p *= b; }
            return (float)(sum * Mathf.Rad2Deg);
        }

        static readonly double[] ZBarFit = { -0.000906, -0.575332, -0.019968, 0.159874, -0.078366, 0.01321 };

        /// <summary>
        /// The hauler's load rule, with hysteresis: the heap hides in the first two degrees of a
        /// raise and returns only when the body is fully down. <paramref name="dumpBodyX"/> is the
        /// signed DumpBody local X angle (negative raises).
        /// </summary>
        public static bool HaulerLoadVisible(float dumpBodyX, bool wasVisible)
        {
            if (dumpBodyX < HaulerLoadHideBelow) return false;
            if (dumpBodyX >= HaulerLoadShowAtOrAbove) return true;
            return wasVisible;
        }

        /// <summary>
        /// Instantiate the LOD0 model and graft every LOD1 mesh under the same-named pivot, so one
        /// set of pivots animates both levels, then add a <see cref="LODGroup"/> over the two.
        /// glTF has no LOD groups, which is why the kit exports two files with matching trees.
        /// Returns null with a reason if a LOD1 mesh has no matching pivot.
        /// </summary>
        public static GameObject BuildMergedLod(GameObject lod0Prefab, GameObject lod1Prefab, string name,
                                                out string error, float lod0Height = 0.25f, float lod1Height = 0.04f)
        {
            error = null;
            var merged = Object.Instantiate(lod0Prefab);
            merged.name = name;
            var donor = Object.Instantiate(lod1Prefab);
            var lod1 = new List<Renderer>();
            foreach (var r in donor.GetComponentsInChildren<Renderer>(true))
            {
                var parent = r.transform.parent;
                var dst = parent == donor.transform ? merged.transform : Find(merged.transform, parent.name);
                if (dst == null)
                {
                    error = "no LOD0 pivot named " + parent.name;
                    break;
                }
                r.transform.SetParent(dst, false);
                lod1.Add(r);
            }
            Object.DestroyImmediate(donor);
            if (error != null)
            {
                Object.DestroyImmediate(merged);
                return null;
            }
            var lod0 = new List<Renderer>();
            foreach (var r in merged.GetComponentsInChildren<Renderer>(true))
                if (!lod1.Contains(r)) lod0.Add(r);
            var group = merged.AddComponent<LODGroup>();
            group.SetLODs(new[] { new LOD(lod0Height, lod0.ToArray()), new LOD(lod1Height, lod1.ToArray()) });
            group.RecalculateBounds();
            return merged;
        }
    }
}
