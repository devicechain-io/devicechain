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
        readonly MeshRenderer counterRenderer;
        readonly GameObject counterPlate;
        int lastCount = -1;
        readonly Transform crew, replacementLabel;
        public GaugeView(AssetRig rig, HarborMaterials mats, double wallZ)
        {
            this.rig = rig; this.mats = mats;
            var t = AssetRig.Node("AlarmCount", rig.Root, Vector3.up * 3.4f); t.localRotation = Quaternion.Euler(0, 180, 0);
            counter = t.gameObject.AddComponent<TextMesh>();
            counter.font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            counterRenderer = t.GetComponent<MeshRenderer>();
            counterRenderer.sharedMaterial = mats.AlarmText; counter.characterSize = 0.15f; counter.fontSize = 32; counter.anchor = TextAnchor.MiddleCenter;
            // Both passes are unlit and depth-independent. A Lit box could be hidden by the
            // beacon or brightened by a lamp; the count must retain its opaque dark backing.
            var plate = new MeshKit();
            plate.Quad(new Vector3(-1.45f,-.325f,.08f),new Vector3(-1.45f,.325f,.08f),new Vector3(1.45f,.325f,.08f),new Vector3(1.45f,-.325f,.08f));
            counterPlate = plate.Spawn("AlarmCountPlate", t, mats.AlarmPlate, false);
            replacementLabel = AssetRig.Node("ReplacementLabel", rig["Plate"], new Vector3(0, 0.2f, 0.04f));
            AssetRig.Tag("replacement (runner)", replacementLabel, Vector3.zero, 0.08f);
            var at = SitePlan.GaugeCrewAt(new Ground(0, 0), wallZ);
            crew = CrewBuilder.Build(rig.Root, mats, false, new Vector3((float)at.X, 0, (float)at.Z));
        }
        public void Apply(HarborFrame f)
        {
            var id = rig.Placement.Device.ExternalId; var n = f.AlarmCounts[id];
            if (n != lastCount) {
                lastCount = n;
                counter.text = n == 0 ? "" : n + (n == 1 ? " alarm" : " alarms");
                counter.font.RequestCharactersInTexture(counter.text, counter.fontSize, FontStyle.Normal);
                var bounds = counterRenderer.localBounds;
                counterPlate.transform.localPosition = new Vector3(bounds.center.x, bounds.center.y, 0);
                counterPlate.transform.localScale = new Vector3(Mathf.Max(2.9f, bounds.size.x + .5f) / 2.9f, Mathf.Max(.65f, bounds.size.y + .35f) / .65f, 1);
                counterPlate.SetActive(n > 0);
            }
            // Dynamic font atlases can be rebuilt by other world labels.
            var atlas = counter.font.material.mainTexture;
            if (mats.AlarmText.mainTexture != atlas) mats.AlarmText.mainTexture = atlas;
            var replaced = f.Replacement?.ExternalId == id;
            rig["Plate"].GetComponent<MeshRenderer>().sharedMaterial = replaced ? mats.WorldCue : mats.Steel;
            replacementLabel.gameObject.SetActive(replaced);
            crew.gameObject.SetActive(replaced && f.World.ReplacementCrew);
        }
    }
}
