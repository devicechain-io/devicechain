// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Simulation
{
    public enum SampleKind { Measurement, Location }

    /// <summary>
    /// One thing a device will say, fixed at the moment the fixed step produced it. Immutable on
    /// purpose: a retry sends this same object, so the same occurred time and the same values go out
    /// again and a late send is never relabelled as a new reading.
    /// </summary>
    public sealed class Sample
    {
        Sample(long sequence, SampleKind kind, DateTimeOffset occurredUtc)
        {
            Sequence = sequence;
            Kind = kind;
            OccurredUtc = occurredUtc;
        }

        /// <summary>Per-device production order.</summary>
        public long Sequence { get; }

        public SampleKind Kind { get; }

        /// <summary>Wall-clock UTC when the sample was produced.</summary>
        public DateTimeOffset OccurredUtc { get; }

        /// <summary>Measurement: every metric the event carries, by key.</summary>
        public IReadOnlyDictionary<string, double> Values { get; private set; }

        public double Latitude { get; private set; }
        public double Longitude { get; private set; }

        /// <summary>Height above the ellipsoid, metres.</summary>
        public double Elevation { get; private set; }

        /// <summary>Ground speed, m/s, never negative.</summary>
        public double SpeedMps { get; private set; }

        /// <summary>Degrees clockwise from north, [0, 360).</summary>
        public double HeadingDegrees { get; private set; }

        public static Sample Measurement(long sequence, DateTimeOffset occurredUtc, IReadOnlyDictionary<string, double> values)
            => new Sample(sequence, SampleKind.Measurement, occurredUtc) { Values = values };

        public static Sample Location(long sequence, DateTimeOffset occurredUtc, GeoPoint at, double elevation, double speedMps, double headingDegrees)
            => new Sample(sequence, SampleKind.Location, occurredUtc)
            {
                Latitude = at.Latitude,
                Longitude = at.Longitude,
                Elevation = elevation,
                SpeedMps = speedMps < 0 ? 0 : speedMps,
                HeadingDegrees = headingDegrees,
            };
    }
}
