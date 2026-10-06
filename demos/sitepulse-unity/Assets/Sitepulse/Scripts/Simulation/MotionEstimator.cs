// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sitepulse.Simulation
{
    /// <summary>
    /// Speed and heading of a machine, from where it has been. Position is site-local metres (east,
    /// north). Velocity is smoothed with an exponential filter, so a frame's jitter does not show as
    /// speed; heading is the bearing of the smoothed motion, and while the machine is too slow for a
    /// bearing to mean anything the last one is held (a parked truck keeps the heading it arrived on,
    /// it does not swing to north). Before the first movement the heading is the way the machine faces.
    /// </summary>
    public sealed class MotionEstimator
    {
        /// <summary>A step faster than this is a teleport (a track looping, a re-placement), not motion.</summary>
        public const double TeleportSpeedMps = 40.0;

        const double HoldBelowMps = 0.4, StartMovingMps = 0.5, StopMovingMps = 0.2;

        readonly double tau;
        double lastEast, lastNorth, vEast, vNorth, heading;
        bool seen, moving;

        public MotionEstimator(double smoothingSeconds = 0.6)
        {
            tau = smoothingSeconds;
        }

        public double SpeedMps { get; private set; }

        /// <summary>Degrees clockwise from north, [0, 360).</summary>
        public double HeadingDegrees => heading;

        /// <summary>Moving with hysteresis, so a machine hovering near the threshold does not flip cadence every step.</summary>
        public bool IsMoving => moving;

        public void Update(double east, double north, double dt, double facingDegrees)
        {
            if (!seen)
            {
                seen = true;
                lastEast = east;
                lastNorth = north;
                heading = SiteDefinition.Canonical(facingDegrees);
                return;
            }

            if (!(dt > 0)) return;
            var rawE = (east - lastEast) / dt;
            var rawN = (north - lastNorth) / dt;
            lastEast = east;
            lastNorth = north;
            if (Math.Sqrt(rawE * rawE + rawN * rawN) > TeleportSpeedMps)
            {
                vEast = vNorth = 0;
            }
            else
            {
                var a = 1.0 - Math.Exp(-dt / tau);
                vEast += (rawE - vEast) * a;
                vNorth += (rawN - vNorth) * a;
            }

            SpeedMps = Math.Sqrt(vEast * vEast + vNorth * vNorth);
            if (SpeedMps >= HoldBelowMps) heading = SiteDefinition.HeadingDegrees(vEast, vNorth);
            moving = moving ? SpeedMps > StopMovingMps : SpeedMps > StartMovingMps;
        }
    }
}
