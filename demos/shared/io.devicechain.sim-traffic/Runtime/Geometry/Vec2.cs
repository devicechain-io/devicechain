// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;

namespace DeviceChain.Sim.Traffic.Geometry
{
    /// <summary>
    /// A point or direction on the ground plane, in metres: X east, Z north, as seen from above (Unity's X/Z with Y up).
    /// Headings are degrees clockwise from north (+Z), so a heading h points along (sin h, cos h).
    /// </summary>
    /// <remarks>
    /// Equality is component-wise and exact, implemented here rather than inherited: the default
    /// <see cref="ValueType.Equals(object)"/> compares double fields through reflection. 0 and -0 are equal and
    /// NaN equals NaN (as <see cref="double.Equals(double)"/> has it), and <see cref="GetHashCode"/> agrees with
    /// that.
    /// </remarks>
    public readonly struct Vec2 : IEquatable<Vec2>
    {
        public readonly double X;
        public readonly double Z;

        public Vec2(double x, double z)
        {
            X = x;
            Z = z;
        }

        public static Vec2 Zero => default;

        public double Length => Math.Sqrt(X * X + Z * Z);

        public static Vec2 operator +(Vec2 a, Vec2 b) => new Vec2(a.X + b.X, a.Z + b.Z);
        public static Vec2 operator -(Vec2 a, Vec2 b) => new Vec2(a.X - b.X, a.Z - b.Z);
        public static Vec2 operator -(Vec2 a) => new Vec2(-a.X, -a.Z);
        public static Vec2 operator *(Vec2 a, double k) => new Vec2(a.X * k, a.Z * k);
        public static Vec2 operator *(double k, Vec2 a) => new Vec2(a.X * k, a.Z * k);

        public static double Dot(Vec2 a, Vec2 b) => a.X * b.X + a.Z * b.Z;

        /// <summary>X1·Z2 − Z1·X2: positive when <paramref name="b"/> lies counter-clockwise of <paramref name="a"/> seen from above.</summary>
        public static double Cross(Vec2 a, Vec2 b) => a.X * b.Z - a.Z * b.X;

        /// <summary>
        /// This vector turned CLOCKWISE by <paramref name="degrees"/> seen from above, the same sense as a heading: north
        /// (0, 1) rotated by h is the heading-h direction (sin h, cos h). Negative degrees turn counter-clockwise.
        /// </summary>
        public Vec2 Rotate(double degrees)
        {
            var r = degrees * Math.PI / 180.0;
            double s = Math.Sin(r), c = Math.Cos(r);
            return new Vec2(X * c + Z * s, Z * c - X * s);
        }

        public bool Equals(Vec2 other) => X.Equals(other.X) && Z.Equals(other.Z);

        public override bool Equals(object obj) => obj is Vec2 other && Equals(other);

        // double.GetHashCode already hashes 0 and -0 alike, and every NaN alike, on both runtimes this compiles for
        // (.NET and Unity's class library), matching double.Equals. EqualVectorsHashAlikeAtSignedZeroAndNaN holds
        // that in both test runs, so a runtime where it stops being true fails a test rather than a lookup.
        public override int GetHashCode() => unchecked((X.GetHashCode() * 397) ^ Z.GetHashCode());

        public override string ToString() =>
            "(" + X.ToString("R", CultureInfo.InvariantCulture) + ", " + Z.ToString("R", CultureInfo.InvariantCulture) + ")";
    }
}
