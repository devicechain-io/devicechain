// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>The exact documents sent by Live. Queries only, operator authority, hand-parsed JSON for IL2CPP.</summary>
    public static class HarborReadQueries
    {
        const string CommandFields = " { results { token deviceToken name status queuedTime sentTime respondedTime } } ";
        const string Pump0 = "p0: commands(criteria:{pageNumber:1,pageSize:10,deviceToken:$p0})" + CommandFields;
        const string Pump1 = "p1: commands(criteria:{pageNumber:1,pageSize:10,deviceToken:$p1})" + CommandFields;
        const string Pump2 = "p2: commands(criteria:{pageNumber:1,pageSize:10,deviceToken:$p2})" + CommandFields;
        public const string PumpCommandsOne = "query HarborPumpCommands($p0:String!) { " + Pump0 + "}";
        public const string PumpCommandsTwo = "query HarborPumpCommands($p0:String!,$p1:String!) { " + Pump0 + Pump1 + "}";
        public const string PumpCommands = "query HarborPumpCommands($p0:String!,$p1:String!,$p2:String!) { " + Pump0 + Pump1 + Pump2 + "}";
        public static string CommandsFor(int boundPumps)
        {
            switch (boundPumps) { case 1: return PumpCommandsOne; case 2: return PumpCommandsTwo; case 3: return PumpCommands; default: throw new ArgumentOutOfRangeException(nameof(boundPumps), "harborpulse has three pumps"); }
        }
        public const string Rollout = "query HarborRollout($token:String!, $criteria:UpdateRolloutTargetSearchCriteria!) { updateRollout(token:$token) { token state pauseReason maxInFlight failureThreshold progress { total pending inFlight succeeded failed unknown cancelled skipped } } updateRolloutTargets(token:$token, criteria:$criteria) { results { deviceToken outcome attemptState skipCode } pagination { totalRecords } } }";
        public const string Suppressions = "query HarborSuppression($tokens:[String!]!) { alarmSuppressionWindowsByToken(tokens:$tokens) { token kind status startTime endTime endedAt targets { type token } alarmKeys bypassSeverity } }";
        public const string Attributes = "query HarborAttributes($criteria:EntityAttributeSearchCriteria!) { entityAttributes(criteria:$criteria) { results { entityType scope attrKey valueType value lastUpdated } } }";
        public const string DirectorAlarms = "query DirectorAlarms($c:AlarmSearchCriteria!) { alarms(criteria:$c) { results { token originatorToken alarmKey state severity raisedTime clearedTime raisedDuringSuppression suppression { windowToken until } } pagination { totalRecords } } }";
        public const string AlarmSuppressionStatus = "query DirectorSuppressionStatus($tokens:[String!]!) { alarmSuppressionStatus(tokens:$tokens) { alarmToken state severity suppression { windowToken until } } }";
        public const string NotificationAttempts = "query DirectorNotifications($c:NotificationAttemptSearchCriteria!) { notificationAttempts(criteria:$c) { results { id alarmToken attemptedAt trigger status policyToken } pagination { totalRecords } } }";
        public const string Replacements = "query DirectorReplacements($c:DeviceReplacementSearchCriteria!) { deviceReplacements(criteria:$c) { results { id occurredTime unitIdentifier retiredCredentialTokens newCredentialToken device { id token externalId } } pagination { totalRecords } } }";
        public const string Credentials = "query DirectorCredentials($tokens:[String!]!) { deviceCredentialsByToken(tokens:$tokens) { token enabled device { token } } }";
        public const string Artifact = "query DirectorArtifact($token:String!) { artifact(token:$token) { token state trustStatus signatures { countsTowardTrust signer { token keyId state } } } }";
        public const string Signer = "query DirectorSigner($token:String!) { artifactSigner(token:$token) { token keyId state } }";
        public const string DirectorRollout = "query DirectorRollout($token:String!) { updateRollout(token:$token) { token state startedAt finishedAt stateChangedAt maxInFlight artifact { token } progress { total pending inFlight succeeded failed unknown cancelled skipped } } }";
        public const string Targets = "query DirectorTargets($token:String!,$c:UpdateRolloutTargetSearchCriteria!) { updateRolloutTargets(token:$token,criteria:$c) { results { deviceToken outcome assignmentToken attemptState skipCode dispatchedAt } pagination { totalRecords } } }";
        public const string Assignment = "query DirectorAssignment($token:String!) { updateAssignment(token:$token) { token deviceToken artifact { token } signedBy { token keyId state } attempt { token state lastReportAt failure { stage code platform } } } }";
        public const string Membership = "query DirectorMembership($c:AnchorDeviceSearchCriteria!) { anchorDevices(criteria:$c) { results { token } pagination { totalRecords } } }";
        public const string MeasurementEvents = "query DirectorMeasurements($c:EventSearchCriteria!) { measurementEvents(criteria:$c) { results { id deviceToken occurredTime name value } pagination { totalRecords } } }";
        public const string DirectorCommands = "query DirectorCommands($c:CommandSearchCriteria!) { commands(criteria:$c) { results { token deviceToken name status queuedTime sentTime respondedTime payload metadata } pagination { totalRecords } } }";
        public static void RequireReadOnly(string document)
        {
            // Strip comments and quoted strings, then examine operation tokens at brace depth zero.
            var tokens = Regex.Matches(document ?? "", "#[^\\r\\n]*|\"(?:\\\\.|[^\"\\\\])*\"|[_A-Za-z][_0-9A-Za-z]*|[{}]");
            var depth = 0;
            var first = true;
            foreach (Match match in tokens)
            {
                var t = match.Value;
                if (t[0] == '#' || t[0] == '"') continue;
                if (first) { if (t != "query" && t != "subscription") throw new InvalidOperationException("read-only GraphQL operation required"); first = false; }
                if (t == "mutation" && depth == 0) throw new InvalidOperationException("mutation refused by read-only scene");
                if (t == "{") depth++;
                if (t == "}") depth--;
            }
            if (first || depth != 0) throw new InvalidOperationException("invalid read-only GraphQL document");
        }
    }

    public sealed class OtaTarget
    {
        public string DeviceToken { get; internal set; }
        public string Outcome { get; internal set; }
        public string AttemptState { get; internal set; }
        public string SkipCode { get; internal set; }
    }

    public sealed class RolloutEvidence
    {
        public string Token { get; internal set; }
        public string State { get; internal set; }
        public string PauseReason { get; internal set; }
        public int MaxInFlight { get; internal set; }
        public int? FailureThreshold { get; internal set; }
        public DateTimeOffset ObservedAt { get; internal set; }
        public IReadOnlyDictionary<string, int> Progress { get; internal set; }
        public IReadOnlyList<OtaTarget> Targets { get; internal set; }
        public int TotalTargets { get; internal set; }
        public bool TargetsTruncated => Targets.Count < TotalTargets;
        public string Label => State == "PAUSED" ? "PAUSED(" + (PauseReason ?? "not reported") + ")" : State;
        public OtaTarget Target(string token) { foreach (var t in Targets) if (t.DeviceToken == token) return t; return null; }

        /// <summary>Accepts GraphQL data only; a null rollout remains absent. Outcome and attempt state are independent platform words.</summary>
        public static RolloutEvidence Parse(string dataJson, DateTimeOffset observedAt)
        {
            using var doc = JsonDocument.Parse(dataJson);
            var root = doc.RootElement;
            var r = Wire.Field(root, "updateRollout");
            if (r.ValueKind == JsonValueKind.Null) return null;
            var progress = new Dictionary<string, int>();
            var p = Wire.Field(r, "progress");
            foreach (var name in new[] { "total", "pending", "inFlight", "succeeded", "failed", "unknown", "cancelled", "skipped" }) progress.Add(name, Wire.Int(p, name));
            var targets = new List<OtaTarget>();
            var rows = Wire.Field(root, "updateRolloutTargets");
            if (rows.ValueKind == JsonValueKind.Null) throw new FormatException("rollout targets not reported");
            foreach (var t in Wire.Array(Wire.Field(rows, "results"), "targets"))
            {
                var outcome = Wire.String(t, "outcome");
                if (Array.IndexOf(new[] { "PENDING", "IN_FLIGHT", "SUCCEEDED", "FAILED", "UNKNOWN", "CANCELLED", "SKIPPED" }, outcome) < 0) throw new FormatException("unknown rollout outcome: " + outcome);
                targets.Add(new OtaTarget { DeviceToken = Wire.String(t, "deviceToken"), Outcome = outcome, AttemptState = Wire.NullableString(t, "attemptState"), SkipCode = Wire.NullableString(t, "skipCode") });
            }
            return new RolloutEvidence { Token = Wire.String(r, "token"), State = RolloutState(Wire.String(r, "state")), PauseReason = Wire.NullableString(r, "pauseReason"),
                MaxInFlight = Wire.Int(r, "maxInFlight"), FailureThreshold = Wire.Field(r, "failureThreshold").ValueKind == JsonValueKind.Null ? (int?)null : Wire.Int(r, "failureThreshold"),
                Progress = new System.Collections.ObjectModel.ReadOnlyDictionary<string, int>(progress), Targets = targets.AsReadOnly(),
                TotalTargets = Wire.Int(Wire.Field(rows, "pagination"), "totalRecords"), ObservedAt = observedAt };
        }
        static string RolloutState(string s)
        {
            if (Array.IndexOf(new[] { "DRAFT", "RUNNING", "PAUSED", "COMPLETED", "ABORTED" }, s) < 0) throw new FormatException("unknown rollout state: " + s);
            return s;
        }
    }

    public readonly struct SuppressionTarget
    {
        public SuppressionTarget(string type, string token) { Type = type; Token = token; }
        public string Type { get; }
        public string Token { get; }
    }

    public sealed class SuppressionEvidence
    {
        public string Token { get; internal set; }
        public string Status { get; internal set; }
        public DateTimeOffset Start { get; internal set; }
        public DateTimeOffset End { get; internal set; }
        public DateTimeOffset? EndedAt { get; internal set; }
        public IReadOnlyList<SuppressionTarget> Targets { get; internal set; }
        public IReadOnlyList<string> AlarmKeys { get; internal set; }
        public string BypassSeverity { get; internal set; }
        public DateTimeOffset ObservedAt { get; internal set; }
        public bool Open(DateTimeOffset now) => now >= Start && now < End && (!EndedAt.HasValue || now < EndedAt.Value);
        public bool TargetsDevice(string token) { foreach (var t in Targets) if (t.Type == "DEVICE" && t.Token == token) return true; return false; }
        public static IReadOnlyList<SuppressionEvidence> Parse(string dataJson, DateTimeOffset now)
        {
            using var doc = JsonDocument.Parse(dataJson);
            var list = new List<SuppressionEvidence>();
            foreach (var w in Wire.Array(Wire.Field(doc.RootElement, "alarmSuppressionWindowsByToken"), "suppression windows"))
            {
                if (Wire.String(w, "kind") != "ONE_SHOT") throw new FormatException("lamp service requires a ONE_SHOT window");
                var targets = new List<SuppressionTarget>();
                foreach (var t in Wire.Array(Wire.Field(w, "targets"), "targets")) targets.Add(new SuppressionTarget(Wire.String(t, "type"), Wire.NullableString(t, "token")));
                var keys = new List<string>();
                foreach (var k in Wire.Array(Wire.Field(w, "alarmKeys"), "alarmKeys")) { if (k.ValueKind != JsonValueKind.String) throw new FormatException("alarm key must be string"); keys.Add(k.GetString()); }
                var ended = Wire.Field(w, "endedAt");
                list.Add(new SuppressionEvidence { Token = Wire.String(w, "token"), Status = Wire.String(w, "status"), Start = Wire.Time(w, "startTime").Value, End = Wire.Time(w, "endTime").Value,
                    EndedAt = ended.ValueKind == JsonValueKind.Null ? (DateTimeOffset?)null : Wire.Time(w, "endedAt"), Targets = targets.AsReadOnly(), AlarmKeys = keys.AsReadOnly(),
                    BypassSeverity = Wire.NullableString(w, "bypassSeverity"), ObservedAt = now });
            }
            return list.AsReadOnly();
        }
    }

    public static class AttributeEvidence
    {
        public static Reading? ParseWellAlarm(string dataJson)
        {
            using var doc = JsonDocument.Parse(dataJson);
            Reading? result = null;
            foreach (var r in Wire.Array(Wire.Field(Wire.Field(doc.RootElement, "entityAttributes"), "results"), "attributes"))
            {
                if (Wire.String(r, "entityType") != "device" || Wire.String(r, "scope") != "SHARED" || Wire.String(r, "attrKey") != "well_alarm_m") continue;
                var kind = Wire.String(r, "valueType");
                if (kind != "DOUBLE" && kind != "LONG") throw new FormatException("well_alarm_m: expected numeric attribute");
                if (!double.TryParse(Wire.String(r, "value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var v) || double.IsNaN(v) || double.IsInfinity(v)) throw new FormatException("well_alarm_m: invalid numeric value");
                result = new Reading(v, Wire.Time(r, "lastUpdated").Value);
            }
            return result;
        }
    }
}
