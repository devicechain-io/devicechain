// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sitepulse.Simulation
{
    /// <summary>Where a device's samples go. The SDK session is one; a test double is another.</summary>
    public interface ISampleSink
    {
        /// <summary>False while the link is down (starting, reconnecting, stopped): the pump waits and the ring buffers.</summary>
        bool CanPublish { get; }

        Task PublishAsync(Sample sample, CancellationToken cancellationToken);
    }

    public interface ISampleObserver
    {
        /// <summary>The broker acknowledged the sample.</summary>
        void Published(Sample sample, DateTimeOffset at);

        /// <summary>A send failed. <paramref name="permanent"/> means the sample itself is refused and has been discarded; otherwise it stays queued for another try.</summary>
        void SendFailed(Sample sample, string reason, bool permanent);
    }

    public enum PumpOutcome { Idle, NotReady, Backoff, Paced, Sent, Failed, Rejected }

    /// <summary>
    /// Limits how fast a backlog drains: never more than twice the device's normal event rate, so a
    /// reconnect is a catch-up at a measured pace and not a burst. In steady state, with production
    /// at the normal rate, it never delays anything.
    /// </summary>
    public sealed class DrainPacer
    {
        public const double MaxRateMultiple = 2.0;

        DateTimeOffset last = DateTimeOffset.MinValue;

        public TimeSpan MinGap(double nominalRatePerSecond)
            => TimeSpan.FromSeconds(1.0 / (MaxRateMultiple * Math.Max(nominalRatePerSecond, 0.01)));

        public bool TryAcquire(DateTimeOffset now, double nominalRatePerSecond)
        {
            if (last != DateTimeOffset.MinValue && now - last < MinGap(nominalRatePerSecond)) return false;
            last = now;
            return true;
        }

        public TimeSpan Remaining(DateTimeOffset now, double nominalRatePerSecond)
        {
            if (last == DateTimeOffset.MinValue) return TimeSpan.Zero;
            var left = MinGap(nominalRatePerSecond) - (now - last);
            return left < TimeSpan.Zero ? TimeSpan.Zero : left;
        }
    }

    /// <summary>
    /// Sends a device's ring, oldest first, one sample at a time. A sample stays in the ring until the
    /// broker acknowledges it, so a failed send is retried with THE SAME object: the same occurred
    /// time and the same values, never relabelled. A sample the SDK rejects as invalid
    /// (<see cref="ArgumentException"/>) can never succeed and is discarded, so it cannot block the
    /// queue behind it. Runs on a pool thread; <see cref="StepAsync"/> takes the clock as an argument
    /// so its pacing is testable without waiting.
    /// </summary>
    public sealed class SamplePump
    {
        static readonly TimeSpan[] Backoffs =
        {
            TimeSpan.FromMilliseconds(250), TimeSpan.FromMilliseconds(500), TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(2),
        };

        readonly OutboundRing ring;
        readonly ISampleSink sink;
        readonly Func<double> nominalRate;
        readonly ISampleObserver observer;
        readonly DrainPacer pacer = new DrainPacer();
        DateTimeOffset retryNotBefore = DateTimeOffset.MinValue;
        int failures;

        public SamplePump(OutboundRing ring, ISampleSink sink, Func<double> nominalRatePerSecond, ISampleObserver observer)
        {
            this.ring = ring ?? throw new ArgumentNullException(nameof(ring));
            this.sink = sink ?? throw new ArgumentNullException(nameof(sink));
            nominalRate = nominalRatePerSecond ?? throw new ArgumentNullException(nameof(nominalRatePerSecond));
            this.observer = observer ?? throw new ArgumentNullException(nameof(observer));
        }

        public async Task<PumpOutcome> StepAsync(DateTimeOffset now, CancellationToken ct)
        {
            if (!ring.TryPeek(out var sample)) return PumpOutcome.Idle;
            if (!sink.CanPublish) return PumpOutcome.NotReady;
            if (now < retryNotBefore) return PumpOutcome.Backoff;
            if (!pacer.TryAcquire(now, nominalRate())) return PumpOutcome.Paced;

            try
            {
                await sink.PublishAsync(sample, ct).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested)
            {
                throw;
            }
            catch (ArgumentException e)
            {
                ring.Remove(sample.Sequence);
                observer.SendFailed(sample, e.GetType().Name + ": " + e.Message, true);
                return PumpOutcome.Rejected;
            }
            catch (Exception e)
            {
                retryNotBefore = now + Backoffs[Math.Min(failures, Backoffs.Length - 1)];
                failures++;
                observer.SendFailed(sample, e.GetType().Name + ": " + e.Message, false);
                return PumpOutcome.Failed;
            }

            failures = 0;

            // A sample the ring dropped for room while it was in flight is already counted as dropped;
            // counting it published too would make produced = published + dropped + queued false.
            if (!ring.Remove(sample.Sequence)) return PumpOutcome.Sent;
            observer.Published(sample, now);
            return PumpOutcome.Sent;
        }

        public async Task RunAsync(CancellationToken ct)
        {
            try
            {
                while (!ct.IsCancellationRequested)
                {
                    var now = DateTimeOffset.UtcNow;
                    var outcome = await StepAsync(now, ct).ConfigureAwait(false);
                    if (outcome == PumpOutcome.Sent || outcome == PumpOutcome.Rejected) continue;
                    var wait = outcome == PumpOutcome.Paced ? pacer.Remaining(now, nominalRate())
                        : outcome == PumpOutcome.Backoff ? retryNotBefore - now
                        : outcome == PumpOutcome.NotReady ? TimeSpan.FromMilliseconds(200)
                        : TimeSpan.FromMilliseconds(25);
                    if (wait < TimeSpan.FromMilliseconds(5)) wait = TimeSpan.FromMilliseconds(5);
                    await Task.Delay(wait, ct).ConfigureAwait(false);
                }
            }
            catch (OperationCanceledException)
            {
                // stopping
            }
        }
    }
}
