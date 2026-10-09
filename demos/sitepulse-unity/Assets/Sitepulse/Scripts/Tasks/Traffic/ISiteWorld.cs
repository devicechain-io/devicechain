// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>One track of a fleet: the frames a machine plays at <see cref="FleetView.Dt"/>, looping over <see cref="Period"/>.</summary>
    public sealed class TrackView
    {
        public TrackView(string kind, double period, double[] x, double[] z, double[] heading, double[] boom)
        {
            Kind = kind;
            Period = period;
            X = x;
            Z = z;
            Heading = heading;
            Boom = boom;
        }

        public string Kind { get; }
        public double Period { get; }
        public double[] X { get; }
        public double[] Z { get; }

        /// <summary>Degrees clockwise from north.</summary>
        public double[] Heading { get; }

        /// <summary>The first rig channel: a loader's boom angle, a dozer's blade, a hauler's dump raise.</summary>
        public double[] Boom { get; }
    }

    public sealed class FleetMachine
    {
        public FleetMachine(string id, int track, double offset)
        {
            Id = id;
            Track = track;
            Offset = offset;
        }

        public string Id { get; }
        public int Track { get; }
        public double Offset { get; }
    }

    /// <summary>A fleet file as the checks read it: its frames and which machine plays which track from when.</summary>
    public sealed class FleetView
    {
        public FleetView(double dt, IReadOnlyList<FleetMachine> machines, IReadOnlyList<TrackView> tracks)
        {
            Dt = dt;
            Machines = machines;
            Tracks = tracks;
        }

        public double Dt { get; }
        public IReadOnlyList<FleetMachine> Machines { get; }
        public IReadOnlyList<TrackView> Tracks { get; }
    }

    /// <summary>A zone's rectangle (x0, x1, z0, z1) and the radius its corners are rounded by.</summary>
    public sealed class ZoneShape
    {
        public ZoneShape(double x0, double x1, double z0, double z1, double corner)
        {
            X0 = x0;
            X1 = x1;
            Z0 = z0;
            Z1 = z1;
            Corner = corner;
        }

        public double X0 { get; }
        public double X1 { get; }
        public double Z0 { get; }
        public double Z1 { get; }
        public double Corner { get; }

        public bool Holds(double x, double z)
        {
            var cx = Math.Max(X0 + Corner, Math.Min(X1 - Corner, x));
            var cz = Math.Max(Z0 + Corner, Math.Min(Z1 - Corner, z));
            return Math.Sqrt((x - cx) * (x - cx) + (z - cz) * (z - cz)) <= Corner && x >= X0 && x <= X1 && z >= Z0 && z <= Z1;
        }
    }

    /// <summary>Something that stands on the site: a prop (a box), a pile or the refuel approach (a capsule).</summary>
    public interface IWorldObstacle
    {
        string Name { get; }

        /// <summary>A point in it and the radius of a circle round it, to tell a far-off one without measuring it.</summary>
        double CenterX { get; }

        double CenterZ { get; }
        double Reach { get; }

        /// <summary>How far a point is from its outline (negative inside).</summary>
        double Distance(double x, double z);

        /// <summary>The clear distance between a machine's footprint and it (negative when they overlap).</summary>
        double Gap(IReadOnlyList<Polygon> footprint);
    }

    /// <summary>The steepest a driven line climbs as a truck feels it, and where.</summary>
    public readonly struct GradeReading
    {
        public GradeReading(double percent, double x, double z)
        {
            Percent = percent;
            X = x;
            Z = z;
        }

        public double Percent { get; }
        public double X { get; }
        public double Z { get; }
    }

    /// <summary>
    /// Everything the checks need to know about the site that the topology does not say, as the quarry knows it: what a machine of each kind
    /// covers, the fleets' frames, what stands, the grade of the ground, the zones and the files the topology was made from. The checks
    /// here know none of it themselves, which is what keeps this folder free of the quarry.
    /// </summary>
    public interface ISiteWorld
    {
        /// <summary>The boxes a machine of this kind covers at a pose (a haul truck is two).</summary>
        IReadOnlyList<Polygon> Footprint(string kind, double x, double z, double headingDegrees, double boom);

        /// <summary>A hauler's length in metres, front and rear: with the air two haul trucks keep it is a slot.</summary>
        double HaulerLength { get; }

        /// <summary>The air two haul trucks keep apart in a lane, in metres; with the length it is a slot.</summary>
        double HaulerClearance { get; }

        /// <summary>The distance a driving hauler keeps from an outline (its half width and the air kept), metres.</summary>
        double TravelReach { get; }

        /// <summary>The steepest grade a lane's driven line may climb, in percent.</summary>
        double MaxGradePercent { get; }

        FleetView Live { get; }
        FleetView Preview { get; }
        IReadOnlyList<IWorldObstacle> Obstacles { get; }
        GradeReading SustainedGrade(IReadOnlyList<double> xs, IReadOnlyList<double> zs);
        ZoneShape Zone(string token);

        /// <summary>The SHA-256 of the files the topology says it was made from, as they are now.</summary>
        SourceHashes Hashes { get; }
    }
}
