// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// Where a card's value came from. A reading has exactly one provenance for its whole life, and
    /// every value written to it must declare the same one: an observed reading cannot be handed an
    /// illustrative number, and an illustrative one cannot be handed an observed number. This is how a
    /// Live card can never show a value the platform did not report.
    /// </summary>
    public enum Provenance { Illustrative, Observed, Replayed }

    /// <summary>When a value happened on the device (its own clock) and when this app saw it.</summary>
    public readonly struct Observation
    {
        public Observation(DateTimeOffset occurredAt, DateTimeOffset observedAt)
        {
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
        }

        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }
}
