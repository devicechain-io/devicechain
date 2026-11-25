// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>
    /// Read-only evidence adapter. Joins use platform identities; runner plans never establish platform outcomes.
    ///
    /// A frame costs what CHANGED, never what is held. The polled reads are republished every few seconds and looked at every
    /// frame, and most of the time nothing in them has moved: so a read is parsed once per distinct answer (a poll that brings
    /// back the same bytes is the same answer with a newer as-of), and each join below runs again only when something it reads
    /// has changed or a clock it waits on has come due (<see cref="Section"/>). What a join found wrong it restates on the
    /// frames it sleeps through, so a gap is as wide as it was when every frame recomputed it.
    /// </summary>
    public sealed class DirectorObserver : IDirectorFeed
    {
        sealed class Row { public string Name, Scope; public JsonElement Value; public Held Owner; public DirectorRead Read => Owner?.Read; }
        /// <summary>One polled read as last parsed. A newer poll with the same answer keeps the rows and moves <see cref="Read"/>.</summary>
        sealed class Held { public string Scope; public DirectorRead Read; public string Raw, Failure; public Row[] Rows = Array.Empty<Row>(); public long Revision; }
        struct Seen { public Held Held; public long Revision; public DirectorRead Read; }
        /// <summary>The reads of one name this frame. Content moves when the usable rows do; Version also when only an as-of did.</summary>
        sealed class Named
        {
            public readonly List<DirectorRead> Group = new List<DirectorRead>();
            public readonly List<Held> Slots = new List<Held>(), Now = new List<Held>();
            public readonly List<Seen> Last = new List<Seen>();
            public readonly List<Row> Rows = new List<Row>();
            public long Content, Version;
            public bool Healthy;
        }
        /// <summary>
        /// One join and what its last run left behind. It runs when its stamp (the sum of the versions of everything it reads,
        /// each of which only ever grows) has moved, or when the earliest instant it was waiting for has arrived: a record
        /// stamped after "now", a service window's end, the minute a replacement waits for its first reading.
        /// </summary>
        sealed class Section
        {
            public long Stamp = -1;
            public DateTimeOffset Wake = DateTimeOffset.MaxValue;
            public readonly List<string> Gaps = new List<string>(), Current = new List<string>();
        }
        sealed class Cycle { public JsonElement Row; public string Raw, Token, Originator, Key; public DateTimeOffset Raised; }
        sealed class Chip { public readonly Section Section = new Section(); public bool Seen, Has; public DateTimeOffset At; public PumpStartKind Kind; }
        readonly HarborLayout layout;
        readonly Func<DirectorReadSnapshot> reads;
        // Sticky problems (a malformed record, a missing instant) last the run; current ones restate this frame's read state.
        readonly HashSet<string> problems = new HashSet<string>(StringComparer.Ordinal);
        readonly HashSet<string> current = new HashSet<string>(StringComparer.Ordinal);
        readonly Dictionary<string, string> emitted = new Dictionary<string, string>();
        readonly Dictionary<string, JsonElement> alarmHistory = new Dictionary<string, JsonElement>();
        // The same lifecycles with their instants parsed, by alarm token and by originator, in the order they were first read.
        readonly Dictionary<string, Cycle> cycles = new Dictionary<string, Cycle>();
        readonly Dictionary<string, List<Cycle>> cyclesByToken = new Dictionary<string, List<Cycle>>(), cyclesByOriginator = new Dictionary<string, List<Cycle>>();
        readonly Dictionary<string, Reading> previous = new Dictionary<string, Reading>();
        readonly Dictionary<string, Dictionary<string, string>> trends = new Dictionary<string, Dictionary<string, string>>();
        readonly Dictionary<string, JsonElement> wellHistory = new Dictionary<string, JsonElement>();
        readonly SortedDictionary<DateTimeOffset, HashSet<string>> dueWells = new SortedDictionary<DateTimeOffset, HashSet<string>>();
        readonly Dictionary<string, string> wellCycles = new Dictionary<string, string>();
        readonly Dictionary<string, Reading> pumpRunning = new Dictionary<string, Reading>();
        readonly Dictionary<string, DateTimeOffset> pumpOffAt = new Dictionary<string, DateTimeOffset>();
        readonly HashSet<string> failedProbes = new HashSet<string>();
        // A command's alarm cycle, once its cause has named it: later reads of the alarm (a new cycle, a clear) never move it.
        readonly Dictionary<string, KeyValuePair<string, string>> commandCycles = new Dictionary<string, KeyValuePair<string, string>>();
        // A command whose cause did not name a cycle: the alarm reads it was tried against, and why not (null while it waits for a later read).
        readonly Dictionary<string, KeyValuePair<long, string>> causeTried = new Dictionary<string, KeyValuePair<long, string>>();
        long alarmStamp;
        readonly Dictionary<string, Named> names = Streams.Keys.ToDictionary(n => n, _ => new Named());
        readonly Section alarmRows = new Section(), escalations = new Section(), windows = new Section(), notifications = new Section(), replacements = new Section(),
            credentials = new Section(), commands = new Section(), rollouts = new Section(), memberships = new Section(), measured = new Section(), runner = new Section();
        readonly Dictionary<string, Chip> chips = new Dictionary<string, Chip>();
        readonly List<Row> streamRows = new List<Row>();
        readonly List<DirectorAlarmChange> changesSeen = new List<DirectorAlarmChange>();
        readonly Dictionary<string, string> deviceByToken = new Dictionary<string, string>(StringComparer.Ordinal), runnerDevices = new Dictionary<string, string>(StringComparer.Ordinal);
        readonly Dictionary<string, List<JsonElement>> replacementsByDevice = new Dictionary<string, List<JsonElement>>(), assignmentsByDevice = new Dictionary<string, List<JsonElement>>();
        readonly Dictionary<string, JsonElement> newestReplacement = new Dictionary<string, JsonElement>();
        readonly string[] bound;
        Section active;
        ScenarioStatus statusSeen;
        string backfillSeen;
        DateTimeOffset lastNow;
        bool rewound;
        long revisions, alarmVersion, pumpOffVersion, bindingVersion, statusVersion, backfillVersion, changesVersion, wellsSeen = -1, replacementsIndexed = -1, assignmentsIndexed = -1;
        int bindingFresh = -1;
        /// <summary>
        /// Records of polled reads visited: one for each record parsed out of a new answer, and one each time a join handles a
        /// read's record. A frame on which no read brought anything new and no clock came due adds none.
        /// </summary>
        public long ReadWorkUnits { get; private set; }
        /// <summary>
        /// Runs every join on every frame, as the observer did before its joins waited for a change. The records emitted must be
        /// the same either way, and a test holds the two against each other: a join that comes to read something its stamp does
        /// not name is caught there, not on a live day.
        /// </summary>
        public bool RunEveryJoin { get; set; }
        /// <summary>How many times a command's cause has been resolved against the alarm reads: once per command per new alarm read, never per frame.</summary>
        public long CauseResolutions { get; private set; }
        DirectorInputs input;
        Action<Observation> observe;
        Action<CoverageInterval> cover;
        readonly List<DirectorRead> complete = new List<DirectorRead>();
        int measurementIndex;
        public long MeasurementWorkUnits { get; private set; }
        string run;
        double startSim, lastSim;
        int measurementReconnects, alarmReconnects;
        static readonly Dictionary<string, string[]> Streams = new Dictionary<string, string[]> {
            ["alarms"] = new[] { "alarms", "notifications", "pump" }, ["notifications"] = new[] { "notifications" },
            ["windows"] = new[] { "notifications" }, ["suppressionStatus"] = new[] { "notifications" },
            ["credentials"] = new[] { "credentials" }, ["replacements"] = new[] { "credentials" },
            ["measurements"] = new[] { "measurements", "pump" }, ["commands"] = new[] { "pump" },
            ["membership"] = new[] { "alarms" }, ["artifact"] = new[] { "updates" }, ["signer"] = new[] { "updates" },
            ["rollout"] = new[] { "updates" }, ["targets"] = new[] { "updates" }, ["assignments"] = new[] { "updates" }
        };
        // Each coverage stream with the reads it needs, in the order the streams are first named above.
        static readonly KeyValuePair<string, string[]>[] StreamNames = Streams.Values.SelectMany(s => s).Distinct()
            .Select(stream => new KeyValuePair<string, string[]>(stream, Streams.Where(p => p.Value.Contains(stream)).Select(p => p.Key).ToArray())).ToArray();
        public DirectorObserver(HarborLayout layout, Func<DirectorReadSnapshot> reads)
        {
            this.layout = layout ?? throw new ArgumentNullException(nameof(layout)); this.reads = reads ?? throw new ArgumentNullException(nameof(reads));
            bound = new string[layout.Devices.Count];
            // each name a runner may use for a device, mapped to the first device in layout order that answers to it
            foreach (var d in layout.Devices) foreach (var name in new[] { d.Token, d.ExternalId })
                if (name != null && !runnerDevices.ContainsKey(name)) runnerDevices[name] = layout.Devices.First(x => x.Token == name || x.ExternalId == name).Token;
        }
        public IReadOnlyList<string> Problems => Array.AsReadOnly(problems.Union(current).OrderBy(p => p, StringComparer.Ordinal).ToArray());
        static string S(JsonElement r, string key) { var v = DirectorProbeEvaluation.Require(r, key); if (v.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(v.GetString())) throw new FormatException("missing required string " + key); return v.GetString(); }
        static string Optional(JsonElement r, string key) => r.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;
        static DateTimeOffset Time(string value) => DateTimeOffset.TryParse(value, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var t) ? t : throw new FormatException("invalid platform instant " + value);
        static DateTimeOffset T(JsonElement r, string key) => Time(S(r, key));
        static string N(double value) => value.ToString("R", CultureInfo.InvariantCulture);
        static string B(bool value) => value ? "true" : "false";
        // The verifier compares stored times at database (microsecond) precision.
        static string ReadingIdentity(string device, string instant) => device + "@" + (Time(instant).UtcDateTime.Ticks / 10).ToString(CultureInfo.InvariantCulture);
        double Wall(DateTimeOffset t) => (t - input.RecordingEpoch).TotalSeconds;
        // Rounded to a microhour (3.6 ms of sim time): converting a platform instant lands a few nanohours off an authored
        // boundary (the 10:45 window opens at 10.7499999967), which would put it outside a page window that starts at 10.75.
        double Sim(DateTimeOffset t) => RoundSim(input.Source?.Status?.StartedAt != null ? input.Source.Status.ToSimSeconds(t) / 3600 : input.Sim + (t - input.Now).TotalSeconds * 24 / layout.DayLengthSeconds);
        public static double RoundSim(double hours) => Math.Round(hours, 6);
        string Device(string token)
        {
            if (token != null && deviceByToken.TryGetValue(token, out var device)) return device;
            throw new FormatException("unbound platform device " + token);
        }
        string RunnerDevice(string token) => token != null && runnerDevices.TryGetValue(token, out var device) && device != null ? device : throw new FormatException("unknown runner device " + token);
        static readonly Row[] NoRows = Array.Empty<Row>();
        IEnumerable<Row> Rows(string name) => names.TryGetValue(name, out var n) ? n.Rows : (IEnumerable<Row>)NoRows;
        void Gap(string name, double from, double to) { if (Streams.TryGetValue(name, out var streams)) foreach (var s in streams) cover(new CoverageInterval(input.Run, s, from, to, true)); }
        void Safe(string name, Action body)
        {
            try { body(); }
            catch (Exception ex) when (ex is FormatException || ex is InvalidOperationException || ex is KeyNotFoundException || ex is JsonException) {
                problems.Add(name + ": " + ex.Message); Gap(name, lastSim, input.Sim);
                // A join that sleeps through the next frames still owes them this gap: the record is as unreadable then as now.
                active?.Gaps.Add(name);
            }
        }
        /// <summary>Whether a join must run this frame. When it need not, what its last run found wrong is restated for this frame.</summary>
        bool Due(Section s, long stamp)
        {
            if (s.Stamp == stamp && input.Now < s.Wake && !rewound && !RunEveryJoin) {
                foreach (var name in s.Gaps) Gap(name, lastSim, input.Sim);
                foreach (var problem in s.Current) current.Add(problem);
                return false;
            }
            s.Stamp = stamp; s.Wake = DateTimeOffset.MaxValue; s.Gaps.Clear(); s.Current.Clear();
            return true;
        }
        /// <summary>Opens a join that is due; the caller closes it by clearing <see cref="active"/>.</summary>
        bool Begin(Section s, long stamp) { if (!Due(s, stamp)) return false; active = s; return true; }
        bool Reached(DateTimeOffset at) { if (at <= input.Now) return true; Await(at); return false; }
        /// <summary>Something the running join could not use yet because its instant has not arrived: the join runs again when it has.</summary>
        void Await(DateTimeOffset at) { if (active != null && at < active.Wake) active.Wake = at; }
        void Trouble(string problem) { current.Add(problem); active?.Current.Add(problem); }
        void Visit() => ReadWorkUnits++;
        /// <summary>Which platform token each layout device is bound to this frame; the joins that map tokens to devices run again when one moves.</summary>
        void Bind()
        {
            var moved = false;
            for (var i = 0; i < bound.Length; i++) {
                string token = null;
                if (input.Source != null && input.Source.TryToken(layout.Devices[i].ExternalId, out var t)) token = t;
                if (!string.Equals(token, bound[i], StringComparison.Ordinal)) { bound[i] = token; moved = true; }
            }
            if (!moved) return;
            bindingVersion++; deviceByToken.Clear();
            for (var i = 0; i < bound.Length; i++) if (bound[i] != null && !deviceByToken.ContainsKey(bound[i])) deviceByToken[bound[i]] = layout.Devices[i].Token;
        }
        /// <summary>The runner's status is a new object on every poll; only what the measurement join reads of it (the buffered spans) is compared.</summary>
        void Status()
        {
            var status = input.Source?.Status;
            if (ReferenceEquals(status, statusSeen)) return;
            statusSeen = status; statusVersion++;
            var spans = status == null ? "" : string.Join("|", status.Backfill.Select(b => b.Device + "@" + b.First.UtcTicks.ToString(CultureInfo.InvariantCulture) + "-" + b.Last.UtcTicks.ToString(CultureInfo.InvariantCulture)));
            if (spans != backfillSeen) { backfillSeen = spans; backfillVersion++; }
        }
        /// <summary>The parsed rows of one polled read, parsed again only when the answer's bytes differ from the last one in its place.</summary>
        Held Hold(Named n, int slot, string name, DirectorRead r)
        {
            while (n.Slots.Count <= slot) n.Slots.Add(new Held());
            var h = n.Slots[slot];
            if (h.Scope != r.Scope) n.Slots[slot] = h = new Held { Scope = r.Scope };
            if (ReferenceEquals(h.Read, r)) return h;
            var raw = r.Data.Value.GetRawText();
            if (h.Raw == null || !string.Equals(raw, h.Raw, StringComparison.Ordinal)) {
                h.Raw = raw; h.Revision = ++revisions;
                try {
                    var records = DirectorProbeEvaluation.Records(name, r.Data.Value);
                    var parsed = new Row[records.Count];
                    for (var i = 0; i < parsed.Length; i++) { parsed[i] = new Row { Name = name, Scope = r.Scope, Value = records[i], Owner = h }; Visit(); }
                    h.Rows = parsed; h.Failure = null;
                }
                catch (Exception ex) when (ex is FormatException || ex is InvalidOperationException || ex is KeyNotFoundException || ex is JsonException) {
                    h.Rows = NoRows; h.Failure = ex.Message;
                }
            }
            h.Read = r;
            return h;
        }
        void Emit(string kind, string identity, string device, string key, string cycle, string status, DateTimeOffset at,
            Dictionary<string, string> fields = null, Dictionary<string, string[]> sets = null, string source = EvidenceSource.Platform, string command = "", string attempt = "", double? arrivalSim = null)
        {
            if (at > input.Now) { Await(at); return; }
            var o = new Observation(input.Run, kind, identity, device, key, cycle, command, attempt, status, Wall(input.Now), Wall(at), arrivalSim ?? Sim(at), source, fields, sets);
            var fingerprint = string.Join("|", o.Fields.OrderBy(p => p.Key).Select(p => p.Key + "=" + p.Value)) + string.Join("|", o.Sets.OrderBy(p => p.Key).Select(p => p.Key + "=" + string.Join(",", p.Value)));
            if (emitted.TryGetValue(o.EventKey, out var old) && old == fingerprint) return;
            emitted[o.EventKey] = fingerprint; observe(o);
        }
        public void Update(DirectorInputs inputs, Action<Observation> observe, Action<CoverageInterval> cover)
        {
            input = inputs ?? throw new ArgumentNullException(nameof(inputs)); this.observe = observe; this.cover = cover; active = null;
            if (run != inputs.Run) {
                run = inputs.Run; emitted.Clear(); alarmHistory.Clear(); previous.Clear(); trends.Clear(); wellHistory.Clear(); dueWells.Clear(); wellCycles.Clear(); pumpRunning.Clear(); pumpOffAt.Clear(); failedProbes.Clear(); commandCycles.Clear(); causeTried.Clear(); problems.Clear();
                startSim = lastSim = input.Sim; measurementReconnects = alarmReconnects = measurementIndex = 0;
                cycles.Clear(); cyclesByToken.Clear(); cyclesByOriginator.Clear(); chips.Clear(); changesSeen.Clear(); deviceByToken.Clear(); Array.Clear(bound, 0, bound.Length);
                replacementsByDevice.Clear(); assignmentsByDevice.Clear(); newestReplacement.Clear(); statusSeen = null; backfillSeen = null; bindingFresh = -1;
                wellsSeen = replacementsIndexed = assignmentsIndexed = -1; lastNow = input.Now;
                // Versions keep growing across runs, so a stamp from the old run can never equal one from the new.
                bindingVersion++; alarmVersion++; statusVersion++; backfillVersion++; changesVersion++; pumpOffVersion++;
                foreach (var n in names.Values) { n.Slots.Clear(); n.Now.Clear(); n.Last.Clear(); n.Rows.Clear(); n.Content++; n.Version++; }
                foreach (var s in new[] { alarmRows, escalations, windows, notifications, replacements, credentials, commands, rollouts, memberships, measured, runner }) { s.Stamp = -1; s.Wake = DateTimeOffset.MaxValue; s.Gaps.Clear(); s.Current.Clear(); }
            }
            if (input.Sim < lastSim) throw new InvalidOperationException("observer requires a new instance on seek");
            // A clock that steps back could un-arrive an instant a join was told had come; every join looks again.
            rewound = input.Now < lastNow; lastNow = input.Now;
            current.Clear(); var snapshot = reads(); streamRows.Clear(); complete.Clear();
            Bind(); Status();
            if (snapshot == null) {
                foreach (var name in Streams.Keys) Gap(name, lastSim, input.Sim);
                if (input.Source is PreviewSource) { current.Add("OFFLINE PREVIEW: scenario stand-ins; platform-only reads unavailable"); Preview(); }
                else current.Add("director reads waiting for bind");
            }
            else {
                foreach (var p in snapshot.Problems) current.Add(p);
                foreach (var n in names.Values) n.Group.Clear();
                for (var i = 0; i < snapshot.Reads.Count; i++) {
                    var r = snapshot.Reads[i];
                    if (r.Probe == DirectorProbe.PROBE_FAILED) failedProbes.Add(r.Name);
                    if (r.Scope != "__probe" && names.TryGetValue(r.Name, out var named)) named.Group.Add(r);
                }
                foreach (var name in failedProbes) Gap(name, startSim, input.Sim);
                foreach (var name in Streams.Keys) {
                    var n = names[name]; var failed = failedProbes.Contains(name);
                    var healthy = n.Group.Count > 0 && !failed;
                    for (var i = 0; healthy && i < n.Group.Count; i++) healthy = n.Group[i].Healthy(input.Now);
                    n.Healthy = healthy;
                    if (!healthy) Gap(name, lastSim, input.Sim);
                    n.Now.Clear();
                    for (var i = 0; i < n.Group.Count; i++) {
                        var r = n.Group[i];
                        for (var c = 0; c < r.Coverage.Count; c++) {
                            var span = r.Coverage[c];
                            if (span.Gap && Sim(span.To) >= startSim && Sim(span.From) <= input.Sim) Gap(name, Math.Max(startSim, Sim(span.From)), Math.Min(input.Sim, Sim(span.To)));
                        }
                        if (!r.Healthy(input.Now) || failed || !r.Data.HasValue) continue;
                        var h = Hold(n, i, name, r);
                        if (h.Failure != null) { problems.Add(name + ": " + h.Failure); Gap(name, lastSim, input.Sim); continue; }
                        complete.Add(r); n.Now.Add(h);
                    }
                    // What this name offers the joins: has any usable row changed, or only the instant a read was taken?
                    bool content = n.Now.Count != n.Last.Count, instance = false;
                    for (var i = 0; !content && i < n.Now.Count; i++) {
                        var last = n.Last[i]; var h = n.Now[i];
                        if (!ReferenceEquals(last.Held, h) || last.Revision != h.Revision) content = true;
                        else if (!ReferenceEquals(last.Read, h.Read)) instance = true;
                    }
                    if (content) { n.Content++; n.Rows.Clear(); foreach (var h in n.Now) n.Rows.AddRange(h.Rows); }
                    if (content || instance) {
                        n.Version++; n.Last.Clear();
                        foreach (var h in n.Now) n.Last.Add(new Seen { Held = h, Revision = h.Revision, Read = h.Read });
                    }
                }
                foreach (var stream in StreamNames) {
                    var covered = true;
                    foreach (var name in stream.Value) if (!names[name].Healthy) { covered = false; break; }
                    if (covered) cover(new CoverageInterval(run, stream.Key, lastSim, input.Sim, false));
                }
                var alarmLive = input.Source?.AlarmObservation.Observed == true;
                if (!alarmLive || snapshot.AlarmReconnects != alarmReconnects) { Gap("alarms", lastSim, input.Sim); }
                if (snapshot.MeasurementsLive != true || snapshot.MeasurementReconnects != measurementReconnects) Gap("measurements", lastSim, input.Sim);
                alarmReconnects = snapshot.AlarmReconnects; measurementReconnects = snapshot.MeasurementReconnects;
                long Content(string name) => names[name].Content; long Version(string name) => names[name].Version;
                if (Begin(alarmRows, Content("alarms") + bindingVersion)) { foreach (var r in Rows("alarms")) { Visit(); Safe("alarms", () => Alarm(r.Value)); } active = null; }
                var changed = snapshot.AlarmChanges.Count != changesSeen.Count;
                for (var i = 0; !changed && i < changesSeen.Count; i++) changed = !ReferenceEquals(changesSeen[i], snapshot.AlarmChanges[i]);
                if (changed) { changesVersion++; changesSeen.Clear(); changesSeen.AddRange(snapshot.AlarmChanges); }
                if (!failedProbes.Contains("alarms") && Begin(escalations, changesVersion + alarmVersion + bindingVersion)) {
                    foreach (var change in snapshot.AlarmChanges) Safe("alarms", () => {
                        var a = alarmHistory.Values.Where(a => S(a, "token") == change.Token && S(a, "originatorToken") == change.DeviceToken && S(a, "alarmKey") == change.Key && T(a, "raisedTime") <= change.OccurredAt)
                            .OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                        if (a.ValueKind == JsonValueKind.Undefined) return; // the poll has not supplied this cycle's identity yet
                        Emit("alarm", change.Token, Device(change.DeviceToken), change.Key, S(a, "raisedTime"), "ESCALATED", change.OccurredAt,
                            new Dictionary<string, string> { ["alarmToken"] = change.Token });
                    });
                    active = null;
                }
                if (Begin(windows, Content("windows") + bindingVersion)) { foreach (var r in Rows("windows")) { Visit(); Safe("windows", () => Window(r.Value)); } active = null; }
                // An attempt joins its alarm's cycle only under an alarm read taken after it, so a newer alarm read is a reason to look again.
                if (Begin(notifications, Content("notifications") + Version("alarms") + alarmVersion + Content("windows") + bindingVersion))
                { foreach (var r in Rows("notifications")) { Visit(); Safe("notifications", () => Notification(r.Value)); } active = null; }
                if (Begin(replacements, Content("replacements") + Content("measurements") + bindingVersion)) { foreach (var r in Rows("replacements")) { Visit(); Safe("replacements", () => Replacement(r.Value)); } active = null; }
                // a credential is a snapshot stamped with its read's as-of, so it is restated for each newer read
                if (Begin(credentials, Version("credentials") + Content("replacements") + bindingVersion)) { foreach (var r in Rows("credentials")) { Visit(); Safe("credentials", () => Credential(r)); } active = null; }
                // the newest complete alarm read this frame: a cause is resolved again only when this moves
                alarmStamp = 0; foreach (var r in complete) if (r.Name == "alarms" && r.AsOf.HasValue) alarmStamp = Math.Max(alarmStamp, r.AsOf.Value.UtcTicks);
                if (Begin(commands, Content("commands") + Version("alarms") + alarmVersion + bindingVersion)) { foreach (var r in Rows("commands")) { Visit(); Safe("commands", () => Command(r.Value)); } active = null; }
                if (Begin(rollouts, Version("rollout") + Version("artifact") + Version("signer") + Version("targets") + Version("assignments") + bindingVersion))
                { foreach (var r in Rows("rollout")) { Visit(); Safe("rollout", () => Ota(r.Value)); } active = null; }
                if (Begin(memberships, Version("membership") + alarmVersion + bindingVersion)) {
                    foreach (var h in names["membership"].Now) Safe("membership", () => { ReadWorkUnits += h.Rows.Length; Membership(h.Read.Scope, h.Rows.Select(x => x.Value).ToArray(), h.Read); });
                    active = null;
                }
                StreamMeasurements(snapshot);
                Measurements(); Chips(); Binding();
            }
            // The runner's declarations move with its status, and its outage window is named for the sim day it falls on.
            if (Begin(runner, statusVersion + (long)Math.Floor(input.Sim / 24))) { Safe("runner", Runner); active = null; }
            lastSim = input.Sim;
        }
        void Alarm(JsonElement a)
        {
            var id = S(a, "token"); var raised = S(a, "raisedTime"); var at = Time(raised); var device = Device(S(a, "originatorToken")); var key = S(a, "alarmKey");
            var state = S(a, "state"); var severity = S(a, "severity"); var stamp = DirectorProbeEvaluation.Require(a, "token");
            if (!a.TryGetProperty("raisedDuringSuppression", out stamp)) throw new FormatException("missing field raisedDuringSuppression");
            var f = new Dictionary<string, string> { ["alarmToken"] = id, ["raisedDuringSuppression"] = B(stamp.ValueKind == JsonValueKind.String) };
            if (stamp.ValueKind == JsonValueKind.String && stamp.GetString() != "restricted") f["suppressionWindowToken"] = stamp.GetString();
            Remember(id + "/" + raised, a, id, S(a, "originatorToken"), key, at);
            Emit("alarm", id, device, key, raised, "RAISED", at, f);
            if (state == "CLEARED") Emit("alarm", id, device, key, raised, "CLEARED", T(a, "clearedTime"), f);
            else if (state != "ACTIVE") throw new FormatException("unknown alarm state " + state);
        }
        void Remember(string cycle, JsonElement a, string token, string originator, string key, DateTimeOffset raised)
        {
            var raw = a.GetRawText();
            if (cycles.TryGetValue(cycle, out var c) && c.Raw == raw) return;
            alarmVersion++; alarmHistory[cycle] = a.Clone();
            var moved = c != null && c.Originator != originator;
            if (c == null) {
                cycles[cycle] = c = new Cycle { Token = token };
                if (!cyclesByToken.TryGetValue(token, out var byToken)) cyclesByToken[token] = byToken = new List<Cycle>();
                byToken.Add(c);
                if (!cyclesByOriginator.TryGetValue(originator, out var byOriginator)) cyclesByOriginator[originator] = byOriginator = new List<Cycle>();
                byOriginator.Add(c);
            }
            c.Row = alarmHistory[cycle]; c.Raw = raw; c.Originator = originator; c.Key = key; c.Raised = raised;
            if (!moved) return;
            // An alarm that changed hands: file every lifecycle again, in the order first read.
            cyclesByOriginator.Clear();
            foreach (var each in cycles.Values) {
                if (!cyclesByOriginator.TryGetValue(each.Originator, out var list)) cyclesByOriginator[each.Originator] = list = new List<Cycle>();
                list.Add(each);
            }
        }
        /// <summary>The latest of these lifecycles the test admits; of two raised at the same instant, the one read first.</summary>
        static Cycle Latest(List<Cycle> list, Func<Cycle, bool> admits)
        {
            Cycle best = null;
            if (list != null) foreach (var c in list) if (admits(c) && (best == null || c.Raised > best.Raised)) best = c;
            return best;
        }
        void Window(JsonElement w)
        {
            if (S(w, "kind") != "ONE_SHOT") throw new FormatException("lamp service requires ONE_SHOT");
            var id = S(w, "token"); var start = T(w, "startTime"); var end = T(w, Optional(w, "endedAt") != null ? "endedAt" : "endTime");
            foreach (var target in w.GetProperty("targets").EnumerateArray()) {
                if (S(target, "type") != "DEVICE") continue;
                var d = Device(S(target, "token"));
                foreach (var key in w.GetProperty("alarmKeys").EnumerateArray().Select(v => v.GetString())) {
                    Emit("window", id, d, key, "", "ACTIVE", start);
                    if (S(w, "status") == "ENDED" || input.Now >= end) Emit("window", id, d, key, "", "ENDED", end);
                    else Await(end); // a window ends by the clock too, with no new read to say so
                }
            }
        }
        public static bool NotificationInCycle(DateTimeOffset attempted, DateTimeOffset raised, DateTimeOffset? nextRaised)
            => attempted >= raised && (!nextRaised.HasValue || attempted < nextRaised.Value);
        JsonElement CycleAt(string token, DateTimeOffset at)
        {
            cyclesByToken.TryGetValue(token, out var known);
            var candidate = Latest(known, c => c.Raised <= at);
            if (candidate == null) return default;
            var device = candidate.Originator;
            // A cached lifecycle cannot exclude a newer raise until this device's complete read covers the record.
            foreach (var h in names["alarms"].Now)
                if (h.Read.AsOf >= at && (h.Read.Scope == device || h.Rows.Any(a => S(a.Value, "token") == token))) return candidate.Row;
            return default;
        }
        void Notification(JsonElement n)
        {
            var id = S(n, "id"); var token = S(n, "alarmToken"); var at = T(n, "attemptedAt");
            var alarm = CycleAt(token, at);
            if (alarm.ValueKind == JsonValueKind.Undefined) return; // another cycle/run, outside the queried window
            var key = S(alarm, "alarmKey"); var f = new Dictionary<string, string> { ["alarmToken"] = token, ["trigger"] = S(n, "trigger") };
            var policy = Optional(n, "policyToken"); if (key == "lamp-flicker" && policy == null) throw new FormatException("missing notification policyToken");
            if (policy != null) f["policy"] = policy;
            var stamp = Optional(alarm, "raisedDuringSuppression");
            var window = Rows("windows").Select(r => r.Value).FirstOrDefault(w => S(w, "token") == stamp);
            if (window.ValueKind != JsonValueKind.Undefined) {
                var close = T(window, Optional(window, "endedAt") != null ? "endedAt" : "endTime");
                f["afterClose"] = B(at >= close);
            }
            var clear = Optional(alarm, "clearedTime"); f["alarmActive"] = B(clear == null || at < Time(clear));
            Emit("attempt", id, Device(S(alarm, "originatorToken")), key == "light-silent" || key == "charger-offline" ? "absence" : key,
                S(alarm, "raisedTime"), S(n, "status"), at, f, attempt: id);
        }
        public static readonly TimeSpan ReplacementReadingWait = TimeSpan.FromSeconds(60);
        IEnumerable<JsonElement> History(string device) => Rows("measurements").Select(r => r.Value).Where(r => S(r, "deviceToken") == device);
        void Replacement(JsonElement r)
        {
            var id = S(r, "id"); var token = S(r.GetProperty("device"), "token"); var device = Device(token); var at = T(r, "occurredTime");
            var history = History(token).Where(m => S(m, "name") == Metrics.WaterLevel).OrderBy(m => T(m, "occurredTime")).ToArray();
            var before = history.LastOrDefault(m => T(m, "occurredTime") < at); var after = history.FirstOrDefault(m => T(m, "occurredTime") > at);
            var f = new Dictionary<string, string>();
            if (before.ValueKind != JsonValueKind.Undefined) f["beforeReading"] = S(before, "id");
            if (after.ValueKind != JsonValueKind.Undefined) f["afterReading"] = S(after, "id");
            // The journal entry is a page trigger, and a trigger is captured once: emitted before the first reading after the swap
            // has been read, its claims could never join that reading. Hold it until the reading is known, or for a minute at most
            // (then it is emitted without one, and the claims that need the reading stay unmet).
            if (!f.ContainsKey("afterReading") && input.Now - at < ReplacementReadingWait) { Await(at + ReplacementReadingWait); return; }
            Emit("replacement", id, device, "", S(r, "occurredTime"), "RECORDED", at, f,
                new Dictionary<string, string[]> { ["disabledCredentials"] = r.GetProperty("retiredCredentialTokens").EnumerateArray().Select(v => v.GetString()).ToArray() });
        }
        Dictionary<string, string> SnapshotFields(DirectorRead read, Dictionary<string, string> fields = null)
        {
            var f = fields ?? new Dictionary<string, string>(); f["asOf"] = N(Wall(read.AsOf.Value)); return f;
        }
        void Credential(Row row)
        {
            var c = row.Value;
            var id = S(c, "token"); var device = Device(S(c.GetProperty("device"), "token"));
            var enabled = DirectorProbeEvaluation.Require(c, "enabled").GetBoolean();
            foreach (var r in Rows("replacements").Select(r => r.Value).Where(r => S(r.GetProperty("device"), "token") == S(c.GetProperty("device"), "token") && r.GetProperty("retiredCredentialTokens").EnumerateArray().Any(t => t.GetString() == id)))
                Emit("credential", id, device, "", S(r, "occurredTime"), enabled ? "ENABLED" : "DISABLED", input.Now,
                    SnapshotFields(row.Read, new Dictionary<string, string> { ["enabled"] = B(enabled), ["replacementToken"] = S(r, "id") }));
        }
        /// <summary>
        /// A served instant, cut to the 100 ns this runtime holds. A cause's detectedAt carries up to nine fractional digits,
        /// which not every runtime's parser accepts; cutting (never rounding up) keeps "raised at or before the detection" true
        /// for an alarm whose raisedTime is the same instant stored at microseconds.
        /// </summary>
        public static DateTimeOffset Instant(string value)
        {
            var dot = (value ?? "").IndexOf('.');
            if (dot >= 0) {
                var end = dot + 1; while (end < value.Length && char.IsDigit(value[end])) end++;
                if (end - dot - 1 > 7) value = value.Substring(0, dot + 8) + value.Substring(end);
            }
            return Time(value);
        }
        /// <summary>
        /// The alarm cycle a command's recorded cause names, or why it names none. The platform records what the rule knew when
        /// it sent the command (its alarm KEY, the device row, the detection's time), never an alarm token, so the alarm is the
        /// row for this device token and key whose originatorId is the cause's deviceId (a device token can be reused). The
        /// cycle is named only when that row was raised at or before the detection and not cleared before it; anything else is
        /// reported unresolved, because the cached lifecycles are not a complete ledger of an alarm's cycles.
        /// </summary>
        bool TryCauseCycle(string deviceToken, JsonElement cause, out JsonElement alarm, out string unresolved, out bool pending)
        {
            alarm = default; unresolved = null; pending = false;
            var key = Optional(cause, "alarmKey"); var deviceId = Optional(cause, "deviceId");
            if (key == null) { unresolved = "the cause names no alarm key"; return false; }
            if (deviceId == null) { unresolved = "the cause names no device"; return false; }
            var detected = Instant(S(cause, "detectedAt"));
            // Only a complete alarm read taken at or after the detection can show the cycle the detection met. One taken before it is
            // followed by a newer one within a poll, so the command waits for that; with no complete alarm read at all there is nothing
            // to wait for, and the command is evidence on its own (it is tied to its alarm when the alarm reads come back).
            var covering = complete.Where(r => r.Name == "alarms" && r.AsOf >= detected).ToArray();
            if (covering.Length == 0) {
                if (complete.Any(r => r.Name == "alarms")) pending = true; else unresolved = "no alarm read to find the alarm in";
                return false;
            }
            var named = Rows("alarms").Where(a => covering.Contains(a.Read) && S(a.Value, "originatorToken") == deviceToken && S(a.Value, "alarmKey") == key).Select(a => a.Value).ToArray();
            if (named.Length == 0) { unresolved = "no alarm with that key on the device"; return false; }
            var rows = named.Where(a => Optional(a, "originatorId") == deviceId).OrderByDescending(a => T(a, "raisedTime")).ToArray();
            if (rows.Length == 0) { unresolved = "the alarm belongs to another device record"; return false; }
            var row = rows[0];
            if (T(row, "raisedTime") > detected) {
                // The row is a later cycle. An earlier one counts only if its own record shows it outlived the detection.
                var token = S(row, "token");
                row = alarmHistory.Values.Where(a => S(a, "token") == token && Optional(a, "originatorId") == deviceId && T(a, "raisedTime") <= detected)
                    .OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                if (row.ValueKind == JsonValueKind.Undefined || Optional(row, "clearedTime") == null) { unresolved = "the alarm's cycle began after the detection"; return false; }
            }
            var cleared = Optional(row, "clearedTime");
            if (cleared != null && Time(cleared) <= detected) { unresolved = "the alarm was cleared before the detection"; return false; }
            alarm = row; return true;
        }
        void Command(JsonElement c)
        {
            var id = S(c, "token"); var deviceToken = S(c, "deviceToken"); var device = Device(deviceToken); var key = S(c, "name"); var f = new Dictionary<string, string>(); string alarmToken = null; var cycle = "";
            // The served Command has no alarm foreign key. Its recorded cause is the only evidence that an alarm's rule sent it.
            if (commandCycles.TryGetValue(id, out var bound)) { alarmToken = bound.Key; cycle = bound.Value; f["cause"] = "ALARM"; }
            else if (c.TryGetProperty("cause", out var cause) && cause.ValueKind == JsonValueKind.Object) {
                // A cause that cannot be read costs the link, never the command: the command's own states are still evidence.
                var kind = Optional(cause, "kind");
                if (kind == null) { f["causeUnresolved"] = "the cause has no kind"; problems.Add("commands: cause without a kind for " + id); }
                else f["cause"] = kind;
                if (kind == "ALARM") {
                    // Resolved against the alarm reads, so tried again only when those have moved on (a few commands a day, but every frame).
                    if (causeTried.TryGetValue(id, out var tried) && tried.Key == alarmStamp) {
                        if (tried.Value == null) return; // still waiting for an alarm read that reaches the detection
                        f["causeUnresolved"] = tried.Value;
                    }
                    else {
                        JsonElement alarm = default; string unresolved = null; var pending = false; bool tied;
                        CauseResolutions++;
                        try { tied = TryCauseCycle(deviceToken, cause, out alarm, out unresolved, out pending); }
                        catch (FormatException ex) { tied = false; pending = false; unresolved = "the cause cannot be read (" + ex.Message + ")"; problems.Add("commands: " + ex.Message + " in the cause of " + id); }
                        if (tied) {
                            alarmToken = S(alarm, "token"); cycle = S(alarm, "raisedTime"); commandCycles[id] = new KeyValuePair<string, string>(alarmToken, cycle); causeTried.Remove(id);
                        }
                        else {
                            causeTried[id] = new KeyValuePair<long, string>(alarmStamp, pending ? null : unresolved);
                            if (pending) return; // the alarm read has not reached the detection yet
                            f["causeUnresolved"] = unresolved;
                        }
                    }
                }
            }
            if (alarmToken != null) f["alarmToken"] = alarmToken;
            var payload = Optional(c, "payload");
            if (payload != null) { using var p = JsonDocument.Parse(payload); if (p.RootElement.TryGetProperty("max_kw", out var max)) f["max_kw"] = StrictJson.Scalar(max); }
            foreach (var state in new[] { "QUEUED", "SENT", S(c, "status") }.Distinct()) {
                var field = state == "QUEUED" ? "queuedTime" : state == "SENT" ? "sentTime" : "respondedTime";
                var at = Optional(c, field);
                if (at != null) Emit("command", id, device, key, cycle, state, Time(at), f, command: id);
                else if (state == S(c, "status")) problems.Add("commands: missing " + field + " for " + id);
            }
        }
        void Ota(JsonElement rollout)
        {
            var id = S(rollout, "token"); var cycle = S(rollout, "startedAt"); var at = Time(cycle); var artifact = S(rollout.GetProperty("artifact"), "token");
            Dictionary<string, string> Fields() => new Dictionary<string, string> { ["artifactToken"] = artifact, ["rolloutToken"] = id };
            var f = Fields(); f["maxInFlight"] = N(rollout.GetProperty("maxInFlight").GetInt32());
            var state = S(rollout, "state");
            var terminal = state == "COMPLETED" || state == "ABORTED";
            if (terminal) Emit("rollout", id, "", "", cycle, "TERMINAL", T(rollout, "finishedAt"), f);
            if (state != "COMPLETED" && state != "ABORTED")
                Emit("rollout", id, "", "", cycle, state, state == "RUNNING" ? at : T(rollout, "stateChangedAt"), f);
            foreach (var row in Rows("artifact").Where(r => S(r.Value, "token") == artifact)) {
                var a = row.Value;
                var sig = a.GetProperty("signatures").EnumerateArray().FirstOrDefault(s => s.GetProperty("countsTowardTrust").GetBoolean() && S(s.GetProperty("signer"), "state") == "ACTIVE" &&
                    Rows("signer").Any(r => S(r.Value, "token") == S(s.GetProperty("signer"), "token") && S(r.Value, "keyId") == S(s.GetProperty("signer"), "keyId") && S(r.Value, "state") == "ACTIVE"));
                var af = Fields(); if (sig.ValueKind != JsonValueKind.Undefined) af["signer"] = S(sig.GetProperty("signer"), "token");
                // SIGNED is the platform's verified trust status; UpdateState.VERIFIED is integrity-only and is never used here.
                Emit("artifact", artifact, "", "", cycle, S(a, "trustStatus") == "SIGNED" && af.ContainsKey("signer") ? "VERIFIED" : "UNVERIFIED", input.Now, SnapshotFields(row.Read, af));
            }
            var targets = Rows("targets").Where(t => t.Scope == id).Select(t => t.Value).ToArray(); int refused = 0;
            var finished = terminal ? T(rollout, "finishedAt") : input.Now;
            bool countsComplete = complete.Any(r => r.Name == "targets" && r.Scope == id && r.AsOf >= finished &&
                r.Data.Value.GetProperty(DirectorProbeEvaluation.Roots["targets"]).ValueKind != JsonValueKind.Null) &&
                targets.Length == rollout.GetProperty("progress").GetProperty("total").GetInt32() && targets.Select(t => S(t, "deviceToken")).Distinct().Count() == targets.Length;
            foreach (var t in targets) {
                var token = S(t, "deviceToken"); var assignment = Optional(t, "assignmentToken");
                if (S(t, "outcome") == "UNKNOWN" || S(t, "outcome") == "IN_FLIGHT") countsComplete = false;
                var a = Rows("assignments").Select(r => r.Value).FirstOrDefault(a => S(a, "token") == assignment && S(a, "deviceToken") == token && S(a.GetProperty("artifact"), "token") == artifact);
                string attempt = "";
                if (a.ValueKind != JsonValueKind.Undefined) {
                    if (!Rows("assignments").Any(r => r.Value.Equals(a) && r.Read.AsOf >= finished)) countsComplete = false;
                    var p = a.GetProperty("attempt"); attempt = S(p, "token"); var pf = Fields(); var ps = S(p, "state");
                    if (p.TryGetProperty("failure", out var failure) && failure.ValueKind != JsonValueKind.Null) {
                        pf["code"] = S(failure, "code"); pf["platform"] = B(failure.GetProperty("platform").GetBoolean());
                        if (ps == "FAILED" && pf["code"] == "VERIFICATION_FAILED" && pf["platform"] == "false") refused++;
                    }
                    else if (ps == "FAILED") countsComplete = false;
                    if (S(t, "outcome") == "FAILED" && ps != "FAILED") countsComplete = false;
                    Emit("attempt", attempt, Device(token), "", cycle, ps, T(p, "lastReportAt"), pf, attempt: attempt);
                }
                else if (assignment != null || S(t, "outcome") == "FAILED" || S(t, "outcome") == "SUCCEEDED") countsComplete = false;
                var targetRead = Rows("targets").First(r => r.Scope == id && S(r.Value, "deviceToken") == token).Read;
                Emit("target", id + "/" + token, Device(token), "", cycle, S(t, "outcome"), input.Now, SnapshotFields(targetRead, Fields()), attempt: attempt);
            }
            if (terminal) {
                if (!countsComplete) { Trouble("rollout counts: complete child reads covering terminal state and resolved attempts required"); Gap("targets", lastSim, input.Sim); active?.Gaps.Add("targets"); return; }
                f = Fields(); f["maxInFlight"] = N(rollout.GetProperty("maxInFlight").GetInt32());
                f["updated"] = N(rollout.GetProperty("progress").GetProperty("succeeded").GetInt32()); f["refused"] = N(refused);
                Emit("rollout", id, "", "", cycle, "COUNTS", input.Now, SnapshotFields(complete.First(r => r.Name == "targets" && r.Scope == id), f));
            }
        }
        void Membership(string quay, JsonElement[] members, DirectorRead read)
        {
            var ids = members.Select(m => Device(S(m, "token"))).ToArray();
            foreach (var a in alarmHistory.Values.Where(a => S(a, "alarmKey") == "light-silent" || S(a, "alarmKey") == "charger-offline")) {
                if (quay != "hp-east-quay") continue;
                var expected = ids.Where(id => { var d = layout.Devices.First(x => x.Token == id); return d.Kind == DeviceKind.Light || d.Kind == DeviceKind.Charger; }).ToArray();
                Emit("membership", "east-absence", "", "", S(a, "raisedTime"), "", input.Now, SnapshotFields(read), sets: new Dictionary<string, string[]> { ["tokens"] = expected });
            }
            Emit("membership", quay, "", quay, "", "SNAPSHOT", input.Now, SnapshotFields(read), sets: new Dictionary<string, string[]> { ["tokens"] = ids });
        }
        /// <summary>A device's newest replacement in the journal read, indexed once per distinct answer.</summary>
        JsonElement NewestReplacement(string token)
        {
            var content = names["replacements"].Content;
            if (replacementsIndexed != content) {
                replacementsIndexed = -1; replacementsByDevice.Clear(); newestReplacement.Clear();
                foreach (var x in Rows("replacements")) {
                    var device = S(x.Value.GetProperty("device"), "token");
                    if (!replacementsByDevice.TryGetValue(device, out var list)) replacementsByDevice[device] = list = new List<JsonElement>();
                    list.Add(x.Value);
                }
                replacementsIndexed = content;
            }
            if (newestReplacement.TryGetValue(token, out var newest)) return newest;
            newest = default;
            if (replacementsByDevice.TryGetValue(token, out var journal)) {
                var best = DateTimeOffset.MinValue;
                foreach (var x in journal) { var at = T(x, "occurredTime"); if (newest.ValueKind == JsonValueKind.Undefined || at > best) { best = at; newest = x; } }
            }
            return newestReplacement[token] = newest;
        }
        static readonly List<JsonElement> NoAssignments = new List<JsonElement>();
        List<JsonElement> Assignments(string token)
        {
            var content = names["assignments"].Content;
            if (assignmentsIndexed != content) {
                assignmentsIndexed = -1; assignmentsByDevice.Clear();
                foreach (var x in Rows("assignments")) {
                    var device = S(x.Value, "deviceToken");
                    if (!assignmentsByDevice.TryGetValue(device, out var list)) assignmentsByDevice[device] = list = new List<JsonElement>();
                    list.Add(x.Value);
                }
                assignmentsIndexed = content;
            }
            return assignmentsByDevice.TryGetValue(token, out var found) ? found : NoAssignments;
        }
        void Measurements()
        {
            // The stream's new readings are joined on the frame they arrive. The history reads are joined again only when they, or
            // something a reading is joined to (an alarm's lifecycle, the replacement journal, a failed update, a pump's last stop,
            // the runner's buffered spans, a device's binding), have changed, or when a reading stamped after "now" has come due:
            // otherwise every one of them would emit what it emitted before. When they are joined they are sorted in with the
            // stream's, as they always were, so a reading's trend is still taken from the reading before it.
            var history = Begin(measured, names["measurements"].Content + alarmVersion + names["replacements"].Content + names["assignments"].Content
                + names["rollout"].Content + names["targets"].Content + pumpOffVersion + backfillVersion + bindingVersion);
            active = null;
            if (!history && streamRows.Count == 0) return;
            foreach (var row in (history ? Rows("measurements").Concat(streamRows) : streamRows).OrderBy(x => Optional(x.Value, "occurredTime"))) {
                var r = row.Value;
                // only a history row is looked at again later: a stream row is offered once, on the frame it arrives
                active = row.Owner != null ? measured : null;
                Safe("measurements", () => {
                    MeasurementWorkUnits++; if (row.Owner != null) Visit();
                    var token = S(r, "deviceToken"); var device = Device(token); var key = S(r, "name"); var at = T(r, "occurredTime");
                    var value = DirectorProbeEvaluation.Require(r, "value").GetDouble(); var f = new Dictionary<string, string> { ["value"] = N(value) }; var cycle = "";
                    var pk = device + "/" + key;
                    var readingKey = S(r, "id");
                    if (trends.TryGetValue(readingKey, out var trend)) foreach (var p in trend) f[p.Key] = p.Value;
                    else if (previous.TryGetValue(pk, out var old) && at > old.OccurredAt) {
                        f["rising"] = B(value > old.Value); f["falling"] = B(value < old.Value);
                        trends[readingKey] = new Dictionary<string, string> { ["rising"] = f["rising"], ["falling"] = f["falling"] };
                    }
                    if (!previous.ContainsKey(pk) || at > previous[pk].OccurredAt) previous[pk] = new Reading(value, at);
                    cyclesByOriginator.TryGetValue(token, out var lifecycles);
                    var alarm = Latest(lifecycles, c => c.Raised <= at);
                    if (alarm != null) cycle = S(alarm.Row, "raisedTime");
                    // A filling-well interval starts with a reported off reading, before its high alarm exists.
                    // Retain those earlier readings and attach them to the subsequently observed alarm cycle;
                    // otherwise an above-float reading before the alarm could disappear from allBelow's proof.
                    if (key == Metrics.WetWell && pumpOffAt.TryGetValue(device, out var off) && at >= off) {
                        var high = Latest(lifecycles, c => c.Key == "wet-well-high" && c.Raised >= off && Reached(c.Raised));
                        if (high != null) cycle = S(high.Row, "raisedTime");
                    }
                    var replacement = NewestReplacement(token);
                    if (replacement.ValueKind != JsonValueKind.Undefined) { cycle = S(replacement, "occurredTime"); f["replacementToken"] = S(replacement, "id"); f["afterReplacement"] = B(at > T(replacement, "occurredTime")); }
                    Emit("measure", S(r, "id"), device, key, cycle, "", at, f);
                    foreach (var assignment in Assignments(token)) {
                        var p = assignment.GetProperty("attempt"); if (S(p, "state") != "FAILED" || at <= T(p, "lastReportAt") || input.Now - at > TimeSpan.FromSeconds(60)) continue;
                        foreach (var rollout in Rows("rollout").Select(x => x.Value).Where(x => S(x.GetProperty("artifact"), "token") == S(assignment.GetProperty("artifact"), "token") &&
                            Rows("targets").Any(t => t.Scope == S(x, "token") && Optional(t.Value, "assignmentToken") == S(assignment, "token") && S(t.Value, "deviceToken") == token))) {
                            var af = new Dictionary<string, string>(f) { ["afterFailure"] = "true", ["artifactToken"] = S(assignment.GetProperty("artifact"), "token"), ["rolloutToken"] = S(rollout, "token") };
                            Emit("measure", S(r, "id"), device, key, S(rollout, "startedAt"), "", at, af, attempt: S(p, "token"));
                        }
                    }
                    foreach (var b in input.Source?.Status?.Backfill ?? Array.Empty<StoryBackfill>()) {
                        if (RunnerDevice(b.Device) != device || at < b.First || at > b.Last) continue;
                        if (at >= input.Now) { Await(at); continue; } // counted as backdated only once its instant is behind us
                        // Include unexpected platform times too: filtering by the expected set would falsely prove exactness.
                        Emit("measure", S(r, "id"), device, "backfill", "", "", at,
                            new Dictionary<string, string> { ["value"] = N(value), ["backdated"] = "true", ["readingIdentity"] = ReadingIdentity(device, S(r, "occurredTime")) }, arrivalSim: input.Sim);
                    }
                });
            }
            active = null;
        }
        void StreamMeasurements(DirectorReadSnapshot snapshot)
        {
            var fresh = new List<DirectorMeasurement>();
            // IReadOnlyList.Skip enumerated the entire prefix on Unity's runtime.
            for (var i = measurementIndex; i < snapshot.Measurements.Count; i++) fresh.Add(snapshot.Measurements[i]);
            var addedWells = new HashSet<string>();
            foreach (var reading in fresh.OrderBy(m => m.OccurredAt)) {
                var token = reading.DeviceToken; var metric = reading.Name; var device = Device(token);
                if (metric == Metrics.PumpRunning && (!pumpRunning.TryGetValue(device, out var old) || reading.OccurredAt > old.OccurredAt)) {
                    if (reading.Value <= .5 && (!pumpRunning.ContainsKey(device) || old.Value > .5)) { pumpOffAt[device] = reading.OccurredAt; pumpOffVersion++; }
                    pumpRunning[device] = new Reading(reading.Value, reading.OccurredAt);
                }
                var id = token + "/" + metric + "@" + reading.OccurredAt.ToString("O");
                var json = DirectorWire.Json(w => {
                    w.WriteStartObject(); w.WriteString("id", id); w.WriteString("deviceToken", token); w.WriteString("name", metric);
                    w.WriteString("occurredTime", reading.OccurredAt.ToString("O")); w.WriteNumber("value", reading.Value); w.WriteEndObject();
                });
                using var doc = JsonDocument.Parse(json); var record = doc.RootElement.Clone();
                if (metric == Metrics.WetWell) {
                    wellHistory[id] = record; addedWells.Add(id);
                    if (reading.OccurredAt > input.Now) {
                        if (!dueWells.TryGetValue(reading.OccurredAt, out var due)) dueWells[reading.OccurredAt] = due = new HashSet<string>();
                        due.Add(id);
                    }
                }
                else streamRows.Add(new Row { Name = "streamMeasurements", Scope = token, Value = record });
            }
            measurementIndex = snapshot.Measurements.Count;
            // Visit only the due prefix, never the retained archive on idle frames. Resolve IDs
            // through wellHistory so corrections received while deferred use their final value.
            while (dueWells.Count > 0) {
                var due = dueWells.First();
                if (due.Key > input.Now) break;
                addedWells.UnionWith(due.Value); dueWells.Remove(due.Key);
            }
            // Collapse the entire fresh batch before emitting: equal timestamps share an event
            // and observation time, so the store must see exactly one final revision.
            foreach (var id in addedWells) {
                var record = wellHistory[id];
                if (T(record, "occurredTime") <= input.Now)
                    streamRows.Add(new Row { Name = "streamMeasurements", Scope = S(record, "deviceToken"), Value = record });
            }
            // Wells already emitted need revisiting only when their alarm/off-interval join
            // changes, so an arriving high alarm still gets ALL pre-alarm float evidence.
            // The join is a function of the alarm lifecycles, each pump's last stop and its binding: unchanged, it names no well.
            if (wellsSeen == alarmVersion + pumpOffVersion + bindingVersion && !RunEveryJoin) return;
            wellsSeen = alarmVersion + pumpOffVersion + bindingVersion;
            var changed = new HashSet<string>();
            foreach (var pump in layout.Devices.Where(d => d.Kind == DeviceKind.Pump)) {
                if (input.Source == null || !input.Source.TryToken(pump.ExternalId, out var token)) continue;
                var cycle = string.Join("|", alarmHistory.Where(a => S(a.Value, "originatorToken") == token).Select(a => a.Key).OrderBy(k => k, StringComparer.Ordinal))
                    + "/" + (pumpOffAt.TryGetValue(pump.Token, out var off) ? off.ToString("O") : "");
                if (!wellCycles.TryGetValue(token, out var old) || old != cycle) { wellCycles[token] = cycle; changed.Add(token); }
            }
            if (changed.Count > 0) foreach (var p in wellHistory) {
                var token = S(p.Value, "deviceToken");
                if (changed.Contains(token) && !addedWells.Contains(p.Key) && T(p.Value, "occurredTime") <= input.Now)
                    streamRows.Add(new Row { Name = "streamMeasurements", Scope = token, Value = p.Value });
            }
        }
        void Binding()
        {
            var fresh = 0;
            foreach (var d in layout.Devices) foreach (var m in HarborObservedModel.MetricsFor(d.Kind))
                if (input.Source != null && input.Source.TryGet(d.ExternalId, m, out var r) && r.OccurredAt <= input.Now && input.Now - r.OccurredAt <= TimeSpan.FromSeconds(60)) { fresh++; break; }
            // The record is keyed by the run alone, so it is new only when its one field is.
            if (fresh == bindingFresh) return;
            bindingFresh = fresh;
            Emit("binding", input.Run, "", "", "", "READY", input.Now, new Dictionary<string, string> { ["freshDevices"] = N(fresh) });
        }
        void Chips()
        {
            if (input.Frame?.Pumps == null) return;
            foreach (var pump in input.Frame.Pumps) {
                var p = pump.Value;
                if (!chips.TryGetValue(pump.Key, out var chip)) chips[pump.Key] = chip = new Chip();
                var when = p.Running.HasValue ? p.TransitionAt ?? p.Running.Value.OccurredAt : default;
                // A pump's chip is its start as last presented, joined to its high-well alarm: the same until either moves.
                if (!chip.Seen || chip.Has != p.Running.HasValue || chip.At != when || chip.Kind != p.StartKind)
                { chip.Seen = true; chip.Has = p.Running.HasValue; chip.At = when; chip.Kind = p.StartKind; chip.Section.Stamp = -1; }
                if (!Begin(chip.Section, alarmVersion + bindingVersion)) continue;
                Safe("pump", () => {
                    if (!p.Running.HasValue) return;
                    var at = p.TransitionAt ?? p.Running.Value.OccurredAt;
                    var device = RunnerDevice(pump.Key);
                    var a = alarmHistory.Values.Where(a => Device(S(a, "originatorToken")) == device && S(a, "alarmKey") == "wet-well-high" && T(a, "raisedTime") <= at).OrderByDescending(a => T(a, "raisedTime")).FirstOrDefault();
                    var f = new Dictionary<string, string> { ["runningAt"] = N(Wall(at)) };
                    var cycle = "";
                    if (a.ValueKind != JsonValueKind.Undefined) { f["alarmToken"] = S(a, "token"); cycle = S(a, "raisedTime"); }
                    Emit("chip", device + "@" + at.ToString("O"), device, "", cycle, p.StartKind == PumpStartKind.Platform ? "PLATFORM" : p.StartKind == PumpStartKind.FloatConsistent ? "FLOAT-CONSISTENT" : "UNCONFIRMED", at, f);
                });
                active = null;
            }
        }
        void Runner()
        {
            var status = input.Source?.Status; if (status?.StartedAt == null) return;
            var metadata = new Dictionary<string, string>();
            if (status.LayoutDigest != null) metadata["layoutDigest"] = status.LayoutDigest;
            Emit("ledger", "run/" + input.Run, "", "run", "", "", status.StartedAt.Value, metadata,
                new Dictionary<string, string[]> { ["defects"] = status.Defects.ToArray() }, EvidenceSource.Scenario);
            foreach (var job in status.Jobs.Where(j => j.WindowOpens.HasValue && j.WindowCloses.HasValue && !string.IsNullOrEmpty(j.Device))) {
                var device = RunnerDevice(job.Device); var id = job.Window ?? job.Campaign;
                Emit("window", id, device, "", "", "ACTIVE", job.WindowOpens.Value, source: EvidenceSource.Scenario);
                Emit("window", id, device, "", "", "ENDED", job.WindowCloses.Value, source: EvidenceSource.Scenario);
            }
            foreach (var beat in status.Beats.Where(b => b.Name == "feeder-outage")) {
                var day = Math.Floor(input.Sim / 24); var from = status.ToWallTime((day * 24 + beat.From) * 3600); var to = status.ToWallTime((day * 24 + beat.To) * 3600);
                Emit("window", "feeder-outage/" + day, "", "feeder-outage", "", "ACTIVE", from, source: EvidenceSource.Scenario);
                Emit("window", "feeder-outage/" + day, "", "feeder-outage", "", "ENDED", to, source: EvidenceSource.Scenario);
            }
            var replacement = status.Replacement;
            if (replacement != null) Emit("replacement", replacement.Device + "@" + replacement.At.ToString("O"), RunnerDevice(replacement.Device), "", "", "", replacement.At, source: EvidenceSource.Scenario);
            foreach (var group in status.Backfill.GroupBy(b => b.Day)) {
                var at = status.ToWallTime((group.Key * 24 + layout.Beat("feeder-outage").To) * 3600);
                Emit("ledger", "backfill/" + group.Key, "", "backfill", "", "", at, sets: new Dictionary<string, string[]> { ["times"] = group.SelectMany(b => b.Times.Select(t => ReadingIdentity(RunnerDevice(b.Device), t))).ToArray() }, source: EvidenceSource.Scenario);
            }
            foreach (var drain in status.RestoreDrains) {
                var at = drain.FirstRefusalAt ?? drain.CaughtUpAt;
                if (at.HasValue) Emit("ledger", "restore/" + drain.Day, "", "restore", "", "", at.Value, new Dictionary<string, string> { ["value"] = N(drain.Refused429) }, source: EvidenceSource.DeviceSide);
                else if (drain.Refused429 > 0) problems.Add("runner: restore refusal count has no instant");
            }
        }
        void Preview()
        {
            foreach (var d in layout.Devices) foreach (var metric in HarborObservedModel.MetricsFor(d.Kind)) if (input.Source.TryGet(d.ExternalId, metric, out var r))
                Emit("measure", "preview:" + d.ExternalId + "/" + metric + "@" + r.OccurredAt.ToString("O"), d.ExternalId, metric, "", "", r.OccurredAt,
                    new Dictionary<string, string> { ["value"] = N(r.Value) }, source: EvidenceSource.Scenario);
        }
    }
}
