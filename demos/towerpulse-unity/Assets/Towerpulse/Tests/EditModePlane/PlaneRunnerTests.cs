// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.DevicePlane;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Towerpulse.DevicePlane;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The engine-side half of the device plane, against a link that is a test double: readiness, one writer per device, the silent chiller, and a command answered over the real handler.</summary>
    public class PlaneRunnerTests
    {
        sealed class FakeLink : IDeviceLink
        {
            public event Action<LinkState> StateChanged;
            public CommandHandler Handler;
            public readonly List<Sample> Sent = new List<Sample>();
            public bool Started, Disposed;
            public bool CanPublish => Started && !Disposed;

            public Task StartAsync(CommandHandler handler, CancellationToken ct)
            {
                Handler = handler; Started = true;
                StateChanged?.Invoke(LinkState.Ready);
                return Task.CompletedTask;
            }

            public Task PublishAsync(Sample sample, CancellationToken ct)
            {
                lock (Sent) Sent.Add(sample);
                return Task.CompletedTask;
            }

            public ValueTask DisposeAsync() { Disposed = true; return default; }
        }

        sealed class Factory : IDeviceLinkFactory
        {
            public readonly Dictionary<string, FakeLink> Links = new Dictionary<string, FakeLink>();
            public IDeviceLink Create(string externalId, string deviceToken, string credentialId)
            {
                if (Links.ContainsKey(externalId)) throw new InvalidOperationException("two sessions for " + externalId);
                return Links[externalId] = new FakeLink();
            }
        }

        static (PlaneRunner runner, Factory factory, TowerPlane plane, ReadinessBoard board) Build(PlaneClock clock, int credentialedCount = 31)
        {
            DemoIdentity.Configure(TowerpulseScene.DemoName);
            var plane = new TowerPlane(1, clock);
            var devices = TowerpulseScene.Devices(plane.Devices);
            var board = new ReadinessBoard(devices, "t");
            var creds = new DeviceCredentials();
            int n = 0;
            foreach (var d in plane.Devices)
            {
                if (n++ >= credentialedCount) break;
                var r = new BindResult { ExternalId = d.ExternalId, Outcome = BindOutcome.Bound, DeviceToken = d.Token, TypeToken = d.TypeToken };
                board.SetBind(r);
                creds.Set(d.Token, d.Token + "-towerpulse");
                board.SetCredential(new CredentialOutcome { ExternalId = d.ExternalId, DeviceToken = d.Token, Ok = true, Path = CredentialPath.Created });
            }
            var factory = new Factory();
            return (new PlaneRunner(plane, board, creds, factory, 10), factory, plane, board);
        }

        [Test]
        public void ThirtyOneCredentialedDevicesGetThirtyOneUniqueSessionsAndTheBoardReadsFull()
        {
            var (runner, factory, _, board) = Build(PlaneClock.FromHour(9));
            Assert.That(board.CredentialedCount, Is.EqualTo(31));
            runner.StartAllAsync().Wait(5000);
            runner.Tick(1, DateTimeOffset.UtcNow);
            Assert.That(factory.Links.Count, Is.EqualTo(31), "one session per device, never two");
            Assert.That(runner.Sessions.Select(s => s.Device.ExternalId).Distinct().Count(), Is.EqualTo(31));
            var ready = runner.Readiness();
            Assert.That(ready.Full, Is.True, ready.ToString());
            StringAssert.StartsWith("31/31 credentialed · 31/31 connected", ready.ToString());
        }

        [Test]
        public void ADeviceWithoutACredentialGetsNoSessionAndTheBoardIsNotFull()
        {
            var (runner, factory, _, _) = Build(PlaneClock.FromHour(9), 30);
            runner.StartAllAsync().Wait(5000);
            runner.Tick(1, DateTimeOffset.UtcNow);
            Assert.That(factory.Links.Count, Is.EqualTo(30));
            Assert.That(runner.Readiness().Full, Is.False);
        }

        [Test]
        public void EveryDeviceReportsInTheScenariosKeysAndAreStampedWithTheWallClock()
        {
            var (runner, factory, _, _) = Build(PlaneClock.FromHour(9));
            runner.StartAllAsync().Wait(5000);
            var now = new DateTimeOffset(2026, 10, 28, 12, 0, 0, TimeSpan.Zero);
            runner.Tick(10, now);
            Assert.That(factory.Links.Values.All(l => l.Sent.Count == 1), Is.True);
            var hot = factory.Links["TP-VAV-T-F4-E"].Sent[0];
            Assert.That(hot.OccurredUtc, Is.EqualTo(now), "occurred-time is the wall clock");
            Assert.That(hot.Values.Keys.OrderBy(k => k), Is.EqualTo(Wire.VavMetrics.OrderBy(k => k)));
        }

        [Test]
        public void AnAcceleratedModelStillStampsEventsWithTheWallClock()
        {
            var (runner, factory, plane, _) = Build(PlaneClock.FromHour(9, 20));
            runner.StartAllAsync().Wait(5000);
            var now = DateTimeOffset.UtcNow;
            runner.Tick(60, now);
            Assert.That(plane.Model.TimeS, Is.EqualTo(plane.Clock.StartSimS + 60 * 20).Within(BuildingModel.StepS));
            Assert.That(factory.Links["TP-VAV-T-F4-E"].Sent[0].OccurredUtc, Is.EqualTo(now));
        }

        [Test]
        public void TheTrippedChillerSendsNothingButKeepsItsSession()
        {
            var (runner, factory, _, _) = Build(PlaneClock.FromHour(16.4));
            runner.StartAllAsync().Wait(5000);
            var now = DateTimeOffset.UtcNow;
            runner.Tick(10, now);
            var annex = factory.Links["TP-CH-A"];
            Assert.That(annex.Sent.Count, Is.EqualTo(1), "reporting before the trip");
            runner.Tick(20 * 60, now.AddMinutes(20));
            runner.Tick(20 * 60 + 10, now.AddMinutes(20).AddSeconds(10));
            Assert.That(annex.Sent.Count, Is.EqualTo(1), "after 16:30 it is not reporting");
            Assert.That(annex.CanPublish, Is.True, "the session is up, so a restart can be delivered");
            Assert.That(factory.Links["TP-CH-T"].Sent.Count, Is.GreaterThan(1), "the other chiller reports on");
            Assert.That(runner.Readiness().NotReporting, Is.EqualTo(1));
            Assert.That(runner.Readiness().Full, Is.True, "not reporting is not disconnected");
        }

        [Test]
        public void ARestartOverTheRealHandlerIsAnsweredOnlyWhenTheChillerIsBack()
        {
            var (runner, factory, _, _) = Build(PlaneClock.FromHour(16.45));
            runner.StartAllAsync().Wait(5000);
            double wall = 0;
            var now = DateTimeOffset.UtcNow;
            runner.Tick(wall += 5, now);
            // run past the 16:30 trip
            while (runner.Plane.Model.TimeS < TowerDay.ChillerTripS + 300) runner.Tick(wall += 5, now);
            var link = factory.Links["TP-CH-A"];
            var cmd = new DeviceCommand("cmd-1", Wire.Restart, JsonDocument.Parse("{}").RootElement, 1);
            var answer = link.Handler(cmd, CancellationToken.None);
            runner.Tick(wall += 5, now);
            Assert.That(answer.IsCompleted, Is.False, "arrival is not completion");
            int guard = 0;
            while (!answer.IsCompleted && guard++ < 2000) runner.Tick(wall += 5, now);
            Assert.That(answer.IsCompleted, Is.True);
            var outcome = answer.Result;
            Assert.That(outcome.Success, Is.True, outcome.Error);
            Assert.That(runner.Plane.Model.Chillers[1].Running, Is.True);
        }

        [Test]
        public void ABoostThatCannotYetBeDoneIsNotAnsweredAndAnInvalidOneFailsAtOnce()
        {
            var (runner, factory, plane, _) = Build(PlaneClock.FromHour(13));
            runner.StartAllAsync().Wait(5000);
            double wall = 0;
            var now = DateTimeOffset.UtcNow;
            var link = factory.Links["TP-VAV-T-F4-E"];
            var bad = link.Handler(new DeviceCommand("c0", Wire.BoostCooling, JsonDocument.Parse("{\"minutes\":500}").RootElement, 1), CancellationToken.None);
            runner.Tick(wall += 5, now);
            Assert.That(bad.IsCompleted && !bad.Result.Success, Is.True, "outside the profile's range");

            var set = link.Handler(new DeviceCommand("c1", Wire.SetSetpoint, JsonDocument.Parse("{\"celsius\":22}").RootElement, 2), CancellationToken.None);
            var boost = link.Handler(new DeviceCommand("c2", Wire.BoostCooling, JsonDocument.Parse("{\"minutes\":120}").RootElement, 3), CancellationToken.None);
            runner.Tick(wall += 5, now);
            Assert.That(set.IsCompleted && set.Result.Success, Is.True, "set-setpoint is applied at once");
            Assert.That(boost.IsCompleted, Is.False);
            var z = plane.Model.Zones[plane.Model.Layout.ZoneIndex(BuildingLayout.HotZone)];
            int guard = 0;
            while (!boost.IsCompleted && guard++ < 4000) runner.Tick(wall += 5, now);
            Assert.That(boost.Result.Success, Is.True, boost.Result.Error);
            Assert.That(z.TempC, Is.LessThanOrEqualTo(22 + BuildingModel.SetpointToleranceC));
        }

        [Test]
        public void ShutdownAnswersWaitingCommandsAndDisposesEverySession()
        {
            var (runner, factory, _, _) = Build(PlaneClock.FromHour(13));
            runner.StartAllAsync().Wait(5000);
            var link = factory.Links["TP-VAV-T-F4-E"];
            link.Handler(new DeviceCommand("c1", Wire.SetSetpoint, JsonDocument.Parse("{\"celsius\":22}").RootElement, 1), CancellationToken.None);
            runner.Tick(5, DateTimeOffset.UtcNow);
            var boost = link.Handler(new DeviceCommand("c2", Wire.BoostCooling, JsonDocument.Parse("{\"minutes\":120}").RootElement, 2), CancellationToken.None);
            runner.Tick(10, DateTimeOffset.UtcNow);
            Assert.That(boost.IsCompleted, Is.False);
            runner.ShutdownAsync().Wait(10000);
            Assert.That(boost.Wait(2000), Is.True, "silence would leave it SENT until it expired");
            Assert.That(boost.Result.Success, Is.False);
            Assert.That(factory.Links.Values.All(l => l.Disposed), Is.True);
        }
    }
}
