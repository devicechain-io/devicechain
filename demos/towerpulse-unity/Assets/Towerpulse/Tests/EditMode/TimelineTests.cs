// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using DeviceChain.Towerpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The scripted day: each beat of the 90-second story happens in its window, and what cools the room is the platform's action.</summary>
    public class TimelineTests
    {
        static int TowerPeople(BuildingModel m) { return m.Zones.Where(z => z.Spec.Building == "tower").Sum(z => z.Occupancy); }

        // ---- beat 1

        [TestCase(1UL)]
        [TestCase(7UL)]
        public void PeopleArriveBetweenEightAndTen(ulong seed)
        {
            var d = new TowerDay(seed);
            d.AdvanceTo(TimeClock.At(0, 7, 55));
            Assert.That(TowerPeople(d.Model), Is.EqualTo(0), "nobody before the arrival window");
            d.AdvanceTo(TimeClock.At(0, 8, 45));
            int mid = TowerPeople(d.Model);
            Assert.That(mid, Is.GreaterThan(0), "arrivals have begun");
            d.AdvanceTo(TimeClock.At(0, 10, 0));
            int full = TowerPeople(d.Model);
            Assert.That(full, Is.GreaterThan(mid));
            int target = d.Model.Zones.Where(z => z.Spec.Building == "tower").Sum(z => z.OccupancyTarget);
            Assert.That(full, Is.GreaterThanOrEqualTo((int)(target * 0.9)), "by ten the tower is at its working strength");
            Assert.That(full, Is.GreaterThan(80));
        }

        // ---- beat 2

        [TestCase(1UL)]
        [TestCase(2UL)]
        [TestCase(3UL)]
        [TestCase(11UL)]
        [TestCase(99UL)]
        public void TheMeetingHeatsF4EastTheRuleFiresAndTheBoostCoolsItToSetpoint(ulong seed)
        {
            var d = new TowerDay(seed);
            var h = new RuleHarness(d);
            h.RunTo(TimeClock.At(0, 13, 55));
            Assert.That(h.HotAlarmAt.Count, Is.EqualTo(0), "quiet before the meeting");
            Assert.That(h.Zone(BuildingLayout.HotZone).TempC, Is.LessThan(RuleHarness.HotC), "the room starts the meeting under the threshold");

            h.RunTo(TimeClock.At(0, 14, 40));
            Assert.That(h.HotAlarmAt.ContainsKey(BuildingLayout.HotZone) || h.BoostSent.ContainsKey(BuildingLayout.HotZone), Is.True, "the rule fired");
            // The FIRST boost is the story: it cools the room to setpoint and completes. The rule may fire again if the meeting keeps the room hot after a
            // 15-minute boost runs out (a retrigger during an ongoing meeting is allowed), so the latest ticket is asserted separately and more loosely.
            var tk = h.FirstBoost[BuildingLayout.HotZone];
            Assert.That(tk.IssuedAtS, Is.InRange(TowerDay.MeetingStartS + 5 * 60, TowerDay.MeetingStartS + 30 * 60), "within half an hour of the meeting filling the room");
            Assert.That(h.MaxF4EastTempC, Is.GreaterThan(RuleHarness.HotC), "it really did go past 26");
            Assert.That(tk.State, Is.EqualTo(CommandState.Completed), "the boost completed");
            Assert.That(tk.EndedAtS, Is.InRange(tk.IssuedAtS + 3 * 60, TowerDay.MeetingEndS), "after cooling for minutes, before the meeting ends");
            Assert.That(string.Join(",", h.BoostSent.Keys.ToArray()), Is.EqualTo(BuildingLayout.HotZone), "it is the only room that needed help");
            var latest = h.BoostSent[BuildingLayout.HotZone];
            Assert.That(latest.State, Is.Not.EqualTo(CommandState.Failed), "no later boost failed");
            Assert.That(latest.IssuedAtS, Is.GreaterThanOrEqualTo(tk.IssuedAtS));
            if (latest.State == CommandState.Completed) Assert.That(h.Zone(BuildingLayout.HotZone).TempC, Is.LessThan(RuleHarness.HotC), "a completed boost leaves the room under the threshold");
        }

        [TestCase(1UL)]
        [TestCase(2UL)]
        [TestCase(11UL)]
        public void WithoutTheBoostTheRoomStaysHot(ulong seed)
        {
            // The negative control: the same day, the same meeting, the rule switched off.
            var h = new RuleHarness(new TowerDay(seed)) { R1Enabled = false };
            int below = 0, samples = 0;
            h.RunTo(TimeClock.At(0, 14, 20));
            h.RunTo(TimeClock.At(0, 14, 45), t => { samples++; if (h.Zone(BuildingLayout.HotZone).TempC <= RuleHarness.HotC) below++; });
            Assert.That(samples, Is.GreaterThan(40));
            Assert.That(below, Is.EqualTo(0), "from 14:20 to the end of the meeting every sample is above 26");
            Assert.That(h.Model.Tickets.Count, Is.EqualTo(0), "nothing sent");
            Assert.That(h.Zone(BuildingLayout.HotZone).DamperPct, Is.LessThan(60), "the damper never left its normal range");
            Assert.That(h.MaxF4EastTempC, Is.GreaterThan(27.0));
        }

        [Test]
        public void TheBoostIsWhatCoolsTheRoom()
        {
            var with = new RuleHarness(new TowerDay(1));
            var without = new RuleHarness(new TowerDay(1)) { R1Enabled = false };
            // Compare at the moment the first boost completes: that is when its work is done.
            double doneAt = double.NaN;
            with.RunTo(TimeClock.At(0, 14, 40), t => { if (double.IsNaN(doneAt) && with.FirstBoost.ContainsKey(BuildingLayout.HotZone) && with.FirstBoost[BuildingLayout.HotZone].State == CommandState.Completed) doneAt = t; });
            Assert.That(double.IsNaN(doneAt), Is.False);
            without.RunTo(doneAt);
            Assert.That(without.Zone(BuildingLayout.HotZone).TempC, Is.GreaterThan(RuleHarness.HotC), "unaided, the room is still hot when the boost's work is done");
        }

        // ---- beat 3

        [TestCase(1UL)]
        [TestCase(5UL)]
        public void TheAnnexChillerDropsAndTheRestartBringsItBack(ulong seed)
        {
            var d = new TowerDay(seed);
            var h = new RuleHarness(d);
            h.RunTo(TimeClock.At(0, 16, 29));
            var c = d.Model.Chillers[d.Model.Layout.ChillerIndex(BuildingLayout.AnnexChiller)];
            Assert.That(c.Online && c.Running, Is.True, "healthy until 16:30");
            Assert.That(double.IsNaN(h.ChillerAlarmAt), Is.True);
            h.RunTo(TimeClock.At(0, 16, 35));
            Assert.That(d.ChillerTripped, Is.True);
            Assert.That(c.Online, Is.False);
            Assert.That(h.ChillerAlarmAt, Is.InRange(TowerDay.ChillerTripS + 100, TowerDay.ChillerTripS + 200), "the absence rule fires after about two minutes of silence");

            h.RunTo(TimeClock.At(0, 17, 0));
            Assert.That(c.Online, Is.False, "30 minutes on it is still down: only a command brings it back");
            var tk = d.Model.RestartChiller(BuildingLayout.AnnexChiller);
            h.RunTo(TimeClock.At(0, 17, 20));
            Assert.That(tk.State, Is.EqualTo(CommandState.Completed));
            Assert.That(tk.EndedAtS - tk.IssuedAtS, Is.InRange(BuildingModel.RestartDelayS, 15 * 60.0));
            Assert.That(c.Online && c.Running, Is.True);
            Assert.That(c.PowerKw, Is.GreaterThan(3.0));
        }

        [Test]
        public void WithoutTheRestartTheChillerStaysDownForGood()
        {
            var h = new RuleHarness(new TowerDay(1));
            h.RunTo(TimeClock.At(0, 22, 0));
            Assert.That(h.Model.Chillers[1].Online, Is.False);
            Assert.That(h.Model.Chillers[1].Running, Is.False);
        }

        // ---- the show clock

        [Test]
        public void TheShowClockPutsEachBeatInItsSlotOfTheStory()
        {
            var h = new RuleHarness(new TowerDay(1));
            h.RunTo(TimeClock.At(0, 17, 30));
            double alarm = h.BoostSent[BuildingLayout.HotZone].IssuedAtS;
            double done = h.BoostSent[BuildingLayout.HotZone].EndedAtS;
            Assert.That(alarm, Is.InRange(ShowClock.ToSim(10), ShowClock.ToSim(35)), "beat 2 starts between 10 s and 35 s of show");
            Assert.That(done, Is.InRange(ShowClock.ToSim(10), ShowClock.ToSim(35)), "and its command completes inside the same window");
            Assert.That(TowerDay.ChillerTripS, Is.InRange(ShowClock.ToSim(35), ShowClock.ToSim(55)), "beat 3 sits in 35-55 s");
            Assert.That(h.ChillerAlarmAt, Is.InRange(ShowClock.ToSim(35), ShowClock.ToSim(55)));
            Assert.That(TowerDay.ArrivalWindowStartS, Is.GreaterThanOrEqualTo(ShowClock.ToSim(0)));
            Assert.That(TowerDay.ArrivalWindowEndS, Is.LessThanOrEqualTo(ShowClock.ToSim(10) + 1), "beat 1 fills the first 10 s");
        }

        [Test]
        public void TheShowClockOnlyEverMovesForward()
        {
            double prev = double.MinValue;
            for (double s = -5; s <= 200; s += 0.25)
            {
                double t = ShowClock.ToSim(s);
                Assert.That(t, Is.GreaterThanOrEqualTo(prev), "show " + s);
                prev = t;
            }
        }

        [Test]
        public void SteppingByShowTimeIsTheSameAsSteppingBySimTime()
        {
            var a = new TowerDay(9);
            var b = new TowerDay(9);
            for (double s = 0; s <= 60; s += 0.5) a.AdvanceToShow(s);
            b.AdvanceTo(ShowClock.ToSim(60));
            Assert.That(a.Model.Snapshot(), Is.EqualTo(b.Model.Snapshot()));
            Assert.That(a.ChillerTripped, Is.True);
        }

        // ---- a week

        [Test]
        public void ASimulatedWeekStaysPhysicalAndKeepsItsRhythm()
        {
            var d = new TowerDay(2024);
            var h = new RuleHarness(d);
            double lastKwh = 0, lastDp = 0;
            int noonChecks = 0, weekendChecks = 0;
            double maxDp = 0, minCo2 = double.MaxValue;
            bool restarted = false;
            double end = TimeClock.At(7, 0, 0);
            h.RunTo(end, t =>
            {
                // The operator restarts the annex chiller as soon as the alarm has been up for 10 minutes.
                if (!restarted && !double.IsNaN(h.ChillerAlarmAt) && t >= h.ChillerAlarmAt + 600)
                {
                    restarted = true;
                    d.Model.RestartChiller(BuildingLayout.AnnexChiller);
                }
                if (((long)t) % 900 != 0) return;
                BuildingModelTests.AssertSane(d.Model, 12, 36, TimeClock.Format(t));
                double kwh = d.Model.Meters.Sum(m => m.Kwh);
                Assert.That(kwh, Is.GreaterThanOrEqualTo(lastKwh), "energy never runs backwards");
                lastKwh = kwh;
                double dp = d.Model.Ahus.Sum(a => a.FilterDpPa);
                Assert.That(dp, Is.GreaterThanOrEqualTo(lastDp), "a filter only gets dirtier");
                lastDp = dp;
                maxDp = d.Model.Ahus.Max(a => a.FilterDpPa);
                foreach (var z in d.Model.Zones) minCo2 = Math.Min(minCo2, z.Co2Ppm);
                double h12 = TimeClock.Hours(t);
                if (Math.Abs(h12 - 11.0) < 0.01 && TimeClock.IsWeekday(t) && TimeClock.DayIndex(t) > 0)
                {
                    noonChecks++;
                    Assert.That(TowerPeople(d.Model), Is.GreaterThan(60), "a working day has people in it, " + TimeClock.Format(t));
                }
                if (Math.Abs(h12 - 11.0) < 0.01 && !TimeClock.IsWeekday(t))
                {
                    weekendChecks++;
                    Assert.That(d.Model.Zones.Sum(z => z.Occupancy), Is.EqualTo(0), "the weekend is empty, " + TimeClock.Format(t));
                }
            });
            Assert.That(restarted, Is.True);
            Assert.That(noonChecks, Is.EqualTo(4));
            Assert.That(weekendChecks, Is.EqualTo(2));
            Assert.That(lastKwh, Is.GreaterThan(1000), "a week of a campus draws real energy");
            Assert.That(maxDp, Is.GreaterThan(150).And.LessThan(600), "the filter rose, slowly");
            Assert.That(minCo2, Is.GreaterThanOrEqualTo(400.0));
        }

        [Test]
        public void EvenIfNobodyEverRestartsTheChillerTheAnnexStaysBoundedForAWeek()
        {
            var d = new TowerDay(77);
            d.AdvanceTo(TimeClock.At(0, 16, 31));
            for (double t = d.TimeS; t < TimeClock.At(7, 0, 0); t += 1800)
            {
                d.AdvanceTo(t);
                BuildingModelTests.AssertSane(d.Model, 10, 55, TimeClock.Format(t));
            }
        }
    }
}
