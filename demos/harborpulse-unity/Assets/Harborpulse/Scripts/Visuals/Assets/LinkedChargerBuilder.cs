// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// A charger that holds its own MQTT session. The same family as the district's cabinet (a post, a screen, a holster, a bay) from another
    /// maker: slimmer, on a wider foot, under a small pitched hood of its own, and marked for how it reaches the platform. One on a device
    /// token is teal, with a whip aerial on the hood's ridge and three signal bars on its door; one on a client certificate is navy under
    /// brass, with a brass ridge, a brass shield on its door and the shield's keyhole. Each has a lamp on the hood's water-side gable (the
    /// part named Link) that the view lights while the charger reports: a bar under the eave on a token, a seal at the apex on a
    /// certificate. Named parts: Pedestal, Screen, Link, Holster, Car, Cable, BayMarkings.
    /// </summary>
    public static class LinkedChargerBuilder
    {
        public const float HeightM = 1.93f, AerialTopM = 4.2f;
        /// <summary>The hood: its eaves' height, its ridge's rise over them, its width across the post and its length from behind the post out over the user.</summary>
        public const float HoodEavesM = 2.62f, HoodRiseM = .46f, HoodWidthM = 1.14f, HoodLengthM = 1.5f, HoodCentreZ = .3f;

        public static AssetRig Build(Placement p, Transform parent, HarborMaterials m, Vector3 carAt, bool certificate, DetailManager detail = null)
        {
            var r = new AssetRig(p, parent);
            // the hood is the post's own colour: a soffit and a gabled roof whose ridge runs out towards the bay
            r.Part("Pedestal", new MeshKit().Box(Vector3.up * .95f, new Vector3(.62f, 1.9f, .46f))
                .Box(Vector3.up * .2f, new Vector3(.76f, .4f, .6f))
                .Box(new Vector3(0, HoodEavesM - .03f, HoodCentreZ), new Vector3(HoodWidthM, .06f, HoodLengthM))
                .Gable(new Vector3(0, HoodEavesM, HoodCentreZ), HoodLengthM, HoodWidthM, HoodRiseM, 90), certificate ? m.ChargerCert : m.ChargerLink, Vector3.zero);
            r.Box("Screen", new Vector3(0, 1.45f, .24f), new Vector3(.42f, .32f, .035f), m.LampUnknown);
            var gable = HoodCentreZ + HoodLengthM / 2;
            // on a token a bar under the eave; on a certificate a seal at the gable's apex
            if (certificate) r.Box("Link", new Vector3(0, HoodEavesM + .17f, gable + .02f), new Vector3(.26f, .2f, .05f), m.LampUnknown);
            else r.Box("Link", new Vector3(0, HoodEavesM - .12f, gable - .04f), new Vector3(.8f, .1f, .06f), m.LampUnknown);
            var fittings = new MeshKit().Box(new Vector3(.38f, 1, .2f), new Vector3(.16f, .35f, .22f))
                .Box(new Vector3(0, 1.45f, .232f), new Vector3(.5f, .4f, .05f))
                .Box(new Vector3(0, .06f, 0), new Vector3(.95f, .12f, .8f))
                .Box(new Vector3(0, .12f, 1.25f), new Vector3(1.7f, .24f, .25f))
                // the plug in its holster: the cable leaves from here when a car is in the bay
                .Box(new Vector3(.45f, 1.2f, .4f), new Vector3(.12f, .25f, .22f));
            // the hood's two posts behind the pedestal, each with a brace out under the soffit
            foreach (var side in new[] { -1, 1 })
                fittings.Beam(new Vector3(side * .5f, 0, -.36f), new Vector3(side * .5f, HoodEavesM - .06f, -.36f), .07f)
                    .Beam(new Vector3(side * .5f, HoodEavesM - .5f, -.36f), new Vector3(side * .5f, HoodEavesM - .08f, .3f), .05f);
            var lastLoop = new Vector3(.4f, 1.05f, .17f);
            for (var i = 1; i <= 8; i++)
            {
                var a = i * Mathf.PI * 2 / 8;
                var next = new Vector3(.61f - .21f * Mathf.Cos(a), .66f + .39f * Mathf.Cos(a), .17f + .18f * Mathf.Sin(a));
                fittings.Beam(lastLoop, next, .045f); lastLoop = next;
            }
            if (certificate)
            {
                // the keyhole in the shield
                fittings.Box(new Vector3(0, .93f, .262f), new Vector3(.05f, .12f, .02f)).Box(new Vector3(0, 1f, .262f), new Vector3(.1f, .08f, .02f));
                // a brass cap, and a shield on the door: a broad top stepping down to a point
                r.Part("Badge", new MeshKit().Box(Vector3.up * 1.935f, new Vector3(.7f, .07f, .54f))
                    .Beam(new Vector3(0, HoodEavesM + HoodRiseM, HoodCentreZ - HoodLengthM / 2 - .04f), new Vector3(0, HoodEavesM + HoodRiseM, gable + .04f), .09f)
                    .Box(new Vector3(0, 1f, .236f), new Vector3(.32f, .26f, .03f))
                    .Box(new Vector3(0, .82f, .236f), new Vector3(.22f, .12f, .03f))
                    .Box(new Vector3(0, .72f, .236f), new Vector3(.1f, .09f, .03f)), m.Brass, Vector3.zero);
            }
            else
            {
                // a dark cap; on the hood's ridge the whip aerial in its collar, and a low puck beside it
                var ridge = HoodEavesM + HoodRiseM;
                fittings.Box(Vector3.up * 1.93f, new Vector3(.68f, .06f, .52f))
                    .Cylinder(new Vector3(0, ridge - .06f, -.1f), .06f, .14f, 6)
                    .Cylinder(new Vector3(0, ridge + .08f, -.1f), .024f, AerialTopM - ridge - .14f, 5, .012f)
                    .Ball(new Vector3(0, AerialTopM - .04f, -.1f), .05f)
                    .Cylinder(new Vector3(0, ridge - .05f, .5f), .1f, .1f, 8);
                // three signal bars on the door
                r.Part("Bars", new MeshKit().Box(new Vector3(-.11f, .75f, .236f), new Vector3(.07f, .1f, .02f))
                    .Box(new Vector3(0, .8f, .236f), new Vector3(.07f, .2f, .02f))
                    .Box(new Vector3(.11f, .85f, .236f), new Vector3(.07f, .3f, .02f)), m.GaugeBoard, Vector3.zero);
            }
            r.Part("Holster", fittings, m.DarkSteel, Vector3.zero);
            // two short posts keep a bumper off the pedestal
            r.Part("Guards", new MeshKit().Cylinder(new Vector3(-.82f, 0, .78f), .06f, .82f, 6).Cylinder(new Vector3(.82f, 0, .78f), .06f, .82f, 6)
                .Cylinder(new Vector3(-.82f, .82f, .78f), .075f, .05f, 6).Cylinder(new Vector3(.82f, .82f, .78f), .075f, .05f, 6), m.Safety, Vector3.zero);
            ChargerBuilder.Bay(r, m, carAt, detail);
            r["Beacon"].localPosition = Vector3.up * (certificate ? HoodEavesM + HoodRiseM + .9f : AerialTopM + .8f);
            r["CardAnchor"].localPosition = Vector3.up * (certificate ? HoodEavesM + HoodRiseM + .5f : AerialTopM + .4f);
            if (!certificate) r.MergeFixed("BayMarkings", "Bars");
            return r;
        }
    }
}
