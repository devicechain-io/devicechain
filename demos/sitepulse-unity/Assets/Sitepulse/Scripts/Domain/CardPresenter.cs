// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>How a card's value is inked.</summary>
    public enum RowTone { Ink, Warn, Dim, Grey }

    /// <summary>The colour of a card's status dot.</summary>
    public enum DotTone { Ok, Warn, Grey, Muted }

    /// <summary>One row of a card: whether it is drawn at all, its text and its ink.</summary>
    public readonly struct RowView
    {
        public RowView(bool shown, string text, RowTone tone)
        {
            Shown = shown;
            Text = text;
            Tone = tone;
        }

        public bool Shown { get; }
        public string Text { get; }
        public RowTone Tone { get; }
    }

    /// <summary>A card's status dot and the tag beside it.</summary>
    public readonly struct StatusView
    {
        public StatusView(DotTone dot, string tag)
        {
            Dot = dot;
            Tag = tag;
        }

        public DotTone Dot { get; }
        public string Tag { get; }
    }

    /// <summary>
    /// What a card prints, decided from values alone (no scene, no UI): the one place a value's age and
    /// the stream's health turn into text and colour. An observed value with nothing behind it, or
    /// nothing newer than a minute, reads as a dash and never as a number; while the stream is not live
    /// nothing is called fresh.
    /// </summary>
    public static class CardPresenter
    {
        public const string NoValue = "—";

        /// <summary>The most rows a card has: the alarm's (or the featured) metric, payload, fuel, speed and the command's state.</summary>
        public const int MaxRows = 5;

        /// <summary>The token of the speed row in <see cref="RowPlan"/>.</summary>
        public const string SpeedToken = "@speed";

        /// <summary>The token of the command row in <see cref="RowPlan"/>.</summary>
        public const string CommandToken = "@command";

        /// <summary>
        /// The rows a card shows, top to bottom, as measurement keys and the two tokens <see cref="SpeedToken"/> and <see cref="CommandToken"/>.
        /// A machine's own rows are payload, fuel and speed, led by <paramref name="first"/> (the alarm's metric, or the one a shot features)
        /// when there is one; a command's state, when it has one, is ALWAYS a row, so a card that is full of metrics never hides what the
        /// operator just sent. Never more than <see cref="MaxRows"/>.
        /// </summary>
        public static List<string> RowPlan(bool plant, string first, bool hasCommand)
        {
            var plan = new List<string>();
            if (plant)
            {
                plan.Add(MeasurementKeys.ThroughputTph);
                plan.Add(MeasurementKeys.PlantRunning);
                return plan;
            }

            if (first != null) plan.Add(first);
            if (first != MeasurementKeys.PayloadT) plan.Add(MeasurementKeys.PayloadT);
            if (first != MeasurementKeys.FuelPct) plan.Add(MeasurementKeys.FuelPct);
            plan.Add(SpeedToken);
            if (hasCommand) plan.Add(CommandToken);
            if (plan.Count > MaxRows) throw new InvalidOperationException("a card plan of " + plan.Count + " rows");
            return plan;
        }

        /// <summary>A measurement (or speed) row. <paramref name="occurredAt"/> is when the value happened on the device.</summary>
        public static RowView Row(bool observed, string value, DateTimeOffset? occurredAt, DateTimeOffset now, bool streamLive, bool warn = false, TimeSpan? freshWithin = null)
        {
            if (value == null) return observed ? new RowView(true, NoValue, RowTone.Grey) : new RowView(false, null, RowTone.Ink);
            var fresh = !observed ? Freshness.Fresh
                : occurredAt.HasValue ? FreshnessRule.Classify(occurredAt.Value, now, streamLive, freshWithin) : Freshness.Gone;
            return Build(value, fresh, warn);
        }

        /// <summary>
        /// The command row. A finished command is a fact that does not age; one still in flight is only as
        /// current as the last time the command poll saw it, so it dims and greys when that poll stops.
        /// </summary>
        public static RowView CommandRow(bool observed, string value, bool final, DateTimeOffset? lastSeenAt, DateTimeOffset now, bool streamLive)
        {
            var fresh = !observed || final ? Freshness.Fresh
                : lastSeenAt.HasValue ? FreshnessRule.Classify(lastSeenAt.Value, now, streamLive, FreshnessRule.CommandFreshWithin) : Freshness.Gone;
            return Build(value, fresh, false);
        }

        static RowView Build(string value, Freshness fresh, bool warn)
        {
            if (fresh == Freshness.Gone) return new RowView(true, NoValue, RowTone.Grey);
            var tone = fresh == Freshness.Stale ? RowTone.Dim : fresh == Freshness.Fresh ? (warn ? RowTone.Warn : RowTone.Ink) : RowTone.Grey;
            return new RowView(true, value, tone);
        }

        /// <summary>
        /// The status dot and tag. An observed card's dot is the freshness of the device's own newest
        /// measurement (<paramref name="newestOccurredAt"/>); the alarm keeps the card's edge and bar.
        /// </summary>
        public static StatusView Status(Provenance provenance, DateTimeOffset? newestOccurredAt, DateTimeOffset now, bool streamLive, bool alarm, bool stopped, bool replayTag = true)
        {
            if (provenance == Provenance.Illustrative)
                return new StatusView(alarm ? DotTone.Warn : stopped ? DotTone.Muted : DotTone.Ok, "illustrative");
            var f = newestOccurredAt.HasValue ? FreshnessRule.Classify(newestOccurredAt.Value, now, streamLive) : Freshness.Gone;
            var age = newestOccurredAt.HasValue ? (int)Math.Max(0.0, (now - newestOccurredAt.Value).TotalSeconds) : 0;
            var dot = f == Freshness.Fresh ? DotTone.Ok : f == Freshness.Stale ? DotTone.Warn : DotTone.Grey;
            var tag = f == Freshness.Fresh ? "observed" : f == Freshness.Stale ? "stale " + age + " s"
                : !newestOccurredAt.HasValue ? "no data" : f == Freshness.Gone ? "no data > 1 min" : "no data " + age + " s";
            if (provenance == Provenance.Replayed)
            {
                // a replayed card ages as the live one did (the clock it is judged by is the recording's), and says it is a replay;
                // an offline render turns the words off (the footage is disclosed in its description), and a fresh card then says nothing
                if (!replayTag) tag = f == Freshness.Fresh ? "" : tag;
                else tag = f == Freshness.Fresh ? "replayed" : "replayed · " + tag;
            }

            return new StatusView(dot, tag);
        }
    }
}
