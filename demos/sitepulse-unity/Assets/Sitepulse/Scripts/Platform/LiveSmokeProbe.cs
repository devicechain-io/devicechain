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
    /// transport, and one GraphQL-over-WebSocket subscription over <c>ClientWebSocket</c> — the one
    /// path no IL2CPP player had run. It also times raw TCP connects to <c>localhost</c> by address
    /// family, because on Windows <c>localhost</c> resolves to <c>::1</c> first and, under WSL
    /// mirrored networking, nothing answers there: the connect hangs instead of being refused.
    /// Results go to the player log and to <c>live-smoke.log</c> beside it; the runner's token is
    /// never written anywhere. Quits when done, so it can run unattended
    /// (<c>-batchmode -nographics</c>).
    /// </summary>
    public sealed class LiveSmokeProbe : MonoBehaviour
    {
        [SerializeField] private string runnerUrl = "http://localhost:8090";
        [SerializeField] private float subscribeSeconds = 35f;
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

            await RawWebSocket(wsUrl);
            await RawWebSocket("ws://127.0.0.1/api/event-management/graphql");
            await DialedWebSocket(wsUrl, token);

            // 4. One subscription over ClientWebSocket: connect, first item, items in a window.
            var ws = new GraphQlWsClient(new ClientWebSocketFactory(), new Uri(wsUrl), tokens);
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(subscribeSeconds));
            var empty = JsonDocument.Parse("{}").RootElement;
            var count = 0;
            long first = -1;
            var seen = new HashSet<string>();
            sw.Restart();
            try
            {
                await foreach (var item in ws.SubscribeAsync("subscription { measurementStream { deviceToken name value occurredTime } }",
                                   empty, Json, Json, cts.Token))
                {
                    if (first < 0) first = sw.ElapsedMilliseconds;
                    count++;
                    seen.Add(item.GetProperty("measurementStream").GetProperty("name").GetString());
                }
            }
            catch (OperationCanceledException)
            {
                // the window closing is the expected end
            }
            catch (Exception e)
            {
                Fail($"measurementStream after {sw.ElapsedMilliseconds} ms: {Chain(e)}");
            }
            finally
            {
                await ws.DisposeAsync();
            }

            Line($"measurementStream · first item {(first < 0 ? "never" : first + " ms")} · {count} items in {subscribeSeconds:0} s · metrics: {string.Join(",", seen)}");
            if (count == 0) Fail("no measurement arrived over the subscription");
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

        private async Task RawWebSocket(string url)
        {
            var sw = Stopwatch.StartNew();
            using var socket = new System.Net.WebSockets.ClientWebSocket();
            socket.Options.AddSubProtocol("graphql-transport-ws");
            try
            {
                using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(30));
                await socket.ConnectAsync(new Uri(url), cts.Token);
                Line($"raw ClientWebSocket {url} → {socket.State} in {sw.ElapsedMilliseconds} ms (subprotocol {socket.SubProtocol})");
            }
            catch (Exception e)
            {
                Line($"raw ClientWebSocket {url} failed after {sw.ElapsedMilliseconds} ms: {Chain(e)}");
            }
        }

        // Prototype: race the resolver's addresses (each next family 250 ms after the last), keep the
        // first socket that connects, do the HTTP/1.1 upgrade by hand so Host stays the URI's host,
        // then let the runtime frame the stream.
        private async Task DialedWebSocket(string url, string token)
        {
            var sw = Stopwatch.StartNew();
            var uri = new Uri(url);
            try
            {
                var addrs = await Dns.GetHostAddressesAsync(uri.Host);
                var pending = new List<Task<Socket>>();
                var winner = (Socket)null;
                using var giveUp = new CancellationTokenSource(TimeSpan.FromSeconds(30));
                foreach (var a in addrs)
                {
                    var s = new Socket(a.AddressFamily, SocketType.Stream, ProtocolType.Tcp) { NoDelay = true };
                    pending.Add(s.ConnectAsync(a, uri.Port).ContinueWith(t => { if (t.IsFaulted) { s.Dispose(); throw t.Exception.InnerException; } return s; }));
                    var settled = await Task.WhenAny(Task.WhenAny(pending), Task.Delay(250));
                    winner = FirstConnected(pending);
                    if (winner != null) break;
                }
                while (winner == null && pending.Exists(t => !t.IsCompleted))
                {
                    await Task.WhenAny(pending.FindAll(t => !t.IsCompleted));
                    winner = FirstConnected(pending);
                }
                foreach (var t in pending) if (t.Status == TaskStatus.RanToCompletion && t.Result != winner) t.Result.Dispose();
                if (winner == null) throw new SocketException((int)SocketError.HostUnreachable);
                var dialMs = sw.ElapsedMilliseconds;
                var via = ((IPEndPoint)winner.RemoteEndPoint).Address;
                var stream = new NetworkStream(winner, ownsSocket: true);
                var key = Convert.ToBase64String(Guid.NewGuid().ToByteArray());
                var req = $"GET {uri.PathAndQuery} HTTP/1.1\r\nHost: {uri.Host}{(uri.IsDefaultPort ? "" : ":" + uri.Port)}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: graphql-transport-ws\r\n\r\n";
                var bytes = Encoding.ASCII.GetBytes(req);
                await stream.WriteAsync(bytes, 0, bytes.Length);
                var head = new StringBuilder();
                var one = new byte[1];
                while (!head.ToString().EndsWith("\r\n\r\n"))
                {
                    if (await stream.ReadAsync(one, 0, 1) == 0) throw new IOException("closed during handshake");
                    head.Append((char)one[0]);
                }
                var status = head.ToString().Split('\n')[0].Trim();
                if (!status.Contains(" 101 ")) throw new IOException("upgrade refused: " + status);
                var ws = System.Net.WebSockets.WebSocket.CreateFromStream(stream, false, "graphql-transport-ws", TimeSpan.FromSeconds(30));
                var init = Encoding.UTF8.GetBytes("{\"type\":\"connection_init\",\"payload\":{\"Authorization\":\"Bearer " + token + "\"}}");
                await ws.SendAsync(new ArraySegment<byte>(init), System.Net.WebSockets.WebSocketMessageType.Text, true, CancellationToken.None);
                var buf = new byte[4096];
                var r = await ws.ReceiveAsync(new ArraySegment<byte>(buf), giveUp.Token);
                var reply = Encoding.UTF8.GetString(buf, 0, r.Count);
                Line($"dialed WebSocket {url} → dial {dialMs} ms via {via}, open+ack {sw.ElapsedMilliseconds} ms, state={ws.State}, first frame type={(reply.Contains("connection_ack") ? "connection_ack" : reply.Substring(0, Math.Min(60, reply.Length)))}");
                await ws.CloseAsync(System.Net.WebSockets.WebSocketCloseStatus.NormalClosure, "", CancellationToken.None);
            }
            catch (Exception e)
            {
                Fail($"dialed WebSocket {url} failed after {sw.ElapsedMilliseconds} ms: {Chain(e)}");
            }
        }

        private static Socket FirstConnected(List<Task<Socket>> pending)
        {
            foreach (var t in pending) if (t.Status == TaskStatus.RanToCompletion) return t.Result;
            return null;
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
