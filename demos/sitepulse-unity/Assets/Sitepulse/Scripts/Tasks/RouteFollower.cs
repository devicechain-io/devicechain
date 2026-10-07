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
    /// Drives a body along a <see cref="Route"/>: the route is followed exactly in position, speed
    /// follows the surface, slope and the corner ahead and eases in to the end, and the heading turns
    /// toward the path at the machine's own yaw rate (so a sharp turn slows it). While something is in its
    /// way (<c>blocked</c>) the target speed is zero.
    /// </summary>
    internal sealed class RouteFollower
    {
        const double Lookahead = 2.0, ArriveWithin = 0.05, CreepSpeed = 0.3, SteerLimit = 25.0;

        /// <summary>The most a machine moves sideways for each metre it moves forward (a gentle swerve, not a sidestep).</summary>
        public const double SwerveMetresPerMetre = 0.35;

        /// <summary>How fast (m/s) a machine goes while it swerves round something on its line.</summary>
        public const double SwerveSpeed = 2.5;

        /// <summary>How fast (m/s) a machine backs up along its route.</summary>
        public const double ReverseSpeed = 1.5;

        /// <summary>The last stretch of a route, in metres, driven on its line: a machine arrives where the route ends.</summary>
        public const double OnLineMetres = 12.0;

        Route route;
        double s;

        /// <summary>How far to the right of the route's line the machine is driving (negative: left), to pass what stands on the line.</summary>
        public double Lateral { get; private set; }

        public double Speed { get; private set; }
        public double HeadingDegrees { get; private set; }
        public Route Route => route;
        public double Remaining => route == null ? 0 : Math.Max(0, route.Length - s);
        public bool Active => route != null;

        /// <summary>How far along the route the machine is, in metres.</summary>
        public double Progress => s;

        public void Start(Route r, double headingDegrees)
        {
            route = r;
            s = 0;
            Lateral = 0;
            HeadingDegrees = headingDegrees;
        }

        public void Clear()
        {
            route = null;
            Speed = 0;
            Lateral = 0;
        }

        public void Stop() => Speed = 0;

        public static double Delta(double toDegrees, double fromDegrees)
        {
            var d = (toDegrees - fromDegrees) % 360.0;
            if (d > 180.0) d -= 360.0;
            if (d < -180.0) d += 360.0;
            return d;
        }

        /// <summary>Moves the body one step. True when it has reached the end of the route.</summary>
        public bool Advance(double dt, IMachineBody body, Kinematics k, Func<double, double, double, bool> blocked, Func<Route, double, double, double, SteerAdvice> advise = null, Func<double, double, double, bool> touches = null)
        {
            if (route == null) return true;
            if (route.Legs == 0)
            {
                route = null;
                return true;
            }

            var remaining = route.Length - s;
            route.PointAt(s, out var px, out var pz, out _, out var leg);
            route.PointAt(Math.Min(route.Length, s + Lookahead), out _, out _, out var look, out _);

            var advice = advise != null ? advise(route, s, Speed, Lateral) : default;
            var headingBefore = HeadingDegrees;
            var err = Delta(look, HeadingDegrees);
            // a machine told to stand does not swing its length round where it stands, into whatever it is standing by
            var turn = advice.Stop && Speed < 0.05 && !advice.Reverse ? 0.0 : Math.Min(Math.Abs(err), k.YawRate * dt);
            HeadingDegrees = SiteDefinition.Canonical(HeadingDegrees + Math.Sign(err) * turn);
            var left = Math.Abs(Delta(look, HeadingDegrees));
            var align = left >= 90.0 ? 0.2 : Math.Max(0.2, Math.Cos(left * Math.PI / 180.0));

            var target = route.SpeedOnLeg(leg, k.Cruise) * align;
            target = Math.Min(target, Math.Sqrt(2.0 * k.Decel * Math.Max(0.0, remaining)) + CreepSpeed);
            var goal = remaining <= OnLineMetres ? 0.0 : advice.Lateral;
            if (advice.Reverse && s > 0.0)
            {
                Speed = 0;
                var back = Math.Min(ReverseSpeed * dt, s);
                s -= back;
                Lateral += Math.Max(-SwerveMetresPerMetre * back, Math.Min(SwerveMetresPerMetre * back, goal - Lateral));
                route.PointAt(s, out var rx, out var rz, out var rh, out _);
                var h0 = rh * Math.PI / 180.0;
                double bkx = rx + Math.Cos(h0) * Lateral, bkz = rz - Math.Sin(h0) * Lateral;
                if (touches != null && touches(bkx, bkz, HeadingDegrees))
                {
                    s += back;
                    return false;
                }

                body.Drive(bkx, bkz, HeadingDegrees, -back, 0.0);
                return false;
            }

            // beside the line while the machine is on its way to the side of it, and where it is going to be: the legacy lane rule asks about that
            var bx = px + Math.Cos(HeadingDegrees * Math.PI / 180.0) * Lateral;
            var bz = pz - Math.Sin(HeadingDegrees * Math.PI / 180.0) * Lateral;
            var swerving = !advice.Stop && Math.Abs(goal - Lateral) > 0.05;
            if (swerving) target = Math.Min(target, SwerveSpeed);
            if (advice.Stop || (!swerving && blocked != null && blocked(bx, bz, HeadingDegrees))) target = 0;

            var dv = target - Speed;
            Speed += Math.Max(-k.Decel * dt, Math.Min(k.Accel * dt, dv));
            if (Speed < 0) Speed = 0;

            var ds = Math.Min(Speed * dt, remaining);
            var sBefore = s;
            var lateralBefore = Lateral;
            s += ds;
            var swerve = SwerveMetresPerMetre * ds;
            Lateral += Math.Max(-swerve, Math.Min(swerve, goal - Lateral));
            route.PointAt(s, out var x, out var z, out var lineHeading, out _);
            if (Lateral != 0.0)
            {
                var lh = lineHeading * Math.PI / 180.0;
                x += Math.Cos(lh) * Lateral;
                z -= Math.Sin(lh) * Lateral;
            }

            // the last word: a step that would put the machine into another is not taken
            if (touches != null && touches(x, z, HeadingDegrees))
            {
                s = sBefore;
                Lateral = lateralBefore;
                HeadingDegrees = headingBefore;
                Speed = 0;
                return false;
            }

            var steer = Math.Max(-SteerLimit, Math.Min(SteerLimit, err));
            body.Drive(x, z, HeadingDegrees, ds, steer);

            if (route.Length - s > ArriveWithin) return false;
            Speed = 0;
            route = null;
            return true;
        }

        /// <summary>Turns in place toward a heading (at most <paramref name="dt"/> of the yaw rate). True when within a few degrees.</summary>
        public bool Align(double dt, IMachineBody body, Kinematics k, double toDegrees, Func<double, double, double, bool> touches = null)
        {
            var err = Delta(toDegrees, HeadingDegrees);
            var turn = Math.Min(Math.Abs(err), k.YawRate * dt);
            var turned = SiteDefinition.Canonical(HeadingDegrees + Math.Sign(err) * turn);
            // it does not swing its length round into what it stands beside
            if (touches != null && touches(body.X, body.Z, turned)) return false;
            HeadingDegrees = turned;
            body.Drive(body.X, body.Z, HeadingDegrees, 0, Math.Max(-SteerLimit, Math.Min(SteerLimit, err)));
            return Math.Abs(Delta(toDegrees, HeadingDegrees)) < 3.0;
        }
    }
}
