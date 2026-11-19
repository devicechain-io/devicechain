// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>The scene's metric vocabulary: the harborpulse profiles' metric keys.</summary>
    public static class Metrics
    {
        public const string WetWell = "wet_well_m", PumpRunning = "pump_running", Flow = "flow_lps", PumpCurrent = "pump_current_a";
        public const string ChargerPower = "power_kw", MaxKw = "max_kw", SessionActive = "session_active", ConnectorTemp = "connector_temp_c";
        public const string MeterKw = "kw", MeterKwh = "kwh", Speed = "speed_mps";
        public const string WaterLevel = "water_level_m", Battery = "battery_v";
        public const string LampOn = "lamp_on", LampPower = "power_w", LampFault = "lamp_fault", Dim = "dim_pct";
    }

    /// <summary>One value as the platform reported it: what, when it happened, and where the scene got it.</summary>
    public readonly struct Reading
    {
        public Reading(double value, DateTimeOffset occurredAt)
        {
            Value = value;
            OccurredAt = occurredAt;
        }

        public double Value { get; }
        public DateTimeOffset OccurredAt { get; }
    }

    /// <summary>
    /// Where the scene's readings come from, one implementation per mode: the platform's observer (live), or the labelled preview. The
    /// scene only reads; nothing here writes anywhere.
    /// </summary>
    public interface IReadingSource
    {
        /// <summary>The newest value of a metric for a device (by external ID), or false when the source has none.</summary>
        bool TryGet(string externalId, string metric, out Reading reading);
    }

    /// <summary>One quay's water, as its gauges report it.</summary>
    public sealed class RegionLevel
    {
        public string Quay { get; internal set; }

        /// <summary>The median of the fresh gauge readings; the last such median while <see cref="Held"/>; null if no gauge has ever reported.</summary>
        public double? LevelM { get; internal set; }

        /// <summary>How many gauges contributed a fresh reading, out of <see cref="Gauges"/>.</summary>
        public int Contributors { get; internal set; }

        public int Gauges { get; internal set; }

        /// <summary>No gauge is fresh: the level is the last one known, shown with its age, not a current reading.</summary>
        public bool Held { get; internal set; }

        /// <summary>When the newest contributing reading happened.</summary>
        public DateTimeOffset? NewestAt { get; internal set; }
    }

    /// <summary>
    /// The tide per quay from the gauges' observed levels. A quay's level is the MEDIAN of its gauges' fresh readings (the three HTTP gauges
    /// and its LoRaWAN gauge; the Basin's legacy gauge only while it has a fresh decoded reading, which its broken decoder mostly denies it),
    /// so one gauge reading high cannot lift the water. A reading older than its kind's freshness window does not count. With no fresh gauge
    /// the level holds at the last median and says so; with no gauge ever, there is no level, and the scene must show that, not invent one.
    /// </summary>
    public sealed class TideBoard
    {
        /// <summary>An HTTP gauge reports every emit interval (5 s by default); this is several intervals.</summary>
        public static readonly TimeSpan GaugeFresh = TimeSpan.FromSeconds(30);

        /// <summary>A LoRaWAN gauge may be commanded to report as slowly as once a minute.</summary>
        public static readonly TimeSpan LoraFresh = TimeSpan.FromSeconds(150);

        readonly Dictionary<string, List<LayoutDevice>> gaugesByQuay = new Dictionary<string, List<LayoutDevice>>(StringComparer.Ordinal);
        readonly Dictionary<string, RegionLevel> regions = new Dictionary<string, RegionLevel>(StringComparer.Ordinal);
        readonly List<double> scratch = new List<double>();

        public TideBoard(HarborLayout layout)
        {
            foreach (var q in layout.Quays)
            {
                gaugesByQuay[q.Token] = new List<LayoutDevice>();
                regions[q.Token] = new RegionLevel { Quay = q.Token };
            }

            foreach (var d in layout.Devices)
                if (DeviceKind.IsGauge(d.Kind))
                    gaugesByQuay[d.Quay].Add(d);
            foreach (var r in regions.Values) r.Gauges = gaugesByQuay[r.Quay].Count;
        }

        public RegionLevel Region(string quay) => regions[quay];

        public IEnumerable<RegionLevel> Regions => regions.Values;

        public static TimeSpan FreshFor(string kind) => kind == DeviceKind.Gauge ? GaugeFresh : LoraFresh;

        public void Update(IReadingSource source, DateTimeOffset now)
        {
            foreach (var pair in gaugesByQuay)
            {
                var region = regions[pair.Key];
                scratch.Clear();
                DateTimeOffset? newest = null;
                foreach (var g in pair.Value)
                {
                    if (!source.TryGet(g.ExternalId, Metrics.WaterLevel, out var r)) continue;
                    if (now - r.OccurredAt > FreshFor(g.Kind)) continue;
                    scratch.Add(r.Value);
                    if (!newest.HasValue || r.OccurredAt > newest.Value) newest = r.OccurredAt;
                }

                region.Contributors = scratch.Count;
                if (scratch.Count > 0)
                {
                    region.LevelM = Median(scratch);
                    region.NewestAt = newest;
                    region.Held = false;
                }
                else
                {
                    region.Held = region.LevelM.HasValue;
                }
            }
        }

        public static double Median(List<double> values)
        {
            if (values.Count == 0) throw new ArgumentException("the median of nothing", nameof(values));
            values.Sort();
            var mid = values.Count / 2;
            return values.Count % 2 == 1 ? values[mid] : (values[mid - 1] + values[mid]) / 2;
        }
    }

    public enum LampCondition
    {
        /// <summary>The platform has never reported this lamp.</summary>
        Unknown,

        /// <summary>Reporting, and the lamp is off.</summary>
        Off,

        /// <summary>Reporting, and the lamp is on (at <see cref="LampState.DimPct"/>).</summary>
        On,

        /// <summary>It reported, but not within the platform's silence window: the scene cannot say what it is doing now.</summary>
        Silent,
    }

    public sealed class LampState
    {
        public LampCondition Condition { get; internal set; }

        /// <summary>Dim level, percent, as last reported within the silence window; null when the platform has no current one.</summary>
        public double? DimPct { get; internal set; }

        /// <summary>The driver reports a fault.</summary>
        public bool Fault { get; internal set; }

        public DateTimeOffset? LastAt { get; internal set; }
        public bool Lit => Condition == LampCondition.On;
    }

    /// <summary>
    /// Each street light as the platform last reported it. A light is drawn lit only on a fresh report that it is on; one silent for longer
    /// than the platform's own light-silent window is shown as silent, not as whatever it last said.
    /// </summary>
    public sealed class LampBoard
    {
        /// <summary>The harborpulse light-silent rule's window.</summary>
        public static readonly TimeSpan Silence = TimeSpan.FromSeconds(45);

        readonly Dictionary<string, LampState> lamps = new Dictionary<string, LampState>(StringComparer.Ordinal);

        public LampBoard(HarborLayout layout)
        {
            foreach (var d in layout.Devices)
                if (d.Kind == DeviceKind.Light)
                    lamps[d.ExternalId] = new LampState();
        }

        public LampState Lamp(string externalId) => lamps[externalId];

        public IEnumerable<KeyValuePair<string, LampState>> Lamps => lamps;

        public int Count(LampCondition c)
        {
            var n = 0;
            foreach (var l in lamps.Values)
                if (l.Condition == c) n++;
            return n;
        }

        public void Update(IReadingSource source, DateTimeOffset now)
        {
            foreach (var pair in lamps)
            {
                var s = pair.Value;
                if (!source.TryGet(pair.Key, Metrics.LampOn, out var on))
                {
                    s.Condition = LampCondition.Unknown;
                    continue;
                }

                s.LastAt = on.OccurredAt;
                s.DimPct = source.TryGet(pair.Key, Metrics.Dim, out var dim) && now - dim.OccurredAt <= Silence ? Math.Max(0, Math.Min(100, dim.Value)) : (double?)null;
                s.Fault = source.TryGet(pair.Key, Metrics.LampFault, out var fault) && fault.Value > 0.5 && now - fault.OccurredAt <= Silence;
                if (now - on.OccurredAt > Silence) s.Condition = LampCondition.Silent;
                else s.Condition = on.Value > 0.5 ? LampCondition.On : LampCondition.Off;
            }
        }
    }
}

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// The water's mean level along the waterfront, from each quay's level: along a quay's own wall it is exactly that quay's level (the
    /// median its readout shows), and only in the open water between two quays does it blend from one to the next. Beyond the outermost
    /// quays it keeps theirs. The shader evaluates the same mapping (HarborWaves.hlsl, HarborTide) with the spans and levels handed to it.
    /// </summary>
    public static class TideProfile
    {
        /// <summary>
        /// <paramref name="xMin"/>, <paramref name="xMax"/> and <paramref name="level"/> describe the quays ordered west to east, spans not
        /// overlapping.
        /// </summary>
        public static double LevelAt(double x, IReadOnlyList<double> xMin, IReadOnlyList<double> xMax, IReadOnlyList<double> level)
        {
            var n = level.Count;
            if (n == 0) throw new ArgumentException("no quays", nameof(level));
            if (x <= xMax[0]) return level[0];
            for (var i = 1; i < n; i++)
            {
                if (x < xMin[i])
                {
                    var t = (x - xMax[i - 1]) / (xMin[i] - xMax[i - 1]);
                    t = t * t * (3 - 2 * t);
                    return level[i - 1] + (level[i] - level[i - 1]) * t;
                }

                if (x <= xMax[i]) return level[i];
            }

            return level[n - 1];
        }
    }
}
