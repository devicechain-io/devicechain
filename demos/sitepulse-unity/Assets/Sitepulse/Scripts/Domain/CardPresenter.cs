// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

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
