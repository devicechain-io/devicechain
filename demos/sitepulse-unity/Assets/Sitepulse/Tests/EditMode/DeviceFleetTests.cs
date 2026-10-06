// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using System.Text.Json;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;
using static DeviceChain.Sitepulse.Tests.PlatformTestData;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class DeviceFleetTests
    {
        // ---- doubles

        sealed class FakeLink : IDeviceLink
        {
            public readonly string Id;
            readonly Counter counter;
            readonly object gate = new object();
            public bool Up;
            public Exception PublishThrows;
            public bool CanPublishThrows, BlindBeforeStartThrows;
            public int Disposed, Starts;
            public CommandHandler Handler;
            public TimeSpan StartTakes = TimeSpan.FromMilliseconds(40);
            public Exception StartThrows;
            public readonly List<Sample> Published = new List<Sample>();

            public FakeLink(string id, Counter counter)
            {
                Id = id;
                this.counter = counter;
            }

            public event Action<LinkState> StateChanged;

            public bool CanPublish => CanPublishThrows ? throw new InvalidOperationException("link state unreadable") : Up;

            public int PublishedCount
            {
                get { lock (gate) return Published.Count; }
            }

            public void Raise(LinkState s) => StateChanged?.Invoke(s);

            public async Task StartAsync(CommandHandler handler, CancellationToken cancellationToken)
            {
                Handler = handler;
                Interlocked.Increment(ref Starts);
                var now = Interlocked.Increment(ref counter.InFlight);
                int max;
                while (now > (max = Volatile.Read(ref counter.Max))) Interlocked.CompareExchange(ref counter.Max, now, max);
                try
                {
                    await Task.Delay(StartTakes, cancellationToken);
                    if (StartThrows != null)
                    {
                        if (BlindBeforeStartThrows) Raise(LinkState.Blind);
                        throw StartThrows;
                    }

                    Up = true;
                    Raise(LinkState.Ready);
                }
                finally
                {
                    Interlocked.Decrement(ref counter.InFlight);
                }
            }

            public Task PublishAsync(Sample sample, CancellationToken cancellationToken)
            {
                var fail = PublishThrows;
                if (fail != null) return Task.FromException(fail);
                lock (gate) Published.Add(sample);
                return Task.CompletedTask;
            }

            public ValueTask DisposeAsync()
            {
                Up = false;
                Interlocked.Increment(ref Disposed);
                return default;
            }
        }

        sealed class Counter
        {
            public int InFlight, Max;
        }

        sealed class FakeFactory : IDeviceLinkFactory
        {
            public readonly Counter Counter = new Counter();
            public readonly Dictionary<string, FakeLink> Links = new Dictionary<string, FakeLink>();
            public readonly List<string> Created = new List<string>();
            public Action<FakeLink> Customise = _ => { };
            public string CreateThrowsFor;

            public IDeviceLink Create(string externalId, string deviceToken, string credentialId)
            {
                if (externalId == CreateThrowsFor) throw new InvalidOperationException("no session for you");
                Created.Add(deviceToken);
                var l = new FakeLink(externalId, Counter);
                Customise(l);
                Links[externalId] = l;
                return l;
            }
        }

        sealed class StillPoses : IPoseSource
        {
            public bool TryGet(string externalId, out MachinePose pose)
            {
                pose = new MachinePose(10, 20, 5, 45, false);
                return true;
            }
        }

        static readonly SceneDevice[] Fleet =
            Enumerable.Range(1, 6).Select(i => new SceneDevice($"SP-HL-{i:0000}", SceneKind.Hauler))
                .Concat(Enumerable.Range(1, 3).Select(i => new SceneDevice($"SP-LD-{i:0000}", SceneKind.Loader)))
                .Concat(new[] { new SceneDevice("SP-PL-0001", SceneKind.Plant) })
                .ToArray();

        // a board over the real binder and a fake platform, so devices come out credentialed exactly as in a run
        static (ReadinessBoard board, DeviceCredentials creds) Credentialed(IReadOnlyList<SceneDevice> scene, params string[] missing)
        {
            var devs = new List<Dev>();
            var rows = new List<string>();
            foreach (var d in scene)
            {
                if (missing.Contains(d.ExternalId)) continue;
                var n = d.ExternalId.Substring(d.ExternalId.Length - 2);
                var dev = d.Kind == SceneKind.Hauler ? Hauler(n) : d.Kind == SceneKind.Loader ? Loader(n) : Plant();
                devs.Add(dev);
                rows.Add(Cred(dev.Token + "-cred", dev.Token));
            }

            QueryFn query = (q, vars, ct) => Task.FromResult(q == DeviceBinder.ResolveQuery ? Devices(devs.ToArray()) : CredBatch(rows.ToArray()));
            var board = new ReadinessBoard(scene, "sim-sitepulse");
            var creds = new DeviceCredentials();
            new DeviceBinder(query, Contract()).BindAsync(scene, board, creds, CancellationToken.None).GetAwaiter().GetResult();
            return (board, creds);
        }

        static bool WaitFor(Func<bool> cond, double seconds = 8)
        {
            var end = DateTime.UtcNow.AddSeconds(seconds);
            while (DateTime.UtcNow < end)
            {
                if (cond()) return true;
                Thread.Sleep(10);
            }

            return cond();
        }

        // advance once so every device queues its first samples, then pump until n devices are publishing
        static void ReachPublishing(DeviceFleet fleet, ReadinessBoard board, int n)
        {
            fleet.Advance(DateTimeOffset.UtcNow, new StillPoses());
            Assert.IsTrue(WaitFor(() =>
            {
                fleet.Pump();
                return board.PublishingCount == n;
            }), $"{n} devices publishing, saw {board.Summary()}");
        }

        static DeviceFleet StartedFleet(ReadinessBoard board, DeviceCredentials creds, FakeFactory factory)
        {
            var fleet = new DeviceFleet(board, creds, factory);
            fleet.StartAll().GetAwaiter().GetResult();
            fleet.Pump();
            return fleet;
        }

        // ---- start order and the ladder

        [Test]
        public void AtMostFourSessionsAreStartingAtOnce()
        {
            var (board, creds) = Credentialed(Fleet);
            Assert.AreEqual(10, board.CredentialedCount);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                Assert.AreEqual(4, factory.Counter.Max, "four in flight, never five, and the limit is actually reached");
                Assert.AreEqual(10, factory.Links.Values.Count(l => l.Starts == 1));
                Assert.AreEqual(10, fleet.Hosts.Count);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void EachDeviceGetsOneSessionAndNoTwoShareAClientId()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                Assert.AreEqual(10, factory.Created.Count);
                Assert.AreEqual(10, factory.Created.Distinct().Count(), "one link per device token");
                Assert.Throws<InvalidOperationException>(() => fleet.StartAll(), "a plane starts once");
                Assert.AreEqual(10, factory.Created.Count);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void TheLadderClimbsFromCredentialedToPublishingAndTheHeaderCountsExactly()
        {
            var (board, creds) = Credentialed(Fleet);
            Assert.AreEqual("10/10 credentialed · 0 failed", board.Summary());
            var factory = new FakeFactory();
            var fleet = new DeviceFleet(board, creds, factory);
            try
            {
                var starting = fleet.StartAll();
                Assert.AreEqual("0/10 publishing · 0 failed", board.Summary());
                Assert.AreEqual(DeviceStage.Connecting, board["SP-HL-0001"].Stage);
                starting.GetAwaiter().GetResult();
                fleet.Pump();
                Assert.AreEqual(DeviceStage.Ready, board["SP-HL-0001"].Stage, "StartAsync returned: ready, not yet publishing");
                Assert.AreEqual("0/10 publishing · 0 failed", board.Summary());

                ReachPublishing(fleet, board, 10);
                Assert.AreEqual("10/10 publishing · 0 failed", board.Summary());
                Assert.AreEqual(DeviceStage.Publishing, board["SP-PL-0001"].Stage);
                Assert.Greater(board["SP-HL-0001"].Published, 0);
                Assert.IsNotNull(board["SP-HL-0001"].LastPublishUtc);
                StringAssert.Contains("sent", board.Lines()[0].Text);
                Assert.AreEqual(LineKind.Ok, board.Lines()[0].Kind);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void ADeviceWithNoBindingOrNoCredentialGetsNoSessionAndNeverPublishes()
        {
            var (board, creds) = Credentialed(Fleet, missing: "SP-LD-0002");
            Assert.AreEqual("9/10 credentialed · 1 failed", board.Summary());
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                Assert.IsFalse(factory.Links.ContainsKey("SP-LD-0002"));
                Assert.AreEqual(9, fleet.Hosts.Count);
                ReachPublishing(fleet, board, 9);
                Assert.AreEqual("9/10 publishing · 1 failed", board.Summary(), "9 of 10, never rounded up");
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- side states

        [Test]
        public void ABlindDeviceIsFailedStopsPublishingAndIsReleased()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                ReachPublishing(fleet, board, 10);
                Assert.AreEqual("10/10 publishing · 0 failed", board.Summary());

                var victim = factory.Links["SP-HL-0004"];
                victim.Raise(LinkState.Blind);
                Assert.IsFalse(fleet["SP-HL-0004"].Accepting, "halted on the SDK thread, before the main thread has heard");
                fleet.Pump();
                Assert.AreEqual(DeviceSide.Blind, board["SP-HL-0004"].Side);
                Assert.IsTrue(board["SP-HL-0004"].Failed);
                Assert.AreEqual("9/10 publishing · 1 failed", board.Summary());
                Assert.IsFalse(fleet["SP-HL-0004"].Accepting);

                var before = victim.PublishedCount;
                for (var i = 0; i < 40; i++) fleet.Advance(DateTimeOffset.UtcNow + TimeSpan.FromSeconds(i), new StillPoses());
                Thread.Sleep(300);
                Assert.AreEqual(before, victim.PublishedCount, "a blind device publishes nothing more");
                Assert.IsTrue(WaitFor(() => victim.Disposed == 1), "its session is released");
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void AReconnectingDeviceIsNeitherPublishingNorFailedAndComesBack()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                ReachPublishing(fleet, board, 10);

                factory.Links["SP-LD-0001"].Up = false;
                factory.Links["SP-LD-0001"].Raise(LinkState.Reconnecting);
                fleet.Pump();
                Assert.AreEqual("9/10 publishing · 0 failed", board.Summary());
                StringAssert.Contains("reconnecting", board.Lines().First(l => l.Text.StartsWith("SP-LD-0001")).Text);

                factory.Links["SP-LD-0001"].Up = true;
                factory.Links["SP-LD-0001"].Raise(LinkState.Ready);
                fleet.Pump();
                Assert.AreEqual("10/10 publishing · 0 failed", board.Summary());
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void ASessionThatCannotStartIsFailedAndReleased()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory { Customise = l => { if (l.Id == "SP-HL-0002") l.StartThrows = new InvalidOperationException("broker said no"); } };
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                Assert.IsTrue(board["SP-HL-0002"].Failed);
                StringAssert.Contains("could not start", board["SP-HL-0002"].FailReason);
                StringAssert.Contains("broker said no", board["SP-HL-0002"].FailReason);
                Assert.AreEqual(1, board.FailedCount);
                Assert.IsTrue(WaitFor(() => factory.Links["SP-HL-0002"].Disposed == 1));
                fleet.Advance(DateTimeOffset.UtcNow, new StillPoses());
                Thread.Sleep(200);
                Assert.AreEqual(0, factory.Links["SP-HL-0002"].PublishedCount);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- commands

        [Test]
        public void ACommandWithNoTaskLayerIsAnsweredFailedAndSaysSo()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                var task = factory.Links["SP-HL-0001"].Handler(new DeviceCommand("c-1", "goto-refuel", null, 1), CancellationToken.None);
                Assert.IsTrue(WaitFor(() => { fleet.Pump(); return task.IsCompleted; }), "a command is never left unanswered");
                var outcome = task.GetAwaiter().GetResult();
                Assert.IsFalse(outcome.Success);
                Assert.AreEqual("this device has no task executor in this run", outcome.Error);
                StringAssert.Contains("received goto-refuel", board.Lines()[0].Text);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void TheCrusherAcceptsNoCommandsAndAnInvalidCommandIsRefusedBeforeItReachesTheMainThread()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                var plant = factory.Links["SP-PL-0001"].Handler(new DeviceCommand("c-1", "goto-area", null, 1), CancellationToken.None).GetAwaiter().GetResult();
                Assert.AreEqual("the crusher accepts no commands", plant.Error);
                var unknown = factory.Links["SP-HL-0001"].Handler(new DeviceCommand("c-2", "self-destruct", null, 2), CancellationToken.None).GetAwaiter().GetResult();
                Assert.IsFalse(unknown.Success);
                StringAssert.Contains("unknown command", unknown.Error);
                fleet.Pump();
                StringAssert.Contains("refused self-destruct: unknown command", board.Lines()[0].Text);
                Assert.AreEqual(0, fleet.Inbox.Pending, "a refused command posts nothing for the task layer");
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- generations and teardown

        [Test]
        public void AnEventFromAnotherGenerationIsDroppedAndCounted()
        {
            var inbox = new DeviceInbox();
            var applied = new List<string>();
            inbox.Post(new DeviceEvent(1, "a", DeviceEventKind.Started));
            inbox.Post(new DeviceEvent(2, "b", DeviceEventKind.Started));
            inbox.Post(new DeviceEvent(1, "c", DeviceEventKind.Command, text: "x"));
            Assert.AreEqual(2, inbox.Drain(1, e => applied.Add(e.ExternalId)));
            CollectionAssert.AreEqual(new[] { "a", "c" }, applied);
            Assert.AreEqual(1, inbox.StaleDropped);
            Assert.AreEqual(0, inbox.Pending);
        }

        [Test]
        public void ShutdownBumpsTheGenerationDisposesEverySessionOnceAndSilencesLateCallbacks()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            var gen = fleet.Generation;

            Assert.IsTrue(fleet.Shutdown(TimeSpan.FromSeconds(10)));
            Assert.AreEqual(gen + 1, fleet.Generation);
            Assert.IsTrue(fleet.IsShutDown);
            foreach (var l in factory.Links.Values) Assert.AreEqual(1, l.Disposed, l.Id);

            // a callback after teardown reaches the inbox stamped with the old generation and is never applied
            var summary = board.Summary();
            factory.Links["SP-HL-0001"].Raise(LinkState.Blind);
            fleet.Pump();
            Assert.AreEqual(summary, board.Summary());
            Assert.IsFalse(board["SP-HL-0001"].Failed);
            Assert.AreEqual(1, fleet.Inbox.Pending, "the late callback is queued");
            Assert.AreEqual(0, fleet.Inbox.Drain(fleet.Generation, _ => Assert.Fail("a torn-down run's event was applied")));
            Assert.AreEqual(1, fleet.Inbox.StaleDropped);

            Assert.IsTrue(fleet.Shutdown(TimeSpan.FromSeconds(10)), "idempotent");
            foreach (var l in factory.Links.Values) Assert.AreEqual(1, l.Disposed, l.Id + " is not disposed twice");
        }

        [Test]
        public void ShutdownDuringTheStartNeverStartsAQueuedSession()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory { Customise = l => l.StartTakes = TimeSpan.FromMilliseconds(300) };
            var fleet = new DeviceFleet(board, creds, factory);
            var starting = fleet.StartAll();
            Thread.Sleep(100);
            Assert.IsTrue(fleet.Shutdown(TimeSpan.FromSeconds(15)));
            starting.GetAwaiter().GetResult();
            Assert.Less(factory.Links.Values.Count(l => l.Starts > 0), 10, "the sessions still queued behind the limit were never started");
            Assert.AreEqual(0, factory.Counter.InFlight);
            foreach (var l in factory.Links.Values) Assert.AreEqual(1, l.Disposed);
        }

        // ---- speed over a frame hitch

        sealed class DrivenPoses : IPoseSource
        {
            public double East;
            public bool TryGet(string externalId, out MachinePose pose)
            {
                pose = new MachinePose(East, 0, 5, 90, false);
                return true;
            }
        }

        [Test]
        public void AFrameHitchKeepsTheSpeedAndNeverReadsAsAStop()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                var poses = new DrivenPoses();
                var wall = DateTimeOffset.UtcNow;
                var motion = fleet["SP-HL-0001"].Simulation.Motion;
                const double frame = 1.0 / 60.0, hitchGame = 0.3, hitchWall = 1.0, speed = 5.0;

                // every 20th frame the scene advances 0.3 s while the wall clock moves a full second
                var slowest = double.MaxValue;
                for (var i = 0; i < 600; i++)
                {
                    var hitch = i % 20 == 19;
                    var game = hitch ? hitchGame : frame;
                    poses.East += speed * game;
                    wall += TimeSpan.FromSeconds(hitch ? hitchWall : frame);
                    fleet.Advance(wall, poses, game);
                    if (i >= 120)
                    {
                        slowest = Math.Min(slowest, motion.SpeedMps);
                        Assert.IsTrue(motion.IsMoving, $"frame {i}: a machine at constant speed is never stopped");
                    }
                }

                Assert.GreaterOrEqual(slowest, 0.5 * speed, "speed over the time the scene moved, not the wall clock");
                Assert.AreEqual(speed, motion.SpeedMps, 0.5);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- stalled, not publishing

        [Test]
        public void ADeviceWithNoAckForFiveSecondsIsStalledNotPublishingAndNotFailed()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                ReachPublishing(fleet, board, 10);
                var acked = board["SP-HL-0001"].LastPublishUtc.Value;

                fleet.Pump(acked + TimeSpan.FromSeconds(4.9));
                Assert.IsFalse(board["SP-HL-0001"].Stalled, "inside the window");
                fleet.Pump(acked + TimeSpan.FromSeconds(20));
                Assert.IsTrue(board["SP-HL-0001"].Stalled);
                Assert.IsFalse(board["SP-HL-0001"].Failed);
                Assert.AreEqual(0, board.PublishingCount);
                Assert.AreEqual(10, board.StalledCount);
                Assert.AreEqual("0/10 publishing · 10 stalled · 0 failed", board.Summary());
                StringAssert.Contains("stalled · no acknowledgement for", board.Lines()[0].Text);
                Assert.AreEqual(LineKind.Pending, board.Lines()[0].Kind);

                // a fresh ack brings it back
                fleet.Advance(DateTimeOffset.UtcNow + TimeSpan.FromSeconds(30), new StillPoses());
                Assert.IsTrue(WaitFor(() =>
                {
                    fleet.Pump(DateTimeOffset.UtcNow);
                    return board.PublishingCount == 10;
                }), board.Summary());
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void ThreeSendFailuresInARowStallADeviceThatAckedASecondAgo()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                ReachPublishing(fleet, board, 10);
                var link = factory.Links["SP-HL-0001"];
                link.PublishThrows = new TimeoutException("broker did not answer");
                var t = DateTimeOffset.UtcNow;
                for (var i = 1; i <= 6; i++) fleet.Advance(t + TimeSpan.FromSeconds(i), new StillPoses());

                Assert.IsTrue(WaitFor(() => fleet["SP-HL-0001"].ConsecutiveSendFailures >= 3, 15));
                fleet.Pump(DateTimeOffset.UtcNow);
                Assert.IsTrue(board["SP-HL-0001"].Stalled);
                StringAssert.Contains("sends failing", board["SP-HL-0001"].StallReason);
                Assert.IsFalse(board["SP-HL-0001"].Failed);
                Assert.AreEqual(9, board.PublishingCount);

                link.PublishThrows = null;
                Assert.IsTrue(WaitFor(() =>
                {
                    fleet.Pump(DateTimeOffset.UtcNow);
                    return board.PublishingCount == 10;
                }, 15), board.Summary());
                Assert.AreEqual(0, fleet["SP-HL-0001"].ConsecutiveSendFailures, "an ack resets the run");
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void ASendLoopThatFaultsFailsTheDeviceAndIsReleased()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                ReachPublishing(fleet, board, 10);
                UnityEngine.TestTools.LogAssert.Expect(UnityEngine.LogType.Error, new System.Text.RegularExpressions.Regex("SP-HL-0003: send loop faulted"));
                factory.Links["SP-HL-0003"].CanPublishThrows = true;
                fleet.Advance(DateTimeOffset.UtcNow + TimeSpan.FromSeconds(5), new StillPoses());
                Assert.IsTrue(WaitFor(() =>
                {
                    fleet.Pump();
                    return board["SP-HL-0003"].Failed;
                }), board.Summary());
                StringAssert.Contains("send loop stopped", board["SP-HL-0003"].FailReason);
                Assert.IsTrue(board["SP-HL-0003"].IsGrey);
                Assert.IsTrue(WaitFor(() => factory.Links["SP-HL-0003"].Disposed == 1));
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- the first publish can be heard before the start returns

        [Test]
        public void AFirstPublishHeardBeforeTheStartedEventKeepsTheDevicePublishing()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = new DeviceFleet(board, creds, factory);
            try
            {
                fleet.StartAll().GetAwaiter().GetResult();
                fleet.Inbox.Drain(fleet.Generation, _ => { });      // drop the Started events the starts posted
                fleet.Inbox.Post(new DeviceEvent(fleet.Generation, "SP-HL-0001", DeviceEventKind.FirstPublish));
                fleet.Inbox.Post(new DeviceEvent(fleet.Generation, "SP-HL-0001", DeviceEventKind.Started, LinkState.Ready));
                fleet.Inbox.Post(new DeviceEvent(fleet.Generation, "SP-HL-0001", DeviceEventKind.LinkState, LinkState.Ready));
                fleet.Pump();
                Assert.AreEqual(DeviceStage.Publishing, board["SP-HL-0001"].Stage);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- blind stays blind

        [Test]
        public void AStartThatFailsBecauseTheBrokerRefusedKeepsSayingBlind()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory
            {
                Customise = l =>
                {
                    if (l.Id != "SP-HL-0002") return;
                    l.StartThrows = new InvalidOperationException("subscription refused");
                    l.BlindBeforeStartThrows = true;
                },
            };
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                Assert.IsTrue(WaitFor(() =>
                {
                    fleet.Pump();
                    return board["SP-HL-0002"].Failed;
                }));
                fleet.Pump();
                Assert.AreEqual(DeviceSide.Blind, board["SP-HL-0002"].Side);
                StringAssert.Contains("blind", board["SP-HL-0002"].FailReason);
                StringAssert.DoesNotContain("could not start", board["SP-HL-0002"].FailReason);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- who is grey

        static void AssertNobodyGrey(ReadinessBoard board, HashSet<string> greyed, string why)
            => Assert.IsEmpty(board.TakeNewlyGrey(greyed), why);

        [Test]
        public void ADeviceWithNoBindingOrNoCredentialIsGreyOnceAndOnlyOnce()
        {
            var (board, creds) = Credentialed(Fleet, missing: "SP-LD-0002");
            var greyed = new HashSet<string>();
            var first = board.TakeNewlyGrey(greyed);
            CollectionAssert.AreEqual(new[] { "SP-LD-0002" }, first.Select(d => d.ExternalId));
            AssertNobodyGrey(board, greyed, "applied once, never again");
        }

        [Test]
        public void ADeviceWhoseSessionWasNeverMadeIsGrey()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory { CreateThrowsFor = "SP-HL-0005" };
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                var grey = board.TakeNewlyGrey(new HashSet<string>());
                CollectionAssert.AreEqual(new[] { "SP-HL-0005" }, grey.Select(d => d.ExternalId));
                StringAssert.Contains("could not be created", board["SP-HL-0005"].FailReason);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void ADeviceWithNoCredentialForItsSessionIsGrey()
        {
            var (board, _) = Credentialed(Fleet);
            var fleet = new DeviceFleet(board, new DeviceCredentials(), new FakeFactory());
            try
            {
                fleet.StartAll().GetAwaiter().GetResult();
                Assert.AreEqual(10, board.TakeNewlyGrey(new HashSet<string>()).Count);
                StringAssert.Contains("no credential for the session", board["SP-HL-0001"].FailReason);
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void ADeviceThatCouldNotStartAndABlindDeviceAreGrey()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory { Customise = l => { if (l.Id == "SP-HL-0002") l.StartThrows = new InvalidOperationException("broker said no"); } };
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                var greyed = new HashSet<string>();
                CollectionAssert.AreEqual(new[] { "SP-HL-0002" }, board.TakeNewlyGrey(greyed).Select(d => d.ExternalId));
                ReachPublishing(fleet, board, 9);
                factory.Links["SP-HL-0004"].Raise(LinkState.Blind);
                fleet.Pump();
                CollectionAssert.AreEqual(new[] { "SP-HL-0004" }, board.TakeNewlyGrey(greyed).Select(d => d.ExternalId), "blind is grey and only newly so");
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        [Test]
        public void AReconnectingConnectingOrStalledDeviceIsNotGrey()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = new DeviceFleet(board, creds, factory);
            try
            {
                var starting = fleet.StartAll();
                AssertNobodyGrey(board, new HashSet<string>(), "connecting");
                starting.GetAwaiter().GetResult();
                fleet.Pump();
                ReachPublishing(fleet, board, 10);

                factory.Links["SP-LD-0001"].Up = false;
                factory.Links["SP-LD-0001"].Raise(LinkState.Reconnecting);
                fleet.Pump();
                AssertNobodyGrey(board, new HashSet<string>(), "reconnecting");

                fleet.Pump(board["SP-HL-0001"].LastPublishUtc.Value + TimeSpan.FromSeconds(30));
                Assert.Greater(board.StalledCount, 0);
                AssertNobodyGrey(board, new HashSet<string>(), "stalled has a session");
            }
            finally { fleet.Shutdown(TimeSpan.FromSeconds(10)); }
        }

        // ---- the board

        [Test]
        public void TheBoardCountsPublishingFailedAndReconnectingExactly()
        {
            var board = new ReadinessBoard(Fleet, "t");
            Assert.AreEqual("0/10 credentialed · 0 failed", board.Summary());
            board.BeginSessions();
            Assert.AreEqual("0/10 publishing · 0 failed", board.Summary());
            board.SetStage("SP-HL-0001", DeviceStage.Publishing);
            board.SetStage("SP-HL-0002", DeviceStage.Publishing);
            board.SetStage("SP-HL-0003", DeviceStage.Ready);
            Assert.AreEqual("2/10 publishing · 0 failed", board.Summary());
            board.SetSide("SP-HL-0002", DeviceSide.Reconnecting);
            Assert.AreEqual("1/10 publishing · 0 failed", board.Summary());
            board.SetSide("SP-HL-0002", DeviceSide.None);
            board.FailSession("SP-HL-0001", "blind");
            Assert.AreEqual("1/10 publishing · 1 failed", board.Summary());
            Assert.AreEqual(1, board.PublishingCount);
        }

        [Test]
        public void TheBoardRedrawsOnlyWhenACounterMoves()
        {
            var board = new ReadinessBoard(Fleet, "t");
            board.SetStats("SP-HL-0001", 5, 0, 0, null);
            var v = board.Version;
            board.SetStats("SP-HL-0001", 5, 0, 0, null);
            Assert.AreEqual(v, board.Version);
            board.SetStats("SP-HL-0001", 6, 0, 0, null);
            Assert.AreEqual(v + 1, board.Version);
        }

        // ---- a command, end to end: SDK thread -> inbox -> task layer -> answer

        static JsonElement JsonEl(string text)
        {
            using var doc = JsonDocument.Parse(text);
            return doc.RootElement.Clone();
        }

        sealed class CommandedFleet : IDisposable
        {
            public DeviceFleet Fleet;
            public TaskDirector Director;
            public Timeline Timeline = new Timeline();
            public FakeFactory Factory = new FakeFactory();
            public ReadinessBoard Board;
            public Dictionary<string, FakeBody> Bodies = new Dictionary<string, FakeBody>();

            public void Dispose() => Fleet.Shutdown(TimeSpan.FromSeconds(10));
        }

        static CommandedFleet Commanded()
        {
            var c = new CommandedFleet();
            var (board, creds) = Credentialed(Fleet);
            c.Board = board;
            c.Fleet = new DeviceFleet(board, creds, c.Factory, sceneHasZone: z => CommandKit.Site.HasZone(z));
            c.Fleet.StartAll().GetAwaiter().GetResult();
            c.Fleet.Pump();
            var track = CommandKit.Track(0);
            var machines = new List<(IMachineBody, MachineModel)>();
            var i = 0;
            foreach (var h in c.Fleet.Hosts)
            {
                if (h.Simulation.Model.IsPlant) continue;
                // the machine the tests send places stands on the haul loop; the rest are parked far off, out of every lane
                var at = h.ExternalId == "SP-HL-0003" ? track[300] : new TrackPoint(4000 + 100 * i++, 4000, 0, 0);
                var body = new FakeBody(h.ExternalId, h.Simulation.Model.Kind, at.X, at.Z, at.HeadingDegrees, track);
                c.Bodies[h.ExternalId] = body;
                machines.Add((body, h.Simulation.Model));
            }

            c.Director = new TaskDirector(CommandKit.Site, CommandKit.Graph, c.Timeline, machines, c.Fleet.Generation);
            c.Fleet.Tasks = c.Director;
            return c;
        }

        // pumps the plane and steps the task layer until the task is done (the handler runs on a pool thread)
        static bool Drive(CommandedFleet c, Task t, int maxIterations = 30000)
        {
            for (var i = 0; i < maxIterations && !t.IsCompleted; i++)
            {
                c.Fleet.Pump();
                c.Director.Step(0.5, 0.5);
                Thread.Sleep(1);
            }

            return t.IsCompleted;
        }

        static bool WaitRunning(CommandedFleet c, string id, string token)
        {
            return WaitFor(() =>
            {
                c.Fleet.Pump();
                return c.Director[id].Running != null && c.Director[id].Running.Token == token;
            });
        }

        [Test]
        public void ACommandFlowsFromTheSessionThroughTheTaskLayerAndBackAsItsOutcome()
        {
            using var c = Commanded();
            var handler = c.Factory.Links["SP-HL-0003"].Handler;
            var t = Task.Run(() => handler(new DeviceCommand("c-1", "goto-area", JsonEl("{\"areaToken\":\"sp-zone-yard\"}"), 1), CancellationToken.None));
            Assert.IsTrue(Drive(c, t), "the command ends with an answer");
            var outcome = t.Result;
            Assert.IsTrue(outcome.Success, outcome.Error);
            Assert.IsTrue(CommandKit.Site.TryZone("sp-zone-yard", out var yard));
            Assert.IsTrue(yard.Contains(c.Bodies["SP-HL-0003"].X, c.Bodies["SP-HL-0003"].Z), "and the machine is where it was sent");
            Assert.IsFalse(c.Bodies["SP-HL-0003"].Attached);
            StringAssert.Contains("received goto-area", c.Board.Lines().First(l => l.Text.Contains("SP-HL-0003")).Text);
            Assert.AreEqual(TimelineKinds.Received, c.Timeline.Rows("SP-HL-0003")[0].Kind);
            Assert.AreEqual(0, c.Timeline.Rows("SP-HL-0004").Count, "no other machine was told anything");
        }

        [Test]
        public void ANewerCommandSupersedesTheOlderOneEndToEnd()
        {
            using var c = Commanded();
            var handler = c.Factory.Links["SP-HL-0003"].Handler;
            var older = Task.Run(() => handler(new DeviceCommand("c-old", "goto-refuel", null, 1), CancellationToken.None));
            Assert.IsTrue(WaitRunning(c, "SP-HL-0003", "c-old"));
            var newer = Task.Run(() => handler(new DeviceCommand("c-new", "goto-area", JsonEl("{\"areaToken\":\"sp-zone-fill\"}"), 2), CancellationToken.None));
            Assert.IsTrue(Drive(c, newer));
            Assert.IsTrue(older.IsCompleted);
            Assert.IsFalse(older.Result.Success);
            Assert.AreEqual("superseded by c-new", older.Result.Error);
            Assert.IsTrue(newer.Result.Success, newer.Result.Error);
        }

        [Test]
        public void TheOlderCommandArrivingSecondFailsAtOnceAndTheNewerOneRuns()
        {
            using var c = Commanded();
            var handler = c.Factory.Links["SP-HL-0003"].Handler;
            var newer = Task.Run(() => handler(new DeviceCommand("c-new", "goto-area", JsonEl("{\"areaToken\":\"sp-zone-yard\"}"), 9), CancellationToken.None));
            Assert.IsTrue(WaitRunning(c, "SP-HL-0003", "c-new"));
            var older = Task.Run(() => handler(new DeviceCommand("c-old", "goto-area", JsonEl("{\"areaToken\":\"sp-zone-fill\"}"), 3), CancellationToken.None));
            Assert.IsTrue(WaitFor(() => { c.Fleet.Pump(); return older.IsCompleted; }), "the older one is answered without waiting for the newer");
            Assert.AreEqual("superseded by c-new", older.Result.Error);
            Assert.IsFalse(newer.IsCompleted);
            Assert.IsTrue(Drive(c, newer));
            Assert.IsTrue(newer.Result.Success);
        }

        [Test]
        public void AResetAnswersARunningCommandBeforeTheSessionsAreClosed()
        {
            using var c = Commanded();
            var handler = c.Factory.Links["SP-HL-0005"].Handler;
            var t = Task.Run(() => handler(new DeviceCommand("c-1", "goto-refuel", null, 1), CancellationToken.None));
            Assert.IsTrue(WaitRunning(c, "SP-HL-0005", "c-1"));
            var answered = c.Director.FailAll();
            Assert.AreEqual(1, answered, "the running command was answered by the task layer's reset");
            var unanswered = c.Fleet.QuiesceCommands(TimeSpan.FromSeconds(2), TimeSpan.Zero, answered);
            Assert.AreEqual(0, unanswered, "every handler has returned");
            Assert.IsTrue(t.IsCompleted);
            Assert.AreEqual("simulation reset before completion", t.Result.Error);
        }

        [Test]
        public void AShutdownAnswersACommandThatWasPostedAndNeverDrained()
        {
            using var c = Commanded();
            var handler = c.Factory.Links["SP-HL-0005"].Handler;
            var t = Task.Run(() => handler(new DeviceCommand("c-1", "goto-refuel", null, 1), CancellationToken.None));
            Assert.IsTrue(WaitFor(() => c.Fleet.Inbox.Pending == 1), "the handler posted its request; nothing pumps the plane again");
            var answered = c.Director.FailAll();
            Assert.AreEqual(0, answered, "the task layer never saw it");
            var unanswered = c.Fleet.QuiesceCommands(TimeSpan.FromSeconds(2), TimeSpan.Zero, answered);
            Assert.AreEqual(0, unanswered);
            Assert.IsTrue(t.Wait(2000), "the handler returned");
            Assert.AreEqual("simulation reset before completion", t.Result.Error);
        }

        [Test]
        public void TheShutdownGraceIsGivenWhenAnythingWasAnsweredEvenIfEveryHandlerHasAlreadyReturned()
        {
            using var c = Commanded();
            var handler = c.Factory.Links["SP-HL-0005"].Handler;
            var t = Task.Run(() => handler(new DeviceCommand("c-1", "goto-refuel", null, 1), CancellationToken.None));
            Assert.IsTrue(WaitRunning(c, "SP-HL-0005", "c-1"));
            var answered = c.Director.FailAll();
            Assert.AreEqual(1, answered);
            Assert.IsTrue(t.Wait(5000), "the handler returned on its own, before the shutdown looked");
            Assert.AreEqual(0, c.Fleet.Hosts.Sum(h => h.CommandsInFlight));

            var sw = System.Diagnostics.Stopwatch.StartNew();
            Assert.AreEqual(0, c.Fleet.QuiesceCommands(TimeSpan.FromSeconds(2), TimeSpan.FromMilliseconds(400), answered));
            Assert.GreaterOrEqual(sw.ElapsedMilliseconds, 350, "its answer still has to be published before the session closes");

            sw.Restart();
            Assert.AreEqual(0, c.Fleet.QuiesceCommands(TimeSpan.FromSeconds(2), TimeSpan.FromMilliseconds(400), 0));
            Assert.Less(sw.ElapsedMilliseconds, 300, "with nothing answered and nothing in flight there is nothing to wait for");
        }
    }
}
