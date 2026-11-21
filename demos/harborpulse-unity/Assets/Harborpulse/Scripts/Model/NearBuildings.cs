// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>How a house or shopfront was drawn by the far level, so its near set lines up with it (set by the town builder from its own locals).</summary>
    public readonly struct HouseSpec
    {
        public readonly float X, Z, W, D, Rise;
        public readonly int Floors, Style;
        public readonly bool Porch, Shop, GableFront, Rich, FarDormer;
        /// <summary>The chimney's centre in the building frame and its height; a height of zero or less means there is none.</summary>
        public readonly float ChimneyX, ChimneyY, ChimneyZ, ChimneyTall;
        public HouseSpec(float x, float z, float w, float d, int floors, bool porch, bool shop, bool gableFront, int style, float rise, bool rich, bool farDormer,
            float chimneyX, float chimneyY, float chimneyZ, float chimneyTall)
        {
            X = x; Z = z; W = w; D = d; Floors = floors; Porch = porch; Shop = shop; GableFront = gableFront; Style = style; Rise = rise; Rich = rich;
            FarDormer = farDormer; ChimneyX = chimneyX; ChimneyY = chimneyY; ChimneyZ = chimneyZ; ChimneyTall = chimneyTall;
        }
        public float Height => Floors * 3.1f + .6f;
    }

    /// <summary>
    /// The near sets of the town's buildings: what is ADDED, in the building's frame, to the walls, roof, chimney, porch slab and flat windows the
    /// far level already draws. Nothing here moves a wall, a roof or a window, and nothing leaves the lot the building reserved.
    /// </summary>
    public static class NearBuildings
    {
        // ---------------------------------------------------------------- shared vocabulary

        /// <summary>What a window's surround is made of and what hangs on it.</summary>
        public readonly struct WindowStyle
        {
            public readonly NearTone Surround;
            public readonly bool Shutters, FlowerBox;
            public readonly int Bars;
            public WindowStyle(NearTone surround, bool shutters, bool flowerBox, int bars) { Surround = surround; Shutters = shutters; FlowerBox = flowerBox; Bars = bars; }
        }

        /// <summary>Frame, sill, head, mullion and transom round a window pane that is already there (a box 0.12 deep standing on the wall plane).</summary>
        public static void WindowFrame(NearPlan p, NearFacade f, float u, float v, float sw, float sh, WindowStyle s)
        {
            var t = s.Surround;
            p.Fb(f, t, u, v + sh / 2 + .1f, .1f, sw + .5f, .14f, .22f);
            p.Fb(f, t == NearTone.Trim ? NearTone.Stone : t, u, v - sh / 2 - .07f, .14f, sw + .4f, .1f, .3f);
            p.Fb(f, t, u - (sw / 2 + .07f), v, .095f, .12f, sh + .04f, .17f);
            p.Fb(f, t, u + (sw / 2 + .07f), v, .095f, .12f, sh + .04f, .17f);
            if (s.Bars >= 2)
            {
                p.Fb(f, t, u - sw / 6, v, .125f, .045f, sh, .05f);
                p.Fb(f, t, u + sw / 6, v, .125f, .045f, sh, .05f);
            }
            else p.Fb(f, t, u, v, .125f, .05f, sh, .05f);
            p.Fb(f, t, u, v + sh * .14f, .125f, sw, .05f, .05f);
            if (s.Shutters)
                for (var k = -1; k <= 1; k += 2)
                {
                    var su = u + k * (sw / 2 + .13f + .2f);
                    p.Fb(f, NearTone.Accent, su, v, .04f, .4f, sh + .2f, .08f);
                    p.Fb(f, NearTone.AccentLight, su, v + sh * .26f, .085f, .36f, .06f, .03f);
                    p.Fb(f, NearTone.AccentLight, su, v - sh * .26f, .085f, .36f, .06f, .03f);
                }
            if (s.FlowerBox)
            {
                p.Fb(f, NearTone.Wood, u, v - sh / 2 - .27f, .2f, sw + .1f, .22f, .24f);
                for (var k = 0; k < 4; k++)
                    p.Fb(f, k == 2 ? NearTone.Red : k % 2 == 0 ? NearTone.Foliage : NearTone.FoliageDark, u - sw * .3f + k * sw * .2f, v - sh / 2 - .08f, .2f, .2f, .16f, .2f);
            }
        }

        /// <summary>One row set, as the town's far windows lay it out (<c>origin</c> is the row's centre along the facade).</summary>
        public static void Windows(NearPlan p, NearFacade f, float origin, float span, int floors, float floorHeight, float sill, float sw, float sh, float spacing, WindowStyle s)
        {
            var count = WindowRow.Count(span, spacing); var start = WindowRow.Start(count, spacing);
            for (var fl = 0; fl < floors; fl++)
                for (var i = 0; i < count; i++)
                    WindowFrame(p, f, origin + start + i * spacing, sill + fl * floorHeight + sh / 2, sw, sh, s);
        }

        /// <summary>Thin shade lines along a wall: clapboard edges, board-and-batten seams, brick courses. Flat strips a hand's breadth off the wall.</summary>
        public static void Courses(NearPlan p, NearFacade f, NearTone tone, float span, float y0, float y1, float spacing, float thickness = .05f)
        {
            for (var y = y0; y < y1; y += spacing) p.Fq(f, tone, -span / 2 + .15f, y, span / 2 - .15f, y + thickness, .012f);
        }

        /// <summary>Vertical seams (battens, corrugation ribs) across a facade.</summary>
        public static void Ribs(NearPlan p, NearFacade f, NearTone tone, float span, float y0, float y1, float spacing, float width = .06f)
        {
            for (var u = -span / 2 + spacing; u < span / 2 - .05f; u += spacing) p.Fq(f, tone, u - width / 2, y0, u + width / 2, y1, .012f);
        }

        /// <summary>Shingle courses on a gabled roof whose ridge runs along the roof frame's X (turn the frame first for a gable-front house).</summary>
        public static void RoofCourses(NearPlan p, float roofW, float roofD, float eave, float rise, NearTone tone)
        {
            float hw = roofW / 2, hd = roofD / 2;
            var nl = (float)Math.Sqrt(hd * hd + rise * rise);
            for (var k = 1; k <= 5; k++)
            {
                float t = k * .16f, t2 = t + .03f;
                for (var side = -1; side <= 1; side += 2)
                {
                    float ny = hd / nl * .02f, nz = side * rise / nl * .02f;
                    var a = new NearVec(-hw + .1f, eave + rise * t + ny, side * hd * (1 - t) + nz);
                    var b = new NearVec(hw - .1f, eave + rise * t + ny, side * hd * (1 - t) + nz);
                    var c = new NearVec(hw - .1f, eave + rise * t2 + ny, side * hd * (1 - t2) + nz);
                    var d = new NearVec(-hw + .1f, eave + rise * t2 + ny, side * hd * (1 - t2) + nz);
                    p.Quad(tone, a, b, c, d, new NearVec(0, hd, side * rise));
                }
            }
        }

        /// <summary>Gutters under a gabled roof's two long eaves and one downpipe to the ground at the front corner.</summary>
        static void Gutters(NearPlan p, float w, float d, float eaveY, bool gableFront)
        {
            if (!gableFront)
            {
                var z = (d + .7f) / 2 + .1f;
                p.Box(NearTone.Metal, 0, eaveY - .14f, -z, w + .8f, .1f, .08f).Box(NearTone.Metal, 0, eaveY - .14f, z, w + .8f, .1f, .08f);
                // the pipe drops from the gutter, jogs in to the wall and runs to the ground
                p.Beam(NearTone.Metal, w / 2 - .5f, eaveY - .2f, -z, w / 2 - .5f, 2.4f, -z, .09f)
                    .Beam(NearTone.Metal, w / 2 - .5f, 2.4f, -z, w / 2 - .5f, 2.1f, -d / 2 - .08f, .09f)
                    .Beam(NearTone.Metal, w / 2 - .5f, 2.1f, -d / 2 - .08f, w / 2 - .5f, .08f, -d / 2 - .08f, .09f);
            }
            else
            {
                var x = (w + .7f) / 2 + .1f;
                p.Box(NearTone.Metal, -x, eaveY - .14f, 0, .08f, .1f, d + .8f).Box(NearTone.Metal, x, eaveY - .14f, 0, .08f, .1f, d + .8f);
                p.Beam(NearTone.Metal, x, eaveY - .2f, -d / 2 + .5f, x, 2.4f, -d / 2 + .5f, .09f)
                    .Beam(NearTone.Metal, x, 2.4f, -d / 2 + .5f, w / 2 + .08f, 2.1f, -d / 2 + .5f, .09f)
                    .Beam(NearTone.Metal, w / 2 + .08f, 2.1f, -d / 2 + .5f, w / 2 + .08f, .08f, -d / 2 + .5f, .09f);
            }
        }

        /// <summary>A front door's near parts: raised panels, a glazed light, a knob, a transom, a doormat. The slab and its frame are the far level's.</summary>
        static void Door(NearPlan p, NearFacade f, float u, float baseY, bool glazed)
        {
            p.Fb(f, NearTone.DoorPanel, u - .24f, baseY + .55f, .11f, .34f, .72f, .03f).Fb(f, NearTone.DoorPanel, u + .24f, baseY + .55f, .11f, .34f, .72f, .03f);
            if (glazed) p.Fb(f, NearTone.Glass, u, baseY + 1.6f, .11f, .74f, .62f, .03f);
            else p.Fb(f, NearTone.DoorPanel, u - .24f, baseY + 1.6f, .11f, .34f, .62f, .03f).Fb(f, NearTone.DoorPanel, u + .24f, baseY + 1.6f, .11f, .34f, .62f, .03f);
            p.Fb(f, NearTone.Metal, u + .4f, baseY + 1.02f, .15f, .06f, .06f, .09f);
            p.Fb(f, NearTone.Glass, u, baseY + 2.255f, .1f, 1f, .1f, .03f);
        }

        /// <summary>Two low steps at a door that stands on the ground (a threshold, not a stoop that buries the door).</summary>
        static void Stoop(NearPlan p, NearFacade f, float u, float width)
        {
            p.Fb(f, NearTone.Stone, u, .06f, .45f, width, .12f, .34f).Fb(f, NearTone.StoneDark, u, .12f, .18f, width - .1f, .1f, .3f);
        }

        // ---------------------------------------------------------------- a house or a shopfront

        public static NearPlan House(HouseSpec s)
        {
            var p = new NearPlan(s.Shop ? "shopfront" : "house");
            float w = s.W, d = s.D, H = s.Height;
            var front = NearFacade.Front(w, d); var rear = NearFacade.Rear(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            var doorX = w * .18f;
            var shutters = !s.Shop && NearHash.Unit(s.X, s.Z, 11) < .45f;
            var boxes = !s.Shop && NearHash.Unit(s.X, s.Z, 12) < .4f;
            var tone = NearTone.Trim;

            // clapboard: a shade line at every board course on all four walls
            Courses(p, front, NearTone.PaintDark, w, .55f, H - .25f, .3f); Courses(p, rear, NearTone.PaintDark, w, .55f, H - .25f, .3f);
            Courses(p, left, NearTone.PaintDark, d, .55f, H - .25f, .3f); Courses(p, right, NearTone.PaintDark, d, .55f, H - .25f, .3f);
            // frieze under the eaves, and foundation vents on the stone course
            p.Fb(front, tone, 0, H - .22f, .05f, w + .1f, .28f, .1f).Fb(rear, tone, 0, H - .22f, .05f, w + .1f, .28f, .1f)
                .Fb(left, tone, 0, H - .22f, .05f, d + .1f, .28f, .1f).Fb(right, tone, 0, H - .22f, .05f, d + .1f, .28f, .1f);
            p.Fb(front, NearTone.Black, -w * .3f, .15f, .14f, .5f, .2f, .05f).Fb(rear, NearTone.Black, -w * .3f, .15f, .14f, .5f, .2f, .05f)
                .Fb(front, NearTone.Black, w * .36f, .15f, .14f, .5f, .2f, .05f);
            if (!s.Rich)
            {
                // outskirts houses have no far door frame or corner boards from the town's detail pass: the near level supplies them
                foreach (var sx in new[] { -1, 1 })
                    p.Fb(front, tone, doorX + sx * .67f, 1.15f, .12f, .18f, 2.45f, .2f);
                p.Fb(front, tone, doorX, 2.4f, .12f, 1.52f, .18f, .2f);
            }
            Gutters(p, w, d, H, s.GableFront);

            // roof: shingle courses on the gabled styles (the far roof is a plain slab of colour)
            if (s.Style == 0 || s.Style == 4)
            {
                if (s.GableFront) { p.Frame(90); RoofCourses(p, d + .8f, w + .7f, H, s.Rise, NearTone.RoofDark); p.Frame(0); }
                else RoofCourses(p, w + .8f, d + .7f, H, s.Rise, NearTone.RoofDark);
            }
            if (s.GableFront && s.Style == 0 && s.Floors >= 2 && NearHash.Unit(s.X, s.Z, 14) < .7f)
            {
                // an attic window in the front gable, drawn with the shared window materials so it follows the night like the rest
                var gz = -(d + .8f) / 2 - .04f; var gy = H + s.Rise * .3f;
                p.Box(NearTone.Window, 0, gy, gz, .7f, .8f, .08f, 71);
                p.Box(NearTone.Trim, 0, gy + .46f, gz - .02f, .9f, .1f, .1f).Box(NearTone.Trim, 0, gy - .46f, gz - .02f, .96f, .08f, .13f)
                    .Box(NearTone.Trim, -.42f, gy, gz - .02f, .08f, .84f, .1f).Box(NearTone.Trim, .42f, gy, gz - .02f, .08f, .84f, .1f)
                    .Box(NearTone.Trim, 0, gy, gz - .03f, .05f, .8f, .06f);
            }

            // chimney: flashing, a corbel course and two pots on the far level's stack
            if (s.ChimneyTall > 0)
            {
                float cx = s.ChimneyX, cz = s.ChimneyZ, top = s.ChimneyY + s.ChimneyTall * .5f + .11f;
                p.Box(NearTone.RoofDark, cx, s.ChimneyY - s.ChimneyTall * .5f + .12f, cz, 1.2f, .12f, 1.2f)
                    .Box(NearTone.Brick, cx, top - .55f, cz, .98f, .12f, .98f)
                    .Drum(NearTone.BrickLight, cx - .2f, top, cz, .13f, .42f, 6).Drum(NearTone.BrickLight, cx + .2f, top, cz, .13f, .42f, 6);
            }

            // dormers on the front slope: the far one gains its window frame, and a second sits opposite on some roofs
            if (s.Rich && s.Style == 0 && !s.GableFront)
            {
                var dormerY = H + s.Rise * .48f; var dz = -d * .28f;
                if (s.FarDormer) Dormer(p, -w * .22f, dormerY, dz, false);
                if (NearHash.Unit(s.X, s.Z, 15) < .55f && s.Rise > 2.2f) Dormer(p, w * .22f, dormerY, dz, true);
            }

            // windows
            var ws = new WindowStyle(tone, shutters, boxes, 1);
            var plain = new WindowStyle(tone, false, false, 1);
            if (s.Shop)
            {
                Windows(p, front, 0, w, s.Floors - 1, 3.1f, 4f, 1f, 1.5f, 2.6f, new WindowStyle(tone, false, NearHash.Unit(s.X, s.Z, 12) < .6f, 1));
                Windows(p, rear, 0, w, s.Floors, 3.1f, .9f, 1f, 1.4f, 2.8f, plain);
                Windows(p, left, 0, d, s.Floors, 3.1f, .9f, 1f, 1.4f, 3.6f, plain); Windows(p, right, 0, d, s.Floors, 3.1f, .9f, 1f, 1.4f, 3.6f, plain);
            }
            else
            {
                Windows(p, front, 0, w, s.Floors, 3.1f, .9f, 1f, 1.4f, 2.8f, ws);
                Windows(p, rear, 0, w, s.Floors, 3.1f, .9f, 1f, 1.4f, 2.8f, plain);
                Windows(p, left, 0, d, s.Floors, 3.1f, .9f, 1f, 1.4f, 3.6f, shutters ? ws : plain);
                Windows(p, right, 0, d, s.Floors, 3.1f, .9f, 1f, 1.4f, 3.6f, shutters ? ws : plain);
            }

            Door(p, front, doorX, 0, !s.Shop && NearHash.Unit(s.X, s.Z, 13) < .5f);
            if (s.Shop) Shopfront(p, front, w, d, doorX, s);
            if (s.Porch) Porch(p, front, w, d, s);
            else if (!s.Shop) Stoop(p, front, doorX, 1.9f);
            return p;
        }

        static void Dormer(NearPlan p, float x, float y, float z, bool whole)
        {
            // the far dormer: a 2.0 x 1.65 x 1.8 box under a gabled cap, its window 0.9 x 1.05 flush on the front
            if (whole)
            {
                p.Box(NearTone.Paint, x, y, z, 2f, 1.65f, 1.8f).Gable(NearTone.Roof, x, y + .83f, z, 2.2f, 2f, 1f, 90);
                p.Box(NearTone.Window, x, y, z - .92f, .9f, 1.05f, .1f, 72);
            }
            var fz = z - .97f;
            p.Box(NearTone.Trim, x, y + .6f, fz, 1.15f, .1f, .08f).Box(NearTone.Trim, x, y - .58f, fz, 1.2f, .08f, .12f)
                .Box(NearTone.Trim, x - .52f, y, fz, .08f, 1.1f, .08f).Box(NearTone.Trim, x + .52f, y, fz, .08f, 1.1f, .08f)
                .Box(NearTone.Trim, x, y, fz - .01f, .05f, 1.05f, .06f).Box(NearTone.Trim, x, y + .08f, fz - .01f, .9f, .05f, .06f);
            // cheek boards either side of the window
            p.Box(NearTone.PaintDark, x - .86f, y, z - .91f, .22f, 1.5f, .04f).Box(NearTone.PaintDark, x + .86f, y, z - .91f, .22f, 1.5f, .04f);
        }

        /// <summary>A shopfront: window bays with mullions over a bulkhead, a recessed door's glazing, a valance and brackets, and goods outside.</summary>
        static void Shopfront(NearPlan p, NearFacade f, float w, float d, float doorX, HouseSpec s)
        {
            var gw = w - 2.5f; var t = NearTone.Trim;
            p.Fb(f, NearTone.PaintDark, 0, .25f, .07f, gw, .5f, .14f);
            p.Fb(f, t, 0, 2.53f, .12f, gw + .14f, .1f, .1f).Fb(f, t, 0, .52f, .12f, gw + .14f, .08f, .1f).Fb(f, t, 0, 2.1f, .11f, gw, .05f, .05f);
            p.Fb(f, t, -(gw / 2 + .05f), 1.5f, .1f, .14f, 2.1f, .14f).Fb(f, t, gw / 2 + .05f, 1.5f, .1f, .14f, 2.1f, .14f);
            var bays = Math.Max(2, (int)Math.Round(gw / 1.7f));
            for (var i = 1; i < bays; i++)
            {
                var u = -gw / 2 + gw * i / bays;
                if (Math.Abs(u - doorX) < .85f) continue;
                p.Fb(f, t, u, 1.5f, .12f, .07f, 2.05f, .07f);
            }
            // the door's own glazing and kick plate
            p.Fb(f, NearTone.Glass, doorX, 1.5f, .115f, .7f, 1.15f, .03f).Fb(f, NearTone.MetalDark, doorX, .2f, .115f, .9f, .3f, .03f);
            p.Fbeam(f, NearTone.Metal, doorX + .4f, .9f, .17f, doorX + .4f, 1.3f, .17f, .04f);
            // a plain valance hanging from the awning's front edge, on two brackets
            var aw = w - 1.5f;
            p.Fb(f, NearTone.Accent, 0, 2.78f, 1.78f, aw, .34f, .06f);
            for (var k = -1; k <= 1; k += 2) p.Fbeam(f, NearTone.Metal, k * (aw / 2 - .6f), 3.45f, .05f, k * (aw / 2 - .6f), 2.95f, 1.7f, .05f);
            if (!s.Porch)
            {
                // with no porch under it the awning's top takes stripes in the accent colour
                for (var k = 0; ; k++)
                {
                    var u = -aw / 2 + .225f + k * .9f;
                    if (u + .225f > aw / 2) break;
                    p.Fb(f, NearTone.AccentLight, u, 3.105f, .9f, .45f, .03f, 1.76f);
                }
            }
            // goods on the boardwalk: a barrel and a crate stack (the porch slab is the floor under a shop that has one)
            var floor = s.Porch ? .5f : 0f;
            if (NearHash.Unit(s.X, s.Z, 16) < .8f)
            {
                p.Fdrum(f, NearTone.Wood, -gw / 2 + .5f, floor, 1.1f, .35f, .9f).Fdrum(f, NearTone.MetalDark, -gw / 2 + .5f, floor + .22f, 1.1f, .37f, .06f)
                    .Fdrum(f, NearTone.MetalDark, -gw / 2 + .5f, floor + .62f, 1.1f, .37f, .06f);
            }
            if (NearHash.Unit(s.X, s.Z, 17) < .8f)
            {
                p.Fb(f, NearTone.WoodLight, gw / 2 - .5f, floor + .3f, 1.1f, .6f, .6f, .6f).Fb(f, NearTone.Wood, gw / 2 - .5f, floor + .88f, 1.1f, .52f, .56f, .52f)
                    .Fb(f, NearTone.WoodLight, gw / 2 - 1.2f, floor + .25f, 1.15f, .5f, .5f, .5f);
            }
        }

        /// <summary>A porch: post bases and caps, a rail with balusters either side of the stair, the stair, and a few things set out.</summary>
        static void Porch(NearPlan p, NearFacade f, float w, float d, HouseSpec s)
        {
            var pw = Math.Min(w - 1, 7f); var edge = pw / 2 - .3f; const float deck = .5f;
            var post = new[] { -edge, edge };
            foreach (var x in post)
                p.Fb(f, NearTone.Trim, x, deck + .15f, 2.4f, .34f, .3f, .34f).Fb(f, NearTone.Trim, x, 2.93f, 2.4f, .32f, .14f, .32f);
            // brackets under the porch roof
            foreach (var x in post)
            {
                var k = x < 0 ? 1 : -1;
                p.Fbeam(f, NearTone.Trim, x, 2.55f, 2.4f, x + k * .5f, 2.95f, 2.4f, .07f);
            }
            if (!s.Shop)
            {
                const float gap = 1.3f;
                // rail and balusters in the two bays either side of the stair, and along each end back to the wall
                void Bay(float u0, float u1)
                {
                    p.Fb(f, NearTone.Trim, (u0 + u1) / 2, deck + .9f, 2.4f, Math.Abs(u1 - u0), .07f, .09f).Fb(f, NearTone.Trim, (u0 + u1) / 2, deck + .22f, 2.4f, Math.Abs(u1 - u0), .06f, .06f);
                    var n = (int)(Math.Abs(u1 - u0) / .25f);
                    for (var i = 1; i < n; i++) p.Fb(f, NearTone.Trim, Math.Min(u0, u1) + i * (Math.Abs(u1 - u0) / n), deck + .55f, 2.4f, .04f, .64f, .04f);
                }
                Bay(-edge, -gap); Bay(gap, edge);
                foreach (var x in post)
                {
                    p.Fb(f, NearTone.Trim, x, deck + .9f, 1.25f, .09f, .07f, 2.3f).Fb(f, NearTone.Trim, x, deck + .22f, 1.25f, .06f, .06f, 2.3f);
                    for (var i = 1; i < 9; i++) p.Fb(f, NearTone.Trim, x, deck + .55f, .15f + i * .25f, .04f, .64f, .04f);
                }
                // stair: two steps and a rail each side
                p.Fb(f, NearTone.Stone, 0, .09f, 3.0f, 2.2f, .18f, .32f).Fb(f, NearTone.StoneDark, 0, .26f, 2.72f, 2.2f, .17f, .3f);
                foreach (var k in new[] { -1f, 1f })
                {
                    p.Fb(f, NearTone.Trim, k * 1.18f, deck + .45f, 3.12f, .1f, .95f, .1f);
                    p.Fbeam(f, NearTone.Trim, k * 1.18f, deck + .9f, 2.55f, k * 1.18f, deck + .35f, 3.12f, .07f);
                }
            }
            else
            {
                p.Fb(f, NearTone.Stone, 0, .09f, 3.0f, pw * .5f, .18f, .32f).Fb(f, NearTone.StoneDark, 0, .26f, 2.72f, pw * .5f, .17f, .3f);
                return;
            }
            // things set out, chosen from the house's position (never from the town's seeded streams)
            var h = NearHash.Unit(s.X, s.Z, 18);
            var doorX = w * .18f;
            if (h < .5f)
            {
                // a bench against the wall, left of the door
                const float bx = -1.4f;
                p.Fb(f, NearTone.Wood, bx, deck + .45f, .4f, 1.5f, .06f, .42f).Fb(f, NearTone.Wood, bx, deck + .85f, .15f, 1.5f, .5f, .05f)
                    .Fb(f, NearTone.WoodDark, bx - .62f, deck + .22f, .4f, .06f, .44f, .38f).Fb(f, NearTone.WoodDark, bx + .62f, deck + .22f, .4f, .06f, .44f, .38f);
            }
            if (h > .25f && h < .8f)
            {
                // a pair of pots beside the door
                for (var k = 0; k < 2; k++)
                {
                    var u = doorX + .95f + k * .45f;
                    p.Fdrum(f, NearTone.Red, u, deck, .5f, .17f, .26f, 6, .22f).Fdrum(f, k == 0 ? NearTone.Foliage : NearTone.FoliageDark, u, deck + .26f, .5f, .22f, .22f, 6, .06f);
                }
            }
            if (h > .6f)
            {
                // a stack of firewood in the corner
                for (var r = 0; r < 3; r++) for (var c = 0; c < 3 - r; c++)
                    p.Fdrum(f, r % 2 == 0 ? NearTone.WoodLight : NearTone.Wood, pw / 2 - 1.1f + c * .24f + r * .12f, deck + .12f + r * .21f, 1.0f, .11f, .5f, 6);
            }
            if (h < .3f) p.Fdrum(f, NearTone.Wood, pw / 2 - 1f, deck, 1.0f, .3f, .75f).Fdrum(f, NearTone.MetalDark, pw / 2 - 1f, deck + .45f, 1.0f, .32f, .06f);
        }

        // ---------------------------------------------------------------- the town's commercial block

        public static NearPlan Commercial(float x, float z, float w, float d, int floors, bool brick, bool rich)
        {
            var p = new NearPlan("commercial");
            var h = floors * 3.5f + .5f;
            var front = NearFacade.Front(w, d); var rear = NearFacade.Rear(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            var st = new WindowStyle(NearTone.Stone, false, false, 2);
            var courseTone = brick ? NearTone.Mortar : NearTone.PaintDark;
            Courses(p, front, courseTone, w, .5f, h - .3f, brick ? .22f : .3f, .03f); Courses(p, rear, courseTone, w, .5f, h - .3f, brick ? .22f : .3f, .03f);
            Courses(p, left, courseTone, d, .5f, h - .3f, brick ? .22f : .3f, .03f); Courses(p, right, courseTone, d, .5f, h - .3f, brick ? .22f : .3f, .03f);
            // pilasters either side of every window column, and a string course over each floor
            var columns = WindowRow.Count(w, 2.7f); var start = WindowRow.Start(columns, 2.7f);
            for (var i = 0; i <= columns; i++)
            {
                var u = start + (i - .5f) * 2.7f;
                if (Math.Abs(u) > w / 2 - .25f) u = Math.Sign(u) * (w / 2 - .3f);
                p.Fb(front, NearTone.BrickLight, u, h / 2 + 2f, .08f, .4f, h - 4f, .16f);
            }
            for (var fl = 1; fl < floors; fl++)
            {
                var y = fl * 3.5f + .05f;
                p.Fb(front, NearTone.Stone, 0, y, .08f, w, .22f, .16f).Fb(rear, NearTone.Stone, 0, y, .08f, w, .22f, .16f)
                    .Fb(left, NearTone.Stone, 0, y, .08f, d, .22f, .16f).Fb(right, NearTone.Stone, 0, y, .08f, d, .22f, .16f);
            }
            // dentils under the front cornice
            for (var u = -w / 2 + .5f; u < w / 2 - .3f; u += .6f) p.Fb(front, NearTone.Trim, u, h - .12f, .14f, .26f, .22f, .2f);
            Windows(p, front, 0, w, floors - 1, 3.5f, 4.4f, 1.1f, 1.8f, 2.7f, st);
            Windows(p, rear, 0, w, floors, 3.5f, .9f, 1.1f, 1.8f, 2.9f, st);
            Windows(p, left, 0, d, floors, 3.5f, .9f, 1.1f, 1.8f, 3.4f, st); Windows(p, right, 0, d, floors, 3.5f, .9f, 1.1f, 1.8f, 3.4f, st);

            // the shopfront: a recessed pair of doors, mullioned bays over a stone bulkhead, a transom and a plain valance
            var gw = w - 2.2f;
            p.Fb(front, NearTone.StoneDark, 0, .2f, .07f, gw, .4f, .14f);
            var bays = Math.Max(3, (int)Math.Round(gw / 1.9f));
            for (var i = 0; i <= bays; i++)
            {
                var u = -gw / 2 + gw * i / bays;
                if (Math.Abs(u) < .9f && i != 0 && i != bays) continue;
                p.Fb(front, NearTone.Trim, u, 1.6f, .12f, .09f, 2.45f, .09f);
            }
            p.Fb(front, NearTone.Trim, 0, 2.82f, .12f, gw + .1f, .1f, .1f).Fb(front, NearTone.Trim, 0, .42f, .12f, gw + .1f, .1f, .1f).Fb(front, NearTone.Trim, 0, 2.3f, .11f, gw, .05f, .05f);
            // doors: two leaves between the frame posts the far level draws
            p.Fb(front, NearTone.Door, -.34f, 1.15f, .09f, .62f, 2.1f, .06f).Fb(front, NearTone.Door, .34f, 1.15f, .09f, .62f, 2.1f, .06f)
                .Fb(front, NearTone.Glass, -.34f, 1.5f, .125f, .4f, 1.1f, .03f).Fb(front, NearTone.Glass, .34f, 1.5f, .125f, .4f, 1.1f, .03f)
                .Fb(front, NearTone.Metal, -.08f, 1.05f, .15f, .04f, .5f, .04f).Fb(front, NearTone.Metal, .08f, 1.05f, .15f, .04f, .5f, .04f);
            if (!rich)
            {
                foreach (var sx in new[] { -1, 1 }) p.Fb(front, NearTone.Trim, sx * .67f, 1.15f, .12f, .18f, 2.45f, .2f);
                p.Fb(front, NearTone.Trim, 0, 2.4f, .12f, 1.52f, .18f, .2f);
            }
            var aw = w - 1.2f;
            p.Fb(front, NearTone.Accent, 0, 2.95f, 1.98f, aw, .36f, .06f);
            for (var k = -1; k <= 1; k += 2) p.Fbeam(front, NearTone.Metal, k * (aw / 2 - .8f), 3.6f, .05f, k * (aw / 2 - .8f), 3.05f, 1.9f, .05f);
            for (var k = 0; ; k++)
            {
                var u = -aw / 2 + .25f + k * 1f;
                if (u + .25f > aw / 2) break;
                p.Fb(front, NearTone.AccentLight, u, 3.205f, 1f, .5f, .03f, 1.98f);
            }
            // a threshold stone, then the roof: two air-handling boxes, vent pipes and a hatch inside the parapet
            p.Fb(front, NearTone.Stone, 0, .05f, .3f, 1.8f, .1f, .5f);
            var top = h + .63f;
            p.Box(NearTone.Metal, -w * .22f, top + .5f, -d * .12f, 2.2f, 1f, 1.5f).Box(NearTone.MetalDark, -w * .22f, top + 1.05f, -d * .12f, 2.1f, .12f, 1.4f)
                .Box(NearTone.Metal, w * .2f, top + .45f, d * .1f, 1.6f, .9f, 1.2f).Box(NearTone.Stone, w * .05f, top + .3f, d * .25f, 1.2f, .6f, 1.2f)
                .Box(NearTone.MetalDark, w * .05f, top + .65f, d * .25f, 1.3f, .1f, 1.3f);
            for (var i = 0; i < 3; i++) p.Drum(NearTone.MetalDark, -w * .05f + i * .7f, top, -d * .3f, .09f, 1.3f, 6);
            // downpipes at both front corners
            foreach (var sx in new[] { -1, 1 }) p.Beam(NearTone.Metal, sx * (w / 2 - .25f), h - .2f, -d / 2 - .06f, sx * (w / 2 - .25f), .08f, -d / 2 - .06f, .1f);
            return p;
        }

        // ---------------------------------------------------------------- the inn

        public static NearPlan Inn(float x, float z, float w, float d, bool rich)
        {
            var p = new NearPlan("inn");
            var h = 3 * 3.1f + .6f; const float rise = 6.2f;
            var front = NearFacade.Front(w, d); var rear = NearFacade.Rear(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            Courses(p, front, NearTone.PaintDark, w, .55f, h - .25f, .3f); Courses(p, rear, NearTone.PaintDark, w, .55f, h - .25f, .3f);
            Courses(p, left, NearTone.PaintDark, d, .55f, h - .25f, .3f); Courses(p, right, NearTone.PaintDark, d, .55f, h - .25f, .3f);
            p.Fb(front, NearTone.Trim, 0, h - .22f, .05f, w + .1f, .28f, .1f).Fb(rear, NearTone.Trim, 0, h - .22f, .05f, w + .1f, .28f, .1f)
                .Fb(left, NearTone.Trim, 0, h - .22f, .05f, d + .1f, .28f, .1f).Fb(right, NearTone.Trim, 0, h - .22f, .05f, d + .1f, .28f, .1f);
            // string courses between the floors
            for (var fl = 1; fl < 3; fl++)
            {
                var y = fl * 3.1f + .3f;
                p.Fb(front, NearTone.Trim, 0, y, .06f, w, .16f, .12f).Fb(rear, NearTone.Trim, 0, y, .06f, w, .16f, .12f)
                    .Fb(left, NearTone.Trim, 0, y, .06f, d, .16f, .12f).Fb(right, NearTone.Trim, 0, y, .06f, d, .16f, .12f);
            }
            var ws = new WindowStyle(NearTone.Trim, NearHash.Unit(x, z, 21) < .6f, false, 1);
            var plain = new WindowStyle(NearTone.Trim, false, false, 1);
            Windows(p, front, 0, w, 3, 3.1f, .9f, 1f, 1.5f, 2.6f, ws); Windows(p, rear, 0, w, 3, 3.1f, .9f, 1f, 1.5f, 2.6f, plain);
            Windows(p, left, 0, d, 3, 3.1f, .9f, 1f, 1.5f, 3.4f, plain); Windows(p, right, 0, d, 3, 3.1f, .9f, 1f, 1.5f, 3.4f, plain);
            // roof: gutters and slates on the long slopes
            var zg = (d + 1) / 2 + .1f;
            p.Box(NearTone.Metal, 0, h - .14f, -zg, w + 1f, .1f, .08f).Box(NearTone.Metal, 0, h - .14f, zg, w + 1f, .1f, .08f);
            RoofCourses(p, w + 1f, d + 1f, h, rise, NearTone.RoofDark);
            // the three dormers: frames round windows the far level drew
            for (var dx = -w / 2 + 4; dx <= w / 2 - 4 + .01f; dx += (w - 8) / 2)
            {
                var dz = -d / 2 + 2.6f; var fz = dz - 1.05f; var dy = h + 1.5f;
                p.Box(NearTone.Trim, dx, dy + .72f, fz, 1.3f, .1f, .08f).Box(NearTone.Trim, dx, dy - .68f, fz, 1.35f, .08f, .12f)
                    .Box(NearTone.Trim, dx - .6f, dy, fz, .08f, 1.3f, .08f).Box(NearTone.Trim, dx + .6f, dy, fz, .08f, 1.3f, .08f)
                    .Box(NearTone.Trim, dx, dy, fz - .01f, .05f, 1.25f, .06f).Box(NearTone.Trim, dx, dy + .1f, fz - .01f, 1f, .05f, .06f);
                p.Box(NearTone.PaintDark, dx - .8f, dy + .1f, dz - 1.03f, .22f, 1.9f, .04f).Box(NearTone.PaintDark, dx + .8f, dy + .1f, dz - 1.03f, .22f, 1.9f, .04f);
            }
            // chimneys: corbel, cap and pots
            foreach (var sx in new[] { -1, 1 })
            {
                var cx = sx * (w / 2 - 1.2f); var top = h + 4.5f + 2f + .12f;
                p.Box(NearTone.Brick, cx, top - .55f, 0, 1.6f, .14f, 1.6f).Box(NearTone.BrickLight, cx, top, 0, 1.7f, .16f, 1.7f)
                    .Drum(NearTone.BrickLight, cx - .35f, top + .08f, 0, .16f, .5f, 6).Drum(NearTone.BrickLight, cx + .35f, top + .08f, 0, .16f, .5f, 6);
            }
            // the door and a transom, and the veranda's rails either side of the central stair
            p.Fb(front, NearTone.DoorPanel, -.4f, .6f, .11f, .55f, .8f, .03f).Fb(front, NearTone.DoorPanel, .4f, .6f, .11f, .55f, .8f, .03f)
                .Fb(front, NearTone.Glass, -.4f, 1.7f, .11f, .55f, .8f, .03f).Fb(front, NearTone.Glass, .4f, 1.7f, .11f, .55f, .8f, .03f)
                .Fb(front, NearTone.Glass, 0, 2.52f, .1f, 1.5f, .2f, .03f).Fb(front, NearTone.Metal, .62f, 1.1f, .15f, .06f, .06f, .09f);
            if (!rich)
            {
                p.Fb(front, NearTone.Trim, -.82f, 1.15f, .12f, .18f, 2.45f, .2f).Fb(front, NearTone.Trim, .82f, 1.15f, .12f, .18f, 2.45f, .2f).Fb(front, NearTone.Trim, 0, 2.5f, .12f, 1.9f, .18f, .2f);
            }
            const float deck = .6f;
            var posts = new System.Collections.Generic.List<float>();
            for (var px = -w / 2 + .4f; px <= w / 2; px += w / 6) posts.Add(px);
            foreach (var px in posts)
                p.Fb(front, NearTone.Trim, px, deck + .15f, 3.0f, .36f, .3f, .36f).Fb(front, NearTone.Trim, px, 3.05f, 3.0f, .34f, .14f, .34f);
            void Rail(float u0, float u1)
            {
                var len = Math.Abs(u1 - u0); var mid = (u0 + u1) / 2;
                p.Fb(front, NearTone.Trim, mid, deck + .95f, 3.0f, len, .07f, .09f).Fb(front, NearTone.Trim, mid, deck + .22f, 3.0f, len, .06f, .06f);
                var n = (int)(len / .4f);
                for (var i = 1; i < n; i++) p.Fb(front, NearTone.Trim, Math.Min(u0, u1) + i * len / n, deck + .6f, 3.0f, .04f, .7f, .04f);
            }
            Rail(-w / 2 + .4f, -1.6f); Rail(1.6f, w / 2 - .5f);
            foreach (var sx in new[] { -1, 1 })
            {
                var ex = sx * (w / 2 - .4f);
                p.Fb(front, NearTone.Trim, ex, deck + .95f, 1.7f, .09f, .07f, 2.6f);
                for (var i = 1; i < 7; i++) p.Fb(front, NearTone.Trim, ex, deck + .6f, .3f + i * .4f, .04f, .7f, .04f);
            }
            p.Fb(front, NearTone.Stone, 0, .1f, 3.36f, 3f, .2f, .2f).Fb(front, NearTone.StoneDark, 0, .3f, 3.14f, 2.8f, .2f, .2f);
            // a bench either side of the door, and barrels
            foreach (var sx in new[] { -1, 1 })
            {
                var bx = sx * 3.2f;
                p.Fb(front, NearTone.Wood, bx, deck + .45f, .5f, 1.7f, .06f, .45f).Fb(front, NearTone.Wood, bx, deck + .85f, .2f, 1.7f, .5f, .05f)
                    .Fb(front, NearTone.WoodDark, bx - .75f, deck + .22f, .5f, .06f, .44f, .4f).Fb(front, NearTone.WoodDark, bx + .75f, deck + .22f, .5f, .06f, .44f, .4f);
                p.Fdrum(front, NearTone.Wood, sx * 5.1f, deck, 1.0f, .33f, .85f).Fdrum(front, NearTone.MetalDark, sx * 5.1f, deck + .5f, 1.0f, .35f, .06f);
            }
            return p;
        }

        // ---------------------------------------------------------------- sheds and boathouses

        /// <summary>
        /// A shed or boathouse: board-and-batten, a sliding door on its rail, a ramp, lobster traps and buoys. <paramref name="reach"/> is what its
        /// lot reserves; the boatyard's sheds only reserve a metre round the walls, so what stands outside stays inside that.
        /// </summary>
        public static NearPlan Shed(float x, float z, float w, float d, bool rich, NearReach reach)
        {
            var p = new NearPlan("shed");
            const float h = 5.2f; var doorX = -w * .2f;
            var front = NearFacade.Front(w, d); var rear = NearFacade.Rear(w, d); var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d);
            Ribs(p, front, NearTone.PaintDark, w, .3f, h - .2f, .75f); Ribs(p, rear, NearTone.PaintDark, w, .3f, h - .2f, .75f);
            Ribs(p, left, NearTone.PaintDark, d, .3f, h - .2f, .75f); Ribs(p, right, NearTone.PaintDark, d, .3f, h - .2f, .75f);
            // eaves board and verge boards
            p.Fb(front, NearTone.Trim, 0, h - .2f, .05f, w + .1f, .26f, .1f).Fb(rear, NearTone.Trim, 0, h - .2f, .05f, w + .1f, .26f, .1f);
            var zg = (d + .8f) / 2 + .1f;
            p.Box(NearTone.Metal, 0, h - .14f, -zg, w + .8f, .1f, .08f).Box(NearTone.Metal, 0, h - .14f, zg, w + .8f, .1f, .08f);
            RoofCourses(p, w + .8f, d + .8f, h, 2.6f, NearTone.RoofDark);
            // the big door: a frame, three rails, a diagonal brace, handle and the track it hangs from
            p.Fb(front, NearTone.Trim, doorX - 2.1f, 1.85f, .1f, .2f, 3.7f, .16f).Fb(front, NearTone.Trim, doorX + 2.1f, 1.85f, .1f, .2f, 3.7f, .16f);
            foreach (var v in new[] { .35f, 1.8f, 3.25f }) p.Fb(front, NearTone.Trim, doorX, v, .11f, 3.9f, .16f, .05f);
            p.Fb(front, NearTone.Trim, doorX, 1.8f, .11f, .16f, 3.5f, .05f);
            p.Fbeam(front, NearTone.Trim, doorX - 1.8f, .4f, .115f, doorX + 1.8f, 3.2f, .115f, .14f, .04f);
            p.Fb(front, NearTone.Metal, doorX + 1.6f, 1.3f, .15f, .08f, .4f, .06f);
            p.Fb(front, NearTone.MetalDark, doorX + 1.9f, 3.98f, .13f, 8f, .1f, .14f);
            for (var k = 0; k < 3; k++) p.Fb(front, NearTone.Metal, doorX - 1.6f + k * 1.7f, 3.88f, .12f, .12f, .22f, .1f);
            var yard = reach.Front <= 1.1f;
            // a plank ramp up to the sill
            var rampLen = yard ? .85f : 2.4f;
            p.Fbeam(front, NearTone.WoodLight, doorX, .02f, rampLen + .1f, doorX, .2f, .3f, 3.6f, .1f);
            // pots stacked at the corner, buoys on the wall, a coil of rope and a barrel
            var n0 = yard ? .45f : .7f; var cornerU = w / 2 - 1.1f;
            for (var layer = 0; layer < 3; layer++)
                for (var c = 0; c < 2; c++)
                    p.Fb(front, layer == 1 ? NearTone.Wood : NearTone.WoodDark, cornerU - c * 1.0f, .24f + layer * .46f, n0, .9f, .44f, .6f)
                        .Fb(front, NearTone.MetalDark, cornerU - c * 1.0f, .5f + layer * .46f, n0, .86f, .03f, .56f);
            var buoyTones = new[] { NearTone.Red, NearTone.Yellow, NearTone.Cream, NearTone.Orange };
            for (var k = 0; k < 4; k++)
            {
                var u = doorX + 2.55f + (k % 2) * .5f; var v = 1.0f + (k / 2) * .55f + .2f * (k % 3);
                if (u > w / 2 - .5f) u = w / 2 - .5f - k * .1f;
                p.Fdrum(front, buoyTones[k], u, v, .2f, .17f, .32f, 6, .1f).Fbeam(front, NearTone.Rope, u, v + .32f, .1f, u, v + .55f, .05f, .02f);
            }
            p.Fdrum(front, NearTone.Rope, doorX - 2.7f, 0, .35f, .35f, .18f, 8).Fdrum(front, NearTone.Wood, doorX - 3.4f, 0, .4f, .3f, .8f).Fdrum(front, NearTone.MetalDark, doorX - 3.4f, .45f, .4f, .32f, .06f);
            if (!rich)
                foreach (var sx in new[] { -1, 1 }) p.Fb(front, NearTone.Trim, doorX + sx * .67f, 1.15f, .12f, .18f, 2.45f, .2f);
            var st = new WindowStyle(NearTone.Trim, false, false, 1);
            Windows(p, front, w * .2f, w * .5f, 1, 3f, 1.4f, 1f, 1f, 2.6f, st);
            Windows(p, left, 0, d, 1, 3f, 1.4f, 1f, 1f, 3.2f, st);
            // a ladder on the right wall, within the reserved side
            if (reach.Side >= .5f)
            {
                var lz = -d * .15f;
                foreach (var k in new[] { -.25f, .25f })
                    p.Fb(right, NearTone.Wood, lz + k, 1.8f, .12f, .07f, 3.6f, .07f);
                for (var y = .4f; y < 3.5f; y += .38f) p.Fb(right, NearTone.Wood, lz, y, .12f, .5f, .05f, .05f);
            }
            return p;
        }

        // ---------------------------------------------------------------- the church

        public static NearPlan Church()
        {
            var p = new NearPlan("church");
            const float w = 12, d = 24, h = 9;
            var left = NearFacade.Left(w, d); var right = NearFacade.Right(w, d); var rear = NearFacade.Rear(w, d);
            Courses(p, left, NearTone.PaintDark, d, .5f, h - .3f, .3f); Courses(p, right, NearTone.PaintDark, d, .5f, h - .3f, .3f); Courses(p, rear, NearTone.PaintDark, w, .5f, h - .3f, .3f);
            // the nave's side windows (3.6 x 1.3, every 4.5 m from the tower end): stone frames and a pointed head
            foreach (var f in new[] { left, right })
                for (var zz = -d / 2 + 4; zz < d / 2 - 2; zz += 4.5f)
                {
                    float u = zz;
                    p.Fb(f, NearTone.Stone, u - .78f, 4.2f, .09f, .14f, 3.7f, .16f).Fb(f, NearTone.Stone, u + .78f, 4.2f, .09f, .14f, 3.7f, .16f)
                        .Fb(f, NearTone.Stone, u, 2.3f, .14f, 1.8f, .14f, .3f).Fb(f, NearTone.Stone, u, 6.1f, .1f, 1.6f, .14f, .2f)
                        .Fb(f, NearTone.Stone, u, 4.2f, .12f, .06f, 3.6f, .05f).Fb(f, NearTone.Stone, u, 4.7f, .12f, 1.3f, .06f, .05f);
                    // the head: a small gable over the lintel reads as the pointed arch
                    p.Gable(NearTone.Stone, f.X(u, .1f), 6.18f, f.Z(u, .1f), .18f, 1.5f, .5f, f.AX != 0 ? 90 : 0);
                    p.Fb(f, NearTone.WoodDark, u, 8.4f, .06f, 1.1f, .5f, .08f);
                }
            // buttresses between the windows
            foreach (var f in new[] { left, right })
                for (var zz = -d / 2 + 6.25f; zz < d / 2 - 3; zz += 4.5f)
                    p.Fb(f, NearTone.Stone, zz, 2.6f, .3f, .55f, 5.2f, .6f).Fb(f, NearTone.StoneDark, zz, 5.4f, .3f, .6f, .3f, .64f);
            // the tower: the double door and its arch, steps, string courses, louvres, a finial
            const float tz = -d / 2 - 2.0f; const float tw = 5;
            var tf = NearFacade.At(0, tz, 1, 0, 0, -1);
            // the tower's front face is at n = tw / 2 from its centre line
            var fz = tw / 2;
            p.Fb(tf, NearTone.Door, -.45f, 1.6f, fz + .13f, .85f, 3.1f, .05f).Fb(tf, NearTone.Door, .45f, 1.6f, fz + .13f, .85f, 3.1f, .05f)
                .Fb(tf, NearTone.DoorPanel, -.45f, 1.0f, fz + .1f, .6f, 1.1f, .03f).Fb(tf, NearTone.DoorPanel, .45f, 1.0f, fz + .1f, .6f, 1.1f, .03f)
                .Fb(tf, NearTone.DoorPanel, -.45f, 2.3f, fz + .1f, .6f, 1.1f, .03f).Fb(tf, NearTone.DoorPanel, .45f, 2.3f, fz + .1f, .6f, 1.1f, .03f)
                .Fb(tf, NearTone.Stone, -1.05f, 1.7f, fz + .1f, .26f, 3.4f, .2f).Fb(tf, NearTone.Stone, 1.05f, 1.7f, fz + .1f, .26f, 3.4f, .2f)
                .Fb(tf, NearTone.Stone, 0, 3.45f, fz + .1f, 2.4f, .24f, .24f)
                .Gable(NearTone.Stone, 0, 3.57f, tz - fz - .1f, .26f, 2.6f, .8f, 90);
            p.Fb(tf, NearTone.Stone, 0, .12f, fz + 1.1f, 3.6f, .24f, .8f).Fb(tf, NearTone.StoneDark, 0, .3f, fz + .8f, 3.2f, .24f, .6f);
            foreach (var y in new[] { 5.6f, 11.2f })
                p.Fb(tf, NearTone.Stone, 0, y, fz + .04f, tw + .1f, .2f, .12f).Fb(NearFacade.At(0, tz, 0, 1, 1, 0), NearTone.Stone, 0, y, tw / 2 + .04f, tw + .1f, .2f, .12f)
                    .Fb(NearFacade.At(0, tz, 0, 1, -1, 0), NearTone.Stone, 0, y, tw / 2 + .04f, tw + .1f, .2f, .12f);
            // tower corner buttresses at the foot, within the footprint
            foreach (var sx in new[] { -1, 1 })
                p.Fb(tf, NearTone.Stone, sx * (tw / 2 + .12f), 1.4f, fz + .1f, .3f, 2.8f, .3f);
            // belfry louvres: slats across the four dark openings the far level draws
            var bf = NearFacade.At(0, tz, 1, 0, 0, -1);
            for (var k = 0; k < 6; k++)
            {
                var y = 17.7f + k * .28f;
                p.Fb(bf, NearTone.WoodDark, 0, y, 1.95f + .05f, 1.2f, .1f, .08f);
                p.Fb(NearFacade.At(0, tz, 0, 1, 1, 0), NearTone.WoodDark, 0, y, 1.95f + .05f, 1.2f, .1f, .08f);
                p.Fb(NearFacade.At(0, tz, 0, 1, -1, 0), NearTone.WoodDark, 0, y, 1.95f + .05f, 1.2f, .1f, .08f);
            }
            // finial and vane on the spire (the far spire is a cone of 13 m from y 20.3)
            p.Beam(NearTone.MetalDark, 0, 33.2f, tz, 0, 35.2f, tz, .09f).Beam(NearTone.MetalDark, -.45f, 34.5f, tz, .45f, 34.5f, tz, .06f)
                .Box(NearTone.MetalDark, 0, 34.9f, tz, .05f, .5f, .7f);
            // gutters along the long eaves
            p.Box(NearTone.Metal, -((w + .8f) / 2 + .16f), h - .14f, 0, .12f, .1f, d + .8f).Box(NearTone.Metal, (w + .8f) / 2 + .16f, h - .14f, 0, .12f, .1f, d + .8f);
            p.Frame(90); RoofCourses(p, d + .8f, w + .8f, h, 5.5f, NearTone.RoofDark); p.Frame(0);
            return p;
        }
    }
}
