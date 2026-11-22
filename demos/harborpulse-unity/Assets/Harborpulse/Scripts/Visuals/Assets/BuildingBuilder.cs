// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public static class BuildingBuilder
    {
        public static void House(Transform parent, Vector3 at, float width, float depth, float height, int floors, HarborMaterials m, Material wall, Material windows, bool pitched)
        {
            new MeshKit().Box(at + Vector3.up * height / 2, new Vector3(width, height, depth)).Spawn("Building", parent, wall);
            var roof = new MeshKit();
            if (pitched) roof.Gable(at + Vector3.up * height, width + 0.6f, depth + 0.6f, Mathf.Min(5, depth * 0.3f));
            else roof.Box(at + Vector3.up * (height + 0.25f), new Vector3(width + 0.3f, 0.5f, depth + 0.3f));
            roof.Spawn("Roof", parent, m.Slate);
            var lit = new MeshKit(); var dark = new MeshKit();
            for (var f = 0; f < floors; f++) for (var x = -width / 2 + 2.2f; x < width / 2 - 1.5f; x += 3.4f)
            {
                var h = Mathf.Sin((at.x + x) * 12.9898f + f * 78.233f + at.z * 37.719f) * 43758.5453f;
                (h - Mathf.Floor(h) < 0.66f ? lit : dark).Box(at + new Vector3(x + 0.7f, 1.7f + f * 3.2f, depth / 2 + 0.04f), new Vector3(1.4f, 1.5f, 0.08f));
            }
            lit.Spawn("Windows", parent, windows, false); dark.Spawn("DarkWindows", parent, m.WindowsDark, false);
        }
        public static void Build(Transform parent, Vector3 at, bool lofts, HarborMaterials m, float wall, DetailManager detail = null)
        {
            var t = AssetRig.Node(lofts ? "Quayside Lofts" : "Ferry Terminal", parent, at);
            // the lofts are a converted brick warehouse with a painted cornice; the terminal a grey clapboard hall under a pitched roof
            var hall = lofts ? Vector3.zero : new Vector3(NearLandmarks.TerminalOffsetX, 0, 0);
            House(t, hall, lofts ? NearLandmarks.LoftsW : NearLandmarks.TerminalW, lofts ? NearLandmarks.LoftsD : NearLandmarks.TerminalD, lofts ? NearLandmarks.LoftsH : NearLandmarks.TerminalH, lofts ? 5 : 2, m, lofts ? m.Brick : m.ClapGrey, lofts ? m.EastWindows : m.Windows, !lofts);
            if (lofts)
            {
                new MeshKit().Box(new Vector3(0, 17.3f, 0), new Vector3(39.2f, 0.6f, 19.2f)).Box(new Vector3(0, 3.6f, 9.08f), new Vector3(38.2f, 0.4f, 0.2f)).Spawn("Cornice", t, m.Trim);
                detail?.Register(NearLandmarks.Lofts(), t, Vector3.zero, Quaternion.identity,
                    NearPalette.Building(m.Brick.GetColor("_BaseColor"), m.Slate.GetColor("_BaseColor"), m.AwningBlue.GetColor("_BaseColor")), m.Surfaces);
                return;
            }
            var z = wall - at.z;
            new MeshKit().Box(new Vector3(0, -1.1f, z + 11), new Vector3(36, 0.5f, 10)).Box(new Vector3(0, -0.8f, z + 3), new Vector3(2.4f, 0.2f, 6)).Spawn("FerryPontoon", t, m.Timber);
            var ferry = AssetRig.Node("BerthedFerry", t, new Vector3(0, -(float)DeviceChain.Harborpulse.Model.SitePlan.DeckHeightM, z + 26));
            new MeshKit().Box(new Vector3(0, 0.3f, 0), new Vector3(33, 2, 9)).Cylinder(new Vector3(16, -0.7f, 0), 4.5f, 2, 12).Spawn("Hull", ferry, m.DarkSteel);
            new MeshKit().Box(new Vector3(0, 2.5f, 0), new Vector3(27, 2.4f, 7)).Box(new Vector3(8, 4.2f, 0), new Vector3(8, 1, 6)).Spawn("FerryDeck", ferry, m.Render);
            new MeshKit().Box(new Vector3(0, 2.8f, 3.55f), new Vector3(25, 0.9f, 0.05f)).Spawn("FerryWindows", ferry, m.Glass);
            if (detail == null) return;
            detail.Register(NearLandmarks.Terminal(), t, hall, Quaternion.identity,
                NearPalette.Building(m.ClapGrey.GetColor("_BaseColor"), m.Slate.GetColor("_BaseColor"), m.AwningGreen.GetColor("_BaseColor")), m.Surfaces);
            // the ferry berth: the pontoon and gangway the plain slabs stand for, and the ferry itself
            detail.Register(NearLandmarks.Ferry(), ferry, Vector3.zero, Quaternion.identity, NearPalette.Vehicle(m.Render.GetColor("_BaseColor")), m.Surfaces);
        }
        public static void Depot(Transform parent, Vector3 at, HarborMaterials m, DetailManager detail = null)
        {
            var t = AssetRig.Node("Works depot", parent, at);
            new MeshKit().Box(Vector3.up * 0.06f, new Vector3(70, 0.06f, 50)).Spawn("Yard", t, m.Asphalt);
            House(t, new Vector3(-18, 0, -14), 28, 16, 7, 1, m, m.Steel, m.Windows, true);
            detail?.Register(NearLandmarks.Depot(), t, new Vector3(-18, 0, -14), Quaternion.identity,
                NearPalette.Building(m.Steel.GetColor("_BaseColor"), m.Slate.GetColor("_BaseColor"), m.Safety.GetColor("_BaseColor")), m.Surfaces);
        }
    }
}
