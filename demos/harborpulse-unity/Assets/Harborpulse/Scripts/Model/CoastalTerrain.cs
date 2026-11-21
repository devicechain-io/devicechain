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

        // ---------------------------------------------------------------- the distant ridge and the hills in front of it

        /// <summary>
        /// One layer of the backdrop: a coarse strip of hills some way behind the harbour. Each is lower in the frame than the one behind it, and
        /// the haze takes a little more of each with distance, so from HOME the land between the last trees and the skyline reads as successive
        /// ranges instead of one pale band.
        /// </summary>
        public sealed class HillLayer
        {
            public HillLayer(string name, double[] rowZ, double[] rowFraction, double[][] hills, double halfWidth, double columnStep, double taperWestM, double taperEastM, double footFadeM, double treeLineM, int seed,
                int side = 0, double alongCentre = 0)
            {
                Name = name; RowZ = rowZ; RowFraction = rowFraction; Hills = hills; HalfWidth = halfWidth; ColumnStep = columnStep; TaperWestM = taperWestM; TaperEastM = taperEastM; FootFadeM = footFadeM;
                TreeLineM = treeLineM; Seed = seed; Side = side; AlongCentre = alongCentre;
            }

            public string Name { get; }
            /// <summary>
            /// Where the layer stands: 0 behind the harbour to the north (it runs along x, its rows step back in z), +1 to the east and -1 to
            /// the west (it runs along z, its rows step out in |x|). <see cref="RowZ"/>, the hills' centres and the tapers are then measured
            /// in the layer's own "along" and "back" directions (<see cref="Along"/>, <see cref="Back"/>).
            /// </summary>
            public int Side { get; }
            /// <summary>The middle of the layer's run, in its along coordinate (0 for the northern layers).</summary>
            public double AlongCentre { get; }
            public double Along(double x, double z) => Side == 0 ? x : z;
            public double Back(double x, double z) => Side == 0 ? z : Side * x;
            /// <summary>The WORLD x and z of a point at an along and a back coordinate.</summary>
            public void World(double along, double back, out double x, out double z)
            {
                if (Side == 0) { x = along; z = back; } else { x = Side * back; z = along; }
            }
            /// <summary>Row values of the layer's mesh in its back coordinate (z for a northern layer), and each row's fraction of the crest height (front foot to hidden back slope).</summary>
            public double[] RowZ { get; }
            public double[] RowFraction { get; }
            /// <summary>Centre x, half-width, crest height above the deck, shape (0 round dome, 1 cusped, 2 plateau).</summary>
            public double[][] Hills { get; }
            public double HalfWidth { get; }
            public double ColumnStep { get; }
            /// <summary>The crest falls to nothing over these distances inside the west and east ends.</summary>
            public double TaperWestM { get; }
            public double TaperEastM { get; }
            /// <summary>The layer's foot dissolves into the ground haze over this height above the deck.</summary>
            public double FootFadeM { get; }
            /// <summary>The crest's broken tree line: up to this many metres are added, column by column, where a hill stands.</summary>
            public double TreeLineM { get; }
            public int Seed { get; }
            public double CrestZ => RowZ[Array.IndexOf(RowFraction, 1.0)];

            /// <summary>The crest line: height above the deck at an along coordinate (x for a northern layer). Hills overlap, each with its own profile; it falls to nothing at both ends.</summary>
            public double Crest(double x)
            {
                var best = 0.0;
                foreach (var h in Hills)
                {
                    var u = Math.Min(1, Math.Abs(x - h[0]) / h[1]);
                    double v;
                    if (h[3] == 0) v = Math.Pow(Math.Cos(u * Math.PI / 2), 1.7);
                    else if (h[3] == 1) v = Math.Pow(1 - u, 1.45);
                    else v = Smooth(1, .42, u);
                    best = Math.Max(best, h[2] * v);
                }
                if (TreeLineM > 0) best += TreeLineM * Noise(x / ColumnStep + Seed * 17.3, Seed * 3.7) * Smooth(2, 14, best);
                var u0 = x - AlongCentre;
                return best * Smooth(-HalfWidth, -HalfWidth + TaperWestM, u0) * (1 - Smooth(HalfWidth - TaperEastM, HalfWidth, u0));
            }

            /// <summary>The layer's height in WORLD metres at (x, z): linear between the mesh's rows, the deck in front of the first and beyond its ends.</summary>
            public double Height(double worldX, double worldZ)
            {
                var x = Along(worldX, worldZ); var z = Back(worldX, worldZ);
                if (Math.Abs(x - AlongCentre) >= HalfWidth) return SitePlan.DeckHeightM;
                if (z <= RowZ[0]) return SitePlan.DeckHeightM;
                if (z >= RowZ[RowZ.Length - 1]) return SitePlan.DeckHeightM + Crest(x) * RowFraction[RowFraction.Length - 1];
                var i = 1;
                while (z > RowZ[i]) i++;
                var t = (z - RowZ[i - 1]) / (RowZ[i] - RowZ[i - 1]);
                return SitePlan.DeckHeightM + Crest(x) * (RowFraction[i - 1] + (RowFraction[i] - RowFraction[i - 1]) * t);
            }
        }

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

        /// <summary>The far ridge that carries the skyline: 2.35-3.05 km out, its highest crest 124 m.</summary>
        public static readonly HillLayer Ridge = new HillLayer("Distant ridge", RidgeRowZ, RidgeRowFraction, RidgeHills, RidgeHalfWidth, RidgeColumnStep, 550, 600, 50, 0, 1);

        /// <summary>
        /// Wooded foothills just past the last trees (1.88-2.13 km out, crests 30-44 m) and a middle range behind them (2.15-2.45 km, 52-76 m).
        /// Their summits stand between the far ridge's, so the three crest lines cross instead of running parallel, and every one of them is
        /// below HOME's eye line: the skyline is still the far ridge's and the sky keeps its share.
        /// </summary>
        public static readonly HillLayer Foothills = new HillLayer("Foothills", new double[] { 1880, 1930, 1990, 2060, 2130 }, new[] { 0, .45, .9, 1, .5 },
            new[]
            {
                new[] { -560.0, 520, 36, 0 }, new[] { 560.0, 600, 44, 2 }, new[] { -1500.0, 560, 30, 1 }, new[] { 1650.0, 520, 38, 0 },
                new[] { -2300.0, 420, 22, 0 }, new[] { 30.0, 300, 20, 0 },
            }, 2700, 75, 500, 500, 14, 9, 2);

        public static readonly HillLayer MidHills = new HillLayer("Middle range", new double[] { 2150, 2210, 2280, 2360, 2450 }, new[] { 0, .4, .85, 1, .55 },
            new[]
            {
                new[] { 180.0, 640, 60, 1 }, new[] { -1020.0, 700, 76, 0 }, new[] { 1380.0, 620, 68, 2 }, new[] { -2100.0, 480, 44, 0 },
                new[] { 2250.0, 380, 30, 1 },
            }, 2700, 100, 500, 500, 26, 8, 3);

        /// <summary>The backdrop behind the harbour, nearest first: what HOME's 16:9 frame looks at.</summary>
        public static readonly HillLayer[] Backdrop = { Foothills, MidHills, Ridge };

        // The uplands east and west of the town run from the coast north to the ridge's foot. HOME's 16:9 frame holds none of them; the 9:16
        // HOME looks east along the harbour straight at the eastern pair, and the ground-level cameras that look east or west get a skyline.
        const double UplandCentreZ = 1130, UplandHalf = 1270;
        static readonly double[] UplandFootRows = { 1960, 2010, 2070, 2140, 2210 }, UplandFootFraction = { 0, .45, .9, 1, .5 };
        static readonly double[] UplandRangeRows = { 2400, 2480, 2590, 2700, 2820, 2940 }, UplandRangeFraction = { 0, .35, .8, 1, .85, .4 };

        public static readonly HillLayer EastFoothills = new HillLayer("East foothills", UplandFootRows, UplandFootFraction,
            new[] { new[] { 260.0, 430, 40, 0 }, new[] { 1150.0, 520, 34, 2 }, new[] { 1920.0, 450, 44, 1 } }, UplandHalf, 75, 380, 400, 14, 8, 4, 1, UplandCentreZ);

        public static readonly HillLayer EastRange = new HillLayer("East range", UplandRangeRows, UplandRangeFraction,
            new[] { new[] { 150.0, 520, 96, 1 }, new[] { 900.0, 620, 124, 0 }, new[] { 1850.0, 560, 84, 2 } }, UplandHalf, 127, 420, 420, 50, 0, 5, 1, UplandCentreZ);

        public static readonly HillLayer WestFoothills = new HillLayer("West foothills", UplandFootRows, UplandFootFraction,
            new[] { new[] { 220.0, 400, 34, 2 }, new[] { 1000.0, 500, 42, 0 }, new[] { 1800.0, 480, 30, 1 } }, UplandHalf, 75, 380, 400, 14, 8, 6, -1, UplandCentreZ);

        public static readonly HillLayer WestRange = new HillLayer("West range", UplandRangeRows, UplandRangeFraction,
            new[] { new[] { 420.0, 600, 110, 0 }, new[] { 1300.0, 560, 82, 1 }, new[] { 2050.0, 400, 60, 0 } }, UplandHalf, 127, 420, 420, 50, 0, 7, -1, UplandCentreZ);

        /// <summary>The uplands to the east and west, nearest first on each side.</summary>
        public static readonly HillLayer[] Uplands = { EastFoothills, EastRange, WestFoothills, WestRange };

        /// <summary>Every layer the backdrop builder draws.</summary>
        public static readonly HillLayer[] HillLayers = { Foothills, MidHills, Ridge, EastFoothills, EastRange, WestFoothills, WestRange };

        /// <summary>The far ridge's crest line: height above the deck at x.</summary>
        public static double RidgeCrest(double x) => Ridge.Crest(x);

        /// <summary>The far ridge's height in WORLD metres at (x, z): linear between the mesh's rows.</summary>
        public static double RidgeHeight(double x, double z) => Ridge.Height(x, z);

        // ---------------------------------------------------------------- the rocky foreshore

        /// <summary>
        /// The coast outside the harbour is a straight line in the terrain's grid; a rocky foreshore in front of it gives it points and coves.
        /// It begins <see cref="ShoreStartX"/> either side of the harbour's middle, clear of the breakwater roots, the depot and the car park,
        /// and of everything HOME's 16:9 frame holds at the coast.
        /// </summary>
        public const double ShoreStartX = 290, ShoreFullX = 380, ShoreMaxReachM = 190;

        // centre x, half-width, extra reach: the named points
        static readonly double[][] ShorePoints =
        {
            new[] { 640.0, 120, 85 }, new[] { -780.0, 160, 110 }, new[] { 1520.0, 240, 130 }, new[] { -1750.0, 200, 95 }, new[] { 2600.0, 300, 120 },
        };

        /// <summary>How far the foreshore runs out from the coast at x, in metres: nothing by the harbour, 6-60 m of ledge along most of the coast, more at the named points.</summary>
        public static double ShoreReach(double x)
        {
            var open = Smooth(ShoreStartX, ShoreFullX, Math.Abs(x));
            if (open <= 0) return 0;
            var broad = Noise(x * .0035 + 31.7, 2.3);
            var fine = Noise(x * .021 + 5.1, 9.9);
            var reach = 6 + 44 * Smooth(.3, .85, broad) + 16 * fine;
            foreach (var p in ShorePoints)
            {
                var u = (x - p[0]) / p[1];
                reach += p[2] * Math.Exp(-u * u);
            }
            return Math.Min(ShoreMaxReachM, reach) * open;
        }

        /// <summary>
        /// The foreshore's height at a fraction <paramref name="t"/> of the way out (0 at the coast, 1 at its seaward edge): level with the
        /// deck at the coast, a ledge that stays above the highest water over its inner part, then down under the lowest water at the edge,
        /// so the waterline wanders with the tide instead of following a mesh edge.
        /// </summary>
        public static double ShoreHeight(double t)
        {
            t = Math.Max(0, Math.Min(1, t));
            return SitePlan.DeckHeightM + .4 - (SitePlan.DeckHeightM + 3.4) * t * t;
        }

        /// <summary>Offshore skerries: x, z, radius, height above the datum. All stand off the open coast, outside what HOME's 16:9 frame holds of the sea.</summary>
        public static readonly double[][] Skerries =
        {
            new[] { 575.0, -262, 15, 3.4 }, new[] { 668.0, -300, 9, 2.0 }, new[] { 930.0, -236, 19, 4.2 }, new[] { 1480.0, -330, 24, 5.0 },
            new[] { -545.0, -250, 12, 2.6 }, new[] { -870.0, -310, 21, 4.6 }, new[] { -925.0, -272, 8, 1.8 }, new[] { -1330.0, -240, 16, 3.6 },
        };

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
