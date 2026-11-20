// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>Read-only evidence adapter. Joins use platform identities; runner plans never establish platform outcomes.</summary>
    public sealed class DirectorObserver : IDirectorFeed
    {
        sealed class Row { public string Name, Scope; public JsonElement Value; public DirectorRead Read; }
        readonly HarborLayout layout;
        readonly Func<DirectorReadSnapshot> reads;
        // Sticky problems (a malformed record, a missing instant) last the run; current ones restate this frame's read state.
        readonly HashSet<string> problems = new HashSet<string>(StringComparer.Ordinal);
        readonly HashSet<string> current = new HashSet<string>(StringComparer.Ordinal);
        readonly Dictionary<string, string> emitted = new Dictionary<string, string>();
        readonly Dictionary<string, JsonElement> alarmHistory = new Dictionary<string, JsonElement>();
        readonly Dictionary<string, Reading> previous = new Dictionary<string, Reading>();
        readonly Dictionary<string, Dictionary<string, string>> trends = new Dictionary<string, Dictionary<string, string>>();
        readonly Dictionary<string, JsonElement> wellHistory = new Dictionary<string, JsonElement>();
        readonly Dictionary<string, string> wellCycles = new Dictionary<string, string>();
        readonly Dictionary<string, Reading> pumpRunning = new Dictionary<string, Reading>();
        readonly Dictionary<string, DateTimeOffset> pumpOffAt = new Dictionary<string, DateTimeOffset>();
        readonly HashSet<string> failedProbes = new HashSet<string>();
        DirectorInputs input;
        Action<Observation> observe;
        Action<CoverageInterval> cover;
        List<Row> rows;
        List<DirectorRead> complete;
        int measurementIndex;
        public long MeasurementWorkUnits { get; private set; }
        string run;
        double startSim, lastSim;
        int measurementReconnects, alarmReconnects;
        static readonly Dictionary<string, string[]> Streams = new Dictionary<string, string[]> {
            ["alarms"] = new[] { "alarms", "notifications", "pump" }, ["notifications"] = new[] { "notifications" },
            ["windows"] = new[] { "notifications" }, ["suppressionStatus"] = new[] { "notifications" },
            ["credentials"] = new[] { "credentials" }, ["replacements"] = new[] { "credentials" },
            ["measurements"] = new[] { "measurements", "pump" }, ["commands"] = new[] { "pump" },
            ["membership"] = new[] { "alarms" }, ["artifact"] = new[] { "updates" }, ["signer"] = new[] { "updates" },
            ["rollout"] = new[] { "updates" }, ["targets"] = new[] { "updates" }, ["assignments"] = new[] { "updates" }
        };
        public DirectorObserver(HarborLayout layout, Func<DirectorReadSnapshot> reads)
        { this.layout = layout ?? throw new ArgumentNullException(nameof(layout)); this.reads = reads ?? throw new ArgumentNullException(nameof(reads)); }
        public IReadOnlyList<string> Problems => Array.AsReadOnly(problems.Union(current).OrderBy(p => p, StringComparer.Ordinal).ToArray());
        static string S(JsonElement r, string key) { var v = DirectorProbeEvaluation.Require(r, key); if (v.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(v.GetString())) throw new FormatException("missing required string " + key); return v.GetString(); }
        static string Optional(JsonElement r, string key) => r.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;
        static DateTimeOffset Time(string value) => DateTimeOffset.TryParse(value, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var t) ? t : throw new FormatException("invalid platform instant " + value);
        static DateTimeOffset T(JsonElement r, string key) => Time(S(r, key));
        static string N(double value) => value.ToString("R", CultureInfo.InvariantCulture);
        static string B(bool value) => value ? "true" : "false";
        // The verifier compares stored times at database (microsecond) precision.
        static string ReadingIdentity(string device, string instant) => device + "@" + (Time(instant).UtcDateTime.Ticks / 10).ToString(CultureInfo.InvariantCulture);
        double Wall(DateTimeOffset t) => (t - input.RecordingEpoch).TotalSeconds;
        // Rounded to a microhour (3.6 ms of sim time): converting a platform instant lands a few nanohours off an authored
        // boundary (the 10:45 window opens at 10.7499999967), which would put it outside a page window that starts at 10.75.
        double Sim(DateTimeOffset t) => RoundSim(input.Source?.Status?.StartedAt != null ? input.Source.Status.ToSimSeconds(t) / 3600 : input.Sim + (t - input.Now).TotalSeconds * 24 / layout.DayLengthSeconds);
        public static double RoundSim(double hours) => Math.Round(hours, 6);
        string Device(string token)
        {
            foreach (var d in layout.Devices) if (input.Source != null && input.Source.TryToken(d.ExternalId, out var bound) && bound == token) return d.Token;
            throw new FormatException("unbound platform device " + token);
        }
        string RunnerDevice(string token) => layout.Devices.FirstOrDefault(d => d.Token == token || d.ExternalId == token)?.Token ?? throw new FormatException("unknown runner device " + token);
        IEnumerable<Row> Rows(string name) => rows.Where(r => r.Name == name);
        void Gap(string name, double from, double to) { if (Streams.TryGetValue(name, out var streams)) foreach (var s in streams) cover(new CoverageInterval(input.Run, s, from, to, true)); }
        void Safe(string name, Action body)
        {
            try { body(); }
            catch (Exception ex) when (ex is FormatException || ex is InvalidOperationException || ex is KeyNotFoundException || ex is JsonException) {
                problems.Add(name + ": " + ex.Message); Gap(name, lastSim, input.Sim);
            }
        }
        void Emit(string kind, string identity, string device, string key, string cycle, string status, DateTimeOffset at,
            Dictionary<string, string> fields = null, Dictionary<string, string[]> sets = null, string source = EvidenceSource.Platform, string command = "", string attempt = "", double? arrivalSim = null)
        {
            if (at > input.Now) return;
            var o = new Observation(input.Run, kind, identity, device, key, cycle, command, attempt, status, Wall(input.Now), Wall(at), arrivalSim ?? Sim(at), source, fields, sets);
            var fingerprint = string.Join("|", o.Fields.OrderBy(p => p.Key).Select(p => p.Key + "=" + p.Value)) + string.Join("|", o.Sets.OrderBy(p => p.Key).Select(p => p.Key + "=" + string.Join(",", p.Value)));
            if (emitted.TryGetValue(o.EventKey, out var old) && old == fingerprint) return;
            emitted[o.EventKey] = fingerprint; observe(o);
        }
        public void Update(DirectorInputs inputs, Action<Observation> observe, Action<CoverageInterval> cover)
        {
            input = inputs ?? throw new ArgumentNullException(nameof(inputs)); this.observe = observe; this.cover = cover;
            if (run != inputs.Run) {
                run = inputs.Run; emitted.Clear(); alarmHistory.Clear(); previous.Clear(); trends.Clear(); wellHistory.Clear(); wellCycles.Clear(); pumpRunning.Clear(); pumpOffAt.Clear(); failedProbes.Clear(); problems.Clear();
                startSim = lastSim = input.Sim; measurementReconnects = alarmReconnects = measurementIndex = 0;
            }
            if (input.Sim < lastSim) throw new InvalidOperationException("observer requires a new instance on seek");
            current.Clear(); var snapshot = reads(); rows = new List<Row>(); complete = new List<DirectorRead>();
            if (snapshot == null) {
                foreach (var name in Streams.Keys) Gap(name, lastSim, input.Sim);
                if (input.Source is PreviewSource) { current.Add("OFFLINE PREVIEW: scenario stand-ins; platform-only reads unavailable"); Preview(); }
                else current.Add("director reads waiting for bind");
            }
            else {
                foreach (var p in snapshot.Problems) current.Add(p);
                foreach (var r in snapshot.Reads.Where(r => r.Probe == DirectorProbe.PROBE_FAILED)) failedProbes.Add(r.Name);
                foreach (var name in failedProbes) Gap(name, startSim, input.Sim);
                foreach (var name in Streams.Keys) {
                    var group = snapshot.Reads.Where(r => r.Name == name && r.Scope != "__probe").ToArray();
                    bool healthy = group.Length > 0 && !failedProbes.Contains(name) && group.All(r => r.Healthy(input.Now));
                    if (!healthy) Gap(name, lastSim, input.Sim);
                    foreach (var r in group) {
                        foreach (var span in r.Coverage.Where(c => c.Gap && Sim(c.To) >= startSim && Sim(c.From) <= input.Sim)) Gap(name, Math.Max(startSim, Sim(span.From)), Math.Min(input.Sim, Sim(span.To)));
                        if (!r.Healthy(input.Now) || failedProbes.Contains(name) || !r.Data.HasValue) continue;
                        Safe(name, () => {
                            var records = DirectorProbeEvaluation.Records(name, r.Data.Value);
                            complete.Add(r);
                            foreach (var v in records) rows.Add(new Row { Name = name, Scope = r.Scope, Value = v, Read = r });
                        });
                    }
                }
                foreach (var stream in Streams.Values.SelectMany(s => s).Distinct()) {
                    var names = Streams.Where(p => p.Value.Contains(stream)).Select(p => p.Key).ToArray();
                    var group = snapshot.Reads.Where(r => names.Contains(r.Name) && r.Scope != "__probe").ToArray();
                    if (names.All(n => group.Any(r => r.Name == n)) && group.All(r => r.Healthy(input.Now)) && !names.Any(failedProbes.Contains))
                        cover(new CoverageInterval(run, stream, lastSim, input.Sim, false));
                }
                var alarmLive = input.Source?.AlarmObservation.Observed == true;
                if (!alarmLive || snapshot.AlarmReconnects != alarmReconnects) { Gap("alarms", lastSim, input.Sim); }
                if (snapshot.MeasurementsLive != true || snapshot.MeasurementReconnects != measurementReconnects) Gap("measurements", lastSim, input.Sim);
                alarmReconnects = snapshot.AlarmReconnects; measurementReconnects = snapshot.MeasurementReconnects;
                foreach (var r in Rows("alarms")) Safe("alarms", () => Alarm(r.Value));
                if (!failedProbes.Contains("alarms")) foreach (var change in snapshot.AlarmChanges) Safe("alarms", () => {
                    var a = alarmHistory.Values.Where(a => S(a, "token") == change.Token && S(a, "originatorToken") == change.DeviceToken && S(a, "alarmKey") == change.Key && T(a, "raisedTime") <= change.OccurredAt)
                        .OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                    if (a.ValueKind == JsonValueKind.Undefined) return; // the poll has not supplied this cycle's identity yet
                    Emit("alarm", change.Token, Device(change.DeviceToken), change.Key, S(a, "raisedTime"), "ESCALATED", change.OccurredAt,
                        new Dictionary<string, string> { ["alarmToken"] = change.Token });
                });
                foreach (var r in Rows("windows")) Safe("windows", () => Window(r.Value));
                foreach (var r in Rows("notifications")) Safe("notifications", () => Notification(r.Value));
                foreach (var r in Rows("replacements")) Safe("replacements", () => Replacement(r.Value));
                foreach (var r in Rows("credentials")) Safe("credentials", () => Credential(r));
                foreach (var r in Rows("commands")) Safe("commands", () => Command(r.Value));
                foreach (var r in Rows("rollout")) Safe("rollout", () => Ota(r.Value));
                foreach (var r in complete.Where(r => r.Name == "membership")) Safe("membership", () => Membership(r.Scope, Rows("membership").Where(x => x.Read == r).Select(x => x.Value).ToArray(), r));
                StreamMeasurements(snapshot);
                Measurements(); Chips(); Binding();
            }
            Safe("runner", Runner);
            lastSim = input.Sim;
        }
        void Alarm(JsonElement a)
        {
            var id = S(a, "token"); var raised = S(a, "raisedTime"); var at = Time(raised); var device = Device(S(a, "originatorToken")); var key = S(a, "alarmKey");
            var state = S(a, "state"); var severity = S(a, "severity"); var stamp = DirectorProbeEvaluation.Require(a, "token");
            if (!a.TryGetProperty("raisedDuringSuppression", out stamp)) throw new FormatException("missing field raisedDuringSuppression");
            var f = new Dictionary<string, string> { ["alarmToken"] = id, ["raisedDuringSuppression"] = B(stamp.ValueKind == JsonValueKind.String) };
            if (stamp.ValueKind == JsonValueKind.String && stamp.GetString() != "restricted") f["suppressionWindowToken"] = stamp.GetString();
            alarmHistory[id + "/" + raised] = a.Clone();
            Emit("alarm", id, device, key, raised, "RAISED", at, f);
            if (state == "CLEARED") Emit("alarm", id, device, key, raised, "CLEARED", T(a, "clearedTime"), f);
            else if (state != "ACTIVE") throw new FormatException("unknown alarm state " + state);
        }
        void Window(JsonElement w)
        {
            if (S(w, "kind") != "ONE_SHOT") throw new FormatException("lamp service requires ONE_SHOT");
            var id = S(w, "token"); var start = T(w, "startTime"); var end = T(w, Optional(w, "endedAt") != null ? "endedAt" : "endTime");
            foreach (var target in w.GetProperty("targets").EnumerateArray()) {
                if (S(target, "type") != "DEVICE") continue;
                var d = Device(S(target, "token"));
                foreach (var key in w.GetProperty("alarmKeys").EnumerateArray().Select(v => v.GetString())) {
                    Emit("window", id, d, key, "", "ACTIVE", start);
                    if (S(w, "status") == "ENDED" || input.Now >= end) Emit("window", id, d, key, "", "ENDED", end);
                }
            }
        }
        public static bool NotificationInCycle(DateTimeOffset attempted, DateTimeOffset raised, DateTimeOffset? nextRaised)
            => attempted >= raised && (!nextRaised.HasValue || attempted < nextRaised.Value);
        JsonElement CycleAt(string token, DateTimeOffset at)
        {
            var candidates = alarmHistory.Values.Where(a => S(a, "token") == token && T(a, "raisedTime") <= at).OrderByDescending(a => T(a, "raisedTime")).ToArray();
            if (candidates.Length == 0) return default;
            var device = S(candidates[0], "originatorToken");
            // A cached lifecycle cannot exclude a newer raise until this device's complete read covers the record.
            if (!complete.Any(r => r.Name == "alarms" && r.AsOf >= at &&
                (r.Scope == device || Rows("alarms").Any(a => a.Read == r && S(a.Value, "token") == token)))) return default;
            return candidates[0];
        }
        void Notification(JsonElement n)
        {
            var id = S(n, "id"); var token = S(n, "alarmToken"); var at = T(n, "attemptedAt");
            var alarm = CycleAt(token, at);
            if (alarm.ValueKind == JsonValueKind.Undefined) return; // another cycle/run, outside the queried window
            var key = S(alarm, "alarmKey"); var f = new Dictionary<string, string> { ["alarmToken"] = token, ["trigger"] = S(n, "trigger") };
            var policy = Optional(n, "policyToken"); if (key == "lamp-flicker" && policy == null) throw new FormatException("missing notification policyToken");
            if (policy != null) f["policy"] = policy;
            var stamp = Optional(alarm, "raisedDuringSuppression");
            var window = Rows("windows").Select(r => r.Value).FirstOrDefault(w => S(w, "token") == stamp);
            if (window.ValueKind != JsonValueKind.Undefined) {
                var close = T(window, Optional(window, "endedAt") != null ? "endedAt" : "endTime");
                f["afterClose"] = B(at >= close);
            }
            var clear = Optional(alarm, "clearedTime"); f["alarmActive"] = B(clear == null || at < Time(clear));
            Emit("attempt", id, Device(S(alarm, "originatorToken")), key == "light-silent" || key == "charger-offline" ? "absence" : key,
                S(alarm, "raisedTime"), S(n, "status"), at, f, attempt: id);
        }
        public static readonly TimeSpan ReplacementReadingWait = TimeSpan.FromSeconds(60);
        IEnumerable<JsonElement> History(string device) => Rows("measurements").Select(r => r.Value).Where(r => S(r, "deviceToken") == device);
        void Replacement(JsonElement r)
        {
            var id = S(r, "id"); var token = S(r.GetProperty("device"), "token"); var device = Device(token); var at = T(r, "occurredTime");
            var history = History(token).Where(m => S(m, "name") == Metrics.WaterLevel).OrderBy(m => T(m, "occurredTime")).ToArray();
            var before = history.LastOrDefault(m => T(m, "occurredTime") < at); var after = history.FirstOrDefault(m => T(m, "occurredTime") > at);
            var f = new Dictionary<string, string>();
            if (before.ValueKind != JsonValueKind.Undefined) f["beforeReading"] = S(before, "id");
            if (after.ValueKind != JsonValueKind.Undefined) f["afterReading"] = S(after, "id");
            // The journal entry is a page trigger, and a trigger is captured once: emitted before the first reading after the swap
            // has been read, its claims could never join that reading. Hold it until the reading is known, or for a minute at most
            // (then it is emitted without one, and the claims that need the reading stay unmet).
            if (!f.ContainsKey("afterReading") && input.Now - at < ReplacementReadingWait) return;
            Emit("replacement", id, device, "", S(r, "occurredTime"), "RECORDED", at, f,
                new Dictionary<string, string[]> { ["disabledCredentials"] = r.GetProperty("retiredCredentialTokens").EnumerateArray().Select(v => v.GetString()).ToArray() });
        }
        Dictionary<string, string> SnapshotFields(DirectorRead read, Dictionary<string, string> fields = null)
        {
            var f = fields ?? new Dictionary<string, string>(); f["asOf"] = N(Wall(read.AsOf.Value)); return f;
        }
        void Credential(Row row)
        {
            var c = row.Value;
            var id = S(c, "token"); var device = Device(S(c.GetProperty("device"), "token"));
            var enabled = DirectorProbeEvaluation.Require(c, "enabled").GetBoolean();
            foreach (var r in Rows("replacements").Select(r => r.Value).Where(r => S(r.GetProperty("device"), "token") == S(c.GetProperty("device"), "token") && r.GetProperty("retiredCredentialTokens").EnumerateArray().Any(t => t.GetString() == id)))
                Emit("credential", id, device, "", S(r, "occurredTime"), enabled ? "ENABLED" : "DISABLED", input.Now,
                    SnapshotFields(row.Read, new Dictionary<string, string> { ["enabled"] = B(enabled), ["replacementToken"] = S(r, "id") }));
        }
        void Command(JsonElement c)
        {
            var id = S(c, "token"); var device = Device(S(c, "deviceToken")); var key = S(c, "name"); var f = new Dictionary<string, string>(); string alarmToken = null;
            // The served Command has no alarm foreign key. Only an explicit platform metadata join is evidence of one.
            var metadata = Optional(c, "metadata");
            if (metadata != null) { using var m = JsonDocument.Parse(metadata); alarmToken = Optional(m.RootElement, "alarmToken"); }
            var payload = Optional(c, "payload");
            if (payload != null) { using var p = JsonDocument.Parse(payload); if (p.RootElement.TryGetProperty("max_kw", out var max)) f["max_kw"] = StrictJson.Scalar(max); }
            foreach (var state in new[] { "QUEUED", "SENT", S(c, "status") }.Distinct()) {
                var field = state == "QUEUED" ? "queuedTime" : state == "SENT" ? "sentTime" : "respondedTime";
                var at = Optional(c, field); if (at != null) {
                    var cycle = "";
                    if (alarmToken != null) {
                        var alarm = CycleAt(alarmToken, Time(at)); if (alarm.ValueKind == JsonValueKind.Undefined) continue;
                        f["alarmToken"] = alarmToken; cycle = S(alarm, "raisedTime");
                    }
                    Emit("command", id, device, key, cycle, state, Time(at), f, command: id);
                }
                else if (state == S(c, "status")) problems.Add("commands: missing " + field + " for " + id);
            }
        }
        void Ota(JsonElement rollout)
        {
            var id = S(rollout, "token"); var cycle = S(rollout, "startedAt"); var at = Time(cycle); var artifact = S(rollout.GetProperty("artifact"), "token");
            Dictionary<string, string> Fields() => new Dictionary<string, string> { ["artifactToken"] = artifact, ["rolloutToken"] = id };
            var f = Fields(); f["maxInFlight"] = N(rollout.GetProperty("maxInFlight").GetInt32());
            var state = S(rollout, "state");
            var terminal = state == "COMPLETED" || state == "ABORTED";
            if (terminal) Emit("rollout", id, "", "", cycle, "TERMINAL", T(rollout, "finishedAt"), f);
            if (state != "COMPLETED" && state != "ABORTED")
                Emit("rollout", id, "", "", cycle, state, state == "RUNNING" ? at : T(rollout, "stateChangedAt"), f);
            foreach (var row in Rows("artifact").Where(r => S(r.Value, "token") == artifact)) {
                var a = row.Value;
                var sig = a.GetProperty("signatures").EnumerateArray().FirstOrDefault(s => s.GetProperty("countsTowardTrust").GetBoolean() && S(s.GetProperty("signer"), "state") == "ACTIVE" &&
                    Rows("signer").Any(r => S(r.Value, "token") == S(s.GetProperty("signer"), "token") && S(r.Value, "keyId") == S(s.GetProperty("signer"), "keyId") && S(r.Value, "state") == "ACTIVE"));
                var af = Fields(); if (sig.ValueKind != JsonValueKind.Undefined) af["signer"] = S(sig.GetProperty("signer"), "token");
                // SIGNED is the platform's verified trust status; UpdateState.VERIFIED is integrity-only and is never used here.
                Emit("artifact", artifact, "", "", cycle, S(a, "trustStatus") == "SIGNED" && af.ContainsKey("signer") ? "VERIFIED" : "UNVERIFIED", input.Now, SnapshotFields(row.Read, af));
            }
            var targets = Rows("targets").Where(t => t.Scope == id).Select(t => t.Value).ToArray(); int refused = 0;
            var finished = terminal ? T(rollout, "finishedAt") : input.Now;
            bool countsComplete = complete.Any(r => r.Name == "targets" && r.Scope == id && r.AsOf >= finished &&
                r.Data.Value.GetProperty(DirectorProbeEvaluation.Roots["targets"]).ValueKind != JsonValueKind.Null) &&
                targets.Length == rollout.GetProperty("progress").GetProperty("total").GetInt32() && targets.Select(t => S(t, "deviceToken")).Distinct().Count() == targets.Length;
            foreach (var t in targets) {
                var token = S(t, "deviceToken"); var assignment = Optional(t, "assignmentToken");
                if (S(t, "outcome") == "UNKNOWN" || S(t, "outcome") == "IN_FLIGHT") countsComplete = false;
                var a = Rows("assignments").Select(r => r.Value).FirstOrDefault(a => S(a, "token") == assignment && S(a, "deviceToken") == token && S(a.GetProperty("artifact"), "token") == artifact);
                string attempt = "";
                if (a.ValueKind != JsonValueKind.Undefined) {
                    if (!Rows("assignments").Any(r => r.Value.Equals(a) && r.Read.AsOf >= finished)) countsComplete = false;
                    var p = a.GetProperty("attempt"); attempt = S(p, "token"); var pf = Fields(); var ps = S(p, "state");
                    if (p.TryGetProperty("failure", out var failure) && failure.ValueKind != JsonValueKind.Null) {
                        pf["code"] = S(failure, "code"); pf["platform"] = B(failure.GetProperty("platform").GetBoolean());
                        if (ps == "FAILED" && pf["code"] == "VERIFICATION_FAILED" && pf["platform"] == "false") refused++;
                    }
                    else if (ps == "FAILED") countsComplete = false;
                    if (S(t, "outcome") == "FAILED" && ps != "FAILED") countsComplete = false;
                    Emit("attempt", attempt, Device(token), "", cycle, ps, T(p, "lastReportAt"), pf, attempt: attempt);
                }
                else if (assignment != null || S(t, "outcome") == "FAILED" || S(t, "outcome") == "SUCCEEDED") countsComplete = false;
                var targetRead = Rows("targets").First(r => r.Scope == id && S(r.Value, "deviceToken") == token).Read;
                Emit("target", id + "/" + token, Device(token), "", cycle, S(t, "outcome"), input.Now, SnapshotFields(targetRead, Fields()), attempt: attempt);
            }
            if (terminal) {
                if (!countsComplete) { current.Add("rollout counts: complete child reads covering terminal state and resolved attempts required"); Gap("targets", lastSim, input.Sim); return; }
                f = Fields(); f["maxInFlight"] = N(rollout.GetProperty("maxInFlight").GetInt32());
                f["updated"] = N(rollout.GetProperty("progress").GetProperty("succeeded").GetInt32()); f["refused"] = N(refused);
                Emit("rollout", id, "", "", cycle, "COUNTS", input.Now, SnapshotFields(complete.First(r => r.Name == "targets" && r.Scope == id), f));
            }
        }
        void Membership(string quay, JsonElement[] members, DirectorRead read)
        {
            var ids = members.Select(m => Device(S(m, "token"))).ToArray();
            foreach (var a in alarmHistory.Values.Where(a => S(a, "alarmKey") == "light-silent" || S(a, "alarmKey") == "charger-offline")) {
                if (quay != "hp-east-quay") continue;
                var expected = ids.Where(id => { var d = layout.Devices.First(x => x.Token == id); return d.Kind == DeviceKind.Light || d.Kind == DeviceKind.Charger; }).ToArray();
                Emit("membership", "east-absence", "", "", S(a, "raisedTime"), "", input.Now, SnapshotFields(read), sets: new Dictionary<string, string[]> { ["tokens"] = expected });
            }
            Emit("membership", quay, "", quay, "", "SNAPSHOT", input.Now, SnapshotFields(read), sets: new Dictionary<string, string[]> { ["tokens"] = ids });
        }
        void Measurements()
        {
            foreach (var r in rows.Where(r => r.Name == "measurements" || r.Name == "streamMeasurements").Select(r => r.Value).OrderBy(r => Optional(r, "occurredTime"))) Safe("measurements", () => {
                MeasurementWorkUnits++;
                var token = S(r, "deviceToken"); var device = Device(token); var key = S(r, "name"); var at = T(r, "occurredTime");
                var value = DirectorProbeEvaluation.Require(r, "value").GetDouble(); var f = new Dictionary<string, string> { ["value"] = N(value) }; var cycle = "";
                var pk = device + "/" + key;
                var readingKey = S(r, "id");
                if (trends.TryGetValue(readingKey, out var trend)) foreach (var p in trend) f[p.Key] = p.Value;
                else if (previous.TryGetValue(pk, out var old) && at > old.OccurredAt) {
                    f["rising"] = B(value > old.Value); f["falling"] = B(value < old.Value);
                    trends[readingKey] = new Dictionary<string, string> { ["rising"] = f["rising"], ["falling"] = f["falling"] };
                }
                if (!previous.ContainsKey(pk) || at > previous[pk].OccurredAt) previous[pk] = new Reading(value, at);
                var alarm = alarmHistory.Values.Where(a => S(a, "originatorToken") == token && T(a, "raisedTime") <= at).OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                if (alarm.ValueKind != JsonValueKind.Undefined) cycle = S(alarm, "raisedTime");
                // A filling-well interval starts with a reported off reading, before its high alarm exists.
                // Retain those earlier readings and attach them to the subsequently observed alarm cycle;
                // otherwise an above-float reading before the alarm could disappear from allBelow's proof.
                if (key == Metrics.WetWell && pumpOffAt.TryGetValue(device, out var off) && at >= off) {
                    var high = alarmHistory.Values.Where(a => S(a, "originatorToken") == token && S(a, "alarmKey") == "wet-well-high" && T(a, "raisedTime") >= off && T(a, "raisedTime") <= input.Now)
                        .OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                    if (high.ValueKind != JsonValueKind.Undefined) cycle = S(high, "raisedTime");
                }
                var replacement = Rows("replacements").Select(x => x.Value).Where(x => S(x.GetProperty("device"), "token") == token).OrderByDescending(x => T(x, "occurredTime")).FirstOrDefault();
                if (replacement.ValueKind != JsonValueKind.Undefined) { cycle = S(replacement, "occurredTime"); f["replacementToken"] = S(replacement, "id"); f["afterReplacement"] = B(at > T(replacement, "occurredTime")); }
                Emit("measure", S(r, "id"), device, key, cycle, "", at, f);
                foreach (var assignment in Rows("assignments").Select(x => x.Value).Where(a => S(a, "deviceToken") == token)) {
                    var p = assignment.GetProperty("attempt"); if (S(p, "state") != "FAILED" || at <= T(p, "lastReportAt") || input.Now - at > TimeSpan.FromSeconds(60)) continue;
                    foreach (var rollout in Rows("rollout").Select(x => x.Value).Where(x => S(x.GetProperty("artifact"), "token") == S(assignment.GetProperty("artifact"), "token") &&
                        Rows("targets").Any(t => t.Scope == S(x, "token") && Optional(t.Value, "assignmentToken") == S(assignment, "token") && S(t.Value, "deviceToken") == token))) {
                        var af = new Dictionary<string, string>(f) { ["afterFailure"] = "true", ["artifactToken"] = S(assignment.GetProperty("artifact"), "token"), ["rolloutToken"] = S(rollout, "token") };
                        Emit("measure", S(r, "id"), device, key, S(rollout, "startedAt"), "", at, af, attempt: S(p, "token"));
                    }
                }
                foreach (var b in input.Source?.Status?.Backfill ?? Array.Empty<StoryBackfill>()) {
                    if (RunnerDevice(b.Device) != device || at < b.First || at > b.Last || at >= input.Now) continue;
                    // Include unexpected platform times too: filtering by the expected set would falsely prove exactness.
                    Emit("measure", S(r, "id"), device, "backfill", "", "", at,
                        new Dictionary<string, string> { ["value"] = N(value), ["backdated"] = "true", ["readingIdentity"] = ReadingIdentity(device, S(r, "occurredTime")) }, arrivalSim: input.Sim);
                }
            });
        }
        void StreamMeasurements(DirectorReadSnapshot snapshot)
        {
            var fresh = new List<DirectorMeasurement>();
            // IReadOnlyList.Skip enumerated the entire prefix on Unity's runtime.
            for (var i = measurementIndex; i < snapshot.Measurements.Count; i++) fresh.Add(snapshot.Measurements[i]);
            var addedWells = new HashSet<string>();
            foreach (var reading in fresh.OrderBy(m => m.OccurredAt)) {
                var token = reading.DeviceToken; var metric = reading.Name; var device = Device(token);
                if (metric == Metrics.PumpRunning && (!pumpRunning.TryGetValue(device, out var old) || reading.OccurredAt > old.OccurredAt)) {
                    if (reading.Value <= .5 && (!pumpRunning.ContainsKey(device) || old.Value > .5)) pumpOffAt[device] = reading.OccurredAt;
                    pumpRunning[device] = new Reading(reading.Value, reading.OccurredAt);
                }
                var id = token + "/" + metric + "@" + reading.OccurredAt.ToString("O");
                var json = DirectorWire.Json(w => {
                    w.WriteStartObject(); w.WriteString("id", id); w.WriteString("deviceToken", token); w.WriteString("name", metric);
                    w.WriteString("occurredTime", reading.OccurredAt.ToString("O")); w.WriteNumber("value", reading.Value); w.WriteEndObject();
                });
                using var doc = JsonDocument.Parse(json); var record = doc.RootElement.Clone();
                if (metric == Metrics.WetWell) { wellHistory[id] = record; addedWells.Add(id); }
                rows.Add(new Row { Name = "streamMeasurements", Scope = token, Value = record });
            }
            measurementIndex = snapshot.Measurements.Count;
            // Wells already emitted need revisiting only when their alarm/off-interval join
            // changes, so an arriving high alarm still gets ALL pre-alarm float evidence.
            var changed = new HashSet<string>();
            foreach (var pump in layout.Devices.Where(d => d.Kind == DeviceKind.Pump)) {
                if (input.Source == null || !input.Source.TryToken(pump.ExternalId, out var token)) continue;
                var cycle = string.Join("|", alarmHistory.Where(a => S(a.Value, "originatorToken") == token).Select(a => a.Key).OrderBy(k => k, StringComparer.Ordinal))
                    + "/" + (pumpOffAt.TryGetValue(pump.Token, out var off) ? off.ToString("O") : "");
                if (!wellCycles.TryGetValue(token, out var old) || old != cycle) { wellCycles[token] = cycle; changed.Add(token); }
            }
            if (changed.Count > 0) foreach (var p in wellHistory) {
                var token = S(p.Value, "deviceToken");
                if (changed.Contains(token) && !addedWells.Contains(p.Key)) rows.Add(new Row { Name = "streamMeasurements", Scope = token, Value = p.Value });
            }
        }
        void Binding()
        {
            var fresh = layout.Devices.Count(d => HarborObservedModel.MetricsFor(d.Kind).Any(m => input.Source != null && input.Source.TryGet(d.ExternalId, m, out var r) && r.OccurredAt <= input.Now && input.Now - r.OccurredAt <= TimeSpan.FromSeconds(60)));
            Emit("binding", input.Run, "", "", "", "READY", input.Now, new Dictionary<string, string> { ["freshDevices"] = N(fresh) });
        }
        void Chips()
        {
            if (input.Frame?.Pumps == null) return;
            foreach (var pump in input.Frame.Pumps) Safe("pump", () => {
                var p = pump.Value; if (!p.Running.HasValue) return;
                var at = p.TransitionAt ?? p.Running.Value.OccurredAt;
                var device = RunnerDevice(pump.Key);
                var a = alarmHistory.Values.Where(a => Device(S(a, "originatorToken")) == device && S(a, "alarmKey") == "wet-well-high" && T(a, "raisedTime") <= at).OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                var f = new Dictionary<string, string> { ["runningAt"] = N(Wall(at)) };
                var cycle = "";
                if (a.ValueKind != JsonValueKind.Undefined) { f["alarmToken"] = S(a, "token"); cycle = S(a, "raisedTime"); }
                Emit("chip", device + "@" + at.ToString("O"), device, "", cycle, p.StartKind == PumpStartKind.Platform ? "PLATFORM" : p.StartKind == PumpStartKind.FloatConsistent ? "FLOAT-CONSISTENT" : "UNCONFIRMED", at, f);
            });
        }
        void Runner()
        {
            var status = input.Source?.Status; if (status?.StartedAt == null) return;
            var metadata = new Dictionary<string, string>();
            if (status.LayoutDigest != null) metadata["layoutDigest"] = status.LayoutDigest;
            Emit("ledger", "run/" + input.Run, "", "run", "", "", status.StartedAt.Value, metadata,
                new Dictionary<string, string[]> { ["defects"] = status.Defects.ToArray() }, EvidenceSource.Scenario);
            foreach (var job in status.Jobs.Where(j => j.WindowOpens.HasValue && j.WindowCloses.HasValue && !string.IsNullOrEmpty(j.Device))) {
                var device = RunnerDevice(job.Device); var id = job.Window ?? job.Campaign;
                Emit("window", id, device, "", "", "ACTIVE", job.WindowOpens.Value, source: EvidenceSource.Scenario);
                Emit("window", id, device, "", "", "ENDED", job.WindowCloses.Value, source: EvidenceSource.Scenario);
            }
            foreach (var beat in status.Beats.Where(b => b.Name == "feeder-outage")) {
                var day = Math.Floor(input.Sim / 24); var from = status.ToWallTime((day * 24 + beat.From) * 3600); var to = status.ToWallTime((day * 24 + beat.To) * 3600);
                Emit("window", "feeder-outage/" + day, "", "feeder-outage", "", "ACTIVE", from, source: EvidenceSource.Scenario);
                Emit("window", "feeder-outage/" + day, "", "feeder-outage", "", "ENDED", to, source: EvidenceSource.Scenario);
            }
            var replacement = status.Replacement;
            if (replacement != null) Emit("replacement", replacement.Device + "@" + replacement.At.ToString("O"), RunnerDevice(replacement.Device), "", "", "", replacement.At, source: EvidenceSource.Scenario);
            foreach (var group in status.Backfill.GroupBy(b => b.Day)) {
                var at = status.ToWallTime((group.Key * 24 + layout.Beat("feeder-outage").To) * 3600);
                Emit("ledger", "backfill/" + group.Key, "", "backfill", "", "", at, sets: new Dictionary<string, string[]> { ["times"] = group.SelectMany(b => b.Times.Select(t => ReadingIdentity(RunnerDevice(b.Device), t))).ToArray() }, source: EvidenceSource.Scenario);
            }
            foreach (var drain in status.RestoreDrains) {
                var at = drain.FirstRefusalAt ?? drain.CaughtUpAt;
                if (at.HasValue) Emit("ledger", "restore/" + drain.Day, "", "restore", "", "", at.Value, new Dictionary<string, string> { ["value"] = N(drain.Refused429) }, source: EvidenceSource.DeviceSide);
                else if (drain.Refused429 > 0) problems.Add("runner: restore refusal count has no instant");
            }
        }
        void Preview()
        {
            foreach (var d in layout.Devices) foreach (var metric in HarborObservedModel.MetricsFor(d.Kind)) if (input.Source.TryGet(d.ExternalId, metric, out var r))
                Emit("measure", "preview:" + d.ExternalId + "/" + metric + "@" + r.OccurredAt.ToString("O"), d.ExternalId, metric, "", "", r.OccurredAt,
                    new Dictionary<string, string> { ["value"] = N(r.Value) }, source: EvidenceSource.Scenario);
        }
    }
}
