// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    // The track clock and pose sampling every machine on a choreography track is drawn by, played through one class.
    public sealed class TrackPlayerTests
    {
        const int Stride = 8;

        sealed class Choreo
        {
            public float Dt;
            public List<(float Period, float[] Data)> Tracks = new List<(float, float[])>();
            public List<(string Id, int Track, float Offset)> Machines = new List<(string, int, float)>();
        }

        static Choreo Load(string file)
        {
            using var doc = JsonDocument.Parse(File.ReadAllText(CommandKit.AssetPath("Data/" + file)));
            var root = doc.RootElement;
            var c = new Choreo { Dt = (float)root.GetProperty("dt").GetDouble() };
            foreach (var t in root.GetProperty("tracks").EnumerateArray())
                c.Tracks.Add(((float)t.GetProperty("period").GetDouble(), t.GetProperty("data").EnumerateArray().Select(v => (float)v.GetDouble()).ToArray()));
            foreach (var m in root.GetProperty("machines").EnumerateArray())
                c.Machines.Add((m.GetProperty("id").GetString(), m.GetProperty("track").GetInt32(), (float)m.GetProperty("offset").GetDouble()));
            return c;
        }

        // The sampling the fleet preview did before the class existed, line for line (QuarryFleetPreview.Seek).
        static float[] Reference(float[] data, float dt, float period, float offset, float t, out bool loaded)
        {
            int frames = data.Length / Stride;
            float ft = Mathf.Repeat(t - offset, period) / dt;
            int i = Mathf.Min((int)ft, frames - 1), j = (i + 1) % frames;
            float w = ft - (int)ft;
            float V(int k) => Mathf.Lerp(data[i * Stride + k], data[j * Stride + k], w);
            float heading = data[i * Stride + 2] + Mathf.DeltaAngle(data[i * Stride + 2], data[j * Stride + 2]) * w;
            float travel = j == 0 ? data[i * Stride + 3] : V(3);
            loaded = data[i * Stride + 7] > 0.5f;
            return new[] { V(0), V(1), heading, travel, V(4), V(5), V(6) };
        }

        // ---- playing the site's own choreography

        [Test]
        public void EveryMachineOfBothChoreographiesIsPlayedFrameForFrameAsItWasBeforeTheClassExisted()
        {
            var checkedFrames = 0;
            foreach (var file in new[] { "quarry_fleet.json", "quarry_fleet_live.json" })
            {
                var c = Load(file);
                foreach (var (id, track, offset) in c.Machines)
                {
                    var (period, data) = c.Tracks[track];
                    var p = new TrackPlayer(data, c.Dt, period, offset);
                    // through two loops and a little before time began, at an interval that lands on and between frames
                    for (var t = -period * 0.3f; t < period * 2.1f; t += 0.37f)
                    {
                        var f = p.SampleAt(t);
                        var r = Reference(data, c.Dt, period, offset, t, out var loaded);
                        Assert.AreEqual(r[0], f.X, $"{file} {id} x at {t}");
                        Assert.AreEqual(r[1], f.Z, $"{file} {id} z at {t}");
                        Assert.AreEqual(r[2], f.Heading, $"{file} {id} heading at {t}");
                        Assert.AreEqual(r[3], f.Travel, $"{file} {id} travel at {t}");
                        Assert.AreEqual(r[4], f.P1, $"{file} {id} p1 at {t}");
                        Assert.AreEqual(r[5], f.P2, $"{file} {id} p2 at {t}");
                        Assert.AreEqual(r[6], f.Steer, $"{file} {id} steer at {t}");
                        Assert.AreEqual(loaded, f.Loaded, $"{file} {id} load at {t}");
                        checkedFrames++;
                    }
                }
            }

            Assert.Greater(checkedFrames, 10000);
        }

        [Test]
        public void TheNearestPlaceOnATrackIsTheOneTheSceneFoundBefore()
        {
            var c = Load("quarry_fleet_live.json");
            var (period, data) = c.Tracks[0];
            var p = new TrackPlayer(data, c.Dt, period, 0f);
            foreach (var (x, z) in new[] { (4f, 36f), (-44f, -30f), (71f, -12f), (500f, 500f), (0f, 0f) })
            {
                // the loop over every frame the scene ran
                var best = -1;
                var bestD = float.MaxValue;
                for (var i = 0; i < data.Length / Stride; i++)
                {
                    float dx = data[i * Stride] - x, dz = data[i * Stride + 1] - z;
                    var d = dx * dx + dz * dz;
                    if (d < bestD) { bestD = d; best = i; }
                }

                Assert.IsTrue(p.TryNearest(x, z, out var pt));
                Assert.AreEqual(data[best * Stride], pt.X);
                Assert.AreEqual(data[best * Stride + 1], pt.Z);
                Assert.AreEqual(data[best * Stride + 2], pt.HeadingDegrees);
                Assert.AreEqual(best * c.Dt, pt.TrackSeconds);
            }
        }

        // ---- the clock

        // a straight track of 8 frames, 1 s apart, 10 m a frame along +z
        static float[] Straight()
        {
            var d = new float[8 * Stride];
            for (var i = 0; i < 8; i++)
            {
                d[i * Stride + 1] = 10f * i;
                d[i * Stride + 3] = 10f * i;
                d[i * Stride + 7] = i >= 4 ? 1f : 0f;
            }

            return d;
        }

        [Test]
        public void ATrackIsReadAtTheSceneClockMinusItsOffsetAndLoops()
        {
            var p = new TrackPlayer(Straight(), 1f, 8f, 3f);
            Assert.AreEqual(0f, p.SampleAt(3f).Z, 1e-6, "at its offset it is at the start of its loop");
            Assert.AreEqual(25f, p.SampleAt(5.5f).Z, 1e-5);
            Assert.AreEqual(p.SampleAt(5.5f).Z, p.SampleAt(5.5f + 8f).Z, 1e-4, "and a loop later it is in the same place");
            Assert.AreEqual(p.SampleAt(5.5f).Z, p.SampleAt(5.5f - 16f).Z, 1e-4, "or two before");
            Assert.AreEqual(2.5f, p.TrackSecondsAt(5.5f), 1e-6);
            Assert.IsFalse(p.SampleAt(6.9f).Loaded);
            Assert.IsTrue(p.SampleAt(7.1f).Loaded, "the load flag is read from the frame the machine is on");
        }

        [Test]
        public void AtTheEndOfTheLoopThePlaceHeadsBackToTheStartAndTheTravelDoesNotJump()
        {
            var p = new TrackPlayer(Straight(), 1f, 8f, 0f);
            var f = p.Sample(7.5f);
            Assert.AreEqual(35f, f.Z, 1e-4, "half way from the last frame's place to the first's");
            Assert.AreEqual(70f, f.Travel, 1e-4, "the travel stays at the last frame's: the counter wraps with the loop, so a wheel does not spin back");
        }

        [Test]
        public void AHeadingTurnsTheShortWayRoundThroughNorth()
        {
            var d = new float[2 * Stride];
            d[2] = 350f;
            d[Stride + 2] = 10f;
            var p = new TrackPlayer(d, 1f, 2f, 0f);
            Assert.AreEqual(360f, p.Sample(0.5f).Heading, 1e-3, "half way from 350 to 10 degrees is 0, not 180");
        }

        [Test]
        public void ARateOfZeroHoldsTheMachineWhereItIsAndALowerRateFallsBehind()
        {
            var held = new TrackPlayer(Straight(), 1f, 8f, 0f);
            held.SetRate(0f);
            var at = held.SampleAt(2f).Z;
            for (var t = 2f; t < 12f; t += 0.5f) held.Advance(0.5f);
            Assert.AreEqual(at, held.SampleAt(12f).Z, 1e-4, "ten seconds later it has not moved");
            Assert.AreEqual(10f, held.Offset, 1e-5, "its clock was put back by every second it did not play");

            var slow = new TrackPlayer(Straight(), 1f, 8f, 0f);
            slow.SetRate(0.25f);
            slow.Advance(4f);
            Assert.AreEqual(3f, slow.Offset, 1e-6, "three of every four seconds were not played");
            Assert.AreEqual(1f, slow.TrackSecondsAt(4f), 1e-6, "it has played one second of four");

            var full = new TrackPlayer(Straight(), 1f, 8f, 1.5f);
            full.Advance(100f);
            Assert.AreEqual(1.5f, full.Offset, "at its own pace it falls behind by nothing");
        }

        [Test]
        public void ARateStaysBetweenHeldAndItsOwnPace()
        {
            var p = new TrackPlayer(Straight(), 1f, 8f, 0f);
            p.SetRate(3f);
            Assert.AreEqual(1f, p.Rate, "no faster than the track's own pace");
            p.SetRate(-2f);
            Assert.AreEqual(0f, p.Rate, "and it is never played backwards");
        }

        [Test]
        public void ATrackTakenOffPlaysAtItsOwnPaceOncePutBackAtTheSecondItWasPutBackAt()
        {
            var p = new TrackPlayer(Straight(), 1f, 8f, 0f);
            p.SetRate(0.5f);
            p.Detach();
            Assert.AreEqual(1f, p.Rate, "whatever held it back is over");
            p.Attach(20f, 6f);
            Assert.AreEqual(6f, p.TrackSecondsAt(20f), 1e-6, "put back 6 s into the loop at scene time 20");
            Assert.AreEqual(7f, p.TrackSecondsAt(21f), 1e-6);
        }

        // ---- a body on a track, under the task layer

        static TrackBody HaulerOnTheLoop(out Choreo c)
        {
            c = Load("quarry_fleet_live.json");
            var (id, track, _) = c.Machines.First(m => m.Id == "SP-HL-0003");
            var (period, data) = c.Tracks[track];
            // 75 s into the loop when the clock starts
            return new TrackBody(id, DeviceChain.Sitepulse.Simulation.EquipmentKind.Hauler, data, c.Dt, period, -300f * c.Dt);
        }

        [Test]
        public void ABodyHeldByItsRateStandsWhereItIsAndPlaysLessThanTheClock()
        {
            var body = HaulerOnTheLoop(out _);
            for (var i = 0; i < 40; i++) body.Advance(0.25);
            double x = body.X, z = body.Z;
            body.SetTrackRate(0.0);
            for (var i = 0; i < 400; i++) body.Advance(0.25);
            Assert.AreEqual(x, body.X, "a rate of 0 holds it for 100 s");
            Assert.AreEqual(z, body.Z);
            Assert.AreEqual(10.0, body.PlayedSeconds, 1e-3, "it has played the ten seconds before it was held, and none since");
            body.SetTrackRate(1.0);
            for (var i = 0; i < 40; i++) body.Advance(0.25);
            Assert.AreEqual(20.0, body.PlayedSeconds, 1e-3);
            Assert.AreNotEqual(x, body.X, "released, it plays on from where it stood");
        }

        [Test]
        public void ACommandedBodyIsPutBackOnItsTrackAtTheSecondTheTaskLayerNamedAndPlaysFromThere()
        {
            var body = HaulerOnTheLoop(out var c);
            var timeline = new Timeline();
            var ctl = new MachineController(body, new DeviceChain.Sitepulse.Simulation.MachineModel(DeviceChain.Sitepulse.Simulation.EquipmentKind.Hauler, body.Id),
                CommandKit.Site, CommandKit.Graph, new BayReservations(), new ParkingLot(), timeline, new OpenWorld());
            var request = new TaskRequest("c-1", "goto-area", "sp-zone-yard", 1, 1);
            ctl.Submit(request);
            double Step() { ctl.Step(0.25, 0.25); body.Advance(0.25); return 0.25; }
            for (var t = 0.0; t < 1500 && !request.IsComplete; t += Step()) { }
            Assert.IsTrue(request.Completion.Answer().Succeeded);
            Assert.IsFalse(body.Attached, "it left its track to go there");
            Assert.IsNull(ctl.Resume());
            for (var t = 0.0; t < 1500 && ctl.Mode != MachineMode.Working; t += Step()) { }
            Assert.AreEqual(MachineMode.Working, ctl.Mode);
            Assert.IsTrue(body.Attached);
            Assert.AreEqual(1, body.Attaches);
            // it is on its loop at a frame of it: where it stands is where the loop is at the second it is playing
            var (period, data) = c.Tracks[c.Machines.First(m => m.Id == body.Id).Track];
            var p = new TrackPlayer(data, c.Dt, period, 0f);
            var f = p.Sample(body.TrackSeconds);
            Assert.AreEqual(f.X, body.X, 1e-3);
            Assert.AreEqual(f.Z, body.Z, 1e-3);
        }

        [Test]
        public void ATrackNeedsFramesAndATimeToPlayThem()
        {
            Assert.Throws<ArgumentNullException>(() => new TrackPlayer(null, 1f, 1f, 0f));
            Assert.Throws<ArgumentException>(() => new TrackPlayer(new float[3], 1f, 1f, 0f));
            Assert.Throws<ArgumentException>(() => new TrackPlayer(Straight(), 0f, 8f, 0f));
            Assert.Throws<ArgumentException>(() => new TrackPlayer(Straight(), 1f, 0f, 0f));
        }
    }
}
