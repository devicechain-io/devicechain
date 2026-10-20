// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// The whole site over long runs, judged by PROGRESS and not only by overlap: a guard that keeps machines apart can do it by stopping
    /// them all, and the demo's gridlock did exactly that (every haul truck standing at the ramp bottom for good, none of them overlapping).
    /// So every run here also asks that every haul truck keeps going round its loop, that none stands still on its track for long, that every
    /// command is answered, and that nothing is left held at the end.
    /// </summary>
    public sealed class TrafficSoakTests
    {
        const double Dt = 0.1;

        /// <summary>The longest a haul truck may stand still on its track, in seconds (it waits a load or a dump at most, and a little more).</summary>
        const double LongestStandSeconds = 90.0;

        static SceneSim Site() => new SceneSim(CommandKit.AssetPath("Data/quarry_fleet_live.json"), CommandKit.Site, CommandKit.Graph, CommandKit.Prepared);

        /// <summary>What the interlocking reports went wrong beneath the run: drivers that went further than they were allowed, and defects of the site.</summary>
        static string Faults(SceneSim sim)
        {
            var il = sim.Director.Traffic.Interlocking;
            return il.Overruns == 0 && il.Defects == 0 ? "" : il.Overruns + " overrun(s) (" + il.LastOverrun + "), " + il.Defects + " site defect(s)";
        }

        /// <summary>One run's record: every haul truck's laps, its longest stand on its track, and the commands it was sent.</summary>
        sealed class Watch
        {
            public readonly Dictionary<string, int> Laps = new Dictionary<string, int>();
            public readonly Dictionary<string, double> LapsAfter = new Dictionary<string, double>();
            public readonly Dictionary<string, (double Seconds, double At)> LongestStand = new Dictionary<string, (double, double)>();
            public readonly List<(string Id, TaskRequest Request, double At)> Commands = new List<(string, TaskRequest, double)>();
            readonly Dictionary<string, double> lastTau = new Dictionary<string, double>();
            readonly Dictionary<string, double> standing = new Dictionary<string, double>();

            public void Observe(SceneSim sim, double markAfter)
            {
                foreach (var id in sim.Ids.Where(i => i.StartsWith("SP-HL", StringComparison.Ordinal)))
                {
                    var b = sim.Bodies[id];
                    if (!Laps.ContainsKey(id))
                    {
                        Laps[id] = 0;
                        LapsAfter[id] = 0;
                        LongestStand[id] = (0, 0);
                    }

                    if (!b.Attached)
                    {
                        lastTau.Remove(id);
                        standing.Remove(id);
                        continue;
                    }

                    var tau = b.TrackSeconds;
                    if (lastTau.TryGetValue(id, out var was) && tau < was - 60.0)
                    {
                        Laps[id]++;
                        if (sim.Now >= markAfter) LapsAfter[id]++;
                    }

                    lastTau[id] = tau;
                    if (b.Rate < 0.05)
                    {
                        if (!standing.ContainsKey(id)) standing[id] = sim.Now;
                        var stood = sim.Now - standing[id];
                        if (stood > LongestStand[id].Seconds) LongestStand[id] = (stood, standing[id]);
                    }
                    else standing.Remove(id);
                }
            }
        }

        /// <summary>Runs the site for <paramref name="seconds"/>, sending the script's commands as their time comes and resuming a machine parked longer than <paramref name="resumeAfter"/>.</summary>
        static Watch Run(SceneSim sim, double seconds, IList<(double At, string Id, string Key, string Area)> script, double resumeAfter = double.MaxValue, double markAfter = double.MaxValue)
        {
            var watch = new Watch();
            var next = 0;
            var parkedSince = new Dictionary<string, double>();
            for (var t = 0.0; t < seconds; t += Dt)
            {
                while (next < script.Count && sim.Now >= script[next].At)
                {
                    var (_, id, key, area) = script[next++];
                    watch.Commands.Add((id, sim.Send(id, key, area), sim.Now));
                }

                foreach (var id in sim.Ids)
                {
                    var c = sim.Director[id];
                    if (c.Mode != MachineMode.Parked) { parkedSince.Remove(id); continue; }
                    if (!parkedSince.ContainsKey(id)) parkedSince[id] = sim.Now;
                    else if (sim.Now - parkedSince[id] > resumeAfter)
                    {
                        c.Resume();
                        parkedSince.Remove(id);
                    }
                }

                sim.Step(Dt);
                watch.Observe(sim, markAfter);
            }

            return watch;
        }

        static string Overlaps(SceneSim sim) => string.Join("; ", sim.Pairs.Values.Where(p => p.MinGap < 0)
            .OrderBy(p => p.MinGap).Select(p => p.A + "/" + p.B + " " + p.MinGap.ToString("0.00") + " m at " + p.At.ToString("0") + " s"));

        static string Story(SceneSim sim, Watch w) =>
            string.Join("\n", w.Laps.Keys.Select(id => id + ": " + w.Laps[id] + " laps, longest stand " + w.LongestStand[id].Seconds.ToString("0") + " s at " +
                                                       w.LongestStand[id].At.ToString("0") + " s, " + sim.Director[id].Mode + " (" + (sim.Director.Traffic.Explain(id) ?? "-") + ")"));

        // ---- the take: what froze every haul truck at the ramp bottom

        [Test]
        public void TheTakesRefuelAndOperatorVisitLeaveTheLoopCycling()
        {
            // the feature video's take: SP-HL-0006 to the refuel bay at 221 s, the operator's SP-HL-0003 to the yard at 600 s
            var sim = Site();
            var w = Run(sim, 1200.0, new[] { (221.0, "SP-HL-0006", "goto-refuel", (string)null), (600.0, "SP-HL-0003", "goto-area", "sp-zone-yard") }, markAfter: 900.0);
            Assert.AreEqual("", Overlaps(sim));
            foreach (var (id, request, at) in w.Commands)
            {
                Assert.IsTrue(request.IsComplete, id + " sent at " + at + " s is answered: " + sim.Timeline.Text(id, 20));
                Assert.IsTrue(request.Completion.Answer().Succeeded, id + ": " + request.Completion.Answer() + "\n" + sim.Timeline.Text(id, 20));
            }

            foreach (var id in w.Laps.Keys)
            {
                Assert.LessOrEqual(w.LongestStand[id].Seconds, LongestStandSeconds, id + " stood still on its track\n" + Story(sim, w));
                if (id == "SP-HL-0003") continue;   // parked in the yard by the operator
                Assert.AreEqual(MachineMode.Working, sim.Director[id].Mode, id + " is at work at the end\n" + Story(sim, w));
                Assert.GreaterOrEqual(w.Laps[id], 4, id + " went round\n" + Story(sim, w));
                Assert.GreaterOrEqual(w.LapsAfter[id], 1, id + " was still going round in the last five minutes\n" + Story(sim, w));
            }

            Assert.AreEqual(MachineMode.Parked, sim.Director["SP-HL-0003"].Mode, "the operator's truck stands in the yard");
            Assert.AreEqual("", Faults(sim));
        }

        // ---- half an hour of errands

        static List<(double At, string Id, string Key, string Area)> Errands(int seed, double until)
        {
            // a haul truck every two to five minutes: to the refuel bay, or to the yard (and resumed a minute later)
            var rnd = new Random(seed);
            var haulers = Enumerable.Range(1, 6).Select(i => "SP-HL-000" + i).ToList();
            var script = new List<(double, string, string, string)>();
            for (var t = 150.0; t < until; t += 150.0 + rnd.NextDouble() * 150.0)
                script.Add(rnd.NextDouble() < 0.6 ? (t, haulers[rnd.Next(6)], "goto-refuel", (string)null) : (t, haulers[rnd.Next(6)], "goto-area", "sp-zone-yard"));
            return script;
        }

        [Test]
        public void ThirtyMinutesOfErrandsKeepEveryTruckCycling([Values(1, 2, 3)] int seed)
        {
            var sim = Site();
            var w = Run(sim, 1800.0, Errands(seed, 1500.0), resumeAfter: 60.0, markAfter: 1500.0);
            Assert.AreEqual("", Overlaps(sim), "seed " + seed);
            foreach (var (id, request, at) in w.Commands)
                Assert.IsTrue(request.IsComplete, "seed " + seed + ": " + id + " sent at " + at.ToString("0") + " s is answered\n" + sim.Timeline.Text(id, 20));
            var answeredOk = w.Commands.Count(c => c.Request.Completion.Answer().Succeeded);
            Assert.GreaterOrEqual(answeredOk, w.Commands.Count * 2 / 3, "seed " + seed + ": most errands were carried out: " +
                string.Join("; ", w.Commands.Where(c => !c.Request.Completion.Answer().Succeeded).Select(c => c.Id + ": " + c.Request.Completion.Answer().Reason)));
            foreach (var id in w.Laps.Keys)
            {
                Assert.GreaterOrEqual(w.Laps[id], 5, "seed " + seed + ": " + id + " went round\n" + Story(sim, w));
                Assert.LessOrEqual(w.LongestStand[id].Seconds, LongestStandSeconds, "seed " + seed + ": " + id + " stood still on its track\n" + Story(sim, w));
                Assert.GreaterOrEqual(w.LapsAfter[id], 1, "seed " + seed + ": " + id + " was still going round in the last five minutes\n" + Story(sim, w));
            }

            Assert.AreEqual("", Faults(sim), "seed " + seed);
        }

        // ---- commands on top of commands

        [Test]
        public void CommandsThatSupersedeEachOtherEveryFewSecondsNeverStrandATruck([Values(1, 2, 3, 4, 5, 101, 120)] int seed)
        {
            // a haul truck every five to thirty-five seconds, to the refuel bay or to any zone: most land on a truck already on an errand, so
            // trips are given up wherever the truck is (riding to its exit, part-way along a lane, in the queue, in the bay) and the next one is
            // planned from there. None may leave a truck standing on a lane, and once the commands stop and parked trucks are put back to work,
            // every truck must be going round again
            var sim = Site();
            var rnd = new Random(seed);
            var zones = new[] { "sp-zone-cut", "sp-zone-fill", "sp-zone-yard" };
            var script = new List<(double, string, string, string)>();
            for (var t = 20.0; t < 900.0; t += 5 + rnd.NextDouble() * 30)
            {
                var id = "SP-HL-000" + (1 + rnd.Next(6));
                script.Add(rnd.NextDouble() < 0.5 ? (t, id, "goto-refuel", (string)null) : (t, id, "goto-area", zones[rnd.Next(3)]));
            }

            var w = Run(sim, 1800.0, script, resumeAfter: 30.0, markAfter: 1350.0);
            Assert.AreEqual("", Overlaps(sim), "seed " + seed);
            foreach (var (id, request, at) in w.Commands)
                Assert.IsTrue(request.IsComplete, "seed " + seed + ": " + id + " sent at " + at.ToString("0") + " s is answered\n" + sim.Timeline.Text(id, 20));
            foreach (var id in w.Laps.Keys)
            {
                Assert.AreEqual(MachineMode.Working, sim.Director[id].Mode, "seed " + seed + ": " + id + " is back at work\n" + Story(sim, w) + "\n" + sim.Timeline.Text(id, 20));
                Assert.GreaterOrEqual(w.LapsAfter[id], 1, "seed " + seed + ": " + id + " was going round again at the end\n" + Story(sim, w));
            }

            Assert.AreEqual("", Faults(sim), "seed " + seed);
        }

        // ---- anything sent anywhere

        [Test]
        public void AnyMachineSentAnywhereNeverStopsTheLoop([Values(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)] int seed)
        {
            var sim = Site();
            var rnd = new Random(seed);
            var zones = new[] { "sp-zone-cut", "sp-zone-fill", "sp-zone-yard" };
            var script = new List<(double, string, string, string)>();
            for (var t = 30.0; t < 400.0; t += 20 + rnd.NextDouble() * 50)
            {
                var id = sim.Ids[rnd.Next(sim.Ids.Count)];
                script.Add(rnd.NextDouble() < 0.4 ? (t, id, "goto-refuel", (string)null) : (t, id, "goto-area", zones[rnd.Next(3)]));
            }

            // the commands, then five quiet minutes with parked machines resumed: the loop must be going round again by the end of them
            var w = Run(sim, 1000.0, script, resumeAfter: 120.0, markAfter: 700.0);
            Assert.AreEqual("", Overlaps(sim), "seed " + seed);
            foreach (var (id, request, at) in w.Commands)
                Assert.IsTrue(request.IsComplete, "seed " + seed + ": " + id + " sent at " + at.ToString("0") + " s is answered\n" + sim.Timeline.Text(id, 20));
            foreach (var id in w.Laps.Keys)
            {
                Assert.LessOrEqual(w.LongestStand[id].Seconds, LongestStandSeconds, "seed " + seed + ": " + id + " stood still on its track\n" + Story(sim, w));
                if (sim.Bodies[id].Attached)
                    Assert.GreaterOrEqual(w.LapsAfter[id], 1, "seed " + seed + ": " + id + " was going round again at the end\n" + Story(sim, w));
            }

            Assert.AreEqual("", Faults(sim), "seed " + seed);
        }
    }
}
