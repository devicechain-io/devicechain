// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Net;
using System.Net.Security;
using System.Net.Sockets;
using System.Net.WebSockets;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// Drives ClientWebSocketConnection (the dial + upgrade the SDK actually ships) against a
// scripted in-process TCP server, so every byte of the opening handshake is under test control.
public class WebSocketUpgradeTests
{
    private const string AcceptGuid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

    private sealed class Server : IDisposable
    {
        private readonly TcpListener _listener = new(IPAddress.Loopback, 0);
        private readonly Func<string, Stream, Task> _handler;
        private readonly TaskCompletionSource<string> _request = new();
        private readonly X509Certificate2? _cert;
        private readonly TaskCompletionSource<string?> _sni = new();
        private TcpClient? _client;

        public Server(Func<string, Stream, Task> handler, X509Certificate2? cert = null)
        {
            _handler = handler;
            _cert = cert;
            _listener.Start();
            _ = Run();
        }

        public int Port => ((IPEndPoint)_listener.LocalEndpoint).Port;

        public Task<string> Request => _request.Task;

        public Task<string?> ServerName => _sni.Task;

        private async Task Run()
        {
            try
            {
                // Held in a field: an unrooted TcpClient is finalized, which resets the connection mid-test.
                _client = await _listener.AcceptTcpClientAsync();
                TcpClient client = _client;
                Stream stream = client.GetStream();
                if (_cert != null)
                {
                    var tls = new SslStream(stream);
                    await tls.AuthenticateAsServerAsync(new SslServerAuthenticationOptions
                    {
                        ServerCertificateSelectionCallback = (_, host) =>
                        {
                            _sni.TrySetResult(host);
                            return _cert;
                        },
                    });
                    stream = tls;
                }
                string request = await ReadRequest(stream);
                _request.TrySetResult(request);
                await _handler(request, stream);
            }
            catch (Exception ex)
            {
                _request.TrySetException(ex);
                _sni.TrySetResult(null);
            }
        }

        public void Dispose()
        {
            _listener.Stop();
            _client?.Dispose();
        }
    }

    private static async Task<string> ReadRequest(Stream s)
    {
        var sb = new StringBuilder();
        var one = new byte[1];
        while (!sb.ToString().EndsWith("\r\n\r\n", StringComparison.Ordinal))
        {
            if (await s.ReadAsync(one, 0, 1) == 0)
            {
                break;
            }
            sb.Append((char)one[0]);
        }
        return sb.ToString();
    }

    private static string Header(string request, string name)
    {
        foreach (string line in request.Split("\r\n"))
        {
            if (line.StartsWith(name + ":", StringComparison.OrdinalIgnoreCase))
            {
                return line.Substring(name.Length + 1).Trim();
            }
        }
        return "";
    }

    private static string AcceptFor(string request) =>
        Convert.ToBase64String(SHA1.HashData(Encoding.ASCII.GetBytes(Header(request, "Sec-WebSocket-Key") + AcceptGuid)));

    private static byte[] Ok(string request, string? protocol = "graphql-transport-ws", string? accept = null) =>
        Encoding.ASCII.GetBytes(
            "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
            $"Sec-WebSocket-Accept: {accept ?? AcceptFor(request)}\r\n" +
            (protocol != null ? $"Sec-WebSocket-Protocol: {protocol}\r\n" : "") + "\r\n");

    private static byte[] TextFrame(string text)
    {
        byte[] payload = Encoding.UTF8.GetBytes(text);
        Assert.True(payload.Length < 126);
        var frame = new byte[2 + payload.Length];
        frame[0] = 0x81;
        frame[1] = (byte)payload.Length;
        payload.CopyTo(frame, 2);
        return frame;
    }

    private static async Task<Exception> ConnectFailure(Server server, string subProtocol = "graphql-transport-ws")
    {
        using var conn = new ClientWebSocketConnection();
        return await Assert.ThrowsAnyAsync<Exception>(() =>
            conn.ConnectAsync(new Uri($"ws://127.0.0.1:{server.Port}/x"), subProtocol, CancellationToken.None));
    }

    [Fact]
    public async Task A_valid_upgrade_opens_a_socket_that_round_trips_a_text_frame()
    {
        using var server = new Server(async (req, s) =>
        {
            await s.WriteAsync(Ok(req));
            using WebSocket ws = WebSocket.CreateFromStream(s, isServer: true, "graphql-transport-ws", TimeSpan.Zero);
            var buf = new byte[256];
            WebSocketReceiveResult r = await ws.ReceiveAsync(buf, CancellationToken.None);
            await ws.SendAsync(buf.AsMemory(0, r.Count), WebSocketMessageType.Text, true, CancellationToken.None);
        });
        using var conn = new ClientWebSocketConnection();

        await conn.ConnectAsync(new Uri($"ws://localhost:{server.Port}/api/graphql?a=1"), "graphql-transport-ws", CancellationToken.None);
        Assert.True(conn.IsOpen);
        await conn.SendTextAsync(Encoding.UTF8.GetBytes("ping"), CancellationToken.None);
        WebSocketMessage echoed = await conn.ReceiveAsync(CancellationToken.None);

        Assert.Equal("ping", echoed.Text);
        string request = await server.Request;
        Assert.StartsWith("GET /api/graphql?a=1 HTTP/1.1\r\n", request);
        Assert.Equal($"localhost:{server.Port}", Header(request, "Host")); // the URI's host and port, not the address dialled
        Assert.Equal("websocket", Header(request, "Upgrade"));
        Assert.Equal("Upgrade", Header(request, "Connection"));
        Assert.Equal("13", Header(request, "Sec-WebSocket-Version"));
        Assert.Equal("graphql-transport-ws", Header(request, "Sec-WebSocket-Protocol"));
        Assert.Equal(16, Convert.FromBase64String(Header(request, "Sec-WebSocket-Key")).Length);
    }

    [Fact]
    public void The_host_header_omits_a_default_port_and_brackets_an_ipv6_literal()
    {
        Assert.Contains("Host: example.test\r\n", WebSocketUpgrade.BuildRequest(new Uri("ws://example.test/p"), "k", null));
        Assert.Contains("Host: example.test\r\n", WebSocketUpgrade.BuildRequest(new Uri("wss://example.test/p"), "k", null));
        Assert.Contains("Host: example.test:8443\r\n", WebSocketUpgrade.BuildRequest(new Uri("wss://example.test:8443/p"), "k", null));
        Assert.Contains("Host: example.test:443\r\n", WebSocketUpgrade.BuildRequest(new Uri("ws://example.test:443/p"), "k", null));
        Assert.Contains("Host: [::1]:81\r\n", WebSocketUpgrade.BuildRequest(new Uri("ws://[::1]:81/p"), "k", null));
        Assert.DoesNotContain("Sec-WebSocket-Protocol", WebSocketUpgrade.BuildRequest(new Uri("ws://example.test/p"), "k", null));
    }

    [Fact]
    public async Task A_wrong_accept_value_is_refused_naming_the_header()
    {
        using var server = new Server(async (req, s) => await s.WriteAsync(Ok(req, accept: "AAAAAAAAAAAAAAAAAAAAAAAAAAA=")));

        Exception ex = await ConnectFailure(server);

        Assert.IsType<WebSocketException>(ex);
        Assert.Contains("Sec-WebSocket-Accept", ex.Message);
    }

    [Theory]
    [InlineData(null)]
    [InlineData("some-other-protocol")]
    public async Task A_missing_or_different_subprotocol_is_refused(string? selected)
    {
        using var server = new Server(async (req, s) => await s.WriteAsync(Ok(req, protocol: selected)));

        Exception ex = await ConnectFailure(server);

        Assert.IsType<WebSocketException>(ex);
        Assert.Contains("subprotocol", ex.Message);
    }

    [Fact]
    public async Task A_refused_upgrade_surfaces_the_status_line()
    {
        using var server = new Server(async (_, s) =>
            await s.WriteAsync(Encoding.ASCII.GetBytes("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")));

        Exception ex = await ConnectFailure(server);

        Assert.IsType<WebSocketException>(ex);
        Assert.Contains("404", ex.Message);
        Assert.Contains("Not Found", ex.Message);
    }

    [Fact]
    public async Task A_wrong_upgrade_or_connection_header_is_refused()
    {
        using var noUpgrade = new Server(async (req, s) => await s.WriteAsync(Encoding.ASCII.GetBytes(
            $"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: {AcceptFor(req)}\r\nSec-WebSocket-Protocol: graphql-transport-ws\r\n\r\n")));
        Assert.Contains("Upgrade", (await ConnectFailure(noUpgrade)).Message);

        using var noConnection = new Server(async (req, s) => await s.WriteAsync(Encoding.ASCII.GetBytes(
            $"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: {AcceptFor(req)}\r\nSec-WebSocket-Protocol: graphql-transport-ws\r\n\r\n")));
        Assert.Contains("Connection", (await ConnectFailure(noConnection)).Message);
    }

    [Fact]
    public async Task A_header_block_over_the_cap_is_refused()
    {
        using var server = new Server(async (_, s) =>
        {
            await s.WriteAsync(Encoding.ASCII.GetBytes("HTTP/1.1 101 Switching Protocols\r\nX-Pad: "));
            await s.WriteAsync(Encoding.ASCII.GetBytes(new string('a', WebSocketUpgrade.MaxHeaderBytes + 100)));
        });

        Exception ex = await ConnectFailure(server);

        Assert.IsType<WebSocketException>(ex);
        Assert.Contains("exceeded", ex.Message);
    }

    [Fact]
    public async Task A_frame_sent_in_the_same_write_as_the_101_is_not_lost()
    {
        using var server = new Server(async (req, s) =>
        {
            byte[] head = Ok(req);
            byte[] frame = TextFrame("early");
            var both = new byte[head.Length + frame.Length];
            head.CopyTo(both, 0);
            frame.CopyTo(both, head.Length);
            await s.WriteAsync(both); // one TCP write: header block and first frame together
            await Task.Delay(1000);
        });
        using var conn = new ClientWebSocketConnection();

        await conn.ConnectAsync(new Uri($"ws://127.0.0.1:{server.Port}/x"), "graphql-transport-ws", CancellationToken.None);
        WebSocketMessage first = await conn.ReceiveAsync(CancellationToken.None);

        Assert.Equal("early", first.Text);
    }

    [Fact]
    public async Task A_server_that_hangs_up_mid_handshake_fails_the_connect()
    {
        using var server = new Server((_, s) =>
        {
            s.Dispose();
            return Task.CompletedTask;
        });

        Exception ex = await ConnectFailure(server);

        Assert.IsType<WebSocketException>(ex);
    }

    [Fact]
    public async Task Cancelling_a_connect_stuck_in_the_handshake_stops_it()
    {
        using var server = new Server(async (_, _) => await Task.Delay(Timeout.Infinite));
        using var conn = new ClientWebSocketConnection();
        using var cts = new CancellationTokenSource(200);

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() =>
            conn.ConnectAsync(new Uri($"ws://127.0.0.1:{server.Port}/x"), "graphql-transport-ws", cts.Token));
    }

    [Fact]
    public async Task Abort_and_dispose_before_connect_are_safe()
    {
        var conn = new ClientWebSocketConnection();
        Assert.False(conn.IsOpen);
        conn.Abort();
        conn.Dispose();
        await Task.CompletedTask;
    }

    [Fact]
    public async Task Abort_stops_a_connect_still_in_the_handshake()
    {
        using var server = new Server(async (_, _) => await Task.Delay(Timeout.Infinite));
        using var conn = new ClientWebSocketConnection();
        Task connect = conn.ConnectAsync(new Uri($"ws://127.0.0.1:{server.Port}/x"), "graphql-transport-ws", CancellationToken.None);
        await server.Request;

        conn.Abort();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => connect);
    }

    [Fact]
    public async Task A_secure_connect_names_the_uris_host_not_the_address_in_the_tls_handshake()
    {
        using RSA rsa = RSA.Create(2048);
        var req = new CertificateRequest("CN=localhost", rsa, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        using X509Certificate2 cert = req.CreateSelfSigned(DateTimeOffset.UtcNow.AddDays(-1), DateTimeOffset.UtcNow.AddDays(1));
        using var server = new Server((_, _) => Task.CompletedTask, cert);
        using var conn = new ClientWebSocketConnection();

        // The self-signed certificate is not trusted, so the connect fails — after the server has
        // seen which name the client asked for, which is what is asserted.
        await Assert.ThrowsAnyAsync<Exception>(() =>
            conn.ConnectAsync(new Uri($"wss://localhost:{server.Port}/x"), "graphql-transport-ws", CancellationToken.None));

        Assert.Equal("localhost", await server.ServerName);
    }
}
