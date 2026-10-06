// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Where the player finds its runner and the broker's CA. No secret lives here: the operator
    /// token comes from the runner's /config.json, the device credentials from the platform, and
    /// the CA is a public certificate.
    /// </summary>
    public sealed class LiveSettings
    {
        public string RunnerUrl { get; set; }
        public string CaPemPath { get; set; }

        /// <summary>When set, the runner's /config.json must name this instance.</summary>
        public string ExpectInstance { get; set; }

        /// <summary>When set, the runner's /config.json must name this tenant.</summary>
        public string ExpectTenant { get; set; }

        /// <summary>Where these came from, for messages: "command line" or the file's path.</summary>
        public string Source { get; set; }
    }

    /// <summary>
    /// Reads <see cref="LiveSettings"/> from <c>-dc-runner</c> / <c>-dc-ca</c>, else from
    /// <c>live.json</c> under the persistent data path. A missing or invalid input is an error that
    /// names the file and the field; unknown keys are refused, so a misspelt
    /// <c>expectTenat</c> cannot quietly turn a check off.
    /// </summary>
    public static class LiveSettingsLoader
    {
        public const string FileName = "live.json";
        public const string RunnerFlag = "-dc-runner";
        public const string CaFlag = "-dc-ca";

        static readonly HashSet<string> Known = new HashSet<string> { "runnerUrl", "caPemPath", "expectInstance", "expectTenant" };

        public static Parsed<LiveSettings> Resolve(IReadOnlyList<string> args, string liveJsonPath,
            Func<string, bool> fileExists, Func<string, string> readAllText)
        {
            var runner = CommandLineArgs.Get(args, RunnerFlag, out var e1);
            var ca = CommandLineArgs.Get(args, CaFlag, out var e2);
            if (e1 != null || e2 != null) return Parsed<LiveSettings>.Fail(string.Join("\n", Nonnull(e1, e2)));

            if (runner != null || ca != null)
            {
                var errors = new List<string>();
                if (runner == null) errors.Add($"command line: {CaFlag} is given without {RunnerFlag}");
                if (ca == null) errors.Add($"command line: {RunnerFlag} is given without {CaFlag}");
                if (errors.Count > 0) return Parsed<LiveSettings>.Fail(string.Join("\n", errors));
                return Check(new LiveSettings { RunnerUrl = runner, CaPemPath = ca, Source = "command line" }, "command line", "-dc-runner", "-dc-ca", fileExists);
            }

            if (!fileExists(liveJsonPath))
                return Parsed<LiveSettings>.Fail(
                    $"{liveJsonPath}: not found. Copy live.example.json there and set runnerUrl and caPemPath (or pass {RunnerFlag} and {CaFlag})");
            string text;
            try { text = readAllText(liveJsonPath); }
            catch (Exception e) { return Parsed<LiveSettings>.Fail($"{liveJsonPath}: cannot be read: {e.Message}"); }

            var parsed = ParseJson(text, liveJsonPath);
            if (!parsed.Ok) return parsed;
            return Check(parsed.Value, liveJsonPath, "runnerUrl", "caPemPath", fileExists);
        }

        static IEnumerable<string> Nonnull(params string[] s)
        {
            foreach (var x in s)
                if (x != null) yield return x;
        }

        /// <summary>The URL must be absolute http(s) and the CA file must exist.</summary>
        static Parsed<LiveSettings> Check(LiveSettings s, string where, string runnerField, string caField, Func<string, bool> fileExists)
        {
            var errors = new List<string>();
            if (!Uri.TryCreate(s.RunnerUrl, UriKind.Absolute, out var uri) || (uri.Scheme != "http" && uri.Scheme != "https"))
                errors.Add($"{where}: {runnerField} \"{s.RunnerUrl}\" is not an http(s) URL");
            if (!fileExists(s.CaPemPath))
                errors.Add($"{where}: {caField} \"{s.CaPemPath}\" does not exist");
            return errors.Count > 0 ? Parsed<LiveSettings>.Fail(string.Join("\n", errors)) : Parsed<LiveSettings>.Success(s);
        }

        /// <summary>Parse the file's text; <paramref name="where"/> is its path, for messages.</summary>
        public static Parsed<LiveSettings> ParseJson(string json, string where)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json ?? ""); }
            catch (JsonException e) { return Parsed<LiveSettings>.Fail($"{where}: not valid JSON: {e.Message}"); }

            using (doc)
            {
                if (doc.RootElement.ValueKind != JsonValueKind.Object)
                    return Parsed<LiveSettings>.Fail($"{where}: must be a JSON object");

                var errors = new List<string>();
                var s = new LiveSettings { Source = where };
                foreach (var p in doc.RootElement.EnumerateObject())
                {
                    if (!Known.Contains(p.Name)) { errors.Add($"{where}: unknown field \"{p.Name}\""); continue; }
                    if (p.Value.ValueKind != JsonValueKind.String) { errors.Add($"{where}: \"{p.Name}\" must be a string"); continue; }
                    var v = p.Value.GetString();
                    switch (p.Name)
                    {
                        case "runnerUrl": s.RunnerUrl = v; break;
                        case "caPemPath": s.CaPemPath = v; break;
                        case "expectInstance": s.ExpectInstance = v; break;
                        case "expectTenant": s.ExpectTenant = v; break;
                    }
                }

                if (string.IsNullOrWhiteSpace(s.RunnerUrl)) errors.Add($"{where}: missing \"runnerUrl\"");
                if (string.IsNullOrWhiteSpace(s.CaPemPath)) errors.Add($"{where}: missing \"caPemPath\"");
                return errors.Count > 0 ? Parsed<LiveSettings>.Fail(string.Join("\n", errors)) : Parsed<LiveSettings>.Success(s);
            }
        }
    }
}
