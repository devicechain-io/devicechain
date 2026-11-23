// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    public sealed class HarborCard { public Vector3 At; public string Text; }
    /// <summary>Both IMGUI and stills print these exact evidence strings.</summary>
    public static class HarborCards
    {
        public static Color AlarmForeground => new Color(AlarmLabelPalette.ForegroundR, AlarmLabelPalette.ForegroundG, AlarmLabelPalette.ForegroundB, 1);
        public static Color AlarmBackground => new Color(AlarmLabelPalette.BackgroundR, AlarmLabelPalette.BackgroundG, AlarmLabelPalette.BackgroundB, 1);
        const int MaxCommandRows = 6;
        static string Number(double? n, string unit) => n.HasValue ? n.Value.ToString("0.00", CultureInfo.InvariantCulture) + unit : "not reported";
        static string Age(DateTimeOffset? at, HarborFrame f) => at.HasValue ? Math.Max(0, (int)(f.ObservedAt - at.Value).TotalSeconds) + " s ago" : "age not reported";
        static string Value(Reading? r, string unit, HarborFrame f) => r.HasValue ? Number(r.Value.Value, unit) + " · " + Age(r.Value.OccurredAt, f) : "no reading";
        public static string RolloutLine(HarborFrame f)
        {
            if (f?.Rollout == null) return f?.World.OtaNote == "S5 OTA not reported; no OTA cue" ? "no rollout declared by this runner" : "platform rollout not reported";
            var r = f.Rollout; var text = new StringBuilder(r.Label);
            foreach (var p in r.Progress) text.Append(" · ").Append(p.Key).Append(' ').Append(p.Value);
            text.Append(" · ").Append(Age(r.ObservedAt, f));
            if (r.TargetsTruncated) text.Append(" · target page truncated");
            return text.ToString();
        }
        public static string ReplacementLine(HarborFrame f)
        {
            var p = f?.Replacement;
            if (p == null) return "runner replacement not reported";
            // The instant belongs to the runner's scenario clock, not the host clock.
            var seconds = p.NewestAge.HasValue ? Math.Max(0, (int)p.NewestAge.Value.TotalSeconds) : 0;
            var age = p.NewestAge.HasValue ? seconds / 60 + " min " + seconds % 60 + " s ago" : "not reported";
            return "unit replaced " + (p.DeclaredSimSeconds.HasValue ? DeviceChain.Demos.Platform.DayClock.FormatHourMinute(p.DeclaredSimSeconds.Value) : "time not reported") + " (runner) · readings since: " + p.ObservedReadingsAfter + ", newest " + age + " (platform)";
        }
        public static string Device(LayoutDevice device, HarborFrame f)
        {
            var id = device.ExternalId; var s = new StringBuilder(id).Append("\nPLATFORM · ");
            if (f.Pumps.TryGetValue(id, out var pump))
            {
                s.Append("wet_well_m ").Append(Value(pump.WetWell, " m", f));
                s.Append("\nrunning ").Append(Value(pump.Running, "", f)).Append(" · flow ").Append(Value(pump.Flow, " l/s", f));
                s.Append("\ncurrent ").Append(Value(pump.Current, " A", f));
                s.Append("\nimpeller ").Append(Number(pump.ImpellerSpeed, "")).Append(" · visual mapping from flow");
                s.Append("\nstart: ").Append(pump.StartLabel);
                s.Append("\nwell_alarm_m ").Append(Value(pump.WellAlarmRule, " m", f));
                // The newest rows only, so a long history never pushes the readings and the start chip off the card.
                var rows = new List<CommandObservation>();
                foreach (var c in f.Commands) if (c.Device == id) rows.Add(c);
                if (rows.Count == 0) s.Append("\ncommands: none observed");
                var first = Math.Max(0, rows.Count - MaxCommandRows);
                if (first > 0) s.Append("\n").Append(first).Append(" earlier command rows not shown");
                for (var i = first; i < rows.Count; i++)
                { var c = rows[i]; s.Append("\n").Append(c.Name).Append(' ').Append(c.Status).Append(" · ").Append(c.RecordLabel).Append(" · ").Append(Age(c.PlatformAt ?? c.ObservedAt, f)); }
            }
            else if (f.Chargers.TryGetValue(id, out var charger))
            {
                if (!charger.Confirmed) s.Append("state unknown\n");
                s.Append("power ").Append(Value(charger.Power, " kW", f)).Append("\ncap ").Append(Value(charger.MaxKw, " kW", f));
                s.Append("\nconnector ").Append(Value(charger.ConnectorTemperature, " C", f)).Append("\nsession_active ").Append(Value(charger.SessionActive, "", f));
                s.Append("\nOTA ").Append(charger.OtaOutcome ?? "not reported").Append(" · attempt ").Append(charger.AttemptState ?? "not reported");
                s.Append(" · ").Append(Age(charger.OtaObservedAt, f));
            }
            else if (f.LinkedChargers.TryGetValue(id, out var linked))
            {
                s.Append(linked.Link).Append(" · power ").Append(Value(linked.Power, " kW", f));
                s.Append("\ntype ").Append(device.DeviceType).Append(device.Kind == DeviceKind.CertCharger ? " · its own MQTT session, under a client certificate" : " · its own MQTT session, under a device token");
            }
            else if (f.PullLamps.TryGetValue(id, out var pull))
            {
                s.Append(pull.Link).Append(" · dim ").Append(Value(pull.Dim, " %", f));
                s.Append("\ntype ").Append(device.DeviceType).Append(" · asks the platform for its commands");
                s.Append("\nlantern: lit with the dusk (scene), at the reported level");
            }
            else if (f.Lamps.TryGetValue(id, out var lamp))
            {
                s.Append(lamp.Condition).Append(" · dim ").Append(Number(lamp.DimPct, " %")).Append(" · ").Append(Age(lamp.LastAt, f));
                if (f.LampService?.RunnerPlan?.Device == device.Token || f.LampService?.RunnerPlan?.Device == id)
                {
                    s.Append("\n").Append(f.LampService.Note);
                    if (f.LampService.PlatformWindow != null) s.Append("\nwindow ").Append(f.LampService.PlatformWindow.Status).Append(" · observed ").Append(Age(f.LampService.PlatformWindow.ObservedAt, f));
                }
            }
            else if (device.Kind == DeviceKind.Truck)
            {
                var p = f.Truck; s.Append(p.Note).Append("\nspeed ").Append(Number(p.Speed, " m/s")).Append(" · heading ").Append(Number(p.Heading, " deg")).Append("\nfix ").Append(Age(p.FixAt, f));
            }
            else if (f.Measurements.TryGetValue(id, out var readings))
            {
                if (device.Kind == DeviceKind.Meter)
                    foreach (var value in readings) s.Append("\n").Append(value.Key).Append(' ').Append(Value(value.Value, value.Key == Metrics.MeterKw ? " kW" : " kWh", f));
                else s.Append(readings.TryGetValue(Metrics.WaterLevel, out var level) ? Value(level, " m", f) : "no reading");
                if (f.Replacement?.ExternalId == id) s.Append("\n").Append(ReplacementLine(f));
            }
            s.Append("\n").Append(f.AlarmCounts[id]).Append(f.AlarmCounts[id] == 1 ? " alarm" : " alarms");
            foreach (var a in f.Beacons) if (a.Device == id) s.Append("\n").Append(a.Key).Append(' ').Append(a.Cleared ? "CLEARED" : a.Severity).Append(a.Acknowledged ? " ACKNOWLEDGED" : "").Append(" · observed ").Append(Age(a.ObservedAt, f));
            return s.ToString();
        }
    }
}
