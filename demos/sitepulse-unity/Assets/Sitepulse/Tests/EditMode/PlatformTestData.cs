// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>Canned wire shapes, built the way the platform sends them (the shapes were read from a live instance).</summary>
    static class PlatformTestData
    {
        public static readonly string[] Zones = { "sp-zone-cut", "sp-zone-fill", "sp-zone-yard" };

        public static SceneContract Contract() => SitepulseScene.Contract(new HashSet<string>(Zones));

        static string B64(string s) => Convert.ToBase64String(Encoding.UTF8.GetBytes(s)).TrimEnd('=').Replace('+', '-').Replace('/', '_');

        /// <summary>A JWT-shaped string with the given payload; the signature is not a real one.</summary>
        public static string JwtWith(string payloadJson) => B64("{\"alg\":\"HS256\",\"typ\":\"JWT\"}") + "." + B64(payloadJson) + ".c2lnbmF0dXJl";

        public static string JwtExp(long exp) => JwtWith("{\"sub\":\"x\",\"exp\":" + exp + "}");

        public static string ConfigJson(Action<Dictionary<string, object>> edit = null)
        {
            var d = new Dictionary<string, object>
            {
                ["apiOrigin"] = "http://localhost",
                ["wsUrl"] = "ws://localhost/api/event-management/graphql",
                ["mqttBroker"] = "ssl://localhost:1883",
                ["instanceId"] = "sitepulse",
                ["tenant"] = "sim-sitepulse",
                ["manifestId"] = "sitepulse",
                ["token"] = JwtExp(4102444800),
            };
            edit?.Invoke(d);
            var sb = new StringBuilder("{");
            var first = true;
            foreach (var kv in d)
            {
                if (!first) sb.Append(',');
                first = false;
                sb.Append('"').Append(kv.Key).Append("\":");
                sb.Append(kv.Value is bool b ? (b ? "true" : "false") : kv.Value is string s ? "\"" + s + "\"" : kv.Value.ToString());
            }

            return sb.Append('}').ToString();
        }

        public const string EquipmentSchema = "[{\"enum\": [\"sp-zone-cut\", \"sp-zone-fill\", \"sp-zone-yard\"], \"kind\": \"SCALAR\", \"name\": \"areaToken\", \"dataType\": \"STRING\", \"required\": true}]";

        public static string[] EquipmentMetrics() => new[] { "fuel_pct", "engine_temp_c", "engine_hours", "payload_t", "tyre_pressure_kpa" };

        public sealed class Dev
        {
            public string Token, ExternalId, Type, Profile = "sp-equipment-profile";
            public bool HasProfile = true;
            public string[] Metrics = EquipmentMetrics();
            public Dictionary<string, string> BooleanMetrics = new Dictionary<string, string>();
            public (string key, string schema)[] Commands = { ("goto-area", EquipmentSchema), ("goto-refuel", null) };
        }

        public static Dev Hauler(string n = "01") => new Dev { Token = "sp-hauler-" + n, ExternalId = "SP-HL-00" + n, Type = "sp-hauler" };
        public static Dev Loader(string n = "01") => new Dev { Token = "sp-loader-" + n, ExternalId = "SP-LD-00" + n, Type = "sp-loader" };
        public static Dev Dozer(string n = "01") => new Dev { Token = "sp-dozer-" + n, ExternalId = "SP-DZ-00" + n, Type = "sp-dozer" };

        public static Dev Plant() => new Dev
        {
            Token = "sp-plant-01", ExternalId = "SP-PL-0001", Type = "sp-crusher-plant", Profile = "sp-plant-profile",
            Metrics = new[] { "throughput_tph" },
            BooleanMetrics = new Dictionary<string, string> { ["plant_running"] = "BOOLEAN" },
            Commands = new (string, string)[0],
        };

        public static string Devices(params Dev[] devices)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                w.WriteStartArray("devicesByExternalId");
                foreach (var d in devices)
                {
                    w.WriteStartObject();
                    w.WriteString("token", d.Token);
                    w.WriteString("externalId", d.ExternalId);
                    w.WriteStartObject("deviceType");
                    w.WriteString("token", d.Type);
                    if (d.HasProfile)
                    {
                        w.WriteStartObject("profile");
                        w.WriteString("token", d.Profile);
                        w.WriteNumber("activeVersion", 1);
                        w.WriteStartArray("metricDefinitions");
                        foreach (var m in d.Metrics)
                        {
                            w.WriteStartObject();
                            w.WriteString("metricKey", m);
                            w.WriteString("dataType", "DOUBLE");
                            w.WriteNull("unit");
                            w.WriteEndObject();
                        }

                        foreach (var kv in d.BooleanMetrics)
                        {
                            w.WriteStartObject();
                            w.WriteString("metricKey", kv.Key);
                            w.WriteString("dataType", kv.Value);
                            w.WriteNull("unit");
                            w.WriteEndObject();
                        }

                        w.WriteEndArray();
                        w.WriteStartArray("commandDefinitions");
                        foreach (var (key, schema) in d.Commands)
                        {
                            w.WriteStartObject();
                            w.WriteString("commandKey", key);
                            if (schema == null) w.WriteNull("parameterSchema");
                            else w.WriteString("parameterSchema", schema);
                            w.WriteEndObject();
                        }

                        w.WriteEndArray();
                        w.WriteEndObject();
                    }
                    else w.WriteNull("profile");

                    w.WriteEndObject();
                    w.WriteEndObject();
                }

                w.WriteEndArray();
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        public static string Cred(string token, string deviceToken, string type = "ACCESS_TOKEN", bool enabled = true, string id = "0123456789abcdef0123456789abcdef")
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                WriteCred(w, token, deviceToken, type, enabled, id);
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        public static void WriteCred(Utf8JsonWriter w, string token, string deviceToken, string type, bool enabled, string id)
        {
            w.WriteStartObject();
            w.WriteString("token", token);
            w.WriteString("credentialType", type);
            w.WriteString("credentialId", id);
            w.WriteBoolean("enabled", enabled);
            w.WriteStartObject("device");
            w.WriteString("token", deviceToken);
            w.WriteEndObject();
            w.WriteEndObject();
        }

        public static string CredBatch(params string[] credJson) => "{\"deviceCredentialsByToken\":[" + string.Join(",", credJson) + "]}";
        public static string CredSearch(params string[] credJson) => "{\"deviceCredentials\":{\"results\":[" + string.Join(",", credJson) + "]}}";

        public static CredentialRow Row(string token, string deviceToken, string type = "ACCESS_TOKEN", bool enabled = true, string id = "0123456789abcdef0123456789abcdef") =>
            new CredentialRow { Token = token, DeviceToken = deviceToken, CredentialType = type, Enabled = enabled, CredentialId = id };
    }
}
