// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>A linear RGB colour, engine-free.</summary>
    public readonly struct Rgb
    {
        public Rgb(double r, double g, double b)
        {
            R = r;
            G = g;
            B = b;
        }

        public double R { get; }
        public double G { get; }
        public double B { get; }

        public static Rgb Lerp(Rgb a, Rgb b, double t)
        {
            t = Clamp01(t);
            return new Rgb(a.R + (b.R - a.R) * t, a.G + (b.G - a.G) * t, a.B + (b.B - a.B) * t);
        }

        public Rgb Scale(double k) => new Rgb(R * k, G * k, B * k);

        internal static double Clamp01(double x) => x < 0 ? 0 : x > 1 ? 1 : x;

        /// <summary>The colour of a black body at <paramref name="kelvin"/>, normalised to a peak of 1 (Tanner Helland's fit, 1000 K to 40000 K).</summary>
        public static Rgb FromKelvin(double kelvin)
        {
            var t = Math.Max(1000, Math.Min(40000, kelvin)) / 100.0;
            double r, g, b;
            if (t <= 66)
            {
                r = 255;
                g = 99.4708025861 * Math.Log(t) - 161.1195681661;
                b = t <= 19 ? 0 : 138.5177312231 * Math.Log(t - 10) - 305.0447927307;
            }
            else
            {
                r = 329.698727446 * Math.Pow(t - 60, -0.1332047592);
                g = 288.1221695283 * Math.Pow(t - 60, -0.0755148492);
                b = 255;
            }

            return new Rgb(Clamp01(r / 255), Clamp01(g / 255), Clamp01(b / 255));
        }
    }

    /// <summary>The weather the story imposes at an hour: how stormy, how wet.</summary>
    public readonly struct Weather
    {
        public Weather(double storm, double rain)
        {
            Storm = storm;
            Rain = rain;
        }

        /// <summary>0 calm .. 1 full storm: overcast, wind, wave height.</summary>
        public double Storm { get; }

        /// <summary>0 dry .. 1 heavy rain.</summary>
        public double Rain { get; }

        /// <summary>
        /// The story's weather at an hour of day, from the layout's <c>storm</c> beat (the hours /status names it): it builds over the beat's first
        /// hour and blows out over its last, and the rain is the storm's heart. Outside the beat the harbour is calm and dry.
        /// </summary>
        public static Weather At(HarborLayout layout, double hourOfDay)
        {
            var b = layout?.Beat("storm");
            if (b == null) return new Weather(0, 0);
            return Window(b.From, b.To, hourOfDay);
        }

        public static Weather Window(double from, double to, double hourOfDay)
        {
            var storm = Smooth(from, from + 1, hourOfDay) * (1 - Smooth(to - 1.5, to, hourOfDay));
            var rain = Smooth(from + 0.5, from + 1.25, hourOfDay) * (1 - Smooth(to - 2.5, to - 1, hourOfDay));
            return new Weather(storm, rain);
        }

        static double Smooth(double a, double b, double x)
        {
            if (x <= a) return 0;
            if (x >= b) return 1;
            var t = (x - a) / (b - a);
            return t * t * (3 - 2 * t);
        }
    }

    /// <summary>Everything the sky, the lights and the fog need at one moment.</summary>
    public sealed class SkyState
    {
        /// <summary>Sun elevation above the horizon and azimuth clockwise from north, degrees.</summary>
        public double SunElevationDeg { get; internal set; }

        public double SunAzimuthDeg { get; internal set; }
        public double MoonElevationDeg { get; internal set; }
        public double MoonAzimuthDeg { get; internal set; }

        /// <summary>Directional light intensities (URP units, the sun around 2 at noon) and colours.</summary>
        public double SunIntensity { get; internal set; }

        public Rgb SunColor { get; internal set; }
        public double MoonIntensity { get; internal set; }
        public Rgb MoonColor { get; internal set; }

        public Rgb Zenith { get; internal set; }
        public Rgb Horizon { get; internal set; }
        public Rgb Ground { get; internal set; }

        /// <summary>0 .. 1: how much of the star field shows.</summary>
        public double Stars { get; internal set; }

        /// <summary>0 .. 1: storm cloud cover.</summary>
        public double Overcast { get; internal set; }

        public Rgb AmbientSky { get; internal set; }
        public Rgb AmbientEquator { get; internal set; }
        public Rgb AmbientGround { get; internal set; }
        public Rgb FogColor { get; internal set; }

        /// <summary>Exponential-squared fog density.</summary>
        public double FogDensity { get; internal set; }

        /// <summary>0 at full day .. 1 at full night: how strongly the lamps, windows and beacons should read.</summary>
        public double Night { get; internal set; }
    }

    /// <summary>
    /// The time of day as light: a stylised sun and moon on fixed arcs, graded through dawn, day, dusk and night, with the story's storm
    /// laid over it. It is scenario time, not astronomy: the sun rises at 06:15 and sets at 18:15 every day so the story's beats always fall
    /// in the same light (the street lights come on at 18:00), and the platform's solar campaigns, which plan by the real sun, are a separate
    /// matter.
    /// </summary>
    public static class DayLighting
    {
        public const double SunriseHour = 6.25, SunsetHour = 18.25;
        public const double SunMaxElevationDeg = 52, MoonMaxElevationDeg = 38;

        static readonly Rgb NightZenith = new Rgb(0.006, 0.010, 0.030), NightHorizon = new Rgb(0.030, 0.040, 0.075);
        static readonly Rgb TwilightZenith = new Rgb(0.10, 0.13, 0.30), TwilightHorizon = new Rgb(0.95, 0.46, 0.24);
        static readonly Rgb DayZenith = new Rgb(0.16, 0.38, 0.78), DayHorizon = new Rgb(0.64, 0.78, 0.93);
        static readonly Rgb StormZenith = new Rgb(0.20, 0.22, 0.25), StormHorizon = new Rgb(0.36, 0.38, 0.41);
        static readonly Rgb CityGlow = new Rgb(0.085, 0.08, 0.082);

        // the floor the night never drops below, so the quays stay readable by the lamps' and the sky's light
        static readonly Rgb NightAmbientSky = new Rgb(0.075, 0.085, 0.12), NightAmbientEquator = new Rgb(0.065, 0.065, 0.075);
        static readonly Rgb NightAmbientGround = new Rgb(0.03, 0.03, 0.032);

        static Rgb Max(Rgb a, Rgb b) => new Rgb(Math.Max(a.R, b.R), Math.Max(a.G, b.G), Math.Max(a.B, b.B));

        public static SkyState Evaluate(double hourOfDay, Weather weather)
        {
            var h = hourOfDay - Math.Floor(hourOfDay / 24) * 24;
            var s = new SkyState();
            double sunElev, sunAz;
            Arc(h, SunriseHour, SunsetHour, SunMaxElevationDeg, out sunElev, out sunAz);
            double moonElev, moonAz;
            // the moon rides the opposite arc, from 18:45 to 05:45 the next morning
            Arc(h < 12 ? h + 24 : h, SunsetHour + 0.5, SunriseHour + 24 - 0.5, MoonMaxElevationDeg, out moonElev, out moonAz);
            s.SunElevationDeg = sunElev;
            s.SunAzimuthDeg = sunAz;
            s.MoonElevationDeg = moonElev;
            s.MoonAzimuthDeg = moonAz;

            var storm = Rgb.Clamp01(weather.Storm);
            // day 1 above +8 degrees, twilight about the horizon, night below -10
            var day = Smooth(-2, 8, sunElev);
            var twilight = Math.Max(0, 1 - Math.Abs(sunElev - 2) / 12.0) * (1 - day * 0.5);
            var night = 1 - Smooth(-10, 0, sunElev);
            s.Night = night * 0.85 + (1 - day) * 0.15;

            var kelvin = 1900 + 3900 * Smooth(0, 35, sunElev);
            s.SunColor = Rgb.FromKelvin(kelvin);
            s.SunIntensity = 2.1 * Smooth(-1.5, 22, sunElev) * (1 - 0.72 * storm);
            s.MoonColor = Rgb.FromKelvin(7800);
            s.MoonIntensity = 0.32 * Smooth(-2, 10, moonElev) * night * (1 - 0.6 * storm);

            var zenith = Rgb.Lerp(NightZenith, DayZenith, day);
            var horizon = Rgb.Lerp(NightHorizon, DayHorizon, day);
            zenith = Rgb.Lerp(zenith, TwilightZenith, twilight * 0.6);
            horizon = Rgb.Lerp(horizon, TwilightHorizon, twilight);
            var stormLight = 0.12 + 0.88 * Math.Max(day, twilight * 0.5);
            s.Zenith = Rgb.Lerp(zenith, StormZenith.Scale(stormLight), storm * 0.9);
            s.Horizon = Rgb.Lerp(horizon, StormHorizon.Scale(stormLight), storm * 0.9);
            s.Ground = s.Horizon.Scale(0.35);
            s.Stars = night * (1 - storm);
            s.Overcast = storm;

            // the town's own glow on the cloud: a storm night is dim and warm-grey, never black
            s.Horizon = Rgb.Lerp(s.Horizon, CityGlow, night * (0.25 + 0.5 * storm));
            var amb = 0.25 + 0.75 * day;
            s.AmbientSky = Max(s.Zenith.Scale(amb * 1.1 + 0.04), NightAmbientSky);
            s.AmbientEquator = Max(s.Horizon.Scale(amb * 0.8 + 0.03), NightAmbientEquator);
            s.AmbientGround = Max(s.Ground.Scale(0.6), NightAmbientGround);
            s.FogColor = s.Horizon;
            s.FogDensity = 0.00012 + 0.00055 * storm + 0.00008 * night;
            return s;
        }

        /// <summary>
        /// A body that rises at <paramref name="rise"/> due east, culminates due south at <paramref name="maxElev"/> and sets at <paramref name="set"/> due
        /// west; outside those hours it is below the horizon on the matching arc.
        /// </summary>
        static void Arc(double h, double rise, double set, double maxElev, out double elevation, out double azimuth)
        {
            var phase = (h - rise) / (set - rise); // 0 at rise, 1 at set; outside [0,1] below the horizon
            var a = phase * Math.PI;
            elevation = maxElev * Math.Sin(a);
            azimuth = 90 + 180 * phase;
            azimuth -= Math.Floor(azimuth / 360) * 360;
        }

        static double Smooth(double a, double b, double x)
        {
            if (x <= a) return 0;
            if (x >= b) return 1;
            var t = (x - a) / (b - a);
            return t * t * (3 - 2 * t);
        }
    }
}
