// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class GaugeView
    {
        readonly AssetRig rig;
        readonly HarborMaterials mats;
        readonly TextMesh counter;
        readonly GameObject counterPlate;
        readonly Transform crew, replacementLabel;
        public GaugeView(AssetRig rig, HarborMaterials mats, double wallZ)
        {
            this.rig = rig; this.mats = mats;
            var t = AssetRig.Node("AlarmCount", rig.Root, Vector3.up * 3.4f); t.localRotation = Quaternion.Euler(0, 180, 0);
            counter = t.gameObject.AddComponent<TextMesh>();
            counter.font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            t.GetComponent<MeshRenderer>().sharedMaterial = counter.font.material; counter.characterSize = 0.15f; counter.fontSize = 32; counter.anchor = TextAnchor.MiddleCenter;
            // a dark plate behind the count: the alarm beacon glows the same amber right behind it, and the bloom would swallow the text
            counterPlate = new MeshKit().Box(new Vector3(0, 0, -0.08f), new Vector3(2.3f, 0.55f, 0.04f)).Spawn("AlarmCountPlate", t, mats.DoorDark, false);
            replacementLabel = AssetRig.Node("ReplacementLabel", rig["Plate"], new Vector3(0, 0.2f, 0.04f));
            AssetRig.Tag("replacement (runner)", replacementLabel, Vector3.zero, 0.08f);
            var at = SitePlan.GaugeCrewAt(new Ground(0, 0), wallZ);
            crew = CrewBuilder.Build(rig.Root, mats, false, new Vector3((float)at.X, 0, (float)at.Z));
        }
        public void Apply(HarborFrame f)
        {
            var id = rig.Placement.Device.ExternalId; var n = f.AlarmCounts[id];
            counter.text = n == 0 ? "" : n + (n == 1 ? " alarm" : " alarms"); counter.color = new Color(1, 0.65f, 0.1f);
            counterPlate.SetActive(n > 0);
            var replaced = f.Replacement?.ExternalId == id;
            rig["Plate"].GetComponent<MeshRenderer>().sharedMaterial = replaced ? mats.WorldCue : mats.Steel;
            replacementLabel.gameObject.SetActive(replaced);
            crew.gameObject.SetActive(replaced && f.World.ReplacementCrew);
        }
    }
}
