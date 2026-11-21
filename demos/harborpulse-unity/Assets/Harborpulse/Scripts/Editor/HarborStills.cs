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
    public static partial class HarborStills
    {
        public const string DefaultSet = "12.5:0,17.95:7,18.35:7,18.1:6,19.4:4,20.7:10,26.7:8,12.67:9,11.0:10,22.6:5,17.6:11";

        public static string Shoot(string outDir, string set, int width = 1600, int height = 900, bool hud = true)
        {
            EditorSceneManager.OpenScene(HarborSceneBuilder.ScenePath, OpenSceneMode.Single);
            var app = UnityEngine.Object.FindAnyObjectByType<HarborpulseApp>();
            if (app == null) return "the scene has no HarborpulseApp; run Harborpulse > Build Scene first";
            app.offlinePreview = true;
            app.Initialize();
            if (app.Problem != null) { app.Teardown(); return "the scene could not start: " + app.Problem; }
            try
            {
                if (width < 320 || height < 180 || width > 8192 || height > 8192) throw new ArgumentException("stills dimensions must be within 320x180 and 8192x8192");
                Directory.CreateDirectory(outDir);
                var made = 0;
                foreach (var item in set.Split(new[] { ',' }, StringSplitOptions.RemoveEmptyEntries))
                {
                    var parts = item.Split(':');
                    if (parts.Length != 2) throw new ArgumentException("each still must be hour:shot");
                    var hour = double.Parse(parts[0], CultureInfo.InvariantCulture);
                    if (double.IsNaN(hour) || double.IsInfinity(hour) || hour < 0) throw new ArgumentException("still hour must be finite and nonnegative");
                    // a shot is one of the rig's numbered shots, or "@x/y/z/lookX/lookY/lookZ/fov/name": a free camera, for trying a framing
                    var free = parts[1].StartsWith("@", StringComparison.Ordinal) ? parts[1].Substring(1).Split('/') : null;
                    if (free != null && free.Length != 8) throw new ArgumentException("a free still camera is @x/y/z/lookX/lookY/lookZ/fov/name");
                    var shot = free != null ? 0 : int.Parse(parts[1], CultureInfo.InvariantCulture);
                    if (shot < 0 || shot >= app.Rig.Shots.Count) throw new ArgumentException("unknown still shot " + shot);
                    app.WaterTimeOverride = 37.5f;
                    app.Rig.Select(shot, true);
                    app.HoldPreviewAt(hour);
                    app.Rig.Select(shot, true);
                    var shotName = app.Rig.Shots[shot].Name;
                    if (free != null)
                    {
                        var v = new float[7];
                        for (var k = 0; k < 7; k++)
                        {
                            v[k] = float.Parse(free[k], CultureInfo.InvariantCulture);
                            if (float.IsNaN(v[k]) || float.IsInfinity(v[k])) throw new ArgumentException("a free still camera's numbers must be finite");
                        }
                        if (v[6] < 5 || v[6] > 120) throw new ArgumentException("a free still camera's field of view must be within 5 and 120 degrees");
                        var at = new Vector3(v[0], v[1], v[2]); var look = new Vector3(v[3], v[4], v[5]);
                        if ((look - at).sqrMagnitude < 1e-4f) throw new ArgumentException("a free still camera must look somewhere");
                        app.sceneCamera.transform.SetPositionAndRotation(at, Quaternion.LookRotation(look - at, Vector3.up));
                        app.sceneCamera.fieldOfView = v[6];
                        shotName = free[7];
                    }
                    app.RefreshHud();
                    // "-hp-stills-hud 0": the scene alone under the burnt-in stamp, for judging the sky and the light
                    if (!hud && app.Hud != null) app.Hud.Visible = false;
                    Canvas.ForceUpdateCanvases();
                    var name = "harborpulse-" + ((int)hour % 24).ToString("00", CultureInfo.InvariantCulture) + "h" + ((int)System.Math.Round((hour - System.Math.Floor(hour)) * 60) % 60).ToString("00", CultureInfo.InvariantCulture) + "-" + shotName.ToLowerInvariant().Replace(' ', '-') + ".png";
                    var stamp = "OFFLINE PREVIEW - STAND-IN VALUES, NOT PLATFORM DATA - " + app.Clock.Label.Substring(app.Clock.Label.IndexOf(' ') + 1, 5) + " - " + shotName;
                    Render(app.sceneCamera, Path.Combine(outDir, name), width, height, stamp, app);
                    made++;
                }

                return made + " stills in " + outDir;
            }
            finally { app.Teardown(); }
        }

        /// <param name="stamp">Burnt into the top of the image; a still of the preview always carries one.</param>
        public static void Render(Camera cam, string path, int width, int height, string stamp, HarborpulseApp app = null)
        {
            var rt = new RenderTexture(width, height, 24, RenderTextureFormat.ARGBHalf) { antiAliasing = 1 };
            var ldr = new RenderTexture(width, height, 0, RenderTextureFormat.ARGB32);
            var tex = new Texture2D(width, height, TextureFormat.RGB24, false);
            var prevTarget = cam.targetTexture;
            var prevAspect = cam.aspect;
            var prevActive = RenderTexture.active;
            try
            {
                cam.targetTexture = rt;
                cam.aspect = width / (float)height;
                app?.RenderReflectionNow();
                cam.Render();
                Graphics.Blit(rt, ldr);
                RenderTexture.active = ldr;
                tex.ReadPixels(new Rect(0, 0, width, height), 0, 0);
                tex.Apply();
                if (string.IsNullOrEmpty(stamp)) throw new ArgumentException("a still of the preview must say so", nameof(stamp));
                if (app != null && app.EngineerOverlay)
                {
                    var row = 65;
                    foreach (var card in app.Cards())
                    {
                        var point = cam.WorldToViewportPoint(card.At);
                        if (point.z <= 0 || point.x < -0.1f || point.x > 1.1f) continue;
                        row = PreviewStamp.Card(tex, card.Text, width - 680, row, 668);
                    }
                }
                PreviewStamp.Stamp(tex, stamp);
                File.WriteAllBytes(path, tex.EncodeToPNG());
            }
            finally
            {
                cam.targetTexture = prevTarget;
                cam.aspect = prevAspect;
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
                var width = 1600; var height = 900; var hud = true;
                for (var i = 0; i < args.Length - 1; i++)
                {
                    if (args[i] == "-hp-stills-dir") dir = args[i + 1];
                    if (args[i] == "-hp-stills-width") width = int.Parse(args[i + 1], CultureInfo.InvariantCulture);
                    if (args[i] == "-hp-stills-height") height = int.Parse(args[i + 1], CultureInfo.InvariantCulture);
                    if (args[i] == "-hp-stills") set = args[i + 1];
                    if (args[i] == "-hp-stills-hud") hud = args[i + 1] != "0";
                }

                if (dir == null) throw new ArgumentException("-hp-stills-dir is required");
                var result = Shoot(dir, set, width, height, hud);
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
