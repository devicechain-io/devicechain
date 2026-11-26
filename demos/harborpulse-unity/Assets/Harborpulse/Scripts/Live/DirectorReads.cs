// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using UnityEngine.Networking;

namespace DeviceChain.Harborpulse.Live
{
    /// <summary>Operator-only observer reads. One worker per read, bounded pages, immutable publication on the Unity context.</summary>
    public sealed class DirectorReads : IDisposable
    {
        sealed class ReadJob
        {
            public string Name, Scope, Variables;
            public DirectorRead Result;
            public bool Busy;
            public DateTimeOffset Next;
        }
        static readonly Dictionary<string, string> Documents = new Dictionary<string, string> {
            ["alarms"] = HarborReadQueries.DirectorAlarms, ["suppressionStatus"] = HarborReadQueries.AlarmSuppressionStatus,
            ["windows"] = HarborReadQueries.Suppressions, ["notifications"] = HarborReadQueries.NotificationAttempts,
            ["replacements"] = HarborReadQueries.Replacements, ["credentials"] = HarborReadQueries.Credentials,
            ["artifact"] = HarborReadQueries.Artifact, ["signer"] = HarborReadQueries.Signer, ["rollout"] = HarborReadQueries.DirectorRollout,
            ["targets"] = HarborReadQueries.Targets, ["assignments"] = HarborReadQueries.Assignment,
            ["membership"] = HarborReadQueries.Membership, ["measurements"] = HarborReadQueries.MeasurementEvents, ["commands"] = HarborReadQueries.DirectorCommands
        };
        readonly RunnerConfig config;
        readonly TokenBroker broker;
        readonly HarborLayout layout;
        readonly Func<string, string> boundToken;
        readonly ObserverStatus observerStatus;
        readonly bool omitCommandCause;
        string Document(string name) => omitCommandCause && name == "commands" ? HarborReadQueries.DirectorCommandsNoCause : Documents[name];
        readonly CancellationTokenSource stop = new CancellationTokenSource();
        readonly Dictionary<string, ReadJob> jobs = new Dictionary<string, ReadJob>();
        public static int Parallel(string read) => read == "alarms" || read == "notifications" || read == "measurements" || read == "commands" ? 6 : 1;
        readonly HashSet<UnityWebRequest> requests = new HashSet<UnityWebRequest>();
        readonly Dictionary<string, (string state, string severity, DateTimeOffset at)> alarmStates = new Dictionary<string, (string, string, DateTimeOffset)>();
        readonly List<DirectorAlarmChange> alarmChanges = new List<DirectorAlarmChange>();
        readonly DirectorMeasurementTap measurements = new DirectorMeasurementTap();
        public DirectorReadSnapshot Snapshot { get; private set; }
        /// <summary>The shared observer's main-thread tap retains actual stream transition times before its latest-state reduction.</summary>
        public void Watch(ObserverItem item)
        {
            if (item is MeasurementItem m) measurements.Watch(new DirectorMeasurement(m.DeviceToken, m.Name, m.Value, m.OccurredAt));
            if (item is AlarmSnapshotItem snapshot) {
                foreach (var a in snapshot.Alarms)
                    if (!alarmStates.TryGetValue(a.Alarm.Token, out var old) || a.Alarm.OccurredAt >= old.at)
                        alarmStates[a.Alarm.Token] = (a.Alarm.State, a.Alarm.Severity, a.Alarm.OccurredAt);
            }
            else if (item is AlarmItem a) {
                var next = a.Alarm;
                if (alarmStates.TryGetValue(next.Token, out var held) && next.OccurredAt < held.at) return;
                if (alarmStates.TryGetValue(next.Token, out var old) && old.state == "ACTIVE" && next.State == "ACTIVE" && Severity(next.Severity) > Severity(old.severity))
                    alarmChanges.Add(new DirectorAlarmChange(next.Token, a.DeviceToken, next.AlarmKey, next.OccurredAt));
                alarmStates[next.Token] = (next.State, next.Severity, next.OccurredAt);
            }
        }
        static int Severity(string value) => Array.IndexOf(new[] { "WARNING", "MINOR", "MAJOR", "CRITICAL" }, value);

        /// <summary><paramref name="omitCommandCause"/> is the CONTROL of -hp-control-no-cause: the command read asks for no cause, so no command can be tied to an alarm.</summary>
        public DirectorReads(RunnerConfig config, TokenBroker broker, HarborLayout layout, Func<string, string> boundToken, ObserverStatus observerStatus = null, bool omitCommandCause = false)
        {
            this.config = config; this.broker = broker; this.layout = layout; this.boundToken = boundToken;
            this.observerStatus = observerStatus; this.omitCommandCause = omitCommandCause;
            HarborReadQueries.RequireReadOnly(HarborReadQueries.DirectorCommandsNoCause);
            foreach (var d in Documents) {
                HarborReadQueries.RequireReadOnly(d.Value);
                // Nullable token lookups and empty searches still execute authorization and field validation.
                var vars = DirectorWire.Json(w => {
                    w.WriteStartObject();
                    if (new[] { "credentials", "windows", "suppressionStatus" }.Contains(d.Key)) { w.WriteStartArray("tokens"); w.WriteEndArray(); }
                    else if (new[] { "artifact", "signer", "rollout", "assignments" }.Contains(d.Key)) w.WriteString("token", "__harborpulse_probe_absent__");
                    else {
                        if (d.Key == "targets") w.WriteString("token", "__harborpulse_probe_absent__");
                        w.WriteStartObject("c"); w.WriteNumber("pageNumber", 1); w.WriteNumber("pageSize", 1000);
                        if (d.Key == "membership") { w.WriteString("targetType", "area"); w.WriteString("target", "__harborpulse_probe_absent__"); }
                        if (d.Key == "notifications") w.WriteString("alarmToken", "__harborpulse_probe_absent__");
                        if (d.Key == "alarms") { w.WriteString("originatorType", "device"); w.WriteString("originatorToken", "__harborpulse_probe_absent__"); }
                        if (d.Key == "measurements" || d.Key == "commands") w.WriteString("deviceToken", "__harborpulse_probe_absent__");
                        if (d.Key == "replacements") w.WriteString("device", "__harborpulse_probe_absent__");
                        w.WriteEndObject();
                    }
                    w.WriteEndObject();
                });
                Add(d.Key, "__probe", vars);
            }
            // Until an alarm exists the known alarm set is empty. Keep polling its empty search too;
            // alarm coverage is independently required before a negative notification claim can hold.
            Add("notifications", "known-alarms-empty", jobs["notifications/__probe"].Variables);
            Publish();
        }
        void Add(string name, string scope, string variables)
        {
            var key = name + "/" + scope;
            if (jobs.TryGetValue(key, out var old)) { old.Variables = variables; return; }
            readsChanged = true;
            jobs.Add(key, new ReadJob { Name = name, Scope = scope, Variables = variables, Result = new DirectorRead(name, scope) });
        }
        static string Vars(string token) => DirectorWire.Json(w => { w.WriteStartObject(); w.WriteString("token", token); w.WriteEndObject(); });
        static string Tokens(IEnumerable<string> tokens) => VarsJson.StringList("tokens", tokens);
        static string Criteria(Action<Utf8JsonWriter> fields, string token = null) => DirectorWire.Json(w => {
            w.WriteStartObject(); if (token != null) w.WriteString("token", token);
            w.WriteStartObject("c"); w.WriteNumber("pageNumber", 1); w.WriteNumber("pageSize", 1000); fields(w); w.WriteEndObject(); w.WriteEndObject();
        });
        IEnumerable<JsonElement> Rows(string name)
        {
            // Materialized: a caller may Add a job (an assignment found on a target row) while it walks these rows.
            foreach (var j in jobs.Values.Where(j => j.Name == name && j.Scope != "__probe" && j.Result.Data.HasValue && j.Result.Error == null).ToArray())
                foreach (var r in DirectorProbeEvaluation.Records(name, j.Result.Data.Value)) yield return r;
        }
        readonly ReadPlanGate gate = new ReadPlanGate();
        /// <summary>How often the reads were planned and dispatched (see <see cref="ReadPlanGate"/>): not once a frame.</summary>
        public long Plans => gate.Plans;
        public long Dispatches => gate.Dispatches;
        public void Tick(ScenarioStatus status, DateTimeOffset now)
        {
            if (stop.IsCancellationRequested) return;
            // Planning and dispatch cost more with every alarm the day raises. Neither depends on anything that changes between
            // frames, so each runs when its inputs changed (ReadPlanGate), not every frame.
            if (gate.ShouldPlan(status, now)) Plan(status);
            if (gate.ShouldDispatch(now)) Dispatch(now);
            Publish();
        }
        void Plan(ScenarioStatus status)
        {
            foreach (var d in layout.Devices) {
                var token = boundToken(d.ExternalId); if (token == null) continue;
                // U1 supplies current readings. History is needed for replacement and the restore burst only.
                if (d.Token == "hp-gauge-north-3" || d.Token == "hp-charger-east-1" || d.Token == "hp-charger-east-2")
                    Add("measurements", token, Criteria(w => {
                        w.WriteString("deviceToken", token);
                        if (status?.StartedAt != null) {
                            var from = d.Token == "hp-gauge-north-3" ? 12.4 : layout.Beat("feeder-outage").From;
                            var to = d.Token == "hp-gauge-north-3" ? 12.8 : layout.Beat("feeder-outage").To;
                            w.WriteString("startTime", status.ToWallTime(from * 3600).ToString("O"));
                            w.WriteString("endTime", status.ToWallTime(to * 3600).ToString("O"));
                        }
                    }));
                if (d.Kind == DeviceKind.Pump || d.Kind == DeviceKind.Charger) Add("commands", token, Criteria(w => w.WriteString("deviceToken", token)));
            }
            // One fleet read of every alarm active at any point in this run (a few dozen rows a day) instead of one read per
            // originator: per-device polling took a minute and a half to see a new alarm on a busy frame.
            if (status?.StartedAt != null) Add("alarms", "run", Criteria(w => w.WriteString("startTime", status.StartedAt.Value.ToString("O"))));
            foreach (var quay in layout.Devices.Select(d => d.Quay).Distinct())
                Add("membership", quay, Criteria(w => { w.WriteString("targetType", "area"); w.WriteString("target", quay); }));
            var replacementId = layout.Devices.FirstOrDefault(d => d.Token == status?.Replacement?.Device || d.ExternalId == status?.Replacement?.Device)?.ExternalId;
            // Read the logical gauge's journal from bind, before the runner declares completion.
            var gaugeToken = boundToken(replacementId ?? layout.Devices.First(d => d.Token == "hp-gauge-north-3").ExternalId);
            if (gaugeToken != null) Add("replacements", gaugeToken, Criteria(w => w.WriteString("device", gaugeToken)));
            var alarms = Rows("alarms").Select(a => a.GetProperty("token").GetString()).Distinct().ToArray();
            foreach (var alarm in alarms) Add("notifications", alarm, Criteria(w => { w.WriteString("alarmToken", alarm); if (status?.StartedAt != null) w.WriteString("since", status.StartedAt.Value.ToString("O")); }));
            Add("suppressionStatus", "fleet", Tokens(alarms));
            Add("windows", "jobs", Tokens((status?.Jobs ?? Array.Empty<StoryJob>()).Where(j => !string.IsNullOrEmpty(j.Window)).Select(j => j.Window)));
            var retired = Rows("replacements").SelectMany(r => r.GetProperty("retiredCredentialTokens").EnumerateArray().Select(v => v.GetString())).Distinct().ToArray();
            Add("credentials", "retired", Tokens(retired));
            var ota = status?.Ota;
            if (!string.IsNullOrEmpty(ota?.Artifact)) Add("artifact", ota.Artifact, Vars(ota.Artifact));
            if (!string.IsNullOrEmpty(ota?.Rollout)) {
                Add("rollout", ota.Rollout, Vars(ota.Rollout));
                Add("targets", ota.Rollout, Criteria(_ => { }, ota.Rollout));
            }
            foreach (var artifact in Rows("artifact")) foreach (var sig in artifact.GetProperty("signatures").EnumerateArray()) {
                var token = sig.GetProperty("signer").GetProperty("token").GetString(); Add("signer", token, Vars(token));
            }
            foreach (var target in Rows("targets")) if (target.TryGetProperty("assignmentToken", out var t) && t.ValueKind == JsonValueKind.String) Add("assignments", t.GetString(), Vars(t.GetString()));
        }
        void Dispatch(DateTimeOffset now)
        {
            foreach (var name in Documents.Keys) {
                var probe = jobs[name + "/__probe"];
                if (probe.Result.Probe == DirectorProbe.PROBE_FAILED) continue;
                if (probe.Result.Probe == DirectorProbe.Waiting) { if (!probe.Busy) { probe.Busy = true; _ = Poll(probe); } continue; }
                // Per-device reads (one per alarm originator, notification alarm, history device) run several at once, so one
                // round of alarm reads, one for each device, does not take that many round trips; a fleet-wide read keeps one request in flight.
                var limit = Parallel(name);
                var inFlight = jobs.Values.Count(j => j.Name == name && j.Busy);
                foreach (var due in jobs.Values.Where(j => j.Name == name && j.Scope != "__probe" && j.Next <= now && !j.Busy).OrderBy(j => j.Next).Take(Math.Max(0, limit - inFlight)).ToArray())
                { due.Busy = true; _ = Poll(due); }
            }
            DateTimeOffset? idle = null;
            foreach (var j in jobs.Values) if (!j.Busy && j.Scope != "__probe" && j.Next > now && (!idle.HasValue || j.Next < idle.Value)) idle = j.Next;
            gate.Dispatched(now, idle);
        }
        async Task Poll(ReadJob job)
        {
            var old = job.Result; var probe = old.Probe; JsonElement? data = null; string error = null;
            var requestedAt = DateTimeOffset.UtcNow;
            try {
                var used = await broker.Get(stop.Token);
                var body = DirectorWire.Json(w => { w.WriteStartObject(); w.WriteString("query", Document(job.Name)); w.WritePropertyName("variables"); using var d = JsonDocument.Parse(job.Variables); d.RootElement.WriteTo(w); w.WriteEndObject(); });
                var area = new[] { "artifact", "signer", "rollout", "targets", "assignments" }.Contains(job.Name) ? "update-management" : job.Name == "notifications" ? "notification-management" : job.Name == "measurements" ? "event-management" : job.Name == "commands" ? "command-delivery" : "device-management";
                using var req = new UnityWebRequest(config.ApiOrigin.TrimEnd('/') + "/api/" + area + "/graphql", "POST");
                req.uploadHandler = new UploadHandlerRaw(System.Text.Encoding.UTF8.GetBytes(body)); req.downloadHandler = new DownloadHandlerBuffer(); req.timeout = 5;
                req.SetRequestHeader("Content-Type", "application/json"); req.SetRequestHeader("Authorization", "Bearer " + used);
                requests.Add(req);
                try {
                    var done = new TaskCompletionSource<bool>(); var op = req.SendWebRequest(); op.completed += _ => done.TrySetResult(true); if (op.isDone) done.TrySetResult(true); await done.Task;
                    stop.Token.ThrowIfCancellationRequested();
                    var verdict = DirectorProbeEvaluation.Evaluate(job.Name, req.responseCode, req.downloadHandler.text, out data, out error);
                    if (probe == DirectorProbe.Waiting) probe = verdict;
                    if (req.responseCode == 401) await broker.NotifyUnauthorized(used);
                } finally { requests.Remove(req); }
            }
            catch (OperationCanceledException) { return; }
            catch (Exception ex) { error = ex.Message; if (probe == DirectorProbe.Waiting) probe = DirectorProbe.PROBE_FAILED; }
            finally { job.Busy = false; gate.Answered(); }
            if (stop.IsCancellationRequested) return;
            var now = DateTimeOffset.UtcNow; var spans = old.Coverage.ToList();
            var from = old.LastSuccess ?? now;
            var gap = error != null || old.Error != null || now - from > TimeSpan.FromSeconds(6);
            if (spans.Count > 0 && spans[spans.Count - 1].Gap == gap && spans[spans.Count - 1].To == from) { from = spans[spans.Count - 1].From; spans.RemoveAt(spans.Count - 1); }
            spans.Add(new ReadSpan(from, now, gap));
            job.Result = new DirectorRead(job.Name, job.Scope, probe, error == null ? now : old.LastSuccess, error, data ?? old.Data, spans, error == null ? requestedAt : old.AsOf);
            job.Next = now.AddSeconds(3); gate.Answered(); readsChanged = true; Publish();
        }
        // Called every frame. Copying every read and every alarm change each time is work that grows all day; only a new read or
        // an answer changes the reads, so every other frame reuses them (DirectorReadSnapshot.Observed).
        bool readsChanged = true;
        void Publish()
        {
            var live = observerStatus?.Measurements.IsLive; var mr = observerStatus?.Measurements.Reconnects ?? 0; var ar = observerStatus?.Alarms.Reconnects ?? 0;
            if (Snapshot != null && !readsChanged) { Snapshot = Snapshot.Observed(live, mr, ar, alarmChanges, measurements.Records); return; }
            readsChanged = false;
            Snapshot = new DirectorReadSnapshot(jobs.Values.Select(j => j.Result), live, mr, ar, alarmChanges, measurements.Records);
        }
        // A request is never aborted from here. Abort() on one whose native timeout or connection failure has fired but whose completion
        // has not yet reached the main thread unlinks it twice inside the engine, and the player crashed natively (an access violation
        // in UnityPlayer.dll, the same instruction in both recorded dumps) when a day's end tore the platform down under a dozen
        // in-flight reads. Cancelling the token is enough: each read's own timeout (5 s) ends it, its result is dropped, and the
        // request is disposed only by the completion that owns it.
        public void Dispose() { stop.Cancel(); }
    }
}
