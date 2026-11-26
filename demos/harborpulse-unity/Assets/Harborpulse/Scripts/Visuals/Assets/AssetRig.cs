// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Stable named nodes are the refinement contract for the next art pass.</summary>
    public sealed class AssetRig
    {
        public Placement Placement;
        public Transform Root;
        public readonly Dictionary<string, Transform> Nodes = new Dictionary<string, Transform>();
        public Transform this[string name] => Nodes[name];
        public AssetRig(Placement placement, Transform parent)
        {
            Placement = placement;
            Root = Node(placement.Device.ExternalId, parent, new Vector3((float)placement.At.X, (float)SitePlan.DeckHeightM, (float)placement.At.Z));
            Nodes.Add("Beacon", Node("Beacon", Root, Vector3.up * 4));
            Nodes.Add("CardAnchor", Node("CardAnchor", Root, Vector3.up * 5));
        }
        public Transform Add(string name, Vector3 at) { var n = Node(name, Root, at); Nodes.Add(name, n); return n; }
        public static Transform Node(string name, Transform parent, Vector3 at)
        {
            var t = new GameObject(name).transform; t.SetParent(parent, false); t.localPosition = at; return t;
        }
        public Transform Part(string name, MeshKit mesh, Material material, Vector3 at, bool shadow = true)
        {
            var t = mesh.Spawn(name, Root, material, shadow).transform; t.localPosition = at; Nodes.Add(name, t); return t;
        }
        public Transform Box(string name, Vector3 at, Vector3 size, Material mat) => Part(name, new MeshKit().Box(Vector3.zero, size), mat, at);

        /// <summary>Batch only explicitly fixed parts. Named transforms survive; stateful renderers and moving attachments stay separate.</summary>
        public void MergeFixed(params string[] names)
        {
            var parts = new List<MeshFilter>();
            foreach (var name in names) parts.Add(Nodes[name].GetComponent<MeshFilter>());
            MergeFixed(Root, parts);
        }

        public static void MergeFixed(Transform root, IEnumerable<MeshFilter> parts)
        {
            var groups = new Dictionary<(Material, UnityEngine.Rendering.ShadowCastingMode), List<MeshFilter>>();
            foreach (var part in parts)
            {
                if (part == null || part.sharedMesh == null) continue;
                var renderer = part.GetComponent<MeshRenderer>();
                if (renderer == null) continue;
                var key = (renderer.sharedMaterial, renderer.shadowCastingMode);
                if (!groups.TryGetValue(key, out var group)) groups[key] = group = new List<MeshFilter>();
                group.Add(part);
            }
            foreach (var pair in groups)
            {
                if (pair.Value.Count < 2) continue;
                var combine = new CombineInstance[pair.Value.Count];
                for (var i = 0; i < combine.Length; i++) combine[i] = new CombineInstance {
                    mesh = pair.Value[i].sharedMesh, transform = root.worldToLocalMatrix * pair.Value[i].transform.localToWorldMatrix };
                var vertexCount = 0; foreach (var part in pair.Value) vertexCount += part.sharedMesh.vertexCount;
                var mesh = new Mesh { name = "Fixed fittings" };
                if (vertexCount > 65000) mesh.indexFormat = UnityEngine.Rendering.IndexFormat.UInt32;
                mesh.CombineMeshes(combine, true, true);
                var node = Node("Fixed fittings", root, Vector3.zero);
                node.gameObject.AddComponent<MeshFilter>().sharedMesh = mesh;
                var renderer = node.gameObject.AddComponent<MeshRenderer>();
                renderer.sharedMaterial = pair.Key.Item1; renderer.shadowCastingMode = pair.Key.Item2;
                foreach (var part in pair.Value)
                {
                    var old = part.sharedMesh; part.sharedMesh = null;
                    var oldRenderer = part.GetComponent<MeshRenderer>(); oldRenderer.enabled = false;
                    MeshKit.Release(old); MeshKit.Release(oldRenderer); MeshKit.Release(part);
                }
            }
        }
        /// <summary>
        /// Writes a tag in the world. <paramref name="engineer"/> marks one that names a source or a rule key: it is drawn only with
        /// the engineer overlay (see <see cref="WorldTags"/>). Every tag is registered, so a render's text audit can list what a frame drew.
        /// </summary>
        public static MeshRenderer Tag(string text, Transform parent, Vector3 at, float size = 0.12f, Color? colour = null, bool engineer = false)
        {
            var t = Node(text, parent, at); t.localRotation = Quaternion.Euler(0, 180, 0);
            var m = t.gameObject.AddComponent<TextMesh>(); m.text = text; m.characterSize = size; m.fontSize = 36;
            m.font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            t.GetComponent<MeshRenderer>().sharedMaterial = m.font.material;
            m.anchor = TextAnchor.MiddleCenter; m.color = colour ?? new Color(0.78f, 0.85f, 0.90f);
            var renderer = t.GetComponent<MeshRenderer>(); WorldTags.Register(m, renderer, engineer);
            return renderer;
        }
    }
}
