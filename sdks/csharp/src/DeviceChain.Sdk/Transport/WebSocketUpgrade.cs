// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Net.WebSockets;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// The client side of the WebSocket opening handshake (an HTTP/1.1 upgrade) over an already
/// connected <see cref="Stream"/>. Once it returns, the stream carries WebSocket frames.
/// </summary>
internal static class WebSocketUpgrade
{
    /// <summary>The most response header bytes accepted before the server is treated as not speaking the protocol.</summary>
    internal const int MaxHeaderBytes = 16 * 1024;

    private const string AcceptGuid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

    /// <summary>
    /// Sends the upgrade request and validates the server's answer. Throws
    /// <see cref="WebSocketException"/> naming the status line or the header that failed.
    /// </summary>
    internal static async Task PerformAsync(Stream stream, Uri endpoint, string? subProtocol, CancellationToken cancellationToken)
    {
        var keyBytes = new byte[16];
        using (var rng = RandomNumberGenerator.Create())
        {
            rng.GetBytes(keyBytes);
        }
        string key = Convert.ToBase64String(keyBytes);

        byte[] request = Encoding.ASCII.GetBytes(BuildRequest(endpoint, key, subProtocol));
        await stream.WriteAsync(request, 0, request.Length, cancellationToken).ConfigureAwait(false);
        await stream.FlushAsync(cancellationToken).ConfigureAwait(false);

        string head = await ReadHeadAsync(stream, cancellationToken).ConfigureAwait(false);
        Validate(head, key, subProtocol);
    }

    internal static string BuildRequest(Uri endpoint, string key, string? subProtocol)
    {
        bool secure = endpoint.Scheme == "wss" || endpoint.Scheme == "https";
        string host = endpoint.IdnHost;
        if (host.IndexOf(':') >= 0)
        {
            host = "[" + host + "]"; // IPv6 literal
        }
        if (endpoint.Port != (secure ? 443 : 80))
        {
            host += ":" + endpoint.Port;
        }

        var sb = new StringBuilder();
        sb.Append("GET ").Append(endpoint.PathAndQuery).Append(" HTTP/1.1\r\n");
        sb.Append("Host: ").Append(host).Append("\r\n");
        sb.Append("Upgrade: websocket\r\n");
        sb.Append("Connection: Upgrade\r\n");
        sb.Append("Sec-WebSocket-Key: ").Append(key).Append("\r\n");
        sb.Append("Sec-WebSocket-Version: 13\r\n");
        if (!string.IsNullOrEmpty(subProtocol))
        {
            sb.Append("Sec-WebSocket-Protocol: ").Append(subProtocol).Append("\r\n");
        }
        sb.Append("\r\n");
        return sb.ToString();
    }

    // Reads one byte at a time so nothing past the blank line is consumed: the bytes that follow
    // the header block are WebSocket frames, and they belong to the WebSocket.
    private static async Task<string> ReadHeadAsync(Stream stream, CancellationToken cancellationToken)
    {
        var buffer = new byte[MaxHeaderBytes];
        var one = new byte[1];
        int length = 0;
        while (true)
        {
            int read = await stream.ReadAsync(one, 0, 1, cancellationToken).ConfigureAwait(false);
            if (read == 0)
            {
                throw new WebSocketException("The server closed the connection before completing the WebSocket handshake.");
            }
            buffer[length++] = one[0];
            if (length >= 4 && buffer[length - 4] == '\r' && buffer[length - 3] == '\n'
                && buffer[length - 2] == '\r' && buffer[length - 1] == '\n')
            {
                return Encoding.ASCII.GetString(buffer, 0, length - 4);
            }
            if (length == buffer.Length)
            {
                throw new WebSocketException(
                    $"The server's WebSocket handshake response exceeded {MaxHeaderBytes} bytes of headers.");
            }
        }
    }

    private static void Validate(string head, string key, string? subProtocol)
    {
        string[] lines = head.Split(new[] { "\r\n" }, StringSplitOptions.None);
        string status = lines[0];
        string[] parts = status.Split(new[] { ' ' }, 3);
        if (parts.Length < 2 || !parts[0].StartsWith("HTTP/1.", StringComparison.Ordinal) || parts[1] != "101")
        {
            throw new WebSocketException($"The server refused the WebSocket upgrade: {status}");
        }

        string? upgrade = null, connection = null, accept = null, protocol = null;
        for (int i = 1; i < lines.Length; i++)
        {
            int colon = lines[i].IndexOf(':');
            if (colon <= 0)
            {
                continue;
            }
            string name = lines[i].Substring(0, colon).Trim();
            string value = lines[i].Substring(colon + 1).Trim();
            if (name.Equals("Upgrade", StringComparison.OrdinalIgnoreCase)) { upgrade = value; }
            else if (name.Equals("Connection", StringComparison.OrdinalIgnoreCase)) { connection = connection == null ? value : connection + "," + value; }
            else if (name.Equals("Sec-WebSocket-Accept", StringComparison.OrdinalIgnoreCase)) { accept = value; }
            else if (name.Equals("Sec-WebSocket-Protocol", StringComparison.OrdinalIgnoreCase)) { protocol = value; }
        }

        if (!string.Equals(upgrade, "websocket", StringComparison.OrdinalIgnoreCase))
        {
            throw new WebSocketException($"The WebSocket handshake response has a missing or wrong Upgrade header ('{upgrade}').");
        }
        if (!HasToken(connection, "upgrade"))
        {
            throw new WebSocketException($"The WebSocket handshake response has a missing or wrong Connection header ('{connection}').");
        }

        string expected;
        using (var sha1 = SHA1.Create())
        {
            expected = Convert.ToBase64String(sha1.ComputeHash(Encoding.ASCII.GetBytes(key + AcceptGuid)));
        }
        if (!string.Equals(accept, expected, StringComparison.Ordinal))
        {
            throw new WebSocketException("The WebSocket handshake response has a missing or wrong Sec-WebSocket-Accept header.");
        }

        if (!string.IsNullOrEmpty(subProtocol) && !string.Equals(protocol, subProtocol, StringComparison.Ordinal))
        {
            throw new WebSocketException(
                $"The server did not select the requested subprotocol '{subProtocol}' in Sec-WebSocket-Protocol (got '{protocol}').");
        }
    }

    private static bool HasToken(string? header, string token)
    {
        if (header == null)
        {
            return false;
        }
        foreach (string part in header.Split(','))
        {
            if (part.Trim().Equals(token, StringComparison.OrdinalIgnoreCase))
            {
                return true;
            }
        }
        return false;
    }
}
