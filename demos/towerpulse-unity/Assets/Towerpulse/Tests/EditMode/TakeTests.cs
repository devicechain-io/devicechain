// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Linq;
using DeviceChain.Towerpulse.Story;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The take: a caption that names a time of day agrees with the clock beside it, a jump in the clock is labelled, and a take plan reads strictly.</summary>
    public class TakeTests
    {
        // ---- the clock a caption sits beside

        [TestCase(5.0, "dawn")]
        [TestCase(9.6, "morning")]
        [TestCase(13.9, "afternoon")]
        [TestCase(17.4, "afternoon")]
        [TestCase(17.5, "dusk")]
        [TestCase(20.5, "evening")]
        [TestCase(23.0, "night")]
        [TestCase(2.0, "night")]
        public void TheHourFallsInTheRightPartOfTheDay(double hour, string part)
        {
            Assert.That(StoryText.DayPart(hour), Is.EqualTo(part));
        }

        [Test]
        public void ADuskCaptionBesideAMorningClockDisagrees()
        {
            // the caption the establishing shot used to carry, with the clock the first still showed
            Assert.That(StoryText.CaptionAgrees("A working campus at dusk", 9.6), Is.False);
            Assert.That(StoryText.CaptionAgrees("A working campus at dusk", 18.0), Is.True);
            Assert.That(StoryText.CaptionAgrees("A working campus, an ordinary afternoon", 13.9), Is.True);
            Assert.That(StoryText.CaptionAgrees("A working campus, an ordinary afternoon", 9.6), Is.False);
        }

        [Test]
        public void ACaptionThatClaimsNothingAgreesWithAnyClock()
        {
            Assert.That(StoryText.CaptionAgrees("Thirty-one devices, one campus", 3.0), Is.True);
            Assert.That(StoryText.CaptionAgrees(null, 3.0), Is.True);
            Assert.That(StoryText.CaptionAgrees("", 3.0), Is.True);
        }

        [Test]
        public void OnlyAWholeWordIsAClaim()
        {
            Assert.That(StoryText.ClaimedDayPart("Nightingale ward"), Is.Null);
            Assert.That(StoryText.ClaimedDayPart("Sunset boulevard"), Is.EqualTo("dusk"));
        }

        // ---- the label on a jump

        [Test]
        public void AJumpIsLabelledAndRealTimeIsNot()
        {
            Assert.That(StoryText.Jump(1.0), Is.Null);
            Assert.That(StoryText.Jump(1.05), Is.Null, "a hair over real time is real time");
            Assert.That(StoryText.Jump(double.NaN), Is.Null);
            Assert.That(StoryText.Jump(57.5), Is.EqualTo("TIME JUMP x58"));
            Assert.That(StoryText.Jump(5.0), Is.EqualTo("TIME JUMP x5"));
        }

        // ---- the take plan

        const string Pose = @"{ ""pos"": [0, 10, -30], ""look"": [0, 5, 0], ""fov"": 40 }";

        static string Plan(string shot)
        {
            return @"{ ""version"": 1, ""shots"": [ " + shot + " ] }";
        }

        static string Shot(string extra, string caption = "one")
        {
            return @"{ ""id"": ""a"", ""caption"": """ + caption + @""", ""startS"": 0, ""durationS"": 10, " + (extra.Length > 0 ? extra + ", " : "") + @"""from"": " + Pose + @", ""to"": " + Pose.Replace("-30", "-20") + " }";
        }

        [Test]
        public void AShotCarriesItsRunAndItsClock()
        {
            var r = ShotFile.Parse(Plan(Shot(@"""run"": ""A"", ""modelFromHour"": 14.0, ""modelToHour"": 14.1667")));
            Assert.That(r.Ok, Is.True, r.Report());
            var s = r.List.Shots[0];
            Assert.That(s.Run, Is.EqualTo("A"));
            Assert.That(s.HasModel, Is.True);
            Assert.That(s.Rate, Is.EqualTo(60.0).Within(0.05), "ten show seconds for ten model minutes");
            Assert.That(r.List.ModelAt(0), Is.EqualTo(14.0 * 3600).Within(1e-6));
            Assert.That(r.List.ModelAt(5), Is.EqualTo((14.0 + 14.1667) / 2 * 3600).Within(1e-3), "the clock is linear in show time, not eased");
            Assert.That(r.List.ModelAt(10), Is.EqualTo(14.1667 * 3600).Within(1e-3));
        }

        [Test]
        public void AShotWithNoClockHasNoModelTime()
        {
            var r = ShotFile.Parse(Plan(Shot("")));
            Assert.That(r.Ok, Is.True, r.Report());
            Assert.That(r.List.Shots[0].HasModel, Is.False);
            Assert.That(double.IsNaN(r.List.ModelAt(1)), Is.True);
            Assert.That(double.IsNaN(r.List.Shots[0].Rate), Is.True);
        }

        [Test]
        public void TheTwoModelHoursGoTogetherAndNeverBackwards()
        {
            Assert.That(ShotFile.Parse(Plan(Shot(@"""modelFromHour"": 14.0"))).Ok, Is.False);
            Assert.That(ShotFile.Parse(Plan(Shot(@"""modelToHour"": 14.0"))).Ok, Is.False);
            Assert.That(ShotFile.Parse(Plan(Shot(@"""modelFromHour"": 15.0, ""modelToHour"": 14.0"))).Ok, Is.False);
            Assert.That(ShotFile.Parse(Plan(Shot(@"""modelFromHour"": 23.0, ""modelToHour"": 25.0"))).Ok, Is.False);
        }

        [Test]
        public void ACaptionThatDisagreesWithTheClockIsRefused()
        {
            var r = ShotFile.Parse(Plan(Shot(@"""modelFromHour"": 9.6, ""modelToHour"": 9.7", "A working campus at dusk")));
            Assert.That(r.Ok, Is.False);
            StringAssert.Contains("dusk", r.Report());
            StringAssert.Contains("09:36", r.Report());
            // the same words are fine when the clock agrees
            Assert.That(ShotFile.Parse(Plan(Shot(@"""modelFromHour"": 18.0, ""modelToHour"": 18.1", "A working campus at dusk"))).Ok, Is.True);
        }

        [Test]
        public void ACaptionThatCrossesIntoAnotherPartOfTheDayIsRefused()
        {
            // the clock starts in the afternoon and ends at dusk: the caption cannot be true for the whole shot
            var r = ShotFile.Parse(Plan(Shot(@"""modelFromHour"": 17.0, ""modelToHour"": 18.0", "A late afternoon")));
            Assert.That(r.Ok, Is.False);
        }

        // ---- the committed files

        static string Repo(string rel)
        {
            foreach (var start in new[] { AppContext.BaseDirectory, Environment.CurrentDirectory })
            {
                var dir = new DirectoryInfo(start);
                while (dir != null)
                {
                    var p = Path.Combine(dir.FullName, rel);
                    if (File.Exists(p)) return p;
                    dir = dir.Parent;
                }
            }
            Assert.Fail("cannot find " + rel);
            return null;
        }

        [Test]
        public void TheCommittedStoryboardNamesNoTimeOfDayItsClockContradicts()
        {
            // the live shot file has no clock of its own: the only time of day it may name is the afternoon the scripted day runs in (14:00 to 16:30)
            var r = ShotFile.Parse(File.ReadAllText(Repo("demos/towerpulse-unity/Assets/Towerpulse/Data/shots.json")));
            Assert.That(r.Ok, Is.True, r.Report());
            foreach (var s in r.List.Shots)
                Assert.That(StoryText.ClaimedDayPart(s.Caption), Is.Null.Or.EqualTo("afternoon"), s.Id + " says " + s.Caption);
        }

        [Test]
        public void TheCommittedTakePlanIsValidAndMovesForwardInEachRun()
        {
            var r = ShotFile.Parse(File.ReadAllText(Repo("demos/towerpulse-unity/tools/take/take-plan.json")));
            Assert.That(r.Ok, Is.True, r.Report());
            Assert.That(r.List.TotalS, Is.EqualTo(90).Within(0.001), "a 90-second film");
            foreach (var run in new[] { "A", "B" })
            {
                var mine = r.List.Shots.Where(s => s.Run == run).ToList();
                Assert.That(mine, Is.Not.Empty, "run " + run + " has shots");
                for (int i = 0; i < mine.Count; i++)
                {
                    Assert.That(mine[i].HasModel, Is.True, mine[i].Id + " says what time it covers");
                    if (i > 0) Assert.That(mine[i].ModelFromHour, Is.GreaterThanOrEqualTo(mine[i - 1].ModelToHour - 1e-9), mine[i].Id + " starts where " + mine[i - 1].Id + " ended or later");
                }
            }
            Assert.That(r.List.Shots.All(s => s.Run == "A" || s.Run == "B"), Is.True);
        }

        [Test]
        public void EveryJumpInTheCommittedTakePlanIsLabelled()
        {
            var r = ShotFile.Parse(File.ReadAllText(Repo("demos/towerpulse-unity/tools/take/take-plan.json")));
            foreach (var s in r.List.Shots)
                if (s.Rate > 1.05) Assert.That(StoryText.Jump(s.Rate), Is.Not.Null, s.Id);
            Assert.That(r.List.Shots.Any(s => s.Rate > 1.05), Is.True, "the plan does jump the clock, or this test pins nothing");
        }

        // ---- the frame statistics

        [Test]
        public void PercentilesAreByNearestRank()
        {
            var ms = Enumerable.Range(1, 100).Select(i => (double)i).Reverse().ToList();
            var s = FrameStats.Of(ms);
            Assert.That(s.Frames, Is.EqualTo(100));
            Assert.That(s.MeanMs, Is.EqualTo(50.5).Within(1e-9));
            Assert.That(s.P50Ms, Is.EqualTo(50));
            Assert.That(s.P95Ms, Is.EqualTo(95));
            Assert.That(s.P99Ms, Is.EqualTo(99));
            Assert.That(s.MaxMs, Is.EqualTo(100));
        }

        [Test]
        public void NoFramesIsAnErrorNotAZero()
        {
            Assert.Throws<ArgumentException>(() => FrameStats.Of(new double[0]));
        }

        [Test]
        public void OneSlowFrameShowsInTheTailButNotTheMean()
        {
            var ms = Enumerable.Repeat(5.0, 99).Concat(new[] { 50.0 }).ToList();
            var s = FrameStats.Of(ms);
            Assert.That(s.P99Ms, Is.EqualTo(5.0), "nearest rank: 99 of 100 frames are at or under 5 ms");
            Assert.That(s.MaxMs, Is.EqualTo(50.0));
            Assert.That(s.MeanMs, Is.EqualTo(5.45).Within(1e-9));
        }
    }
}
