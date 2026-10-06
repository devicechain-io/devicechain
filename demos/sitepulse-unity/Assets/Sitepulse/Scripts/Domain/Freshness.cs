// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>How current an observed value is.</summary>
    public enum Freshness
    {
        /// <summary>Seen within the window the cadence allows: shown normally, green dot.</summary>
        Fresh,
        /// <summary>Late: dimmed, amber dot, tagged stale.</summary>
        Stale,
        /// <summary>Old, or the observer is not live: greyed, grey dot.</summary>
        Old,
        /// <summary>No observation for a minute: the value is not shown at all.</summary>
        Gone,
    }

    /// <summary>
    /// The freshness rule (age is now minus the value's own occurrence time; both clocks are this
    /// host's). Equipment measurements arrive each second, so fresh is up to three of them; a value
    /// older than 15 s is old; and while the measurement stream is not live nothing can be called fresh
    /// or merely stale, since the observer cannot have seen anything newer. A value a minute old is no
    /// longer shown. An age below zero (a device clock ahead of this one) counts as zero.
    /// </summary>
    public static class FreshnessRule
    {
        public static readonly TimeSpan FreshWithin = TimeSpan.FromSeconds(3);
        public static readonly TimeSpan StaleWithin = TimeSpan.FromSeconds(15);
        public static readonly TimeSpan GoneAfter = TimeSpan.FromSeconds(60);

        /// <summary>A Location event is published at most every two seconds when the machine is stopped
        /// and read by a one-second poll: fresh is three of those periods plus the poll.</summary>
        public static readonly TimeSpan LocationFreshWithin = TimeSpan.FromSeconds(7);

        /// <summary>A command that is still in flight is re-read by a one-second poll: it is fresh for
        /// the poll's period plus slack since the platform last said what state it is in.</summary>
        public static readonly TimeSpan CommandFreshWithin = TimeSpan.FromSeconds(5);

        public static Freshness Classify(TimeSpan age, bool streamLive, TimeSpan? freshWithin = null)
        {
            if (age < TimeSpan.Zero) age = TimeSpan.Zero;
            if (age > GoneAfter) return Freshness.Gone;
            if (age > StaleWithin) return Freshness.Old;
            if (!streamLive) return Freshness.Old;
            return age <= (freshWithin ?? FreshWithin) ? Freshness.Fresh : Freshness.Stale;
        }

        public static Freshness Classify(DateTimeOffset occurredAt, DateTimeOffset now, bool streamLive, TimeSpan? freshWithin = null) =>
            Classify(now - occurredAt, streamLive, freshWithin);
    }
}
