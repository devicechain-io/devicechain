// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// A poll that keeps its period while the server answers and backs off while it does not: a server that
    /// is down is not asked once a second by every poll. The wait doubles with each failure in a row, up to
    /// <see cref="MaxWait"/> (or the poll's own period, if that is longer), and the next success returns it
    /// to the period.
    /// </summary>
    public static class PollLoop
    {
        public static readonly TimeSpan MaxWait = TimeSpan.FromSeconds(10);

        /// <summary>The wait before the next attempt after <paramref name="consecutiveFailures"/> failures in a row.</summary>
        public static TimeSpan Wait(TimeSpan period, int consecutiveFailures)
        {
            if (consecutiveFailures <= 0) return period;
            var cap = period > MaxWait ? period : MaxWait;
            var seconds = period.TotalSeconds * Math.Pow(2, Math.Min(consecutiveFailures, 16));
            return seconds >= cap.TotalSeconds ? cap : TimeSpan.FromSeconds(seconds);
        }

        /// <param name="onResult">Told every attempt's outcome: null for success, else the failure.</param>
        /// <param name="delay">The wait; Task.Delay unless a test says otherwise.</param>
        public static async Task RunAsync(TimeSpan period, Func<CancellationToken, Task> body, Action<Exception> onResult,
            Func<DateTimeOffset> clock, Func<TimeSpan, CancellationToken, Task> delay, CancellationToken ct)
        {
            var failures = 0;
            while (!ct.IsCancellationRequested)
            {
                var began = clock();
                try
                {
                    await body(ct);
                    failures = 0;
                    onResult(null);
                }
                catch (OperationCanceledException) when (ct.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception e)
                {
                    failures++;
                    onResult(e);
                }

                var wait = Wait(period, failures) - (clock() - began);
                if (wait <= TimeSpan.Zero) continue;
                try { await delay(wait, ct); }
                catch (OperationCanceledException) { return; }
            }
        }
    }
}
