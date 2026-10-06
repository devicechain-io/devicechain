// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

public class AddressDialerTests
{
    private static readonly IPAddress V6 = IPAddress.IPv6Loopback;
    private static readonly IPAddress V4 = IPAddress.Loopback;

    private sealed class Conn : IDisposable
    {
        private int _disposed;
        public Conn(IPAddress address) => Address = address;
        public IPAddress Address { get; }
        public bool Disposed => Volatile.Read(ref _disposed) == 1;
        public void Dispose() => Interlocked.Exchange(ref _disposed, 1);
    }

    // The scripted connect: each address gets a behaviour, no network involved.
    private sealed class Script
    {
        public readonly List<Conn> Made = new();
        public readonly List<IPAddress> Started = new();
        public readonly List<DateTime> StartTimes = new();
        public readonly List<bool> Cancelled = new();
        private readonly Dictionary<IPAddress, Func<CancellationToken, Task<Conn>>> _by = new();

        public Script On(IPAddress a, Func<CancellationToken, Task<Conn>> behaviour)
        {
            _by[a] = behaviour;
            return this;
        }

        public async Task<Conn> Connect(IPAddress a, CancellationToken token)
        {
            int slot;
            lock (Made)
            {
                Started.Add(a);
                StartTimes.Add(DateTime.UtcNow);
                Cancelled.Add(false);
                slot = Started.Count - 1;
            }
            try
            {
                return await _by[a](token);
            }
            catch (OperationCanceledException)
            {
                lock (Made) { Cancelled[slot] = true; }
                throw;
            }
        }

        public Conn Make(IPAddress a)
        {
            var c = new Conn(a);
            lock (Made) { Made.Add(c); }
            return c;
        }

        // A connect that never completes until cancelled; it releases what it made on cancel.
        public Func<CancellationToken, Task<Conn>> Hang(IPAddress a) => async token =>
        {
            Conn c = Make(a);
            try
            {
                await Task.Delay(Timeout.Infinite, token);
            }
            catch
            {
                c.Dispose();
                throw;
            }
            return c;
        };

        public Func<CancellationToken, Task<Conn>> Ok(IPAddress a) => _ => Task.FromResult(Make(a));

        public Func<CancellationToken, Task<Conn>> Refuse() =>
            _ => Task.FromException<Conn>(new SocketException((int)SocketError.ConnectionRefused));
    }

    private static async Task WaitUntil(Func<bool> condition)
    {
        for (int i = 0; i < 200 && !condition(); i++)
        {
            await Task.Delay(10);
        }
        Assert.True(condition());
    }

    [Fact]
    public async Task A_hung_first_address_does_not_hold_up_the_second()
    {
        var script = new Script();
        script.On(V6, script.Hang(V6)).On(V4, script.Ok(V4));
        var sw = Stopwatch.StartNew();

        using var bound = new CancellationTokenSource(TimeSpan.FromSeconds(5)); // one-at-a-time dialling fails here instead of hanging
        Conn won = await AddressDialer.ConnectAsync(
            new[] { V6, V4 }, script.Connect, TimeSpan.FromMilliseconds(150), 80, bound.Token);

        sw.Stop();
        Assert.Equal(V4, won.Address);
        Assert.False(won.Disposed);
        Assert.InRange(sw.ElapsedMilliseconds, 100, 1000); // the stagger, not a 21 s OS timeout
        Conn hung = script.Made.Single(c => c.Address.Equals(V6));
        await WaitUntil(() => hung.Disposed);
        Assert.True(script.Cancelled[script.Started.IndexOf(V6)]);
    }

    [Fact]
    public async Task A_refused_first_address_starts_the_second_at_once()
    {
        var script = new Script();
        script.On(V6, script.Refuse()).On(V4, script.Ok(V4));
        var sw = Stopwatch.StartNew();

        Conn won = await AddressDialer.ConnectAsync(
            new[] { V6, V4 }, script.Connect, TimeSpan.FromSeconds(5), 80, CancellationToken.None);

        Assert.Equal(V4, won.Address);
        Assert.True(sw.ElapsedMilliseconds < 2000, $"waited out the stagger: {sw.ElapsedMilliseconds} ms");
    }

    [Fact]
    public async Task Every_address_failing_throws_one_exception_naming_each()
    {
        var script = new Script();
        script.On(V6, script.Refuse()).On(V4, _ => Task.FromException<Conn>(new SocketException((int)SocketError.HostUnreachable)));

        IOException ex = await Assert.ThrowsAsync<IOException>(() => AddressDialer.ConnectAsync(
            new[] { V6, V4 }, script.Connect, TimeSpan.FromMilliseconds(50), 80, CancellationToken.None));

        Assert.Contains(V6.ToString(), ex.Message);
        Assert.Contains(V4.ToString(), ex.Message);
        Assert.Contains("80", ex.Message);
        // The inner exception is the last failure, not the first.
        Assert.Equal(SocketError.HostUnreachable, Assert.IsType<SocketException>(ex.InnerException).SocketErrorCode);
    }

    [Fact]
    public async Task Caller_cancellation_throws_and_releases_every_attempt()
    {
        var script = new Script();
        script.On(V6, script.Hang(V6)).On(V4, script.Hang(V4));
        using var cts = new CancellationTokenSource();
        cts.CancelAfter(300); // after both attempts have started at a 50 ms stagger

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => AddressDialer.ConnectAsync(
            new[] { V6, V4 }, script.Connect, TimeSpan.FromMilliseconds(50), 80, cts.Token));

        Assert.Equal(2, script.Made.Count);
        await WaitUntil(() => script.Made.All(c => c.Disposed));
    }

    [Fact]
    public async Task A_connection_that_completes_after_the_winner_is_disposed()
    {
        var script = new Script();
        var late = new TaskCompletionSource<Conn>();
        script.On(V6, _ => late.Task).On(V4, script.Ok(V4));

        Conn won = await AddressDialer.ConnectAsync(
            new[] { V6, V4 }, script.Connect, TimeSpan.FromMilliseconds(50), 80, CancellationToken.None);
        var straggler = new Conn(V6);
        late.SetResult(straggler);

        await WaitUntil(() => straggler.Disposed);
        Assert.False(won.Disposed);
    }

    [Fact]
    public void Families_alternate_starting_with_the_resolvers_first()
    {
        IPAddress v6a = IPAddress.Parse("2001:db8::1"), v6b = IPAddress.Parse("2001:db8::2");
        IPAddress v4a = IPAddress.Parse("192.0.2.1"), v4b = IPAddress.Parse("192.0.2.2");

        Assert.Equal(new[] { V6, V4 }, AddressDialer.Interleave(new[] { V6, V4 }));
        Assert.Equal(new[] { v6a, v4a, v6b }, AddressDialer.Interleave(new[] { v6a, v6b, v4a }));
        Assert.Equal(new[] { v4a, v6a, v4b, v6b }, AddressDialer.Interleave(new[] { v4a, v4b, v6a, v6b }));
    }

    [Fact]
    public async Task Attempts_start_in_interleaved_order()
    {
        var script = new Script();
        IPAddress v6b = IPAddress.Parse("2001:db8::2"), v4a = IPAddress.Parse("192.0.2.1");
        script.On(V6, script.Refuse()).On(v6b, script.Refuse()).On(v4a, script.Refuse());

        await Assert.ThrowsAsync<IOException>(() => AddressDialer.ConnectAsync(
            new[] { V6, v6b, v4a }, script.Connect, TimeSpan.FromMilliseconds(10), 80, CancellationToken.None));

        Assert.Equal(new[] { V6, v4a, v6b }, script.Started);
    }

    [Fact]
    public async Task Connects_to_a_real_listener_by_name_and_by_literal()
    {
        var listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Start();
        try
        {
            int port = ((IPEndPoint)listener.LocalEndpoint).Port;
            foreach (string host in new[] { "localhost", "127.0.0.1" })
            {
                using Socket s = await AddressDialer.ConnectAsync(host, port, CancellationToken.None);
                Assert.True(s.Connected);
                Assert.Equal(port, ((IPEndPoint)s.RemoteEndPoint!).Port);
            }
        }
        finally
        {
            listener.Stop();
        }
    }
}
