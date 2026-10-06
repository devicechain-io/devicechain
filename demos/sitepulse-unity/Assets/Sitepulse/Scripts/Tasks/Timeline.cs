// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>One line of a machine's story: when, what kind of thing, and the plain words.</summary>
    public readonly struct TimelineRow
    {
        public TimelineRow(DateTimeOffset at, string kind, string text)
        {
            At = at;
            Kind = kind;
            Text = text;
        }

        public DateTimeOffset At { get; }
        public string Kind { get; }
        public string Text { get; }

        public override string ToString() => At.UtcDateTime.ToString("HH:mm:ss", CultureInfo.InvariantCulture) + "  " + Kind.PadRight(KindWidth) + " " + Text;

        public const int KindWidth = 10;
    }

    /// <summary>The kinds of row a machine's timeline has. What the PLATFORM says about a command is on the card, never here.</summary>
    public static class TimelineKinds
    {
        public const string Received = "received", Accepted = "accepted", Refused = "refused", Superseded = "superseded",
            Arrived = "arrived", Refuelling = "refuelling", Outcome = "outcome", Returning = "returning", Working = "working",
            Presenter = "presenter", Reset = "reset", Stalled = "stalled";
    }

    /// <summary>
    /// What each machine did with the commands it was given, in order, kept short and in plain text:
    /// <c>received</c>, <c>accepted: route 212 m, ETA 48 s</c>, <c>superseded</c>, <c>arrived</c>,
    /// <c>refuelling</c> and the outcome. It is the DEVICE's own account of what it did locally; the
    /// platform's state of the command is the card's, read from the platform. Main thread only.
    /// </summary>
    public sealed class Timeline
    {
        public const int MaxRowsPerMachine = 60;

        readonly Dictionary<string, List<TimelineRow>> rows = new Dictionary<string, List<TimelineRow>>(StringComparer.Ordinal);
        readonly Func<DateTimeOffset> clock;

        public Timeline(Func<DateTimeOffset> clock = null)
        {
            this.clock = clock ?? (() => DateTimeOffset.UtcNow);
        }

        /// <summary>Bumped on every row, so a view redraws only when something was said.</summary>
        public int Version { get; private set; }

        /// <summary>The machine that most recently received a command; null before any.</summary>
        public string LastCommanded { get; private set; }

        /// <summary>Raised for every row, as it is added (the machine and the row). A recording listens here.</summary>
        public event Action<string, TimelineRow> Added;

        public void Add(string machine, string kind, string text)
        {
            if (!rows.TryGetValue(machine, out var list)) rows[machine] = list = new List<TimelineRow>();
            var row = new TimelineRow(clock(), kind, text);
            list.Add(row);
            if (list.Count > MaxRowsPerMachine) list.RemoveRange(0, list.Count - MaxRowsPerMachine);
            if (kind == TimelineKinds.Received) LastCommanded = machine;
            Version++;
            Added?.Invoke(machine, row);
        }

        public IReadOnlyList<TimelineRow> Rows(string machine) =>
            rows.TryGetValue(machine, out var list) ? (IReadOnlyList<TimelineRow>)list : Array.Empty<TimelineRow>();

        /// <summary>The newest <paramref name="max"/> rows of a machine as text, oldest first.</summary>
        public string Text(string machine, int max)
        {
            var list = Rows(machine);
            var sb = new StringBuilder();
            for (var i = Math.Max(0, list.Count - max); i < list.Count; i++)
            {
                if (sb.Length > 0) sb.Append('\n');
                sb.Append(list[i]);
            }

            return sb.ToString();
        }
    }
}
