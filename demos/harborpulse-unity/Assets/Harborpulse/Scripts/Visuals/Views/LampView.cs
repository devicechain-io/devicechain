// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class LampView
    {
        readonly AssetRig rig; readonly HarborMaterials mats; readonly Transform crew; readonly Light light;
        readonly MaterialPropertyBlock fadeBlock = new MaterialPropertyBlock();
        public LampView(AssetRig rig, HarborMaterials mats)
        {
            this.rig = rig; this.mats = mats; light = rig["Lantern"].GetComponentInChildren<Light>();
            crew = CrewBuilder.Build(rig.Root, mats, true, new Vector3(4, 0, -3));
        }
        public void Apply(LampState p, HarborFrame frame, SkyState sky, Vector3 camera)
        {
            var on = p.Condition == LampCondition.On;
            rig["Lantern"].GetComponent<MeshRenderer>().sharedMaterial = on ? mats.LampOn : p.Condition == LampCondition.Off ? mats.LampOff : p.Condition == LampCondition.Silent ? mats.LampSilent : mats.LampUnknown;
            var power = (float)frame.World.FeederPower(rig.Placement.Device.Quay);
            var lantern = rig["Lantern"].GetComponent<MeshRenderer>();
            fadeBlock.SetColor("_EmissionColor", lantern.sharedMaterial.GetColor("_EmissionColor") * power);
            fadeBlock.SetColor("_BaseColor", Color.Lerp(new Color(0.10f, 0.12f, 0.15f), lantern.sharedMaterial.GetColor("_BaseColor"), power));
            lantern.SetPropertyBlock(fadeBlock);
            rig["Halo"].gameObject.SetActive(on && power > 0);
            var halo = rig["Halo"].GetComponent<MeshRenderer>();
            fadeBlock.Clear();
            fadeBlock.SetFloat(HarborMaterials.GlowPower, power);
            halo.SetPropertyBlock(fadeBlock);
            light.enabled = on && power > 0 && (rig.Root.position - camera).sqrMagnitude < 520 * 520;
            light.intensity = power * 16 * (float)(p.DimPct ?? 100) / 100 * Mathf.Lerp(0.15f, 1, (float)sky.Night);
            var job = frame.LampService?.RunnerPlan;
            var target = job != null && (job.Device == rig.Placement.Device.Token || job.Device == rig.Placement.Device.ExternalId);
            var worldTarget = frame.World.LampCrewDevice == rig.Placement.Device.Token || frame.World.LampCrewDevice == rig.Placement.Device.ExternalId;
            crew.gameObject.SetActive(worldTarget && frame.World.LampCrew);
            rig["PlatformWindow"].gameObject.SetActive(target && frame.LampService.PlatformWindow != null);
            rig["PlatformWindow"].GetComponent<MeshRenderer>().sharedMaterial = frame.LampService?.WindowOpen == true ? mats.StatusGreen : mats.Steel;
        }
    }
}
