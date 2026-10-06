// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// A record of every value seen, answering quantiles on request. The acceptance soak holds a frame time per frame and an
    /// observation lag per measurement over half an hour, a few hundred thousand floats, which is smaller than one screenshot:
    /// keeping them all is the honest way to report p99 (a sketch would be an approximation of the one number asked for).
    /// </summary>
    public sealed class Quantiles
    {
        readonly List<float> values = new List<float>();

        public int Count => values.Count;

        public void Add(double value) => values.Add((float)value);

        public void Clear() => values.Clear();

        /// <summary>The nearest-rank quantile (0..1) of what was added, or null when nothing was.</summary>
        public double? Of(double q)
        {
            if (values.Count == 0) return null;
            var sorted = values.ToArray();
            Array.Sort(sorted);
            var rank = (int)Math.Ceiling(Math.Max(0.0, Math.Min(1.0, q)) * sorted.Length);
            return sorted[Math.Max(0, Math.Min(sorted.Length - 1, rank - 1))];
        }

        public double? Max
        {
            get
            {
                if (values.Count == 0) return null;
                var m = values[0];
                foreach (var v in values)
                    if (v > m) m = v;
                return m;
            }
        }
    }
}
