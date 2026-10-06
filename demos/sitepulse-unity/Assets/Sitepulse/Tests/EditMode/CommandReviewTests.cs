// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.RegularExpressions;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // Slice A5, second pass: the ways a command's end can go wrong (a budget that ran out in a queue, a refusal that
    // moved a truck, a command answered by someone else) and the timeline's account of them.
    public sealed class CommandReviewTests
    {
        const string Yard = "sp-zone-yard", Fill = "sp-zone-fill";

        static string Rows(Rig r) => string.Join("\n", r.Timeline.Rows(r.Id).Select(x => x.Kind + ": " + x.Text));

        static string F1(double v) => v.ToString("0.0", CultureInfo.InvariantCulture);

        // ---- the budget is for travelling

        [Test]
        public void AWaitInLineAndTheServiceAreNotChargedToTheTravelBudget()
        {
            // a clean drive to the queue, to know how long it takes and what the machine promised
            var probe = new Rig();
            probe.Model.Restore(14.0, 1000);
            probe.World.BayBusy = true;
            probe.Send("goto-refuel");
            Assert.IsTrue(probe.Run(() => probe.Controller.Phase == TaskPhase.WaitingForBay), Rows(probe));
            var drive = probe.Now;
            var eta = double.Parse(Regex.Match(Rows(probe), @"ETA (\d+) s").Groups[1].Value, CultureInfo.InvariantCulture);

            // the same drive held up for a long while on the way (inside what the driving budget allows), then a wait of
            // nearly the whole bay-wait limit, then the service: the whole is far beyond what the driving was promised
            var held = Math.Max(0.0, 2.0 * eta - drive + 35.0);
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            r.World.BayBusy = true;
            var cmd = r.Send("goto-refuel", token: "c-long");
            double blocked = 0, waited = 0;
            var finished = r.Run(() =>
            {
                if (cmd.IsComplete) return true;
                r.World.Block = r.Controller.Phase == TaskPhase.ToQueue && blocked < held;
                if (r.World.Block) blocked += 0.25;
                if (r.Controller.Phase == TaskPhase.WaitingForBay)
                {
                    waited += 0.25;
                    if (waited >= 115.0) r.World.BayBusy = false;
                }

                return false;
            }, 3000);
            Assert.IsTrue(finished, Rows(r));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded, cmd.Completion.Answer() + "\n" + Rows(r));
            Assert.GreaterOrEqual(r.Model.FuelPct, 94.9, "a full tank, not a truck cut off during the service");
            Assert.Greater(r.Now, 2.0 * eta + 60.0 + RefuelService.DurationSeconds, "the whole took longer than the driving was allowed");
        }

        [Test]
        public void AFailureInTheBayDrivesTheTruckOutSoTheNextOneIsServed()
        {
            var track = CommandKit.Track(0);
            var a = new FakeBody("A", EquipmentKind.Hauler, track[540].X, track[540].Z, track[540].HeadingDegrees, track);
            var b = new FakeBody("B", EquipmentKind.Hauler, track[600].X, track[600].Z, track[600].HeadingDegrees, track);
            var ma = new MachineModel(EquipmentKind.Hauler, "A");
            var mb = new MachineModel(EquipmentKind.Hauler, "B");
            ma.Restore(20, 1000);
            mb.Restore(20, 1000);
            var d = new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(), new[] { ((IMachineBody)a, ma), (b, mb) }, 1);
            var ra = new TaskRequest("c-a", "goto-refuel", null, 1, 1);
            d.Submit("A", ra);
            for (var t = 0.0; t < 1500 && d.Servicing != "A"; t += 0.25) d.Step(0.25, 0.25);
            Assert.AreEqual("A", d.Servicing);

            // the real clock runs out while the service runs (simulation time stands still)
            for (var i = 0; i < 200 && !ra.IsComplete; i++) d.Step(0.0, 10.0);
            Assert.IsTrue(ra.IsComplete);
            StringAssert.Contains("wall-clock cap", ra.Completion.Answer().Reason);
            Assert.AreEqual(MachineMode.Returning, d["A"].Mode, "the truck leaves the bay");
            Assert.IsNull(d.Bay.Holder);

            var rb = new TaskRequest("c-b", "goto-refuel", null, 2, 1);
            d.Submit("B", rb);
            for (var t = 0.0; t < 1500 && !rb.IsComplete; t += 0.25) d.Step(0.25, 0.25);
            Assert.IsTrue(rb.IsComplete);
            Assert.IsTrue(rb.Completion.Answer().Succeeded, "the next truck is served: " + rb.Completion.Answer());
            for (var t = 0.0; t < 600 && d["A"].Mode != MachineMode.Working; t += 0.25) d.Step(0.25, 0.25);
            Assert.AreEqual(MachineMode.Working, d["A"].Mode, "and the first is back on its track");
        }

        // ---- a refusal changes nothing about what the machine is doing

        [Test]
        public void ARefusedCommandWhileReturningLeavesTheTruckDrivingBack()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var cmd = r.Send("goto-refuel");
            Assert.IsTrue(r.Run(() => cmd.IsComplete), Rows(r));
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode);
            r.Run(() => false, 3);

            var refused = r.Send("goto-area", "sp-zone-moon");
            Assert.AreEqual("no scene geometry for area sp-zone-moon", refused.Completion.Answer().Reason);
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode, "it is still on its way back");
            Assert.IsFalse(r.Body.Attached);

            var prev = (r.Body.X, r.Body.Z);
            var worst = 0.0;
            Assert.IsTrue(r.Run(() =>
            {
                var step = Math.Sqrt((r.Body.X - prev.X) * (r.Body.X - prev.X) + (r.Body.Z - prev.Z) * (r.Body.Z - prev.Z));
                worst = Math.Max(worst, step);
                prev = (r.Body.X, r.Body.Z);
                return r.Controller.Mode == MachineMode.Working;
            }), Rows(r));
            Assert.LessOrEqual(worst, SpeedModel.HaulerCruise * 0.25 + 1e-6, "it never jumped: no step is longer than the truck can drive in one");
            Assert.AreEqual(1, r.Body.Attaches);
        }

        [Test]
        public void ARefusedCommandLeavesAParkedMachineItsSlot()
        {
            var r = new Rig();
            var cmd = r.Send("goto-area", Yard);
            Assert.IsTrue(r.Run(() => cmd.IsComplete));
            Assert.IsTrue(r.Parking.Holds(r.Id));
            var at = (r.Body.X, r.Body.Z);

            Assert.IsFalse(r.Send("goto-area", "sp-zone-moon").Completion.Answer().Succeeded);
            Assert.IsTrue(r.Parking.Holds(r.Id), "an unknown area");

            var fill = CommandKit.Site.Zones.First(z => z.Name == Fill);
            for (var i = 0; i < 40; i++) r.Parking.Claim("other-" + i, fill, 0, 0, null, ParkingLot.Margin(r.Body.Kind), CommandKit.Site.Obstacles, ParkingLot.FootprintRadius(r.Body.Kind));
            Assert.AreEqual("no free parking slot in sp-zone-fill", r.Send("goto-area", Fill).Completion.Answer().Reason);
            Assert.IsTrue(r.Parking.Holds(r.Id), "a full zone");

            Assert.IsFalse(r.Send("self-destruct").Completion.Answer().Succeeded);
            Assert.IsTrue(r.Parking.Holds(r.Id), "an unknown command");
            Assert.AreEqual(MachineMode.Parked, r.Controller.Mode);
            Assert.AreEqual(at, (r.Body.X, r.Body.Z));
        }

        [Test]
        public void ACommandThatSupersedesAServiceAndIsRefusedDrivesTheTruckOutOfTheBay()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var old = r.Send("goto-refuel");
            Assert.IsTrue(r.Run(() => r.Controller.Phase == TaskPhase.Refuelling));
            var refused = r.Send("goto-area", "sp-zone-moon");
            Assert.IsTrue(old.IsComplete);
            Assert.IsTrue(refused.IsComplete);
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode, "not parked in the bay");
            Assert.IsNull(r.Bay.Holder);
            Assert.IsTrue(r.Run(() => r.Controller.Mode == MachineMode.Working), Rows(r));
        }

        // ---- a command somebody else already answered

        [Test]
        public void ACommandTheSessionAlreadyAnsweredIsDroppedAndTheMachineStops()
        {
            var r = new Rig();
            var cmd = r.Send("goto-area", Yard);
            r.Run(() => false, 10);
            Assert.IsTrue(cmd.Complete(TaskResult.Fail("the simulation gave no answer in time")), "the session's own last resort");
            r.Controller.Step(0.25, 0.25);
            Assert.IsNull(r.Controller.Running);
            Assert.AreEqual(MachineMode.Parked, r.Controller.Mode);
            Assert.IsFalse(r.Parking.Holds(r.Id));
            StringAssert.Contains("outcome: DROPPED", Rows(r));
            Assert.AreEqual("the simulation gave no answer in time", cmd.Completion.Answer().Reason, "the first answer stands");

            var at = (r.Body.X, r.Body.Z);
            r.Run(() => false, 10);
            Assert.AreEqual(at, (r.Body.X, r.Body.Z), "it does not go on driving to a place nobody is waiting for");

            var next = r.Send("goto-area", Fill);
            Assert.IsTrue(r.Run(() => next.IsComplete));
            Assert.IsTrue(next.Completion.Answer().Succeeded, "and it takes the next command");
        }

        [Test]
        public void AServiceTheSessionAlreadyAnsweredStopsAndTheTruckLeavesTheBay()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var cmd = r.Send("goto-refuel");
            Assert.IsTrue(r.Run(() => r.Controller.Phase == TaskPhase.Refuelling));
            r.Run(() => false, 5);
            cmd.Complete(TaskResult.Fail("the device shut down before completion"));
            var at = r.Model.FuelPct;
            r.Controller.Step(0.25, 0.25);
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode);
            Assert.IsNull(r.Bay.Holder);
            StringAssert.Contains("service stopped at " + F1(at) + "% fuel", Rows(r));
            r.Run(() => false, 40);
            Assert.LessOrEqual(r.Model.FuelPct, at + 1e-9, "no more fuel for a command nobody waits on");
        }

        // ---- the timeline tells the truth

        [Test]
        public void ARefusedCommandIsNeverLaterReportedSuperseded()
        {
            var r = new Rig();
            var bad = r.Send("goto-area", "sp-zone-moon", "c-bad");
            var good = r.Send("goto-area", Yard, "c-good");
            Assert.IsTrue(r.Run(() => good.IsComplete));
            Assert.AreEqual("no scene geometry for area sp-zone-moon", bad.Completion.Answer().Reason, "its answer is the refusal");
            Assert.IsFalse(r.Timeline.Rows(r.Id).Any(x => x.Kind == TimelineKinds.Superseded), Rows(r));
        }

        [Test]
        public void ACommandTheSessionAnsweredIsNotReportedSupersededWhenANewerOneArrives()
        {
            var r = new Rig();
            var first = r.Send("goto-area", Yard, "c-1");
            first.Complete(TaskResult.Fail("the simulation gave no answer in time"));
            var second = r.Send("goto-area", Fill, "c-2");
            Assert.AreEqual("the simulation gave no answer in time", first.Completion.Answer().Reason);
            Assert.IsFalse(r.Timeline.Rows(r.Id).Any(x => x.Kind == TimelineKinds.Superseded), "it was not superseded: it had been answered; " + Rows(r));
            StringAssert.Contains("outcome: dropped", Rows(r));
            Assert.IsTrue(r.Run(() => second.IsComplete));
            Assert.IsTrue(second.Completion.Answer().Succeeded);
        }

        [Test]
        public void ACommandWithTheSameSequenceAsTheNewestIsRefused()
        {
            var a = new TaskArbiter();
            var one = new TaskRequest("c-1", "goto-area", Yard, 5, 1);
            Assert.IsTrue(a.Offer(one).Accepted);
            var d = a.Offer(new TaskRequest("c-2", "goto-area", Fill, 5, 1));
            Assert.IsFalse(d.Accepted, "equal is not newer");
            Assert.AreEqual("superseded by c-1", d.Reason);
            Assert.AreSame(one, a.Running);
        }

        [Test]
        public void TheTimelineShowsTheEndOfATokenWhichIsWhatTellsCommandsApart()
        {
            var r = new Rig();
            var token = "5f3a9c1e-42b7-4d0a-9c11-0000000000" + "ab12cd34ef";
            r.Send("goto-area", Yard, token);
            var rows = Rows(r);
            StringAssert.Contains("(\u2026" + token.Substring(token.Length - 10) + ")", rows);
            StringAssert.DoesNotContain(token.Substring(0, 12), rows);

            var s = new Rig("SP-HL-0004");
            s.Send("goto-area", Yard, "c-1");
            StringAssert.Contains("(c-1)", Rows(s), "a short token is shown whole");

            var t = new Rig("SP-HL-0005");
            var older = "first-command-aaaaaaaaaaaaaaaa";
            t.Send("goto-area", Yard, older);
            t.Send("goto-area", Fill, "second-command-bbbbbbbbbbbbbbbb");
            StringAssert.Contains("superseded: goto-area \u2026" + older.Substring(older.Length - 10) + " superseded by second-command-bbbbbbbbbbbbbbbb", Rows(t));
        }

        // ---- going back to work

        static TrackPoint NearestTo(IList<TrackPoint> track, double x, double z) =>
            track.OrderBy(p => (p.X - x) * (p.X - x) + (p.Z - z) * (p.Z - z)).First();

        [Test]
        public void TheMachineIsPutBackOnTheFrameOfItsTrackNearestToWhereItStands()
        {
            var track = CommandKit.Track(0);

            // after a refuel: it stands in the bay
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var cmd = r.Send("goto-refuel");
            Assert.IsTrue(r.Run(() => cmd.IsComplete));
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode);
            var expected = NearestTo(track, r.Body.X, r.Body.Z);
            Assert.IsTrue(r.Run(() => r.Controller.Mode == MachineMode.Working), Rows(r));
            Assert.AreEqual(expected.TrackSeconds, r.Body.LastAttach.Value.TrackSeconds, 1e-9, "the nearest frame of its track");
            Assert.AreEqual(expected.X, r.Body.LastAttach.Value.X, 3.0);
            Assert.AreEqual(expected.Z, r.Body.LastAttach.Value.Z, 3.0);
            Assert.AreEqual(expected.X, r.Body.AttachedFrom.Value.X, 3.0, "and it had driven to that place before it was put back");
            Assert.AreEqual(expected.Z, r.Body.AttachedFrom.Value.Z, 3.0);

            // after parking in a zone and resuming: it stands in the zone
            var p = new Rig("SP-HL-0004");
            var area = p.Send("goto-area", Fill);
            Assert.IsTrue(p.Run(() => area.IsComplete));
            var expectedFromFill = NearestTo(track, p.Body.X, p.Body.Z);
            Assert.AreNotEqual(expected.TrackSeconds, expectedFromFill.TrackSeconds, "a different place on the track");
            Assert.IsNull(p.Controller.Resume());
            Assert.IsTrue(p.Run(() => p.Controller.Mode == MachineMode.Working), Rows(p));
            Assert.AreEqual(expectedFromFill.TrackSeconds, p.Body.LastAttach.Value.TrackSeconds, 1e-9);
            Assert.AreEqual(expectedFromFill.X, p.Body.AttachedFrom.Value.X, 3.0);
            Assert.AreEqual(expectedFromFill.Z, p.Body.AttachedFrom.Value.Z, 3.0);
        }

        // ---- parking keeps the whole machine inside the zone

        [Test]
        public void EveryParkingSlotKeepsTheWholeMachineInsideItsZone()
        {
            foreach (var kind in new[] { EquipmentKind.Hauler, EquipmentKind.Loader, EquipmentKind.Dozer })
            {
                var margin = ParkingLot.Margin(kind);
                Assert.GreaterOrEqual(margin, ParkingLot.LengthOf(kind) / 2.0 + 1.0, kind.ToString());
                foreach (var z in CommandKit.Site.Zones)
                {
                    var slots = ParkingLot.Slots(z, z.CentreX, z.CentreZ, margin);
                    Assert.IsNotEmpty(slots, z.Name);
                    foreach (var s in slots)
                        Assert.IsTrue(z.Contains(s.X, s.Z, -margin), $"{kind} in {z.Name} slot {s.Index}: ({s.X:0.0}, {s.Z:0.0}) is too near the edge");
                }
            }
        }

        [Test]
        public void AZoneTooSmallForTheMachineHasNoSlotsAndTheCommandIsRefused()
        {
            var tiny = new Rect2("tiny", 0, 10, 0, 10);
            Assert.IsEmpty(ParkingLot.Slots(tiny, 0, 0, ParkingLot.Margin(EquipmentKind.Hauler)), "nothing is parked half outside a zone");
            Assert.IsNull(new ParkingLot().Claim("A", tiny, 0, 0, null, ParkingLot.Margin(EquipmentKind.Hauler)));
        }

        [Test]
        public void ATruckSentToAZoneParksWellInsideIt()
        {
            var r = new Rig("SP-HL-0003");
            var cmd = r.Send("goto-area", Yard);
            Assert.IsTrue(r.Run(() => cmd.IsComplete));
            Assert.IsTrue(CommandKit.Site.TryZone(Yard, out var yard));
            Assert.IsTrue(yard.Contains(r.Body.X, r.Body.Z, -ParkingLot.Margin(EquipmentKind.Hauler)), "the truck that was sent parks well inside");
        }

        // ---- parking keeps the whole machine clear of what stands in the zone

        static readonly EquipmentKind[] Kinds = { EquipmentKind.Hauler, EquipmentKind.Loader, EquipmentKind.Dozer };

        [Test]
        public void EveryParkingSlotKeepsTheMachineClearOfEveryPropAndPile()
        {
            Assert.IsNotEmpty(CommandKit.Site.Obstacles, "the feature file's props, piles and the refuel approach are read");
            foreach (var kind in Kinds)
                foreach (var z in CommandKit.Site.Zones)
                    foreach (var s in ParkingLot.Slots(z, z.CentreX, z.CentreZ, ParkingLot.Margin(kind), CommandKit.Site.Obstacles, ParkingLot.FootprintRadius(kind)))
                        foreach (var o in CommandKit.Site.Obstacles)
                            Assert.GreaterOrEqual(o.Distance(s.X, s.Z), ParkingLot.FootprintRadius(kind) + ParkingLot.Clearance,
                                $"{kind} in {z.Name} slot {s.Index} ({s.X:0.0}, {s.Z:0.0}) touches {o.Name}");
        }

        [Test]
        public void EveryZoneStillHasAtLeastThreeHaulerSlots()
        {
            foreach (var z in CommandKit.Site.Zones)
            {
                var slots = ParkingLot.Slots(z, z.CentreX, z.CentreZ, ParkingLot.Margin(EquipmentKind.Hauler), CommandKit.Site.Obstacles, ParkingLot.FootprintRadius(EquipmentKind.Hauler));
                Assert.GreaterOrEqual(slots.Count, 3, z.Name + ": " + string.Join(" ", slots.Select(s => $"({s.X:0.#},{s.Z:0.#})")));
            }
        }

        [Test]
        public void ThePropsAreReadWhereTheFeatureFileStandsThemAndTheWorkshopRoofIsNotAParkingSlot()
        {
            var site = CommandKit.Site;
            var workshop = site.Obstacles.First(o => o.Name == "workshop");
            Assert.Less(workshop.Distance(-70, -36), 0, "the workshop's middle is inside it");
            Assert.Greater(workshop.Distance(-70, -50), 0, "and 14 m north of it is outside");
            Assert.Less(site.Obstacles.First(o => o.Name == "yard-stockpile-1").Distance(-98, -22), 0, "a pile is read as an obstacle");
            Assert.Less(site.Obstacles.First(o => o.Name == "refuel-approach").Distance(-59.5, -60), 0, "so is the strip between the refuel queue and the bay");
        }

        [Test]
        public void AZoneWhoseSlotsAreAllBlockedRefusesWithAReason()
        {
            var zone = new Rect2("blocked", 0, 40, 0, 40);
            var wall = new[] { Obstacle.Box("wall", 20, 20, 30, 30, 0) };
            Assert.IsEmpty(ParkingLot.Slots(zone, 0, 0, ParkingLot.Margin(EquipmentKind.Hauler), wall, ParkingLot.FootprintRadius(EquipmentKind.Hauler)));
            Assert.IsNull(new ParkingLot().Claim("A", zone, 0, 0, null, ParkingLot.Margin(EquipmentKind.Hauler), wall, ParkingLot.FootprintRadius(EquipmentKind.Hauler)));
        }

        [Test]
        public void ATruckSentToTheYardParksClearOfTheYardsProps()
        {
            var r = new Rig("SP-HL-0003");
            var cmd = r.Send("goto-area", Yard);
            Assert.IsTrue(r.Run(() => cmd.IsComplete));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded);
            foreach (var o in CommandKit.Site.Obstacles)
                Assert.GreaterOrEqual(o.Distance(r.Body.X, r.Body.Z), ParkingLot.FootprintRadius(EquipmentKind.Hauler), $"parked on {o.Name}");
        }

        [Test]
        public void TwoMachinesAskingFromDifferentPlacesAreOfferedTheSameSlotsNeverOverlappingOnes()
        {
            var site = CommandKit.Site;
            foreach (var z in site.Zones)
            {
                var m = ParkingLot.Margin(EquipmentKind.Hauler);
                var f = ParkingLot.FootprintRadius(EquipmentKind.Hauler);
                var a = ParkingLot.Slots(z, z.X0, z.Z0, m, site.Obstacles, f);
                var b = ParkingLot.Slots(z, z.X1, z.Z1, m, site.Obstacles, f);
                CollectionAssert.AreEquivalent(a.Select(s => s.Index), b.Select(s => s.Index), z.Name);
                CollectionAssert.AreEqual(a.Select(s => s.Index), ParkingLot.Slots(z, z.X0, z.Z0, m, site.Obstacles, f).Select(s => s.Index), z.Name + " is deterministic");
                foreach (var p in a)
                    foreach (var q in a)
                        if (p.Index != q.Index)
                            Assert.GreaterOrEqual(Math.Sqrt((p.X - q.X) * (p.X - q.X) + (p.Z - q.Z) * (p.Z - q.Z)), ParkingLot.Spacing - 1e-6, z.Name);
            }
        }
    }
}
