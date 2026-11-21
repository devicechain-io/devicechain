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
        /// A complete low-poly hatchback that REPLACES the far level's two stacked boxes (the manager hides those while this is shown): a hull with
        /// a sloped bonnet and tail, a cabin narrower at the roof with a raked windscreen and rear window and glass in a body-coloured frame, tyres
        /// under dark arch trim with hubs, bumpers, lamps, plates, mirrors, door lines and handles. Same footprint, colour and place as the far
        /// body; everything stays within 0.06 m of the sides and 0.1 m of the ends, which is the footprint a parked car reserves.
        /// </summary>
        public static NearPlan Car(CarSpec s)
        {
            var p = new NearPlan("car");
            var f = s.Front; float bw = s.BodyW / 2, bl = s.BodyL / 2;
            float bb = s.BodyBottom, bt = s.BodyTop, belt = bt - .22f, roof = s.GlassTop + .09f, cw = s.CabinW / 2;
            float zf = f * bl, zr = -f * bl, zw = s.CabinZ + f * s.CabinL / 2, zc = s.CabinZ - f * s.CabinL / 2;
            NearVec V(float x, float y, float z) => new NearVec(x, y, z);
            // hull below the belt line, with the sills a touch narrower than the flanks
            p.Hex(NearTone.Paint, new[] { V(-bw + .03f, bb, zr), V(bw - .03f, bb, zr), V(bw - .03f, bb, zf), V(-bw + .03f, bb, zf),
                V(-bw, belt, zr), V(bw, belt, zr), V(bw, belt, zf), V(-bw, belt, zf) });
            // bonnet: slopes up from the nose to the foot of the windscreen
            p.Hex(NearTone.Paint, new[] { V(-bw, belt, zw), V(bw, belt, zw), V(bw, belt, zf), V(-bw, belt, zf),
                V(-bw + .05f, bt, zw), V(bw - .05f, bt, zw), V(bw - .1f, bt - .13f, zf), V(-bw + .1f, bt - .13f, zf) });
            // shoulder and tail: level to the cabin, then falling a little to the back panel
            p.Hex(NearTone.Paint, new[] { V(-bw, belt, zr), V(bw, belt, zr), V(bw, belt, zw), V(-bw, belt, zw),
                V(-bw + .1f, bt - .06f, zr - f * -.08f), V(bw - .1f, bt - .06f, zr - f * -.08f), V(bw - .05f, bt, zw), V(-bw + .05f, bt, zw) });
            // cabin: wide at the base, narrow at the roof, windscreen and rear window raked
            var rf = zw - f * .62f; var rr = zc + f * .5f;
            var cabin = new[] { V(-cw, bt, zc), V(cw, bt, zc), V(cw, bt, zw), V(-cw, bt, zw),
                V(-cw + .15f, roof, rr), V(cw - .15f, roof, rr), V(cw - .15f, roof, rf), V(-cw + .15f, roof, rf) };
            p.Hex(NearTone.Paint, cabin);
            var mid = V(0, (bt + roof) / 2, s.CabinZ);
            p.Glazed(cabin[3], cabin[2], cabin[6], cabin[7], mid, .13f, .012f);   // windscreen
            p.Glazed(cabin[0], cabin[1], cabin[5], cabin[4], mid, .15f, .012f);   // rear window
            p.Glazed(cabin[0], cabin[3], cabin[7], cabin[4], mid, .11f, .012f);   // left side
            p.Glazed(cabin[1], cabin[2], cabin[6], cabin[5], mid, .11f, .012f);   // right side
            // bumpers, lamps, grille and plates
            p.Box(NearTone.Black, 0, bb + .2f, f * (bl - .02f), s.BodyW - .08f, .22f, .13f).Box(NearTone.Black, 0, bb + .2f, -f * (bl - .02f), s.BodyW - .08f, .22f, .13f);
            p.Box(NearTone.Black, 0, bt - .3f, f * (bl + .012f), .95f, .16f, .03f).Box(NearTone.Metal, 0, bt - .3f, f * (bl + .03f), .9f, .02f, .02f)
                .Box(NearTone.Cream, -.62f, bt - .27f, f * (bl + .012f), .36f, .13f, .03f).Box(NearTone.Cream, .62f, bt - .27f, f * (bl + .012f), .36f, .13f, .03f)
                .Box(NearTone.Orange, -.8f, bt - .47f, f * (bl + .012f), .12f, .07f, .03f).Box(NearTone.Orange, .8f, bt - .47f, f * (bl + .012f), .12f, .07f, .03f);
            p.Box(NearTone.Red, -.64f, bt - .25f, -f * (bl + .012f), .36f, .16f, .03f).Box(NearTone.Red, .64f, bt - .25f, -f * (bl + .012f), .36f, .16f, .03f)
                .Box(NearTone.Cream, 0, bb + .38f, -f * (bl + .016f), .5f, .11f, .02f);
            foreach (var sx in new[] { -1, 1 })
            {
                var x = sx * (bw + .008f);
                // door lines, handles, a belt strip along the flank
                foreach (var z in new[] { s.CabinZ + f * (s.CabinL / 2 - .05f), s.CabinZ - f * .05f, s.CabinZ - f * (s.CabinL / 2 - .05f) })
                    p.Box(NearTone.Black, x, bb + (belt - bb) / 2 + .1f, z, .016f, belt - bb - .05f, .022f);
                p.Box(NearTone.Metal, x + sx * .012f, belt - .02f, s.CabinZ + f * .55f, .03f, .045f, .15f).Box(NearTone.Metal, x + sx * .012f, belt - .02f, s.CabinZ - f * .5f, .03f, .045f, .15f);
                p.Box(NearTone.Metal, x + sx * .004f, belt + .02f, 0, .012f, .03f, s.BodyL - .7f);
                // mirror on a short arm at the foot of the windscreen
                var mz = zw - f * .12f;
                p.Beam(NearTone.Black, sx * (cw - .02f), bt + .12f, mz, sx * (cw + .07f), bt + .14f, mz, .035f)
                    .Box(NearTone.Paint, sx * (cw + .11f), bt + .14f, mz, .08f, .11f, .16f);
                // arch trim: a dark liner behind each tyre, which stands proud of the flank with a hub
                foreach (var wz in new[] { -s.WheelZ, s.WheelZ })
                {
                    var ax = sx * (bw + .005f);
                    p.Cyl(NearTone.Black, ax, s.WheelR + .02f, wz, ax + sx * .012f, s.WheelR + .02f, wz, s.WheelR + .11f, -1, 12);
                    p.Cyl(NearTone.Black, sx * (bw - .17f), s.WheelR, wz, sx * (bw + .03f), s.WheelR, wz, s.WheelR, -1, 12);
                    p.Cyl(NearTone.Metal, sx * (bw + .03f), s.WheelR, wz, sx * (bw + .045f), s.WheelR, wz, s.WheelR * .56f, -1, 8);
                }
            }
            // roof rails, a small fin and a lip spoiler at the tail
            foreach (var sx in new[] { -1, 1 }) p.Box(NearTone.Black, sx * (cw - .22f), roof + .03f, s.CabinZ, .05f, .04f, s.CabinL - .9f);
            p.Box(NearTone.Black, 0, roof + .04f, rr + f * .25f, .08f, .06f, .22f).Box(NearTone.Paint, 0, bt + .02f, zr + f * .12f, s.CabinW - .3f, .05f, .14f);
            p.Box(NearTone.Black, 0, bb - .02f, 0, s.BodyW - .14f, .08f, s.BodyL - .3f);
            return p;
        }

        // ---------------------------------------------------------------- the service truck

        /// <summary>
        /// The flood-response truck in its rig frame (+Z forward, origin on the deck): a complete low-poly service truck that REPLACES the far
        /// level's boxes while it is shown (the manager hides those; the far light bar stays, because the platform's reports switch it). A cab
        /// with a raked windscreen and glass in body-coloured frames over a grille, lamps and a bumper; external mudguards over tyres with
        /// rims and hubs; a chassis with rails, a tank and an exhaust; and a service body of two side cabinets with compartment doors round an
        /// open bed that carries the flood kit, under a ladder rack. Same footprint, stance and colours as the far boxes.
        /// </summary>
        public static NearPlan Truck()
        {
            var p = new NearPlan("truck");
            NearVec V(float x, float y, float z) => new NearVec(x, y, z);
            const float hw = 1.125f, bw = 1.15f;                       // half widths: cab, service body
            const float floor = .78f, belt = 1.52f, roof = 2.5f;       // cab heights
            const float zr = .66f, zf = 2.55f;                         // cab rear wall and nose

            // ---- chassis: rails, cross members, axles, a tank, a battery box and the exhaust
            foreach (var sx in new[] { -1, 1 }) p.Box(NearTone.MetalDark, sx * .45f, .70f, -.05f, .12f, .2f, 5.2f);
            foreach (var z in new[] { -2.45f, -1.2f, 0f, 1.2f }) p.Box(NearTone.Black, 0, .70f, z, .9f, .1f, .1f);
            foreach (var wz in new[] { -1.9f, 1.9f }) p.Cyl(NearTone.MetalDark, -1.06f, .55f, wz, 1.06f, .55f, wz, .085f, -1, 6);
            p.Box(NearTone.MetalDark, 0, .55f, -1.9f, .34f, .3f, .34f);
            p.Cyl(NearTone.Metal, -.92f, .6f, -.5f, -.92f, .6f, .5f, .23f, -1, 10)
                .Box(NearTone.Black, -.92f, .6f, -.25f, .5f, .5f, .04f).Box(NearTone.Black, -.92f, .6f, .25f, .5f, .5f, .04f);
            p.Box(NearTone.MetalDark, .92f, .64f, 0, .38f, .36f, .8f).Box(NearTone.Black, 1.115f, .64f, 0, .012f, .26f, .66f);
            p.Cyl(NearTone.MetalDark, .78f, .46f, -.6f, .78f, .46f, -2.6f, .05f, -1, 6);

            // ---- wheels: a tyre, a rim, a dark dish and a hub; an external mudguard over each, with a flap behind
            foreach (var sx in new[] { -1, 1 })
                foreach (var wz in new[] { -1.9f, 1.9f })
                {
                    p.Cyl(NearTone.Black, sx * 1.06f, .55f, wz, sx * 1.36f, .55f, wz, .53f, -1, 14);
                    p.Cyl(NearTone.Metal, sx * 1.36f, .55f, wz, sx * 1.372f, .55f, wz, .33f, -1, 12);
                    p.Cyl(NearTone.MetalDark, sx * 1.372f, .55f, wz, sx * 1.378f, .55f, wz, .23f, -1, 10);
                    p.Cyl(NearTone.Metal, sx * 1.378f, .55f, wz, sx * 1.43f, .55f, wz, .12f, .07f, 8);
                    // the guard: a top plate, a sloped plate before and behind, and a lip on the outside
                    p.Box(NearTone.Black, sx * 1.22f, 1.15f, wz, .36f, .05f, .86f);
                    p.Beam(NearTone.Black, sx * 1.22f, 1.15f, wz + .42f, sx * 1.22f, .86f, wz + .70f, .36f, .05f);
                    p.Beam(NearTone.Black, sx * 1.22f, 1.15f, wz - .42f, sx * 1.22f, .86f, wz - .70f, .36f, .05f);
                    p.Box(NearTone.MetalDark, sx * 1.395f, 1.12f, wz, .02f, .1f, .86f);
                    p.Box(NearTone.Black, sx * 1.22f, .56f, wz - .72f, .34f, .52f, .03f);
                }

            // ---- cab: the body below the belt, the glasshouse above it narrowing to the roof, the screen raked back
            p.Hex(NearTone.Paint, new[] { V(-hw + .03f, floor, zr), V(hw - .03f, floor, zr), V(hw - .03f, floor, zf), V(-hw + .03f, floor, zf),
                V(-hw, belt, zr), V(hw, belt, zr), V(hw, belt, zf - .03f), V(-hw, belt, zf - .03f) });
            var house = new[] { V(-hw, belt, zr), V(hw, belt, zr), V(hw, belt, zf - .03f), V(-hw, belt, zf - .03f),
                V(-hw + .1f, roof, zr + .04f), V(hw - .1f, roof, zr + .04f), V(hw - .1f, roof, zf - .44f), V(-hw + .1f, roof, zf - .44f) };
            p.Hex(NearTone.Paint, house);
            var mid = V(0, (belt + roof) / 2, (zr + zf) / 2);
            p.Glazed(house[3], house[2], house[6], house[7], mid, .09f, .012f);   // windscreen
            p.Glazed(house[0], house[1], house[5], house[4], mid, .32f, .012f);   // rear window
            p.Glazed(house[0], house[3], house[7], house[4], mid, .15f, .012f);   // left side
            p.Glazed(house[1], house[2], house[6], house[5], mid, .15f, .012f);   // right side

            // wipers lie on the screen, which leans back 0.41 m over its 0.98 m of height
            float Screen(float y) => zf - .03f - (y - belt) * .418f + .03f;
            p.Beam(NearTone.Black, -.12f, belt + .1f, Screen(belt + .1f), -.72f, belt + .52f, Screen(belt + .52f), .03f);
            p.Beam(NearTone.Black, .6f, belt + .1f, Screen(belt + .1f), 0f, belt + .52f, Screen(belt + .52f), .03f);
            // roof: the light bar's base, a visor over the screen, three marker lamps and an aerial
            p.Box(NearTone.Black, 0, roof + .005f, 1.6f, 1.56f, .03f, .38f).Box(NearTone.Black, 0, roof - .03f, zf - .32f, 2.0f, .045f, .28f);
            foreach (var x in new[] { -.6f, 0f, .6f }) p.Box(NearTone.Orange, x, roof + .03f, zf - .56f, .12f, .05f, .08f);
            p.Beam(NearTone.Black, .82f, roof, .95f, .82f, roof + .75f, .82f, .022f);
            // nose: grille with slats, lamp housings, lamps and indicators
            p.Box(NearTone.Black, 0, 1.15f, zf + .012f, 1.16f, .44f, .03f);
            for (var k = 0; k < 4; k++) p.Box(NearTone.Metal, 0, 1.0f + k * .1f, zf + .032f, 1.08f, .028f, .02f);
            foreach (var sx in new[] { -1, 1 })
                p.Box(NearTone.Black, sx * .84f, 1.15f, zf + .012f, .46f, .32f, .03f).Box(NearTone.Cream, sx * .8f, 1.17f, zf + .032f, .3f, .2f, .02f)
                    .Box(NearTone.Orange, sx * 1.0f, 1.17f, zf + .032f, .08f, .2f, .02f);
            // bumper with corner steps, fog lamps, tow eyes and a plain plate
            p.Box(NearTone.Black, 0, .78f, zf + .1f, 2.38f, .26f, .2f).Box(NearTone.MetalDark, 0, .92f, zf + .1f, 2.2f, .03f, .16f)
                .Box(NearTone.Cream, 0, .8f, zf + .205f, .5f, .12f, .02f);
            foreach (var sx in new[] { -1, 1 })
                p.Box(NearTone.Cream, sx * .86f, .78f, zf + .205f, .2f, .1f, .02f).Box(NearTone.Red, sx * .5f, .68f, zf + .205f, .07f, .07f, .03f);
            foreach (var sx in new[] { -1, 1 })
            {
                // door: seams, a handle, a grab rail behind it, a step, and the mirror on its arm
                var x = sx * (hw + .006f);
                p.Box(NearTone.Black, x, 1.18f, .98f, .016f, .7f, .022f).Box(NearTone.Black, x, 1.18f, 2.24f, .016f, .7f, .022f)
                    .Box(NearTone.Black, x, .84f, 1.61f, .016f, .022f, 1.28f)
                    .Box(NearTone.Metal, x + sx * .014f, 1.38f, 2.08f, .03f, .05f, .2f)
                    .Beam(NearTone.Metal, sx * (hw + .03f), 1.0f, .8f, sx * (hw + .03f), 2.1f, .8f, .04f);
                p.Box(NearTone.MetalDark, sx * 1.26f, .56f, 1.02f, .26f, .05f, .5f).Box(NearTone.Black, sx * 1.15f, .66f, 1.02f, .04f, .2f, .5f);
                p.Beam(NearTone.Black, sx * 1.1f, 2.0f, 2.2f, sx * 1.4f, 2.0f, 2.06f, .05f)
                    .Box(NearTone.Black, sx * 1.42f, 2.0f, 2.05f, .12f, .3f, .2f).Box(NearTone.Glass, sx * 1.42f, 2.0f, 1.942f, .09f, .25f, .015f);
                // the fleet stripe along the cab's flank
                p.Box(NearTone.Accent, sx * (hw + .008f), 1.2f, 1.6f, .016f, .2f, 1.84f).Box(NearTone.AccentLight, sx * (hw + .008f), 1.37f, 1.6f, .016f, .08f, 1.84f);
            }

            // ---- service body: two side cabinets round an open bed, a headboard behind the cab and a tailgate
            foreach (var sx in new[] { -1, 1 })
            {
                p.Box(NearTone.Metal, sx * .87f, 1.2f, -1.03f, .56f, 1.2f, 3.06f).Box(NearTone.MetalDark, sx * .87f, 1.81f, -1.03f, .5f, .02f, 3.0f);
                var x = sx * (bw + .006f);
                // three compartments a side: the middle one is short, over the wheel
                foreach (var z in new[] { -2.5f, -1.45f, -.45f, .44f }) p.Box(NearTone.Black, x, 1.2f, z, .014f, 1.1f, .022f);
                p.Box(NearTone.Black, x, 1.74f, -1.03f, .014f, .022f, 2.96f).Box(NearTone.Black, x, .66f, -1.03f, .014f, .022f, 2.96f)
                    .Box(NearTone.Black, x, 1.28f, -1.95f, .014f, .022f, 1.03f);
                foreach (var z in new[] { -1.95f, -.95f, 0f })
                    p.Box(NearTone.TrimDark, x + sx * .012f, z < -1.5f ? 1.5f : 1.25f, z + .32f, .024f, .07f, .16f)
                        .Box(NearTone.MetalDark, x + sx * .006f, 1.6f, z - .44f, .02f, .1f, .035f).Box(NearTone.MetalDark, x + sx * .006f, z < -1.5f ? 1.38f : .85f, z - .44f, .02f, .1f, .035f);
                // the wheel arch cut into the rear compartment, and the stripe
                p.Box(NearTone.Black, x, .93f, -1.9f, .014f, .62f, 1.0f);
                p.Box(NearTone.Accent, x + sx * .004f, 1.2f, -.52f, .016f, .2f, 1.84f).Box(NearTone.AccentLight, x + sx * .004f, 1.37f, -.52f, .016f, .08f, 1.84f);
                // tail lamps and a reflector on the cabinet's end
                p.Box(NearTone.Black, sx * .87f, .96f, -2.565f, .4f, .46f, .02f).Box(NearTone.Red, sx * .87f, 1.06f, -2.58f, .3f, .14f, .02f)
                    .Box(NearTone.Orange, sx * .87f, .92f, -2.58f, .3f, .09f, .02f).Box(NearTone.Cream, sx * .87f, .8f, -2.58f, .3f, .08f, .02f);
            }
            p.Box(NearTone.MetalDark, 0, 1.0f, -1.03f, 1.2f, .06f, 3.06f);
            p.Box(NearTone.Metal, 0, 1.45f, .47f, 1.2f, .9f, .06f).Box(NearTone.Black, 0, 2.02f, .47f, 2.2f, .05f, .05f)
                .Box(NearTone.Black, -.5f, 1.92f, .47f, .04f, .22f, .04f).Box(NearTone.Black, .5f, 1.92f, .47f, .04f, .22f, .04f);
            foreach (var sx in new[] { -1, 1 }) p.Box(NearTone.Cream, sx * .8f, 2.1f, .5f, .18f, .11f, .09f);
            p.Box(NearTone.Metal, 0, 1.25f, -2.535f, 1.2f, .46f, .05f).Box(NearTone.Black, 0, 1.3f, -2.57f, .3f, .05f, .03f);
            // rear bumper with chevrons, a step, a plate and the tow hitch
            p.Box(NearTone.Black, 0, .68f, -2.66f, 2.4f, .2f, .18f).Box(NearTone.MetalDark, 0, .5f, -2.74f, 1.2f, .05f, .26f)
                .Box(NearTone.Cream, 0, 1.0f - .12f, -2.57f, .5f, .12f, .02f).Cyl(NearTone.Metal, 0, .58f, -2.75f, 0, .58f, -2.95f, .055f, -1, 8);
            for (var k = 0; k < 10; k++)
                p.Box(k % 2 == 0 ? NearTone.Red : NearTone.Yellow, -1.0f + k * .222f, .68f, -2.755f, .2f, .16f, .012f);

            // ---- the ladder rack over the bed, and a ladder on it
            foreach (var sx in new[] { -1, 1 })
            {
                p.Beam(NearTone.Metal, sx * .8f, 1.8f, -2.42f, sx * .8f, 2.3f, -2.42f, .07f).Beam(NearTone.Metal, sx * .8f, 1.8f, .32f, sx * .8f, 2.3f, .32f, .07f)
                    .Beam(NearTone.Metal, sx * .8f, 2.3f, -2.47f, sx * .8f, 2.3f, .42f, .065f)
                    .Beam(NearTone.Metal, sx * .8f, 1.85f, -2.42f, sx * .8f, 2.25f, -2.0f, .04f);
            }
            foreach (var z in new[] { -2.42f, -1.05f, .32f }) p.Beam(NearTone.Metal, -.8f, 2.3f, z, .8f, 2.3f, z, .06f);
            foreach (var x in new[] { .28f, .66f }) p.Beam(NearTone.Trim, x, 2.37f, -2.6f, x, 2.37f, .5f, .05f, .08f);
            for (var k = 0; k < 9; k++) p.Beam(NearTone.TrimDark, .28f, 2.37f, -2.45f + k * .35f, .66f, 2.37f, -2.45f + k * .35f, .035f);
            p.Cyl(NearTone.Orange, -.5f, 2.42f, -2.5f, -.5f, 2.42f, .4f, .075f, -1, 6);

            // ---- the flood kit in the bed: sandbags, a portable pump on its frame, a hose reel, cans and stacked cones
            for (var k = 0; k < 3; k++)
                p.Box(NearTone.Rope, -.28f + (k % 2) * .5f, 1.13f, -2.2f + (k / 2) * .4f, .46f, .2f, .32f)
                    .Box(NearTone.Rope, -.1f + (k % 2) * .3f - .1f, 1.32f, -2.1f + (k / 2) * .3f, .44f, .18f, .3f);
            p.Box(NearTone.Red, -.2f, 1.3f, -1.3f, .56f, .42f, .5f).Drum(NearTone.MetalDark, -.2f, 1.51f, -1.3f, .11f, .3f, 6)
                .Box(NearTone.Black, -.2f, 1.06f, -1.3f, .64f, .06f, .58f).Cyl(NearTone.Metal, .08f, 1.3f, -1.3f, .3f, 1.3f, -1.3f, .09f, -1, 6);
            p.Cyl(NearTone.Yellow, .3f, 1.32f, -.6f, .52f, 1.32f, -.6f, .3f, -1, 10).Cyl(NearTone.MetalDark, .26f, 1.32f, -.6f, .56f, 1.32f, -.6f, .09f, -1, 6)
                .Box(NearTone.Black, .41f, 1.06f, -.6f, .4f, .06f, .5f);
            p.Box(NearTone.Red, -.38f, 1.22f, -.5f, .2f, .38f, .32f).Box(NearTone.Red, -.38f, 1.22f, -.12f, .2f, .38f, .32f)
                .Box(NearTone.Black, -.38f, 1.43f, -.5f, .08f, .04f, .1f).Box(NearTone.Black, -.38f, 1.43f, -.12f, .08f, .04f, .1f);
            for (var k = 0; k < 3; k++) p.Drum(NearTone.Orange, .1f, 1.07f + k * .1f, .12f, .15f, .42f, 8, .04f);
            p.Box(NearTone.Black, .1f, 1.05f, .12f, .34f, .03f, .34f);
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
