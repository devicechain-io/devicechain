// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class ChargerView
    {
        readonly AssetRig rig;
        readonly HarborMaterials mats;
        readonly MaterialPropertyBlock glow = new MaterialPropertyBlock();
        public ChargerView(AssetRig rig, HarborMaterials mats) { this.rig = rig; this.mats = mats; }
        public void Apply(ChargerBoard p, StoryCues world)
        {
            var screen = rig["Screen"].GetComponent<MeshRenderer>();
            screen.sharedMaterial = !p.Confirmed ? mats.LampUnknown : p.OtaOutcome == "FAILED" ? mats.Failed : p.ShowCarAndCable ? mats.Flow : mats.StatusGreen;
            var fade = !p.Confirmed ? 0 : (float)world.FeederPower(rig.Placement.Device.Quay);
            glow.SetColor("_EmissionColor", screen.sharedMaterial.GetColor("_EmissionColor") * fade);
            glow.SetColor("_BaseColor", Color.Lerp(new Color(0.05f, 0.06f, 0.08f), screen.sharedMaterial.GetColor("_BaseColor"), fade)); screen.SetPropertyBlock(glow);
            var active = p.ShowCarAndCable;
            rig["Car"].gameObject.SetActive(active); rig["Cable"].gameObject.SetActive(active);
            var connector = rig["Connector"].GetComponent<MeshRenderer>();
            glow.Clear(); glow.SetColor("_EmissionColor", new Color(0.15f, 0.55f, 1) * (float)(p.Glow ?? 0) * 4); connector.SetPropertyBlock(glow);
            rig["MaintenanceRing"].gameObject.SetActive(world.ChargerOtaNight);
        }
    }
}
