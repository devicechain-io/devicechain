// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>
    /// One recorded live day, loaded once and read at any recorded instant. The observations go into one
    /// <see cref="ObservationStore"/>, whose views answer "what had been observed by second t", so a cut can jump between
    /// spans without replaying the day; the <c>/status</c> samples map a recorded second to the runner's clock. Nothing here
    /// is invented: a value the recorder did not write (an alarm's severity or the truck's fixes in a recording made before
    /// those were sampled) stays absent.
    /// </summary>
    public sealed class RecordedDay
    {
        public sealed class WorldSample
        {
            public double WallS, SimHour;
            public string Beat, LayoutDigest;
            public string[] Defects = Array.Empty<string>();
            public HarborLocation? Truck;
            /// <summary>Severity and acknowledgement by "externalId|alarmKey", when the recorder sampled them; else null.</summary>
            public Dictionary<string, KeyValuePair<string, bool>> Alarms;
        }
        sealed class Series
        {
            public readonly List<Observation> Rows = new List<Observation>();
            // Newest (by occurrence) among Rows[0..i]: buffered readings arrive late and must not replace a newer one.
            public readonly List<Observation> Newest = new List<Observation>();
        }
        readonly Dictionary<string, Series> measures = new Dictionary<string, Series>(StringComparer.Ordinal);
        readonly List<Observation> other = new List<Observation>();
        readonly List<WorldSample> world = new List<WorldSample>();
        public ObservationStore Store { get; } = new ObservationStore();
        public string Run { get; private set; }
        public DateTimeOffset StartedAt { get; private set; }
        /// <summary>The wall instant of recorded second 0.</summary>
        public DateTimeOffset Epoch { get; private set; }
        public double EndS { get; private set; }
        public IReadOnlyList<WorldSample> World => world;
        /// <summary>Every observation that is not a stream measurement, in observation order.</summary>
        public IReadOnlyList<Observation> Records => other;
        public DateTimeOffset At(double wallS) => Epoch.AddSeconds(wallS);

        /// <summary>Reads recorder streams (or one compact file holding every row). Checkpoints are not needed.</summary>
        public static RecordedDay Load(params TextReader[] streams)
        {
            var day = new RecordedDay(); var all = new List<Observation>();
            foreach (var input in streams)
            {
                string line;
                while ((line = input.ReadLine()) != null)
                {
                    if (line.Length == 0) continue;
                    try
                    {
                        using (var doc = JsonDocument.Parse(line))
                        {
                            var e = doc.RootElement; var kind = StrictJson.Str(e, "kind"); var at = StrictJson.Num(e, "wallS"); var data = e.GetProperty("data");
                            if (at > day.EndS) day.EndS = at;
                            if (kind == "observation") all.Add(DirectorWire.ReadObservation(data));
                            else if (kind == "coverage") day.Store.AddCoverage(DirectorWire.ReadCoverage(data));
                            else if (kind == "world") day.world.Add(ReadWorld(at, data));
                            else if (kind != "director" && kind != "consumed") throw new FormatException("unknown recording kind " + kind);
                        }
                    }
                    catch (JsonException ex) { throw new FormatException("recording NDJSON: " + ex.Message, ex); }
                }
            }
            if (day.world.Count == 0) throw new FormatException("the recording has no /status samples");
            day.world.Sort((a, b) => a.WallS.CompareTo(b.WallS));
            var started = all.FirstOrDefault(o => o.Kind == "ledger" && o.Key == "run") ?? throw new FormatException("the recording does not name its run");
            day.Run = started.Run;
            day.StartedAt = DateTimeOffset.Parse(day.Run, CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind);
            day.Epoch = day.StartedAt.AddSeconds(-started.OccurredAtS);
            foreach (var o in all.OrderBy(o => o.ObservedAtS))
            {
                if (o.Run != day.Run) throw new FormatException("the recording holds more than one run");
                day.Store.Add(o);
                if (o.Kind != "measure") { day.other.Add(o); continue; }
                if (o.Key == "backfill") continue; // the same reading, re-keyed for the restore tally
                var key = o.Device + "|" + o.Key;
                if (!day.measures.TryGetValue(key, out var series)) day.measures.Add(key, series = new Series());
                var newest = series.Newest.Count == 0 || o.OccurredAtS >= series.Newest[series.Newest.Count - 1].OccurredAtS ? o : series.Newest[series.Newest.Count - 1];
                series.Rows.Add(o); series.Newest.Add(newest);
            }
            return day;
        }
        static WorldSample ReadWorld(double at, JsonElement data)
        {
            var s = new WorldSample { WallS = at, SimHour = StrictJson.Num(data, "simHour") };
            if (data.TryGetProperty("beat", out var beat) && beat.ValueKind == JsonValueKind.String) s.Beat = beat.GetString();
            if (data.TryGetProperty("layoutDigest", out var digest) && digest.ValueKind == JsonValueKind.String) s.LayoutDigest = digest.GetString();
            if (data.TryGetProperty("defects", out var defects)) s.Defects = defects.EnumerateArray().Select(v => v.GetString()).ToArray();
            if (data.TryGetProperty("truck", out var t) && t.ValueKind == JsonValueKind.Object)
            {
                double? N(string key) => t.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.Number ? v.GetDouble() : (double?)null;
                s.Truck = new HarborLocation(N("lat"), N("lon"), N("heading"), N("speed"), DateTimeOffset.Parse(StrictJson.Str(t, "at"), CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind));
            }
            if (data.TryGetProperty("alarms", out var alarms) && alarms.ValueKind == JsonValueKind.Array)
            {
                s.Alarms = new Dictionary<string, KeyValuePair<string, bool>>(StringComparer.Ordinal);
                foreach (var a in alarms.EnumerateArray())
                    s.Alarms[StrictJson.Str(a, "device") + "|" + StrictJson.Str(a, "key")] = new KeyValuePair<string, bool>(StrictJson.Str(a, "severity"), a.GetProperty("acknowledged").GetBoolean());
            }
            return s;
        }
        int WorldIndex(double wallS)
        {
            int lo = 0, hi = world.Count - 1;
            while (lo < hi) { var mid = (lo + hi + 1) / 2; if (world[mid].WallS <= wallS) lo = mid; else hi = mid - 1; }
            return lo;
        }
        /// <summary>The newest /status sample at or before the instant (the first one before any).</summary>
        public WorldSample WorldAt(double wallS) => world[WorldIndex(wallS)];
        /// <summary>Absolute sim hours (26 is the following night's 02:00), interpolated between /status samples.</summary>
        public double SimAt(double wallS)
        {
            var i = WorldIndex(wallS); var a = world[i];
            if (wallS <= a.WallS || i + 1 >= world.Count) return a.SimHour;
            var b = world[i + 1];
            return b.WallS <= a.WallS ? a.SimHour : a.SimHour + (b.SimHour - a.SimHour) * (wallS - a.WallS) / (b.WallS - a.WallS);
        }
        /// <summary>The newest reading of a device's metric that had been observed by the instant.</summary>
        public bool TryMeasure(string device, string key, double wallS, out Observation reading)
        {
            reading = null;
            if (!measures.TryGetValue(device + "|" + key, out var series)) return false;
            int lo = 0, hi = series.Rows.Count;
            while (lo < hi) { var mid = (lo + hi) / 2; if (series.Rows[mid].ObservedAtS <= wallS) lo = mid + 1; else hi = mid; }
            if (lo == 0) return false;
            reading = series.Newest[lo - 1]; return true;
        }
        /// <summary>
        /// The day as one small stream: every observation and /status sample, and the coverage as the store merged it (a live
        /// recording writes about seven coverage rows a frame; merged they are a handful). <see cref="Load"/> reads it back.
        /// </summary>
        public void WriteCompact(TextWriter output, Func<Observation, bool> keep = null)
        {
            long sequence = 0;
            void Row(double at, string kind, Action<Utf8JsonWriter> data) => output.WriteLine(DirectorWire.Json(w =>
            { w.WriteStartObject(); w.WriteNumber("wallS", at); w.WriteNumber("sequence", ++sequence); w.WriteString("kind", kind); w.WritePropertyName("data"); data(w); w.WriteEndObject(); }));
            foreach (var c in Store.Coverage.OrderBy(c => c.Stream, StringComparer.Ordinal).ThenBy(c => c.Gap).ThenBy(c => c.FromSim)) Row(0, "coverage", w => DirectorWire.Coverage(w, c));
            foreach (var o in Store.Records.OrderBy(o => o.ObservedAtS).ThenBy(o => o.EventKey, StringComparer.Ordinal)) if (keep == null || keep(o)) Row(o.ObservedAtS, "observation", w => DirectorWire.Observation(w, o));
            foreach (var s in world) Row(s.WallS, "world", w =>
            {
                w.WriteStartObject(); w.WriteString("sample", "/status"); w.WriteNumber("simHour", s.SimHour); w.WriteString("beat", s.Beat); w.WriteString("layoutDigest", s.LayoutDigest);
                w.WriteStartArray("defects"); foreach (var d in s.Defects) w.WriteStringValue(d); w.WriteEndArray(); w.WriteEndObject();
            });
            Row(EndS, "world", w => { w.WriteStartObject(); w.WriteString("sample", "end"); w.WriteNumber("simHour", SimAt(EndS)); w.WriteEndObject(); });
        }
    }

    /// <summary>
    /// The scene's source in a replay: the recorded day as it stood at one recorded instant. It answers from recorded
    /// observations only. What a recording does not hold is reported as absent or unrecorded, never filled in: a severity the
    /// recorder did not sample reads <see cref="UnrecordedSeverity"/>, and the truck has no fix unless fixes were sampled.
    /// </summary>
    public sealed class ReplaySource : IHarborSource
    {
        public const string UnrecordedSeverity = "UNRECORDED";
        readonly RecordedDay day;
        readonly HarborLayout layout;
        readonly Dictionary<string, string> tokens = new Dictionary<string, string>(StringComparer.Ordinal), externals = new Dictionary<string, string>(StringComparer.Ordinal);
        readonly StoryJob[] jobs;
        readonly StoryReplacement replacement;
        double wallS;
        int cursor;
        readonly Dictionary<string, List<Observation>> seen = new Dictionary<string, List<Observation>>(StringComparer.Ordinal);
        ScenarioStatus status;
        List<HarborAlarm> alarms; List<HarborCommand> commands; RolloutEvidence rollout; List<SuppressionEvidence> suppressions;
        public ReplaySource(RecordedDay day, HarborLayout layout)
        {
            this.day = day; this.layout = layout;
            foreach (var d in layout.Devices) { tokens[d.ExternalId] = d.Token; externals[d.Token] = d.ExternalId; }
            // The runner's plans were served by /status from the start of the day; they are SCENARIO records, never evidence.
            var plan = day.Records.Where(o => o.Source == EvidenceSource.Scenario).ToArray();
            jobs = plan.Where(o => o.Kind == "window" && o.Status == "ACTIVE").Select(open =>
            {
                var close = plan.FirstOrDefault(o => o.Kind == "window" && o.Status == "ENDED" && o.Identity == open.Identity);
                return new StoryJob { Name = "lamp-service", Device = open.Device, Window = open.Identity, WindowOpens = day.At(open.OccurredAtS), WindowCloses = close == null ? (DateTimeOffset?)null : day.At(close.OccurredAtS) };
            }).Where(j => j.WindowCloses.HasValue).ToArray();
            var swap = plan.FirstOrDefault(o => o.Kind == "replacement");
            if (swap != null) replacement = new StoryReplacement { Device = swap.Device, At = day.At(swap.OccurredAtS), UnitIdentifier = "" };
            Seek(0);
        }
        public double WallS => wallS;
        public DateTimeOffset Now => day.At(wallS);
        /// <summary>Moves to a recorded instant. Going back rebuilds from the start of the day.</summary>
        public void Seek(double at)
        {
            if (at < wallS) { cursor = 0; seen.Clear(); }
            wallS = at;
            var records = day.Records;
            while (cursor < records.Count && records[cursor].ObservedAtS <= at)
            {
                var o = records[cursor++];
                if (!seen.TryGetValue(o.Kind, out var list)) seen.Add(o.Kind, list = new List<Observation>());
                list.Add(o);
            }
            alarms = null; commands = null; rollout = null; suppressions = null;
            var sample = day.WorldAt(at); var sim = day.SimAt(at);
            StoryOta ota = null;
            var declared = Seen("rollout").FirstOrDefault();
            if (declared != null) ota = new StoryOta { Rollout = declared.Identity, ScheduleHour = layout.Cues.RolloutSim, MaxInFlight = int.TryParse(declared.Field("maxInFlight"), NumberStyles.Integer, CultureInfo.InvariantCulture, out var cap) ? cap : 0 };
            status = ScenarioStatus.Recorded(day.StartedAt, layout.DayLengthSeconds, layout.StartHour, sim, sample.Beat, sample.LayoutDigest, sample.Defects, jobs, replacement, ota);
        }
        IReadOnlyList<Observation> Seen(string kind) => seen.TryGetValue(kind, out var list) ? (IReadOnlyList<Observation>)list : Array.Empty<Observation>();
        string External(string token) => externals.TryGetValue(token ?? "", out var id) ? id : token;
        public ScenarioStatus Status => status;
        public bool TryGet(string externalId, string metric, out Reading reading)
        {
            reading = default;
            if (!tokens.TryGetValue(externalId, out var token) || !day.TryMeasure(token, metric, wallS, out var o)) return false;
            if (!double.TryParse(o.Field("value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var value)) return false;
            reading = new Reading(value, day.At(o.OccurredAtS)); return true;
        }
        public IReadOnlyList<HarborAlarm> Alarms
        {
            get
            {
                if (alarms != null) return alarms;
                alarms = new List<HarborAlarm>(); var sampled = day.WorldAt(wallS).Alarms;
                foreach (var cycle in Seen("alarm").GroupBy(o => o.Identity + "|" + o.Cycle))
                {
                    var raised = cycle.FirstOrDefault(o => o.Status == "RAISED"); if (raised == null) continue;
                    var cleared = cycle.FirstOrDefault(o => o.Status == "CLEARED"); var id = External(raised.Device);
                    var severity = UnrecordedSeverity; var acknowledged = false;
                    if (sampled != null && sampled.TryGetValue(id + "|" + raised.Key, out var s)) { severity = s.Key; acknowledged = s.Value; }
                    var last = cycle.OrderBy(o => o.OccurredAtS).Last();
                    alarms.Add(new HarborAlarm(id, raised.Key, cleared != null ? "CLEARED" : "ACTIVE", severity, acknowledged, day.At(last.OccurredAtS), day.At(cycle.Max(o => o.ObservedAtS))));
                }
                // One row per device and key, as the live observer holds them: the newest cycle wins.
                alarms = alarms.GroupBy(a => a.Device + "|" + a.Key).Select(g => g.OrderBy(a => a.OccurredAt).Last()).ToList();
                return alarms;
            }
        }
        public IReadOnlyList<HarborCommand> Commands
        {
            get
            {
                if (commands != null) return commands;
                commands = new List<HarborCommand>();
                foreach (var c in Seen("command").GroupBy(o => o.Command))
                {
                    DateTimeOffset? When(Func<Observation, bool> pick) { var o = c.FirstOrDefault(pick); return o == null ? (DateTimeOffset?)null : day.At(o.OccurredAtS); }
                    var last = c.OrderBy(o => o.OccurredAtS).ThenBy(o => o.ObservedAtS).Last();
                    commands.Add(new HarborCommand(External(last.Device), c.Key, last.Key, last.Status, day.At(last.ObservedAtS), When(o => o.Status == "QUEUED"), When(o => o.Status == "SENT"),
                        When(o => o.Status != "QUEUED" && o.Status != "SENT"), last.Device));
                }
                return commands;
            }
        }
        /// <summary>Positive only where the recording's own pump coverage is: since the run began, or over the last two sim minutes.</summary>
        public CommandCoverage CommandPoll
        {
            get
            {
                var sim = day.SimAt(wallS); var first = day.World[0];
                if (day.Store.Covers(day.Run, "pump", first.SimHour + 0.05, sim)) return new CommandCoverage(day.At(first.WallS), Now);
                var back = 2.0 / 60;
                return day.Store.Covers(day.Run, "pump", sim - back, sim) ? new CommandCoverage(Now.AddSeconds(-back * layout.DayLengthSeconds / 24), Now) : default;
            }
        }
        public AlarmCoverage AlarmObservation
        {
            get { var sim = day.SimAt(wallS); var live = day.Store.Covers(day.Run, "alarms", sim, sim); return new AlarmCoverage(live, live); }
        }
        public bool TryToken(string externalId, out string token) => tokens.TryGetValue(externalId ?? "", out token);
        public bool TryLocation(string externalId, out HarborLocation location)
        {
            location = default; var fix = day.WorldAt(wallS).Truck;
            if (!fix.HasValue || !tokens.TryGetValue(externalId ?? "", out var token) || layout.Devices.First(d => d.Token == token).Kind != DeviceKind.Truck) return false;
            location = fix.Value; return true;
        }
        public RolloutEvidence Rollout
        {
            get
            {
                if (rollout != null) return rollout;
                var states = Seen("rollout"); if (states.Count == 0) return null;
                var first = states[0]; var targets = new List<OtaTarget>();
                foreach (var t in Seen("target").Where(o => o.Field("rolloutToken") == first.Identity).GroupBy(o => o.Device))
                {
                    var attempt = Seen("attempt").LastOrDefault(o => o.Device == t.Key && o.Field("rolloutToken") == first.Identity);
                    targets.Add(new OtaTarget { DeviceToken = t.Key, Outcome = t.Last().Status, AttemptState = attempt?.Status });
                }
                int Count(string outcome) => targets.Count(t => t.Outcome == outcome);
                var state = states.Any(o => o.Status == "TERMINAL") ? "COMPLETED" : "RUNNING";
                rollout = new RolloutEvidence { Token = first.Identity, State = state, MaxInFlight = int.TryParse(first.Field("maxInFlight"), NumberStyles.Integer, CultureInfo.InvariantCulture, out var cap) ? cap : 0,
                    ObservedAt = day.At(Math.Max(states.Max(o => o.ObservedAtS), Seen("target").Select(o => o.ObservedAtS).DefaultIfEmpty(0).Max())), Targets = targets, TotalTargets = targets.Count,
                    Progress = new Dictionary<string, int> { ["total"] = targets.Count, ["pending"] = Count("PENDING"), ["inFlight"] = Count("IN_FLIGHT"), ["succeeded"] = Count("SUCCEEDED"), ["failed"] = Count("FAILED"),
                        ["unknown"] = 0, ["cancelled"] = Count("CANCELLED"), ["skipped"] = Count("SKIPPED") } };
                return rollout;
            }
        }
        public IReadOnlyList<SuppressionEvidence> Suppressions
        {
            get
            {
                if (suppressions != null) return suppressions;
                suppressions = new List<SuppressionEvidence>();
                foreach (var w in Seen("window").Where(o => o.Source == EvidenceSource.Platform).GroupBy(o => o.Identity))
                {
                    var open = w.FirstOrDefault(o => o.Status == "ACTIVE"); if (open == null) continue;
                    var ended = w.FirstOrDefault(o => o.Status == "ENDED"); var start = day.At(open.OccurredAtS);
                    // The window's planned end is not in a recording; until its end is recorded it is open-ended here.
                    suppressions.Add(new SuppressionEvidence { Token = w.Key, Status = ended != null ? "ENDED" : "ACTIVE", Start = start, End = ended != null ? day.At(ended.OccurredAtS) : start.AddDays(1),
                        EndedAt = ended == null ? (DateTimeOffset?)null : day.At(ended.OccurredAtS), ObservedAt = day.At(w.Max(o => o.ObservedAtS)),
                        Targets = w.Select(o => o.Device).Distinct().Select(d => new SuppressionTarget("DEVICE", d)).ToArray(), AlarmKeys = w.Select(o => o.Key).Distinct().ToArray() });
                }
                return suppressions;
            }
        }
        public bool TryAttribute(string externalId, string key, out Reading value) { value = default; return false; }
        public string ReadProblem => null;
    }
}
