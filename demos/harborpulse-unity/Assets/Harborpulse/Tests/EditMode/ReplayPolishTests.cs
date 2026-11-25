// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>What a review of the first replay renders found: rain that climbed, mirrored ticks, a truck that never moved, and a story nobody could follow cold.</summary>
    public class ReplayPolishTests
    {
        static readonly Weather Storm = new Weather(1, 1), Drizzle = new Weather(0.2, 0.08);

        // ---- rain
        [TestCase(30)]
        [TestCase(60)]
        [TestCase(24)]
        public void EveryDropFallsItsOwnSpeedEveryFrameAndNeverClimbs(int fps)
        {
            foreach (var weather in new[] { Storm, Drizzle, new Weather(0.5, 0.02) })
            {
                RainField.Wind(weather, out var windX, out var windZ); var dt = 1.0 / fps; var wraps = 0; var steps = 0;
                for (var drop = 0; drop < 200; drop++)
                {
                    RainField.TryStreak(drop, 0, weather, 10, 30, -40, out var before);
                    for (var frame = 1; frame <= 90; frame++)
                    {
                        RainField.TryStreak(drop, frame * dt, weather, 10, 30, -40, out var now);
                        var dy = now.Y - before.Y; steps++;
                        if (dy > 0) { wraps++; Assert.That(dy, Is.EqualTo(RainField.BoxY - RainField.FallSpeed(drop) * dt).Within(1e-6), "a drop goes up only by re-entering at the top of the box"); }
                        else
                        {
                            Assert.That(-dy, Is.EqualTo(RainField.FallSpeed(drop) * dt).Within(1e-6), "drop " + drop + " at " + fps + " fps");
                            var dx = now.X - before.X; if (Math.Abs(dx) < RainField.BoxX / 2) Assert.That(dx, Is.EqualTo(windX * dt).Within(1e-6));
                            var dz = now.Z - before.Z; if (Math.Abs(dz) < RainField.BoxZ / 2) Assert.That(dz, Is.EqualTo(windZ * dt).Within(1e-6));
                        }
                        before = now;
                    }
                }
                // a drop crosses the box in about three seconds: re-entries are rare, never the rule (the old sheet wrapped every frame in light rain)
                Assert.That(wraps / (double)steps, Is.LessThan(0.03));
            }
        }

        [Test]
        public void AStreakIsItsDropsPathOverOneExposureWhateverTheRainsStrength()
        {
            foreach (var weather in new[] { Storm, Drizzle })
            {
                RainField.Wind(weather, out var windX, out var windZ);
                for (var drop = 0; drop < 100; drop++)
                {
                    RainField.TryStreak(drop, 3.7, weather, 0, 20, 0, out var s);
                    var fall = RainField.FallSpeed(drop);
                    Assert.That(fall, Is.InRange(RainField.FallMin, RainField.FallMax));
                    Assert.That(s.TailY, Is.EqualTo(fall * RainField.ExposureS).Within(1e-9), "the tail points back up the fall");
                    Assert.That(s.TailX, Is.EqualTo(-windX * RainField.ExposureS).Within(1e-9)); Assert.That(s.TailZ, Is.EqualTo(-windZ * RainField.ExposureS).Within(1e-9));
                    Assert.That(s.TailY, Is.InRange(0.4, 0.6), "about half a metre, in drizzle as in a storm");
                }
            }
            RainField.Wind(Storm, out var stormX, out _); RainField.Wind(Drizzle, out var calmX, out _);
            Assert.That(Math.Abs(stormX), Is.GreaterThan(Math.Abs(calmX)), "the storm leans the rain harder");
            Assert.That(stormX * calmX, Is.GreaterThan(0), "and always the same way");
        }

        [Test]
        public void StrengthChangesHowManyDropsThereAreNeverWhereOneIs()
        {
            Assert.That(RainField.Count(new Weather(0, 0)), Is.EqualTo(0)); Assert.That(RainField.Count(Storm), Is.EqualTo(RainField.Capacity));
            Assert.That(RainField.Count(new Weather(1, 0.25)), Is.EqualTo(RainField.Capacity / 2));
            Assert.That(RainField.Count(Drizzle), Is.InRange(1, RainField.Count(new Weather(0.2, 0.5))));
            // same wind, more rain: the drops already falling stay where they are
            RainField.TryStreak(7, 12.5, new Weather(0.6, 0.2), 0, 20, 0, out var light); RainField.TryStreak(7, 12.5, new Weather(0.6, 0.9), 0, 20, 0, out var heavy);
            Assert.That(heavy.X, Is.EqualTo(light.X)); Assert.That(heavy.Y, Is.EqualTo(light.Y)); Assert.That(heavy.Z, Is.EqualTo(light.Z));
        }

        [Test]
        public void TheRainStaysInTheWorldWhenTheEyeMovesAndIsAFunctionOfTheClock()
        {
            // a small move of the eye leaves a drop where it was (unless it left the box); the old sheet moved with the camera
            var moved = 0;
            for (var drop = 0; drop < 300; drop++)
            {
                RainField.TryStreak(drop, 5, Storm, 0, 20, 0, out var a); RainField.TryStreak(drop, 5, Storm, 0.8, 20.3, -0.5, out var b);
                if (Math.Abs(a.X - b.X) > 1e-9 || Math.Abs(a.Y - b.Y) > 1e-9 || Math.Abs(a.Z - b.Z) > 1e-9) moved++;
                Assert.That(b.X, Is.InRange(0.8 - RainField.BoxX / 2, 0.8 + RainField.BoxX / 2)); Assert.That(b.Y, Is.InRange(20.3 - RainField.BoxY / 2, 20.3 + RainField.BoxY / 2));
            }
            Assert.That(moved, Is.LessThan(30), "only drops at the box's faces change copy");
            // a seek to t is a play to t
            RainField.TryStreak(11, 41.25, Storm, 3, 9, 4, out var once); RainField.TryStreak(11, 0, Storm, 3, 9, 4, out _); RainField.TryStreak(11, 41.25, Storm, 3, 9, 4, out var again);
            Assert.That(again.Y, Is.EqualTo(once.Y)); Assert.That(again.X, Is.EqualTo(once.X));
            // nothing at the lens
            var near = 0;
            for (var drop = 0; drop < RainField.Capacity; drop++)
                if (!RainField.TryStreak(drop, 2, Storm, 0, 20, 0, out var s)) { near++; Assert.That(Math.Sqrt(s.X * s.X + (s.Y - 20) * (s.Y - 20) + s.Z * s.Z), Is.LessThan(RainField.LensClearM)); }
            Assert.That(near, Is.LessThan(RainField.Capacity / 50));
        }

        // ---- the cleared tick
        [Test]
        public void AClearedTickReadsLeftToRightFromEveryEye()
        {
            for (var bearing = 0; bearing < 360; bearing += 15)
            {
                var eyeX = 40 + 30 * Math.Sin(bearing * Math.PI / 180); var eyeZ = -12 + 30 * Math.Cos(bearing * Math.PI / 180);
                var yaw = AlarmBeacons.TickYawDeg(40, -12, eyeX, eyeZ) * Math.PI / 180;
                // the tick's own +x, turned by the yaw (a yaw about +y takes +x to (cos, 0, -sin)) ...
                var glyphX = Math.Cos(yaw); var glyphZ = -Math.Sin(yaw);
                // ... against the eye's right: up cross forward, forward being from the eye to the tick
                var forwardX = (40 - eyeX) / 30; var forwardZ = (-12 - eyeZ) / 30; var rightX = forwardZ; var rightZ = -forwardX;
                Assert.That(glyphX * rightX + glyphZ * rightZ, Is.EqualTo(1).Within(1e-9), "bearing " + bearing + ": the short stroke is on the eye's left");
            }
            // CONTROL: a tick that keeps its device's heading is mirrored for an eye behind it
            Assert.That(Math.Cos(0) * -1, Is.LessThan(0), "yaw 0 seen from +z: its +x is the eye's left");
        }

        // ---- the context line
        [Test]
        public void EveryStoryPageSaysWhereTheViewerIsAndNeverWhatThePlatformDid()
        {
            var story = DirectorKit.Story();
            foreach (var page in story.Pages.Where(p => p.Id != "9"))
            {
                Assert.That(page.ContextLine, Is.Not.Empty, "page " + page.Id); Assert.That(page.ContextLine.Length, Is.LessThanOrEqualTo(64));
                Assert.That(page.ContextLine, Does.Contain(" · "), "place · what is in frame");
            }
            Assert.That(story.Page("9").ContextLine, Is.Empty, "the end card needs none");
            foreach (var bad in new[] { "North Quay · the platform starts the pump", "East Quay · 6 lamps", "The Basin · alarm detected", "x".PadRight(65, 'x') })
            {
                var edited = DirectorKit.Object(DirectorKit.StoryText()); DirectorKit.Obj(DirectorKit.Arr(edited["pages"])[1])["context"] = bad;
                Assert.Throws<FormatException>(() => Storyboard.Parse(DirectorKit.Json(edited), new PresentationMap(Kit.Layout())), bad);
            }
            // the live director and a cut frame carry it to the HUD
            var layout = Kit.Layout(); var map = new PresentationMap(layout); var host = new DirectorHost(story, layout, map);
            var view = host.Tick(new DirectorSample("run", 6.0, 1), new DirectorInputs { Run = "run", Sim = 6.0 });
            for (var t = 2; t < 8 && view.Page == null; t++) view = host.Tick(new DirectorSample("run", 6.0 + t / 60.0, t), new DirectorInputs { Run = "run", Sim = 6.0 + t / 60.0 });
            Assert.That(view.Page, Is.EqualTo("1")); Assert.That(view.Context, Is.EqualTo(story.Page("1").ContextLine));
        }

        // ---- the truck
        static string Row(double at, int seq, string kind, string data) => "{\"wallS\":" + at.ToString("0.0", CultureInfo.InvariantCulture) + ",\"sequence\":" + seq + ",\"kind\":\"" + kind + "\",\"data\":" + data + "}";
        [Test]
        public void ARecordingWithTruckFixesDrawsTheTruckWhereThePlatformHadIt()
        {
            var layout = Kit.Layout(); var truck = layout.Devices.First(d => d.Kind == DeviceKind.Truck); var route = layout.TruckRoute;
            var run = "2026-11-24T20:41:47.0000000+00:00"; var start = DateTimeOffset.Parse(run, CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind);
            var lines = new List<string> { Row(0, 1, "observation", DirectorWire.Json(w => DirectorWire.Observation(w, DirectorKit.O("ledger", key: "run", id: "run/" + run, source: EvidenceSource.Scenario, run: run, wall: 0, sim: 6, cycle: "")))) };
            // one /status row a second, as the player writes them; the truck is out on its route from second 3
            for (var t = 0; t <= 10; t++)
            {
                var at = route[Math.Min(route.Count - 1, Math.Max(0, t - 3))];
                var fix = t < 3 ? "" : ",\"truck\":{\"lat\":" + at.Lat.ToString("R", CultureInfo.InvariantCulture) + ",\"lon\":" + at.Lon.ToString("R", CultureInfo.InvariantCulture) + ",\"heading\":90,\"speed\":8,\"at\":\"" + start.AddSeconds(t - 0.2).ToString("O", CultureInfo.InvariantCulture) + "\"}";
                lines.Add(Row(t, 10 + t, "world", "{\"sample\":\"/status\",\"simHour\":" + (6 + t / 60.0).ToString("R", CultureInfo.InvariantCulture) + ",\"beat\":\"night\",\"layoutDigest\":null,\"defects\":[],\"alarms\":[{\"device\":\"" + truck.ExternalId + "\",\"key\":\"truck-in-flood-plain\",\"severity\":\"WARNING\",\"acknowledged\":false}]" + fix + "}"));
            }
            foreach (var text in new[] { string.Join("\n", lines), Compact(RecordedDay.Load(new StringReader(string.Join("\n", lines)))) })
            {
                var day = RecordedDay.Load(new StringReader(text)); var source = new ReplaySource(day, layout); var model = new HarborObservedModel(layout); var map = new PresentationMap(layout);
                source.Seek(2); Assert.That(source.TryLocation(truck.ExternalId, out _), Is.False);
                Assert.That(model.Update(source, new ClockReading(day.SimAt(2) * 3600, ClockMode.Live, ""), source.Now).Truck.NoFix, Is.True);
                var fixes = new List<TimedFix>();
                for (var t = 3; t <= 10; t++)
                {
                    source.Seek(t); Assert.That(source.TryLocation(truck.ExternalId, out var fix), Is.True, "second " + t);
                    Assert.That(fix.Speed, Is.EqualTo(8)); Assert.That(fix.Heading, Is.EqualTo(90));
                    fixes.Add(new TimedFix((fix.OccurredAt - day.Epoch).TotalSeconds, new LatLon(fix.Latitude.Value, fix.Longitude.Value)));
                    var frame = model.Update(source, new ClockReading(day.SimAt(t) * 3600, ClockMode.Live, ""), source.Now);
                    Assert.That(frame.Truck.NoFix, Is.False); Assert.That(frame.Truck.Note, Is.EqualTo("platform fix"));
                }
                // drawn through the same map the live scene uses, away from the depot once it has left
                var first = map.TruckAt(fixes, fixes[0].WallS).At; var last = map.TruckAt(fixes, fixes[fixes.Count - 1].WallS).At;
                Assert.That(Math.Abs(last.X - first.X) + Math.Abs(last.Z - first.Z), Is.GreaterThan(5), "the drawn truck moves along its route");
            }
        }
        [Test]
        public void ARecordingCutOffMidRowLosesOnlyThatRow()
        {
            var run = "2026-11-24T20:41:47.0000000+00:00";
            var lines = new[] { Row(0, 1, "observation", DirectorWire.Json(w => DirectorWire.Observation(w, DirectorKit.O("ledger", key: "run", id: "run/" + run, source: EvidenceSource.Scenario, run: run, wall: 0, sim: 6, cycle: "")))),
                Row(0, 2, "world", "{\"sample\":\"/status\",\"simHour\":6}"), Row(60, 3, "world", "{\"sample\":\"/status\",\"simHour\":7}") };
            var whole = string.Join("\n", lines) + "\n";
            var torn = RecordedDay.Load(new StringReader(whole + Row(120, 4, "world", "{\"sample\":\"/status\",\"simHour\":8}").Substring(0, 40)));
            Assert.That(torn.TornRows, Is.EqualTo(1)); Assert.That(torn.EndS, Is.EqualTo(60)); Assert.That(torn.SimAt(60), Is.EqualTo(7));
            Assert.That(RecordedDay.Load(new StringReader(whole)).TornRows, Is.EqualTo(0));
            // a bad row that is not the last is a damaged recording, and is refused
            Assert.Throws<FormatException>(() => RecordedDay.Load(new StringReader(lines[0] + "\n" + lines[1].Substring(0, 40) + "\n" + lines[2] + "\n")));
        }
        static string Compact(RecordedDay day) { var text = new StringWriter { NewLine = "\n" }; day.WriteCompact(text); return text.ToString(); }
    }
}
