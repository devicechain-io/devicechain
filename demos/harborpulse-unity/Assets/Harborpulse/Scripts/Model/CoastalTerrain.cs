// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// Seeded WORLD landform. The mapped harbour stays flat; the town sits on a low apron, and beyond it the land rolls in
    /// gentle knolls and hollows that stay far below the camera's eye line at HOME. The skyline is given to a separate,
    /// distant, hazy ridge (<see cref="RidgeHeight"/>) rather than to the near ground.
    /// </summary>
    public static class CoastalTerrain
    {
        public const double CoreHalfX = 240, CoreNorthZ = 140, CoastZ = -140;

        /// <summary>No ground within this distance of the core stands more than <see cref="MaxReliefM"/> above the deck.</summary>
        public const double RollingReachM = 1300, MaxReliefM = 32;

        /// <summary>The terrain mesh runs north to this z, where the ridge's foot begins, and east and west to <see cref="TerrainHalfWidthM"/>:
        /// flat low land, hazed out long before its edge, so no edge shows in the frame inside the 3.6 km far clip.</summary>
        public const double TerrainReachM = 2400, TerrainHalfWidthM = 3800;

        public static double CoreDistance(double x, double z)
        {
            var dx = Math.Max(0, Math.Abs(x) - CoreHalfX);
            var dz = Math.Max(0, z - CoreNorthZ);
            return Math.Sqrt(dx * dx + dz * dz);
        }

        public static double Smooth(double a, double b, double x)
        {
            var t = Math.Max(0, Math.Min(1, (x - a) / (b - a)));
            return t * t * (3 - 2 * t);
        }

        public static double Hash(int x, int z)
        {
            unchecked
            {
                var h = (uint)(x * 374761393 + z * 668265263);
                h = (h ^ (h >> 13)) * 1274126177u;
                return ((h ^ (h >> 16)) & 0xffffff) / 16777215.0;
            }
        }

        public static double Noise(double x, double z)
        {
            int ix = (int)Math.Floor(x), iz = (int)Math.Floor(z);
            var fx = Smooth(0, 1, x - ix); var fz = Smooth(0, 1, z - iz);
            var a = Hash(ix, iz) * (1 - fx) + Hash(ix + 1, iz) * fx;
            var b = Hash(ix, iz + 1) * (1 - fx) + Hash(ix + 1, iz + 1) * fx;
            return a * (1 - fz) + b * fz;
        }

        static double Mound(double x, double z, double cx, double cz, double sx, double sz)
        {
            x = (x - cx) / sx; z = (z - cz) / sz;
            return Math.Exp(-(x * x + z * z));
        }

        // centre x, centre z, half-width x, half-width z, height (negative: a hollow)
        static readonly double[][] Knolls =
        {
            new[] { -330.0, 760, 230, 190, 12 }, new[] { 440.0, 640, 200, 220, 10 }, new[] { -80.0, 1060, 300, 250, 12 },
            new[] { 720.0, 1130, 220, 200, 11 }, new[] { -780.0, 560, 210, 180, 7 }, new[] { 1050.0, 700, 260, 200, 7 },
            new[] { -1050.0, 1000, 250, 220, 9 }, new[] { 150.0, 430, 160, 120, 4 },
            new[] { -120.0, 900, 200, 160, -6 }, new[] { 300.0, 930, 210, 170, -5 }, new[] { -560.0, 1180, 180, 150, -5 },
        };

        /// <summary>Signed rolling relief in metres: a broad swell, a knoll-scale ripple and the named knolls and hollows.</summary>
        static double Roll(double x, double z)
        {
            var swell = (Noise(x * .0021 + 3.1, z * .0021 - 7.3) - .5) * 2 * 6;
            var ripple = (Noise(x * .0058 + 1.7, z * .0058 + 5.2) - .5) * 2 * 3.5;
            var knolls = 0.0;
            foreach (var k in Knolls) knolls += k[4] * Mound(x, z, k[0], k[1], k[2], k[3]);
            return swell + ripple + knolls;
        }

        /// <summary>Ground height in WORLD metres: the deck inside the core, a low apron outside it (about 9 m by 450 m), then rolling land.</summary>
        public static double Height(double x, double z)
        {
            var d = CoreDistance(x, z);
            if (d == 0) return SitePlan.DeckHeightM;
            var apron = 9.5 * Smooth(0, 450, d);
            var rolling = Roll(x, z) * Smooth(240, 760, d);
            var lift = Math.Max(0, apron + rolling);
            // A smooth ceiling: almost the identity for small lifts, never above MaxReliefM.
            lift = MaxReliefM * Math.Tanh(lift / MaxReliefM);
            // The land settles to the deck again before the terrain mesh ends; the ridge carries the skyline.
            return SitePlan.DeckHeightM + lift * Smooth(CoastZ, CoastZ + 120, z) * (1 - Smooth(1250, 2100, d));
        }

        // ---------------------------------------------------------------- the distant ridge

        /// <summary>Row z values of the ridge mesh, and each row's fraction of the crest height (front foot to hidden back slope).</summary>
        public static readonly double[] RidgeRowZ = { 2350, 2480, 2620, 2760, 2900, 3050 };
        public static readonly double[] RidgeRowFraction = { 0, .35, .8, 1, .85, .4 };
        public const double RidgeColumnStep = 150, RidgeHalfWidth = 2400;

        // centre x, half-width, crest height above the deck, shape (0 round dome, 1 cusped, 2 plateau)
        static readonly double[][] RidgeHills =
        {
            new[] { -420.0, 760, 124, 0 }, new[] { 760.0, 560, 90, 1 }, new[] { -1400.0, 640, 62, 2 },
            new[] { 1700.0, 560, 48, 0 }, new[] { -2050.0, 420, 26, 1 },
        };

        /// <summary>The crest line: height above the deck at x. Two or three hills overlap, each with its own profile; it falls to nothing at both ends.</summary>
        public static double RidgeCrest(double x)
        {
            var best = 0.0;
            foreach (var h in RidgeHills)
            {
                var u = Math.Min(1, Math.Abs(x - h[0]) / h[1]);
                double v;
                if (h[3] == 0) v = Math.Pow(Math.Cos(u * Math.PI / 2), 1.7);
                else if (h[3] == 1) v = Math.Pow(1 - u, 1.45);
                else v = Smooth(1, .42, u);
                best = Math.Max(best, h[2] * v);
            }
            return best * Smooth(-RidgeHalfWidth, -1850, x) * (1 - Smooth(1800, RidgeHalfWidth, x));
        }

        /// <summary>The ridge's height in WORLD metres at (x, z): linear between the mesh's rows.</summary>
        public static double RidgeHeight(double x, double z)
        {
            if (z <= RidgeRowZ[0]) return SitePlan.DeckHeightM;
            if (z >= RidgeRowZ[RidgeRowZ.Length - 1]) return SitePlan.DeckHeightM + RidgeCrest(x) * RidgeRowFraction[RidgeRowFraction.Length - 1];
            var i = 1;
            while (z > RidgeRowZ[i]) i++;
            var t = (z - RidgeRowZ[i - 1]) / (RidgeRowZ[i] - RidgeRowZ[i - 1]);
            return SitePlan.DeckHeightM + RidgeCrest(x) * (RidgeRowFraction[i - 1] + (RidgeRowFraction[i] - RidgeRowFraction[i - 1]) * t);
        }

        // ---------------------------------------------------------------- what grows where

        // centre x, centre z, radius: meadow clearings of 70-180 m. The first six are where HOME looks: on the rising ground behind the
        // town and on both flanks, two of them touching the town's edge. The rest are further out; one sits on a knoll's summit.
        static readonly double[][] Clearings =
        {
            new[] { -250.0, 372, 90 }, new[] { 330.0, 370, 105 }, new[] { 40.0, 610, 85 }, new[] { -430.0, 650, 110 },
            new[] { 480.0, 700, 120 }, new[] { -130.0, 820, 130 },
            new[] { -640.0, 300, 75 }, new[] { -80.0, 1060, 150 }, new[] { -520.0, 880, 125 },
            new[] { 830.0, 900, 130 }, new[] { -950.0, 790, 100 }, new[] { 950.0, 430, 90 }, new[] { -330.0, 1000, 110 },
        };

        /// <summary>The number of meadows (the first ones) placed where HOME sees them.</summary>
        public const int VisibleMeadows = 6;
        public static int MeadowCount => Clearings.Length;
        /// <summary>A meadow's centre and nominal radius.</summary>
        public static (double X, double Z, double R) Meadow(int i) => (Clearings[i][0], Clearings[i][1], Clearings[i][2]);

        /// <summary>1 inside a clearing, fading to 0 across its irregular edge.</summary>
        public static double ClearingWeight(double x, double z)
        {
            var best = 0.0;
            var wobble = .78 + .44 * Noise(x * .021 + 2, z * .021 + 9);
            foreach (var c in Clearings)
            {
                var dx = x - c[0]; var dz = z - c[1];
                var u = Math.Sqrt(dx * dx + dz * dz) / (c[2] * wobble);
                if (u < 1.2) best = Math.Max(best, 1 - Smooth(.82, 1.0, u));
            }
            return best;
        }

        /// <summary>Open meadow: a clearing, or a patch where the wood thins out.</summary>
        public static bool Clearing(double x, double z) => !Woodland(x, z);

        /// <summary>Broad connected stands with an irregular edge: three scales of value noise.</summary>
        public static double ForestField(double x, double z) =>
            Noise(x * .0085 + 13, z * .0085 - 4) * .56 + Noise(x * .026, z * .026 + 8) * .30 + Noise(x * .075 + 5, z * .075) * .14;

        /// <summary>Where <see cref="ForestField"/> is above this the land is wooded; tuned so about 70 % of the open land within 1100 m is.</summary>
        public const double WoodThreshold = .35;

        public static bool Woodland(double x, double z) => ClearingWeight(x, z) <= .5 && ForestField(x, z) > WoodThreshold;

        /// <summary>0 .. 1: how much of a spot is forest floor (smooth, for the ground's colour; <see cref="Woodland"/> is its hard version).</summary>
        public static double ForestCover(double x, double z) =>
            Smooth(WoodThreshold - .035, WoodThreshold + .045, ForestField(x, z)) * (1 - Smooth(.2, .75, ClearingWeight(x, z)));

        /// <summary>Keep exactly three in five accepted house lots, without changing their street or collision reservations.</summary>
        public static bool KeepHouse(int acceptedLot) => acceptedLot % 5 < 3;
    }
}
