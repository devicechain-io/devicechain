// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>How a kind of machine moves: how fast, how hard it speeds up and brakes, how fast it can turn.</summary>
    public readonly struct Kinematics
    {
        public Kinematics(double cruise, double accel, double decel, double yawRate)
        {
            Cruise = cruise;
            Accel = accel;
            Decel = decel;
            YawRate = yawRate;
        }

        public double Cruise { get; }
        public double Accel { get; }
        public double Decel { get; }

        /// <summary>Degrees per second.</summary>
        public double YawRate { get; }

        public static Kinematics For(EquipmentKind kind)
        {
            switch (kind)
            {
                case EquipmentKind.Hauler: return new Kinematics(SpeedModel.HaulerCruise, 1.5, 3.0, 40.0);
                case EquipmentKind.Loader: return new Kinematics(SpeedModel.LoaderCruise, 1.2, 2.5, 50.0);
                default: return new Kinematics(SpeedModel.DozerCruise, 0.8, 2.0, 35.0);
            }
        }
    }

    /// <summary>
    /// Drives a body along a <see cref="Route"/>, keeping to the lane the road gives the way it is going (see <see cref="LaneLine"/>):
    /// the line is followed exactly in position, speed follows the surface, slope and the corner ahead and eases in to the end, and
    /// the heading turns toward the path at the machine's own yaw rate (so a sharp turn slows it). How far along its line it may go is
    /// someone else's to say (<c>reach</c>: the interlocking's authority); it brakes so as to stop short of it, and never passes it.
    /// </summary>
    internal sealed class RouteFollower
    {
        const double Lookahead = 2.0, ArriveWithin = 0.05, CreepSpeed = 0.3, SteerLimit = 25.0;

        /// <summary>A machine held short of its reach stops this far (metres) before it.</summary>
        public const double StopShortMetres = 0.3;

        Route route;
        LaneLine line;
        double s;   // metres driven along the line

        public double Speed { get; private set; }
        public double HeadingDegrees { get; private set; }
        public Route Route => route;
        public double Remaining => route == null ? 0 : Math.Max(0, line.Length - s);
        public bool Active => route != null;

        /// <summary>
        /// How much of its lane's offset this machine takes on the routes it drives next, from 0 (the road's own line) to 1 (the lane).
        /// It is the machine's own: a machine alone on a road can be given a smaller share and drift toward the middle of it.
        /// </summary>
        public double LaneShare { get; set; } = 1.0;

        /// <summary>How far to the right of the route's own line the machine is now (negative: to the left, in its lane).</summary>
        public double Lateral => route == null ? 0.0 : line.LateralAt(s);

        /// <summary>How far along the route's own line the machine is now, in metres (the line it drives is not the same length: see <see cref="LaneLine"/>).</summary>
        public double Progress => route == null ? 0.0 : line.CentrelineAt(s);

        /// <summary>The line the machine is driving (its lane along the route); null when it is not driving one.</summary>
        public LaneLine Line => line;

        /// <summary>How far along <see cref="Line"/> the machine is, in metres.</summary>
        public double LineProgress => s;

        /// <summary>Starts a route from its beginning; a machine already moving keeps its speed (it carries on, it does not stop to turn onto it).</summary>
        public void Start(Route r, double headingDegrees, double laneShare)
        {
            route = r;
            line = r == null ? null : LaneLine.Build(r, laneShare);
            s = 0;
            HeadingDegrees = headingDegrees;
        }

        public void Start(Route r, double headingDegrees) => Start(r, headingDegrees, LaneShare);

        /// <summary>Starts a route at a speed already reached (a truck leaving its track at speed onto a lane).</summary>
        public void StartMoving(Route r, double headingDegrees, double speed)
        {
            Start(r, headingDegrees, 0.0);
            Speed = Math.Max(0.0, speed);
        }

        public void Clear()
        {
            route = null;
            line = null;
            Speed = 0;
        }

        public void Stop() => Speed = 0;

        public static double Delta(double toDegrees, double fromDegrees)
        {
            var d = (toDegrees - fromDegrees) % 360.0;
            if (d > 180.0) d -= 360.0;
            if (d < -180.0) d += 360.0;
            return d;
        }

        /// <summary>A point <paramref name="right"/> metres to the right of (x, z) as seen facing <paramref name="headingDegrees"/>.</summary>
        public static void Beside(double x, double z, double headingDegrees, double right, out double ox, out double oz)
        {
            var h = headingDegrees * Math.PI / 180.0;
            ox = x + Math.Cos(h) * right;
            oz = z - Math.Sin(h) * right;
        }

        /// <summary>
        /// Moves the body one step. True when it has reached the end of the route. <paramref name="reach"/> says how far along the line (metres
        /// from its start) the machine may go now; without it the way is open to the end.
        /// </summary>
        public bool Advance(double dt, IMachineBody body, Kinematics k, Func<double, double> reach = null)
        {
            if (route == null) return true;
            if (route.Legs == 0)
            {
                route = null;
                line = null;
                return true;
            }

            var remaining = line.Length - s;
            var limit = reach == null ? line.Length : Math.Min(line.Length, Math.Max(s, reach(s)));
            line.PointAt(Math.Min(line.Length, s + Lookahead), out _, out _, out var look, out _);
            line.PointAt(s, out _, out _, out _, out var leg);

            var err = Delta(look, HeadingDegrees);
            var turn = Math.Min(Math.Abs(err), k.YawRate * dt);
            HeadingDegrees = SiteDefinition.Canonical(HeadingDegrees + Math.Sign(err) * turn);
            var left = Math.Abs(Delta(look, HeadingDegrees));
            var align = left >= 90.0 ? 0.2 : Math.Max(0.2, Math.Cos(left * Math.PI / 180.0));

            var target = route.SpeedOnLeg(leg, k.Cruise) * align;
            target = Math.Min(target, Math.Sqrt(2.0 * k.Decel * Math.Max(0.0, remaining)) + CreepSpeed);
            // held short of the end of the route: brake for it as for the end, and stop short of it
            if (limit < line.Length) target = Math.Min(target, Math.Sqrt(2.0 * k.Decel * Math.Max(0.0, limit - s - StopShortMetres)));

            var dv = target - Speed;
            Speed += Math.Max(-k.Decel * dt, Math.Min(k.Accel * dt, dv));
            if (Speed < 0) Speed = 0;

            var ds = Math.Max(0.0, Math.Min(Speed * dt, limit - s));
            if (ds < Speed * dt && limit < line.Length) Speed = ds / Math.Max(dt, 1e-9);
            s += ds;
            line.PointAt(s, out var x, out var z, out _, out _);

            var steer = Math.Max(-SteerLimit, Math.Min(SteerLimit, err));
            body.Drive(x, z, HeadingDegrees, ds, steer);

            if (line.Length - s > ArriveWithin) return false;
            Speed = 0;
            route = null;
            line = null;
            return true;
        }

        /// <summary>Turns in place toward a heading (at most <paramref name="dt"/> of the yaw rate). True when within a few degrees.</summary>
        public bool Align(double dt, IMachineBody body, Kinematics k, double toDegrees)
        {
            var err = Delta(toDegrees, HeadingDegrees);
            var turn = Math.Min(Math.Abs(err), k.YawRate * dt);
            HeadingDegrees = SiteDefinition.Canonical(HeadingDegrees + Math.Sign(err) * turn);
            body.Drive(body.X, body.Z, HeadingDegrees, 0, Math.Max(-SteerLimit, Math.Min(SteerLimit, err)));
            return Math.Abs(Delta(toDegrees, HeadingDegrees)) < 3.0;
        }
    }
}
