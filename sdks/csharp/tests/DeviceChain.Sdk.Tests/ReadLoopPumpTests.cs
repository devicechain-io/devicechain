// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Subscriptions;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// Proves the slice-4.3 read-loop seam: the subscription client can run its read loop WITHOUT a
// thread pool, advanced only by a host's frame loop, which is the one thing Unity WebGL needs and
// cannot fake. The socket here mirrors WebGlWebSocketFactory's real shape — a poll loop whose only
// suspension point is a bare `await Task.Yield()` — so what the tests exercise is the same
// scheduling behaviour the browser transport depends on, not an easier analogue of it.
public class ReadLoopPumpTests
{
    private static readonly Uri Endpoint = new("wss://host/api/event-management/graphql");

    // ── a socket that behaves like the browser one: nothing blocks, everything polls ──────────

    private sealed class PolledSocketFactory : IWebSocketFactory
    {
        private readonly Func<string, IReadOnlyList<WebSocketMessage>> _respond;

        public PolledSocketFactory(Func<string, IReadOnlyList<WebSocketMessage>> respond) => _respond = respond;

        public int CreateCount { get; private set; }
        public PolledSocket? Last { get; private set; }

        public IWebSocketConnection Create()
        {
            CreateCount++;
            return Last = new PolledSocket(_respond);
        }
    }

    private sealed class PolledSocket : IWebSocketConnection
    {
        private readonly Func<string, IReadOnlyList<WebSocketMessage>> _respond;
        private readonly ConcurrentQueue<WebSocketMessage> _inbound = new();
        private volatile bool _open;
        private volatile bool _aborted;

        public PolledSocket(Func<string, IReadOnlyList<WebSocketMessage>> respond) => _respond = respond;

        /// <summary>Thread each completed receive returned on — how the tests see WHERE the loop ran.</summary>
        public ConcurrentQueue<int> ReceiveThreadIds { get; } = new();

        public bool IsOpen => _open && !_aborted;

        public Task ConnectAsync(Uri endpoint, string subProtocol, CancellationToken cancellationToken)
        {
            _open = true;
            return Task.CompletedTask;
        }

        public Task SendTextAsync(byte[] utf8Payload, CancellationToken cancellationToken)
        {
            foreach (WebSocketMessage reply in _respond(Encoding.UTF8.GetString(utf8Payload)))
            {
                _inbound.Enqueue(reply);
            }
            return Task.CompletedTask;
        }

        // Mirrors WebGlWebSocketFactory.ReceiveAsync: abort checked first, then a message, then a
        // bare `await Task.Yield()`. That yield is the ONLY suspension point, so under a pump
        // context this receive advances exactly once per pump and never touches a pool thread.
        public async Task<WebSocketMessage> ReceiveAsync(CancellationToken cancellationToken)
        {
            while (true)
            {
                if (_aborted)
                {
                    ReceiveThreadIds.Enqueue(Thread.CurrentThread.ManagedThreadId);
                    return WebSocketMessage.OfClose(1006, "aborted");
                }
                if (_inbound.TryDequeue(out WebSocketMessage message))
                {
                    ReceiveThreadIds.Enqueue(Thread.CurrentThread.ManagedThreadId);
                    return message;
                }
                cancellationToken.ThrowIfCancellationRequested();
                await Task.Yield();
            }
        }

        public void Abort() => _aborted = true;

        public void Dispose() => Abort();
    }

    // Acks the handshake, then answers each subscribe with two events and a complete.
    private static Func<string, IReadOnlyList<WebSocketMessage>> TwoEventsThenComplete() => frame =>
    {
        using JsonDocument doc = JsonDocument.Parse(frame);
        string type = doc.RootElement.GetProperty("type").GetString()!;
        if (type == "connection_init")
        {
            return new[] { WebSocketMessage.OfText("{\"type\":\"connection_ack\"}") };
        }
        if (type == "subscribe")
        {
            string id = doc.RootElement.GetProperty("id").GetString()!;
            return new[]
            {
                WebSocketMessage.OfText($"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"a\"}}}}}}"),
                WebSocketMessage.OfText($"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"b\"}}}}}}"),
                WebSocketMessage.OfText($"{{\"id\":\"{id}\",\"type\":\"complete\"}}"),
            };
        }
        return Array.Empty<WebSocketMessage>();
    };

    private static IAsyncEnumerator<Thing> Subscribe(GraphQlWsClient client, CancellationToken cancellationToken = default) =>
        client.SubscribeAsync("subscription{x}", EmptyVariables.Value,
            TestJson.Default.EmptyVariables, TestJson.Default.Thing, cancellationToken).GetAsyncEnumerator(cancellationToken);

    // ── 1. the load-bearing property: no pump, no progress ────────────────────────────────────

    [Fact]
    public async Task A_pumped_read_loop_delivers_nothing_until_the_host_pumps()
    {
        var driver = new ManualPumpDriver();
        var factory = new PolledSocketFactory(TwoEventsThenComplete());
        await using var client = new GraphQlWsClient(factory, Endpoint, null, driver);

        IAsyncEnumerator<Thing> events = Subscribe(client);
        ValueTask<bool> first = events.MoveNextAsync();

        // The handshake and the subscribe both ran (they are the caller's own work), so the server
        // has already queued three frames — yet nothing can be dispatched.
        Assert.Equal(1, factory.CreateCount);
        await Task.Delay(150);
        Assert.False(first.IsCompleted, "an unpumped read loop must not deliver — see the control test below");

        for (int i = 0; i < 32 && !first.IsCompleted; i++)
        {
            driver.Pump();
            await Task.Yield(); // let the channel hand the item to the awaiting consumer
        }

        Assert.True(await first.AsTask().WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.Equal("a", events.Current.Name);
        await events.DisposeAsync();
    }

    // The control that makes the assertion above non-vacuous: the SAME socket and the SAME protocol
    // on the default driver deliver with no pump at all. So test 1 measures the driver, not a fake
    // that simply never produces anything.
    [Fact]
    public async Task The_default_driver_delivers_the_same_stream_with_no_pump_at_all()
    {
        var factory = new PolledSocketFactory(TwoEventsThenComplete());
        await using var client = new GraphQlWsClient(factory, Endpoint, null, new BackgroundTaskDriver());

        IAsyncEnumerator<Thing> events = Subscribe(client);
        Assert.True(await events.MoveNextAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(5)));
        Assert.Equal("a", events.Current.Name);
        await events.DisposeAsync();
    }

    // ── 2. WHERE it runs — the property that actually encodes "there is no thread pool" ────────

    [Fact]
    public async Task A_pumped_read_loop_runs_only_on_the_pumping_thread()
    {
        var driver = new ManualPumpDriver();
        var factory = new PolledSocketFactory(TwoEventsThenComplete());
        await using var client = new GraphQlWsClient(factory, Endpoint, null, driver);

        // A dedicated non-pool thread stands in for Unity's main thread. Asserting equality with
        // THIS id (rather than "not the test thread") is what fails against a pooled loop.
        int pumpThreadId = 0;
        using var started = new ManualResetEventSlim();
        using var stop = new CancellationTokenSource();
        var pump = new Thread(() =>
        {
            pumpThreadId = Thread.CurrentThread.ManagedThreadId;
            Assert.False(Thread.CurrentThread.IsThreadPoolThread);
            started.Set();
            while (!stop.IsCancellationRequested)
            {
                driver.Pump();
                Thread.Sleep(1);
            }
        })
        { IsBackground = true, Name = "pump" };
        pump.Start();
        started.Wait();

        IAsyncEnumerator<Thing> events = Subscribe(client);
        var names = new List<string>();
        while (await events.MoveNextAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(10)))
        {
            names.Add(events.Current.Name);
        }
        await events.DisposeAsync();
        Assert.Equal(new[] { "a", "b" }, names);

        stop.Cancel();
        pump.Join(TimeSpan.FromSeconds(5));

        // The first receive is the handshake ack, which the CALLER performs before the loop starts;
        // every one after it belongs to the read loop and must have run on the pump thread.
        int[] threads = factory.Last!.ReceiveThreadIds.ToArray();
        Assert.True(threads.Length >= 4, $"expected ack + 3 loop receives, got {threads.Length}");
        for (int i = 1; i < threads.Length; i++)
        {
            Assert.Equal(pumpThreadId, threads[i]);
        }
    }

    // ── 3. teardown must not require another pump ─────────────────────────────────────────────

    [Fact]
    public async Task Dispose_finishes_promptly_after_the_host_has_stopped_pumping()
    {
        var driver = new ManualPumpDriver();
        // This server never completes the subscription, so the read loop is genuinely parked mid-poll.
        var factory = new PolledSocketFactory(frame =>
            JsonDocument.Parse(frame).RootElement.GetProperty("type").GetString() == "connection_init"
                ? new[] { WebSocketMessage.OfText("{\"type\":\"connection_ack\"}") }
                : Array.Empty<WebSocketMessage>());

        var client = new GraphQlWsClient(factory, Endpoint, null, driver);
        IAsyncEnumerator<Thing> events = Subscribe(client);
        ValueTask<bool> pending = events.MoveNextAsync();
        for (int i = 0; i < 8; i++)
        {
            driver.Pump(); // the loop is alive and parked on the socket poll
        }
        Assert.False(pending.IsCompleted);

        // From here the host pumps no more — exactly a MonoBehaviour whose OnDestroy runs after its
        // final Update. Disposal has to unwind the loop itself.
        var elapsed = Stopwatch.StartNew();
        await client.DisposeAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(30));
        elapsed.Stop();

        // The client's internal drain budget is 2s; finishing well inside it is what proves the loop
        // was actually unwound rather than waited out.
        Assert.True(elapsed.ElapsedMilliseconds < 1000,
            $"dispose waited out the drain budget ({elapsed.ElapsedMilliseconds}ms) instead of unwinding the loop");

        // And the in-flight subscription ended, rather than being left hanging on a dead client.
        await Assert.ThrowsAsync<GraphQlRequestException>(async () => await pending);
    }

    // ── 4. the IsLive trap: liveness must come from the driver, not from a Task ────────────────

    [Fact]
    public async Task Two_subscriptions_over_a_pumped_connection_share_one_socket()
    {
        var driver = new ManualPumpDriver();
        var factory = new PolledSocketFactory(TwoEventsThenComplete());
        await using var client = new GraphQlWsClient(factory, Endpoint, null, driver);

        using var stop = new CancellationTokenSource();
        var pump = new Thread(() =>
        {
            while (!stop.IsCancellationRequested)
            {
                driver.Pump();
                Thread.Sleep(1);
            }
        })
        { IsBackground = true, Name = "pump" };
        pump.Start();

        for (int round = 0; round < 2; round++)
        {
            IAsyncEnumerator<Thing> events = Subscribe(client);
            var names = new List<string>();
            while (await events.MoveNextAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(10)))
            {
                names.Add(events.Current.Name);
            }
            await events.DisposeAsync();
            Assert.Equal(new[] { "a", "b" }, names);
        }

        stop.Cancel();
        pump.Join(TimeSpan.FromSeconds(5));

        // A liveness test based on "a Task has not completed" reads a pumped loop as dead and opens
        // a socket per subscribe — an endless reconnect that would present as a server fault.
        Assert.Equal(1, factory.CreateCount);
    }

    // ── 5. the pump itself ────────────────────────────────────────────────────────────────────

    [Fact]
    public void Pump_runs_only_the_work_already_queued_so_a_poll_loop_cannot_spin_a_frame_away()
    {
        var driver = new ManualPumpDriver();
        int turns = 0;
        driver.Start(async _ =>
        {
            while (turns < 100)
            {
                turns++;
                await Task.Yield();
            }
        }, CancellationToken.None);

        Assert.Equal(0, turns);        // Start alone runs nothing
        Assert.Equal(1, driver.Pump()); // the start itself is one work item; the loop runs to its first yield
        Assert.Equal(1, turns);
        driver.Pump();
        Assert.Equal(2, turns);         // one turn per pump — a re-queued continuation waits for the next
    }

    [Fact]
    public async Task A_started_loop_reads_as_running_before_it_has_executed_anything()
    {
        var driver = new ManualPumpDriver();
        IReadLoopHandle handle = driver.Start(_ => Task.CompletedTask, CancellationToken.None);

        // The whole point of IsRunning not being "a Task is incomplete": nothing has run yet.
        Assert.True(handle.IsRunning);
        Assert.False(handle.Completion.IsCompleted);

        driver.Pump();
        Assert.False(handle.IsRunning);
        await handle.Completion.WaitAsync(TimeSpan.FromSeconds(5));
    }
}
