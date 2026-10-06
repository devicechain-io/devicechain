// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Subscriptions;
using DeviceChain.Sdk.Transport;
using DeviceChain.Sdk.Unity;
using UnityEngine;
using UnityEngine.Networking;
using Debug = UnityEngine.Debug;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// The smallest scene that proves the live path in a player before any overlay work depends on
    /// it: the runner's /config.json over UnityWebRequest, one GraphQL query over the SDK's HTTP
    /// transport, and a round trip: a measurement subscription over the SDK's WebSocket transport,
    /// then one measurement published as the plant over MQTT, which must come back on it. It also
    /// times raw TCP connects to <c>localhost</c> by address family, because on Windows
    /// <c>localhost</c> resolves to <c>::1</c> first and, under WSL mirrored networking, nothing
    /// answers there: a connect that tries <c>::1</c> alone hangs instead of being refused.
    /// Results go to the player log and to <c>live-smoke.log</c> beside it; the runner's token is
    /// never written anywhere. Quits when done, so it can run unattended
    /// (<c>-batchmode -nographics</c>).
    /// </summary>
    public sealed class LiveSmokeProbe : MonoBehaviour
    {
        [SerializeField] private string runnerUrl = "http://localhost:8090";
        [SerializeField] private float roundTripSeconds = 40f;
        [SerializeField] private bool quitWhenDone = true;

        private static readonly JsonTypeInfo<JsonElement> Json = PlatformJson.Element;

        private readonly StringBuilder _report = new StringBuilder();
        private int _failures;

        private async void Start()
        {
            Line($"live smoke · {Application.platform} · {(Application.isEditor ? "editor" : "player")} · scripting={ScriptingBackend()}");
            try
            {
                await Run();
            }
            catch (Exception e)
            {
                Fail($"aborted: {e.GetType().Name}: {e.Message}");
            }

            Line(_failures == 0 ? "RESULT PASS" : $"RESULT FAIL ({_failures})");
            var path = Path.Combine(Application.persistentDataPath, "live-smoke.log");
            File.WriteAllText(path, _report.ToString());
            Debug.Log($"[live-smoke] report written to {path}");
            if (quitWhenDone && !Application.isEditor) Application.Quit(_failures == 0 ? 0 : 1);
        }

        private async Task Run()
        {
            // 1. Name resolution and raw TCP, by family. Tells us whether this runtime's own
            //    socket stack stalls on ::1, independently of every client built on top of it.
            var addrs = Dns.GetHostAddresses("localhost");
            Line("dns localhost → " + string.Join(", ", Array.ConvertAll(addrs, a => a.ToString())));
            await TimeTcp("localhost", 1883, AddressFamily.Unspecified);
            await TimeTcp("localhost", 1883, AddressFamily.InterNetwork);
            await TimeTcp("localhost", 80, AddressFamily.Unspecified);

            // 2. The runner's presentation config, the way the player will read it.
            var sw = Stopwatch.StartNew();
            string body;
            using (var req = UnityWebRequest.Get(runnerUrl.TrimEnd('/') + "/config.json"))
            {
                var op = req.SendWebRequest();
                while (!op.isDone) await Task.Yield();
                if (req.result != UnityWebRequest.Result.Success)
                {
                    Fail($"config.json: {req.error} after {sw.ElapsedMilliseconds} ms");
                    return;
                }

                body = req.downloadHandler.text;
            }

            using var cfgDoc = JsonDocument.Parse(body);
            var cfg = cfgDoc.RootElement;
            string Str(string k) => cfg.TryGetProperty(k, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;
            var apiOrigin = Str("apiOrigin");
            var wsUrl = Str("wsUrl");
            var token = Str("token");
            Line($"config.json {sw.ElapsedMilliseconds} ms · instance={Str("instanceId")} tenant={Str("tenant")} apiOrigin={apiOrigin} wsUrl={wsUrl} mqtt={Str("mqttBroker")} token={(string.IsNullOrEmpty(token) ? "MISSING" : "present")}");
            if (apiOrigin == null || wsUrl == null || string.IsNullOrEmpty(token))
            {
                Fail("config.json is missing apiOrigin, wsUrl or token");
                return;
            }

            TokenProvider tokens = _ => new ValueTask<string>(token);

            // 3. One GraphQL query over the SDK's HTTP transport.
            var gql = new GraphQlClient(new UnityWebRequestHttpTransport(), new Uri(apiOrigin), tokens);
            var ids = new List<string>();
            for (var i = 1; i <= 6; i++) { ids.Add($"SP-HL-{i:0000}"); ids.Add($"SP-LD-{i:0000}"); ids.Add($"SP-DZ-{i:0000}"); }
            ids.Add("SP-PL-0001");
            var vars = JsonDocument.Parse(VarsJson.StringList("ids", ids)).RootElement;
            sw.Restart();
            var data = await gql.SendAsync(Area.DeviceManagement,
                "query Resolve($ids: [String!]!) { devicesByExternalId(externalIds: $ids) { token externalId deviceType { token profile { token activeVersion metricDefinitions { metricKey dataType unit } commandDefinitions { commandKey } } } } }",
                vars, Json, Json);
            var devices = data.GetProperty("devicesByExternalId");
            Line($"devicesByExternalId {sw.ElapsedMilliseconds} ms · {devices.GetArrayLength()} of {ids.Count} resolved");
            if (devices.GetArrayLength() != ids.Count) Fail("not every scene device resolved");

            // 4. One subscription through the SDK, and one measurement published to it over MQTT
            //    as the plant: the Sitepulse runner emits nothing itself (the player is the
            //    device), so the round trip is the only way to see an item arrive.
            string plantToken = null;
            foreach (var d in devices.EnumerateArray())
                if (d.GetProperty("externalId").GetString() == "SP-PL-0001") plantToken = d.GetProperty("token").GetString();
            if (plantToken == null) { Fail("SP-PL-0001 did not resolve"); return; }

            var ws = new GraphQlWsClient(new ClientWebSocketFactory(), new Uri(wsUrl), tokens);
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(roundTripSeconds));
            long opened = -1, arrived = -1;
            var marker = 1000 + new System.Random().Next(9000) + 0.25;
            var connected = new TaskCompletionSource<bool>();
            sw.Restart();
            var listen = Listen();
            async Task Listen()
            {
                try
                {
                    var stream = ws.SubscribeAsync("subscription($d: String) { measurementStream(deviceToken: $d) { deviceToken name value occurredTime } }",
                        JsonDocument.Parse("{\"d\":\"" + plantToken + "\"}").RootElement,
                        Json, Json, cts.Token).GetAsyncEnumerator(cts.Token);
                    var next = stream.MoveNextAsync();
                    opened = sw.ElapsedMilliseconds;
                    connected.TrySetResult(true);
                    while (await next)
                    {
                        var m = stream.Current.GetProperty("measurementStream");
                        if (m.GetProperty("name").GetString() == "throughput_tph" && Math.Abs(m.GetProperty("value").GetDouble() - marker) < 0.001)
                        {
                            arrived = sw.ElapsedMilliseconds;
                            cts.Cancel();
                            break;
                        }
                        next = stream.MoveNextAsync();
                    }
                }
                catch (OperationCanceledException) { }
                catch (Exception e) { Fail($"measurementStream after {sw.ElapsedMilliseconds} ms: {Chain(e)}"); }
                finally { connected.TrySetResult(false); }
            }

            await connected.Task;
            await Task.Delay(1500);                                // let the subscribe frame reach the server

            var settings = LiveSettingsLoader.Resolve(Environment.GetCommandLineArgs(),
                Path.Combine(Application.persistentDataPath, LiveSettingsLoader.FileName), File.Exists, File.ReadAllText);
            if (!settings.Ok) { Fail("live settings: " + settings.Error); return; }
            var credData = await gql.SendAsync(Area.DeviceManagement,
                "query Creds($t: [String!]!) { deviceCredentialsByToken(tokens: $t) { credentialId device { token } } }",
                JsonDocument.Parse(VarsJson.StringList("t", new[] { plantToken + "-cred" })).RootElement, Json, Json);
            var rows = credData.GetProperty("deviceCredentialsByToken");
            if (rows.GetArrayLength() != 1) { Fail("plant credential not found"); return; }
            var credentialId = rows[0].GetProperty("credentialId").GetString();

            var mqttSw = Stopwatch.StartNew();
            var options = new DeviceChain.Sdk.Mqtt.MqttSessionOptions(new Uri(Str("mqttBroker")), Str("instanceId"), Str("tenant"), plantToken, credentialId)
            {
                Trust = MqttTrust.PinnedCa(File.ReadAllBytes(settings.Value.CaPemPath)),
            };
            await using (var session = new DeviceChain.Sdk.Mqtt.MqttDeviceSession(options))
            {
                try
                {
                    await session.StartAsync((c, _) => Task.FromResult(DeviceChain.Sdk.Mqtt.CommandOutcome.Failed("smoke probe accepts no commands")), CancellationToken.None);
                    Line($"mqtt session ready in {mqttSw.ElapsedMilliseconds} ms");
                    var publisher = new DeviceChain.Sdk.Ingest.DeviceEventPublisher(new DeviceChain.Sdk.Ingest.MqttDeviceEventCarrier(session, plantToken));
                    await publisher.EmitMeasurementsAsync(plantToken, credentialId, new Dictionary<string, double> { ["throughput_tph"] = marker });
                    Line($"published throughput_tph={marker} as SP-PL-0001 at {sw.ElapsedMilliseconds} ms");
                }
                catch (Exception e)
                {
                    Fail($"mqtt after {mqttSw.ElapsedMilliseconds} ms: {Chain(e)}");
                }

                await listen;
            }

            await ws.DisposeAsync();
            Line($"measurementStream · open {opened} ms · our measurement {(arrived < 0 ? "NEVER arrived" : "arrived at " + arrived + " ms")}");
            if (arrived < 0) Fail("the published measurement did not come back over the subscription");
        }

        private async Task TimeTcp(string host, int port, AddressFamily family)
        {
            var sw = Stopwatch.StartNew();
            try
            {
                using var client = family == AddressFamily.Unspecified ? new TcpClient() : new TcpClient(family);
                var connect = client.ConnectAsync(host, port);
                var done = await Task.WhenAny(connect, Task.Delay(30000));
                if (done != connect) { Line($"tcp {host}:{port} family={family} TIMEOUT after {sw.ElapsedMilliseconds} ms"); return; }
                await connect;
                Line($"tcp {host}:{port} family={family} connected in {sw.ElapsedMilliseconds} ms via {((IPEndPoint)client.Client.RemoteEndPoint).Address}");
            }
            catch (Exception e)
            {
                Line($"tcp {host}:{port} family={family} failed after {sw.ElapsedMilliseconds} ms: {e.GetType().Name}: {e.Message}");
            }
        }

        private static string Chain(Exception e)
        {
            var sb = new StringBuilder();
            for (var x = e; x != null; x = x.InnerException) sb.Append(" ⟶ ").Append(x.GetType().Name).Append(": ").Append(x.Message);
            return sb.ToString();
        }

        private static string ScriptingBackend()
        {
#if ENABLE_IL2CPP
            return "IL2CPP";
#else
            return "Mono";
#endif
        }

        private void Line(string s)
        {
            _report.AppendLine(s);
            Debug.Log("[live-smoke] " + s);
        }

        private void Fail(string s)
        {
            _failures++;
            Line("FAIL " + s);
        }
    }
}
