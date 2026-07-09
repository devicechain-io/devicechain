// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Net.WebSockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>The default <see cref="IWebSocketFactory"/> — yields <see cref="ClientWebSocket"/>-backed connections.</summary>
public sealed class ClientWebSocketFactory : IWebSocketFactory
{
    /// <inheritdoc />
    public IWebSocketConnection Create() => new ClientWebSocketConnection();
}

/// <summary>
/// The default <see cref="IWebSocketConnection"/> over <see cref="ClientWebSocket"/>. It reassembles
/// underlying frames into whole text messages and maps a server close into a
/// <see cref="WebSocketMessageKind.Closed"/> message (carrying the spec close code) instead of
/// throwing, so the subscription client owns the close-to-exception mapping.
/// </summary>
public sealed class ClientWebSocketConnection : IWebSocketConnection
{
    private readonly ClientWebSocket _socket = new();

    /// <inheritdoc />
    public bool IsOpen => _socket.State == WebSocketState.Open;

    /// <inheritdoc />
    public async Task ConnectAsync(Uri endpoint, string subProtocol, CancellationToken cancellationToken)
    {
        if (!string.IsNullOrEmpty(subProtocol))
        {
            _socket.Options.AddSubProtocol(subProtocol);
        }
        await _socket.ConnectAsync(endpoint, cancellationToken).ConfigureAwait(false);
    }

    /// <inheritdoc />
    public Task SendTextAsync(byte[] utf8Payload, CancellationToken cancellationToken) =>
        _socket.SendAsync(new ArraySegment<byte>(utf8Payload), WebSocketMessageType.Text, endOfMessage: true, cancellationToken);

    /// <inheritdoc />
    public async Task<WebSocketMessage> ReceiveAsync(CancellationToken cancellationToken)
    {
        var buffer = new byte[8192];
        using var ms = new MemoryStream();
        WebSocketReceiveResult result;
        do
        {
            result = await _socket.ReceiveAsync(new ArraySegment<byte>(buffer), cancellationToken).ConfigureAwait(false);
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
    public void Abort() => _socket.Abort();

    /// <inheritdoc />
    public void Dispose() => _socket.Dispose();
}
