// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>A fleet file (quarry_fleet_live.json and its kind) as the interlocking reads it: its frames, and which machine plays which track from when.</summary>
    public static class FleetViewReader
    {
        public static FleetView Parse(string json)
        {
            if (string.IsNullOrEmpty(json)) throw new ArgumentException("a fleet file is not empty", nameof(json));
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            var machines = new List<FleetMachine>();
            foreach (var m in root.GetProperty("machines").EnumerateArray())
                machines.Add(new FleetMachine(m.GetProperty("id").GetString(), m.GetProperty("track").GetInt32(), m.GetProperty("offset").GetDouble()));
            var tracks = new List<TrackView>();
            foreach (var t in root.GetProperty("tracks").EnumerateArray())
            {
                var data = t.GetProperty("data");
                var n = data.GetArrayLength() / TrackPlayer.Stride;
                double[] x = new double[n], z = new double[n], h = new double[n], boom = new double[n];
                var i = 0;
                var k = 0;
                foreach (var v in data.EnumerateArray())
                {
                    var f = k % TrackPlayer.Stride;
                    if (f == 0) x[i] = v.GetDouble();
                    else if (f == 1) z[i] = v.GetDouble();
                    else if (f == 2) h[i] = v.GetDouble();
                    else if (f == 4) boom[i] = v.GetDouble();
                    if (++k % TrackPlayer.Stride == 0) i++;
                }

                tracks.Add(new TrackView(t.GetProperty("kind").GetString(), t.GetProperty("period").GetDouble(), x, z, h, boom));
            }

            return new FleetView(root.GetProperty("dt").GetDouble(), machines, tracks);
        }
    }

    /// <summary>
    /// The quarry as the interlocking measures it when the demo starts (<see cref="PreparedSite.Measure"/>): what a machine of each kind covers, and
    /// the live fleet the haul loop was cut from. The rest of what the topology's checks ask belongs to the checks (the EditMode tests read the
    /// whole of it): asked here, it says so rather than answer.
    /// </summary>
    public sealed class QuarryTrafficWorld : ISiteWorld
    {
        public QuarryTrafficWorld(FleetView live) => Live = live ?? throw new ArgumentNullException(nameof(live));

        static EquipmentKind KindOf(string kind) =>
            kind == "Hauler" ? EquipmentKind.Hauler : kind == "Loader" ? EquipmentKind.Loader : kind == "Dozer" ? EquipmentKind.Dozer
            : throw new ArgumentException("the quarry has no machine of kind \"" + kind + "\"");

        static Polygon Of(in Quad q) => new Polygon(new[] { q.Ax, q.Bx, q.Cx, q.Dx }, new[] { q.Az, q.Bz, q.Cz, q.Dz });

        public IReadOnlyList<Polygon> Footprint(string kind, double x, double z, double headingDegrees, double boom)
        {
            var k = KindOf(kind);
            var shape = Tasks.Footprint.At(k, x, z, headingDegrees, k == EquipmentKind.Loader && boom < Tasks.Footprint.LoaderRaisedBoomDegrees);
            return shape.Count == 2 ? new[] { Of(shape.First), Of(shape.Second) } : new[] { Of(shape.First) };
        }

        public double HaulerLength
        {
            get
            {
                Tasks.Footprint.Dimensions(EquipmentKind.Hauler, out _, out var front, out var rear);
                return front + rear;
            }
        }

        public double HaulerClearance => throw Unused(nameof(HaulerClearance));
        public double TravelReach => ParkingLot.TravelRadius(EquipmentKind.Hauler) + ParkingLot.TravelClearance;
        public double MaxGradePercent => Grade.MaxPct;
        public FleetView Live { get; }
        public FleetView Preview => throw Unused(nameof(Preview));
        public IReadOnlyList<IWorldObstacle> Obstacles => throw Unused(nameof(Obstacles));
        public GradeReading SustainedGrade(IReadOnlyList<double> xs, IReadOnlyList<double> zs) => throw Unused(nameof(SustainedGrade));
        public ZoneShape Zone(string token) => throw Unused(nameof(Zone));
        public SourceHashes Hashes => throw Unused(nameof(Hashes));

        static NotSupportedException Unused(string what) =>
            new NotSupportedException(what + " is the topology checks' to ask (the EditMode tests read it); the demo measures the site with footprints and the live fleet only");
    }
}
