// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Simulation
{
    public enum StateSource { Seed, Platform }

    /// <summary>What a machine starts its fuel and engine hours from, and where each came from.</summary>
    public readonly struct StartingState
    {
        public StartingState(double fuelPct, StateSource fuelSource, double engineHours, StateSource engineHoursSource)
        {
            FuelPct = fuelPct;
            FuelSource = fuelSource;
            EngineHours = engineHours;
            EngineHoursSource = engineHoursSource;
        }

        public double FuelPct { get; }
        public StateSource FuelSource { get; }
        public double EngineHours { get; }
        public StateSource EngineHoursSource { get; }
    }

    /// <summary>
    /// Where a machine resumes from. The platform's last observed value wins, so a relaunch does not
    /// move a machine's fuel (a tank that rose on every relaunch would look like a refuel nobody did);
    /// the deterministic seed is the fallback for a value the platform does not have. A platform value
    /// is used as it is, even when it is below the seed's range: a low tank is what the platform saw.
    /// </summary>
    public static class LastState
    {
        public static StartingState Choose(double seedFuelPct, double seedEngineHours, IReadOnlyDictionary<string, double> platform)
        {
            var fuel = seedFuelPct;
            var fuelSource = StateSource.Seed;
            if (Usable(platform, MeasurementKeys.FuelPct, out var f) && f >= 0.0 && f <= 100.0)
            {
                fuel = f;
                fuelSource = StateSource.Platform;
            }

            var hours = seedEngineHours;
            var hoursSource = StateSource.Seed;
            if (Usable(platform, MeasurementKeys.EngineHours, out var h) && h >= 0.0)
            {
                hours = h;
                hoursSource = StateSource.Platform;
            }

            return new StartingState(fuel, fuelSource, hours, hoursSource);
        }

        static bool Usable(IReadOnlyDictionary<string, double> platform, string key, out double value)
        {
            value = 0;
            return platform != null && platform.TryGetValue(key, out value) && !double.IsNaN(value) && !double.IsInfinity(value);
        }
    }
}
