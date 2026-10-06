// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Live means a frame has arrived on the stream: the only evidence that data flows. The SDK does not say
    /// when the server accepted a subscription (its connection_ack is handled inside), so a stream that is
    /// open but has not yet delivered anything is <see cref="Subscribed"/>, not Live.
    /// </summary>
    public enum StreamState { Idle, Connecting, Subscribed, Live, Reconnecting }

    /// <summary>Why a stream stopped delivering.</summary>
    public enum StreamFailureKind
    {
        /// <summary>The server closed the socket with 4401: the token is no longer valid.</summary>
        TokenRejected,
        /// <summary>The socket dropped, or the server ended the subscription.</summary>
        Dropped,
        /// <summary>Anything else.</summary>
        Error,
    }

    public readonly struct StreamFailure
    {
        public StreamFailure(StreamFailureKind kind, string reason)
        {
            Kind = kind;
            Reason = reason;
        }

        public StreamFailureKind Kind { get; }

        /// <summary>A line a person can read; free of tokens.</summary>
        public string Reason { get; }

        /// <summary>
        /// The SDK reports a socket the server closed as a <see cref="GraphQlRequestException"/> whose text
        /// carries the close code, e.g. "subscription socket closed by server (4401): ...". 4401 is the only
        /// code that means a new token is needed.
        /// </summary>
        public static StreamFailure Classify(Exception e)
        {
            var text = e.Message ?? "";
            if (e is StreamEndedException) return new StreamFailure(StreamFailureKind.Dropped, "the server ended the subscription");
            if (e is GraphQlRequestException && text.Contains("(4401)"))
                return new StreamFailure(StreamFailureKind.TokenRejected, "token expired (4401)");
            if (e is GraphQlRequestException)
                return new StreamFailure(StreamFailureKind.Dropped, "dropped: " + Redactor.Redact(text));
            return new StreamFailure(StreamFailureKind.Error, "error: " + e.GetType().Name + ": " + Redactor.Redact(text));
        }
    }

    /// <summary>A subscription that finished without being asked to: the server completed it.</summary>
    public sealed class StreamEndedException : Exception
    {
        public StreamEndedException() : base("the subscription ended") { }
    }

    /// <summary>
    /// One subscription kept alive across drops. The SDK does not resubscribe, so this loop does:
    /// open the stream first, take the snapshot on its first frame or when <see cref="SnapshotWait"/>
    /// has passed (subscribing before snapshotting closes the gap the other order leaves), then hand
    /// every item on until the stream fails. On failure it reports the reason, refreshes the token and
    /// reconnects at once for a 4401 (once; a second one in a row backs off), and otherwise waits
    /// 1, 2, 4 ... 30 seconds. A stream is reported Live only when its first frame arrives. Everything it tells
    /// the outside world goes through the callbacks. The loop is started from, and continues on, the caller's
    /// context (every await keeps it), which in Unity is the main thread: the callbacks, a frame's parse
    /// included, run there. The observer turns what they produce into inbox items stamped with the run's
    /// generation, so a stopped run is silent.
    /// </summary>
    public sealed class StreamRunner<T>
    {
        public static readonly TimeSpan SnapshotWait = TimeSpan.FromSeconds(2);
        public static readonly TimeSpan MaxBackoff = TimeSpan.FromSeconds(30);

        /// <summary>A connection that stayed live this long is a good one: the backoff starts over after it.</summary>
        public static readonly TimeSpan StableAfter = TimeSpan.FromSeconds(10);

        readonly Func<CancellationToken, IAsyncEnumerable<T>> open;
        readonly Func<CancellationToken, Task> snapshot;
        readonly Action<T> onItem;
        readonly Action<StreamState, string> onState;
        readonly Func<CancellationToken, Task> refreshToken;
        readonly Func<TimeSpan, CancellationToken, Task> delay;
        readonly Func<DateTimeOffset> clock;
        readonly TimeSpan snapshotWait;

        public StreamRunner(Func<CancellationToken, IAsyncEnumerable<T>> open, Func<CancellationToken, Task> snapshot, Action<T> onItem,
            Action<StreamState, string> onState, Func<CancellationToken, Task> refreshToken,
            Func<TimeSpan, CancellationToken, Task> delay = null, Func<DateTimeOffset> clock = null, TimeSpan? snapshotWait = null)
        {
            this.snapshotWait = snapshotWait ?? SnapshotWait;
            this.open = open ?? throw new ArgumentNullException(nameof(open));
            this.snapshot = snapshot;
            this.onItem = onItem ?? throw new ArgumentNullException(nameof(onItem));
            this.onState = onState ?? throw new ArgumentNullException(nameof(onState));
            this.refreshToken = refreshToken;
            this.delay = delay ?? ((d, ct) => Task.Delay(d, ct));
            this.clock = clock ?? (() => DateTimeOffset.UtcNow);
        }

        /// <summary>1, 2, 4, 8, 16, then 30 seconds.</summary>
        public static TimeSpan Backoff(int attempt)
        {
            if (attempt < 0) attempt = 0;
            var seconds = attempt >= 5 ? MaxBackoff.TotalSeconds : Math.Min(Math.Pow(2, attempt), MaxBackoff.TotalSeconds);
            return TimeSpan.FromSeconds(seconds);
        }

        public async Task RunAsync(CancellationToken ct)
        {
            var attempt = 0;
            var rejected = false;
            string reason = null;
            var first = true;
            while (!ct.IsCancellationRequested)
            {
                onState(first ? StreamState.Connecting : StreamState.Reconnecting, reason);
                first = false;
                IAsyncEnumerator<T> stream = null;
                DateTimeOffset? liveAt = null;
                try
                {
                    stream = open(ct).GetAsyncEnumerator(ct);
                    var next = stream.MoveNextAsync().AsTask();
                    using (var wait = CancellationTokenSource.CreateLinkedTokenSource(ct))
                    {
                        await Task.WhenAny(next, delay(snapshotWait, wait.Token)).ConfigureAwait(true);
                        wait.Cancel();
                    }

                    string note = null;
                    if (!next.IsFaulted && !next.IsCanceled && snapshot != null)
                    {
                        try
                        {
                            await snapshot(ct).ConfigureAwait(true);
                        }
                        catch (OperationCanceledException) when (ct.IsCancellationRequested)
                        {
                            throw;
                        }
                        catch (Exception e)
                        {
                            // the stream is up; the values it brings are still the platform's. Say the snapshot failed.
                            note = "snapshot failed: " + StreamFailure.Classify(e).Reason;
                        }
                    }

                    // the stream may have failed while the snapshot ran: surface that before saying anything
                    if (next.IsFaulted || next.IsCanceled) await next.ConfigureAwait(true);
                    // no frame yet is not live: report only that the subscription is open, and wait for evidence
                    if (!next.IsCompleted) onState(StreamState.Subscribed, note);
                    var delivered = await next.ConfigureAwait(true);
                    if (!delivered) throw new StreamEndedException();
                    liveAt = clock();
                    rejected = false;
                    onState(StreamState.Live, note);
                    do
                    {
                        onItem(stream.Current);
                        next = stream.MoveNextAsync().AsTask();
                    }
                    while (await next.ConfigureAwait(true));

                    throw new StreamEndedException();
                }
                catch (OperationCanceledException) when (ct.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception e)
                {
                    var failure = StreamFailure.Classify(e);
                    reason = failure.Reason;
                    if (liveAt.HasValue && clock() - liveAt.Value >= StableAfter) attempt = 0;
                    onState(StreamState.Reconnecting, reason);
                    await DisposeQuietly(stream).ConfigureAwait(true);
                    stream = null;
                    try
                    {
                        if (failure.Kind == StreamFailureKind.TokenRejected && !rejected)
                        {
                            // a new token and straight back: this is routine every ~15 minutes
                            rejected = true;
                            if (refreshToken != null) await refreshToken(ct).ConfigureAwait(true);
                        }
                        else
                        {
                            if (failure.Kind == StreamFailureKind.TokenRejected && refreshToken != null) await refreshToken(ct).ConfigureAwait(true);
                            await delay(Backoff(attempt++), ct).ConfigureAwait(true);
                        }
                    }
                    catch (OperationCanceledException) when (ct.IsCancellationRequested)
                    {
                        return;
                    }
                    catch (Exception re)
                    {
                        // the token could not be refreshed: wait, then try again; the reason says why
                        reason = "reconnect: " + StreamFailure.Classify(re).Reason;
                        onState(StreamState.Reconnecting, reason);
                        try { await delay(Backoff(attempt++), ct).ConfigureAwait(true); }
                        catch (OperationCanceledException) { return; }
                    }
                }
                finally
                {
                    await DisposeQuietly(stream).ConfigureAwait(true);
                }
            }
        }

        static async Task DisposeQuietly(IAsyncEnumerator<T> stream)
        {
            if (stream == null) return;
            try { await stream.DisposeAsync().ConfigureAwait(true); }
            catch { /* the stream already failed; there is nothing more to learn from closing it */ }
        }
    }
}
