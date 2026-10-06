// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class FreshnessTests
    {
        static Freshness At(double seconds, bool live = true) => FreshnessRule.Classify(TimeSpan.FromSeconds(seconds), live);

        [Test]
        public void ThreeSecondsIsStillFreshAndAnythingLaterIsStaleUntilFifteen()
        {
            Assert.AreEqual(Freshness.Fresh, At(0));
            Assert.AreEqual(Freshness.Fresh, At(1.2));
            Assert.AreEqual(Freshness.Fresh, At(3));
            Assert.AreEqual(Freshness.Stale, At(3.001));
            Assert.AreEqual(Freshness.Stale, At(9));
            Assert.AreEqual(Freshness.Stale, At(15));
            Assert.AreEqual(Freshness.Old, At(15.001));
            Assert.AreEqual(Freshness.Old, At(45));
            Assert.AreEqual(Freshness.Old, At(60));
        }

        [Test]
        public void AValueAMinuteOldIsNoLongerShown()
        {
            Assert.AreEqual(Freshness.Gone, At(60.001));
            Assert.AreEqual(Freshness.Gone, At(3600));
        }

        [Test]
        public void WhileTheStreamIsNotLiveNothingIsFreshOrMerelyStale()
        {
            Assert.AreEqual(Freshness.Old, At(0, live: false));
            Assert.AreEqual(Freshness.Old, At(3, live: false));
            Assert.AreEqual(Freshness.Old, At(10, live: false));
            Assert.AreEqual(Freshness.Old, At(30, live: false));
            Assert.AreEqual(Freshness.Gone, At(61, live: false));
        }

        [Test]
        public void ADeviceClockAheadOfOursCountsAsNow()
        {
            Assert.AreEqual(Freshness.Fresh, At(-4));
        }

        [Test]
        public void ALocationIsJudgedByTheCadenceItIsPublishedAt()
        {
            // a stopped machine reports its position every 2 s and the poll adds up to a second
            Assert.AreEqual(Freshness.Stale, FreshnessRule.Classify(TimeSpan.FromSeconds(4), true));
            Assert.AreEqual(Freshness.Fresh, FreshnessRule.Classify(TimeSpan.FromSeconds(4), true, FreshnessRule.LocationFreshWithin));
            Assert.AreEqual(Freshness.Fresh, FreshnessRule.Classify(TimeSpan.FromSeconds(7), true, FreshnessRule.LocationFreshWithin));
            Assert.AreEqual(Freshness.Stale, FreshnessRule.Classify(TimeSpan.FromSeconds(7.1), true, FreshnessRule.LocationFreshWithin));
            Assert.AreEqual(Freshness.Old, FreshnessRule.Classify(TimeSpan.FromSeconds(7), false, FreshnessRule.LocationFreshWithin));
        }
    }

    public sealed class ObservedStateTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
        const string Dev = "sp-hauler-06";

        static DateTimeOffset S(double seconds) => T0.AddSeconds(seconds);

        static ObservedAlarm Alarm(string token, string state, double occurred, string severity = "CRITICAL") =>
            new ObservedAlarm { Token = token, AlarmKey = "low-fuel", MetricKey = "fuel_pct", State = state, Severity = severity, OccurredAt = S(occurred), ObservedAt = S(occurred) };

        static ObservedCommand Cmd(string token, string status, double queued) =>
            new ObservedCommand { Token = token, Name = "goto-refuel", Status = status, QueuedAt = S(queued), ObservedAt = S(queued) };

        static double Value(ObservedState st, string name) => st.TryGet(Dev, out var d) ? d.Measurements[name].Value : double.NaN;

        [Test]
        public void ASnapshotNeverOverwritesANewerStreamedValue()
        {
            var st = new ObservedState(T0);
            st.ApplyMeasurement(Dev, "fuel_pct", 50, S(10), S(10.1), fromSnapshot: false);
            // the snapshot was taken before the streamed value happened and arrives after it
            st.ApplyMeasurement(Dev, "fuel_pct", 70, S(5), S(10.5), fromSnapshot: true);
            Assert.AreEqual(50, Value(st, "fuel_pct"));
            // at the same instant the stream's word stands
            st.ApplyMeasurement(Dev, "fuel_pct", 99, S(10), S(10.6), fromSnapshot: true);
            Assert.AreEqual(50, Value(st, "fuel_pct"));
            // a newer snapshot value does replace it
            st.ApplyMeasurement(Dev, "fuel_pct", 48, S(11), S(11.2), fromSnapshot: true);
            Assert.AreEqual(48, Value(st, "fuel_pct"));
            // and a late-arriving older stream frame does not step it back
            st.ApplyMeasurement(Dev, "fuel_pct", 51, S(9), S(11.3), fromSnapshot: false);
            Assert.AreEqual(48, Value(st, "fuel_pct"));
        }

        [Test]
        public void TheMergeRuleHoldsThroughTheApplierToo()
        {
            var st = new ObservedState(T0);
            var status = new ObserverStatus();
            ObserverApplier.Apply(st, status, new MeasurementItem(Dev, "fuel_pct", 50, S(10), S(10), false), null);
            ObserverApplier.Apply(st, status, new MeasurementItem(Dev, "fuel_pct", 70, S(5), S(11), true), null);
            Assert.AreEqual(50, Value(st, "fuel_pct"));
        }

        [Test]
        public void ASnapshotCannotBringBackAnAlarmTheStreamClearedOrOneItIsStaleAbout()
        {
            var st = new ObservedState(T0);
            st.ApplyAlarm(Dev, Alarm("a1", "CLEARED", 20));
            // the snapshot lists a1 as active, as it was raised at 10: older than the clearing
            st.ApplyAlarm(Dev, Alarm("a1", "ACTIVE", 10));
            Assert.IsFalse(st.TryGet(Dev, out var d) && d.Alarms["a1"].IsActive);
            // an escalation seen on the stream is not undone by the snapshot's older severity
            st.ApplyAlarm(Dev, Alarm("a2", "ACTIVE", 30, "CRITICAL"));
            st.ApplyAlarm(Dev, Alarm("a2", "ACTIVE", 12, "WARNING"));
            Assert.AreEqual("CRITICAL", d.Alarms["a2"].Severity);
        }

        [Test]
        public void AnAlarmThatEndedWhileTheObserverWasAwayIsClearedBySnapshot()
        {
            var st = new ObservedState(T0);
            st.ApplyAlarm(Dev, Alarm("ended", "ACTIVE", 5));
            st.ApplyAlarm(Dev, Alarm("still", "ACTIVE", 6));
            st.ApplyAlarm(Dev, Alarm("newer-than-request", "ACTIVE", 12));
            st.ReconcileActiveAlarms(new HashSet<string> { "still" }, S(10), S(10.5));
            st.TryGet(Dev, out var d);
            Assert.IsFalse(d.Alarms["ended"].IsActive);
            Assert.IsTrue(d.Alarms["still"].IsActive);
            Assert.IsTrue(d.Alarms["newer-than-request"].IsActive, "an event newer than the request is not the snapshot's to judge");
        }

        [Test]
        public void ATerminalCommandIsNeverReplacedByANonTerminalOne()
        {
            var st = new ObservedState(T0);
            st.ApplyCommand(Dev, Cmd("c1", "SENT", 1));
            st.ApplyCommand(Dev, Cmd("c1", "TIMEOUT", 1));
            st.ApplyCommand(Dev, Cmd("c1", "SENT", 1));   // the poll's page is a moment behind
            st.TryGet(Dev, out var d);
            Assert.AreEqual("TIMEOUT", d.commands["c1"].Status);
            Assert.AreEqual("TIMEOUT", d.LastCommand.Status);
        }

        [Test]
        public void TheLastCommandIsTheNewestQueuedWhateverItsState()
        {
            var st = new ObservedState(T0);
            st.ApplyCommand(Dev, Cmd("old", "SENT", 1));
            st.ApplyCommand(Dev, Cmd("new", "QUEUED", 5));
            st.ApplyCommand(Dev, Cmd("older", "SUCCESSFUL", 0));
            st.TryGet(Dev, out var d);
            Assert.AreEqual("new", d.LastCommand.Token);
        }

        [Test]
        public void OnlyAMeasurementFromThisRunCountsAsTheDeviceBeingObserved()
        {
            var st = new ObservedState(S(100));
            // the platform's last value from an earlier run is shown, with its age, but is not an observation of this run
            Assert.IsFalse(st.ApplyMeasurement(Dev, "fuel_pct", 70, S(60), S(101), fromSnapshot: true));
            st.TryGet(Dev, out var d);
            Assert.IsNull(d.FirstOwnObservation);
            Assert.IsTrue(st.ApplyMeasurement(Dev, "fuel_pct", 69, S(102), S(102.2), fromSnapshot: false));
            Assert.AreEqual(S(102.2), d.FirstOwnObservation);
            // only the first says so
            Assert.IsFalse(st.ApplyMeasurement(Dev, "fuel_pct", 68, S(103), S(103.2), fromSnapshot: false));
        }

        [Test]
        public void ALocationOlderThanTheOneHeldIsDropped()
        {
            var st = new ObservedState(T0);
            st.ApplyLocation(Dev, new ObservedLocation { SpeedMps = 5, OccurredAt = S(10), ObservedAt = S(10) });
            st.ApplyLocation(Dev, new ObservedLocation { SpeedMps = 1, OccurredAt = S(8), ObservedAt = S(11) });
            st.TryGet(Dev, out var d);
            Assert.AreEqual(5, d.Location.SpeedMps);
        }

        // ---- the type boundary

        [Test]
        public void NothingOutsideThePlatformAssemblyCanWriteTheObservedState()
        {
            foreach (var m in typeof(ObservedState).GetMethods(BindingFlags.Public | BindingFlags.Instance | BindingFlags.DeclaredOnly))
                Assert.IsFalse(m.Name.StartsWith("Apply") || m.Name.StartsWith("Reconcile"), "public mutator " + m.Name);
            foreach (var t in new[] { typeof(ObservedDevice), typeof(ObservedAlarm), typeof(ObservedCommand) })
            {
                // the held dictionaries are read-only views outside the assembly
                foreach (var f in t.GetFields(BindingFlags.Public | BindingFlags.Instance)) Assert.Fail(t.Name + " exposes a public field " + f.Name);
            }
        }

        [Test]
        public void TheLocalSimulationLivesInAnAssemblyThatCannotSeeThePlatformOne()
        {
            // the machine model and its samples are in the base assembly, which Platform depends on, not the other way round
            var refs = typeof(MachineModel).Assembly.GetReferencedAssemblies().Select(a => a.Name).ToList();
            CollectionAssert.DoesNotContain(refs, typeof(ObservedState).Assembly.GetName().Name);
            Assert.AreNotEqual(typeof(MachineModel).Assembly, typeof(ObservedState).Assembly);
            refs = typeof(DeviceReading).Assembly.GetReferencedAssemblies().Select(a => a.Name).ToList();
            CollectionAssert.DoesNotContain(refs, typeof(ObservedState).Assembly.GetName().Name);
        }
    }

    public sealed class StreamRecoveryTests
    {
        sealed class Harness
        {
            public readonly ConcurrentQueue<(StreamState state, string reason)> States = new ConcurrentQueue<(StreamState, string)>();
            public readonly ConcurrentQueue<int> Items = new ConcurrentQueue<int>();
            public readonly ConcurrentQueue<TimeSpan> Delays = new ConcurrentQueue<TimeSpan>();
            public int Snapshots, Refreshes, Opens;
            public readonly TimeSpan Snap = TimeSpan.FromSeconds(7.5);

            public StreamRunner<int> Runner(Func<int, IAsyncEnumerable<int>> script)
            {
                return new StreamRunner<int>(
                    ct => script(Interlocked.Increment(ref Opens)),
                    ct => { Interlocked.Increment(ref Snapshots); return Task.CompletedTask; },
                    i => Items.Enqueue(i),
                    (s, r) => States.Enqueue((s, r)),
                    ct => { Interlocked.Increment(ref Refreshes); return Task.CompletedTask; },
                    (d, ct) => { Delays.Enqueue(d); return d == Snap ? Task.Delay(d, ct) : Task.CompletedTask; },
                    snapshotWait: Snap);
            }

            public TimeSpan[] Backoffs => Delays.Where(d => d != Snap).ToArray();
            public StreamState[] Sequence => States.Select(s => s.state).ToArray();
        }

        static GraphQlRequestException Closed(int code) => new GraphQlRequestException($"subscription socket closed by server ({code}): ");

        static async IAsyncEnumerable<int> Frames(int[] items, Exception then, CancellationToken hold = default, bool block = false)
        {
            await Task.Yield();
            foreach (var i in items) yield return i;
            if (then != null) throw then;
            if (block) await Task.Delay(Timeout.Infinite, hold);
        }

        /// <summary>A stream that delivers one item and then goes down once released: a closed socket.</summary>
        static async IAsyncEnumerable<int> DownAfterRelease(ManualResetEventSlim release)
        {
            await Task.Yield();
            yield return 1;
            while (!release.IsSet) await Task.Delay(5);
            throw Closed(1006);
        }

        [Test]
        public void ARejectedTokenRefreshesAndReconnectsAtOnce()
        {
            var h = new Harness();
            var cts = new CancellationTokenSource();
            var runner = h.Runner(n => n == 1 ? Frames(new[] { 1, 2 }, Closed(4401)) : Frames(new[] { 9 }, null, cts.Token, block: true));
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (!h.Items.Contains(9) && DateTime.UtcNow < until) Thread.Sleep(5);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
            Assert.AreEqual(new[] { 1, 2, 9 }, h.Items.ToArray());
            Assert.AreEqual(1, h.Refreshes, "a rejected token is refreshed");
            Assert.IsEmpty(h.Backoffs, "and reconnected at once, with no wait");
            Assert.AreEqual(2, h.Snapshots, "a snapshot after every subscribe");
            var seq = h.Sequence;
            Assert.AreEqual(StreamState.Connecting, seq[0]);
            CollectionAssert.Contains(seq, StreamState.Live);
            var down = h.States.First(s => s.state == StreamState.Reconnecting);
            Assert.AreEqual("token expired (4401)", down.reason);
            Assert.AreEqual(StreamState.Live, seq[seq.Length - 1]);
        }

        [Test]
        public void ADropBacksOffOneTwoFourAndTheTokenIsLeftAlone()
        {
            var h = new Harness();
            var cts = new CancellationTokenSource();
            var runner = h.Runner(n => n <= 3 ? Frames(new int[0], Closed(1006)) : Frames(new[] { 7 }, null, cts.Token, block: true));
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (!h.Items.Contains(7) && DateTime.UtcNow < until) Thread.Sleep(5);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
            Assert.AreEqual(new[] { TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(4) }, h.Backoffs);
            Assert.AreEqual(0, h.Refreshes, "a dropped socket is not a rejected token");
            Assert.IsTrue(h.States.Any(s => s.state == StreamState.Reconnecting && s.reason.StartsWith("dropped")));
        }

        [Test]
        public void BackoffDoublesAndStopsAtThirtySeconds()
        {
            Assert.AreEqual(new double[] { 1, 2, 4, 8, 16, 30, 30, 30 }, Enumerable.Range(0, 8).Select(i => StreamRunner<int>.Backoff(i).TotalSeconds));
        }

        [Test]
        public void TwoRejectionsInARowBackOffInsteadOfSpinning()
        {
            var h = new Harness();
            var cts = new CancellationTokenSource();
            var runner = h.Runner(n => n <= 2 ? Frames(new int[0], Closed(4401)) : Frames(new[] { 5 }, null, cts.Token, block: true));
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (!h.Items.Contains(5) && DateTime.UtcNow < until) Thread.Sleep(5);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
            Assert.AreEqual(new[] { TimeSpan.FromSeconds(1) }, h.Backoffs, "the first rejection reconnects at once; the second waits");
            Assert.AreEqual(2, h.Refreshes);
        }

        [Test]
        public void AServerThatEndsTheSubscriptionIsAReconnectNotASilentStop()
        {
            var h = new Harness();
            var cts = new CancellationTokenSource();
            var runner = h.Runner(n => n == 1 ? Frames(new[] { 1 }, null) : Frames(new[] { 2 }, null, cts.Token, block: true));
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (!h.Items.Contains(2) && DateTime.UtcNow < until) Thread.Sleep(5);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
            Assert.IsTrue(h.States.Any(s => s.state == StreamState.Reconnecting && s.reason == "the server ended the subscription"));
        }

        [Test]
        public void AFailedSnapshotLeavesTheStreamLiveAndSaysSo()
        {
            var states = new ConcurrentQueue<(StreamState, string)>();
            var items = new ConcurrentQueue<int>();
            var cts = new CancellationTokenSource();
            var runner = new StreamRunner<int>(
                ct => Frames(new[] { 3 }, null, cts.Token, block: true),
                ct => Task.FromException(new InvalidOperationException("snapshot refused")),
                i => items.Enqueue(i),
                (s, r) => states.Enqueue((s, r)), null,
                (d, ct) => Task.CompletedTask);
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (!items.Contains(3) && DateTime.UtcNow < until) Thread.Sleep(5);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
            var live = states.First(s => s.Item1 == StreamState.Live);
            StringAssert.Contains("snapshot failed", live.Item2);
        }

        [Test]
        public void TheStaleBannerAppearsWhileAFakeStreamIsDownAndGoesWhenItIsLive()
        {
            var state = new ObservedState(DateTimeOffset.UtcNow.AddMinutes(-5));
            var status = new ObserverStatus();
            var seen = DateTimeOffset.UtcNow.AddSeconds(-20);
            ObserverApplier.Apply(state, status, new MeasurementItem("sp-hauler-06", "fuel_pct", 60, seen, seen, false), null);

            var cts = new CancellationTokenSource();
            var release = new ManualResetEventSlim();
            var opens = 0;
            var runner = new StreamRunner<int>(
                ct => Interlocked.Increment(ref opens) == 1 ? DownAfterRelease(release) : Frames(new int[0], Closed(1006)),
                null, i => { }, (st, r) => ObserverApplier.Apply(state, status, new StatusItem("measurements", st.ToString(), r, DateTimeOffset.UtcNow), null),
                null, (d, ct) => d == TimeSpan.FromSeconds(7.5) ? Task.CompletedTask : Task.Delay(20, ct), snapshotWait: TimeSpan.FromSeconds(7.5));
            // first life: live, then the stream throws (the closed socket) and the runner reports it
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (status.Measurements.State != StreamState.Live && DateTime.UtcNow < until) Thread.Sleep(5);
            Assert.AreEqual(StreamState.Live, status.Measurements.State);
            Assert.IsNull(ObserverBanner.Text(status, state));
            release.Set();
            until = DateTime.UtcNow.AddSeconds(8);
            while (status.Measurements.State != StreamState.Reconnecting && DateTime.UtcNow < until) Thread.Sleep(5);
            Assert.AreEqual(StreamState.Reconnecting, status.Measurements.State);
            var banner = ObserverBanner.Text(status, state);
            Assert.AreEqual("Observer reconnecting — values frozen at " + ObserverBanner.Clock(seen), banner);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
        }

        [Test]
        public void TheBannerNeverClaimsValuesBeforeAnyWereSeen()
        {
            var state = new ObservedState(DateTimeOffset.UtcNow);
            var status = new ObserverStatus();
            Assert.AreEqual("Observer connecting — no values observed yet", ObserverBanner.Text(status, state));
            status.Measurements.State = StreamState.Reconnecting;
            Assert.AreEqual("Observer reconnecting — no values observed yet", ObserverBanner.Text(status, state));
            status.Measurements.State = StreamState.Live;
            Assert.IsNull(ObserverBanner.Text(status, state));
            // the alarm stream is its own stream
            status.Alarms.State = StreamState.Reconnecting;
            StringAssert.Contains("Alarm stream reconnecting", ObserverBanner.Text(status, state));
        }

        [Test]
        public void AStatusReportMovesTheStreamStatusAndCountsReconnects()
        {
            var state = new ObservedState(DateTimeOffset.UtcNow);
            var status = new ObserverStatus();
            var t = DateTimeOffset.UtcNow;
            void Report(StreamState s, string r) => ObserverApplier.Apply(state, status, new StatusItem("measurements", s.ToString(), r, t), null);
            Report(StreamState.Connecting, null);
            Report(StreamState.Live, null);
            Report(StreamState.Reconnecting, "token expired (4401)");
            Report(StreamState.Reconnecting, "token expired (4401)");
            Report(StreamState.Live, null);
            Assert.AreEqual(1, status.Measurements.Reconnects);
            Assert.IsTrue(status.Measurements.IsLive);
            ObserverApplier.Apply(state, status, new StatusItem("locations", "failed", "boom", t), null);
            Assert.AreEqual("boom", status.PollErrors["locations"]);
            ObserverApplier.Apply(state, status, new StatusItem("locations", "ok", null, t), null);
            Assert.IsEmpty(status.PollErrors);
        }

        [Test]
        public void AFailureIsClassifiedByItsCloseCode()
        {
            Assert.AreEqual(StreamFailureKind.TokenRejected, StreamFailure.Classify(Closed(4401)).Kind);
            Assert.AreEqual(StreamFailureKind.Dropped, StreamFailure.Classify(Closed(4429)).Kind);
            Assert.AreEqual(StreamFailureKind.Dropped, StreamFailure.Classify(Closed(1006)).Kind);
            Assert.AreEqual(StreamFailureKind.Error, StreamFailure.Classify(new InvalidOperationException("x")).Kind);
            // a reason that carries a credential-shaped string is redacted before it can reach the screen
            var leaked = StreamFailure.Classify(new InvalidOperationException("bad " + new string('a', 40))).Reason;
            StringAssert.DoesNotContain(new string('a', 40), leaked);
        }

        [Test]
        public void StaleItemsFromAStoppedObserverAreDroppedAndCounted()
        {
            var inbox = new ObserverInbox();
            var now = DateTimeOffset.UtcNow;
            var old = new MeasurementItem("d", "fuel_pct", 1, now, now, false) { };
            old.Generation = 1;
            var current = new MeasurementItem("d", "fuel_pct", 2, now, now, false) { };
            current.Generation = 2;
            inbox.Post(old);
            inbox.Post(current);
            var applied = new List<double>();
            inbox.Drain(2, i => applied.Add(((MeasurementItem)i).Value));
            Assert.AreEqual(new[] { 2.0 }, applied);
            Assert.AreEqual(1, inbox.StaleDropped);
        }
    }
}
