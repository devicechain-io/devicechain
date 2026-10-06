// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Net.Security;
using System.Net.Sockets;
using System.Net.WebSockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// The default <see cref="IWebSocketFactory"/> — yields connections that dial the endpoint's
/// addresses themselves (see <see cref="ClientWebSocketConnection"/>).
/// </summary>
public sealed class ClientWebSocketFactory : IWebSocketFactory
{
    /// <inheritdoc />
    public IWebSocketConnection Create() => new ClientWebSocketConnection();
}

/// <summary>
/// The default <see cref="IWebSocketConnection"/>. It opens the TCP connection itself, racing the
/// host's resolved addresses so a name such as <c>localhost</c> does not stall on an address nothing
/// listens on, upgrades it with the WebSocket opening handshake, and hands the stream to a
/// <see cref="WebSocket"/>. The <c>Host</c> header and TLS server name stay the endpoint's host.
/// This does not use <see cref="ClientWebSocket"/>, so it does not use a system HTTP proxy. It
/// reassembles underlying frames into whole text messages and maps a server close into a
/// <see cref="WebSocketMessageKind.Closed"/> message (carrying the spec close code) instead of
/// throwing, so the subscription client owns the close-to-exception mapping.
/// </summary>
public sealed class ClientWebSocketConnection : IWebSocketConnection
{
    private readonly CancellationTokenSource _lifetime = new();
    private WebSocket? _ws;
    private Stream? _stream;

    /// <inheritdoc />
    public bool IsOpen => _ws?.State == WebSocketState.Open;

    /// <inheritdoc />
    public async Task ConnectAsync(Uri endpoint, string subProtocol, CancellationToken cancellationToken)
    {
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _lifetime.Token);
        CancellationToken token = linked.Token;
        bool secure = endpoint.Scheme == "wss" || endpoint.Scheme == "https";
        Socket? tcp = null;
        Stream? stream = null;
        try
        {
            tcp = await AddressDialer.ConnectAsync(endpoint.IdnHost, endpoint.Port, token).ConfigureAwait(false);
            stream = new NetworkStream(tcp, ownsSocket: true);
            // The stream is disposed on cancellation so a handshake that is waiting on the network stops.
            using (token.Register(static s => ((Stream)s!).Dispose(), stream))
            {
                if (secure)
                {
                    var tls = new SslStream(stream, leaveInnerStreamOpen: false);
                    stream = tls;
                    // The server name is the URI's host, never the address that happened to connect.
                    await tls.AuthenticateAsClientAsync(endpoint.IdnHost).ConfigureAwait(false);
                }
                await WebSocketUpgrade.PerformAsync(stream, endpoint, subProtocol, token).ConfigureAwait(false);
            }
            token.ThrowIfCancellationRequested();
            _stream = stream;
            _ws = WebSocket.CreateFromStream(
                stream, isServer: false, string.IsNullOrEmpty(subProtocol) ? null : subProtocol, WebSocket.DefaultKeepAliveInterval);
        }
        catch
        {
            if (stream != null)
            {
                stream.Dispose();
            }
            else
            {
                tcp?.Dispose();
            }
            token.ThrowIfCancellationRequested();
            throw;
        }
    }

    /// <inheritdoc />
    public Task SendTextAsync(byte[] utf8Payload, CancellationToken cancellationToken) =>
        Connected.SendAsync(new ArraySegment<byte>(utf8Payload), WebSocketMessageType.Text, endOfMessage: true, cancellationToken);

    /// <inheritdoc />
    public async Task<WebSocketMessage> ReceiveAsync(CancellationToken cancellationToken)
    {
        WebSocket ws = Connected;
        var buffer = new byte[8192];
        using var ms = new MemoryStream();
        WebSocketReceiveResult result;
        do
        {
            result = await ws.ReceiveAsync(new ArraySegment<byte>(buffer), cancellationToken).ConfigureAwait(false);
            if (result.MessageType == WebSocketMessageType.Close)
            {
                return WebSocketMessage.OfClose((int?)result.CloseStatus, result.CloseStatusDescription);
            }
            ms.Write(buffer, 0, result.Count);
        }
        while (!result.EndOfMessage);
        return WebSocketMessage.OfText(Encoding.UTF8.GetString(ms.ToArray()));
    }

    /// <inheritdoc />
    public void Abort()
    {
        _lifetime.Cancel(); // also stops a connect still in flight
        _ws?.Abort();
        _stream?.Dispose();
    }

    /// <inheritdoc />
    public void Dispose()
    {
        _lifetime.Cancel();
        _ws?.Dispose();
        _stream?.Dispose();
        _lifetime.Dispose();
    }

    private WebSocket Connected => _ws ?? throw new InvalidOperationException("The WebSocket is not connected.");
}
