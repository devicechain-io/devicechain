// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Model;

namespace DeviceChain.Towerpulse.Plane
{
    /// <summary>
    /// The wire vocabulary of the platform's towerpulse scenario, restated here because the device must speak it exactly. The Go scenario
    /// (backend/sims/dc-simulator/sim/towerpulse.go) is authoritative; a test reads its constants from the repository and fails when these drift.
    /// </summary>
    public static class Wire
    {
        public const string Temp = "temp_c", Setpoint = "setpoint_c", Co2 = "co2_ppm", Occupancy = "occupancy", Damper = "damper_pct";
        public const string SupplyAir = "supply_air_c", Fan = "fan_pct", FilterDp = "filter_dp_pa", Mode = "mode";
        public const string ChwSupply = "chw_supply_c", Power = "power_kw", Cooling = "cooling_kw", Running = "running";
        public const string MeterKw = "kw", MeterKwh = "kwh";

        public const string BoostCooling = "boost-cooling", SetSetpoint = "set-setpoint", SetMode = "set-mode", Restart = "restart";
        public const string MinutesParam = "minutes", CelsiusParam = "celsius", ModeParam = "mode";

        public const string VavType = "tp-vav-zone", AhuType = "tp-ahu", ChillerType = "tp-chiller", MeterType = "tp-energy-meter";

        /// <summary>The profile's parameter bounds (the platform's command schemas), tighter than the model's own physical limits.</summary>
        public const double BoostMinutesMin = 1, BoostMinutesMax = 120, SetpointMin = 18, SetpointMax = 28;

        public static readonly string[] VavMetrics = { Temp, Setpoint, Co2, Occupancy, Damper };
        public static readonly string[] AhuMetrics = { SupplyAir, Fan, FilterDp, Mode };
        /// <summary>Reported by a chiller. The platform computes cop itself, so it is never sent.</summary>
        public static readonly string[] ChillerNumericMetrics = { ChwSupply, Power, Cooling };
        public static readonly string[] ChillerBooleanMetrics = { Running };
        public static readonly string[] MeterMetrics = { MeterKw, MeterKwh };
    }

    public enum PlaneKind { Vav, Ahu, Chiller, Meter }

    /// <summary>One of the 31 devices: the model's id for it, and the platform's token and external id.</summary>
    public sealed class PlaneDevice
    {
        public PlaneDevice(string layoutId, PlaneKind kind, string token, string externalId)
        {
            LayoutId = layoutId; Kind = kind; Token = token; ExternalId = externalId;
        }

        /// <summary>The id the Model and the art use (BuildingLayout).</summary>
        public string LayoutId { get; }
        public PlaneKind Kind { get; }
        /// <summary>The platform device token the scenario provisions.</summary>
        public string Token { get; }
        /// <summary>What the binder resolves a device by.</summary>
        public string ExternalId { get; }

        public string TypeToken
        {
            get
            {
                switch (Kind)
                {
                    case PlaneKind.Vav: return Wire.VavType;
                    case PlaneKind.Ahu: return Wire.AhuType;
                    case PlaneKind.Chiller: return Wire.ChillerType;
                    default: return Wire.MeterType;
                }
            }
        }

        public override string ToString() { return ExternalId + " (" + LayoutId + ")"; }
    }

    /// <summary>
    /// Layout ids to the scenario's device tokens. The scenario names the annex rooms by compass (north, south) where the model's art names them Lab A and
    /// Lab B; the scenario's names win on the wire. The scenario's air handlers are tower 1 and 2 and annex 1 (it does not group zones by floor, only by
    /// building); the model's are low, high and annex, in the same order.
    /// </summary>
    public static class DeviceMap
    {
        public static IReadOnlyList<PlaneDevice> All(BuildingLayout layout)
        {
            var list = new List<PlaneDevice>();
            foreach (var z in layout.Zones)
            {
                bool tower = z.Building == "tower";
                string dir;
                if (tower) dir = z.Id.Substring(z.Id.LastIndexOf('-') + 1);
                else dir = z.Id.EndsWith("laba", StringComparison.Ordinal) ? "north" : "south";
                string b = tower ? "tower" : "annex";
                list.Add(new PlaneDevice(z.Id, PlaneKind.Vav, "tp-vav-" + b + "-f" + z.Floor + "-" + dir,
                    "TP-VAV-" + (tower ? "T" : "A") + "-F" + z.Floor + "-" + char.ToUpperInvariant(dir[0])));
            }
            string[] ahuTokens = { "tp-ahu-tower-1", "tp-ahu-tower-2", "tp-ahu-annex-1" };
            string[] ahuExt = { "TP-AHU-T-1", "TP-AHU-T-2", "TP-AHU-A-1" };
            for (int i = 0; i < layout.Ahus.Count; i++) list.Add(new PlaneDevice(layout.Ahus[i].Id, PlaneKind.Ahu, ahuTokens[i], ahuExt[i]));
            string[] chTokens = { "tp-chiller-tower", "tp-chiller-annex" };
            string[] chExt = { "TP-CH-T", "TP-CH-A" };
            for (int i = 0; i < layout.Chillers.Count; i++) list.Add(new PlaneDevice(layout.Chillers[i].Id, PlaneKind.Chiller, chTokens[i], chExt[i]));
            string[] mTokens = { "tp-meter-tower", "tp-meter-annex" };
            string[] mExt = { "TP-EM-T", "TP-EM-A" };
            for (int i = 0; i < layout.Meters.Count; i++) list.Add(new PlaneDevice(layout.Meters[i].Id, PlaneKind.Meter, mTokens[i], mExt[i]));
            return list;
        }
    }
}
