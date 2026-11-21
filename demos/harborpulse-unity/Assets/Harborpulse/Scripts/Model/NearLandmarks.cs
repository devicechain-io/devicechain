// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// The near sets of the three metered landmark buildings (the quayside lofts, the ferry terminal, the works depot) and the berthed ferry.
    /// Their far models are the plain boxes of the building builder: walls, a flat or pitched roof, and a row of flat window panes on the
    /// harbour-facing +Z wall (so that wall, local +Z, is what the near level dresses most).
    /// </summary>
    public static class NearLandmarks
    {
        public const float LoftsW = 38, LoftsD = 18, LoftsH = 17, TerminalW = 56, TerminalD = 22, TerminalH = 8, DepotW = 28, DepotD = 16, DepotH = 7;

        static readonly NearBuildings.WindowStyle Brickwork = new NearBuildings.WindowStyle(NearTone.Stone, false, false, 2);
        static readonly NearBuildings.WindowStyle Cladding = new NearBuildings.WindowStyle(NearTone.Trim, false, false, 2);

        /// <summary>The pane centres of the building builder's window rows: x from -w/2+2.2 every 3.4 while x &lt; w/2-1.5, the pane 0.7 right of x.</summary>
        static void PaneRow(Action<float> each, float w)
        {
            for (var x = -w / 2 + 2.2f; x < w / 2 - 1.5f; x += 3.4f) each(x + .7f);
        }

        static void Downpipes(NearPlan p, float w, float d, float h)
        {
            foreach (var sx in new[] { -1, 1 })
                p.Beam(NearTone.Metal, sx * (w / 2 - .3f), h - .3f, d / 2 + .09f, sx * (w / 2 - .3f), .08f, d / 2 + .09f, .12f)
                    .Beam(NearTone.Metal, sx * (w / 2 - .3f), h - .3f, -d / 2 - .09f, sx * (w / 2 - .3f), .08f, -d / 2 - .09f, .12f);
        }

        // ---------------------------------------------------------------- the lofts

        public static NearPlan Lofts()
        {
            const float w = LoftsW, d = LoftsD, h = LoftsH;
            var p = new NearPlan("lofts");
            var rear = NearFacade.Rear(w, d); var front = NearFacade.Front(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            // brick coursing on every wall
            NearBuildings.Courses(p, rear, NearTone.Mortar, w, .6f, h - .4f, .36f, .035f); NearBuildings.Courses(p, front, NearTone.Mortar, w, .6f, h - .4f, .36f, .035f);
            NearBuildings.Courses(p, left, NearTone.Mortar, d, .6f, h - .4f, .36f, .035f); NearBuildings.Courses(p, right, NearTone.Mortar, d, .6f, h - .4f, .36f, .035f);
            // every pane gets a surround, mullion and transom
            PaneRow(u =>
            {
                for (var f = 0; f < 5; f++) NearBuildings.WindowFrame(p, rear, u, 1.7f + f * 3.2f, 1.4f, 1.5f, Brickwork);
            }, w);
            // pilasters between the window columns (leaving the entrance bay clear) and a string course over each floor
            for (var k = 0; k < 10; k++)
            {
                var u = -14.4f + 3.4f * k;
                if (Math.Abs(u - 2.6f) < 1.2f) continue;
                p.Fb(rear, NearTone.BrickLight, u, h / 2 - .2f, .06f, .4f, h - .4f, .12f);
            }
            foreach (var y in new[] { 6.5f, 9.7f, 12.9f }) p.Fb(rear, NearTone.Stone, 0, y, .07f, w + .1f, .18f, .14f);
            // the entrance bay: double doors in a frame, glazed lights, a transom, a shallow hood on two brackets
            const float eu = 2.6f;
            p.Fb(rear, NearTone.Trim, eu - .72f, 1.3f, .1f, .12f, 2.6f, .18f).Fb(rear, NearTone.Trim, eu + .72f, 1.3f, .1f, .12f, 2.6f, .18f).Fb(rear, NearTone.Trim, eu, 2.62f, .1f, 1.56f, .14f, .18f)
                .Fb(rear, NearTone.Door, eu - .33f, 1.2f, .08f, .6f, 2.4f, .06f).Fb(rear, NearTone.Door, eu + .33f, 1.2f, .08f, .6f, 2.4f, .06f)
                .Fb(rear, NearTone.Glass, eu - .33f, 1.55f, .115f, .4f, 1.3f, .03f).Fb(rear, NearTone.Glass, eu + .33f, 1.55f, .115f, .4f, 1.3f, .03f)
                .Fb(rear, NearTone.Metal, eu - .06f, 1.1f, .14f, .04f, .5f, .04f).Fb(rear, NearTone.Metal, eu + .06f, 1.1f, .14f, .04f, .5f, .04f)
                .Fb(rear, NearTone.Glass, eu, 2.45f, .11f, 1.2f, .18f, .03f)
                .Fb(rear, NearTone.Trim, eu, 2.98f, .28f, 2.4f, .12f, .5f)
                .Fbeam(rear, NearTone.Metal, eu - 1f, 2.55f, .05f, eu - 1f, 2.95f, .45f, .05f).Fbeam(rear, NearTone.Metal, eu + 1f, 2.55f, .05f, eu + 1f, 2.95f, .45f, .05f)
                .Fb(rear, NearTone.Stone, eu, .06f, .3f, 2.2f, .12f, .4f);
            // two roller doors on each long side wall with a frame and a hood
            foreach (var f in new[] { left, right })
                foreach (var u in new[] { -3.2f, 3.2f })
                {
                    p.Fb(f, NearTone.Trim, u - 1.55f, 1.65f, .09f, .16f, 3.3f, .16f).Fb(f, NearTone.Trim, u + 1.55f, 1.65f, .09f, .16f, 3.3f, .16f).Fb(f, NearTone.Trim, u, 3.32f, .09f, 3.26f, .18f, .16f)
                        .Fb(f, NearTone.MetalDark, u, 1.55f, .06f, 2.9f, 3.1f, .04f);
                    for (var y = .3f; y < 3.1f; y += .3f) p.Fb(f, NearTone.Metal, u, y, .085f, 2.9f, .03f, .03f);
                    p.Fb(f, NearTone.Metal, u, 3.0f, .13f, 2.9f, .2f, .14f);
                }
            Downpipes(p, w, d, h);
            // the roof: two air-handling boxes, a stair bulkhead, a water tank on legs and a flue
            const float top = h + .55f;
            p.Box(NearTone.Metal, -9, top + .55f, -2, 2.6f, 1.1f, 1.8f).Box(NearTone.MetalDark, -9, top + 1.15f, -2, 2.5f, .12f, 1.7f)
                .Box(NearTone.Metal, 6, top + .5f, 2.5f, 2f, 1f, 1.4f).Box(NearTone.Brick, -13, top + 1.4f, 2.5f, 3f, 2.8f, 2.6f).Box(NearTone.DoorPanel, -13, top + .9f, .9f, 1f, 1.8f, .06f)
                .Box(NearTone.StoneDark, -13, top + 2.85f, 2.5f, 3.2f, .14f, 2.8f);
            for (var sx = -1; sx <= 1; sx += 2) for (var sz = -1; sz <= 1; sz += 2) p.Beam(NearTone.MetalDark, 10 + sx * .8f, top, -3 + sz * .8f, 10 + sx * .8f, top + 1.6f, -3 + sz * .8f, .1f);
            p.Drum(NearTone.Wood, 10, top + 1.6f, -3, 1.2f, 2f, 10).Cyl(NearTone.RoofDark, 10, top + 3.6f, -3, 10, top + 4.3f, -3, 1.3f, .05f, 10)
                .Drum(NearTone.MetalDark, 1, top, -5, .3f, 4f, 8);
            return p;
        }

        // ---------------------------------------------------------------- the ferry terminal

        public static NearPlan Terminal()
        {
            const float w = TerminalW, d = TerminalD, h = TerminalH, rise = 5f;
            var p = new NearPlan("terminal");
            var rear = NearFacade.Rear(w, d); var front = NearFacade.Front(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            NearBuildings.Courses(p, rear, NearTone.PaintDark, w, .5f, h - .3f, .3f); NearBuildings.Courses(p, front, NearTone.PaintDark, w, .5f, h - .3f, .3f);
            NearBuildings.Courses(p, left, NearTone.PaintDark, d, .5f, h - .3f, .3f); NearBuildings.Courses(p, right, NearTone.PaintDark, d, .5f, h - .3f, .3f);
            p.Fb(rear, NearTone.Trim, 0, h - .22f, .05f, w + .1f, .28f, .1f).Fb(front, NearTone.Trim, 0, h - .22f, .05f, w + .1f, .28f, .1f)
                .Fb(left, NearTone.Trim, 0, h - .22f, .05f, d + .1f, .28f, .1f).Fb(right, NearTone.Trim, 0, h - .22f, .05f, d + .1f, .28f, .1f);
            p.Fb(rear, NearTone.Trim, 0, 3.3f, .06f, w, .16f, .12f);
            PaneRow(u =>
            {
                for (var f = 0; f < 2; f++) NearBuildings.WindowFrame(p, rear, u, 1.7f + f * 3.2f, 1.4f, 1.5f, Cladding);
            }, w);
            // the passenger entrance: wide double doors, a transom and a hood, in the bay between two panes
            const float eu = .4f;
            p.Fb(rear, NearTone.Trim, eu - .7f, 1.3f, .1f, .12f, 2.6f, .18f).Fb(rear, NearTone.Trim, eu + .7f, 1.3f, .1f, .12f, 2.6f, .18f).Fb(rear, NearTone.Trim, eu, 2.62f, .1f, 1.52f, .14f, .18f)
                .Fb(rear, NearTone.Door, eu - .3f, 1.2f, .08f, .56f, 2.4f, .06f).Fb(rear, NearTone.Door, eu + .3f, 1.2f, .08f, .56f, 2.4f, .06f)
                .Fb(rear, NearTone.Glass, eu - .3f, 1.5f, .115f, .38f, 1.3f, .03f).Fb(rear, NearTone.Glass, eu + .3f, 1.5f, .115f, .38f, 1.3f, .03f)
                .Fb(rear, NearTone.Metal, eu - .06f, 1.05f, .14f, .04f, .5f, .04f).Fb(rear, NearTone.Metal, eu + .06f, 1.05f, .14f, .04f, .5f, .04f)
                .Fb(rear, NearTone.Accent, eu, 2.98f, .24f, 2.2f, .12f, .46f)
                .Fbeam(rear, NearTone.Metal, eu - .9f, 2.55f, .05f, eu - .9f, 2.95f, .4f, .05f).Fbeam(rear, NearTone.Metal, eu + .9f, 2.55f, .05f, eu + .9f, 2.95f, .4f, .05f)
                .Fb(rear, NearTone.Stone, eu, .06f, .27f, 2.1f, .12f, .4f);
            // two big luggage and loading doors at the ends of the rear wall, with a frame and a sill
            foreach (var u in new[] { -24f, 24f })
                p.Fb(rear, NearTone.Trim, u - 1.45f, 1.6f, .1f, .16f, 3.2f, .18f).Fb(rear, NearTone.Trim, u + 1.45f, 1.6f, .1f, .16f, 3.2f, .18f).Fb(rear, NearTone.Trim, u, 3.22f, .1f, 3.06f, .18f, .18f)
                    .Fb(rear, NearTone.MetalDark, u, 1.5f, .07f, 2.7f, 3.0f, .04f).Fb(rear, NearTone.Metal, u, 2.0f, .1f, 2.7f, .04f, .04f).Fb(rear, NearTone.Metal, u, 1.0f, .1f, 2.7f, .04f, .04f);
            // roof: gutters and shingle courses
            var zg = (d + .6f) / 2 + .1f;
            p.Box(NearTone.Metal, 0, h - .14f, -zg, w + .6f, .1f, .08f).Box(NearTone.Metal, 0, h - .14f, zg, w + .6f, .1f, .08f);
            NearBuildings.RoofCourses(p, w + .6f, d + .6f, h, rise, NearTone.RoofDark);
            Downpipes(p, w, d, h);
            // a flue and a ridge vent
            p.Drum(NearTone.MetalDark, -12, h + 3.6f, -2, .3f, 2f, 8).Box(NearTone.Metal, 8, h + rise + .15f, 0, 3f, .3f, .6f);
            return p;
        }

        // ---------------------------------------------------------------- the depot

        public static NearPlan Depot()
        {
            const float w = DepotW, d = DepotD, h = DepotH, rise = 4.8f;
            var p = new NearPlan("depot");
            var rear = NearFacade.Rear(w, d); var front = NearFacade.Front(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            // corrugated steel: a rib every half metre on each wall
            NearBuildings.Ribs(p, rear, NearTone.MetalDark, w, .3f, h - .3f, .5f, .07f); NearBuildings.Ribs(p, front, NearTone.MetalDark, w, .3f, h - .3f, .5f, .07f);
            NearBuildings.Ribs(p, left, NearTone.MetalDark, d, .3f, h - .3f, .5f, .07f); NearBuildings.Ribs(p, right, NearTone.MetalDark, d, .3f, h - .3f, .5f, .07f);
            p.Fb(rear, NearTone.Trim, 0, h - .22f, .05f, w + .1f, .28f, .1f).Fb(front, NearTone.Trim, 0, h - .22f, .05f, w + .1f, .28f, .1f)
                .Fb(left, NearTone.Trim, 0, h - .22f, .05f, d + .1f, .28f, .1f).Fb(right, NearTone.Trim, 0, h - .22f, .05f, d + .1f, .28f, .1f);
            PaneRow(u => NearBuildings.WindowFrame(p, rear, u, 1.7f, 1.4f, 1.5f, Cladding), w);
            // a personnel door in the bay between two panes, and a roller door each end of the left wall's run
            const float du = .8f;
            p.Fb(rear, NearTone.Trim, du - .62f, 1.1f, .1f, .12f, 2.2f, .16f).Fb(rear, NearTone.Trim, du + .62f, 1.1f, .1f, .12f, 2.2f, .16f).Fb(rear, NearTone.Trim, du, 2.24f, .1f, 1.36f, .14f, .16f)
                .Fb(rear, NearTone.Door, du, 1.05f, .08f, 1.1f, 2.1f, .06f).Fb(rear, NearTone.Glass, du, 1.55f, .115f, .6f, .7f, .03f).Fb(rear, NearTone.Metal, du + .4f, 1.0f, .14f, .06f, .06f, .09f);
            foreach (var u in new[] { -3.5f, 3.5f })
            {
                p.Fb(left, NearTone.Trim, u - 1.7f, 1.8f, .09f, .18f, 3.6f, .16f).Fb(left, NearTone.Trim, u + 1.7f, 1.8f, .09f, .18f, 3.6f, .16f).Fb(left, NearTone.Trim, u, 3.62f, .09f, 3.58f, .2f, .16f)
                    .Fb(left, NearTone.MetalDark, u, 1.7f, .06f, 3.2f, 3.4f, .04f);
                for (var y = .3f; y < 3.3f; y += .3f) p.Fb(left, NearTone.Metal, u, y, .085f, 3.2f, .03f, .03f);
                p.Fb(left, NearTone.Metal, u, 3.5f, .13f, 3.2f, .24f, .14f);
            }
            var zg = (d + .6f) / 2 + .1f;
            p.Box(NearTone.Metal, 0, h - .14f, -zg, w + .6f, .1f, .08f).Box(NearTone.Metal, 0, h - .14f, zg, w + .6f, .1f, .08f);
            NearBuildings.RoofCourses(p, w + .6f, d + .6f, h, rise, NearTone.RoofDark);
            Downpipes(p, w, d, h);
            p.Box(NearTone.Metal, 4, h + rise + .15f, 0, 4f, .3f, .6f).Drum(NearTone.MetalDark, -7, h + 3.3f, 1, .25f, 1.9f, 8);
            return p;
        }

        // ---------------------------------------------------------------- the berthed ferry and its pontoon

        /// <summary>
        /// Everything at the ferry berth, in the ferry node's frame (its origin is the waterline at the berth centre; the terminal's frame
        /// is 2.4 m above it): the pontoon and gangway the far level draws as plain slabs, and the ferry's hull, cabin and bridge.
        /// </summary>
        public static NearPlan Ferry()
        {
            var p = new NearPlan("ferry");
            const float deck = 2.4f;
            // ---- pontoon: planks, rails on three sides, bollards. The far slab is 36 x 0.5 x 10 centred at z -15, top at y 1.55
            const float pz0 = -20, pz1 = -10, ptop = -.85f + deck;
            for (var z = pz0 + .6f; z < pz1; z += .6f) p.Quad(NearTone.WoodDark, new NearVec(-17.8f, ptop + .006f, z), new NearVec(-17.8f, ptop + .006f, z + .04f), new NearVec(17.8f, ptop + .006f, z + .04f),
                new NearVec(17.8f, ptop + .006f, z), new NearVec(0, 1, 0));
            void Rails(float x0, float z0, float x1, float z1, float spacing)
            {
                var len = (float)Math.Sqrt((x1 - x0) * (x1 - x0) + (z1 - z0) * (z1 - z0)); var n = Math.Max(1, (int)Math.Round(len / spacing));
                for (var i = 0; i <= n; i++)
                {
                    var t = i / (float)n; var x = x0 + (x1 - x0) * t; var z = z0 + (z1 - z0) * t;
                    p.Beam(NearTone.MetalDark, x, ptop, z, x, ptop + 1.0f, z, .06f);
                }
                p.Beam(NearTone.MetalDark, x0, ptop + 1.0f, z0, x1, ptop + 1.0f, z1, .05f).Beam(NearTone.MetalDark, x0, ptop + .5f, z0, x1, ptop + .5f, z1, .04f);
            }
            Rails(-17.6f, pz0 + .2f, -17.6f, pz1 - .2f, 2.5f); Rails(17.6f, pz0 + .2f, 17.6f, pz1 - .2f, 2.5f); Rails(-17.6f, pz1 - .2f, 17.6f, pz1 - .2f, 3.5f);
            foreach (var x in new[] { -16f, 16f })
                p.Drum(NearTone.MetalDark, x, ptop, pz1 - 1.2f, .22f, .4f, 8).Drum(NearTone.Metal, x, ptop + .4f, pz1 - 1.2f, .27f, .06f, 8);
            // ---- gangway rails: the far slab is 2.4 x 0.2 x 6 at z -23 (y 1.5)
            foreach (var sx in new[] { -1, 1 })
                Rails(sx * 1.1f, -25.6f, sx * 1.1f, -20.4f, 1.7f);
            // ---- hull (33 x 2 x 9, y -0.7..1.3): portholes, fenders, a coloured band
            for (var k = 0; k < 9; k++)
                foreach (var sz in new[] { -1, 1 })
                    p.Cyl(NearTone.Black, -14 + k * 3.5f, .45f, sz * 4.48f, -14 + k * 3.5f, .45f, sz * 4.55f, .2f, -1, 6);
            foreach (var sz in new[] { -1, 1 })
            {
                p.Box(NearTone.Orange, 0, .85f, sz * 4.53f, 33.1f, .22f, .06f);
                for (var x = -14f; x <= 14f; x += 7f) p.Drum(NearTone.Black, x, -.55f, sz * 4.66f, .2f, 1.25f, 6);
            }
            // ---- the main cabin (27 x 2.4 x 7, y 1.3..3.7): glazing on both long sides with pillars, head and sill rails
            foreach (var sz in new[] { -1, 1 })
            {
                var f = NearFacade.At(0, sz * 3.5f, 1, 0, 0, sz);
                // the far level glazes the +Z side only; the other side gets its glass here
                if (sz < 0) p.Fb(f, NearTone.Glass, 0, 2.8f, .03f, 25f, .9f, .06f);
                p.Fb(f, NearTone.Trim, 0, 3.3f, .06f, 25.4f, .1f, .12f).Fb(f, NearTone.Trim, 0, 2.3f, .06f, 25.4f, .1f, .12f);
                for (var x = -12.5f; x <= 12.6f; x += 2.5f) p.Fb(f, NearTone.Trim, x, 2.8f, .06f, .12f, .96f, .1f);
                // life rings on the cabin side
                foreach (var x in new[] { -9f, 3f })
                    p.Cyl(NearTone.Orange, x, 2.1f, sz * 3.55f, x, 2.1f, sz * 3.63f, .34f, -1, 8);
            }
            // ---- the top deck's rail, round the cabin roof (y 3.7), with the bridge's footprint (x 4..12) left clear
            for (var x = -13.4f; x <= 3.4f; x += 2.8f)
                foreach (var sz in new[] { -3.3f, 3.3f })
                    p.Beam(NearTone.Metal, x, 3.7f, sz, x, 4.6f, sz, .06f);
            foreach (var sz in new[] { -3.3f, 3.3f })
                p.Beam(NearTone.Metal, -13.4f, 4.6f, sz, 3.4f, 4.6f, sz, .05f).Beam(NearTone.Metal, -13.4f, 4.15f, sz, 3.4f, 4.15f, sz, .04f);
            p.Beam(NearTone.Metal, -13.4f, 4.6f, -3.3f, -13.4f, 4.6f, 3.3f, .05f);
            // ---- the bridge (8 x 1 x 6 at x 8, y 3.7..4.7): windows all round, a roof, a funnel and a mast
            p.Box(NearTone.Glass, 8, 4.2f, 0, 8.1f, .5f, 6.1f);
            for (var x = 4.2f; x <= 11.9f; x += 1.9f) foreach (var sz in new[] { -1, 1 }) p.Box(NearTone.Trim, x, 4.2f, sz * 3.04f, .1f, .56f, .1f);
            for (var z = -2.5f; z <= 2.6f; z += 1.7f) p.Box(NearTone.Trim, 12.05f, 4.2f, z, .1f, .56f, .1f).Box(NearTone.Trim, 3.95f, 4.2f, z, .1f, .56f, .1f);
            p.Box(NearTone.Trim, 8, 4.77f, 0, 8.5f, .1f, 6.5f)
                .Cyl(NearTone.Red, -3, 3.7f, 0, -3, 5.4f, 0, .8f, .62f, 10).Cyl(NearTone.Black, -3, 5.4f, 0, -3, 5.55f, 0, .62f, -1, 10)
                .Beam(NearTone.MetalDark, 9, 4.8f, 0, 9, 7.2f, 0, .1f).Box(NearTone.Black, 9, 6.6f, 0, .1f, .08f, 1.4f).Box(NearTone.Metal, 9, 6.9f, 0, .1f, .08f, .8f)
                .Box(NearTone.Red, 11.6f, 4.95f, -2.8f, .2f, .16f, .16f).Box(NearTone.Foliage, 11.6f, 4.95f, 2.8f, .2f, .16f, .16f);
            return p;
        }
    }
}
