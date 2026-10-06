// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
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
            public string Refusal;
            public Action<string> OnCommand;
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

            public async Task StartAsync(string refusalReason, Action<string> onCommand, CancellationToken cancellationToken)
            {
                Refusal = refusalReason;
                OnCommand = onCommand;
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
        public void EveryCommandIsRefusedHonestlyAndRecordedOnTheDevicesLine()
        {
            var (board, creds) = Credentialed(Fleet);
            var factory = new FakeFactory();
            var fleet = StartedFleet(board, creds, factory);
            try
            {
                Assert.AreEqual("this build of the demo does not execute commands yet", factory.Links["SP-HL-0001"].Refusal);
                Assert.AreEqual("the crusher accepts no commands", factory.Links["SP-PL-0001"].Refusal);

                factory.Links["SP-HL-0001"].OnCommand("goto-refuel");
                fleet.Pump();
                StringAssert.Contains("refused goto-refuel: this build of the demo does not execute commands yet", board.Lines()[0].Text);
                factory.Links["SP-PL-0001"].OnCommand("goto-area");
                fleet.Pump();
                StringAssert.Contains("refused goto-area: the crusher accepts no commands", board.Lines().Last().Text);
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
    }
}
