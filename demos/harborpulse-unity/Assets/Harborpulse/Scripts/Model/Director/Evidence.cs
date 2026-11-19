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
        public double Sim { get; }
        public string Source { get; }
        public IReadOnlyDictionary<string, string> Fields { get; }
        public IReadOnlyDictionary<string, string[]> Sets { get; }
        static string Part(string s) => s.Length.ToString(CultureInfo.InvariantCulture) + ":" + s;
        // Observation time is deliberately absent: a resubscribe is the same event, and cycles are distinct.
        public string EventKey => string.Concat(new[] { Run, Kind, Identity, Device, Key, Cycle, Command, Attempt, Status, Source }.Select(Part));
        public string Field(string name)
        {
            switch (name)
            {
                case "device": return Device; case "key": return Key; case "cycle": return Cycle;
                case "identity": return Identity; case "command": return Command; case "attempt": return Attempt;
                case "status": return Status; case "source": return Source;
                case "occurredAtS": return OccurredAtS.ToString("R", CultureInfo.InvariantCulture);
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

    public sealed class ObservationStore
    {
        readonly Dictionary<string, Observation> records = new Dictionary<string, Observation>(StringComparer.Ordinal);
        readonly List<CoverageInterval> coverage = new List<CoverageInterval>();
        public IReadOnlyCollection<Observation> Records => records.Values;
        public IReadOnlyList<CoverageInterval> Coverage => coverage.AsReadOnly();
        public bool Add(Observation o)
        {
            if (records.TryGetValue(o.EventKey, out var old) && old.ObservedAtS <= o.ObservedAtS) return false;
            records[o.EventKey] = o; return true;
        }
        public void AddCoverage(CoverageInterval interval)
        {
            if (!coverage.Any(c => c.Run == interval.Run && c.Stream == interval.Stream && c.FromSim == interval.FromSim && c.ToSim == interval.ToSim && c.Gap == interval.Gap)) coverage.Add(interval);
        }
        public bool Covers(string run, string stream, double from, double to)
        {
            if (to < from || coverage.Any(g => g.Run == run && g.Stream == stream && g.Gap && g.FromSim <= to && g.ToSim >= from)) return false;
            var end = from;
            foreach (var c in coverage.Where(c => !c.Gap && c.Run == run && c.Stream == stream).OrderBy(c => c.FromSim))
            {
                if (c.FromSim > end) break;
                if (c.ToSim >= end) end = c.ToSim;
                if (end >= to) return true;
            }
            return false;
        }
    }

    public sealed class EvidenceResult
    {
        public bool Holds { get; internal set; }
        public IReadOnlyList<Observation> Records { get; internal set; } = Array.Empty<Observation>();
        public IReadOnlyDictionary<string, string> Bindings { get; internal set; } = new Dictionary<string, string>();
    }

    public sealed class EvidenceContext
    {
        public EvidenceContext(string run, double fromSim, double toSim, double nowS, string source = EvidenceSource.Platform, Observation trigger = null)
        { Run = run; FromSim = fromSim; ToSim = toSim; NowS = nowS; Source = source; Trigger = trigger; }
        public string Run { get; }
        public double FromSim { get; }
        public double ToSim { get; }
        public double NowS { get; }
        public string Source { get; }
        public Observation Trigger { get; }
    }

    /// <summary>A closed predicate language; no executable code or reflection-based JSON.</summary>
    public sealed class Needs
    {
        static readonly string[] KnownFields = { "value", "runningAt", "freshDevices", "windowToken", "raisedDuringSuppression", "suppressionWindowToken", "policy", "afterClose", "alarmActive", "afterReplacement", "enabled", "max_kw", "falling", "rising", "trigger", "backdated", "readingIdentity", "signer", "code", "platform", "afterFailure", "updated", "refused", "maxInFlight", "device", "key", "identity", "cycle", "command", "attempt", "status", "source", "occurredAtS" };
        public string Kind { get; private set; }
        public string Source { get; private set; }
        public IReadOnlyList<Needs> Children { get; private set; } = Array.Empty<Needs>();
        public IReadOnlyDictionary<string, string> Match { get; private set; } = new Dictionary<string, string>();
        public IReadOnlyDictionary<string, string> Bind { get; private set; } = new Dictionary<string, string>();
        public IReadOnlyList<string> Present { get; private set; } = Array.Empty<string>();
        public double? Gte { get; private set; }
        public double? AllBelow { get; private set; }
        public string Until { get; private set; }
        public double? Count { get; private set; }
        public double? MaxAgeS { get; private set; }
        public double? FromSim { get; private set; }
        public double? ToSim { get; private set; }
        public string Stream { get; private set; }
        public string SetField { get; private set; }
        public string ExpectedField { get; private set; }
        public IEnumerable<string> BoundNames => Bind.Keys.Concat(Children.SelectMany(c => c.BoundNames));
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
            if (p.Name == "set")
            {
                StrictJson.Keys(body, "observed", "expected", "field", "expectedField", "stream", "fromSim", "toSim");
                n.Children = new[] { Parse(body.GetProperty("observed")), Parse(body.GetProperty("expected")) };
                n.SetField = StrictJson.Str(body, "field"); n.ExpectedField = StrictJson.Str(body, "expectedField");
                n.Stream = StrictJson.Str(body, "stream"); n.FromSim = StrictJson.Num(body, "fromSim"); n.ToSim = StrictJson.Num(body, "toSim"); return n;
            }
            if (p.Name == "coverage")
            {
                StrictJson.Keys(body, "stream", "fromSim", "toSim"); n.Stream = StrictJson.Str(body, "stream");
                n.FromSim = StrictJson.Num(body, "fromSim"); n.ToSim = StrictJson.Num(body, "toSim"); return n;
            }
            if (!Observation.Kinds.Contains(p.Name)) throw new FormatException("unknown record kind " + p.Name);
            StrictJson.Keys(body, "device", "key", "identity", "cycle", "command", "attempt", "status", "state", "event", "kind", "source", "fields", "bind", "present", "gte", "allBelow", "until", "count", "maxAgeS", "stream", "fromSim", "toSim");
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
            foreach (var value in match.Values.Where(v => v.StartsWith("$", StringComparison.Ordinal)))
                if (!new[] { "$trigger.cycle", "$trigger.identity", "$trigger.command", "$trigger.attempt", "$trigger.device", "$window.identity" }.Contains(value)) throw new FormatException("unknown evidence reference " + value);
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
            n.Gte = Optional("gte"); n.AllBelow = Optional("allBelow"); n.Count = Optional("count"); n.MaxAgeS = Optional("maxAgeS");
            n.FromSim = Optional("fromSim"); n.ToSim = Optional("toSim");
            if (body.TryGetProperty("until", out _)) n.Until = StrictJson.Str(body, "until");
            if (body.TryGetProperty("stream", out _)) n.Stream = StrictJson.Str(body, "stream");
            if ((n.Count.HasValue || n.AllBelow.HasValue) && (n.Stream == null || !n.FromSim.HasValue || !n.ToSim.HasValue)) throw new FormatException(p.Name + ": exact/none/allBelow requires coverage interval");
            if (n.AllBelow.HasValue && n.Until == null) throw new FormatException("allBelow requires until");
            if (n.Until != null && n.Until != "chip.runningAt") throw new FormatException("unknown until reference " + n.Until);
            if (n.Count.HasValue && (n.Count < 0 || n.Count % 1 != 0)) throw new FormatException("count requires nonnegative integer");
            if (n.MaxAgeS < 0 || (n.FromSim.HasValue && n.ToSim.HasValue && n.FromSim > n.ToSim)) throw new FormatException("invalid evidence interval");
            return n;
        }
        IEnumerable<Observation> Select(ObservationStore store, EvidenceContext c)
        {
            if (Kind == "any" || Kind == "all") return Children.SelectMany(n => n.Select(store, c)).Distinct();
            string Resolve(string value, Observation candidate)
            {
                if (value.StartsWith("$trigger.", StringComparison.Ordinal)) return c.Trigger?.Field(value.Substring(9));
                if (value == "$window.identity")
                {
                    // Match the actual platform window identity active at this alarm's cycle, not a token baked into the story.
                    var window = store.Records.Where(o => o.Run == c.Run && o.Kind == "window" && o.Source == EvidenceSource.Platform && o.Device == candidate.Device && o.Key == candidate.Key && o.ObservedAtS <= c.NowS && o.OccurredAtS <= candidate.OccurredAtS && o.Sim >= c.FromSim && o.Sim <= c.ToSim).OrderByDescending(o => o.OccurredAtS).FirstOrDefault();
                    return window?.Status == "ACTIVE" ? window.Identity : null;
                }
                return value;
            }
            return store.Records.Where(o => o.Run == c.Run && o.Kind == Kind && o.Source == Source && o.ObservedAtS <= c.NowS && o.Sim >= c.FromSim && o.Sim <= c.ToSim &&
                (!FromSim.HasValue || o.Sim >= FromSim) && (!ToSim.HasValue || o.Sim <= ToSim) &&
                (!MaxAgeS.HasValue || c.NowS - o.ObservedAtS <= MaxAgeS) && Present.All(f => !string.IsNullOrEmpty(o.Field(f))) && Match.All(m =>
                    o.Field(m.Key) != null && o.Field(m.Key) == Resolve(m.Value, o)));
        }
        public EvidenceResult Evaluate(ObservationStore store, EvidenceContext context)
        {
            EvidenceResult Result(bool holds, IEnumerable<Observation> records = null)
            {
                var rows = (records ?? Array.Empty<Observation>()).Distinct().OrderBy(o => o.ObservedAtS).ThenBy(o => o.EventKey, StringComparer.Ordinal).ToArray();
                var bound = new Dictionary<string, string>(StringComparer.Ordinal);
                if (holds && rows.Length > 0) foreach (var b in Bind) { var value = rows.Last().Field(b.Value); if (value == null) return new EvidenceResult(); bound[b.Key] = value; }
                return new EvidenceResult { Holds = holds, Records = rows, Bindings = bound };
            }
            if (Kind == "coverage") return Result(store.Covers(context.Run, Stream, FromSim.Value, ToSim.Value));
            if (Kind == "all" || Kind == "any")
            {
                var results = Children.Select(n => n.Evaluate(store, context)).ToArray();
                var ok = Kind == "all" ? results.All(r => r.Holds) : results.Any(r => r.Holds);
                var selected = Kind == "all" ? results : results.Where(r => r.Holds).Take(1).ToArray();
                var result = Result(ok, selected.SelectMany(r => r.Records));
                result.Bindings = selected.SelectMany(r => r.Bindings).GroupBy(b => b.Key).ToDictionary(g => g.Key, g => g.First().Value, StringComparer.Ordinal);
                return result;
            }
            if (Kind == "set")
            {
                var actual = Children[0].Select(store, context).ToArray(); var expected = Children[1].Select(store, context).ToArray();
                // A scenario ledger supplies an expectation, never proof that the platform stored it.
                if (actual.Any(o => o.Source != context.Source) || expected.Length == 0 || !store.Covers(context.Run, Stream, FromSim.Value, ToSim.Value)) return Result(false);
                IEnumerable<string> Values(Observation o, string field) => o.Sets.TryGetValue(field, out var set) ? set : o.Field(field) == null ? Array.Empty<string>() : new[] { o.Field(field) };
                var a = new HashSet<string>(actual.SelectMany(o => Values(o, SetField)), StringComparer.Ordinal);
                var b = new HashSet<string>(expected.SelectMany(o => Values(o, ExpectedField)), StringComparer.Ordinal);
                return Result(a.SetEquals(b), actual.Concat(expected));
            }
            if (Source != context.Source) return Result(false);
            var rows = Select(store, context).ToArray();
            if (Count.HasValue) return Result(store.Covers(context.Run, Stream, FromSim.Value, ToSim.Value) && rows.Length == Count.Value, rows);
            if (AllBelow.HasValue)
            {
                // until is an explicit observed reference, e.g. chip.runningAt (wall seconds).
                var parts = Until.Split('.');
                if (parts.Length != 2 || !Observation.Kinds.Contains(parts[0])) throw new FormatException("unknown until reference " + Until);
                var device = Match.TryGetValue("device", out var d) ? d : "";
                var reference = store.Records.Where(o => o.Run == context.Run && o.Kind == parts[0] && o.Device == device && o.Source == context.Source && o.ObservedAtS <= context.NowS && o.Sim >= context.FromSim && o.Sim <= context.ToSim).OrderByDescending(o => o.ObservedAtS).FirstOrDefault();
                if (reference == null || !double.TryParse(reference.Field(parts[1]), NumberStyles.Float, CultureInfo.InvariantCulture, out var end)) return Result(false);
                rows = rows.Where(o => o.OccurredAtS <= end).ToArray();
                return Result(rows.Length > 0 && store.Covers(context.Run, Stream, FromSim.Value, ToSim.Value) && rows.All(o => double.TryParse(o.Field("value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var value) && value < AllBelow.Value), rows.Append(reference));
            }
            if (Gte.HasValue) rows = rows.Where(o => double.TryParse(o.Field("value"), NumberStyles.Float, CultureInfo.InvariantCulture, out var value) && value >= Gte.Value).ToArray();
            return Result(rows.Length > 0, rows.TakeLast(1));
        }
    }
}
