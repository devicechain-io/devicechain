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
    /// The per-device access tokens, in memory only. The token is the bearer a device connects with,
    /// so this holder never prints: <c>ToString</c> and the debugger view show a count, and nothing
    /// here writes to disk, PlayerPrefs, a log or a recording. The platform keeps only a digest of an
    /// access token and returns the value once, to the call that sets it, so a token held here cannot
    /// be read back: a run that loses it sets a new one.
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

    public enum CredentialPath { Created, Rotated }

    /// <summary>The result of issuing one device's token. Carries no secret.</summary>
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
    /// Sitepulse's own access token per device. A platform that stores access tokens as digests cannot
    /// hand one back, so the player cannot read the sim's <c>{device}-cred</c> credential to borrow its
    /// token. Instead each start sets a token of its own on a credential named
    /// <c>{deviceToken}-sitepulse</c> (created if absent, rotated if present) and keeps the value from
    /// that response in memory. The sim's credential is never touched, so the sim emitter keeps working.
    /// A rotation retires the previous run's token at once: the last player to start owns the
    /// device's session.
    /// </summary>
    public static class CredentialResolver
    {
        public const string AccessToken = "ACCESS_TOKEN";

        const string Fields = "token credentialType credentialId enabled device { token }";

        /// <summary>Which of the player's own credentials already exist. Asks for no token: none is readable.</summary>
        public const string ExistsQuery =
            "query Creds($tokens: [String!]!) { deviceCredentialsByToken(tokens: $tokens) { token credentialType enabled device { token } } }";

        public const string CreateMutation =
            "mutation CreateCred($r: DeviceCredentialCreateRequest) { createDeviceCredential(request: $r) { " + Fields + " } }";

        public const string RotateMutation =
            "mutation RotateCred($t: String!, $r: DeviceCredentialUpdateRequest!) { updateDeviceCredential(token: $t, request: $r) { " + Fields + " } }";

        /// <summary>The platform's marker for a stored token: what every read returns instead of the token.</summary>
        public const string FingerprintPrefix = "dctk1$";

        public static string CredentialToken(string deviceToken) => deviceToken + "-sitepulse";

        /// <summary>
        /// A fresh random token: 32 bytes as 64 lowercase hex characters. Hex, not base64url, because a
        /// run of 32 or more hex characters is what the <see cref="Domain.Redactor"/> recognises as a
        /// credential, so a token that ever reached a log line or a recording would be redacted there.
        /// </summary>
        public static string MintToken()
        {
            var bytes = new byte[32];
            using (var rng = System.Security.Cryptography.RandomNumberGenerator.Create()) rng.GetBytes(bytes);
            var sb = new System.Text.StringBuilder(64);
            foreach (var b in bytes) sb.Append(b.ToString("x2"));
            return sb.ToString();
        }

        /// <summary>Null when the row is an enabled ACCESS_TOKEN credential of <paramref name="deviceToken"/> that carries an id, else why not.</summary>
        public static string Check(CredentialRow row, string deviceToken)
        {
            if (row.DeviceToken != deviceToken) return $"credential {row.Token} belongs to device {row.DeviceToken ?? "(none)"}, not {deviceToken}";
            if (row.CredentialType != AccessToken) return $"credential {row.Token} is {row.CredentialType}, not {AccessToken}";
            if (!row.Enabled) return $"credential {row.Token} is disabled";
            if (string.IsNullOrEmpty(row.CredentialId)) return $"credential {row.Token} has no id";
            return null;
        }

        public static List<CredentialRow> ParseExisting(string dataJson)
        {
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty("deviceCredentialsByToken", out var list)) throw new FormatException("the response has no deviceCredentialsByToken");
            if (list.ValueKind != JsonValueKind.Array) throw new FormatException("deviceCredentialsByToken is not a list");
            var rows = new List<CredentialRow>();
            foreach (var e in list.EnumerateArray()) rows.Add(ParseRow(e));
            return rows;
        }

        /// <summary>The credential a create or update mutation answered with.</summary>
        public static CredentialRow ParseIssued(string dataJson, string field)
        {
            using var doc = JsonDocument.Parse(dataJson);
            if (!doc.RootElement.TryGetProperty(field, out var e) || e.ValueKind != JsonValueKind.Object) throw new FormatException($"the response has no {field}");
            return ParseRow(e);
        }

        static CredentialRow ParseRow(JsonElement e)
        {
            var device = DeviceBinder.Obj(e, "device");
            return new CredentialRow
            {
                Token = DeviceBinder.Str(e, "token"),
                CredentialType = DeviceBinder.Str(e, "credentialType"),
                CredentialId = DeviceBinder.Str(e, "credentialId"),
                Enabled = e.TryGetProperty("enabled", out var en) && en.ValueKind == JsonValueKind.True,
                DeviceToken = device.HasValue ? DeviceBinder.Str(device.Value, "token") : null,
            };
        }

        /// <summary>One call for which credentials exist, then one create or rotate per bound device.</summary>
        public static async Task<IReadOnlyList<CredentialOutcome>> ResolveAll(QueryFn q, IReadOnlyList<BindResult> bound, DeviceCredentials credentials, CancellationToken ct)
        {
            var tokens = new List<string>();
            foreach (var b in bound) tokens.Add(CredentialToken(b.DeviceToken));
            var existing = ParseExisting(await q(ExistsQuery, VarsJson.StringList("tokens", tokens), ct));
            return await Resolve(bound, existing, async (device, exists, token) =>
            {
                var credToken = CredentialToken(device);
                return exists
                    ? ParseIssued(await q(RotateMutation, VarsJson.CredentialRotate(credToken, token), ct), "updateDeviceCredential")
                    : ParseIssued(await q(CreateMutation, VarsJson.CredentialCreate(credToken, device, token), ct), "createDeviceCredential");
            }, credentials, MintToken);
        }

        /// <summary>
        /// Pure core. For each bound device: refuse an existing <c>-sitepulse</c> row that is not that
        /// device's ACCESS_TOKEN credential (it is never rewritten); otherwise mint a token, ask the
        /// platform to set it, and accept the answer only if the response carries back the very token
        /// that was sent. Anything else is a failed outcome with a reason, never an unusable bearer.
        /// </summary>
        public static async Task<IReadOnlyList<CredentialOutcome>> Resolve(IReadOnlyList<BindResult> bound, IReadOnlyList<CredentialRow> existing,
            Func<string, bool, string, Task<CredentialRow>> issue, DeviceCredentials credentials, Func<string> mint = null)
        {
            mint ??= MintToken;
            var byToken = new Dictionary<string, CredentialRow>(StringComparer.Ordinal);
            foreach (var row in existing)
                if (row.Token != null) byToken[row.Token] = row;

            var outcomes = new List<CredentialOutcome>();
            foreach (var b in bound)
            {
                var o = new CredentialOutcome { ExternalId = b.ExternalId, DeviceToken = b.DeviceToken, Path = CredentialPath.Created };
                outcomes.Add(o);
                var credToken = CredentialToken(b.DeviceToken);

                var exists = byToken.TryGetValue(credToken, out var row);
                if (exists)
                {
                    o.Path = CredentialPath.Rotated;
                    // the exists query asks for no token, so only ownership and type are judged here
                    if (row.DeviceToken != b.DeviceToken) { Fail(b, o, $"credential {credToken} belongs to device {row.DeviceToken ?? "(none)"}, not {b.DeviceToken}; not rewriting it"); continue; }
                    if (row.CredentialType != AccessToken) { Fail(b, o, $"credential {credToken} is {row.CredentialType}, not {AccessToken}; not rewriting it"); continue; }
                }

                var token = mint();
                CredentialRow issued;
                try
                {
                    issued = await issue(b.DeviceToken, exists, token);
                }
                catch (OperationCanceledException) { throw; }
                catch (Exception e)
                {
                    Fail(b, o, $"the platform refused to {(exists ? "rotate" : "create")} {credToken}: {e.Message}");
                    continue;
                }

                // CredentialId is compared, never printed: it is the secret, or its fingerprint
                var why = issued.CredentialId != null && issued.CredentialId.StartsWith(FingerprintPrefix, StringComparison.Ordinal)
                    ? $"the platform returned a fingerprint, not the token it was given, for {credToken}"
                    : issued.CredentialId != token
                        ? $"the platform did not return the token it was given for {credToken}"
                        : Check(issued, b.DeviceToken);
                if (why != null) { Fail(b, o, why); continue; }

                credentials.Set(b.DeviceToken, token);
                o.Ok = true;
                PlatformLog.Info($"{b.ExternalId}: access token {(exists ? "rotated" : "created")} on {credToken}");
            }

            return outcomes;
        }

        static void Fail(BindResult b, CredentialOutcome o, string reason)
        {
            o.Reason = reason;
            PlatformLog.Error($"{b.ExternalId}: {reason}");
        }
    }
}
