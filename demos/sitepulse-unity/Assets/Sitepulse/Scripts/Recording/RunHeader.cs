// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>A device the run was bound to: its scene id, the platform token the observer's lines name it by, and what kind it is.</summary>
    public sealed class RunDevice
    {
        public string Id { get; set; }
        public string Token { get; set; }
        public string Kind { get; set; }
    }

    /// <summary>A stretch of the run under one clock: the scene's own clock at its real rate, or sped up.</summary>
    public sealed class ClockSegment
    {
        public const string Real = "real";
        public const string Accelerated = "accelerated";

        public double From { get; set; }
        public string Mode { get; set; }
        public double Scale { get; set; }
    }

    /// <summary>What the player was built from: build-info.json, as the acceptance driver stamped it.</summary>
    public sealed class BuildInfo
    {
        public string GitSha { get; set; }
        public bool? TrackedTreeClean { get; set; }
        public string SdkCommit { get; set; }
        public string UnityVersion { get; set; }
        public string ScriptingBackend { get; set; }
        public string BuiltAtUtc { get; set; }

        public static BuildInfo Unknown() => new BuildInfo { GitSha = "unknown" };

        /// <summary>Reads build-info.json; a file that is missing or unreadable is reported as an unknown build, never guessed at.</summary>
        public static BuildInfo Read(string path)
        {
            try
            {
                if (path == null || !File.Exists(path)) return Unknown();
                using var doc = JsonDocument.Parse(File.ReadAllBytes(path));
                var e = doc.RootElement;
                if (e.ValueKind != JsonValueKind.Object) return Unknown();
                return new BuildInfo
                {
                    GitSha = JsonIo.Str(e, "gitSha", "unknown"),
                    TrackedTreeClean = e.TryGetProperty("trackedTreeClean", out var c) && (c.ValueKind == JsonValueKind.True || c.ValueKind == JsonValueKind.False) ? c.GetBoolean() : (bool?)null,
                    SdkCommit = JsonIo.Str(e, "sdkCommit"),
                    UnityVersion = JsonIo.Str(e, "unityVersion"),
                    ScriptingBackend = JsonIo.Str(e, "scriptingBackend"),
                    BuiltAtUtc = JsonIo.Str(e, "buildFinishedAt"),
                };
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException || e is JsonException)
            {
                return Unknown();
            }
        }
    }

    /// <summary>
    /// run.json: who recorded what, with what. No credential and no token is in it (every value passes the Redactor when it is
    /// written), and nothing in it is a secret by construction: ids, versions, times and counts.
    /// </summary>
    public sealed class RunHeader
    {
        public int FormatVersion { get; set; } = RecordingFormat.Version;
        public string RunId { get; set; }
        public DateTimeOffset StartedAtUtc { get; set; }
        public BuildInfo Build { get; set; } = BuildInfo.Unknown();
        public string PlatformVersion { get; set; }
        public string Instance { get; set; }
        public string Tenant { get; set; }
        public string Manifest { get; set; }
        public string Seed { get; set; }
        public double SiteOriginLatitude { get; set; }
        public double SiteOriginLongitude { get; set; }
        public double? ClockOffsetMs { get; set; }
        public List<RunDevice> Devices { get; } = new List<RunDevice>();
        public List<ClockSegment> Clock { get; } = new List<ClockSegment>();

        /// <summary>
        /// How the clock segments were established. Set by the reader, never written: <see cref="ClockRecorded"/> for a run that ended
        /// cleanly, <see cref="ClockRebuilt"/> for one that did not, whose segments are the header's reconciled with presenter.ndjson's clock lines, and
        /// <see cref="ClockUnknown"/> when a run that did not end cleanly left no trace of its clock at all. An unknown clock is
        /// treated as accelerated everywhere: the caption is required rather than risk footage of a fast clock passing for real time.
        /// </summary>
        public string ClockBasis { get; set; } = ClockRecorded;

        public const string ClockRecorded = "recorded", ClockRebuilt = "reconstructed", ClockUnknown = "unknown";
        public List<string> PresenterPresets { get; } = new List<string>();

        // written when the run ends
        public DateTimeOffset? EndedAtUtc { get; set; }
        public bool EndedCleanly { get; set; }
        public double DurationSeconds { get; set; }
        public int SimFrames { get; set; }
        public long SimBytes { get; set; }
        public long ObservedLines { get; set; }
        public long DeviceLines { get; set; }
        public long PresenterLines { get; set; }
        public double WriteMillisTotal { get; set; }
        public double WriteMillisWorstFrame { get; set; }

        public string TokenOf(string deviceId)
        {
            foreach (var d in Devices)
                if (d.Id == deviceId) return d.Token;
            return null;
        }

        public string IdOf(string deviceToken)
        {
            foreach (var d in Devices)
                if (d.Token == deviceToken) return d.Id;
            return null;
        }

        /// <summary>Whether the scene's clock was running faster than real time at <paramref name="t"/> seconds into the run.</summary>
        public bool IsAccelerated(double t)
        {
            if (ClockBasis == ClockUnknown) return true;
            var mode = ClockSegment.Real;
            foreach (var s in Clock)
            {
                if (s.From > t) break;
                mode = s.Mode;
            }

            return mode == ClockSegment.Accelerated;
        }

        /// <summary>Whether any part of [<paramref name="from"/>, <paramref name="to"/>] ran under an accelerated clock.</summary>
        public bool AnyAccelerated(double from, double to)
        {
            if (ClockBasis == ClockUnknown) return true;
            if (IsAccelerated(from)) return true;
            foreach (var s in Clock)
                if (s.Mode == ClockSegment.Accelerated && s.From > from && s.From <= to) return true;
            return false;
        }

        /// <summary>Segments from presenter.ndjson's clock lines are merged in: a line the header lacks is added, one it has is left alone.</summary>
        public void MergeClockSegments(IEnumerable<ClockSegment> found)
        {
            foreach (var f in found)
            {
                var have = false;
                foreach (var s in Clock)
                    if (Math.Abs(s.From - f.From) < 0.0015 && s.Mode == f.Mode && Math.Abs(s.Scale - f.Scale) < 1e-6) have = true;
                if (have) continue;
                var at = Clock.Count;
                while (at > 0 && Clock[at - 1].From > f.From) at--;
                Clock.Insert(at, f);
            }
        }

        public string ToJson()
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                w.WriteNumber("formatVersion", FormatVersion);
                w.WriteString("runId", RunId);
                w.WriteString("startedAtUtc", JsonIo.Time(StartedAtUtc));
                w.WriteStartObject("build");
                JsonIo.WriteStr(w, "gitSha", Build.GitSha);
                if (Build.TrackedTreeClean.HasValue) w.WriteBoolean("trackedTreeClean", Build.TrackedTreeClean.Value);
                JsonIo.WriteStr(w, "sdkCommit", Build.SdkCommit);
                JsonIo.WriteStr(w, "unityVersion", Build.UnityVersion);
                JsonIo.WriteStr(w, "scriptingBackend", Build.ScriptingBackend);
                JsonIo.WriteStr(w, "builtAtUtc", Build.BuiltAtUtc);
                w.WriteEndObject();
                JsonIo.WriteStr(w, "platformVersion", PlatformVersion);
                JsonIo.WriteStr(w, "instance", Instance);
                JsonIo.WriteStr(w, "tenant", Tenant);
                JsonIo.WriteStr(w, "manifest", Manifest);
                JsonIo.WriteStr(w, "seed", Seed);
                w.WriteStartObject("siteOrigin");
                w.WriteNumber("latitude", SiteOriginLatitude);
                w.WriteNumber("longitude", SiteOriginLongitude);
                w.WriteEndObject();
                if (ClockOffsetMs.HasValue) w.WriteNumber("clockOffsetMs", ClockOffsetMs.Value);
                else w.WriteNull("clockOffsetMs");
                w.WriteStartArray("devices");
                foreach (var d in Devices)
                {
                    w.WriteStartObject();
                    w.WriteString("id", d.Id);
                    w.WriteString("token", d.Token);
                    JsonIo.WriteStr(w, "kind", d.Kind);
                    w.WriteEndObject();
                }

                w.WriteEndArray();
                w.WriteStartArray("clock");
                foreach (var s in Clock)
                {
                    w.WriteStartObject();
                    w.WriteNumber("from", JsonIo.Seconds(s.From));
                    w.WriteString("mode", s.Mode);
                    w.WriteNumber("scale", s.Scale);
                    w.WriteEndObject();
                }

                w.WriteEndArray();
                w.WriteStartArray("presenterPresets");
                foreach (var p in PresenterPresets) w.WriteStringValue(p);
                w.WriteEndArray();
                w.WriteStartObject("end");
                w.WriteBoolean("cleanly", EndedCleanly);
                JsonIo.WriteTime(w, "endedAtUtc", EndedAtUtc);
                w.WriteNumber("durationSeconds", JsonIo.Seconds(DurationSeconds));
                w.WriteNumber("simFrames", SimFrames);
                w.WriteNumber("simBytes", SimBytes);
                w.WriteNumber("observedLines", ObservedLines);
                w.WriteNumber("deviceLines", DeviceLines);
                w.WriteNumber("presenterLines", PresenterLines);
                w.WriteNumber("writeMillisTotal", Math.Round(WriteMillisTotal, 2));
                w.WriteNumber("writeMillisWorstFrame", Math.Round(WriteMillisWorstFrame, 3));
                w.WriteEndObject();
                w.WriteEndObject();
            }

            return Redactor.RedactForRecording(Encoding.UTF8.GetString(ms.ToArray())) + "\n";
        }

        public static RunHeader Parse(string json)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json); }
            catch (JsonException e) { throw new RecordingFormatException("run.json is not JSON: " + e.Message); }
            using (doc)
            {
                var e = doc.RootElement;
                if (e.ValueKind != JsonValueKind.Object) throw new RecordingFormatException("run.json is not an object");
                var version = (int)JsonIo.Long(e, "formatVersion", -1);
                if (version != RecordingFormat.Version) throw new RecordingFormatException($"run.json is format version {version}; this build reads version {RecordingFormat.Version}");
                var h = new RunHeader
                {
                    RunId = JsonIo.RequireStr(e, "runId", "run.json"),
                    StartedAtUtc = JsonIo.ParseTime(JsonIo.RequireStr(e, "startedAtUtc", "run.json"), "run.json.startedAtUtc"),
                    PlatformVersion = JsonIo.Str(e, "platformVersion"),
                    Instance = JsonIo.Str(e, "instance"),
                    Tenant = JsonIo.Str(e, "tenant"),
                    Manifest = JsonIo.Str(e, "manifest"),
                    Seed = JsonIo.Str(e, "seed"),
                    ClockOffsetMs = JsonIo.NumOrNull(e, "clockOffsetMs"),
                };
                if (e.TryGetProperty("build", out var b) && b.ValueKind == JsonValueKind.Object)
                    h.Build = new BuildInfo
                    {
                        GitSha = JsonIo.Str(b, "gitSha", "unknown"),
                        TrackedTreeClean = b.TryGetProperty("trackedTreeClean", out var c) && (c.ValueKind == JsonValueKind.True || c.ValueKind == JsonValueKind.False) ? c.GetBoolean() : (bool?)null,
                        SdkCommit = JsonIo.Str(b, "sdkCommit"),
                        UnityVersion = JsonIo.Str(b, "unityVersion"),
                        ScriptingBackend = JsonIo.Str(b, "scriptingBackend"),
                        BuiltAtUtc = JsonIo.Str(b, "builtAtUtc"),
                    };
                if (e.TryGetProperty("siteOrigin", out var o) && o.ValueKind == JsonValueKind.Object)
                {
                    h.SiteOriginLatitude = JsonIo.Num(o, "latitude");
                    h.SiteOriginLongitude = JsonIo.Num(o, "longitude");
                }

                if (e.TryGetProperty("devices", out var ds) && ds.ValueKind == JsonValueKind.Array)
                    foreach (var d in ds.EnumerateArray())
                        h.Devices.Add(new RunDevice { Id = JsonIo.RequireStr(d, "id", "run.json.devices"), Token = JsonIo.RequireStr(d, "token", "run.json.devices"), Kind = JsonIo.Str(d, "kind") });
                if (e.TryGetProperty("clock", out var cs) && cs.ValueKind == JsonValueKind.Array)
                    foreach (var s in cs.EnumerateArray())
                        h.Clock.Add(new ClockSegment { From = JsonIo.Num(s, "from"), Mode = JsonIo.RequireStr(s, "mode", "run.json.clock"), Scale = JsonIo.Num(s, "scale", 1.0) });
                if (e.TryGetProperty("presenterPresets", out var ps) && ps.ValueKind == JsonValueKind.Array)
                    foreach (var p in ps.EnumerateArray())
                        if (p.ValueKind == JsonValueKind.String) h.PresenterPresets.Add(p.GetString());
                if (e.TryGetProperty("end", out var en) && en.ValueKind == JsonValueKind.Object)
                {
                    h.EndedCleanly = JsonIo.Bool(en, "cleanly");
                    h.EndedAtUtc = JsonIo.TimeOrNull(en, "endedAtUtc", "run.json.end");
                    h.DurationSeconds = JsonIo.Num(en, "durationSeconds");
                    h.SimFrames = (int)JsonIo.Long(en, "simFrames");
                    h.SimBytes = JsonIo.Long(en, "simBytes");
                    h.ObservedLines = JsonIo.Long(en, "observedLines");
                    h.DeviceLines = JsonIo.Long(en, "deviceLines");
                    h.PresenterLines = JsonIo.Long(en, "presenterLines");
                    h.WriteMillisTotal = JsonIo.Num(en, "writeMillisTotal");
                    h.WriteMillisWorstFrame = JsonIo.Num(en, "writeMillisWorstFrame");
                }

                return h;
            }
        }
    }
}
