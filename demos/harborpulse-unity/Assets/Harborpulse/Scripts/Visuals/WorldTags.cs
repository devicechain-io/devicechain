// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// Every tag written in the world (<see cref="AssetRig.Tag"/>), so two things can be said of them. An engineer's tag (a rule key,
    /// "(WORLD)", "(runner)": see <see cref="WorldText"/>) is drawn only while the engineer overlay is on, never in a presentation or
    /// a rendered cut. And a render's text audit lists the tags a frame really drew, the way it lists the HUD's text: a label that
    /// reaches a cut is then on the record, not found by eye in a still.
    /// </summary>
    public static class WorldTags
    {
        sealed class Entry { public TextMesh Text; public MeshRenderer Renderer; public bool Engineer; }
        static readonly List<Entry> entries = new List<Entry>();

        /// <summary>Whether engineer's tags are drawn. The composition root sets it from the engineer overlay every frame.</summary>
        public static bool Engineer { get; set; }

        public static void Clear() { entries.Clear(); Framed.Clear(); }

        internal static void Register(TextMesh text, MeshRenderer renderer, bool engineer)
        {
            entries.Add(new Entry { Text = text, Renderer = renderer, Engineer = engineer });
            if (engineer) renderer.enabled = Engineer;
        }

        /// <summary>Whether a tag's renderer belongs to an engineer's tag (a view that frames its own tags asks before it shows one).</summary>
        public static bool IsEngineer(MeshRenderer renderer)
        {
            foreach (var e in entries) if (e.Renderer == renderer) return e.Engineer;
            return false;
        }

        /// <summary>Shows or hides every engineer's tag that no view frames itself. Call once a frame, after the views.</summary>
        public static void Apply()
        {
            entries.RemoveAll(e => e.Renderer == null);
            foreach (var e in entries) if (e.Engineer && !Framed.Contains(e.Renderer) && e.Renderer.enabled != Engineer) e.Renderer.enabled = Engineer;
        }

        /// <summary>Renderers a view switches on and off itself (a well's level tags, by where the camera stands).</summary>
        public static readonly HashSet<MeshRenderer> Framed = new HashSet<MeshRenderer>();

        /// <summary>The tags a camera draws now: active, enabled, in front of the eye and inside the frame.</summary>
        public static IEnumerable<KeyValuePair<string, bool>> Drawn(Camera camera)
        {
            foreach (var e in entries)
            {
                if (e.Renderer == null || !e.Renderer.enabled || !e.Renderer.gameObject.activeInHierarchy || string.IsNullOrEmpty(e.Text.text)) continue;
                var p = camera.WorldToViewportPoint(e.Renderer.bounds.center);
                if (p.z <= 0 || p.x < -0.02f || p.x > 1.02f || p.y < -0.02f || p.y > 1.02f) continue;
                yield return new KeyValuePair<string, bool>(e.Text.text, e.Engineer || WorldText.LooksInternal(e.Text.text));
            }
        }
    }
}
