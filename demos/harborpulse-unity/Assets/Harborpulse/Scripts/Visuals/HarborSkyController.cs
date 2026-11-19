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

        readonly Light sun, moon;
        readonly Material skybox;

        // the render settings the scene found, put back when it goes
        readonly Material previousSkybox;
        readonly AmbientMode previousAmbientMode;
        readonly bool previousFog;
        readonly FogMode previousFogMode;
        readonly Light previousSun;
        readonly float previousReflectionIntensity;

        public HarborSkyController(Transform parent, Shader skyShader)
        {
            previousSkybox = RenderSettings.skybox;
            previousAmbientMode = RenderSettings.ambientMode;
            previousFog = RenderSettings.fog;
            previousFogMode = RenderSettings.fogMode;
            previousSun = RenderSettings.sun;
            previousReflectionIntensity = RenderSettings.reflectionIntensity;
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
            RenderSettings.sun = sunLeads ? sun : moon;

            Shader.SetGlobalColor(Zenith, C(s.Zenith));
            Shader.SetGlobalColor(Horizon, C(s.Horizon));
            Shader.SetGlobalColor(GroundCol, C(s.Ground));
            Shader.SetGlobalVector(SunDir, new Vector4(toSun.x, toSun.y, toSun.z, Mathf.Clamp01((float)(s.SunElevationDeg + 1) / 2) * (1 - (float)s.Overcast)));
            Shader.SetGlobalColor(SunColor, C(s.SunColor));
            Shader.SetGlobalVector(MoonDir, new Vector4(toMoon.x, toMoon.y, toMoon.z, Mathf.Clamp01((float)(s.MoonElevationDeg + 1) / 2) * (1 - (float)s.Overcast)));
            Shader.SetGlobalColor(MoonColor, C(s.MoonColor));
            Shader.SetGlobalFloat(Stars, (float)s.Stars);
            Shader.SetGlobalFloat(Overcast, (float)s.Overcast);
            Shader.SetGlobalFloat(SkyTime, time);
            Shader.SetGlobalFloat(Night, (float)s.Night);

            RenderSettings.ambientSkyColor = C(s.AmbientSky);
            RenderSettings.ambientEquatorColor = C(s.AmbientEquator);
            RenderSettings.ambientGroundColor = C(s.AmbientGround);
            // what still reflects the environment (glass, steel) reflects less of the probe's daylight as the day goes
            RenderSettings.reflectionIntensity = Mathf.Lerp(1f, 0.08f, (float)s.Night);
            RenderSettings.fogColor = C(s.FogColor);
            RenderSettings.fogDensity = (float)s.FogDensity;
        }
    }
}
