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
        /// <summary>The largest machine's body (the haul truck's footprint from the fleet's own measurements, and its height with the cab), in metres, and the air a camera keeps from it.</summary>
        public const float BodyHalfLength = 5.7f, BodyHalfWidth = 2.85f, BodyHeight = 7.5f, BodyClearance = 1.0f;

        /// <summary>True when a camera <paramref name="side"/> metres beside, <paramref name="back"/> behind and <paramref name="up"/> above a machine's pose is inside its body or too close to see past.</summary>
        public static bool InsideBody(float side, float back, float up) =>
            Mathf.Abs(side) < BodyHalfWidth + BodyClearance && Mathf.Abs(back) < BodyHalfLength + BodyClearance && up < BodyHeight + BodyClearance;

        /// <summary>
        /// Why a camera that follows or orbits a machine would pass through it, or null when it never does (checked along the whole ease,
        /// start to end). Fixed cameras are placed by hand against the scene and are not checked here.
        /// </summary>
        public static string ClippingReason(CameraSpec c)
        {
            if (c.Rig == RigKind.Orbit && c.Target != null)
            {
                var reach = Mathf.Sqrt(BodyHalfLength * BodyHalfLength + BodyHalfWidth * BodyHalfWidth) + BodyClearance;
                if (c.Radius < reach && c.Height < BodyHeight + BodyClearance)
                    return $"the orbit camera at radius {c.Radius:0.#} m and height {c.Height:0.#} m runs through {c.Target} (its body reaches {reach:0.#} m from its pose)";
            }

            if (c.Rig == RigKind.Follow)
            {
                for (var i = 0; i <= 20; i++)
                {
                    var w = i / 20f;
                    w = w * w * (3f - 2f * w);
                    var back = Mathf.Lerp(c.Back, c.ToBack ?? c.Back, w);
                    var up = Mathf.Lerp(c.Up, c.ToUp ?? c.Up, w);
                    var side = Mathf.Lerp(c.Side, c.ToSide ?? c.Side, w);
                    if (InsideBody(side, back, up))
                        return $"the follow camera {Mathf.Abs(side):0.#} m beside, {Mathf.Abs(back):0.#} m {(back >= 0 ? "behind" : "ahead of")} and {up:0.#} m above {c.Target} is inside its body (a haul truck is {2 * BodyHalfWidth:0.##} m wide, {2 * BodyHalfLength:0.#} m long and {BodyHeight:0.#} m high)";
                }
            }

            return null;
        }

        /// <summary>A machine's body as the fleet's footprint measurements give it: half width, metres ahead of the pose and behind it, and its height with the cab or the boom.</summary>
        public static (float Half, float Front, float Rear, float Height) BodyOf(SimKind kind)
        {
            switch (kind)
            {
                case SimKind.Hauler: return (2.85f, 5.7f, 4.6f, 7.5f);
                case SimKind.Loader: return (1.55f, 5.2f, 3.6f, 5.5f);
                default: return (1.75f, 3.7f, 3.4f, 4.0f);
            }
        }

        /// <summary>
        /// Why the camera of a shot would, at some moment of it, be inside a machine (any machine, not only the one it follows: a truck driving
        /// into the lens is the same defect), or null when it never is. Samples the recorded poses every <paramref name="step"/> seconds of the shot.
        /// </summary>
        public static string IntrusionReason(CameraSpec c, SimBinReader sim, double start, double duration, double step = 0.1)
        {
            const float Air = 0.8f;
            var machines = sim.Machines;
            for (var t = 0.0; t <= duration + 1e-9; t += step)
            {
                var at = start + t;
                var pose = Evaluate(c, id => sim.TryIndexOf(id, out var i) ? sim.Sample(at, i) : default, t, duration);
                for (var m = 0; m < machines.Count; m++)
                {
                    var s = sim.Sample(at, m);
                    var body = BodyOf(machines[m].Kind);
                    var d = pose.Position - new Vector3(s.X, s.Y, s.Z);
                    var yaw = s.Heading * Mathf.Deg2Rad;
                    float forward = d.x * Mathf.Sin(yaw) + d.z * Mathf.Cos(yaw), right = d.x * Mathf.Cos(yaw) - d.z * Mathf.Sin(yaw);
                    if (Mathf.Abs(right) < body.Half + Air && forward < body.Front + Air && forward > -(body.Rear + Air) && d.y < body.Height + Air && d.y > -Air)
                        return $"the camera is inside {machines[m].Id} {t:0.0} s into the shot ({Mathf.Abs(right):0.0} m beside its pose, {forward:0.0} m ahead of it, {d.y:0.0} m above it)";
                }
            }

            return null;
        }

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
                    var w = duration > 0 ? (float)Math.Max(0.0, Math.Min(1.0, t / duration)) : 0f;
                    w = w * w * (3f - 2f * w);
                    var back = Mathf.Lerp(c.Back, c.ToBack ?? c.Back, w);
                    var up = Mathf.Lerp(c.Up, c.ToUp ?? c.Up, w);
                    var side = Mathf.Lerp(c.Side, c.ToSide ?? c.Side, w);
                    var look = Mathf.Lerp(c.LookHeight, c.ToLookHeight ?? c.LookHeight, w);
                    var fov = Mathf.Lerp(c.Fov, c.ToFov ?? c.Fov, w);
                    var pos = Where(s) + yaw * new Vector3(side, up, -back);
                    return new CameraPose(pos, Where(s) + yaw * new Vector3(c.LookSide, 0f, 0f) + Vector3.up * look, fov);
                }

                default:
                    throw new ArgumentOutOfRangeException(nameof(c));
            }
        }
    }
}
