// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.IO;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// A frame-time benchmark for a player build, off unless the player is started with
    /// <c>-sitepulse-benchmark</c>. It turns vsync off, sets 1920x1080, flies the main camera on a
    /// slow orbit over the site, waits out a warm-up, then records every frame's time for a fixed
    /// window and writes the average FPS, the 99th-percentile and worst frame times to
    /// <c>sitepulse-benchmark.txt</c> beside the executable before quitting.
    /// </summary>
    public sealed class FrameTimeBenchmark : MonoBehaviour
    {
        public const string Flag = "-sitepulse-benchmark";

        public float warmupSeconds = 10f;
        public float windowSeconds = 30f;
        public Vector3 orbitCentre = new Vector3(5f, 0f, -5f);
        public float orbitRadius = 175f;
        public float orbitHeight = 70f;
        [Tooltip("Seconds per full orbit.")]
        public float orbitPeriod = 120f;

        readonly List<float> samples = new List<float>(8192);
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
            QualitySettings.vSyncCount = 0;
            Application.targetFrameRate = -1;
            Screen.SetResolution(1920, 1080, FullScreenMode.Windowed);
        }

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
                return;
            }
            samples.Add(Time.unscaledDeltaTime);
            if (now - start < windowSeconds) return;
            Report();
            Application.Quit();
            enabled = false;
        }

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
            int machines = FindObjectsByType<MachineRig>(FindObjectsSortMode.None).Length;
            var terrain = Terrain.activeTerrain;
            int trees = terrain != null && terrain.terrainData != null ? terrain.terrainData.treeInstanceCount : 0;
            string line =
                $"frames={samples.Count} window_s={sum:F1} avg_fps={samples.Count / sum:F1} avg_ms={1000 * sum / samples.Count:F2} " +
                $"p99_ms={1000 * p99:F2} max_ms={1000 * max:F2} res={Screen.width}x{Screen.height} machines={machines} trees={trees} " +
                $"gpu=\"{SystemInfo.graphicsDeviceName}\" cpu=\"{SystemInfo.processorType}\" quality={QualitySettings.names[QualitySettings.GetQualityLevel()]} " +
                $"backend={(Application.isEditor ? "editor" : SystemInfo.graphicsDeviceType.ToString())}";
            File.WriteAllText(Path.Combine(Path.GetDirectoryName(Application.dataPath) ?? ".", "sitepulse-benchmark.txt"), line + "\n");
            Debug.Log("[benchmark] " + line);
        }
    }
}
