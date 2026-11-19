// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The scene's palette: one shared material per surface, all on URP's Lit shader with flat colours (no textures), plus the emissive
    /// materials the night is carried by (lamp heads, windows, beacons). The look is stylised-realistic: muted stone, asphalt and brick, and
    /// saturated colour only where something signals.
    /// </summary>
    public sealed class HarborMaterials
    {
        static readonly int BaseColor = Shader.PropertyToID("_BaseColor");
        static readonly int Smoothness = Shader.PropertyToID("_Smoothness");
        static readonly int Metallic = Shader.PropertyToID("_Metallic");
        static readonly int EmissionColor = Shader.PropertyToID("_EmissionColor");
        static readonly int EnvironmentReflections = Shader.PropertyToID("_EnvironmentReflections");

        readonly Shader lit;
        readonly Dictionary<string, Material> byName = new Dictionary<string, Material>(StringComparer.Ordinal);

        public HarborMaterials(Shader litShader)
        {
            lit = litShader != null ? litShader : Shader.Find("Universal Render Pipeline/Lit");
            if (lit == null) throw new InvalidOperationException("URP's Lit shader is not available: is the project's render pipeline asset assigned?");

            // the matte surfaces take no environment reflection: the reflection probe is the Editor's default sky, not this scene's
            // time of day, and at grazing angles it painted the far land and the night's facades daylight blue
            Ground = Matte(Make("ground", new Color(0.36f, 0.40f, 0.33f), 0.1f));
            Paving = Matte(Make("paving", new Color(0.62f, 0.60f, 0.56f), 0.2f));
            Asphalt = Matte(Make("asphalt", new Color(0.13f, 0.13f, 0.14f), 0.3f));
            Stone = Matte(Make("stone", new Color(0.47f, 0.45f, 0.42f), 0.2f));
            Coping = Matte(Make("coping", new Color(0.72f, 0.70f, 0.66f), 0.25f));
            Seabed = Matte(Make("seabed", new Color(0.30f, 0.27f, 0.20f), 0.05f));
            Timber = Matte(Make("timber", new Color(0.36f, 0.26f, 0.17f), 0.2f));
            Steel = Make("steel", new Color(0.33f, 0.35f, 0.38f), 0.55f, 0.6f);
            DarkSteel = Make("dark-steel", new Color(0.12f, 0.13f, 0.14f), 0.5f, 0.5f);
            Brick = Matte(Make("brick", new Color(0.52f, 0.28f, 0.21f), 0.15f));
            Render = Matte(Make("render", new Color(0.80f, 0.77f, 0.70f), 0.2f));
            Slate = Matte(Make("slate", new Color(0.22f, 0.24f, 0.27f), 0.3f));
            Glass = Make("glass", new Color(0.20f, 0.28f, 0.32f), 0.9f, 0.2f);
            GaugeBoard = Make("gauge-board", new Color(0.92f, 0.92f, 0.88f), 0.4f);
            Minor = Emissive("level-minor", new Color(0.95f, 0.78f, 0.10f), 2f);
            Major = Emissive("level-major", new Color(0.95f, 0.45f, 0.08f), 3f);
            Critical = Emissive("level-critical", new Color(1f, 0.32f, 0.02f), 4f);
            Safety = Make("safety-yellow", new Color(0.95f, 0.72f, 0.08f), 0.45f);
            BuoyRed = Make("buoy-red", new Color(0.75f, 0.10f, 0.08f), 0.5f);
            BuoyGreen = Make("buoy-green", new Color(0.08f, 0.50f, 0.20f), 0.5f);
            Charger = Make("charger", new Color(0.18f, 0.42f, 0.62f), 0.5f);
            Grass = Matte(Make("grass", new Color(0.24f, 0.36f, 0.20f), 0.1f));

            Flow = Emissive("flow", new Color(0.1f, 0.55f, 1f), 2f);
            WorldCue = Emissive("world-cue", new Color(0.55f, 0.75f, 0.88f), 1.8f);
            Failed = Emissive("failed", new Color(1f, 0.32f, 0.25f), 2f);
            EastWindows = Emissive("east-windows", new Color(1f, 0.78f, 0.48f), 0);
            WellGlass = Make("well-glass", new Color(0.55f, 0.8f, 0.85f, 0.12f), 0.85f);
            WellGlass.SetFloat("_Surface", 1);
            WellGlass.SetFloat("_SrcBlend", (float)UnityEngine.Rendering.BlendMode.SrcAlpha);
            WellGlass.SetFloat("_DstBlend", (float)UnityEngine.Rendering.BlendMode.OneMinusSrcAlpha);
            WellGlass.SetFloat("_ZWrite", 0);
            WellGlass.SetFloat("_Cull", 0);
            WellGlass.EnableKeyword("_SURFACE_TYPE_TRANSPARENT");
            WellGlass.renderQueue = 3000;
            for (var i = 0; i < 6; i++)
                Courses[i] = Matte(Make("stone-course-" + i, new Color(0.43f + i * 0.025f, 0.42f + i * 0.023f, 0.39f + i * 0.022f), 0.2f));
            LampOn = Emissive("lamp-on", new Color(1.0f, 0.86f, 0.62f), 10f);
            LampHalo = Emissive("lamp-halo", new Color(1.0f, 0.80f, 0.55f), 3.0f);
            LampOff = Make("lamp-off", new Color(0.30f, 0.30f, 0.28f), 0.6f);
            LampSilent = Emissive("lamp-silent", new Color(0.45f, 0.30f, 0.65f), 0.6f);
            LampUnknown = Make("lamp-unknown", new Color(0.18f, 0.18f, 0.20f), 0.3f);
            Windows = Emissive("windows", new Color(1.0f, 0.78f, 0.48f), 0f);
            WindowsDark = Make("windows-dark", new Color(0.10f, 0.12f, 0.15f), 0.85f);
            StatusGreen = Emissive("status-green", new Color(0.15f, 0.95f, 0.35f), 2.5f);
            Beacon = Emissive("beacon-amber", new Color(1.0f, 0.55f, 0.05f), 4f);
        }

        public Material Flow { get; }
        public Material WorldCue { get; }
        public Material Failed { get; }
        public Material EastWindows { get; }
        public Material WellGlass { get; }
        public Material[] Courses { get; } = new Material[6];
        public Material Jet { get; private set; }
        public Material WetWell { get; private set; }
        public void CreateWetWell(Shader shader)
        {
            if (shader == null) throw new InvalidOperationException("Harborpulse/WetWellWater shader missing");
            WetWell = new Material(shader) { name = "Harbor/wet-well" };
            byName.Add("wet-well", WetWell);
            Jet = new Material(shader) { name = "Harbor/flow-streaks" }; Jet.SetFloat("_Streak", 1);
            byName.Add("flow-streaks", Jet);
        }

        public Material Ground { get; }
        public Material Paving { get; }
        public Material Asphalt { get; }
        public Material Stone { get; }
        public Material Coping { get; }
        public Material Seabed { get; }
        public Material Timber { get; }
        public Material Steel { get; }
        public Material DarkSteel { get; }
        public Material Brick { get; }
        public Material Render { get; }
        public Material Slate { get; }
        public Material Glass { get; }
        public Material GaugeBoard { get; }
        public Material Minor { get; }
        public Material Major { get; }
        public Material Critical { get; }
        public Material Safety { get; }
        public Material BuoyRed { get; }
        public Material BuoyGreen { get; }
        public Material Charger { get; }
        public Material Grass { get; }
        public Material LampOn { get; }
        public Material LampOff { get; }

        /// <summary>The glow round a lit lamp: the lights stand 430 m apart, and without it a lit head is a pixel from the overview.</summary>
        public Material LampHalo { get; }
        public Material LampSilent { get; }
        public Material LampUnknown { get; }
        public Material Windows { get; }
        public Material WindowsDark { get; }
        public Material StatusGreen { get; }
        public Material Beacon { get; }

        /// <summary>Destroys every material this palette made.</summary>
        public void Dispose()
        {
            foreach (var m in byName.Values) MeshKit.Release(m);
            byName.Clear();
        }

        /// <summary>Lights the town's windows: 0 by day, full at night.</summary>
        public void SetNight(float night, float outage = 0)
        {
            var w = new Color(1.0f, 0.78f, 0.48f) * Mathf.Lerp(0.0f, 1.3f, Mathf.SmoothStep(0, 1, night));
            Windows.SetColor(EmissionColor, w);
            EastWindows.SetColor(EmissionColor, w * (1 - outage));
            EastWindows.SetColor(BaseColor, Color.Lerp(new Color(0.10f, 0.12f, 0.15f), new Color(0.55f, 0.45f, 0.30f), night * (1 - outage)));
            Windows.SetColor(BaseColor, Color.Lerp(new Color(0.16f, 0.20f, 0.24f), new Color(0.55f, 0.45f, 0.30f), night));
        }

        Material Make(string name, Color color, float smoothness, float metallic = 0)
        {
            var m = new Material(lit) { name = "Harbor/" + name, enableInstancing = true };
            m.SetColor(BaseColor, color);
            m.SetFloat(Smoothness, smoothness);
            m.SetFloat(Metallic, metallic);
            byName[name] = m;
            return m;
        }

        static Material Matte(Material m)
        {
            m.SetFloat(EnvironmentReflections, 0);
            m.EnableKeyword("_ENVIRONMENTREFLECTIONS_OFF");
            return m;
        }

        Material Emissive(string name, Color color, float intensity)
        {
            var m = Make(name, color, 0.5f);
            m.EnableKeyword("_EMISSION");
            m.globalIlluminationFlags = MaterialGlobalIlluminationFlags.RealtimeEmissive;
            m.SetColor(EmissionColor, color * intensity);
            return m;
        }
    }
}
