// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.Text.Json;
using DeviceChain.Demos.Platform;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// What the runner's <c>GET /status</c> says about the compressed day: the simulator's ScenarioClock, whose JSON names are its contract
    /// with the scenario verifier and with this scene. Only the fields the scene uses are read.
    /// </summary>
    public sealed class ScenarioStatus
    {
        /// <summary>The runner's lifecycle state: CREATED, BOOTSTRAPPED, RUNNING, STOPPED.</summary>
        public string RunnerState { get; internal set; }

        public string Tenant { get; internal set; }
        public string InstanceId { get; internal set; }
        public long EmitIntervalMs { get; internal set; }
        public bool Started { get; internal set; }

        /// <summary>The run's first tick; null until <see cref="Started"/>. A different value is a different run.</summary>
        public DateTimeOffset? StartedAt { get; internal set; }

        public double DayLengthSeconds { get; internal set; }
        public double StartHour { get; internal set; }
        public int Day { get; internal set; }
        public double SimHour { get; internal set; }
        public string Beat { get; internal set; }

        public bool Running => RunnerState == "RUNNING";

        /// <summary>The sim time the runner reported, <see cref="DayClock"/> seconds.</summary>
        public double SimSeconds => CompressedDay.SimSecondsOf(Day, SimHour);

        /// <summary>Whether the comma-separated beat list names <paramref name="beat"/>.</summary>
        public bool InBeat(string beat) => Beat != null && ("," + Beat + ",").Contains("," + beat + ",");

        /// <summary>Parses a <c>/status</c> body. A runner that is not running harborpulse, or a body missing what the clock needs, is refused.</summary>
        public static ScenarioStatus Parse(string json)
        {
            JsonDocument doc;
            try { doc = JsonDocument.Parse(json ?? ""); }
            catch (JsonException e) { throw new FormatException("/status: not valid JSON: " + e.Message, e); }

            using (doc)
            {
                var root = doc.RootElement;
                if (root.ValueKind != JsonValueKind.Object) throw new FormatException("/status: must be a JSON object");
                var s = new ScenarioStatus
                {
                    RunnerState = Str(root, "state", true),
                    Tenant = Str(root, "tenant", false),
                    InstanceId = Str(root, "instanceId", false),
                    EmitIntervalMs = root.TryGetProperty("emitIntervalMs", out var ei) && ei.ValueKind == JsonValueKind.Number && ei.TryGetInt64(out var ms) ? ms : 0,
                };
                if (!root.TryGetProperty("scenario", out var c) || c.ValueKind != JsonValueKind.Object)
                    throw new FormatException("/status: the runner reports no scenario day; is it running the harborpulse manifest?");
                var name = Str(c, "name", true);
                if (name != "harborpulse") throw new FormatException("/status: the runner is running scenario \"" + name + "\", not harborpulse");
                // started and day are always on the wire (no omitempty): one missing is a body this scene does not understand, not a
                // run at day 0 that has not begun
                if (!c.TryGetProperty("started", out var st) || (st.ValueKind != JsonValueKind.True && st.ValueKind != JsonValueKind.False))
                    throw new FormatException("/status: scenario.\"started\" must be a boolean");
                s.Started = st.ValueKind == JsonValueKind.True;
                s.DayLengthSeconds = Num(c, "dayLengthSeconds");
                s.StartHour = Num(c, "startHour");
                s.SimHour = Num(c, "simHour");
                if (!c.TryGetProperty("day", out var d) || d.ValueKind != JsonValueKind.Number || !d.TryGetInt32(out var di) || di < 0)
                    throw new FormatException("/status: scenario.\"day\" must be a non-negative integer");
                s.Day = di;
                s.Beat = Str(c, "beat", false) ?? "";
                if (s.Started)
                {
                    var at = Str(c, "startedAt", true);
                    if (!DateTimeOffset.TryParse(at, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var t))
                        throw new FormatException("/status: scenario.startedAt \"" + at + "\" is not an RFC 3339 time");
                    s.StartedAt = t;
                }

                if (!(s.DayLengthSeconds > 0)) throw new FormatException("/status: scenario.dayLengthSeconds must be positive");
                if (s.StartHour < 0 || s.StartHour >= 24 || s.SimHour < 0 || s.SimHour >= 24) throw new FormatException("/status: scenario hours must be in [0, 24)");
                return s;
            }
        }

        static string Str(JsonElement o, string name, bool required)
        {
            if (o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String) return v.GetString();
            if (required) throw new FormatException("/status: missing \"" + name + "\"");
            return null;
        }

        static double Num(JsonElement o, string name)
        {
            if (o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetDouble(out var d) && !double.IsNaN(d)) return d;
            throw new FormatException("/status: scenario.\"" + name + "\" must be a number");
        }
    }

    public enum ClockMode
    {
        /// <summary>No status yet, or the run has not ticked: the scene shows the opening hour and says so.</summary>
        Waiting,

        /// <summary>Following a running runner whose status is fresh.</summary>
        Live,

        /// <summary>The runner stopped, or its status stopped arriving: the clock holds at the last time it was known, visibly.</summary>
        Stale,

        /// <summary>No runner at all: a labelled rehearsal clock.</summary>
        Preview,
    }

    /// <summary>A moment of the scene's day.</summary>
    public readonly struct ClockReading
    {
        public ClockReading(double simSeconds, ClockMode mode, string note)
        {
            SimSeconds = simSeconds;
            Mode = mode;
            Note = note ?? "";
        }

        /// <summary><see cref="DayClock"/> seconds: day index and hour of day both derive from it, so midnight is continuous.</summary>
        public double SimSeconds { get; }

        public ClockMode Mode { get; }
        public string Note { get; }
        public double HourOfDay => DayClock.Hours(SimSeconds);
        public int Day => DayClock.DayIndex(SimSeconds);
        public string Label => DayClock.Format(SimSeconds);
    }

    /// <summary>
    /// Follows the runner's compressed day between polls of <c>/status</c>. Each status is the runner's own answer to "what sim time is it",
    /// computed on its clock; between polls the scene advances that answer by the local wall time since it arrived, at the day's rate, so no
    /// clock skew between the two hosts enters. The time never runs backwards within one run (a later poll that lands a few milliseconds
    /// earlier is held, not rewound), and a changed <c>startedAt</c> is a new run, which starts over.
    ///
    /// When the runner stops, or no status has arrived for <see cref="StaleAfter"/>, the clock holds where it was last known and says it is
    /// stale; it never keeps a "live" world turning on its own.
    /// </summary>
    public sealed class ScenarioClockFollower
    {
        public static readonly TimeSpan StaleAfter = TimeSpan.FromSeconds(5);

        ScenarioStatus last;
        DateTimeOffset lastAt;
        DateTimeOffset? lastOkAt;
        double floor = double.MinValue;
        string failure;

        // the time the scene froze at when the runner stopped. The runner's own clock keeps counting from its first tick while it is
        // stopped, so each STOPPED status reports a later time; the scene holds this one until the runner runs again.
        double? frozen;

        /// <summary>The newest status, or null before the first.</summary>
        public ScenarioStatus Last => last;

        /// <summary>How many runs this follower has seen begin (a changed startedAt).</summary>
        public int Runs { get; private set; }

        public void Observe(ScenarioStatus s, DateTimeOffset receivedAt)
        {
            if (s == null) throw new ArgumentNullException(nameof(s));
            if (last == null || last.StartedAt != s.StartedAt)
            {
                if (s.StartedAt.HasValue) Runs++;
                floor = double.MinValue;
                frozen = null;
            }

            if (s.Running) frozen = null;
            else if (!frozen.HasValue) frozen = Math.Max(floor, last != null && last.StartedAt == s.StartedAt && last.Running ? Estimate(receivedAt) : s.SimSeconds);

            last = s;
            lastAt = receivedAt;
            lastOkAt = receivedAt;
            failure = null;
        }

        /// <summary>A poll failed; the clock goes stale once the last good status is older than <see cref="StaleAfter"/>.</summary>
        public void ObserveFailure(string why) => failure = why;

        public ClockReading Read(DateTimeOffset now)
        {
            if (last == null) return new ClockReading(0, ClockMode.Waiting, failure ?? "waiting for the runner's /status");
            var day = new CompressedDay(last.StartHour, last.DayLengthSeconds);
            if (!last.Started) return new ClockReading(day.SimSecondsAt(0), ClockMode.Waiting, "the run has not ticked yet");

            if (!last.Running) return new ClockReading(frozen ?? last.SimSeconds, ClockMode.Stale, "the runner is " + last.RunnerState);

            var fresh = lastOkAt.HasValue && now - lastOkAt.Value <= StaleAfter;
            if (!fresh)
                return new ClockReading(Math.Max(last.SimSeconds, floor), ClockMode.Stale,
                    "no /status for " + (int)(now - lastOkAt.Value).TotalSeconds + " s" + (failure != null ? ": " + failure : ""));

            var sim = Math.Max(Estimate(now), floor);
            floor = sim;
            return new ClockReading(sim, ClockMode.Live, "");
        }

        // the running day at a wall time: the last status's sim time advanced at the day's rate
        double Estimate(DateTimeOffset now) => last.SimSeconds + DayClock.Day * Math.Max(0, (now - lastAt).TotalSeconds) / last.DayLengthSeconds;
    }
}
