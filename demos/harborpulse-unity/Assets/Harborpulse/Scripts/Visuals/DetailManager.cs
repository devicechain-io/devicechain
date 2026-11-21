// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The level-of-detail switch for the town's buildings and vehicles: one manager that owns every near-detail set and, every few frames or
    /// when the camera has moved a metre, shows the ones nearest the active camera (enter 110 m, leave 130 m, at most 25 at once).
    ///
    /// Why a manager and not LODGroup: the far level is merged per tile and material, so there is no per-building renderer for a LODGroup to
    /// switch, and breaking the merge up would add hundreds of renderers at HOME. The near level is instead ADDITIVE: it draws only the trim
    /// the far level leaves out (frames, sashes, rails, wheels, gear), over the same walls, roof and flat window panes, which stay drawn at
    /// every distance. So nothing has to be hidden while a set is shown, no hole can open, nothing pops but the added detail, the night's
    /// lit windows are the far level's own in both levels, and HOME draws exactly the far level (every set inactive).
    ///
    /// The decision itself is <see cref="DetailSwitch"/> in the model (hysteresis, cap, no allocation), with its own tests. A set is built the
    /// first time it is wanted, or at load when a story camera can reach it (<see cref="Prebuild"/>), and kept inactive when it is not near.
    /// </summary>
    public sealed class DetailManager
    {
        sealed class Entry
        {
            public string Name;
            public NearPlan Plan;
            public Transform Parent;
            public Vector3 LocalPosition;
            public Quaternion LocalRotation;
            internal NearPalette Palette;
            public Material Scenery;
            public Func<Vector3, int, Material> Windows;
            public bool Dynamic;
            public Vector3 LocalCentre;
            public GameObject Root;
            public bool Shown;
            /// <summary>The far level's own renderers this set replaces (a vehicle's body boxes): off while the set is shown.</summary>
            public Renderer[] Hide;
        }

        readonly List<Entry> entries = new List<Entry>();
        readonly List<int> dynamic = new List<int>();
        readonly DetailSwitch sw = new DetailSwitch(256);
        long editFrame;

        public int Count => entries.Count;
        public int NearCount => sw.NearCount;

        /// <summary>How many sets are built (inactive ones included) and the vertices in them.</summary>
        public void Stats(out int built, out int shown, out long vertices, out long shownVertices)
        {
            built = shown = 0; vertices = shownVertices = 0;
            foreach (var e in entries)
            {
                if (e.Root == null) continue;
                built++; vertices += e.Plan.Vertices;
                if (e.Shown) { shown++; shownVertices += e.Plan.Vertices; }
            }
        }

        /// <summary>
        /// Registers a set whose plan is in the frame of <paramref name="parent"/> placed at <paramref name="localPosition"/> and
        /// <paramref name="localRotation"/>. A <paramref name="dynamic"/> set (the truck, a charger's car) follows its parent every refresh and is
        /// ignored while the parent is inactive; the rest are fixed where they were registered.
        /// </summary>
        internal int Register(NearPlan plan, Transform parent, Vector3 localPosition, Quaternion localRotation, NearPalette palette, Material scenery,
            Func<Vector3, int, Material> windows = null, bool dynamic = false, Renderer[] hide = null)
        {
            plan.Sphere(out var x, out var y, out var z, out var radius);
            var local = localPosition + localRotation * new Vector3(x, y, z);
            var world = parent.TransformPoint(local);
            var index = sw.Add(world.x, world.y, world.z, radius);
            entries.Add(new Entry
            {
                Name = plan.Name, Plan = plan, Parent = parent, LocalPosition = localPosition, LocalRotation = localRotation, Palette = palette, Scenery = scenery,
                Windows = windows, Dynamic = dynamic, LocalCentre = local, Hide = hide,
            });
            if (dynamic) this.dynamic.Add(index);
            return index;
        }

        /// <summary>
        /// Builds, inactive, every set a story camera could bring near, so a cut to a shot does not hitch on it. <paramref name="cameras"/> and
        /// <paramref name="looks"/> are the authored poses' camera and look points (see <see cref="DetailCandidates"/>).
        /// </summary>
        public void Prebuild(IReadOnlyList<NearVec> cameras, IReadOnlyList<NearVec> looks)
        {
            for (var i = 0; i < entries.Count; i++)
            {
                var e = entries[i];
                if (e.Root != null || e.Parent == null) continue;
                var p = e.Parent.TransformPoint(e.LocalCentre);
                e.Plan.Sphere(out _, out _, out _, out var r);
                if (DetailCandidates.Reachable(p.x, p.y, p.z, r, cameras, looks)) Build(e);
            }
        }

        static void SetFar(Entry e, bool on)
        {
            if (e.Hide == null) return;
            foreach (var r in e.Hide) if (r != null) r.enabled = on;
        }

        static void Build(Entry e)
        {
            e.Root = NearMeshes.Spawn(e.Plan, e.Parent, e.LocalPosition, e.LocalRotation, e.Palette, e.Scenery, e.Windows);
        }

        /// <summary>
        /// Retakes the decision for a camera at <paramref name="camera"/>. The app calls it every frame (it returns at once unless the camera
        /// moved a metre or a few frames passed) and the stills path calls it forced, after it has placed the camera, so a still shows the
        /// level that camera would have.
        /// </summary>
        public void Refresh(Vector3 camera, bool force = false)
        {
            if (entries.Count == 0) return;
            var frame = Application.isPlaying ? Time.frameCount : ++editFrame;
            for (var k = 0; k < dynamic.Count; k++)
            {
                var i = dynamic[k]; var e = entries[i];
                if (e.Parent == null) { sw.SetActive(i, false); continue; }
                var w = e.Parent.TransformPoint(e.LocalCentre);
                sw.Move(i, w.x, w.y, w.z);
                sw.SetActive(i, e.Parent.gameObject.activeInHierarchy);
            }
            if (!force && !sw.Due(frame, camera.x, camera.y, camera.z)) return;
            sw.Update(frame, camera.x, camera.y, camera.z, force);
            for (var i = 0; i < entries.Count; i++)
            {
                var e = entries[i]; var want = sw.IsNear(i);
                if (want == e.Shown) continue;
                if (want && e.Root == null) Build(e);
                if (e.Root != null) e.Root.SetActive(want);
                SetFar(e, !want);
                e.Shown = want;
            }
        }

        /// <summary>Hides every set (the next refresh shows what is near again).</summary>
        public void HideAll()
        {
            foreach (var e in entries)
            {
                if (e.Root != null) e.Root.SetActive(false);
                SetFar(e, true);
                e.Shown = false;
            }
        }
    }
}
