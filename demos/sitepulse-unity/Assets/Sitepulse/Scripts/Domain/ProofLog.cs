// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>Who says a proof row is so: the platform (what the observer was told), the device (its own timeline), or the presenter's hand.</summary>
    public enum ProofSource { Platform, Device, Presenter }

    /// <summary>One row of a machine's evidence chain: when it happened, who says so, and the plain words.</summary>
    public readonly struct ProofRow
    {
        public ProofRow(DateTimeOffset at, ProofSource source, string text)
        {
            At = at;
            Source = source;
            Text = text;
        }

        public DateTimeOffset At { get; }
        public ProofSource Source { get; }
        public string Text { get; }

        /// <summary>The label a drawer prints beside the row.</summary>
        public string SourceLabel => Source == ProofSource.Platform ? "platform" : Source == ProofSource.Device ? "device" : "presenter";
    }

    /// <summary>Where a drawer reads its rows from; <see cref="Version"/> moves whenever a row is added or the log is cleared.</summary>
    public interface IProofRows
    {
        int Version { get; }

        /// <summary>Adds the newest <paramref name="max"/> rows of a device, oldest first, to <paramref name="into"/>.</summary>
        void Rows(string deviceId, List<ProofRow> into, int max);
    }

    /// <summary>The lines the platform's detection rules draw (sitepulse.go), which a proof row names when a sample crosses one.</summary>
    public static class RuleLines
    {
        public const double LowFuelPct = 15.0;
        public const double TyreLowKpa = 600.0;

        /// <summary>The line a measurement is watched against (the rule fires on a value BELOW it); false for a key no rule watches.</summary>
        public static bool TryBelow(string key, out double line)
        {
            switch (key)
            {
                case MeasurementKeys.FuelPct: line = LowFuelPct; return true;
                case MeasurementKeys.TyrePressureKpa: line = TyreLowKpa; return true;
                default: line = 0; return false;
            }
        }
    }

    /// <summary>
    /// The proof drawer's evidence, per device: only things that HAPPENED, each with the time it happened and who says so. A row is added
    /// when the platform said something (a sample on the far side of a rule's line, an alarm changing state, a command changing state),
    /// when the device's own timeline said something, or when the presenter's hand moved an input. Nothing is inferred: a command state
    /// the observer never saw has no row, an alarm that was never reported cleared has no CLEARED row, and a device's "SUCCESS" is never
    /// turned into a platform's SUCCESSFUL. A snapshot's measurement is a fact about the past, not an event, and adds no row. Main thread.
    /// </summary>
    public sealed class ProofLog : IProofRows
    {
        public const int MaxPerDevice = 80;
        const int MaxText = 84;

        /// <summary>The device timeline's kinds the drawer shows. The rest (driving back to its track, back at work, queue arrivals) are the machine's own business.</summary>
        static readonly HashSet<string> ShownKinds = new HashSet<string>(StringComparer.Ordinal)
        {
            "received", "accepted", "refused", "superseded", "refuelling", "outcome", "stalled", "presenter",
        };

        sealed class Lane
        {
            public readonly List<ProofRow> Rows = new List<ProofRow>();
            public readonly HashSet<string> Seen = new HashSet<string>(StringComparer.Ordinal);
            public readonly Dictionary<string, bool> Armed = new Dictionary<string, bool>(StringComparer.Ordinal);
        }

        readonly Dictionary<string, Lane> lanes = new Dictionary<string, Lane>(StringComparer.Ordinal);

        public int Version { get; private set; }

        public void Reset()
        {
            lanes.Clear();
            Version++;
        }

        Lane Of(string device)
        {
            if (!lanes.TryGetValue(device, out var l)) lanes[device] = l = new Lane();
            return l;
        }

        static string Num(double v, string format = "0.##") => v.ToString(format, CultureInfo.InvariantCulture);

        static string Clip(string s) => s.Length <= MaxText ? s : s.Substring(0, MaxText - 1) + "…";

        void Add(Lane lane, ProofRow row)
        {
            // chronological by the time the thing happened; a row at the same instant goes after the ones already there
            var i = lane.Rows.Count;
            while (i > 0 && lane.Rows[i - 1].At > row.At) i--;
            lane.Rows.Insert(i, row);
            if (lane.Rows.Count > MaxPerDevice) lane.Rows.RemoveAt(0);
            Version++;
        }

        /// <summary>
        /// A measurement the platform reported. Its first sample below a rule's line (after one at or above it) is a row; a snapshot's
        /// value is not an event and adds nothing, and does not re-arm the line.
        /// </summary>
        public void SampleObserved(string device, string key, double value, DateTimeOffset occurredAt, bool fromSnapshot)
        {
            if (device == null || fromSnapshot || !RuleLines.TryBelow(key, out var line)) return;
            var lane = Of(device);
            var armed = !lane.Armed.TryGetValue(key, out var a) || a;
            if (value >= line)
            {
                lane.Armed[key] = true;
                return;
            }

            if (!armed) return;
            lane.Armed[key] = false;
            var unit = MeasurementKeys.Unit(key);
            Add(lane, new ProofRow(occurredAt, ProofSource.Platform, $"sample {key} {Num(value, "0.0#")} {unit} — below the rule line ({Num(line)} {unit})"));
        }

        /// <summary>An alarm event the platform reported: one row for each state of each alarm, however often it is repeated.</summary>
        public void AlarmObserved(string device, string token, string key, string state, string severity, DateTimeOffset occurredAt)
        {
            if (device == null || token == null || key == null || state == null) return;
            var lane = Of(device);
            if (!lane.Seen.Add("a|" + token + "|" + state)) return;
            Add(lane, new ProofRow(occurredAt, ProofSource.Platform, "alarm " + key + " " + state + (string.IsNullOrEmpty(severity) ? "" : " · " + severity)));
        }

        /// <summary>
        /// A command's state as the observer last saw it: one row for each state of each command it SAW. It is dated when the platform queued
        /// it (QUEUED) or when this app saw the state (every other), since the platform does not date the transitions between.
        /// </summary>
        public void CommandObserved(string device, string token, string name, string status, DateTimeOffset queuedAt, DateTimeOffset observedAt)
        {
            if (device == null || token == null || name == null || status == null) return;
            var lane = Of(device);
            if (!lane.Seen.Add("c|" + token + "|" + status)) return;
            Add(lane, new ProofRow(status == "QUEUED" ? queuedAt : observedAt, ProofSource.Platform, "command " + name + " " + status));
        }

        /// <summary>A row of the machine's own timeline (its account of what it did). Kinds the drawer does not show are ignored.</summary>
        public void DeviceRow(string device, DateTimeOffset at, string kind, string text)
        {
            if (device == null || kind == null || !ShownKinds.Contains(kind)) return;
            var source = kind == "presenter" ? ProofSource.Presenter : ProofSource.Device;
            Add(Of(device), new ProofRow(at, source, Clip(kind + " · " + (text ?? ""))));
        }

        public void Rows(string deviceId, List<ProofRow> into, int max)
        {
            if (deviceId == null || !lanes.TryGetValue(deviceId, out var lane)) return;
            for (var i = Math.Max(0, lane.Rows.Count - max); i < lane.Rows.Count; i++) into.Add(lane.Rows[i]);
        }
    }
}
