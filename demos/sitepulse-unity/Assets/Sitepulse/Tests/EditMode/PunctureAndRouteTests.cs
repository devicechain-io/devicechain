// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Replay;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The presenter's puncture: a slow leak through the model's own step, an input and nothing more.</summary>
    public sealed class PunctureTests
    {
        const double Dt = 0.1;

        static MachineModel Truck() => new MachineModel(EquipmentKind.Hauler, "SP-HL-0003");

        static void Run(MachineModel m, double seconds, Action<double, double> each = null, double from = 0)
        {
            for (var t = 0.0; t < seconds; t += Dt)
            {
                m.Step(Dt, new MachineInput(5.0, false));
                each?.Invoke(from + t + Dt, m.TyrePressureKpa);
            }
        }

        [Test]
        public void ThePunctureBringsThePressureToJustAboveTheLineAndTheLeakCarriesItThroughInAboutAMinute()
        {
            var m = Truck();
            Run(m, 30);
            Assert.Greater(m.TyrePressureKpa, 690, "a healthy tyre");
            Assert.IsTrue(m.PrepareTyreLeak(605, 60, 600));
            double crossedAt = -1;
            var above = 0;
            Run(m, 90, (t, kpa) =>
            {
                if (kpa < 600 && crossedAt < 0) crossedAt = t;
                if (crossedAt >= 0 && kpa >= 600) above++;
            }, from: 0);
            Assert.Greater(crossedAt, 15, "it does not drop through the line at once");
            Assert.Less(crossedAt, 62, "and it is through it within about a minute");
            Assert.AreEqual(0, above, "once through it stays below: a leak only grows");
        }

        [Test]
        public void TheFirstStepAfterThePunctureIsAlreadyJustAboveTheLineNotWhereItWas()
        {
            var m = Truck();
            Run(m, 5);
            m.PrepareTyreLeak(605, 60, 600);
            m.Step(Dt, new MachineInput(5.0, false));
            Assert.That(m.TyrePressureKpa, Is.InRange(604.0, 606.0));
        }

        [Test]
        public void TheLeakStopsWellPastTheLineAndTheModelHoldsThere()
        {
            var m = Truck();
            m.PrepareTyreLeak(605, 60, 600);
            Run(m, 400);
            Assert.AreEqual(0.0, m.LeakKpaPerSecond, "the leak ran its course");
            Assert.That(m.TyrePressureKpa, Is.InRange(584.0, 589.5), "about a dozen kPa past the line, not flat");
            var held = m.TyrePressureKpa;
            Run(m, 60);
            Assert.AreEqual(held, m.TyrePressureKpa, 2.0, "and it stays there");
        }

        [Test]
        public void ThePressureThePlatformIsToldIsTheModelsOwnPublishedValue()
        {
            var m = Truck();
            m.PrepareTyreLeak(605, 60, 600);
            Run(m, 80);
            var published = m.Measurements()[MeasurementKeys.TyrePressureKpa];
            Assert.AreEqual(Math.Round(m.TyrePressureKpa, 1), published, 1e-9, "the samples the device sends carry the leaking pressure, through the ordinary path");
            Assert.Less(published, 600);
        }

        [Test]
        public void APunctureCanOnlyLowerThePressureNeverRaiseIt()
        {
            var m = Truck();
            m.PrepareTyreLeak(605, 60, 600);
            Run(m, 120);
            var low = m.TyrePressureKpa;
            Assert.Less(low, 600);
            Assert.IsFalse(m.PrepareTyreLeak(605, 60, 600), "already through the line: nothing to prepare");
            Run(m, 1);
            Assert.LessOrEqual(m.TyrePressureKpa, low + 1.0, "and nothing was raised");
        }

        [Test]
        public void ADozerAndTheCrusherHaveNoTyresToPuncture()
        {
            foreach (var kind in new[] { EquipmentKind.Dozer, EquipmentKind.Plant })
            {
                var m = new MachineModel(kind, "X-1");
                Assert.IsFalse(m.PrepareTyreLeak(605, 60, 600));
                Assert.AreEqual(0.0, m.LeakKpaPerSecond);
            }
        }

        [Test]
        public void TheLeakNeverTouchesTheFuelOrTheEngine()
        {
            var a = Truck();
            var b = Truck();
            b.PrepareTyreLeak(605, 60, 600);
            Run(a, 120);
            Run(b, 120);
            Assert.AreEqual(a.FuelPct, b.FuelPct, 1e-12);
            Assert.AreEqual(a.EngineTempC, b.EngineTempC, 1e-12);
            Assert.AreEqual(a.EngineHours, b.EngineHours, 1e-12);
        }

        [Test]
        public void ThePresentersActionSaysWhatItDidInTheMachinesTimelineAsAPresentersRow()
        {
            var tl = new Timeline();
            var m = Truck();
            var said = PresenterActions.PrepareTyreLeak("SP-HL-0003", m, tl);
            StringAssert.StartsWith("puncture (a slow tyre leak): tyre pressure", said);
            StringAssert.Contains("falls through 600 kPa in about 60 s", said);
            var row = tl.Rows("SP-HL-0003").Single();
            Assert.AreEqual(TimelineKinds.Presenter, row.Kind);
            Assert.AreEqual(said, row.Text);

            var dozer = PresenterActions.PrepareTyreLeak("SP-DZ-0001", new MachineModel(EquipmentKind.Dozer, "SP-DZ-0001"), tl);
            StringAssert.Contains("no tyres", dozer);
            Assert.AreEqual(TimelineKinds.Presenter, tl.Rows("SP-DZ-0001").Single().Kind, "even a refusal is on the record");
        }

        [Test]
        public void ThePresentersKeyReachesTheSelectedMachineAndAnUnknownOneNone()
        {
            var bodies = new[] { "SP-HL-0001", "SP-HL-0003" }.Select(i => (IMachineBody)new FakeBody(i, EquipmentKind.Hauler, 0, 0)).ToList();
            var models = bodies.ToDictionary(b => b.Id, b => new MachineModel(EquipmentKind.Hauler, b.Id));
            var d = new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(), bodies.Select(b => (b, models[b.Id])), 1);
            var pc = new PresenterControls(d, id => models[id], null);
            Assert.IsNull(pc.PrepareTyreLeak("SP-XX-0001"));
            Assert.IsNotNull(pc.PrepareTyreLeak("SP-HL-0003"));
            Assert.Greater(models["SP-HL-0003"].LeakKpaPerSecond, 0);
            Assert.AreEqual(0.0, models["SP-HL-0001"].LeakKpaPerSecond, "the other truck is untouched");
            StringAssert.Contains("puncture", pc.Message);
            StringAssert.Contains("K puncture a tyre", PresenterControls.HelpLine);
        }
    }

    /// <summary>The route highlight's source: what the task layer drives, announced as it starts and ends, and drawn again from a recording.</summary>
    public sealed class RouteHighlightSourceTests
    {
        [Test]
        public void ARoutePolylineKeepsThePointsAndTheirDistances()
        {
            var r = RoutePolyline.FromFlat(new double[] { 0, 0, 3, 4, 3, 14 });
            Assert.AreEqual(3, r.Count);
            Assert.AreEqual(15f, r.Length, 1e-4);
            Assert.AreEqual(5f, r.At(1), 1e-4);
            Assert.AreEqual(14f, r.Z(2));
        }

        [Test]
        public void AnEmptyOrOddListIsNotARoute()
        {
            Assert.IsNull(RoutePolyline.FromFlat(new double[0]));
            Assert.IsNull(RoutePolyline.FromFlat(new double[] { 1, 2 }), "one point is not a route");
            Assert.IsNull(RoutePolyline.FromFlat(new double[] { 1, 2, 3, 4, 5 }));
            Assert.IsNull(RoutePolyline.FromFlat(null));
        }

        [Test]
        public void TheCacheHandsBackTheSamePolylineForTheSameRouteAndNoneWhenThereIsNone()
        {
            var cache = new RouteCache();
            var rig = new Rig();
            rig.Send("goto-area", "sp-zone-yard");
            var route = rig.Controller.CurrentRoute;
            Assert.IsNotNull(route, "an accepted command is driving a route");
            var a = cache.Of(rig.Id, route);
            Assert.AreSame(a, cache.Of(rig.Id, route));
            Assert.AreEqual(route.Xs.Count, a.Count);
            Assert.IsNull(cache.Of(rig.Id, null));
            Assert.AreNotSame(a, cache.Of(rig.Id, route), "once it was none, the same route is a new highlight");
        }

        [Test]
        public void ARouteIsFlattenedToATenthOfAMetre()
        {
            var rig = new Rig();
            rig.Send("goto-refuel");
            var route = rig.Controller.CurrentRoute;
            var flat = RoutePolyline.ToFlat(route);
            Assert.AreEqual(route.Xs.Count * 2, flat.Count);
            Assert.AreEqual(Math.Round(route.Xs[0], 1), flat[0]);
            Assert.IsEmpty(RoutePolyline.ToFlat(null), "no route is an empty list, which a recording reads as 'stopped driving'");
        }

        [Test]
        public void TheDirectorAnnouncesARouteWhenItStartsAndAnotherWhenItEndsAndNothingBetween()
        {
            var body = new FakeBody("SP-HL-0003", EquipmentKind.Hauler, CommandKit.Track(0)[300].X, CommandKit.Track(0)[300].Z, 0, CommandKit.Track(0));
            var model = new MachineModel(EquipmentKind.Hauler, "SP-HL-0003");
            var d = new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(), new[] { ((IMachineBody)body, model) }, 1);
            var events = new List<(string id, Route route)>();
            d.RouteChanged += (id, r) => events.Add((id, r));

            var req = new TaskRequest("c-1", "goto-area", "sp-zone-yard", 1, 1);
            d.Submit("SP-HL-0003", req);
            Assert.AreEqual(0, events.Count, "nothing is announced until the director steps");
            d.Step(0.25, 0.25);
            Assert.AreEqual(1, events.Count);
            Assert.IsNotNull(events[0].route);
            Assert.AreEqual("SP-HL-0003", events[0].id);
            d.Step(0.25, 0.25);
            Assert.AreEqual(1, events.Count, "driving along the same route announces nothing");

            for (var t = 0; t < 8000 && !req.IsComplete; t++) d.Step(0.25, 0.25);
            Assert.IsTrue(req.IsComplete);
            d.Step(0.25, 0.25);
            Assert.AreEqual(2, events.Count);
            Assert.IsNull(events[1].route, "stopped driving it");
        }

        [Test]
        public void ARefuelAnnouncesTheDriveToTheQueueTheApproachToTheBayAndTheWayBack()
        {
            var body = new FakeBody("SP-HL-0003", EquipmentKind.Hauler, CommandKit.Track(0)[300].X, CommandKit.Track(0)[300].Z, 0, CommandKit.Track(0));
            var model = new MachineModel(EquipmentKind.Hauler, "SP-HL-0003");
            var d = new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(), new[] { ((IMachineBody)body, model) }, 1);
            var events = new List<Route>();
            d.RouteChanged += (id, r) => events.Add(r);
            d.Submit("SP-HL-0003", new TaskRequest("c-1", "goto-refuel", null, 1, 1));
            for (var t = 0; t < 12000 && !(d["SP-HL-0003"].Mode == MachineMode.Working && events.Count > 2); t++) d.Step(0.25, 0.25);
            Assert.AreEqual(MachineMode.Working, d["SP-HL-0003"].Mode, "back at work");
            var started = events.Count(r => r != null);
            Assert.AreEqual(3, started, "the queue, the bay and the way back");
            for (var i = 1; i < events.Count; i++) Assert.AreNotEqual(events[i - 1] == null, events[i] == null, "starts and ends alternate");
            Assert.IsNull(events.Last());
        }

        [Test]
        public void ARouteLineSurvivesTheRecordingAndEmptyMeansStopped()
        {
            var route = new Rig().Controller.CurrentRoute;
            Assert.IsNull(route, "an idle machine drives nothing");
            var l = RecordingMaps.Route("SP-HL-0003", null);
            Assert.AreEqual(DeviceKinds.Route, l.K);
            Assert.AreEqual(0, l.Points.Count);

            var line = DeviceLine.Of(DeviceKinds.Route, "SP-HL-0003");
            line.T = 1.5;
            line.Utc = SyntheticRun.Start;
            line.Points.AddRange(new[] { 1.04, 2.0, 30.26, 4.5 });
            using var doc = JsonDocument.Parse(line.ToJson());
            var back = DeviceLine.Read(doc.RootElement);
            Assert.AreEqual(DeviceKinds.Route, back.K);
            CollectionAssert.AreEqual(new[] { 1.0, 2.0, 30.3, 4.5 }, back.Points.ToArray(), "to a tenth of a metre");
        }

        [Test]
        public void AReplayDrawsTheRouteFromTheRecordingWhileItWasDrivenAndNotBeforeOrAfter()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 12.0);
            var start = DeviceLine.Of(DeviceKinds.Route, SyntheticRun.Truck);
            start.Points.AddRange(new double[] { 0, 0, 10, 0, 10, 10 });
            run.Dev(3.0, start);
            run.Dev(8.0, DeviceLine.Of(DeviceKinds.Route, SyntheticRun.Truck));
            var session = new ReplaySession(run.Reload());

            session.Seek(1.0);
            Assert.IsNull(session.RouteOf(SyntheticRun.Truck));
            session.Seek(5.0);
            var r = session.RouteOf(SyntheticRun.Truck);
            Assert.IsNotNull(r);
            Assert.AreEqual(20f, r.Length, 1e-4);
            Assert.IsNull(session.RouteOf(SyntheticRun.Loader), "another machine drove none");
            session.Seek(9.0);
            Assert.IsNull(session.RouteOf(SyntheticRun.Truck), "it stopped driving it");
            session.Seek(4.0);
            Assert.IsNotNull(session.RouteOf(SyntheticRun.Truck), "and rewound to while it drove, it is drawn again");
        }
    }
}
