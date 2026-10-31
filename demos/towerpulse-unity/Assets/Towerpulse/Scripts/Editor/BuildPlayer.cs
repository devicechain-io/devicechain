// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;
using UnityEditor;
using UnityEditor.Build;
using UnityEditor.Build.Reporting;

namespace DeviceChain.Towerpulse.EditorTools
{
    /// <summary>
    /// Builds the Windows player the acceptance run launches. It builds the story scene alone (the Editor's build-settings scene list is project state this
    /// must not depend on or change) with whatever scripting backend the project is set to. The build is described in Build/build-info.json, which the
    /// recorder reads into a run's header; the commit and the tracked-tree state are stamped in afterwards by acceptance.sh, because the Editor cannot read
    /// this repository's git metadata.
    /// </summary>
    public static class BuildPlayer
    {
        public const string OutputPath = "Build/Towerpulse.exe";
        public const string InfoPath = "Build/build-info.json";
        public const string StoryScene = "Assets/Towerpulse/Scenes/Towerpulse.unity";

        /// <summary>The <c>-executeMethod</c> entry point for <c>Unity.exe -batchmode</c>: a build that did not finish exits non-zero.</summary>
        public static void Windows64()
        {
            try
            {
                UnityEngine.Debug.Log("[towerpulse] build: " + Run());
            }
            catch (Exception e)
            {
                UnityEngine.Debug.LogError("[towerpulse] build FAILED: " + e.Message);
                if (UnityEngine.Application.isBatchMode) EditorApplication.Exit(1);
                throw;
            }
        }

        public static string Run()
        {
            if (!File.Exists(StoryScene)) throw new InvalidOperationException(StoryScene + " does not exist");
            var options = new BuildPlayerOptions
            {
                scenes = new[] { StoryScene },
                locationPathName = OutputPath,
                target = BuildTarget.StandaloneWindows64,
                options = BuildOptions.None,
            };
            var started = DateTimeOffset.UtcNow;
            var report = BuildPipeline.BuildPlayer(options);
            var summary = report.summary;
            if (summary.result != BuildResult.Succeeded)
            {
                var errors = new StringBuilder();
                foreach (var step in report.steps)
                    foreach (var m in step.messages)
                        if (m.type == UnityEngine.LogType.Error || m.type == UnityEngine.LogType.Exception) errors.AppendLine(m.content);
                throw new InvalidOperationException("the player build ended " + summary.result + " with " + summary.totalErrors + " error(s)\n" + errors);
            }

            File.WriteAllText(InfoPath, Describe(started, DateTimeOffset.UtcNow, (long)summary.totalSize), new UTF8Encoding(false));
            return "Succeeded · " + summary.totalTime.ToString("g") + " · " + summary.totalSize / (1024 * 1024) + " MiB · " + OutputPath;
        }

        static string Describe(DateTimeOffset started, DateTimeOffset finished, long bytes)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                w.WriteString("gitSha", "unknown");
                w.WriteString("unityVersion", UnityEngine.Application.unityVersion);
                w.WriteString("scriptingBackend", PlayerSettings.GetScriptingBackend(NamedBuildTarget.Standalone).ToString());
                w.WriteString("target", "StandaloneWindows64");
                w.WriteString("output", OutputPath);
                w.WriteNumber("outputBytes", bytes);
                w.WriteString("scene", StoryScene);
                w.WriteString("buildStartedAt", started.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteString("buildFinishedAt", finished.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteEndObject();
            }
            return Encoding.UTF8.GetString(ms.ToArray());
        }
    }
}
