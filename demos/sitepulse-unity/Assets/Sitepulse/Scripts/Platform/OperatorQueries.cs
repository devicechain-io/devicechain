// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Unity;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// The operator plane's HTTP half: one <see cref="GraphQlClient"/> over the SDK's
    /// UnityWebRequest transport, authorised by the <see cref="TokenBroker"/>. It holds the operator
    /// token and nothing else; device credentials never pass through it. A 401 refreshes the token
    /// and retries once. JSON stays hand-read (<see cref="JsonElement"/> only), which IL2CPP cannot
    /// strip.
    /// </summary>
    public sealed class OperatorQueries
    {
        // the one JsonTypeInfo the SDK needs: no reflection-based serializer is ever asked for a POCO
        static readonly JsonTypeInfo<JsonElement> Json = PlatformJson.Element;

        readonly GraphQlClient client;
        readonly TokenBroker broker;
        readonly Area area;

        public OperatorQueries(GraphQlClient client, TokenBroker broker, Area area = Area.DeviceManagement)
        {
            this.client = client ?? throw new ArgumentNullException(nameof(client));
            this.broker = broker ?? throw new ArgumentNullException(nameof(broker));
            this.area = area;
        }

        /// <summary>Construct on the main thread (the transport captures its context).</summary>
        public static OperatorQueries Create(RunnerConfig config, TokenBroker broker) =>
            new OperatorQueries(new GraphQlClient(new UnityWebRequestHttpTransport(), new Uri(config.ApiOrigin), broker.AsProvider()), broker);

        public QueryFn AsQueryFn() => Run;

        async Task<string> Run(string query, string variablesJson, CancellationToken ct)
        {
            using var vars = JsonDocument.Parse(variablesJson);
            var root = vars.RootElement.Clone();
            for (var attempt = 0; ; attempt++)
            {
                var used = await broker.Get(ct);
                try
                {
                    var data = await client.SendAsync(area, query, root, Json, Json, null, ct);
                    return data.GetRawText();
                }
                catch (GraphQlRequestException e) when (e.Status == 401 && attempt == 0)
                {
                    await broker.NotifyUnauthorized(used);
                }
            }
        }
    }

    /// <summary>GraphQL variables as JSON text, written by hand so no reflection serializer is involved.</summary>
    public static class VarsJson
    {
        public static string StringList(string name, System.Collections.Generic.IEnumerable<string> values) =>
            Write(w =>
            {
                w.WriteStartArray(name);
                foreach (var v in values) w.WriteStringValue(v);
                w.WriteEndArray();
            });

        /// <summary>Criteria for the credential search fallback: this device's enabled ACCESS_TOKEN credentials.</summary>
        public static string CredentialSearch(string deviceToken) =>
            Write(w =>
            {
                w.WriteStartObject("c");
                w.WriteNumber("pageNumber", 1);
                w.WriteNumber("pageSize", 10);
                w.WriteString("device", deviceToken);
                w.WriteString("credentialType", CredentialResolver.AccessToken);
                w.WriteBoolean("enabled", true);
                w.WriteEndObject();
            });

        static string Write(Action<Utf8JsonWriter> body)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                body(w);
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }
    }
}
