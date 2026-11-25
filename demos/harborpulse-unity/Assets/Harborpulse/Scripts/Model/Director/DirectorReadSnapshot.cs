// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Collections;
using System.Collections.ObjectModel;
using System.Linq;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    public enum DirectorProbe { Waiting, PROBE_OK, PROBE_FAILED }

    /// <summary>Main-thread tap, called before the shared observer reduces to latest state.</summary>
    public sealed class DirectorMeasurementTap
    {
        readonly List<DirectorMeasurement> records = new List<DirectorMeasurement>();
        public IReadOnlyList<DirectorMeasurement> Records => new Prefix(records);
        // Publication captures a count, not a copy of the run's entire stream. Appending on
        // the main thread cannot change any already-published prefix or its elements.
        internal sealed class Prefix : IReadOnlyList<DirectorMeasurement>
        {
            readonly List<DirectorMeasurement> records;
            public int Count { get; }
            public Prefix(List<DirectorMeasurement> records) { this.records = records; Count = records.Count; }
            public DirectorMeasurement this[int index] => index >= 0 && index < Count ? records[index] : throw new ArgumentOutOfRangeException(nameof(index));
            public IEnumerator<DirectorMeasurement> GetEnumerator() { for (var i = 0; i < Count; i++) yield return records[i]; }
            IEnumerator IEnumerable.GetEnumerator() => GetEnumerator();
        }
        public void Watch(DirectorMeasurement measurement)
        {
            records.Add(measurement);
        }
    }

    public sealed class DirectorMeasurement
    {
        public DirectorMeasurement(string deviceToken, string name, double value, DateTimeOffset occurredAt)
        { DeviceToken = deviceToken; Name = name; Value = value; OccurredAt = occurredAt; }
        public string DeviceToken { get; }
        public string Name { get; }
        public double Value { get; }
        public DateTimeOffset OccurredAt { get; }
    }

    public sealed class DirectorAlarmChange
    {
        public DirectorAlarmChange(string token, string deviceToken, string key, DateTimeOffset occurredAt)
        { Token = token; DeviceToken = deviceToken; Key = key; OccurredAt = occurredAt; }
        public string Token { get; }
        public string DeviceToken { get; }
        public string Key { get; }
        public DateTimeOffset OccurredAt { get; }
    }

    public sealed class ReadSpan
    {
        public ReadSpan(DateTimeOffset from, DateTimeOffset to, bool gap) { From = from; To = to; Gap = gap; }
        public DateTimeOffset From { get; }
        public DateTimeOffset To { get; }
        public bool Gap { get; }
    }

    /// <summary>A detached successful response. JsonElement clones are immutable and do not retain a disposed document.</summary>
    public sealed class DirectorRead
    {
        public DirectorRead(string name, string scope, DirectorProbe probe = DirectorProbe.Waiting,
            DateTimeOffset? lastSuccess = null, string error = null, JsonElement? data = null, IEnumerable<ReadSpan> coverage = null, DateTimeOffset? asOf = null)
        {
            Name = name; Scope = scope; Probe = probe; LastSuccess = lastSuccess; Error = error;
            Data = data?.Clone(); Coverage = Array.AsReadOnly((coverage ?? Array.Empty<ReadSpan>()).ToArray());
            AsOf = asOf ?? lastSuccess;
        }
        public string Name { get; }
        public string Scope { get; }
        public DirectorProbe Probe { get; }
        public DateTimeOffset? LastSuccess { get; }
        public DateTimeOffset? AsOf { get; }
        public string Error { get; }
        public JsonElement? Data { get; }
        public IReadOnlyList<ReadSpan> Coverage { get; }
        public string State => Probe == DirectorProbe.PROBE_FAILED ? "PROBE_FAILED" : Error != null ? "failed" : LastSuccess.HasValue ? "healthy" : "waiting";
        /// <summary>
        /// How far a success may be stamped after the frame that reads it. The poll stamps the system clock; the frame's "now" is a
        /// recording epoch plus a monotonic timer, and the two differ by milliseconds. Refusing every success stamped after "now"
        /// made each completed poll unhealthy for the next frame, a one-frame gap in every stream about every three seconds,
        /// and one gap is enough to refuse every "none happened" and "exactly" claim of the day.
        /// </summary>
        public static readonly TimeSpan ClockSlack = TimeSpan.FromSeconds(1);
        public bool Healthy(DateTimeOffset now) => Probe == DirectorProbe.PROBE_OK && Error == null && Data.HasValue && LastSuccess.HasValue && now + ClockSlack >= LastSuccess.Value && now - LastSuccess.Value <= TimeSpan.FromSeconds(6);
    }

    public sealed class DirectorReadSnapshot
    {
        public DirectorReadSnapshot(IEnumerable<DirectorRead> reads, bool? measurementsLive = null, int measurementReconnects = 0, int alarmReconnects = 0,
            IEnumerable<DirectorAlarmChange> alarmChanges = null, IEnumerable<DirectorMeasurement> measurements = null)
        {
            Reads = Array.AsReadOnly(reads.ToArray());
            MeasurementsLive = measurementsLive; MeasurementReconnects = measurementReconnects; AlarmReconnects = alarmReconnects;
            AlarmChanges = Array.AsReadOnly((alarmChanges ?? Array.Empty<DirectorAlarmChange>()).ToArray());
            Measurements = measurements is DirectorMeasurementTap.Prefix prefix ? prefix : Array.AsReadOnly((measurements ?? Array.Empty<DirectorMeasurement>()).ToArray());
            Problems = Array.AsReadOnly(Reads.Where(r => r.Error != null || r.Probe == DirectorProbe.PROBE_FAILED)
                .Select(r => r.Name + "/" + r.Scope + ": " + r.Probe + ": " + r.Error).ToArray());
        }
        public IReadOnlyList<DirectorRead> Reads { get; }
        public IReadOnlyList<string> Problems { get; }
        public bool? MeasurementsLive { get; }
        public int MeasurementReconnects { get; }
        public int AlarmReconnects { get; }
        public IReadOnlyList<DirectorAlarmChange> AlarmChanges { get; }
        public IReadOnlyList<DirectorMeasurement> Measurements { get; }
    }

    /// <summary>Shared by the live probes and canned tests. Nullable lookups may be absent; non-null selections may not.</summary>
    public static class DirectorProbeEvaluation
    {
        public static readonly IReadOnlyDictionary<string, string> Roots = new ReadOnlyDictionary<string, string>(new Dictionary<string, string> {
            ["alarms"] = "alarms", ["suppressionStatus"] = "alarmSuppressionStatus", ["windows"] = "alarmSuppressionWindowsByToken",
            ["notifications"] = "notificationAttempts", ["replacements"] = "deviceReplacements", ["credentials"] = "deviceCredentialsByToken",
            ["artifact"] = "artifact", ["signer"] = "artifactSigner", ["rollout"] = "updateRollout", ["targets"] = "updateRolloutTargets",
            ["assignments"] = "updateAssignment", ["membership"] = "anchorDevices", ["measurements"] = "measurementEvents", ["commands"] = "commands" });
        static readonly Dictionary<string, string[]> Required = new Dictionary<string, string[]> {
            ["alarms"] = new[] { "token", "alarmKey", "state", "severity" },
            ["notifications"] = new[] { "id", "alarmToken", "attemptedAt", "trigger", "status" },
            ["replacements"] = new[] { "id", "occurredTime", "retiredCredentialTokens", "newCredentialToken", "device.token", "device.id" },
            ["credentials"] = new[] { "token", "enabled", "device.token" },
            ["artifact"] = new[] { "token", "state", "trustStatus", "signatures" }, ["signer"] = new[] { "token", "keyId", "state" },
            ["rollout"] = new[] { "token", "state", "maxInFlight", "artifact.token", "progress.total", "progress.pending", "progress.inFlight", "progress.succeeded", "progress.failed", "progress.unknown", "progress.cancelled", "progress.skipped" },
            ["targets"] = new[] { "deviceToken", "outcome" }, ["assignments"] = new[] { "token", "deviceToken", "artifact.token", "attempt.token", "attempt.state" },
            ["membership"] = new[] { "token" }, ["measurements"] = new[] { "id", "deviceToken", "name" },
            ["commands"] = new[] { "token", "deviceToken", "name", "status" }, ["windows"] = new[] { "token", "kind", "status", "targets", "alarmKeys" },
            ["suppressionStatus"] = new[] { "alarmToken", "state", "severity" }
        };
        public static bool Paged(string name) => new[] { "alarms", "notifications", "replacements", "targets", "membership", "measurements", "commands" }.Contains(name);
        public static JsonElement Require(JsonElement record, string path)
        {
            var value = record;
            foreach (var part in path.Split('.'))
                if (value.ValueKind != JsonValueKind.Object || !value.TryGetProperty(part, out value) || value.ValueKind == JsonValueKind.Null)
                    throw new FormatException("missing required field " + path);
            return value;
        }
        public static IReadOnlyList<JsonElement> Records(string name, JsonElement data)
        {
            if (!data.TryGetProperty(Roots[name], out var root)) throw new FormatException("missing field " + Roots[name]);
            if (root.ValueKind == JsonValueKind.Null) {
                if (new[] { "artifact", "signer", "rollout", "assignments", "targets" }.Contains(name)) return Array.Empty<JsonElement>();
                throw new FormatException("null non-null field " + Roots[name]);
            }
            if (Paged(name)) {
                var results = Require(root, "results");
                var total = Require(root, "pagination.totalRecords").GetInt32();
                if (total > results.GetArrayLength()) throw new FormatException(name + " page truncated (" + results.GetArrayLength() + "/" + total + ")");
                root = results;
            }
            var rows = root.ValueKind == JsonValueKind.Array ? root.EnumerateArray().ToArray() : new[] { root };
            foreach (var row in rows) {
                foreach (var path in Required[name]) Require(row, path);
                if (name == "assignments" && row.GetProperty("attempt").TryGetProperty("failure", out var failure) && failure.ValueKind != JsonValueKind.Null)
                    foreach (var f in new[] { "stage", "code", "platform" }) Require(failure, f);
                if (name == "artifact") foreach (var sig in row.GetProperty("signatures").EnumerateArray())
                    foreach (var f in new[] { "countsTowardTrust", "signer.token", "signer.keyId", "signer.state" }) Require(sig, f);
            }
            return Array.AsReadOnly(rows);
        }
        public static DirectorProbe Evaluate(string name, long httpStatus, string response, out JsonElement? data, out string error)
        {
            data = null; error = null;
            try {
                if (httpStatus < 200 || httpStatus >= 300) throw new FormatException("HTTP " + httpStatus);
                using var doc = JsonDocument.Parse(response);
                if (doc.RootElement.TryGetProperty("errors", out var errors) && errors.ValueKind == JsonValueKind.Array && errors.GetArrayLength() > 0)
                    throw new FormatException("GraphQL " + string.Join("; ", errors.EnumerateArray().Select(e => e.TryGetProperty("message", out var m) ? m.GetString() : e.GetRawText())));
                var result = Require(doc.RootElement, "data"); Records(name, result); data = result.Clone();
                return DirectorProbe.PROBE_OK;
            }
            catch (Exception ex) when (ex is FormatException || ex is JsonException || ex is InvalidOperationException || ex is KeyNotFoundException) {
                error = ex.Message; return DirectorProbe.PROBE_FAILED;
            }
        }
    }
}
