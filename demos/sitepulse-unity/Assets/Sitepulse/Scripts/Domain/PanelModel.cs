// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>One metric of the selected-machine panel: its name, its value with the unit, and when it was last observed.</summary>
    public readonly struct PanelRow
    {
        public PanelRow(string label, string value, string observedAt, RowTone tone)
        {
            Label = label;
            Value = value;
            ObservedAt = observedAt;
            Tone = tone;
        }

        public string Label { get; }
        public string Value { get; }

        /// <summary>"hh:mm:ss" in UTC of the moment the value happened on the device; empty when it has none (nothing observed, or an illustrative value).</summary>
        public string ObservedAt { get; }

        public RowTone Tone { get; }
    }

    /// <summary>What the selected-machine panel prints: a title, every metric of the machine's profile and a footer.</summary>
    public sealed class PanelView
    {
        public string Title { get; set; }
        public string Kind { get; set; }
        public List<PanelRow> Rows { get; } = new List<PanelRow>();
        public string Footer { get; set; }
    }

    /// <summary>
    /// The selected-machine panel's content, decided from a reading alone (no scene, no UI). It shows the machine's own profile metrics
    /// and nothing else (a dozer's three, a truck's or loader's five, the crusher's two), each with its unit and the time it was last observed, inked
    /// by the same freshness rule as a card. An observed value too old to show is a dash, never a number.
    /// </summary>
    public static class PanelModel
    {
        public static string Clock(DateTimeOffset t) => t.UtcDateTime.ToString("HH:mm:ss", CultureInfo.InvariantCulture);

        /// <param name="keys">The measurement keys the machine's profile reports (the simulation's own list for its kind).</param>
        public static PanelView Build(DeviceReading r, string kindLabel, IReadOnlyList<string> keys, DateTimeOffset now, bool streamLive)
        {
            var view = new PanelView { Title = r.DeviceId, Kind = kindLabel };
            var observed = r.Provenance != Provenance.Illustrative;
            DateTimeOffset? newest = null;
            foreach (var key in keys)
            {
                var stamped = r.TryGetStamp(key, out var stamp);
                var row = CardPresenter.Row(observed, r.Format(key), stamped ? stamp.OccurredAt : (DateTimeOffset?)null, now, streamLive);
                var shown = row.Shown ? row.Text : CardPresenter.NoValue;
                var when = stamped && row.Shown && row.Text != CardPresenter.NoValue ? Clock(stamp.OccurredAt) : "";
                if (when.Length > 0 && (!newest.HasValue || stamp.OccurredAt > newest.Value)) newest = stamp.OccurredAt;
                view.Rows.Add(new PanelRow(MeasurementKeys.Label(key), shown, when, row.Shown ? row.Tone : RowTone.Grey));
            }

            view.Footer = !observed ? "illustrative values (no platform time)"
                : newest.HasValue ? "last observed " + Clock(newest.Value) + " UTC" : "nothing observed yet";
            return view;
        }
    }
}
