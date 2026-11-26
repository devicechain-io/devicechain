// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Collections.ObjectModel;
using System.Globalization;
using System.Linq;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    public static class StrictJson
    {
        public static void Keys(JsonElement e, params string[] allowed)
        {
            if (e.ValueKind != JsonValueKind.Object) throw new FormatException("expected object");
            var seen = new HashSet<string>(StringComparer.Ordinal);
            foreach (var p in e.EnumerateObject())
                if (!seen.Add(p.Name) || !allowed.Contains(p.Name)) throw new FormatException("unknown or duplicate key " + p.Name);
        }
        public static string Str(JsonElement e, string key, string fallback = null)
        {
            if (!e.TryGetProperty(key, out var v)) { if (fallback != null) return fallback; throw new FormatException("missing " + key); }
            if (v.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(v.GetString())) throw new FormatException("expected string " + key);
            return v.GetString();
        }
        public static double Num(JsonElement e, string key, double fallback = double.NaN)
        {
            if (!e.TryGetProperty(key, out var v)) { if (!double.IsNaN(fallback)) return fallback; throw new FormatException("missing " + key); }
            if (v.ValueKind != JsonValueKind.Number || !v.TryGetDouble(out var n) || double.IsNaN(n) || double.IsInfinity(n)) throw new FormatException("expected finite number " + key);
            return n;
        }
        public static IEnumerable<JsonElement> Array(JsonElement e, string key)
        {
            if (!e.TryGetProperty(key, out var v) || v.ValueKind != JsonValueKind.Array) throw new FormatException("expected array " + key);
            return v.EnumerateArray();
        }
        public static string Scalar(JsonElement v)
        {
            if (v.ValueKind == JsonValueKind.String) return v.GetString();
            if (v.ValueKind == JsonValueKind.Number || v.ValueKind == JsonValueKind.True || v.ValueKind == JsonValueKind.False) return v.GetRawText();
            throw new FormatException("expected scalar field");
        }
    }

    public static class EvidenceSource
    {
        public const string Platform = "PLATFORM", Scenario = "SCENARIO", DeviceSide = "DEVICE-SIDE";
        public static void Check(string value)
        { if (value != Platform && value != Scenario && value != DeviceSide) throw new FormatException("unknown source " + value); }
    }

    /// <summary>Immutable detached observation. Times are wall seconds from recording start and absolute sim hours.</summary>
    public sealed class Observation
    {
        public static readonly string[] Kinds = { "alarm", "command", "chip", "measure", "attempt", "window", "replacement", "credential", "artifact", "rollout", "target", "membership", "ledger", "binding" };
        public Observation(string run, string kind, string identity, string device, string key, string cycle, string command,
            string attempt, string status, double observedAtS, double occurredAtS, double sim, string source,
            IReadOnlyDictionary<string, string> fields = null, IReadOnlyDictionary<string, string[]> sets = null)
        {
            if (!Kinds.Contains(kind)) throw new FormatException("unknown record kind " + kind);
            EvidenceSource.Check(source);
            if (string.IsNullOrEmpty(run) || string.IsNullOrEmpty(identity)) throw new FormatException("observation requires run and identity");
            if (new[] { observedAtS, occurredAtS, sim }.Any(x => double.IsNaN(x) || double.IsInfinity(x))) throw new FormatException("observation time must be finite");
            Run = run; Kind = kind; Identity = identity; Device = device ?? ""; Key = key ?? ""; Cycle = cycle ?? "";
            Command = command ?? ""; Attempt = attempt ?? ""; Status = status ?? ""; ObservedAtS = observedAtS;
            OccurredAtS = occurredAtS; Sim = sim; Source = source;
            Fields = new ReadOnlyDictionary<string, string>(fields == null ? new Dictionary<string, string>() : fields.ToDictionary(p => p.Key, p => p.Value, StringComparer.Ordinal));
            Sets = new ReadOnlyDictionary<string, string[]>(sets == null ? new Dictionary<string, string[]>() : sets.ToDictionary(p => p.Key, p => p.Value.Distinct().OrderBy(x => x, StringComparer.Ordinal).ToArray(), StringComparer.Ordinal));
        }
        public string Run { get; }
        public string Kind { get; }
        public string Identity { get; }
        public string Device { get; }
        public string Key { get; }
        public string Cycle { get; }
        public string Command { get; }
        public string Attempt { get; }
        public string Status { get; }
        public double ObservedAtS { get; }
        public double OccurredAtS { get; }
        public bool HasOccurrence => !Fields.ContainsKey("asOf");
        public double Sim { get; }
        public string Source { get; }
        public IReadOnlyDictionary<string, string> Fields { get; }
        public IReadOnlyDictionary<string, string[]> Sets { get; }
        static string Part(string s) => s.Length.ToString(CultureInfo.InvariantCulture) + ":" + s;
        // Observation time is deliberately absent: a resubscribe is the same event, and cycles are distinct.
        string snapshotKey, eventKey;
        public string SnapshotKey => snapshotKey ?? (snapshotKey = string.Concat(new[] { Run, Kind, Identity, Device, Key, Cycle, Command, Attempt, Source }.Select(Part)));
        public string EventKey => eventKey ?? (eventKey = string.Concat(new[] { Run, Kind, Identity, Device, Key, Cycle, Command, Attempt, Status, Source }.Select(Part)));
        public string Field(string name)
        {
            switch (name)
            {
                case "device": return Device; case "key": return Key; case "cycle": return Cycle;
                case "identity": return Identity; case "command": return Command; case "attempt": return Attempt;
                case "status": return Status; case "source": return Source;
                case "occurredAtS": return HasOccurrence ? OccurredAtS.ToString("R", CultureInfo.InvariantCulture) : null;
                default: return Fields.TryGetValue(name, out var v) ? v : null;
            }
        }
    }

    public sealed class CoverageInterval
    {
        public CoverageInterval(string run, string stream, double fromSim, double toSim, bool gap)
        { if (toSim < fromSim) throw new FormatException("reversed coverage interval"); Run = run; Stream = stream; FromSim = fromSim; ToSim = toSim; Gap = gap; }
        public string Run { get; }
        public string Stream { get; }
        public double FromSim { get; }
        public double ToSim { get; }
        public bool Gap { get; }
    }

    public sealed partial class ObservationStore
    {
        readonly Dictionary<string, Observation> records = new Dictionary<string, Observation>(StringComparer.Ordinal);
        IReadOnlyCollection<Observation> archive;
        public IReadOnlyCollection<Observation> Records => archive ?? (archive = new Archive(this));
        public bool Add(Observation o)
        {
            var key = o.EventKey + ":" + o.ObservedAtS.ToString("R", CultureInfo.InvariantCulture);
            if (records.ContainsKey(key)) return false;
            records[key] = o; Index(o); return true;
        }
        public void AddCoverage(CoverageInterval interval)
        {
            IndexCoverage(interval);
        }
        public bool Covers(string run, string stream, double from, double to)
        {
            return IndexedCovers(run, stream, from, to);
        }
    }

    public sealed class EvidenceResult
    {
        public bool Holds { get; internal set; }
        public string Error { get; internal set; }
        public IReadOnlyList<Observation> Records { get; internal set; } = Array.Empty<Observation>();
        public IReadOnlyDictionary<string, string> Bindings { get; internal set; } = new Dictionary<string, string>();
        internal Dictionary<string, Observation> Named { get; set; } = new Dictionary<string, Observation>();
    }

    public sealed class EvidenceContext
    {
        public EvidenceContext(string run, double fromSim, double toSim, double nowS, string source = EvidenceSource.Platform, Observation trigger = null, double? nowSim = null, double dayLengthSeconds = 1440, IReadOnlyDictionary<string, Observation> named = null, bool restrictInterval = false)
        { RestrictInterval = restrictInterval; NowSim = nowSim; DayLengthSeconds = dayLengthSeconds; Named = named ?? new Dictionary<string, Observation>(); Run = run; FromSim = fromSim; ToSim = toSim; NowS = nowS; Source = source; Trigger = trigger; }
        public string Run { get; }
        public double FromSim { get; }
        public double ToSim { get; }
        public double NowS { get; }
        public string Source { get; }
        public Observation Trigger { get; }
        public double? NowSim { get; }
        public double DayLengthSeconds { get; }
        internal bool RestrictInterval { get; }
        internal IReadOnlyDictionary<string, Observation> Named { get; }
        internal EvidenceContext With(double from, double to, string source = null, IReadOnlyDictionary<string, Observation> named = null, bool? restrictInterval = null) => new EvidenceContext(Run, from, to, NowS, source ?? Source, Trigger, NowSim, DayLengthSeconds, named ?? Named, restrictInterval ?? RestrictInterval);
        internal double ObservedSim(Observation o) => NowSim.HasValue ? NowSim.Value - (NowS - o.ObservedAtS) * 24 / DayLengthSeconds : o.Sim;
    }

    /// <summary>A closed predicate language; no executable code or reflection-based JSON.</summary>
    public sealed class Needs
    {
        static readonly string[] KnownFields = { "alarmToken", "artifactToken", "rolloutToken", "replacementToken", "beforeReading", "afterReading", "value", "runningAt", "freshDevices", "windowToken", "raisedDuringSuppression", "suppressionWindowToken", "policy", "afterClose", "alarmActive", "afterReplacement", "enabled", "max_kw", "falling", "rising", "trigger", "backdated", "readingIdentity", "signer", "code", "platform", "afterFailure", "updated", "refused", "maxInFlight", "device", "key", "identity", "cycle", "command", "attempt", "status", "source", "occurredAtS" };
        public string Kind { get; private set; }
        public string Mode { get; private set; } = "occurred";
        public string Alias { get; private set; }
        public string Before { get; private set; }
        public string After { get; private set; }
        public string Source { get; private set; }
        public IReadOnlyList<Needs> Children { get; private set; } = Array.Empty<Needs>();
        public IReadOnlyDictionary<string, string> Match { get; private set; } = new Dictionary<string, string>();
        public IReadOnlyDictionary<string, string> Bind { get; private set; } = new Dictionary<string, string>();
        public IReadOnlyList<string> Present { get; private set; } = Array.Empty<string>();
        public bool ThroughNow { get; private set; }
        /// <summary>A set that asks only that every expected member was observed (see the parser's note).</summary>
        public bool AtLeast { get; private set; }
        public double? Gte { get; private set; }
        public double? AllBelow { get; private set; }
        public string Until { get; private set; }
        public double? Count { get; private set; }
        public double? MaxAgeS { get; private set; }
        public double? FromSim { get; private set; }
        public double? ToSim { get; private set; }
        public string FromCue { get; private set; }
        public string ToCue { get; private set; }
        public string Stream { get; private set; }
        public string SetField { get; private set; }
        public string ExpectedField { get; private set; }
        public IEnumerable<string> BoundNames => Kind == "tally" ? Bind.Keys : Bind.Keys.Concat(Kind == "any" ? Children.Select(c => c.BoundNames).Aggregate((a, b) => a.Intersect(b)) : Children.SelectMany(c => c.BoundNames));
        public static Needs Parse(JsonElement e)
        {
            try { return Read(e); }
            catch (InvalidOperationException ex) { throw new FormatException("needs value: " + ex.Message, ex); }
            catch (ArgumentException ex) { throw new FormatException("needs field: " + ex.Message, ex); }
        }
        static Needs Read(JsonElement e)
        {
            if (e.ValueKind != JsonValueKind.Object || e.EnumerateObject().Count() != 1) throw new FormatException("needs: exactly one predicate kind");
            var p = e.EnumerateObject().First(); var n = new Needs { Kind = p.Name }; var body = p.Value;
            if (p.Name == "all" || p.Name == "any")
            {
                if (body.ValueKind != JsonValueKind.Array || body.GetArrayLength() == 0) throw new FormatException(p.Name + ": nonempty array required");
                n.Children = body.EnumerateArray().Select(Parse).ToArray(); return n;
            }
            if (body.ValueKind == JsonValueKind.Object) { if (body.TryGetProperty("fromCue", out _)) n.FromCue = StrictJson.Str(body, "fromCue"); if (body.TryGetProperty("toCue", out _)) n.ToCue = StrictJson.Str(body, "toCue"); }
            if (p.Name == "set")
            {
                StrictJson.Keys(body, "fromCue", "toCue", "observed", "expected", "field", "expectedField", "stream", "fromSim", "toSim", "cardinality", "atLeast");
                // atLeast: the observed set holds every expected member ("every buffered reading is stored"). Each member is its own
                // positive evidence, so a record beyond the expected ones does not refute it and no stream coverage is asked: a gap in
                // the stream can hide a record, never invent one. Without it the two sets must be equal over a covered interval ("exactly
                // these"), which is what a claim that nothing else happened needs.
                if (body.TryGetProperty("atLeast", out var atLeast)) { if (atLeast.ValueKind != JsonValueKind.True && atLeast.ValueKind != JsonValueKind.False) throw new FormatException("expected boolean atLeast"); n.AtLeast = atLeast.GetBoolean(); }
                if (n.AtLeast && body.TryGetProperty("cardinality", out _)) throw new FormatException("an atLeast set states no cardinality");
                n.Children = new[] { Parse(body.GetProperty("observed")), Parse(body.GetProperty("expected")) };
                n.Count = body.TryGetProperty("cardinality", out _) ? StrictJson.Num(body, "cardinality") : (double?)null;
                if (n.Count.HasValue && (n.Count < 1 || n.Count % 1 != 0)) throw new FormatException("set cardinality must be positive integer");
                n.SetField = StrictJson.Str(body, "field"); n.ExpectedField = StrictJson.Str(body, "expectedField");
                n.Stream = StrictJson.Str(body, "stream"); n.FromSim = StrictJson.Num(body, "fromSim"); n.ToSim = StrictJson.Num(body, "toSim"); if (n.FromSim > n.ToSim) throw new FormatException("reversed interval"); return n;
            }
            if (p.Name == "tally")
            {
                // How many distinct values of one field the matching records carry: a count of what was recorded. It makes no
                // "only these" statement, so it asks for no coverage; a caption or a card row binds the number it found.
                StrictJson.Keys(body, "of", "field", "bind", "min");
                n.Children = new[] { Parse(body.GetProperty("of")) }; n.SetField = StrictJson.Str(body, "field");
                if (!KnownFields.Contains(n.SetField)) throw new FormatException("unknown tally field " + n.SetField);
                n.Bind = new Dictionary<string, string>(StringComparer.Ordinal) { [StrictJson.Str(body, "bind")] = "count" };
                n.Count = StrictJson.Num(body, "min", 1);
                if (n.Count < 1 || n.Count % 1 != 0) throw new FormatException("tally min must be a positive integer");
                return n;
            }
            if (p.Name == "coverage")
            {
                StrictJson.Keys(body, "fromCue", "toCue", "stream", "fromSim", "toSim"); n.Stream = StrictJson.Str(body, "stream");
                n.FromSim = StrictJson.Num(body, "fromSim"); n.ToSim = StrictJson.Num(body, "toSim"); if (n.FromSim > n.ToSim) throw new FormatException("reversed interval"); return n;
            }
            if (!Observation.Kinds.Contains(p.Name)) throw new FormatException("unknown record kind " + p.Name);
            StrictJson.Keys(body, "before", "after", "fromCue", "toCue", "throughNow", "mode", "as", "device", "key", "identity", "cycle", "command", "attempt", "status", "state", "event", "kind", "source", "fields", "bind", "present", "gte", "allBelow", "until", "count", "maxAgeS", "stream", "fromSim", "toSim");
            n.Before = body.TryGetProperty("before", out _) ? StrictJson.Str(body, "before") : null;
            n.After = body.TryGetProperty("after", out _) ? StrictJson.Str(body, "after") : null;
            if ((n.Before != null && n.Before != "$trigger.occurredAtS") || (n.After != null && n.After != "$trigger.occurredAtS")) throw new FormatException("unsupported temporal reference");
            n.Mode = StrictJson.Str(body, "mode", "occurred");
            if (n.Mode != "occurred" && n.Mode != "current") throw new FormatException("unknown evidence mode");
            if (body.TryGetProperty("as", out _)) n.Alias = StrictJson.Str(body, "as");
            if (new[] { "status", "state", "event", "kind" }.Count(k => body.TryGetProperty(k, out _)) > 1) throw new FormatException("conflicting status aliases");
            n.Source = StrictJson.Str(body, "source", EvidenceSource.Platform); EvidenceSource.Check(n.Source);
            var match = new Dictionary<string, string>(StringComparer.Ordinal);
            foreach (var f in new[] { "device", "key", "identity", "cycle", "command", "attempt", "status", "state", "event", "kind" })
                if (body.TryGetProperty(f, out var v)) match[f == "state" || f == "event" || f == "kind" ? "status" : f] = StrictJson.Scalar(v);
            if (body.TryGetProperty("fields", out var fields))
            {
                if (fields.ValueKind != JsonValueKind.Object) throw new FormatException("fields must be an object");
                foreach (var f in fields.EnumerateObject()) { if (!KnownFields.Contains(f.Name)) throw new FormatException("unknown evidence field " + f.Name); if (match.ContainsKey(f.Name)) throw new FormatException("duplicate field " + f.Name); match.Add(f.Name, StrictJson.Scalar(f.Value)); }
            }
            n.Match = match;
            foreach (var value in match.Values.Where(v => v.StartsWith("$", StringComparison.Ordinal))) {
                if (value.StartsWith("$match.", StringComparison.Ordinal) && (value.Split('.').Length != 3 || !KnownFields.Contains(value.Split('.').Last()))) throw new FormatException("invalid named reference");
                if (!value.StartsWith("$match.", StringComparison.Ordinal) && !new[] { "$trigger.cycle", "$trigger.identity", "$trigger.command", "$trigger.attempt", "$trigger.device", "$trigger.artifactToken", "$trigger.beforeReading", "$trigger.afterReading", "$window.identity" }.Contains(value)) throw new FormatException("unknown evidence reference " + value); }
            if (body.TryGetProperty("present", out _))
            {
                n.Present = StrictJson.Array(body, "present").Select(v => v.GetString()).ToArray();
                foreach (var field in n.Present) if (!KnownFields.Contains(field)) throw new FormatException("unknown present evidence field " + field);
            }
            if (body.TryGetProperty("bind", out var binds))
            {
                if (binds.ValueKind != JsonValueKind.Object) throw new FormatException("bind must be an object");
                n.Bind = binds.EnumerateObject().ToDictionary(f => f.Name, f => f.Value.GetString(), StringComparer.Ordinal);
                foreach (var b in n.Bind) if (!KnownFields.Contains(b.Value)) throw new FormatException("unknown bound evidence field " + b.Value);
            }
            double? Optional(string name) => body.TryGetProperty(name, out _) ? StrictJson.Num(body, name) : (double?)null;
            if (body.TryGetProperty("throughNow", out var through)) n.ThroughNow = through.GetBoolean();
            n.Gte = Optional("gte"); n.AllBelow = Optional("allBelow"); n.Count = Optional("count"); n.MaxAgeS = Optional("maxAgeS");
            n.FromSim = Optional("fromSim"); n.ToSim = Optional("toSim");
            if (body.TryGetProperty("until", out _)) n.Until = StrictJson.Str(body, "until");
            if (body.TryGetProperty("stream", out _)) n.Stream = StrictJson.Str(body, "stream");
            if ((n.Count.HasValue || n.AllBelow.HasValue) && (n.Stream == null || !n.FromSim.HasValue || !n.ToSim.HasValue)) throw new FormatException(p.Name + ": exact/none/allBelow requires coverage interval");
            if (n.AllBelow.HasValue && n.Until == null) throw new FormatException("allBelow requires until");
            if (n.ThroughNow && !n.Count.HasValue) throw new FormatException("throughNow requires count");
            if (n.Until != null && !n.AllBelow.HasValue) throw new FormatException("until requires allBelow");
            if (n.Count.HasValue && (n.Gte.HasValue || n.AllBelow.HasValue)) throw new FormatException("count cannot combine with value constraints");
            if (n.Gte.HasValue && n.AllBelow.HasValue) throw new FormatException("incompatible value constraints");
            if (n.FromSim.HasValue != n.ToSim.HasValue) throw new FormatException("interval requires both endpoints");
            if (n.Until != null && n.Until != "$match.running.runningAt") throw new FormatException("unknown until reference " + n.Until);
            if (n.Count.HasValue && (n.Count < 0 || n.Count % 1 != 0)) throw new FormatException("count requires nonnegative integer");
            if (n.MaxAgeS < 0 || (n.FromSim.HasValue && n.ToSim.HasValue && n.FromSim > n.ToSim)) throw new FormatException("invalid evidence interval");
            return n;
        }
        public static double Cue(string name, HarborLayout layout) {
            var outage = layout.Beats.First(b => b.Name == "feeder-outage").From;
            switch (name) {
                case "airLock.detected": return Scheduler.WallOffsetSim(layout.Cues.AirLockFromSim, layout.Cues.AirLockHoldS, layout.DayLengthSeconds);
                case "outage.wait": return Scheduler.WallOffsetSim(outage, 20, layout.DayLengthSeconds);
                case "outage.detected": return Scheduler.WallOffsetSim(outage, layout.Cues.SilenceS, layout.DayLengthSeconds);
                case "outage.tally": return Scheduler.WallOffsetSim(outage, layout.Cues.SilenceS + 3, layout.DayLengthSeconds);
                case "outage.notified": return Scheduler.WallOffsetSim(outage, layout.Cues.SilenceS + 6, layout.DayLengthSeconds);
                case "outage.escalated": return Scheduler.WallOffsetSim(outage, layout.Cues.SilenceS + 6 + layout.Cues.EscalateAfterS, layout.DayLengthSeconds);
                default: throw new FormatException("unknown layout cue " + name);
            }
        }
        public void ResolveTimes(HarborLayout layout) {
            if (FromCue != null) FromSim = Cue(FromCue, layout);
            if (ToCue != null) ToSim = Cue(ToCue, layout);
            if (FromSim > ToSim) throw new FormatException("reversed resolved interval");
            foreach (var child in Children) child.ResolveTimes(layout);
        }
        IEnumerable<Observation> Select(ObservationStore store, EvidenceContext c)
        {
            if (Kind == "any" || Kind == "all" || Kind == "tally") return Children.SelectMany(n => n.Select(store, c)).Distinct();
            string Resolve(string value, Observation candidate)
            {
                if (value.StartsWith("$match.", StringComparison.Ordinal)) { var parts = value.Split('.'); return parts.Length == 3 && c.Named.TryGetValue(parts[1], out var row) ? row.Field(parts[2]) : null; }
                if (value.StartsWith("$trigger.", StringComparison.Ordinal)) return c.Trigger?.Field(value.Substring(9));
                if (value == "$window.identity")
                {
                    // Match the actual platform window identity active at this alarm's cycle, not a token baked into the story.
                    var window = store.WindowHistory(c.Run, candidate.Device, candidate.Key, c.NowS).Where(o => o.OccurredAtS <= candidate.OccurredAtS).OrderByDescending(o => o.OccurredAtS).FirstOrDefault();
                    return window?.Status == "ACTIVE" ? window.Identity : null;
                }
                return value;
            }
            string StableMatch(string field) => Match.TryGetValue(field, out var value) && value != "$window.identity" ? Resolve(value, null) : null;
            // Inside a set (RestrictInterval) a record that carries its own occurrence is placed by WHEN IT HAPPENED, not by when a poll
            // first saw it: the platform's alarms are read a few seconds after they are raised, so bounding them by the read's instant
            // dropped every one that landed past the interval's end and 'exactly the six that went quiet' could not hold on a live day.
            var from = Math.Max(FromSim ?? c.FromSim, c.RestrictInterval ? c.FromSim : double.NegativeInfinity);
            // A set's interval bounds what happened in it. A snapshot it is compared with is the newest read, and every poll
            // moves that read's instant on: held to the interval's end, a claim could hold only until the next poll after it
            // (the credentials read a minute after a replacement, the membership read once late alarms are in). The snapshot
            // must not predate the interval; it may be newer than its end.
            var latest = c.RestrictInterval && Mode == "current";
            var to = latest ? double.PositiveInfinity : Math.Min(ToSim ?? c.ToSim, c.RestrictInterval ? c.ToSim : double.PositiveInfinity);
            var available = store.Query(c.Run, Kind, Source, Mode == "current", c.NowS, from, to, StableMatch("device"), StableMatch("key"));
            return available.Where(o => (!c.RestrictInterval || (o.Sim >= c.FromSim && (latest || o.Sim <= c.ToSim))) && (Before == null || (c.Trigger != null && c.Trigger.HasOccurrence && o.HasOccurrence && o.OccurredAtS < c.Trigger.OccurredAtS)) && (After == null || (c.Trigger != null && c.Trigger.HasOccurrence && o.HasOccurrence && o.OccurredAtS > c.Trigger.OccurredAtS)) && o.Sim >= (FromSim ?? c.FromSim) && (latest || o.Sim <= (ToSim ?? c.ToSim)) && c.ObservedSim(o) >= (FromSim ?? c.FromSim) && (latest || (c.RestrictInterval && o.HasOccurrence) || c.ObservedSim(o) <= c.ToSim) &&
                (!MaxAgeS.HasValue || c.NowS - o.ObservedAtS <= MaxAgeS) && (!Gte.HasValue || double.TryParse(o.Field("value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var numeric) && numeric >= Gte.Value) && Present.All(f => !string.IsNullOrEmpty(o.Field(f))) && Match.All(m =>
                    o.Field(m.Key) != null && o.Field(m.Key) == Resolve(m.Value, o)));
        }
        /// <summary>
        /// Why a predicate holds or does not, for a person re-timing a cut: each part on its own line, and for a set the two sides'
        /// sizes, what is missing, what is extra, and whether its stream is covered. It decides nothing; <see cref="Evaluate"/> does.
        /// </summary>
        public string Explain(ObservationStore store, EvidenceContext context, string indent = "")
        {
            var holds = Evaluate(store, context).Holds; var head = indent + Kind + (holds ? " HOLDS" : " unmet");
            if (Kind == "all" || Kind == "any")
            {
                var named = new Dictionary<string, Observation>(context.Named); var lines = new List<string> { head };
                foreach (var child in Children)
                {
                    var scoped = context.With(context.FromSim, context.ToSim, named: named);
                    lines.Add(child.Explain(store, scoped, indent + "  "));
                    var r = child.Evaluate(store, scoped); if (Kind == "all" && r.Holds) foreach (var pair in r.Named) named[pair.Key] = pair.Value;
                }
                return string.Join("\n", lines);
            }
            if (Kind == "set")
            {
                var end = ToSim.Value;
                var scoped = context.With(FromSim.Value, end, restrictInterval: true);
                IEnumerable<string> Values(Observation o, string field) => o.Sets.TryGetValue(field, out var set) ? set : new[] { o.Field(field) };
                var a = new HashSet<string>(Children[0].Select(store, scoped).SelectMany(o => Values(o, SetField)).Where(v => v != null), StringComparer.Ordinal);
                var b = new HashSet<string>(Children[1].Select(store, scoped.With(FromSim.Value, end, Children[1].Source ?? context.Source)).SelectMany(o => Values(o, ExpectedField)).Where(v => v != null), StringComparer.Ordinal);
                string Some(IEnumerable<string> values) { var v = values.OrderBy(x => x, StringComparer.Ordinal).ToArray(); return v.Length + (v.Length == 0 ? "" : " (" + string.Join(", ", v.Take(3)) + (v.Length > 3 ? ", ..." : "") + ")"); }
                return head + ": observed " + a.Count + ", expected " + b.Count + ", missing " + Some(b.Except(a)) + ", extra " + Some(a.Except(b))
                    + (AtLeast ? " (at least: extras and coverage are not asked)" : ", stream " + Stream + " covered " + N(FromSim.Value) + " to " + N(end) + ": " + (store.Covers(context.Run, Stream, FromSim.Value, end) ? "yes" : "NO"));
            }
            if (Kind == "coverage") return head + ": stream " + Stream + " " + N(FromSim.Value) + " to " + N(ToSim.Value);
            if (Kind == "tally") { var r = Evaluate(store, context); return head + ": " + (r.Bindings.Count > 0 ? r.Bindings.First().Value : "0") + " distinct " + SetField + "\n" + Children[0].Explain(store, context, indent + "  "); }
            var rows = Select(store, context).ToArray();
            return head + ": " + rows.Length + " matching record" + (rows.Length == 1 ? "" : "s") + " (" + string.Join(" ", Match.Select(m => m.Key + "=" + m.Value)) + ")"
                + (Count.HasValue ? ", wants exactly " + N(Count.Value) + ", stream " + Stream + " covered: " + (store.Covers(context.Run, Stream, FromSim.Value, ThroughNow && context.NowSim.HasValue ? Math.Min(ToSim.Value, context.NowSim.Value) : ToSim.Value) ? "yes" : "NO") : "");
        }
        static string N(double v) => v.ToString("0.###", CultureInfo.InvariantCulture);
        public EvidenceResult Evaluate(ObservationStore store, EvidenceContext context)
        {
            EvidenceResult Result(bool holds, IEnumerable<Observation> records = null)
            {
                var rows = (records ?? Array.Empty<Observation>()).Distinct().OrderBy(o => o.ObservedAtS).ThenBy(o => o.EventKey, StringComparer.Ordinal).ToArray();
                var bound = new Dictionary<string, string>(StringComparer.Ordinal);
                if (holds && rows.Length > 0) foreach (var b in Bind) { var value = rows.Last().Field(b.Value); if (value == null) return new EvidenceResult(); bound[b.Key] = value; }
                var named = new Dictionary<string, Observation>(context.Named);
                if (holds && Alias != null && rows.Length > 0) named[Alias] = rows.Last();
                return new EvidenceResult { Holds = holds, Records = rows, Bindings = bound, Named = named };
            }
            if (Kind == "coverage") return Result(store.Covers(context.Run, Stream, FromSim.Value, ToSim.Value));
            if (Kind == "tally")
            {
                var counted = Children[0].Select(store, context).Where(o => !string.IsNullOrEmpty(o.Field(SetField))).ToArray();
                if (counted.Any(o => o.Source != context.Source)) return Result(false);
                var distinct = counted.Select(o => o.Field(SetField)).Distinct(StringComparer.Ordinal).Count();
                if (distinct < Count.Value) return Result(false);
                // one record per counted value, the first observed: the count's evidence is exactly what was counted
                var tally = new EvidenceResult { Holds = true, Records = counted.OrderBy(o => o.ObservedAtS).ThenBy(o => o.EventKey, StringComparer.Ordinal).GroupBy(o => o.Field(SetField), StringComparer.Ordinal).Select(g => g.First()).ToArray(), Named = new Dictionary<string, Observation>(context.Named) };
                tally.Bindings = new Dictionary<string, string>(StringComparer.Ordinal) { [Bind.Keys.First()] = distinct.ToString(CultureInfo.InvariantCulture) };
                return tally;
            }
            if (Kind == "all" || Kind == "any")
            {
                var collected = new List<EvidenceResult>(); var named = new Dictionary<string, Observation>(context.Named);
                foreach (var child in Children) {
                    var r = child.Evaluate(store, context.With(context.FromSim, context.ToSim, named: named)); collected.Add(r);
                    if (Kind == "all" && r.Holds) foreach (var pair in r.Named) named[pair.Key] = pair.Value;
                }
                var results = collected.ToArray();
                var ok = Kind == "all" ? results.All(r => r.Holds) : results.Any(r => r.Holds);
                var selected = Kind == "all" ? results : results.Where(r => r.Holds).Take(1).ToArray();
                var result = Result(ok, selected.SelectMany(r => r.Records));
                result.Bindings = selected.SelectMany(r => r.Bindings).GroupBy(b => b.Key).ToDictionary(g => g.Key, g => g.First().Value, StringComparer.Ordinal);
                result.Error = results.FirstOrDefault(r => r.Error != null)?.Error; result.Named = named; return result;
            }
            if (Kind == "set")
            {
                var scoped = context.With(FromSim.Value, ToSim.Value, restrictInterval: true);
                var observedResult = Children[0].Evaluate(store, scoped);
                var expectedResult = Children[1].Evaluate(store, scoped.With(FromSim.Value, ToSim.Value, Children[1].Source ?? context.Source));
                var actual = Children[0].Select(store, scoped).ToArray(); var expected = Children[1].Select(store, scoped).ToArray();
                bool Has(Observation o, string f) => o.Sets.ContainsKey(f) || !string.IsNullOrEmpty(o.Field(f));
                if (actual.Any(o => !Has(o, SetField)) || expected.Any(o => !Has(o, ExpectedField))) return new EvidenceResult { Error = "missing required set field: " + SetField + " / " + ExpectedField };
                if (!observedResult.Holds || !expectedResult.Holds || actual.Length == 0 || actual.Any(o => o.Source != context.Source) || expected.Length == 0 || (!AtLeast && !store.Covers(context.Run, Stream, FromSim.Value, ToSim.Value))) return Result(false);
                IEnumerable<string> Values(Observation o, string field) => o.Sets.TryGetValue(field, out var set) ? set : new[] { o.Field(field) };
                var a = new HashSet<string>(actual.SelectMany(o => Values(o, SetField)), StringComparer.Ordinal);
                var b = new HashSet<string>(expected.SelectMany(o => Values(o, ExpectedField)), StringComparer.Ordinal);
                if (AtLeast) return Result(b.Count > 0 && b.IsSubsetOf(a), actual.Concat(expected));
                return Result(a.Count > 0 && (!Count.HasValue || a.Count == Count.Value) && a.SetEquals(b), actual.Concat(expected));
            }
            if (Source != context.Source) return Result(false);
            var rows = Select(store, context).ToArray();
            if (Count.HasValue) { var end = ThroughNow && context.NowSim.HasValue ? Math.Min(ToSim.Value, context.NowSim.Value) : ToSim.Value; return Result(store.Covers(context.Run, Stream, FromSim.Value, end) && rows.Length == Count.Value, rows); }
            if (AllBelow.HasValue)
            {
                if (!context.Named.TryGetValue("running", out var reference) || !reference.HasOccurrence || reference.Kind != "chip" || reference.Status != "PLATFORM" || reference.Cycle != context.Trigger?.Cycle ||
                    !double.TryParse(reference.Field("runningAt"), NumberStyles.Float, CultureInfo.InvariantCulture, out var end) || Math.Abs(end - reference.OccurredAtS) > 1e-6) return Result(false);
                // Select through this exact transition, including readings beyond any old authored estimate.
                rows = Select(store, context.With(FromSim.Value, reference.Sim)).Where(o => o.HasOccurrence && o.Sim <= reference.Sim && o.OccurredAtS <= end && o.Cycle == reference.Cycle).ToArray();
                return Result(rows.Length > 0 && store.Covers(context.Run, Stream, FromSim.Value, reference.Sim) && rows.All(o => double.TryParse(o.Field("value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var value) && value < AllBelow.Value), rows.Append(reference));
            }
            if (Gte.HasValue) rows = rows.Where(o => double.TryParse(o.Field("value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var value) && value >= Gte.Value).ToArray();
            return Result(rows.Length > 0, rows.OrderBy(o => o.ObservedAtS).ThenBy(o => o.EventKey, StringComparer.Ordinal).Take(1));
        }
    }
}
