// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>Frame-time statistics for a measured run: mean and the percentiles a viewer feels (a smooth average hides a stutter).</summary>
    public sealed class FrameStats
    {
        public int Frames;
        public double MeanMs, P50Ms, P95Ms, P99Ms, MaxMs;

        /// <summary>The percentile by the nearest-rank method (the value at or below which at least p percent of the frames fall).</summary>
        public static double Percentile(IReadOnlyList<double> sorted, double p)
        {
            if (sorted == null || sorted.Count == 0) throw new ArgumentException("there are no frames to take a percentile of", nameof(sorted));
            if (!(p > 0 && p <= 100)) throw new ArgumentOutOfRangeException(nameof(p), "a percentile is above 0 and at most 100");
            int rank = (int)Math.Ceiling(p / 100.0 * sorted.Count);
            return sorted[Math.Max(1, Math.Min(sorted.Count, rank)) - 1];
        }

        public static FrameStats Of(IReadOnlyList<double> frameMs)
        {
            if (frameMs == null || frameMs.Count == 0) throw new ArgumentException("there are no frames to summarise", nameof(frameMs));
            var sorted = new List<double>(frameMs);
            sorted.Sort();
            double sum = 0;
            foreach (var v in sorted) sum += v;
            return new FrameStats
            {
                Frames = sorted.Count, MeanMs = sum / sorted.Count, P50Ms = Percentile(sorted, 50), P95Ms = Percentile(sorted, 95), P99Ms = Percentile(sorted, 99), MaxMs = sorted[sorted.Count - 1],
            };
        }

        public override string ToString()
        {
            var c = CultureInfo.InvariantCulture;
            return Frames + " frames, mean " + MeanMs.ToString("0.00", c) + " ms, p50 " + P50Ms.ToString("0.00", c) + ", p95 " + P95Ms.ToString("0.00", c) + ", p99 " + P99Ms.ToString("0.00", c) + ", max " + MaxMs.ToString("0.00", c) + " ms";
        }
    }
}
