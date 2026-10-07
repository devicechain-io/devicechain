// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>A machine body with no scene: it goes where it is driven, and remembers what was done to it.</summary>
    internal sealed class FakeBody : IMachineBody
    {
        readonly IList<TrackPoint> track;

        public FakeBody(string id, EquipmentKind kind, double x, double z, double heading = 0.0, IList<TrackPoint> track = null)
        {
            Id = id;
            Kind = kind;
            X = x;
            Z = z;
            HeadingDegrees = heading;
            this.track = track ?? new List<TrackPoint>();
        }

        public string Id { get; }
        public EquipmentKind Kind { get; }
        public double X { get; private set; }
        public double Z { get; private set; }
        public double HeadingDegrees { get; private set; }
        public bool Attached { get; private set; } = true;
        public int Detaches, Attaches, Drives;
        public double Travelled;
        public TrackPoint? LastAttach;

        /// <summary>Where the machine stood when it was put back on its track (the place it had driven to, before the jump to the track).</summary>
        public (double X, double Z)? AttachedFrom;

        public void Detach()
        {
            Attached = false;
            Detaches++;
        }

        public void Drive(double x, double z, double headingDegrees, double distance, double steerDegrees)
        {
            if (Attached) throw new InvalidOperationException("driving a machine that is still on its track");
            X = x;
            Z = z;
            HeadingDegrees = headingDegrees;
            Travelled += distance;
            Drives++;
        }

        public bool TryNearestTrackPoint(double x, double z, out TrackPoint point)
        {
            point = default;
            var best = double.MaxValue;
            var found = false;
            foreach (var p in track)
            {
                var d = (p.X - x) * (p.X - x) + (p.Z - z) * (p.Z - z);
                if (d < best) { best = d; point = p; found = true; }
            }

            return found;
        }

        public void Attach(TrackPoint point)
        {
            Attached = true;
            Attaches++;
            LastAttach = point;
            AttachedFrom = (X, Z);
            X = point.X;
            Z = point.Z;
            HeadingDegrees = point.HeadingDegrees;
        }
    }

    /// <summary>A site where nothing is ever in the way, unless a test says so.</summary>
    internal sealed class OpenWorld : ITaskWorld
    {
        public bool Block;
        public bool BayBusy;
        public bool Blocked(string id, double x, double z, double headingDegrees) => Block;
        public bool BayClear(string id) => !BayBusy;
        public bool Occupied(string id, double x, double z) => false;

        /// <summary>Somebody is driving past the place the machine would rejoin its track.</summary>
        public bool TrackBusy;
        public bool TrackClear(string id, double x, double z) => !TrackBusy;
    }

    internal static class CommandKit
    {
        static SiteGeometry site;
        static RouteGraph graph;
        static JsonDocument fleet;

        public static string AssetPath(string relative) => Path.Combine(Application.dataPath, "Sitepulse", relative);

        public static SiteGeometry Site => site ??= SiteGeometryReader.Parse(File.ReadAllText(AssetPath("Art/Terrain/quarry_features.json")));
        static TerrainHeights ground;

        /// <summary>The ground the scene is built on, read from the same files.</summary>
        public static TerrainHeights Ground => ground ??= SiteGeometryReader.ParseTerrain(File.ReadAllText(AssetPath("Art/Terrain/quarry_features.json")), File.ReadAllBytes(AssetPath("Art/Terrain/quarry_height.bytes")));

        public static RouteGraph Graph => graph ??= RouteGraph.Build(Site, Ground);

        static JsonElement Fleet => (fleet ??= JsonDocument.Parse(File.ReadAllText(AssetPath("Data/quarry_fleet_live.json")))).RootElement;

        public static int TrackCount => Fleet.GetProperty("tracks").GetArrayLength();

        /// <summary>Where machine <paramref name="id"/> is on its track <paramref name="seconds"/> into the live choreography (the nearest frame).</summary>
        public static TrackPoint MachineAt(string id, double seconds)
        {
            foreach (var m in Fleet.GetProperty("machines").EnumerateArray())
            {
                if (m.GetProperty("id").GetString() != id) continue;
                var track = m.GetProperty("track").GetInt32();
                var period = Fleet.GetProperty("tracks")[track].GetProperty("period").GetDouble();
                var frames = Track(track);
                var t = (seconds - m.GetProperty("offset").GetDouble()) % period;
                if (t < 0) t += period;
                return frames[(int)Math.Round(t / Fleet.GetProperty("dt").GetDouble()) % frames.Count];
            }

            throw new ArgumentException("no machine " + id, nameof(id));
        }

        /// <summary>The frames of one of the live choreography's tracks, as track points.</summary>
        public static List<TrackPoint> Track(int index)
        {
            var dt = Fleet.GetProperty("dt").GetDouble();
            var data = Fleet.GetProperty("tracks")[index].GetProperty("data");
            var n = data.GetArrayLength() / 8;
            var list = new List<TrackPoint>(n);
            for (var i = 0; i < n; i++) list.Add(new TrackPoint(data[i * 8].GetDouble(), data[i * 8 + 1].GetDouble(), data[i * 8 + 2].GetDouble(), i * dt));
            return list;
        }
    }

    /// <summary>One machine's task layer over fakes, and the clock that drives it.</summary>
    internal sealed class Rig
    {
        public readonly string Id;
        public readonly FakeBody Body;
        public readonly MachineModel Model;
        public readonly Timeline Timeline = new Timeline();
        public readonly BayReservations Bay = new BayReservations();
        public readonly ParkingLot Parking = new ParkingLot();
        public readonly OpenWorld World = new OpenWorld();
        public readonly MachineController Controller;
        public double Now;
        long sequence;

        public Rig(string id = "SP-HL-0003", EquipmentKind kind = EquipmentKind.Hauler, double? x = null, double? z = null, bool withTrack = true, RouteGraph graph = null)
        {
            Id = id;
            var track = withTrack ? CommandKit.Track(0) : null;
            // by default a truck on the haul loop, a good way from everything
            var start = track != null ? track[300] : new TrackPoint(0, 0, 0, 0);
            Body = new FakeBody(id, kind, x ?? start.X, z ?? start.Z, start.HeadingDegrees, track);
            Model = new MachineModel(kind, id);
            Controller = new MachineController(Body, Model, CommandKit.Site, graph ?? CommandKit.Graph, Bay, Parking, Timeline, World);
        }

        public TaskRequest Send(string key, string area = null, string token = null, long? seq = null)
        {
            var s = seq ?? ++sequence;
            if (s > sequence) sequence = s;
            var r = new TaskRequest(token ?? ("cmd-" + s), key, area, s, 1);
            Controller.Submit(r);
            return r;
        }

        /// <summary>Steps the model and the controller until the condition holds; fails the test if it takes more than maxSeconds.</summary>
        public bool Run(Func<bool> done, double maxSeconds = 1500, double dt = 0.25)
        {
            for (var t = 0.0; t < maxSeconds; t += dt)
            {
                if (done()) return true;
                Model.Step(dt, new MachineInput(Controller.SpeedMps, false));
                Controller.Step(dt, dt);
                Now += dt;
            }

            return done();
        }
    }
}
