// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>One Gerstner component, ready for the shader: direction, wavelength, amplitude and steepness.</summary>
    public readonly struct WaveComponent
    {
        public WaveComponent(double dirX, double dirZ, double wavelengthM, double amplitudeM, double steepness)
        {
            DirX = dirX;
            DirZ = dirZ;
            WavelengthM = wavelengthM;
            AmplitudeM = amplitudeM;
            Steepness = steepness;
        }

        public double DirX { get; }
        public double DirZ { get; }
        public double WavelengthM { get; }
        public double AmplitudeM { get; }

        /// <summary>The Q of the Gerstner sum, already divided so the summed crests never fold over (Q x k x A x N &lt;= 1 overall).</summary>
        public double Steepness { get; }

        public double K => 2 * Math.PI / WavelengthM;

        /// <summary>Deep-water angular speed: omega = sqrt(g k).</summary>
        public double Omega => Math.Sqrt(GerstnerWaves.G * K);
    }

    /// <summary>
    /// The harbour's waves: four deterministic Gerstner components whose amplitude follows the story's storm. They are VISUAL ONLY and zero-mean:
    /// the water's mean height is the gauges' observed tide, never anything computed here, so a wave can never be mistaken for a reading.
    ///
    /// The wavelengths and directions are fixed for the whole run (changing a wavelength mid-run would jump every crest, since the phase is
    /// k x position - omega x time over a long time); only the amplitude moves, calm to storm. The shader evaluates the same sum with the same
    /// numbers, handed to it by the water controller, so <see cref="Displace"/> here is the shader's surface for anything that floats.
    /// </summary>
    public static class GerstnerWaves
    {
        public const double G = 9.81;
        public const int Count = 4;

        // direction (degrees clockwise from north: the swell runs in from the north-west, the harbour's open side), wavelength (m), share of the amplitude
        static readonly double[] DirDeg = { 160, 135, 190, 115 };
        static readonly double[] WavelengthM = { 23, 13.5, 8.2, 4.9 };
        static readonly double[] Share = { 1.0, 0.62, 0.38, 0.22 };

        /// <summary>Amplitude of the longest component in calm water and in the full storm, metres.</summary>
        public const double CalmAmplitudeM = 0.05, StormAmplitudeM = 0.28;

        /// <summary>Overall steepness; the per-component Q is this divided by k A N, clamped so the crests never loop.</summary>
        public const double SteepnessCalm = 0.35, SteepnessStorm = 0.7;

        public static WaveComponent[] For(double storm)
        {
            storm = Rgb.Clamp01(storm);
            var a0 = CalmAmplitudeM + (StormAmplitudeM - CalmAmplitudeM) * storm;
            var steep = SteepnessCalm + (SteepnessStorm - SteepnessCalm) * storm;
            var waves = new WaveComponent[Count];
            for (var i = 0; i < Count; i++)
            {
                var rad = DirDeg[i] * Math.PI / 180.0;
                var amp = a0 * Share[i];
                var k = 2 * Math.PI / WavelengthM[i];
                var q = amp > 0 ? Math.Min(1.0, steep / (k * amp * Count)) : 0;
                waves[i] = new WaveComponent(Math.Sin(rad), Math.Cos(rad), WavelengthM[i], amp, q);
            }

            return waves;
        }

        /// <summary>
        /// The displaced surface point for a rest position (x, z) at time t, as the shader computes it: (dx, dy, dz) to add to (x, mean level, z).
        /// </summary>
        public static void Displace(WaveComponent[] waves, double x, double z, double t, out double dx, out double dy, out double dz)
        {
            dx = dy = dz = 0;
            foreach (var w in waves)
            {
                var phase = w.K * (w.DirX * x + w.DirZ * z) - w.Omega * t;
                var c = Math.Cos(phase);
                dx += w.Steepness * w.AmplitudeM * w.DirX * c;
                dz += w.Steepness * w.AmplitudeM * w.DirZ * c;
                dy += w.AmplitudeM * Math.Sin(phase);
            }
        }
    }
}
