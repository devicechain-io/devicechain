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
        static readonly int GlowColor = Shader.PropertyToID("_Color");
        static readonly int GlowRadius = Shader.PropertyToID("_Radius");

        /// <summary>The glow shader's per-lamp strength (0 dark .. 1 full), set through a property block.</summary>
        public static readonly int GlowPower = Shader.PropertyToID("_Power");

        /// <summary>The radius of a lit lamp's glow seen close; from far off the shader holds it at a least angle instead.</summary>
        public const float LampGlowRadiusM = 1.7f;

        readonly Shader lit;
        readonly Dictionary<string, Material> byName = new Dictionary<string, Material>(StringComparer.Ordinal);

        public HarborMaterials(Shader litShader)
        {
            lit = litShader != null ? litShader : Shader.Find("Universal Render Pipeline/Lit");
            if (lit == null) throw new InvalidOperationException("URP's Lit shader is not available: is the project's render pipeline asset assigned?");

            // the matte surfaces take no environment reflection: the reflection probe is the Editor's default sky, not this scene's
            // time of day, and at grazing angles it painted the far land and the night's facades daylight blue
            Surfaces = SceneryMesh.Material(); byName["surfaces"] = Surfaces;
            var labelShader = Resources.Load<Shader>("HarborAlarmLabel");
            AlarmText = new Material(labelShader) { name = "Harbor/alarm-text", renderQueue = 4000 };
            AlarmPlate = new Material(labelShader) { name = "Harbor/alarm-plate", renderQueue = 3999 };
            AlarmText.SetColor("_Color", HarborCards.AlarmForeground);
            AlarmPlate.SetColor("_Color", HarborCards.AlarmBackground);
            byName["alarm-text"] = AlarmText; byName["alarm-plate"] = AlarmPlate;
            Ground = Matte(Make("ground", new Color(0.28f, 0.42f, 0.22f), 0.1f));
            Paving = Matte(Make("paving", new Color(0.62f, 0.60f, 0.56f), 0.2f));
            Asphalt = Matte(Make("asphalt", new Color(0.13f, 0.13f, 0.14f), 0.3f));
            Stone = Matte(Make("stone", new Color(0.47f, 0.45f, 0.42f), 0.2f));
            Coping = Matte(Make("coping", new Color(0.59f, 0.57f, 0.53f), 0.25f));
            Seabed = Matte(Make("seabed", new Color(0.30f, 0.27f, 0.20f), 0.05f));
            Timber = Matte(Make("timber", new Color(0.36f, 0.26f, 0.17f), 0.2f));
            Steel = Make("steel", new Color(0.33f, 0.35f, 0.38f), 0.55f, 0.6f);
            DarkSteel = Make("dark-steel", new Color(0.12f, 0.13f, 0.14f), 0.5f, 0.5f);
            Brick = Matte(Make("brick", new Color(0.52f, 0.28f, 0.21f), 0.15f));
            Render = Matte(Make("render", new Color(0.80f, 0.77f, 0.70f), 0.2f));
            Slate = Matte(Make("slate", new Color(0.22f, 0.24f, 0.27f), 0.3f));
            Glass = Make("glass", new Color(0.20f, 0.28f, 0.32f), 0.9f, 0.2f);
            GaugeBoard = Make("gauge-board", new Color(0.92f, 0.92f, 0.88f), 0.4f);
            // Signal emission is tuned for the player, where (since U3) the emission variant is actually compiled: bright enough to read
            // as a signal at HOME distance, low enough that the tonemapper keeps each severity's colour (a hot emissive turns cream)
            // and the bloom never swallows the label drawn over it.
            Minor = Emissive("level-minor", new Color(0.95f, 0.78f, 0.10f), 0.6f);
            Major = Emissive("level-major", new Color(0.95f, 0.45f, 0.08f), 0.8f);
            Critical = Emissive("level-critical", new Color(1f, 0.32f, 0.02f), 1.0f);
            Safety = Make("safety-yellow", new Color(0.95f, 0.72f, 0.08f), 0.45f);
            BuoyRed = Make("buoy-red", new Color(0.75f, 0.10f, 0.08f), 0.5f);
            BuoyGreen = Make("buoy-green", new Color(0.08f, 0.50f, 0.20f), 0.5f);
            Charger = Make("charger", new Color(0.18f, 0.42f, 0.62f), 0.5f);
            Grass = Matte(Make("grass", new Color(0.24f, 0.36f, 0.20f), 0.1f));

            // the town: weathered clapboard paints, brick, cedar shingle and painted trim, with a few colour accents
            ClapWhite = Matte(Make("clapboard-white", new Color(0.86f, 0.85f, 0.81f), 0.15f));
            ClapCream = Matte(Make("clapboard-cream", new Color(0.84f, 0.78f, 0.64f), 0.15f));
            ClapGrey = Matte(Make("clapboard-grey", new Color(0.60f, 0.62f, 0.62f), 0.15f));
            ClapBlue = Matte(Make("clapboard-blue", new Color(0.44f, 0.56f, 0.66f), 0.15f));
            ClapSage = Matte(Make("clapboard-sage", new Color(0.53f, 0.60f, 0.48f), 0.15f));
            ClapYellow = Matte(Make("clapboard-yellow", new Color(0.86f, 0.69f, 0.35f), 0.15f));
            BarnRed = Matte(Make("barn-red", new Color(0.52f, 0.19f, 0.15f), 0.15f));
            Shingle = Matte(Make("cedar-shingle", new Color(0.47f, 0.43f, 0.38f), 0.1f));
            BrickDark = Matte(Make("brick-dark", new Color(0.38f, 0.21f, 0.17f), 0.15f));
            Trim = Matte(Make("trim", new Color(0.92f, 0.91f, 0.88f), 0.2f));
            DoorDark = Matte(Make("door", new Color(0.12f, 0.13f, 0.14f), 0.3f));
            RoofBrown = Matte(Make("roof-brown", new Color(0.31f, 0.25f, 0.21f), 0.2f));
            RoofGreen = Matte(Make("roof-green", new Color(0.24f, 0.32f, 0.29f), 0.2f));
            RoofRed = Matte(Make("roof-red", new Color(0.40f, 0.17f, 0.13f), 0.2f));
            AwningGreen = Matte(Make("awning-green", new Color(0.12f, 0.36f, 0.27f), 0.2f));
            AwningBlue = Matte(Make("awning-blue", new Color(0.13f, 0.30f, 0.50f), 0.2f));
            Lane = Matte(Make("lane", new Color(0.20f, 0.21f, 0.21f), 0.25f));
            Lawn = Matte(Make("lawn", new Color(0.30f, 0.47f, 0.21f), 0.1f));
            Piling = Matte(Make("piling", new Color(0.26f, 0.21f, 0.16f), 0.15f));
            HullWhite = Make("hull-white", new Color(0.90f, 0.90f, 0.88f), 0.5f);
            HullNavy = Make("hull-navy", new Color(0.10f, 0.17f, 0.30f), 0.5f);
            HullRed = Make("hull-red", new Color(0.62f, 0.15f, 0.12f), 0.5f);
            // autumn foliage: greens with gold, orange and red; dark pines
            FoliageGreen = Matte(Make("foliage-green", new Color(0.20f, 0.33f, 0.15f), 0.05f));
            FoliageOlive = Matte(Make("foliage-olive", new Color(0.40f, 0.42f, 0.17f), 0.05f));
            FoliageGold = Matte(Make("foliage-gold", new Color(0.78f, 0.58f, 0.14f), 0.05f));
            FoliageOrange = Matte(Make("foliage-orange", new Color(0.80f, 0.38f, 0.09f), 0.05f));
            FoliageRed = Matte(Make("foliage-red", new Color(0.60f, 0.15f, 0.08f), 0.05f));
            FoliagePine = Matte(Make("foliage-pine", new Color(0.11f, 0.20f, 0.13f), 0.05f));
            Bark = Matte(Make("bark", new Color(0.24f, 0.19f, 0.15f), 0.1f));
            TownLamp = Emissive("town-lamp", new Color(1.0f, 0.85f, 0.6f), 0);
            EastTownLamp = Emissive("east-town-lamp", new Color(1.0f, 0.85f, 0.6f), 0);

            Flow = Emissive("flow", new Color(0.1f, 0.55f, 1f), 1.2f);
            WorldCue = Emissive("world-cue", new Color(0.55f, 0.75f, 0.88f), 1.1f);
            Rain = Emissive("rain", RainCool, 1.1f);
            Failed = Emissive("failed", new Color(1f, 0.32f, 0.25f), 1.2f);
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
            LampOn = Emissive("lamp-on", new Color(1.0f, 0.86f, 0.62f), 4f);
            var glowShader = Resources.Load<Shader>("HarborGlow");
            if (glowShader == null) throw new InvalidOperationException("HarborGlow shader missing from Resources");
            LampHalo = new Material(glowShader) { name = "Harbor/lamp-halo" };
            LampHalo.SetColor(GlowColor, new Color(1.0f, 0.80f, 0.55f) * 1.15f);
            LampHalo.SetFloat(GlowRadius, LampGlowRadiusM);
            byName["lamp-halo"] = LampHalo;
            LampOff = Make("lamp-off", new Color(0.30f, 0.30f, 0.28f), 0.6f);
            LampSilent = Emissive("lamp-silent", new Color(0.45f, 0.30f, 0.65f), 0.6f);
            LampUnknown = Make("lamp-unknown", new Color(0.18f, 0.18f, 0.20f), 0.3f);
            Windows = Emissive("windows", new Color(1.0f, 0.78f, 0.48f), 0f);
            WindowsDark = Make("windows-dark", new Color(0.10f, 0.12f, 0.15f), 0.85f);
            StatusGreen = Emissive("status-green", new Color(0.15f, 0.95f, 0.35f), 1.4f);
            Beacon = Emissive("beacon-amber", new Color(1.0f, 0.55f, 0.05f), 1.0f);
            // The devices that hold their own session. A charger on a token is teal and one on a certificate is navy with brass, so the
            // two read apart from each other and from the district's blue at a glance; each wears a band that is lit while it reports.
            ChargerLink = Make("charger-link", new Color(0.09f, 0.46f, 0.44f), 0.5f);
            ChargerCert = Make("charger-cert", new Color(0.12f, 0.16f, 0.30f), 0.55f);
            Brass = Make("brass", new Color(0.80f, 0.62f, 0.24f), 0.65f, 0.8f);
            SolarGlass = Make("solar-glass", new Color(0.06f, 0.11f, 0.24f), 0.9f, 0.3f);
            LinkOn = Emissive("link-on", new Color(0.15f, 0.90f, 0.85f), 1.3f);
            SealOn = Emissive("seal-on", new Color(1.0f, 0.74f, 0.25f), 1.2f);
            PipOn = Emissive("pip-on", new Color(1.0f, 0.86f, 0.62f), 1.6f);
        }

        /// <summary>The body of a charger that holds an MQTT session under a device token, and of one that holds it under a client certificate.</summary>
        public Material ChargerLink { get; }
        public Material ChargerCert { get; }
        public Material Brass { get; }
        /// <summary>A pull lamp's own solar panel.</summary>
        public Material SolarGlass { get; }
        /// <summary>The band on a reporting charger: cyan on a token, gold on a certificate.</summary>
        public Material LinkOn { get; }
        public Material SealOn { get; }
        /// <summary>A lit pip of a pull lamp's level board.</summary>
        public Material PipOn { get; }

        public Material Surfaces { get; }
        public Material AlarmText { get; }
        public Material AlarmPlate { get; }
        public Material Flow { get; }
        public Material WorldCue { get; }
        /// <summary>The rain in the air: the WORLD cue's cool white, warmed when the low sun lights it through the break under the deck.</summary>
        public Material Rain { get; }
        static readonly Color RainCool = new Color(0.55f, 0.75f, 0.88f);
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
        public Material ClapWhite { get; }
        public Material ClapCream { get; }
        public Material ClapGrey { get; }
        public Material ClapBlue { get; }
        public Material ClapSage { get; }
        public Material ClapYellow { get; }
        public Material BarnRed { get; }
        public Material Shingle { get; }
        public Material BrickDark { get; }
        public Material Trim { get; }
        public Material DoorDark { get; }
        public Material RoofBrown { get; }
        public Material RoofGreen { get; }
        public Material RoofRed { get; }
        public Material AwningGreen { get; }
        public Material AwningBlue { get; }
        public Material Lane { get; }
        public Material Lawn { get; }
        public Material Piling { get; }
        public Material HullWhite { get; }
        public Material HullNavy { get; }
        public Material HullRed { get; }
        public Material FoliageGreen { get; }
        public Material FoliageOlive { get; }
        public Material FoliageGold { get; }
        public Material FoliageOrange { get; }
        public Material FoliageRed { get; }
        public Material FoliagePine { get; }
        public Material Bark { get; }

        /// <summary>The town's own street lamps (scenery, not devices): lit with the night, the eastern ones dark through the feeder outage.</summary>
        public Material TownLamp { get; }
        public Material EastTownLamp { get; }

        /// <summary>
        /// The glow round a lit lamp, on the Harborpulse/Glow shader: a soft round camera-facing disc with no edge (it was a faceted ball, which
        /// read as a paper lantern up close). The lights stand 430 m apart, and without it a lit head is a pixel from the overview.
        /// </summary>
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

        /// <summary>Lights the town's windows and street lamps: 0 by day, full at night. The east of the town is on the east feeder.</summary>
        public void SetNight(float night, float outage = 0)
        {
            var lit = Mathf.SmoothStep(0, 1, night);
            var w = new Color(1.0f, 0.78f, 0.48f) * Mathf.Lerp(0.0f, 4f, lit);
            Windows.SetColor(EmissionColor, w);
            EastWindows.SetColor(EmissionColor, w * (1 - outage));
            var lamp = new Color(1.0f, 0.80f, 0.52f) * Mathf.Lerp(0.0f, 12f, lit);
            TownLamp.SetColor(EmissionColor, lamp);
            EastTownLamp.SetColor(EmissionColor, lamp * (1 - outage));
            TownLamp.SetColor(BaseColor, Color.Lerp(new Color(0.55f, 0.55f, 0.52f), new Color(1.0f, 0.85f, 0.6f), lit));
            EastTownLamp.SetColor(BaseColor, Color.Lerp(new Color(0.55f, 0.55f, 0.52f), new Color(1.0f, 0.85f, 0.6f), lit * (1 - outage)));
            EastWindows.SetColor(BaseColor, Color.Lerp(new Color(0.10f, 0.12f, 0.15f), new Color(0.55f, 0.45f, 0.30f), night * (1 - outage)));
            Windows.SetColor(BaseColor, Color.Lerp(new Color(0.16f, 0.20f, 0.24f), new Color(0.55f, 0.45f, 0.30f), night));
        }

        /// <summary>Colours the rain by the sky: cool by default, gold while the break under the storm's deck lights it from behind.</summary>
        public void SetRain(DeviceChain.Harborpulse.Model.SkyState sky)
        {
            var warm = new Color((float)sky.BreakColor.R, (float)sky.BreakColor.G, (float)sky.BreakColor.B);
            var c = Color.Lerp(RainCool, warm, (float)sky.HorizonBreak * 0.7f);
            Rain.SetColor(BaseColor, c);
            Rain.SetColor(EmissionColor, c * 1.1f);
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
