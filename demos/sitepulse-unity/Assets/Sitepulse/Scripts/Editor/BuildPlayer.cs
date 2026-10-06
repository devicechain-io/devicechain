// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.Json;
using UnityEditor;
using UnityEditor.Build;
using UnityEditor.Build.Reporting;
using UnityEngine;

namespace DeviceChain.Sitepulse.EditorTools
{
    /// <summary>
    /// Builds the Windows x64 IL2CPP player the Phase A acceptance runs, and records exactly what was built
    /// (<c>Build/build-info.json</c>): the Unity version, the scripting backend, the stripping level and the build time.
    /// The git commit, the tracked-tree state and the C# SDK commit are NOT read here (the Editor runs on Windows and
    /// the worktree's git metadata lives in WSL): the acceptance driver stamps them in after the build. Run it with
    /// the Editor closed:
    /// <code>Unity.exe -batchmode -quit -projectPath ... -executeMethod DeviceChain.Sitepulse.EditorTools.BuildPlayer.Windows64Il2Cpp -logFile -</code>
    /// or from an Editor that is open, by an eval file that calls <see cref="Run"/> (the file
    /// <c>phase-a-acceptance.sh --build editor</c> writes). A failed build throws, and in batch mode the
    /// process exits non-zero: a build that did not finish is never mistaken for one that did.
    /// </summary>
    public static class BuildPlayer
    {
        public const string OutputPath = "Build/Sitepulse.exe";
        public const string InfoPath = "Build/build-info.json";
        public const string QuarryScene = "Assets/Sitepulse/Scenes/Quarry.unity";

        /// <summary>The <c>-executeMethod</c> entry point.</summary>
        public static void Windows64Il2Cpp()
        {
            try
            {
                var summary = Run();
                UnityEngine.Debug.Log("[sitepulse] build: " + summary);
            }
            catch (Exception e)
            {
                UnityEngine.Debug.LogError("[sitepulse] build FAILED: " + e.Message);
                if (Application.isBatchMode) EditorApplication.Exit(1);
                throw;
            }
        }

        /// <summary>Builds and returns a one-line summary; throws when the build did not succeed.</summary>
        public static string Run()
        {
            var scenes = EditorBuildSettings.scenes.Where(s => s.enabled).Select(s => s.path).ToArray();
            if (scenes.Length == 0) throw new InvalidOperationException("EditorBuildSettings holds no enabled scene: run Sitepulse > Quarry > Rebuild Everything");
            if (!scenes.Contains(QuarryScene)) throw new InvalidOperationException($"{QuarryScene} is not among the build settings' scenes");

            var target = NamedBuildTarget.Standalone;
            if (PlayerSettings.GetScriptingBackend(target) != ScriptingImplementation.IL2CPP)
                throw new InvalidOperationException("the Standalone scripting backend is not IL2CPP; set it in Player settings (the acceptance runs the IL2CPP player)");

            var options = new BuildPlayerOptions
            {
                scenes = scenes,
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
                throw new InvalidOperationException($"the player build ended {summary.result} with {summary.totalErrors} error(s)\n{errors}");
            }

            var info = Describe(started, DateTimeOffset.UtcNow, scenes, (long)summary.totalSize, PlayerSettings.GetManagedStrippingLevel(target).ToString());
            File.WriteAllText(InfoPath, info, new UTF8Encoding(false));
            return $"Succeeded · {summary.totalTime:g} · {summary.totalSize / (1024 * 1024)} MiB · {OutputPath} · {InfoPath}";
        }

        static string Describe(DateTimeOffset started, DateTimeOffset finished, string[] scenes, long bytes, string stripping)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                // the Editor cannot read this repository's git metadata (it runs on Windows, the worktree's .git lives in WSL):
                // the acceptance driver records the commit, the tracked-tree state and the SDK commit and stamps them in after the build
                w.WriteString("git", "not resolvable from the Editor; stamped by tools/phase-a-acceptance.sh after the build");
                w.WriteString("unityVersion", Application.unityVersion);
                w.WriteString("scriptingBackend", PlayerSettings.GetScriptingBackend(NamedBuildTarget.Standalone).ToString());
                w.WriteString("strippingLevel", stripping);
                w.WriteString("target", "StandaloneWindows64");
                w.WriteString("output", OutputPath);
                w.WriteNumber("outputBytes", bytes);
                w.WriteStartArray("scenes");
                foreach (var s in scenes) w.WriteStringValue(s);
                w.WriteEndArray();
                w.WriteString("buildStartedAt", started.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteString("buildFinishedAt", finished.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }
    }
}
