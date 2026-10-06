// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Runtime.CompilerServices;
using DeviceChain.Sitepulse.Domain;

[assembly: InternalsVisibleTo("DeviceChain.Sitepulse.Tests.EditMode")]

namespace DeviceChain.Sitepulse.Simulation
{
    public enum EquipmentKind { Hauler, Loader, Dozer, Plant }

    /// <summary>What the scene tells the model about a machine at one step. Plain numbers, so the model needs no Unity.</summary>
    public readonly struct MachineInput
    {
        public MachineInput(double speedMps, bool loaded, bool running = true)
        {
            SpeedMps = speedMps < 0 ? 0 : speedMps;
            Loaded = loaded;
            Running = running;
        }

        /// <summary>Smoothed ground speed, m/s.</summary>
        public double SpeedMps { get; }

        /// <summary>A hauler's load is on, or a loader's bucket is racked back with a load in it.</summary>
        public bool Loaded { get; }

        /// <summary>The plant's running state (ignored by machines).</summary>
        public bool Running { get; }
    }

    /// <summary>
    /// Permission to put fuel into a tank. The ONLY way fuel rises in <see cref="MachineModel"/> is
    /// <see cref="MachineModel.Refill"/>, and that takes one of these. The constructor is not public,
    /// so nothing outside this assembly can make one, and inside it nothing does in this slice: the
    /// <c>Refuelling</c> task state (slice A5) is the one place that will. That is what makes the
    /// phase B negative control mean something: a machine that is not refuelling cannot get fuel back,
    /// whatever else is wrong, and a rule that fires on a falling tank has nothing to hide behind.
    /// </summary>
    public sealed class RefuelPermit
    {
        internal RefuelPermit(string deviceId, double targetPct)
        {
            DeviceId = deviceId ?? throw new ArgumentNullException(nameof(deviceId));
            TargetPct = targetPct;
        }

        public string DeviceId { get; }
        public double TargetPct { get; }
        public bool Used { get; internal set; }
    }

    /// <summary>A small deterministic generator, so a machine's seeded values are the same on every run and every runtime.</summary>
    public static class StableRandom
    {
        /// <summary>FNV-1a over the id, then one SplitMix64 round.</summary>
        public static ulong Seed(string text)
        {
            ulong h = 14695981039346656037UL;
            foreach (var c in text)
            {
                h ^= c;
                h *= 1099511628211UL;
            }

            return h;
        }

        /// <summary>A value in [0, 1) that depends only on the seed and the stream number.</summary>
        public static double Unit(ulong seed, ulong stream)
        {
            ulong z = seed + 0x9E3779B97F4A7C15UL * (stream + 1);
            z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9UL;
            z = (z ^ (z >> 27)) * 0x94D049BB133111EBUL;
            z ^= z >> 31;
            return (z >> 11) * (1.0 / 9007199254740992.0);
        }
    }

    /// <summary>
    /// The local physical model of one device: what it would report. Unity-free, driven by a fixed
    /// step in simulation time (a real-time clock in this slice). Every starting value is seeded from
    /// the device's id, so a run is repeatable.
    ///
    /// Normal operation stays well inside the platform's three rules (sitepulse.go): fuel falls from
    /// 35 to 95 percent at demo-scaled rates (a haul truck really burns several percent an hour; here
    /// no machine loses more than 1.9 points an hour, so an eight-hour shift from the lowest start
    /// ends above 20, clear of the 15 percent rule), engine temperature settles at 85 to 97 degrees
    /// (rule: above 105), tyre pressure stays within 690 to 720 kPa (rule: below 600). The demo
    /// reaches an alarm by a command, never by the model drifting there.
    /// </summary>
    public sealed class MachineModel
    {
        const double MaxMovingSpeed = 0.3;

        readonly ulong seed;
        readonly double tyreBase, tempOffset, fuelRateScale, phase;
        double fuel, engineTemp, engineHours, tyre, payload, throughput, simTime;
        bool wasLoaded;
        ulong loads;

        public MachineModel(EquipmentKind kind, string deviceId)
        {
            Kind = kind;
            DeviceId = deviceId ?? throw new ArgumentNullException(nameof(deviceId));
            seed = StableRandom.Seed(deviceId);
            fuel = 35.0 + 60.0 * StableRandom.Unit(seed, 1);
            engineTemp = 80.0 + 8.0 * StableRandom.Unit(seed, 2);
            engineHours = 4000.0 + 12000.0 * StableRandom.Unit(seed, 3);
            tyreBase = 697.0 + 13.0 * StableRandom.Unit(seed, 4);
            tempOffset = -1.5 + 3.0 * StableRandom.Unit(seed, 5);
            fuelRateScale = 0.9 + 0.2 * StableRandom.Unit(seed, 6);
            phase = 2.0 * Math.PI * StableRandom.Unit(seed, 7);
            tyre = tyreBase;
            throughput = 0;
            Running = kind == EquipmentKind.Plant;
            if (kind == EquipmentKind.Plant) throughput = PlantBase();
        }

        public EquipmentKind Kind { get; }
        public string DeviceId { get; }

        public bool IsPlant => Kind == EquipmentKind.Plant;

        /// <summary>A tracked dozer measures neither tyres nor payload, and a plant has no engine.</summary>
        public bool HasTyres => Kind == EquipmentKind.Hauler || Kind == EquipmentKind.Loader;
        public bool HasPayload => HasTyres;

        public double FuelPct => fuel;
        public double EngineTempC => engineTemp;
        public double EngineHours => engineHours;
        public double TyrePressureKpa => tyre;
        public double PayloadT => payload;
        public double ThroughputTph => throughput;
        public bool Running { get; private set; }

        double PlantBase() => 780.0 + 80.0 * StableRandom.Unit(seed, 8);

        /// <summary>Advance by <paramref name="dt"/> seconds of simulation time.</summary>
        public void Step(double dt, MachineInput input)
        {
            if (!(dt > 0)) return;
            simTime += dt;
            if (IsPlant)
            {
                Running = input.Running;
                var target = Running ? PlantBase() + 25.0 * Math.Sin(2.0 * Math.PI * simTime / 150.0 + phase) : 0.0;
                throughput += (target - throughput) * (1.0 - Math.Exp(-dt / 8.0));
                if (!Running && throughput < 0.05) throughput = 0;
                return;
            }

            var moving = input.SpeedMps > MaxMovingSpeed;
            var load = LoadFactor(moving, input.Loaded);

            // engine: first-order lag toward a load-dependent setpoint
            var setpoint = 85.0 + 10.0 * load + tempOffset + 0.4 * Math.Sin(2.0 * Math.PI * simTime / 90.0 + phase);
            engineTemp += (setpoint - engineTemp) * (1.0 - Math.Exp(-dt / 120.0));

            // fuel only ever falls here; Refill is the one way up
            var perHour = FuelPeakPerHour() * (0.3 + 0.7 * load) * fuelRateScale;
            var next = fuel - perHour * dt / 3600.0;
            fuel = next < 0 ? 0 : next < fuel ? next : fuel;

            engineHours += dt / 3600.0;

            if (HasTyres)
            {
                tyre = tyreBase + 0.3 * (engineTemp - 85.0) + 0.8 * Math.Sin(2.0 * Math.PI * simTime / 240.0 + phase);

                if (input.Loaded && !wasLoaded)
                {
                    loads++;
                    payload = Kind == EquipmentKind.Hauler
                        ? 86.0 + 8.0 * StableRandom.Unit(seed, 1000 + loads)      // a truck's load: 86 to 94 t
                        : 11.0 + 4.0 * StableRandom.Unit(seed, 1000 + loads);     // a loader's bucket: 11 to 15 t
                }
                else if (!input.Loaded)
                {
                    payload = 0;
                }

                wasLoaded = input.Loaded;
            }
        }

        double FuelPeakPerHour() => Kind == EquipmentKind.Hauler ? 1.8 : Kind == EquipmentKind.Loader ? 1.7 : 1.7;

        double LoadFactor(bool moving, bool loaded)
        {
            switch (Kind)
            {
                case EquipmentKind.Hauler: return loaded ? 0.9 : moving ? 0.5 : 0.15;
                case EquipmentKind.Loader: return loaded ? 0.8 : moving ? 0.5 : 0.3;
                default: return moving ? 0.7 : 0.35;
            }
        }

        /// <summary>
        /// Raises the tank to the permit's level (never lowers it). The permit is single use and names
        /// this device; nothing else in the model can raise fuel.
        /// </summary>
        public void Refill(RefuelPermit permit)
        {
            if (permit == null) throw new ArgumentNullException(nameof(permit));
            if (IsPlant) throw new InvalidOperationException("a plant has no fuel tank");
            if (permit.Used) throw new InvalidOperationException("this refuel permit has already been used");
            if (!string.Equals(permit.DeviceId, DeviceId, StringComparison.Ordinal))
                throw new InvalidOperationException($"the refuel permit is for {permit.DeviceId}, not {DeviceId}");
            permit.Used = true;
            var target = permit.TargetPct > 100.0 ? 100.0 : permit.TargetPct;
            if (target > fuel) fuel = target;
        }

        /// <summary>The metrics this device reports, by key, rounded for the wire. A dozer has no tyre or payload; the plant has throughput and running only.</summary>
        public Dictionary<string, double> Measurements()
        {
            var m = new Dictionary<string, double>();
            if (IsPlant)
            {
                m[MeasurementKeys.ThroughputTph] = Math.Round(throughput, 1);
                m[MeasurementKeys.PlantRunning] = Running ? 1.0 : 0.0;
                return m;
            }

            m[MeasurementKeys.FuelPct] = Math.Round(fuel, 2);
            m[MeasurementKeys.EngineTempC] = Math.Round(engineTemp, 1);
            m[MeasurementKeys.EngineHours] = Math.Round(engineHours, 3);
            if (HasPayload) m[MeasurementKeys.PayloadT] = Math.Round(payload, 1);
            if (HasTyres) m[MeasurementKeys.TyrePressureKpa] = Math.Round(tyre, 1);
            return m;
        }
    }
}
