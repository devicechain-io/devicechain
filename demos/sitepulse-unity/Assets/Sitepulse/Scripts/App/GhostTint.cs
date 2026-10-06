// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// A grey "not on the platform" tint for a machine that did not bind. It is a per-renderer
    /// property block on the glTF colour inputs the machine shader reads, so no material asset or
    /// instance is touched. Each material slot gets it as well as the renderer, because the rig's
    /// tread scroll writes slot blocks, and every writer here reads the block before it writes, so
    /// the tint survives theirs and theirs survives it. Mesh renderers only: the dust and exhaust
    /// particles are not part of the machine.
    /// </summary>
    public static class GhostTint
    {
        static readonly int BaseColor = Shader.PropertyToID("baseColorFactor");
        static readonly int Emissive = Shader.PropertyToID("emissiveFactor");
        static readonly Color Grey = new Color(0.52f, 0.56f, 0.58f, 1f);

        public static void Apply(Transform machine)
        {
            if (machine == null) return;
            var block = new MaterialPropertyBlock();
            foreach (var r in machine.GetComponentsInChildren<Renderer>(true))
            {
                if (!(r is MeshRenderer) && !(r is SkinnedMeshRenderer)) continue;
                Write(r, block, -1);
                for (var i = 0; i < r.sharedMaterials.Length; i++) Write(r, block, i);
            }
        }

        static void Write(Renderer r, MaterialPropertyBlock block, int slot)
        {
            if (slot < 0) r.GetPropertyBlock(block);
            else r.GetPropertyBlock(block, slot);
            block.SetColor(BaseColor, Grey);
            block.SetColor(Emissive, Color.black);
            if (slot < 0) r.SetPropertyBlock(block);
            else r.SetPropertyBlock(block, slot);
        }
    }
}
