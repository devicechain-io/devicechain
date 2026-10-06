// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Which commands the observer must ask for by name. The command poll's page lists only commands that
    /// are not finished; one that finishes drops off the page, so to learn how it ended the observer names
    /// every command it has seen unfinished until it is seen finished. Bounded: at <see cref="MaxPending"/>
    /// the OLDEST pending token is given up to make room (a new command is never the one refused), and a
    /// token the platform stops returning when asked for by name is dropped after
    /// <see cref="MissedPollsBeforeDrop"/> polls and counted, rather than asked for forever.
    /// </summary>
    public sealed class CommandTracker
    {
        public const int MaxPending = 200;
        public const int MissedPollsBeforeDrop = 3;

        readonly int max;
        readonly List<string> order = new List<string>();
        readonly Dictionary<string, int> missed = new Dictionary<string, int>(StringComparer.Ordinal);

        public CommandTracker(int maxPending = MaxPending)
        {
            max = maxPending;
        }

        /// <summary>Pending tokens given up to make room.</summary>
        public int Evicted { get; private set; }

        /// <summary>Pending tokens the platform stopped answering for.</summary>
        public int Vanished { get; private set; }

        public int Count
        {
            get
            {
                lock (order) return order.Count;
            }
        }

        /// <summary>The tokens the next poll must name, oldest first.</summary>
        public string[] Named()
        {
            lock (order) return order.ToArray();
        }

        /// <summary>
        /// What a poll that named <paramref name="named"/> got back. A command seen unfinished is pending;
        /// one seen finished is not; a named token the answer lacks has missed a poll.
        /// </summary>
        public void Observe(IReadOnlyCollection<string> named, IReadOnlyList<CommandItem> returned)
        {
            lock (order)
            {
                var seen = new HashSet<string>(StringComparer.Ordinal);
                foreach (var c in returned)
                {
                    var token = c.Command.Token;
                    seen.Add(token);
                    if (c.Command.IsTerminal)
                    {
                        Remove(token);
                        continue;
                    }

                    missed.Remove(token);
                    if (order.Contains(token)) continue;
                    if (order.Count >= max)
                    {
                        Remove(order[0]);
                        Evicted++;
                    }

                    order.Add(token);
                }

                if (named == null) return;
                foreach (var token in named)
                {
                    if (seen.Contains(token) || !order.Contains(token)) continue;
                    missed.TryGetValue(token, out var n);
                    if (++n >= MissedPollsBeforeDrop)
                    {
                        Remove(token);
                        Vanished++;
                    }
                    else missed[token] = n;
                }
            }
        }

        void Remove(string token)
        {
            order.Remove(token);
            missed.Remove(token);
        }
    }
}
