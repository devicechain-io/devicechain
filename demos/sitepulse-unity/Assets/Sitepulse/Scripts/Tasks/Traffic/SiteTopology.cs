// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>
    /// One cell of a lane: a stretch of the line a machine drives, cut every <see cref="SiteTopology.CellM"/> metres. The haul loop's
    /// cells also carry the track time at their start; every other lane's do not (<see cref="T0"/> is NaN).
    /// </summary>
    public readonly struct Cell
    {
        public Cell(double x0, double z0, double x1, double z1, double t0)
        {
            X0 = x0;
            Z0 = z0;
            X1 = x1;
            Z1 = z1;
            T0 = t0;
        }

        public double X0 { get; }
        public double Z0 { get; }
        public double X1 { get; }
        public double Z1 { get; }
        public double T0 { get; }

        public double Length => Math.Sqrt((X1 - X0) * (X1 - X0) + (Z1 - Z0) * (Z1 - Z0));

        /// <summary>The way the cell points, in degrees clockwise from north (+Z).</summary>
        public double HeadingDegrees => (Math.Atan2(X1 - X0, Z1 - Z0) * 180.0 / Math.PI + 360.0) % 360.0;
    }

    /// <summary>A directed lane: its cells in the order they are driven.</summary>
    public sealed class Lane
    {
        public Lane(string id, string kind, bool cyclic, IReadOnlyList<Cell> cells, int track, string road, double neighbourSpanM)
        {
            Id = id;
            Kind = kind;
            Cyclic = cyclic;
            Cells = cells;
            Track = track;
            Road = road;
            NeighbourSpanM = neighbourSpanM;
            var arc = new double[cells.Count];
            var sum = 0.0;
            for (var k = 0; k < cells.Count; k++)
            {
                arc[k] = sum;
                sum += cells[k].Length;
            }

            ArcStarts = arc;
            TotalLength = sum;
        }

        public string Id { get; }

        /// <summary>"loop", "road" or "access".</summary>
        public string Kind { get; }

        public bool Cyclic { get; }
        public IReadOnlyList<Cell> Cells { get; }

        /// <summary>The fleet track a loop lane was cut from (-1 for any other lane).</summary>
        public int Track { get; }

        /// <summary>The road a road lane runs along (null for any other lane).</summary>
        public string Road { get; }

        /// <summary>
        /// The along-lane distance within which two cells of this lane are a machine and the one following it, which the follower rule keeps
        /// apart, and not a conflict.
        /// </summary>
        public double NeighbourSpanM { get; }

        /// <summary>The distance along the lane from its first cell's start to the start of each cell.</summary>
        public IReadOnlyList<double> ArcStarts { get; }

        public double TotalLength { get; }

        /// <summary>How far apart two cells are along the lane (the shorter way round on the cyclic loop).</summary>
        public double Along(int a, int b)
        {
            var d = Math.Abs(ArcStarts[a] - ArcStarts[b]);
            return Cyclic ? Math.Min(d, TotalLength - d) : d;
        }

        public double Length() => TotalLength;
    }

    /// <summary>A run of cells of one lane, both ends inclusive; on a cyclic lane a run may wrap past the end (<see cref="A"/> greater than <see cref="B"/>).</summary>
    public readonly struct CellRun
    {
        public CellRun(int a, int b)
        {
            A = a;
            B = b;
        }

        public int A { get; }
        public int B { get; }
    }

    public sealed class LaneRun
    {
        public LaneRun(string lane, CellRun cells)
        {
            Lane = lane;
            Cells = cells;
        }

        public string Lane { get; }
        public CellRun Cells { get; }
    }

    public sealed class LaneCell
    {
        public LaneCell(string lane, int cell)
        {
            Lane = lane;
            Cell = cell;
        }

        public string Lane { get; }
        public int Cell { get; }
    }

    /// <summary>A diverge: the lane <see cref="To"/> leaves <see cref="Lane"/> after its cell <see cref="Cell"/> and runs with it for its first <see cref="Shared"/> cells.</summary>
    public sealed class LaneExit
    {
        public LaneExit(string id, string lane, int cell, string to, int shared)
        {
            Id = id;
            Lane = lane;
            Cell = cell;
            To = to;
            Shared = shared;
        }

        public string Id { get; }
        public string Lane { get; }
        public int Cell { get; }
        public string To { get; }
        public int Shared { get; }
    }

    /// <summary>The lane <see cref="From"/> joins <see cref="Lane"/> at its cell <see cref="Cell"/>, through the junction <see cref="Junction"/> (null when none holds it).</summary>
    public sealed class LaneEntry
    {
        public LaneEntry(string id, string from, string lane, int cell, string junction)
        {
            Id = id;
            From = from;
            Lane = lane;
            Cell = cell;
            Junction = junction;
        }

        public string Id { get; }
        public string From { get; }
        public string Lane { get; }
        public int Cell { get; }
        public string Junction { get; }
    }

    /// <summary>A junction box: a convex polygon, the cells of every lane with an end in it, where a machine waits to be granted it, and the room it needs beyond.</summary>
    public sealed class Junction
    {
        public Junction(string id, string kind, Polygon polygon, IReadOnlyList<LaneRun> members, IReadOnlyList<LaneCell> approach, IReadOnlyList<LaneRun> room)
        {
            Id = id;
            Kind = kind;
            Polygon = polygon;
            Members = members;
            Approach = approach;
            Room = room;
        }

        public string Id { get; }

        /// <summary>"merge", "crossing" or "oncoming".</summary>
        public string Kind { get; }

        public Polygon Polygon { get; }
        public IReadOnlyList<LaneRun> Members { get; }
        public IReadOnlyList<LaneCell> Approach { get; }
        public IReadOnlyList<LaneRun> Room { get; }
    }

    /// <summary>A place a machine is left standing, off every lane, reached by <see cref="In"/> and left by <see cref="Out"/>.</summary>
    public sealed class StandPlace
    {
        public StandPlace(string id, string role, string zone, IReadOnlyList<string> kinds, double x, double z, double headingDegrees, string inLane, string outLane, double clearanceM)
        {
            Id = id;
            Role = role;
            Zone = zone;
            Kinds = kinds;
            X = x;
            Z = z;
            HeadingDegrees = headingDegrees;
            In = inLane;
            Out = outLane;
            ClearanceM = clearanceM;
        }

        public string Id { get; }

        /// <summary>"zone" (a place goto-area may send a machine to stay) or "service" (the refuel queue and bay: a machine goes there to be served and moves on).</summary>
        public string Role { get; }

        public string Zone { get; }
        public IReadOnlyList<string> Kinds { get; }
        public double X { get; }
        public double Z { get; }
        public double HeadingDegrees { get; }
        public string In { get; }
        public string Out { get; }
        public double ClearanceM { get; }
    }

    /// <summary>The refuel bay: a drive-through, in from the loop to the queue stand, a hop to the bay stand, out to the loop.</summary>
    public sealed class Bay
    {
        public Bay(string queue, string bay, string inLane, string hop, string outLane)
        {
            Queue = queue;
            BayStand = bay;
            In = inLane;
            Hop = hop;
            Out = outLane;
        }

        public string Queue { get; }
        public string BayStand { get; }
        public string In { get; }
        public string Hop { get; }
        public string Out { get; }
    }

    /// <summary>The loader a load station is coupled to: it waits at its ready pose, and clears its truck by <see cref="MinGapM"/>.</summary>
    public sealed class StationPartner
    {
        public StationPartner(string machine, int track, double readySeconds, double readyX, double readyZ, double readyHeading, double minGapM)
        {
            Machine = machine;
            Track = track;
            ReadySeconds = readySeconds;
            ReadyX = readyX;
            ReadyZ = readyZ;
            ReadyHeading = readyHeading;
            MinGapM = minGapM;
        }

        public string Machine { get; }
        public int Track { get; }
        public double ReadySeconds { get; }
        public double ReadyX { get; }
        public double ReadyZ { get; }
        public double ReadyHeading { get; }
        public double MinGapM { get; }
    }

    /// <summary>A timed station on a lane: a core machines run through at the track's own rate, an exit buffer behind it, a capacity and a headway.</summary>
    public sealed class StationSpec
    {
        public StationSpec(string id, string lane, CellRun core, CellRun buffer, int capacity, double windowSeconds, double headwaySeconds,
            bool rideThrough, StationPartner partner, double minGapM)
        {
            Id = id;
            Lane = lane;
            Core = core;
            Buffer = buffer;
            Capacity = capacity;
            WindowSeconds = windowSeconds;
            HeadwaySeconds = headwaySeconds;
            RideThrough = rideThrough;
            Partner = partner;
            MinGapM = minGapM;
        }

        public string Id { get; }
        public string Lane { get; }
        public CellRun Core { get; }
        public CellRun Buffer { get; }
        public int Capacity { get; }
        public double WindowSeconds { get; }
        public double HeadwaySeconds { get; }

        /// <summary>Whether the station admits trucks without loading while its coupled loader is away (the load point does).</summary>
        public bool RideThrough { get; }

        public StationPartner Partner { get; }
        public double MinGapM { get; }
    }

    /// <summary>The ground another machine works: a convex polygon round its footprint over its whole track.</summary>
    public sealed class WorkArea
    {
        public WorkArea(string machine, Polygon polygon)
        {
            Machine = machine;
            Polygon = polygon;
        }

        public string Machine { get; }
        public Polygon Polygon { get; }
    }

    /// <summary>What a lane or a stand is not held to: an outline it is laid out between, or the track only one machine plays.</summary>
    public sealed class Exemption
    {
        public Exemption(string to, string obstacle, bool hasSpot, double spotX, double spotZ, double radiusM, string machine)
        {
            To = to;
            Obstacle = obstacle;
            HasSpot = hasSpot;
            SpotX = spotX;
            SpotZ = spotZ;
            RadiusM = radiusM;
            Machine = machine;
        }

        public string To { get; }
        public string Obstacle { get; }
        public bool HasSpot { get; }
        public double SpotX { get; }
        public double SpotZ { get; }
        public double RadiusM { get; }
        public string Machine { get; }
    }

    public sealed class ZoneKinds
    {
        public ZoneKinds(string token, IReadOnlyList<string> kinds)
        {
            Token = token;
            Kinds = kinds;
        }

        public string Token { get; }
        public IReadOnlyList<string> Kinds { get; }
    }

    /// <summary>
    /// The thresholds the topology was made with and is checked by: the file states them and the checks read them from it, so that nothing about
    /// the site is a constant of the checker.
    /// </summary>
    public sealed class TopologyRules
    {
        public TopologyRules(double conflictHaulM, double conflictAccessM, double conflictSameRouteM, double poseStepM, double poseStepDegrees, int divergeWindow, int divergeMax)
        {
            ConflictHaulM = conflictHaulM;
            ConflictAccessM = conflictAccessM;
            ConflictSameRouteM = conflictSameRouteM;
            PoseStepM = poseStepM;
            PoseStepDegrees = poseStepDegrees;
            DivergeWindow = divergeWindow;
            DivergeMax = divergeMax;
        }

        /// <summary>Two cells of two haul lanes (the loop and the roads) whose swept footprints come nearer than this conflict, in metres.</summary>
        public double ConflictHaulM { get; }

        /// <summary>The same for a pair with a cell of an access lane (the bay's).</summary>
        public double ConflictAccessM { get; }

        /// <summary>Two cells of one lane, or of one stand's way through it, further apart along it than the neighbour span conflict when they come nearer than this (overlap).</summary>
        public double ConflictSameRouteM { get; }

        /// <summary>A cell off the loop is swept by a hauler posed at most this far apart along it ...</summary>
        public double PoseStepM { get; }

        /// <summary>... and turned at most this much between poses, in degrees.</summary>
        public double PoseStepDegrees { get; }

        /// <summary>The cells either side of an exit that the lane leaving shares ground with, and the most of that lane that may.</summary>
        public int DivergeWindow { get; }

        public int DivergeMax { get; }

        /// <summary>The threshold a pair of cells of two different lanes is held to: the haul one between two haul lanes, the access one when either is an access lane.</summary>
        public double ConflictM(string kindA, string kindB) => IsHaul(kindA) && IsHaul(kindB) ? ConflictHaulM : ConflictAccessM;

        static bool IsHaul(string kind) => kind == "loop" || kind == "road";
    }

    /// <summary>The SHA-256 of the files the topology was made from.</summary>
    public sealed class SourceHashes
    {
        public SourceHashes(string features, string heights, string fleet, string fleetPreview)
        {
            Features = features;
            Heights = heights;
            Fleet = fleet;
            FleetPreview = fleetPreview;
        }

        public string Features { get; }
        public string Heights { get; }
        public string Fleet { get; }
        public string FleetPreview { get; }
    }

    public sealed class FleetBlock
    {
        public FleetBlock(int loopTrucks, int machines, double periodSeconds)
        {
            LoopTrucks = loopTrucks;
            Machines = machines;
            PeriodSeconds = periodSeconds;
        }

        public int LoopTrucks { get; }
        public int Machines { get; }
        public double PeriodSeconds { get; }
    }

    /// <summary>
    /// The site as an interlocking needs it, as <c>ArtSource/terrain/quarry_topology.py</c> writes it: directed lanes cut into cells,
    /// junction boxes, stand places with their access lanes, the refuel bay, the timed stations and the ground other machines work.
    /// Immutable plain data: nothing here knows a quarry, a machine kind's size or Unity.
    /// </summary>
    public sealed class SiteTopology
    {
        readonly Dictionary<string, Lane> byId;

        public SiteTopology(string generator, SourceHashes source, double cellM, double slotM, TopologyRules rules, FleetBlock fleet, IReadOnlyList<ZoneKinds> zones,
            IReadOnlyList<Lane> lanes, IReadOnlyList<LaneExit> exits, IReadOnlyList<LaneEntry> entries, IReadOnlyList<Junction> junctions,
            IReadOnlyList<StandPlace> stands, Bay bay, IReadOnlyList<StationSpec> stations, IReadOnlyList<WorkArea> workAreas,
            IReadOnlyList<Exemption> exempt)
        {
            Generator = generator;
            Source = source;
            CellM = cellM;
            SlotM = slotM;
            Rules = rules;
            Fleet = fleet;
            Zones = zones;
            Lanes = lanes;
            Exits = exits;
            Entries = entries;
            Junctions = junctions;
            Stands = stands;
            Bay = bay;
            Stations = stations;
            WorkAreas = workAreas;
            Exempt = exempt;
            byId = new Dictionary<string, Lane>(StringComparer.Ordinal);
            foreach (var l in lanes) byId[l.Id] = l;
        }

        public string Generator { get; }
        public SourceHashes Source { get; }

        /// <summary>The length a lane is cut into cells at, in metres.</summary>
        public double CellM { get; }

        /// <summary>A hauler's length and the air haul trucks keep: what every capacity (a cycle, a span's complement, a buffer) is counted in, in metres.</summary>
        public double SlotM { get; }

        public TopologyRules Rules { get; }
        public FleetBlock Fleet { get; }
        public IReadOnlyList<ZoneKinds> Zones { get; }
        public IReadOnlyList<Lane> Lanes { get; }
        public IReadOnlyList<LaneExit> Exits { get; }
        public IReadOnlyList<LaneEntry> Entries { get; }
        public IReadOnlyList<Junction> Junctions { get; }
        public IReadOnlyList<StandPlace> Stands { get; }

        /// <summary>The refuel bay, or null when the site has none.</summary>
        public Bay Bay { get; }

        public IReadOnlyList<StationSpec> Stations { get; }
        public IReadOnlyList<WorkArea> WorkAreas { get; }
        public IReadOnlyList<Exemption> Exempt { get; }

        public Lane Lane(string id) => byId.TryGetValue(id, out var l) ? l : throw new KeyNotFoundException("no lane " + id);

        public bool HasLane(string id) => byId.ContainsKey(id);

        /// <summary>The haul loop: the one lane of kind "loop".</summary>
        public Lane Loop
        {
            get
            {
                foreach (var l in Lanes)
                    if (l.Kind == "loop") return l;
                throw new KeyNotFoundException("no loop lane");
            }
        }
    }
}
