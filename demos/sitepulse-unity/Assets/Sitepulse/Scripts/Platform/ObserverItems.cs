// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Threading;

namespace DeviceChain.Sitepulse.Platform
{
    public enum StreamKind { Measurements, Alarms }

    /// <summary>
    /// A plain record of something the observer saw on a network task, queued for the main thread. It
    /// carries the run generation it was raised under and no reference to anything in the scene.
    /// </summary>
    public abstract class ObserverItem
    {
        public int Generation { get; internal set; }
    }

    public sealed class MeasurementItem : ObserverItem
    {
        public MeasurementItem(string deviceToken, string name, double value, DateTimeOffset occurredAt, DateTimeOffset observedAt, bool fromSnapshot)
        {
            DeviceToken = deviceToken;
            Name = name;
            Value = value;
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
            FromSnapshot = fromSnapshot;
        }

        public string DeviceToken { get; }
        public string Name { get; }
        public double Value { get; }
        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
        public bool FromSnapshot { get; }
    }

    public sealed class AlarmItem : ObserverItem
    {
        public AlarmItem(string deviceToken, ObservedAlarm alarm)
        {
            DeviceToken = deviceToken;
            Alarm = alarm;
        }

        public string DeviceToken { get; }
        public ObservedAlarm Alarm { get; }
    }

    /// <summary>
    /// The platform's list of active alarms as of <see cref="RequestedAt"/>. It is the platform's WHOLE
    /// answer only when <see cref="Truncated"/> is false: a truncated list says nothing about the alarms it
    /// does not name, so it is never used to clear one.
    /// </summary>
    public sealed class AlarmSnapshotItem : ObserverItem
    {
        public AlarmSnapshotItem(IReadOnlyList<AlarmItem> alarms, DateTimeOffset requestedAt, DateTimeOffset observedAt, bool truncated = false, int? totalRecords = null)
        {
            Alarms = alarms;
            RequestedAt = requestedAt;
            ObservedAt = observedAt;
            Truncated = truncated;
            TotalRecords = totalRecords;
        }

        /// <summary>The platform has more active alarms than this list holds.</summary>
        public bool Truncated { get; }

        /// <summary>How many active alarms the platform said it has, when it said.</summary>
        public int? TotalRecords { get; }

        public IReadOnlyList<AlarmItem> Alarms { get; }
        public DateTimeOffset RequestedAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    public sealed class LocationItem : ObserverItem
    {
        public LocationItem(string deviceToken, ObservedLocation location)
        {
            DeviceToken = deviceToken;
            Location = location;
        }

        public string DeviceToken { get; }
        public ObservedLocation Location { get; }
    }

    public sealed class CommandItem : ObserverItem
    {
        public CommandItem(string deviceToken, ObservedCommand command)
        {
            DeviceToken = deviceToken;
            Command = command;
        }

        public string DeviceToken { get; }
        public ObservedCommand Command { get; }
    }

    public sealed class PresenceItem : ObserverItem
    {
        public PresenceItem(string deviceToken, ObservedPresence presence)
        {
            DeviceToken = deviceToken;
            Presence = presence;
        }

        public string DeviceToken { get; }
        public ObservedPresence Presence { get; }
    }

    /// <summary>A stream changed state, or a poll started or stopped failing.</summary>
    public sealed class StatusItem : ObserverItem
    {
        public StatusItem(string source, string state, string reason, DateTimeOffset at)
        {
            Source = source;
            State = state;
            Reason = reason;
            At = at;
        }

        /// <summary>"measurements", "alarms", or a poll's name.</summary>
        public string Source { get; }

        /// <summary>A <see cref="StreamState"/> name for a stream; "ok" or "failed" for a poll.</summary>
        public string State { get; }

        public string Reason { get; }
        public DateTimeOffset At { get; }
    }

    /// <summary>
    /// The hand-off from network tasks to the main thread: a concurrent queue, drained in
    /// <c>Update</c>. An item from a generation that is no longer current is dropped, and counted, so a
    /// callback from a stopped observer cannot touch the next one.
    /// </summary>
    public sealed class ObserverInbox
    {
        readonly ConcurrentQueue<ObserverItem> queue = new ConcurrentQueue<ObserverItem>();
        long stale;

        public long StaleDropped => Interlocked.Read(ref stale);
        public int Pending => queue.Count;

        public void Post(ObserverItem item) => queue.Enqueue(item);

        /// <summary>Hands every queued item of generation <paramref name="current"/> to <paramref name="apply"/>; returns how many it applied.</summary>
        public int Drain(int current, Action<ObserverItem> apply)
        {
            var n = 0;
            while (queue.TryDequeue(out var item))
            {
                if (item.Generation != current)
                {
                    Interlocked.Increment(ref stale);
                    continue;
                }

                apply(item);
                n++;
            }

            return n;
        }
    }
}
