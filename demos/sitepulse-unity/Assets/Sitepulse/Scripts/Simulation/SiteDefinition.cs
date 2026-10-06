// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sitepulse.Simulation
{
    /// <summary>A WGS84 position in decimal degrees.</summary>
    public readonly struct GeoPoint
    {
        public GeoPoint(double latitude, double longitude)
        {
            Latitude = latitude;
            Longitude = longitude;
        }

        public double Latitude { get; }
        public double Longitude { get; }
    }

    /// <summary>
    /// The site's one geographic definition: where Unity site-local (0, 0) is on the globe, and how a
    /// metre east or north of it becomes a degree. Unity X is east, Z is north, both in metres, and
    /// (0, 0) is the terrain centre.
    ///
    /// This is the PLAYER'S HALF of a contract. The other half is the platform's pit geofence ring,
    /// <c>backend/sims/dc-simulator/sim/sitepulse_geofence.go</c> (<c>sitepulseOriginLatitude</c>,
    /// <c>sitepulseOriginLongitude</c> and the small-area formula in its header), which derives its
    /// ring from the same origin. If the two disagree, a machine reports a position the platform's
    /// fence does not agree with, by however far apart the origins are, and nothing on either side
    /// says so. The tests pin a literal vertex of that ring.
    ///
    /// The conversion is a small-area equirectangular approximation, not survey-grade geodesy.
    /// </summary>
    public static class SiteDefinition
    {
        public const double OriginLatitude = 39.0;
        public const double OriginLongitude = -117.0;
        public const double EarthRadiusMetres = 6371000.0;

        /// <summary>
        /// A DECLARED datum, not a survey: the height above the WGS84 ellipsoid that the scene's
        /// terrain height of zero is declared to stand at. About 1,800 m is plausible for central
        /// Nevada high desert. Elevation reported to the platform is the machine's terrain height
        /// (Unity y) plus this constant.
        /// </summary>
        public const double SiteDatumEllipsoidHeightM = 1800.0;

        static readonly double CosOrigin = Math.Cos(OriginLatitude * Math.PI / 180.0);

        /// <summary>The geographic position of a point <paramref name="east"/> and <paramref name="north"/> metres from the origin.</summary>
        public static GeoPoint ToGeographic(double east, double north) => new GeoPoint(
            OriginLatitude + north / EarthRadiusMetres * 180.0 / Math.PI,
            OriginLongitude + east / (EarthRadiusMetres * CosOrigin) * 180.0 / Math.PI);

        /// <summary>The inverse of <see cref="ToGeographic"/>.</summary>
        public static void ToLocal(double latitude, double longitude, out double east, out double north)
        {
            north = (latitude - OriginLatitude) * Math.PI / 180.0 * EarthRadiusMetres;
            east = (longitude - OriginLongitude) * Math.PI / 180.0 * EarthRadiusMetres * CosOrigin;
        }

        /// <summary>Height above the ellipsoid for a point at Unity height <paramref name="unityY"/>.</summary>
        public static double EllipsoidHeight(double unityY) => unityY + SiteDatumEllipsoidHeightM;

        /// <summary>
        /// The bearing of a motion vector in degrees clockwise from north, in [0, 360): moving east
        /// is 90, south 180, west 270. A zero vector has no bearing and answers 0; callers that must
        /// not report a bearing for a stationary machine hold the last one instead.
        /// </summary>
        public static double HeadingDegrees(double eastVelocity, double northVelocity)
        {
            var deg = Math.Atan2(eastVelocity, northVelocity) * 180.0 / Math.PI;
            return Canonical(deg);
        }

        /// <summary>Folds a bearing into [0, 360), with anything that would round to 360 at four places folded to 0 (the platform stores four).</summary>
        public static double Canonical(double degrees)
        {
            degrees %= 360.0;
            if (degrees < 0) degrees += 360.0;
            return degrees >= 359.9999 ? 0.0 : degrees;
        }
    }
}
