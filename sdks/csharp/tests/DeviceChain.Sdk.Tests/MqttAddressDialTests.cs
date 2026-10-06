// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Security;
using System.Net.Sockets;
using System.Security.Authentication;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// The MQTT connect dials the broker's addresses itself (racing them, as the subscription transport
// does) and hands MQTTnet the winning endpoint, instead of letting MQTTnet try the resolver's
// addresses one at a time. The host a test connects to is a NAME that no resolver knows, so the
// only way these connects can succeed is through the dial seam — a connect that quietly went back
// to dialling by name would fail to resolve.
//
// Timing is asserted loosely on purpose: the defect is a connect that takes ~21 s or never ends,
// so a generous bound separates it from a working run without flaking on a slow runner.
public class MqttAddressDialTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(10);
    private const string Name = "broker.dial-test.invalid";

    private static MqttConnectOptions OptionsFor(Uri uri) =>
        new(uri, "inst:acme:sensor-001") { Username = "acme:cred-1", Password = string.Empty };

    private static int PortOf(ScriptedMqttBroker broker) => broker.BrokerUri.Port;

    // A host whose first address never answers and whose second is the live broker, scripted
    // through the generic dialer so no network topology is involved.
    private static Func<string, int, CancellationToken, Task<Socket>> HungThenLive(
        List<Socket> probes, List<string> hosts)
    {
        return (host, port, token) =>
        {
            lock (hosts)
            {
                hosts.Add(host);
            }
            return AddressDialer.ConnectAsync<Socket>(
                new[] { IPAddress.IPv6Loopback, IPAddress.Loopback },
                async (address, attemptToken) =>
                {
                    if (address.AddressFamily == AddressFamily.InterNetworkV6)
                    {
                        await Task.Delay(System.Threading.Timeout.Infinite, attemptToken).ConfigureAwait(false);
                    }
                    var socket = new Socket(address.AddressFamily, SocketType.Stream, ProtocolType.Tcp);
                    await socket.ConnectAsync(new IPEndPoint(address, port)).ConfigureAwait(false);
                    lock (probes)
                    {
                        probes.Add(socket);
                    }
                    return socket;
                },
                TimeSpan.FromMilliseconds(50), port, token);
        };
    }

    [Fact]
    public async Task AFirstAddressThatNeverAnswersDoesNotHoldUpTheConnect()
    {
        using var broker = new ScriptedMqttBroker();
        var probes = new List<Socket>();
        var hosts = new List<string>();
        await using var connection = new MqttNetConnection { DialAsync = HungThenLive(probes, hosts) };
        using var cts = new CancellationTokenSource(Timeout);

        var timer = Stopwatch.StartNew();
        await connection.ConnectAsync(OptionsFor(new Uri($"tcp://{Name}:{PortOf(broker)}")), cts.Token);
        timer.Stop();

        Assert.True(connection.IsConnected);
        Assert.Equal("inst:acme:sensor-001", broker.ClientId);
        Assert.True(timer.Elapsed < TimeSpan.FromSeconds(5), $"connect took {timer.Elapsed}");
        // The race is run once, for the name the caller gave, and not again per publish.
        Assert.Equal(new[] { Name }, hosts);
    }

    [Fact]
    public async Task TheProbeSocketIsClosedOnceTheWinnerIsKnown()
    {
        using var broker = new ScriptedMqttBroker();
        var probes = new List<Socket>();
        await using var connection = new MqttNetConnection { DialAsync = HungThenLive(probes, new List<string>()) };
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(new Uri($"tcp://{Name}:{PortOf(broker)}")), cts.Token);

        var probe = Assert.Single(probes);
        // A disposed socket reports itself unconnected; one left open would still be connected.
        Assert.False(probe.Connected);
    }

    [Fact]
    public async Task APinnedFamilyKeepsTheByNamePathAndSkipsTheRace()
    {
        using var broker = new ScriptedMqttBroker();
        await using var connection = new MqttNetConnection { DialAsync = NeverCalled };
        var options = OptionsFor(new Uri($"tcp://localhost:{PortOf(broker)}"));
        options.AddressFamily = AddressFamily.InterNetwork;
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(options, cts.Token);

        Assert.True(connection.IsConnected);
    }

    [Fact]
    public async Task AnIpLiteralHostHasNothingToRace()
    {
        using var broker = new ScriptedMqttBroker();
        await using var connection = new MqttNetConnection { DialAsync = NeverCalled };
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(broker.BrokerUri), cts.Token);

        Assert.True(connection.IsConnected);
    }

    [Fact]
    public async Task TheCallersTokenCancelsTheRace()
    {
        await using var connection = new MqttNetConnection
        {
            DialAsync = async (_, _, token) =>
            {
                await Task.Delay(System.Threading.Timeout.Infinite, token).ConfigureAwait(false);
                throw new InvalidOperationException("unreachable");
            },
        };
        using var cts = new CancellationTokenSource(TimeSpan.FromMilliseconds(300));

        var timer = Stopwatch.StartNew();
        var connect = connection.ConnectAsync(OptionsFor(new Uri($"tcp://{Name}:1883")), cts.Token);
        // Bounded here, so a race that ignores the token fails instead of hanging the suite.
        var finished = await Task.WhenAny(connect, Task.Delay(TimeSpan.FromSeconds(5)));
        Assert.True(ReferenceEquals(finished, connect), "the connect did not end after the caller cancelled");
        // A deliberate cancellation stays a cancellation; it is not presented as a broker failure.
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => connect);
        Assert.True(timer.Elapsed < TimeSpan.FromSeconds(5), $"cancel took {timer.Elapsed}");
    }

    // The race ending on its own bound, with the caller still waiting, is a connection failure the
    // session retries; surfaced as a cancellation it would end the session's reconnects for good.
    [Fact]
    public async Task ARaceThatTimesOutIsAConnectionFailureNotACancellation()
    {
        await using var connection = new MqttNetConnection
        {
            DialAsync = (_, _, _) => throw new OperationCanceledException(),
        };
        using var cts = new CancellationTokenSource(Timeout);

        var failure = await Assert.ThrowsAsync<MqttConnectionException>(
            () => connection.ConnectAsync(OptionsFor(new Uri($"tcp://{Name}:1883")), cts.Token));

        Assert.IsType<TimeoutException>(failure.InnerException);
    }

    [Fact]
    public async Task ADialThatNeverAnswersEndsAtTheRaceBound()
    {
        await using var connection = new MqttNetConnection
        {
            DialTimeout = TimeSpan.FromMilliseconds(300),
            DialAsync = async (_, _, token) =>
            {
                await Task.Delay(System.Threading.Timeout.Infinite, token).ConfigureAwait(false);
                throw new InvalidOperationException("unreachable");
            },
        };
        using var cts = new CancellationTokenSource(Timeout);

        var timer = Stopwatch.StartNew();
        var connect = connection.ConnectAsync(OptionsFor(new Uri($"tcp://{Name}:1883")), cts.Token);
        var finished = await Task.WhenAny(connect, Task.Delay(TimeSpan.FromSeconds(5)));
        Assert.True(ReferenceEquals(finished, connect), "the race was not bounded");
        var failure = await Assert.ThrowsAsync<MqttConnectionException>(() => connect);

        Assert.IsType<TimeoutException>(failure.InnerException);
        Assert.True(timer.Elapsed < TimeSpan.FromSeconds(5), $"bound took {timer.Elapsed}");
    }

    // A failed race is final for that attempt: there is no second dial by name behind it.
    [Fact]
    public async Task AFailedRaceIsNotRetriedByName()
    {
        var dials = 0;
        await using var connection = new MqttNetConnection
        {
            DialAsync = (_, port, _) =>
            {
                Interlocked.Increment(ref dials);
                throw new IOException($"Could not connect to port {port}: refused");
            },
        };
        using var cts = new CancellationTokenSource(Timeout);

        await Assert.ThrowsAsync<MqttConnectionException>(
            () => connection.ConnectAsync(OptionsFor(new Uri($"tcp://localhost:1883")), cts.Token));

        Assert.Equal(1, Volatile.Read(ref dials));
    }

    // The session tells a refused connect (stay Blind) from a transport failure (retry) by
    // exception type, so a connect that finds no address must still be the transport kind.
    [Fact]
    public async Task AnUnreachableHostIsATransportFailureNotARefusal()
    {
        await using var connection = new MqttNetConnection
        {
            DialAsync = (_, port, _) => throw new IOException($"Could not connect to port {port}: ::1: refused"),
        };
        using var cts = new CancellationTokenSource(Timeout);

        var failure = await Assert.ThrowsAsync<MqttConnectionException>(
            () => connection.ConnectAsync(OptionsFor(new Uri($"tcp://{Name}:1883")), cts.Token));

        Assert.IsNotType<MqttConnectRefusedException>(failure);
        Assert.Contains("failed", failure.Message);
        Assert.IsType<IOException>(failure.InnerException);
    }

    // The real dialer against a name that resolves but where nothing listens: the same type and
    // message shape as a refused TCP connect always had.
    [Fact]
    public async Task ARefusedTcpConnectKeepsItsClassification()
    {
        var listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Start();
        var port = ((IPEndPoint)listener.LocalEndpoint).Port;
        listener.Stop();

        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        var failure = await Assert.ThrowsAsync<MqttConnectionException>(
            () => connection.ConnectAsync(OptionsFor(new Uri($"tcp://localhost:{port}")), cts.Token));

        Assert.IsNotType<MqttConnectRefusedException>(failure);
        Assert.StartsWith($"connecting to tcp://localhost:{port}/ as \"inst:acme:sensor-001\" failed: ", failure.Message);
    }

    // ── TLS: the raced endpoint is an IP, the certificate is for a NAME ───────

    [Fact]
    public async Task TlsStillValidatesTheCertificateAgainstTheHostName()
    {
        using var root = Ca("pinned-root");
        using var leaf = ServerLeaf(root, "localhost");
        using var broker = new TlsConnackBroker(leaf);
        await using var connection = new MqttNetConnection();
        var options = OptionsFor(new Uri($"ssl://localhost:{broker.Port}"));
        options.Trust = MqttTrust.PinnedCa(root.RawData);
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(options, cts.Token);

        Assert.True(connection.IsConnected);
        Assert.Equal(1, broker.Handshakes);
    }

    [Fact]
    public async Task ACertificateForAnotherNameIsRefusedEvenThoughTheAddressIsReachable()
    {
        using var root = Ca("pinned-root");
        using var leaf = ServerLeaf(root, "some-other-host.example");
        using var broker = new TlsConnackBroker(leaf);
        await using var connection = new MqttNetConnection();
        var options = OptionsFor(new Uri($"ssl://localhost:{broker.Port}"));
        options.Trust = MqttTrust.PinnedCa(root.RawData);
        using var cts = new CancellationTokenSource(Timeout);

        await Assert.ThrowsAsync<MqttConnectionException>(() => connection.ConnectAsync(options, cts.Token));

        Assert.False(connection.IsConnected);
    }

    private static Task<Socket> NeverCalled(string host, int port, CancellationToken token) =>
        throw new InvalidOperationException($"the address race must not run for {host}");

    private static X509Certificate2 Ca(string name)
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest($"CN={name}", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, false, 0, true));
        request.CertificateExtensions.Add(
            new X509KeyUsageExtension(X509KeyUsageFlags.KeyCertSign | X509KeyUsageFlags.CrlSign, true));
        return request.CreateSelfSigned(DateTimeOffset.UtcNow.AddDays(-30), DateTimeOffset.UtcNow.AddDays(365));
    }

    // A server certificate that names its host in a DNS SAN and, like the real broker's, has no IP SAN.
    private static X509Certificate2 ServerLeaf(X509Certificate2 issuer, string dnsName)
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest($"CN={dnsName}", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(false, false, 0, true));
        request.CertificateExtensions.Add(new X509EnhancedKeyUsageExtension(
            new OidCollection { new Oid("1.3.6.1.5.5.7.3.1") }, false));
        var san = new SubjectAlternativeNameBuilder();
        san.AddDnsName(dnsName);
        request.CertificateExtensions.Add(san.Build());
        var serial = new byte[16];
        RandomNumberGenerator.Fill(serial);
        using var signed = request.Create(
            issuer, DateTimeOffset.UtcNow.AddDays(-1), DateTimeOffset.UtcNow.AddDays(90), serial);
        using var withKey = signed.CopyWithPrivateKey(key);
        // Round-tripped through PFX so the key is usable by SslStream on every platform.
        return new X509Certificate2(withKey.Export(X509ContentType.Pfx));
    }

    // A TLS endpoint that answers CONNECT with an accepting CONNACK and counts completed handshakes.
    private sealed class TlsConnackBroker : IDisposable
    {
        private readonly TcpListener _listener;
        private readonly X509Certificate2 _certificate;
        private readonly CancellationTokenSource _cts = new();
        private int _handshakes;

        public int Port { get; }

        public int Handshakes => Volatile.Read(ref _handshakes);

        public TlsConnackBroker(X509Certificate2 certificate)
        {
            _certificate = certificate;
            _listener = new TcpListener(IPAddress.Loopback, 0);
            _listener.Start();
            Port = ((IPEndPoint)_listener.LocalEndpoint).Port;
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
                // Listener stopped.
            }
        }

        private async Task ServeAsync(TcpClient client)
        {
            try
            {
                using (client)
                using (var ssl = new SslStream(client.GetStream(), false))
                {
                    await ssl.AuthenticateAsServerAsync(
                        _certificate, false, SslProtocols.Tls12 | SslProtocols.Tls13, false).ConfigureAwait(false);
                    Interlocked.Increment(ref _handshakes);
                    var buffer = new byte[1024];
                    await ssl.ReadAsync(buffer, 0, buffer.Length, _cts.Token).ConfigureAwait(false);
                    await ssl.WriteAsync(new byte[] { 0x20, 0x02, 0x00, 0x00 }, 0, 4, _cts.Token).ConfigureAwait(false);
                    await ssl.FlushAsync(_cts.Token).ConfigureAwait(false);
                    while (await ssl.ReadAsync(buffer, 0, buffer.Length, _cts.Token).ConfigureAwait(false) > 0)
                    {
                    }
                }
            }
            catch (Exception)
            {
                // A client that refuses the certificate aborts the handshake; that is the point.
            }
        }

        public void Dispose()
        {
            _cts.Cancel();
            _listener.Stop();
            _certificate.Dispose();
            _cts.Dispose();
        }
    }
}
