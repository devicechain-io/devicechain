// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Marks a near-detail root, so the scene-budget log and the HOME mask can tell it from the far level.</summary>
    public sealed class NearDetailRoot : MonoBehaviour { }

    /// <summary>Resolves a near part's role (paint, trim, brick, glass...) to a display colour against one building's or vehicle's own colours.</summary>
    internal sealed class NearPalette
    {
        readonly Color[] table = new Color[Enum.GetValues(typeof(NearTone)).Length];

        public NearPalette(Color paint, Color roof, Color accent, Color hull)
        {
            Color Mix(Color a, Color b, float t) { var c = Color.Lerp(a, b, t); c.a = 1; return c; }
            Color Dim(Color a, float k) => new Color(a.r * k, a.g * k, a.b * k, 1);
            var white = new Color(.93f, .91f, .85f);
            void Set(NearTone t, Color c) { c.a = 1; table[(int)t] = c; }
            Set(NearTone.Paint, paint); Set(NearTone.PaintDark, Dim(paint, .74f)); Set(NearTone.PaintLight, Mix(paint, white, .3f));
            Set(NearTone.Trim, new Color(.90f, .89f, .85f)); Set(NearTone.TrimDark, new Color(.68f, .67f, .62f));
            Set(NearTone.Stone, new Color(.56f, .54f, .50f)); Set(NearTone.StoneDark, new Color(.40f, .38f, .36f));
            Set(NearTone.Brick, new Color(.50f, .27f, .20f)); Set(NearTone.BrickLight, new Color(.64f, .40f, .31f)); Set(NearTone.Mortar, new Color(.34f, .30f, .27f));
            Set(NearTone.Wood, new Color(.38f, .27f, .18f)); Set(NearTone.WoodDark, new Color(.23f, .16f, .11f)); Set(NearTone.WoodLight, new Color(.58f, .44f, .29f));
            Set(NearTone.Roof, roof); Set(NearTone.RoofDark, Dim(roof, .68f));
            Set(NearTone.Accent, accent); Set(NearTone.AccentLight, Mix(accent, white, .72f));
            Set(NearTone.Door, new Color(.15f, .17f, .20f)); Set(NearTone.DoorPanel, new Color(.26f, .28f, .31f));
            Set(NearTone.Metal, new Color(.47f, .49f, .52f)); Set(NearTone.MetalDark, new Color(.21f, .22f, .24f)); Set(NearTone.Black, new Color(.07f, .07f, .08f));
            Set(NearTone.Glass, new Color(.12f, .17f, .21f));
            Set(NearTone.Foliage, new Color(.20f, .33f, .15f)); Set(NearTone.FoliageDark, new Color(.13f, .24f, .12f));
            Set(NearTone.Red, new Color(.66f, .12f, .08f)); Set(NearTone.Yellow, new Color(.92f, .73f, .12f)); Set(NearTone.Orange, new Color(.90f, .42f, .08f));
            Set(NearTone.Cream, new Color(.90f, .86f, .74f)); Set(NearTone.Rope, new Color(.62f, .52f, .36f));
            Set(NearTone.Hull, hull); Set(NearTone.HullDark, Dim(hull, .7f));
            Set(NearTone.Window, new Color(.1f, .12f, .15f));
            Set(NearTone.GlassSky, new Color(.36f, .46f, .55f)); Set(NearTone.GlassGlint, new Color(.52f, .61f, .68f));
        }

        public Color Of(NearTone tone) => table[(int)tone];

        static readonly Color[] Doors = { new Color(.16f, .30f, .22f), new Color(.17f, .25f, .40f), new Color(.45f, .16f, .13f), new Color(.62f, .46f, .20f) };

        /// <summary>A house's door and shutter colour, chosen from its position (not from the town's seeded streams).</summary>
        public static Color DoorAccent(Vector3 at) => Doors[Mathf.Min(Doors.Length - 1, (int)(NearHash.Unit(at.x, at.z, 31) * Doors.Length))];

        public static NearPalette Building(Color paint, Color roof, Color accent) => new NearPalette(paint, roof, accent, paint);
        public static NearPalette Vehicle(Color paint) => new NearPalette(paint, paint, paint, paint);
    }

    /// <summary>Turns a <see cref="NearPlan"/> into meshes: one vertex-coloured renderer for everything, and one per window material for near-only windows.</summary>
    internal static class NearMeshes
    {
        static Vector3 V(NearVec v) => new Vector3(v.X, v.Y, v.Z);

        /// <summary>
        /// Builds the set under <paramref name="parent"/> at the given place, inactive. All of it is small trim over the far level's body, so it
        /// casts no shadow; the renderers are the one vertex-coloured scenery draw and, only where the plan has near-only windows, up to three more.
        /// </summary>
        public static GameObject Spawn(NearPlan plan, Transform parent, Vector3 localPosition, Quaternion localRotation, NearPalette palette, Material scenery,
            Func<Vector3, int, Material> windowMaterial)
        {
            var root = new GameObject("Near detail " + plan.Name);
            root.AddComponent<NearDetailRoot>();
            root.transform.SetParent(parent, false);
            root.transform.localPosition = localPosition; root.transform.localRotation = localRotation;
            var mesh = new SceneryMesh();
            Dictionary<Material, MeshKit> windows = null;
            foreach (var p in plan.Parts)
            {
                if (p.Tone == NearTone.Window)
                {
                    if (windowMaterial == null) continue;
                    windows = windows ?? new Dictionary<Material, MeshKit>();
                    var mat = windowMaterial(root.transform.TransformPoint(V(p.A)), p.Seed);
                    if (!windows.TryGetValue(mat, out var kit)) windows[mat] = kit = new MeshKit();
                    kit.Box(V(p.A), V(p.B), p.Yaw);
                    continue;
                }
                var c = palette.Of(p.Tone);
                switch (p.Shape)
                {
                    case NearShape.Box: mesh.Box(V(p.A), V(p.B), c, Quaternion.Euler(0, p.Yaw, 0)); break;
                    case NearShape.Beam: mesh.Beam(V(p.A), V(p.B), p.R, p.C.X, c); break;
                    case NearShape.Cylinder: mesh.Cylinder(V(p.A), V(p.B), p.R, p.C.X, (int)p.C.Y, c); break;
                    case NearShape.Gable: mesh.Gable(V(p.A), p.B.X, p.B.Z, p.B.Y, p.Yaw, c); break;
                    default: mesh.Facing(V(p.A), V(p.B), V(p.C), V(p.D), V(p.N), c); break;
                }
            }
            if (mesh.VertexCount > 0) mesh.Spawn("Detail", root.transform, scenery);
            if (windows != null)
                foreach (var pair in windows) pair.Value.Spawn("Windows " + pair.Key.name.Replace("Harbor/", ""), root.transform, pair.Key, false);
            root.SetActive(false);
            return root;
        }
    }
}
