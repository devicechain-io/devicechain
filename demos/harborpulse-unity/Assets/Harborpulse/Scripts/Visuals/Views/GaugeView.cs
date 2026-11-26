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
        readonly Transform replacementLabel; readonly double wallZ;
        Transform crew;
        public GaugeView(AssetRig rig, HarborMaterials mats, double wallZ)
        {
            this.rig = rig; this.mats = mats;
            replacementLabel = AssetRig.Node("ReplacementLabel", rig["Plate"], new Vector3(0, 0.2f, 0.04f));
            AssetRig.Tag(WorldText.Replacement, replacementLabel, Vector3.zero, 0.08f, null, true);
            this.wallZ = wallZ;
        }
        public void Apply(HarborFrame f)
        {
            // how many alarms a gauge has is its pin's badge (the story HUD draws it); nothing is written on the gauge itself
            var id = rig.Placement.Device.ExternalId;
            var replaced = f.Replacement?.ExternalId == id;
            rig["Plate"].GetComponent<MeshRenderer>().sharedMaterial = replaced ? mats.WorldCue : mats.Steel;
            replacementLabel.gameObject.SetActive(replaced);
            // the crew is built when the scenario first sends one here (see LampView)
            var crewHere = replaced && f.World.ReplacementCrew;
            if (crewHere && crew == null)
            {
                var at = SitePlan.GaugeCrewAt(new Ground(0, 0), wallZ);
                crew = CrewBuilder.Build(rig.Root, mats, false, new Vector3((float)at.X, 0, (float)at.Z));
            }
            if (crew != null) crew.gameObject.SetActive(crewHere);
        }
    }
}
