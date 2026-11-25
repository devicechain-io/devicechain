// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;

namespace DeviceChain.Harborpulse.Model
{
    public sealed class StoryBeat
    {
        public StoryBeat(string name, double from, double to) { Name = name; From = from; To = to; }
        public string Name { get; }
        public double From { get; }
        public double To { get; }
    }

    /// <summary>Runner declarations only. None of these fields establishes platform execution.</summary>
    public sealed class StoryJob
    {
        public int Day { get; internal set; }
        public string Name { get; internal set; }
        public string Campaign { get; internal set; }
        public string Window { get; internal set; }
        public string Device { get; internal set; }
        public string Group { get; internal set; }
        public DateTimeOffset? FireAt { get; internal set; }
        public DateTimeOffset? WindowOpens { get; internal set; }
        public DateTimeOffset? WindowCloses { get; internal set; }
    }

    public sealed class StoryOta
    {
        public string Artifact { get; internal set; }
        public string Version { get; internal set; }
        public string Rollout { get; internal set; }
        public string Group { get; internal set; }
        public int MaxInFlight { get; internal set; }
        public int FailureThreshold { get; internal set; }
        public double ScheduleHour { get; internal set; }
        public DateTimeOffset? CreatedAt { get; internal set; }
        public DateTimeOffset? StartedAt { get; internal set; }
        public string RefusalCode { get; internal set; }
    }

    public sealed class StoryReplacement
    {
        public string Device { get; internal set; }
        public DateTimeOffset At { get; internal set; }
        public string UnitIdentifier { get; internal set; }
    }

    /// <summary>What one store-and-forward device buffered in one outage, as the runner declares it (SCENARIO, never platform storage).</summary>
    public sealed class StoryBackfill
    {
        public string Device { get; internal set; }
        public int Day { get; internal set; }
        public int Buffered { get; internal set; }
        public DateTimeOffset First { get; internal set; }
        public DateTimeOffset Last { get; internal set; }
        /// <summary>When each buffered reading was taken, oldest first, exactly as the runner reports them (RFC 3339, nanoseconds).</summary>
        public IReadOnlyList<string> Times { get; internal set; }
    }

    /// <summary>The sender's own count of one day's restore drain (DEVICE-SIDE): never a platform success numerator.</summary>
    public sealed class StoryRestoreDrain
    {
        public int Day { get; internal set; }
        public int Offered { get; internal set; }
        public int Accepted { get; internal set; }
        public int Refused429 { get; internal set; }
        public DateTimeOffset? FirstRefusalAt { get; internal set; }
        public DateTimeOffset? CaughtUpAt { get; internal set; }
    }

    public sealed partial class ScenarioStatus
    {
        /// <summary>The planted defects this run carries (a TEST CONTROL run when non-empty).</summary>
        public IReadOnlyList<string> Defects { get; private set; } = Array.Empty<string>();
        /// <summary>The lowercase hex SHA-256 of the layout the runner generates, or null when the runner reports none.</summary>
        public string LayoutDigest { get; private set; }
        public IReadOnlyList<StoryBackfill> Backfill { get; private set; } = Array.Empty<StoryBackfill>();
        public IReadOnlyList<StoryRestoreDrain> RestoreDrains { get; private set; } = Array.Empty<StoryRestoreDrain>();
        public IReadOnlyList<StoryBeat> Beats { get; private set; } = Array.Empty<StoryBeat>();
        public bool BeatsFromRunner { get; private set; }
        public IReadOnlyList<StoryJob> Jobs { get; private set; } = Array.Empty<StoryJob>();
        public StoryOta Ota { get; private set; }
        public StoryReplacement Replacement { get; private set; }
        public bool HasS5 { get; private set; }

        /// <summary>The inverse of scenario-verify's wall-time mapping; absolute sim seconds, continuous across midnight.</summary>
        public double ToSimSeconds(DateTimeOffset instant)
        {
            if (!StartedAt.HasValue) throw new InvalidOperationException("runner start instant not reported");
            return StartHour * 3600 + (instant - StartedAt.Value).TotalSeconds * 86400 / DayLengthSeconds;
        }

        public DateTimeOffset ToWallTime(double simSeconds)
        {
            if (!StartedAt.HasValue) throw new InvalidOperationException("runner start instant not reported");
            return StartedAt.Value.AddSeconds((simSeconds / 3600 - StartHour) / 24 * DayLengthSeconds);
        }

        /// <summary>
        /// A recorded run's status at one recorded instant, for a replay: the clock, the beat and the runner's plans as a recording
        /// holds them. The beats are the committed layout's (a recording does not carry the runner's own list).
        /// </summary>
        public static ScenarioStatus Recorded(DateTimeOffset startedAt, double dayLengthSeconds, double startHour, double simHours, string beat, string layoutDigest,
            IEnumerable<string> defects, IEnumerable<StoryJob> jobs, StoryReplacement replacement, StoryOta ota)
        {
            var day = (int)Math.Floor(simHours / 24);
            return new ScenarioStatus
            {
                RunnerState = "RUNNING", Started = true, StartedAt = startedAt, DayLengthSeconds = dayLengthSeconds, StartHour = startHour, Day = day, SimHour = simHours - day * 24, Beat = beat,
                LayoutDigest = layoutDigest, Defects = new List<string>(defects ?? Array.Empty<string>()).AsReadOnly(), Jobs = new List<StoryJob>(jobs ?? Array.Empty<StoryJob>()).AsReadOnly(),
                Replacement = replacement, Ota = ota, HasS5 = replacement != null || ota != null
            };
        }

        void ParseMetadata(JsonElement c)
        {
            if (c.TryGetProperty("defects", out var defects))
            {
                var list = new List<string>();
                foreach (var d in Wire.Array(defects, "defects"))
                {
                    if (d.ValueKind != JsonValueKind.String || string.IsNullOrEmpty(d.GetString())) throw new FormatException("defects: expected non-empty strings");
                    list.Add(d.GetString());
                }
                Defects = list.AsReadOnly();
            }
            if (c.TryGetProperty("layoutDigest", out _))
            {
                var digest = Wire.String(c, "layoutDigest");
                if (digest.Length != 64 || !System.Text.RegularExpressions.Regex.IsMatch(digest, "^[0-9a-f]{64}$")) throw new FormatException("layoutDigest: expected lowercase hex SHA-256");
                LayoutDigest = digest;
            }
            if (c.TryGetProperty("backfill", out var backfill))
            {
                var list = new List<StoryBackfill>();
                foreach (var b in Wire.Array(backfill, "backfill"))
                {
                    var times = new List<string>();
                    foreach (var t in Wire.Array(Wire.Field(b, "times"), "times"))
                    {
                        if (t.ValueKind != JsonValueKind.String) throw new FormatException("backfill.times: expected strings");
                        times.Add(t.GetString());
                    }
                    list.Add(new StoryBackfill { Device = Wire.String(b, "device"), Day = Wire.Int(b, "day"), Buffered = Wire.Int(b, "buffered"),
                        First = Wire.Time(b, "first").Value, Last = Wire.Time(b, "last").Value, Times = times.AsReadOnly() });
                }
                Backfill = list.AsReadOnly();
            }
            if (c.TryGetProperty("beats", out var beats))
            {
                var list = new List<StoryBeat>();
                foreach (var b in Wire.Array(beats, "beats"))
                {
                    var from = Wire.Number(b, "from");
                    var to = Wire.Number(b, "to");
                    if (from < 0 || from >= 24 || to < 0 || to > 24 || from == to) throw new FormatException("beats: invalid hours");
                    list.Add(new StoryBeat(Wire.String(b, "name"), from, to));
                }
                Beats = list.AsReadOnly();
                BeatsFromRunner = true;
            }
            if (c.TryGetProperty("jobs", out var jobs))
            {
                var list = new List<StoryJob>();
                foreach (var j in Wire.Array(jobs, "jobs"))
                    list.Add(new StoryJob { Day = Wire.Int(j, "day"), Name = Wire.String(j, "name"),
                        Campaign = Wire.String(j, "campaign"), Window = Wire.String(j, "window", true),
                        Device = Wire.String(j, "device", true), Group = Wire.String(j, "group"),
                        FireAt = Wire.Time(j, "fireAt", true), WindowOpens = Wire.Time(j, "windowOpens", true), WindowCloses = Wire.Time(j, "windowCloses", true) });
                Jobs = list.AsReadOnly();
            }
            if (!c.TryGetProperty("s5", out var s5)) return;
            Wire.Object(s5, "s5");
            HasS5 = true;
            if (s5.TryGetProperty("ota", out var o))
            {
                Wire.Object(o, "ota");
                Ota = new StoryOta { Artifact = Wire.String(o, "artifact"), Version = Wire.String(o, "version"),
                    Rollout = Wire.String(o, "rollout", true), Group = Wire.String(o, "group"), MaxInFlight = Wire.Int(o, "maxInFlight"),
                    FailureThreshold = Wire.Int(o, "failureThreshold"), ScheduleHour = Wire.Number(o, "scheduleHour"),
                    CreatedAt = Wire.Time(o, "createdAt", true), StartedAt = Wire.Time(o, "startedAt", true), RefusalCode = Wire.String(o, "refusalCode", true) };
                // the runner states the rollout hour from day 0's midnight: 26 is 02:00 on the night after the run starts
                if (Ota.ScheduleHour < 0 || Ota.ScheduleHour >= 48) throw new FormatException("ota.scheduleHour outside [0,48)");
            }
            if (s5.TryGetProperty("restoreDrains", out var drains))
            {
                var list = new List<StoryRestoreDrain>();
                foreach (var d in Wire.Array(drains, "restoreDrains"))
                    list.Add(new StoryRestoreDrain { Day = Wire.Int(d, "day"), Offered = Wire.Int(d, "offered"), Accepted = Wire.Int(d, "accepted"),
                        Refused429 = Wire.Int(d, "refused429"), FirstRefusalAt = Wire.Time(d, "firstRefusalAt", true), CaughtUpAt = Wire.Time(d, "caughtUpAt", true) });
                RestoreDrains = list.AsReadOnly();
            }
            if (s5.TryGetProperty("replacement", out var r))
            {
                Wire.Object(r, "replacement");
                Replacement = new StoryReplacement { Device = Wire.String(r, "device"), At = Wire.Time(r, "at").Value, UnitIdentifier = Wire.String(r, "unitIdentifier") };
            }
            // Unknown members are deliberately not retained. In particular, no credential fields or raw JSON survive parsing.
        }
    }

    internal static class Wire
    {
        public static void Object(JsonElement e, string name) { if (e.ValueKind != JsonValueKind.Object) throw new FormatException(name + ": expected object"); }
        public static JsonElement Field(JsonElement e, string name) { Object(e, name); if (!e.TryGetProperty(name, out var v)) throw new FormatException(name + ": missing"); return v; }
        public static string String(JsonElement e, string name, bool optional = false)
        {
            Object(e, name);
            if (!e.TryGetProperty(name, out var v) && optional) return null;
            if (v.ValueKind != JsonValueKind.String) throw new FormatException(name + ": expected string");
            return v.GetString();
        }
        public static string NullableString(JsonElement e, string name) => Field(e, name).ValueKind == JsonValueKind.Null ? null : String(e, name);
        public static double Number(JsonElement e, string name)
        {
            var v = Field(e, name);
            if (v.ValueKind != JsonValueKind.Number || !v.TryGetDouble(out var n) || double.IsNaN(n) || double.IsInfinity(n)) throw new FormatException(name + ": expected finite number");
            return n;
        }
        public static int Int(JsonElement e, string name)
        {
            var v = Field(e, name);
            if (v.ValueKind != JsonValueKind.Number || !v.TryGetInt32(out var n) || n < 0) throw new FormatException(name + ": expected nonnegative integer");
            return n;
        }
        public static DateTimeOffset? Time(JsonElement e, string name, bool optional = false)
        {
            var s = String(e, name, optional);
            if (s == null || (optional && s == "")) return null; // Go's non-omitempty job times are empty until planned.
            if (!DateTimeOffset.TryParse(s, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var t)) throw new FormatException(name + ": expected timestamp");
            return t;
        }
        public static JsonElement.ArrayEnumerator Array(JsonElement e, string name)
        {
            if (e.ValueKind != JsonValueKind.Array) throw new FormatException(name + ": expected array");
            return e.EnumerateArray();
        }
    }
}
