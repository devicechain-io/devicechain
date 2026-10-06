// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// The bay's service: the Refuelling task state. It is the ONE place in the program that makes a
    /// <see cref="RefuelPermit"/>, and so the one place a tank can rise: a machine that is not in this
    /// state, wherever else it is and whatever else is wrong, gets no fuel back. Each tick it asks for the
    /// next small step of the tank's way to <see cref="TargetPct"/> over <see cref="DurationSeconds"/> of
    /// simulation time (so fuel climbs as the service goes, not all at the end), with a permit of its own
    /// for each step. The permit type has no constructor anyone can call: it is abstract, and the one class that
    /// derives from it is the private <see cref="Permit"/> below, so nothing else can make one. A guard test reads
    /// the source and fails if anything else names the permit or calls the model's refill.
    /// </summary>
    internal sealed class RefuelService
    {
        sealed class Permit : RefuelPermit
        {
            public Permit(string deviceId, double targetPct) : base(deviceId, targetPct) { }
        }

        public const double TargetPct = 95.0;
        public const double DurationSeconds = 40.0;

        readonly MachineModel model;
        readonly double ratePerSecond;

        public RefuelService(MachineModel model)
        {
            this.model = model ?? throw new ArgumentNullException(nameof(model));
            StartPct = model.FuelPct;
            ratePerSecond = Math.Max(0.0, TargetPct - StartPct) / DurationSeconds;
        }

        public double StartPct { get; }
        public double ElapsedSeconds { get; private set; }
        public bool Done => ElapsedSeconds >= DurationSeconds;
        public double Progress => Math.Min(1.0, ElapsedSeconds / DurationSeconds);

        public void Step(double dt)
        {
            if (!(dt > 0) || Done) return;
            ElapsedSeconds += dt;
            var target = Done ? TargetPct : Math.Min(TargetPct, model.FuelPct + ratePerSecond * dt);
            model.Refill(new Permit(model.DeviceId, target));
        }
    }
}
