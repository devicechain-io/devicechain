// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Sdk;
using UnityEngine.Networking;

namespace DeviceChain.Harborpulse.Live
{
    /// <summary>Read-only supplementary polls. One request in flight per area, with failures retained beside aged evidence.</summary>
    public sealed class HarborPlatformReads : IDisposable
    {
        public const string RolloutQuery = HarborReadQueries.Rollout;
        public const string SuppressionQuery = HarborReadQueries.Suppressions;
        public const string AttributeQuery = HarborReadQueries.Attributes;
        readonly RunnerConfig config;
        readonly TokenBroker broker;
        readonly HarborLayout layout;
        readonly Func<string, string> boundToken;
        readonly QueryFn management;
        readonly QueryFn commandDelivery;
        readonly PumpCommandPoll commandPoll = new PumpCommandPoll();
        readonly List<LayoutDevice> boundPumps = new List<LayoutDevice>();
        readonly CancellationTokenSource stop = new CancellationTokenSource();
        readonly Dictionary<string, Reading> attributes = new Dictionary<string, Reading>();
        DateTimeOffset otaNext, managementNext;
        bool otaBusy, managementBusy;
        string otaProblem, managementProblem, commandProblem, rolloutToken;
        public RolloutEvidence Rollout { get; private set; }
        public IReadOnlyList<SuppressionEvidence> Suppressions { get; private set; } = Array.Empty<SuppressionEvidence>();
        public IReadOnlyList<HarborCommand> Commands { get; private set; } = Array.Empty<HarborCommand>();
        public CommandCoverage CommandCoverage(DateTimeOffset now) => commandPoll.Coverage(now);
        public string Problem
        {
            get { var problems = new List<string>(Problems()); return problems.Count == 0 ? null : string.Join("; ", problems); }
        }
        IEnumerable<string> Problems()
        {
            if (otaProblem != null) yield return otaProblem;
            if (managementProblem != null) yield return managementProblem;
            if (commandProblem != null) yield return commandProblem;
        }
        public HarborPlatformReads(RunnerConfig config, TokenBroker broker, HarborLayout layout, Func<string, string> boundToken)
        {
            this.config = config; this.broker = broker; this.layout = layout; this.boundToken = boundToken;
            management = OperatorQueries.Create(config, broker).AsQueryFn();
            commandDelivery = OperatorQueries.Create(config, broker, Area.CommandDelivery).AsQueryFn();
            foreach (var d in layout.Devices) if (d.Kind == DeviceKind.Pump && boundToken(d.ExternalId) != null) boundPumps.Add(d);
            if (boundPumps.Count > 0) HarborReadQueries.RequireReadOnly(HarborReadQueries.CommandsFor(boundPumps.Count));
            HarborReadQueries.RequireReadOnly(RolloutQuery); HarborReadQueries.RequireReadOnly(SuppressionQuery); HarborReadQueries.RequireReadOnly(AttributeQuery);
        }
        public bool TryAttribute(string id, string key, out Reading value) => attributes.TryGetValue(id + ":" + key, out value);
        public void Tick(ScenarioStatus status, DateTimeOffset now)
        {
            if (stop.IsCancellationRequested) return;
            if (boundPumps.Count > 0 && commandPoll.TryBegin(now)) _ = PollCommands();
            var token = status?.Ota?.Rollout;
            if (token != rolloutToken) { rolloutToken = token; Rollout = null; otaNext = DateTimeOffset.MinValue; }
            if (!otaBusy && !string.IsNullOrEmpty(token) && now >= otaNext) { otaBusy = true; otaNext = now.AddSeconds(2); _ = PollOta(token); }
            if (!managementBusy && now >= managementNext) { managementBusy = true; managementNext = now.AddSeconds(5); _ = PollManagement(status); }
        }
        async Task PollCommands()
        {
            try
            {
                var tokens = new List<string>();
                foreach (var d in boundPumps) tokens.Add(boundToken(d.ExternalId));
                var data = await commandDelivery(HarborReadQueries.CommandsFor(tokens.Count), VarsJson.Strings(tokens, i => "p" + i), stop.Token);
                using var doc = JsonDocument.Parse(data);
                var parsed = new List<HarborCommand>();
                var now = DateTimeOffset.UtcNow;
                for (var i = 0; i < tokens.Count; i++)
                {
                    if (doc.RootElement.ValueKind != JsonValueKind.Object || !doc.RootElement.TryGetProperty("p" + i, out var page)) throw new FormatException("pump command page missing");
                    parsed.AddRange(PumpCommandRecords.Parse(page, boundPumps[i].ExternalId, tokens[i], now));
                }
                if (stop.IsCancellationRequested) return;
                Commands = parsed.AsReadOnly(); commandProblem = null; commandPoll.Complete(now);
            }
            catch (OperationCanceledException) { commandPoll.Fail(); }
            catch (Exception e) { commandPoll.Fail(); if (!stop.IsCancellationRequested) commandProblem = "command-delivery read failed: " + e.GetType().Name; }
        }
        async Task PollOta(string token)
        {
            try
            {
                // The harbor fleet has six chargers. Ask for a bounded page and report truncation rather than hide it.
                var vars = Write(w => { w.WriteString("token", token); w.WriteStartObject("criteria"); w.WriteNumber("pageNumber", 1); w.WriteNumber("pageSize", 1000); w.WriteEndObject(); });
                var data = await UpdateQuery(RolloutQuery, vars, stop.Token);
                var parsed = RolloutEvidence.Parse(data, DateTimeOffset.UtcNow);
                if (stop.IsCancellationRequested || token != rolloutToken) return;
                Rollout = parsed;
                otaProblem = parsed == null ? "platform rollout not reported" : parsed.TargetsTruncated ? "platform rollout target page truncated" : null;
            }
            catch (OperationCanceledException) { }
            catch (Exception e) { if (!stop.IsCancellationRequested) otaProblem = "update-management read failed: " + e.GetType().Name; }
            finally { otaBusy = false; }
        }
        async Task PollManagement(ScenarioStatus status)
        {
            try
            {
                var windows = new HashSet<string>();
                foreach (var j in status?.Jobs ?? Array.Empty<StoryJob>()) if (j.Name == "lamp-service" && !string.IsNullOrEmpty(j.Window)) windows.Add(j.Window);
                if (windows.Count > 0)
                {
                    var data = await management(SuppressionQuery, VarsJson.StringList("tokens", windows), stop.Token);
                    var parsed = SuppressionEvidence.Parse(data, DateTimeOffset.UtcNow);
                    if (stop.IsCancellationRequested) return;
                    Suppressions = parsed;
                }
                foreach (var d in layout.Devices)
                {
                    if (d.Kind != DeviceKind.Pump) continue;
                    var token = boundToken(d.ExternalId);
                    if (token == null) continue;
                    var vars = Write(w => { w.WriteStartObject("criteria"); w.WriteNumber("pageNumber", 1); w.WriteNumber("pageSize", 10); w.WriteString("entityType", "device");
                        w.WriteString("entity", token); w.WriteString("scope", "SHARED"); w.WriteStartArray("attrKeys"); w.WriteStringValue("well_alarm_m"); w.WriteEndArray(); w.WriteEndObject(); });
                    var data = await management(AttributeQuery, vars, stop.Token);
                    var value = AttributeEvidence.ParseWellAlarm(data);
                    if (stop.IsCancellationRequested) return;
                    if (value.HasValue) attributes[d.ExternalId + ":well_alarm_m"] = value.Value;
                    else attributes.Remove(d.ExternalId + ":well_alarm_m");
                }
                managementProblem = null;
            }
            catch (OperationCanceledException) { }
            catch (Exception e) { if (!stop.IsCancellationRequested) managementProblem = "device-management read failed: " + e.GetType().Name; }
            finally { managementBusy = false; }
        }
        async Task<string> UpdateQuery(string query, string variables, CancellationToken ct)
        {
            HarborReadQueries.RequireReadOnly(query);
            for (var attempt = 0; ; attempt++)
            {
                ct.ThrowIfCancellationRequested();
                var used = await broker.Get(ct);
                var body = Write(w => { w.WriteString("query", query); w.WritePropertyName("variables"); using var doc = JsonDocument.Parse(variables); doc.RootElement.WriteTo(w); });
                using var req = new UnityWebRequest(config.ApiOrigin.TrimEnd('/') + "/api/update-management/graphql", "POST");
                req.uploadHandler = new UploadHandlerRaw(Encoding.UTF8.GetBytes(body)); req.downloadHandler = new DownloadHandlerBuffer(); req.timeout = 5;
                req.SetRequestHeader("Content-Type", "application/json"); req.SetRequestHeader("Authorization", "Bearer " + used);
                var done = new TaskCompletionSource<bool>();
                var op = req.SendWebRequest();
                op.completed += _ => done.TrySetResult(true);
                if (op.isDone) done.TrySetResult(true);
                await done.Task;
                ct.ThrowIfCancellationRequested();
                if (req.responseCode == 401 && attempt == 0) { await broker.NotifyUnauthorized(used); continue; }
                if (req.result != UnityWebRequest.Result.Success) throw new InvalidOperationException("update-management HTTP " + req.responseCode);
                using var response = JsonDocument.Parse(req.downloadHandler.text);
                if (response.RootElement.TryGetProperty("errors", out var errors) && errors.ValueKind == JsonValueKind.Array && errors.GetArrayLength() > 0) throw new FormatException("update-management GraphQL errors");
                return WireData(response.RootElement);
            }
        }
        static string WireData(JsonElement response)
        {
            if (!response.TryGetProperty("data", out var data) || data.ValueKind != JsonValueKind.Object) throw new FormatException("update-management data missing");
            return data.GetRawText();
        }
        static string Write(Action<Utf8JsonWriter> body)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms)) { w.WriteStartObject(); body(w); w.WriteEndObject(); }
            return Encoding.UTF8.GetString(ms.ToArray());
        }
        // No Abort: see DirectorReads.Dispose. The read in flight ends on its own 5 s timeout and its result is dropped.
        public void Dispose() { stop.Cancel(); }
    }
}
