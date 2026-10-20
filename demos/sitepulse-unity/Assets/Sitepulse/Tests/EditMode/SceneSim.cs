// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Json;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>A straight track, for a scene of a few machines: the frames a choreography would hold for a machine driving along a line.</summary>
    internal static class StraightTrack
    {
        public const float FrameSeconds = 0.25f;

        /// <summary>Frames for a machine that drives from (x, z) along <paramref name="headingDegrees"/> at <paramref name="speed"/> for <paramref name="lengthMetres"/>.</summary>
        public static float[] Frames(double x, double z, double headingDegrees, double speed, double lengthMetres)
        {
            var n = (int)Math.Ceiling(lengthMetres / speed / FrameSeconds);
            var f = new float[n * 8];
            var h = headingDegrees * Math.PI / 180.0;
            for (var i = 0; i < n; i++)
            {
                var d = speed * FrameSeconds * i;
                f[i * 8] = (float)(x + Math.Sin(h) * d);
                f[i * 8 + 1] = (float)(z + Math.Cos(h) * d);
                f[i * 8 + 2] = (float)headingDegrees;
                f[i * 8 + 3] = (float)d;
            }

            return f;
        }

        public static float PeriodOf(float[] frames) => frames.Length / 8 * FrameSeconds;
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

        public SceneSim(string fleetJsonPath, SiteGeometry site, RouteGraph graph, PreparedSite prepared, double startClock = StartClock)
        {
            Timeline = new Timeline(() => DateTimeOffset.UnixEpoch.AddSeconds(Now));
            using var doc = JsonDocument.Parse(File.ReadAllText(fleetJsonPath));
            var root = doc.RootElement;
            var dt = (float)root.GetProperty("dt").GetDouble();
            var tracks = new List<(float[] Data, float Period)>();
            foreach (var t in root.GetProperty("tracks").EnumerateArray())
            {
                var arr = t.GetProperty("data");
                var d = new float[arr.GetArrayLength()];
                var k = 0;
                foreach (var v in arr.EnumerateArray()) d[k++] = (float)v.GetDouble();
                tracks.Add((d, (float)t.GetProperty("period").GetDouble()));
            }

            var machines = new List<(IMachineBody, MachineModel)>();
            foreach (var m in root.GetProperty("machines").EnumerateArray())
            {
                var id = m.GetProperty("id").GetString();
                var kind = Enum.Parse<EquipmentKind>(m.GetProperty("kind").GetString());
                var (data, period) = tracks[m.GetProperty("track").GetInt32()];
                var body = new TrackBody(id, kind, data, dt, period, (float)(m.GetProperty("offset").GetDouble() - startClock));
                var model = new MachineModel(kind, id);
                Bodies[id] = body;
                Models[id] = model;
                Ids.Add(id);
                machines.Add((body, model));
            }

            Director = new TaskDirector(site, graph, prepared, Timeline, machines, 1);
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
