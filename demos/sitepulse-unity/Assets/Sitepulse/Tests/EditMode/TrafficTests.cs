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

        // ---- the whole site

        static string Overlaps(SceneSim sim) => string.Join("; ", sim.Pairs.Values.Where(p => p.MinGap < 0)
            .OrderBy(p => p.MinGap).Select(p => p.A + "/" + p.B + " " + p.MinGap.ToString("0.00") + " m at " + p.At.ToString("0") + " s"));

        static SceneSim Site(double startClock = SceneSim.StartClock) => new SceneSim(CommandKit.AssetPath("Data/quarry_fleet_live.json"), CommandKit.Site, CommandKit.Graph, CommandKit.Prepared, startClock);

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
        public void TheChoreographyRunsAtItsOwnPaceWhileNothingIsCommanded()
        {
            // the loop as laid out passes every junction box and station on time, so the interlocking holds nobody back for long: each truck
            // plays its track at its own pace but for moments (the ramp bottom's tightest crossing), and the coupled loader keeps its truck
            var sim = Site();
            var played = sim.Ids.ToDictionary(id => id, id => 0.0);
            var lowest = sim.Ids.ToDictionary(id => id, id => 1.0);
            for (var i = 0; i < 6000; i++)
            {
                sim.Step(Dt);
                foreach (var id in sim.Ids) lowest[id] = Math.Min(lowest[id], sim.Director.TrackRateOf(id));
            }

            foreach (var id in sim.Ids)
            {
                Assert.GreaterOrEqual(sim.Bodies[id].PlayedSeconds, 0.995 * sim.Now, id + " played " + sim.Bodies[id].PlayedSeconds.ToString("0.0") + " of " + sim.Now.ToString("0") + " s");
                Assert.GreaterOrEqual(lowest[id], 0.5, id + " was never held to a crawl");
            }

            Assert.AreEqual("", Overlaps(sim), "the 18 machines as they were laid out");
            Assert.AreEqual(0, sim.Director.Traffic.Interlocking.Overruns, sim.Director.Traffic.Interlocking.LastOverrun);
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

        [Test]
        public void AHaulTruckSentElsewhereWhileOnALaneGoesOnFromWhereItIsAndIsNeverLeftThere([Values(true, false)] bool standFree)
        {
            // SP-HL-0006 leaves the loop for the refuel queue, and the moment it is on the queue's lane it is sent to the yard instead. With the
            // yard's stand free it drives there from where it is (on through the bay's lanes and round). With the stand taken (SP-HL-0001 was sent
            // there first) the command is refused, and the truck, whose refuel it superseded, must not be left standing on the lane, where its tail
            // is over the loop's crossing: it drives on, back to work. Either way the other trucks keep going round, and nothing is driven past
            // the cells it holds.
            var sim = Site();
            TaskRequest refuel = null, yard = null;
            var onLane = -1.0;
            var others = new[] { "SP-HL-0002", "SP-HL-0003", "SP-HL-0004", "SP-HL-0005" };
            Dictionary<string, double> playedAt600 = null;
            for (var t = 0.0; t < 900.0; t += Dt)
            {
                if (!standFree && t == 0.0) sim.Send("SP-HL-0001", "goto-area", "sp-zone-yard");
                if (refuel == null && sim.Now >= 30.0) refuel = sim.Send("SP-HL-0006", "goto-refuel");
                if (refuel != null && yard == null && !sim.Bodies["SP-HL-0006"].Attached && sim.Director["SP-HL-0006"].Phase == TaskPhase.ToQueue)
                {
                    yard = sim.Send("SP-HL-0006", "goto-area", "sp-zone-yard");
                    onLane = sim.Now;
                }

                if (playedAt600 == null && sim.Now >= 600.0) playedAt600 = others.ToDictionary(id => id, id => sim.Bodies[id].PlayedSeconds);
                sim.Step(Dt);
            }

            var il = sim.Director.Traffic.Interlocking;
            Assert.Greater(onLane, 0.0, "SP-HL-0006 was sent on while on the queue's lane");
            Assert.AreEqual("", Overlaps(sim));
            Assert.AreEqual(0, il.Overruns, il.LastOverrun);
            Assert.IsTrue(yard.IsComplete, sim.Timeline.Text("SP-HL-0006", 20));
            Assert.AreEqual(standFree, yard.Completion.Answer().Succeeded, yard.Completion.Answer() + "\n" + sim.Timeline.Text("SP-HL-0006", 20));
            Assert.AreEqual(standFree ? MachineMode.Parked : MachineMode.Working, sim.Director["SP-HL-0006"].Mode,
                "SP-HL-0006 " + (standFree ? "stands in the yard" : "is back at work, not standing on the lane") + "\n" + sim.Timeline.Text("SP-HL-0006", 20));
            foreach (var id in others)
                Assert.GreaterOrEqual(sim.Bodies[id].PlayedSeconds - playedAt600[id], 150.0, id + " kept going round in the last five minutes (" + sim.Director.Traffic.Explain(id) + ")");
        }
    }
}
