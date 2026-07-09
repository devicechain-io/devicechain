// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// Creates <see cref="IWebSocketConnection"/>s — the seam that lets the subscription client run
/// over something other than <c>System.Net.WebSockets.ClientWebSocket</c>. On plain .NET the
/// default <see cref="ClientWebSocketFactory"/> is used; under Unity WebGL (where
/// <c>ClientWebSocket</c> does not work) a host injects a factory backed by a browser
/// <c>WebSocket</c> via a <c>.jslib</c> interop shim. One factory yields a fresh connection per
/// socket generation (a dropped socket is replaced, never reused).
/// </summary>
public interface IWebSocketFactory
{
    /// <summary>Creates a new, unconnected connection.</summary>
    IWebSocketConnection Create();
}

/// <summary>
/// A single text-framed WebSocket connection speaking whole messages (reassembly of any
/// underlying frames is the transport's concern — a browser <c>WebSocket</c> already delivers
/// whole messages). The subscription client drives exactly these operations, so an alternate
/// transport need implement only this surface.
/// </summary>
public interface IWebSocketConnection : IDisposable
{
    /// <summary>Opens the connection, negotiating <paramref name="subProtocol"/>.</summary>
    Task ConnectAsync(Uri endpoint, string subProtocol, CancellationToken cancellationToken);

    /// <summary>True while the socket is open and usable.</summary>
    bool IsOpen { get; }

    /// <summary>Sends one whole UTF-8 text message.</summary>
    Task SendTextAsync(byte[] utf8Payload, CancellationToken cancellationToken);

    /// <summary>
    /// Receives the next whole message. A close is returned as a
    /// <see cref="WebSocketMessageKind.Closed"/> message (carrying the close code) rather than
    /// thrown, so the caller owns the close-to-exception mapping.
    /// </summary>
    Task<WebSocketMessage> ReceiveAsync(CancellationToken cancellationToken);

    /// <summary>Forcibly aborts the connection so a pending receive unblocks and the socket is no longer open.</summary>
    void Abort();
}

/// <summary>Whether a received message carries text or signals the socket closed.</summary>
public enum WebSocketMessageKind
{
    /// <summary>A text message; <see cref="WebSocketMessage.Text"/> is set.</summary>
    Text,

    /// <summary>The socket closed; <see cref="WebSocketMessage.CloseCode"/>/<see cref="WebSocketMessage.CloseReason"/> describe it.</summary>
    Closed,
}

/// <summary>One received message: either text, or a close signal with its code/reason.</summary>
public readonly struct WebSocketMessage
{
    private WebSocketMessage(WebSocketMessageKind kind, string? text, int? closeCode, string? closeReason)
    {
        Kind = kind;
        Text = text;
        CloseCode = closeCode;
        CloseReason = closeReason;
    }

    /// <summary>The message kind.</summary>
    public WebSocketMessageKind Kind { get; }

    /// <summary>The text payload for a <see cref="WebSocketMessageKind.Text"/> message.</summary>
    public string? Text { get; }

    /// <summary>The close code (e.g. 4401 invalid token, 4429 rate-limited) for a close message.</summary>
    public int? CloseCode { get; }

    /// <summary>The close reason for a close message, if any.</summary>
    public string? CloseReason { get; }

    /// <summary>A text message.</summary>
    public static WebSocketMessage OfText(string text) =>
        new(WebSocketMessageKind.Text, text, null, null);

    /// <summary>A close signal.</summary>
    public static WebSocketMessage OfClose(int? code, string? reason) =>
        new(WebSocketMessageKind.Closed, null, code, reason);
}
