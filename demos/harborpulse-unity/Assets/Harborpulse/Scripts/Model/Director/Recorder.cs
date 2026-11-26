// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>Explicit wire codecs: also used by checkpoints, no serializer metadata or reflection.</summary>
    public static class DirectorWire
    {
        public static string Json(Action<Utf8JsonWriter> write)
        {
            using (var stream = new MemoryStream())
            { using (var writer = new Utf8JsonWriter(stream)) { write(writer); writer.Flush(); } return Encoding.UTF8.GetString(stream.ToArray()); }
        }
        public static void Observation(Utf8JsonWriter w, Observation o)
        {
            w.WriteStartObject();
            w.WriteString("run", o.Run); w.WriteString("kind", o.Kind); w.WriteString("identity", o.Identity); w.WriteString("device", o.Device);
            w.WriteString("key", o.Key); w.WriteString("cycle", o.Cycle); w.WriteString("command", o.Command); w.WriteString("attempt", o.Attempt);
            w.WriteString("status", o.Status); w.WriteNumber("observedAtS", o.ObservedAtS); w.WriteNumber("occurredAtS", o.OccurredAtS); w.WriteNumber("sim", o.Sim); w.WriteString("source", o.Source);
            w.WriteStartObject("fields"); foreach (var p in o.Fields.OrderBy(p => p.Key, StringComparer.Ordinal)) w.WriteString(p.Key, p.Value); w.WriteEndObject();
            w.WriteStartObject("sets"); foreach (var p in o.Sets.OrderBy(p => p.Key, StringComparer.Ordinal)) { w.WriteStartArray(p.Key); foreach (var s in p.Value.Distinct().OrderBy(x => x, StringComparer.Ordinal)) w.WriteStringValue(s); w.WriteEndArray(); } w.WriteEndObject();
            w.WriteEndObject();
        }
        public static Observation ReadObservation(JsonElement e)
        {
            StrictJson.Keys(e, "run", "kind", "identity", "device", "key", "cycle", "command", "attempt", "status", "observedAtS", "occurredAtS", "sim", "source", "fields", "sets");
            string S(string key) => e.GetProperty(key).GetString();
            return new Observation(S("run"), S("kind"), S("identity"), S("device"), S("key"), S("cycle"), S("command"), S("attempt"), S("status"),
                StrictJson.Num(e, "observedAtS"), StrictJson.Num(e, "occurredAtS"), StrictJson.Num(e, "sim"), S("source"),
                e.GetProperty("fields").EnumerateObject().ToDictionary(p => p.Name, p => p.Value.GetString(), StringComparer.Ordinal),
                e.GetProperty("sets").EnumerateObject().ToDictionary(p => p.Name, p => p.Value.EnumerateArray().Select(v => v.GetString()).ToArray(), StringComparer.Ordinal));
        }
        public static void Coverage(Utf8JsonWriter w, CoverageInterval c)
        { w.WriteStartObject(); w.WriteString("run", c.Run); w.WriteString("stream", c.Stream); w.WriteNumber("fromSim", c.FromSim); w.WriteNumber("toSim", c.ToSim); w.WriteBoolean("gap", c.Gap); w.WriteEndObject(); }
        public static CoverageInterval ReadCoverage(JsonElement e)
        { StrictJson.Keys(e, "run", "stream", "fromSim", "toSim", "gap"); return new CoverageInterval(StrictJson.Str(e, "run"), StrictJson.Str(e, "stream"), StrictJson.Num(e, "fromSim"), StrictJson.Num(e, "toSim"), e.GetProperty("gap").GetBoolean()); }
        public static void Event(Utf8JsonWriter w, DirectorEvent e)
        {
            w.WriteStartObject(); w.WriteNumber("showS", e.ShowS); w.WriteString("kind", e.Kind); w.WriteString("page", e.Page); w.WriteString("value", e.Value);
            w.WriteStartArray("evidence"); foreach (var key in e.Evidence) w.WriteStringValue(key); w.WriteEndArray(); w.WriteEndObject();
        }
        public static DirectorEvent ReadEvent(JsonElement e)
        { StrictJson.Keys(e, "showS", "kind", "page", "value", "evidence"); return new DirectorEvent(StrictJson.Num(e, "showS"), StrictJson.Str(e, "kind"), e.GetProperty("page").GetString(), e.GetProperty("value").GetString(), StrictJson.Array(e, "evidence").Select(v => v.GetString())); }
        public static void Canonical(Utf8JsonWriter w, JsonElement e)
        {
            if (e.ValueKind == JsonValueKind.Object) { w.WriteStartObject(); foreach (var p in e.EnumerateObject().OrderBy(p => p.Name, StringComparer.Ordinal)) { w.WritePropertyName(p.Name); Canonical(w, p.Value); } w.WriteEndObject(); }
            else if (e.ValueKind == JsonValueKind.Array) { w.WriteStartArray(); foreach (var v in e.EnumerateArray()) Canonical(w, v); w.WriteEndArray(); }
            else e.WriteTo(w);
        }
    }

    public sealed class RecordedState
    {
        public ObservationStore Observed { get; } = new ObservationStore();
        public HashSet<string> Consumed { get; } = new HashSet<string>(StringComparer.Ordinal);
        public Dictionary<string, DirectorEvent> Decisions { get; } = new Dictionary<string, DirectorEvent>(StringComparer.Ordinal);
        public string World { get; internal set; } = "null";
        public string VisibleCaption(string page)
        {
            if (!Decisions.TryGetValue(page + ":caption", out var caption)) return null;
            if (Decisions.TryGetValue(page + ":caption-end", out var end) && end.ShowS >= caption.ShowS) return null;
            return caption.Value;
        }
        internal void Apply(string kind, JsonElement data)
        {
            switch (kind)
            {
                case "observation": Observed.Add(DirectorWire.ReadObservation(data)); break;
                case "coverage": Observed.AddCoverage(DirectorWire.ReadCoverage(data)); break;
                case "world": World = DirectorWire.Json(w => DirectorWire.Canonical(w, data)); break;
                case "director":
                    var e = DirectorWire.ReadEvent(data);
                    if (e.Kind == "caption") Decisions.Remove(e.Page + ":caption-end");
                    if (e.Kind == "caption-end") Decisions.Remove(e.Page + ":caption");
                    Decisions[e.Page + ":" + e.Kind + (e.Kind == "node" || e.Kind == "node-label" || e.Kind == "link" ? ":" + e.Value : "")] = e; foreach (var key in e.Evidence) Consumed.Add(key); break;
                case "consumed": foreach (var key in data.EnumerateArray()) Consumed.Add(key.GetString()); break;
                default: throw new FormatException("unknown recording kind " + kind);
            }
        }
        public string Serialize() => DirectorWire.Json(Write);
        internal void Write(Utf8JsonWriter w)
        {
            w.WriteStartObject();
            w.WriteStartArray("observations"); foreach (var o in Observed.Records.OrderBy(o => o.EventKey, StringComparer.Ordinal).ThenBy(o => o.ObservedAtS)) DirectorWire.Observation(w, o); w.WriteEndArray();
            w.WriteStartArray("coverage"); foreach (var c in Observed.Coverage.OrderBy(c => c.Run, StringComparer.Ordinal).ThenBy(c => c.Stream, StringComparer.Ordinal).ThenBy(c => c.FromSim).ThenBy(c => c.ToSim).ThenBy(c => c.Gap)) DirectorWire.Coverage(w, c); w.WriteEndArray();
            w.WritePropertyName("world"); using (var d = JsonDocument.Parse(World)) DirectorWire.Canonical(w, d.RootElement);
            w.WriteStartArray("decisions"); foreach (var p in Decisions.OrderBy(p => p.Key, StringComparer.Ordinal)) DirectorWire.Event(w, p.Value); w.WriteEndArray();
            w.WriteStartArray("consumed"); foreach (var key in Consumed.OrderBy(x => x, StringComparer.Ordinal)) w.WriteStringValue(key); w.WriteEndArray();
            w.WriteEndObject();
        }
        internal static RecordedState Read(JsonElement e)
        {
            StrictJson.Keys(e, "observations", "coverage", "world", "decisions", "consumed"); var s = new RecordedState();
            foreach (var o in StrictJson.Array(e, "observations")) s.Apply("observation", o);
            foreach (var c in StrictJson.Array(e, "coverage")) s.Apply("coverage", c);
            s.Apply("world", e.GetProperty("world")); foreach (var d in StrictJson.Array(e, "decisions")) s.Apply("director", d); s.Apply("consumed", e.GetProperty("consumed")); return s;
        }
        public string Hash()
        {
            using (var sha = SHA256.Create()) return string.Concat(sha.ComputeHash(Encoding.UTF8.GetBytes(Serialize())).Select(b => b.ToString("x2", CultureInfo.InvariantCulture)));
        }
    }

    /// <summary>Writers are caller-owned. All four streams are NDJSON; sequence resolves equal-time ordering.</summary>
    public sealed class Recorder
    {
        readonly TextWriter observed, world, director, checkpoints;
        readonly Action<Action> enqueue;
        readonly bool disabled;
        readonly RecordedState state = new RecordedState();
        long sequence;
        double lastS;
        double checkpointS;
        public Recorder(TextWriter observed, TextWriter world, TextWriter director, TextWriter checkpoints, Action<Action> enqueue = null)
        {
            this.observed = observed ?? throw new ArgumentNullException(nameof(observed)); this.world = world ?? throw new ArgumentNullException(nameof(world));
            this.director = director ?? throw new ArgumentNullException(nameof(director)); this.checkpoints = checkpoints ?? throw new ArgumentNullException(nameof(checkpoints));
            this.enqueue = enqueue;
            disabled = observed == TextWriter.Null && world == TextWriter.Null && director == TextWriter.Null && checkpoints == TextWriter.Null;
            Run(() => Checkpoint(0));
        }
        // An optional single-consumer dispatcher owns ALL state, serialization and checkpoint
        // work. Inputs are immutable; queue order preserves the synchronous recorder's bytes.
        void Run(Action work) { if (!disabled) { if (enqueue == null) work(); else enqueue(work); } }
        public void Advance(double wallS) => Run(() => AdvanceCore(wallS));
        void AdvanceCore(double wallS)
        {
            if (wallS < lastS || double.IsNaN(wallS) || double.IsInfinity(wallS)) throw new ArgumentException("recording time must increase");
            // A checkpoint holds the state as of its own second, so what is still pending reaches the stream before it is written.
            if (checkpointS + 60 <= wallS) FlushCoverage(true);
            while (checkpointS + 60 <= wallS) { checkpointS += 60; Checkpoint(checkpointS); }
            lastS = wallS;
        }
        void Checkpoint(double atS)
        {
            checkpoints.WriteLine(DirectorWire.Json(w => { w.WriteStartObject(); w.WriteNumber("wallS", atS); w.WriteNumber("sequence", sequence); w.WritePropertyName("state"); state.Write(w); w.WriteEndObject(); }));
            checkpoints.Flush();
        }
        // The director reports a stream's coverage every frame, and nearly always as the interval that continues the last one. Written
        // as it came, a day was three million rows (700 MB) that every frame allocated, serialised and parsed again, and a recording the
        // size of the day it described. A run of contiguous intervals of one stream is one row, written when the run breaks, when any
        // other row is written (so the order is the order things happened), when a checkpoint falls due, or CoverageFlushS after it
        // began. A reader merges coverage by interval, so what it reads back is the same coverage.
        public const double CoverageFlushS = 1.0;
        sealed class PendingCoverage { public CoverageInterval Interval; public double StartedS, WallS; }
        readonly Dictionary<(string, string, bool), PendingCoverage> pendingCoverage = new Dictionary<(string, string, bool), PendingCoverage>();
        // A row is written at the second its run last grew, but never before the last second already written (time only moves forward),
        // and, when a checkpoint is falling due, never after it (the checkpoint is the state as of its own second).
        void FlushCoverage(bool beforeCheckpoint = false)
        {
            if (pendingCoverage.Count == 0) return;
            var rows = pendingCoverage.Values.OrderBy(p => p.WallS).ToArray(); pendingCoverage.Clear();
            var cap = checkpointS + 60;
            foreach (var p in rows) AppendRaw(observed, Math.Max(lastS, beforeCheckpoint ? Math.Min(p.WallS, cap) : p.WallS), "coverage", w => DirectorWire.Coverage(w, p.Interval));
        }
        void CoverageCore(double wallS, CoverageInterval interval)
        {
            if (checkpointS + 60 <= wallS) FlushCoverage(true);
            var key = (interval.Run, interval.Stream, interval.Gap);
            if (pendingCoverage.TryGetValue(key, out var held))
            {
                if (interval.FromSim <= held.Interval.ToSim && interval.ToSim >= held.Interval.FromSim)
                {
                    held.Interval = new CoverageInterval(interval.Run, interval.Stream, Math.Min(held.Interval.FromSim, interval.FromSim), Math.Max(held.Interval.ToSim, interval.ToSim), interval.Gap);
                    held.WallS = wallS;
                    if (wallS - held.StartedS >= CoverageFlushS) FlushCoverage();
                    return;
                }
                FlushCoverage(); // every pending run, oldest first: rows never go back in time
            }
            pendingCoverage[key] = new PendingCoverage { Interval = interval, StartedS = wallS, WallS = wallS };
        }
        void Append(TextWriter output, double wallS, string kind, Action<Utf8JsonWriter> write)
        {
            FlushCoverage(); AppendRaw(output, wallS, kind, write);
        }
        void AppendRaw(TextWriter output, double wallS, string kind, Action<Utf8JsonWriter> write)
        {
            AdvanceCore(wallS); var payload = DirectorWire.Json(write); sequence++;
            output.WriteLine(DirectorWire.Json(w => { w.WriteStartObject(); w.WriteNumber("wallS", wallS); w.WriteNumber("sequence", sequence); w.WriteString("kind", kind); w.WritePropertyName("data"); using (var d = JsonDocument.Parse(payload)) d.RootElement.WriteTo(w); w.WriteEndObject(); }));
            output.Flush(); using (var d = JsonDocument.Parse(payload)) state.Apply(kind, d.RootElement);
        }
        public void Observe(double wallS, Observation observation) => Run(() => Append(observed, wallS, "observation", w => DirectorWire.Observation(w, observation)));
        public void Coverage(double wallS, CoverageInterval interval) => Run(() => CoverageCore(wallS, interval));
        public void World(double wallS, string statusJson) => Run(() => Append(world, wallS, "world", w => { using (var d = JsonDocument.Parse(statusJson)) DirectorWire.Canonical(w, d.RootElement); }));
        public void Decision(DirectorEvent e) => Run(() => Append(director, e.ShowS, "director", w => DirectorWire.Event(w, e)));
        public void End() => Run(() => Append(director, lastS, "director", w => DirectorWire.Event(w, new DirectorEvent(lastS, "recording-end", "", ""))));
        public void Consumption(double wallS, IEnumerable<string> keys)
        {
            if (disabled) return;
            var snapshot = keys.ToArray();
            Run(() => Append(director, wallS, "consumed", w => { w.WriteStartArray(); foreach (var key in snapshot.OrderBy(k => k, StringComparer.Ordinal)) w.WriteStringValue(key); w.WriteEndArray(); }));
        }
    }

    public sealed class RecordingReader
    {
        sealed class Row { public double WallS; public long Sequence; public string Kind; public JsonElement Data; }
        sealed class Checkpoint { public double WallS; public long Sequence; public JsonElement State; }
        readonly List<Row> rows = new List<Row>();
        readonly List<Checkpoint> checkpoints = new List<Checkpoint>();
        public double LastCheckpointLoadedS { get; private set; }
        public IReadOnlyList<double> CheckpointTimes => checkpoints.Select(c => c.WallS).ToArray();
        public RecordingReader(TextReader observed, TextReader world, TextReader director, TextReader checkpoints)
        {
            foreach (var input in new[] { observed, world, director }) ReadLines(input, e =>
            {
                StrictJson.Keys(e, "wallS", "sequence", "kind", "data"); rows.Add(new Row { WallS = StrictJson.Num(e, "wallS"), Sequence = e.GetProperty("sequence").GetInt64(), Kind = StrictJson.Str(e, "kind"), Data = e.GetProperty("data").Clone() });
            });
            ReadLines(checkpoints, e => { StrictJson.Keys(e, "wallS", "sequence", "state"); this.checkpoints.Add(new Checkpoint { WallS = StrictJson.Num(e, "wallS"), Sequence = e.GetProperty("sequence").GetInt64(), State = e.GetProperty("state").Clone() }); });
            rows.Sort((a, b) => { var time = a.WallS.CompareTo(b.WallS); return time != 0 ? time : a.Sequence.CompareTo(b.Sequence); });
            if (rows.Select(r => r.Sequence).Distinct().Count() != rows.Count) throw new FormatException("duplicate recording sequence");
            this.checkpoints.Sort((a, b) => a.WallS.CompareTo(b.WallS));
        }
        static void ReadLines(TextReader input, Action<JsonElement> read)
        {
            string line; while ((line = input.ReadLine()) != null)
            {
                try { using (var doc = JsonDocument.Parse(line)) read(doc.RootElement); }
                catch (JsonException e) { throw new FormatException("recording NDJSON: " + e.Message, e); }
            }
        }
        public RecordedState PlayTo(double wallS)
        { var state = new RecordedState(); foreach (var r in rows.Where(r => r.WallS <= wallS)) state.Apply(r.Kind, r.Data); return state; }
        public RecordedState Seek(double wallS)
        {
            var checkpoint = checkpoints.LastOrDefault(c => c.WallS <= wallS);
            var state = checkpoint == null ? new RecordedState() : RecordedState.Read(checkpoint.State);
            LastCheckpointLoadedS = checkpoint?.WallS ?? 0;
            foreach (var row in rows.Where(r => r.WallS <= wallS && (checkpoint == null || r.Sequence > checkpoint.Sequence))) state.Apply(row.Kind, row.Data);
            return state;
        }
        public RecordedState SeekCut(StoryCut cut, double showS)
        {
            if (showS < 0) throw new ArgumentOutOfRangeException(nameof(showS));
            var start = 0.0;
            foreach (var segment in cut.Segments)
            {
                if (showS <= start + segment.DurS || segment == cut.Segments.Last())
                {
                    var state = Seek(segment.RecordedAt(showS - start));
                    // Skipped spans consume events silently; borrowed shots use a reconstructed state too.
                    foreach (var row in rows.Where(r => r.Kind == "observation" && r.WallS < segment.RecordedWallFromS))
                        state.Consumed.Add(DirectorWire.ReadObservation(row.Data).EventKey);
                    return state;
                }
                start += segment.DurS;
            }
            throw new FormatException("empty cut " + cut.Id);
        }
    }
}
