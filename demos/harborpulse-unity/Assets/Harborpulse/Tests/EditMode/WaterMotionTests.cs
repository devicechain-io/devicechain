// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class WaterMotionTests
    {
        static HarborTestSource Gauges(DateTimeOffset at, double north, double east, double basin)
        {
            var source = new HarborTestSource();
            foreach (var d in Kit.Layout().Devices) if (DeviceKind.IsGauge(d.Kind))
                source.Set(d.ExternalId, Metrics.WaterLevel, d.Quay == "hp-north-quay" ? north : d.Quay == "hp-east-quay" ? east : basin, at);
            return source;
        }
        [Test]
        public void ReflectionUsesMedianOfReportedQuaysAndDatumWhenNone()
        {
            var board = new TideBoard(Kit.Layout());
            Assert.That(WaterMotion.ReflectionLevel(board.Regions), Is.EqualTo(0));
            board.Update(Gauges(Kit.T0, 1, 9, 3), Kit.T0);
            Assert.That(WaterMotion.ReflectionLevel(board.Regions), Is.EqualTo(3));
        }
        [Test]
        public void SprayFollowsObservedRiseAndStormWithoutChangingTide()
        {
            var board = new TideBoard(Kit.Layout()); var motion = new WaterMotion();
            board.Update(Gauges(Kit.T0, 1, 1, 1), Kit.T0);
            var first = motion.Update(board.Regions, 1);
            board.Update(Gauges(Kit.T0.AddSeconds(5), 1.1, 0.9, 1), Kit.T0.AddSeconds(5));
            var rising = motion.Update(board.Regions, 1);
            Assert.That(rising["hp-north-quay"], Is.GreaterThan(first["hp-north-quay"]));
            Assert.That(rising["hp-east-quay"], Is.EqualTo(first["hp-east-quay"]));
            Assert.That(board.Region("hp-north-quay").LevelM, Is.EqualTo(1.1));
            Assert.That(first["hp-north-quay"], Is.EqualTo(0.2)); // prior snapshot is detached
            Assert.That(motion.Update(board.Regions, 0)["hp-north-quay"], Is.EqualTo(0));
        }
        [Test]
        public void RepeatedOrOutOfOrderSamplesDoNotInventRiseAndHeldWaterHasNoSpray()
        {
            var board = new TideBoard(Kit.Layout()); var motion = new WaterMotion();
            board.Update(Gauges(Kit.T0, 1, 1, 1), Kit.T0);
            motion.Update(board.Regions, 1);
            Assert.That(motion.Update(board.Regions, 1)["hp-north-quay"], Is.EqualTo(0.2));
            board.Update(new HarborTestSource(), Kit.T0.AddMinutes(4));
            Assert.That(motion.Update(board.Regions, 1)["hp-north-quay"], Is.EqualTo(0));
        }
        [Test]
        public void OvertoppingUsesObservedLevelAtTheDeclaredFloodingLine()
        {
            var board = new TideBoard(Kit.Layout());
            Assert.That(WaterMotion.Overtopping(board.Regions, 2.1)["hp-east-quay"], Is.EqualTo(0));
            board.Update(Gauges(Kit.T0, 1, 2.25, 2.1), Kit.T0);
            var overflow = WaterMotion.Overtopping(board.Regions, Kit.Layout().Levels.QuayFloodingM);
            Assert.That(overflow["hp-east-quay"], Is.EqualTo(0.5).Within(1e-7));
            Assert.That(overflow["hp-basin"], Is.EqualTo(0));
        }
        [Test]
        public void ReplacementLabelUsesScenarioTimeInsteadOfHostTime()
        {
            var layout = Kit.Layout(); var source = new PreviewSource(layout);
            source.SetTime(12.67 * 3600, Kit.T0);
            var frame = new HarborObservedModel(layout).Update(source, new ClockReading(12.67 * 3600, ClockMode.Preview, ""), Kit.T0);
            Assert.That(frame.Replacement.DeclaredSimSeconds.Value, Is.EqualTo(12.5 * 3600).Within(0.01));
            Assert.That(frame.Replacement.RunnerDeclaration.At.Hour, Is.Not.EqualTo(12));
        }
        [Test]
        public void StillFadeSettlesOnlyThePreviewAndLiveKeepsItsTwoSecondRamp()
        {
            var layout = Kit.Layout();
            var preview = new ClockReading(19.75 * 3600, ClockMode.Preview, "");
            Assert.That(StoryCues.At(layout, null, preview).EastFeederOutage, Is.EqualTo(0));
            Assert.That(StoryCues.At(layout, null, preview, true).EastFeederOutage, Is.EqualTo(1));
            Assert.That(StoryCues.At(layout, null, new ClockReading(21.25 * 3600, ClockMode.Preview, ""), true).EastFeederOutage, Is.EqualTo(0));
            Assert.Throws<InvalidOperationException>(() => StoryCues.At(layout, null, new ClockReading(19.75 * 3600, ClockMode.Live, ""), true));
        }
        [Test]
        public void CrewTargetComesFromRunnerJobInsteadOfAFixedVisualDevice()
        {
            var source = new PreviewSource(Kit.Layout()); source.SetTime(11 * 3600, Kit.T0);
            var status = ScenarioStatus.Parse(Kit.Status().Substring(0, Kit.Status().Length - 2) +
                ",\"jobs\":[{\"day\":0,\"name\":\"lamp-service\",\"campaign\":\"\",\"group\":\"\",\"window\":\"w\",\"device\":\"hp-light-north-2\",\"fireAt\":\"\",\"windowOpens\":\"2026-11-19T11:49:00Z\",\"windowCloses\":\"2026-11-19T12:20:00Z\"}]}}");
            var cues = StoryCues.At(Kit.Layout(), status, new ClockReading(11 * 3600, ClockMode.Live, ""));
            Assert.That(cues.LampCrew, Is.True);
            Assert.That(cues.LampCrewDevice, Is.EqualTo("hp-light-north-2"));
        }
    }
}
