// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;
using DeviceChain.Demos.Platform;

namespace DeviceChain.Harborpulse.Model
{
    public sealed partial class PreviewSource
    {
        public ScenarioStatus Status { get; private set; }
        public IReadOnlyList<HarborAlarm> Alarms { get; private set; } = Array.Empty<HarborAlarm>();
        public IReadOnlyList<HarborCommand> Commands { get; private set; } = Array.Empty<HarborCommand>();
        readonly Dictionary<string, HarborCommand> commandRecords = new Dictionary<string, HarborCommand>();
        public CommandCoverage CommandPoll => new CommandCoverage(now.AddDays(-1), now);
        public AlarmCoverage AlarmObservation => new AlarmCoverage(true, true);
        public RolloutEvidence Rollout { get; private set; }
        public IReadOnlyList<SuppressionEvidence> Suppressions { get; private set; } = Array.Empty<SuppressionEvidence>();
        public string ReadProblem => "OFFLINE PREVIEW: stand-in values, not platform data";
        public bool TryToken(string externalId, out string token) { token = layout.Find(externalId ?? "")?.Token; return token != null; }
        DateTimeOffset Wall(double simHour) => now.AddSeconds((simHour * 3600 - simSeconds) / 86400 * layout.DayLengthSeconds);
        static string Stamp(DateTimeOffset time) => JsonSerializerFreeString(time.ToString("O", CultureInfo.InvariantCulture));
        static string JsonSerializerFreeString(string value) => "\"" + value.Replace("\\", "\\\\").Replace("\"", "\\\"") + "\"";
        string DeviceId(string token) { foreach (var d in layout.Devices) if (d.Token == token) return d.ExternalId; return null; }

        void BuildPreviewEvidence()
        {
            var h = DayClock.Hours(simSeconds); var day = DayClock.DayIndex(simSeconds); var dayHours = day * 24;
            string N(double n) => n.ToString("R", CultureInfo.InvariantCulture);
            var beats = new List<string>();
            foreach (var b in layout.Beats) beats.Add("{\"name\":" + JsonSerializerFreeString(b.Name) + ",\"from\":" + N(b.From) + ",\"to\":" + N(b.To) + "}");
            var lamp = "hp-light-east-3";
            Status = ScenarioStatus.Parse("{\"state\":\"RUNNING\",\"scenario\":{\"name\":\"harborpulse\",\"started\":true,\"startedAt\":" + Stamp(Wall(layout.StartHour)) +
                ",\"dayLengthSeconds\":" + N(layout.DayLengthSeconds) + ",\"startHour\":" + N(layout.StartHour) + ",\"simHour\":" + N(h) + ",\"day\":" + day + ",\"beats\":[" + string.Join(",", beats) +
                "],\"jobs\":[{\"day\":" + day + ",\"name\":\"lamp-service\",\"campaign\":\"\",\"group\":\"\",\"device\":\"" + lamp + "\",\"window\":\"preview-lamp-window\",\"fireAt\":\"\",\"windowOpens\":" + Stamp(Wall(dayHours + 10.75)) + ",\"windowCloses\":" + Stamp(Wall(dayHours + 11.5)) +
                "}],\"s5\":{\"ota\":{\"artifact\":\"preview-artifact\",\"version\":\"2.0-preview\",\"rollout\":\"preview-rollout\",\"group\":\"preview-chargers\",\"maxInFlight\":2,\"failureThreshold\":2,\"scheduleHour\":26}" +
                (simSeconds >= 12.5 * 3600 ? ",\"replacement\":{\"device\":\"hp-gauge-north-3\",\"at\":" + Stamp(Wall(12.5)) + ",\"unitIdentifier\":\"preview-gauge-replacement\"}" : "") + "}}}");
            var alarms = new List<HarborAlarm>(); var commands = new List<HarborCommand>();
            void Alarm(LayoutDevice d, string key, string severity, bool active, double changedHour)
            { alarms.Add(new HarborAlarm(d.ExternalId, key, active ? "ACTIVE" : "CLEARED", severity, false, Wall(dayHours + changedHour), Wall(dayHours + changedHour))); }
            foreach (var d in layout.Devices)
            {
                if (d.Kind == DeviceKind.Pump)
                {
                    var north = d.Quay == "hp-north-quay"; var start = north ? 18.5 : 18.0;
                    if (h >= 17.5) Alarm(d, "wet-well-high", "MAJOR", h < 20.2, h < 20.2 ? 17.5 : 20.2);
                    if (!north && h >= 17.75)
                    {
                        var state = h < 17.8 ? "QUEUED" : h < 17.85 ? "SENT" : "SUCCESSFUL";
                        var token = "preview-start-" + d.Token + "-" + day;
                        commandRecords.TryGetValue(token, out var previous);
                        var record = new HarborCommand(d.ExternalId, token, "start-pump", state, now,
                            previous.QueuedTime ?? Wall(dayHours + 17.75), state == "QUEUED" ? (DateTimeOffset?)null : previous.SentTime ?? Wall(dayHours + 17.8),
                            state == "SUCCESSFUL" ? previous.RespondedTime ?? Wall(dayHours + 17.85) : (DateTimeOffset?)null, d.Token);
                        commandRecords[token] = record; commands.Add(record);
                    }
                }
                if (DeviceKind.IsGauge(d.Kind) && d.Quay == "hp-east-quay" && h >= 17)
                {
                    var severity = h < 18 ? "MINOR" : h < 19 ? "MAJOR" : "CRITICAL";
                    Alarm(d, "flood-level", severity, h < 22, h < 18 ? 17 : h < 19 ? 18 : h < 22 ? 19 : 22);
                    Alarm(d, "surge-rising", "WARNING", h < 20, h < 20 ? 17 : 20);
                }
                var outageElapsed = (h - 19.75) / 24 * layout.DayLengthSeconds;
                if (d.Quay == "hp-east-quay" && h >= 19.75 && outageElapsed >= 45 && (d.Kind == DeviceKind.Light || d.Kind == DeviceKind.Charger))
                    Alarm(d, d.Kind == DeviceKind.Light ? "light-silent" : "charger-offline", "MAJOR", h < 21.25, h < 21.25 ? 19.75 + 45 * 24 / layout.DayLengthSeconds : 21.25);
                // The stand-in alarm tracks the fault through the paging hold, just as it does after the hold ends.
                if (d.Token == lamp && h >= 11 + 10.0 / 60) Alarm(d, "lamp-flicker", "MAJOR", h < 12 + 50.0 / 60, h < 12 + 50.0 / 60 ? 11 + 10.0 / 60 : 12 + 50.0 / 60);
                // the platform's connectivity rule, on the charger whose session dropped
                if (d.Kind == DeviceKind.MqttCharger && d.Index == 1 && h >= MqttDropFrom)
                    Alarm(d, "mqtt-charger-disconnected", "WARNING", h < MqttDropTo, h < MqttDropTo ? MqttDropFrom : MqttDropTo);
                if (d.Kind == DeviceKind.Truck && h >= layout.Cues.TruckInPlainSim)
                {
                    var inPlain = h < layout.Cues.TruckOutPlainSim;
                    Alarm(d, "truck-in-flood-plain", "WARNING", inPlain, inPlain ? layout.Cues.TruckInPlainSim : layout.Cues.TruckOutPlainSim);
                }
            }
            Alarms = alarms.AsReadOnly(); Commands = commands.AsReadOnly();
            Suppressions = new[] { new SuppressionEvidence { Token = "preview-lamp-window", Status = h >= 10.75 && h < 11.5 ? "ACTIVE" : h < 10.75 ? "SCHEDULED" : "ENDED",
                Start = Wall(dayHours + 10.75), End = Wall(dayHours + 11.5), ObservedAt = now, Targets = new[] { new SuppressionTarget("DEVICE", lamp) }, AlarmKeys = new[] { "lamp-flicker" } } };
            BuildRollout();
        }

        void BuildRollout()
        {
            var targets = new List<OtaTarget>();
            var progress = new Dictionary<string, int>();
            foreach (var key in new[] { "total", "pending", "inFlight", "succeeded", "failed", "unknown", "cancelled", "skipped" }) progress[key] = 0;
            var elapsed = simSeconds / 3600 - 26;
            var i = 0;
            foreach (var d in layout.Devices)
            {
                if (d.Kind != DeviceKind.Charger) continue;
                var start = i / 2 * 0.3;
                var outcome = elapsed < start ? "PENDING" : elapsed < start + 0.25 ? "IN_FLIGHT" : d.Token == "hp-charger-north-1" ? "FAILED" : "SUCCEEDED";
                targets.Add(new OtaTarget { DeviceToken = d.Token, Outcome = outcome, AttemptState = outcome == "PENDING" ? null : outcome == "IN_FLIGHT" ? "DOWNLOADING" : outcome == "FAILED" ? "FAILED" : "UPDATED" });
                progress["total"]++; progress[outcome == "PENDING" ? "pending" : outcome == "IN_FLIGHT" ? "inFlight" : outcome == "FAILED" ? "failed" : "succeeded"]++; i++;
            }
            Rollout = new RolloutEvidence { Token = "preview-rollout", State = elapsed < 0 ? "DRAFT" : elapsed < 1 ? "RUNNING" : "COMPLETED", MaxInFlight = 2,
                FailureThreshold = 2, ObservedAt = now, Targets = targets.AsReadOnly(), TotalTargets = targets.Count,
                Progress = new System.Collections.ObjectModel.ReadOnlyDictionary<string, int>(progress) };
        }

        bool TryPreviewMetric(LayoutDevice d, string metric, double h, bool outage, out Reading reading)
        {
            reading = default;
            double value;
            if (d.Kind == DeviceKind.Pump)
            {
                var start = d.Quay == "hp-north-quay" ? 18.5 : 18;
                var running = h >= start && h < 20.5;
                var well = h < 16 ? 1 : h < start ? 1 + (h - 16) * (d.Quay == "hp-north-quay" ? 0.9 : 0.85) : Math.Max(0.8, 3.3 - (h - start) * 1.2);
                switch (metric) { case Metrics.WetWell: value = well; break; case Metrics.PumpRunning: value = running ? 1 : 0; break; case Metrics.Flow: value = running ? 100 : 0; break; case Metrics.PumpCurrent: value = running ? 38 : 0; break; default: return false; }
            }
            else if (d.Kind == DeviceKind.Charger)
            {
                var active = h >= 7 && h < 23;
                var hotCharger = d.Token == "hp-charger-basin-2";
                var cap = hotCharger && h >= 14.1 ? 11 : 22;
                switch (metric)
                {
                    case Metrics.ChargerPower: value = active ? Math.Min(cap, 16) : 0; break;
                    case Metrics.MaxKw: value = cap; break;
                    case Metrics.SessionActive: value = active ? 1 : 0; break;
                    case Metrics.ConnectorTemp: value = hotCharger && h >= 14 && h < 15 ? 76 - 16 * Rgb.Clamp01((h - 14.1) / 0.9) : 18; break;
                    default: return false;
                }
            }
            else if (DeviceKind.IsLinkedCharger(d.Kind))
            {
                if (metric != Metrics.ChargerPower) return false;
                // a silent one keeps its last report, from the hour it fell silent
                var since = LinkedSilentSince(d, h);
                reading = new Reading(LinkedChargerKw, since.HasValue ? Wall(DayClock.DayIndex(simSeconds) * 24 + since.Value) : now); return true;
            }
            else if (d.Kind == DeviceKind.Meter) { if (metric == Metrics.MeterKw) value = h >= 8 && h < 18 ? 48 : 12; else if (metric == Metrics.MeterKwh) value = simSeconds / 3600 * 24; else return false; }
            else if (d.Kind == DeviceKind.Truck && metric == Metrics.Speed) { value = h >= 17 && h < 18 || h >= 21.5 && h < 22.5 ? 8 : 0; }
            else if (d.Kind == DeviceKind.Light) { if (metric == Metrics.LampFault) value = d.Token == "hp-light-east-3" && h >= 11 && h < 12 ? 1 : 0; else if (metric == Metrics.LampPower) value = LampsOn(h) ? 80 : 0; else return false; }
            else return false;
            var at = outage && d.Kind == DeviceKind.Charger ? Wall(DayClock.DayIndex(simSeconds) * 24 + 19.75) : now;
            reading = new Reading(value, at); return true;
        }

        public bool TryAttribute(string externalId, string key, out Reading value)
        {
            value = default;
            if (layout.Find(externalId ?? "")?.Token != "hp-pump-basin" || key != "well_alarm_m") return false;
            value = new Reading(DayClock.Hours(simSeconds) >= 18.5 ? 1.8 : 1.0, now); return true;
        }
        public bool TryLocation(string externalId, out HarborLocation location)
        {
            location = default;
            if (layout.Find(externalId ?? "")?.Kind != DeviceKind.Truck) return false;
            var h = DayClock.Hours(simSeconds);
            // the stand-in drives the layout's own route: out over the hour after it departs, parked, then home over the last hour
            var c = layout.Cues;
            var from = layout.TruckRoute[0];
            var to = layout.TruckRoute[layout.TruckRoute.Count - 1];
            const double legH = 1.0;
            var outbound = h >= c.TruckDepartSim && h < c.TruckDepartSim + legH;
            var inbound = h >= c.TruckHomeSim - legH && h < c.TruckHomeSim;
            var fraction = outbound ? h - c.TruckDepartSim : h >= c.TruckDepartSim + legH && h < c.TruckHomeSim - legH ? 1 : inbound ? c.TruckHomeSim - h : 0;
            var lat = from.Lat + fraction * (to.Lat - from.Lat);
            var lon = from.Lon + fraction * (to.Lon - from.Lon);
            var moving = outbound || inbound;
            var heading = Math.Atan2((to.Lon - from.Lon) * Math.Cos(from.Lat * Math.PI / 180), to.Lat - from.Lat) * 180 / Math.PI + (inbound ? 180 : 0);
            location = new HarborLocation(lat, lon, (heading + 360) % 360, moving ? 8 : 0, now); return true;
        }
    }
}
