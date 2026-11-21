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

        /// <summary>0 .. 1: the sky's whole cloud cover, the town's high cloud or the storm's, whichever is thicker. The sky draws this.</summary>
        public double CloudCover { get; internal set; }

        public Rgb AmbientSky { get; internal set; }
        public Rgb AmbientEquator { get; internal set; }
        public Rgb AmbientGround { get; internal set; }
        public Rgb FogColor { get; internal set; }

        /// <summary>Exponential-squared fog density.</summary>
        public double FogDensity { get; internal set; }

        /// <summary>WORLD scenery height fog, in inverse metres; height density decays upwards from the base.</summary>
        public double HeightFogDensity { get; internal set; }
        public double HeightFogBase { get; internal set; }
        public double HeightFogFalloff { get; internal set; }

        /// <summary>The scenery shader multiplies <see cref="FogDensity"/> by this (the planar and URP fog on every other material keep the plain value).</summary>
        public const double SceneryFogGain = 6;

        /// <summary>The distant ridge's own, lighter fog: its air density as a fraction of the scenery's, so it stays a readable shape at noon and nearly vanishes in the storm.</summary>
        public const double RidgeFogScale = .34;

        /// <summary>
        /// Matches the scenery shader's distance and midpoint-height optical depth. Fog counts from the camera (about half at 900 m and
        /// nine tenths by 1,500 m at noon, half at 550 m in the storm) but ramps in with the distance outside the core, so the town, its
        /// yards and the quays stay crisp in every view.
        /// </summary>
        public double SceneryTransmission(double distanceM, double cameraHeight, double worldHeight, double outsideCoreM)
        {
            var depth = Math.Max(0, distanceM);
            var air = FogDensity * SceneryFogGain * depth;
            var t = Rgb.Clamp01((outsideCoreM - 120) / 300);
            var inland = t * t * (3 - 2 * t);
            var exponent = Math.Max(-8, Math.Min(4, (HeightFogBase - (cameraHeight + worldHeight) * .5) * HeightFogFalloff));
            return Math.Exp(-(air * air + HeightFogDensity * Math.Exp(exponent) * depth) * inland);
        }

        /// <summary>The ridge's transmission at a distance: distance only, no height or core ramp (the shader also fades its foot into the haze by height).</summary>
        public double RidgeTransmission(double distanceM)
        {
            var air = FogDensity * SceneryFogGain * RidgeFogScale * Math.Max(0, distanceM);
            return Math.Exp(-air * air);
        }

        /// <summary>0 at full day .. 1 at full night: how strongly the lamps, windows and beacons should read.</summary>
        public double Night { get; internal set; }

        /// <summary>0 .. 1: how dark the dominant light's shadows are; soft under the town's high cloud, softer in the storm.</summary>
        public double ShadowStrength { get; internal set; }
    }

    /// <summary>
    /// The time of day as light: a stylised sun and moon on fixed arcs, graded through dawn, day, dusk and night, under a fixed high cloud
    /// (<see cref="Cloud"/>, the soft overcast of a northern harbour town), with the story's storm laid over it. Night and storm are kept
    /// readable: the ambient never drops below a moonlit floor and the storm darkens the sky, not the town. It is scenario time, not astronomy: the sun rises at 06:15 and sets at 18:15 every day so the story's beats always fall
    /// in the same light (the street lights come on at 18:00), and the platform's solar campaigns, which plan by the real sun, are a separate
    /// matter.
    /// </summary>
    public static class DayLighting
    {
        public const double SunriseHour = 6.25, SunsetHour = 18.25;
        public const double SunMaxElevationDeg = 52, MoonMaxElevationDeg = 38;

        /// <summary>The fair-weather cloud cover: it softens the sun and its shadows, pales the sky and lifts the ambient.</summary>
        public const double Cloud = 0.55;

        static readonly Rgb NightZenith = new Rgb(0.012, 0.018, 0.045), NightHorizon = new Rgb(0.050, 0.062, 0.105);
        static readonly Rgb TwilightZenith = new Rgb(0.10, 0.13, 0.30), TwilightHorizon = new Rgb(0.95, 0.46, 0.24);
        static readonly Rgb DayZenith = new Rgb(0.16, 0.38, 0.78), DayHorizon = new Rgb(0.64, 0.78, 0.93);
        // the high cloud's sky: a pale grey-blue overhead, a bright pearl horizon
        static readonly Rgb CloudZenith = new Rgb(0.46, 0.53, 0.63), CloudHorizon = new Rgb(0.55, 0.65, 0.76);
        static readonly Rgb StormZenith = new Rgb(0.30, 0.33, 0.36), StormHorizon = new Rgb(0.46, 0.48, 0.50);
        static readonly Rgb CityGlow = new Rgb(0.085, 0.08, 0.082);

        // the floor the night never drops below, so the quays, the town and the hills stay readable: a cool moonlit blue
        static readonly Rgb NightAmbientSky = new Rgb(0.15, 0.17, 0.24), NightAmbientEquator = new Rgb(0.12, 0.125, 0.15);
        static readonly Rgb NightAmbientGround = new Rgb(0.055, 0.055, 0.06);

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
            s.SunIntensity = 2.1 * Smooth(-1.5, 22, sunElev) * (1 - 0.4 * Cloud) * (1 - 0.72 * storm);
            s.MoonColor = Rgb.FromKelvin(7800);
            s.MoonIntensity = 0.5 * Smooth(-2, 10, moonElev) * night * (1 - 0.6 * storm);
            s.ShadowStrength = 0.85 * (1 - 0.6 * Cloud) * (1 - 0.4 * storm);

            var zenith = Rgb.Lerp(NightZenith, Rgb.Lerp(DayZenith, CloudZenith, Cloud), day);
            var horizon = Rgb.Lerp(NightHorizon, Rgb.Lerp(DayHorizon, CloudHorizon, Cloud), day);
            zenith = Rgb.Lerp(zenith, TwilightZenith, twilight * 0.6);
            horizon = Rgb.Lerp(horizon, TwilightHorizon, twilight);
            var stormLight = 0.12 + 0.88 * Math.Max(day, twilight * 0.5);
            s.Zenith = Rgb.Lerp(zenith, StormZenith.Scale(stormLight), storm * 0.9);
            s.Horizon = Rgb.Lerp(horizon, StormHorizon.Scale(stormLight), storm * 0.9);
            s.Ground = s.Horizon.Scale(0.35);
            s.Stars = night * (1 - storm);
            s.Overcast = storm;
            s.CloudCover = Math.Max(Cloud, storm);

            // the town's own glow on the cloud: a storm night is dim and warm-grey, never black
            s.Horizon = Rgb.Lerp(s.Horizon, CityGlow, night * (0.25 + 0.5 * storm));
            // a cloudy day's light is mostly sky: more ambient, and the storm's ambient is lifted so the town stays legible under it
            var amb = (0.25 + 0.75 * day) * (1 + 0.35 * Cloud) * (1 + 0.5 * storm);
            s.AmbientSky = Max(s.Zenith.Scale(amb * 1.1 + 0.04), NightAmbientSky);
            s.AmbientEquator = Max(s.Horizon.Scale(amb * 0.8 + 0.03), NightAmbientEquator);
            s.AmbientGround = Max(s.Ground.Scale(0.6), NightAmbientGround);
            s.FogColor = s.Horizon;
            // haze closes the view on purpose: scenery fog is about half at 900 m by day and at 550 m in the storm (see SceneryFogGain)
            s.FogDensity = 0.00016 + 0.00008 * storm + 0.00001 * night;
            s.HeightFogDensity = 0.00004 + 0.00020 * storm + 0.00002 * night;
            s.HeightFogBase = 65;
            s.HeightFogFalloff = 0.006;
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
