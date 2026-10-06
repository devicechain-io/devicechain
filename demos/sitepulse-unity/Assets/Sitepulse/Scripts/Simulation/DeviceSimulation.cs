// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Simulation
{
    /// <summary>Where a machine is and what state it is in, sampled from the scene on the main thread.</summary>
    public readonly struct MachinePose
    {
        public MachinePose(double east, double north, double unityY, double facingDegrees, bool loaded)
        {
            East = east;
            North = north;
            UnityY = unityY;
            FacingDegrees = facingDegrees;
            Loaded = loaded;
        }

        /// <summary>Site-local metres east of the origin (Unity X).</summary>
        public double East { get; }

        /// <summary>Site-local metres north of the origin (Unity Z).</summary>
        public double North { get; }

        /// <summary>Terrain height (Unity y).</summary>
        public double UnityY { get; }

        /// <summary>Degrees clockwise from north that the machine faces.</summary>
        public double FacingDegrees { get; }

        public bool Loaded { get; }
    }

    /// <summary>The scene, as the device plane sees it: a pose per device, or none.</summary>
    public interface IPoseSource
    {
        bool TryGet(string externalId, out MachinePose pose);
    }

    /// <summary>
    /// Everything one device does locally between the scene and its outbound ring: the physical model
    /// on a fixed step, the motion estimate, and the schedule of what to say. Unity-free.
    /// </summary>
    public sealed class DeviceSimulation
    {
        public const double FixedStepSeconds = 0.1;
        const int MaxStepsPerAdvance = 50;

        readonly MotionEstimator motion = new MotionEstimator();
        readonly TelemetryScheduler scheduler;
        double accumulator;

        public DeviceSimulation(EquipmentKind kind, string deviceId)
        {
            Model = new MachineModel(kind, deviceId);
            scheduler = new TelemetryScheduler(kind);
        }

        public MachineModel Model { get; }
        public MotionEstimator Motion => motion;
        public double NominalRatePerSecond => scheduler.NominalRatePerSecond;

        /// <summary>
        /// Advance by <paramref name="elapsedSeconds"/> of real time and add what is due, stamped
        /// <paramref name="now"/>, to <paramref name="output"/> (only when <paramref name="emit"/>,
        /// so a device whose link is not up yet still lives but does not queue).
        /// </summary>
        public void Advance(DateTimeOffset now, double elapsedSeconds, in MachinePose pose, bool emit, ICollection<Sample> output)
        {
            if (Model.IsPlant)
            {
                Step(elapsedSeconds, new MachineInput(0, false));
                if (emit) scheduler.Tick(now, Model, null, pose.East, pose.North, pose.UnityY, output);
                return;
            }

            motion.Update(pose.East, pose.North, elapsedSeconds, pose.FacingDegrees);
            Step(elapsedSeconds, new MachineInput(motion.SpeedMps, pose.Loaded));
            if (emit) scheduler.Tick(now, Model, motion, pose.East, pose.North, pose.UnityY, output);
        }

        void Step(double elapsedSeconds, MachineInput input)
        {
            accumulator += elapsedSeconds;
            var n = 0;
            while (accumulator >= FixedStepSeconds && n < MaxStepsPerAdvance)
            {
                Model.Step(FixedStepSeconds, input);
                accumulator -= FixedStepSeconds;
                n++;
            }

            if (accumulator >= FixedStepSeconds) accumulator = 0;
        }
    }
}
