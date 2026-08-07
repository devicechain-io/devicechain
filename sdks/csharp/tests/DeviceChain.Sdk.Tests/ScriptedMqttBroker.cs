// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Tests;

/// <summary>
/// A minimal, scripted MQTT 3.1.1 broker over loopback TCP, used to drive the REAL MQTTnet-backed
/// connection through wire outcomes a cooperative broker will not produce on demand.
/// </summary>
/// <remarks>
/// <para>
/// 🔑 WHY A HAND-WRITTEN BROKER RATHER THAN A FAKE <c>IMqttConnection</c>: the property under test
/// is whether the MQTTnet ADAPTER reads the SUBACK return code, and a fake connection replaces the
/// very code that would be doing the reading. A fake that skips the step under test measures the
/// fixture. The refusal has to arrive as real bytes on a real socket.
/// </para>
/// <para>
/// It is also why a real broker is not enough on its own: an authless broker grants everything, so
/// nothing in a normal rig ever emits 0x80. This mirrors the Go side, which hand-rolls a fake TCP
/// broker for exactly this case in <c>messaging/mqtt_subscribe_test.go</c>.
/// </para>
/// <para>
/// It implements only what these tests drive: CONNECT/CONNACK, SUBSCRIBE/SUBACK, QoS-1
/// PUBLISH/PUBACK, PINGREQ/PINGRESP, DISCONNECT.
/// </para>
/// </remarks>
internal sealed class ScriptedMqttBroker : IDisposable
{
    private readonly TcpListener _listener;
    private readonly CancellationTokenSource _cts = new();
    private readonly byte[] _subackCodes;
    private readonly byte _connackCode;

    /// <summary>Topics the broker received a PUBLISH for, in arrival order.</summary>
    public List<string> PublishedTopics { get; } = new();

    /// <summary>Payloads the broker received, aligned with <see cref="PublishedTopics"/>.</summary>
    public List<byte[]> PublishedPayloads { get; } = new();

    /// <summary>Topic filters the broker received a SUBSCRIBE for.</summary>
    public List<string> SubscribedFilters { get; } = new();

    /// <summary>The client id presented in CONNECT, once one has arrived.</summary>
    public string? ClientId { get; private set; }

    /// <summary>The username presented in CONNECT, if any.</summary>
    public string? Username { get; private set; }

    /// <summary>The password presented in CONNECT, if any (empty string is distinct from null).</summary>
    public string? Password { get; private set; }

    /// <summary>Whether CONNECT asked for a clean session.</summary>
    public bool CleanSession { get; private set; }

    /// <summary>The loopback endpoint clients dial.</summary>
    public Uri BrokerUri { get; }

    /// <param name="subackCodes">
    /// The return code(s) to answer every SUBSCRIBE with. 0x00/0x01/0x02 are grants (0x00 and
    /// 0x01 model a downgrade); 0x80 is a refusal.
    /// </param>
    /// <param name="connackCode">The CONNACK return code; 0x00 accepts.</param>
    public ScriptedMqttBroker(byte[]? subackCodes = null, byte connackCode = 0x00)
    {
        _subackCodes = subackCodes ?? new byte[] { 0x01 };
        _connackCode = connackCode;
        _listener = new TcpListener(IPAddress.Loopback, 0);
        _listener.Start();
        var port = ((IPEndPoint)_listener.LocalEndpoint).Port;
        BrokerUri = new Uri($"tcp://127.0.0.1:{port}");
        _ = Task.Run(AcceptLoopAsync);
    }

    private async Task AcceptLoopAsync()
    {
        try
        {
            while (!_cts.IsCancellationRequested)
            {
                var client = await _listener.AcceptTcpClientAsync().ConfigureAwait(false);
                _ = Task.Run(() => ServeAsync(client));
            }
        }
        catch (Exception)
        {
            // Listener stopped; nothing to do.
        }
    }

    private async Task ServeAsync(TcpClient client)
    {
        try
        {
            using (client)
            using (var stream = client.GetStream())
            {
                while (!_cts.IsCancellationRequested)
                {
                    var header = await ReadByteAsync(stream).ConfigureAwait(false);
                    if (header < 0)
                    {
                        return;
                    }

                    var packetType = (header & 0xF0) >> 4;
                    var length = await ReadRemainingLengthAsync(stream).ConfigureAwait(false);
                    var body = await ReadExactlyAsync(stream, length).ConfigureAwait(false);

                    switch (packetType)
                    {
                        case 1: // CONNECT
                            ParseConnect(body);
                            await stream.WriteAsync(new byte[] { 0x20, 0x02, 0x00, _connackCode }, 0, 4).ConfigureAwait(false);
                            break;

                        case 8: // SUBSCRIBE
                            await HandleSubscribeAsync(stream, body).ConfigureAwait(false);
                            break;

                        case 3: // PUBLISH
                            await HandlePublishAsync(stream, header, body).ConfigureAwait(false);
                            break;

                        case 12: // PINGREQ
                            await stream.WriteAsync(new byte[] { 0xD0, 0x00 }, 0, 2).ConfigureAwait(false);
                            break;

                        case 14: // DISCONNECT
                            return;
                    }
                }
            }
        }
        catch (Exception)
        {
            // A client that vanishes mid-exchange is normal in these tests.
        }
    }

    private void ParseConnect(byte[] body)
    {
        var offset = 0;
        ReadString(body, ref offset);              // protocol name
        offset++;                                   // protocol level
        var flags = body[offset++];
        CleanSession = (flags & 0x02) != 0;
        offset += 2;                                // keep-alive
        ClientId = ReadString(body, ref offset);

        if ((flags & 0x04) != 0)                    // will flag
        {
            ReadString(body, ref offset);           // will topic
            ReadString(body, ref offset);           // will message
        }

        if ((flags & 0x80) != 0)                    // username flag
        {
            Username = ReadString(body, ref offset);
        }

        if ((flags & 0x40) != 0)                    // password flag
        {
            Password = ReadString(body, ref offset);
        }
    }

    private async Task HandleSubscribeAsync(Stream stream, byte[] body)
    {
        var packetId = (body[0] << 8) | body[1];
        var offset = 2;
        var filters = 0;
        while (offset < body.Length)
        {
            SubscribedFilters.Add(ReadString(body, ref offset));
            offset++;                               // requested QoS
            filters++;
        }

        // One return code per filter, cycling the script if it is shorter.
        var codes = new byte[filters];
        for (var i = 0; i < filters; i++)
        {
            codes[i] = _subackCodes[i % _subackCodes.Length];
        }

        var suback = new byte[4 + filters];
        suback[0] = 0x90;
        suback[1] = (byte)(2 + filters);
        suback[2] = (byte)(packetId >> 8);
        suback[3] = (byte)(packetId & 0xFF);
        Array.Copy(codes, 0, suback, 4, filters);
        await stream.WriteAsync(suback, 0, suback.Length).ConfigureAwait(false);
    }

    private async Task HandlePublishAsync(Stream stream, int header, byte[] body)
    {
        var qos = (header & 0x06) >> 1;
        var offset = 0;
        var topic = ReadString(body, ref offset);
        var packetId = 0;
        if (qos > 0)
        {
            packetId = (body[offset] << 8) | body[offset + 1];
            offset += 2;
        }

        var payload = new byte[body.Length - offset];
        Array.Copy(body, offset, payload, 0, payload.Length);
        PublishedTopics.Add(topic);
        PublishedPayloads.Add(payload);

        if (qos == 1)
        {
            await stream.WriteAsync(
                new byte[] { 0x40, 0x02, (byte)(packetId >> 8), (byte)(packetId & 0xFF) }, 0, 4).ConfigureAwait(false);
        }
    }

    private static string ReadString(byte[] buffer, ref int offset)
    {
        var length = (buffer[offset] << 8) | buffer[offset + 1];
        offset += 2;
        var value = Encoding.UTF8.GetString(buffer, offset, length);
        offset += length;
        return value;
    }

    private static async Task<int> ReadByteAsync(Stream stream)
    {
        var one = new byte[1];
        var read = await stream.ReadAsync(one, 0, 1).ConfigureAwait(false);
        return read == 0 ? -1 : one[0];
    }

    private static async Task<int> ReadRemainingLengthAsync(Stream stream)
    {
        var multiplier = 1;
        var value = 0;
        while (true)
        {
            var b = await ReadByteAsync(stream).ConfigureAwait(false);
            if (b < 0)
            {
                throw new IOException("connection closed while reading a remaining length");
            }

            value += (b & 0x7F) * multiplier;
            if ((b & 0x80) == 0)
            {
                return value;
            }

            multiplier *= 128;
        }
    }

    private static async Task<byte[]> ReadExactlyAsync(Stream stream, int count)
    {
        var buffer = new byte[count];
        var read = 0;
        while (read < count)
        {
            var n = await stream.ReadAsync(buffer, read, count - read).ConfigureAwait(false);
            if (n == 0)
            {
                throw new IOException("connection closed mid-packet");
            }

            read += n;
        }

        return buffer;
    }

    public void Dispose()
    {
        _cts.Cancel();
        _listener.Stop();
        _cts.Dispose();
    }
}
