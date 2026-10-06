// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // Slice A5: the command handler (SDK thread), its validation, the hand-off to the main thread, and the presenter's fresh run.
    public sealed class CommandHandlerTests
    {
        static readonly string[] Areas = { "sp-zone-cut", "sp-zone-fill", "sp-zone-yard" };

        static JsonElement Json(string text)
        {
            using var doc = JsonDocument.Parse(text);
            return doc.RootElement.Clone();
        }

        static CommandCheck Check(string name, string payload, bool plant = false, string[] areas = null, Func<string, bool> scene = null) =>
            CommandValidator.Validate(name, payload == null ? (JsonElement?)null : Json(payload), plant, areas ?? Areas, scene ?? (z => Areas.Contains(z)));

        // ---- validation

        [Test]
        public void AKnownCommandWithTheRightPayloadIsValid()
        {
            var area = Check("goto-area", "{\"areaToken\":\"sp-zone-yard\"}");
            Assert.IsTrue(area.Ok);
            Assert.AreEqual("sp-zone-yard", area.Area);
            Assert.IsTrue(Check("goto-refuel", null).Ok);
            Assert.IsTrue(Check("goto-refuel", "null").Ok);
            Assert.IsTrue(Check("goto-refuel", "{}").Ok);
        }

        [Test]
        public void AnUnknownCommandKeyIsRefusedWithItsName()
        {
            var c = Check("self-destruct", null);
            Assert.IsFalse(c.Ok);
            Assert.AreEqual("unknown command \"self-destruct\"", c.Reason);
            Assert.IsFalse(Check("", null).Ok);
        }

        [TestCase(null)]
        [TestCase("null")]
        [TestCase("\"sp-zone-yard\"")]
        [TestCase("[\"sp-zone-yard\"]")]
        [TestCase("{}")]
        [TestCase("{\"areaToken\":7}")]
        [TestCase("{\"areaToken\":null}")]
        [TestCase("{\"areaToken\":\"\"}")]
        [TestCase("{\"areaToken\":\"   \"}")]
        [TestCase("{\"areaToken\":\"sp-zone-yard\",\"speed\":9}")]
        [TestCase("{\"area\":\"sp-zone-yard\"}")]
        public void AMalformedGotoAreaPayloadIsRefused(string payload)
        {
            var c = Check("goto-area", payload);
            Assert.IsFalse(c.Ok, payload);
            Assert.IsNotEmpty(c.Reason);
        }

        [Test]
        public void AnAreaOutsideTheProfileOrTheSceneIsRefusedForTheRightReason()
        {
            var notListed = Check("goto-area", "{\"areaToken\":\"sp-zone-moon\"}");
            Assert.AreEqual("areaToken \"sp-zone-moon\" is not one of this device's areas", notListed.Reason);

            // in the profile's enum, but the scene has nowhere to send a machine
            var noGeometry = Check("goto-area", "{\"areaToken\":\"sp-zone-yard\"}", areas: Areas, scene: z => z != "sp-zone-yard");
            Assert.AreEqual("no scene geometry for area \"sp-zone-yard\"", noGeometry.Reason);

            // in the scene, but not in the profile's enum
            var notInEnum = Check("goto-area", "{\"areaToken\":\"sp-zone-yard\"}", areas: new[] { "sp-zone-cut" }, scene: z => true);
            Assert.AreEqual("areaToken \"sp-zone-yard\" is not one of this device's areas", notInEnum.Reason);

            var noEnum = Check("goto-area", "{\"areaToken\":\"sp-zone-yard\"}", areas: new string[0]);
            Assert.IsFalse(noEnum.Ok, "a profile that lists no areas sends a machine nowhere");
        }

        [Test]
        public void GotoRefuelTakesNoParameters()
        {
            Assert.IsFalse(Check("goto-refuel", "{\"station\":\"x\"}").Ok);
            Assert.IsFalse(Check("goto-refuel", "\"now\"").Ok);
            Assert.IsFalse(Check("goto-refuel", "[]").Ok);
        }

        [Test]
        public void TheCrusherRefusesEveryCommandWhateverItsPayload()
        {
            foreach (var name in new[] { "goto-area", "goto-refuel", "self-destruct" })
            {
                var c = Check(name, "{\"areaToken\":\"sp-zone-yard\"}", plant: true);
                Assert.IsFalse(c.Ok);
                Assert.AreEqual("the crusher accepts no commands", c.Reason);
            }
        }

        [Test]
        public void WhatAReasonEchoesFromACommandIsClippedAndHasNoControlCharacters()
        {
            var c = Check("goto-area", "{\"areaToken\":\"" + new string('x', 500) + "\\n\\u0007end\"}");
            Assert.IsFalse(c.Ok);
            Assert.Less(c.Reason.Length, 140);
            Assert.IsFalse(c.Reason.Any(char.IsControl));
        }

        // ---- the handler, on a thread of its own

        sealed class NoLink : IDeviceLink
        {
            public event Action<LinkState> StateChanged { add { } remove { } }
            public bool CanPublish => false;
            public Task StartAsync(CommandHandler handler, CancellationToken cancellationToken) => Task.CompletedTask;
            public Task PublishAsync(Sample sample, CancellationToken cancellationToken) => Task.CompletedTask;
            public ValueTask DisposeAsync() => default;
        }

        static DeviceSessionHost Host(string id, SceneKind kind, DeviceInbox inbox, int generation = 1) =>
            new DeviceSessionHost(new SceneDevice(id, kind), "tok-" + id, new NoLink(), generation, inbox, new CommandContext(Areas, z => Areas.Contains(z)));

        static DeviceCommand Cmd(string token, string name, string payload, long seq) => new DeviceCommand(token, name, payload == null ? (JsonElement?)null : Json(payload), seq);

        static List<DeviceEvent> Drain(DeviceInbox inbox, int generation = 1)
        {
            var list = new List<DeviceEvent>();
            inbox.Drain(generation, list.Add);
            return list;
        }

        [Test]
        public void AnInvalidCommandIsAnsweredFailedWithoutTroublingTheMainThread()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox);
            var outcome = host.HandleAsync(Cmd("c-1", "goto-area", "{\"areaToken\":\"sp-zone-moon\"}", 1), CancellationToken.None).GetAwaiter().GetResult();
            Assert.IsFalse(outcome.Success);
            StringAssert.Contains("not one of this device's areas", outcome.Error);
            var events = Drain(inbox);
            Assert.AreEqual(1, events.Count);
            Assert.AreEqual(DeviceEventKind.CommandRefused, events[0].Kind);
            Assert.IsNull(events[0].Task, "nothing for the task layer");
            Assert.AreEqual(0, host.CommandsInFlight);
        }

        [Test]
        public void AValidCommandIsPostedForTheMainThreadAndTheHandlerWaitsForItsAnswerWithoutBlocking()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox);
            var pending = Task.Run(() => host.HandleAsync(Cmd("c-1", "goto-area", "{\"areaToken\":\"sp-zone-yard\"}", 7), CancellationToken.None));
            DeviceEvent posted = null;
            var end = DateTime.UtcNow.AddSeconds(5);
            while (posted == null && DateTime.UtcNow < end)
            {
                inbox.Drain(1, e => posted = e);
                Thread.Sleep(5);
            }

            Assert.IsNotNull(posted, "the handler posted the request");
            Assert.AreEqual(DeviceEventKind.Task, posted.Kind);
            Assert.AreEqual("c-1", posted.Task.Token);
            Assert.AreEqual("goto-area", posted.Task.Key);
            Assert.AreEqual("sp-zone-yard", posted.Task.Area);
            Assert.AreEqual(7, posted.Task.Sequence, "the SDK's arrival order travels with it");
            Assert.AreEqual(1, posted.Task.Generation);
            Assert.IsFalse(pending.Wait(100), "the handler is waiting for the simulation");
            Assert.AreEqual(1, host.CommandsInFlight);

            posted.Task.Complete(TaskResult.Ok("arrived"));
            Assert.IsTrue(pending.Wait(5000), "completing the request releases the handler");
            Assert.IsTrue(pending.Result.Success);
            Assert.AreEqual("arrived", pending.Result.Payload);
            Assert.AreEqual(0, host.CommandsInFlight);
        }

        [Test]
        public void AFailedTaskBecomesAFailedOutcomeCarryingItsReason()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox);
            var pending = Task.Run(() => host.HandleAsync(Cmd("c-1", "goto-refuel", null, 1), CancellationToken.None));
            DeviceEvent posted = null;
            var end = DateTime.UtcNow.AddSeconds(5);
            while (posted == null && DateTime.UtcNow < end) { inbox.Drain(1, e => posted = e); Thread.Sleep(5); }
            posted.Task.Complete(TaskResult.Fail("superseded by c-2"));
            Assert.IsTrue(pending.Wait(5000));
            Assert.IsFalse(pending.Result.Success);
            Assert.AreEqual("superseded by c-2", pending.Result.Error);
        }

        [Test]
        public void ADeliveryWithNoSequenceStillGetsAnArrivalOrder()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox);
            _ = Task.Run(() => host.HandleAsync(Cmd("c-1", "goto-refuel", null, 0), CancellationToken.None));
            var first = Await(inbox);
            _ = Task.Run(() => host.HandleAsync(Cmd("c-2", "goto-refuel", null, 0), CancellationToken.None));
            var second = Await(inbox);
            Assert.Greater(second.Task.Sequence, first.Task.Sequence, "so a newer one still wins");
            first.Task.Complete(TaskResult.Fail("x"));
            second.Task.Complete(TaskResult.Fail("x"));
        }

        static DeviceEvent Await(DeviceInbox inbox)
        {
            DeviceEvent got = null;
            var end = DateTime.UtcNow.AddSeconds(5);
            while (got == null && DateTime.UtcNow < end) { inbox.Drain(1, e => got = e); Thread.Sleep(5); }
            Assert.IsNotNull(got);
            return got;
        }

        [Test]
        public void AShutdownWhileACommandWaitsStillAnswersIt()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox);
            using var stop = new CancellationTokenSource();
            var pending = Task.Run(() => host.HandleAsync(Cmd("c-1", "goto-refuel", null, 1), stop.Token));
            Await(inbox);
            stop.Cancel();
            Assert.IsTrue(pending.Wait(5000));
            Assert.IsFalse(pending.Result.Success);
            StringAssert.Contains("shut down", pending.Result.Error);
        }

        [Test]
        public void ACommandFromARunThatIsOverIsAnsweredResetNotLeftWaiting()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox, generation: 1);
            var pending = Task.Run(() => host.HandleAsync(Cmd("c-1", "goto-refuel", null, 1), CancellationToken.None));
            var end = DateTime.UtcNow.AddSeconds(5);
            while (inbox.Pending == 0 && DateTime.UtcNow < end) Thread.Sleep(5);
            inbox.Drain(2, _ => Assert.Fail("a stale event must not be applied"));   // the plane has moved on to generation 2
            Assert.IsTrue(pending.Wait(5000));
            Assert.AreEqual("simulation reset before completion", pending.Result.Error);
            Assert.AreEqual(1, inbox.StaleDropped);
        }

        [Test]
        public void CommandsStillQueuedAtTheEndOfARunAreAnsweredReset()
        {
            var inbox = new DeviceInbox();
            var host = Host("SP-HL-0003", SceneKind.Hauler, inbox);
            var pending = Task.Run(() => host.HandleAsync(Cmd("c-1", "goto-refuel", null, 1), CancellationToken.None));
            var end = DateTime.UtcNow.AddSeconds(5);
            while (inbox.Pending == 0 && DateTime.UtcNow < end) Thread.Sleep(5);
            Assert.AreEqual(1, inbox.FailQueuedTasks(TaskReasons.Reset));
            Assert.IsTrue(pending.Wait(5000));
            Assert.AreEqual("simulation reset before completion", pending.Result.Error);
        }

        // ---- the presenter's fresh run

        static CommandItem Row(string token, string status) =>
            new CommandItem("dev-" + token, new ObservedCommand { Token = token, Name = "goto-refuel", Status = status, QueuedAt = DateTimeOffset.UtcNow, ObservedAt = DateTimeOffset.UtcNow });

        [Test]
        public void AFreshRunSplitsWhatCanBeCancelledFromWhatIsAlreadyAtADevice()
        {
            var plan = FreshRun.Plan(new[] { Row("a", "QUEUED"), Row("b", "HELD"), Row("c", "PARKED"), Row("d", "SENT"), Row("e", "SUCCESSFUL"), Row("f", "WEIRD") }, false);
            Assert.AreEqual(new[] { "a", "b", "c" }, plan.Cancellable.Select(c => c.Command.Token).ToArray());
            Assert.AreEqual(new[] { "d" }, plan.AtDevice.Select(c => c.Command.Token).ToArray());
            StringAssert.Contains("3 can be cancelled", plan.Describe());
            StringAssert.Contains("1 already sent", plan.Describe());
        }

        sealed class FakePlatform
        {
            public readonly List<string> Cancelled = new List<string>();
            public readonly List<string> Queries = new List<string>();
            public string[] Rows = new string[0];
            public string FailCancelFor;
            public string StillSentFor;

            public QueryFn Fn => (query, vars, ct) =>
            {
                Queries.Add(query);
                if (query == FreshRun.CancelMutation)
                {
                    using var doc = JsonDocument.Parse(vars);
                    var token = doc.RootElement.GetProperty("t").GetString();
                    if (token == FailCancelFor) throw new InvalidOperationException("forbidden");
                    if (token == StillSentFor) return Task.FromResult("{\"cancelCommand\":{\"token\":\"" + token + "\",\"status\":\"SENT\"}}");
                    Cancelled.Add(token);
                    return Task.FromResult("{\"cancelCommand\":{\"token\":\"" + token + "\",\"status\":\"CANCELLED\"}}");
                }

                // the list: rows are "token:STATUS"
                var items = Rows.Select(r =>
                {
                    var p = r.Split(':');
                    return "{\"token\":\"" + p[0] + "\",\"deviceToken\":\"dev-" + p[0] + "\",\"name\":\"goto-refuel\",\"status\":\"" + p[1] + "\",\"queuedTime\":\"2026-10-05T10:00:00Z\"}";
                });
                return Task.FromResult("{\"commands\":{\"results\":[" + string.Join(",", items) + "]}}");
            };
        }

        [Test]
        public void FreshRunListsThenCancelsOnlyWhatThePlatformCanStop()
        {
            var p = new FakePlatform { Rows = new[] { "a:QUEUED", "b:HELD", "c:SENT", "d:PARKED" } };
            var plan = FreshRun.ListAsync(p.Fn, () => DateTimeOffset.UtcNow, CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(3, plan.Cancellable.Count);
            Assert.AreEqual(1, plan.AtDevice.Count);
            Assert.IsEmpty(p.Cancelled, "listing cancels nothing");
            var result = FreshRun.CancelAsync(p.Fn, plan, CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(new[] { "a", "b", "d" }, p.Cancelled.ToArray(), "the SENT command is left to its device");
            Assert.AreEqual(3, result.Cancelled);
            StringAssert.Contains("cancelled 3", result.Describe());
        }

        [Test]
        public void FreshRunReportsACancelThatFailedOrFoundTheCommandAlreadyMovedOn()
        {
            var p = new FakePlatform { Rows = new[] { "a:QUEUED", "b:QUEUED", "c:HELD" }, FailCancelFor = "b", StillSentFor = "c" };
            var plan = FreshRun.ListAsync(p.Fn, () => DateTimeOffset.UtcNow, CancellationToken.None).GetAwaiter().GetResult();
            var result = FreshRun.CancelAsync(p.Fn, plan, CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(1, result.Cancelled);
            Assert.AreEqual(1, result.Failed);
            Assert.AreEqual(1, result.Unchanged, "the platform said SENT, not CANCELLED: it is not counted as cancelled");
            StringAssert.Contains("forbidden", result.FirstError);
        }

        [Test]
        public void FreshRunVariablesAskForTheUnfinishedStatesOnly()
        {
            using var doc = JsonDocument.Parse(FreshRun.ListVariables(2));
            var c = doc.RootElement.GetProperty("c");
            Assert.AreEqual(2, c.GetProperty("pageNumber").GetInt32());
            CollectionAssert.AreEquivalent(new[] { "QUEUED", "HELD", "SENT", "PARKED" }, c.GetProperty("statuses").EnumerateArray().Select(e => e.GetString()).ToArray());
        }

        static TaskDirector Director(out MachineModel model)
        {
            var body = new FakeBody("SP-HL-0006", EquipmentKind.Hauler, 0, 0);
            model = new MachineModel(EquipmentKind.Hauler, "SP-HL-0006");
            return new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(), new[] { ((IMachineBody)body, model) }, 1);
        }

        [Test]
        public void ThePresenterConfirmsBeforeAnythingIsCancelledAndCanDecline()
        {
            var p = new FakePlatform { Rows = new[] { "a:HELD", "b:QUEUED" } };
            var d = Director(out var model);
            var pc = new PresenterControls(d, id => model, () => p.Fn);
            pc.BeginFresh(CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(FreshState.Confirm, pc.Fresh);
            StringAssert.Contains("2 can be cancelled", pc.Message);
            Assert.IsEmpty(p.Cancelled, "asking is not cancelling: cancelling is an operator act");
            pc.DeclineFresh();
            Assert.AreEqual(FreshState.Idle, pc.Fresh);
            Assert.IsEmpty(p.Cancelled);

            pc.BeginFresh(CancellationToken.None).GetAwaiter().GetResult();
            pc.ConfirmFresh(CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(FreshState.Idle, pc.Fresh);
            Assert.AreEqual(new[] { "a", "b" }, p.Cancelled.OrderBy(x => x).ToArray());
            StringAssert.Contains("cancelled 2", pc.Message);
        }

        [Test]
        public void AFreshRunWithNothingToCancelSaysSoAndAsksNothing()
        {
            var p = new FakePlatform();
            var d = Director(out var model);
            var pc = new PresenterControls(d, id => model, () => p.Fn);
            pc.BeginFresh(CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(FreshState.Idle, pc.Fresh);
            StringAssert.Contains("no unfinished commands", pc.Message);
        }

        [Test]
        public void ConfirmWithoutAnAskDoesNothing()
        {
            var p = new FakePlatform { Rows = new[] { "a:HELD" } };
            var d = Director(out var model);
            var pc = new PresenterControls(d, id => model, () => p.Fn);
            pc.ConfirmFresh(CancellationToken.None).GetAwaiter().GetResult();
            Assert.IsEmpty(p.Cancelled);
            Assert.IsEmpty(p.Queries);
        }

        [Test]
        public void ThePresenterPicksMachinesAndTheirActionsWriteRowsToTheirTimelines()
        {
            var site = CommandKit.Site;
            var bodies = new[] { "SP-HL-0001", "SP-HL-0002" }.Select(i => (IMachineBody)new FakeBody(i, EquipmentKind.Hauler, 0, 0)).ToList();
            var models = bodies.ToDictionary(b => b.Id, b => new MachineModel(EquipmentKind.Hauler, b.Id));
            var tl = new Timeline();
            var d = new TaskDirector(site, CommandKit.Graph, tl, bodies.Select(b => (b, models[b.Id])), 1);
            var pc = new PresenterControls(d, id => models[id], null);
            Assert.AreEqual("SP-HL-0001", pc.Selected);
            pc.Select(1);
            Assert.AreEqual("SP-HL-0002", pc.Selected);
            pc.Select(1);
            Assert.AreEqual("SP-HL-0001", pc.Selected, "it wraps");
            pc.Select(-1);
            Assert.AreEqual("SP-HL-0002", pc.Selected);

            models["SP-HL-0002"].Restore(80, 1000);
            pc.PrepareLowFuel();
            Assert.AreEqual(15.5, models["SP-HL-0002"].FuelPct, 1e-9);
            Assert.AreEqual(TimelineKinds.Presenter, tl.Rows("SP-HL-0002").Last().Kind);
            StringAssert.Contains("prepare low-fuel cycle", tl.Rows("SP-HL-0002").Last().Text);
            Assert.AreEqual(0, tl.Rows("SP-HL-0001").Count);

            // a new command on the other machine moves the selection, unless the presenter holds it
            pc.FollowLatest("SP-HL-0001");
            Assert.AreEqual("SP-HL-0001", pc.Selected);
            StringAssert.Contains("SP-HL-0001", pc.PanelText(DateTimeOffset.UtcNow) ?? "SP-HL-0001");
        }

        [Test]
        public void TheKeyHelpNamesEveryPresenterBinding()
        {
            foreach (var key in new[] { "[ ] machine", "T timeline", "P prepare low fuel", "G resume work", "F fresh run", "R readiness", "L local sim" })
                StringAssert.Contains(key, PresenterControls.HelpLine);
        }
    }
}
