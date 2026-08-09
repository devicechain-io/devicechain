// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Sdk.Ingest;

/// <summary>
/// One position report, in the units the platform fixes for every device: WGS84 (EPSG:4326)
/// decimal degrees, elevation above the <b>ellipsoid</b> in metres, accuracy in metres, speed in
/// metres per second, heading in degrees clockwise from true north. None of them is configurable
/// per device, so a receiver reporting height above mean sea level must convert before sending.
/// </summary>
/// <remarks>
/// <para>
/// <see cref="Latitude"/> and <see cref="Longitude"/> are constructor arguments because the
/// platform requires them. The other four are nullable and default to <c>null</c> — <b>send what
/// the receiver actually knows rather than a placeholder</b>. A zero written where nothing was
/// measured is stored, drawn by widgets, and can never be unpicked afterwards, because a stored
/// zero is indistinguishable from a measured one.
/// </para>
/// <para>
/// A struct rather than a class so a Unity update loop emitting per-frame fixes allocates nothing.
/// The default value is the valid position (0, 0) in the Gulf of Guinea, not an "empty" fix — a
/// caller with nothing to report should not emit at all.
/// </para>
/// </remarks>
public readonly struct LocationFix : IEquatable<LocationFix>
{
    /// <summary>Creates a fix at the given WGS84 coordinate. Optionals are set with object-initializer syntax.</summary>
    /// <param name="latitude">Decimal degrees, ±90.</param>
    /// <param name="longitude">Decimal degrees, ±180.</param>
    public LocationFix(double latitude, double longitude)
    {
        Latitude = latitude;
        Longitude = longitude;
        Elevation = null;
        Accuracy = null;
        Speed = null;
        Heading = null;
    }

    /// <summary>WGS84 latitude in decimal degrees (±90). Required.</summary>
    public double Latitude { get; }

    /// <summary>WGS84 longitude in decimal degrees (±180). Required.</summary>
    public double Longitude { get; }

    /// <summary>Metres above the WGS84 <b>ellipsoid</b> — not above mean sea level. Null when unreported.</summary>
    public double? Elevation { get; init; }

    /// <summary>Horizontal accuracy in metres (0 or greater). Null when unreported.</summary>
    public double? Accuracy { get; init; }

    /// <summary>Ground speed in metres per second (0 or greater). Null when unreported.</summary>
    public double? Speed { get; init; }

    /// <summary>Bearing in degrees clockwise from true north, in <c>[0, 360)</c>. Null when unreported.</summary>
    public double? Heading { get; init; }

    /// <summary>
    /// Folds a raw bearing in degrees into the <c>[0, 360)</c> the platform stores. Pass a computed
    /// bearing (an <c>atan2</c> result, a transform's yaw) through this before putting it on a fix.
    /// </summary>
    /// <remarks>
    /// The wrap at the top of the range is not a clamp and is the part that is easy to get wrong:
    /// 360 and 0 are the SAME bearing, so folding one onto the other loses nothing, and storing
    /// both spellings of north is how a consumer ends up with two headings that compare unequal.
    /// The fold has to happen a rounding quantum INSIDE 360 rather than at it, because the platform
    /// stores four decimal places and ROUNDS to get there — 359.99999 is a legal double below 360
    /// and would be stored as exactly 360.0000, the value the exclusive bound exists to refuse.
    /// A producer handing over a raw bearing therefore trips the range check eventually, and only
    /// occasionally, which is the worst way to find out.
    /// </remarks>
    /// <param name="degrees">Any bearing, including negative or over-360 values.</param>
    /// <returns>The same bearing in <c>[0, 360)</c>, with anything that would round to 360 folded to 0.</returns>
    public static double CanonicalHeading(double degrees)
    {
        degrees %= 360;
        if (degrees < 0)
        {
            degrees += 360;
        }

        // Comfortably inside the platform's 360 - quantum/2, so rounding at the column cannot
        // carry an admitted value up to 360.0000.
        return degrees >= 359.9999 ? 0 : degrees;
    }

    /// <inheritdoc/>
    public bool Equals(LocationFix other) =>
        Latitude.Equals(other.Latitude) && Longitude.Equals(other.Longitude) &&
        Nullable.Equals(Elevation, other.Elevation) && Nullable.Equals(Accuracy, other.Accuracy) &&
        Nullable.Equals(Speed, other.Speed) && Nullable.Equals(Heading, other.Heading);

    /// <inheritdoc/>
    public override bool Equals(object? obj) => obj is LocationFix other && Equals(other);

    /// <inheritdoc/>
    public override int GetHashCode()
    {
        unchecked
        {
            int hash = Latitude.GetHashCode();
            hash = (hash * 397) ^ Longitude.GetHashCode();
            hash = (hash * 397) ^ Elevation.GetHashCode();
            hash = (hash * 397) ^ Accuracy.GetHashCode();
            hash = (hash * 397) ^ Speed.GetHashCode();
            hash = (hash * 397) ^ Heading.GetHashCode();
            return hash;
        }
    }

    /// <summary>Equality operator.</summary>
    public static bool operator ==(LocationFix left, LocationFix right) => left.Equals(right);

    /// <summary>Inequality operator.</summary>
    public static bool operator !=(LocationFix left, LocationFix right) => !left.Equals(right);
}
