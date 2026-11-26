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

        /// <summary>
        /// The exposure the grade adds, in stops: the eye opens up at night, a little in the storm, and through the dim light of a dawn, a
        /// dusk or a heavy overcast, when the sky is bright but little of its light reaches the town.
        /// </summary>
        public double ExposureLift { get; internal set; }

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

        /// <summary>
        /// The haze over the far land, seen from above: darker and bluer than <see cref="FogColor"/>, which is the haze along the horizon itself.
        /// A view ray takes the one or the other by its elevation (<see cref="HazeColor"/>), in the sky and in the scenery's fog alike, so far
        /// ground fades to a tone that is darker than the sky above it and the land keeps an edge against the horizon.
        /// </summary>
        public Rgb GroundHaze { get; internal set; }

        /// <summary>The view-ray elevations (the ray's unit y) between which the haze turns from the horizon's colour to the ground's.</summary>
        public const double HazeHorizonY = .004, HazeGroundY = -.05;

        /// <summary>The haze colour along a view ray whose unit direction has this y: the sky shader and the scenery shader both draw exactly this.</summary>
        public Rgb HazeColor(double rayY)
        {
            var t = Rgb.Clamp01((HazeHorizonY - rayY) / (HazeHorizonY - HazeGroundY));
            return Rgb.Lerp(FogColor, GroundHaze, t * t * (3 - 2 * t));
        }

        /// <summary>
        /// What the sky, its cloud and the haze are multiplied by when they are drawn (never the light on the town): the grade opens up by
        /// <see cref="ExposureLift"/> for a dim town, and without this a dawn's or a storm's sky, which is bright already, washes out to white.
        /// </summary>
        public double SkyGain { get; internal set; }

        /// <summary>The warm light a low sun throws on the cloud's bases from below, and how strong it is, 0 .. 1: strongest towards the sun.</summary>
        public Rgb CloudUnderlight { get; internal set; }

        public double CloudUnderlightStrength { get; internal set; }

        /// <summary>0 .. 1: how strongly the moon silvers the cloud: all of the night's cloud a little, the cloud round the moon a lot.</summary>
        public double MoonCloudGlow { get; internal set; }

        /// <summary>
        /// 0 .. 1: the break of light under the storm's deck about sunset. The front that closes the sky stops short of the horizon towards
        /// the low sun, so for the last hour of the day a band of clear, lit sky shows under the cloud along the western horizon, the sun
        /// reaches the town through it as a low warm key, and the rain between is lit from behind. It is nothing in fair weather (there is
        /// no deck to break), nothing while the sun is high, and it dies with the afterglow.
        /// </summary>
        public double HorizonBreak { get; internal set; }

        /// <summary>The colour of the sky in the break, at the horizon and at its upper edge under the cloud.</summary>
        public Rgb BreakColor { get; internal set; }

        public Rgb BreakEdgeColor { get; internal set; }

        /// <summary>How far round from the sun's bearing the break still shows, as the cosine of that angle: it reaches a little past north and south.</summary>
        public const double BreakReachCos = -0.35;

        /// <summary>The break's height above the horizon towards the sun, as the view ray's unit y (about 6 degrees).</summary>
        public const double BreakHeightY = 0.105;

        /// <summary>0 .. 1: how much of the break shows on a bearing whose angle from the sun's has this cosine. The sky shader draws exactly this.</summary>
        public static double BreakReach(double cosFromSun) => Rgb.Clamp01((cosFromSun - BreakReachCos) / (1 - BreakReachCos));

        /// <summary>Exponential-squared fog density.</summary>
        public double FogDensity { get; internal set; }

        /// <summary>WORLD scenery height fog, in inverse metres; height density decays upwards from the base.</summary>
        public double HeightFogDensity { get; internal set; }
        public double HeightFogBase { get; internal set; }
        public double HeightFogFalloff { get; internal set; }

        /// <summary>Scenery fog starts this far from the camera: HOME already stands 550-900 m from the town, so nothing nearer is hazed.</summary>
        public const double SceneryFogStartM = 350;

        /// <summary>
        /// The squalls in a storm's haze (HarborHaze.hlsl, which holds the same two numbers) are drawn for an eye above the first height
        /// and in full above the second. The 9:16 HOME stands above both; the 16:9 HOME and every ground-level camera stand below both,
        /// where the band the squalls lie in is too shallow to read as anything but smudges on the ranges.
        /// </summary>
        public const double SquallEyeFromM = 120, SquallEyeFullM = 190;

        /// <summary>0 .. 1: how much of the squalls an eye at a height sees.</summary>
        public static double SquallsFromEye(double eyeHeightM)
        {
            var t = Math.Max(0, Math.Min(1, (Math.Abs(eyeHeightM) - SquallEyeFromM) / (SquallEyeFullM - SquallEyeFromM)));
            return t * t * (3 - 2 * t);
        }

        /// <summary>The scenery shader multiplies <see cref="FogDensity"/> by this (the planar and URP fog on every other material keep the plain value).</summary>
        public const double SceneryFogGain = 3.5;

        /// <summary>The distant ridge's own, lighter fog: its air density as a fraction of the scenery's, so it stays a readable shape at noon and nearly vanishes in the storm.</summary>
        public const double RidgeFogScale = .40;

        /// <summary>
        /// Matches the scenery shader's distance and midpoint-height optical depth. Fog counts from 350 m out (from HOME, 1,000-1,100 m is
        /// still 80 % clear, half falls at about 1,750 m and 80 % is gone by 2,500 m at noon; in the storm half falls at about 1,000 m) and
        /// ramps in with the distance outside the core, so the town, its yards and the quays stay crisp in every view.
        /// </summary>
        public double SceneryTransmission(double distanceM, double cameraHeight, double worldHeight, double outsideCoreM)
        {
            var depth = Math.Max(0, distanceM - SceneryFogStartM);
            var air = FogDensity * SceneryFogGain * depth;
            var t = Rgb.Clamp01((outsideCoreM - 120) / 300);
            var inland = t * t * (3 - 2 * t);
            var exponent = Math.Max(-8, Math.Min(4, (HeightFogBase - (cameraHeight + worldHeight) * .5) * HeightFogFalloff));
            return Math.Exp(-(air * air + HeightFogDensity * Math.Exp(exponent) * depth) * inland);
        }

        /// <summary>
        /// The sea's transmission at a distance: the scenery's distance curve with no height term and no core ramp, so open water fades into
        /// the haze towards the horizon exactly as the land beside it does, and its far edge is gone before the far clip.
        /// </summary>
        public double SeaTransmission(double distanceM)
        {
            var air = FogDensity * SceneryFogGain * Math.Max(0, distanceM - SceneryFogStartM);
            return Math.Exp(-air * air);
        }

        /// <summary>The ridge's transmission at a distance: distance only, no height or core ramp (the shader also fades its foot into the haze by height).</summary>
        public double RidgeTransmission(double distanceM)
        {
            var air = FogDensity * SceneryFogGain * RidgeFogScale * Math.Max(0, distanceM);
            return Math.Exp(-air * air);
        }

        /// <summary>The height above which a backdrop layer starts to show through the ground haze (the deck is at 2.4 m).</summary>
        public const double BackdropFootY = 4;

        /// <summary>
        /// A nearer backdrop layer's transmission at a point on it: the ridge's distance curve, times the layer's foot dissolving into the ground
        /// haze over <paramref name="footFadeM"/> (the far ridge keeps its own 6 m and 50 m; see the scenery shader).
        /// </summary>
        public double BackdropTransmission(double distanceM, double worldY, double footFadeM) =>
            RidgeTransmission(distanceM) * Rgb.Clamp01((worldY - BackdropFootY) / footFadeM);

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
        // the same floor once the sky is light: near neutral, so a dawn or a storm dusk is dim and not blue (the night keeps its blue)
        static readonly Rgb DimAmbientSky = new Rgb(0.20, 0.195, 0.20), DimAmbientEquator = new Rgb(0.135, 0.13, 0.135);

        /// <summary>How much of the floor lift a clear dawn or dusk gets while the low sun is still a weak key (the keyless hour gets all of it).</summary>
        public const double TwilightFloorShare = 1.0;

        /// <summary>How much of the sky's own colour the daylight ambient keeps: flat ground is lit by the whole sky, cloud and haze included, not by the zenith's blue.</summary>
        public const double DayAmbientKeep = 0.42;
        /// <summary>
        /// The early morning and the late afternoon, while the sun is under <see cref="EarlySunDeg"/>: how much more of the sky's colour the
        /// light on flat ground gives up, and how much of the horizon's the light on the walls does. A sun that low is weak on a level
        /// surface, so the ambient alone sets a quay's colour, and with the sky's blue in it 07:06 was a blue hour between a warm dawn and
        /// a neutral noon.
        /// </summary>
        public const double EarlySkyDrain = 0.62, EarlyWallDrain = 0.50, EarlySunDeg = 32;

        /// <summary>The fraction of the storm's dimming the sun gets back through the break under the deck.</summary>
        public const double BreakKeyShare = 0.75;

        static readonly Rgb BreakGold = new Rgb(1.0, 0.60, 0.22), BreakEmber = new Rgb(0.92, 0.30, 0.13);

        /// <summary>How far the ambient floor is lifted when neither the sun nor the moon gives a key light (a storm dusk, the hour before dawn).</summary>
        public const double KeylessFloorLift = 2.0;

        /// <summary>The stops the grade opens up by when the sun is up but weak: dawn, dusk, and under the storm.</summary>
        public const double DimLightLift = 1.3;

        /// <summary>How much of the dim-light and storm exposure lift the sky and the haze are held back by (see <see cref="SkyState.SkyGain"/>).</summary>
        public const double SkyExposureHold = 0.8;

        /// <summary>Stops the grade adds for the town at night and under the storm, on top of the eye opening; the sky is held back by the same.</summary>
        public const double NightStoryLift = 0.35, StormStoryLift = 0.2;

        /// <summary>The fraction of full key a sun 7 degrees up already gives (the plain curve gives a third of it there).</summary>
        public const double LowSunKey = 0.62;

        /// <summary>How dark the haze over far ground is beside the haze along the horizon.</summary>
        public const double GroundHazeDepth = 0.72;

        static readonly Rgb UnderlightColor = new Rgb(1.0, 0.44, 0.24);

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

            // a low sun is warm, not red: its light has to leave the town's own colours readable on the faces it reaches
            var kelvin = 2500 + 3300 * Smooth(0, 35, sunElev);
            s.SunColor = Rgb.FromKelvin(kelvin);
            // the key comes up quickly after sunrise and holds late before sunset, so for an hour at each end of the day the faces
            // towards the sun are plainly lit and the others are not: that side light is what tells the hour from a camera looking north
            var key = Math.Max(Smooth(-1.5, 22, sunElev), LowSunKey * Smooth(-1.5, 7, sunElev));
            // the break under the deck: open while the sun is within a hand of the horizon, gone with the afterglow
            var breakBand = front * Smooth(-6, -1.5, sunElev) * (1 - Smooth(8, 15, sunElev));
            s.HorizonBreak = breakBand;
            // gold while the sun is up, ember once it is under the horizon
            var emberT = 1 - Smooth(-3.5, 1.5, sunElev);
            s.BreakColor = Rgb.Lerp(BreakGold, BreakEmber, emberT * 0.7);
            s.BreakEdgeColor = Rgb.Lerp(BreakEmber, new Rgb(0.55, 0.20, 0.18), emberT);
            // and the sun reaches the town through it
            // (only once it has sunk into the band: a sun ten degrees up is still behind the deck)
            var breakKey = breakBand * Smooth(-1, 2.5, sunElev) * (1 - Smooth(5, 9, sunElev));
            s.SunIntensity = 2.1 * key * (1 - 0.4 * veil) * (1 - 0.72 * storm * (1 - BreakKeyShare * breakKey));
            s.MoonColor = Rgb.FromKelvin(7800);
            s.MoonIntensity = 0.5 * moonUp * night * (1 - 0.6 * storm);
            s.ShadowStrength = 0.85 * (1 - 0.6 * veil) * (1 - 0.4 * storm);
            // long shadows under a low sun are part of the hour: keep them a little darker than the same cloud would leave them at noon
            s.ShadowStrength = Math.Min(0.85, s.ShadowStrength * (1 + 0.35 * low * (1 - storm)));

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
            // a storm's deck at dusk is slate, not the brown the drained rose leaves: the break under it is where the warmth is
            var slate = storm * low;
            lit = new Rgb(lit.R * (1 - 0.16 * slate), lit.G * (1 - 0.04 * slate), lit.B * (1 + 0.16 * slate));
            shade = new Rgb(shade.R * (1 - 0.16 * slate), shade.G * (1 - 0.04 * slate), shade.B * (1 + 0.16 * slate));
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
            double floor;
            // the ambient takes the rose of a sunrise only lightly: the light on the town stays near neutral
            var equator = Rgb.Lerp(s.Horizon, Drained(s.Horizon, 0.3, 1), twilight * 0.6);
            // under a low sun the flat ground is lit by the sky alone: take some of the blue out of it, or the quays turn periwinkle
            // and by day the ground is lit by the whole sky, its cloud and its haze, never by the zenith's blue alone: a quay is concrete, not lavender
            // (and least of all in the first and last hours of the day, when the sun is too low to outweigh it)
            // (U8: 07:06 still read blue, 1.24 times as much blue as red on a quay's top. The hours after sunrise have a sun too low and too
            // warm to outweigh the sky's light on flat ground, so until it stands about 30 degrees up that light gives up most of its colour.)
            var early = (1 - Smooth(10, EarlySunDeg, sunElev)) * light;
            var skyLight = Drained(s.Zenith, (1 - 0.7 * (1 - high) * light) * (1 - (1 - DayAmbientKeep) * light) * (1 - EarlySkyDrain * early), 1);
            // a clear dawn or dusk has a key, but a weak and grazing one: the floor carries the flat ground then too
            var grazing = low * (1 - night) * (1 - Smooth(0.35, 1.1, s.SunIntensity));
            floor = 1 + KeylessFloorLift * Math.Max(keyless, TwilightFloorShare * grazing);
            var dimDay = Smooth(-5, 1, sunElev);
            var floorSky = Rgb.Lerp(NightAmbientSky, DimAmbientSky, dimDay);
            var floorEquator = Rgb.Lerp(NightAmbientEquator, DimAmbientEquator, dimDay);
            // the walls take the horizon's colour, which an hour after sunrise is as blue as the zenith's: the same early hours drain it
            equator = Drained(equator, 1 - EarlyWallDrain * early * (1 - low), 1);
            // under the storm the last of the twilight's violet is drained from the light on the walls as well
            equator = Drained(equator, 1 - 0.6 * storm * low, 1);
            s.AmbientSky = Max(skyLight.Scale(amb * 1.1 + 0.04), floorSky.Scale(floor));
            s.AmbientEquator = Max(equator.Scale(amb * 0.8 + 0.03), floorEquator.Scale(floor));
            s.AmbientGround = Max(s.Ground.Scale(0.6), NightAmbientGround.Scale(floor));
            s.FogColor = s.Horizon;
            s.GroundHaze = Rgb.Lerp(s.FogColor.Scale(GroundHazeDepth), s.Zenith.Scale(0.9), 0.25);
            // (the key now comes up sooner after sunrise, so the lift is held a little longer into it: the first hour stays readable)
            var dim = DimLightLift * (1 - s.Night) * (1 - Smooth(0.3, 1.6, s.SunIntensity));
            // and the first and last light of a clear day get a little more, so the town reads while the sun is still on the horizon
            // The story lift: a recorded cut is watched on a phone and through a video codec, where the night and the storm were too
            // dark to follow. The town, the quays and the water are opened up further; the sky is held back by exactly as much, so it
            // stays the sky it was graded as.
            var story = s.Night * NightStoryLift + storm * StormStoryLift;
            s.ExposureLift = s.Night * 0.55 + storm * 0.1 + dim + 0.3 * low * (1 - storm) * (1 - night) + story;
            s.SkyGain = Math.Pow(2, -SkyExposureHold * (dim + storm * 0.1) - story);
            // the low sun lights the cloud from below, rose and ember, for about an hour either side of the horizon; a closed front hides most of it
            var under = Math.Max(0, 1 - Math.Abs(sunElev) / 13.0);
            s.CloudUnderlight = UnderlightColor;
            s.CloudUnderlightStrength = under * (1 - 0.93 * front) * (1 - night);
            s.MoonCloudGlow = moonUp * night * (1 - storm);
            // haze closes the far view on purpose: half at about 1,750 m from HOME by day and 1,000 m in the storm (see SceneryFogGain)
            s.FogDensity = 0.00016 + 0.00018 * storm + 0.00002 * night;
            s.HeightFogDensity = 0.00002 + 0.00010 * storm + 0.00002 * night;
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
