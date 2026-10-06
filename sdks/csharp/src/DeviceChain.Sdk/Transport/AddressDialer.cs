// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// Opens a TCP connection to a host by racing its resolved addresses instead of trying them one
/// at a time. A name like <c>localhost</c> commonly resolves to <c>::1, 127.0.0.1</c>; when nothing
/// listens on <c>::1</c> the OS may not refuse the connect but leave it hanging for tens of seconds
/// before the next address is tried. Racing starts each next attempt as soon as the previous one
/// fails, or after a short stagger, and keeps the first socket that connects. In the spirit of
/// "Happy Eyeballs" connection racing.
/// </summary>
internal static class AddressDialer
{
    /// <summary>How long an attempt runs alone before the next address is raced against it.</summary>
    internal static readonly TimeSpan DefaultAttemptDelay = TimeSpan.FromMilliseconds(250);

    /// <summary>Resolves <paramref name="host"/> and returns the first socket that connects to <paramref name="port"/>.</summary>
    internal static async Task<Socket> ConnectAsync(string host, int port, CancellationToken cancellationToken)
    {
        IPAddress[] addresses = IPAddress.TryParse(host, out IPAddress? literal)
            ? new[] { literal }
            : await Dns.GetHostAddressesAsync(host).ConfigureAwait(false);
        if (addresses.Length == 0)
        {
            throw new IOException($"'{host}' did not resolve to any address.");
        }
        return await ConnectAsync(
            addresses, (address, token) => ConnectOneAsync(address, port, token), DefaultAttemptDelay, port, cancellationToken)
            .ConfigureAwait(false);
    }

    /// <summary>
    /// The race itself, with the per-address connect and the stagger injectable so it can be tested
    /// without a network. <paramref name="connect"/> must stop and release what it holds when its
    /// token is cancelled; a connection it completes anyway after the race is decided is disposed here.
    /// </summary>
    internal static async Task<T> ConnectAsync<T>(
        IReadOnlyList<IPAddress> addresses,
        Func<IPAddress, CancellationToken, Task<T>> connect,
        TimeSpan attemptDelay,
        int port,
        CancellationToken cancellationToken) where T : class, IDisposable
    {
        IPAddress[] ordered = Interleave(addresses);
        using var race = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        var pending = new List<Task<Attempt<T>>>();
        var failures = new List<string>();
        Exception? last = null;
        int next = 0;
        Task? stagger = null;
        CancellationTokenSource? staggerCts = null;
        T? winner = null;

        void StartNext()
        {
            IPAddress address = ordered[next++];
            pending.Add(RunAttempt(address, connect, race.Token));
            staggerCts?.Cancel();
            staggerCts?.Dispose();
            staggerCts = null;
            stagger = null;
            if (next < ordered.Length)
            {
                staggerCts = new CancellationTokenSource();
                stagger = Task.Delay(attemptDelay, staggerCts.Token);
            }
        }

        try
        {
            StartNext();
            while (true)
            {
                var waits = new List<Task>(pending);
                if (stagger != null)
                {
                    waits.Add(stagger);
                }
                if (waits.Count == 0)
                {
                    break;
                }
                Task done = await Task.WhenAny(waits).ConfigureAwait(false);
                cancellationToken.ThrowIfCancellationRequested();
                if (done == stagger)
                {
                    StartNext();
                    continue;
                }
                var finished = (Task<Attempt<T>>)done;
                pending.Remove(finished);
                Attempt<T> attempt = finished.Result;
                if (attempt.Value != null)
                {
                    winner = attempt.Value;
                    break;
                }
                failures.Add($"{attempt.Address}: {attempt.Error!.Message}");
                last = attempt.Error;
                if (next < ordered.Length)
                {
                    StartNext(); // a failure does not wait out the stagger
                }
            }
        }
        finally
        {
            race.Cancel();
            staggerCts?.Cancel();
            staggerCts?.Dispose();
            foreach (Task<Attempt<T>> loser in pending)
            {
                // A loser still in flight was cancelled above; if it connects anyway, release it.
                _ = loser.ContinueWith(
                    t => { if (t.Status == TaskStatus.RanToCompletion) { t.Result.Value?.Dispose(); } },
                    CancellationToken.None, TaskContinuationOptions.ExecuteSynchronously, TaskScheduler.Default);
            }
        }

        if (winner == null)
        {
            throw new IOException(
                $"Could not connect to port {port}: {string.Join("; ", failures)}", last);
        }
        return winner;
    }

    // Reorders so the families alternate, starting with the family of the resolver's first address.
    internal static IPAddress[] Interleave(IReadOnlyList<IPAddress> addresses)
    {
        if (addresses.Count == 0)
        {
            return Array.Empty<IPAddress>();
        }
        AddressFamily first = addresses[0].AddressFamily;
        var same = new Queue<IPAddress>(addresses.Where(a => a.AddressFamily == first));
        var other = new Queue<IPAddress>(addresses.Where(a => a.AddressFamily != first));
        var result = new List<IPAddress>(addresses.Count);
        while (same.Count > 0 || other.Count > 0)
        {
            if (same.Count > 0)
            {
                result.Add(same.Dequeue());
            }
            if (other.Count > 0)
            {
                result.Add(other.Dequeue());
            }
        }
        return result.ToArray();
    }

    private static async Task<Attempt<T>> RunAttempt<T>(
        IPAddress address, Func<IPAddress, CancellationToken, Task<T>> connect, CancellationToken token) where T : class
    {
        try
        {
            return new Attempt<T>(address, await connect(address, token).ConfigureAwait(false), null);
        }
        catch (Exception ex)
        {
            return new Attempt<T>(address, null, ex);
        }
    }

    // netstandard2.1 has no Socket.ConnectAsync that takes a CancellationToken, so a cancelled
    // attempt is stopped by disposing its socket, which aborts the pending connect.
    private static async Task<Socket> ConnectOneAsync(IPAddress address, int port, CancellationToken token)
    {
        var socket = new Socket(address.AddressFamily, SocketType.Stream, ProtocolType.Tcp) { NoDelay = true };
        try
        {
            using (token.Register(static s => ((Socket)s!).Dispose(), socket))
            {
                token.ThrowIfCancellationRequested();
                await Task.Factory.FromAsync(
                    (cb, state) => socket.BeginConnect(new IPEndPoint(address, port), cb, state),
                    socket.EndConnect,
                    null).ConfigureAwait(false);
            }
            token.ThrowIfCancellationRequested();
            return socket;
        }
        catch
        {
            socket.Dispose();
            token.ThrowIfCancellationRequested();
            throw;
        }
    }

    private readonly struct Attempt<T> where T : class
    {
        internal Attempt(IPAddress address, T? value, Exception? error)
        {
            Address = address;
            Value = value;
            Error = error;
        }

        internal IPAddress Address { get; }

        internal T? Value { get; }

        internal Exception? Error { get; }
    }
}
