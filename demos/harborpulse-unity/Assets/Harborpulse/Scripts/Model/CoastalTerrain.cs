// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>Seeded WORLD landform. The mapped harbour stays flat; a low coastal ridge has spurs, a saddle and one rounded summit.</summary>
    public static class CoastalTerrain
    {
        public const double CoreHalfX = 240, CoreNorthZ = 140, CoastZ = -140;
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

        public static double Height(double x, double z)
        {
            var d = CoreDistance(x, z);
            if (d == 0) return SitePlan.DeckHeightM;
            var rise = 30 * Smooth(0, 400, d);
            var hill = Smooth(300, 720, d) * (8 + 12 * Noise(x * .0024 + 3.1, z * .0024 - 7.3));
            // HOME's central rounded summit, with two lower shoulders and an exposed western saddle.
            var summit = 64 * Mound(x, z, -90, 1050, 270, 280);
            var spur = 24 * Mound(x, z, -560, 760, 240, 380) + 20 * Mound(x, z, 650, 700, 190, 360);
            var saddle = 10 * Mound(x, z, -340, 980, 140, 260);
            var shape = (summit + spur - saddle) * Smooth(300, 650, d);
            // No rising far wall: the land settles behind the skyline and leaves the sky visible.
            return SitePlan.DeckHeightM + (rise + hill + shape) * Smooth(CoastZ, CoastZ + 120, z)
                * (1 - Smooth(1250, 2200, d));
        }

        /// <summary>Open summit ledges and broad meadow gaps, independent of generation order.</summary>
        public static bool Clearing(double x, double z) =>
            Mound(x, z, -90, 1050, 330, 300) > .30 ||
            Mound(x, z, -300, 650, 170, 240) > .42 || Noise(x * .005 + 2, z * .005 + 9) < .34;

        /// <summary>Broad connected woodland with an irregular edge, rather than a uniform scatter of isolated trees.</summary>
        public static bool Woodland(double x, double z) => !Clearing(x, z) &&
            Noise(x * .008 + 13, z * .008 - 4) + .18 * Noise(x * .027, z * .027 + 8) > .67;

        // No tiny crowns on the receding land behind the summit. Test the whole crown, not just its trunk.
        public static bool ForestFits(double x, double z, double height) =>
            z + height * .56 <= 1000 && CoreDistance(x, z) + height * .56 <= 1100;

        /// <summary>Keep exactly three in five accepted house lots, without changing their street or collision reservations.</summary>
        public static bool KeepHouse(int acceptedLot) => acceptedLot % 5 < 3;

        // Stable candidate thinning gives an upper bound of 20% of the old forest, before the new meadow exclusions.
        public static bool KeepTree(int candidate) => candidate % 20 < 4;
    }
}
