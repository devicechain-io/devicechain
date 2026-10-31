// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using DeviceChain.Towerpulse.Story;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The story layer: how values become colours and motion, what a command card claims at each step, and the shot file that points the camera.</summary>
    public class StoryTests
    {
        // ---- zone tint

        [Test]
        public void TintAtSetpointIsTheCalmColour()
        {
            var c = TintRamp.Colour(22.5, 22.5);
            Assert.That(c.R, Is.EqualTo(TintRamp.Neutral.R).Within(1e-6));
            Assert.That(c.G, Is.EqualTo(TintRamp.Neutral.G).Within(1e-6));
            Assert.That(c.B, Is.EqualTo(TintRamp.Neutral.B).Within(1e-6));
            Assert.That(TintRamp.Warmth(22.5, 22.5), Is.EqualTo(0));
        }

        [Test]
        public void TintGetsWarmerAsTheRoomGetsHotter()
        {
            // "warm" here is red minus blue; it must never fall as the temperature climbs
            double prev = double.NegativeInfinity;
            for (double t = 17.0; t <= 32.0; t += 0.25)
            {
                var c = TintRamp.Colour(t, 22.5);
                double warm = c.R - c.B;
                Assert.That(warm, Is.GreaterThanOrEqualTo(prev - 1e-9), "at " + t + " C");
                prev = warm;
            }
        }

        [Test]
        public void TheStoryboardLoopsOnlyWhenAsked()
        {
            Assert.That(ShotList.LoopRequested(new[] { "Towerpulse.exe", "-tp-loop" }), Is.True);
            Assert.That(ShotList.LoopRequested(new[] { "Towerpulse.exe", "-tp-seed", "1" }), Is.False, "a live run plays the storyboard once unless asked");
            Assert.That(ShotList.LoopRequested(null), Is.False);
        }

        [Test]
        public void ColdIsBlueAndHotIsRed()
        {
            var cold = TintRamp.Colour(19.0, 22.5);
            var hot = TintRamp.Colour(28.0, 22.5);
            Assert.That(cold.B, Is.GreaterThan(cold.R + 0.4), "a cold room reads blue");
            Assert.That(hot.R, Is.GreaterThan(hot.B + 0.6), "a hot room reads red");
            Assert.That(hot.G, Is.LessThan(0.3f));
        }

        [Test]
        public void ADepartureFromSetpointShowsMoreThanComfort()
        {
            Assert.That(TintRamp.Colour(28.0, 22.5).A, Is.GreaterThan(TintRamp.Colour(22.5, 22.5).A), "a hot room is drawn more strongly than a calm one");
        }

        [Test]
        public void TintIsClampedBeyondTheSpans()
        {
            Assert.That(TintRamp.Warmth(60, 22.5), Is.EqualTo(1));
            Assert.That(TintRamp.Warmth(-10, 22.5), Is.EqualTo(-1));
            Assert.That(TintRamp.Warmth(22.5 + TintRamp.WarmSpanC, 22.5), Is.EqualTo(1));
            Assert.That(TintRamp.Warmth(22.5 - TintRamp.CoolSpanC, 22.5), Is.EqualTo(-1));
        }

        [Test]
        public void NoReadingIsNotAComfortableRoom()
        {
            var c = TintRamp.Colour(double.NaN, 22.5);
            Assert.That(c.A, Is.LessThan(TintRamp.Neutral.A));
            Assert.That(c.R, Is.EqualTo(TintRamp.NoData.R));
            Assert.That(double.IsNaN(TintRamp.Warmth(22, double.NaN)));
        }

        [TestCase(0.0, 14, 0)]
        [TestCase(6.0, 6, 6)]
        [TestCase(14.0, 14, 14)]
        [TestCase(14.0, 6, 6)]
        [TestCase(-3.0, 6, 0)]
        [TestCase(double.NaN, 6, 0)]
        [TestCase(5.0, 0, 0)]
        public void PeopleShownFollowTheReportedOccupancyAndTheSlotsThere(double reported, int slots, int expected)
        {
            Assert.That(PeopleView.Visible(reported, slots), Is.EqualTo(expected));
        }

        // ---- motion and the chiller lamp

        [Test]
        public void AhuFanSpinFollowsTheReportedPercent()
        {
            Assert.That(FanSpin.AhuDegPerS(0), Is.EqualTo(0));
            Assert.That(FanSpin.AhuDegPerS(100), Is.EqualTo(FanSpin.MaxDegPerS));
            Assert.That(FanSpin.AhuDegPerS(50), Is.EqualTo(FanSpin.MaxDegPerS / 2).Within(1e-9));
            Assert.That(FanSpin.AhuDegPerS(250), Is.EqualTo(FanSpin.MaxDegPerS), "more than 100 % is still full speed");
            Assert.That(FanSpin.AhuDegPerS(-5), Is.EqualTo(0));
            Assert.That(FanSpin.AhuDegPerS(double.NaN), Is.EqualTo(0));
        }

        [Test]
        public void ChillerFansTurnOnlyWhileRunning()
        {
            Assert.That(FanSpin.ChillerFansDegPerS(true), Is.GreaterThan(0));
            Assert.That(FanSpin.ChillerFansDegPerS(false), Is.EqualTo(0));
        }

        [Test]
        public void AFanRampsRatherThanSnaps()
        {
            double r = FanSpin.Approach(0, 540, 270, 0.5);
            Assert.That(r, Is.EqualTo(135).Within(1e-9));
            Assert.That(FanSpin.Approach(530, 540, 270, 0.5), Is.EqualTo(540));
            Assert.That(FanSpin.Approach(540, 0, 270, 0.5), Is.EqualTo(405).Within(1e-9));
            Assert.That(FanSpin.Approach(10, 0, 270, 0.5), Is.EqualTo(0));
        }

        [Test]
        public void TheLampSeparatesSilenceFromStopped()
        {
            Assert.That(LampLook.Of(null), Is.EqualTo(LampState.NotReporting));
            Assert.That(LampLook.Of(new Dictionary<string, double> { { Wire.Running, 1 } }), Is.EqualTo(LampState.Running));
            Assert.That(LampLook.Of(new Dictionary<string, double> { { Wire.Running, 0 } }), Is.EqualTo(LampState.Stopped));
            Assert.That(LampLook.Of(new Dictionary<string, double> { { Wire.Power, 12 } }), Is.EqualTo(LampState.NotReporting), "a report with no running flag does not say it runs");

            var running = LampLook.Colour(LampState.Running);
            var stopped = LampLook.Colour(LampState.Stopped);
            var silent = LampLook.Colour(LampState.NotReporting);
            Assert.That(running.G, Is.GreaterThan(running.R));
            Assert.That(stopped.R, Is.GreaterThan(stopped.G + 0.5));
            Assert.That(silent.R, Is.GreaterThan(0.9f).And.LessThan(1.01f));
            Assert.That(silent.G, Is.GreaterThan(stopped.G + 0.3), "silence is not drawn in the colour of a stop");
            Assert.That(LampLook.Label(LampState.Stopped), Is.Not.EqualTo(LampLook.Label(LampState.NotReporting)));
        }

        [Test]
        public void OnlySilenceBlinks()
        {
            var lows = 0;
            for (double t = 0; t < 4; t += 0.1) if (LampLook.Brightness(LampState.NotReporting, t) < 0.5) lows++;
            Assert.That(lows, Is.GreaterThan(10), "a silent lamp goes dark part of the time");
            for (double t = 0; t < 4; t += 0.1)
            {
                Assert.That(LampLook.Brightness(LampState.Stopped, t), Is.EqualTo(1.0));
                Assert.That(LampLook.Brightness(LampState.Running, t), Is.EqualTo(1.0));
            }
        }

        [Test]
        public void TheTrippedChillerReadsAsNotReportingThenStopsMeaningIt()
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(16.4, 1));
            var chiller = OfflinePreview.ByLayout(plane, BuildingLayout.AnnexChiller);
            Assert.That(LampLook.Of(Readings.For(plane.Model, chiller)), Is.EqualTo(LampState.Running), "before the trip it runs and reports");
            plane.Day.AdvanceTo(TimeClock.At(0, 16, 40));
            Assert.That(Readings.For(plane.Model, chiller), Is.Null, "after the trip it sends nothing");
            Assert.That(LampLook.Of(Readings.For(plane.Model, chiller)), Is.EqualTo(LampState.NotReporting));
        }

        [Test]
        public void TheAcceleratedLabelAppearsWheneverTheModelIsFasterThanTheWall()
        {
            Assert.That(StoryText.Accelerated(1.0), Is.Null);
            Assert.That(StoryText.Accelerated(0.5), Is.Null);
            Assert.That(StoryText.Accelerated(2.0), Is.EqualTo("ACCELERATED x2"));
            Assert.That(StoryText.Accelerated(7.5), Is.EqualTo("ACCELERATED x7.5"));
            Assert.That(StoryText.Accelerated(60), Does.StartWith("ACCELERATED"));
            Assert.That(StoryText.Accelerated(double.NaN), Is.Null);
        }

        // ---- command cards: delivered is not done

        [Test]
        public void DeliveredIsNotDone()
        {
            var c = new CommandCard("cmd-1", "TP-VAV-T-F4-E", "boost-cooling");
            c.ApplyPlatformStatus("QUEUED", 10);
            Assert.That(c.Delivered, Is.False);
            Assert.That(c.Stage, Is.EqualTo(CardStage.Queued));

            c.ApplyPlatformStatus("SENT", 22);
            Assert.That(c.Delivered, Is.True, "SENT is delivery");
            Assert.That(c.Done, Is.False, "...and delivery is not the job");
            Assert.That(c.Terminal, Is.False);
            Assert.That(c.Stage, Is.EqualTo(CardStage.Delivered));
            Assert.That(c.Has(CardStage.Done), Is.False);

            c.ApplyPlatformStatus("SUCCESSFUL", 400);
            Assert.That(c.Done, Is.True);
            Assert.That(c.Stage, Is.EqualTo(CardStage.Done));
            Assert.That(c.AtS(CardStage.Delivered), Is.EqualTo(22), "the delivery time is kept");
            Assert.That(c.AtS(CardStage.Done), Is.EqualTo(400));
        }

        [Test]
        public void TheStagesCarryDifferentWords()
        {
            Assert.That(CommandCard.StageLabel(CardStage.Delivered), Is.Not.EqualTo(CommandCard.StageLabel(CardStage.Done)));
            Assert.That(CommandCard.StageGloss(CardStage.Delivered), Does.Contain("delivered"));
            Assert.That(CommandCard.StageGloss(CardStage.Done), Does.Contain("done"));
            Assert.That(CommandCard.StageGloss(CardStage.Delivered), Does.Not.Contain("done"));
        }

        [Test]
        public void ADeviceThatHasOnlyReceivedHasNotDoneIt()
        {
            var c = new CommandCard("cmd-2", "TP-CH-A", "restart");
            c.DeviceReceived(50);
            Assert.That(c.Delivered, Is.True);
            Assert.That(c.SourceOf(CardStage.Delivered), Is.EqualTo(FactSource.Device));
            Assert.That(c.Done, Is.False);
            c.DeviceAnswered(true, "", 90);
            Assert.That(c.Done, Is.True);
            Assert.That(c.SourceOf(CardStage.Done), Is.EqualTo(FactSource.Device));
        }

        [Test]
        public void StagesNeverGoBackwards()
        {
            var c = new CommandCard("cmd-3", "d", "n");
            c.ApplyPlatformStatus("SENT", 20);
            c.ApplyPlatformStatus("QUEUED", 30); // a stale poll
            Assert.That(c.Delivered, Is.True);
            Assert.That(c.Stage, Is.EqualTo(CardStage.Delivered));
            Assert.That(c.AtS(CardStage.Queued), Is.EqualTo(20), "the first word about a stage stands");
            c.ApplyPlatformStatus("SUCCESSFUL", 40);
            c.ApplyPlatformStatus("SENT", 50);
            c.ApplyPlatformStatus("QUEUED", 60);
            Assert.That(c.Done, Is.True);
            Assert.That(c.Stage, Is.EqualTo(CardStage.Done));
        }

        [Test]
        public void TheFirstTerminalWordStands()
        {
            var c = new CommandCard("cmd-4", "d", "n");
            c.ApplyPlatformStatus("SENT", 20);
            c.ApplyPlatformStatus("FAILED", 30);
            Assert.That(c.Outcome, Is.EqualTo(CardOutcome.Failed));
            Assert.That(c.Done, Is.False, "a failure is a finished command but not a done job");
            Assert.That(c.Terminal, Is.True);
            Assert.That(c.FailReason, Is.EqualTo("FAILED"));
            c.ApplyPlatformStatus("SUCCESSFUL", 40);
            c.DeviceAnswered(true, "", 41);
            Assert.That(c.Outcome, Is.EqualTo(CardOutcome.Failed), "a later success does not rewrite it");
        }

        [TestCase("TIMEOUT")]
        [TestCase("EXPIRED")]
        [TestCase("CANCELLED")]
        public void OtherEndingsAreFailuresNotSuccesses(string word)
        {
            var c = new CommandCard("cmd-5", "d", "n");
            c.ApplyPlatformStatus(word, 10);
            Assert.That(c.Outcome, Is.EqualTo(CardOutcome.Failed));
            Assert.That(c.FailReason, Is.EqualTo(word));
            Assert.That(c.Done, Is.False);
        }

        [Test]
        public void AWordNobodyKnowsChangesNothing()
        {
            var c = new CommandCard("cmd-6", "d", "n");
            c.ApplyPlatformStatus("TELEPORTED", 10);
            c.ApplyPlatformStatus(null, 11);
            Assert.That(c.Has(CardStage.Queued), Is.False);
            Assert.That(c.Terminal, Is.False);
        }

        [Test]
        public void EachStageRemembersWhoSaidIt()
        {
            var c = new CommandCard("cmd-7", "d", "n");
            c.ApplyPlatformStatus("QUEUED", 1, FactSource.Preview);
            c.DeviceReceived(5);
            c.ApplyPlatformStatus("SUCCESSFUL", 9, FactSource.Platform);
            Assert.That(c.SourceOf(CardStage.Queued), Is.EqualTo(FactSource.Preview));
            Assert.That(c.SourceOf(CardStage.Delivered), Is.EqualTo(FactSource.Device));
            Assert.That(c.SourceOf(CardStage.Done), Is.EqualTo(FactSource.Platform));
            Assert.That(FactLabels.Tag(FactSource.Device), Does.Contain("simulated"));
        }

        [Test]
        public void TheBoardKeepsOneCardPerIdAcrossWitnesses()
        {
            var board = new CommandBoard();
            var ledger = new CommandLedger();
            board.ApplyPlatform("cmd-9", "TP-VAV-T-F4-E", "boost-cooling", "QUEUED", 10);
            ledger.Received("cmd-9", "TP-VAV-T-F4-E", "boost-cooling", 22);
            board.Sync(ledger, 22);
            Assert.That(board.Cards.Count, Is.EqualTo(1));
            var c = board.Get("cmd-9");
            Assert.That(c.Delivered, Is.True);
            Assert.That(c.Done, Is.False, "the device has it; the job is not done");

            ledger.Answered("cmd-9", true, "", 300);
            board.Sync(ledger, 300);
            board.ApplyPlatform("cmd-9", "TP-VAV-T-F4-E", "boost-cooling", "SUCCESSFUL", 301);
            Assert.That(board.Cards.Count, Is.EqualTo(1));
            Assert.That(board.Get("cmd-9"), Is.SameAs(c), "the same card, the same id");
            Assert.That(c.Done, Is.True);
            Assert.That(board.Latest("TP-VAV-T-F4-E"), Is.SameAs(c));
            Assert.That(board.Latest("TP-CH-A"), Is.Null);
        }

        [Test]
        public void ARedeliveredCommandIsOneCommand()
        {
            var ledger = new CommandLedger();
            var a = ledger.Received("cmd-10", "d", "restart", 5);
            var b = ledger.Received("cmd-10", "d", "restart", 9);
            Assert.That(b, Is.SameAs(a));
            Assert.That(ledger.Records.Count, Is.EqualTo(1));
            ledger.Answered("cmd-10", true, "", 20);
            ledger.Answered("cmd-10", false, "late", 30);
            Assert.That(a.Success, Is.True, "the first answer stands");
        }

        // ---- alarms

        [Test]
        public void AnAlarmRaisesOnceAndClears()
        {
            var b = new AlarmBoard();
            var a = b.Raise("TP-VAV-T-F4-E", "zone-hot", "MAJOR", FactSource.Preview, 100);
            Assert.That(b.Raise("TP-VAV-T-F4-E", "zone-hot", "MAJOR", FactSource.Preview, 130), Is.SameAs(a));
            Assert.That(b.Facts.Count, Is.EqualTo(1));
            Assert.That(b.Headline(), Is.SameAs(a));
            b.Clear("TP-VAV-T-F4-E", "zone-hot", 900);
            Assert.That(a.Active, Is.False);
            Assert.That(a.ClearedAtS, Is.EqualTo(900));
            var again = b.Raise("TP-VAV-T-F4-E", "zone-hot", "MAJOR", FactSource.Preview, 1200);
            Assert.That(again, Is.Not.SameAs(a), "a new episode is a new alarm");
        }

        // ---- the offline preview closes the story's two loops, tagged as a preview

        [Test]
        public void ThePreviewBoostIsDeliveredLongBeforeItIsDone()
        {
            var (plane, board, alarms, ledger, preview) = Preview(13.9);
            Run(plane, preview, board, ledger, 13.9 * 3600, TimeClock.At(0, 15, 30), () => board.Cards.Any(c => c.Name == Wire.BoostCooling && c.Done));
            var card = board.Cards.FirstOrDefault(c => c.Name == Wire.BoostCooling);
            Assert.That(card, Is.Not.Null, "the hot meeting room raised a boost");
            Assert.That(card.Done, Is.True, "and it finished");
            Assert.That(card.Outcome, Is.EqualTo(CardOutcome.Successful));
            Assert.That(card.Has(CardStage.Queued) && card.Has(CardStage.Delivered), Is.True);
            Assert.That(card.AtS(CardStage.Queued), Is.LessThan(card.AtS(CardStage.Delivered)));
            Assert.That(card.AtS(CardStage.Done), Is.GreaterThan(card.AtS(CardStage.Delivered) + 60), "the job took real time after delivery");
            Assert.That(card.SourceOf(CardStage.Queued), Is.EqualTo(FactSource.Preview), "no platform is connected, and the card says so");
            var alarm = alarms.Find("TP-VAV-T-F4-E", "zone-hot");
            Assert.That(alarm, Is.Not.Null);
            Assert.That(alarm.Source, Is.EqualTo(FactSource.Preview));
        }

        [Test]
        public void ThePreviewRestartFollowsTheAbsenceAlarmWithTheSameIdThroughout()
        {
            var (plane, board, alarms, ledger, preview) = Preview(16.4);
            Run(plane, preview, board, ledger, 16.4 * 3600, TimeClock.At(0, 17, 30), () => board.Cards.Any(c => c.Name == Wire.Restart && c.Done));
            var silent = alarms.Find("TP-CH-A", "chiller-silent");
            Assert.That(silent, Is.Not.Null, "two silent minutes raised the alarm");
            Assert.That(silent.Severity, Is.EqualTo("CRITICAL"));
            Assert.That(board.Cards.Count(c => c.Name == Wire.Restart), Is.EqualTo(1), "one restart, one id");
            var card = board.Cards.First(c => c.Name == Wire.Restart);
            Assert.That(card.Done, Is.True);
            Assert.That(card.AtS(CardStage.Delivered), Is.LessThan(card.AtS(CardStage.Done)));
            Assert.That(card.AtS(CardStage.Queued), Is.GreaterThan(silent.RaisedAtS - 1), "the restart is the answer to the alarm, not before it");
        }

        static (TowerPlane, CommandBoard, AlarmBoard, CommandLedger, OfflinePreview) Preview(double startHour)
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(startHour, 1));
            var board = new CommandBoard();
            var alarms = new AlarmBoard();
            var ledger = new CommandLedger();
            return (plane, board, alarms, ledger, new OfflinePreview(plane, board, alarms, ledger));
        }

        static void Run(TowerPlane plane, OfflinePreview preview, CommandBoard board, CommandLedger ledger, double fromSimS, double toSimS, Func<bool> stop)
        {
            for (double wall = 0; fromSimS + wall < toSimS; wall += 5)
            {
                plane.Advance(wall);
                preview.Step();
                board.Sync(ledger, plane.Model.TimeS);
                if (stop()) return;
            }
        }

        // ---- the shot file

        const string Good = @"{ ""version"": 1, ""shots"": [
          { ""id"": ""a"", ""beat"": ""b0"", ""caption"": ""one"", ""startS"": 0, ""durationS"": 8, ""cards"": [""zone""],
            ""from"": { ""pos"": [0, 10, -30], ""look"": [0, 5, 0], ""fov"": 40 },
            ""to"":   { ""pos"": [10, 10, -20], ""look"": [0, 5, 0], ""fov"": 30 } },
          { ""id"": ""b"", ""startS"": 8, ""durationS"": 4, ""ease"": ""linear"",
            ""from"": { ""pos"": [1, 1, 1], ""look"": [2, 2, 2], ""fov"": 50 },
            ""to"":   { ""pos"": [3, 3, 3], ""look"": [2, 2, 2], ""fov"": 50 } } ] }";

        static ShotFile.Result Bad(string json) { return ShotFile.Parse(json); }

        [Test]
        public void AGoodShotFileParses()
        {
            var r = ShotFile.Parse(Good);
            Assert.That(r.Ok, Is.True, r.Report());
            Assert.That(r.List.Shots.Count, Is.EqualTo(2));
            Assert.That(r.List.Shots[0].Cards, Is.EqualTo(new[] { "zone" }));
            Assert.That(r.List.Shots[0].Smooth, Is.True, "smooth is the default");
            Assert.That(r.List.Shots[1].Smooth, Is.False);
            Assert.That(r.List.TotalS, Is.EqualTo(12));
            Assert.That(r.List.Shots[0].From.Fov, Is.EqualTo(40));
        }

        [Test]
        public void ThePoseInterpolatesAndHoldsAcrossAGap()
        {
            var r = ShotFile.Parse(Good.Replace("\"startS\": 8", "\"startS\": 10"));
            Assert.That(r.Ok, Is.True, r.Report());
            Assert.That(r.List.TryPose(0, out var p0, out var s0), Is.True);
            Assert.That(p0.Px, Is.EqualTo(0));
            Assert.That(s0.Id, Is.EqualTo("a"));
            r.List.TryPose(8, out var pEnd, out _);
            Assert.That(pEnd.Px, Is.EqualTo(10).Within(1e-9));
            r.List.TryPose(9, out var pGap, out var sGap);
            Assert.That(pGap.Px, Is.EqualTo(10).Within(1e-9), "in the gap the camera holds the last pose");
            Assert.That(sGap.Id, Is.EqualTo("a"));
            r.List.TryPose(4, out var pMid, out _);
            Assert.That(pMid.Px, Is.EqualTo(5).Within(1e-9), "smooth easing is symmetric about the middle");
            Assert.That(r.List.TryPose(100, out var pAfter, out var sAfter), Is.True);
            Assert.That(sAfter.Id, Is.EqualTo("b"));
            Assert.That(pAfter.Px, Is.EqualTo(3));
        }

        [Test]
        public void NothingPlaysBeforeTheFirstShot()
        {
            var r = ShotFile.Parse(Good.Replace("\"startS\": 0", "\"startS\": 2").Replace("\"startS\": 8", "\"startS\": 10"));
            Assert.That(r.Ok, Is.True, r.Report());
            Assert.That(r.List.TryPose(1, out _, out var s), Is.False);
            Assert.That(s, Is.Null);
        }

        [Test]
        public void BrokenJsonIsAnErrorNotAnEmptyList()
        {
            var r = Bad("{ \"version\": 1, \"shots\": [ ");
            Assert.That(r.Ok, Is.False);
            Assert.That(r.Report(), Does.Contain("not valid JSON"));
            Assert.That(Bad("[]").Ok, Is.False);
            Assert.That(Bad("").Ok, Is.False);
        }

        [Test]
        public void EachViolationIsNamed()
        {
            string Mut(string from, string to) { Assert.That(Good, Does.Contain(from), "the test's own edit must change something"); return Good.Replace(from, to); }

            Assert.That(Bad(Mut("\"version\": 1", "\"version\": 2")).Report(), Does.Contain("version"));
            Assert.That(Bad(Mut("\"id\": \"b\"", "\"id\": \"a\"")).Report(), Does.Contain("used twice"));
            Assert.That(Bad(Mut("\"startS\": 8", "\"startS\": 6")).Report(), Does.Contain("before"));
            Assert.That(Bad(Mut("\"durationS\": 8", "\"durationS\": 0")).Report(), Does.Contain("greater than zero"));
            Assert.That(Bad(Mut("\"fov\": 40", "\"fov\": 5")).Report(), Does.Contain("fov"));
            Assert.That(Bad(Mut("\"fov\": 40", "\"fov\": 140")).Report(), Does.Contain("fov"));
            Assert.That(Bad(Mut("[\"zone\"]", "[\"minimap\"]")).Report(), Does.Contain("unknown card"));
            Assert.That(Bad(Mut("[\"zone\"]", "[\"zone\", \"zone\"]")).Report(), Does.Contain("twice"));
            Assert.That(Bad(Mut("\"ease\": \"linear\"", "\"ease\": \"bouncy\"")).Report(), Does.Contain("ease"));
            Assert.That(Bad(Mut("\"look\": [0, 5, 0], \"fov\": 40", "\"look\": [0, 10, -30], \"fov\": 40")).Report(), Does.Contain("own position"));
            Assert.That(Bad(Mut("\"pos\": [0, 10, -30]", "\"pos\": [0, 10]")).Report(), Does.Contain("three numbers"));
            Assert.That(Bad(Mut("\"startS\": 0", "\"startS\": -1")).Report(), Does.Contain("negative"));
        }

        [Test]
        public void AMisspelledKeyIsRefusedNotIgnored()
        {
            Assert.That(Bad(Good.Replace("\"durationS\": 8", "\"duration\": 8")).Ok, Is.False, "a missing durationS");
            var extra = Good.Replace("\"caption\": \"one\"", "\"captoin\": \"one\"");
            var r = Bad(extra);
            Assert.That(r.Ok, Is.False);
            Assert.That(r.Report(), Does.Contain("unknown key \"captoin\""));
            Assert.That(Bad(Good.Replace("\"version\": 1,", "\"version\": 1, \"verison\": 1,")).Report(), Does.Contain("unknown key"));
        }

        [Test]
        public void DuplicateKeysAndTrailingTextAreRefused()
        {
            Assert.That(Bad(Good.Replace("\"version\": 1,", "\"version\": 1, \"version\": 1,")).Report(), Does.Contain("duplicate key"));
            Assert.That(Bad(Good + " x").Ok, Is.False);
            Assert.That(Bad("{ \"version\": 1, \"shots\": [] }").Report(), Does.Contain("empty"));
        }

        [Test]
        public void TheCommittedShotFileIsValidAndCoversTheStoryboardBeats()
        {
            var path = FindData("shots.json");
            Assert.That(path, Is.Not.Null, "Assets/Towerpulse/Data/shots.json must exist");
            var r = ShotFile.Parse(File.ReadAllText(path));
            Assert.That(r.Ok, Is.True, r.Report());
            var ids = r.List.Shots.Select(s => s.Id).ToList();
            Assert.That(ids, Does.Contain("dusk-establishing"));
            Assert.That(ids, Does.Contain("f4-east-push-in"));
            Assert.That(ids, Does.Contain("chiller-close-up"));
            Assert.That(ids, Does.Contain("pull-back"));
            Assert.That(ids.IndexOf("dusk-establishing"), Is.LessThan(ids.IndexOf("f4-east-push-in")));
            Assert.That(ids.IndexOf("chiller-close-up"), Is.LessThan(ids.IndexOf("pull-back")));
            Assert.That(r.List.TotalS, Is.LessThanOrEqualTo(90.0), "the storyboard is ninety seconds");
            Assert.That(r.List.Shots.Single(s => s.Id == "f4-east-push-in").Cards, Does.Contain("command"));
            Assert.That(r.List.Shots.Single(s => s.Id == "chiller-close-up").Cards, Does.Contain("chiller"));
        }

        internal static string FindData(string name)
        {
            foreach (var start in new[] { Directory.GetCurrentDirectory(), AppContext.BaseDirectory })
            {
                var dir = new DirectoryInfo(start);
                while (dir != null)
                {
                    var p = Path.Combine(dir.FullName, "Assets", "Towerpulse", "Data", name);
                    if (File.Exists(p)) return p;
                    var q = Path.Combine(dir.FullName, "demos", "towerpulse-unity", "Assets", "Towerpulse", "Data", name);
                    if (File.Exists(q)) return q;
                    dir = dir.Parent;
                }
            }
            return null;
        }
    }
}
