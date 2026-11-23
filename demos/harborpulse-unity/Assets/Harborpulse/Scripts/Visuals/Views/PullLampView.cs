// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// A lamp that asks for its commands, as its board has it. The pips on its post are the level it last reported: the platform's, shown by
    /// day and by night. The lantern is the scene's: the lamp reports no on or off, so the lantern burns with the dusk, as a lamp on a
    /// photocell does, at the reported level, and only while the report is current. Silent past the platform's window it is violet, like a
    /// silent street light; never heard, it is dark.
    /// </summary>
    public sealed class PullLampView
    {
        readonly HarborMaterials mats;
        readonly MeshRenderer lantern, halo; readonly MeshRenderer[] pips = new MeshRenderer[PullLampState.PipCount];
        readonly MaterialPropertyBlock block = new MaterialPropertyBlock();
        static readonly int Emission = Shader.PropertyToID("_EmissionColor"), Base = Shader.PropertyToID("_BaseColor");

        /// <summary>How much of a street light's glow a pull lamp's small lantern gives at full level.</summary>
        public const float GlowShare = .6f;

        public PullLampView(AssetRig rig, HarborMaterials mats)
        {
            this.mats = mats;
            lantern = rig["Lantern"].GetComponent<MeshRenderer>(); halo = rig["Halo"].GetComponent<MeshRenderer>();
            for (var i = 0; i < pips.Length; i++) pips[i] = rig["Pip" + i].GetComponent<MeshRenderer>();
        }

        /// <summary>How brightly the lantern burns, 0 to 1: the reported level through the dusk, nothing unless the report is current.</summary>
        public static float Burn(PullLampState p, double night) => (float)((p.DimPct ?? 0) / 100) * Mathf.SmoothStep(0, 1, (float)night);

        public void Apply(PullLampState p, SkyState sky)
        {
            for (var i = 0; i < pips.Length; i++) pips[i].sharedMaterial = i < p.Pips ? mats.PipOn : mats.LampUnknown;
            var burn = Burn(p, sky.Night); var lit = burn > .02f;
            lantern.sharedMaterial = p.NoReading ? mats.LampUnknown : p.Stale ? mats.LampSilent : lit ? mats.LampOn : mats.LampOff;
            block.Clear();
            if (lit)
            {
                block.SetColor(Emission, mats.LampOn.GetColor(Emission) * burn);
                block.SetColor(Base, Color.Lerp(mats.LampOff.GetColor(Base), mats.LampOn.GetColor(Base), burn));
            }
            lantern.SetPropertyBlock(lit ? block : null);
            halo.gameObject.SetActive(lit);
            block.Clear(); block.SetFloat(HarborMaterials.GlowPower, burn * GlowShare); halo.SetPropertyBlock(block);
        }
    }
}
