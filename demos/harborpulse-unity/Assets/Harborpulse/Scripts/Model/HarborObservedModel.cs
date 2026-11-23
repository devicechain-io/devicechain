// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Collections.ObjectModel;

namespace DeviceChain.Harborpulse.Model
{
    public sealed class ChargerBoard
    {
        public Reading? Power { get; internal set; }
        public Reading? MaxKw { get; internal set; }
        public Reading? SessionActive { get; internal set; }
        public Reading? ConnectorTemperature { get; internal set; }
        public bool NoReading => !Power.HasValue;
        public bool Stale { get; internal set; }
        public bool SessionKnown { get; internal set; }
        public bool Confirmed => !NoReading && !Stale && SessionKnown;
        public bool ShowCarAndCable => Confirmed && SessionActive.Value.Value > 0.5;
        public double? Glow => ConnectorTemperature.HasValue ? GlowFor(ConnectorTemperature.Value.Value) : (double?)null;
        /// <summary>Visual mapping, one at the rule's 70 C line; over-temperature continues above one, clamped at 1.5.</summary>
        public static double GlowFor(double c) => Math.Max(0, Math.Min(1.5, (c - 18) / 52));
        public string OtaOutcome { get; internal set; }
        public string AttemptState { get; internal set; }
        public DateTimeOffset? OtaObservedAt { get; internal set; }
    }

    /// <summary>
    /// A charger that holds its own MQTT session (under a device token, or under a client certificate) as the platform last heard it. It
    /// reports one thing, its charging power, so that is all the scene says of it: reporting, silent for longer than the platform's own
    /// silence window, or never heard. Why a silent one is silent (a dropped session, a revoked certificate) is the platform's to say, in
    /// its alarms and its credential record; the scene does not guess it from the quiet.
    /// </summary>
    public sealed class LinkedChargerBoard
    {
        /// <summary>Above this the charger is drawn delivering: a car in its bay, the cable out.</summary>
        public const double DeliveringKw = 0.5;
        public Reading? Power { get; internal set; }
        public bool NoReading => !Power.HasValue;
        public bool Stale { get; internal set; }
        public bool Reporting => !NoReading && !Stale;
        public bool Delivering => Reporting && Power.Value.Value > DeliveringKw;
        public string Link => NoReading ? "never reported" : Stale ? "silent" : "reporting";
        public static LinkedChargerBoard From(Reading? power, DateTimeOffset now) =>
            new LinkedChargerBoard { Power = power, Stale = power.HasValue && now - power.Value.OccurredAt > LampBoard.Silence };
    }

    /// <summary>
    /// A lamp that asks the platform for its commands, as the platform last heard it. It reports its dim level and nothing else: not whether
    /// it is lit. The level is shown as it was reported; whether the lantern burns is the scene's own (it lights with the dusk, like any
    /// lamp on a photocell) and is never read back as platform state.
    /// </summary>
    public sealed class PullLampState
    {
        public const int PipCount = 4;
        public Reading? Dim { get; internal set; }
        public bool NoReading => !Dim.HasValue;
        public bool Stale { get; internal set; }
        public bool Reporting => !NoReading && !Stale;
        /// <summary>The reported level, 0 to 100, while it is current; null when the platform has none that is.</summary>
        public double? DimPct => Reporting ? Math.Max(0, Math.Min(100, Dim.Value.Value)) : (double?)null;
        /// <summary>How many of the post's four level pips are lit: one for each quarter the level reaches into.</summary>
        public int Pips => PipsFor(DimPct);
        public static int PipsFor(double? pct) => !pct.HasValue || pct.Value <= 0 ? 0 : Math.Min(PipCount, (int)Math.Ceiling(pct.Value / (100.0 / PipCount) - 1e-9));
        public string Link => NoReading ? "never reported" : Stale ? "silent" : "reporting";
        public static PullLampState From(Reading? dim, DateTimeOffset now) =>
            new PullLampState { Dim = dim, Stale = dim.HasValue && now - dim.Value.OccurredAt > LampBoard.Silence };
    }

    public sealed class TruckTrack
    {
        public Ground At { get; internal set; }
        public double? Heading { get; internal set; }
        public double? Speed { get; internal set; }
        public DateTimeOffset? FixAt { get; internal set; }
        public bool NoFix => !FixAt.HasValue;
        public bool Stale { get; internal set; }
        public bool LightBar { get; internal set; }
        public string Note => NoFix ? "no fix" : Stale ? "stale fix" : "platform fix";
        public static TruckTrack From(SitePlan plan, HarborLocation? location, bool floodAlarm, DateTimeOffset now)
        {
            var s = new TruckTrack { At = plan.Depot, LightBar = floodAlarm };
            if (!location.HasValue || !location.Value.Latitude.HasValue || !location.Value.Longitude.HasValue) return s;
            var p = location.Value;
            if (Math.Abs(p.Latitude.Value) > 90 || Math.Abs(p.Longitude.Value) > 180) throw new FormatException("truck coordinate outside WGS84");
            s.At = plan.Frame.ToGround(new LatLon(p.Latitude.Value, p.Longitude.Value));
            s.FixAt = p.OccurredAt; s.Heading = p.Heading; s.Speed = p.Speed;
            s.Stale = now - p.OccurredAt > TimeSpan.FromSeconds(10);
            s.LightBar = p.Speed > 0 || floodAlarm;
            return s;
        }
    }

    /// <summary>Runner replacement declaration plus only observed readings after it on the same bound external ID/token. No claim of unit identity.</summary>
    public sealed class ReplacementPlate
    {
        public double? DeclaredSimSeconds { get; internal set; }
        public StoryReplacement RunnerDeclaration { get; internal set; }
        public string ExternalId { get; internal set; }
        public string BoundToken { get; internal set; }
        public int ObservedReadingsAfter { get; internal set; }
        public DateTimeOffset? NewestOccurredAt { get; internal set; }
        public TimeSpan? NewestAge { get; internal set; }
        public string Claim => "runner's replacement plan; PLATFORM: readings after the declared instant on the same bound device only";
    }

    public sealed class LampService
    {
        public StoryJob RunnerPlan { get; internal set; }
        public SuppressionEvidence PlatformWindow { get; internal set; }
        public bool? WindowOpen { get; internal set; }
        public AlarmCoverage AlarmObservation { get; internal set; }
        public string FlickerSeverity { get; internal set; }
        public bool FlickerActive => AlarmObservation.Observed && FlickerSeverity != null;
        public string WindowNote => "platform suppression window " + (PlatformWindow?.Status ?? "not reported") + " (holds paging for lamp-flicker)";
        public string AlarmNote => !AlarmObservation.Observed ? "alarm state unknown (alarms not observed)"
            : FlickerActive ? "lamp-flicker active " + FlickerSeverity : "no lamp-flicker alarm";
        public string Note => WindowNote + "; " + AlarmNote;
        public static LampService From(StoryJob plan, string boundToken, IReadOnlyList<SuppressionEvidence> windows, AlarmCoverage coverage, string flickerSeverity, DateTimeOffset now)
        {
            if (plan == null || string.IsNullOrEmpty(boundToken)) coverage = default;
            var s = new LampService { RunnerPlan = plan, AlarmObservation = coverage, FlickerSeverity = coverage.Observed ? flickerSeverity : null };
            if (plan == null) return s;
            foreach (var w in windows)
                if (w.Token == plan.Window && w.TargetsDevice(boundToken)) { s.PlatformWindow = w; s.WindowOpen = w.Open(now); break; }
            return s;
        }
    }

    /// <summary>A detached read-only frame. Keep it while later frames are built; reducers never mutate a returned frame.</summary>
    public sealed class HarborFrame
    {
        public double ReflectionLevelM { get; internal set; }
        public IReadOnlyDictionary<string, double> Overtopping { get; internal set; }
        public IReadOnlyDictionary<string, double> Spray { get; internal set; }
        public ClockReading Clock { get; internal set; }
        public DateTimeOffset ObservedAt { get; internal set; }
        public StoryCues World { get; internal set; }
        public IReadOnlyDictionary<string, RegionLevel> Tides { get; internal set; }
        public IReadOnlyDictionary<string, LampState> Lamps { get; internal set; }
        public IReadOnlyDictionary<string, PumpPresentation> Pumps { get; internal set; }
        public IReadOnlyDictionary<string, ChargerBoard> Chargers { get; internal set; }
        /// <summary>The chargers that hold their own session, by external ID. None of them is in <see cref="Chargers"/>.</summary>
        public IReadOnlyDictionary<string, LinkedChargerBoard> LinkedChargers { get; internal set; }
        /// <summary>The lamps that ask for their commands, by external ID. None of them is in <see cref="Lamps"/>.</summary>
        public IReadOnlyDictionary<string, PullLampState> PullLamps { get; internal set; }
        public IReadOnlyDictionary<string, IReadOnlyDictionary<string, Reading>> Measurements { get; internal set; }
        public IReadOnlyDictionary<string, int> AlarmCounts { get; internal set; }
        public IReadOnlyList<AlarmBeacon> Beacons { get; internal set; }
        public IReadOnlyList<CommandObservation> Commands { get; internal set; }
        public RolloutEvidence Rollout { get; internal set; }
        public TruckTrack Truck { get; internal set; }
        public ReplacementPlate Replacement { get; internal set; }
        public LampService LampService { get; internal set; }
        public string ReadProblem { get; internal set; }
    }

    public sealed class HarborObservedModel
    {
        readonly HarborLayout layout;
        readonly SitePlan plan;
        readonly TideBoard tides;
        readonly WaterMotion motion = new WaterMotion();
        readonly LampBoard lamps;
        readonly AlarmBeacons beacons = new AlarmBeacons();
        readonly Dictionary<string, PumpStartEvidence> pumps = new Dictionary<string, PumpStartEvidence>();
        public CommandHistory CommandHistory { get; } = new CommandHistory();
        readonly HashSet<(string, DateTimeOffset)> replacedReadings = new HashSet<(string, DateTimeOffset)>();
        DateTimeOffset? replacementAt, newestReplacement;
        string replacementToken;
        DateTimeOffset? run;
        HarborLocation? truckFix;

        public HarborObservedModel(HarborLayout layout)
        {
            this.layout = layout ?? throw new ArgumentNullException(nameof(layout));
            plan = SitePlan.Build(layout); tides = new TideBoard(layout); lamps = new LampBoard(layout);
        }
        static readonly IReadOnlyList<string> PumpMetrics = Array.AsReadOnly(new[] { Metrics.WetWell, Metrics.PumpRunning, Metrics.Flow, Metrics.PumpCurrent });
        static readonly IReadOnlyList<string> ChargerMetrics = Array.AsReadOnly(new[] { Metrics.ChargerPower, Metrics.MaxKw, Metrics.SessionActive, Metrics.ConnectorTemp });
        static readonly IReadOnlyList<string> LightMetrics = Array.AsReadOnly(new[] { Metrics.LampOn, Metrics.Dim, Metrics.LampFault, Metrics.LampPower });
        static readonly IReadOnlyList<string> MeterMetrics = Array.AsReadOnly(new[] { Metrics.MeterKw, Metrics.MeterKwh });
        static readonly IReadOnlyList<string> TruckMetrics = Array.AsReadOnly(new[] { Metrics.Speed });
        static readonly IReadOnlyList<string> GaugeMetrics = Array.AsReadOnly(new[] { Metrics.WaterLevel, Metrics.Battery });
        static readonly IReadOnlyList<string> LinkedChargerMetrics = Array.AsReadOnly(new[] { Metrics.ChargerPower });
        static readonly IReadOnlyList<string> PullLightMetrics = Array.AsReadOnly(new[] { Metrics.Dim });
        public static IReadOnlyList<string> MetricsFor(string kind)
        {
            switch (kind)
            {
                case DeviceKind.Pump: return PumpMetrics;
                case DeviceKind.Charger: return ChargerMetrics;
                case DeviceKind.Light: return LightMetrics;
                case DeviceKind.Meter: return MeterMetrics;
                case DeviceKind.Truck: return TruckMetrics;
                case DeviceKind.MqttCharger: case DeviceKind.CertCharger: return LinkedChargerMetrics;
                case DeviceKind.PullLight: return PullLightMetrics;
                default: return DeviceKind.IsGauge(kind) ? GaugeMetrics : Array.Empty<string>();
            }
        }
        public HarborFrame Update(IHarborSource source, ClockReading clock, DateTimeOffset now, bool settleWorldFades = false)
        {
            if (source == null) throw new ArgumentNullException(nameof(source));
            // A model belongs to one run. Fail loudly instead of leaking evidence into a restarted runner.
            if (clock.Mode != ClockMode.Preview && run.HasValue && source.Status?.StartedAt != run) throw new InvalidOperationException("runner changed; create a new HarborObservedModel for the new run");
            if (clock.Mode != ClockMode.Preview && source.Status?.StartedAt != null) run = source.Status.StartedAt;
            var alarms = source.Alarms;
            var status = source.Status;
            var rollout = source.Rollout;
            CommandHistory.Observe(source.Commands);
            tides.Update(source, now); lamps.Update(source, now);
            var beaconFrame = beacons.Update(alarms, now);
            var counts = new Dictionary<string, int>();
            var all = new Dictionary<string, IReadOnlyDictionary<string, Reading>>();
            var pumpFrame = new Dictionary<string, PumpPresentation>();
            var chargerFrame = new Dictionary<string, ChargerBoard>();
            var linkedFrame = new Dictionary<string, LinkedChargerBoard>();
            var pullFrame = new Dictionary<string, PullLampState>();
            foreach (var d in layout.Devices)
            {
                var readings = new Dictionary<string, Reading>();
                foreach (var m in MetricsFor(d.Kind)) if (source.TryGet(d.ExternalId, m, out var r)) readings[m] = r;
                all.Add(d.ExternalId, ReadOnly(readings));
                var keys = new HashSet<string>();
                foreach (var a in beaconFrame) if (a.Device == d.ExternalId && !a.Cleared) keys.Add(a.Key);
                counts.Add(d.ExternalId, keys.Count);
                if (d.Kind == DeviceKind.Pump)
                {
                    if (!pumps.TryGetValue(d.ExternalId, out var evidence)) pumps.Add(d.ExternalId, evidence = new PumpStartEvidence());
                    var wet = Get(readings, Metrics.WetWell); var running = Get(readings, Metrics.PumpRunning);
                    evidence.Update(d.ExternalId, running, wet, CommandHistory, source.CommandPoll);
                    pumpFrame.Add(d.ExternalId, new PumpPresentation { WetWell = wet, Running = running, Flow = Get(readings, Metrics.Flow), Current = Get(readings, Metrics.PumpCurrent),
                        WellAlarmRule = source.TryAttribute(d.ExternalId, "well_alarm_m", out var attr) ? attr : (Reading?)null,
                        StartKind = evidence.Kind, StartLabel = evidence.Label, TransitionAt = evidence.TransitionAt });
                }
                if (d.Kind == DeviceKind.Charger)
                {
                    source.TryToken(d.ExternalId, out var token);
                    var ota = rollout?.Target(token);
                    var power = Get(readings, Metrics.ChargerPower);
                    var session = Get(readings, Metrics.SessionActive);
                    chargerFrame.Add(d.ExternalId, new ChargerBoard { Power = power, MaxKw = Get(readings, Metrics.MaxKw), SessionActive = session, SessionKnown = session.HasValue && now - session.Value.OccurredAt <= LampBoard.Silence,
                        ConnectorTemperature = Get(readings, Metrics.ConnectorTemp), Stale = power.HasValue && now - power.Value.OccurredAt > LampBoard.Silence,
                        OtaOutcome = ota?.Outcome, AttemptState = ota?.AttemptState, OtaObservedAt = rollout?.ObservedAt });
                }
                if (DeviceKind.IsLinkedCharger(d.Kind)) linkedFrame.Add(d.ExternalId, LinkedChargerBoard.From(Get(readings, Metrics.ChargerPower), now));
                if (d.Kind == DeviceKind.PullLight) pullFrame.Add(d.ExternalId, PullLampState.From(Get(readings, Metrics.Dim), now));
                if (d.Kind == DeviceKind.Truck && source.TryLocation(d.ExternalId, out var fix) && fix.Latitude.HasValue && fix.Longitude.HasValue && (!truckFix.HasValue || fix.OccurredAt >= truckFix.Value.OccurredAt)) truckFix = fix;
            }
            var tideFrame = new Dictionary<string, RegionLevel>();
            foreach (var t in tides.Regions) tideFrame.Add(t.Quay, new RegionLevel { Quay = t.Quay, LevelM = t.LevelM, Contributors = t.Contributors, Gauges = t.Gauges, Held = t.Held, NewestAt = t.NewestAt });
            var lampFrame = new Dictionary<string, LampState>();
            foreach (var pair in lamps.Lamps) { var l = pair.Value; lampFrame.Add(pair.Key, new LampState { Condition = l.Condition, DimPct = l.DimPct, Fault = l.Fault, LastAt = l.LastAt }); }
            var flood = false;
            foreach (var a in alarms) if (a.Active && a.Key == "truck-in-flood-plain") flood = true;
            StoryJob job = null;
            foreach (var j in status?.Jobs ?? Array.Empty<StoryJob>()) if (j.Name == "lamp-service" && j.Day == clock.Day) job = j;
            var lampId = ExternalId(job?.Device); source.TryToken(lampId, out var lampToken);
            string flickerSeverity = null;
            foreach (var a in alarms) if (a.Device == lampId && a.Key == "lamp-flicker" && a.Active) flickerSeverity = a.Severity;
            var world = StoryCues.At(layout, status, clock, settleWorldFades);
            return new HarborFrame { Overtopping = WaterMotion.Overtopping(tides.Regions, layout.Levels.QuayFloodingM), ReflectionLevelM = WaterMotion.ReflectionLevel(tides.Regions), Spray = motion.Update(tides.Regions, world.Weather.Storm), Clock = clock, ObservedAt = now, World = world, Tides = ReadOnly(tideFrame), Lamps = ReadOnly(lampFrame),
                Pumps = ReadOnly(pumpFrame), Chargers = ReadOnly(chargerFrame), LinkedChargers = ReadOnly(linkedFrame), PullLamps = ReadOnly(pullFrame), Measurements = ReadOnly(all), AlarmCounts = ReadOnly(counts), Beacons = beaconFrame, Commands = CommandHistory.Snapshot(),
                Rollout = rollout, Truck = TruckTrack.From(plan, truckFix, flood, now), Replacement = Replacement(source, all, now), LampService = LampService.From(job, lampToken, source.Suppressions, source.AlarmObservation, flickerSeverity, now), ReadProblem = source.ReadProblem };
        }
        string ExternalId(string runnerDevice)
        {
            foreach (var d in layout.Devices) if (d.Token == runnerDevice || d.ExternalId == runnerDevice) return d.ExternalId;
            return null;
        }
        ReplacementPlate Replacement(IHarborSource source, Dictionary<string, IReadOnlyDictionary<string, Reading>> all, DateTimeOffset now)
        {
            var r = source.Status?.Replacement;
            if (r == null) return null;
            var id = ExternalId(r.Device); source.TryToken(id, out var token);
            if (replacementAt != r.At || replacementToken != token) { replacedReadings.Clear(); newestReplacement = null; replacementAt = r.At; replacementToken = token; }
            if (id != null && token != null && all.TryGetValue(id, out var values))
                foreach (var v in values)
                    if (v.Value.OccurredAt > r.At)
                    {
                        replacedReadings.Add((v.Key, v.Value.OccurredAt));
                        if (!newestReplacement.HasValue || v.Value.OccurredAt > newestReplacement.Value) newestReplacement = v.Value.OccurredAt;
                    }
            return new ReplacementPlate { DeclaredSimSeconds = source.Status.StartedAt.HasValue ? source.Status.ToSimSeconds(r.At) : (double?)null, RunnerDeclaration = r, ExternalId = id, BoundToken = token, ObservedReadingsAfter = replacedReadings.Count, NewestOccurredAt = newestReplacement,
                NewestAge = newestReplacement.HasValue ? now - newestReplacement.Value : (TimeSpan?)null };
        }
        static Reading? Get(IReadOnlyDictionary<string, Reading> readings, string name) => readings.TryGetValue(name, out var r) ? r : (Reading?)null;
        static IReadOnlyDictionary<string, T> ReadOnly<T>(Dictionary<string, T> d) => new ReadOnlyDictionary<string, T>(d);
    }
}
