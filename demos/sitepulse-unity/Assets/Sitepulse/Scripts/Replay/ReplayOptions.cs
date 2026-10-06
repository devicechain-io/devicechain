// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>
    /// The command line of a replay or an offline render. <c>-sitepulse-replay &lt;runId|path&gt;</c> names the recording (a run id is looked
    /// for under the recordings directory, <c>-sitepulse-record &lt;dir&gt;</c> or the player's persistent data path);
    /// <c>-sitepulse-render &lt;shots.json&gt; -sitepulse-out &lt;dir&gt;</c> renders shots instead of playing; <c>-sitepulse-replay-start &lt;seconds&gt;</c>
    /// starts an interactive replay part-way in. A flag with no value, a number that is not one, or a render with no recording or no
    /// output directory is an error, never a default.
    /// </summary>
    public sealed class ReplayOptions
    {
        public const string ReplayFlag = "-sitepulse-replay";
        public const string RenderFlag = "-sitepulse-render";
        public const string OutFlag = "-sitepulse-out";
        public const string StartFlag = "-sitepulse-replay-start";
        public const string RecordFlag = "-sitepulse-record";

        public string Recording { get; private set; }
        public string ShotsFile { get; private set; }
        public string OutDir { get; private set; }
        public double StartSeconds { get; private set; }

        /// <summary>An offline render: frames to files, with nothing of the app's own on them.</summary>
        public bool Render => ShotsFile != null;

        /// <summary>Whether any replay or render flag is on the command line (a replay was asked for, in whatever mode the line names).</summary>
        public static bool Wanted(IReadOnlyList<string> args) =>
            Has(args, ReplayFlag) || Has(args, RenderFlag) || Has(args, OutFlag) || Has(args, StartFlag);

        static bool Has(IReadOnlyList<string> args, string flag)
        {
            if (args == null) return false;
            for (var i = 0; i < args.Count; i++)
                if (args[i] == flag) return true;
            return false;
        }

        /// <summary>The value after a flag; null when the flag is absent; an error when it has no value.</summary>
        public static string Get(IReadOnlyList<string> args, string flag, out string error)
        {
            error = null;
            if (args == null) return null;
            for (var i = 0; i < args.Count; i++)
            {
                if (args[i] != flag) continue;
                if (i + 1 >= args.Count || string.IsNullOrEmpty(args[i + 1]) || args[i + 1].StartsWith("-", StringComparison.Ordinal))
                {
                    error = flag + " needs a value";
                    return null;
                }

                return args[i + 1];
            }

            return null;
        }

        /// <summary>Parses the line; <paramref name="error"/> is set (one problem per line) when it cannot be used.</summary>
        public static ReplayOptions Parse(IReadOnlyList<string> args, out string error)
        {
            var problems = new List<string>();
            string Value(string flag)
            {
                var v = Get(args, flag, out var e);
                if (e != null) problems.Add(e);
                return v;
            }

            var o = new ReplayOptions();
            var recording = Value(ReplayFlag);
            o.ShotsFile = Value(RenderFlag);
            o.OutDir = Value(OutFlag);
            var start = Value(StartFlag);
            var recordDir = Value(RecordFlag);
            if (start != null)
            {
                if (!double.TryParse(start, NumberStyles.Float, CultureInfo.InvariantCulture, out var s) || s < 0 || double.IsInfinity(s))
                    problems.Add($"{StartFlag} \"{start}\" is not a number of seconds");
                else o.StartSeconds = s;
            }

            if (recording == null && problems.Count == 0) problems.Add($"{ReplayFlag} <runId|path> is required: a replay needs a recording");
            if (o.Render && o.OutDir == null && problems.Count == 0) problems.Add($"{RenderFlag} needs {OutFlag} <dir>: frames have to go somewhere");
            if (!o.Render && o.OutDir != null) problems.Add($"{OutFlag} is only for {RenderFlag}");
            if (o.Render && start != null) problems.Add($"{StartFlag} is for an interactive replay; a render starts each shot at its recorded event");
            if (problems.Count > 0)
            {
                error = string.Join("\n", problems);
                return null;
            }

            o.Recording = recording;
            o.recordDir = recordDir;
            error = null;
            return o;
        }

        string recordDir;

        /// <summary>The directory the recording is in: the path as given, or the run id under <paramref name="defaultRecordings"/> (or the <c>-sitepulse-record</c> directory).</summary>
        public string RecordingDirectory(string defaultRecordings)
        {
            if (Recording.IndexOfAny(new[] { '/', '\\' }) >= 0 || Directory.Exists(Recording)) return Path.GetFullPath(Recording);
            return Path.Combine(recordDir ?? defaultRecordings, Recording);
        }
    }

    /// <summary>
    /// How a replay is put together, decided from its options alone, so a test can say what a render may show without a scene. An
    /// interactive replay names itself on screen (the badge, the key help, the timeline panel); an offline render shows none of them,
    /// and says what it is in its <c>render.json</c> instead.
    /// </summary>
    public readonly struct ReplayComposition
    {
        ReplayComposition(bool badge, bool hud, bool replayTag, bool writesRenderJson, bool readsKeyboard)
        {
            Badge = badge;
            Hud = hud;
            ReplayTag = replayTag;
            WritesRenderJson = writesRenderJson;
            ReadsKeyboard = readsKeyboard;
        }

        /// <summary>The REPLAY badge is on screen.</summary>
        public bool Badge { get; }

        /// <summary>The screen-space HUD (badge, panels, key help) exists at all.</summary>
        public bool Hud { get; }

        /// <summary>The cards say "replayed" in their status tag.</summary>
        public bool ReplayTag { get; }

        public bool WritesRenderJson { get; }
        public bool ReadsKeyboard { get; }

        public static ReplayComposition For(ReplayOptions options)
        {
            if (options == null) throw new ArgumentNullException(nameof(options));
            return options.Render ? new ReplayComposition(false, false, false, true, false) : new ReplayComposition(true, true, true, false, true);
        }
    }
}
