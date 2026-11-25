// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class BeaconView
    {
        readonly Transform root, core, tick, shaft;
        readonly MeshRenderer[] ringRenderers = new MeshRenderer[3];
        readonly Transform[] rings = new Transform[3];
        readonly MeshRenderer coreRenderer;
        readonly HarborMaterials mats;
        public BeaconView(Transform parent, HarborMaterials mats, int slot)
        {
            this.mats = mats;
            root = AssetRig.Node("AlarmBeacon", parent, Vector3.up * slot * 3.2f);
            // drawn large enough to read from a quay-wide shot: the beacon is the platform's alarm, the one thing the eye must find
            root.localScale = Vector3.one * 2.2f;
            core = new MeshKit().Ball(Vector3.zero, 0.22f).Spawn("Core", root, mats.Beacon, false).transform;
            coreRenderer = core.GetComponent<MeshRenderer>();
            for (var i = 0; i < 3; i++)
            {
                rings[i] = new MeshKit().Ring(Vector3.zero, 0.5f + i * 0.19f, 0.07f).Spawn("Ring" + i, root, mats.Beacon, false).transform;
                ringRenderers[i] = rings[i].GetComponent<MeshRenderer>();
            }

            // CRITICAL's light shaft (design: three rings, a deep amber-red core and a shaft)
            shaft = new MeshKit().Cylinder(Vector3.zero, 0.05f, 6f, 8).Spawn("Shaft", root, mats.Critical, false).transform;
            tick = new MeshKit().Rod(new Vector3(-0.3f, 0, 0), new Vector3(-0.05f, -0.25f, 0), 0.05f).Rod(new Vector3(-0.05f, -0.25f, 0), new Vector3(0.4f, 0.3f, 0), 0.05f).Spawn("ClearedTick", root, mats.StatusGreen, false).transform;
        }
        public void Apply(AlarmBeacon alarm, DateTimeOffset now, Vector3 eye)
        {
            if (alarm.Cleared) tick.rotation = Quaternion.Euler(0, (float)AlarmBeacons.TickYawDeg(tick.position.x, tick.position.z, eye.x, eye.z), 0);
            root.gameObject.SetActive(true); tick.gameObject.SetActive(alarm.Cleared); core.gameObject.SetActive(!alarm.Cleared);
            var material = alarm.Severity == "CRITICAL" ? mats.Critical : alarm.Severity == "MAJOR" ? mats.Major : mats.Minor;
            coreRenderer.sharedMaterial = material;
            var pulse = (float)alarm.Pulse(now);
            core.localScale = Vector3.one * (alarm.Severity == "CRITICAL" ? 1.8f : 1) * (0.7f + 0.3f * pulse);
            shaft.gameObject.SetActive(!alarm.Cleared && alarm.Severity == "CRITICAL");
            for (var i = 0; i < rings.Length; i++)
            {
                rings[i].gameObject.SetActive(!alarm.Cleared && i < alarm.Rings);
                ringRenderers[i].sharedMaterial = material;
                rings[i].localScale = Vector3.one * (0.85f + 0.3f * pulse);
            }
        }
        public void Hide() => root.gameObject.SetActive(false);
    }
}
