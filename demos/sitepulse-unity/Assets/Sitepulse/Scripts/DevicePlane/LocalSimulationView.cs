// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>
    /// The local simulation as text, for the debug panel (key L): what each machine's model holds
    /// before anything is published, how old its last sample is, when the broker last acknowledged one,
    /// and the send errors and dropped samples. It is the device side of the demo, not platform data,
    /// and says so on every line of the panel; nothing here ever reaches a card.
    /// </summary>
    public static class LocalSimulationView
    {
        public const string Title = "Local simulation (not platform data)";

        public static string Text(IReadOnlyList<DeviceSessionHost> hosts, DateTimeOffset now)
        {
            var sb = new StringBuilder(Title).Append('\n');
            foreach (var h in hosts) sb.Append('\n').Append(Line(h, now));
            if (hosts.Count == 0) sb.Append("\nno device has a session");
            return sb.ToString();
        }

        public static string Line(DeviceSessionHost h, DateTimeOffset now)
        {
            var m = h.Simulation.Model;
            var sb = new StringBuilder(h.ExternalId).Append("  ");
            if (m.IsPlant)
                sb.Append("tph ").Append(N(m.ThroughputTph, 0)).Append(" run ").Append(m.Running ? "yes" : "no");
            else
            {
                sb.Append("fuel ").Append(N(m.FuelPct, 1)).Append(" temp ").Append(N(m.EngineTempC, 1)).Append(" hrs ").Append(N(m.EngineHours, 1));
                if (m.HasPayload) sb.Append(" pay ").Append(N(m.PayloadT, 1));
                if (m.HasTyres) sb.Append(" tyre ").Append(N(m.TyrePressureKpa, 0));
            }

            sb.Append("  | sample ").Append(Age(h.LastMeasurementSampleUtc, now))
              .Append("  ack ").Append(Age(h.LastPublishUtc, now))
              .Append("  err ").Append(h.SendErrors.ToString(CultureInfo.InvariantCulture))
              .Append("  drop ").Append(h.Ring.Dropped.ToString(CultureInfo.InvariantCulture));
            return sb.ToString();
        }

        static string N(double v, int decimals) => v.ToString("F" + decimals, CultureInfo.InvariantCulture);

        /// <summary>"0.4 s", or "none" before the first.</summary>
        public static string Age(DateTimeOffset? at, DateTimeOffset now) =>
            at.HasValue ? Math.Max(0.0, (now - at.Value).TotalSeconds).ToString("F1", CultureInfo.InvariantCulture) + " s" : "none";
    }
}
