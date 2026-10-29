// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using DeviceChain.Demos.Platform;
using DeviceChain.Towerpulse.Story;
using UnityEngine;

namespace DeviceChain.Towerpulse.Visuals
{
    /// <summary>
    /// Measures the frame time of the campus scene in a player: <c>-tp-bench SECONDS</c> with <c>-tp-replay RUNDIR</c> (or <c>-tp-offline</c>), at the window size
    /// the player was started with. It lets the shot file play (the cameras move through every beat) with vertical sync off, throws the first five seconds away
    /// (shader compiles, texture uploads), and logs one line of <see cref="FrameStats"/>. A maintainer's number: it is not a claim and goes in no caption.
    /// </summary>
    public sealed class FrameBench : MonoBehaviour
    {
        public const string Flag = "-tp-bench";
        const float WarmupS = 5f;

        public static bool Requested { get { return Array.IndexOf(Environment.GetCommandLineArgs(), Flag) >= 0; } }

        readonly List<double> frames = new List<double>();
        float seconds, started;
        StoryApp story;

        void Start()
        {
            story = GetComponent<StoryApp>();
            var raw = CommandLineArgs.Get(Environment.GetCommandLineArgs(), Flag, out var err);
            if (err != null || !float.TryParse(raw, NumberStyles.Float, CultureInfo.InvariantCulture, out seconds) || seconds < 5)
            {
                Debug.LogError("[towerpulse] bench: " + (err ?? "-tp-bench takes the number of seconds to measure, at least 5"));
                Application.Quit(2);
                enabled = false;
                return;
            }
            QualitySettings.vSyncCount = 0;
            Application.targetFrameRate = -1;
            started = Time.realtimeSinceStartup;
            if (story != null && story.Director != null) { story.Director.Loop = true; story.Director.Playing = true; }
        }

        void Update()
        {
            float t = Time.realtimeSinceStartup - started;
            if (t < WarmupS) return;
            frames.Add(Time.unscaledDeltaTime * 1000.0);
            if (t < WarmupS + seconds) return;
            var stats = FrameStats.Of(frames);
            Debug.Log("[towerpulse] bench: " + Screen.width + "x" + Screen.height + " " + SystemInfo.graphicsDeviceType + " " + SystemInfo.graphicsDeviceName + ": " + stats);
            enabled = false;
            Application.Quit(0);
        }
    }
}
