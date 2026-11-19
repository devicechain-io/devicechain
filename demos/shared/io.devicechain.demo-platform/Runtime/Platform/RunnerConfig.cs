// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// What the runner's <c>/config.json</c> says: where the platform is and the operator token for
    /// reading it. The token is a live tenant-admin credential, so this type never prints it.
    /// </summary>
    public sealed class RunnerConfig
    {
        public string ApiOrigin { get; set; }
        public string WsUrl { get; set; }
        public string MqttBroker { get; set; }
        public string InstanceId { get; set; }
        public string Tenant { get; set; }

        /// <summary>Absent, false or true as the runner sent it.</summary>
        public bool? MqttTlsInsecure { get; set; }

        public string Token { get; set; }

        public override string ToString() => $"RunnerConfig({Tenant}@{InstanceId})";
    }

    /// <summary>What a player does with the runner's platform: be its devices (MQTT sessions), or only watch it.</summary>
    public enum RunnerUse
    {
        /// <summary>The player opens a device session per device over the broker, so the broker is required and must be verified.</summary>
        DevicePlane,

        /// <summary>
        /// The player only reads the platform under the operator token (GraphQL over HTTP and WebSocket) and never dials the broker, so
        /// <c>mqttBroker</c> may be absent and an unverified broker is recorded, not refused: nothing here would connect to it.
        /// </summary>
        ObserveOnly,
    }

    /// <summary>
    /// Parses and validates <c>/config.json</c>. Every required field must be present and sensible;
    /// <c>mqttTLSInsecure: true</c> is refused because Live pins the broker's CA and a runner that
    /// advertises an insecure broker is not one the player trusts; and the file's expected instance
    /// and tenant, when set, must match, so a player aimed at the wrong runner says so. An
    /// <see cref="RunnerUse.ObserveOnly"/> player, which never dials the broker, needs neither of the
    /// broker rules.
    /// </summary>
    public static class RunnerConfigParser
    {
        const string Where = "/config.json";

        public static Parsed<RunnerConfig> Parse(string json, LiveSettings expect) => Parse(json, expect, RunnerUse.DevicePlane);

        public static Parsed<RunnerConfig> Parse(string json, LiveSettings expect, RunnerUse use)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json ?? ""); }
            catch (JsonException e) { return Parsed<RunnerConfig>.Fail($"{Where}: not valid JSON: {e.Message}"); }

            using (doc)
            {
                var root = doc.RootElement;
                if (root.ValueKind != JsonValueKind.Object) return Parsed<RunnerConfig>.Fail($"{Where}: must be a JSON object");

                var errors = new List<string>();
                var observeOnly = use == RunnerUse.ObserveOnly;
                var cfg = new RunnerConfig
                {
                    ApiOrigin = Str(root, "apiOrigin", errors),
                    WsUrl = Str(root, "wsUrl", errors),
                    // a watcher never dials the broker, so its address is optional; when present it is still checked below
                    MqttBroker = observeOnly ? OptionalStr(root, "mqttBroker", errors) : Str(root, "mqttBroker", errors),
                    InstanceId = Str(root, "instanceId", errors),
                    Tenant = Str(root, "tenant", errors),
                    Token = Str(root, "token", errors),
                };

                if (root.TryGetProperty("mqttTLSInsecure", out var insecure) && insecure.ValueKind != JsonValueKind.Null)
                {
                    if (insecure.ValueKind == JsonValueKind.True) cfg.MqttTlsInsecure = true;
                    else if (insecure.ValueKind == JsonValueKind.False) cfg.MqttTlsInsecure = false;
                    else errors.Add($"{Where}: \"mqttTLSInsecure\" must be a boolean");
                }

                if (cfg.MqttTlsInsecure == true && !observeOnly)
                    errors.Add($"{Where}: \"mqttTLSInsecure\" is true; Live mode pins the broker's CA and refuses a runner that advertises an insecure broker");

                Url(cfg.ApiOrigin, "apiOrigin", new[] { "http", "https" }, errors);
                Url(cfg.WsUrl, "wsUrl", new[] { "ws", "wss" }, errors);
                if (!string.IsNullOrWhiteSpace(cfg.MqttBroker) && !Uri.TryCreate(cfg.MqttBroker, UriKind.Absolute, out _))
                    errors.Add($"{Where}: \"mqttBroker\" \"{cfg.MqttBroker}\" is not a URL");

                // the token's exp drives the refresh schedule; one with no readable exp cannot be scheduled
                if (!string.IsNullOrWhiteSpace(cfg.Token) && !Jwt.TryGetExpiry(cfg.Token, out _))
                    errors.Add($"{Where}: \"token\" is not a JWT with an exp claim");

                if (expect != null)
                {
                    if (!string.IsNullOrEmpty(expect.ExpectInstance) && cfg.InstanceId != null && cfg.InstanceId != expect.ExpectInstance)
                        errors.Add($"{expect.Source}: expectInstance is \"{expect.ExpectInstance}\" but the runner serves instance \"{cfg.InstanceId}\"");
                    if (!string.IsNullOrEmpty(expect.ExpectTenant) && cfg.Tenant != null && cfg.Tenant != expect.ExpectTenant)
                        errors.Add($"{expect.Source}: expectTenant is \"{expect.ExpectTenant}\" but the runner serves tenant \"{cfg.Tenant}\"");
                }

                return errors.Count > 0 ? Parsed<RunnerConfig>.Fail(string.Join("\n", errors)) : Parsed<RunnerConfig>.Success(cfg);
            }
        }

        static string Str(JsonElement root, string name, List<string> errors)
        {
            if (!root.TryGetProperty(name, out var v) || v.ValueKind == JsonValueKind.Null)
            {
                errors.Add($"{Where}: missing \"{name}\"");
                return null;
            }

            if (v.ValueKind != JsonValueKind.String)
            {
                errors.Add($"{Where}: \"{name}\" must be a string");
                return null;
            }

            var s = v.GetString();
            if (string.IsNullOrWhiteSpace(s))
            {
                errors.Add($"{Where}: \"{name}\" is blank");
                return null;
            }

            return s;
        }

        static string OptionalStr(JsonElement root, string name, List<string> errors)
        {
            if (!root.TryGetProperty(name, out var v) || v.ValueKind == JsonValueKind.Null) return null;
            if (v.ValueKind != JsonValueKind.String)
            {
                errors.Add($"{Where}: \"{name}\" must be a string");
                return null;
            }

            var s = v.GetString();
            return string.IsNullOrWhiteSpace(s) ? null : s;
        }

        static void Url(string value, string name, string[] schemes, List<string> errors)
        {
            if (string.IsNullOrWhiteSpace(value)) return;
            if (!Uri.TryCreate(value, UriKind.Absolute, out var u) || Array.IndexOf(schemes, u.Scheme) < 0)
                errors.Add($"{Where}: \"{name}\" \"{value}\" is not a {string.Join("/", schemes)} URL");
        }
    }
}
