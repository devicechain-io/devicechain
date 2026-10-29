// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>A colour with straight (not premultiplied) alpha, as plain floats so the mapping can be tested with no engine.</summary>
    public readonly struct Rgba
    {
        public Rgba(float r, float g, float b, float a) { R = r; G = g; B = b; A = a; }
        public float R { get; }
        public float G { get; }
        public float B { get; }
        public float A { get; }

        public static Rgba Lerp(Rgba x, Rgba y, float t)
        {
            t = t < 0 ? 0 : (t > 1 ? 1 : t);
            return new Rgba(x.R + (y.R - x.R) * t, x.G + (y.G - x.G) * t, x.B + (y.B - x.B) * t, x.A + (y.A - x.A) * t);
        }

        public override string ToString() { return "rgba(" + R.ToString("0.00") + "," + G.ToString("0.00") + "," + B.ToString("0.00") + "," + A.ToString("0.00") + ")"; }
    }

    /// <summary>
    /// The cool-to-warm ramp a zone's glass volume is tinted with, from the temperature the zone REPORTED and the setpoint it REPORTED. This is a
    /// display mapping only: it decides a colour for a value that was handed to it, and calculates nothing about the building.
    ///
    ///   at or below <see cref="CoolSpanC"/> under setpoint  cool blue
    ///   at setpoint                                          calm green-teal
    ///   <see cref="WarmSpanC"/> over setpoint and above      hot red, through amber
    ///
    /// A value that is missing (NaN) is a faint grey: no data is not a comfortable room.
    /// </summary>
    public static class TintRamp
    {
        public const double CoolSpanC = 3.0;
        public const double WarmSpanC = 5.0;

        public static readonly Rgba Cool = new Rgba(0.25f, 0.55f, 1.00f, 0.38f);
        public static readonly Rgba Neutral = new Rgba(0.50f, 0.82f, 0.88f, 0.14f);
        public static readonly Rgba Amber = new Rgba(1.00f, 0.78f, 0.15f, 0.52f);
        public static readonly Rgba Hot = new Rgba(0.98f, 0.18f, 0.10f, 0.68f);
        public static readonly Rgba NoData = new Rgba(0.55f, 0.58f, 0.62f, 0.12f);

        /// <summary>-1 at <see cref="CoolSpanC"/> or more below setpoint, 0 at setpoint, +1 at <see cref="WarmSpanC"/> or more above it. NaN in, NaN out.</summary>
        public static double Warmth(double tempC, double setpointC)
        {
            if (double.IsNaN(tempC) || double.IsNaN(setpointC) || double.IsInfinity(tempC) || double.IsInfinity(setpointC)) return double.NaN;
            double dev = tempC - setpointC;
            double w = dev < 0 ? dev / CoolSpanC : dev / WarmSpanC;
            return w < -1 ? -1 : (w > 1 ? 1 : w);
        }

        /// <summary>How strongly a zone glows (0 to 1): nothing at or below a gentle warmth, full at the hot end, so a room that is too hot reads from across the campus.</summary>
        public static double Glow(double tempC, double setpointC)
        {
            double w = Warmth(tempC, setpointC);
            if (double.IsNaN(w) || w <= 0.25) return 0;
            return (w - 0.25) / 0.75;
        }

        public static Rgba Colour(double tempC, double setpointC)
        {
            double w = Warmth(tempC, setpointC);
            if (double.IsNaN(w)) return NoData;
            if (w < 0) return Rgba.Lerp(Neutral, Cool, (float)-w);
            if (w < 0.5) return Rgba.Lerp(Neutral, Amber, (float)(w / 0.5));
            return Rgba.Lerp(Amber, Hot, (float)((w - 0.5) / 0.5));
        }
    }

    /// <summary>How many people a zone shows: the occupancy it reported, bounded by the places there are to put them.</summary>
    public static class PeopleView
    {
        public static int Visible(double reportedOccupancy, int slots)
        {
            if (slots <= 0 || double.IsNaN(reportedOccupancy) || reportedOccupancy <= 0) return 0;
            double rounded = Math.Round(reportedOccupancy, MidpointRounding.AwayFromZero);
            return rounded >= slots ? slots : (int)rounded;
        }
    }
}
