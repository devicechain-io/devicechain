// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Demos.Platform
{
    /// <summary>What <see cref="AlarmGapDetector.Check"/> found: the alarm stream and the platform's list disagree and have for a while.</summary>
    public readonly struct AlarmGap
    {
        public AlarmGap(IReadOnlyList<string> missed, IReadOnlyList<string> endedUnseen)
        {
            Missed = missed;
            EndedUnseen = endedUnseen;
        }

        /// <summary>Alarms the platform lists as active that the observer never heard about.</summary>
        public IReadOnlyList<string> Missed { get; }

        /// <summary>Alarms the observer holds as active that the platform no longer lists.</summary>
        public IReadOnlyList<string> EndedUnseen { get; }

        public bool Any => (Missed != null && Missed.Count > 0) || (EndedUnseen != null && EndedUnseen.Count > 0);
    }

    /// <summary>
    /// The alarm stream is silent when nothing happens, which is exactly what a stream that stopped delivering looks like: a
    /// subscription cannot tell its own death from a quiet hour. So the observer also asks the platform for its active alarms
    /// on a timer, and this compares that answer with what the observer holds. A difference that lasts longer than
    /// <c>grace</c> is a gap (the stream is normally within a second of a poll, so a shorter one is the two views being a
    /// moment apart, not a fault). A gap is reported once per grace period for as long as it lasts.
    /// </summary>
    public sealed class AlarmGapDetector
    {
        readonly TimeSpan grace;
        readonly Dictionary<string, DateTimeOffset> unheard = new Dictionary<string, DateTimeOffset>(StringComparer.Ordinal);
        readonly Dictionary<string, DateTimeOffset> unended = new Dictionary<string, DateTimeOffset>(StringComparer.Ordinal);

        public AlarmGapDetector(TimeSpan grace)
        {
            if (grace < TimeSpan.Zero) throw new ArgumentOutOfRangeException(nameof(grace), "the grace period cannot be negative");
            this.grace = grace;
        }

        /// <param name="platformActive">Tokens of the alarms the platform lists as active, as of <paramref name="now"/>.</param>
        /// <param name="heldActive">Tokens of the alarms the observer holds as active.</param>
        public AlarmGap Check(IEnumerable<string> platformActive, IEnumerable<string> heldActive, DateTimeOffset now)
        {
            var platform = new HashSet<string>(platformActive ?? Array.Empty<string>(), StringComparer.Ordinal);
            var held = new HashSet<string>(heldActive ?? Array.Empty<string>(), StringComparer.Ordinal);
            var missed = Expired(unheard, platform, held, now);
            var ended = Expired(unended, held, platform, now);
            return new AlarmGap(missed, ended);
        }

        // tokens in `have` and not in `other`, tracked since first sight; those older than the grace are returned and their clock restarts
        List<string> Expired(Dictionary<string, DateTimeOffset> since, HashSet<string> have, HashSet<string> other, DateTimeOffset now)
        {
            var stale = new List<string>();
            foreach (var t in new List<string>(since.Keys))
                if (!have.Contains(t) || other.Contains(t)) since.Remove(t);
            foreach (var t in have)
            {
                if (other.Contains(t)) continue;
                if (!since.TryGetValue(t, out var first))
                {
                    since[t] = now;
                    continue;
                }

                if (now - first >= grace)
                {
                    stale.Add(t);
                    since[t] = now;
                }
            }

            stale.Sort(StringComparer.Ordinal);
            return stale;
        }
    }
}
