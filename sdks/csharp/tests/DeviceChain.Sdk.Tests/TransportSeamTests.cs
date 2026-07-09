// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Channels;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Subscriptions;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// Proves the slice-4 transport seam: the SDK drives GraphQL, device-emit, and live subscriptions
// over INJECTED transports that are neither HttpClient nor ClientWebSocket — the exact substitution
// Unity WebGL needs (UnityWebRequest + a .jslib browser-WebSocket shim). If these pass, the WebGL
// plugin can implement IHttpTransport/IWebSocketFactory without touching the SDK.
public class TransportSeamTests
{
    // ── HTTP seam ────────────────────────────────────────────────────────────

    private sealed class FakeHttpTransport : IHttpTransport
    {
        private readonly Func<HttpTransportRequest, HttpTransportResponse> _responder;
        public List<HttpTransportRequest> Requests { get; } = new();

        public FakeHttpTransport(Func<HttpTransportRequest, HttpTransportResponse> responder) => _responder = responder;

        public Task<HttpTransportResponse> SendAsync(HttpTransportRequest request, CancellationToken cancellationToken)
        {
            Requests.Add(request);
            return Task.FromResult(_responder(request));
        }
    }

    private static HttpTransportResponse Ok(string json) =>
        new() { Status = 200, Body = Encoding.UTF8.GetBytes(json) };

    [Fact]
    public async Task GraphQlClient_runs_over_an_injected_http_transport()
    {
        var transport = new FakeHttpTransport(_ => Ok("{\"data\":{\"name\":\"widget\"}}"));
        var client = new GraphQlClient(transport, new Uri("https://test.local"), _ => new ValueTask<string?>("tok-abc"));

        Thing thing = await client.SendAsync(
            Area.DeviceManagement, "query{thing{name}}", EmptyVariables.Value,
            TestJson.Default.EmptyVariables, TestJson.Default.Thing);

        Assert.Equal("widget", thing.Name);
        HttpTransportRequest sent = Assert.Single(transport.Requests);
        Assert.Equal("/api/device-management/graphql", sent.Uri.AbsolutePath);
        Assert.Equal("tok-abc", sent.BearerToken); // resolved token flows to the transport, not a header the SDK builds
        Assert.Contains("query{thing{name}}", Encoding.UTF8.GetString(sent.Body!));
    }

    [Fact]
    public async Task DeviceEventPublisher_runs_over_an_injected_http_transport()
    {
        var transport = new FakeHttpTransport(_ => new HttpTransportResponse { Status = 202 });
        var pub = new DeviceEventPublisher(transport, new Uri("https://ingress.local"), "dc", "acme");

        await pub.EmitMeasurementsAsync(
            "dev-1", "cred-9", new Dictionary<string, double> { ["temperature"] = 21.5 });

        HttpTransportRequest sent = Assert.Single(transport.Requests);
        Assert.Equal("/dc/acme/events", sent.Uri.AbsolutePath);
        Assert.Null(sent.BearerToken); // a device presents its credential in-body, never a Bearer
        string body = Encoding.UTF8.GetString(sent.Body!);
        Assert.Contains("\"credentialId\":\"cred-9\"", body);
        Assert.Contains("\"temperature\":\"21.5\"", body);
    }

    [Fact]
    public async Task DeviceEventPublisher_over_injected_transport_throws_on_non_202()
    {
        var transport = new FakeHttpTransport(_ =>
            new HttpTransportResponse { Status = 429, Body = Encoding.UTF8.GetBytes("slow down") });
        var pub = new DeviceEventPublisher(transport, new Uri("https://ingress.local"), "dc", "acme");

        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() =>
            pub.EmitMeasurementsAsync("dev-1", "cred-9", new Dictionary<string, double> { ["t"] = 1 }));
        Assert.Equal(429, ex.Status);
        Assert.Contains("slow down", ex.Message);
    }

    // ── WebSocket seam ───────────────────────────────────────────────────────

    // An in-memory graphql-transport-ws server behind IWebSocketConnection: the client's sends are
    // parsed and scripted replies (text OR a close) are queued for its receives — the whole
    // protocol, zero sockets.
    private sealed class FakeWebSocketFactory : IWebSocketFactory
    {
        private readonly Func<string, IReadOnlyList<WebSocketMessage>> _respond;
        public FakeWebSocketConnection? Last { get; private set; }

        public FakeWebSocketFactory(Func<string, IReadOnlyList<WebSocketMessage>> respond) => _respond = respond;

        public IWebSocketConnection Create() => Last = new FakeWebSocketConnection(_respond);
    }

    private sealed class FakeWebSocketConnection : IWebSocketConnection
    {
        private readonly Func<string, IReadOnlyList<WebSocketMessage>> _respond;
        private readonly Channel<WebSocketMessage> _inbound = Channel.CreateUnbounded<WebSocketMessage>();
        private volatile bool _open;
        public List<string> Sent { get; } = new();

        public FakeWebSocketConnection(Func<string, IReadOnlyList<WebSocketMessage>> respond) => _respond = respond;

        public bool IsOpen => _open;

        public Task ConnectAsync(Uri endpoint, string subProtocol, CancellationToken cancellationToken)
        {
            Assert.Equal("graphql-transport-ws", subProtocol);
            _open = true;
            return Task.CompletedTask;
        }

        public Task SendTextAsync(byte[] utf8Payload, CancellationToken cancellationToken)
        {
            string frame = Encoding.UTF8.GetString(utf8Payload);
            Sent.Add(frame);
            foreach (WebSocketMessage reply in _respond(frame))
            {
                // A queued close stays IN the stream — _open flips only when the read loop reads it
                // and aborts, so the loop reliably delivers the close frame (no IsOpen race).
                _inbound.Writer.TryWrite(reply);
            }
            return Task.CompletedTask;
        }

        public async Task<WebSocketMessage> ReceiveAsync(CancellationToken cancellationToken)
        {
            try
            {
                return await _inbound.Reader.ReadAsync(cancellationToken).ConfigureAwait(false);
            }
            catch (ChannelClosedException)
            {
                return WebSocketMessage.OfClose(1000, "closed");
            }
        }

        public void Abort()
        {
            _open = false;
            _inbound.Writer.TryComplete();
        }

        public void Dispose()
        {
            _open = false;
            _inbound.Writer.TryComplete();
        }
    }

    private static IReadOnlyList<WebSocketMessage> Text(params string[] frames)
    {
        var list = new List<WebSocketMessage>(frames.Length);
        foreach (string f in frames)
        {
            list.Add(WebSocketMessage.OfText(f));
        }
        return list;
    }

    [Fact]
    public async Task GraphQlWsClient_runs_the_whole_protocol_over_an_injected_factory()
    {
        var factory = new FakeWebSocketFactory(frame =>
        {
            using var doc = JsonDocument.Parse(frame);
            string type = doc.RootElement.GetProperty("type").GetString()!;
            switch (type)
            {
                case "connection_init":
                    return Text("{\"type\":\"connection_ack\"}");
                case "subscribe":
                    string id = doc.RootElement.GetProperty("id").GetString()!;
                    return Text(
                        $"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"a\"}}}}}}",
                        $"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"b\"}}}}}}",
                        $"{{\"id\":\"{id}\",\"type\":\"complete\"}}");
                default:
                    return Array.Empty<WebSocketMessage>();
            }
        });

        await using var client = new GraphQlWsClient(factory, new Uri("wss://host/api/event-management/graphql"),
            _ => new ValueTask<string?>("tok-xyz"));

        var names = new List<string>();
        await foreach (Thing thing in client.SubscribeAsync(
            "subscription{x}", EmptyVariables.Value, TestJson.Default.EmptyVariables, TestJson.Default.Thing))
        {
            names.Add(thing.Name);
        }

        Assert.Equal(new[] { "a", "b" }, names);
        // The token rode in connection_init (never a query param), proving the auth seam survives the swap.
        Assert.Contains("Bearer tok-xyz", factory.Last!.Sent[0]);
        Assert.Contains("connection_init", factory.Last!.Sent[0]);
    }

    [Fact]
    public async Task GraphQlWsClient_surfaces_a_server_close_code_over_an_injected_factory()
    {
        // The fake acks, then answers the subscribe with a close carrying a spec code (4401 =
        // invalid token) — the client must throw with that code, not hang.
        var factory = new FakeWebSocketFactory(frame =>
        {
            using var doc = JsonDocument.Parse(frame);
            return doc.RootElement.GetProperty("type").GetString() switch
            {
                "connection_init" => Text("{\"type\":\"connection_ack\"}"),
                "subscribe" => new[] { WebSocketMessage.OfClose(4401, "invalid token") },
                _ => Array.Empty<WebSocketMessage>(),
            };
        });

        await using var client = new GraphQlWsClient(factory, new Uri("wss://host/api/event-management/graphql"));
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(10));

        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(async () =>
        {
            await foreach (Thing _ in client.SubscribeAsync(
                "subscription{x}", EmptyVariables.Value, TestJson.Default.EmptyVariables, TestJson.Default.Thing, cts.Token))
            {
            }
        });
        Assert.Contains("4401", ex.Message);
        Assert.False(cts.IsCancellationRequested, "the stream should have faulted on close, not hung");
    }

    // ── facade over both injected transports (the Unity WebGL entry point) ─────

    [Fact]
    public async Task DeviceChainClient_facade_wires_gql_subscriptions_and_emit_over_injected_transports()
    {
        // One HTTP transport serving BOTH the GraphQL origin and the device-plane ingress by path.
        var http = new FakeHttpTransport(req =>
            req.Uri.AbsolutePath.EndsWith("/events", StringComparison.Ordinal)
                ? new HttpTransportResponse { Status = 202 }
                : Ok("{\"data\":{\"name\":\"widget\"}}"));

        var wsFactory = new FakeWebSocketFactory(frame =>
        {
            using var doc = JsonDocument.Parse(frame);
            string type = doc.RootElement.GetProperty("type").GetString()!;
            if (type == "connection_init") return Text("{\"type\":\"connection_ack\"}");
            if (type == "subscribe")
            {
                string id = doc.RootElement.GetProperty("id").GetString()!;
                return Text(
                    $"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"live\"}}}}}}",
                    $"{{\"id\":\"{id}\",\"type\":\"complete\"}}");
            }
            return Array.Empty<WebSocketMessage>();
        });

        await using var client = new DeviceChainClient(new Uri("https://demo.local"), http, wsFactory);

        // Gql runs over the injected HTTP transport.
        Thing queried = await client.Gql.SendAsync(
            Area.DeviceManagement, "query{thing{name}}", EmptyVariables.Value,
            TestJson.Default.EmptyVariables, TestJson.Default.Thing, new RequestOptions { Anonymous = true });
        Assert.Equal("widget", queried.Name);

        // Subscriptions run over the injected WebSocket factory.
        var names = new List<string>();
        await foreach (Thing thing in client.Subscriptions(Area.EventManagement).SubscribeAsync(
            "subscription{x}", EmptyVariables.Value, TestJson.Default.EmptyVariables, TestJson.Default.Thing))
        {
            names.Add(thing.Name);
        }
        Assert.Equal(new[] { "live" }, names);

        // Device-plane emit runs over the injected HTTP transport.
        await client.DevicePublisher(new Uri("https://ingress.local"), "dc", "acme")
            .EmitMeasurementsAsync("dev-1", "cred-9", new Dictionary<string, double> { ["t"] = 1 });

        Assert.Contains(http.Requests, r => r.Uri.AbsolutePath == "/api/device-management/graphql");
        Assert.Contains(http.Requests, r => r.Uri.AbsolutePath == "/dc/acme/events");
    }
}
