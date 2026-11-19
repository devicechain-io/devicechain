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
        public static void Tag(string text, Transform parent, Vector3 at, float size = 0.12f, Color? colour = null)
        {
            var t = Node(text, parent, at); t.localRotation = Quaternion.Euler(0, 180, 0);
            var m = t.gameObject.AddComponent<TextMesh>(); m.text = text; m.characterSize = size; m.fontSize = 36;
            m.font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            t.GetComponent<MeshRenderer>().sharedMaterial = m.font.material;
            m.anchor = TextAnchor.MiddleCenter; m.color = colour ?? new Color(0.78f, 0.85f, 0.90f);
        }
    }
}
