// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Model;

namespace DeviceChain.Towerpulse.Plane
{
    /// <summary>What each device reports now, in the scenario's metric keys, rounded the way a controller would round for the wire.</summary>
    public static class Readings
    {
        static double R(double v, int digits) { return Math.Round(v, digits, MidpointRounding.ToEven); }

        /// <summary>
        /// The device's reading, or null when it is not reporting: a chiller that has tripped sends no telemetry, which is what the absence rule sees.
        /// Its MQTT session stays up (so a restart can be delivered); "not reporting" is a different signal from the broker's presence and the device status.
        /// </summary>
        public static Dictionary<string, double> For(BuildingModel m, PlaneDevice d)
        {
            switch (d.Kind)
            {
                case PlaneKind.Vav:
                {
                    var z = m.Zones[m.Layout.ZoneIndex(d.LayoutId)];
                    return new Dictionary<string, double>
                    {
                        { Wire.Temp, R(z.TempC, 2) }, { Wire.Setpoint, R(z.SetpointC, 1) }, { Wire.Co2, R(z.Co2Ppm, 0) },
                        { Wire.Occupancy, z.Occupancy }, { Wire.Damper, R(z.DamperPct, 0) },
                    };
                }
                case PlaneKind.Ahu:
                {
                    var a = m.Ahus[m.Layout.AhuIndex(d.LayoutId)];
                    return new Dictionary<string, double>
                    {
                        { Wire.SupplyAir, R(a.SupplyAirC, 1) }, { Wire.Fan, R(a.FanPct, 0) }, { Wire.FilterDp, R(a.FilterDpPa, 0) }, { Wire.Mode, (int)a.Mode },
                    };
                }
                case PlaneKind.Chiller:
                {
                    var c = m.Chillers[m.Layout.ChillerIndex(d.LayoutId)];
                    if (!c.Online) return null;
                    return new Dictionary<string, double>
                    {
                        { Wire.ChwSupply, R(c.ChwSupplyC, 2) }, { Wire.Power, R(c.PowerKw, 1) }, { Wire.Cooling, R(c.CoolingKw, 1) }, { Wire.Running, c.Running ? 1 : 0 },
                    };
                }
                default:
                {
                    int i = -1;
                    for (int k = 0; k < m.Meters.Count; k++) if (m.Meters[k].Spec.Id == d.LayoutId) i = k;
                    var me = m.Meters[i];
                    return new Dictionary<string, double> { { Wire.MeterKw, R(me.Kw, 1) }, { Wire.MeterKwh, R(me.Kwh, 1) } };
                }
            }
        }

        /// <summary>True when the device is deliberately sending nothing (a tripped chiller).</summary>
        public static bool NotReporting(BuildingModel m, PlaneDevice d)
        {
            return d.Kind == PlaneKind.Chiller && !m.Chillers[m.Layout.ChillerIndex(d.LayoutId)].Online;
        }
    }
}
