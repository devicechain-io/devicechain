// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Harborpulse.Model;
using UnityEngine;
using UnityEngine.Rendering;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// Puts a <see cref="SkyState"/> on screen: the sun's and the moon's directional lights on their arcs (only the dominant one casts shadows),
    /// the procedural skybox's gradient, discs, stars and overcast, the trilight ambient and the fog. Everything is a function of the scene's
    /// clock and the story's weather; nothing is animated on its own.
    /// </summary>
    public sealed class HarborSkyController
    {
        static readonly int Zenith = Shader.PropertyToID("_HarborSkyZenith");
        static readonly int Horizon = Shader.PropertyToID("_HarborSkyHorizon");
        static readonly int GroundCol = Shader.PropertyToID("_HarborSkyGround");
        static readonly int SunDir = Shader.PropertyToID("_HarborSunDir");
        static readonly int SunColor = Shader.PropertyToID("_HarborSunColor");
        static readonly int MoonDir = Shader.PropertyToID("_HarborMoonDir");
        static readonly int MoonColor = Shader.PropertyToID("_HarborMoonColor");
        static readonly int Stars = Shader.PropertyToID("_HarborStars");
        static readonly int Overcast = Shader.PropertyToID("_HarborOvercast");
        static readonly int SkyTime = Shader.PropertyToID("_HarborSkyTime");
        static readonly int Night = Shader.PropertyToID("_HarborNight");
        static readonly int SkyTop = Shader.PropertyToID("_HarborSkyTop");
        static readonly int SkyMid = Shader.PropertyToID("_HarborSkyMid");
        static readonly int SkyLow = Shader.PropertyToID("_HarborSkyLow");
        static readonly int SkyHaze = Shader.PropertyToID("_HarborSkyHaze");
        static readonly int SkyGlow = Shader.PropertyToID("_HarborSkyGlow");
        static readonly int CloudLit = Shader.PropertyToID("_HarborCloudLit");
        static readonly int CloudShade = Shader.PropertyToID("_HarborCloudShade");
        static readonly int CloudLight = Shader.PropertyToID("_HarborCloudLight");
        static readonly int Cloud = Shader.PropertyToID("_HarborCloud");
        static readonly int CloudDrift = Shader.PropertyToID("_HarborCloudDrift");
        static readonly int SceneryFog = Shader.PropertyToID("_HarborSceneryFog");
        static readonly int SceneryFogColor = Shader.PropertyToID("_HarborSceneryFogColor");
        static readonly int SceneryFogLow = Shader.PropertyToID("_HarborSceneryFogLow");
        static readonly int CloudUnder = Shader.PropertyToID("_HarborCloudUnder");
        static readonly int MoonGlow = Shader.PropertyToID("_HarborMoonGlow");

        readonly Light sun, moon;
        readonly Material skybox;

        // the render settings the scene found, put back when it goes
        readonly Material previousSkybox;
        readonly AmbientMode previousAmbientMode;
        readonly bool previousFog;
        readonly FogMode previousFogMode;
        readonly Light previousSun;
        readonly float previousReflectionIntensity;
        readonly Color previousFogColor;
        readonly float previousFogDensity;
        readonly Vector4 previousSceneryFog;
        readonly Color previousSceneryFogColor, previousSceneryFogLow, previousCloudUnder;
        readonly float previousMoonGlow;

        public HarborSkyController(Transform parent, Shader skyShader)
        {
            previousSkybox = RenderSettings.skybox;
            previousAmbientMode = RenderSettings.ambientMode;
            previousFog = RenderSettings.fog;
            previousFogMode = RenderSettings.fogMode;
            previousSun = RenderSettings.sun;
            previousReflectionIntensity = RenderSettings.reflectionIntensity;
            previousFogColor = RenderSettings.fogColor;
            previousFogDensity = RenderSettings.fogDensity;
            previousSceneryFog = Shader.GetGlobalVector(SceneryFog);
            previousSceneryFogColor = Shader.GetGlobalColor(SceneryFogColor);
            previousSceneryFogLow = Shader.GetGlobalColor(SceneryFogLow);
            previousCloudUnder = Shader.GetGlobalColor(CloudUnder);
            previousMoonGlow = Shader.GetGlobalFloat(MoonGlow);
            sun = MakeLight("Sun", parent, LightShadows.Soft);
            moon = MakeLight("Moon", parent, LightShadows.None);
            var shader = skyShader != null ? skyShader : Shader.Find("Harborpulse/Sky");
            if (shader == null) throw new System.InvalidOperationException("the Harborpulse/Sky shader is missing from the project");
            RenderSettings.skybox = skybox = new Material(shader) { name = "Harbor/sky" };
            RenderSettings.ambientMode = AmbientMode.Trilight;
            RenderSettings.fog = true;
            RenderSettings.fogMode = FogMode.ExponentialSquared;
        }

        public SkyState Current { get; private set; }

        /// <summary>Puts the render settings back and destroys the sky's material and lights.</summary>
        public void Dispose()
        {
            RenderSettings.skybox = previousSkybox;
            RenderSettings.ambientMode = previousAmbientMode;
            RenderSettings.fog = previousFog;
            RenderSettings.fogMode = previousFogMode;
            RenderSettings.sun = previousSun;
            RenderSettings.reflectionIntensity = previousReflectionIntensity;
            RenderSettings.fogColor = previousFogColor;
            RenderSettings.fogDensity = previousFogDensity;
            Shader.SetGlobalVector(SceneryFog, previousSceneryFog);
            Shader.SetGlobalColor(SceneryFogColor, previousSceneryFogColor);
            Shader.SetGlobalColor(SceneryFogLow, previousSceneryFogLow);
            Shader.SetGlobalColor(CloudUnder, previousCloudUnder);
            Shader.SetGlobalFloat(MoonGlow, previousMoonGlow);
            MeshKit.Release(skybox);
            if (sun != null) MeshKit.Release(sun.gameObject);
            if (moon != null) MeshKit.Release(moon.gameObject);
        }

        static Light MakeLight(string name, Transform parent, LightShadows shadows)
        {
            var go = new GameObject(name);
            go.transform.SetParent(parent, false);
            var l = go.AddComponent<Light>();
            l.type = LightType.Directional;
            l.shadows = shadows;
            l.shadowStrength = 0.85f;
            return l;
        }

        /// <summary>The unit vector towards a body at an elevation and azimuth (clockwise from north; +Z is north, +X east).</summary>
        public static Vector3 Toward(double elevationDeg, double azimuthDeg)
        {
            var el = (float)(elevationDeg * Mathf.Deg2Rad);
            var az = (float)(azimuthDeg * Mathf.Deg2Rad);
            return new Vector3(Mathf.Sin(az) * Mathf.Cos(el), Mathf.Sin(el), Mathf.Cos(az) * Mathf.Cos(el));
        }

        static Color C(Rgb c, float k = 1) => new Color((float)c.R * k, (float)c.G * k, (float)c.B * k, 1);

        public void Apply(SkyState s, float time)
        {
            Current = s;
            var toSun = Toward(s.SunElevationDeg, s.SunAzimuthDeg);
            var toMoon = Toward(s.MoonElevationDeg, s.MoonAzimuthDeg);
            sun.transform.rotation = Quaternion.LookRotation(-toSun);
            moon.transform.rotation = Quaternion.LookRotation(-toMoon);
            sun.color = C(s.SunColor);
            sun.intensity = (float)s.SunIntensity;
            sun.enabled = s.SunIntensity > 0.001;
            moon.color = C(s.MoonColor);
            moon.intensity = (float)s.MoonIntensity;
            moon.enabled = s.MoonIntensity > 0.001;
            // the dominant body is the one that casts shadows and that the water glints
            var sunLeads = s.SunIntensity >= s.MoonIntensity;
            sun.shadows = sunLeads ? LightShadows.Soft : LightShadows.None;
            moon.shadows = sunLeads ? LightShadows.None : LightShadows.Soft;
            sun.shadowStrength = moon.shadowStrength = (float)s.ShadowStrength;
            RenderSettings.sun = sunLeads ? sun : moon;

            // the sky, its cloud and the haze are held back from the grade's dim-light lift (SkyState.SkyGain); the light on the town is not
            var g = (float)s.SkyGain;
            Shader.SetGlobalColor(Zenith, C(s.Zenith, g));
            Shader.SetGlobalColor(Horizon, C(s.Horizon, g));
            Shader.SetGlobalColor(GroundCol, C(s.Ground, g));
            // the discs are whole whenever they are up: the sky shader hides them behind the cloud it draws, where the cloud is
            Shader.SetGlobalVector(SunDir, new Vector4(toSun.x, toSun.y, toSun.z, Mathf.Clamp01((float)(s.SunElevationDeg + 1) / 2)));
            Shader.SetGlobalColor(SunColor, C(s.SunColor));
            Shader.SetGlobalVector(MoonDir, new Vector4(toMoon.x, toMoon.y, toMoon.z, Mathf.Clamp01((float)(s.MoonElevationDeg + 1) / 2)));
            Shader.SetGlobalColor(MoonColor, C(s.MoonColor));
            Shader.SetGlobalFloat(Stars, (float)s.Stars);
            Shader.SetGlobalFloat(Overcast, (float)s.CloudCover);
            Shader.SetGlobalFloat(SkyTime, time);
            Shader.SetGlobalFloat(Night, (float)s.Night);
            // the sky the shader draws: the clear gradient, the cloud in front of it and where the scenario clock has carried that cloud
            Shader.SetGlobalColor(SkyTop, C(s.ClearZenith, g));
            Shader.SetGlobalColor(SkyMid, C(s.ClearMid, g));
            Shader.SetGlobalColor(SkyLow, C(s.ClearHorizon, g));
            Shader.SetGlobalColor(SkyHaze, C(s.FogColor, g));
            var glow = C(s.TwilightGlow, g); glow.a = (float)s.TwilightStrength;
            Shader.SetGlobalColor(SkyGlow, glow);
            Shader.SetGlobalColor(CloudLit, C(s.CloudLit, g));
            Shader.SetGlobalColor(CloudShade, C(s.CloudShade, g));
            var under = C(s.CloudUnderlight, g); under.a = (float)s.CloudUnderlightStrength;
            Shader.SetGlobalColor(CloudUnder, under);
            Shader.SetGlobalFloat(MoonGlow, (float)s.MoonCloudGlow);
            var toLight = s.SunElevationDeg > -4 ? toSun : toMoon;
            Shader.SetGlobalVector(CloudLight, new Vector4(toLight.x, toLight.y, toLight.z, 0));
            Shader.SetGlobalVector(Cloud, new Vector4((float)s.CloudCover, (float)s.CloudDepth, (float)s.Rain, 0));
            // the high layer drifts the other way round at half the pace, so the two layers slide over each other
            Shader.SetGlobalVector(CloudDrift, new Vector4((float)s.CloudDriftX, (float)s.CloudDriftZ, (float)(-s.CloudDriftZ * 0.5), (float)(s.CloudDriftX * 0.5)));

            RenderSettings.ambientSkyColor = C(s.AmbientSky);
            RenderSettings.ambientEquatorColor = C(s.AmbientEquator);
            RenderSettings.ambientGroundColor = C(s.AmbientGround);
            // what still reflects the environment (glass, steel) reflects less of the probe's daylight as the day goes
            RenderSettings.reflectionIntensity = Mathf.Lerp(1f, 0.08f, (float)s.Night);
            RenderSettings.fogColor = C(s.FogColor, g);
            RenderSettings.fogDensity = (float)s.FogDensity;
            Shader.SetGlobalColor(SceneryFogColor, C(s.FogColor, g));
            Shader.SetGlobalColor(SceneryFogLow, C(s.GroundHaze, g));
            Shader.SetGlobalVector(SceneryFog, new Vector4((float)s.FogDensity, (float)s.HeightFogDensity,
                (float)s.HeightFogBase, (float)s.HeightFogFalloff));
        }
    }
}
