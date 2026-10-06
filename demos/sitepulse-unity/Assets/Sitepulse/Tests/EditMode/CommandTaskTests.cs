// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text.RegularExpressions;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // Slice A5: what a machine does with a command, from the arbiter to the bay to the way back to work.
    public sealed class CommandTaskTests
    {
        const string Yard = "sp-zone-yard", Fill = "sp-zone-fill", Cut = "sp-zone-cut";

        static bool InZone(Rig r, string token)
        {
            Assert.IsTrue(CommandKit.Site.TryZone(token, out var z));
            return z.Contains(r.Body.X, r.Body.Z);
        }

        static string Rows(Rig r) => string.Join("\n", r.Timeline.Rows(r.Id).Select(x => x.Kind + ": " + x.Text));

        // ---- the arbiter

        [Test]
        public void ANewerCommandSupersedesTheRunningOne()
        {
            var a = new TaskArbiter();
            var one = new TaskRequest("c-1", "goto-area", Yard, 5, 1);
            var two = new TaskRequest("c-2", "goto-refuel", null, 6, 1);
            var d1 = a.Offer(one);
            Assert.IsTrue(d1.Accepted);
            Assert.IsNull(d1.Superseded);
            var d2 = a.Offer(two);
            Assert.IsTrue(d2.Accepted);
            Assert.AreSame(one, d2.Superseded, "the running one is the one that ends");
            Assert.AreSame(two, a.Running);
        }

        [Test]
        public void ALateArrivingOlderCommandFailsAtOnceNamingTheOneThatIsRunning()
        {
            var a = new TaskArbiter();
            var newer = new TaskRequest("c-new", "goto-area", Yard, 9, 1);
            var older = new TaskRequest("c-old", "goto-area", Fill, 8, 1);
            a.Offer(newer);
            var d = a.Offer(older);
            Assert.IsFalse(d.Accepted);
            Assert.AreEqual("superseded by c-new", d.Reason);
            Assert.AreSame(newer, a.Running, "the older one never displaced it");
        }

        [Test]
        public void AnOlderCommandThatArrivesAfterTheNewerOneFinishedStillDoesNotRun()
        {
            var a = new TaskArbiter();
            var newer = new TaskRequest("c-new", "goto-area", Yard, 9, 1);
            a.Offer(newer);
            a.Finished(newer);
            Assert.IsNull(a.Running);
            var d = a.Offer(new TaskRequest("c-old", "goto-area", Fill, 3, 1));
            Assert.IsFalse(d.Accepted, "running it now would act on an intent the operator has replaced");
            Assert.AreEqual("superseded by c-new", d.Reason);
        }

        [Test]
        public void ARefuelDriveIsSupersededByAGotoAreaAndReleasesTheBay()
        {
            var r = new Rig();
            var refuel = r.Send("goto-refuel", token: "c-refuel");
            Assert.IsTrue(r.Run(() => r.Controller.Phase == TaskPhase.Refuelling), "the truck reaches the bay and starts service");
            Assert.AreEqual(r.Id, r.Bay.Holder);

            var area = r.Send("goto-area", Yard, "c-area");
            Assert.IsTrue(refuel.IsComplete);
            var result = refuel.Completion.Answer();
            Assert.IsFalse(result.Succeeded);
            Assert.AreEqual("superseded by c-area", result.Reason);
            Assert.IsNull(r.Bay.Holder, "its reservation went with it");
            StringAssert.Contains("superseded", Rows(r));
            Assert.IsFalse(area.IsComplete, "the newer command is now running");
            Assert.IsTrue(r.Run(() => area.IsComplete));
            Assert.IsTrue(area.Completion.Answer().Succeeded);
        }

        [Test]
        public void ALateOlderCommandFailsWhileTheNewerOneKeepsRunning()
        {
            var r = new Rig();
            var newer = r.Send("goto-area", Yard, "c-new", seq: 10);
            var older = r.Send("goto-area", Fill, "c-old", seq: 4);
            Assert.IsTrue(older.IsComplete);
            Assert.AreEqual("superseded by c-new", older.Completion.Answer().Reason);
            Assert.IsFalse(newer.IsComplete);
            Assert.IsTrue(r.Run(() => newer.IsComplete));
            Assert.IsTrue(newer.Completion.Answer().Succeeded);
            Assert.IsTrue(InZone(r, Yard));
        }

        // ---- goto-area

        [Test]
        public void GotoAreaDrivesOffItsTrackArrivesInsideTheZoneAndStaysParked()
        {
            var r = new Rig();
            Assert.IsTrue(r.Body.Attached);
            var cmd = r.Send("goto-area", Yard);
            Assert.IsFalse(r.Body.Attached, "an accepted command takes the machine off its track");
            Assert.AreEqual(MachineMode.Commanded, r.Controller.Mode);
            Assert.IsTrue(r.Run(() => cmd.IsComplete), Rows(r));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded, Rows(r));
            Assert.IsTrue(InZone(r, Yard), "success is arrival inside the zone");
            Assert.AreEqual(MachineMode.Parked, r.Controller.Mode);

            var at = (r.Body.X, r.Body.Z);
            r.Run(() => false, 30);
            Assert.AreEqual(at, (r.Body.X, r.Body.Z), "and it stays there: nothing sends it back");
            Assert.IsFalse(r.Body.Attached);
            Assert.AreEqual(0, r.Body.Attaches);
            Assert.IsTrue(r.Parking.Holds(r.Id), "its slot stays claimed while it is parked");

            var rows = Rows(r);
            StringAssert.Contains("received: goto-area sp-zone-yard", rows);
            Assert.IsTrue(Regex.IsMatch(rows, @"accepted: sp-zone-yard: route \d+ m, ETA \d+ s"), rows);
            StringAssert.Contains("arrived", rows);
            StringAssert.Contains("outcome: SUCCESS", rows);
        }

        [Test]
        public void TwoMachinesSentToOneZoneGetDifferentSlots()
        {
            var a = new Rig("SP-HL-0001");
            var lot = a.Parking;
            var zone = CommandKit.Site.Zones.First(z => z.Name == Yard);
            var first = lot.Claim("A", zone, 0, 0, null);
            var second = lot.Claim("B", zone, 0, 0, null);
            Assert.IsTrue(first.HasValue && second.HasValue);
            Assert.AreNotEqual(first.Value.Index, second.Value.Index);
            lot.Release("A");
            var third = lot.Claim("C", zone, 0, 0, null);
            Assert.AreEqual(first.Value.Index, third.Value.Index, "a slot given back is free again");
        }

        [Test]
        public void EverySlotOfEveryZoneIsInsideItsZone()
        {
            foreach (var z in CommandKit.Site.Zones)
            {
                var slots = ParkingLot.Slots(z, z.CentreX, z.CentreZ);
                Assert.IsNotEmpty(slots, z.Name);
                foreach (var s in slots) Assert.IsTrue(z.Contains(s.X, s.Z), $"{z.Name} slot {s.Index}");
            }
        }

        [Test]
        public void AFullZoneRefusesTheCommandWithAReason()
        {
            var r = new Rig();
            var zone = CommandKit.Site.Zones.First(z => z.Name == Fill);
            for (var i = 0; i < 40; i++) r.Parking.Claim("other-" + i, zone, 0, 0, null, ParkingLot.Margin(r.Body.Kind), CommandKit.Site.Obstacles, ParkingLot.FootprintRadius(r.Body.Kind));
            var cmd = r.Send("goto-area", Fill);
            Assert.IsTrue(cmd.IsComplete);
            Assert.IsFalse(cmd.Completion.Answer().Succeeded);
            Assert.AreEqual("no free parking slot in sp-zone-fill", cmd.Completion.Answer().Reason);
            Assert.IsTrue(r.Body.Attached, "a refused command does not take the machine off its track");
        }

        [Test]
        public void AnAreaWithNoSceneGeometryIsRefused()
        {
            var r = new Rig();
            var cmd = r.Send("goto-area", "sp-zone-moon");
            Assert.AreEqual("no scene geometry for area sp-zone-moon", cmd.Completion.Answer().Reason);
            Assert.IsTrue(r.Body.Attached);
        }

        // ---- the route network

        [Test]
        public void TheRoadNetworkIsOnePiece()
        {
            Assert.AreEqual(1, CommandKit.Graph.Components(), "every road, spot and pad is reachable from every other");
        }

        [Test]
        public void EveryZoneAndTheRefuelBayAreReachableFromEveryTrack()
        {
            var goals = new List<(string, double, double)>();
            foreach (var z in CommandKit.Site.Zones)
                foreach (var s in ParkingLot.Slots(z, z.CentreX, z.CentreZ)) goals.Add(($"{z.Name}#{s.Index}", s.X, s.Z));
            foreach (var spot in new[] { RouteGraph.QueueSpot, RouteGraph.BaySpot })
            {
                Assert.IsTrue(CommandKit.Site.Spots.TryGetValue(spot, out var sp), spot);
                goals.Add((spot, sp.X, sp.Z));
            }

            Assert.AreEqual(13, CommandKit.TrackCount);
            for (var t = 0; t < CommandKit.TrackCount; t++)
            {
                var frames = CommandKit.Track(t);
                foreach (var f in new[] { frames[0], frames[frames.Count / 2] })
                    foreach (var (name, x, z) in goals)
                    {
                        var route = CommandKit.Graph.Plan(f.X, f.Z, x, z);
                        Assert.IsNotNull(route, $"track {t} at ({f.X:0},{f.Z:0}) to {name}");
                        Assert.Greater(route.Length, 0.0);
                    }
            }
        }

        [Test]
        public void ZoneRectsAreReadAsWestEastSouthNorth()
        {
            Assert.IsTrue(CommandKit.Site.TryZone(Yard, out var yard));
            Assert.AreEqual(-108.0, yard.X0);
            Assert.AreEqual(-46.0, yard.X1);
            Assert.AreEqual(-76.0, yard.Z0);
            Assert.AreEqual(-14.0, yard.Z1);
            Assert.IsTrue(yard.Contains(-70, -40));
            Assert.IsFalse(yard.Contains(-30, -40), "east of the yard is not the yard");
        }

        [Test]
        public void ARouteIsFollowedAtTheSpeedsOfItsSurfaceAndSlope()
        {
            Assert.Less(SpeedModel.GradeFactor(10.0), 0.7, "a 10% climb slows a truck a good deal");
            Assert.AreEqual(1.0, SpeedModel.GradeFactor(0.0));
            Assert.Less(SpeedModel.GradeFactor(-5.0), 1.0, "and it eases off going down");
            var r = new Rig();
            var cmd = r.Send("goto-area", Yard);
            var peak = 0.0;
            var steps = 0;
            r.Run(() => { peak = Math.Max(peak, r.Controller.SpeedMps); steps++; return cmd.IsComplete; });
            Assert.LessOrEqual(peak, SpeedModel.HaulerCruise + 1e-9, "never faster than a truck's cruise");
            Assert.Greater(peak, 2.0, "and it does get up to speed");
        }

        // ---- the yield rule

        [Test]
        public void AMachineYieldsToOneAheadInItsLaneButNotToOneBehindOrBeside()
        {
            var site = CommandKit.Site;
            var me = new FakeBody("A", EquipmentKind.Hauler, 0, 0, 0);          // facing north
            var ahead = new FakeBody("B", EquipmentKind.Hauler, 0, 5, 0);
            var behind = new FakeBody("C", EquipmentKind.Hauler, 0, -5, 0);
            var beside = new FakeBody("D", EquipmentKind.Hauler, 6, 5, 0);
            var far = new FakeBody("E", EquipmentKind.Hauler, 0, 12, 0);
            Func<FakeBody[], TaskDirector> make = others => new TaskDirector(site, CommandKit.Graph, new Timeline(),
                new[] { me }.Concat(others).Select(b => ((IMachineBody)b, new MachineModel(EquipmentKind.Hauler, b.Id))), 1);
            Assert.IsTrue(make(new[] { ahead }).Blocked("A", 0, 0, 0), "5 m ahead in the lane");
            Assert.IsFalse(make(new[] { behind }).Blocked("A", 0, 0, 0), "behind is not in the way");
            Assert.IsFalse(make(new[] { beside }).Blocked("A", 0, 0, 0), "another lane");
            Assert.IsFalse(make(new[] { far }).Blocked("A", 0, 0, 0), "12 m is beyond the yield distance");
            Assert.IsTrue(make(new[] { ahead }).Blocked("A", 0, 0, 20), "slightly off the line is still the lane");
            Assert.IsFalse(make(new[] { ahead }).Blocked("A", 0, 0, 180), "facing away");
        }

        // ---- budgets

        [Test]
        public void ACommandThatCannotMakeProgressEndsFailedWhenItsBudgetRunsOut()
        {
            var r = new Rig();
            r.World.Block = true;          // something sits in its way for good
            var cmd = r.Send("goto-area", Yard);
            Assert.IsTrue(r.Run(() => cmd.IsComplete, 3000, 1.0), "every command gets a terminal answer");
            var res = cmd.Completion.Answer();
            Assert.IsFalse(res.Succeeded);
            StringAssert.StartsWith("budget exceeded", res.Reason);
            StringAssert.Contains("driving to sp-zone-yard", res.Reason);
            Assert.AreEqual(MachineMode.Parked, r.Controller.Mode);
            Assert.IsFalse(r.Parking.Holds(r.Id), "and its parking claim is given back");
        }

        [Test]
        public void TheWallClockCapEndsACommandEvenWhenSimulationTimeStandsStill()
        {
            var r = new Rig();
            var cmd = r.Send("goto-area", Yard);
            for (var i = 0; i < 700 && !cmd.IsComplete; i++) r.Controller.Step(0.0, 1.0);   // a paused simulation, a running clock
            Assert.IsTrue(cmd.IsComplete);
            StringAssert.Contains("wall-clock cap", cmd.Completion.Answer().Reason);
        }

        [Test]
        public void ARefuelWhoseBayNeverFreesGivesUpAfterTheBayWait()
        {
            var r = new Rig();
            r.World.BayBusy = true;
            var cmd = r.Send("goto-refuel");
            Assert.IsTrue(r.Run(() => cmd.IsComplete, 3000));
            var res = cmd.Completion.Answer();
            Assert.IsFalse(res.Succeeded);
            Assert.AreEqual("refuel bay wait exceeded 120 s", res.Reason);
            Assert.IsNull(r.Bay.Holder);
            Assert.AreEqual(0, r.Bay.Waiting, "it left the line");
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode, "and the truck drives out of the queue rather than parking in it");
        }

        [Test]
        public void ARunningOutOfFuelOnTheWayFailsTheCommandAndStallsTheMachine()
        {
            var r = new Rig();
            r.Model.Restore(0.004, 1000);
            var cmd = r.Send("goto-area", Yard);
            Assert.IsFalse(cmd.IsComplete, "it starts: there is a little fuel");
            Assert.IsTrue(r.Run(() => cmd.IsComplete, 600));
            Assert.AreEqual("out of fuel", cmd.Completion.Answer().Reason);
            Assert.AreEqual(MachineMode.Stalled, r.Controller.Mode);
            var at = (r.Body.X, r.Body.Z);
            r.Run(() => false, 20);
            Assert.AreEqual(at, (r.Body.X, r.Body.Z), "a dry machine stands where it ran out");
        }

        [Test]
        public void ACommandToAnEmptyMachineIsRefusedAtOnce()
        {
            var r = new Rig();
            r.Model.Restore(0.0, 1000);
            var cmd = r.Send("goto-refuel");
            Assert.AreEqual("out of fuel", cmd.Completion.Answer().Reason);
        }

        [Test]
        public void AWorkingMachineThatRunsDryStopsRatherThanKeepOnWorking()
        {
            var r = new Rig();
            r.Model.Restore(0.003, 1000);
            Assert.IsTrue(r.Run(() => r.Controller.Mode == MachineMode.Stalled, 600));
            Assert.IsFalse(r.Body.Attached, "it is off its track and standing");
            StringAssert.Contains("out of fuel", Rows(r));
        }

        // ---- refuelling

        [Test]
        public void GotoRefuelQueuesEntersTheBayRefuelsOverTheServiceTimeAndSucceeds()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var cmd = r.Send("goto-refuel", token: "c-refuel");
            Assert.IsTrue(r.Run(() => cmd.IsComplete), Rows(r));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded, Rows(r));
            Assert.GreaterOrEqual(r.Model.FuelPct, 94.9, "the tank is full when service ends");
            var rows = Rows(r);
            StringAssert.Contains("refuelling: service started", rows);
            StringAssert.Contains("refuelling: service finished", rows);
            Assert.IsNull(r.Bay.Holder, "and the bay is free again");
        }

        [Test]
        public void FuelRisesOnlyWhileTheMachineIsInTheRefuellingState()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var cmd = r.Send("goto-refuel");
            var risesOutside = 0;
            var risesInside = 0;
            var phaseAtStart = r.Controller.Phase;
            var last = r.Model.FuelPct;
            var phaseSeconds = 0.0;
            var ok = r.Run(() =>
            {
                var now = r.Model.FuelPct;
                if (now > last + 1e-9)
                {
                    if (phaseAtStart == TaskPhase.Refuelling) risesInside++;
                    else risesOutside++;
                }

                if (phaseAtStart == TaskPhase.Refuelling) phaseSeconds += 0.25;
                last = now;
                phaseAtStart = r.Controller.Phase;
                return r.Controller.Mode == MachineMode.Working;
            });
            Assert.IsTrue(ok, Rows(r));
            Assert.AreEqual(0, risesOutside, "no step outside the Refuelling state ever raised the tank");
            Assert.Greater(risesInside, 100, "fuel climbs through the service, not at its end");
            Assert.AreEqual(RefuelService.DurationSeconds, phaseSeconds, 1.0);
            Assert.IsTrue(cmd.Completion.Answer().Succeeded);
        }

        [Test]
        public void OnlyTheRefuellingServiceCanMakeOrUseARefuelPermit()
        {
            // The structural half of "fuel rises only in Refuelling". The compiler: the permit is abstract with no
            // public constructor, so nothing can `new` one, and the one class that derives from it is a private
            // type nested in the service.
            Assert.IsTrue(typeof(RefuelPermit).IsAbstract);
            Assert.IsEmpty(typeof(RefuelPermit).GetConstructors(BindingFlags.Public | BindingFlags.Instance));
            var heirs = typeof(RefuelPermit).Assembly.GetTypes().Where(t => t != typeof(RefuelPermit) && typeof(RefuelPermit).IsAssignableFrom(t)).ToList();
            Assert.AreEqual(1, heirs.Count, string.Join(", ", heirs.Select(h => h.FullName)));
            Assert.IsTrue(heirs[0].IsNestedPrivate, "the heir is private to its service");
            Assert.AreEqual("RefuelService", heirs[0].DeclaringType.Name);

            // The source: nothing but the model and the service so much as names the permit or calls the model's refill
            // (a target-typed `model.Refill(new(...))` has no `new RefuelPermit` in it).
            var scripts = Path.Combine(UnityEngine.Application.dataPath, "Sitepulse", "Scripts");
            var strays = new List<string>();
            var inService = new List<string>();
            foreach (var file in Directory.GetFiles(scripts, "*.cs", SearchOption.AllDirectories))
            {
                var name = Path.GetFileName(file);
                var n = 0;
                foreach (var line in File.ReadAllLines(file))
                {
                    n++;
                    var code = line.TrimStart();
                    if (code.StartsWith("//") || code.StartsWith("*") || code.StartsWith("/*")) continue;
                    var mentions = code.Contains("RefuelPermit") || code.Contains("Refill(");
                    if (!mentions) continue;
                    if (name == "RefuelService.cs") inService.Add(code);
                    else if (name != "MachineModel.cs") strays.Add(name + ":" + n + " " + code);
                }
            }

            Assert.IsEmpty(strays, "a second place that names the permit or refills a tank is a second way to refuel");
            Assert.AreEqual(1, inService.Count(l => l.Contains(".Refill(")), "the service calls the refill once");
            Assert.AreEqual(1, inService.Count(l => l.Contains("new Permit(")), "and makes its permit in one place");
        }

        // counts every rise of a tank, and in which task state the step that raised it started
        sealed class FuelLedger
        {
            readonly Rig rig;
            double last;
            TaskPhase phase;

            public FuelLedger(Rig rig)
            {
                this.rig = rig;
                last = rig.Model.FuelPct;
                phase = rig.Controller.Phase;
            }

            public int Outside, Inside;

            public bool Tick(Func<bool> done = null)
            {
                var now = rig.Model.FuelPct;
                if (now > last + 1e-9)
                {
                    if (phase == TaskPhase.Refuelling) Inside++;
                    else Outside++;
                }

                last = now;
                phase = rig.Controller.Phase;
                return done != null && done();
            }
        }

        [Test]
        public void NoPathThroughTheTaskLayerRaisesAFuelTankOutsideRefuelling()
        {
            var r = new Rig();
            r.Model.Restore(60.0, 1000);
            var ledger = new FuelLedger(r);
            Func<Func<bool>, double, bool> go = (done, secs) => r.Run(() => ledger.Tick(done), secs);

            // P: the presenter's low-fuel cycle only ever lowers
            PresenterActions.PrepareLowFuel(r.Id, r.Model, r.Timeline);
            Assert.IsTrue(go(() => r.Model.FuelPct < 15.0, 300), "the cycle crosses the line");

            // a command that parks the machine, and G: resume work
            var area = r.Send("goto-area", Yard);
            Assert.IsTrue(go(() => area.IsComplete, 1500));
            Assert.IsNull(r.Controller.Resume());
            Assert.IsTrue(go(() => r.Controller.Mode == MachineMode.Working, 1500));

            // refused commands of each kind, a supersede of one command by another and a refuel interrupted by a command
            r.Send("goto-area", "sp-zone-moon");
            r.Send("self-destruct");
            var refuel = r.Send("goto-refuel");
            Assert.IsTrue(go(() => r.Controller.Phase == TaskPhase.Refuelling, 1500));
            go(() => false, 10);
            var elsewhere = r.Send("goto-area", Fill);
            Assert.IsTrue(refuel.IsComplete && !refuel.Completion.Answer().Succeeded);
            Assert.IsTrue(go(() => elsewhere.IsComplete, 1500));
            Assert.IsNull(r.Controller.Resume());
            Assert.IsTrue(go(() => r.Controller.Mode == MachineMode.Working, 1500));

            // a refuel that gives up on the bay, one that completes, one cut off by the end of the run
            r.World.BayBusy = true;
            var waits = r.Send("goto-refuel");
            Assert.IsTrue(go(() => waits.IsComplete, 3000));
            Assert.IsTrue(go(() => r.Controller.Mode == MachineMode.Working, 1500));
            r.World.BayBusy = false;
            var served = r.Send("goto-refuel");
            Assert.IsTrue(go(() => served.IsComplete, 3000));
            Assert.IsTrue(served.Completion.Answer().Succeeded);
            Assert.IsTrue(go(() => r.Controller.Mode == MachineMode.Working, 1500));
            var cut = r.Send("goto-refuel");
            Assert.IsTrue(go(() => r.Controller.Phase == TaskPhase.Refuelling, 1500));
            go(() => false, 5);
            Assert.AreEqual(1, r.Controller.FailAll(TaskReasons.Reset));
            go(() => false, 30);

            Assert.AreEqual(0, ledger.Outside, "no step outside the Refuelling state ever raised a tank: " + Rows(r));
            Assert.Greater(ledger.Inside, 100, "and the service itself did");
        }

        [Test]
        public void ASupersedeDuringServiceStopsTheFuelAndSaysWhereItStopped()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var old = r.Send("goto-refuel", token: "c-old");
            Assert.IsTrue(r.Run(() => r.Controller.Phase == TaskPhase.Refuelling));
            r.Run(() => false, 10);
            var at = r.Model.FuelPct;
            Assert.Greater(at, 20.0, "the service is well under way");
            var ledger = new FuelLedger(r);

            var area = r.Send("goto-area", Yard, "c-area");
            Assert.IsTrue(old.IsComplete);
            Assert.IsFalse(old.Completion.Answer().Succeeded);
            Assert.AreEqual("superseded by c-area", old.Completion.Answer().Reason);
            StringAssert.Contains("refuelling: service stopped at " + at.ToString("0.0", System.Globalization.CultureInfo.InvariantCulture) + "% fuel", Rows(r));
            Assert.IsNull(r.Bay.Holder);

            Assert.IsTrue(r.Run(() => ledger.Tick(() => area.IsComplete)));
            Assert.IsTrue(area.Completion.Answer().Succeeded);
            Assert.AreEqual(0, ledger.Outside);
            Assert.AreEqual(0, ledger.Inside, "not one more rise after the supersede");
            Assert.LessOrEqual(r.Model.FuelPct, at + 1e-9);
        }

        [Test]
        public void TwoMachinesShareOneBayInTheOrderTheyReachedTheQueue()
        {
            var site = CommandKit.Site;
            var track = CommandKit.Track(0);
            var a = new FakeBody("A", EquipmentKind.Hauler, track[540].X, track[540].Z, track[540].HeadingDegrees, track);
            var b = new FakeBody("B", EquipmentKind.Hauler, track[600].X, track[600].Z, track[600].HeadingDegrees, track);
            var ma = new MachineModel(EquipmentKind.Hauler, "A");
            var mb = new MachineModel(EquipmentKind.Hauler, "B");
            ma.Restore(20, 1000);
            mb.Restore(20, 1000);
            var tl = new Timeline();
            var d = new TaskDirector(site, CommandKit.Graph, tl, new[] { ((IMachineBody)a, ma), (b, mb) }, 1);
            var ra = new TaskRequest("c-a", "goto-refuel", null, 1, 1);
            var rb = new TaskRequest("c-b", "goto-refuel", null, 1, 1);
            d.Submit("A", ra);
            d.Submit("B", rb);
            var order = new List<string>();
            for (var t = 0.0; t < 1500 && !(ra.IsComplete && rb.IsComplete); t += 0.25)
            {
                ma.Step(0.25, new MachineInput(0, false));
                mb.Step(0.25, new MachineInput(0, false));
                d.Step(0.25, 0.25);
                if (d.Bay.Holder != null && (order.Count == 0 || order[order.Count - 1] != d.Bay.Holder)) order.Add(d.Bay.Holder);
            }

            Assert.IsTrue(ra.Completion.Answer().Succeeded, "A: " + ra.Completion.Answer());
            Assert.IsTrue(rb.Completion.Answer().Succeeded, "B: " + rb.Completion.Answer());
            Assert.AreEqual(2, order.Count);
            Assert.AreNotEqual(order[0], order[1], "one at a time");
        }

        [Test]
        public void TheBayIsFirstComeFirstServedAndAnAskerKeepsItsPlace()
        {
            var bay = new BayReservations();
            bay.Request("A");
            bay.Request("B");
            bay.Request("A");
            Assert.AreEqual(2, bay.Waiting);
            Assert.IsFalse(bay.TryGrant("B", true), "not B's turn");
            Assert.IsFalse(bay.TryGrant("A", false), "a bay with something in it is not free");
            Assert.IsTrue(bay.TryGrant("A", true));
            Assert.IsFalse(bay.TryGrant("B", true), "the bay is held");
            bay.Release("A");
            Assert.IsTrue(bay.TryGrant("B", true));
            bay.Release("B");
            Assert.IsNull(bay.Holder);
        }

        // ---- back to work

        [Test]
        public void AfterRefuellingTheMachineDrivesBackToItsTrackAndIsReAttachedThere()
        {
            var r = new Rig();
            r.Model.Restore(14.0, 1000);
            var cmd = r.Send("goto-refuel");
            Assert.IsTrue(r.Run(() => cmd.IsComplete), Rows(r));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded);
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode, "success is answered first; then it goes back to work");
            Assert.IsFalse(r.Body.Attached);
            Assert.IsTrue(r.Run(() => r.Controller.Mode == MachineMode.Working), Rows(r));
            Assert.IsTrue(r.Body.Attached);
            Assert.AreEqual(1, r.Body.Attaches);
            Assert.IsTrue(r.Body.LastAttach.HasValue);
            var track = CommandKit.Track(0);
            Assert.IsTrue(track.Any(p => p.TrackSeconds == r.Body.LastAttach.Value.TrackSeconds), "it joined its own track, at a point of it");
            StringAssert.Contains("working: back on its track", Rows(r));
        }

        [Test]
        public void ResumeWorkSendsAParkedMachineBackAndRefusesTheRest()
        {
            var r = new Rig();
            var cmd = r.Send("goto-area", Yard);
            Assert.IsTrue(r.Run(() => cmd.IsComplete));
            Assert.IsNull(r.Controller.Resume());
            Assert.AreEqual(MachineMode.Returning, r.Controller.Mode);
            Assert.IsFalse(r.Parking.Holds(r.Id), "it left its slot");
            Assert.IsNotNull(r.Controller.Resume(), "already on its way back");
            Assert.IsTrue(r.Run(() => r.Controller.Mode == MachineMode.Working));
            StringAssert.Contains("already on its track", r.Controller.Resume());

            var busy = new Rig();
            busy.Send("goto-area", Yard);
            StringAssert.Contains("refused: running goto-area", busy.Controller.Resume());
        }

        // ---- reset

        [Test]
        public void AResetFailsTheRunningTaskAndEveryLaterCommand()
        {
            var r = new Rig();
            var cmd = r.Send("goto-refuel");
            r.Run(() => false, 20);
            Assert.IsFalse(cmd.IsComplete);
            r.Controller.FailAll(TaskReasons.Reset);
            Assert.IsTrue(cmd.IsComplete);
            Assert.AreEqual("simulation reset before completion", cmd.Completion.Answer().Reason);
            Assert.IsNull(r.Bay.Holder);
            var late = r.Send("goto-area", Yard);
            Assert.AreEqual("simulation reset before completion", late.Completion.Answer().Reason, "nothing new runs after a reset");
        }

        [Test]
        public void TheDirectorAnswersEveryRunningTaskOnResetAndRefusesWhatComesAfter()
        {
            var site = CommandKit.Site;
            var track = CommandKit.Track(0);
            var bodies = Enumerable.Range(0, 3).Select(i => (IMachineBody)new FakeBody("M" + i, EquipmentKind.Hauler, track[100 + i * 50].X, track[100 + i * 50].Z, 0, track)).ToList();
            var d = new TaskDirector(site, CommandKit.Graph, new Timeline(), bodies.Select(b => (b, new MachineModel(EquipmentKind.Hauler, b.Id))), 1);
            var reqs = bodies.Select((b, i) => new TaskRequest("c" + i, i == 1 ? "goto-refuel" : "goto-area", i == 1 ? null : Yard, 1, 1)).ToList();
            for (var i = 0; i < reqs.Count; i++) d.Submit("M" + i, reqs[i]);
            d.Step(1, 1);
            d.FailAll();
            foreach (var q in reqs)
            {
                Assert.IsTrue(q.IsComplete, q.Token);
                Assert.AreEqual("simulation reset before completion", q.Completion.Answer().Reason);
            }

            var after = new TaskRequest("late", "goto-area", Yard, 9, 1);
            d.Submit("M0", after);
            Assert.AreEqual("simulation reset before completion", after.Completion.Answer().Reason);
        }

        [Test]
        public void ACommandForADeviceWithNoControllerAnswersFailedAtOnce()
        {
            var d = new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(), new (IMachineBody, MachineModel)[0], 1);
            var q = new TaskRequest("c", "goto-refuel", null, 1, 1);
            d.Submit("SP-PL-0001", q);
            Assert.AreEqual("this device has no task executor in this run", q.Completion.Answer().Reason);
            var old = new TaskRequest("old", "goto-refuel", null, 1, 7);
            d.Submit("SP-PL-0001", old);
            Assert.AreEqual("simulation reset before completion", old.Completion.Answer().Reason, "a command from another run is a reset");
        }

        [Test]
        public void ARequestIsAnsweredOnceAndItsContinuationNeverRunsInline()
        {
            var q = new TaskRequest("c", "goto-refuel", null, 1, 1);
            var thread = -1;
            var done = new System.Threading.ManualResetEventSlim();
            q.Completion.ContinueWith(_ =>
            {
                thread = System.Threading.Thread.CurrentThread.ManagedThreadId;
                done.Set();
            });
            var mine = System.Threading.Thread.CurrentThread.ManagedThreadId;
            Assert.IsTrue(q.Complete(TaskResult.Ok()));
            Assert.IsFalse(q.Complete(TaskResult.Fail("again")), "the first answer stands");
            Assert.IsTrue(done.Wait(2000));
            Assert.IsTrue(q.Completion.Answer().Succeeded);
            Assert.AreNotEqual(mine, thread, "completing from the main thread must not run the SDK's continuation on it");
        }

        // ---- presenter inputs

        [Test]
        public void PrepareLowFuelCanLowerTheTankAndNeverRaiseIt()
        {
            var high = new MachineModel(EquipmentKind.Hauler, "A");
            high.Restore(80, 1000);
            Assert.IsTrue(high.PrepareLowFuel(15.5, 60));
            Assert.AreEqual(15.5, high.FuelPct, 1e-9, "lowered to just above the line");

            var middling = new MachineModel(EquipmentKind.Hauler, "B");
            middling.Restore(15.2, 1000);
            Assert.IsTrue(middling.PrepareLowFuel(15.5, 60));
            Assert.AreEqual(15.2, middling.FuelPct, 1e-9, "a tank already between the line and the target is left alone: never raised");

            var low = new MachineModel(EquipmentKind.Hauler, "C");
            low.Restore(9.0, 1000);
            Assert.IsFalse(low.PrepareLowFuel(15.5, 60), "already below the line: nothing to prepare");
            Assert.AreEqual(9.0, low.FuelPct, 1e-9);

            Assert.Throws<InvalidOperationException>(() => new MachineModel(EquipmentKind.Plant, "P").PrepareLowFuel(15.5, 60));
        }

        [Test]
        public void ThePreparedCycleCrossesTheLineInAboutAMinuteThroughTheNormalDrain()
        {
            var m = new MachineModel(EquipmentKind.Hauler, "SP-HL-0006");
            m.Restore(70, 1000);
            m.PrepareLowFuel(PresenterActions.JustAbovePct, PresenterActions.CrossWithinSeconds);
            var last = m.FuelPct;
            double crossedAt = -1;
            for (var t = 0.1; t < 300; t += 0.1)
            {
                m.Step(0.1, new MachineInput(4, true));
                Assert.LessOrEqual(m.FuelPct, last + 1e-12, "fuel only ever falls");
                last = m.FuelPct;
                if (crossedAt < 0 && m.FuelPct < PresenterActions.LowFuelLinePct) crossedAt = t;
            }

            Assert.Greater(crossedAt, 40.0);
            Assert.Less(crossedAt, 80.0, "about a minute");
            Assert.AreEqual(0.0, m.DrainBoostPctPerHour, "and the extra burn ends below the line: a normal drain after that");
            Assert.Greater(m.FuelPct, 13.5, "it does not run on to empty");
        }

        [Test]
        public void ThePresenterActionsWriteTimelineRowsAndNeverRaiseFuel()
        {
            var r = new Rig();
            r.Model.Restore(12.0, 1000);
            var said = PresenterActions.PrepareLowFuel(r.Id, r.Model, r.Timeline);
            StringAssert.Contains("nothing to prepare", said);
            Assert.AreEqual(12.0, r.Model.FuelPct, 1e-9);
            StringAssert.Contains("presenter", Rows(r));
        }

        [Test]
        public void TheTimelineKeepsABoundedHistoryPerMachine()
        {
            var t = new Timeline();
            for (var i = 0; i < Timeline.MaxRowsPerMachine + 25; i++) t.Add("A", TimelineKinds.Received, "row " + i);
            Assert.AreEqual(Timeline.MaxRowsPerMachine, t.Rows("A").Count);
            StringAssert.EndsWith("row " + (Timeline.MaxRowsPerMachine + 24), t.Text("A", 1));
            Assert.AreEqual(0, t.Rows("B").Count);
            Assert.AreEqual("A", t.LastCommanded);
        }

        // ---- getting round what stands on the site

        // The yard's workshop, offices, containers and fuel tank: the things a machine must not be driven through. (The roads
        // pass close to cones and barriers by design; only the off-road legs and the parking are held clear of those.)
        static bool IsBuilding(Obstacle o) => o.Name == "workshop" || o.Name == "site_office" || o.Name.StartsWith("container") || o.Name == "fuel_tank";

        static IEnumerable<(double X, double Z)> Sampled(double ax, double az, double bx, double bz)
        {
            var n = Math.Max(1, (int)Math.Ceiling(Math.Sqrt((bx - ax) * (bx - ax) + (bz - az) * (bz - az)) / 0.5));
            for (var i = 0; i <= n; i++) yield return (ax + (bx - ax) * i / n, az + (bz - az) * i / n);
        }

        [Test]
        public void EveryOffRoadLegToEveryZoneSlotKeepsTheMachineClearOfEverythingOnTheSite()
        {
            var obstacles = CommandKit.Site.Obstacles;
            Assert.Greater(obstacles.Count, 0);
            var starts = new List<TrackPoint>();
            for (var t = 0; t < CommandKit.TrackCount; t++)
            {
                var frames = CommandKit.Track(t);
                starts.Add(frames[0]);
                starts.Add(frames[frames.Count / 3]);
                starts.Add(frames[frames.Count * 2 / 3]);
            }

            var reached = 0;
            foreach (var kind in new[] { EquipmentKind.Hauler, EquipmentKind.Loader, EquipmentKind.Dozer })
            {
                var foot = ParkingLot.FootprintRadius(kind);
                var travel = ParkingLot.TravelRadius(kind);
                var clearance = travel + ParkingLot.TravelClearance;
                foreach (var zone in CommandKit.Site.Zones)
                {
                    var slots = ParkingLot.Slots(zone, zone.CentreX, zone.CentreZ, ParkingLot.Margin(kind), obstacles, foot);
                    Assert.Greater(slots.Count, 0, zone.Name);
                    foreach (var f in starts)
                    {
                        var anyReach = false;
                        foreach (var slot in slots.Where((q, ix) => ix % Math.Max(1, slots.Count / 6) == 0))
                        {
                            var route = CommandKit.Graph.Plan(f.X, f.Z, slot.X, slot.Z, obstacles, travel);
                            if (route == null) continue;
                            anyReach = true;
                            reached++;
                            var xs = route.Xs;
                            var zs = route.Zs;
                            for (var leg = 0; leg < route.Legs; leg++)
                            {
                                if (Math.Abs(route.LegFactor(leg) - SpeedModel.OffRoadFactor) > 1e-9) continue;
                                foreach (var (x, z) in Sampled(xs[leg], zs[leg], xs[leg + 1], zs[leg + 1]))
                                    foreach (var o in obstacles)
                                    {
                                        // a leg that starts or ends nearer than the clearance (a road end beside a sign, a machine that starts inside a yard) goes no nearer than that
                                        var need = Math.Min(Math.Min(clearance, Math.Min(o.Distance(xs[leg], zs[leg]), o.Distance(xs[leg + 1], zs[leg + 1]))), Math.Min(o.Distance(f.X, f.Z), o.Distance(slot.X, slot.Z)));
                                        Assert.GreaterOrEqual(o.Distance(x, z), need - 1e-3, $"{kind} from ({f.X:0},{f.Z:0}) to {zone.Name} slot {slot.Index}: leg {leg} at ({x:0.0},{z:0.0}) is within reach of {o.Name}");
                                    }
                            }
                        }

                        Assert.IsTrue(anyReach, $"{kind} from ({f.X:0},{f.Z:0}) has no clear way into {zone.Name} at all");
                    }
                }
            }

            Assert.Greater(reached, 0);
        }

        [Test]
        public void TheStraightLineThroughTheWorkshopIsGoneAround()
        {
            // the first yard slot sits beyond the workshop from the road end at (-44, -30)
            var foot = ParkingLot.TravelRadius(EquipmentKind.Hauler);
            var direct = CommandKit.Graph.Plan(-44, -30, -89.5, -43.5);
            var around = CommandKit.Graph.Plan(-44, -30, -89.5, -43.5, CommandKit.Site.Obstacles, foot);
            Assert.IsNotNull(around);
            Assert.IsNotNull(direct);
            var workshop = CommandKit.Site.Obstacles.First(o => o.Name == "workshop");
            var crossesDirect = false;
            for (var leg = 0; leg < direct.Legs; leg++)
                foreach (var (x, z) in Sampled(direct.Xs[leg], direct.Zs[leg], direct.Xs[leg + 1], direct.Zs[leg + 1]))
                    if (workshop.Distance(x, z) < 0) crossesDirect = true;
            Assert.IsTrue(crossesDirect, "premise: the plain route does cut through the shed");
            for (var leg = 0; leg < around.Legs; leg++)
                foreach (var (x, z) in Sampled(around.Xs[leg], around.Zs[leg], around.Xs[leg + 1], around.Zs[leg + 1]))
                    Assert.GreaterOrEqual(workshop.Distance(x, z), 0, $"at ({x:0.0},{z:0.0})");
        }

        [Test]
        public void TheYardIsReachedWithinItsBudgetFromWhereverASouthboundTruckStartsAndNeverThroughABuilding()
        {
            var buildings = CommandKit.Site.Obstacles.Where(IsBuilding).ToList();
            Assert.Greater(buildings.Count, 0);
            var frames = CommandKit.Track(0);
            var checkedStarts = 0;
            for (var i = 0; i < frames.Count; i += Math.Max(1, frames.Count / 10))
            {
                var f = frames[i];
                var r = new Rig("SP-HL-0003", x: f.X, z: f.Z);
                var cmd = r.Send("goto-area", Yard);
                var through = "";
                var done = r.Run(() =>
                {
                    foreach (var b in buildings)
                        if (b.Distance(r.Body.X, r.Body.Z) < 0 && through == "") through = $"{b.Name} at ({r.Body.X:0.0},{r.Body.Z:0.0})";
                    return cmd.IsComplete;
                });
                Assert.IsTrue(done, $"frame {i}: " + Rows(r));
                Assert.IsTrue(cmd.Completion.Answer().Succeeded, $"frame {i}: " + Rows(r));
                Assert.IsTrue(InZone(r, Yard), $"frame {i}");
                Assert.AreEqual("", through, $"frame {i}: drove into a building");
                checkedStarts++;
            }

            Assert.GreaterOrEqual(checkedStarts, 10);
        }

        // ---- the yield rule, and what it must never do

        static TaskDirector YieldWorld(FakeBody[] bodies) => new TaskDirector(CommandKit.Site, CommandKit.Graph, new Timeline(),
            bodies.Select(b => ((IMachineBody)b, new MachineModel(EquipmentKind.Hauler, b.Id))), 1);

        [Test]
        public void AMachineYieldsToOneThatIsMovingInItsLaneForAsLongAsItMoves()
        {
            var me = new FakeBody("A", EquipmentKind.Hauler, 0, 0, 0);
            var other = new FakeBody("B", EquipmentKind.Hauler, 0, 5, 0);
            other.Detach();
            var d = YieldWorld(new[] { me, other });
            for (var i = 0; i < 120; i++)
            {
                other.Drive(0, i % 2 == 0 ? 5.0 : 6.5, 0, 1.5, 0);   // 3 m/s back and forth, always inside the lane ahead
                d.Step(0.5, 0.5);
                Assert.IsTrue(d.Blocked("A", 0, 0, 0), "step " + i);
            }
        }

        [Test]
        public void AMachineWaitsBehindOneThatStandsOnlyAWhileAndThenCarriesOn()
        {
            var me = new FakeBody("A", EquipmentKind.Hauler, 0, 0, 0);
            var other = new FakeBody("B", EquipmentKind.Hauler, 0, 5, 0);
            var d = YieldWorld(new[] { me, other });
            var waited = 0.0;
            var stuck = true;
            for (var t = 0.0; t < 60.0; t += 0.5)
            {
                d.Step(0.5, 0.5);
                if (!d.Blocked("A", 0, 0, 0)) { stuck = false; break; }
                waited = t;
            }

            Assert.IsFalse(stuck, "never deadlocked behind a machine that is not moving");
            Assert.GreaterOrEqual(waited, TaskDirector.StandingYieldSeconds - 1.5, "it did give way first");
            Assert.LessOrEqual(waited, TaskDirector.StandingYieldSeconds + 1.5);
            for (var i = 0; i < 20; i++)
            {
                d.Step(0.5, 0.5);
                Assert.IsFalse(d.Blocked("A", 0, 0, 0), "and once it is past its wait it keeps going");
            }

            d.Step(0.5, 0.5);
            Assert.IsFalse(d.Blocked("A", 0, 0, 180), "the wait starts over for the next one");
        }

        [Test]
        public void ATruckDrivingToTheYardPastAMachineStandingOnItsRouteStillArrives()
        {
            var frames = CommandKit.Track(0);
            var f = frames[300];
            var a = new FakeBody("SP-HL-0003", EquipmentKind.Hauler, f.X, f.Z, f.HeadingDegrees, frames);
            var plan = CommandKit.Graph.Plan(f.X, f.Z, -90, -45, CommandKit.Site.Obstacles, ParkingLot.TravelRadius(EquipmentKind.Hauler));
            Assert.IsNotNull(plan);
            plan.PointAt(plan.Length * 0.3, out var px, out var pz, out _, out _);
            var blocker = new FakeBody("SP-HL-0004", EquipmentKind.Hauler, px, pz, 0);
            var d = YieldWorld(new[] { a, blocker });
            var cmd = new TaskRequest("c-1", "goto-area", Yard, 1, 1);
            d.Submit("SP-HL-0003", cmd);
            for (var t = 0.0; t < 1500 && !cmd.IsComplete; t += 0.25) d.Step(0.25, 0.25);
            Assert.IsTrue(cmd.IsComplete, d.Timeline.Text("SP-HL-0003", 20));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded, d.Timeline.Text("SP-HL-0003", 20));
        }

        // ---- reading the feature file

        [Test]
        public void AFeatureFileWithoutRoadsOrZonesIsRefusedLoudly()
        {
            Assert.Throws<FormatException>(() => SiteGeometryReader.Parse("{\"roads\": [], \"zones\": [], \"spots\": {}}"));
            Assert.Throws<FormatException>(() => SiteGeometryReader.Parse("{\"zones\": []}"));
            Assert.Throws<FormatException>(() => SiteGeometryReader.Parse(""));
        }
    }
}
