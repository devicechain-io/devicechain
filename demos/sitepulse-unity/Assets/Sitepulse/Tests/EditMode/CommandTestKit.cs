// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Json;
using DeviceChain.Sim.Traffic;
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

    /// <summary>
    /// A site where nothing is ever in the way, unless a test says so: the machine's trips are the free trips every machine off the lanes takes
    /// (<see cref="FreeTrips"/>), held to nothing but the test's flags.
    /// </summary>
    internal sealed class OpenWorld : ITaskWorld, IWayKeeper
    {
        /// <summary>The machine may not drive on from where it is (something sits in its way for good).</summary>
        public bool Block;

        /// <summary>Something stands in the refuel bay.</summary>
        public bool BayBusy;

        public readonly ParkingLot Parking = new ParkingLot();
        readonly FreeTrips trips;

        public OpenWorld(SiteGeometry site = null, RouteGraph graph = null) => trips = new FreeTrips(site ?? CommandKit.Site, graph ?? CommandKit.Graph, Parking);

        public ITrip TripToArea(IMachineBody body, string area, double laneShare, out string refusal) => trips.ToArea(body, area, laneShare, this, null, null, out refusal);
        public ITrip TripToQueue(IMachineBody body, double laneShare, out string refusal) => trips.ToSpot(body, RouteGraph.QueueSpot, "the refuel queue", laneShare, this, out refusal);
        public ITrip TripIntoBay(IMachineBody body, out string refusal) => trips.IntoBay(body, this, out refusal);
        public ITrip TripBack(IMachineBody body, double laneShare, out string refusal) => trips.Back(body, laneShare, this, out refusal);
        public void Release(string id) => trips.Release(id);
        public bool BayClear(string id) => !BayBusy;

        public string Check(IMachineBody body, LaneLine line, bool endsStanding) => null;

        public bool MayLeave(string id, out string why)
        {
            why = null;
            return true;
        }

        public string Begin(IMachineBody body, LaneLine line, bool endsStanding) => null;
        public double Reach(string id, double s) => Block ? s : double.MaxValue;
        public void Ended(string id) { }
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

        static PreparedSite prepared;

        /// <summary>The site as the interlocking measures it (once: a second or so), for every director a test builds.</summary>
        public static PreparedSite Prepared => prepared ??= PreparedSite.Measure(TopologyKit.Topology, TopologyKit.World);

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

        /// <summary>
        /// A haul truck of the live choreography on its track (the haul loop), where the fleet has it <paramref name="seconds"/> into the
        /// choreography: a body that plays the track as the scene's does, for a director to drive (call <see cref="TrackBody.Advance"/> each step).
        /// </summary>
        public static TrackBody LoopTruck(string id, double seconds = SceneSim.StartClock)
        {
            var dt = (float)Fleet.GetProperty("dt").GetDouble();
            foreach (var m in Fleet.GetProperty("machines").EnumerateArray())
            {
                if (m.GetProperty("id").GetString() != id) continue;
                var t = Fleet.GetProperty("tracks")[m.GetProperty("track").GetInt32()];
                var arr = t.GetProperty("data");
                var data = new float[arr.GetArrayLength()];
                var k = 0;
                foreach (var v in arr.EnumerateArray()) data[k++] = (float)v.GetDouble();
                return new TrackBody(id, EquipmentKind.Hauler, data, dt, (float)t.GetProperty("period").GetDouble(), (float)(m.GetProperty("offset").GetDouble() - seconds));
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
        public readonly ParkingLot Parking;
        public readonly OpenWorld World;
        public readonly MachineController Controller;
        public double Now;
        long sequence;

        public Rig(string id = "SP-HL-0003", EquipmentKind kind = EquipmentKind.Hauler, double? x = null, double? z = null, bool withTrack = true, RouteGraph graph = null, SiteGeometry site = null)
        {
            Id = id;
            var track = withTrack ? CommandKit.Track(0) : null;
            // by default a truck on the haul loop, a good way from everything
            var start = track != null ? track[300] : new TrackPoint(0, 0, 0, 0);
            Body = new FakeBody(id, kind, x ?? start.X, z ?? start.Z, start.HeadingDegrees, track);
            Model = new MachineModel(kind, id);
            World = new OpenWorld(site, graph);
            Parking = World.Parking;
            Controller = new MachineController(Body, Model, Bay, Timeline, World);
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
