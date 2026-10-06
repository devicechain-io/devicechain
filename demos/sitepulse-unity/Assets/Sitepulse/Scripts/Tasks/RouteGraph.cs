// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>How fast a machine takes a stretch: its own cruise speed, scaled by the surface and the slope.</summary>
    public static class SpeedModel
    {
        public const double HaulerCruise = 7.0, LoaderCruise = 4.5, DozerCruise = 3.0;

        /// <summary>Off a road: across a pad, onto a slot, to a spot.</summary>
        public const double OffRoadFactor = 0.5;

        /// <summary>The short crawl from the refuel queue into the bay.</summary>
        public const double BayApproachFactor = 0.3;

        public static double Cruise(Simulation.EquipmentKind kind)
        {
            switch (kind)
            {
                case Simulation.EquipmentKind.Hauler: return HaulerCruise;
                case Simulation.EquipmentKind.Loader: return LoaderCruise;
                default: return DozerCruise;
            }
        }

        public static double RoadFactor(string kind)
        {
            switch (kind)
            {
                case "haul-ramp": return 0.65;
                case "service-road": return 0.6;
                default: return 1.0;
            }
        }

        /// <summary>Uphill slows a machine in proportion to the grade; downhill it eases off.</summary>
        public static double GradeFactor(double gradePct)
        {
            if (gradePct > 0) return 1.0 / (1.0 + 0.05 * gradePct);
            return gradePct < -1.0 ? 0.85 : 1.0;
        }
    }

    /// <summary>A path over the site: waypoints, and for each leg how fast it can be taken.</summary>
    public sealed class Route
    {
        readonly double[] x, z, cumulative, factor, grade;

        internal Route(List<double> xs, List<double> zs, List<double> factors, List<double> grades)
        {
            x = xs.ToArray();
            z = zs.ToArray();
            factor = factors.ToArray();
            grade = grades.ToArray();
            cumulative = new double[x.Length];
            for (var i = 1; i < x.Length; i++)
                cumulative[i] = cumulative[i - 1] + Math.Sqrt(Sq(x[i] - x[i - 1]) + Sq(z[i] - z[i - 1]));
        }

        static double Sq(double v) => v * v;

        public int Legs => x.Length - 1;
        public double Length => cumulative[cumulative.Length - 1];
        public double EndX => x[x.Length - 1];
        public double EndZ => z[z.Length - 1];
        public double StartX => x[0];
        public double StartZ => z[0];

        /// <summary>How fast leg <paramref name="leg"/> may be taken, as a share of cruise: a road's own factor, or <see cref="SpeedModel.OffRoadFactor"/> off one.</summary>
        public double LegFactor(int leg) => factor[leg];

        public IReadOnlyList<double> Xs => x;
        public IReadOnlyList<double> Zs => z;

        /// <summary>A route of one straight leg.</summary>
        public static Route Straight(double x0, double z0, double x1, double z1, double factor, double gradePct = 0)
        {
            var b = new RouteBuilder();
            b.Start(x0, z0);
            b.Leg(x1, z1, factor, gradePct);
            return b.Build();
        }

        int LegAt(double s)
        {
            var leg = 0;
            while (leg < Legs - 1 && s > cumulative[leg + 1]) leg++;
            return leg;
        }

        /// <summary>The place at distance <paramref name="s"/> along the route, the way it points there, and the leg it is on.</summary>
        public void PointAt(double s, out double px, out double pz, out double headingDegrees, out int leg)
        {
            if (Legs == 0)
            {
                px = x[0];
                pz = z[0];
                headingDegrees = 0;
                leg = 0;
                return;
            }

            if (s < 0) s = 0;
            if (s > Length) s = Length;
            leg = LegAt(s);
            var len = cumulative[leg + 1] - cumulative[leg];
            var t = len > 1e-9 ? (s - cumulative[leg]) / len : 1.0;
            px = x[leg] + (x[leg + 1] - x[leg]) * t;
            pz = z[leg] + (z[leg + 1] - z[leg]) * t;
            headingDegrees = Simulation.SiteDefinition.HeadingDegrees(x[leg + 1] - x[leg], z[leg + 1] - z[leg]);
        }

        /// <summary>The speed this leg can be taken at, for a machine of the given cruise speed.</summary>
        public double SpeedOnLeg(int leg, double cruise) => cruise * factor[leg] * SpeedModel.GradeFactor(grade[leg]);

        /// <summary>How long the route takes at the speeds of each leg, ignoring acceleration and waiting.</summary>
        public double Eta(double cruise)
        {
            var t = 0.0;
            for (var i = 0; i < Legs; i++)
            {
                var len = cumulative[i + 1] - cumulative[i];
                var v = SpeedOnLeg(i, cruise);
                t += v > 0.05 ? len / v : 0;
            }

            return t;
        }

        /// <summary>This route followed by <paramref name="next"/>, which must start where this ends.</summary>
        public Route Then(Route next)
        {
            var b = new RouteBuilder();
            b.Start(x[0], z[0]);
            for (var i = 0; i < Legs; i++) b.Leg(x[i + 1], z[i + 1], factor[i], grade[i]);
            for (var i = 0; i < next.Legs; i++) b.Leg(next.x[i + 1], next.z[i + 1], next.factor[i], next.grade[i]);
            return b.Build();
        }
    }

    sealed class RouteBuilder
    {
        readonly List<double> xs = new List<double>(), zs = new List<double>(), factors = new List<double>(), grades = new List<double>();

        public void Start(double x, double z)
        {
            xs.Add(x);
            zs.Add(z);
        }

        public void Leg(double x, double z, double factor, double gradePct)
        {
            var dx = x - xs[xs.Count - 1];
            var dz = z - zs[zs.Count - 1];
            if (dx * dx + dz * dz < 1e-8) return;
            xs.Add(x);
            zs.Add(z);
            factors.Add(factor);
            grades.Add(gradePct);
        }

        public Route Build() => new Route(xs, zs, factors, grades);
    }

    /// <summary>
    /// The site's drivable network, built from the feature file's roads: every road centreline vertex is a
    /// node and each stretch between neighbours an edge (two-way). Roads that meet are one network: nodes
    /// within a metre of each other are one node, a road's ends join the nearest other road within
    /// <see cref="JoinTolerance"/>, and the ends of roads that reach the same pad are joined across it,
    /// since a pad is open ground. A named spot is joined to the nearest node, except the refuel bay,
    /// which hangs off the queue and the road and not off one alone.
    ///
    /// Routing never fails to ground: a machine and its goal are each joined to the network by a straight
    /// off-road leg, chosen (with the node it joins) to make the whole trip cheapest, so a truck on the
    /// road does not double back to a vertex behind it.
    /// </summary>
    public sealed class RouteGraph
    {
        public const double MergeTolerance = 1.0;
        public const double JoinTolerance = 8.0;
        public const double PadMargin = 6.0;
        public const int Candidates = 4;

        public const string BaySpot = "refuel-bay", QueueSpot = "refuel-queue";

        struct Edge
        {
            public int To;
            public double Length, Factor, GradePct;
            public double Cost => Length / Factor;
        }

        readonly List<double> nx = new List<double>(), ny = new List<double>(), nz = new List<double>();
        readonly List<List<Edge>> adjacency = new List<List<Edge>>();
        readonly Dictionary<string, int> spotNodes = new Dictionary<string, int>(StringComparer.Ordinal);

        int roadNodeCount;

        public int NodeCount => nx.Count;

        RouteGraph() { }

        public static RouteGraph Build(SiteGeometry site)
        {
            if (site == null) throw new ArgumentNullException(nameof(site));
            var g = new RouteGraph();
            var ends = new List<int>();
            var roadNodes = new List<List<int>>();
            foreach (var road in site.Roads)
            {
                var nodes = new List<int>();
                var factor = SpeedModel.RoadFactor(road.Kind);
                for (var i = 0; i < road.Points.Count; i++)
                {
                    var p = road.Points[i];
                    var n = g.NodeAt(p.X, p.Y, p.Z);
                    if (nodes.Count > 0 && nodes[nodes.Count - 1] != n) g.Connect(nodes[nodes.Count - 1], n, factor);
                    if (nodes.Count == 0 || nodes[nodes.Count - 1] != n) nodes.Add(n);
                }

                roadNodes.Add(nodes);
                if (nodes.Count > 0)
                {
                    ends.Add(nodes[0]);
                    if (nodes[nodes.Count - 1] != nodes[0]) ends.Add(nodes[nodes.Count - 1]);
                }
            }

            // a road's end joins the nearest other road within reach
            for (var r = 0; r < roadNodes.Count; r++)
            {
                var nodes = roadNodes[r];
                if (nodes.Count == 0) continue;
                foreach (var e in new[] { nodes[0], nodes[nodes.Count - 1] })
                {
                    var best = -1;
                    var bestD = JoinTolerance;
                    for (var other = 0; other < roadNodes.Count; other++)
                    {
                        if (other == r) continue;
                        foreach (var n in roadNodes[other])
                        {
                            var d = g.Dist(e, n);
                            if (d < bestD) { bestD = d; best = n; }
                        }
                    }

                    if (best >= 0 && best != e) g.Connect(e, best, 0.6);
                }
            }

            // roads that end on the same pad are joined across it
            var pads = new List<Rect2>(site.Pads);
            pads.AddRange(site.Zones);
            foreach (var pad in pads)
            {
                var inside = new List<int>();
                foreach (var e in ends)
                    if (pad.Contains(g.nx[e], g.nz[e], PadMargin) && !inside.Contains(e)) inside.Add(e);
                for (var i = 1; i < inside.Count; i++) g.Connect(inside[0], inside[i], SpeedModel.OffRoadFactor);
            }

            // spots: the queue joins the network, the bay hangs off the queue and the road
            var roadCount = g.NodeCount;
            g.roadNodeCount = roadCount;
            foreach (var kv in site.Spots)
            {
                if (kv.Key == BaySpot) continue;
                var n = g.AddNode(kv.Value.X, 0, kv.Value.Z);
                g.spotNodes[kv.Key] = n;
                var near = g.Nearest(kv.Value.X, kv.Value.Z, roadCount);
                if (near >= 0) g.Connect(n, near, SpeedModel.OffRoadFactor);
            }

            if (site.Spots.TryGetValue(BaySpot, out var bay))
            {
                var n = g.AddNode(bay.X, 0, bay.Z);
                g.spotNodes[BaySpot] = n;
                var near = g.Nearest(bay.X, bay.Z, roadCount);
                if (near >= 0) g.Connect(n, near, SpeedModel.OffRoadFactor);
                if (g.spotNodes.TryGetValue(QueueSpot, out var queue)) g.Connect(n, queue, SpeedModel.BayApproachFactor);
            }

            return g;
        }

        int AddNode(double x, double y, double z)
        {
            nx.Add(x);
            ny.Add(y);
            nz.Add(z);
            adjacency.Add(new List<Edge>());
            return nx.Count - 1;
        }

        int NodeAt(double x, double y, double z)
        {
            for (var i = 0; i < nx.Count; i++)
                if (Math.Abs(nx[i] - x) < MergeTolerance && Math.Abs(nz[i] - z) < MergeTolerance) return i;
            return AddNode(x, y, z);
        }

        double Dist(int a, int b) => Math.Sqrt((nx[a] - nx[b]) * (nx[a] - nx[b]) + (nz[a] - nz[b]) * (nz[a] - nz[b]));

        void Connect(int a, int b, double factor)
        {
            if (a == b) return;
            var len = Dist(a, b);
            var grade = len > 1e-6 ? (ny[b] - ny[a]) / len * 100.0 : 0;
            foreach (var e in adjacency[a])
                if (e.To == b) return;
            adjacency[a].Add(new Edge { To = b, Length = len, Factor = factor, GradePct = grade });
            adjacency[b].Add(new Edge { To = a, Length = len, Factor = factor, GradePct = -grade });
        }

        int Nearest(double x, double z, int limit)
        {
            var best = -1;
            var bestD = double.MaxValue;
            for (var i = 0; i < limit; i++)
            {
                var d = (nx[i] - x) * (nx[i] - x) + (nz[i] - z) * (nz[i] - z);
                if (d < bestD) { bestD = d; best = i; }
            }

            return best;
        }

        List<int> NearestSet(double x, double z, int count)
        {
            var order = new List<int>();
            for (var i = 0; i < nx.Count; i++) order.Add(i);
            order.Sort((a, b) =>
                ((nx[a] - x) * (nx[a] - x) + (nz[a] - z) * (nz[a] - z)).CompareTo((nx[b] - x) * (nx[b] - x) + (nz[b] - z) * (nz[b] - z)));
            if (order.Count > count) order.RemoveRange(count, order.Count - count);
            return order;
        }

        /// <summary>The network node of a named spot, or -1.</summary>
        public int SpotNode(string name) => spotNodes.TryGetValue(name, out var n) ? n : -1;

        public bool TryNodePosition(int node, out double x, out double z)
        {
            if (node < 0 || node >= nx.Count) { x = z = 0; return false; }
            x = nx[node];
            z = nz[node];
            return true;
        }

        /// <summary>How many separate pieces the network is in (a connected site has one).</summary>
        public int Components()
        {
            var seen = new bool[nx.Count];
            var count = 0;
            for (var i = 0; i < nx.Count; i++)
            {
                if (seen[i]) continue;
                count++;
                var stack = new Stack<int>();
                stack.Push(i);
                seen[i] = true;
                while (stack.Count > 0)
                {
                    var n = stack.Pop();
                    foreach (var e in adjacency[n])
                        if (!seen[e.To]) { seen[e.To] = true; stack.Push(e.To); }
                }
            }

            return count;
        }

        /// <summary>The cheapest way from a place to a place over the site, or null when there is none.</summary>
        public Route Plan(double sx, double sz, double gx, double gz)
        {
            if (nx.Count == 0) return null;
            var starts = NearestSet(sx, sz, Candidates);
            var goals = NearestSet(gx, gz, Candidates);
            Route best = null;
            var bestCost = double.MaxValue;
            foreach (var s in starts)
            {
                var prev = new int[nx.Count];
                var dist = Dijkstra(s, prev);
                foreach (var g in goals)
                {
                    if (double.IsPositiveInfinity(dist[g])) continue;
                    var cost = Math.Sqrt(Sq(nx[s] - sx) + Sq(nz[s] - sz)) / SpeedModel.OffRoadFactor + dist[g]
                               + Math.Sqrt(Sq(nx[g] - gx) + Sq(nz[g] - gz)) / SpeedModel.OffRoadFactor;
                    if (cost >= bestCost) continue;
                    bestCost = cost;
                    best = Assemble(sx, sz, gx, gz, s, g, prev);
                }
            }

            return best;
        }

        /// <summary>
        /// As <see cref="Plan(double,double,double,double)"/>, but every off-road leg (the machine to the road, the road to
        /// the goal) keeps a machine sweeping a circle of <paramref name="footprint"/> metres clear of every obstacle by
        /// <see cref="ParkingLot.TravelClearance"/>, going around them when the straight line does not. An obstacle an endpoint
        /// already stands nearer than that is kept no nearer than the end is (a machine in a bay can drive out of it). Null when no clear approach
        /// exists. The roads themselves are taken as built.
        /// </summary>
        public Route Plan(double sx, double sz, double gx, double gz, IReadOnlyList<Obstacle> avoid, double footprint)
        {
            if (avoid == null || avoid.Count == 0) return Plan(sx, sz, gx, gz);
            if (nx.Count == 0) return null;
            var clearance = footprint + ParkingLot.TravelClearance;
            // the joins across a pad and to a spot are open ground too: a machine is not sent down one that runs through something
            var joins = new Dictionary<long, bool>();
            Func<int, int, bool> usable = (a, b) =>
            {
                var edge = adjacency[a].Find(e => e.To == b);
                if (Math.Abs(edge.Factor - SpeedModel.OffRoadFactor) > 1e-9 && Math.Abs(edge.Factor - SpeedModel.BayApproachFactor) > 1e-9) return true;
                var key = a < b ? (long)a * nx.Count + b : (long)b * nx.Count + a;
                if (!joins.TryGetValue(key, out var ok))
                    joins[key] = ok = Detour.StraightClear(nx[a], nz[a], nx[b], nz[b], avoid, clearance);
                return ok;
            };
            var starts = ClearApproaches(sx, sz, avoid, clearance);
            var goals = ClearApproaches(gx, gz, avoid, clearance);
            Route best = null;
            var bestCost = double.MaxValue;
            foreach (var s in starts)
            {
                var prev = new int[nx.Count];
                var dist = Dijkstra(s.Node, prev, usable);
                foreach (var g in goals)
                {
                    if (double.IsPositiveInfinity(dist[g.Node])) continue;
                    var cost = s.Length / SpeedModel.OffRoadFactor + dist[g.Node] + g.Length / SpeedModel.OffRoadFactor;
                    if (cost >= bestCost) continue;
                    bestCost = cost;
                    best = Assemble(s.Path, g.Path, s.Node, g.Node, prev);
                }
            }

            return best;
        }

        struct Approach
        {
            public int Node;
            public double Length;
            public List<double[]> Path; // from the point to the node, the point first
        }

        // the nearest few road nodes the point can reach clear of the obstacles, with the way there
        readonly Dictionary<(double, double, double), List<Approach>> approaches = new Dictionary<(double, double, double), List<Approach>>();
        IReadOnlyList<Obstacle> approachesFor;

        List<Approach> ClearApproaches(double x, double z, IReadOnlyList<Obstacle> active, double clearance)
        {
            // a machine asking about slot after slot asks from the same place: remember where it can get on the road
            if (!ReferenceEquals(approachesFor, active) || approaches.Count > 64) { approaches.Clear(); approachesFor = active; }
            if (approaches.TryGetValue((x, z, clearance), out var known)) return known;
            return approaches[(x, z, clearance)] = FindApproaches(x, z, active, clearance);
        }

        List<Approach> FindApproaches(double x, double z, IReadOnlyList<Obstacle> active, double clearance)
        {
            var order = new List<int>();
            for (var i = 0; i < roadNodeCount; i++) order.Add(i);
            order.Sort((a, b) => (Sq(nx[a] - x) + Sq(nz[a] - z)).CompareTo(Sq(nx[b] - x) + Sq(nz[b] - z)));
            var found = new List<Approach>();
            foreach (var n in order)
            {
                if (found.Count >= Candidates) break;
                var path = Detour.Find(x, z, nx[n], nz[n], active, clearance);
                if (path == null) continue;
                found.Add(new Approach { Node = n, Path = path, Length = Detour.Length(path) });
            }

            return found;
        }

        Route Assemble(List<double[]> toRoad, List<double[]> fromRoad, int s, int g, int[] prev)
        {
            var chain = new List<int>();
            for (var n = g; n >= 0; n = prev[n])
            {
                chain.Add(n);
                if (n == s) break;
            }

            chain.Reverse();
            var b = new RouteBuilder();
            b.Start(toRoad[0][0], toRoad[0][1]);
            for (var i = 1; i < toRoad.Count; i++) b.Leg(toRoad[i][0], toRoad[i][1], SpeedModel.OffRoadFactor, 0);
            for (var i = 1; i < chain.Count; i++)
            {
                var a = chain[i - 1];
                var c = chain[i];
                foreach (var e in adjacency[a])
                    if (e.To == c)
                    {
                        b.Leg(nx[c], nz[c], e.Factor, e.GradePct);
                        break;
                    }
            }

            // the goal's path runs point -> node; the route runs node -> point
            for (var i = fromRoad.Count - 2; i >= 0; i--) b.Leg(fromRoad[i][0], fromRoad[i][1], SpeedModel.OffRoadFactor, 0);
            return b.Build();
        }

        static double Sq(double v) => v * v;

        double[] Dijkstra(int source, int[] prev, Func<int, int, bool> usable = null)
        {
            var dist = new double[nx.Count];
            for (var i = 0; i < dist.Length; i++)
            {
                dist[i] = double.PositiveInfinity;
                prev[i] = -1;
            }

            dist[source] = 0;
            var done = new bool[nx.Count];
            for (var iter = 0; iter < nx.Count; iter++)
            {
                var u = -1;
                var best = double.PositiveInfinity;
                for (var i = 0; i < dist.Length; i++)
                    if (!done[i] && dist[i] < best) { best = dist[i]; u = i; }
                if (u < 0) break;
                done[u] = true;
                foreach (var e in adjacency[u])
                {
                    if (usable != null && !usable(u, e.To)) continue;
                    var alt = dist[u] + e.Cost;
                    if (alt < dist[e.To]) { dist[e.To] = alt; prev[e.To] = u; }
                }
            }

            return dist;
        }

        Route Assemble(double sx, double sz, double gx, double gz, int s, int g, int[] prev)
        {
            var chain = new List<int>();
            for (var n = g; n >= 0; n = prev[n])
            {
                chain.Add(n);
                if (n == s) break;
            }

            chain.Reverse();
            var b = new RouteBuilder();
            b.Start(sx, sz);
            b.Leg(nx[s], nz[s], SpeedModel.OffRoadFactor, 0);
            for (var i = 1; i < chain.Count; i++)
            {
                var a = chain[i - 1];
                var c = chain[i];
                foreach (var e in adjacency[a])
                    if (e.To == c)
                    {
                        b.Leg(nx[c], nz[c], e.Factor, e.GradePct);
                        break;
                    }
            }

            b.Leg(gx, gz, SpeedModel.OffRoadFactor, 0);
            return b.Build();
        }
    }
}

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// A way around the obstacles between two points: the shortest path over the straight lines between the
    /// points and the corners of the obstacles' clearance outlines (a visibility graph). Only the obstacles a
    /// path actually runs into are built into the graph, so a long clear leg costs one sweep.
    ///
    /// Every obstacle is kept <c>clearance</c> away, except that one an end of the path already stands nearer than
    /// that (a road end beside a sign, a machine in the bay) is kept as far as that end is: the path may leave it
    /// but not go nearer than it starts.
    /// </summary>
    internal static class Detour
    {
        const double SampleStep = 1.0, Slack = 0.3, Tolerance = 1e-6;

        public static double Length(List<double[]> path)
        {
            var len = 0.0;
            for (var i = 1; i < path.Count; i++)
                len += Math.Sqrt((path[i][0] - path[i - 1][0]) * (path[i][0] - path[i - 1][0]) + (path[i][1] - path[i - 1][1]) * (path[i][1] - path[i - 1][1]));
            return len;
        }

        static int Violator(IReadOnlyList<Obstacle> all, double[] thr, double x, double z)
        {
            for (var i = 0; i < all.Count; i++)
                if (!all[i].IsAtLeast(x, z, thr[i] - Tolerance)) return i;
            return -1;
        }

        // every one of the listed obstacles the segment runs nearer to than it may
        static void Blockers(IReadOnlyList<Obstacle> all, double[] thr, double ax, double az, double bx, double bz, List<int> into)
        {
            var len = Math.Sqrt((bx - ax) * (bx - ax) + (bz - az) * (bz - az));
            var n = Math.Max(1, (int)Math.Ceiling(len / SampleStep));
            for (var k = 0; k < all.Count; k++)
            {
                if (into.Contains(k)) continue;
                for (var i = 0; i <= n; i++)
                {
                    var t = (double)i / n;
                    if (all[k].IsAtLeast(ax + (bx - ax) * t, az + (bz - az) * t, thr[k] - Tolerance)) continue;
                    into.Add(k);
                    break;
                }
            }
        }

        // the first of the listed obstacles the segment runs nearer to than it may, or -1
        static int Blocker(IReadOnlyList<Obstacle> all, double[] thr, IReadOnlyList<int> which, double ax, double az, double bx, double bz)
        {
            var len = Math.Sqrt((bx - ax) * (bx - ax) + (bz - az) * (bz - az));
            var n = Math.Max(1, (int)Math.Ceiling(len / SampleStep));
            for (var i = 0; i <= n; i++)
            {
                var t = (double)i / n;
                var x = ax + (bx - ax) * t;
                var z = az + (bz - az) * t;
                for (var k = 0; k < which.Count; k++)
                    if (!all[which[k]].IsAtLeast(x, z, thr[which[k]] - Tolerance)) return which[k];
            }

            return -1;
        }

        /// <summary>True when the straight line from a to b keeps <paramref name="clearance"/> (as <see cref="Find"/> reads it).</summary>
        public static bool StraightClear(double ax, double az, double bx, double bz, IReadOnlyList<Obstacle> all, double clearance)
        {
            var thr = new double[all.Count];
            var everyone = new List<int>();
            for (var i = 0; i < all.Count; i++)
            {
                thr[i] = Math.Min(clearance, Math.Min(all[i].Distance(ax, az), all[i].Distance(bx, bz)));
                everyone.Add(i);
            }

            return Blocker(all, thr, everyone, ax, az, bx, bz) < 0;
        }

        /// <summary>The path from a to b (both included) clear of every obstacle by <paramref name="clearance"/>, or null when there is none.</summary>
        public static List<double[]> Find(double ax, double az, double bx, double bz, IReadOnlyList<Obstacle> all, double clearance)
        {
            var thr = new double[all.Count];
            var everyone = new List<int>();
            for (var i = 0; i < all.Count; i++)
            {
                thr[i] = Math.Min(clearance, Math.Min(all[i].Distance(ax, az), all[i].Distance(bx, bz)));
                everyone.Add(i);
            }

            // start with what the straight line runs into, and take on whatever each path found still runs into
            var relevant = new List<int>();
            Blockers(all, thr, ax, az, bx, bz, relevant);
            for (var round = 0; round <= 2 * all.Count; round++)
            {
                var droppers = new List<int>();
                var path = Shortest(ax, az, bx, bz, relevant, all, thr, clearance, droppers);
                var more = new List<int>();
                if (path == null)
                {
                    // the corners that would have let it round the obstacles in hand may stand inside others: those matter too
                    foreach (var d in droppers)
                        if (!relevant.Contains(d) && !more.Contains(d)) more.Add(d);
                }
                else
                {
                    for (var i = 1; i < path.Count; i++) Blockers(all, thr, path[i - 1][0], path[i - 1][1], path[i][0], path[i][1], more);
                    more.RemoveAll(m => relevant.Contains(m));
                    if (more.Count == 0) return path;
                }

                if (more.Count == 0) return null;
                relevant.AddRange(more);
            }

            return null;
        }

        static List<double[]> Shortest(double ax, double az, double bx, double bz, List<int> relevant, IReadOnlyList<Obstacle> all, double[] thr, double clearance, List<int> droppers)
        {
            var px = new List<double> { ax, bx };
            var pz = new List<double> { az, bz };
            var wx = new List<double>();
            var wz = new List<double>();
            foreach (var o in relevant) all[o].CornerWaypoints(clearance + Slack, wx, wz);
            for (var i = 0; i < wx.Count; i++)
            {
                var twin = false;
                for (var j = 2; j < px.Count && !twin; j++) twin = Math.Abs(px[j] - wx[i]) < 1e-6 && Math.Abs(pz[j] - wz[i]) < 1e-6;
                if (twin) continue;
                var inside = Violator(all, thr, wx[i], wz[i]);
                if (inside < 0) { px.Add(wx[i]); pz.Add(wz[i]); }
                else droppers.Add(inside);
            }

            var count = px.Count;
            var dist = new double[count];
            var prev = new int[count];
            var done = new bool[count];
            for (var i = 0; i < count; i++) { dist[i] = double.PositiveInfinity; prev[i] = -1; }
            dist[0] = 0;
            for (var iter = 0; iter < count; iter++)
            {
                var u = -1;
                var best = double.PositiveInfinity;
                for (var i = 0; i < count; i++)
                    if (!done[i] && dist[i] < best) { best = dist[i]; u = i; }
                if (u < 0 || u == 1) break;
                done[u] = true;
                for (var v = 0; v < count; v++)
                {
                    if (done[v]) continue;
                    var d = Math.Sqrt((px[u] - px[v]) * (px[u] - px[v]) + (pz[u] - pz[v]) * (pz[u] - pz[v]));
                    if (dist[u] + d >= dist[v]) continue;
                    if (relevant.Count > 0 && Blocker(all, thr, relevant, px[u], pz[u], px[v], pz[v]) >= 0) continue;
                    dist[v] = dist[u] + d;
                    prev[v] = u;
                }
            }

            if (double.IsPositiveInfinity(dist[1])) return null;
            var path = new List<double[]>();
            for (var n = 1; n >= 0; n = prev[n]) path.Add(new[] { px[n], pz[n] });
            path.Reverse();
            return path;
        }
    }
}
