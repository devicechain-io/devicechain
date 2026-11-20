// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class PumpStationView
    {
        readonly AssetRig rig;
        public PumpStationView(AssetRig rig) { this.rig = rig; }
        public void Apply(PumpPresentation p, float dt, float time)
        {
            var surface = rig["WellSurface"]; surface.gameObject.SetActive(p.WetWell.HasValue);
            if (p.WetWell.HasValue) surface.localPosition = new Vector3(9, PumpStationBuilder.WellFloor + (float)p.WetWell.Value.Value, 6);
            if (p.ImpellerSpeed.HasValue) rig["Impeller"].Rotate(Vector3.up, (float)p.ImpellerSpeed.Value * 720 * dt, Space.Self);
            rig["CurrentNeedle"].gameObject.SetActive(p.Current.HasValue);
            if (p.Current.HasValue) rig["CurrentNeedle"].localRotation = Quaternion.Euler(0, 0, Mathf.Lerp(110, -110, Mathf.Clamp01((float)p.Current.Value.Value / 60)));
            var flow = p.Flow.HasValue ? Mathf.Clamp01((float)p.Flow.Value.Value / 120) : 0;
            rig["OutfallFlap"].localRotation = Quaternion.Euler(-flow * 75, 0, 0);
            var jet = rig["OutfallJet"]; jet.gameObject.SetActive(flow > 0);
            jet.localScale = new Vector3(0.6f + flow * 0.4f, 0.6f + flow * 0.4f, flow * (5 + Mathf.Sin(time * 14) * 0.3f));
            var marker = rig["AttributeMarker"]; marker.gameObject.SetActive(p.WellAlarmRule.HasValue);
            if (p.WellAlarmRule.HasValue) marker.localPosition = new Vector3(9, PumpStationBuilder.WellFloor + (float)p.WellAlarmRule.Value.Value, 6);
        }
    }
}
