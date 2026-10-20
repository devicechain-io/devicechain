// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// A way the task layer drives a machine somewhere: planned by the site (<see cref="ITaskWorld"/>), stepped by the machine's controller. A
    /// haul truck's trips run on the site's lanes (it rides its track to an exit, drives the stand's lane, and rejoins its track through a
    /// merge); every other machine's run on a free route over open ground. Either way the interlocking says how far it may go.
    /// </summary>
    public interface ITrip
    {
        /// <summary>The route drawn for it (the highlight and the recording); null while the machine still rides its track to where it leaves it.</summary>
        Route Route { get; }

        /// <summary>How far the trip is, in metres (what is said when the command is accepted).</summary>
        double Metres { get; }

        /// <summary>How long it takes at the machine's pace, waiting aside, in seconds.</summary>
        double EtaSeconds { get; }

        /// <summary>Where it is going, in words ("slot 2 of sp-zone-yard", "stand yard-1").</summary>
        string Place { get; }

        /// <summary>How fast the machine is driven now, metres per second (0 while it rides its track).</summary>
        double Speed { get; }

        /// <summary>Whether it ends with the machine back on its track (a trip back to work) rather than standing where it went.</summary>
        bool EndsOnTrack { get; }

        /// <summary>One step. True when the machine is where the trip was going: standing there, or back on its track.</summary>
        bool Step(double dt);

        /// <summary>What the machine is waiting for, in words, while it is held (null when it is moving).</summary>
        string Waiting { get; }

        /// <summary>
        /// Whether the machine may be left standing where it is now: off every lane, so that nothing waits on it. A truck on a lane may not (a
        /// command that fails there drives it on to where it can stand, or back to work).
        /// </summary>
        bool MayStandHere { get; }

        /// <summary>The trip is given up: the machine stands where it is (only ever where it <see cref="MayStandHere"/>), and what the trip held is given back.</summary>
        void Abandon();
    }

    /// <summary>
    /// Whether a machine may take a free route over open ground, and when: the site's side of a free trip. A route is admitted only where it
    /// keeps out of everyone's way, and one machine at a time drives one (free routes are not held to each other).
    /// </summary>
    public interface IWayKeeper
    {
        /// <summary>
        /// Whether a machine may take a free route along <paramref name="line"/>: null when it may, otherwise why not (it crosses the lanes,
        /// it passes another machine at work, it would leave the machine standing where it may not). Asks only.
        /// </summary>
        string Check(IMachineBody body, LaneLine line, bool endsStanding);

        /// <summary>Whether the machine may set off on its route now, and if not why (another machine is driving one).</summary>
        bool MayLeave(string id, out string why);

        /// <summary>The machine sets off along <paramref name="line"/> (the route checked again from where it is): null, or why it may not after all.</summary>
        string Begin(IMachineBody body, LaneLine line, bool endsStanding);

        /// <summary>How far along its line (metres from its start) the machine may drive now: the whole of an admitted route, unless the keeper holds it.</summary>
        double Reach(string id, double s);

        /// <summary>The machine's route is driven, or given up: it is done with the way.</summary>
        void Ended(string id);
    }

    /// <summary>A trip over open ground along one route, held to what a <see cref="IWayKeeper"/> allows.</summary>
    internal sealed class FreeTrip : ITrip
    {
        readonly IMachineBody body;
        readonly Kinematics kin;
        readonly IWayKeeper keeper;
        readonly RouteFollower follow = new RouteFollower();
        readonly Func<Route> replan;
        readonly double laneShare;
        readonly TrackPoint rejoin;
        readonly bool endsStanding;
        readonly double fromX, fromZ;
        Route route;
        string waiting;
        bool started, driven, done;

        /// <param name="replan">The same route planned again from where the machine is, for when it moved before it could leave (it went on working).</param>
        public FreeTrip(IMachineBody body, Route route, Func<Route> replan, double laneShare, IWayKeeper keeper, string place, bool endsStanding, TrackPoint? rejoin = null)
        {
            this.endsStanding = endsStanding;
            this.body = body;
            this.route = route;
            this.replan = replan;
            fromX = body.X;
            fromZ = body.Z;
            this.laneShare = laneShare;
            this.keeper = keeper;
            kin = Kinematics.For(body.Kind);
            Place = place;
            EndsOnTrack = rejoin.HasValue;
            this.rejoin = rejoin ?? default;
        }

        public Route Route => done ? null : route;
        public double Metres => route.Length;
        public double EtaSeconds => route.Eta(kin.Cruise);
        public string Place { get; }
        public double Speed => follow.Speed;
        public bool EndsOnTrack { get; }
        public string Waiting => waiting;
        public bool MayStandHere => true;

        public bool Step(double dt)
        {
            if (done) return true;
            waiting = null;
            if (!started)
            {
                if (!keeper.MayLeave(body.Id, out var why))
                {
                    waiting = why;
                    return false;
                }

                if (body.Attached) body.Detach();

                // a machine that went on working while it waited to leave starts from where it is now
                if (Math.Abs(body.X - fromX) > 0.05 || Math.Abs(body.Z - fromZ) > 0.05)
                {
                    var again = replan();
                    if (again == null)
                    {
                        waiting = "no route from where it stands";
                        return false;
                    }

                    route = again;
                }

                var refused = keeper.Begin(body, LaneLine.Build(route, laneShare), endsStanding);
                if (refused != null)
                {
                    waiting = refused;
                    return false;
                }

                follow.Start(route, body.HeadingDegrees, laneShare);
                started = true;
            }

            if (!driven)
            {
                if (!follow.Advance(dt, body, kin, s => keeper.Reach(body.Id, s))) return false;
                driven = true;
            }

            if (EndsOnTrack)
            {
                if (!follow.Align(dt, body, kin, rejoin.HeadingDegrees)) return false;
                body.Attach(rejoin);
            }

            keeper.Ended(body.Id);
            done = true;
            return true;
        }

        public void Abandon()
        {
            if (done) return;
            follow.Clear();
            done = true;
            if (started) keeper.Ended(body.Id);
        }
    }

    /// <summary>
    /// The trips a machine drives over open ground: to a parking slot of a zone, to the refuel queue and into the bay, and back to its track.
    /// Plans with the route network (grade-limited, clear of what stands on the site) and holds each trip to its keeper.
    /// </summary>
    public sealed class FreeTrips
    {
        readonly SiteGeometry site;
        readonly RouteGraph graph;
        readonly ParkingLot parking;
        readonly Dictionary<string, ParkingSlot> held = new Dictionary<string, ParkingSlot>(StringComparer.Ordinal);

        public FreeTrips(SiteGeometry site, RouteGraph graph, ParkingLot parking)
        {
            this.site = site ?? throw new ArgumentNullException(nameof(site));
            this.graph = graph ?? throw new ArgumentNullException(nameof(graph));
            this.parking = parking ?? throw new ArgumentNullException(nameof(parking));
        }

        public SiteGeometry Site => site;
        public RouteGraph Graph => graph;
        public ParkingLot Parking => parking;

        static string F0(double v) => v.ToString("0", CultureInfo.InvariantCulture);

        /// <summary>The machine no longer stands at the slot it was sent to: the slot is free for another.</summary>
        public void Release(string id)
        {
            parking.Release(id);
            held.Remove(id);
        }

        /// <summary>Why no route is planned from where a machine stands, or null when one may be.</summary>
        public string Outside(IMachineBody body) =>
            graph.OnSite(body.X, body.Z) ? null : "the machine is outside the site at (" + F0(body.X) + ", " + F0(body.Z) + "): no route is planned over ground the site does not hold";

        /// <summary>A refusal that says why: the planner drops every stretch over the grade limit, so a place only a steeper way leads to has no route.</summary>
        public static string NoRoute(string where) =>
            "no drivable route to " + where + " (a route stays within a " + Grade.MaxPct.ToString("0", CultureInfo.InvariantCulture) + " % grade and clear of obstacles)";

        /// <summary>
        /// A trip to a free parking slot of a zone: nearest the site's parking spot first when the zone holds it, otherwise nearest the machine,
        /// with the machine's whole footprint clear of everything standing on the site and of <paramref name="avoid"/>, by a route clear of them
        /// too, that the keeper admits. The slot is the machine's from now on. Null with the reason when there is none.
        /// </summary>
        public ITrip ToArea(IMachineBody body, string area, double laneShare, IWayKeeper keeper, Func<double, double, bool> occupied, IReadOnlyList<Obstacle> avoid, out string refusal)
        {
            refusal = Outside(body);
            if (refusal != null) return null;
            if (!site.TryZone(area, out var zone))
            {
                refusal = "no scene geometry for area " + area;
                return null;
            }

            var gateX = body.X;
            var gateZ = body.Z;
            if (site.Spots.TryGetValue(SiteGeometry.ParkingSpot, out var park) && zone.Contains(park.X, park.Z))
            {
                gateX = park.X;
                gateZ = park.Z;
            }

            var footprint = ParkingLot.FootprintRadius(body.Kind);
            var obstacles = avoid ?? site.Obstacles;
            var tried = new List<(double X, double Z)>();
            string kept = null;
            var unreachable = false;
            while (true)
            {
                var slot = parking.Claim(body.Id, zone, gateX, gateZ,
                    (x, z) => (occupied != null && occupied(x, z)) || tried.Exists(u => Math.Abs(u.X - x) < 1e-6 && Math.Abs(u.Z - z) < 1e-6),
                    ParkingLot.Margin(body.Kind), obstacles, footprint);
                if (slot == null) break;
                tried.Add((slot.Value.X, slot.Value.Z));
                var route = graph.Plan(body.X, body.Z, slot.Value.X, slot.Value.Z, obstacles, ParkingLot.TravelRadius(body.Kind));
                if (route == null)
                {
                    unreachable = true;
                    continue;
                }

                var why = keeper.Check(body, LaneLine.Build(route, laneShare), true);
                if (why != null)
                {
                    kept = why;
                    continue;
                }

                var to = slot.Value;
                held[body.Id] = to;
                return new FreeTrip(body, route, () => graph.Plan(body.X, body.Z, to.X, to.Z, obstacles, ParkingLot.TravelRadius(body.Kind)), laneShare, keeper,
                    "slot " + (to.Index + 1).ToString(CultureInfo.InvariantCulture) + " of " + area, true);
            }

            // a machine parked somewhere keeps its place when it is not given another
            parking.Release(body.Id);
            if (held.TryGetValue(body.Id, out var before)) parking.Hold(body.Id, before);
            refusal = kept != null ? "no free place in " + area + " off the working routes (" + kept + ")"
                : unreachable ? NoRoute(area) : "no free parking slot in " + area;
            return null;
        }

        /// <summary>A trip to a spot of the site (the refuel queue), by the route network.</summary>
        public ITrip ToSpot(IMachineBody body, string spot, string words, double laneShare, IWayKeeper keeper, out string refusal)
        {
            refusal = Outside(body);
            if (refusal != null) return null;
            if (!site.Spots.TryGetValue(spot, out var at))
            {
                refusal = "this site has no " + words;
                return null;
            }

            var route = graph.Plan(body.X, body.Z, at.X, at.Z);
            if (route == null)
            {
                refusal = NoRoute(words);
                return null;
            }

            refusal = keeper.Check(body, LaneLine.Build(route, laneShare), false);
            return refusal != null ? null : new FreeTrip(body, route, () => graph.Plan(body.X, body.Z, at.X, at.Z), laneShare, keeper, words, false);
        }

        /// <summary>The short straight drive from the queue spot into the bay, at the bay's approach speed.</summary>
        public ITrip IntoBay(IMachineBody body, IWayKeeper keeper, out string refusal)
        {
            refusal = null;
            if (!site.Spots.TryGetValue(RouteGraph.BaySpot, out var bayAt))
            {
                refusal = "this site has no refuel bay";
                return null;
            }

            var route = Route.Straight(body.X, body.Z, bayAt.X, bayAt.Z, SpeedModel.BayApproachFactor);
            refusal = keeper.Check(body, LaneLine.Build(route, 1.0), false);
            return refusal != null ? null : new FreeTrip(body, route, () => Route.Straight(body.X, body.Z, bayAt.X, bayAt.Z, SpeedModel.BayApproachFactor), 1.0, keeper, "the refuel bay", false);
        }

        /// <summary>A trip back to the machine's own track: to the nearest point of it (or where the keeper says), and onto it there.</summary>
        public ITrip Back(IMachineBody body, double laneShare, IWayKeeper keeper, out string refusal)
        {
            if (!body.TryNearestTrackPoint(body.X, body.Z, out var point))
            {
                refusal = "no track of its own to return to";
                return null;
            }

            refusal = Outside(body);
            if (refusal != null) return null;
            var route = graph.Plan(body.X, body.Z, point.X, point.Z);
            if (route == null)
            {
                refusal = "no route back to its track";
                return null;
            }

            refusal = keeper.Check(body, LaneLine.Build(route, laneShare), false);
            return refusal != null ? null : new FreeTrip(body, route, () => graph.Plan(body.X, body.Z, point.X, point.Z), laneShare, keeper, "its track", false, point);
        }
    }
}
