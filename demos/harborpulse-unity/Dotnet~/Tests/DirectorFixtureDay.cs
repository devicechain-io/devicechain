// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text;
using System.Text.Json;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// One compressed harbour day as the live player's observer is fed it: the polled director reads (a new read object every
    /// three seconds whether or not anything changed, as the live poller publishes them), the measurement stream, and the
    /// runner's status. Its sizes are those of a recorded live day: about forty alarms, as many notification attempts, a
    /// replaced gauge with a twenty-row history, 25 stream readings a second, and a feeder outage whose restore puts 720
    /// buffered rows into two history reads and as many again on the stream. A few things are in it to make each join come
    /// back for something: an alarm the read lists late, a second gauge swap whose first reading never arrives, a runner
    /// that declares its buffered span past the restore, and (when asked) an alarm on a device outside the scene. Everything is a function of the wall second
    /// asked for, so two observers given the same frames are given the same day.
    /// </summary>
    sealed class DirectorFixtureDay
    {
        public static readonly DateTimeOffset Epoch = DateTimeOffset.Parse("2026-01-01T00:00:00Z", CultureInfo.InvariantCulture);
        public const double OutageFrom = 825, OutageTo = 915, RolloutAt = 1200, RolloutDone = 1245, PollS = 3;
        public static DateTimeOffset At(double wall) => Epoch.AddTicks((long)Math.Round(wall * TimeSpan.TicksPerSecond));
        static string Ts(double wall) => At(wall).UtcDateTime.ToString("yyyy-MM-ddTHH:mm:ss.ffffffZ", CultureInfo.InvariantCulture);
        static string Q(string value) => value == null ? "null" : "\"" + value + "\"";
        static string F(double value) => value.ToString("R", CultureInfo.InvariantCulture);

        public sealed class Source : IHarborSource
        {
            public readonly Dictionary<string, string> Tokens = new Dictionary<string, string>(StringComparer.Ordinal);
            public ScenarioStatus Status { get; set; }
            public Dictionary<string, Reading> Values { get; } = new Dictionary<string, Reading>(StringComparer.Ordinal);
            public IReadOnlyList<HarborAlarm> Alarms => Array.Empty<HarborAlarm>();
            public IReadOnlyList<HarborCommand> Commands => Array.Empty<HarborCommand>();
            public CommandCoverage CommandPoll => new CommandCoverage(Epoch, Epoch.AddDays(1));
            public AlarmCoverage AlarmObservation => new AlarmCoverage(true, true);
            public RolloutEvidence Rollout => null;
            public IReadOnlyList<SuppressionEvidence> Suppressions => Array.Empty<SuppressionEvidence>();
            public string ReadProblem => null;
            public bool TryToken(string id, out string token) => Tokens.TryGetValue(id ?? "", out token);
            public bool TryGet(string id, string metric, out Reading reading) => Values.TryGetValue(id + "/" + metric, out reading);
            public bool TryLocation(string id, out HarborLocation location) { location = default; return false; }
            public bool TryAttribute(string id, string key, out Reading reading) { reading = default; return false; }
        }

        sealed class AlarmSpec { public string Token, Device, Key, Stamp, Severity = "MINOR"; public double Raised; public double? Cleared, Late; public double Listed => Late ?? Raised; }
        sealed class Job { public string Name, Scope; public double Phase, From; public Func<double, string> Body; public DirectorRead Result; public double Polled = double.NegativeInfinity; public JsonElement Data; public int Records; }
        sealed class StreamRow { public double Arrives; public DirectorMeasurement Row; public string ExternalId; }

        public readonly HarborLayout Layout = Kit.Layout();
        public readonly Source Feed = new Source();
        readonly List<AlarmSpec> alarms = new List<AlarmSpec>();
        readonly List<Job> jobs = new List<Job>();
        readonly List<StreamRow> stream = new List<StreamRow>();
        readonly DirectorMeasurementTap tap = new DirectorMeasurementTap();
        readonly int backfillRows;
        int streamIndex, statusStage = -1;
        /// <summary>Every poll due inside one of these spans of wall seconds fails, so each read turns unhealthy in its turn and comes back.</summary>
        public double[][] Stalls = new double[0][];
        /// <summary>
        /// How many seconds the platform's clock runs ahead of the player's: every read answers as of that much later, so its
        /// newest records are stamped after the frame's "now" and are held back until their instant has come.
        /// </summary>
        public double Lead;
        /// <summary>
        /// Late in the night the alarm read lists an alarm on a device outside the scene: a record the observer cannot place, on
        /// every frame from then on.
        /// </summary>
        public bool Stranger;
        const string Visitor = "hp-visitor-berth-9";
        IEnumerable<AlarmSpec> Listed(double t) => alarms.Where(a => a.Listed <= t && (Stranger || a.Device != Visitor));
        /// <summary>How many rows the measurement history reads hold as last published.</summary>
        public int ReadRows { get; private set; }

        public DirectorFixtureDay(int backfillRows = 360)
        {
            this.backfillRows = backfillRows;
            foreach (var d in Layout.Devices) Feed.Tokens[d.ExternalId] = d.Token;
            var devices = Layout.Devices.ToArray();
            var keys = new[] { "attribute-well-high", "flood-level", "connector-hot", "battery-low" };
            for (var i = 0; i < 30; i++)
                alarms.Add(new AlarmSpec { Token = "gen-" + i, Device = devices[i * 7 % devices.Length].Token, Key = keys[i % keys.Length], Raised = 10 + i * 29.5, Cleared = i % 3 == 0 ? (double?)null : 10 + i * 29.5 + 45 });
            // one token raised twice: the read shows its current cycle only, the observer keeps both
            alarms.Add(new AlarmSpec { Token = "gen-4", Device = devices[4 * 7 % devices.Length].Token, Key = keys[0], Raised = 500, Cleared = 560 });
            alarms.Add(new AlarmSpec { Token = "flicker", Device = "hp-light-east-3", Key = "lamp-flicker", Raised = 300, Cleared = 336, Stamp = "service" });
            foreach (var pump in devices.Where(d => d.Kind == DeviceKind.Pump))
                alarms.Add(new AlarmSpec { Token = "high-" + pump.Token, Device = pump.Token, Key = "wet-well-high", Raised = 664, Cleared = 720, Severity = "MAJOR" });
            foreach (var d in devices.Where(d => d.Quay == "hp-east-quay" && (d.Kind == DeviceKind.Light || d.Kind == DeviceKind.Charger)))
                alarms.Add(new AlarmSpec { Token = "silent-" + d.Token, Device = d.Token, Key = d.Kind == DeviceKind.Light ? "light-silent" : "charger-offline", Raised = OutageFrom + 20, Cleared = OutageTo + 6 });

            // an alarm the read lists late: by then the history rows it covers have been joined once already
            alarms.Add(new AlarmSpec { Token = "late-charger", Device = "hp-charger-east-1", Key = "connector-hot", Raised = OutageFrom + 25, Late = OutageTo + 11, Cleared = OutageTo + 60 });
            alarms.Add(new AlarmSpec { Token = "stranger", Device = Visitor, Key = "flood-level", Raised = 1100 });

            double phase = 0;
            void Add(string name, string scope, double from, Func<double, string> body)
            { jobs.Add(new Job { Name = name, Scope = scope, From = from, Body = body, Phase = (phase += 0.137) % PollS }); }
            string Page(IEnumerable<string> records) { var r = records.ToArray(); return "{\"results\":[" + string.Join(",", r) + "],\"pagination\":{\"totalRecords\":" + r.Length + "}}"; }
            string List(IEnumerable<string> records) => "[" + string.Join(",", records) + "]";

            Add("alarms", "run", 0, t => Page(Listed(t).GroupBy(a => a.Token).Select(g => g.OrderBy(a => a.Raised).Last()).Select(a => AlarmRow(a, t))));
            Add("suppressionStatus", "fleet", 0, t => List(Listed(t).GroupBy(a => a.Token).Select(g => "{\"alarmToken\":" + Q(g.Key) + ",\"state\":\"ACTIVE\",\"severity\":\"MINOR\"}")));
            Add("notifications", "known-alarms-empty", 0, t => Page(Array.Empty<string>()));
            var attempt = 0;
            foreach (var a in alarms) {
                var first = ++attempt; var second = alarms.IndexOf(a) % 5 == 0 ? ++attempt : 0; var spec = a;
                if (jobs.Any(j => j.Name == "notifications" && j.Scope == a.Token)) continue; // a second cycle shares its token's read
                Add("notifications", a.Token, a.Raised + 3, t => Page(alarms.Where(x => x.Token == spec.Token).SelectMany(x => new[] {
                    x.Raised + 2 <= t ? Attempt(x == spec ? first : first + 1000, x, x.Raised + 2, "ALARM") : null,
                    x == spec && second > 0 && x.Raised + 30 <= t ? Attempt(second, x, x.Raised + 30, "ESCALATION") : null }).Where(r => r != null)));
            }
            Add("windows", "jobs", 0, t => List(new[] { "{\"token\":\"service\",\"kind\":\"ONE_SHOT\",\"status\":" + Q(t >= 330 ? "ENDED" : "ACTIVE") + ",\"startTime\":" + Q(Ts(285)) + ",\"endTime\":" + Q(Ts(330)) + ",\"endedAt\":" + (t >= 330 ? Q(Ts(330)) : "null") + ",\"targets\":[{\"type\":\"DEVICE\",\"token\":\"hp-light-east-3\"}],\"alarmKeys\":[\"lamp-flicker\"]}" }));
            Add("replacements", "hp-gauge-north-3", 0, t => Page(new[] { 390.0, 700.0 }.Where(at => at + 1 <= t).Select(at => Journal(at))));
            Add("credentials", "retired", 0, t => List(new[] { 390.0, 700.0 }.Where(at => at + 2 <= t).Select(at => "{\"token\":" + Q(at < 700 ? "hp-gauge-north-3-cred" : "new-cred") + ",\"enabled\":false,\"device\":{\"token\":\"hp-gauge-north-3\"}}")));
            // the replaced gauge's history around the swap: twenty rows, complete a few seconds after the window closes
            Add("measurements", "hp-gauge-north-3", 0, t => Page(Enumerable.Range(0, 10).Select(i => 384 + i * 2.4).Where(at => at + 1 <= t).SelectMany(at => new[] {
                Measure("hp-gauge-north-3", Metrics.WaterLevel, at, 0.2 + at / 4000), Measure("hp-gauge-north-3", Metrics.Battery, at, 3.6) })));
            // the two chargers that buffer through the outage: nothing until power is back, then the drain fills the window
            var chargers = new[] { "hp-charger-east-1", "hp-charger-east-2" };
            for (var c = 0; c < chargers.Length; c++) {
                var token = chargers[c]; var starts = OutageTo + c * 15;
                Add("measurements", token, 0, t => Page(BackfillTimes().Take((int)Math.Max(0, Math.Min(BackfillTimes().Length, (t - starts) / 10 * BackfillTimes().Length)))
                    .Concat(Enumerable.Range(0, 8).Select(i => OutageTo + 2.5 + i * 5).Where(at => at <= t)) // and its first readings on mains again
                    .SelectMany(at => ChargerMetrics.Select((m, i) => Measure(token, m, at, 5 + i + at / 1000)))));
            }
            foreach (var d in devices.Where(d => d.Kind == DeviceKind.Pump || d.Kind == DeviceKind.Charger)) {
                var token = d.Token; var pump = d.Kind == DeviceKind.Pump;
                Add("commands", token, 0, t => Page(pump
                    ? new[] { 666.0 }.Where(at => at <= t).Select(at => CommandRow("cmd-" + token, token, "start-pump", at, t, null, "{\"kind\":\"ALARM\",\"ruleToken\":\"hp-well-high\",\"detectedAt\":" + Q(Ts(664)) + ",\"alarmKey\":\"wet-well-high\",\"deviceId\":" + Q("row-" + token) + "}"))
                    : token == "hp-charger-north-1" ? new[] { 500.0 }.Where(at => at <= t).Select(at => CommandRow("cmd-" + token, token, "derate", at, t, "{\\\"max_kw\\\":11}", "{\"kind\":\"OPERATOR\"}"))
                    : Array.Empty<string>()));
            }
            foreach (var quay in devices.Select(d => d.Quay).Distinct()) {
                var q = quay;
                Add("membership", q, 0, t => Page(devices.Where(d => d.Quay == q).Select(d => "{\"token\":" + Q(d.Token) + "}")));
            }
            var targets = devices.Where(d => d.Kind == DeviceKind.Charger).Select(d => d.Token).ToArray();
            Add("artifact", "fw", RolloutAt, t => "{\"token\":\"fw\",\"state\":\"AVAILABLE\",\"trustStatus\":\"SIGNED\",\"signatures\":[{\"countsTowardTrust\":true,\"signer\":{\"token\":\"signer\",\"keyId\":\"key\",\"state\":\"ACTIVE\"}}]}");
            Add("signer", "signer", RolloutAt, t => "{\"token\":\"signer\",\"keyId\":\"key\",\"state\":\"ACTIVE\"}");
            Add("rollout", "rollout", RolloutAt, t => {
                var done = t >= RolloutDone;
                return "{\"token\":\"rollout\",\"state\":" + Q(done ? "COMPLETED" : "RUNNING") + ",\"startedAt\":" + Q(Ts(RolloutAt)) + ",\"finishedAt\":" + (done ? Q(Ts(RolloutDone)) : "null") + ",\"stateChangedAt\":" + Q(Ts(RolloutAt))
                    + ",\"maxInFlight\":2,\"artifact\":{\"token\":\"fw\"},\"progress\":{\"total\":" + targets.Length + ",\"pending\":0,\"inFlight\":0,\"succeeded\":" + (targets.Length - 1) + ",\"failed\":1,\"unknown\":0,\"cancelled\":0,\"skipped\":0}}";
            });
            // The last target's row lags the rollout's own state by eight seconds: a finished rollout whose counts cannot be stated yet.
            double Ends(int i) => i == targets.Length - 1 ? RolloutDone + 8 : RolloutAt + 6 + i * 6;
            Add("targets", "rollout", RolloutAt, t => Page(targets.Select((token, i) => "{\"deviceToken\":" + Q(token) + ",\"outcome\":" + Q(t < Ends(i) - 5 ? "PENDING" : t < Ends(i) ? "IN_FLIGHT" : i == 0 ? "FAILED" : "SUCCEEDED")
                + ",\"assignmentToken\":" + (t < Ends(i) - 5 ? "null" : Q("assign-" + token)) + "}")));
            for (var i = 0; i < targets.Length; i++) {
                var token = targets[i]; var n = i;
                Add("assignments", "assign-" + token, Ends(n) - 4, t => "{\"token\":" + Q("assign-" + token) + ",\"deviceToken\":" + Q(token) + ",\"artifact\":{\"token\":\"fw\"},\"attempt\":{\"token\":" + Q("try-" + token)
                    + ",\"state\":" + Q(t < Ends(n) ? "DOWNLOADING" : n == 0 ? "FAILED" : "SUCCEEDED") + ",\"lastReportAt\":" + Q(Ts(Math.Min(t, Ends(n))))
                    + ",\"failure\":" + (n == 0 && t >= Ends(n) ? "{\"stage\":\"VERIFY\",\"code\":\"VERIFICATION_FAILED\",\"platform\":false}" : "null") + "}}");
            }
            BuildStream(devices);
        }

        static readonly string[] ChargerMetrics = { Metrics.ChargerPower, Metrics.MaxKw, Metrics.SessionActive, Metrics.ConnectorTemp };
        double[] backfillTimes;
        double[] BackfillTimes() => backfillTimes ?? (backfillTimes = Enumerable.Range(0, backfillRows / ChargerMetrics.Length).Select(i => OutageFrom + (i + .5) * (OutageTo - OutageFrom) / (backfillRows / ChargerMetrics.Length)).ToArray());
        static string Id(string device, string metric, double at) => "ev-" + device + "-" + metric + "-" + At(at).UtcTicks.ToString(CultureInfo.InvariantCulture);
        static string Measure(string device, string metric, double at, double value)
            => "{\"id\":" + Q(Id(device, metric, at)) + ",\"deviceToken\":" + Q(device) + ",\"occurredTime\":" + Q(Ts(at)) + ",\"name\":" + Q(metric) + ",\"value\":" + F(Math.Round(value, 3)) + "}";
        // The gauge is swapped at 12:30 and again in the afternoon; the second unit's first reading is never in the history read,
        // so its journal entry waits out its minute and is emitted without one.
        static string Journal(double at) => "{\"id\":" + Q(at < 700 ? "1" : "2") + ",\"occurredTime\":" + Q(Ts(at)) + ",\"retiredCredentialTokens\":[" + Q(at < 700 ? "hp-gauge-north-3-cred" : "new-cred")
            + "],\"newCredentialToken\":" + Q(at < 700 ? "new-cred" : "newer-cred") + ",\"device\":{\"id\":\"row-hp-gauge-north-3\",\"token\":\"hp-gauge-north-3\",\"externalId\":\"hp-gauge-north-3\"}}";
        static string AlarmRow(AlarmSpec a, double t)
        {
            var cleared = a.Cleared.HasValue && a.Cleared.Value <= t;
            return "{\"token\":" + Q(a.Token) + ",\"originatorToken\":" + Q(a.Device) + ",\"originatorId\":" + Q("row-" + a.Device) + ",\"alarmKey\":" + Q(a.Key) + ",\"state\":" + Q(cleared ? "CLEARED" : "ACTIVE")
                + ",\"severity\":" + Q(a.Severity) + ",\"raisedTime\":" + Q(Ts(a.Raised)) + ",\"clearedTime\":" + (cleared ? Q(Ts(a.Cleared.Value)) : "null") + ",\"raisedDuringSuppression\":" + Q(a.Stamp) + "}";
        }
        static string Attempt(int id, AlarmSpec a, double at, string trigger)
            => "{\"id\":" + Q(id.ToString(CultureInfo.InvariantCulture)) + ",\"alarmToken\":" + Q(a.Token) + ",\"attemptedAt\":" + Q(Ts(at)) + ",\"trigger\":" + Q(trigger) + ",\"status\":\"SENT\",\"policyToken\":\"hp-duty-escalation\"}";
        static string CommandRow(string id, string device, string name, double at, double t, string payload, string cause)
        {
            var responded = at + 2 <= t;
            return "{\"token\":" + Q(id) + ",\"deviceToken\":" + Q(device) + ",\"name\":" + Q(name) + ",\"status\":" + Q(responded ? "SUCCESSFUL" : at + 1 <= t ? "SENT" : "QUEUED") + ",\"queuedTime\":" + Q(Ts(at))
                + ",\"sentTime\":" + (at + 1 <= t ? Q(Ts(at + 1)) : "null") + ",\"respondedTime\":" + (responded ? Q(Ts(at + 2)) : "null") + ",\"payload\":" + Q(payload) + ",\"cause\":" + cause + "}";
        }

        void BuildStream(LayoutDevice[] devices)
        {
            for (var n = 0; n < devices.Length; n++) {
                var d = devices[n]; var metrics = HarborObservedModel.MetricsFor(d.Kind);
                var buffers = d.Token == "hp-charger-east-1" || d.Token == "hp-charger-east-2";
                var dark = d.Quay == "hp-east-quay" && (d.Kind == DeviceKind.Light || d.Kind == DeviceKind.Charger);
                for (var at = n * 0.11; at < 1290; at += 5) {
                    if (dark && at >= OutageFrom && at < OutageTo) continue;
                    foreach (var m in metrics) stream.Add(new StreamRow { Arrives = at + 0.4, ExternalId = d.ExternalId, Row = new DirectorMeasurement(d.Token, m, Value(d, m, at), At(at)) });
                }
                // the buffered readings come down the stream too, in the seconds after power is back
                if (buffers) {
                    var starts = OutageTo + (d.Token == "hp-charger-east-2" ? 15 : 0); var times = BackfillTimes();
                    for (var i = 0; i < times.Length; i++)
                        for (var m = 0; m < ChargerMetrics.Length; m++)
                            stream.Add(new StreamRow { Arrives = starts + 0.5 + 10.0 * i / times.Length, ExternalId = d.ExternalId, Row = new DirectorMeasurement(d.Token, ChargerMetrics[m], Math.Round(5 + m + times[i] / 1000, 3), At(times[i])) });
                }
            }
            var ordered = stream.Select((row, i) => new { row, i }).OrderBy(x => x.row.Arrives).ThenBy(x => x.i).Select(x => x.row).ToArray();
            stream.Clear(); stream.AddRange(ordered);
        }
        static double Value(LayoutDevice d, string metric, double at)
        {
            if (metric == Metrics.WetWell) return Math.Round(at < 600 ? 1.5 + at / 1500 : at < 668 ? 1.9 + (at - 600) / 45 : Math.Max(1.2, 3.4 - (at - 668) / 30), 3);
            if (metric == Metrics.PumpRunning) return at >= 668 && at < 740 ? 1 : 0;
            return Math.Round(1 + (at % 97) / 10 + metric.Length, 3);
        }

        string StatusJson(int stage)
        {
            var b = new StringBuilder("{\"state\":\"RUNNING\",\"tenant\":\"sim-hp\",\"instanceId\":\"dc\",\"deviceCount\":43,\"emitIntervalMs\":5000,\"scenario\":{\"name\":\"harborpulse\",\"started\":true,\"startedAt\":");
            b.Append(Q(Ts(0))).Append(",\"dayLengthSeconds\":1440,\"startHour\":6,\"day\":0,\"simHour\":6,\"simTime\":\"06:00\",\"beat\":\"quiet\",\"defects\":[],\"layoutDigest\":").Append(Q(new string('a', 64)));
            b.Append(",\"jobs\":[{\"day\":0,\"name\":\"lamp-service\",\"campaign\":\"lamp\",\"window\":\"service\",\"device\":\"hp-light-east-3\",\"group\":\"g\",\"fireAt\":").Append(Q(Ts(285)))
                .Append(",\"windowOpens\":").Append(Q(Ts(285))).Append(",\"windowCloses\":").Append(Q(Ts(330))).Append("}]");
            if (stage >= 2) {
                var times = "[" + string.Join(",", BackfillTimes().Select(at => Q(Ts(at)))) + "]";
                b.Append(",\"backfill\":[").Append(string.Join(",", new[] { "hp-charger-east-1", "hp-charger-east-2" }.Select(d => "{\"device\":" + Q(d) + ",\"day\":0,\"buffered\":" + BackfillTimes().Length
                    + ",\"first\":" + Q(Ts(BackfillTimes().First())) + ",\"last\":" + Q(Ts(OutageTo + 40)) + ",\"times\":" + times + "}"))).Append("]");
            }
            b.Append(",\"s5\":{\"ota\":{\"artifact\":\"fw\",\"version\":\"2.4.1\",\"rollout\":\"rollout\",\"group\":\"chargers\",\"maxInFlight\":2,\"failureThreshold\":2,\"scheduleHour\":26}");
            if (stage >= 1) b.Append(",\"replacement\":{\"device\":\"hp-gauge-north-3\",\"at\":").Append(Q(Ts(390))).Append(",\"unitIdentifier\":\"unit-2\"}");
            if (stage >= 2) b.Append(",\"restoreDrains\":[{\"day\":0,\"offered\":").Append(2 * BackfillTimes().Length).Append(",\"accepted\":").Append(2 * BackfillTimes().Length)
                .Append(",\"refused429\":7,\"firstRefusalAt\":").Append(Q(Ts(OutageTo + 1))).Append(",\"caughtUpAt\":").Append(Q(Ts(OutageTo + 27))).Append("}]");
            return b.Append("}}}").ToString();
        }

        /// <summary>The reads as published at this wall second. Asked for in ascending order, as frames are.</summary>
        public DirectorReadSnapshot Snapshot(double wall)
        {
            var stage = wall >= OutageTo + 1 ? 2 : wall >= 390.2 ? 1 : 0;
            if (stage != statusStage) { statusStage = stage; Feed.Status = ScenarioStatus.Parse(StatusJson(stage)); }
            for (; streamIndex < stream.Count && stream[streamIndex].Arrives <= wall; streamIndex++) {
                var row = stream[streamIndex]; tap.Watch(row.Row);
                var key = row.ExternalId + "/" + row.Row.Name;
                if (!Feed.Values.TryGetValue(key, out var old) || row.Row.OccurredAt >= old.OccurredAt) Feed.Values[key] = new Reading(row.Row.Value, row.Row.OccurredAt);
            }
            ReadRows = 0;
            foreach (var job in jobs) {
                if (wall < job.From) continue;
                var due = Math.Floor((wall - job.Phase) / PollS) * PollS + job.Phase;
                if (due > job.Polled && due >= job.From) {
                    job.Polled = due;
                    var asked = due - 0.08; // the request left before the answer came back
                    if (Stalls.Any(stall => due >= stall[0] && due < stall[1])) job.Result = new DirectorRead(job.Name, job.Scope, DirectorProbe.PROBE_OK, job.Result?.LastSuccess, "timeout", job.Result?.Data, null, job.Result?.AsOf);
                    else {
                        var body = "{\"" + DirectorProbeEvaluation.Roots[job.Name] + "\":" + job.Body(asked + Lead) + "}";
                        // An unchanged answer is still a new document, as it is off the wire.
                        using (var doc = JsonDocument.Parse(body)) job.Data = doc.RootElement.Clone();
                        job.Records = body.Split(new[] { "\"id\":" }, StringSplitOptions.None).Length - 1;
                        job.Result = new DirectorRead(job.Name, job.Scope, DirectorProbe.PROBE_OK, At(due), null, job.Data, new[] { new ReadSpan(Epoch, At(due), false) }, At(asked));
                    }
                }
                if (job.Name == "measurements") ReadRows += job.Records;
            }
            return new DirectorReadSnapshot(jobs.Where(j => j.Result != null).Select(j => j.Result), true, 0, 0, null, tap.Records);
        }
        public DirectorInputs Inputs(double wall, HarborFrame frame = null)
            => new DirectorInputs { Run = "run", RecordingEpoch = Epoch, Now = At(wall), Sim = 6 + wall / 60, Source = Feed, Frame = frame };
    }
}
