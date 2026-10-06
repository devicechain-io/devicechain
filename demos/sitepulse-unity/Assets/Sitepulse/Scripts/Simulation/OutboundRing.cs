// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Simulation
{
    /// <summary>
    /// A device's bounded outbound queue: at most <see cref="Capacity"/> samples, and when it is full
    /// the OLDEST goes (a stale reading is worth less than a fresh one), counted in
    /// <see cref="Dropped"/> so the loss is visible and never silent. Thread-safe: the main thread
    /// enqueues and a pool thread sends.
    /// </summary>
    public sealed class OutboundRing
    {
        public const int Capacity = 32;

        readonly object gate = new object();
        readonly LinkedList<Sample> items = new LinkedList<Sample>();
        long dropped;

        public long Dropped
        {
            get { lock (gate) return dropped; }
        }

        public int Count
        {
            get { lock (gate) return items.Count; }
        }

        public void Enqueue(Sample sample)
        {
            if (sample == null) throw new ArgumentNullException(nameof(sample));
            lock (gate)
            {
                if (items.Count >= Capacity)
                {
                    items.RemoveFirst();
                    dropped++;
                }

                items.AddLast(sample);
            }
        }

        /// <summary>The oldest sample still waiting, without removing it: it stays until it is acknowledged.</summary>
        public bool TryPeek(out Sample sample)
        {
            lock (gate)
            {
                sample = items.First?.Value;
                return sample != null;
            }
        }

        /// <summary>Removes the sample with this sequence, if it is still there (it may have been dropped while it was in flight).</summary>
        public bool Remove(long sequence)
        {
            lock (gate)
            {
                for (var n = items.First; n != null; n = n.Next)
                {
                    if (n.Value.Sequence != sequence) continue;
                    items.Remove(n);
                    return true;
                }

                return false;
            }
        }
    }
}
