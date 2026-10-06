// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Threading;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.DevicePlane
{
    public enum DeviceEventKind { LinkState, Started, StartFailed, FirstPublish, Command, PumpFaulted, Task, CommandRefused }

    /// <summary>
    /// A plain record of something that happened on an SDK thread, queued for the main thread. It
    /// carries the run generation it was raised under, and no reference to anything in the scene.
    /// </summary>
    public sealed class DeviceEvent
    {
        public DeviceEvent(int generation, string externalId, DeviceEventKind kind, LinkState state = LinkState.Starting, string text = null, TaskRequest task = null)
        {
            Task = task;
            Generation = generation;
            ExternalId = externalId;
            Kind = kind;
            State = state;
            Text = text;
        }

        public int Generation { get; }
        public string ExternalId { get; }
        public DeviceEventKind Kind { get; }
        public LinkState State { get; }
        public string Text { get; }

        /// <summary>A validated command for the machine's task layer (<see cref="DeviceEventKind.Task"/> only).</summary>
        public TaskRequest Task { get; }
    }

    /// <summary>
    /// The hand-off from SDK pool threads to the main thread: a concurrent queue, drained in
    /// <c>Update</c>. An event from a generation that is no longer current is dropped, and counted, so
    /// a callback from a torn-down run cannot touch the next one.
    /// </summary>
    public sealed class DeviceInbox
    {
        readonly ConcurrentQueue<DeviceEvent> queue = new ConcurrentQueue<DeviceEvent>();
        long stale;

        public long StaleDropped => Interlocked.Read(ref stale);
        public int Pending => queue.Count;

        public void Post(DeviceEvent e) => queue.Enqueue(e);

        /// <summary>
        /// Answers every command still waiting in the queue with <paramref name="reason"/> and discards everything else.
        /// For the end of a run, when nothing will ever drain the queue again.
        /// </summary>
        public int FailQueuedTasks(string reason)
        {
            var n = 0;
            while (queue.TryDequeue(out var e))
                if (e.Task != null && e.Task.Complete(TaskResult.Fail(reason))) n++;
            return n;
        }

        /// <summary>Hands every queued event of generation <paramref name="current"/> to <paramref name="apply"/>; returns how many it applied.</summary>
        public int Drain(int current, Action<DeviceEvent> apply)
        {
            var n = 0;
            while (queue.TryDequeue(out var e))
            {
                if (e.Generation != current)
                {
                    Interlocked.Increment(ref stale);
                    // a command from a run that is over still gets its answer: nobody is left to run it
                    e.Task?.Complete(TaskResult.Fail(TaskReasons.Reset));
                    continue;
                }

                apply(e);
                n++;
            }

            return n;
        }
    }
}
