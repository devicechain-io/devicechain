// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.IO;
using Unity.Profiling;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.Rendering.Universal;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// A frame-time benchmark for a player build, off unless the player is started with
    /// <c>-sitepulse-benchmark</c>. It turns vsync off, sets 1920x1080, flies the main camera on a
    /// slow orbit over the site, waits out a warm-up, then records every frame's time for a fixed
    /// window and writes the average FPS, the 99th-percentile and worst frame times to
    /// <c>sitepulse-benchmark-&lt;quality&gt;.txt</c> beside the executable before quitting. It also
    /// saves the first measured frame as <c>sitepulse-benchmark-&lt;quality&gt;.png</c>, as a record of
    /// what was drawn.
    /// With <c>-sitepulse-quality &lt;name&gt;</c> the player runs at that quality level.
    /// </summary>
    public sealed class FrameTimeBenchmark : MonoBehaviour
    {
        public const string Flag = "-sitepulse-benchmark";

        /// <summary>Command-line flag selecting a quality level by name, e.g.
        /// <c>-sitepulse-quality Laptop</c>. It applies before the scene loads, so everything is
        /// built at that level.</summary>
        public const string QualityFlag = "-sitepulse-quality";

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.BeforeSceneLoad)]
        static void SelectQuality()
        {
            var args = System.Environment.GetCommandLineArgs();
            int i = System.Array.IndexOf(args, QualityFlag);
            if (i < 0 || i + 1 >= args.Length) return;
            int level = System.Array.IndexOf(QualitySettings.names, args[i + 1]);
            if (level >= 0) QualitySettings.SetQualityLevel(level, true);
            else Debug.LogWarning($"[benchmark] no quality level named {args[i + 1]}");
            // for comparing settings without a rebuild: -sitepulse-msaa N, -sitepulse-shadows METRES,
            // -sitepulse-no-ao (no screen-space ambient occlusion), -sitepulse-no-post
            if (GraphicsSettings.currentRenderPipeline is UniversalRenderPipelineAsset rp)
            {
                if (Arg(args, "-sitepulse-msaa") is string m) rp.msaaSampleCount = int.Parse(m);
                if (Arg(args, "-sitepulse-shadows") is string d) rp.shadowDistance = float.Parse(d, System.Globalization.CultureInfo.InvariantCulture);
                if (System.Array.IndexOf(args, "-sitepulse-no-ao") >= 0)
                    foreach (var data in rp.rendererDataList)
                        if (data != null)
                            foreach (var f in data.rendererFeatures)
                                if (f != null && f.GetType().Name == "ScreenSpaceAmbientOcclusion") f.SetActive(false);
            }
            noPost = System.Array.IndexOf(args, "-sitepulse-no-post") >= 0;
        }

        static bool noPost;

        static string Arg(string[] args, string flag)
        {
            int i = System.Array.IndexOf(args, flag);
            return i >= 0 && i + 1 < args.Length ? args[i + 1] : null;
        }

        public float warmupSeconds = 10f;
        public float windowSeconds = 30f;
        public Vector3 orbitCentre = new Vector3(0f, 0f, -25f);
        public float orbitRadius = 190f;
        public float orbitHeight = 70f;
        [Tooltip("Seconds per full orbit.")]
        public float orbitPeriod = 120f;

        readonly List<float> samples = new List<float>(8192);
        readonly List<float> stamps = new List<float>(8192);
        readonly List<float> gpu = new List<float>(8192);
        readonly List<float> cpu = new List<float>(8192);
        readonly FrameTiming[] timing = new FrameTiming[1];
        // what was drawn: averaged over the window (where the player exposes the counters)
        static readonly string[] Counters = { "Batches Count", "SetPass Calls Count", "Draw Calls Count", "Triangles Count", "Shadow Casters Count" };
        readonly List<ProfilerRecorder> recorders = new List<ProfilerRecorder>();
        readonly double[] counterSums = new double[Counters.Length];
        float start = -1f;
        bool active;

        void Awake()
        {
            active = !Application.isEditor && System.Array.IndexOf(System.Environment.GetCommandLineArgs(), Flag) >= 0;
            if (!active)
            {
                enabled = false;
                return;
            }
            foreach (var c in Counters) recorders.Add(ProfilerRecorder.StartNew(ProfilerCategory.Render, c));
            QualitySettings.vSyncCount = 0;
            if (noPost && Camera.main != null && Camera.main.GetComponent<UniversalAdditionalCameraData>() is UniversalAdditionalCameraData cd)
                cd.renderPostProcessing = false;
            Application.targetFrameRate = -1;
            Screen.SetResolution(1920, 1080, FullScreenMode.Windowed);
            // -sitepulse-no-overlay measures the scene without its data layer
            overlayOn = System.Array.IndexOf(System.Environment.GetCommandLineArgs(), "-sitepulse-no-overlay") < 0;
            if (!overlayOn)
                foreach (var o in FindObjectsByType<IotOverlay>(FindObjectsInactive.Include))
                    o.show = false;
        }

        bool overlayOn = true;

        void LateUpdate()
        {
            float now = Time.realtimeSinceStartup;
            var cam = Camera.main;
            if (cam != null)
            {
                float a = now / orbitPeriod * Mathf.PI * 2f;
                cam.transform.position = orbitCentre + new Vector3(Mathf.Sin(a) * orbitRadius, orbitHeight, -Mathf.Cos(a) * orbitRadius);
                cam.transform.LookAt(orbitCentre + Vector3.up * -4f);
            }
            if (now < warmupSeconds) return;
            if (start < 0f)
            {
                start = now;
                ScreenCapture.CaptureScreenshot(Path.Combine(OutputDir, $"sitepulse-benchmark-{QualityName}.png"));
                return;
            }
            samples.Add(Time.unscaledDeltaTime);
            stamps.Add(now);
            for (int k = 0; k < recorders.Count; k++)
                if (recorders[k].Valid) counterSums[k] += recorders[k].LastValue;
            // the render cost apart from frame pacing (needs Frame Timing Stats in the player)
            FrameTimingManager.CaptureFrameTimings();
            bool timed = FrameTimingManager.GetLatestTimings(1, timing) > 0;
            gpu.Add(timed ? (float)timing[0].gpuFrameTime : -1f);
            cpu.Add(timed ? (float)timing[0].cpuMainThreadFrameTime : -1f);
            if (now - start < windowSeconds) return;
            Report();
            Application.Quit();
            enabled = false;
        }

        string CounterText()
        {
            var sb = new System.Text.StringBuilder();
            for (int k = 0; k < Counters.Length; k++)
                if (recorders[k].Valid)
                    sb.Append(Counters[k].Replace(" Count", "").Replace(" ", "_").ToLowerInvariant()).Append('=')
                      .Append((counterSums[k] / Mathf.Max(1, samples.Count)).ToString("F0", System.Globalization.CultureInfo.InvariantCulture)).Append(' ');
            return sb.ToString();
        }

        static string OutputDir => Path.GetDirectoryName(Application.dataPath) ?? ".";

        static string QualityName => QualitySettings.names[QualitySettings.GetQualityLevel()];

        void Report()
        {
            double sum = 0, max = 0;
            foreach (var s in samples)
            {
                sum += s;
                if (s > max) max = s;
            }
            var sorted = new List<float>(samples);
            sorted.Sort();
            float p99 = sorted[Mathf.Min(sorted.Count - 1, (int)(sorted.Count * 0.99f))];
            // the preview's machines are DontSave objects, which FindObjectsByType does not return
            int machines = 0;
            foreach (var f in FindObjectsByType<QuarryFleetPreview>()) machines += f.Count;
            var terrain = Terrain.activeTerrain;
            int trees = terrain != null && terrain.terrainData != null ? terrain.terrainData.treeInstanceCount : 0;
            string Pct(List<float> v)
            {
                var s = v.FindAll(x => x >= 0f);
                if (s.Count == 0) return "n/a";
                s.Sort();
                return $"{s[s.Count / 2]:F2}/{s[Mathf.Min(s.Count - 1, (int)(s.Count * 0.99f))]:F2}";
            }
            string line =
                $"frames={samples.Count} window_s={sum:F1} avg_fps={samples.Count / sum:F1} avg_ms={1000 * sum / samples.Count:F2} " +
                $"p99_ms={1000 * p99:F2} max_ms={1000 * max:F2} gpu_median_p99_ms={Pct(gpu)} cpu_median_p99_ms={Pct(cpu)} " +
                $"res={Screen.width}x{Screen.height} machines={machines} trees={trees} " + CounterText() +
                $"gpu=\"{SystemInfo.graphicsDeviceName}\" cpu=\"{SystemInfo.processorType}\" quality={QualityName} " +
                $"overlay={(overlayOn ? "on" : "off")} " +
                $"backend={(Application.isEditor ? "editor" : SystemInfo.graphicsDeviceType.ToString())}";
            File.WriteAllText(Path.Combine(OutputDir, $"sitepulse-benchmark-{QualityName}.txt"), line + "\n");
            // every frame, for finding where on the flight the slow frames are
            var inv = System.Globalization.CultureInfo.InvariantCulture;
            var csv = new System.Text.StringBuilder("seconds,frame_ms,gpu_ms,cpu_main_ms\n");
            for (int i = 0; i < samples.Count; i++)
                csv.Append(stamps[i].ToString("F3", inv)).Append(',').Append((samples[i] * 1000f).ToString("F3", inv)).Append(',')
                   .Append(gpu[i].ToString("F3", inv)).Append(',').Append(cpu[i].ToString("F3", inv)).Append('\n');
            File.WriteAllText(Path.Combine(OutputDir, $"sitepulse-benchmark-{QualityName}-frames.csv"), csv.ToString());
            Debug.Log("[benchmark] " + line);
        }
    }
}
