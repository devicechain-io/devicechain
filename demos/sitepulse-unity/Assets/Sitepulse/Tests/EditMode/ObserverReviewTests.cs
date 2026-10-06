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
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>What the observer's merge rules do when an item comes through the one place items become state.</summary>
    public sealed class ObserverApplierTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
        const string Dev = "sp-hauler-06";

        static DateTimeOffset S(double seconds) => T0.AddSeconds(seconds);

        static void Apply(ObservedState st, ObserverItem item, ObserverStatus status = null, List<string> newly = null) =>
            ObserverApplier.Apply(st, status ?? new ObserverStatus(), item, newly);

        static ObservedAlarm Alarm(string token, string state, double occurred, string severity = "CRITICAL", string key = "low-fuel") =>
            new ObservedAlarm { Token = token, AlarmKey = key, MetricKey = "fuel_pct", State = state, Severity = severity, OccurredAt = S(occurred), ObservedAt = S(occurred) };

        // ---- the header is satisfied only by this run's own telemetry

        [Test]
        public void OldDataNeverSatisfiesTheHeaderThroughTheApplier()
        {
            var st = new ObservedState(S(100));
            var newly = new List<string>();
            // the platform's last value from an earlier run, delivered as a snapshot: shown, not an observation
            Apply(st, new MeasurementItem(Dev, "fuel_pct", 70, S(60), S(101), true), newly: newly);
            Assert.IsEmpty(newly, "a value that happened before the run began is not this run's");
            // the first value at or after the floor is
            Apply(st, new MeasurementItem(Dev, "fuel_pct", 69, S(100), S(100.5), false), newly: newly);
            CollectionAssert.AreEqual(new[] { Dev }, newly);
            // only the first says so
            Apply(st, new MeasurementItem(Dev, "fuel_pct", 68, S(101), S(101.2), false), newly: newly);
            CollectionAssert.AreEqual(new[] { Dev }, newly);
        }

        // ---- alarms through the applier

        [Test]
        public void AnAlarmSnapshotClearsWhatItDoesNotListThroughTheApplier()
        {
            var st = new ObservedState(T0);
            Apply(st, new AlarmItem(Dev, Alarm("ended", "ACTIVE", 5)));
            Apply(st, new AlarmItem(Dev, Alarm("listed", "ACTIVE", 6)));
            Apply(st, new AlarmItem(Dev, Alarm("newer", "ACTIVE", 12)));
            var snapshot = new AlarmSnapshotItem(new[] { new AlarmItem(Dev, Alarm("listed", "ACTIVE", 6)) }, S(10), S(10.5));
            Apply(st, snapshot);
            st.TryGet(Dev, out var d);
            Assert.IsFalse(d.Alarms["ended"].IsActive, "an alarm the snapshot does not list ended while the observer was away");
            Assert.IsTrue(d.Alarms["listed"].IsActive);
            Assert.IsTrue(d.Alarms["newer"].IsActive, "an event newer than the request is not the snapshot's to judge");
        }

        [Test]
        public void ATruncatedAlarmSnapshotNeverClearsAnAlarmItDoesNotList()
        {
            var st = new ObservedState(T0);
            var status = new ObserverStatus();
            Apply(st, new AlarmItem(Dev, Alarm("unlisted", "ACTIVE", 5)));
            var partial = new AlarmSnapshotItem(new[] { new AlarmItem(Dev, Alarm("listed", "ACTIVE", 6)) }, S(10), S(10.5), truncated: true, totalRecords: 150);
            Apply(st, partial, status);
            st.TryGet(Dev, out var d);
            Assert.IsTrue(d.Alarms["unlisted"].IsActive, "a partial list cannot say an alarm it does not name has ended");
            Assert.IsTrue(d.Alarms["listed"].IsActive, "what it does list is still the platform's word");
            StringAssert.Contains("partial", status.AlarmSnapshotNote);
            StringAssert.Contains("150", status.AlarmSnapshotNote);
            StringAssert.Contains("alarm snapshot partial", ObserverBanner.Line(status));
            // the next complete snapshot reconciles, and the note goes
            Apply(st, new AlarmSnapshotItem(new[] { new AlarmItem(Dev, Alarm("listed", "ACTIVE", 6)) }, S(20), S(20.5)), status);
            Assert.IsFalse(d.Alarms["unlisted"].IsActive);
            Assert.IsNull(status.AlarmSnapshotNote);
        }

        static string AlarmRow(int i, string state = "ACTIVE") =>
            "{\"token\":\"a" + i + "\",\"originatorToken\":\"" + Dev + "\",\"alarmKey\":\"low-fuel\",\"metricKey\":\"fuel_pct\",\"state\":\"" + state + "\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"raisedTime\":\"2026-10-06T03:31:00Z\"}";

        static string AlarmPageJson(int first, int count, int? total) =>
            "{\"alarms\":{\"results\":[" + string.Join(",", Enumerable.Range(first, count).Select(i => AlarmRow(i))) + "]" +
            (total.HasValue ? ",\"pagination\":{\"totalRecords\":" + total.Value + "}" : "") + "}}";

        [Test]
        public async Task TheAlarmSnapshotPagesThroughTheWholeListWhenThePlatformHasMore()
        {
            var asked = new List<int>();
            var snap = await ObserverQueries.FetchActiveAlarms(p =>
            {
                asked.Add(p);
                return Task.FromResult(AlarmPageJson((p - 1) * 100, p < 3 ? 100 : 50, 250));
            }, S(1), () => S(2));
            CollectionAssert.AreEqual(new[] { 1, 2, 3 }, asked);
            Assert.AreEqual(250, snap.Alarms.Count);
            Assert.IsFalse(snap.Truncated);
            Assert.AreEqual(250, snap.TotalRecords);
        }

        [Test]
        public async Task TheAlarmSnapshotStopsAtTenPagesAndSaysItIsTruncated()
        {
            var asked = 0;
            var snap = await ObserverQueries.FetchActiveAlarms(p =>
            {
                asked++;
                return Task.FromResult(AlarmPageJson((p - 1) * 100, 100, 5000));
            }, S(1), () => S(2));
            Assert.AreEqual(ObserverQueries.MaxAlarmPages, asked);
            Assert.AreEqual(10, ObserverQueries.MaxAlarmPages);
            Assert.IsTrue(snap.Truncated);
            Assert.AreEqual(1000, snap.Alarms.Count);
        }

        [Test]
        public async Task AFullPageWithNoTotalIsTreatedAsMoreToCome()
        {
            var asked = 0;
            var snap = await ObserverQueries.FetchActiveAlarms(p =>
            {
                asked++;
                return Task.FromResult(AlarmPageJson((p - 1) * 100, p == 1 ? 100 : 7, null));
            }, S(1), () => S(2));
            Assert.AreEqual(2, asked);
            Assert.IsFalse(snap.Truncated);
            Assert.AreEqual(107, snap.Alarms.Count);
            // a platform that answers nothing further while saying it has more cannot be made to finish
            var stuck = await ObserverQueries.FetchActiveAlarms(p => Task.FromResult(p == 1 ? AlarmPageJson(0, 100, 300) : AlarmPageJson(0, 0, 300)), S(1), () => S(2));
            Assert.IsTrue(stuck.Truncated);
        }

        [Test]
        public async Task TheObserversAlarmSnapshotPostsATruncatedListThatTheApplierWillNotReconcile()
        {
            var posted = new List<ObserverItem>();
            QueryFn fake = (q, vars, ct) =>
            {
                using var doc = JsonDocument.Parse(vars);
                var page = doc.RootElement.GetProperty("c").GetProperty("pageNumber").GetInt32();
                StringAssert.Contains("pagination", q);
                return Task.FromResult(AlarmPageJson((page - 1) * 100, 100, 5000));
            };
            await PlatformObserver.SnapshotAlarms(fake, posted.Add, () => S(3), CancellationToken.None);
            var snap = (AlarmSnapshotItem)posted.Single();
            Assert.IsTrue(snap.Truncated);
            var st = new ObservedState(T0);
            Apply(st, new AlarmItem(Dev, Alarm("elsewhere", "ACTIVE", -50)));
            Apply(st, snap);
            st.TryGet(Dev, out var d);
            Assert.IsTrue(d.Alarms["elsewhere"].IsActive);
        }

        // ---- which time an alarm is stamped with

        [Test]
        public void AStreamFramesOccurredTimeBeatsItsRaisedTime()
        {
            const string frame = "{\"alarmStream\":{\"eventType\":\"ESCALATED\",\"alarmToken\":\"a1\",\"originatorToken\":\"sp-hauler-06\",\"alarmKey\":\"low-fuel\",\"metricKey\":\"fuel_pct\",\"state\":\"ACTIVE\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"raisedTime\":\"2026-10-06T03:31:00Z\",\"occurredTime\":\"2026-10-06T03:35:00Z\"}}";
            var a = ObserverQueries.ParseAlarmFrame(JsonDocument.Parse(frame).RootElement, T0);
            Assert.AreEqual(new DateTimeOffset(2026, 10, 6, 3, 35, 0, TimeSpan.Zero), a.Alarm.OccurredAt);
        }

        [Test]
        public void AClearedFrameIsNotUndoneByASnapshotRowAboutTheSameAlarm()
        {
            const string cleared = "{\"alarmStream\":{\"eventType\":\"CLEARED\",\"alarmToken\":\"a1\",\"originatorToken\":\"sp-hauler-06\",\"alarmKey\":\"low-fuel\",\"metricKey\":\"fuel_pct\",\"state\":\"CLEARED\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"raisedTime\":\"2026-10-06T03:31:00Z\",\"occurredTime\":\"2026-10-06T03:35:00Z\"}}";
            const string row = "{\"alarms\":{\"results\":[{\"token\":\"a1\",\"originatorToken\":\"sp-hauler-06\",\"alarmKey\":\"low-fuel\",\"metricKey\":\"fuel_pct\",\"state\":\"ACTIVE\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"raisedTime\":\"2026-10-06T03:31:00Z\"}]}}";
            var st = new ObservedState(T0);
            Apply(st, ObserverQueries.ParseAlarmFrame(JsonDocument.Parse(cleared).RootElement, T0));
            // the snapshot was read before the clearing and arrives after it
            Apply(st, ObserverQueries.ParseActiveAlarms(row, T0.AddSeconds(-1), T0));
            st.TryGet("sp-hauler-06", out var d);
            Assert.IsFalse(d.Alarms["a1"].IsActive);
        }

        [Test]
        public void ASnapshotRowIsStampedWithTheNewestOfItsRaisedAcknowledgedAndClearedTimes()
        {
            string Row(string extra) => "{\"alarms\":{\"results\":[{\"token\":\"a1\",\"originatorToken\":\"d\",\"alarmKey\":\"low-fuel\",\"state\":\"ACTIVE\",\"severity\":\"WARNING\",\"raisedTime\":\"2026-10-06T03:31:00Z\"" + extra + "}]}}";
            DateTimeOffset At(string extra) => ObserverQueries.ParseActiveAlarms(Row(extra), T0, T0).Alarms.Single().Alarm.OccurredAt;
            Assert.AreEqual(new DateTimeOffset(2026, 10, 6, 3, 31, 0, TimeSpan.Zero), At(""));
            Assert.AreEqual(new DateTimeOffset(2026, 10, 6, 3, 33, 0, TimeSpan.Zero), At(",\"acknowledgedTime\":\"2026-10-06T03:33:00Z\""));
            Assert.AreEqual(new DateTimeOffset(2026, 10, 6, 3, 40, 0, TimeSpan.Zero),
                At(",\"acknowledgedTime\":\"2026-10-06T03:33:00Z\",\"clearedTime\":\"2026-10-06T03:40:00Z\""));
            StringAssert.Contains("acknowledgedTime", ObserverQueries.ActiveAlarmsQuery);
            StringAssert.Contains("clearedTime", ObserverQueries.ActiveAlarmsQuery);
        }

        [Test]
        public void AnAcknowledgementTheStreamAlreadyShowedIsNotReplacedByAnOlderSnapshotRow()
        {
            var st = new ObservedState(T0);
            var ack = Alarm("a1", "ACTIVE", 10);
            ack.Acknowledged = true;
            Apply(st, new AlarmItem(Dev, ack));
            const string row = "{\"alarms\":{\"results\":[{\"token\":\"a1\",\"originatorToken\":\"sp-hauler-06\",\"alarmKey\":\"low-fuel\",\"state\":\"ACTIVE\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"raisedTime\":\"2026-10-06T03:31:00Z\"}]}}";
            Apply(st, ObserverQueries.ParseActiveAlarms(row, S(-5), S(-4)));
            st.TryGet(Dev, out var d);
            Assert.IsTrue(d.Alarms["a1"].Acknowledged);
        }

        // ---- the card names the newest alarm of a key

        [Test]
        public void TheCardShowsTheNewestAlarmsSeverityWhenTwoShareAKey()
        {
            foreach (var oldFirst in new[] { true, false })
            {
                var st = new ObservedState(T0);
                var older = Alarm(oldFirst ? "a-1" : "a-2", "ACTIVE", 1, "CRITICAL");
                var newer = Alarm(oldFirst ? "a-2" : "a-1", "ACTIVE", 9, "WARNING");
                st.ApplyAlarm(Dev, older);
                st.ApplyAlarm(Dev, newer);
                var src = new ObservedReadingSource(st, id => Dev, () => true);
                var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);
                src.Fill(new ReadingSubject("SP-HL-0006"), r, T0);
                Assert.AreEqual(1, r.Alarms.Count);
                Assert.AreEqual("WARNING", r.FirstAlarm.Severity, "the card names the most recent alarm, not the oldest");
            }
        }

        // ---- commands

        static ObservedCommand Cmd(string token, string status, double queued) =>
            new ObservedCommand { Token = token, Name = "goto-refuel", Status = status, QueuedAt = S(queued), ObservedAt = S(queued) };

        [Test]
        public void EveryFinishedCommandStateResistsALaterNonTerminalOne()
        {
            foreach (var final in new[] { "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED" })
            {
                var st = new ObservedState(T0);
                Apply(st, new CommandItem(Dev, Cmd("c1", "SENT", 1)));
                Apply(st, new CommandItem(Dev, Cmd("c1", final, 1)));
                // the poll's page is a moment behind and still says it is in flight
                foreach (var behind in new[] { "QUEUED", "HELD", "SENT", "PARKED" })
                    Apply(st, new CommandItem(Dev, Cmd("c1", behind, 1)));
                st.TryGet(Dev, out var d);
                Assert.AreEqual(final, d.LastCommand.Status, final);
                Assert.IsTrue(d.LastCommand.IsTerminal, final);
            }

            foreach (var live in new[] { "QUEUED", "HELD", "SENT", "PARKED", "BOGUS" })
                Assert.IsFalse(ObservedCommand.IsTerminalStatus(live), live);
        }

        [Test]
        public void TheTerminalCommandStatesAreDefinedOnceAndTheObserverAgreesWithTheCard()
        {
            foreach (var raw in new[] { "QUEUED", "HELD", "SENT", "PARKED", "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED", "BOGUS", "" })
                Assert.AreEqual(CommandStatus.Parse(raw).IsTerminal, ObservedCommand.IsTerminalStatus(raw), raw);
        }

        static ObservedCommand C(string token, string status) => Cmd(token, status, 1);

        static CommandItem Item(string token, string status) => new CommandItem(Dev, C(token, status));

        [Test]
        public void ACommandSeenQueuedIsAskedForByTokenOnTheNextPollAndNotOnceFinished()
        {
            var tracker = new CommandTracker();
            tracker.Observe(tracker.Named(), new[] { Item("c1", "QUEUED"), Item("c2", "SENT") });
            CollectionAssert.AreEquivalent(new[] { "c1", "c2" }, tracker.Named());
            // c1 finished (the named lookup says so); c2 is still in flight
            tracker.Observe(tracker.Named(), new[] { Item("c1", "SUCCESSFUL"), Item("c2", "SENT") });
            CollectionAssert.AreEqual(new[] { "c2" }, tracker.Named());
            // a command first seen finished is never pending
            tracker.Observe(tracker.Named(), new[] { Item("c3", "FAILED"), Item("c2", "CANCELLED") });
            Assert.IsEmpty(tracker.Named());
        }

        [Test]
        public void ATokenThePlatformStopsReturningIsDroppedAndCountedAfterThreePolls()
        {
            var tracker = new CommandTracker();
            tracker.Observe(null, new[] { Item("gone", "QUEUED"), Item("kept", "QUEUED") });
            for (var i = 1; i < CommandTracker.MissedPollsBeforeDrop; i++)
            {
                tracker.Observe(tracker.Named(), new[] { Item("kept", "QUEUED") });
                CollectionAssert.Contains(tracker.Named(), "gone");
            }

            // one more answer that lacks it, and it is given up
            tracker.Observe(tracker.Named(), new[] { Item("kept", "QUEUED") });
            CollectionAssert.AreEqual(new[] { "kept" }, tracker.Named());
            Assert.AreEqual(1, tracker.Vanished);
        }

        [Test]
        public void AnAnswerThatNamesATokenAgainStartsItsMissedCountOver()
        {
            var tracker = new CommandTracker();
            tracker.Observe(null, new[] { Item("c", "QUEUED") });
            tracker.Observe(tracker.Named(), new CommandItem[0]);
            tracker.Observe(tracker.Named(), new CommandItem[0]);
            tracker.Observe(tracker.Named(), new[] { Item("c", "SENT") });
            tracker.Observe(tracker.Named(), new CommandItem[0]);
            tracker.Observe(tracker.Named(), new CommandItem[0]);
            CollectionAssert.AreEqual(new[] { "c" }, tracker.Named());
            Assert.AreEqual(0, tracker.Vanished);
        }

        [Test]
        public void AtTheCapTheOldestPendingCommandIsGivenUpNotTheNewOne()
        {
            var tracker = new CommandTracker(3);
            tracker.Observe(null, new[] { Item("a", "QUEUED"), Item("b", "QUEUED"), Item("c", "QUEUED") });
            tracker.Observe(tracker.Named(), new[] { Item("d", "QUEUED") });
            CollectionAssert.AreEqual(new[] { "b", "c", "d" }, tracker.Named());
            Assert.AreEqual(1, tracker.Evicted);
            Assert.AreEqual(3, tracker.Count);
            Assert.AreEqual(200, CommandTracker.MaxPending);
        }

        [Test]
        public async Task TheCommandPollNamesWhatItSawInFlightAndStopsOnceItFinishes()
        {
            var tracker = new CommandTracker();
            var asked = new List<(string query, string vars)>();
            var answers = new Queue<string>(new[]
            {
                "{\"commands\":{\"results\":[{\"token\":\"c1\",\"deviceToken\":\"sp-hauler-06\",\"name\":\"goto-refuel\",\"status\":\"QUEUED\",\"queuedTime\":\"2026-10-06T03:31:00Z\"}]}}",
                "{\"commands\":{\"results\":[]},\"commandsByToken\":[{\"token\":\"c1\",\"deviceToken\":\"sp-hauler-06\",\"name\":\"goto-refuel\",\"status\":\"SUCCESSFUL\",\"queuedTime\":\"2026-10-06T03:31:00Z\"}]}",
                "{\"commands\":{\"results\":[]}}",
            });
            QueryFn fake = (q, v, ct) =>
            {
                asked.Add((q, v));
                return Task.FromResult(answers.Dequeue());
            };
            var posted = new List<ObserverItem>();
            for (var i = 0; i < 3; i++) await PlatformObserver.PollCommands(fake, tracker, posted.Add, () => T0, CancellationToken.None);
            StringAssert.DoesNotContain("commandsByToken", asked[0].query);
            StringAssert.Contains("commandsByToken", asked[1].query);
            using (var doc = JsonDocument.Parse(asked[1].vars))
                CollectionAssert.AreEqual(new[] { "c1" }, doc.RootElement.GetProperty("t").EnumerateArray().Select(e => e.GetString()));
            StringAssert.DoesNotContain("commandsByToken", asked[2].query, "a finished command is no longer asked for");
            Assert.AreEqual(2, posted.Count);
        }

        // ---- the polls keep the server from being hammered

        [Test]
        public void APollBacksOffWhileTheServerIsDownAndReturnsToItsPeriodWhenItAnswers()
        {
            var one = TimeSpan.FromSeconds(1);
            Assert.AreEqual(new double[] { 1, 2, 4, 8, 10, 10 }, Enumerable.Range(0, 6).Select(f => PollLoop.Wait(one, f).TotalSeconds));
            Assert.AreEqual(new double[] { 5, 10, 10 }, Enumerable.Range(0, 3).Select(f => PollLoop.Wait(TimeSpan.FromSeconds(5), f).TotalSeconds));
        }

        [Test]
        public void ThePollLoopWaitsLongerAfterEachFailureAndResetsOnSuccess()
        {
            var waits = new List<double>();
            var results = new List<bool>();
            var calls = 0;
            var cts = new CancellationTokenSource();
            // five failures, then three successes (the last cancels the loop)
            Func<CancellationToken, Task> body = ct =>
            {
                calls++;
                if (calls <= 5) throw new InvalidOperationException("down");
                if (calls == 8) cts.Cancel();
                return Task.CompletedTask;
            };
            PollLoop.RunAsync(TimeSpan.FromSeconds(1), body, e => results.Add(e == null), () => T0,
                (d, ct) => { waits.Add(d.TotalSeconds); return Task.CompletedTask; }, cts.Token).Wait(5000);
            CollectionAssert.AreEqual(new double[] { 2, 4, 8, 10, 10, 1, 1, 1 }, waits);
            CollectionAssert.AreEqual(new[] { false, false, false, false, false, true, true, true }, results);
        }
    }

    /// <summary>The stream runner's recovery rules, run against a clock the test owns.</summary>
    public sealed class StreamRunnerClockTests
    {
        sealed class Rig
        {
            public static readonly TimeSpan Snap = TimeSpan.FromMilliseconds(50);
            public DateTimeOffset Now = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
            public readonly ConcurrentQueue<(StreamState state, string reason)> States = new ConcurrentQueue<(StreamState, string)>();
            public readonly ConcurrentQueue<int> Items = new ConcurrentQueue<int>();
            public readonly ConcurrentQueue<TimeSpan> Delays = new ConcurrentQueue<TimeSpan>();
            public int Snapshots, Refreshes, Opens;

            public StreamRunner<int> Runner(Func<int, IAsyncEnumerable<int>> script) => new StreamRunner<int>(
                ct => script(Interlocked.Increment(ref Opens)),
                ct => { Interlocked.Increment(ref Snapshots); return Task.CompletedTask; },
                i => Items.Enqueue(i),
                (s, r) => States.Enqueue((s, r)),
                ct => { Interlocked.Increment(ref Refreshes); return Task.CompletedTask; },
                (d, ct) => { if (d == Snap) return Task.Delay(d, ct); Delays.Enqueue(d); return Task.CompletedTask; },
                () => Now, Snap);

            public TimeSpan[] Backoffs => Delays.ToArray();

            public void RunUntil(StreamRunner<int> runner, CancellationTokenSource cts, Func<bool> done)
            {
                var task = Task.Run(() => runner.RunAsync(cts.Token));
                var until = DateTime.UtcNow.AddSeconds(8);
                while (!done() && DateTime.UtcNow < until) Thread.Sleep(5);
                cts.Cancel();
                Assert.IsTrue(task.Wait(5000));
            }
        }

        static GraphQlRequestException Closed(int code) => new GraphQlRequestException($"subscription socket closed by server ({code}): ");

        /// <summary>One frame, then (after <paramref name="beforeDrop"/>) a failure.</summary>
        static async IAsyncEnumerable<int> LiveThenDrop(int item, Action beforeDrop, Exception failure)
        {
            await Task.Yield();
            yield return item;
            beforeDrop?.Invoke();
            throw failure;
        }

        static async IAsyncEnumerable<int> LiveThenHold(int item, CancellationToken hold)
        {
            await Task.Yield();
            yield return item;
            await Task.Delay(Timeout.Infinite, hold);
        }

        [Test]
        public void ARunOfConnectionsThatDropRightAfterGoingLiveKeepsBackingOff()
        {
            var rig = new Rig();
            var cts = new CancellationTokenSource();
            var runner = rig.Runner(n => n <= 3 ? LiveThenDrop(n, null, Closed(1006)) : LiveThenHold(99, cts.Token));
            rig.RunUntil(runner, cts, () => rig.Items.Contains(99));
            // each was live for no time at all: that is a storm, not a stable connection
            Assert.AreEqual(new[] { TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(4) }, rig.Backoffs);
        }

        [Test]
        public void AConnectionThatStayedLiveLongEnoughStartsTheBackoffOver()
        {
            var rig = new Rig();
            var cts = new CancellationTokenSource();
            var runner = rig.Runner(n =>
                n == 1 ? LiveThenDrop(1, null, Closed(1006))
                : n == 2 ? LiveThenDrop(2, () => rig.Now += StreamRunner<int>.StableAfter, Closed(1006))
                : LiveThenHold(99, cts.Token));
            rig.RunUntil(runner, cts, () => rig.Items.Contains(99));
            Assert.AreEqual(new[] { TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(1) }, rig.Backoffs, "the second drop came after a stable stretch, so it is the first of a new run");
        }

        [Test]
        public void ARejectedTokenAfterAStableStretchIsRefreshedAtOnceAgain()
        {
            var rig = new Rig();
            var cts = new CancellationTokenSource();
            var runner = rig.Runner(n => n <= 2
                ? LiveThenDrop(n, () => rig.Now += StreamRunner<int>.StableAfter + TimeSpan.FromSeconds(1), Closed(4401))
                : LiveThenHold(99, cts.Token));
            rig.RunUntil(runner, cts, () => rig.Items.Contains(99));
            Assert.AreEqual(2, rig.Refreshes);
            Assert.IsEmpty(rig.Backoffs, "a token that expires every fifteen minutes is routine: each time is refreshed and reconnected at once");
        }

        // ---- a stream is Live only when something has arrived on it

        static async IAsyncEnumerable<int> FirstFrameAfter(ManualResetEventSlim gate, CancellationToken stop)
        {
            await Task.Yield();
            while (!gate.IsSet) await Task.Delay(5, stop);
            yield return 1;
            await Task.Delay(Timeout.Infinite, stop);
        }

        [Test]
        public void AStreamThatHasDeliveredNothingIsSubscribedAndNeverLive()
        {
            var rig = new Rig();
            var cts = new CancellationTokenSource();
            var gate = new ManualResetEventSlim();
            var runner = rig.Runner(n => FirstFrameAfter(gate, cts.Token));
            var task = Task.Run(() => runner.RunAsync(cts.Token));
            var until = DateTime.UtcNow.AddSeconds(8);
            while (!rig.States.Any(s => s.state == StreamState.Subscribed) && DateTime.UtcNow < until) Thread.Sleep(5);
            Thread.Sleep(150);
            Assert.IsTrue(rig.States.Any(s => s.state == StreamState.Subscribed), "the snapshot ran and the subscription is open");
            Assert.IsFalse(rig.States.Any(s => s.state == StreamState.Live), "no frame has arrived, so nothing says the stream is live");
            Assert.AreEqual(1, rig.Snapshots);
            // the first frame is the evidence
            gate.Set();
            until = DateTime.UtcNow.AddSeconds(8);
            while (!rig.Items.Contains(1) && DateTime.UtcNow < until) Thread.Sleep(5);
            cts.Cancel();
            Assert.IsTrue(task.Wait(5000));
            CollectionAssert.AreEqual(new[] { StreamState.Connecting, StreamState.Subscribed, StreamState.Live }, rig.States.Select(s => s.state).ToArray());
        }

        [Test]
        public void ASubscribedStreamIsShownAsSuchAndIsNotLive()
        {
            var state = new ObservedState(T0());
            var status = new ObserverStatus();
            void Report(StreamState s, string source = "measurements") => ObserverApplier.Apply(state, status, new StatusItem(source, s.ToString(), null, T0()), null);
            Report(StreamState.Connecting);
            Report(StreamState.Subscribed);
            Assert.AreEqual(StreamState.Subscribed, status.Measurements.State);
            Assert.IsFalse(status.Measurements.IsLive, "a card is judged against a stream that is not live");
            StringAssert.Contains("subscribed · no data yet", ObserverBanner.Line(status));
            StringAssert.DoesNotContain("measurements live", ObserverBanner.Line(status));
            StringAssert.Contains("subscribed", ObserverBanner.Text(status, state));
            StringAssert.DoesNotContain("live", ObserverBanner.Text(status, state));
            Report(StreamState.Live);
            Assert.IsNull(ObserverBanner.Text(status, state));
            // an alarm stream with nothing to say is normal and is not a banner
            Report(StreamState.Subscribed, "alarms");
            Assert.IsNull(ObserverBanner.Text(status, state));
            StringAssert.Contains("alarms subscribed · no data yet", ObserverBanner.Line(status));
            // dropping from subscribed counts as a reconnect
            Report(StreamState.Reconnecting);
            Assert.AreEqual(1, status.Measurements.Reconnects);
        }

        static DateTimeOffset T0() => new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
    }

    public sealed class CardPresenterTests
    {
        static readonly DateTimeOffset Now = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);

        static RowView Row(string value, double ageSeconds, bool live = true, bool warn = false) =>
            CardPresenter.Row(true, value, Now.AddSeconds(-ageSeconds), Now, live, warn);

        [Test]
        public void AValueOlderThanAMinuteReadsAsADashNotANumber()
        {
            var r = Row("73 %", 61);
            Assert.IsTrue(r.Shown);
            Assert.AreEqual("—", r.Text);
            Assert.AreEqual(RowTone.Grey, r.Tone);
            Assert.AreEqual("73 %", Row("73 %", 59).Text, "up to a minute it is shown, greyed");
            Assert.AreEqual(RowTone.Grey, Row("73 %", 59).Tone);
        }

        [Test]
        public void AValueBetweenThreeAndFifteenSecondsOldIsDimmedAndOlderIsGrey()
        {
            Assert.AreEqual(RowTone.Ink, Row("73 %", 2).Tone);
            Assert.AreEqual(RowTone.Warn, Row("73 %", 2, warn: true).Tone);
            Assert.AreEqual(RowTone.Dim, Row("73 %", 4).Tone);
            Assert.AreEqual(RowTone.Dim, Row("73 %", 15).Tone);
            Assert.AreEqual(RowTone.Grey, Row("73 %", 16).Tone);
            Assert.AreEqual("73 %", Row("73 %", 10).Text);
        }

        [Test]
        public void WhileTheStreamIsNotLiveNoValueIsFreshOrMerelyStale()
        {
            Assert.AreEqual(RowTone.Grey, Row("73 %", 1, live: false).Tone);
            Assert.AreEqual(RowTone.Grey, Row("73 %", 5, live: false).Tone);
            Assert.AreEqual("73 %", Row("73 %", 1, live: false).Text);
        }

        [Test]
        public void AnObservedValueWithNothingBehindItIsADashAndNeverZero()
        {
            var absent = CardPresenter.Row(true, null, null, Now, true);
            Assert.IsTrue(absent.Shown);
            Assert.AreEqual("—", absent.Text);
            Assert.AreEqual(RowTone.Grey, absent.Tone);
            // a value with no time to judge it by is not shown as fresh either
            Assert.AreEqual("—", CardPresenter.Row(true, "73 %", null, Now, true).Text);
            // an illustrative card has no row for what it has no value for
            Assert.IsFalse(CardPresenter.Row(false, null, null, Now, true).Shown);
            Assert.AreEqual(RowTone.Ink, CardPresenter.Row(false, "73 %", null, Now, true).Tone);
        }

        [Test]
        public void ALocationIsJudgedAgainstItsOwnCadence()
        {
            Assert.AreEqual(RowTone.Dim, CardPresenter.Row(true, "5 km/h", Now.AddSeconds(-5), Now, true).Tone);
            Assert.AreEqual(RowTone.Ink, CardPresenter.Row(true, "5 km/h", Now.AddSeconds(-5), Now, true, false, FreshnessRule.LocationFreshWithin).Tone);
        }

        static StatusView Status(double? age, bool live = true) =>
            CardPresenter.Status(Provenance.Observed, age.HasValue ? Now.AddSeconds(-age.Value) : (DateTimeOffset?)null, Now, live, false, false);

        [Test]
        public void TheDotAndTagFollowTheFreshnessOfTheDevicesOwnTelemetry()
        {
            Assert.AreEqual(new StatusView(DotTone.Ok, "observed"), Status(1));
            var stale = Status(5);
            Assert.AreEqual(DotTone.Warn, stale.Dot);
            Assert.AreEqual("stale 5 s", stale.Tag);
            var old = Status(30);
            Assert.AreEqual(DotTone.Grey, old.Dot);
            Assert.AreEqual("no data 30 s", old.Tag);
            Assert.AreEqual("no data > 1 min", Status(90).Tag);
            Assert.AreEqual("no data", Status(null).Tag);
            Assert.AreEqual(DotTone.Grey, Status(null).Dot);
        }

        [Test]
        public void WhileTheStreamIsNotLiveACardIsGreyAndNeverTaggedObserved()
        {
            var s = Status(1, live: false);
            Assert.AreEqual(DotTone.Grey, s.Dot);
            Assert.AreNotEqual("observed", s.Tag);
            Assert.AreEqual("no data 1 s", s.Tag);
        }

        [Test]
        public void AnIllustrativeCardSaysSoWhateverItsAge()
        {
            Assert.AreEqual(new StatusView(DotTone.Ok, "illustrative"), CardPresenter.Status(Provenance.Illustrative, null, Now, true, false, false));
            Assert.AreEqual(DotTone.Warn, CardPresenter.Status(Provenance.Illustrative, null, Now, true, true, false).Dot);
            Assert.AreEqual(DotTone.Muted, CardPresenter.Status(Provenance.Illustrative, null, Now, true, false, true).Dot);
        }

        [Test]
        public void ACommandInFlightAgesByTheLastTimeTheCommandPollSawItAndAFinishedOneDoesNot()
        {
            RowView Cmd(bool final, double seenAgo, bool live = true) => CardPresenter.CommandRow(true, "SENT", final, Now.AddSeconds(-seenAgo), Now, live);
            Assert.AreEqual(RowTone.Ink, Cmd(false, 2).Tone);
            Assert.AreEqual(RowTone.Dim, Cmd(false, 6).Tone, "the poll has not seen it for a few seconds");
            Assert.AreEqual(RowTone.Grey, Cmd(false, 20).Tone);
            Assert.AreEqual("SENT", Cmd(false, 20).Text);
            Assert.AreEqual("—", Cmd(false, 70).Text, "no word from the poll for over a minute: not shown as a state");
            Assert.AreEqual(RowTone.Grey, Cmd(false, 1, live: false).Tone);
            // a finished command is a fact
            Assert.AreEqual(RowTone.Ink, Cmd(true, 3600).Tone);
            Assert.AreEqual("SENT", Cmd(true, 3600).Text);
            // an illustrative card's command is not judged
            Assert.AreEqual(RowTone.Ink, CardPresenter.CommandRow(false, "SENT", false, null, Now, true).Tone);
        }
    }

    public sealed class ObservedSourceBoundaryTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 2, 11, TimeSpan.Zero);

        [Test]
        public void WhatASourceIsAskedToFillCarriesADeviceIdAndNothingOfTheChoreography()
        {
            var members = typeof(ReadingSubject).GetMembers(BindingFlags.Public | BindingFlags.Instance | BindingFlags.DeclaredOnly)
                .Where(m => m is PropertyInfo || m is FieldInfo || m is MethodInfo mi && !mi.IsSpecialName)
                .Select(m => m.Name).Where(n => n != "get_DeviceId").ToArray();
            CollectionAssert.AreEquivalent(new[] { "DeviceId" }, members);
            foreach (var c in typeof(ReadingSubject).GetConstructors())
                CollectionAssert.AreEqual(new[] { typeof(string) }, c.GetParameters().Select(p => p.ParameterType).ToArray());
        }

        [Test]
        public void AModelSpeedNeverReachesAnObservedCardThatHasNoLocation()
        {
            const float Sentinel = 12.345f;
            var go = new GameObject("SP-HL-0006");
            try
            {
                var rig = go.AddComponent<MachineRig>();
                // the choreography has this machine doing 12.345 m/s; the platform has said nothing about its position
                var illustrative = new IllustrativeReadingSource(null, "SP-HL-0001", AlarmKeys.LowFuel);
                illustrative.SetModel("SP-HL-0006", rig, Sentinel);
                var st = new ObservedState(T0);
                // the device is known to the platform's state (it has reported a measurement) but has no location
                st.ApplyMeasurement("sp-hauler-06", "fuel_pct", 50, T0, T0, false);
                var observed = new ObservedReadingSource(st, id => "sp-hauler-06", () => true);
                var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);
                observed.Fill(new ReadingSubject("SP-HL-0006"), r, T0);
                Assert.AreEqual("50 %", r.Format("fuel_pct"), "the card was filled from the state");
                Assert.IsNull(r.SpeedKmh);
                var kmh = Sentinel * 3.6;
                foreach (var key in MeasurementKeys.Equipment)
                    if (r.TryGet(key, out var v)) Assert.AreNotEqual(Sentinel, v, key);
                Assert.IsFalse(r.SpeedKmh.HasValue && Math.Abs(r.SpeedKmh.Value - kmh) < 1e-6);
            }
            finally
            {
                UnityEngine.Object.DestroyImmediate(go);
            }
        }

        [Test]
        public void TheIllustrativeSourceRefusesADeviceItWasNotGivenAModelFor()
        {
            var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment);
            Assert.Throws<InvalidOperationException>(() => new IllustrativeReadingSource(null, "SP-HL-0006", AlarmKeys.LowFuel).Fill(new ReadingSubject("SP-HL-0006"), r, T0));
        }
    }

    public sealed class ReadinessDecayTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
        static readonly string[] Ids = { "SP-HL-0001", "SP-HL-0002", "SP-HL-0003" };

        static ReadinessBoard Board(bool observer = true)
        {
            var board = new ReadinessBoard(Ids.Select(i => new SceneDevice(i, SceneKind.Hauler)).ToList(), "t");
            foreach (var i in Ids)
            {
                board.SetBind(new BindResult { ExternalId = i, Outcome = BindOutcome.Bound, DeviceToken = "tok-" + i });
                board.SetStage(i, DeviceStage.Publishing);
                board[i].LastPublishUtc = DateTimeOffset.UtcNow;
            }

            board.BeginSessions();
            foreach (var i in Ids) board.SetStage(i, DeviceStage.Publishing);
            if (!observer) return board;
            board.BeginObserver();
            foreach (var i in Ids) board.MarkObserved("tok-" + i);
            return board;
        }

        static ObservedState Seen(params (string id, double at)[] when)
        {
            var st = new ObservedState(T0.AddSeconds(-100));
            foreach (var (id, at) in when) st.ApplyMeasurement("tok-" + id, "fuel_pct", 50, T0.AddSeconds(at), T0.AddSeconds(at), false);
            return st;
        }

        [Test]
        public void ADeviceCountsAsObservedOnlyWhileItsNewestOwnMeasurementIsFifteenSecondsOldOrLess()
        {
            Assert.AreEqual(FreshnessRule.StaleWithin, ReadinessBoard.ObservedWithin, "the header goes quiet when the card goes grey");
            var board = Board();
            var st = Seen((Ids[0], 0), (Ids[1], 0), (Ids[2], 0));
            board.EvaluateObserved(T0.AddSeconds(15), st.NewestOwnRunAt);
            Assert.AreEqual("3/3 observed · 0 failed", board.Summary(), "exactly fifteen seconds is still observed");
            board.EvaluateObserved(T0.AddSeconds(15.1), st.NewestOwnRunAt);
            Assert.AreEqual(0, board.ObservedCount);
            Assert.AreEqual(3, board.QuietCount);
            Assert.AreEqual("0/3 observed · 3 quiet · 0 failed", board.Summary());
            // one device reports again and is counted again
            st.ApplyMeasurement("tok-" + Ids[1], "fuel_pct", 49, T0.AddSeconds(20), T0.AddSeconds(20), false);
            board.EvaluateObserved(T0.AddSeconds(21), st.NewestOwnRunAt);
            Assert.AreEqual("1/3 observed · 2 quiet · 0 failed", board.Summary());
            StringAssert.Contains("quiet", board.Lines().First(l => l.Text.StartsWith(Ids[0])).Text);
            Assert.AreEqual(LineKind.Pending, board.Lines().First(l => l.Text.StartsWith(Ids[0])).Kind);
        }

        [Test]
        public void ADeviceWithNoMeasurementFromThisRunIsNotCountedEvenThoughItWasMarkedObserved()
        {
            var board = Board();
            var st = new ObservedState(T0);
            // an old value, from before the run, is all the platform holds
            st.ApplyMeasurement("tok-" + Ids[0], "fuel_pct", 50, T0.AddSeconds(-60), T0, true);
            board.EvaluateObserved(T0.AddSeconds(1), st.NewestOwnRunAt);
            Assert.AreEqual(0, board.ObservedCount);
        }

        [Test]
        public void ObservedStateKeepsTheNewestMeasurementOfThisRunPerDevice()
        {
            var st = new ObservedState(T0);
            Assert.IsNull(st.NewestOwnRunAt("tok-x"));
            st.ApplyMeasurement("tok-x", "fuel_pct", 1, T0.AddSeconds(-5), T0, true);
            Assert.IsNull(st.NewestOwnRunAt("tok-x"), "before the floor is not this run's");
            st.ApplyMeasurement("tok-x", "fuel_pct", 1, T0.AddSeconds(3), T0, false);
            st.ApplyMeasurement("tok-x", "payload_t", 1, T0.AddSeconds(2), T0, false);
            Assert.AreEqual(T0.AddSeconds(3), st.NewestOwnRunAt("tok-x"));
        }

        [Test]
        public void ADeviceThatIsNotSendingIsNotCountedAsObservedHoweverFreshItsLastValue()
        {
            foreach (var side in new[] { DeviceSide.Reconnecting, DeviceSide.Blind, DeviceSide.Stopped })
            {
                var board = Board();
                var st = Seen((Ids[0], 0), (Ids[1], 0), (Ids[2], 0));
                board.EvaluateObserved(T0.AddSeconds(1), st.NewestOwnRunAt);
                Assert.AreEqual(3, board.ObservedCount);
                board.SetSide(Ids[1], side);
                Assert.AreEqual(2, board.ObservedCount, side.ToString());
                Assert.AreEqual("2/3 observed · 0 failed", board.Summary());
            }
        }

        [Test]
        public void AFailedOrStalledDeviceIsNotCountedAsObservedEither()
        {
            var board = Board();
            var st = Seen((Ids[0], 0), (Ids[1], 0), (Ids[2], 0));
            board.EvaluateObserved(T0.AddSeconds(1), st.NewestOwnRunAt);
            board.FailSession(Ids[0], "blind");
            Assert.AreEqual(2, board.ObservedCount);
            board.Evaluate(DateTimeOffset.UtcNow.AddSeconds(30));
            Assert.AreEqual(0, board.ObservedCount);
        }

        // ---- the compact panel's one line

        [Test]
        public void TheBriefSaysAllObservedWhenEveryDeviceIsAndOtherwiseNamesTheOnesThatAreNot()
        {
            var board = Board();
            var st = Seen((Ids[0], 0), (Ids[1], 0), (Ids[2], 0));
            board.EvaluateObserved(T0.AddSeconds(1), st.NewestOwnRunAt);
            Assert.AreEqual("all observed", board.Brief());
            board.Evaluate(DateTimeOffset.UtcNow.AddSeconds(30));
            board.SetSide(Ids[2], DeviceSide.Reconnecting);
            Assert.AreEqual("SP-HL-0001 stalled · SP-HL-0002 stalled · SP-HL-0003 reconnecting", board.Brief());
            Assert.AreEqual("SP-HL-0001 stalled · SP-HL-0002 stalled · +1 more", board.Brief(2));
        }

        [Test]
        public void TheBriefNamesAFailedAQuietAndANotYetObservedDevice()
        {
            var board = Board();
            board.FailSession(Ids[0], "blind");
            var st = Seen((Ids[1], 0));
            board.EvaluateObserved(T0.AddSeconds(30), st.NewestOwnRunAt);
            Assert.AreEqual("SP-HL-0001 failed · SP-HL-0002 quiet · SP-HL-0003 quiet", board.Brief());
            var fresh = Board(observer: false);
            Assert.AreEqual("all publishing", fresh.Brief());
            fresh.SetStage(Ids[1], DeviceStage.Connecting);
            Assert.AreEqual("SP-HL-0002 connecting", fresh.Brief());
        }

        [Test]
        public void TheBriefBeforeSessionsBeginReadsAgainstCredentials()
        {
            var board = new ReadinessBoard(Ids.Select(i => new SceneDevice(i, SceneKind.Hauler)).ToList(), "t");
            Assert.AreEqual("SP-HL-0001 resolving · SP-HL-0002 resolving · SP-HL-0003 resolving", board.Brief());
            foreach (var i in Ids)
            {
                board.SetBind(new BindResult { ExternalId = i, Outcome = BindOutcome.Bound, DeviceToken = "tok-" + i });
                board.SetStage(i, DeviceStage.Credentialed);
            }

            Assert.AreEqual("all credentialed", board.Brief());
        }

        [Test]
        public void TheCompactPanelHasTheHeaderTheTokenTheObserverAndTheBriefAndNotEveryDevice()
        {
            var board = Board();
            board.SetSide(Ids[2], DeviceSide.Reconnecting);
            var lines = board.Lines();
            var compact = HudText.Readiness(board.Summary(), "operator token fresh", "observer · measurements live", board.Brief(), lines, board.Notes(), false);
            StringAssert.Contains(board.Summary(), compact);
            StringAssert.Contains("operator token fresh", compact);
            StringAssert.Contains("observer · measurements live", compact);
            StringAssert.Contains("SP-HL-0003 reconnecting", compact);
            StringAssert.Contains(HudText.CompactHint, compact);
            foreach (var l in lines) StringAssert.DoesNotContain(l.Text, compact);
            Assert.LessOrEqual(compact.Split('\n').Length, 5, "five lines: header, token, observer, brief, hint");

            var expanded = HudText.Readiness(board.Summary(), "operator token fresh", "observer · measurements live", board.Brief(), lines, board.Notes(), true);
            foreach (var l in lines) StringAssert.Contains(HudText.Esc(l.Text), expanded);
            StringAssert.Contains(HudText.ExpandedHint, expanded);
            StringAssert.DoesNotContain(HudText.CompactHint, expanded);
        }

        [Test]
        public void ReasonTextWithAngleBracketsCannotBecomeRichTextTags()
        {
            var text = HudText.Readiness("s", "t", "o <b>x</b>", "SP-HL-0001 failed", new PanelLine[0], new string[0], false);
            StringAssert.DoesNotContain("<b>x</b>", text);
        }

        [Test]
        public void TheHudsRectsAreInTheCardLayoutsUnitsWithTheOriginAtTheBottomLeft()
        {
            var badge = SitepulseHud.FromTopLeft(18f, 18f, new Vector2(300f, 36f));
            Assert.AreEqual(new Rect(18f, 1080f - 18f - 36f, 300f, 36f), badge);
            var banner = SitepulseHud.FromTopCentre(1920f, 18f, new Vector2(400f, 36f));
            Assert.AreEqual(new Rect(760f, 1026f, 400f, 36f), banner);
            // a panel hung under the badge reaches down the frame: its top edge is the bottom of its box in these units
            var panel = SitepulseHud.FromTopLeft(18f, 18f + 36f + 8f, new Vector2(640f, 120f));
            Assert.AreEqual(1080f - 62f - 120f, panel.yMin, 1e-4f);
            Assert.AreEqual(1080f - 62f, panel.yMax, 1e-4f);
            Assert.IsFalse(panel.Overlaps(badge));
        }

        [Test]
        public void TheOverlayTreatsEverythingTheHudHoldsAsSpaceACardMayNotTake()
        {
            var blockers = new List<Rect> { new Rect(500f, 500f, 10f, 10f) };
            var hud = new Rect(18f, 900f, 640f, 120f);
            IotOverlay.AddObstacles((w, into) =>
            {
                Assert.AreEqual(1920f, w);
                into.Add(hud);
            }, 1920f, new List<Rect>(), blockers);
            Assert.AreEqual(2, blockers.Count);
            var added = blockers[1];
            Assert.IsTrue(added.xMin <= hud.xMin && added.yMin <= hud.yMin && added.xMax >= hud.xMax && added.yMax >= hud.yMax, "the blocker covers the HUD rect");
            // a card that would sit just under the panel's edge is kept off it
            Assert.IsTrue(added.Overlaps(new Rect(100f, hud.yMin - 3f, 252f, 100f)));
            IotOverlay.AddObstacles(null, 1920f, new List<Rect>(), blockers);
            Assert.AreEqual(2, blockers.Count);
        }
    }
}
