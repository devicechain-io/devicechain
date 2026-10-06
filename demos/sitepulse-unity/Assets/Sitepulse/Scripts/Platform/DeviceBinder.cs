// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>Runs one operator GraphQL query and returns the response's <c>data</c> as JSON text.</summary>
    public delegate Task<string> QueryFn(string query, string variablesJson, CancellationToken ct);

    /// <summary>
    /// Resolves the scene's devices against the platform. <see cref="Classify"/> is pure over the
    /// response text so every outcome is testable without a network; <see cref="BindAsync"/> runs the
    /// one <c>devicesByExternalId</c> query, classifies, then looks up credentials for the devices
    /// that bound. Nothing here guesses: a device the platform does not return is
    /// <see cref="BindOutcome.Missing"/>, and a query that fails fails every device that was waiting
    /// on it with the reason, rather than reading as "missing".
    /// </summary>
    public sealed class DeviceBinder
    {
        public const string ResolveQuery =
            "query Resolve($ids: [String!]!) { devicesByExternalId(externalIds: $ids) { token externalId deviceType { token profile { token activeVersion " +
            "metricDefinitions { metricKey dataType unit } commandDefinitions { commandKey parameterSchema } } } } }";

        readonly QueryFn query;
        readonly SceneContract contract;

        public DeviceBinder(QueryFn query, SceneContract contract)
        {
            this.query = query ?? throw new ArgumentNullException(nameof(query));
            this.contract = contract ?? throw new ArgumentNullException(nameof(contract));
        }

        /// <summary>Resolve and credential every device, recording progress on <paramref name="board"/>.</summary>
        public async Task BindAsync(IReadOnlyList<SceneDevice> devices, ReadinessBoard board, DeviceCredentials credentials, CancellationToken ct)
        {
            var ids = new List<string>();
            foreach (var d in devices) ids.Add(d.ExternalId);

            IReadOnlyList<BindResult> results;
            try
            {
                var data = await query(ResolveQuery, VarsJson.StringList("ids", ids), ct);
                results = Classify(contract, devices, data);
            }
            catch (OperationCanceledException) { throw; }
            catch (Exception e)
            {
                PlatformLog.Error($"resolve query failed: {e.GetType().Name}: {e.Message}");
                foreach (var d in devices) board.FailBind(d.ExternalId, "the platform query failed: " + e.Message);
                return;
            }

            var bound = new List<BindResult>();
            foreach (var r in results)
            {
                board.SetBind(r);
                if (r.IsBound) bound.Add(r);
            }

            if (bound.Count == 0) return;
            try
            {
                var outcomes = await CredentialResolver.ResolveAll(query, bound, credentials, ct);
                foreach (var o in outcomes) board.SetCredential(o);
            }
            catch (OperationCanceledException) { throw; }
            catch (Exception e)
            {
                PlatformLog.Error($"credential query failed: {e.GetType().Name}: {e.Message}");
                foreach (var r in bound) board.FailCredential(r.ExternalId, "the credential query failed: " + e.Message);
            }
        }

        /// <summary>
        /// Classify the <c>data</c> of a <c>devicesByExternalId</c> response, one result per scene
        /// device in the order given.
        /// </summary>
        public static IReadOnlyList<BindResult> Classify(SceneContract contract, IReadOnlyList<SceneDevice> devices, string dataJson)
        {
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty("devicesByExternalId", out var rows) || rows.ValueKind != JsonValueKind.Array)
                throw new FormatException("the response has no devicesByExternalId list");

            // an external ID may legitimately come back more than once: that is Ambiguous, not a bug
            var byId = new Dictionary<string, List<JsonElement>>(StringComparer.Ordinal);
            foreach (var row in rows.EnumerateArray())
            {
                var id = Str(row, "externalId");
                if (id == null) continue;
                if (!byId.TryGetValue(id, out var list)) byId[id] = list = new List<JsonElement>();
                list.Add(row);
            }

            var results = new List<BindResult>(devices.Count);
            foreach (var d in devices)
            {
                byId.TryGetValue(d.ExternalId, out var matches);
                results.Add(ClassifyOne(contract, d, matches));
            }

            return results;
        }

        static BindResult ClassifyOne(SceneContract c, SceneDevice d, List<JsonElement> matches)
        {
            var r = new BindResult { ExternalId = d.ExternalId };
            if (matches == null || matches.Count == 0)
            {
                r.Outcome = BindOutcome.Missing;
                return r;
            }

            if (matches.Count > 1)
            {
                r.Outcome = BindOutcome.Ambiguous;
                r.Count = matches.Count;
                return r;
            }

            var row = matches[0];
            r.Count = 1;
            r.DeviceToken = Str(row, "token");
            var type = Obj(row, "deviceType");
            r.TypeToken = type.HasValue ? Str(type.Value, "token") : null;
            var want = c.TypeTokens[d.Kind];
            if (r.TypeToken != want)
            {
                r.Outcome = BindOutcome.WrongType;
                r.Detail = $"resolves to type {r.TypeToken ?? "(none)"}, the scene needs {want}";
                return r;
            }

            var profile = Obj(type.Value, "profile");
            if (!profile.HasValue)
            {
                r.Outcome = BindOutcome.ProfileMismatch;
                r.Detail = $"device type {r.TypeToken} has no profile";
                return r;
            }

            r.ProfileToken = Str(profile.Value, "token");
            r.ActiveVersion = profile.Value.TryGetProperty("activeVersion", out var av) && av.ValueKind == JsonValueKind.Number && av.TryGetInt32(out var v) ? v : 0;

            var metrics = new Dictionary<string, string>(StringComparer.Ordinal); // metricKey -> dataType
            if (profile.Value.TryGetProperty("metricDefinitions", out var md) && md.ValueKind == JsonValueKind.Array)
                foreach (var m in md.EnumerateArray())
                {
                    var key = Str(m, "metricKey");
                    if (key != null) metrics[key] = Str(m, "dataType") ?? "";
                }

            var commands = new Dictionary<string, string>(StringComparer.Ordinal); // commandKey -> parameterSchema
            if (profile.Value.TryGetProperty("commandDefinitions", out var cd) && cd.ValueKind == JsonValueKind.Array)
                foreach (var m in cd.EnumerateArray())
                {
                    var key = Str(m, "commandKey");
                    if (key != null) commands[key] = Str(m, "parameterSchema");
                }

            var problems = new List<string>();
            var plant = c.IsPlant(d.Kind);
            var numeric = plant ? c.PlantMetrics : c.EquipmentMetrics;
            Missing(numeric, metrics.ContainsKey, "metric", problems);
            foreach (var key in numeric)
                if (metrics.TryGetValue(key, out var dt) && string.Equals(dt, "BOOLEAN", StringComparison.OrdinalIgnoreCase))
                    problems.Add($"metric {key} is BOOLEAN, the scene reads a number");
            if (plant)
            {
                Missing(c.PlantFlags, metrics.ContainsKey, "metric", problems);
                foreach (var key in c.PlantFlags)
                    if (metrics.TryGetValue(key, out var dt) && !string.Equals(dt, "BOOLEAN", StringComparison.OrdinalIgnoreCase))
                        problems.Add($"metric {key} is {dt}, the scene needs BOOLEAN");
            }
            else
            {
                Missing(c.EquipmentCommands, commands.ContainsKey, "command", problems);
            }

            // goto-area: any area the command accepts that the scene has no geometry for
            var unmapped = new List<string>();
            if (!plant && c.AreaCommand != null && commands.TryGetValue(c.AreaCommand, out var schema) && !string.IsNullOrWhiteSpace(schema))
            {
                if (!TryEnumValues(schema, out var values, out var why))
                    problems.Add($"command {c.AreaCommand} has an unreadable parameterSchema: {why}");
                else
                    foreach (var area in values)
                        if (!c.Zones.Contains(area)) unmapped.Add(area);
            }

            if (problems.Count > 0)
            {
                r.Outcome = BindOutcome.ProfileMismatch;
                r.Detail = $"profile {r.ProfileToken}: " + string.Join("; ", problems);
                return r;
            }

            r.Outcome = BindOutcome.Bound;
            r.UnmappedAreas = unmapped;
            return r;
        }

        static void Missing(IReadOnlyList<string> need, Func<string, bool> has, string what, List<string> problems)
        {
            var absent = new List<string>();
            foreach (var key in need)
                if (!has(key)) absent.Add(key);
            if (absent.Count > 0) problems.Add($"missing {what}{(absent.Count > 1 ? "s" : "")} {string.Join(", ", absent)}");
        }

        /// <summary>
        /// Every enum value in a command's parameter schema (a JSON array of
        /// <c>{name, kind, dataType, required, enum}</c>), across all its parameters.
        /// </summary>
        public static bool TryEnumValues(string parameterSchema, out List<string> values, out string why)
        {
            values = new List<string>();
            why = null;
            try
            {
                using var doc = JsonDocument.Parse(parameterSchema);
                if (doc.RootElement.ValueKind != JsonValueKind.Array) { why = "not a JSON array"; return false; }
                foreach (var p in doc.RootElement.EnumerateArray())
                {
                    if (p.ValueKind != JsonValueKind.Object || !p.TryGetProperty("enum", out var e) || e.ValueKind != JsonValueKind.Array) continue;
                    foreach (var v in e.EnumerateArray())
                        if (v.ValueKind == JsonValueKind.String) values.Add(v.GetString());
                }

                return true;
            }
            catch (JsonException j)
            {
                why = j.Message;
                return false;
            }
        }

        internal static string Str(JsonElement o, string name) =>
            o.ValueKind == JsonValueKind.Object && o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;

        internal static JsonElement? Obj(JsonElement o, string name) =>
            o.ValueKind == JsonValueKind.Object && o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Object ? v : (JsonElement?)null;
    }
}
