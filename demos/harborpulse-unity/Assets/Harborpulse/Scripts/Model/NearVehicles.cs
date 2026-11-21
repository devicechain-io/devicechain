// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>The proportions a hatchback's near set is dressed to, taken from how its far boxes were drawn.</summary>
    public readonly struct CarSpec
    {
        public readonly float BodyW, BodyL, BodyBottom, BodyTop, CabinW, CabinL, CabinZ, GlassW, GlassL, GlassBottom, GlassTop, WheelR, WheelZ;
        /// <summary>+1 when the bonnet is toward local +Z, -1 when it is toward -Z.</summary>
        public readonly int Front;
        /// <summary>True when the far level already draws the wheels (the charging car); false when the near level supplies them.</summary>
        public readonly bool WheelsInFar;
        public CarSpec(float bodyW, float bodyL, float bodyBottom, float bodyTop, float cabinW, float cabinL, float cabinZ, float glassW, float glassL, float glassBottom,
            float glassTop, float wheelR, float wheelZ, int front, bool wheelsInFar)
        {
            BodyW = bodyW; BodyL = bodyL; BodyBottom = bodyBottom; BodyTop = bodyTop; CabinW = cabinW; CabinL = cabinL; CabinZ = cabinZ; GlassW = glassW; GlassL = glassL;
            GlassBottom = glassBottom; GlassTop = glassTop; WheelR = wheelR; WheelZ = wheelZ; Front = front; WheelsInFar = wheelsInFar;
        }

        /// <summary>A car in a bay: 1.8 x 4.3 body, 0.8 tall from 0.15, a cabin 1.6 x 2.3 offset 0.2 toward the rear, the far level draws no wheels.</summary>
        public static readonly CarSpec Parked = new CarSpec(1.8f, 4.3f, .15f, .95f, 1.6f, 2.3f, .2f, 1.64f, 2.1f, 1.01f, 1.43f, .31f, 1.35f, -1, false);
        /// <summary>The car on a charger: 1.8 x 3.6 body, a cabin 1.6 x 1.9, the far level already draws its wheels (r 0.34, 0.78 out).</summary>
        public static readonly CarSpec Charging = new CarSpec(1.8f, 3.6f, .325f, .975f, 1.6f, 1.9f, -.2f, 1.5f, 1.7f, .9f, 1.4f, .34f, 1.1f, 1, true);
    }

    public enum BoatKind { Sail, Skiff, Lobster, Fishing, Ferry }

    /// <summary>The near sets of the vehicles: the service truck, the cars, the moored and hauled boats.</summary>
    public static class NearVehicles
    {
        // ---------------------------------------------------------------- a hatchback

        /// <summary>
        /// Wheels with arches and hubs, glazing pillars, lights, mirrors, bumpers and door seams for a hatchback. Everything stays within 0.06 m of
        /// the sides and 0.1 m of the ends of the body the far level draws, which is the footprint a parked car reserves.
        /// </summary>
        public static NearPlan Car(CarSpec s)
        {
            var p = new NearPlan("car");
            var f = s.Front; float bw = s.BodyW / 2, bl = s.BodyL / 2;
            var bodyH = s.BodyTop - s.BodyBottom;
            // underbody shadow, bumpers, lamps and plates
            p.Box(NearTone.Black, 0, s.BodyBottom - .02f, 0, s.BodyW - .12f, .1f, s.BodyL - .3f);
            p.Box(NearTone.Black, 0, s.BodyBottom + .2f, f * (bl - .02f), s.BodyW - .08f, .22f, .13f).Box(NearTone.Black, 0, s.BodyBottom + .2f, -f * (bl - .02f), s.BodyW - .08f, .22f, .13f);
            p.Box(NearTone.Black, 0, s.BodyTop - .3f, f * (bl + .012f), .95f, .16f, .03f)
                .Box(NearTone.Metal, 0, s.BodyTop - .3f, f * (bl + .03f), .9f, .02f, .02f)
                .Box(NearTone.Cream, -.62f, s.BodyTop - .27f, f * (bl + .012f), .36f, .13f, .03f).Box(NearTone.Cream, .62f, s.BodyTop - .27f, f * (bl + .012f), .36f, .13f, .03f)
                .Box(NearTone.Orange, -.8f, s.BodyTop - .47f, f * (bl + .012f), .12f, .07f, .03f).Box(NearTone.Orange, .8f, s.BodyTop - .47f, f * (bl + .012f), .12f, .07f, .03f);
            p.Box(NearTone.Red, -.64f, s.BodyTop - .25f, -f * (bl + .012f), .36f, .16f, .03f).Box(NearTone.Red, .64f, s.BodyTop - .25f, -f * (bl + .012f), .36f, .16f, .03f)
                .Box(NearTone.Cream, 0, s.BodyBottom + .38f, -f * (bl + .016f), .5f, .11f, .02f);
            // door seams, handles and a belt line along both flanks
            foreach (var sx in new[] { -1, 1 })
            {
                var x = sx * (bw + .008f);
                foreach (var z in new[] { s.CabinZ + f * (s.CabinL / 2 - .05f), s.CabinZ - f * .05f, s.CabinZ - f * (s.CabinL / 2 - .05f) })
                    p.Box(NearTone.Black, x, s.BodyBottom + bodyH / 2 - .02f, z, .016f, bodyH - .12f, .022f);
                p.Box(NearTone.Metal, x + sx * .012f, s.BodyTop - .2f, s.CabinZ + f * .55f, .03f, .045f, .15f).Box(NearTone.Metal, x + sx * .012f, s.BodyTop - .2f, s.CabinZ - f * .5f, .03f, .045f, .15f);
                p.Box(NearTone.Metal, x + sx * .004f, s.BodyTop - .04f, 0, .012f, .03f, s.BodyL - .7f);
                // mirror on its short arm, at the cabin's front corner
                var mz = s.CabinZ + f * (s.CabinL / 2 - .1f);
                p.Beam(NearTone.Black, sx * (s.CabinW / 2), s.BodyTop + .15f, mz, sx * (s.CabinW / 2 + .08f), s.BodyTop + .17f, mz, .035f)
                    .Box(NearTone.Paint, sx * (s.CabinW / 2 + .11f), s.BodyTop + .17f, mz, .08f, .11f, .16f);
                // the pillars round the glass, in body colour, with a chrome sill
                var gx = sx * (s.GlassW / 2 + .02f); var gy = (s.GlassBottom + s.GlassTop) / 2; var gh = s.GlassTop - s.GlassBottom + .04f;
                p.Box(NearTone.Paint, gx, gy, s.CabinZ + f * (s.GlassL / 2 - .04f), .06f, gh, .09f).Box(NearTone.Paint, gx, gy, s.CabinZ - f * .05f, .06f, gh, .07f)
                    .Box(NearTone.Paint, gx, gy, s.CabinZ - f * (s.GlassL / 2 - .04f), .06f, gh, .1f)
                    .Box(NearTone.Metal, gx, s.GlassBottom - .02f, s.CabinZ, .05f, .025f, s.GlassL);
            }
            // roof: a pair of rails, a small fin and a lip spoiler at the tail
            foreach (var sx in new[] { -1, 1 })
                p.Box(NearTone.Black, sx * (s.CabinW / 2 - .1f), s.GlassTop + .17f, s.CabinZ, .05f, .04f, s.CabinL - .5f);
            p.Box(NearTone.Black, 0, s.GlassTop + .2f, s.CabinZ - f * (s.CabinL / 2 - .25f), .08f, .06f, .22f)
                .Box(NearTone.Paint, 0, s.GlassTop + .1f, s.CabinZ - f * (s.CabinL / 2 + .02f), s.CabinW - .2f, .05f, .14f);
            // wheels: tyre, arch liner and hub (the charging car's far wheels are kept, so it only gains hub and arch)
            foreach (var sx in new[] { -1, 1 })
                foreach (var wz in new[] { -s.WheelZ, s.WheelZ })
                {
                    var ax = sx * (bw + .005f);
                    p.Cyl(NearTone.Black, ax, s.WheelR + .02f, wz, ax + sx * .012f, s.WheelR + .02f, wz, s.WheelR + .1f, -1, 10);
                    if (!s.WheelsInFar)
                    {
                        // the tyre: 0.2 wide, its outer face 0.03 proud of the body side
                        p.Cyl(NearTone.Black, sx * (bw - .17f), s.WheelR, wz, sx * (bw + .03f), s.WheelR, wz, s.WheelR, -1, 10);
                        p.Cyl(NearTone.Metal, sx * (bw + .03f), s.WheelR, wz, sx * (bw + .045f), s.WheelR, wz, s.WheelR * .56f, -1, 8);
                    }
                    else p.Cyl(NearTone.Metal, sx * 1.0f, s.WheelR, wz, sx * 1.016f, s.WheelR, wz, s.WheelR * .52f, -1, 8);
                }
            if (s.WheelsInFar)
            {
                // the charging car's cabin has a windscreen only: glazing down the sides and at the tail
                foreach (var sx in new[] { -1, 1 }) p.Box(NearTone.Glass, sx * (s.CabinW / 2 + .012f), (s.GlassBottom + s.GlassTop) / 2 - .1f, s.CabinZ, .03f, .4f, s.CabinL - .3f);
                p.Box(NearTone.Glass, 0, (s.GlassBottom + s.GlassTop) / 2 - .1f, s.CabinZ - s.CabinL / 2 - .012f, s.CabinW - .2f, .4f, .03f);
            }
            return p;
        }

        // ---------------------------------------------------------------- the service truck

        /// <summary>The flood-response truck in its rig frame (+Z forward, origin on the deck): see the truck builder for the far boxes this dresses.</summary>
        public static NearPlan Truck()
        {
            var p = new NearPlan("truck");
            foreach (var sx in new[] { -1, 1 })
            {
                foreach (var wz in new[] { -1.9f, 1.9f })
                {
                    // wheel arch liner behind the tyre, a rim, and five wheel nuts
                    p.Cyl(NearTone.Black, sx * 1.16f, .55f, wz, sx * 1.178f, .55f, wz, .64f, -1, 12);
                    p.Cyl(NearTone.Metal, sx * 1.36f, .55f, wz, sx * 1.39f, .55f, wz, .34f, -1, 10);
                    for (var k = 0; k < 5; k++)
                    {
                        var a = k * (float)Math.PI * 2 / 5;
                        p.Box(NearTone.MetalDark, sx * 1.4f, .55f + (float)Math.Cos(a) * .2f, wz + (float)Math.Sin(a) * .2f, .03f, .05f, .05f);
                    }
                }
                // cab door: outline seams, a handle, a grab rail, and the window frames
                var x = sx * 1.132f;
                p.Box(NearTone.Black, x, 1.5f, 1.0f, .018f, 1.75f, .022f).Box(NearTone.Black, x, 1.5f, 2.22f, .018f, 1.75f, .022f)
                    .Box(NearTone.Black, x, 1.65f, 1.6f, .018f, .022f, 1.2f).Box(NearTone.Black, x, .86f, 1.6f, .018f, .022f, 1.2f)
                    .Box(NearTone.Metal, x + sx * .014f, 1.55f, 2.05f, .03f, .05f, .2f)
                    .Beam(NearTone.Metal, sx * 1.15f, 1.0f, .78f, sx * 1.15f, 2.1f, .78f, .045f);
                var wx = sx * 1.17f;
                p.Box(NearTone.Black, wx, 2.38f, 1.6f, .03f, .06f, 1.34f).Box(NearTone.Black, wx, 1.53f, 1.6f, .03f, .06f, 1.34f)
                    .Box(NearTone.Black, wx, 1.95f, .96f, .03f, .88f, .06f).Box(NearTone.Black, wx, 1.95f, 2.24f, .03f, .88f, .06f);
                // mirror glass, and a step under the door
                p.Box(NearTone.Glass, sx * 1.485f, 2.0f, 2.05f, .02f, .22f, .18f).Box(NearTone.MetalDark, sx * 1.28f, .58f, 1.0f, .24f, .05f, .46f);
                // tool body: two compartment doors a side, with handles and hinges
                foreach (var z in new[] { -1.55f, -.65f })
                    p.Box(NearTone.MetalDark, sx * 1.172f, 1.2f, z, .02f, .74f, .8f).Box(NearTone.Metal, sx * 1.19f, 1.1f, z + .28f, .02f, .06f, .2f)
                        .Box(NearTone.Metal, sx * 1.185f, 1.45f, z - .38f, .02f, .12f, .04f).Box(NearTone.Metal, sx * 1.185f, .95f, z - .38f, .02f, .12f, .04f);
                // headlamp housing and indicator, and the fuel filler behind the cab
                p.Box(NearTone.Black, sx * .78f, 1.32f, 2.585f, .5f, .3f, .05f).Box(NearTone.Cream, sx * .78f, 1.32f, 2.615f, .36f, .18f, .03f).Box(NearTone.Orange, sx * 1.0f, 1.32f, 2.615f, .12f, .1f, .03f);
                // rear lights, reflectors and chevrons on the bumper
                p.Box(NearTone.Red, sx * .85f, .95f, -2.58f, .3f, .2f, .04f).Box(NearTone.Cream, sx * 1.0f, 1.3f, -2.58f, .12f, .12f, .04f);
            }
            // windscreen frame and wipers, a grille with slats and a plain plate
            p.Box(NearTone.Black, 0, 2.38f, 2.585f, 2.05f, .07f, .06f).Box(NearTone.Black, 0, 1.52f, 2.585f, 2.05f, .07f, .06f)
                .Box(NearTone.Black, -1.0f, 1.95f, 2.585f, .07f, .9f, .06f).Box(NearTone.Black, 1.0f, 1.95f, 2.585f, .07f, .9f, .06f);
            p.Beam(NearTone.Black, -.15f, 1.6f, 2.6f, -.8f, 2.05f, 2.6f, .03f).Beam(NearTone.Black, .55f, 1.6f, 2.6f, -.1f, 2.05f, 2.6f, .03f);
            for (var k = 0; k < 4; k++) p.Box(NearTone.Black, 0, 1.08f + k * .08f, 2.63f, 1.2f, .03f, .03f);
            p.Box(NearTone.Cream, 0, .72f, 2.77f, .5f, .12f, .02f);
            for (var k = 0; k < 10; k++)
                p.Box(k % 2 == 0 ? NearTone.Red : NearTone.Yellow, -1.0f + k * .22f, .7f, -2.745f, .2f, .16f, .01f);
            p.Box(NearTone.Cream, 0, .98f, -2.58f, .5f, .12f, .02f).Cyl(NearTone.Metal, 0, .6f, -2.76f, 0, .6f, -3.0f, .06f, -1, 8);
            // the load on the roof rack: sandbags, cones, a coiled hose, a portable pump and cans
            for (var k = 0; k < 2; k++) p.Box(NearTone.Cream, -.45f, 1.9f, -1.95f + k * .65f, .55f, .2f, .32f).Box(NearTone.Cream, -.45f + .15f, 2.1f, -1.95f + k * .65f, .5f, .2f, .3f);
            for (var k = 0; k < 3; k++)
                p.Drum(NearTone.Orange, .5f, 1.83f, -2.1f + k * .5f, .15f, .4f, 8, .04f).Box(NearTone.Black, .5f, 1.815f, -2.1f + k * .5f, .32f, .03f, .32f);
            p.Drum(NearTone.Yellow, .42f, 1.8f, -.35f, .34f, .2f, 10).Drum(NearTone.MetalDark, .42f, 2.0f, -.35f, .1f, .04f, 8);
            p.Box(NearTone.Red, -.42f, 2.05f, -.45f, .6f, .4f, .5f).Drum(NearTone.MetalDark, -.42f, 2.25f, -.45f, .1f, .35f, 6).Box(NearTone.Metal, -.42f, 1.84f, -.45f, .66f, .06f, .56f)
                .Box(NearTone.Red, .05f, 1.95f, -.05f, .24f, .35f, .14f).Box(NearTone.Red, .3f, 1.95f, -.05f, .24f, .35f, .14f);
            // cab roof: a visor over the windscreen, and its light-bar brackets
            p.Box(NearTone.Black, 0, 2.58f, 2.4f, 2.1f, .05f, .3f);
            return p;
        }

        // ---------------------------------------------------------------- boats

        /// <summary>
        /// A moored boat in its own frame (waterline at the origin, bow toward +Z), on top of the chined hull, deck, cabin and glazing the boat
        /// builder draws. <paramref name="length"/> and <paramref name="beam"/> are the hull's overall measures.
        /// </summary>
        public static NearPlan Boat(BoatKind kind, float length, float beam)
        {
            var p = new NearPlan("boat-" + kind);
            float L = length, b = beam;
            var skiff = kind == BoatKind.Skiff;
            var deckY = skiff ? .34f : 1.22f;
            var half = b * (skiff ? .32f : .42f);
            // plank seams across the deck slab
            for (var z = -L * .44f + .3f; z < L * .2f; z += .34f)
                p.Quad(NearTone.WoodDark, new NearVec(-half + .05f, deckY + .006f, z), new NearVec(-half + .05f, deckY + .006f, z + .03f), new NearVec(half - .05f, deckY + .006f, z + .03f),
                    new NearVec(half - .05f, deckY + .006f, z), new NearVec(0, 1, 0));
            // bow: a samson post, a fairlead and an anchor on its roller
            p.Box(NearTone.Wood, 0, deckY + .17f, L * .2f - .15f, .2f, .34f, .2f).Box(NearTone.Metal, 0, deckY + .05f, L * .2f + .35f, .5f, .08f, .1f)
                .Beam(NearTone.MetalDark, 0, deckY + .3f, L * .2f + .5f, 0, deckY - .3f, L * .2f + .8f, .05f);
            switch (kind)
            {
                case BoatKind.Sail: Sail(p, L, b, deckY); break;
                case BoatKind.Skiff: Skiff(p, L, b); break;
                case BoatKind.Ferry: FerryBoat(p, L, b, deckY); break;
                default: Workboat(p, L, b, deckY, kind == BoatKind.Lobster); break;
            }
            return p;
        }

        static void Lifelines(NearPlan p, float L, float b, float deckY, float z0, float z1)
        {
            foreach (var sx in new[] { -1, 1 })
            {
                var x = sx * b * .45f;
                for (var z = z0; z <= z1 + .01f; z += 1.4f) p.Beam(NearTone.Metal, x, deckY, z, x, deckY + .6f, z, .035f);
                p.Beam(NearTone.Metal, x, deckY + .6f, z0, x, deckY + .6f, z1, .018f).Beam(NearTone.Metal, x, deckY + .3f, z0, x, deckY + .3f, z1, .015f);
            }
        }

        static void Sail(NearPlan p, float L, float b, float deckY)
        {
            var mastTop = L * .55f + .6f;
            Lifelines(p, L, b, deckY, -L * .36f, L * .3f);
            // pulpit round the bow
            p.Beam(NearTone.Metal, -b * .3f, deckY, L * .34f, -b * .12f, deckY + .6f, L * .44f, .035f).Beam(NearTone.Metal, b * .3f, deckY, L * .34f, b * .12f, deckY + .6f, L * .44f, .035f)
                .Beam(NearTone.Metal, -b * .3f, deckY + .6f, L * .34f, -b * .12f, deckY + .6f, L * .44f, .02f).Beam(NearTone.Metal, b * .3f, deckY + .6f, L * .34f, b * .12f, deckY + .6f, L * .44f, .02f)
                .Beam(NearTone.Metal, -b * .12f, deckY + .6f, L * .44f, b * .12f, deckY + .6f, L * .44f, .02f);
            // the rig: collar, spreaders, shroud plates, winches, a tiller
            var sy = 1.4f + (mastTop - 1.4f) * .55f;
            p.Box(NearTone.Metal, 0, deckY + .15f, L * .1f, .24f, .3f, .24f).Beam(NearTone.Metal, -.7f, sy, L * .1f, .7f, sy, L * .1f, .04f)
                .Beam(NearTone.Metal, -.5f, sy + .3f, L * .1f, .5f, sy + .3f, L * .1f, .03f).Box(NearTone.Metal, 0, mastTop + .05f, L * .1f, .12f, .1f, .12f);
            foreach (var sx in new[] { -1, 1 })
            {
                p.Box(NearTone.MetalDark, sx * b * .44f, deckY + .05f, L * .1f, .1f, .08f, .1f);
                p.Drum(NearTone.Metal, sx * b * .33f, deckY, -L * .3f, .09f, .12f, 8);
                p.Cyl(NearTone.Black, sx * (b * .295f + .005f), 1.45f, -L * .1f - L * .08f, sx * (b * .295f + .03f), 1.45f, -L * .1f - L * .08f, .1f, -1, 8)
                    .Cyl(NearTone.Black, sx * (b * .295f + .005f), 1.45f, -L * .1f + L * .08f, sx * (b * .295f + .03f), 1.45f, -L * .1f + L * .08f, .1f, -1, 8);
            }
            p.Box(NearTone.WoodLight, 0, 1.82f, -L * .1f, .7f, .07f, .6f).Box(NearTone.Wood, 0, 1.78f, -L * .1f + .45f, .76f, .05f, .08f)
                .Beam(NearTone.WoodLight, 0, deckY + .2f, -L * .44f, 0, deckY + .3f, -L * .28f, .06f)
                .Box(NearTone.Red, -b * .4f, deckY + .62f, L * .36f, .16f, .12f, .12f).Box(NearTone.Foliage, b * .4f, deckY + .62f, L * .36f, .16f, .12f, .12f)
                .Cyl(NearTone.Orange, b * .38f, 1.5f, -L * .46f, b * .38f, 1.5f, -L * .46f + .08f, .3f, -1, 8);
            // coaming round the cockpit
            p.Box(NearTone.WoodLight, -b * .36f, deckY + .13f, -L * .3f, .08f, .26f, L * .24f).Box(NearTone.WoodLight, b * .36f, deckY + .13f, -L * .3f, .08f, .26f, L * .24f);
        }

        static void Skiff(NearPlan p, float L, float b)
        {
            // outboard: cowling, leg, gearcase and propeller
            const float oz = -.48f;
            var z = L * oz;
            p.Box(NearTone.Red, 0, 1.25f, z, .4f, .3f, .34f).Beam(NearTone.Metal, 0, .35f, z - .05f, 0, -.45f, z - .05f, .09f).Box(NearTone.Metal, 0, -.5f, z - .05f, .14f, .16f, .34f)
                .Box(NearTone.MetalDark, 0, -.5f, z - .26f, .34f, .04f, .08f).Box(NearTone.MetalDark, 0, -.5f, z - .26f, .04f, .34f, .08f);
            p.Box(NearTone.Red, b * .2f, .56f, -L * .32f, .3f, .3f, .22f).Beam(NearTone.Black, b * .2f, .56f, -L * .32f - .11f, 0, 1.0f, z + .2f, .03f);
            // oars across the thwarts, a painter and a bow eye
            p.Beam(NearTone.WoodLight, -b * .4f, .8f, -L * .1f, b * .42f, .82f, L * .26f, .06f).Beam(NearTone.WoodLight, b * .4f, .8f, -L * .1f, -b * .42f, .82f, L * .26f, .06f);
            p.Beam(NearTone.Rope, 0, .5f, L * .45f, b * .1f, .36f, L * .3f, .03f).Box(NearTone.Metal, 0, .5f, L * .46f, .08f, .08f, .08f);
            // gunwale caps and rowlocks
            foreach (var sx in new[] { -1, 1 })
            {
                p.Beam(NearTone.WoodLight, sx * b * .5f, .5f, -L * .44f, sx * b * .5f, .5f, L * .1f, .06f);
                foreach (var rz in new[] { -L * .1f, L * .1f }) p.Box(NearTone.Metal, sx * (b * .5f + .02f), .56f, rz, .06f, .1f, .06f);
            }
        }

        static void Workboat(NearPlan p, float L, float b, float deckY, bool lobster)
        {
            var cz = lobster ? L * .1f : -L * .06f; var cl = L * .3f;
            float front = cz + cl / 2, back = cz - cl / 2;
            // wheelhouse glazing: a front pane and frames round the side band the far level draws
            p.Box(NearTone.Glass, 0, 2.17f, front + .012f, b * .5f, .4f, .03f);
            p.Box(NearTone.Trim, 0, 2.43f, front + .03f, b * .56f, .07f, .06f).Box(NearTone.Trim, 0, 1.91f, front + .03f, b * .56f, .07f, .06f)
                .Box(NearTone.Trim, -b * .25f, 2.17f, front + .03f, .06f, .5f, .06f).Box(NearTone.Trim, b * .25f, 2.17f, front + .03f, .06f, .5f, .06f).Box(NearTone.Trim, 0, 2.17f, front + .03f, .05f, .5f, .06f);
            foreach (var sx in new[] { -1, 1 })
            {
                var x = sx * (b * .34f + .012f);
                p.Box(NearTone.Trim, x, 2.45f, cz, .04f, .07f, cl * .88f).Box(NearTone.Trim, x, 1.89f, cz, .04f, .07f, cl * .88f);
                for (var z = back + cl * .1f; z <= front - cl * .1f + .01f; z += cl * .2f) p.Box(NearTone.Trim, x, 2.17f, z, .04f, .56f, .06f);
                // a grab rail along the cabin side and a door seam aft
                p.Beam(NearTone.Metal, sx * (b * .33f + .05f), 1.62f, back + cl * .1f, sx * (b * .33f + .05f), 1.62f, front - cl * .15f, .035f)
                    .Box(NearTone.Black, x, 1.58f, back + cl * .35f, .02f, .62f, .02f).Box(NearTone.Black, x, 1.58f, back + cl * .12f, .02f, .62f, .02f);
            }
            // cabin roof: radar dome, two aerials, an exhaust and the navigation lights
            p.Drum(NearTone.Cream, 0, 2.7f, cz + cl * .2f, .22f, .2f, 8, .1f).Beam(NearTone.Metal, -b * .26f, 2.7f, cz - cl * .3f, -b * .26f, 4.0f, cz - cl * .3f, .03f)
                .Beam(NearTone.Metal, b * .24f, 2.7f, cz - cl * .38f, b * .24f, 3.6f, cz - cl * .38f, .025f).Drum(NearTone.MetalDark, b * .22f, 2.7f, cz - cl * .1f, .1f, .7f, 6)
                .Box(NearTone.Red, -b * .32f, 2.74f, front - .1f, .12f, .1f, .12f).Box(NearTone.Foliage, b * .32f, 2.74f, front - .1f, .12f, .1f, .12f);
            // working gear aft: a hauler and its sheave, rope, a bait box and buoys hung on the cabin's back wall
            p.Cyl(NearTone.Metal, b * .34f, deckY + .9f, back - .25f, b * .34f + .16f, deckY + .9f, back - .25f, .2f, -1, 8).Beam(NearTone.MetalDark, b * .34f, deckY, back - .25f, b * .34f, deckY + .9f, back - .25f, .08f)
                .Drum(NearTone.Rope, b * .3f, deckY, -L * .2f, .3f, .14f, 8).Box(NearTone.Cream, -b * .25f, deckY + .18f, -L * .4f, .5f, .36f, .4f);
            var buoy = new[] { NearTone.Red, NearTone.Yellow, NearTone.Orange, NearTone.Cream, NearTone.Red };
            for (var k = 0; k < (lobster ? 5 : 3); k++)
                p.Drum(buoy[k], -b * .26f + k * b * .13f, 1.75f, back - .14f, .13f, .26f, 6, .09f).Beam(NearTone.Rope, -b * .26f + k * b * .13f, 2.05f, back - .08f, -b * .26f + k * b * .13f, 2.3f, back - .02f, .02f);
            // block at the top of the A-frame, and its guard
            p.Box(NearTone.Metal, 0, 3.45f, -L * .34f, .14f, .14f, .14f).Box(NearTone.WoodDark, -b * .25f, 1.36f, -L * .2f, .5f, .25f, .4f);
        }

        static void FerryBoat(NearPlan p, float L, float b, float deckY)
        {
            var cl = L * .65f; const float cz = 0f;
            // frames and pillars on the lower glazing, both sides
            foreach (var sx in new[] { -1, 1 })
            {
                var x = sx * (b * .34f + .012f);
                p.Box(NearTone.Trim, x, 2.45f, cz, .04f, .07f, cl * .86f).Box(NearTone.Trim, x, 1.89f, cz, .04f, .07f, cl * .86f);
                for (var z = -cl * .42f; z <= cl * .42f + .01f; z += 1.1f) p.Box(NearTone.Trim, x, 2.17f, z, .04f, .56f, .07f);
                // upper deck windows
                p.Box(NearTone.Trim, sx * (b * .29f + .012f), 3.3f, 0, .04f, .06f, L * .34f);
                for (var z = -L * .16f; z <= L * .16f + .01f; z += 1.0f) p.Box(NearTone.Trim, sx * (b * .29f + .012f), 3.1f, z, .04f, .36f, .06f);
                // handrail on the stanchions and a life ring
                p.Beam(NearTone.Trim, sx * b * .45f, 1.85f, -L * .36f, sx * b * .45f, 1.85f, L * .36f, .045f)
                    .Beam(NearTone.Trim, sx * b * .45f, 1.5f, -L * .36f, sx * b * .45f, 1.5f, L * .36f, .03f)
                    .Cyl(NearTone.Orange, sx * (b * .33f + .02f), 2.1f, -L * .12f, sx * (b * .33f + .09f), 2.1f, -L * .12f, .3f, -1, 8);
            }
            // funnel cap and bands, upper-deck roof lights, a mast with a radar bar
            p.Box(NearTone.Black, 0, 3.6f, -L * .26f, .7f, .1f, .8f).Box(NearTone.Red, 0, 3.3f, -L * .26f, .68f, .16f, .78f)
                .Beam(NearTone.MetalDark, 0, 3.52f, L * .06f, 0, 4.6f, L * .06f, .07f).Box(NearTone.Black, 0, 4.4f, L * .06f, .08f, .07f, 1.0f)
                .Box(NearTone.Red, -b * .27f, 3.56f, L * .18f, .14f, .12f, .12f).Box(NearTone.Foliage, b * .27f, 3.56f, L * .18f, .14f, .12f, .12f);
            // benches on the foredeck and aft
            foreach (var z in new[] { L * .38f, -L * .42f })
                p.Box(NearTone.Wood, 0, deckY + .35f, z, b * .5f, .06f, .4f).Box(NearTone.WoodDark, -b * .22f, deckY + .17f, z, .06f, .34f, .36f).Box(NearTone.WoodDark, b * .22f, deckY + .17f, z, .06f, .34f, .36f);
        }

        // ---------------------------------------------------------------- the hauled-out boat

        /// <summary>The boat on blocks in the yard: props, a ladder, rails, a rudder and propeller, window frames on the cabin.</summary>
        public static NearPlan HauledBoat()
        {
            var p = new NearPlan("hauled-boat");
            // hull: waterline y 1.4, freeboard 1.6 (top y 3.0), draft 0.9 (keel y 0.5), 11 long, 3.6 beam; cabin box 2.4 x 1.6 x 3.4 at (0, 3.4, -1.5)
            foreach (var sx in new[] { -1, 1 })
            {
                foreach (var z in new[] { -3.5f, -1.0f, 2.0f })
                    p.Beam(NearTone.Metal, sx * 1.5f, 1.7f, z, sx * 1.88f, .08f, z, .1f, .1f).Box(NearTone.Wood, sx * 1.88f, .04f, z, .24f, .08f, .5f);
                // rail on stanchions along the deck edge
                for (var z = -4.6f; z <= 4f; z += 1.4f) p.Beam(NearTone.Metal, sx * 1.62f, 3.0f, z, sx * 1.62f, 3.65f, z, .04f);
                p.Beam(NearTone.Metal, sx * 1.62f, 3.65f, -4.6f, sx * 1.62f, 3.65f, 4.0f, .025f).Beam(NearTone.Metal, sx * 1.62f, 3.3f, -4.6f, sx * 1.62f, 3.3f, 4.0f, .02f);
                // cabin windows, three a side, in frames
                for (var k = 0; k < 3; k++)
                {
                    var z = -2.6f + k * 1.1f;
                    p.Box(NearTone.Glass, sx * 1.205f, 3.5f, z, .03f, .5f, .8f).Box(NearTone.Trim, sx * 1.21f, 3.79f, z, .04f, .06f, .92f).Box(NearTone.Trim, sx * 1.21f, 3.21f, z, .04f, .06f, .92f)
                        .Box(NearTone.Trim, sx * 1.21f, 3.5f, z - .46f, .04f, .6f, .06f).Box(NearTone.Trim, sx * 1.21f, 3.5f, z + .46f, .04f, .6f, .06f);
                }
            }
            p.Box(NearTone.Glass, 0, 3.5f, .215f, 1.9f, .5f, .03f).Box(NearTone.Trim, 0, 3.79f, .22f, 2f, .06f, .04f).Box(NearTone.Trim, 0, 3.21f, .22f, 2f, .06f, .04f);
            // rudder, propeller and a boarding ladder at the stern
            p.Box(NearTone.Black, 0, .9f, -5.55f, .1f, 1.1f, .6f).Box(NearTone.Metal, 0, .7f, -5.2f, .8f, .06f, .1f).Box(NearTone.Metal, 0, .7f, -5.2f, .06f, .8f, .1f)
                .Cyl(NearTone.MetalDark, 0, .7f, -5.0f, 0, .7f, -5.3f, .08f, -1, 6);
            p.Beam(NearTone.Metal, -.3f, 3.0f, -5.75f, -.3f, .1f, -5.9f, .06f).Beam(NearTone.Metal, .3f, 3.0f, -5.75f, .3f, .1f, -5.9f, .06f);
            for (var y = .4f; y < 2.9f; y += .4f) p.Box(NearTone.Wood, 0, y, -5.75f - (y / 2.9f) * .15f, .6f, .05f, .06f);
            return p;
        }
    }
}
