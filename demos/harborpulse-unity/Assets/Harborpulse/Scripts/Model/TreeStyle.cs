// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    public enum TreeKind { Spruce = 0, Fir = 1, Pine = 2, Maple = 3, Birch = 4, Oak = 5 }

    /// <summary>Detail by distance from the core: the cameras live in the core, so the bands are static and baked into the tile meshes.</summary>
    public enum TreeLod { Near = 0, Mid = 1, Far = 2 }

    /// <summary>
    /// The forest's design in one pure place: which species stand where (60 % conifers by count), their greens and autumn colours
    /// (broadleaf colour drifts in clusters, not per tree), their heights, their level of detail and the vertex cost the visual
    /// builder pays for each, so a sidecar test can total the budget without an engine.
    /// </summary>
    public static class TreeStyle
    {
        public const double NearM = 380, MidM = 800, PlantationM = 1700;

        public static bool IsConifer(TreeKind k) => k <= TreeKind.Pine;

        public static TreeLod Lod(double coreDistanceM) => coreDistanceM <= NearM ? TreeLod.Near : coreDistanceM <= MidM ? TreeLod.Mid : TreeLod.Far;

        /// <summary>A species from a uniform roll in [0, 1) and the position: 60 % conifers (spruce 30, fir 15, pine 15), 40 % broadleaf (maple 15, birch 10, oak 15), with a gentle stand-by-stand bias that averages out.</summary>
        public static TreeKind Pick(double roll, double x, double z)
        {
            var bias = (CoastalTerrain.Noise(x * .007 + 31, z * .007 - 17) - .5) * .36;
            var c = Math.Max(.02, Math.Min(.98, .6 + bias));
            if (roll < c)
            {
                var u = roll / c;
                return u < .5 ? TreeKind.Spruce : u < .75 ? TreeKind.Fir : TreeKind.Pine;
            }
            var v = (roll - c) / (1 - c);
            return v < .375 ? TreeKind.Maple : v < .625 ? TreeKind.Birch : TreeKind.Oak;
        }

        /// <summary>Height in metres: 8-16 in the forest, 5-10 in the yards.</summary>
        public static double Height(TreeKind k, double roll, bool yard)
        {
            if (yard) return 5 + 5 * roll;
            switch (k)
            {
                case TreeKind.Spruce: return 10 + 6 * roll;
                case TreeKind.Fir: return 9 + 5 * roll;
                case TreeKind.Pine: return 8 + 5 * roll;
                case TreeKind.Birch: return 9 + 5 * roll;
                case TreeKind.Oak: return 9 + 5 * roll;
                default: return 8 + 5 * roll;
            }
        }

        /// <summary>Planned crown spacing in metres by distance from the core: about 12 m beside the town (the vertex budget's price for full-detail crowns), thinning out into the haze.</summary>
        public static double Spacing(double coreDistanceM) =>
            12 + 3 * CoastalTerrain.Smooth(300, 800, coreDistanceM) + 6 * CoastalTerrain.Smooth(800, 1100, coreDistanceM) + 8 * CoastalTerrain.Smooth(1100, 1700, coreDistanceM);

        /// <summary>1 inside the plantation, fading to 0 at its edge so nothing pops where the forest ends.</summary>
        public static double Fade(double coreDistanceM) => 1 - CoastalTerrain.Smooth(1250, PlantationM, coreDistanceM);

        /// <summary>Vertices one tree costs in the visual builder (flat shading: every triangle owns three).</summary>
        public static int Vertices(TreeKind k, TreeLod lod)
        {
            switch (lod)
            {
                case TreeLod.Near:
                    // trunk + crown pieces, exactly as TreeBuilder emits them
                    switch (k)
                    {
                        case TreeKind.Spruce: return 4 * 7 + 5 * 7 * 3;
                        case TreeKind.Fir: return 4 * 7 + 4 * 7 * 3;
                        case TreeKind.Pine: return 5 * 7 + 4 * 7 * 3;
                        case TreeKind.Birch: return 5 * 7 + 96;
                        default: return 5 * 7 + 96 + 24 + 4 * 7; // maple, oak: one side lobe on a short branch
                    }
                case TreeLod.Mid:
                    return IsConifer(k) ? 3 * 7 + 2 * 5 * 3 : 3 * 7 + 24;
                default:
                    return IsConifer(k) ? (k == TreeKind.Spruce ? 2 : 1) * 4 * 3 : 24;
            }
        }

        // ---------------------------------------------------------------- colour (display RGB, 0 .. 1)

        public readonly struct Colour
        {
            public Colour(double r, double g, double b) { R = r; G = g; B = b; }
            public double R { get; }
            public double G { get; }
            public double B { get; }
            public Colour Scale(double s) => new Colour(R * s, G * s, B * s);
            public static Colour Lerp(Colour a, Colour b, double t) => new Colour(a.R + (b.R - a.R) * t, a.G + (b.G - a.G) * t, a.B + (b.B - a.B) * t);
        }

        static readonly Colour DeepGreen = new Colour(.16, .34, .19), LightGreen = new Colour(.28, .44, .24), BlueGreen = new Colour(.17, .33, .30);
        static readonly Colour Gold = new Colour(.84, .66, .18), Orange = new Colour(.82, .42, .12), Crimson = new Colour(.66, .15, .12),
            Russet = new Colour(.56, .27, .12), Olive = new Colour(.40, .45, .18), LeafGreen = new Colour(.26, .42, .17);

        /// <summary>
        /// A conifer's body green, lit for a tone-mapped, shadowless frame: mid to light greens, about 15 % blue-green, the top tier a quarter
        /// lighter than the lowest (<paramref name="tier"/> 0 low .. 1 top), and whole stands a little lighter or darker (drifts, by position).
        /// </summary>
        public static Colour Conifer(TreeKind k, double roll, double tier, double x = 0, double z = 0)
        {
            var body = Colour.Lerp(DeepGreen, LightGreen, roll);
            if (roll > .85) body = Colour.Lerp(body, BlueGreen, .8);
            if (k == TreeKind.Fir) body = body.Scale(.94);
            else if (k == TreeKind.Pine) body = Colour.Lerp(body, new Colour(.27, .40, .19), .3);
            var stand = .88 + .24 * CoastalTerrain.Noise(x * .011 + 23, z * .011 - 5);
            return body.Scale(stand * (.92 + .23 * tier));
        }

        /// <summary>
        /// A broadleaf crown's autumn colour. The tone follows a slow position noise, so gold, orange and crimson come in drifts of a
        /// few dozen metres; each species keeps its own range of it (birch yellow, maple red and orange, oak russet).
        /// </summary>
        public static Colour Broadleaf(TreeKind k, double x, double z, double roll)
        {
            var drift = CoastalTerrain.Noise(x * .0055 + 11, z * .0055 + 3) + (roll - .5) * .12;
            var bin = drift < .2 ? 0 : drift < .38 ? 1 : drift < .56 ? 2 : drift < .74 ? 3 : 4;
            Colour c;
            switch (k)
            {
                case TreeKind.Maple: c = bin == 0 ? LeafGreen : bin == 1 ? Gold : bin == 2 ? Orange : Crimson; break;
                case TreeKind.Birch: c = bin == 0 ? Olive : bin == 4 ? Colour.Lerp(Gold, Orange, .35) : Gold; break;
                default: c = bin == 0 ? Olive : bin == 1 ? Colour.Lerp(Russet, Gold, .5) : bin == 2 ? Colour.Lerp(Orange, Russet, .5) : Russet; break;
            }
            return c.Scale(.92 + .16 * roll);
        }

        /// <summary>Trunk colour: pale for birch, dark brown otherwise.</summary>
        public static Colour Bark(TreeKind k) => k == TreeKind.Birch ? new Colour(.72, .70, .61) : new Colour(.27, .22, .17);
    }
}
