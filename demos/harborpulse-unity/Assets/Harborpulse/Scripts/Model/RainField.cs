// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>One rain streak at one instant: where the drop is, and the vector back along its path that a shutter would smear.</summary>
    public readonly struct RainStreak
    {
        public RainStreak(double x, double y, double z, double tailX, double tailY, double tailZ) { X = x; Y = y; Z = z; TailX = tailX; TailY = tailY; TailZ = tailZ; }
        public double X { get; }
        public double Y { get; }
        public double Z { get; }
        /// <summary>From the drop back to where it was one exposure ago: opposite its velocity, its length the fall speed times the exposure.</summary>
        public double TailX { get; }
        public double TailY { get; }
        public double TailZ { get; }
    }

    /// <summary>
    /// The rain, as a pure function of the wave clock: visual only (WORLD weather), never a reading.
    ///
    /// Each drop falls through the world at its own terminal speed, pushed by the wind, and is drawn where it is inside a box around
    /// the eye (it re-enters at the top when it leaves the bottom). The first rain was one rigid sheet of streaks that repeated
    /// every few metres of height, slid down and wrapped: its repeat shrank with the rain's strength, so in light rain the sheet
    /// fell further in one frame than its own repeat and the eye read it standing still or climbing (a wagon wheel); it was
    /// squeezed about the eye as the strength changed, which moved every streak up or down; and it travelled with the camera.
    /// Here nothing repeats inside the box, a drop's speed is its own, the camera moving does not move a drop, and strength
    /// changes how many drops there are, never where one is.
    /// </summary>
    public static class RainField
    {
        /// <summary>The box around the eye, metres: the drops live in the world, and are drawn where they are inside it.</summary>
        public const double BoxX = 50, BoxY = 24, BoxZ = 50;
        /// <summary>A raindrop's terminal speed, m/s: large drops fall at about nine.</summary>
        public const double FallMin = 8, FallMax = 10;
        /// <summary>The shutter a streak is smeared over, seconds: about half a metre of fall.</summary>
        public const double ExposureS = 0.042;

        /// <summary>
        /// How much of a streak is there, and how wide it is drawn per metre from the eye. Rain is weather, not the subject: it is
        /// translucent and about two thirds of a pixel wide, so it never overpowers a device, a pin or the story; how hard it
        /// rains is said by how many streaks there are.
        /// </summary>
        public const double Opacity = 0.3, WidthPerMetre = 0.00042, MinWidthM = 0.002;
        /// <summary>Drops at full strength. A lighter rain draws the first share of them.</summary>
        public const int Capacity = 2600;
        /// <summary>Closer to the lens than this a streak would cross the whole frame; it is not drawn.</summary>
        public const double LensClearM = 5;

        /// <summary>The wind on the rain, m/s along x and z: it leans the streaks seaward and west, harder as the storm builds.</summary>
        public static void Wind(Weather weather, out double x, out double z)
        {
            var strength = 1.0 + 2.5 * Rgb.Clamp01(weather.Storm);
            x = -0.977 * strength; z = 0.21 * strength;
        }

        /// <summary>How many of the drops a rain of this strength draws.</summary>
        public static int Count(Weather weather) => weather.Rain <= 0.01 ? 0 : (int)Math.Round(Capacity * Math.Sqrt(Rgb.Clamp01(weather.Rain)));

        static double Hash(int i, int salt)
        {
            unchecked
            {
                var h = (uint)i * 2654435761u ^ (uint)salt * 2246822519u;
                h ^= h >> 15; h *= 2246822519u; h ^= h >> 13; h *= 3266489917u; h ^= h >> 16;
                return h / 4294967296.0;
            }
        }
        static double Wrap(double v, double size) { v %= size; return v < 0 ? v + size : v; }

        /// <summary>A drop's fall speed, its own for the whole run.</summary>
        public static double FallSpeed(int drop) => FallMin + (FallMax - FallMin) * Hash(drop, 4);

        /// <summary>
        /// Drop <paramref name="drop"/> at wave-clock second <paramref name="timeS"/>, for an eye at the given place. False when it
        /// is too close to the lens to draw.
        /// </summary>
        public static bool TryStreak(int drop, double timeS, Weather weather, double eyeX, double eyeY, double eyeZ, out RainStreak streak)
        {
            Wind(weather, out var windX, out var windZ); var fall = FallSpeed(drop);
            // the drop's place in the world: a start of its own, then straight along its velocity
            var worldX = Hash(drop, 1) * BoxX + windX * timeS; var worldY = Hash(drop, 2) * BoxY - fall * timeS; var worldZ = Hash(drop, 3) * BoxZ + windZ * timeS;
            // drawn at its copy inside the box centred on the eye
            var x = eyeX - BoxX / 2 + Wrap(worldX - (eyeX - BoxX / 2), BoxX);
            var y = eyeY - BoxY / 2 + Wrap(worldY - (eyeY - BoxY / 2), BoxY);
            var z = eyeZ - BoxZ / 2 + Wrap(worldZ - (eyeZ - BoxZ / 2), BoxZ);
            streak = new RainStreak(x, y, z, -windX * ExposureS, fall * ExposureS, -windZ * ExposureS);
            var dx = x - eyeX; var dy = y - eyeY; var dz = z - eyeZ;
            return dx * dx + dy * dy + dz * dz >= LensClearM * LensClearM;
        }
    }
}
