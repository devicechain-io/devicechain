// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class TruckView
    {
        readonly AssetRig rig;
        bool placed;
        public TruckView(AssetRig rig) { this.rig = rig; }
        public void Apply(TruckTrack p, float dt, float time)
        {
            var at = new Vector3((float)p.At.X, (float)SitePlan.DeckHeightM, (float)p.At.Z);
            var k = !placed || dt <= 0 || p.NoFix ? 1 : 1 - Mathf.Exp(-dt * 4);
            rig.Root.position = Vector3.Lerp(rig.Root.position, at, k);
            if (p.Heading.HasValue) rig.Root.rotation = Quaternion.Slerp(rig.Root.rotation, Quaternion.Euler(0, (float)p.Heading.Value, 0), k);
            var bar = rig["LightBar"];
            bar.gameObject.SetActive(p.LightBar);
            // the bar flashes by pulsing its size, so a still never catches it dark while the platform says it should run
            bar.localScale = new Vector3(1, 1 + 0.6f * Mathf.Abs(Mathf.Sin(time * 6)), 1);
            placed = true;
        }
    }
}
