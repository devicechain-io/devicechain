// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Recording;
using UnityEngine;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>Where a camera is, what it looks at and how wide it sees.</summary>
    public readonly struct CameraPose
    {
        public CameraPose(Vector3 position, Vector3 lookAt, float fov)
        {
            Position = position;
            LookAt = lookAt;
            Fov = fov;
        }

        public Vector3 Position { get; }
        public Vector3 LookAt { get; }
        public float Fov { get; }
    }

    /// <summary>The camera rigs: a pose from a spec, the recorded machine poses and the time into the shot. No clock, no state.</summary>
    public static class CameraRigs
    {
        static Vector3 V(float[] a) => new Vector3(a[0], a[1], a[2]);

        static Vector3 Where(MachineSample s) => new Vector3(s.X, s.Y, s.Z);

        /// <param name="poseOf">The recorded machine's sample at the shot's current moment.</param>
        /// <param name="t">Seconds into the shot.</param>
        /// <param name="duration">The shot's length (a crane eases over it).</param>
        public static CameraPose Evaluate(CameraSpec c, Func<string, MachineSample> poseOf, double t, double duration)
        {
            switch (c.Rig)
            {
                case RigKind.Fixed:
                {
                    var w = duration > 0 ? (float)Math.Max(0.0, Math.Min(1.0, t / duration)) : 0f;
                    w = w * w * (3f - 2f * w);   // ease in and out
                    var pos = V(c.Pos);
                    var look = c.LookAtTarget != null ? Where(poseOf(c.LookAtTarget)) + Vector3.up * c.LookHeight : V(c.LookAt);
                    var fov = c.Fov;
                    if (c.ToPos != null) pos = Vector3.Lerp(pos, V(c.ToPos), w);
                    if (c.ToLookAt != null) look = Vector3.Lerp(look, V(c.ToLookAt), w);
                    if (c.ToFov.HasValue) fov = Mathf.Lerp(fov, c.ToFov.Value, w);
                    return new CameraPose(pos, look, fov);
                }

                case RigKind.Orbit:
                {
                    var centre = c.Target != null ? Where(poseOf(c.Target)) : V(c.Point);
                    var a = (c.StartDegrees + c.DegreesPerSecond * (float)t) * Mathf.Deg2Rad;
                    var pos = centre + new Vector3(Mathf.Sin(a) * c.Radius, c.Height, Mathf.Cos(a) * c.Radius);
                    return new CameraPose(pos, centre + Vector3.up * c.LookHeight, c.Fov);
                }

                case RigKind.Follow:
                {
                    var s = poseOf(c.Target);
                    var yaw = Quaternion.Euler(0f, s.Heading, 0f);
                    var pos = Where(s) + yaw * new Vector3(c.Side, c.Up, -c.Back);
                    return new CameraPose(pos, Where(s) + Vector3.up * c.LookHeight, c.Fov);
                }

                default:
                    throw new ArgumentOutOfRangeException(nameof(c));
            }
        }
    }
}
