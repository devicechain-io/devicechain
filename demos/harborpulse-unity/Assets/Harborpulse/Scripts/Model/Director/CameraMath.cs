// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>
    /// Pinhole-camera maths for a <see cref="CameraPose"/> (Unity's convention: a vertical field of view, +Y up, the camera's right
    /// is up x forward, no roll). Pure, so a sidecar test can place the horizon, project a device anchor into the frame and cast a
    /// frame-corner ray onto the water plane without an engine.
    /// </summary>
    public static class CameraMath
    {
        public readonly struct Basis
        {
            public Basis(Point3 forward, Point3 right, Point3 up) { Forward = forward; Right = right; Up = up; }
            public Point3 Forward { get; }
            public Point3 Right { get; }
            public Point3 Up { get; }
        }

        static double Dot(Point3 a, Point3 b) => a.X * b.X + a.Y * b.Y + a.Z * b.Z;

        public static Basis BasisOf(CameraPose pose)
        {
            var fx = pose.Look.X - pose.Camera.X; var fy = pose.Look.Y - pose.Camera.Y; var fz = pose.Look.Z - pose.Camera.Z;
            var n = Math.Sqrt(fx * fx + fy * fy + fz * fz);
            if (n < 1e-9) throw new ArgumentException("a pose must look somewhere");
            fx /= n; fy /= n; fz /= n;
            var rx = fz; var rz = -fx; var rn = Math.Sqrt(rx * rx + rz * rz);
            if (rn < 1e-9) throw new ArgumentException("a pose must not look straight up or down");
            rx /= rn; rz /= rn;
            var ux = fy * rz; var uy = fz * rx - fx * rz; var uz = -fy * rx;
            return new Basis(new Point3(fx, fy, fz), new Point3(rx, 0, rz), new Point3(ux, uy, uz));
        }

        public static double TanHalf(double verticalFovDegrees) => Math.Tan(verticalFovDegrees * Math.PI / 360);

        /// <summary>Elevation of the view axis above the horizontal, degrees (negative: looking down).</summary>
        public static double PitchDegrees(CameraPose pose)
        {
            var b = BasisOf(pose);
            return Math.Asin(Math.Max(-1, Math.Min(1, b.Forward.Y))) * 180 / Math.PI;
        }

        /// <summary>
        /// Where the flat-ground horizon falls in the frame, as a fraction of the height from the top (0 top, 1 bottom; outside 0..1 when
        /// off-screen). The eye is a few hundred metres up at most, so the horizon's dip is ignored.
        /// </summary>
        public static double HorizonFromTop(CameraPose pose)
        {
            var pitch = PitchDegrees(pose) * Math.PI / 180;
            return .5 + .5 * Math.Tan(pitch) / TanHalf(pose.VerticalFov);
        }

        /// <summary>Normalised device coordinates (x right, y up, both -1 .. 1 inside the frame) of a WORLD point, false when behind the camera.</summary>
        public static bool Project(CameraPose pose, double aspect, double x, double y, double z, out double nx, out double ny)
        {
            var b = BasisOf(pose);
            var v = new Point3(x - pose.Camera.X, y - pose.Camera.Y, z - pose.Camera.Z);
            var depth = Dot(v, b.Forward);
            nx = ny = double.NaN;
            if (depth <= .3) return false;
            var t = TanHalf(pose.VerticalFov);
            nx = Dot(v, b.Right) / (depth * t * aspect);
            ny = Dot(v, b.Up) / (depth * t);
            return true;
        }

        /// <summary>The point of the plane y = <paramref name="planeY"/> seen through a frame position, false when that ray never meets it.</summary>
        public static bool GroundHit(CameraPose pose, double aspect, double nx, double ny, double planeY, out double x, out double z)
        {
            var b = BasisOf(pose);
            var t = TanHalf(pose.VerticalFov);
            var dx = b.Forward.X + b.Right.X * nx * t * aspect + b.Up.X * ny * t;
            var dy = b.Forward.Y + b.Right.Y * nx * t * aspect + b.Up.Y * ny * t;
            var dz = b.Forward.Z + b.Right.Z * nx * t * aspect + b.Up.Z * ny * t;
            x = z = double.NaN;
            if (Math.Abs(dy) < 1e-9) return false;
            var s = (planeY - pose.Camera.Y) / dy;
            // a plane behind the camera is not seen through the frame
            if (s <= 0) return false;
            x = pose.Camera.X + dx * s; z = pose.Camera.Z + dz * s;
            return true;
        }
    }
}
