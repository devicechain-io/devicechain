// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // Machines never occupy overlapping footprints, whoever is driving them: a machine on its routine track slows or stops behind
    // what is ahead of it, a machine on an errand stops for and goes round what is in its way, and the whole site of 18 machines keeps
    // clear of itself over minutes of work.
    public sealed class FootprintTests
    {
        [Test]
        public void TwoHaulTrucksSideBySideAtTheSpacingOfTwoLanesOverlapByTheWidthTheyShare()
        {
            // the take's tailgating: 5 m between two trucks 5.7 m wide
            var a = Footprint.At(EquipmentKind.Hauler, 0, 0, 90);
            var b = Footprint.At(EquipmentKind.Hauler, 0, 5, 90);
            Assert.AreEqual(-0.7, Footprint.Gap(a, b), 0.01);
            Assert.AreEqual(2.3, Footprint.Gap(a, Footprint.At(EquipmentKind.Hauler, 0, 8, 90)), 0.01);
        }

        [Test]
        public void AHaulTruckIsAWideDeckAndANarrowerBodyNotOneBox()
        {
            // heading 90 is +X: the deck is the forward 4.5 m (x 1.2 to 5.7, 2.85 each side), the body and rear tyres behind it (2.25 each side)
            var truck = Footprint.At(EquipmentKind.Hauler, 0, 0, 90);
            var besideTheBody = Footprint.At(EquipmentKind.Dozer, -4, 4.1, 90);   // 1.75 half width: clear of the body's 2.25 by 0.1
            var besideTheDeck = Footprint.At(EquipmentKind.Dozer, 3, 4.1, 90);
            Assert.AreEqual(0.1, Footprint.Gap(truck, besideTheBody), 0.01);
            Assert.Less(Footprint.Gap(truck, besideTheDeck), 0.0);
        }

        [Test]
        public void ALoaderWithItsBoomRaisedStopsAtItsFrontTyresAndOtherwiseCarriesItsBucketOut()
        {
            var truck = Footprint.At(EquipmentKind.Hauler, 0, 8, 180);   // facing the loader, deck 5.7 m from its middle
            var up = Footprint.At(EquipmentKind.Loader, 0, 0, 0, boomRaised: true);
            var down = Footprint.At(EquipmentKind.Loader, 0, 0, 0, boomRaised: false);
            Assert.AreEqual(8 - 5.7 - Footprint.LoaderRaisedFront, Footprint.Gap(up, truck), 0.01);
            Assert.AreEqual(8 - 5.7 - 5.2, Footprint.Gap(down, truck), 0.01);
        }
    }

    public sealed class TrafficTests
    {
        const double Dt = 0.1;

        sealed class Scene
        {
            public readonly TaskDirector Director;
            public readonly List<TrackBody> Tracks = new List<TrackBody>();
            public readonly List<IMachineBody> Bodies = new List<IMachineBody>();
            public readonly Timeline Timeline;
            public double Now;

            public Scene(params IMachineBody[] bodies)
            {
                Timeline = new Timeline(() => DateTimeOffset.UnixEpoch.AddSeconds(Now));
                foreach (var b in bodies)
                {
                    Bodies.Add(b);
                    if (b is TrackBody t) Tracks.Add(t);
                }

                Director = new TaskDirector(CommandKit.Site, CommandKit.Graph, Timeline,
                    bodies.Select(b => (b, new MachineModel(b.Kind, b.Id))), 1);
            }

            public void Step()
            {
                Director.Step(Dt, Dt);
                foreach (var t in Tracks) t.Advance(Dt);
                Now += Dt;
            }

            public double Gap(IMachineBody a, IMachineBody b) => Footprint.Gap(
                Footprint.At(a.Kind, a.X, a.Z, a.HeadingDegrees, a.BoomRaised), Footprint.At(b.Kind, b.X, b.Z, b.HeadingDegrees, b.BoomRaised));
        }

        // a hauler on a line along +X: the track is 'length' metres at 'speed', and its machine starts 'start' metres along it
        static TrackBody HaulerOnALine(string id, double fromX, double speed, double length = 900.0, double start = 0.0, double z = 0.0)
        {
            var frames = StraightTrack.Frames(fromX, z, 90, speed, length);
            return new TrackBody(id, EquipmentKind.Hauler, frames, StraightTrack.FrameSeconds, StraightTrack.PeriodOf(frames), start / speed);
        }

        static FakeBody Standing(string id, double x, double z, double heading = 90, EquipmentKind kind = EquipmentKind.Hauler)
        {
            var b = new FakeBody(id, kind, x, z, heading);
            b.Detach();
            return b;
        }

        // ---- a machine on its routine track follows it blind: the director looks for it

        [Test]
        public void AHaulerOnItsTrackStopsShortOfAMachineStandingAheadOnTheSameRoad()
        {
            var hauler = HaulerOnALine("SP-HL-0001", 0, 6.0);
            var standing = Standing("SP-HL-0006", 250, 0);
            var s = new Scene(hauler, standing);
            var nearest = double.MaxValue;
            for (var i = 0; i < 900; i++)
            {
                s.Step();
                nearest = Math.Min(nearest, s.Gap(hauler, standing));
            }

            Assert.Greater(nearest, 1.0, "it kept clear of it: nearest " + nearest.ToString("0.00") + " m of air");
            Assert.Less(hauler.X, 250 - 10.3, "it stopped on its own side of it");
            Assert.AreEqual(0.0, s.Director.TrackRateOf(hauler.Id), 1e-6, "and stands there, held");
        }

        [Test]
        public void AHaulerOnItsTrackFollowsAMachineMovingSlowerOnTheSameRoadAtADistance()
        {
            var hauler = HaulerOnALine("SP-HL-0001", 0, 6.0);
            var ahead = Standing("SP-HL-0006", 70, 0);
            var s = new Scene(hauler, ahead);
            var nearest = double.MaxValue;
            for (var i = 0; i < 1200; i++)
            {
                ahead.Drive(ahead.X + 2.0 * Dt, 0, 90, 2.0 * Dt, 0);   // 2 m/s along the road, a quarter of the pace of the track
                s.Step();
                nearest = Math.Min(nearest, s.Gap(hauler, ahead));
            }

            Assert.Greater(nearest, 1.0, "nearest " + nearest.ToString("0.00") + " m");
            Assert.Less(hauler.X, ahead.X, "it never got past it");
            Assert.Less(s.Director.SpeedOf(hauler.Id), 3.0, "and by the end it was going at about the pace of the one in front");
        }

        [Test]
        public void HaulersQueueBehindAMachineStandingInTheRoadAndNoneOfThemTouchesAnother()
        {
            var first = HaulerOnALine("SP-HL-0002", 0, 6.0, start: 120);
            var second = HaulerOnALine("SP-HL-0003", 0, 6.0, start: 60);
            var third = HaulerOnALine("SP-HL-0004", 0, 6.0, start: 0);
            var standing = Standing("SP-HL-0006", 300, 0);
            var s = new Scene(first, second, third, standing);
            var all = new IMachineBody[] { first, second, third, standing };
            var nearest = double.MaxValue;
            for (var i = 0; i < 1500; i++)
            {
                s.Step();
                for (var a = 0; a < all.Length; a++)
                    for (var b = a + 1; b < all.Length; b++)
                        nearest = Math.Min(nearest, s.Gap(all[a], all[b]));
            }

            Assert.Greater(nearest, 0.5, "nearest " + nearest.ToString("0.00") + " m");
            Assert.Greater(first.X, second.X, "the order they were in is the order they queue in");
            Assert.Greater(second.X, third.X);
        }

        [Test]
        public void AHaulerGoesOnAtItsOwnPaceWhenNothingIsInItsWay()
        {
            var hauler = HaulerOnALine("SP-HL-0001", 0, 6.0);
            var other = Standing("SP-HL-0006", 300, 40);   // well off its road
            var s = new Scene(hauler, other);
            for (var i = 0; i < 300; i++)
            {
                s.Step();
                Assert.AreEqual(1.0, s.Director.TrackRateOf(hauler.Id), 1e-9, "step " + i);
            }

            Assert.AreEqual(180.0, hauler.X, 0.5, "30 s at 6 m/s");
        }

        // ---- a machine on an errand goes round what is in its way

        [Test]
        public void AMachineIsNotPutBackOnItsTrackWhereItsFootprintWouldOverlapOneStandingBeside()
        {
            var me = Standing("SP-HL-0001", 0, 0, 0);
            var ahead = Standing("SP-HL-0002", 0, 10.5, 180);   // 10.5 m between middles: past the old 8 m radius, but 0.2 m of air between a truck's tail and another's nose
            var s = new Scene(me, ahead);
            s.Step();
            s.Step();
            Assert.IsTrue(s.Director.TrackClear(me.Id, 0, 0), "by its middle alone it looked clear");
            Assert.IsFalse(s.Director.TrackClearFor(me.Id, EquipmentKind.Hauler, 0, 0, 0), "by the machine it is not");
            ahead.Drive(0, 20, 180, 0, 0);
            s.Step();
            s.Step();
            Assert.IsTrue(s.Director.TrackClearFor(me.Id, EquipmentKind.Hauler, 0, 0, 0));
        }

        [Test]
        public void ACommandWaitsWhileTakingTheMachineOffItsTrackWouldPutWhatItStowsIntoAnother()
        {
            var frames = CommandKit.Track(0);
            var f = frames[300];
            var loader = new FakeBody("SP-LD-0003", EquipmentKind.Loader, f.X, f.Z, f.HeadingDegrees, frames);
            var h = f.HeadingDegrees * Math.PI / 180.0;
            // a truck nose to nose with it, 9 m on: clear while the loader's boom is up, not once its bucket is carried out
            var truck = new FakeBody("SP-HL-0004", EquipmentKind.Hauler, f.X + 9 * Math.Sin(h), f.Z + 9 * Math.Cos(h), f.HeadingDegrees + 180);
            var s = new Scene(loader, truck);
            s.Step();
            var cmd = new TaskRequest("c-1", "goto-area", "sp-zone-yard", 1, 1);
            s.Director.Submit(loader.Id, cmd);
            for (var i = 0; i < 100; i++) s.Step();
            Assert.AreEqual(0, loader.Detaches, "it went on working: " + s.Timeline.Text(loader.Id, 10));
            Assert.IsFalse(cmd.IsComplete);

            truck.Detach();
            truck.Drive(f.X + 60 * Math.Sin(h), f.Z + 60 * Math.Cos(h), 0, 0, 0);   // the truck leaves
            s.Step();
            s.Step();
            Assert.AreEqual(1, loader.Detaches, "and was taken off its track once the place was clear: " + s.Timeline.Text(loader.Id, 10));
        }

        [Test]
        public void ACommandThatCannotStartForAMinuteIsAnsweredFailedNotLeftWaiting()
        {
            var frames = CommandKit.Track(0);
            var f = frames[300];
            var loader = new FakeBody("SP-LD-0003", EquipmentKind.Loader, f.X, f.Z, f.HeadingDegrees, frames);
            var h = f.HeadingDegrees * Math.PI / 180.0;
            var truck = new FakeBody("SP-HL-0004", EquipmentKind.Hauler, f.X + 9 * Math.Sin(h), f.Z + 9 * Math.Cos(h), f.HeadingDegrees + 180);
            var s = new Scene(loader, truck);
            s.Step();
            var cmd = new TaskRequest("c-1", "goto-area", "sp-zone-yard", 1, 1);
            s.Director.Submit(loader.Id, cmd);
            for (var i = 0; i < (int)((TaskDirector.DeferSeconds + 5) / Dt); i++) s.Step();
            Assert.IsTrue(cmd.IsComplete);
            Assert.IsFalse(cmd.Completion.Answer().Succeeded);
            Assert.AreEqual(0, loader.Detaches);
        }

        // ---- the whole site

        static string Overlaps(SceneSim sim) => string.Join("; ", sim.Pairs.Values.Where(p => p.MinGap < 0)
            .OrderBy(p => p.MinGap).Select(p => p.A + "/" + p.B + " " + p.MinGap.ToString("0.00") + " m at " + p.At.ToString("0") + " s"));

        static SceneSim Site(double startClock = SceneSim.StartClock) => new SceneSim(CommandKit.AssetPath("Data/quarry_fleet_live.json"), CommandKit.Site, CommandKit.Graph, startClock);

        static void Run(SceneSim sim, double seconds, params (double At, string Id, string Key, string Area)[] script)
        {
            var next = 0;
            for (var t = 0.0; t < seconds; t += Dt)
            {
                while (next < script.Length && sim.Now >= script[next].At)
                {
                    sim.Send(script[next].Id, script[next].Key, script[next].Area);
                    next++;
                }

                sim.Step(Dt);
            }
        }

        [Test]
        public void TheChoreographyIsLeftAloneWhileNothingIsCommanded()
        {
            var sim = Site();
            for (var i = 0; i < 3000; i++)
            {
                sim.Step(Dt);
                foreach (var id in sim.Ids) Assert.AreEqual(1.0, sim.Director.TrackRateOf(id), 1e-9, id + " at " + sim.Now.ToString("0.0") + " s");
            }

            Assert.AreEqual("", Overlaps(sim), "the 18 machines as they were laid out");
        }

        [Test]
        public void NoTwoMachinesOverlapAtAnyMomentOfTheTakesRefuelVisit()
        {
            // SP-HL-0006 is sent to the refuel bay at 221 s of a take, across the haul loop the other five trucks run
            var sim = Site();
            var cmd = (TaskRequest)null;
            for (var t = 0.0; t < 900.0; t += Dt)
            {
                if (cmd == null && sim.Now >= 221.0) cmd = sim.Send("SP-HL-0006", "goto-refuel");
                sim.Step(Dt);
            }

            Assert.AreEqual("", Overlaps(sim));
            Assert.IsTrue(cmd.IsComplete, sim.Timeline.Text("SP-HL-0006", 20));
            Assert.IsTrue(cmd.Completion.Answer().Succeeded, sim.Timeline.Text("SP-HL-0006", 20));
            Assert.AreEqual(MachineMode.Working, sim.Director["SP-HL-0006"].Mode, "and back at work: " + sim.Timeline.Text("SP-HL-0006", 8));
        }

        [Test]
        public void NoTwoMachinesOverlapWhileSeveralAreSentOffAtOnceAndPutBackToWork()
        {
            var sim = Site();
            var script = new[]
            {
                (40.0, "SP-HL-0006", "goto-refuel", (string)null), (60.0, "SP-HL-0002", "goto-area", "sp-zone-yard"),
                (90.0, "SP-DZ-0001", "goto-area", "sp-zone-fill"), (120.0, "SP-HL-0003", "goto-refuel", null),
                (150.0, "SP-LD-0003", "goto-area", "sp-zone-cut"), (200.0, "SP-HL-0001", "goto-area", "sp-zone-fill"),
                (260.0, "SP-HL-0004", "goto-refuel", null), (300.0, "SP-HL-0005", "goto-refuel", null),
            };
            Run(sim, 600.0, script);
            foreach (var id in sim.Ids)
                if (sim.Director[id].Mode == MachineMode.Parked) sim.Director[id].Resume();
            Run(sim, 400.0);
            Assert.AreEqual("", Overlaps(sim));
        }

        [Test]
        public void NoTwoMachinesOverlapWhenAnyOfThemIsSentAnywhereAtAnyTime()
        {
            // deterministic pseudo-random commands: the same ones every run
            foreach (var seed in new[] { 1, 2, 3 })
            {
                var sim = Site();
                var rnd = new Random(seed);
                var zones = new[] { "sp-zone-cut", "sp-zone-fill", "sp-zone-yard" };
                var script = new List<(double At, string Id, string Key, string Area)>();
                for (var t = 30.0; t < 400.0; t += 20 + rnd.NextDouble() * 50)
                {
                    var id = sim.Ids[rnd.Next(sim.Ids.Count)];
                    script.Add(rnd.NextDouble() < 0.4 ? (t, id, "goto-refuel", (string)null) : (t, id, "goto-area", zones[rnd.Next(3)]));
                }

                Run(sim, 600.0, script.ToArray());
                Assert.AreEqual("", Overlaps(sim), "seed " + seed);
            }
        }
    }
}
