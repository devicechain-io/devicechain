// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Nodes;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>An outline that stands on the site, as the checks ask about it: the task layer's own <see cref="Obstacle"/> with where it is.</summary>
    internal sealed class QuarryObstacle : IWorldObstacle
    {
        readonly Obstacle obstacle;

        public QuarryObstacle(Obstacle obstacle, double cx, double cz)
        {
            this.obstacle = obstacle;
            CenterX = cx;
            CenterZ = cz;
        }

        public string Name => obstacle.Name;
        public double CenterX { get; }
        public double CenterZ { get; }

        /// <summary>A generous circle: the largest thing on the site (the crusher plant) is 27 m from its middle to a corner.</summary>
        public double Reach => 30.0;

        public double Distance(double x, double z) => obstacle.Distance(x, z);

        /// <summary>
        /// The nearest a footprint's edge or inside comes to the outline, sampled a tenth of a metre apart along every edge (negative when the outline's
        /// middle is inside it). The distance to a convex outline is convex along an edge, so sampling can only read it a little too far: by less than
        /// 0.05^2 / 2 / 1.5 m = 1 mm at the distances the checks hold, which is the tolerance the Python side's exact distance is compared at.
        /// </summary>
        public double Gap(IReadOnlyList<Polygon> footprint)
        {
            foreach (var p in footprint)
                if (Geometry.InPolygon(p, CenterX, CenterZ)) return -1.0;
            var best = double.MaxValue;
            foreach (var p in footprint)
            {
                for (var i = 0; i < p.Count; i++)
                {
                    int j = (i + 1) % p.Count;
                    double dx = p.X[j] - p.X[i], dz = p.Z[j] - p.Z[i];
                    var len = Math.Sqrt(dx * dx + dz * dz);
                    var n = Math.Max(1, (int)Math.Ceiling(len / 0.1));
                    for (var k = 0; k <= n; k++) best = Math.Min(best, obstacle.Distance(p.X[i] + dx * k / n, p.Z[i] + dz * k / n));
                }
            }

            return best;
        }
    }

    /// <summary>What the checks ask the quarry: the machines' footprints, the fleets' frames, the outlines, the ground, the zones and the files.</summary>
    internal sealed class QuarrySiteWorld : ISiteWorld
    {
        static readonly string[] Files =
        {
            "Art/Terrain/quarry_features.json", "Art/Terrain/quarry_height.bytes", "Data/quarry_fleet_live.json", "Data/quarry_fleet.json",
        };

        readonly List<IWorldObstacle> obstacles = new List<IWorldObstacle>();

        public QuarrySiteWorld()
        {
            Live = ReadFleet("Data/quarry_fleet_live.json");
            Preview = ReadFleet("Data/quarry_fleet.json");
            using var doc = JsonDocument.Parse(File.ReadAllText(CommandKit.AssetPath("Art/Terrain/quarry_features.json")));
            var root = doc.RootElement;
            // the task layer lists props, then piles, then the refuel approach (SiteGeometryReader.Obstacles): the same order, so each takes its own middle
            var centres = new List<(double, double)>();
            foreach (var p in root.GetProperty("props").EnumerateArray()) centres.Add((p.GetProperty("x").GetDouble(), p.GetProperty("z").GetDouble()));
            foreach (var p in root.GetProperty("piles").EnumerateArray()) centres.Add((p.GetProperty("x").GetDouble(), p.GetProperty("z").GetDouble()));
            var q = root.GetProperty("spots").GetProperty("refuel-queue");
            var b = root.GetProperty("spots").GetProperty("refuel-bay");
            centres.Add(((q.GetProperty("x").GetDouble() + b.GetProperty("x").GetDouble()) / 2.0, (q.GetProperty("z").GetDouble() + b.GetProperty("z").GetDouble()) / 2.0));
            var list = CommandKit.Site.Obstacles;
            if (list.Count != centres.Count) throw new InvalidOperationException("the task layer lists " + list.Count + " outlines and the feature file " + centres.Count);
            for (var i = 0; i < list.Count; i++) obstacles.Add(new QuarryObstacle(list[i], centres[i].Item1, centres[i].Item2));
        }

        static FleetView ReadFleet(string file)
        {
            using var doc = JsonDocument.Parse(File.ReadAllText(CommandKit.AssetPath(file)));
            var root = doc.RootElement;
            var machines = root.GetProperty("machines").EnumerateArray()
                .Select(m => new FleetMachine(m.GetProperty("id").GetString(), m.GetProperty("track").GetInt32(), m.GetProperty("offset").GetDouble())).ToList();
            var tracks = new List<TrackView>();
            foreach (var t in root.GetProperty("tracks").EnumerateArray())
            {
                var data = t.GetProperty("data");
                var n = data.GetArrayLength() / 8;
                double[] x = new double[n], z = new double[n], h = new double[n], boom = new double[n];
                for (var i = 0; i < n; i++)
                {
                    x[i] = data[i * 8].GetDouble();
                    z[i] = data[i * 8 + 1].GetDouble();
                    h[i] = data[i * 8 + 2].GetDouble();
                    boom[i] = data[i * 8 + 4].GetDouble();
                }

                tracks.Add(new TrackView(t.GetProperty("kind").GetString(), t.GetProperty("period").GetDouble(), x, z, h, boom));
            }

            return new FleetView(root.GetProperty("dt").GetDouble(), machines, tracks);
        }

        static EquipmentKind KindOf(string kind) =>
            kind == "Hauler" ? EquipmentKind.Hauler : kind == "Loader" ? EquipmentKind.Loader : kind == "Dozer" ? EquipmentKind.Dozer
            : throw new ArgumentException("the quarry has no machine of kind \"" + kind + "\"");

        static Polygon Of(in Quad q) => new Polygon(new[] { q.Ax, q.Bx, q.Cx, q.Dx }, new[] { q.Az, q.Bz, q.Cz, q.Dz });

        public IReadOnlyList<Polygon> Footprint(string kind, double x, double z, double headingDegrees, double boom)
        {
            var k = KindOf(kind);
            var shape = Footprint_At(k, x, z, headingDegrees, boom);
            return shape.Count == 2 ? new[] { Of(shape.First), Of(shape.Second) } : new[] { Of(shape.First) };
        }

        static FootprintShape Footprint_At(EquipmentKind kind, double x, double z, double heading, double boom) =>
            Tasks.Footprint.At(kind, x, z, heading, kind == EquipmentKind.Loader && boom < Tasks.Footprint.LoaderRaisedBoomDegrees);

        public double HaulerLength
        {
            get
            {
                Tasks.Footprint.Dimensions(EquipmentKind.Hauler, out _, out var front, out var rear);
                return front + rear;
            }
        }

        /// <summary>The air two haul trucks keep apart in a lane: ArtSource/terrain/quarry_fleet.py <c>HAULER_CLEARANCE</c>, which the generator holds the loop to.</summary>
        public double HaulerClearance => 2.5;

        public double TravelReach => ParkingLot.TravelRadius(EquipmentKind.Hauler) + ParkingLot.TravelClearance;
        public double MaxGradePercent => Grade.MaxPct;
        public FleetView Live { get; }
        public FleetView Preview { get; }
        public IReadOnlyList<IWorldObstacle> Obstacles => obstacles;

        public GradeReading SustainedGrade(IReadOnlyList<double> xs, IReadOnlyList<double> zs)
        {
            var worst = Grade.MaxSustained(CommandKit.Ground, xs, zs);
            var legs = Grade.PerLeg(CommandKit.Ground, xs, zs);
            var k = Array.IndexOf(legs, legs.Max());
            return new GradeReading(worst, xs[k], zs[k]);
        }

        public ZoneShape Zone(string token)
        {
            var z = CommandKit.Site.Zones.First(r => r.Name == token);
            return new ZoneShape(z.X0, z.X1, z.Z0, z.Z1, 6.0);
        }

        public SourceHashes Hashes
        {
            get
            {
                string H(string f)
                {
                    using var sha = SHA256.Create();
                    return string.Concat(sha.ComputeHash(File.ReadAllBytes(CommandKit.AssetPath(f))).Select(b => b.ToString("x2")));
                }

                return new SourceHashes(H(Files[0]), H(Files[1]), H(Files[2]), H(Files[3]));
            }
        }
    }

    /// <summary>The committed topology, its world, and edits of it for the checks' negative controls.</summary>
    internal static class TopologyKit
    {
        public const string File = "Data/quarry_topology.json";
        static string text;
        static SiteTopology topology;
        static QuarrySiteWorld world;

        public static string Text => text ??= System.IO.File.ReadAllText(CommandKit.AssetPath(File));
        public static SiteTopology Topology => topology ??= SiteTopologyReader.Parse(Text);
        public static QuarrySiteWorld World => world ??= new QuarrySiteWorld();

        /// <summary>The topology with an edit applied to its JSON: the same crafted bad sites <c>quarry_topology.py --selftest</c> makes.</summary>
        public static SiteTopology Edit(Action<JsonNode> edit)
        {
            var node = JsonNode.Parse(Text);
            edit(node);
            return SiteTopologyReader.Parse(node.ToJsonString());
        }

        /// <summary>The element of a JSON array (stands, entries, stations, zones, junctions) with a given id: edits select by id, never by position.</summary>
        public static JsonNode ById(JsonNode array, string id) => array.AsArray().First(x => (string)(x["id"] ?? x["token"]) == id);

        public static JsonNode Lane(JsonNode root, string id) => root["lanes"].AsArray().First(l => (string)l["id"] == id);

        public static IEnumerable<string> Check(Func<TopologyValidator, List<string>> check, SiteTopology t = null) => check(new TopologyValidator(t ?? Topology, World));
    }
}
