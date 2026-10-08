// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Json;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// A machine on a choreography track with no scene: the track's frames played at a rate (1 = the track's own pace, 0 = held),
    /// and driven by someone else once it is taken off. It answers the same questions the scene's body does, so the task layer
    /// cannot tell the two apart.
    /// </summary>
    internal sealed class TrackBody : IMachineBody
    {
        const int Stride = 8;   // x, z, heading, travel, p1, p2, steer, flag
        readonly double[] data;
        readonly double frameDt, period;
        double phase;

        public TrackBody(string id, EquipmentKind kind, double[] frames, double frameDt, double period, double startPhase)
        {
            Id = id;
            Kind = kind;
            data = frames;
            this.frameDt = frameDt;
            this.period = period;
            phase = Repeat(startPhase);
            Attached = true;
            Pose(phase, out var x, out var z, out var h, out _);
            X = x; Z = z; HeadingDegrees = h;
        }

        public string Id { get; }
        public EquipmentKind Kind { get; }
        public double X { get; private set; }
        public double Z { get; private set; }
        public double HeadingDegrees { get; private set; }
        public bool Attached { get; private set; }

        /// <summary>The first implement angle on the track now (a loader's boom): below -40 degrees it is up.</summary>
        public double P1 { get; private set; }

        public double Rate { get; private set; } = 1.0;

        /// <summary>How many seconds of its track it has played (a held machine plays less than the clock).</summary>
        public double PlayedSeconds { get; private set; }
        public int Attaches;

        double Repeat(double t) => ((t % period) + period) % period;

        void Pose(double t, out double x, out double z, out double heading, out double p1)
        {
            var frames = data.Length / Stride;
            var ft = Repeat(t) / frameDt;
            var i = Math.Min((int)ft, frames - 1);
            var j = (i + 1) % frames;
            var w = ft - (int)ft;
            double V(int k) => data[i * Stride + k] + (data[j * Stride + k] - data[i * Stride + k]) * w;
            x = V(0);
            z = V(1);
            var a = data[i * Stride + 2];
            var d = (data[j * Stride + 2] - a) % 360.0;
            if (d > 180) d -= 360; else if (d < -180) d += 360;
            heading = SiteDefinition.Canonical(a + d * w);
            p1 = V(4);
        }

        /// <summary>Plays the track for one step at its current rate.</summary>
        public void Advance(double dt)
        {
            if (!Attached) return;
            phase = Repeat(phase + dt * Rate);
            PlayedSeconds += dt * Rate;
            Pose(phase, out var x, out var z, out var h, out var p1);
            X = x; Z = z; HeadingDegrees = h; P1 = p1;
        }

        public void Detach() => Attached = false;

        public void Drive(double x, double z, double headingDegrees, double distance, double steerDegrees)
        {
            if (Attached) throw new InvalidOperationException("driving a machine that is still on its track");
            X = x; Z = z; HeadingDegrees = headingDegrees; P1 = 0;
        }

        public bool TryNearestTrackPoint(double x, double z, out TrackPoint point)
        {
            var frames = data.Length / Stride;
            var best = -1;
            var bestD = double.MaxValue;
            for (var i = 0; i < frames; i++)
            {
                double dx = data[i * Stride] - x, dz = data[i * Stride + 1] - z;
                var d = dx * dx + dz * dz;
                if (d < bestD) { bestD = d; best = i; }
            }

            point = best < 0 ? default : new TrackPoint(data[best * Stride], data[best * Stride + 1], data[best * Stride + 2], best * frameDt);
            return best >= 0;
        }

        public void Attach(TrackPoint point)
        {
            phase = Repeat(point.TrackSeconds);
            Attached = true;
            Attaches++;
            Pose(phase, out var x, out var z, out var h, out var p1);
            X = x; Z = z; HeadingDegrees = h; P1 = p1;
        }

        public bool BoomRaised => Kind == EquipmentKind.Loader && Attached && P1 < Footprint.LoaderRaisedBoomDegrees;

        public bool TryTrackPoseAhead(double seconds, out double x, out double z, out double headingDegrees, out bool boomRaised)
        {
            Pose(phase + seconds, out x, out z, out headingDegrees, out var p1);
            boomRaised = Kind == EquipmentKind.Loader && p1 < Footprint.LoaderRaisedBoomDegrees;
            return Attached;
        }

        public void SetTrackRate(double rate) => Rate = Math.Max(0.0, Math.Min(1.0, rate));
    }

    /// <summary>A straight track, for a scene of a few machines: the frames a choreography would hold for a machine driving along a line.</summary>
    internal static class StraightTrack
    {
        public const double FrameSeconds = 0.25;

        /// <summary>Frames for a machine that drives from (x, z) along <paramref name="headingDegrees"/> at <paramref name="speed"/> for <paramref name="lengthMetres"/>.</summary>
        public static double[] Frames(double x, double z, double headingDegrees, double speed, double lengthMetres)
        {
            var n = (int)Math.Ceiling(lengthMetres / speed / FrameSeconds);
            var f = new double[n * 8];
            var h = headingDegrees * Math.PI / 180.0;
            for (var i = 0; i < n; i++)
            {
                var d = speed * FrameSeconds * i;
                f[i * 8] = x + Math.Sin(h) * d;
                f[i * 8 + 1] = z + Math.Cos(h) * d;
                f[i * 8 + 2] = headingDegrees;
                f[i * 8 + 3] = d;
            }

            return f;
        }

        public static double PeriodOf(double[] frames) => frames.Length / 8 * FrameSeconds;
    }

    /// <summary>The nearest two machines ever came, over a run.</summary>
    internal sealed class ClosePair
    {
        public string A, B;
        public double MinGap = double.MaxValue, At;
        public int OverlapSteps;
    }

    /// <summary>
    /// The whole site with no scene: the 18 machines of the live choreography on their tracks, the task layer over them, and the same
    /// step the app runs (the director first, then the tracks play on). Records the closest approach of every pair, by the footprints
    /// the choreography is checked against.
    /// </summary>
    internal sealed class SceneSim
    {
        /// <summary>Where in the choreography's clock the run starts (a live take starts part-way in).</summary>
        public const double StartClock = 30.25;

        public readonly Dictionary<string, TrackBody> Bodies = new Dictionary<string, TrackBody>(StringComparer.Ordinal);
        public readonly Dictionary<string, MachineModel> Models = new Dictionary<string, MachineModel>(StringComparer.Ordinal);
        public readonly List<string> Ids = new List<string>();
        public readonly Timeline Timeline;
        public readonly TaskDirector Director;
        public readonly Dictionary<(string, string), ClosePair> Pairs = new Dictionary<(string, string), ClosePair>();
        public double Now;
        long sequence;

        public SceneSim(string fleetJsonPath, SiteGeometry site, RouteGraph graph, double startClock = StartClock)
        {
            Timeline = new Timeline(() => DateTimeOffset.UnixEpoch.AddSeconds(Now));
            using var doc = JsonDocument.Parse(File.ReadAllText(fleetJsonPath));
            var root = doc.RootElement;
            var dt = root.GetProperty("dt").GetDouble();
            var tracks = new List<(double[] Data, double Period)>();
            foreach (var t in root.GetProperty("tracks").EnumerateArray())
            {
                var arr = t.GetProperty("data");
                var d = new double[arr.GetArrayLength()];
                var k = 0;
                foreach (var v in arr.EnumerateArray()) d[k++] = v.GetDouble();
                tracks.Add((d, t.GetProperty("period").GetDouble()));
            }

            var machines = new List<(IMachineBody, MachineModel)>();
            foreach (var m in root.GetProperty("machines").EnumerateArray())
            {
                var id = m.GetProperty("id").GetString();
                var kind = Enum.Parse<EquipmentKind>(m.GetProperty("kind").GetString());
                var (data, period) = tracks[m.GetProperty("track").GetInt32()];
                var body = new TrackBody(id, kind, data, dt, period, startClock - m.GetProperty("offset").GetDouble());
                var model = new MachineModel(kind, id);
                Bodies[id] = body;
                Models[id] = model;
                Ids.Add(id);
                machines.Add((body, model));
            }

            Director = new TaskDirector(site, graph, Timeline, machines, 1);
        }

        public TaskRequest Send(string id, string key, string area = null)
        {
            var r = new TaskRequest("cmd-" + (++sequence), key, area, sequence, 1);
            Director.Submit(id, r);
            return r;
        }

        /// <summary>One step of the app: the task layer, then the tracks, then the models and the pair record.</summary>
        public void Step(double dt)
        {
            Director.Step(dt, dt);
            foreach (var id in Ids) Bodies[id].Advance(dt);
            foreach (var id in Ids)
            {
                var speed = Director.SpeedOf(id);
                Models[id].Step(dt, new MachineInput(speed, false));
            }

            Now += dt;
            Record();
        }

        public static FootprintShape ShapeOf(TrackBody b) =>
            Footprint.At(b.Kind, b.X, b.Z, b.HeadingDegrees, b.BoomRaised);

        void Record()
        {
            for (var i = 0; i < Ids.Count; i++)
            {
                var a = Bodies[Ids[i]];
                FootprintShape? sa = null;
                for (var j = i + 1; j < Ids.Count; j++)
                {
                    var b = Bodies[Ids[j]];
                    double dx = a.X - b.X, dz = a.Z - b.Z;
                    if (dx * dx + dz * dz > 30.0 * 30.0) continue;
                    sa ??= ShapeOf(a);
                    var g = Footprint.Gap(sa.Value, ShapeOf(b));
                    if (!Pairs.TryGetValue((Ids[i], Ids[j]), out var p)) Pairs[(Ids[i], Ids[j])] = p = new ClosePair { A = Ids[i], B = Ids[j] };
                    if (g < p.MinGap) { p.MinGap = g; p.At = Now; }
                    if (g < 0) p.OverlapSteps++;
                }
            }
        }
    }
}
