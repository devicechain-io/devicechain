// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// A lamp that asks the platform for its commands. It is on no feeder and no signal cable, and it is built to say so: a galvanised post
    /// two thirds the height of a street light's column, its own solar panel tilted to the south above it, a battery box strapped to the
    /// post, and a small lantern on a short arm. A board on the post carries four pips, lit to the level the lamp last reported. Named
    /// parts: Lantern, Halo, Pip0 to Pip3.
    /// </summary>
    public static class PullLampBuilder
    {
        public const float PostTopM = 5.2f, LanternY = 4.3f, LanternReachM = .78f;

        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m)
        {
            var r = new AssetRig(p, parent);
            r.Part("Footing", new MeshKit().Box(new Vector3(0, .11f, 0), new Vector3(.66f, .22f, .66f)).Cylinder(Vector3.up * .22f, .2f, .1f, 8, .15f), m.Coping, Vector3.zero);
            var post = new MeshKit().Cylinder(Vector3.up * .3f, .1f, PostTopM - .3f, 8, .07f)
                .Cylinder(Vector3.up * PostTopM, .09f, .08f, 8)
                // the lantern's arm and its stay, and the hood over the glass
                .Beam(new Vector3(0, 4.72f, 0), new Vector3(0, 4.72f, LanternReachM), .07f)
                .Beam(new Vector3(0, 4.2f, .05f), new Vector3(0, 4.68f, .6f), .04f)
                .Cylinder(new Vector3(0, LanternY + .3f, LanternReachM), .27f, .1f, 8, .15f)
                // straps round the battery box
                .Box(new Vector3(0, .9f, -.1f), new Vector3(.3f, .04f, .34f)).Box(new Vector3(0, 1.22f, -.1f), new Vector3(.3f, .04f, .34f));
            r.Part("Post", post, m.Steel, Vector3.zero);
            r.Part("Battery", new MeshKit().Box(new Vector3(0, 1.05f, -.24f), new Vector3(.36f, .56f, .26f)).Box(new Vector3(0, 1.36f, -.24f), new Vector3(.42f, .05f, .32f)), m.Slate, Vector3.zero);
            // The panel faces the sun's side of the sky: low edge towards the water (south, on North Quay), 27 degrees from the flat.
            var low = new Vector3(0, 5.3f, .52f); var high = new Vector3(0, 5.8f, -.48f);
            var normal = Vector3.Cross((high - low).normalized, Vector3.right).normalized; if (normal.y < 0) normal = -normal;
            var frame = new MeshKit().Beam(low, high, 1.16f, .05f)
                .Beam(new Vector3(0, PostTopM + .06f, 0), (low + high) / 2, .07f)
                // the level board on the post, facing the water
                .Box(new Vector3(0, 2.26f, .1f), new Vector3(.3f, 1.34f, .05f));
            r.Part("PanelFrame", frame, m.DarkSteel, Vector3.zero);
            r.Part("Panel", new MeshKit().Beam(Vector3.Lerp(low, high, .04f) + normal * .035f, Vector3.Lerp(low, high, .96f) + normal * .035f, 1.06f, .02f), m.SolarGlass, Vector3.zero);
            for (var i = 0; i < PullLampState.PipCount; i++)
                r.Box("Pip" + i, new Vector3(0, 1.81f + .3f * i, .135f), new Vector3(.2f, .2f, .04f), m.LampUnknown);
            var head = r.Part("Lantern", new MeshKit().Cylinder(Vector3.zero, .12f, .3f, 8, .2f), m.LampUnknown, new Vector3(0, LanternY, LanternReachM));
            var halo = AssetRig.Node("Halo", r.Root, head.localPosition + Vector3.up * .1f);
            halo.gameObject.AddComponent<MeshFilter>().sharedMesh = LampBuilder.GlowDisc();
            var haloRenderer = halo.gameObject.AddComponent<MeshRenderer>();
            haloRenderer.sharedMaterial = m.LampHalo; haloRenderer.shadowCastingMode = UnityEngine.Rendering.ShadowCastingMode.Off; haloRenderer.receiveShadows = false;
            r.Nodes.Add("Halo", halo);
            r["Beacon"].localPosition = Vector3.up * 6.8f;
            r["CardAnchor"].localPosition = Vector3.up * 7.4f;
            return r;
        }
    }
}
