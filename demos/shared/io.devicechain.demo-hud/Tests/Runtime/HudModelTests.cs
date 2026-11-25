// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using NUnit.Framework;

namespace DeviceChain.Demos.Hud.Tests
{
    public class CaptionBoxTests
    {
        static CaptionContent C(string text, string kicker = "PLACE", CaptionTone tone = CaptionTone.Platform) => new CaptionContent(kicker, text, "", tone);

        [Test]
        public void ACaptionFadesInAndANewOneNeverSwapsInPlace()
        {
            var box = new CaptionBoxState();
            box.Set(C("First."), 10); Assert.That(box.Alpha, Is.EqualTo(0)); Assert.That(box.Shown.Text, Is.EqualTo("First."));
            box.Tick(10.125); Assert.That(box.Alpha, Is.EqualTo(0.5f).Within(1e-4));
            box.Tick(11); Assert.That(box.Alpha, Is.EqualTo(1));
            // the next line: the old words fade out, the box is away for the gap, the new words fade in
            box.Set(C("Second."), 20); Assert.That(box.Shown.Text, Is.EqualTo("First.")); Assert.That(box.Alpha, Is.EqualTo(1));
            box.Tick(20.125); Assert.That(box.Shown.Text, Is.EqualTo("First.")); Assert.That(box.Alpha, Is.EqualTo(0.5f).Within(1e-4));
            box.Tick(20.3); Assert.That(box.Shown.Text, Is.EqualTo("First.")); Assert.That(box.Alpha, Is.EqualTo(0), "the gap");
            box.Tick(20.4 + 1e-9); Assert.That(box.Shown.Text, Is.EqualTo("Second.")); Assert.That(box.Alpha, Is.EqualTo(0).Within(1e-4));
            box.Tick(20.65 + 1e-9); Assert.That(box.Alpha, Is.EqualTo(1).Within(1e-4));
            Assert.That(CaptionBoxState.FadeOutS + CaptionBoxState.GapS + CaptionBoxState.FadeInS, Is.EqualTo(0.65).Within(1e-9));
        }

        [Test]
        public void TheSameClockGivesTheSameFramesWhateverTheFrameRate()
        {
            float At(int fps)
            {
                var box = new CaptionBoxState(); box.Set(C("One."), 0);
                for (var i = 1; i < 5 * fps; i++) box.Tick(i / (double)fps);
                box.Set(C("Two."), 5);
                for (var i = 5 * fps + 1; i <= 5 * fps + fps / 2; i++) box.Tick(i / (double)fps);
                return box.Alpha;
            }
            Assert.That(At(60), Is.EqualTo(At(30)).Within(1e-4));
            Assert.That(At(30), Is.EqualTo(0.4f).Within(1e-3), "0.1 s into the fade-in");
        }

        [Test]
        public void AHardCutTakesTheCaptionWithTheShotAndAKickerChangesInPlace()
        {
            var box = new CaptionBoxState(); box.Set(C("First."), 0); box.Tick(2);
            box.Set(C("First.", "CHAPTER TAG"), 2.5); Assert.That(box.Alpha, Is.EqualTo(1), "a kicker alone does not re-fade the words"); Assert.That(box.Shown.Kicker, Is.EqualTo("CHAPTER TAG"));
            box.Cut(); Assert.That(box.Alpha, Is.EqualTo(0)); Assert.That(box.Shown.IsEmpty, Is.True);
            box.Set(C("After the cut."), 3); box.Tick(3.25); Assert.That(box.Shown.Text, Is.EqualTo("After the cut.")); Assert.That(box.Alpha, Is.EqualTo(1).Within(1e-4));
            // asking for nothing fades the box out and leaves it empty
            box.Set(CaptionContent.Empty, 6); box.Tick(6.5); Assert.That(box.Alpha, Is.EqualTo(0)); Assert.That(box.Shown.IsEmpty, Is.True);
            // a line that changes again while the box is away: the last one asked for is the one that comes back
            box.Set(C("A."), 7); box.Tick(8); box.Set(C("B."), 8); box.Set(C("C."), 8.1); box.Tick(9);
            Assert.That(box.Shown.Text, Is.EqualTo("C."));
        }

        [Test]
        public void ACaptionIsAtMostTwoLines()
        {
            Assert.That(CaptionBoxState.TwoLines("a\nb\nc"), Is.EqualTo("a\nb"));
            Assert.That(CaptionBoxState.TwoLines("a\r\nb"), Is.EqualTo("a\nb"));
            Assert.That(CaptionBoxState.TwoLines(null), Is.EqualTo(""));
            Assert.That(new CaptionContent(null, "one\ntwo\nthree", null, CaptionTone.Scenario).Text, Is.EqualTo("one\ntwo"));
        }
    }

    public class ChainCardTests
    {
        static ChainRow Row(string id, ChainRowKind kind = ChainRowKind.Platform, double? cue = null) => new ChainRow(id, "Label " + id, kind, cue);

        [Test]
        public void ARowFadesAndSlidesInWhenItFirstAppearsAndOlderRowsStepBack()
        {
            var card = new ChainCardState();
            card.Set(new[] { Row("trigger", ChainRowKind.ScenarioTrigger) }, 0);
            card.Set(new[] { Row("trigger", ChainRowKind.ScenarioTrigger), Row("a", cue: 45) }, 5);
            var at = card.Draw(5.1);
            Assert.That(at.Select(r => r.Row.Id), Is.EqualTo(new[] { "trigger", "a" }));
            Assert.That(at[1].Alpha, Is.EqualTo(0.5f).Within(1e-4)); Assert.That(at[1].SlidePx, Is.EqualTo(4f).Within(1e-4)); Assert.That(at[1].Cue, Is.EqualTo("+45 s"));
            Assert.That(at[0].Alpha, Is.EqualTo(1), "the trigger does not step back");
            card.Set(new[] { Row("trigger", ChainRowKind.ScenarioTrigger), Row("a", cue: 45), Row("b", cue: 8) }, 9);
            var later = card.Draw(10);
            Assert.That(later[1].Alpha, Is.EqualTo(ChainCardState.OlderAlpha)); Assert.That(later[1].Newest, Is.False);
            Assert.That(later[2].Alpha, Is.EqualTo(1)); Assert.That(later[2].Newest, Is.True); Assert.That(later[2].SlidePx, Is.EqualTo(0));
        }

        [Test]
        public void TheCardKeepsItsTriggerAndItsNewestSteps()
        {
            var rows = new[] { Row("t", ChainRowKind.ScenarioTrigger), Row("a"), Row("b"), Row("c"), Row("d") };
            Assert.That(ChainCardState.Window(rows, 4).Select(r => r.Id), Is.EqualTo(new[] { "t", "b", "c", "d" }));
            Assert.That(ChainCardState.Window(rows, 2).Select(r => r.Id), Is.EqualTo(new[] { "t", "d" }));
            Assert.That(ChainCardState.Window(rows.Skip(1).ToArray(), 2).Select(r => r.Id), Is.EqualTo(new[] { "c", "d" }));
            Assert.That(ChainCardState.Window(rows.Take(3).ToArray(), 4).Count, Is.EqualTo(3));
            Assert.Throws<ArgumentOutOfRangeException>(() => ChainCardState.Window(rows, 0));
        }

        [Test]
        public void ARowThatIsNotPassedAgainIsGoneAndTheCardFadesFromItsExit()
        {
            var card = new ChainCardState();
            card.Set(new[] { Row("a"), Row("b") }, 0); Assert.That(card.CardAlpha(1), Is.EqualTo(1));
            card.Set(new[] { Row("a"), Row("b") }, 4, exitAtS: 5);
            Assert.That(card.CardAlpha(5), Is.EqualTo(1)); Assert.That(card.CardAlpha(5.3), Is.EqualTo(0.5f).Within(1e-4)); Assert.That(card.CardAlpha(5.7), Is.EqualTo(0));
            card.Set(new[] { Row("c") }, 6);
            Assert.That(card.Draw(6).Select(r => r.Row.Id), Is.EqualTo(new[] { "c" })); Assert.That(card.CardAlpha(6), Is.EqualTo(1), "a new chain is a new card");
            card.Settle(); Assert.That(card.Draw(6)[0].Alpha, Is.EqualTo(1), "a row that was already true when the shot began is simply there");
            card.Set(new ChainRow[0], 7); Assert.That(card.CardAlpha(7), Is.EqualTo(0));
        }

        [Test]
        public void ACueIsRecordedSecondsAndTheCardKeepsOutOfTheCentreThird()
        {
            Assert.That(ChainCardState.CueText(null), Is.EqualTo("")); Assert.That(ChainCardState.CueText(0.2), Is.EqualTo(""));
            Assert.That(ChainCardState.CueText(0.9), Is.EqualTo("+1 s")); Assert.That(ChainCardState.CueText(3.7), Is.EqualTo("+4 s"));
            Assert.That(ChainCardState.CueText(45.2), Is.EqualTo("+45 s")); Assert.That(ChainCardState.CueText(180), Is.EqualTo("+3 min"));
            foreach (CardDock dock in Enum.GetValues(typeof(CardDock)))
                foreach (var width in new[] { 1920f, 1440f, 2560f })
                {
                    ChainCardState.Span(dock, width, out var left, out var right);
                    Assert.That(right - left, Is.EqualTo(width >= 1920 ? ChainCardState.MaxWidthPx : 432).Within(1e-3));
                    Assert.That(right <= width / 3 || left >= width * 2 / 3, Is.True, dock + " enters the centre third at " + width);
                    Assert.That(left, Is.GreaterThanOrEqualTo(HudTheme.SafeMarginPx)); Assert.That(right, Is.LessThanOrEqualTo(width - HudTheme.SafeMarginPx));
                }
            Assert.That(ChainCardState.BottomMarginPx(1080), Is.EqualTo(86.4f).Within(1e-3), "above a player's control bar, about 8 % of the height");
        }
    }

    public class AlarmPinTests
    {
        [Test]
        public void ThePinIsSizedInScreenSpaceBetweenAFloorAndACeiling()
        {
            Assert.That(AlarmPins.Pixels(20, 40), Is.EqualTo(AlarmPins.MaxPx), "close: the ceiling, so the device stays the largest thing");
            Assert.That(AlarmPins.Pixels(600, 35), Is.EqualTo(AlarmPins.MinPx), "a wide: the floor");
            var mid = AlarmPins.Pixels(80, 40); Assert.That(mid, Is.InRange(AlarmPins.MinPx + 1, AlarmPins.MaxPx - 1));
            Assert.That(AlarmPins.Pixels(100, 40), Is.LessThan(mid));
            Assert.That(AlarmPins.NominalPx, Is.InRange(AlarmPins.MinPx, AlarmPins.MaxPx));
            Assert.That(AlarmPins.Pixels(0, 40), Is.EqualTo(AlarmPins.MaxPx)); Assert.That(AlarmPins.GlowPixels(5000, 35), Is.EqualTo(AlarmPins.MinPx));
        }

        [Test]
        public void SeverityIsNeverColourAloneAndAnUnknownWordIsTheWeakest()
        {
            Assert.That(AlarmPins.Severity("CRITICAL"), Is.EqualTo(PinSeverity.Critical)); Assert.That(AlarmPins.Severity("MAJOR"), Is.EqualTo(PinSeverity.Major));
            Assert.That(AlarmPins.Severity("MINOR"), Is.EqualTo(PinSeverity.Minor)); Assert.That(AlarmPins.Severity("WARNING"), Is.EqualTo(PinSeverity.Warning));
            Assert.That(AlarmPins.Severity("UNRECORDED"), Is.EqualTo(PinSeverity.Warning)); Assert.That(AlarmPins.Severity(null), Is.EqualTo(PinSeverity.Warning));
            var styles = new[] { PinSeverity.Warning, PinSeverity.Minor, PinSeverity.Major, PinSeverity.Critical }.Select(AlarmPins.Style).ToArray();
            Assert.That(styles[0].Outlined, Is.True); Assert.That(styles.Skip(1).Any(s => s.Outlined), Is.False);
            // minor and major differ by their edge as well as their fill; critical by size and breath as well
            Assert.That(HudColor.Contrast(styles[1].Edge, styles[2].Edge), Is.GreaterThan(4.5));
            Assert.That(styles[3].Scale, Is.EqualTo(AlarmPins.CriticalScale)); Assert.That(styles[3].Breathes, Is.True); Assert.That(styles.Take(3).Any(s => s.Breathes), Is.False);
            // every glyph reads on its pin, and every pin on a night grade
            var night = new HudColor(0.03f, 0.05f, 0.09f);
            foreach (var s in styles)
            {
                Assert.That(HudColor.Contrast(s.Glyph, s.Fill), Is.GreaterThanOrEqualTo(4.5));
                Assert.That(Math.Max(HudColor.Contrast(s.Fill, night), HudColor.Contrast(s.Edge, night)), Is.GreaterThanOrEqualTo(4.5));
                if (!s.Outlined) Assert.That(HudColor.Contrast(s.Fill, night), Is.GreaterThan(3));
            }
            // coral is refused or failed, never an alarm
            foreach (var s in styles.Skip(1))
                Assert.That(Math.Sqrt(Math.Pow(s.Fill.R - HudTheme.Coral.R, 2) + Math.Pow(s.Fill.G - HudTheme.Coral.G, 2) + Math.Pow(s.Fill.B - HudTheme.Coral.B, 2)), Is.GreaterThan(0.25));
        }

        [Test]
        public void ARaiseScalesInWithOneRingAndAClearCrossFadesToATickThatHoldsAndGoes()
        {
            var board = new AlarmPinBoard();
            Assert.That(board.Update(new PinInput[0], 0), Is.Empty);
            var pin = new[] { new PinInput("lamp", PinSeverity.Major, 1) };
            var raised = board.Update(pin, 10).Single();
            Assert.That(raised.Scale, Is.EqualTo(0)); Assert.That(raised.Ring, Is.EqualTo(0)); Assert.That(raised.PinAlpha, Is.EqualTo(1));
            var grown = board.Update(pin, 10.25).Single(); Assert.That(grown.Scale, Is.EqualTo(1).Within(1e-4)); Assert.That(grown.Ring, Is.InRange(0.4f, 0.43f));
            Assert.That(board.Update(pin, 10.7).Single().Ring, Is.EqualTo(-1), "one ring, once");
            // it escalates: the same pin, not raised again, now with a count
            var worse = board.Update(new[] { new PinInput("lamp", PinSeverity.Critical, 3, steady: true) }, 20).Single();
            Assert.That(worse.Severity, Is.EqualTo(PinSeverity.Critical)); Assert.That(worse.Count, Is.EqualTo(3)); Assert.That(worse.Ring, Is.EqualTo(-1)); Assert.That(worse.Scale, Is.EqualTo(AlarmPins.CriticalScale).Within(1e-4));
            // cleared
            Assert.That(board.Update(new PinInput[0], 30).Single().TickAlpha, Is.EqualTo(0));
            var half = board.Update(new PinInput[0], 30.2).Single(); Assert.That(half.PinAlpha, Is.EqualTo(0.5f).Within(1e-4)); Assert.That(half.TickAlpha, Is.EqualTo(0.5f).Within(1e-4));
            var held = board.Update(new PinInput[0], 33).Single(); Assert.That(held.PinAlpha, Is.EqualTo(0)); Assert.That(held.TickAlpha, Is.EqualTo(1));
            Assert.That(board.Update(new PinInput[0], 33.9).Single().TickAlpha, Is.EqualTo(0.5f).Within(1e-3));
            Assert.That(board.Update(new PinInput[0], 34.5), Is.Empty);
            // raised again after a clear: a new raise
            board.Update(pin, 40); board.Update(new PinInput[0], 41);
            Assert.That(board.Update(pin, 42).Single().Scale, Is.EqualTo(0));
        }

        [Test]
        public void AShotThatBeginsWithAnAlarmDrawsItSettledAndACriticalPinBreathesUnlessAcknowledged()
        {
            var board = new AlarmPinBoard(); board.Update(new[] { new PinInput("gone", PinSeverity.Minor, 1) }, 0); board.Update(new PinInput[0], 1);
            board.Settle();
            var first = board.Update(new[] { new PinInput("pump", PinSeverity.Critical, 1) }, 100);
            Assert.That(first.Select(p => p.Key), Is.EqualTo(new[] { "pump" }), "no tick for an alarm that cleared before the shot");
            Assert.That(first[0].Ring, Is.EqualTo(-1)); Assert.That(first[0].Scale, Is.GreaterThan(1));
            var scales = Enumerable.Range(0, 40).Select(i => board.Update(new[] { new PinInput("pump", PinSeverity.Critical, 1) }, 100 + i * 0.05).Single().Scale).ToArray();
            Assert.That(scales.Max() - scales.Min(), Is.EqualTo(2 * AlarmPins.BreathDepth * AlarmPins.CriticalScale).Within(0.01));
            var steady = Enumerable.Range(0, 40).Select(i => board.Update(new[] { new PinInput("pump", PinSeverity.Critical, 1, steady: true) }, 110 + i * 0.05).Single().Scale).Distinct().Count();
            Assert.That(steady, Is.EqualTo(1));
            // one pin per device, and a device with no active alarm has none
            var two = board.Update(new[] { new PinInput("pump", PinSeverity.Critical, 2), new PinInput("pump", PinSeverity.Minor, 1), new PinInput("idle", PinSeverity.Minor, 0), null }, 120);
            Assert.That(two.Count, Is.EqualTo(1)); Assert.That(two[0].Count, Is.EqualTo(2));
        }
    }

    public class TopStripAndEndCardTests
    {
        [Test]
        public void TheStripNamesThePictureThePlaceTheClockAndTheRate()
        {
            Assert.That(TopStrip.Rate(1.2), Is.EqualTo("×1.2")); Assert.That(TopStrip.Rate(12.4), Is.EqualTo("×12")); Assert.That(TopStrip.Rate(4), Is.EqualTo("×4"));
            Assert.That(TopStrip.Rate(1.004), Is.EqualTo("")); Assert.That(TopStrip.Rate(0), Is.EqualTo("")); Assert.That(TopStrip.Rate(double.NaN), Is.EqualTo(""));
            Assert.That(TopStrip.Rate(1.2), Does.Not.Contain("x"));
            Assert.That(TopStrip.Text("Recorded", "Port Alder", "17:37", TopStrip.Rate(1.2)), Is.EqualTo("RECORDED  ·  Port Alder  ·  17:37  ·  ×1.2"));
            Assert.That(TopStrip.Text("recorded", "Port Alder", "19:00", "", "later that day"), Is.EqualTo("RECORDED  ·  Port Alder  ·  19:00  ·  later that day"));
        }

        [Test]
        public void TheEndCardsLevelsFadeInAsTheirWordsArrive()
        {
            var card = new EndCardState();
            card.Set(new EndCardContent("Bridge.", "", "", ""), 0); card.Set(new EndCardContent("Bridge.", "", "", ""), 0.2);
            Assert.That(card.Alpha[0], Is.EqualTo(0.5f).Within(1e-4)); Assert.That(card.Alpha[1], Is.EqualTo(0)); Assert.That(card.Alpha[2], Is.EqualTo(0));
            card.Set(new EndCardContent("Bridge.", "Tagline.", "Sub.", "Small print."), 3); card.Set(new EndCardContent("Bridge.", "Tagline.", "Sub.", "Small print."), 3.4);
            Assert.That(card.Alpha, Is.EqualTo(new[] { 1f, 1f, 1f }));
            Assert.That(card.Content.Lines.Count(), Is.EqualTo(4)); Assert.That(new EndCardContent(null, null, null, null).IsEmpty, Is.True);
            card.Set(null, 5); Assert.That(card.Alpha, Is.EqualTo(new[] { 0f, 0f, 0f }));
        }
    }

    public class PanelTests
    {
        [Test]
        public void EveryTextColourHoldsItsContrastOnAPanelOverTheBrightestBackground()
        {
            var white = new HudColor(1, 1, 1);
            Assert.That(HudTheme.PanelOpacity, Is.InRange(0.92f, 0.95f)); Assert.That(HudTheme.SolidOpacity, Is.GreaterThanOrEqualTo(0.95f));
            foreach (var scrim in new[] { HudTheme.Scrim, HudTheme.Scrim.WithAlpha(HudTheme.SolidOpacity) })
            {
                // the worst thing a scene can put behind a panel is pure white: a lamp's bloom, a blown-out sky
                var behind = HudTheme.Over(scrim, white);
                foreach (var text in new[] { HudTheme.Ink, HudTheme.Dim, HudTheme.BlueText }) // the colours text is written in; the grammar's other colours are marks, not words
                    Assert.That(HudColor.Contrast(text, behind), Is.GreaterThanOrEqualTo(HudTheme.MinContrast));
                Assert.That(HudTheme.ContrastOn(HudTheme.Ink, behind.Luminance), Is.EqualTo(HudColor.Contrast(HudTheme.Ink, behind)).Within(1e-9));
            }
            // CONTROL: the scrim this replaced (84 %) and a thin one do not hold the floor for the small grey text over white
            Assert.That(HudColor.Contrast(HudTheme.Dim, HudTheme.Over(HudTheme.Scrim.WithAlpha(0.6f), white)), Is.LessThan(HudTheme.MinContrast));
            Assert.That(HudColor.Contrast(HudTheme.Dim, HudTheme.Over(HudTheme.Scrim.WithAlpha(0.92f), white)), Is.LessThan(HudTheme.MinContrast), "92 % was measured at 4.4:1 in a render");
            // compositing is in linear light: half black over white is mid grey in light, which is brighter than half in sRGB
            Assert.That(HudTheme.Over(new HudColor(0, 0, 0, 0.5f), white).R, Is.EqualTo(0.735f).Within(0.002f));
        }

        [Test]
        public void AWorldMarkerOnAPanelIsNotDrawn()
        {
            var panels = new[] { new[] { 0.35f, 0.82f, 0.65f, 0.92f }, new[] { 0.74f, 0.08f, 0.975f, 0.3f } };
            Assert.That(HudTheme.OnPanel(0.5f, 0.87f, panels, 0, 0), Is.True, "under the caption box");
            Assert.That(HudTheme.OnPanel(0.8f, 0.2f, panels, 0, 0), Is.True, "under the chain card");
            Assert.That(HudTheme.OnPanel(0.5f, 0.5f, panels, 0, 0), Is.False);
            // a marker has a size: one whose tip is just outside a panel still reaches onto it
            Assert.That(HudTheme.OnPanel(0.5f, 0.80f, panels, 0, 0), Is.False); Assert.That(HudTheme.OnPanel(0.5f, 0.80f, panels, 0.01f, 0.03f), Is.True);
            Assert.That(HudTheme.OnPanel(0.34f, 0.87f, panels, 0.012f, 0), Is.True);
            Assert.That(HudTheme.OnPanel(0.5f, 0.87f, null, 0, 0), Is.False); Assert.That(HudTheme.OnPanel(0.5f, 0.87f, new float[0][], 0, 0), Is.False);
        }
    }

    public class PinAnchorTests
    {
        [Test]
        public void AStaticDevicesAnchorIsConstantAndAMovingSubjectIsFollowed()
        {
            var anchors = new PinAnchors();
            var first = anchors.Place("lamp", false, 10, 9, -4);
            // whatever a later frame says (a transform read mid-update, a float that wobbles), a static device's pin stays where it stood
            for (var frame = 1; frame < 600; frame++)
                Assert.That(anchors.Place("lamp", false, 10 + System.Math.Sin(frame) * 3, 9, -4 + frame * 0.01), Is.EqualTo(new[] { 10.0, 9, -4 }));
            Assert.That(anchors.Place("lamp", false, 99, 99, 99), Is.SameAs(first));
            // a vehicle's pin is where the vehicle is this frame, and moves as smoothly as the vehicle does
            var last = 0.0;
            for (var frame = 0; frame < 600; frame++)
            {
                var at = anchors.Place("truck", true, frame * 0.05, 2.4, 7);
                Assert.That(at[0] - last, Is.InRange(0, 0.05 + 1e-9)); last = at[0];
            }
            // two devices never share an anchor by accident, and a rebuilt world takes its anchors afresh
            Assert.That(anchors.Place("gauge", false, 1, 2, 3), Is.EqualTo(new[] { 1.0, 2, 3 }));
            anchors.Clear(); Assert.That(anchors.Place("lamp", false, 5, 5, 5), Is.EqualTo(new[] { 5.0, 5, 5 }));
        }
    }
}
