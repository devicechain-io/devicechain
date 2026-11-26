// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class PumpStationView
    {
        readonly AssetRig rig;
        readonly MeshRenderer[] tags;
        public PumpStationView(AssetRig rig)
        {
            this.rig = rig;
            // the level tags' text: the font's own material draws over everything, so the view decides when they are drawn at all
            var found = new System.Collections.Generic.List<MeshRenderer>();
            foreach (var name in new[] { "LowCutoff", "HighMarker", "FloatMarker", "AttributeMarker" })
                foreach (var text in rig[name].GetComponentsInChildren<TextMesh>(true)) found.Add(text.GetComponent<MeshRenderer>());
            tags = found.ToArray();
            foreach (var tag in tags) WorldTags.Framed.Add(tag);
        }
        /// <summary>Text is drawn through the deck, so beside a lamp or a gauge the tags of a well at the frame's edge used to float over the quay.</summary>
        public void FrameTags(Vector3 camera, Vector3 forward, bool tallFrame)
        {
            var well = rig.Root.TransformPoint(new Vector3((float)WellFootprint.LocalX, 0, (float)WellFootprint.LocalZ));
            var show = WellFootprint.TagsInView(well.x - camera.x, well.y - camera.y, well.z - camera.z, forward.x, forward.y, forward.z, tallFrame);
            // a tag that names its source is drawn only with the engineer overlay, wherever the camera stands
            foreach (var tag in tags) { var draw = show && (WorldTags.Engineer || !WorldTags.IsEngineer(tag)); if (tag.enabled != draw) tag.enabled = draw; }
        }

        public void Apply(PumpPresentation p, float dt, float time, Vector3 camera, Vector3 forward, bool tallFrame)
        {
            FrameTags(camera, forward, tallFrame);
            var surface = rig["WellSurface"]; surface.gameObject.SetActive(p.WetWell.HasValue);
            if (p.WetWell.HasValue) surface.localPosition = new Vector3((float)WellFootprint.LocalX, PumpStationBuilder.WellFloor + (float)p.WetWell.Value.Value, (float)WellFootprint.LocalZ);
            if (p.ImpellerSpeed.HasValue) rig["Impeller"].Rotate(Vector3.up, (float)p.ImpellerSpeed.Value * 720 * dt, Space.Self);
            rig["CurrentNeedle"].gameObject.SetActive(p.Current.HasValue);
            if (p.Current.HasValue) rig["CurrentNeedle"].localRotation = Quaternion.Euler(0, 0, Mathf.Lerp(110, -110, Mathf.Clamp01((float)p.Current.Value.Value / 60)));
            var flow = p.Flow.HasValue ? Mathf.Clamp01((float)p.Flow.Value.Value / 120) : 0;
            rig["OutfallFlap"].localRotation = Quaternion.Euler(-flow * 75, 0, 0);
            var jet = rig["OutfallJet"]; jet.gameObject.SetActive(flow > 0);
            jet.localScale = new Vector3(0.6f + flow * 0.4f, 0.6f + flow * 0.4f, flow * (5 + Mathf.Sin(time * 14) * 0.3f));
            // the tenant's rule level is an engineer's mark: without its tag the ring is one more unexplained line in the well
            var marker = rig["AttributeMarker"]; marker.gameObject.SetActive(p.WellAlarmRule.HasValue && WorldTags.Engineer);
            if (p.WellAlarmRule.HasValue) marker.localPosition = new Vector3((float)WellFootprint.LocalX, PumpStationBuilder.WellFloor + (float)p.WellAlarmRule.Value.Value, (float)WellFootprint.LocalZ);
        }
    }
}
