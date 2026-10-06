// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Simulation
{
    /// <summary>
    /// When a device says what. One measurement event per second carrying every metric the device
    /// has (one emit, not one per metric); a Location fix twice a second while it moves and every two
    /// seconds while it is stopped; the plant sends no Location. Each sample is stamped with the wall
    /// clock the caller passes, at the moment it is produced. Cadence is kept on a schedule rather than
    /// "a period after the last send", so frame jitter does not stretch it, and a long stall resumes
    /// at the normal rate rather than in a burst.
    /// </summary>
    public sealed class TelemetryScheduler
    {
        public static readonly TimeSpan MeasurementPeriod = TimeSpan.FromSeconds(1);
        public static readonly TimeSpan MovingLocationPeriod = TimeSpan.FromSeconds(0.5);
        public static readonly TimeSpan StoppedLocationPeriod = TimeSpan.FromSeconds(2);

        readonly bool hasLocation;
        long sequence;
        bool started;
        DateTimeOffset nextMeasurement, nextLocation, lastLocationDue;
        double nominalRate;

        public TelemetryScheduler(EquipmentKind kind)
        {
            hasLocation = kind != EquipmentKind.Plant;
            nominalRate = Rate(false);
        }

        /// <summary>The events per second this device would produce right now, which the send pacer scales its limit from.</summary>
        public double NominalRatePerSecond => System.Threading.Volatile.Read(ref nominalRate);

        double Rate(bool moving) => 1.0 / MeasurementPeriod.TotalSeconds
            + (hasLocation ? 1.0 / (moving ? MovingLocationPeriod : StoppedLocationPeriod).TotalSeconds : 0.0);

        /// <summary>Produces whatever is due at <paramref name="now"/> into <paramref name="output"/>.</summary>
        public void Tick(DateTimeOffset now, MachineModel model, MotionEstimator motion, double east, double north, double unityY, ICollection<Sample> output)
        {
            var moving = hasLocation && motion != null && motion.IsMoving;
            System.Threading.Volatile.Write(ref nominalRate, Rate(moving));
            if (!started)
            {
                started = true;
                nextMeasurement = now;
                nextLocation = now;
                lastLocationDue = now - StoppedLocationPeriod;
            }

            if (now >= nextMeasurement)
            {
                output.Add(Sample.Measurement(++sequence, now, model.Measurements()));
                nextMeasurement += MeasurementPeriod;
                if (nextMeasurement <= now) nextMeasurement = now + MeasurementPeriod;
            }

            if (!hasLocation || motion == null) return;

            // a machine that starts moving does not wait out a stopped-cadence gap
            var movingDue = lastLocationDue + MovingLocationPeriod;
            if (moving && movingDue < nextLocation) nextLocation = movingDue;
            if (now < nextLocation) return;

            var geo = SiteDefinition.ToGeographic(east, north);
            output.Add(Sample.Location(++sequence, now, geo, SiteDefinition.EllipsoidHeight(unityY), motion.SpeedMps, motion.HeadingDegrees));
            var due = nextLocation;
            var period = moving ? MovingLocationPeriod : StoppedLocationPeriod;
            nextLocation += period;
            if (nextLocation <= now)
            {
                // a stall: resume at the normal rate from here, not as a burst
                nextLocation = now + period;
                due = now;
            }

            lastLocationDue = due;
        }
    }
}
