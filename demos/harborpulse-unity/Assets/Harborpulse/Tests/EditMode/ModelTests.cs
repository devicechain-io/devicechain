// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    static class Kit
    {
        static string layoutText;

        /// <summary>The committed layout file, found from wherever the runner starts (the Unity project root, or the dotnet test's bin folder).</summary>
        public static string LayoutText()
        {
            if (layoutText != null) return layoutText;
            var rel = Path.Combine("Assets", "Harborpulse", "Resources", "harbor_layout.json");
            for (var dir = new DirectoryInfo(Directory.GetCurrentDirectory()); dir != null; dir = dir.Parent)
            {
                var direct = Path.Combine(dir.FullName, rel);
                if (File.Exists(direct)) return layoutText = File.ReadAllText(direct);
                var inRepo = Path.Combine(dir.FullName, "demos", "harborpulse-unity", rel);
                if (File.Exists(inRepo)) return layoutText = File.ReadAllText(inRepo);
            }

            throw new FileNotFoundException("harbor_layout.json not found above " + Directory.GetCurrentDirectory());
        }

        public static HarborLayout Layout() => HarborLayout.Parse(LayoutText());

        public static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 11, 19, 12, 0, 0, TimeSpan.Zero);

        public static string Status(string state = "RUNNING", bool started = true, string startedAt = "2026-11-19T11:50:00.123456789Z", int day = 0, double simHour = 12.5,
            string beat = "quiet", string name = "harborpulse") =>
            "{\"state\":\"" + state + "\",\"tenant\":\"sim-hp\",\"instanceId\":\"dc\",\"deviceCount\":43,\"emitIntervalMs\":5000,\"scenario\":{\"name\":\"" + name +
            "\",\"started\":" + (started ? "true" : "false") + (started ? ",\"startedAt\":\"" + startedAt + "\"" : "") +
            ",\"dayLengthSeconds\":1440,\"startHour\":6,\"day\":" + day + ",\"simHour\":" + simHour.ToString(System.Globalization.CultureInfo.InvariantCulture) +
            ",\"simTime\":\"12:30\",\"beat\":\"" + beat + "\"}}";
    }

    sealed class FakeSource : IReadingSource
    {
        readonly Dictionary<string, Reading> values = new Dictionary<string, Reading>(StringComparer.Ordinal);

        public void Set(string externalId, string metric, double v, DateTimeOffset at) => values[externalId + "|" + metric] = new Reading(v, at);

        public bool TryGet(string externalId, string metric, out Reading reading) => values.TryGetValue(externalId + "|" + metric, out reading);
    }

    public class LayoutTests
    {
        [Test]
        public void TheCommittedLayoutIsTheWholeFleet()
        {
            var l = Kit.Layout();
            Assert.That(l.Devices.Count, Is.EqualTo(PresentationMap.DeviceCount));
            Assert.That(PresentationMap.DeviceCount, Is.EqualTo(43), "the district's 37 and the six that hold their own session");
            Assert.That(l.Quays.Count, Is.EqualTo(3));
            var lights = 0;
            foreach (var d in l.Devices)
            {
                if (d.Kind == DeviceKind.Light)
                {
                    lights++;
                    Assert.That(d.Position.HasValue, Is.True, d.ExternalId + " is a light with no surveyed position");
                }
                else Assert.That(d.Position.HasValue, Is.False, d.ExternalId + " has a position the manifest never gave it");
            }

            Assert.That(lights, Is.EqualTo(12));
            Assert.That(l.Beat("storm"), Is.Not.Null);
            Assert.That(l.Levels.FloodCriticalM, Is.GreaterThan(l.Levels.FloodMajorM));
        }

        [Test]
        public void AnotherVersionIsRefused()
        {
            var text = Kit.LayoutText().Replace("\"version\": 1", "\"version\": 2");
            var e = Assert.Throws<FormatException>(() => HarborLayout.Parse(text));
            Assert.That(e.Message, Does.Contain("version 2"));
        }

        [Test]
        public void AKindTheSceneCannotDrawIsRefused()
        {
            var text = Kit.LayoutText().Replace("\"kind\": \"truck\"", "\"kind\": \"tugboat\"");
            Assert.That(() => HarborLayout.Parse(text), Throws.TypeOf<FormatException>().With.Message.Contains("tugboat"));
        }

        [Test]
        public void ADeviceOnAnUndeclaredQuayIsRefused()
        {
            var text = Kit.LayoutText().Replace("\"quay\": \"hp-basin\"", "\"quay\": \"hp-west-quay\"");
            Assert.That(() => HarborLayout.Parse(text), Throws.TypeOf<FormatException>().With.Message.Contains("hp-west-quay"));
        }

        [Test]
        public void ADuplicateDeviceIsRefused()
        {
            var text = Kit.LayoutText().Replace("PAW-GAUGE-NORTH-2", "PAW-GAUGE-NORTH-1");
            Assert.That(() => HarborLayout.Parse(text), Throws.TypeOf<FormatException>().With.Message.Contains("twice"));
        }

        [Test]
        public void TheCuesRouteAndControlFrameAreRead()
        {
            var l = Kit.Layout();
            Assert.That(l.Cues.TruckInPlainSim, Is.EqualTo(17.75));
            Assert.That(l.Cues.RolloutSim, Is.EqualTo(26));
            Assert.That(l.Cues.SilenceS, Is.EqualTo(45));
            Assert.That(l.TruckRoute[0].Lat, Is.EqualTo(l.Depot.Lat));
            Assert.That(l.TruckRoute[0].Lon, Is.EqualTo(l.Depot.Lon));
            Assert.That(l.ControlFrame[0].Lat, Is.LessThan(l.TruckRoute[1].Lat));
            Assert.That(l.ControlFrame[2].Lat, Is.GreaterThan(l.PromenadeLat));
        }

        [Test]
        public void MissingOrMisorderedCuesAreRefused()
        {
            Assert.That(() => HarborLayout.Parse(Kit.LayoutText().Replace("\"truckRoute\"", "\"route\"")),
                Throws.TypeOf<FormatException>().With.Message.Contains("truckRoute"));
            Assert.That(() => HarborLayout.Parse(Kit.LayoutText().Replace("\"truckInPlainSim\": 17.75", "\"truckInPlainSim\": 23")),
                Throws.TypeOf<FormatException>().With.Message.Contains("not in order"));
            Assert.That(() => HarborLayout.Parse(Kit.LayoutText().Replace("\"silenceS\"", "\"silence\"")),
                Throws.TypeOf<FormatException>().With.Message.Contains("silenceS"));
            Assert.That(() => HarborLayout.Parse(Kit.LayoutText().Replace("\"silenceS\": 45", "\"silenceS\": -45")),
                Throws.TypeOf<FormatException>().With.Message.Contains("not positive"));
            Assert.That(() => HarborLayout.Parse(Kit.LayoutText().Replace("\"lampServiceToSim\": 11.5", "\"lampServiceToSim\": 10")),
                Throws.TypeOf<FormatException>().With.Message.Contains("ends before"));
        }

        [Test]
        public void ARouteOffTheDepotOrAFrameThatMissesAPositionIsRefused()
        {
            var l = Kit.Layout();
            var depotLat = l.Depot.Lat.ToString(System.Globalization.CultureInfo.InvariantCulture);
            var text = Kit.LayoutText();
            var routeAt = text.IndexOf("\"truckRoute\"", StringComparison.Ordinal);
            var moved = text.Substring(0, routeAt) + text.Substring(routeAt).Replace("\"lat\": " + depotLat, "\"lat\": 49.79", StringComparison.Ordinal);
            Assert.That(() => HarborLayout.Parse(moved), Throws.TypeOf<FormatException>().With.Message.Contains("does not start at the depot"));
            var frameAt = text.IndexOf("\"controlFrame\"", StringComparison.Ordinal);
            var south = l.ControlFrame[0].Lat.ToString(System.Globalization.CultureInfo.InvariantCulture);
            var shrunk = text.Substring(0, frameAt) + text.Substring(frameAt).Replace("\"lat\": " + south, "\"lat\": 49.7999", StringComparison.Ordinal);
            Assert.That(() => HarborLayout.Parse(shrunk), Throws.TypeOf<FormatException>().With.Message.Contains("not strictly inside the control frame"));
        }
    }

    public class SitePlanTests
    {
        [Test]
        public void TheLightsStandWhereTheManifestSurveysThem()
        {
            var l = Kit.Layout();
            var plan = SitePlan.Build(l);
            var a = plan.Find("PAW-LIGHT-NORTH-1");
            var b = plan.Find("PAW-LIGHT-NORTH-2");
            Assert.That(a.Surveyed && b.Surveyed, Is.True);
            // 0.006 degrees of longitude at 49.8 N is about 431 m, and the scene neither compresses nor stretches it
            Assert.That(b.At.X - a.At.X, Is.EqualTo(431).Within(3));
            Assert.That(b.At.Z - a.At.Z, Is.EqualTo(0).Within(1e-6));
        }

        [Test]
        public void OnlyTheLightsAreSurveyed()
        {
            var plan = SitePlan.Build(Kit.Layout());
            foreach (var p in plan.Placements)
                Assert.That(p.Surveyed, Is.EqualTo(p.Device.Kind == DeviceKind.Light), p.Device.ExternalId);
        }

        [Test]
        public void TheQuaysRunWestToEastAndDoNotOverlap()
        {
            var plan = SitePlan.Build(Kit.Layout());
            var basin = plan.Quay("hp-basin");
            var north = plan.Quay("hp-north-quay");
            var east = plan.Quay("hp-east-quay");
            Assert.That(basin.XMax, Is.LessThan(north.XMin));
            Assert.That(north.XMax, Is.LessThan(east.XMin));
            Assert.That(plan.MaxX - plan.MinX, Is.GreaterThan(5000), "the district is several kilometres wide, as the manifest has it");
        }

        [Test]
        public void EveryGaugeIsOnOrBeyondItsQuayWall()
        {
            var plan = SitePlan.Build(Kit.Layout());
            foreach (var p in plan.Placements)
            {
                if (!DeviceKind.IsGauge(p.Device.Kind)) continue;
                var q = plan.Quay(p.Device.Quay);
                Assert.That(p.At.Z, Is.GreaterThanOrEqualTo(plan.WallZ), p.Device.ExternalId + " is inland of the wall");
                Assert.That(p.At.X, Is.InRange(q.XMin, q.XMax), p.Device.ExternalId + " is off its quay");
            }
        }
    }

    public class ScenarioClockTests
    {
        static ScenarioClockFollower Following(string status, DateTimeOffset at)
        {
            var f = new ScenarioClockFollower();
            f.Observe(ScenarioStatus.Parse(status), at);
            return f;
        }

        [Test]
        public void TheStatusIsParsedAsTheSimulatorWritesIt()
        {
            var s = ScenarioStatus.Parse(Kit.Status(day: 1, simHour: 19.25, beat: "storm,air-lock,feeder-outage"));
            Assert.That(s.Running, Is.True);
            Assert.That(s.Started, Is.True);
            Assert.That(s.StartedAt, Is.EqualTo(new DateTimeOffset(2026, 11, 19, 11, 50, 0, TimeSpan.Zero).AddTicks(1234568)));
            Assert.That(s.SimSeconds, Is.EqualTo(DayClock.Day + 19.25 * 3600).Within(1e-6));
            Assert.That(s.InBeat("storm"), Is.True);
            Assert.That(s.InBeat("air"), Is.False, "a beat is matched whole, not as a prefix");
        }

        [Test]
        public void AnotherScenarioIsRefused()
        {
            Assert.That(() => ScenarioStatus.Parse(Kit.Status(name: "towerpulse")), Throws.TypeOf<FormatException>().With.Message.Contains("towerpulse"));
            Assert.That(() => ScenarioStatus.Parse("{\"state\":\"RUNNING\"}"), Throws.TypeOf<FormatException>().With.Message.Contains("no scenario day"));
        }

        [Test]
        public void BetweenPollsTheClockRunsAtTheDaysRate()
        {
            var f = Following(Kit.Status(simHour: 12), Kit.T0);
            // a 24-minute day: one wall minute is one sim hour
            var r = f.Read(Kit.T0.AddSeconds(3));
            Assert.That(r.Mode, Is.EqualTo(ClockMode.Live));
            Assert.That(r.HourOfDay, Is.EqualTo(12.05).Within(1e-9));
        }

        [Test]
        public void MidnightIsContinuous()
        {
            var f = Following(Kit.Status(day: 0, simHour: 23.99), Kit.T0);
            var before = f.Read(Kit.T0);
            var after = f.Read(Kit.T0.AddSeconds(2));
            Assert.That(after.SimSeconds, Is.GreaterThan(before.SimSeconds));
            Assert.That(after.Day, Is.EqualTo(1));
            Assert.That(after.HourOfDay, Is.LessThan(0.1));
        }

        [Test]
        public void TheClockNeverRunsBackwardsWithinARun()
        {
            var f = Following(Kit.Status(simHour: 12), Kit.T0);
            var ahead = f.Read(Kit.T0.AddSeconds(4));
            // the next poll's answer is a little behind what the scene already showed
            f.Observe(ScenarioStatus.Parse(Kit.Status(simHour: 12.06)), Kit.T0.AddSeconds(4));
            Assert.That(f.Read(Kit.T0.AddSeconds(4)).SimSeconds, Is.GreaterThanOrEqualTo(ahead.SimSeconds));
        }

        [Test]
        public void ANewRunStartsOver()
        {
            var f = Following(Kit.Status(simHour: 20), Kit.T0);
            f.Read(Kit.T0.AddSeconds(1));
            f.Observe(ScenarioStatus.Parse(Kit.Status(simHour: 6, startedAt: "2026-11-19T12:00:00Z")), Kit.T0.AddSeconds(2));
            var r = f.Read(Kit.T0.AddSeconds(2));
            Assert.That(r.HourOfDay, Is.EqualTo(6).Within(1e-9));
            Assert.That(f.Runs, Is.EqualTo(2));
        }

        [Test]
        public void AStoppedRunnerHoldsTheClockAndSaysSo()
        {
            var f = Following(Kit.Status(state: "STOPPED", simHour: 14), Kit.T0);
            var r = f.Read(Kit.T0.AddSeconds(30));
            Assert.That(r.Mode, Is.EqualTo(ClockMode.Stale));
            Assert.That(r.HourOfDay, Is.EqualTo(14).Within(1e-9), "a stopped day does not keep turning");
            Assert.That(r.Note, Does.Contain("STOPPED"));
        }

        [Test]
        public void AStoppedRunnersClockDoesNotCreepOnWithItsOwn()
        {
            // the runner keeps counting from its first tick while it is stopped, so each STOPPED status reports a later time
            var f = Following(Kit.Status(simHour: 14), Kit.T0);
            var atStop = f.Read(Kit.T0.AddSeconds(2)).SimSeconds;
            f.Observe(ScenarioStatus.Parse(Kit.Status(state: "STOPPED", simHour: 14.04)), Kit.T0.AddSeconds(2));
            f.Observe(ScenarioStatus.Parse(Kit.Status(state: "STOPPED", simHour: 14.5)), Kit.T0.AddSeconds(30));
            f.Observe(ScenarioStatus.Parse(Kit.Status(state: "STOPPED", simHour: 15.0)), Kit.T0.AddSeconds(60));
            var r = f.Read(Kit.T0.AddSeconds(61));
            Assert.That(r.Mode, Is.EqualTo(ClockMode.Stale));
            Assert.That(r.SimSeconds, Is.EqualTo(atStop).Within(1e-6), "the scene holds the time the runner stopped at");

            // running again, it follows the runner from where the runner is
            f.Observe(ScenarioStatus.Parse(Kit.Status(simHour: 15.1)), Kit.T0.AddSeconds(66));
            Assert.That(f.Read(Kit.T0.AddSeconds(66)).HourOfDay, Is.EqualTo(15.1).Within(1e-9));
        }

        [Test]
        public void AStatusWithoutItsDayOrStartIsRefused()
        {
            var noDay = Kit.Status().Replace("\"day\":0,", "");
            Assert.That(() => ScenarioStatus.Parse(noDay), Throws.TypeOf<FormatException>().With.Message.Contains("day"));
            var noStarted = Kit.Status().Replace("\"started\":true,", "");
            Assert.That(() => ScenarioStatus.Parse(noStarted), Throws.TypeOf<FormatException>().With.Message.Contains("started"));
            var negative = Kit.Status().Replace("\"day\":0,", "\"day\":-1,");
            Assert.That(() => ScenarioStatus.Parse(negative), Throws.TypeOf<FormatException>());
        }

        [Test]
        public void ASilentRunnerGoesStale()
        {
            var f = Following(Kit.Status(simHour: 14), Kit.T0);
            Assert.That(f.Read(Kit.T0.AddSeconds(4)).Mode, Is.EqualTo(ClockMode.Live));
            f.ObserveFailure("connection refused");
            var r = f.Read(Kit.T0.AddSeconds(9));
            Assert.That(r.Mode, Is.EqualTo(ClockMode.Stale));
            Assert.That(r.Note, Does.Contain("connection refused"));
            Assert.That(r.HourOfDay, Is.LessThan(14.1), "the clock holds where it was last known");
        }

        [Test]
        public void ARunThatHasNotTickedWaitsAtItsOpeningHour()
        {
            var f = Following(Kit.Status(started: false, simHour: 6), Kit.T0);
            var r = f.Read(Kit.T0.AddMinutes(5));
            Assert.That(r.Mode, Is.EqualTo(ClockMode.Waiting));
            Assert.That(r.HourOfDay, Is.EqualTo(6).Within(1e-9));
        }
    }

    public class SkyTests
    {
        [Test]
        public void NoonIsBrightAndMidnightIsStarry()
        {
            var noon = DayLighting.Evaluate(12.25, new Weather(0, 0));
            var midnight = DayLighting.Evaluate(0, new Weather(0, 0));
            Assert.That(noon.SunElevationDeg, Is.EqualTo(DayLighting.SunMaxElevationDeg).Within(0.5));
            Assert.That(noon.SunIntensity, Is.GreaterThan(1.5));
            Assert.That(noon.Stars, Is.EqualTo(0).Within(1e-6));
            Assert.That(midnight.SunIntensity, Is.EqualTo(0));
            Assert.That(midnight.Stars, Is.GreaterThan(0.95));
            Assert.That(midnight.MoonIntensity, Is.GreaterThan(0.1));
            Assert.That(midnight.Night, Is.GreaterThan(0.95));
            Assert.That(noon.Night, Is.LessThan(0.05));
        }

        [Test]
        public void DuskIsWarmerThanNoon()
        {
            var dusk = DayLighting.Evaluate(18.1, new Weather(0, 0));
            var noon = DayLighting.Evaluate(12.25, new Weather(0, 0));
            Assert.That(dusk.SunColor.B / dusk.SunColor.R, Is.LessThan(noon.SunColor.B / noon.SunColor.R));
            Assert.That(dusk.Horizon.R, Is.GreaterThan(dusk.Horizon.B), "a dusk horizon is orange");
        }

        [Test]
        public void TheSunCrossesMidnightWithoutAJump()
        {
            var a = DayLighting.Evaluate(23.999, new Weather(0, 0));
            var b = DayLighting.Evaluate(0.001, new Weather(0, 0));
            Assert.That(Math.Abs(a.SunElevationDeg - b.SunElevationDeg), Is.LessThan(0.2));
            Assert.That(Math.Abs(a.MoonElevationDeg - b.MoonElevationDeg), Is.LessThan(0.2));
        }

        [Test]
        public void TheStormDimsTheSunAndHidesTheStars()
        {
            var clear = DayLighting.Evaluate(16.5, new Weather(0, 0));
            var storm = DayLighting.Evaluate(16.5, new Weather(1, 1));
            Assert.That(storm.SunIntensity, Is.LessThan(clear.SunIntensity * 0.5));
            Assert.That(storm.Overcast, Is.EqualTo(1));
            Assert.That(DayLighting.Evaluate(21, new Weather(1, 1)).Stars, Is.EqualTo(0));
            Assert.That(storm.FogDensity, Is.GreaterThan(clear.FogDensity));
        }

        [Test]
        public void TheStormFollowsTheStormBeat()
        {
            var l = Kit.Layout();
            Assert.That(Weather.At(l, 12).Storm, Is.EqualTo(0));
            Assert.That(Weather.At(l, 19).Storm, Is.EqualTo(1));
            Assert.That(Weather.At(l, 19).Rain, Is.EqualTo(1));
            Assert.That(Weather.At(l, 23).Storm, Is.EqualTo(0));
            Assert.That(Weather.At(l, 16.5).Storm, Is.InRange(0.01, 0.99), "it builds rather than switching on");
        }
    }

    public class WaveTests
    {
        [Test]
        public void TheWavesAreZeroMeanSoTheTideIsTheGauges()
        {
            var waves = GerstnerWaves.For(1);
            double sum = 0;
            var n = 0;
            for (var x = 0; x < 400; x += 3)
            for (var z = 0; z < 400; z += 3)
            {
                GerstnerWaves.Displace(waves, x, z, 17.3, out _, out var dy, out _);
                sum += dy;
                n++;
            }

            Assert.That(Math.Abs(sum / n), Is.LessThan(0.01));
        }

        [Test]
        public void TheCrestsNeverFold()
        {
            foreach (var storm in new[] { 0.0, 0.5, 1.0 })
            {
                double q = 0;
                foreach (var w in GerstnerWaves.For(storm)) q += w.Steepness * w.K * w.AmplitudeM;
                Assert.That(q, Is.LessThanOrEqualTo(1.0 + 1e-9), "storm " + storm);
            }
        }

        [Test]
        public void TheStormRaisesTheWaves()
        {
            Assert.That(GerstnerWaves.For(1)[0].AmplitudeM, Is.EqualTo(GerstnerWaves.StormAmplitudeM).Within(1e-9));
            Assert.That(GerstnerWaves.For(0)[0].AmplitudeM, Is.EqualTo(GerstnerWaves.CalmAmplitudeM).Within(1e-9));
            Assert.That(GerstnerWaves.For(0)[0].WavelengthM, Is.EqualTo(GerstnerWaves.For(1)[0].WavelengthM), "only the amplitude moves");
        }
    }

    public class ReadingTests
    {
        static readonly DateTimeOffset Now = Kit.T0;

        [Test]
        public void AQuaysLevelIsTheMedianOfItsFreshGauges()
        {
            var l = Kit.Layout();
            var src = new FakeSource();
            src.Set("PAW-GAUGE-EAST-1", Metrics.WaterLevel, 2.10, Now.AddSeconds(-2));
            src.Set("PAW-GAUGE-EAST-2", Metrics.WaterLevel, 2.14, Now.AddSeconds(-3));
            src.Set("PAW-GAUGE-EAST-3", Metrics.WaterLevel, 9.99, Now.AddSeconds(-1)); // one wild gauge
            src.Set("PAW-LORA-EAST", Metrics.WaterLevel, 2.12, Now.AddSeconds(-60)); // a LoRaWAN gauge reporting once a minute
            var board = new TideBoard(l);
            board.Update(src, Now);
            var east = board.Region("hp-east-quay");
            Assert.That(east.Contributors, Is.EqualTo(4));
            Assert.That(east.LevelM, Is.EqualTo(2.13).Within(1e-9), "one gauge reading high does not lift the water");
            Assert.That(east.Held, Is.False);
        }

        [Test]
        public void AStaleGaugeDoesNotCount()
        {
            var l = Kit.Layout();
            var src = new FakeSource();
            src.Set("PAW-GAUGE-NORTH-1", Metrics.WaterLevel, 1.0, Now.AddSeconds(-5));
            src.Set("PAW-GAUGE-NORTH-2", Metrics.WaterLevel, 3.0, Now - TideBoard.GaugeFresh - TimeSpan.FromSeconds(1));
            var board = new TideBoard(l);
            board.Update(src, Now);
            Assert.That(board.Region("hp-north-quay").Contributors, Is.EqualTo(1));
            Assert.That(board.Region("hp-north-quay").LevelM, Is.EqualTo(1.0));
        }

        [Test]
        public void WithNoFreshGaugeTheLevelHoldsAndSaysSo()
        {
            var l = Kit.Layout();
            var src = new FakeSource();
            src.Set("PAW-GAUGE-BASIN-1", Metrics.WaterLevel, 0.8, Now);
            var board = new TideBoard(l);
            board.Update(src, Now);
            board.Update(src, Now.AddMinutes(10));
            var basin = board.Region("hp-basin");
            Assert.That(basin.Held, Is.True);
            Assert.That(basin.LevelM, Is.EqualTo(0.8));
            Assert.That(basin.Contributors, Is.EqualTo(0));
        }

        [Test]
        public void WithNoGaugeEverThereIsNoLevel()
        {
            var board = new TideBoard(Kit.Layout());
            board.Update(new FakeSource(), Now);
            Assert.That(board.Region("hp-basin").LevelM, Is.Null);
            Assert.That(board.Region("hp-basin").Held, Is.False);
        }

        [Test]
        public void TheLegacyGaugeCountsOnlyWhileItDecodes()
        {
            var l = Kit.Layout();
            var src = new FakeSource();
            src.Set("PAW-LEGACY-BASIN", Metrics.WaterLevel, 0.9, Now.AddSeconds(-10));
            var board = new TideBoard(l);
            board.Update(src, Now);
            Assert.That(board.Region("hp-basin").Gauges, Is.EqualTo(5));
            Assert.That(board.Region("hp-basin").Contributors, Is.EqualTo(1));
        }

        [Test]
        public void AlongAQuaysWallTheWaterIsThatQuaysLevel()
        {
            var xMin = new[] { -100.0, 100.0, 300.0 };
            var xMax = new[] { 0.0, 200.0, 400.0 };
            var level = new[] { 1.0, 2.0, 3.0 };
            Assert.That(TideProfile.LevelAt(-500, xMin, xMax, level), Is.EqualTo(1.0), "beyond the westernmost quay");
            Assert.That(TideProfile.LevelAt(0, xMin, xMax, level), Is.EqualTo(1.0));
            Assert.That(TideProfile.LevelAt(100, xMin, xMax, level), Is.EqualTo(2.0), "a quay's whole wall, to its very end");
            Assert.That(TideProfile.LevelAt(200, xMin, xMax, level), Is.EqualTo(2.0));
            Assert.That(TideProfile.LevelAt(50, xMin, xMax, level), Is.EqualTo(1.5).Within(1e-9), "half way between two quays");
            Assert.That(TideProfile.LevelAt(25, xMin, xMax, level), Is.InRange(1.0, 1.5));
            Assert.That(TideProfile.LevelAt(900, xMin, xMax, level), Is.EqualTo(3.0));
        }

        [Test]
        public void ALampsDimLevelIsUnknownUnlessItIsReported()
        {
            var l = Kit.Layout();
            var src = new FakeSource();
            src.Set("PAW-LIGHT-NORTH-1", Metrics.LampOn, 1, Now);
            src.Set("PAW-LIGHT-NORTH-2", Metrics.LampOn, 1, Now);
            src.Set("PAW-LIGHT-NORTH-2", Metrics.Dim, 40, Now - LampBoard.Silence - TimeSpan.FromSeconds(1));
            var board = new LampBoard(l);
            board.Update(src, Now);
            Assert.That(board.Lamp("PAW-LIGHT-NORTH-1").DimPct, Is.Null, "no dim report is not a report of 100 %");
            Assert.That(board.Lamp("PAW-LIGHT-NORTH-2").DimPct, Is.Null, "nor is a stale one");
            Assert.That(board.Lamp("PAW-LIGHT-NORTH-1").Condition, Is.EqualTo(LampCondition.On));
        }

        [Test]
        public void ALampIsLitOnlyOnAFreshReportThatItIsOn()
        {
            var l = Kit.Layout();
            var src = new FakeSource();
            src.Set("PAW-LIGHT-EAST-1", Metrics.LampOn, 1, Now.AddSeconds(-4));
            src.Set("PAW-LIGHT-EAST-1", Metrics.Dim, 40, Now.AddSeconds(-4));
            src.Set("PAW-LIGHT-EAST-2", Metrics.LampOn, 1, Now - LampBoard.Silence - TimeSpan.FromSeconds(1));
            src.Set("PAW-LIGHT-EAST-3", Metrics.LampOn, 0, Now);
            var board = new LampBoard(l);
            board.Update(src, Now);
            Assert.That(board.Lamp("PAW-LIGHT-EAST-1").Condition, Is.EqualTo(LampCondition.On));
            Assert.That(board.Lamp("PAW-LIGHT-EAST-1").DimPct, Is.EqualTo(40));
            Assert.That(board.Lamp("PAW-LIGHT-EAST-2").Condition, Is.EqualTo(LampCondition.Silent), "a silent lamp is not drawn as whatever it last said");
            Assert.That(board.Lamp("PAW-LIGHT-EAST-3").Condition, Is.EqualTo(LampCondition.Off));
            Assert.That(board.Lamp("PAW-LIGHT-EAST-4").Condition, Is.EqualTo(LampCondition.Unknown));
            Assert.That(board.Count(LampCondition.Unknown), Is.EqualTo(9));
        }

        [Test]
        public void ThePreviewIsTheStorysShape()
        {
            var l = Kit.Layout();
            var p = new PreviewSource(l);
            p.SetTime(DayClock.At(0, 19, 30), Now);
            Assert.That(p.TryGet("PAW-GAUGE-EAST-1", Metrics.WaterLevel, out var surge), Is.True);
            p.SetTime(DayClock.At(0, 13, 0), Now);
            Assert.That(p.TryGet("PAW-GAUGE-EAST-1", Metrics.WaterLevel, out var low), Is.True);
            Assert.That(surge.Value, Is.GreaterThan(l.Levels.FloodCriticalM), "the evening surge on East Quay passes the critical level");
            Assert.That(low.Value, Is.LessThan(1.0));
            Assert.That(p.TryGet("PAW-LEGACY-BASIN", Metrics.WaterLevel, out _), Is.False, "the legacy gauge never decodes");
        }
    }
}
