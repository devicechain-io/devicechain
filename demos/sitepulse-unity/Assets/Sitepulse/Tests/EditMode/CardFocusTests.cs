// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Linq;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Replay;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>Which devices carry a card: the rules, the cap, the order, the linger and the size hysteresis.</summary>
    public sealed class CardSelectionTests
    {
        static CardInput Idle(string id, float share = 1f, bool visible = true) => new CardInput(id, visible, share, 0, false);
        static CardInput Alarm(string id, string severity = "MAJOR", float share = 1f) => new CardInput(id, true, share, CardSelection.AlarmRank(severity), false);
        static CardInput Cmd(string id, bool inFlight = true, float share = 1f) => new CardInput(id, true, share, 0, inFlight);

        static List<string> Run(CardSelection s, double now, string selected, params CardInput[] inputs)
        {
            var chosen = new List<string>();
            s.Choose(now, inputs, selected, chosen);
            return chosen;
        }

        [Test]
        public void NothingIsShownForAnIdleFleet() =>
            CollectionAssert.IsEmpty(Run(new CardSelection(), 0, null, Idle("A"), Idle("B")));

        [Test]
        public void AnActiveAlarmShowsACard() =>
            CollectionAssert.AreEqual(new[] { "B" }, Run(new CardSelection(), 0, null, Idle("A"), Alarm("B")));

        [Test]
        public void ADeviceUnderACommandShowsACardAndTheSelectedOneAlwaysDoes()
        {
            var s = new CardSelection();
            CollectionAssert.AreEqual(new[] { "A" }, Run(s, 0, null, Cmd("A"), Idle("B")));
            CollectionAssert.AreEqual(new[] { "B", "A" }, Run(new CardSelection(), 0, "B", Cmd("A"), Idle("B")));
        }

        [Test]
        public void ThePlantFollowsTheSameRules()
        {
            var s = new CardSelection();
            CollectionAssert.IsEmpty(Run(s, 0, null, Idle("SP-PL-0001")));
            CollectionAssert.AreEqual(new[] { "SP-PL-0001" }, Run(s, 1, null, Alarm("SP-PL-0001")));
            CollectionAssert.AreEqual(new[] { "SP-PL-0001" }, Run(new CardSelection(), 0, "SP-PL-0001", Idle("SP-PL-0001")));
        }

        [Test]
        public void ADeviceThatCannotBePointedAtGetsNoCardEvenWhenSelected()
        {
            var chosen = Run(new CardSelection(), 0, "A", new CardInput("A", false, 1f, 0, false), new CardInput("B", false, 1f, 4, false));
            CollectionAssert.IsEmpty(chosen);
        }

        [Test]
        public void AFinishedCommandKeepsItsCardForFiveSecondsAndThenLosesIt()
        {
            var s = new CardSelection();
            Assert.AreEqual(1, Run(s, 10.0, null, Cmd("A", inFlight: true)).Count);
            // the platform says SUCCESSFUL at 12 s: no longer in flight
            Assert.AreEqual(1, Run(s, 12.0, null, Cmd("A", inFlight: false)).Count, "just finished");
            Assert.AreEqual(1, Run(s, 14.9, null, Cmd("A", inFlight: false)).Count, "still inside the linger");
            Assert.AreEqual(1, Run(s, 15.0, null, Cmd("A", inFlight: false)).Count, "5 s after the last time it was in flight");
            Assert.AreEqual(0, Run(s, 15.2, null, Cmd("A", inFlight: false)).Count, "gone");
        }

        [Test]
        public void ALingerRunsFromTheLastTimeTheCommandWasInFlight()
        {
            var s = new CardSelection();
            Run(s, 0, null, Cmd("A", true));
            Run(s, 3, null, Cmd("A", true));
            Assert.AreEqual(1, Run(s, 7.9, null, Cmd("A", false)).Count);
            Assert.AreEqual(0, Run(s, 8.1, null, Cmd("A", false)).Count);
        }

        [Test]
        public void ACommandThatWasNeverInFlightNeverShows()
        {
            // an old terminal command read at start-up
            Assert.AreEqual(0, Run(new CardSelection(), 100, null, Cmd("A", false)).Count);
        }

        [Test]
        public void PriorityIsSelectedThenAlarmsWorstFirstThenCommands()
        {
            var s = new CardSelection { MaxCards = 6 };
            var chosen = Run(s, 0, "S", Cmd("C"), Alarm("MIN", "MINOR"), Alarm("CRIT", "CRITICAL"), Idle("S"), Alarm("MAJ", "MAJOR"));
            CollectionAssert.AreEqual(new[] { "S", "CRIT", "MAJ", "MIN", "C" }, chosen);
        }

        [Test]
        public void MajorOutranksMinorAndAnUnknownSeverityIsLast()
        {
            Assert.Greater(CardSelection.AlarmRank("MAJOR"), CardSelection.AlarmRank("MINOR"));
            Assert.Greater(CardSelection.AlarmRank("minor"), CardSelection.AlarmRank("WARNING"));
            Assert.Greater(CardSelection.AlarmRank("WARNING"), CardSelection.AlarmRank(null));
            Assert.AreEqual(CardSelection.AlarmRank(null), CardSelection.AlarmRank("WHATEVER"));
            Assert.Greater(CardSelection.AlarmRank(null), 0, "an alarm of any kind is still an alarm");
        }

        [Test]
        public void AtMostFourCardsShowAndTheLowestPrioritiesAreDropped()
        {
            var s = new CardSelection();
            var chosen = Run(s, 0, null, Alarm("A", "MINOR"), Alarm("B", "MAJOR"), Cmd("C"), Alarm("D", "MAJOR"), Alarm("E", "MINOR"), Cmd("F"));
            Assert.AreEqual(4, chosen.Count);
            CollectionAssert.AreEquivalent(new[] { "B", "D", "A", "E" }, chosen);
            Assert.AreEqual(4, Run(s, 1, "F", Alarm("A"), Alarm("B"), Alarm("C"), Alarm("D"), Alarm("E"), Idle("F")).Count);
            Assert.AreEqual("F", Run(s, 2, "F", Alarm("A"), Alarm("B"), Alarm("C"), Alarm("D"), Alarm("E"), Idle("F"))[0], "the selected one is never the one dropped");
        }

        [Test]
        public void EqualPrioritiesNeverSwapBackAndForth()
        {
            var s = new CardSelection { MaxCards = 1 };
            // ordinal order would favour A, but B was there first
            Assert.AreEqual("B", Run(s, 0, null, Alarm("B"))[0]);
            for (int i = 1; i < 200; i++)
                Assert.AreEqual("B", Run(s, i * 0.05, null, Alarm("A"), Alarm("B"))[0], "frame " + i);
        }

        [Test]
        public void EqualPrioritiesAreDecidedTheSameWayEveryTimeWhenNoneWasShown()
        {
            var first = Run(new CardSelection { MaxCards = 2 }, 0, null, Alarm("C"), Alarm("A"), Alarm("B"));
            var second = Run(new CardSelection { MaxCards = 2 }, 0, null, Alarm("B"), Alarm("C"), Alarm("A"));
            CollectionAssert.AreEqual(first, second);
            CollectionAssert.AreEqual(new[] { "A", "B" }, first);
        }

        [Test]
        public void AMachineNeedsTheMinimumShareToAppearAndKeepsItDownToEightyPercentOfIt()
        {
            var s = new CardSelection { MinShare = 0.35f };
            Assert.AreEqual(0, Run(s, 0, null, Alarm("A", share: 0.34f)).Count, "below the threshold: never shown");
            Assert.AreEqual(1, Run(s, 1, null, Alarm("A", share: 0.36f)).Count, "appears");
            Assert.AreEqual(1, Run(s, 2, null, Alarm("A", share: 0.34f)).Count, "inside the hysteresis band: stays");
            Assert.AreEqual(1, Run(s, 3, null, Alarm("A", share: 0.29f)).Count, "still");
            Assert.AreEqual(0, Run(s, 4, null, Alarm("A", share: 0.27f)).Count, "below 80% of the threshold: dropped");
            Assert.AreEqual(0, Run(s, 5, null, Alarm("A", share: 0.34f)).Count, "and it must reach the threshold again to return");
            Assert.AreEqual(1, Run(s, 6, null, Alarm("A", share: 0.35f)).Count);
        }

        [Test]
        public void ThePolicyDoesNotFlickerAtTheThreshold()
        {
            var s = new CardSelection { MinShare = 0.35f };
            Run(s, 0, null, Alarm("A", share: 0.40f));
            int changes = 0;
            int last = 1;
            for (int i = 1; i < 100; i++)
            {
                float share = 0.35f + (i % 2 == 0 ? 0.01f : -0.01f);   // wobbling across the threshold
                int n = Run(s, i * 0.05, null, Alarm("A", share: share)).Count;
                if (n != last) changes++;
                last = n;
            }

            Assert.AreEqual(0, changes);
        }

        [Test]
        public void TheSelectedMachineIgnoresTheSizeRule() =>
            CollectionAssert.AreEqual(new[] { "A" }, Run(new CardSelection(), 0, "A", Idle("A", share: 0.01f)));

        [Test]
        public void ResetForgetsWhatWasShownAndEveryLinger()
        {
            var s = new CardSelection();
            Run(s, 0, null, Cmd("A", true));
            s.Reset();
            Assert.AreEqual(0, Run(s, 1, null, Cmd("A", false)).Count);
            CollectionAssert.IsEmpty(s.Shown);
        }
    }

    /// <summary>Where a card sits: kept until its slot has been unusable for half a second, then moved by a glide, and faded.</summary>
    public sealed class CardSlotTests
    {
        const float Dt = 1f / 60f;

        static CardSlot Placed(Vector2? at = null)
        {
            var s = new CardSlot();
            Assert.IsTrue(s.Observe(false, Dt), "a card with no slot needs one");
            s.Assign(at ?? new Vector2(100f, 50f), new Vector2(60f, 40f), false);
            return s;
        }

        static bool Blocked(CardSlot s, float seconds)
        {
            bool move = false;
            for (float t = 0; t < seconds - 1e-4f; t += Dt) move = s.Observe(false, Dt);
            return move;
        }

        [Test]
        public void ASlotBlockedForThreeTenthsOfASecondIsKept() => Assert.IsFalse(Blocked(Placed(), 0.3f));

        [Test]
        public void ASlotBlockedForSixTenthsOfASecondIsGivenUp() => Assert.IsTrue(Blocked(Placed(), 0.6f));

        [Test]
        public void AValidFrameBreaksTheCount()
        {
            var s = Placed();
            Assert.IsFalse(Blocked(s, 0.4f));
            Assert.IsFalse(s.Observe(true, Dt));
            Assert.AreEqual(0f, s.InvalidFor);
            Assert.IsFalse(Blocked(s, 0.4f), "0.4 s twice with a good frame between is not 0.5 s continuously");
        }

        [Test]
        public void TheHoldIsExactlyHalfASecond()
        {
            var s = Placed();
            Assert.IsFalse(s.Observe(false, 0.5f));
            Assert.IsTrue(s.Observe(false, 0.01f));
        }

        [Test]
        public void ARelayoutIsLetThroughAtMostTwiceASecond()
        {
            var gate = new SolveGate();
            int taken = 0, inFirstSecond = 0;
            for (int i = 0; i < 180; i++)
            {
                gate.Tick(Dt);
                if (gate.TryTake())
                {
                    taken++;
                    if (i < 60) inFirstSecond++;
                }
            }

            Assert.LessOrEqual(inFirstSecond, 3, "the first one is immediate, then every half second");
            Assert.LessOrEqual(taken, 7);
            Assert.GreaterOrEqual(taken, 5);
        }

        [Test]
        public void AGlideNeverMovesACardMoreThanTheSmoothingAllowsInAFrame()
        {
            var s = Placed(new Vector2(0f, 0f));
            for (int i = 0; i < 120; i++) s.Step(Dt, true, false, false);
            var from = s.Display;
            var to = new Vector2(400f, -150f);
            s.Assign(to, to + new Vector2(30f, 30f), false);
            Assert.AreEqual(from.x, s.Display.x, 1e-3f, "assigning a slot does not move the card");
            float total = Vector2.Distance(from, to), prev = 0f;
            var last = s.Display;
            for (int i = 0; i < 90; i++)
            {
                s.Step(Dt, true, false, false);
                float step = Vector2.Distance(s.Display, last);
                Assert.LessOrEqual(step, total * 0.06f + 1e-3f, "frame " + i);
                Assert.LessOrEqual(step, CardTiming.GlideMaxSpeed * Dt + 1e-3f);
                float along = Vector2.Distance(from, s.Display);
                Assert.GreaterOrEqual(along, prev - 1e-3f, "it never goes backwards");
                prev = along;
                last = s.Display;
            }

            Assert.Less(Vector2.Distance(s.Display, to), 2f, "and it arrives within about a second");
        }

        [Test]
        public void AGlideSurvivesAHitchWithoutJumping()
        {
            var s = Placed(new Vector2(0f, 0f));
            s.Step(Dt, true, false, false);
            s.Assign(new Vector2(500f, 0f), new Vector2(530f, 30f), false);
            var before = s.Display;
            s.Step(0.1f, true, false, false);
            Assert.LessOrEqual(Vector2.Distance(before, s.Display), CardTiming.GlideMaxSpeed * 0.1f + 1e-3f);
            Assert.Less(s.Display.x, 500f);
        }

        [Test]
        public void TheLeadersBendGlidesWithTheCard()
        {
            var s = Placed(new Vector2(0f, 0f));
            s.Step(Dt, true, false, false);
            s.Assign(new Vector2(300f, 0f), new Vector2(340f, -20f), false);
            for (int i = 0; i < 20; i++) s.Step(Dt, true, false, false);
            Assert.Greater(s.DisplayElbow.x, 60f, "moving");
            Assert.Less(s.DisplayElbow.x, 340f, "not there yet");
            for (int i = 0; i < 120; i++) s.Step(Dt, true, false, false);
            Assert.AreEqual(340f, s.DisplayElbow.x, 1f);
            Assert.AreEqual(-20f, s.DisplayElbow.y, 1f);
        }

        [Test]
        public void ACardFadesInOverAQuarterOfASecondAndOutAgain()
        {
            var s = Placed();
            Assert.AreEqual(0f, s.Alpha);
            for (int i = 0; i < 7; i++) s.Step(Dt, true, false, false);
            Assert.That(s.Alpha, Is.InRange(0.3f, 0.6f), "half way after about 7 frames");
            for (int i = 0; i < 10; i++) s.Step(Dt, true, false, false);
            Assert.AreEqual(1f, s.Alpha);
            Assert.IsFalse(s.Gone(true));
            for (int i = 0; i < 7; i++) s.Step(Dt, false, false, false);
            Assert.That(s.Alpha, Is.InRange(0.4f, 0.7f));
            Assert.IsFalse(s.Gone(false), "still fading");
            for (int i = 0; i < 12; i++) s.Step(Dt, false, false, false);
            Assert.AreEqual(0f, s.Alpha);
            Assert.IsTrue(s.Gone(false));
        }

        [Test]
        public void ANewCardAppearsAtItsSlotAndANoLongerWantedOneKeepsItsPlaceWhileItFades()
        {
            var s = Placed(new Vector2(30f, 40f));
            Assert.AreEqual(30f, s.Display.x);
            for (int i = 0; i < 30; i++) s.Step(Dt, true, false, false);
            for (int i = 0; i < 5; i++) s.Step(Dt, false, false, false);
            Assert.AreEqual(30f, s.Display.x, 1e-3f);
        }

        [Test]
        public void ReducedMotionIsAShortCrossFadeNotAGlide()
        {
            var s = Placed(new Vector2(0f, 0f));
            for (int i = 0; i < 30; i++) s.Step(Dt, true, true, false);
            Assert.AreEqual(1f, s.Alpha);
            s.Assign(new Vector2(300f, 0f), new Vector2(330f, 0f), true);
            Assert.AreEqual(300f, s.Display.x, "it is at the new place at once, faded out");
            Assert.AreEqual(0f, s.Alpha);
            int frames = 0;
            while (s.Alpha < 1f && frames < 100)
            {
                s.Step(Dt, true, true, false);
                frames++;
            }

            Assert.LessOrEqual(frames, Mathf.CeilToInt(CardTiming.ReducedFadeSeconds / Dt) + 1);
            Assert.Less(CardTiming.ReducedFadeSeconds, CardTiming.FadeSeconds);
        }

        [Test]
        public void ReducedMotionNeverGlides()
        {
            var s = Placed(new Vector2(0f, 0f));
            s.Step(Dt, true, true, false);
            s.Assign(new Vector2(300f, 0f), new Vector2(330f, 0f), true);
            for (int i = 0; i < 10; i++)
            {
                s.Step(Dt, true, true, false);
                Assert.AreEqual(300f, s.Display.x);
            }
        }

        [Test]
        public void ASnapStepShowsTheCardAtOnce()
        {
            var s = Placed();
            s.Step(0f, true, false, true);
            Assert.AreEqual(1f, s.Alpha);
        }

        [Test]
        public void AReleasedCardNeedsANewSlot()
        {
            var s = Placed();
            s.Release();
            Assert.IsFalse(s.HasSlot);
            Assert.IsTrue(s.Observe(true, Dt));
        }
    }

    /// <summary>A click on the scene lands on the machine whose box the ray enters first.</summary>
    public sealed class CardPickingTests
    {
        static readonly List<(string id, Bounds bounds)> Fleet = new List<(string, Bounds)>
        {
            ("SP-HL-0001", new Bounds(new Vector3(0f, 2f, 0f), new Vector3(8f, 4f, 4f))),
            ("SP-HL-0002", new Bounds(new Vector3(30f, 2f, 0f), new Vector3(8f, 4f, 4f))),
            ("SP-PL-0001", new Bounds(new Vector3(0f, 2f, 40f), new Vector3(9f, 6f, 9f))),
        };

        [Test]
        public void ARayDownOntoAMachineLandsOnThatMachine()
        {
            Assert.AreEqual("SP-HL-0001", CardPicking.Pick(new Ray(new Vector3(1f, 30f, 0.5f), Vector3.down), Fleet, out var d));
            Assert.AreEqual(25.4f, d, 0.01f, "from 30 m to the top of a box 4 m tall, less the click slop");
            Assert.AreEqual("SP-HL-0002", CardPicking.Pick(new Ray(new Vector3(29f, 30f, 0f), Vector3.down), Fleet, out _));
        }

        [Test]
        public void ARayThatMissesEverythingPicksNothing()
        {
            Assert.IsNull(CardPicking.Pick(new Ray(new Vector3(15f, 30f, 0f), Vector3.down), Fleet, out var d));
            Assert.IsTrue(float.IsPositiveInfinity(d));
        }

        [Test]
        public void TheNearerMachineWinsWhenTwoLineUpAlongTheRay()
        {
            var ray = new Ray(new Vector3(0f, 2f, -60f), Vector3.forward);
            Assert.AreEqual("SP-HL-0001", CardPicking.Pick(ray, Fleet, out _), "the truck is in front of the crusher");
            Assert.AreEqual("SP-PL-0001", CardPicking.Pick(new Ray(new Vector3(0f, 2f, 20f), Vector3.forward), Fleet, out _), "past the truck, the crusher");
        }

        [Test]
        public void AClickJustOutsideTheBoxStillCounts()
        {
            // the box ends at x=4; a click a little beyond it is still on the machine
            Assert.AreEqual("SP-HL-0001", CardPicking.Pick(new Ray(new Vector3(4.4f, 30f, 0f), Vector3.down), Fleet, out _));
            Assert.IsNull(CardPicking.Pick(new Ray(new Vector3(5f, 30f, 0f), Vector3.down), Fleet, out _));
        }

        [Test]
        public void ARayFromAnAngleMapsToTheMachineItPointsAt()
        {
            var from = new Vector3(-20f, 20f, -20f);
            var toward = new Vector3(30f, 2f, 0f) - from;
            Assert.AreEqual("SP-HL-0002", CardPicking.Pick(new Ray(from, toward.normalized), Fleet, out _));
        }

        [Test]
        public void ABoxWithNoSizeIsNeverPicked()
        {
            var list = new List<(string id, Bounds bounds)> { ("X", new Bounds(Vector3.zero, Vector3.zero)) };
            Assert.IsNull(CardPicking.Pick(new Ray(new Vector3(0f, 5f, 0f), Vector3.down), list, out _));
        }
    }

    /// <summary>A shot names the machine whose card shows; a follow camera's target counts as selected.</summary>
    public sealed class ShotFocusTests
    {
        static string File(string camera, string focus = "") =>
            @"{""fps"":30,""shots"":[{""name"":""s"",""startEvent"":{""kind"":""alarm"",""key"":""low-fuel"",""state"":""ACTIVE"",""device"":""SP-HL-0006""},""duration"":5," + focus + @"""camera"":" + camera + "}]}";

        [Test]
        public void AFollowCamerasTargetIsTheSelectedMachine() =>
            Assert.AreEqual("SP-HL-0006", ShotFile.Parse(File(@"{""rig"":""follow"",""target"":""SP-HL-0006""}")).Shots[0].Selected);

        [Test]
        public void AFocusOverridesTheFollowTargetAndWorksWithAnyCamera()
        {
            Assert.AreEqual("SP-HL-0003", ShotFile.Parse(File(@"{""rig"":""follow"",""target"":""SP-HL-0006""}", @"""focus"":""SP-HL-0003"",")).Shots[0].Selected);
            Assert.AreEqual("SP-HL-0003", ShotFile.Parse(File(@"{""rig"":""fixed"",""pos"":[0,10,0],""lookAt"":[0,0,0]}", @"""focus"":""SP-HL-0003"",")).Shots[0].Selected);
        }

        [Test]
        public void ACameraThatFollowsNothingSelectsNothing() =>
            Assert.IsNull(ShotFile.Parse(File(@"{""rig"":""fixed"",""pos"":[0,10,0],""lookAt"":[0,0,0]}")).Shots[0].Selected);
    }
}
