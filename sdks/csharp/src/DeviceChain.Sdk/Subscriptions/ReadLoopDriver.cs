// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Subscriptions;

/// <summary>
/// The seam that decides HOW a subscription's read loop is driven — the third platform seam
/// alongside <c>IHttpTransport</c> and <c>IWebSocketFactory</c> (ADR-035 slice 4.3).
///
/// The subscription client multiplexes every operation over one socket read by a single loop.
/// On plain .NET that loop belongs on the thread pool (<see cref="BackgroundTaskDriver"/>). Unity
/// WebGL has NO thread pool under IL2CPP — a pooled work item is simply never scheduled — so the
/// loop must instead be advanced by the host's frame loop (<see cref="ManualPumpDriver"/>).
/// Injecting the choice keeps that platform difference out of the protocol code.
/// </summary>
public interface IReadLoopDriver
{
    /// <summary>
    /// Begins driving <paramref name="loop"/>. The returned handle is the client's only view of
    /// it: an implementation is free to start it on another thread, or to defer it until the host
    /// pumps. <paramref name="cancellationToken"/> fires when the owning client is disposed.
    /// </summary>
    IReadLoopHandle Start(Func<CancellationToken, Task> loop, CancellationToken cancellationToken);
}

/// <summary>One started read loop, as seen by the client that owns it.</summary>
public interface IReadLoopHandle
{
    /// <summary>
    /// True from <see cref="IReadLoopDriver.Start"/> until the loop finishes. This is NOT
    /// "a Task is incomplete": a pumped loop has not begun executing at all before the first
    /// pump, and must still read as running or the client would treat its connection as dead.
    /// </summary>
    bool IsRunning { get; }

    /// <summary>Completes (never faults) when the loop has finished, however it ended.</summary>
    Task Completion { get; }
}

/// <summary>
/// The default driver: runs the read loop as a thread-pool work item. Correct everywhere a thread
/// pool exists — plain .NET, the Unity Editor, and IL2CPP desktop/mobile players.
/// </summary>
public sealed class BackgroundTaskDriver : IReadLoopDriver
{
    /// <inheritdoc />
    public IReadLoopHandle Start(Func<CancellationToken, Task> loop, CancellationToken cancellationToken)
    {
        if (loop is null) throw new ArgumentNullException(nameof(loop));
        return new Handle(Task.Run(() => loop(cancellationToken), CancellationToken.None));
    }

    private sealed class Handle : IReadLoopHandle
    {
        private readonly Task _task;

        public Handle(Task task)
        {
            _task = task;
            Completion = Swallow(task);
        }

        public bool IsRunning => !_task.IsCompleted;

        // Never surface the loop's own failure as this handle's: the loop reports failures through
        // the per-operation channels, and a caller awaiting teardown must not get them thrown again.
        // Built once — a property that minted a fresh Task per read would make every caller's view
        // of "has it finished?" a different object.
        public Task Completion { get; }

        private static async Task Swallow(Task task)
        {
            try
            {
                await task.ConfigureAwait(false);
            }
            catch
            {
                // Observed deliberately — the loop's finally has already failed every operation.
            }
        }
    }
}

/// <summary>
/// A driver for hosts with no thread pool — Unity WebGL under IL2CPP. It starts nothing on its
/// own: the read loop advances only while <see cref="Pump"/> runs, and every continuation it
/// creates resumes on whichever thread called <see cref="Pump"/>. A Unity host pumps it from
/// <c>MonoBehaviour.Update()</c>, which also means subscription callbacks land on the main thread,
/// where touching scene objects is legal.
///
/// The mechanism is a <see cref="SynchronizationContext"/> installed for the duration of the loop:
/// every <c>await</c> on that path captures it and posts its continuation to this driver's queue
/// instead of requesting a pool thread that does not exist.
/// </summary>
public sealed class ManualPumpDriver : IReadLoopDriver
{
    // A drain triggered by disposal is bounded: a poll loop that re-posts every turn (the shape a
    // browser-WebSocket receive has) would otherwise spin here forever if its socket never closes.
    private const int MaxDrainTurns = 1024;

    private readonly object _gate = new();
    private readonly Queue<WorkItem> _queue = new();
    private readonly PumpContext _context;
    private bool _pumping;

    /// <summary>Creates a driver whose loops advance only when <see cref="Pump"/> is called.</summary>
    public ManualPumpDriver() => _context = new PumpContext(this);

    /// <inheritdoc />
    public IReadLoopHandle Start(Func<CancellationToken, Task> loop, CancellationToken cancellationToken)
    {
        if (loop is null) throw new ArgumentNullException(nameof(loop));

        var handle = new Handle();

        // Queue the START itself, so the loop's very first synchronous stretch also runs under the
        // pump context — otherwise its first await would capture whatever context Start's caller
        // happened to be on.
        Enqueue(_ => handle.Run(loop, cancellationToken), null);

        // Disposal usually means the host has stopped pumping (a MonoBehaviour's OnDestroy runs
        // after its last Update), so drain here or every teardown would wait out the client's
        // dispose timeout. Cancel fires this synchronously on the disposing thread, which is the
        // main thread in the only host that uses this driver.
        handle.Registration = cancellationToken.Register(DrainForShutdown);
        // An ALREADY-cancelled token runs that callback inside Register, so the loop can be finished
        // before there is a registration to hand it. Release it here instead of leaking it.
        if (!handle.IsRunning)
        {
            handle.Registration.Dispose();
        }
        return handle;
    }

    /// <summary>
    /// Advances every loop this driver is driving by one turn each, on the calling thread, and
    /// returns how many continuations ran. Call once per frame.
    /// </summary>
    public int Pump()
    {
        // Only work already queued is run: a continuation that re-queues (a poll loop) is picked up
        // by the NEXT pump, so one call can never spin a frame away.
        int pending;
        lock (_gate)
        {
            if (_pumping)
            {
                return 0; // re-entrant call from inside a continuation — the outer pump owns the queue
            }
            pending = _queue.Count;
            if (pending == 0)
            {
                return 0;
            }
            _pumping = true;
        }

        SynchronizationContext? previous = SynchronizationContext.Current;
        SynchronizationContext.SetSynchronizationContext(_context);
        int ran = 0;
        try
        {
            for (int i = 0; i < pending; i++)
            {
                WorkItem item;
                lock (_gate)
                {
                    if (_queue.Count == 0)
                    {
                        break;
                    }
                    item = _queue.Dequeue();
                }
                ran++;
                item.Callback(item.State);
            }
        }
        finally
        {
            SynchronizationContext.SetSynchronizationContext(previous);
            lock (_gate)
            {
                _pumping = false;
            }
        }
        return ran;
    }

    private void DrainForShutdown()
    {
        lock (_gate)
        {
            if (_pumping)
            {
                return; // disposing from inside Update() — the pump in progress keeps draining
            }
        }
        for (int turn = 0; turn < MaxDrainTurns && Pump() > 0; turn++)
        {
        }
    }

    internal void Enqueue(SendOrPostCallback callback, object? state)
    {
        lock (_gate)
        {
            _queue.Enqueue(new WorkItem(callback, state));
        }
    }

    private readonly struct WorkItem
    {
        public WorkItem(SendOrPostCallback callback, object? state)
        {
            Callback = callback;
            State = state;
        }

        public SendOrPostCallback Callback { get; }
        public object? State { get; }
    }

    // Posting queues; sending runs inline. The only host with no thread pool is also single-threaded,
    // so an inline Send is on the pump thread by construction.
    private sealed class PumpContext : SynchronizationContext
    {
        private readonly ManualPumpDriver _driver;

        public PumpContext(ManualPumpDriver driver) => _driver = driver;

        public override void Post(SendOrPostCallback d, object? state) => _driver.Enqueue(d, state);

        public override void Send(SendOrPostCallback d, object? state) => d(state);

        public override SynchronizationContext CreateCopy() => this;
    }

    private sealed class Handle : IReadLoopHandle
    {
        private readonly TaskCompletionSource<object?> _completion =
            new(TaskCreationOptions.RunContinuationsAsynchronously);
        private volatile bool _finished;

        public CancellationTokenRegistration Registration { get; set; }

        // True from Start, before the loop has executed a single instruction — see IReadLoopHandle.
        public bool IsRunning => !_finished;

        public Task Completion => _completion.Task;

        public void Run(Func<CancellationToken, Task> loop, CancellationToken cancellationToken)
        {
            Task task;
            try
            {
                task = loop(cancellationToken);
            }
            catch
            {
                Finish();
                return;
            }
            // Continues under the pump context that Pump() installed, so it needs no scheduling of
            // its own; ContinueWith rather than await keeps this method synchronous for the pump.
            task.ContinueWith(
                (_, s) => ((Handle)s!).Finish(),
                this,
                CancellationToken.None,
                TaskContinuationOptions.ExecuteSynchronously,
                TaskScheduler.Current);
        }

        private void Finish()
        {
            _finished = true;
            Registration.Dispose();
            _completion.TrySetResult(null);
        }
    }
}
