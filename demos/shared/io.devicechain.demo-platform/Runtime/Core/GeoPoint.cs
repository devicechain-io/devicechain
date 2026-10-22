// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

namespace DeviceChain.Demos.Core
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
}
