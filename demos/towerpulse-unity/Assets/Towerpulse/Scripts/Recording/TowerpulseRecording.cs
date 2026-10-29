// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.Recording;
using DeviceChain.Towerpulse.Story;

namespace DeviceChain.Towerpulse.Recording
{
    public static class TowerpulseFiles
    {
        /// <summary>What only Towerpulse records: the run's identity beyond the shared header.</summary>
        public const string Info = "towerpulse.json";
        /// <summary>The story layer's feed: every fact the boards were told, at the model time it was told.</summary>
        public const string Story = "story.ndjson";
    }

    /// <summary>
    /// The part of a run's header the shared <see cref="RunHeader"/> has no field for. The shared header carries the seed, the manifest, the tenant, the
    /// instance and the clock segments; this adds the model's name, the story clock (start hour and speed) and the platform's rule and profile versions
    /// the day was judged against. It is written beside run.json as towerpulse.json, at the start of the run and again when it ends.
    /// </summary>
    public sealed class TowerpulseRunInfo
    {
        public const int FormatVersion = 1;
        public const string ClockWall = "wall", ClockAccelerated = "accelerated";

        public string ModelVersion { get; set; }
        /// <summary>The scenario manifest the platform side was provisioned from ("towerpulse").</summary>
        public string ManifestId { get; set; }
        public ulong Seed { get; set; }
        public double StartHour { get; set; }
        public double Speed { get; set; } = 1.0;
        /// <summary>Seconds between readings from each device.</summary>
        public double IntervalSeconds { get; set; } = 10;
        /// <summary>Rule token to the version of it that was published when the run began.</summary>
        public SortedDictionary<string, int> RuleVersions { get; } = new SortedDictionary<string, int>(StringComparer.Ordinal);
        /// <summary>Rule token to whether it was enabled. A disabled rule is the control run's whole point, so it is part of the header.</summary>
        public SortedDictionary<string, bool> RuleEnabled { get; } = new SortedDictionary<string, bool>(StringComparer.Ordinal);
        public SortedDictionary<string, int> ProfileVersions { get; } = new SortedDictionary<string, int>(StringComparer.Ordinal);
        /// <summary>Free labels for the run (for example "control" or "rule-disabled:r1").</summary>
        public string Label { get; set; }

        public string ClockMode => Speed > 1.0000001 ? ClockAccelerated : ClockWall;

        public string ToJson(RunHeader header)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                w.WriteNumber("formatVersion", FormatVersion);
                JsonIo.WriteStr(w, "modelVersion", ModelVersion);
                JsonIo.WriteStr(w, "manifestId", ManifestId);
                w.WriteString("seed", Seed.ToString(CultureInfo.InvariantCulture));
                w.WriteNumber("startHour", StartHour);
                w.WriteNumber("speed", Speed);
                w.WriteString("clockMode", ClockMode);
                w.WriteNumber("intervalSeconds", IntervalSeconds);
                JsonIo.WriteStr(w, "label", Label);
                w.WriteStartArray("clockSegments");
                if (header != null)
                    foreach (var s in header.Clock)
                    {
                        w.WriteStartObject();
                        w.WriteNumber("from", JsonIo.Seconds(s.From));
                        w.WriteString("mode", s.Mode);
                        w.WriteNumber("scale", s.Scale);
                        w.WriteEndObject();
                    }
                w.WriteEndArray();
                w.WriteStartObject("ruleVersions");
                foreach (var kv in RuleVersions) w.WriteNumber(kv.Key, kv.Value);
                w.WriteEndObject();
                w.WriteStartObject("ruleEnabled");
                foreach (var kv in RuleEnabled) w.WriteBoolean(kv.Key, kv.Value);
                w.WriteEndObject();
                w.WriteStartObject("profileVersions");
                foreach (var kv in ProfileVersions) w.WriteNumber(kv.Key, kv.Value);
                w.WriteEndObject();
                w.WriteEndObject();
            }
            return Redactor.RedactForRecording(Encoding.UTF8.GetString(ms.ToArray())) + "\n";
        }

        public static TowerpulseRunInfo Parse(string json)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json); }
            catch (JsonException e) { throw new RecordingFormatException(TowerpulseFiles.Info + " is not JSON: " + e.Message); }
            using (doc)
            {
                var e = doc.RootElement;
                if (e.ValueKind != JsonValueKind.Object) throw new RecordingFormatException(TowerpulseFiles.Info + " is not an object");
                var v = (int)JsonIo.Long(e, "formatVersion", -1);
                if (v != FormatVersion) throw new RecordingFormatException(TowerpulseFiles.Info + " is format version " + v + "; this build reads version " + FormatVersion);
                var info = new TowerpulseRunInfo
                {
                    ModelVersion = JsonIo.RequireStr(e, "modelVersion", TowerpulseFiles.Info),
                    ManifestId = JsonIo.RequireStr(e, "manifestId", TowerpulseFiles.Info),
                    StartHour = JsonIo.Num(e, "startHour"),
                    Speed = JsonIo.Num(e, "speed", 1.0),
                    IntervalSeconds = JsonIo.Num(e, "intervalSeconds", 10),
                    Label = JsonIo.Str(e, "label"),
                };
                if (!ulong.TryParse(JsonIo.RequireStr(e, "seed", TowerpulseFiles.Info), NumberStyles.None, CultureInfo.InvariantCulture, out var seed))
                    throw new RecordingFormatException(TowerpulseFiles.Info + ": \"seed\" is not an unsigned integer");
                info.Seed = seed;
                if (e.TryGetProperty("ruleVersions", out var rv) && rv.ValueKind == JsonValueKind.Object)
                    foreach (var p in rv.EnumerateObject()) info.RuleVersions[p.Name] = (int)p.Value.GetDouble();
                if (e.TryGetProperty("ruleEnabled", out var re) && re.ValueKind == JsonValueKind.Object)
                    foreach (var p in re.EnumerateObject()) info.RuleEnabled[p.Name] = p.Value.ValueKind == JsonValueKind.True;
                if (e.TryGetProperty("profileVersions", out var pv) && pv.ValueKind == JsonValueKind.Object)
                    foreach (var p in pv.EnumerateObject()) info.ProfileVersions[p.Name] = (int)p.Value.GetDouble();
                return info;
            }
        }
    }

    /// <summary>One story fact on a line of story.ndjson. <c>t</c> is the recorder's clock (seconds since the run began), <c>sim</c> the model time the story layer saw it at.</summary>
    public static class StoryLine
    {
        static string KindOf(StoryFactKind k)
        {
            switch (k)
            {
                case StoryFactKind.Alarm: return "alarm";
                case StoryFactKind.Command: return "command";
                case StoryFactKind.LedgerReceived: return "received";
                default: return "answered";
            }
        }

        static StoryFactKind ParseKind(string s)
        {
            switch (s)
            {
                case "alarm": return StoryFactKind.Alarm;
                case "command": return StoryFactKind.Command;
                case "received": return StoryFactKind.LedgerReceived;
                case "answered": return StoryFactKind.LedgerAnswered;
                default: throw new RecordingFormatException(TowerpulseFiles.Story + " has a line of unknown kind \"" + s + "\"");
            }
        }

        static string SourceOf(FactSource s)
        {
            switch (s)
            {
                case FactSource.Device: return "device";
                case FactSource.Preview: return "preview";
                default: return "platform";
            }
        }

        static FactSource ParseSource(string s)
        {
            switch (s)
            {
                case "device": return FactSource.Device;
                case "preview": return FactSource.Preview;
                case "platform": return FactSource.Platform;
                default: throw new RecordingFormatException(TowerpulseFiles.Story + " has a line of unknown source \"" + s + "\"");
            }
        }

        public static string ToJson(double t, StoryFact f)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                w.WriteNumber("t", JsonIo.Seconds(t));
                w.WriteNumber("sim", Math.Round(f.SimS, 3));
                w.WriteString("k", KindOf(f.Kind));
                w.WriteString("src", SourceOf(f.Source));
                w.WriteString("dev", f.Device);
                w.WriteString("key", f.Key);
                switch (f.Kind)
                {
                    case StoryFactKind.Alarm:
                        w.WriteString("sev", f.Severity);
                        w.WriteBoolean("active", f.Active);
                        break;
                    case StoryFactKind.Command:
                        w.WriteString("token", f.Token);
                        w.WriteString("status", f.Status);
                        break;
                    case StoryFactKind.LedgerReceived:
                        w.WriteString("token", f.Token);
                        if (f.Numbers.Count > 0)
                        {
                            w.WriteStartObject("num");
                            foreach (var kv in new SortedDictionary<string, double>(f.Numbers, StringComparer.Ordinal)) w.WriteNumber(kv.Key, kv.Value);
                            w.WriteEndObject();
                        }
                        if (f.Strings.Count > 0)
                        {
                            w.WriteStartObject("str");
                            foreach (var kv in new SortedDictionary<string, string>(f.Strings, StringComparer.Ordinal)) w.WriteString(kv.Key, kv.Value);
                            w.WriteEndObject();
                        }
                        break;
                    default:
                        w.WriteString("token", f.Token);
                        w.WriteBoolean("ok", f.Success);
                        w.WriteString("reason", f.Reason);
                        break;
                }
                w.WriteEndObject();
            }
            return Redactor.RedactForRecording(Encoding.UTF8.GetString(ms.ToArray()));
        }

        public static StoryFact Read(JsonElement e, out double t)
        {
            t = JsonIo.Num(e, "t", double.NaN);
            if (double.IsNaN(t)) throw new RecordingFormatException("a story line has no \"t\"");
            var sim = JsonIo.Num(e, "sim", double.NaN);
            if (double.IsNaN(sim)) throw new RecordingFormatException("a story line has no \"sim\"");
            var f = new StoryFact
            {
                Kind = ParseKind(JsonIo.RequireStr(e, "k", "story line")),
                SimS = sim,
                Source = ParseSource(JsonIo.RequireStr(e, "src", "story line")),
                Device = JsonIo.RequireStr(e, "dev", "story line"),
                Key = JsonIo.RequireStr(e, "key", "story line"),
            };
            switch (f.Kind)
            {
                case StoryFactKind.Alarm:
                    f.Severity = JsonIo.Str(e, "sev", "");
                    f.Active = JsonIo.Bool(e, "active");
                    break;
                case StoryFactKind.Command:
                    f.Token = JsonIo.RequireStr(e, "token", "story command line");
                    f.Status = JsonIo.RequireStr(e, "status", "story command line");
                    break;
                case StoryFactKind.LedgerReceived:
                    f.Token = JsonIo.RequireStr(e, "token", "story received line");
                    if (e.TryGetProperty("num", out var n) && n.ValueKind == JsonValueKind.Object)
                        foreach (var p in n.EnumerateObject()) f.Numbers[p.Name] = p.Value.GetDouble();
                    if (e.TryGetProperty("str", out var s) && s.ValueKind == JsonValueKind.Object)
                        foreach (var p in s.EnumerateObject()) f.Strings[p.Name] = p.Value.GetString();
                    break;
                default:
                    f.Token = JsonIo.RequireStr(e, "token", "story answered line");
                    f.Success = JsonIo.Bool(e, "ok");
                    f.Reason = JsonIo.Str(e, "reason", "");
                    break;
            }
            return f;
        }
    }

    /// <summary>A Towerpulse run read back from its folder. Refuses a folder whose files are not in the formats this build writes.</summary>
    public sealed class TowerpulseRecording
    {
        public RunHeader Header { get; private set; }
        public TowerpulseRunInfo Info { get; private set; }
        public List<StoryFact> Facts { get; } = new List<StoryFact>();
        public List<DeviceLine> Samples { get; } = new List<DeviceLine>();
        public List<ObservedLine> Observed { get; } = new List<ObservedLine>();

        public static TowerpulseRecording Open(string directory)
        {
            if (string.IsNullOrEmpty(directory) || !Directory.Exists(directory)) throw new RecordingFormatException("there is no recording at " + directory);
            var r = new TowerpulseRecording();
            r.Header = RunHeader.Parse(File.ReadAllText(Path.Combine(directory, RecordingFiles.RunJson)));
            r.Info = TowerpulseRunInfo.Parse(File.ReadAllText(Path.Combine(directory, TowerpulseFiles.Info)));
            double last = double.NegativeInfinity;
            foreach (var line in Lines(Path.Combine(directory, TowerpulseFiles.Story)))
            {
                using var doc = JsonDocument.Parse(line);
                var f = StoryLine.Read(doc.RootElement, out var t);
                if (t < last) throw new RecordingFormatException(TowerpulseFiles.Story + " goes backwards in time");
                last = t;
                r.Facts.Add(f);
            }
            foreach (var line in Lines(Path.Combine(directory, RecordingFiles.Device)))
            {
                using var doc = JsonDocument.Parse(line);
                r.Samples.Add(DeviceLine.Read(doc.RootElement));
            }
            foreach (var line in Lines(Path.Combine(directory, RecordingFiles.Observed)))
            {
                using var doc = JsonDocument.Parse(line);
                r.Observed.Add(ObservedLine.Read(doc.RootElement));
            }
            // a replay applies facts in model-time order; a recording whose model time runs backwards (a plane reset) cannot be replayed
            for (int i = 1; i < r.Facts.Count; i++)
                if (r.Facts[i].SimS < r.Facts[i - 1].SimS) throw new RecordingFormatException(TowerpulseFiles.Story + ": model time runs backwards at line " + (i + 1));
            return r;
        }

        static IEnumerable<string> Lines(string path)
        {
            if (!File.Exists(path)) throw new RecordingFormatException("the recording is missing " + Path.GetFileName(path));
            foreach (var line in File.ReadAllLines(path))
                if (line.Length > 0) yield return line;
        }
    }
}
