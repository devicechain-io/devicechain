// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// The per-device credentials, in memory only. An ACCESS_TOKEN credential's id is the bearer a
    /// device connects with, so this holder never prints: <c>ToString</c> and the debugger view show
    /// a count, and nothing here writes to disk, PlayerPrefs, a log or a recording.
    /// </summary>
    [DebuggerDisplay("DeviceCredentials")]
    public sealed class DeviceCredentials
    {
        readonly Dictionary<string, string> byDevice = new Dictionary<string, string>(StringComparer.Ordinal);

        public int Count => byDevice.Count;

        internal void Set(string deviceToken, string credentialId) => byDevice[deviceToken] = credentialId;

        public bool TryGet(string deviceToken, out string credentialId) => byDevice.TryGetValue(deviceToken, out credentialId);

        public override string ToString() => $"DeviceCredentials({byDevice.Count})";
    }

    public enum CredentialPath { Batched, Fallback }

    /// <summary>The result of looking up one device's credential. Carries no secret.</summary>
    public sealed class CredentialOutcome
    {
        public string ExternalId { get; set; }
        public string DeviceToken { get; set; }
        public bool Ok { get; set; }
        public CredentialPath Path { get; set; }
        public string Reason { get; set; }
    }

    /// <summary>A credential row as the platform returned it. Its <c>ToString</c> omits the id.</summary>
    public sealed class CredentialRow
    {
        public string Token { get; set; }
        public string CredentialType { get; set; }
        public string CredentialId { get; set; }
        public bool Enabled { get; set; }
        public string DeviceToken { get; set; }

        public override string ToString() => $"credential {Token} ({CredentialType}, {(Enabled ? "enabled" : "disabled")}, device {DeviceToken})";
    }

    /// <summary>
    /// The documented <c>{deviceToken}-cred</c> contract, cross-checked: a row is accepted only if it
    /// belongs to the device it was looked up for, is an ACCESS_TOKEN and is enabled. The player
    /// never re-derives a credential from a seed, though it could.
    /// </summary>
    public static class CredentialResolver
    {
        public const string AccessToken = "ACCESS_TOKEN";

        public const string BatchQuery =
            "query Creds($tokens: [String!]!) { deviceCredentialsByToken(tokens: $tokens) { token credentialType credentialId enabled device { token } } }";

        public const string FallbackQuery =
            "query Cred($c: DeviceCredentialSearchCriteria!) { deviceCredentials(criteria: $c) { results { token credentialType credentialId enabled device { token } } } }";

        public static string CredentialToken(string deviceToken) => deviceToken + "-cred";

        /// <summary>Null when the row is acceptable for <paramref name="deviceToken"/>, else why not.</summary>
        public static string Check(CredentialRow row, string deviceToken)
        {
            if (row.DeviceToken != deviceToken) return $"credential {row.Token} belongs to device {row.DeviceToken ?? "(none)"}, not {deviceToken}";
            if (row.CredentialType != AccessToken) return $"credential {row.Token} is {row.CredentialType}, not {AccessToken}";
            if (!row.Enabled) return $"credential {row.Token} is disabled";
            if (string.IsNullOrEmpty(row.CredentialId)) return $"credential {row.Token} has no id";
            return null;
        }

        public static List<CredentialRow> ParseBatch(string dataJson) => Parse(dataJson, "deviceCredentialsByToken", false);

        public static List<CredentialRow> ParseSearch(string dataJson) => Parse(dataJson, "deviceCredentials", true);

        static List<CredentialRow> Parse(string dataJson, string field, bool paged)
        {
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty(field, out var list)) throw new FormatException($"the response has no {field}");
            if (paged)
            {
                if (list.ValueKind != JsonValueKind.Object || !list.TryGetProperty("results", out list)) throw new FormatException($"{field} has no results");
            }

            if (list.ValueKind != JsonValueKind.Array) throw new FormatException($"{field} is not a list");
            var rows = new List<CredentialRow>();
            foreach (var e in list.EnumerateArray())
            {
                var device = DeviceBinder.Obj(e, "device");
                rows.Add(new CredentialRow
                {
                    Token = DeviceBinder.Str(e, "token"),
                    CredentialType = DeviceBinder.Str(e, "credentialType"),
                    CredentialId = DeviceBinder.Str(e, "credentialId"),
                    Enabled = e.TryGetProperty("enabled", out var en) && en.ValueKind == JsonValueKind.True,
                    DeviceToken = device.HasValue ? DeviceBinder.Str(device.Value, "token") : null,
                });
            }

            return rows;
        }

        /// <summary>One batched call for every bound device, then the search fallback for any that did not answer.</summary>
        public static async Task<IReadOnlyList<CredentialOutcome>> ResolveAll(QueryFn q, IReadOnlyList<BindResult> bound, DeviceCredentials credentials, CancellationToken ct)
        {
            var tokens = new List<string>();
            foreach (var b in bound) tokens.Add(CredentialToken(b.DeviceToken));
            var batch = ParseBatch(await q(BatchQuery, VarsJson.StringList("tokens", tokens), ct));
            return await Resolve(bound, batch, async device =>
                ParseSearch(await q(FallbackQuery, VarsJson.CredentialSearch(device), ct)), credentials);
        }

        /// <summary>
        /// Pure core: pick each device's credential from the batch, falling back to
        /// <paramref name="search"/> when the batch has no acceptable row for it, and record which
        /// path answered. The fallback must find exactly one acceptable row; with several, it refuses
        /// to choose.
        /// </summary>
        public static async Task<IReadOnlyList<CredentialOutcome>> Resolve(IReadOnlyList<BindResult> bound, IReadOnlyList<CredentialRow> batch,
            Func<string, Task<List<CredentialRow>>> search, DeviceCredentials credentials)
        {
            var byToken = new Dictionary<string, CredentialRow>(StringComparer.Ordinal);
            foreach (var row in batch)
                if (row.Token != null) byToken[row.Token] = row;

            var outcomes = new List<CredentialOutcome>();
            foreach (var b in bound)
            {
                var o = new CredentialOutcome { ExternalId = b.ExternalId, DeviceToken = b.DeviceToken, Path = CredentialPath.Batched };
                outcomes.Add(o);

                string batchWhy = null;
                if (byToken.TryGetValue(CredentialToken(b.DeviceToken), out var row))
                {
                    batchWhy = Check(row, b.DeviceToken);
                    if (batchWhy == null)
                    {
                        credentials.Set(b.DeviceToken, row.CredentialId);
                        o.Ok = true;
                        PlatformLog.Info($"{b.ExternalId}: credential via batched {CredentialToken(b.DeviceToken)}");
                        continue;
                    }
                }

                // no acceptable {device}-cred row: ask for the device's enabled ACCESS_TOKEN credentials
                o.Path = CredentialPath.Fallback;
                var found = await search(b.DeviceToken);
                var good = new List<CredentialRow>();
                var why = new List<string>();
                if (batchWhy != null) why.Add(batchWhy);
                foreach (var f in found)
                {
                    var w = Check(f, b.DeviceToken);
                    if (w == null) good.Add(f);
                    else why.Add(w);
                }

                if (good.Count == 1)
                {
                    credentials.Set(b.DeviceToken, good[0].CredentialId);
                    o.Ok = true;
                    PlatformLog.Info($"{b.ExternalId}: credential via search fallback ({CredentialToken(b.DeviceToken)} did not answer)");
                }
                else if (good.Count > 1)
                {
                    o.Reason = $"{good.Count} enabled {AccessToken} credentials for {b.DeviceToken}; refusing to choose one";
                }
                else
                {
                    o.Reason = $"no usable {AccessToken} credential for {b.DeviceToken}" + (why.Count > 0 ? " (" + string.Join("; ", why) + ")" : "");
                }
            }

            return outcomes;
        }
    }
}
