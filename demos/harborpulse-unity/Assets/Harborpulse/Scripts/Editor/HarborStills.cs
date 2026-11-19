// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.IO;
using DeviceChain.Harborpulse.Visuals;
using UnityEditor;
using UnityEditor.SceneManagement;
using UnityEngine;

namespace DeviceChain.Harborpulse.EditorTools
{
    /// <summary>
    /// Look-check stills of the OFFLINE PREVIEW, rendered without entering Play mode, so they can be made in batch mode with no window:
    ///
    ///   Unity -batchmode -projectPath demos/harborpulse-unity -executeMethod DeviceChain.Harborpulse.EditorTools.HarborStills.ShootFromCommandLine
    ///         -hp-stills-dir OUT [-hp-stills "19.4:3,12.5:0"]
    ///
    /// Each "hour:shot" renders the scene's camera at that preview hour from that shot (the numbers the scene's own shot keys use). These are
    /// stills of the preview's stand-in values, for checking light and water; they are not, and must not be captioned as, a live run.
    /// </summary>
    public static class HarborStills
    {
        public const string DefaultSet = "8.0:0,12.5:2,17.9:3,19.4:2,19.4:4,22.6:5,2.0:0";

        public static string Shoot(string outDir, string set, int width = 1600, int height = 900)
        {
            EditorSceneManager.OpenScene(HarborSceneBuilder.ScenePath, OpenSceneMode.Single);
            var app = UnityEngine.Object.FindAnyObjectByType<HarborpulseApp>();
            if (app == null) return "the scene has no HarborpulseApp; run Harborpulse > Build Scene first";
            app.offlinePreview = true;
            app.Initialize();
            if (app.Problem != null) return "the scene could not start: " + app.Problem;
            Directory.CreateDirectory(outDir);
            var made = 0;
            foreach (var item in set.Split(new[] { ',' }, StringSplitOptions.RemoveEmptyEntries))
            {
                var parts = item.Split(':');
                var hour = double.Parse(parts[0], CultureInfo.InvariantCulture);
                var shot = int.Parse(parts[1], CultureInfo.InvariantCulture);
                app.WaterTimeOverride = 37.5f;
                app.Rig.Select(shot, true);
                app.HoldPreviewAt(hour);
                var name = "harborpulse-" + hour.ToString("00.0", CultureInfo.InvariantCulture).Replace('.', 'h') + "-" + app.Rig.Shots[shot].Name.ToLowerInvariant().Replace(' ', '-') + ".png";
                var stamp = "OFFLINE PREVIEW - STAND-IN VALUES, NOT PLATFORM DATA - " + app.Clock.Label.Substring(app.Clock.Label.IndexOf(' ') + 1, 5) + " - " + app.Rig.Shots[shot].Name;
                Render(app.sceneCamera, Path.Combine(outDir, name), width, height, stamp);
                made++;
            }

            app.Teardown();
            return made + " stills in " + outDir;
        }

        /// <param name="stamp">Burnt into the top of the image; a still of the preview always carries one.</param>
        public static void Render(Camera cam, string path, int width, int height, string stamp)
        {
            var rt = new RenderTexture(width, height, 24, RenderTextureFormat.ARGBHalf) { antiAliasing = 1 };
            var ldr = new RenderTexture(width, height, 0, RenderTextureFormat.ARGB32);
            var tex = new Texture2D(width, height, TextureFormat.RGB24, false);
            var prevTarget = cam.targetTexture;
            var prevActive = RenderTexture.active;
            try
            {
                cam.targetTexture = rt;
                cam.Render();
                Graphics.Blit(rt, ldr);
                RenderTexture.active = ldr;
                tex.ReadPixels(new Rect(0, 0, width, height), 0, 0);
                tex.Apply();
                if (string.IsNullOrEmpty(stamp)) throw new ArgumentException("a still of the preview must say so", nameof(stamp));
                PreviewStamp.Stamp(tex, stamp);
                File.WriteAllBytes(path, tex.EncodeToPNG());
            }
            finally
            {
                cam.targetTexture = prevTarget;
                RenderTexture.active = prevActive;
                rt.Release();
                ldr.Release();
                UnityEngine.Object.DestroyImmediate(rt);
                UnityEngine.Object.DestroyImmediate(ldr);
                UnityEngine.Object.DestroyImmediate(tex);
            }
        }

        public static void ShootFromCommandLine()
        {
            try
            {
                var args = Environment.GetCommandLineArgs();
                string dir = null, set = DefaultSet;
                for (var i = 0; i < args.Length - 1; i++)
                {
                    if (args[i] == "-hp-stills-dir") dir = args[i + 1];
                    if (args[i] == "-hp-stills") set = args[i + 1];
                }

                if (dir == null) throw new ArgumentException("-hp-stills-dir is required");
                var result = Shoot(dir, set);
                Debug.Log("[harborpulse] " + result);
                EditorApplication.Exit(result.Contains(" stills in ") ? 0 : 1);
            }
            catch (Exception e)
            {
                Debug.LogError("[harborpulse] stills failed: " + e);
                EditorApplication.Exit(1);
            }
        }

        [MenuItem("Harborpulse/Render Preview Stills")]
        static void Menu()
        {
            var dir = Path.GetFullPath(Path.Combine(Application.dataPath, "..", "Stills"));
            Debug.Log("[harborpulse] " + Shoot(dir, DefaultSet));
        }
    }
}
