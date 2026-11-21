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
        public Weather(double storm, double rain) : this(storm, rain, storm)
        {
        }

        public Weather(double storm, double rain, double front)
        {
            Storm = storm;
            Rain = rain;
            Front = front;
        }

        /// <summary>0 calm .. 1 full storm: overcast, wind, wave height.</summary>
        public double Storm { get; }

        /// <summary>0 dry .. 1 heavy rain.</summary>
        public double Rain { get; }

        /// <summary>
        /// 0 .. 1: the storm's cloud front. It arrives ahead of the wind (it builds over the three hours before the beat) and leaves after it
        /// (it breaks up over the two hours after the beat ends), so the sky announces the storm and then visibly clears.
        /// </summary>
        public double Front { get; }

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
            // the front spills over both ends of the beat, and over midnight: a caller may pass the hour on either side of the wrap
            var front = Math.Max(FrontAt(from, to, hourOfDay), Math.Max(FrontAt(from, to, hourOfDay - 24), FrontAt(from, to, hourOfDay + 24)));
            return new Weather(storm, rain, Math.Max(front, storm));
        }

        public const double FrontBuildHours = 3, FrontClearHours = 2;

        static double FrontAt(double from, double to, double h) =>
            Smooth(from - FrontBuildHours, from + 0.25, h) * (1 - Smooth(to - 0.25, to + FrontClearHours, h));

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

        /// <summary>0 .. 1: the sky's whole cloud cover: the fair-weather cloud of the hour, closed by the storm's front. The sky draws this.</summary>
        public double CloudCover { get; internal set; }

        /// <summary>The wrapped scenario hour this state was evaluated at. The sky moves by this, never by render time.</summary>
        public double HourOfDay { get; internal set; }

        /// <summary>0 .. 1: how heavy and dark the cloud's bases are; it rises as the front builds and is full in the storm.</summary>
        public double CloudDepth { get; internal set; }

        /// <summary>0 dry .. 1 heavy rain, as the sky and the rain in the air draw it.</summary>
        public double Rain { get; internal set; }

        /// <summary>The clear sky behind the cloud, from the horizon band through the middle sky to the zenith.</summary>
        public Rgb ClearHorizon { get; internal set; }

        public Rgb ClearMid { get; internal set; }
        public Rgb ClearZenith { get; internal set; }

        /// <summary>The low sun's glow along the horizon (strongest towards the sun), and how strong it is, 0 .. 1.</summary>
        public Rgb TwilightGlow { get; internal set; }

        public double TwilightStrength { get; internal set; }

        /// <summary>The cloud's lit faces and its shaded bases.</summary>
        public Rgb CloudLit { get; internal set; }

        public Rgb CloudShade { get; internal set; }

        /// <summary>
        /// Where the cloud has drifted to, in the sky shader's cloud-plane units: a slow circle over the day, so the value and its direction
        /// are the same at 24:00 and at 00:00 and a still of an hour is always the same sky.
        /// </summary>
        public double CloudDriftX => Math.Sin(HourOfDay * Math.PI / 12) * DayLighting.CloudDriftRadius;

        public double CloudDriftZ => Math.Cos(HourOfDay * Math.PI / 12) * DayLighting.CloudDriftRadius;

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
    /// The time of day and the weather as light and sky: a stylised sun and moon on fixed arcs, graded through dawn, day, dusk and night, under
    /// a cloud cover that changes through the day (<see cref="FairCloud"/>: thin in the small hours, fair-weather cloud by noon) and is closed
    /// by the story's storm front, which builds for hours before the storm and breaks up after it. The light follows the same cover as the
    /// sky draws, so the two always agree. Night, dusk and storm are kept readable: the ambient never drops below a moonlit floor, the floor
    /// is lifted when neither the sun nor the moon gives a key light, and the storm darkens the sky, not the town. It is scenario time, not
    /// astronomy: the sun rises at 06:15 and sets at 18:15 every day so the story's beats always fall in the same light (the street lights
    /// come on at 18:00), and the platform's solar campaigns, which plan by the real sun, are a separate matter.
    /// </summary>
    public static class DayLighting
    {
        public const double SunriseHour = 6.25, SunsetHour = 18.25;
        public const double SunMaxElevationDeg = 52, MoonMaxElevationDeg = 38;

        /// <summary>The radius of the cloud's daily drift circle, in the sky shader's cloud-plane units (about one unit an hour).</summary>
        public const double CloudDriftRadius = 4;

        /// <summary>The fair-weather cloud cover at an hour: about a fifth of the sky in the small hours, two fifths in the early afternoon.</summary>
        public static double FairCloud(double hourOfDay) => 0.31 + 0.11 * Math.Cos((hourOfDay - 13) * Math.PI / 12);

        /// <summary>How much a cover softens the sun and its shadows and lifts the ambient: noon's fair cloud is about one half.</summary>
        static double Veil(double cover) => 0.30 + 0.50 * cover;

        // the clear night is a deep blue, not black, and paler at the horizon
        static readonly Rgb NightZenith = new Rgb(0.018, 0.030, 0.080), NightHorizon = new Rgb(0.055, 0.075, 0.135);
        static readonly Rgb TwilightZenith = new Rgb(0.11, 0.15, 0.36), TwilightMid = new Rgb(0.46, 0.34, 0.50), TwilightHorizon = new Rgb(0.92, 0.55, 0.42);
        static readonly Rgb TwilightGlowColor = new Rgb(1.0, 0.50, 0.20);
        static readonly Rgb DayZenith = new Rgb(0.16, 0.38, 0.78), DayHorizon = new Rgb(0.64, 0.78, 0.93);
        // the sky behind a building front: paler and greyer than the fair-weather blue
        static readonly Rgb CloudZenith = new Rgb(0.46, 0.53, 0.63), CloudHorizon = new Rgb(0.55, 0.65, 0.76);
        static readonly Rgb StormZenith = new Rgb(0.30, 0.33, 0.36), StormHorizon = new Rgb(0.46, 0.48, 0.50);
        static readonly Rgb CityGlow = new Rgb(0.085, 0.08, 0.082);
        static readonly Rgb DayCloudLit = new Rgb(0.96, 0.97, 1.0), DayCloudShade = new Rgb(0.50, 0.57, 0.68);
        static readonly Rgb TwilightCloudLit = new Rgb(1.0, 0.60, 0.40), TwilightCloudShade = new Rgb(0.30, 0.25, 0.40);
        static readonly Rgb NightCloudLit = new Rgb(0.075, 0.090, 0.130), NightCloudShade = new Rgb(0.016, 0.020, 0.034);

        // the floor the night never drops below, so the quays, the town and the hills stay readable: a cool moonlit blue
        static readonly Rgb NightAmbientSky = new Rgb(0.15, 0.17, 0.24), NightAmbientEquator = new Rgb(0.12, 0.125, 0.15);
        static readonly Rgb NightAmbientGround = new Rgb(0.055, 0.055, 0.06);

        /// <summary>How far the ambient floor is lifted when neither the sun nor the moon gives a key light (a storm dusk, the hour before dawn).</summary>
        public const double KeylessFloorLift = 1.2;

        static Rgb Max(Rgb a, Rgb b) => new Rgb(Math.Max(a.R, b.R), Math.Max(a.G, b.G), Math.Max(a.B, b.B));
        static Rgb Add(Rgb a, Rgb b, double k) => new Rgb(a.R + b.R * k, a.G + b.G * k, a.B + b.B * k);
        static double Luma(Rgb c) => 0.2126 * c.R + 0.7152 * c.G + 0.0722 * c.B;

        /// <summary>The colour drained towards its own grey, and scaled: a storm takes the colour out of the cloud as well as the light.</summary>
        static Rgb Drained(Rgb c, double keep, double scale)
        {
            var g = Luma(c);
            return new Rgb((g + (c.R - g) * keep) * scale, (g + (c.G - g) * keep) * scale, (g + (c.B - g) * keep) * scale);
        }

        public static SkyState Evaluate(double hourOfDay, Weather weather)
        {
            var h = hourOfDay - Math.Floor(hourOfDay / 24) * 24;
            var s = new SkyState();
            s.HourOfDay = h;
            double sunElev, sunAz;
            Arc(h, SunriseHour, SunsetHour, SunMaxElevationDeg, out sunElev, out sunAz);
            double moonElev, moonAz;
            // the moon rides the opposite arc, from 18:45 to 05:45 the next morning
            Arc(h, SunsetHour + 0.5, SunriseHour + 24 - 0.5, MoonMaxElevationDeg, out moonElev, out moonAz);
            s.SunElevationDeg = sunElev;
            s.SunAzimuthDeg = sunAz;
            s.MoonElevationDeg = moonElev;
            s.MoonAzimuthDeg = moonAz;

            var storm = Rgb.Clamp01(weather.Storm);
            var front = Math.Max(storm, Rgb.Clamp01(weather.Front));
            var fair = FairCloud(h);
            var cover = fair + (1 - fair) * front;
            var veil = Veil(cover);
            var depth = Math.Max(storm, 0.7 * front * front);
            s.CloudCover = cover;
            s.CloudDepth = depth;
            s.Overcast = storm;
            s.Rain = Rgb.Clamp01(weather.Rain) * storm;

            // the lamps' day (1 above +8 degrees) and the sky's: the sky is already light when the sun is still under the horizon
            var day = Smooth(-2, 8, sunElev);
            var light = Smooth(-6, 8, sunElev);
            var low = Math.Max(0, 1 - Math.Abs(sunElev) / 9.0);
            var twilight = low * (1 - 0.8 * front);
            var high = Smooth(2, 14, sunElev);
            var night = 1 - Smooth(-10, 0, sunElev);
            var moonUp = Smooth(-2, 10, moonElev);
            s.Night = night * 0.85 + (1 - day) * 0.15;

            var kelvin = 1900 + 3900 * Smooth(0, 35, sunElev);
            s.SunColor = Rgb.FromKelvin(kelvin);
            s.SunIntensity = 2.1 * Smooth(-1.5, 22, sunElev) * (1 - 0.4 * veil) * (1 - 0.72 * storm);
            s.MoonColor = Rgb.FromKelvin(7800);
            s.MoonIntensity = 0.5 * moonUp * night * (1 - 0.6 * storm);
            s.ShadowStrength = 0.85 * (1 - 0.6 * veil) * (1 - 0.4 * storm);

            // the clear sky behind the cloud: blue on a fair day, paler ahead of the front, rose and violet about sunrise and sunset
            var zenith = Rgb.Lerp(NightZenith, Rgb.Lerp(DayZenith, CloudZenith, 0.15 + 0.5 * front), light);
            var horizon = Rgb.Lerp(NightHorizon, Rgb.Lerp(DayHorizon, CloudHorizon, 0.2 + 0.6 * front), light);
            zenith = Rgb.Lerp(zenith, TwilightZenith, twilight * 0.7);
            horizon = Rgb.Lerp(horizon, TwilightHorizon, twilight * 0.75);
            var mid = Rgb.Lerp(Rgb.Lerp(horizon, zenith, 0.55), TwilightMid, twilight * 0.6);
            var stormLight = 0.2 + 0.8 * Math.Max(light, low * 0.5);
            zenith = Rgb.Lerp(zenith, StormZenith.Scale(stormLight), storm * 0.9);
            horizon = Rgb.Lerp(horizon, StormHorizon.Scale(stormLight), storm * 0.9);
            mid = Rgb.Lerp(mid, Rgb.Lerp(StormHorizon, StormZenith, 0.55).Scale(stormLight), storm * 0.9);
            s.ClearZenith = zenith;
            s.ClearMid = mid;
            s.ClearHorizon = horizon;
            s.TwilightGlow = TwilightGlowColor;
            s.TwilightStrength = low * (1 - 0.55 * front) * (1 - night);

            // the cloud: white over grey-blue bases by day, lit rose from below about sunrise and sunset, moon-pale at night; the front
            // drains and darkens it to slate, and the town's own glow keeps a storm night's deck from going black
            var lit = Rgb.Lerp(NightCloudLit.Scale(0.55 + 0.45 * moonUp), Rgb.Lerp(TwilightCloudLit, DayCloudLit, high), light);
            var shade = Rgb.Lerp(NightCloudShade, Rgb.Lerp(TwilightCloudShade, DayCloudShade, high), light);
            lit = Rgb.Lerp(lit, Drained(lit, 0.35, 0.50), depth);
            shade = Rgb.Lerp(shade, Drained(shade, 0.5, 0.40), depth);
            var glow = night * (0.3 + 0.5 * storm);
            s.CloudLit = Add(lit, CityGlow, glow);
            s.CloudShade = Add(shade, CityGlow, glow * 0.8);

            // what the water mirrors and the ambient is made of: the clear sky and the cloud in front of it
            var cloud = Rgb.Lerp(s.CloudShade, s.CloudLit, 0.4);
            var covered = cover * cover * 0.8;
            s.Zenith = Rgb.Lerp(zenith, cloud, covered);
            s.Horizon = Rgb.Lerp(horizon, cloud, covered);
            s.Ground = s.Horizon.Scale(0.35);
            s.Stars = night * (1 - storm);

            // the town's own glow on the cloud: a storm night is dim and warm-grey, never black
            s.Horizon = Rgb.Lerp(s.Horizon, CityGlow, night * (0.25 + 0.5 * storm));
            // a cloudy day's light is mostly sky: more ambient, and the storm's ambient is lifted so the town stays legible under it
            var amb = (0.25 + 0.75 * light) * (1 + 0.35 * veil) * (1 + 0.5 * storm);
            // with no key light at all the floor itself has to carry the scene
            var keyless = 1 - Smooth(0.05, 0.35, s.SunIntensity + s.MoonIntensity);
            var floor = 1 + KeylessFloorLift * keyless;
            // the ambient takes the rose of a sunrise only lightly: the light on the town stays near neutral
            var equator = Rgb.Lerp(s.Horizon, Drained(s.Horizon, 0.3, 1), twilight * 0.6);
            s.AmbientSky = Max(s.Zenith.Scale(amb * 1.1 + 0.04), NightAmbientSky.Scale(floor));
            s.AmbientEquator = Max(equator.Scale(amb * 0.8 + 0.03), NightAmbientEquator.Scale(floor));
            s.AmbientGround = Max(s.Ground.Scale(0.6), NightAmbientGround.Scale(floor));
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
        /// west, then passes under the horizon through the north over the REST of the 24 hours. The hidden arc has its own length: a body up for
        /// eleven hours is down for thirteen, and timing both by the visible arc used to jump its direction once a day.
        /// </summary>
        static void Arc(double h, double rise, double set, double maxElev, out double elevation, out double azimuth)
        {
            h = rise + (h - rise - Math.Floor((h - rise) / 24) * 24);
            if (h > set)
            {
                var hidden = (h - set) / (rise + 24 - set);
                elevation = -maxElev * Math.Sin(hidden * Math.PI);
                azimuth = 270 + 180 * hidden;
            }
            else
            {
                var phase = (h - rise) / (set - rise);
                elevation = maxElev * Math.Sin(phase * Math.PI);
                azimuth = 90 + 180 * phase;
            }

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
