// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>Who says a proof row is so: the platform (what the observer was told), the device (its own timeline), or the presenter's hand.</summary>
    public enum ProofSource { Platform, Device, Presenter }

    /// <summary>One row of a machine's evidence chain: when it happened, who says so, and the plain words.</summary>
    public readonly struct ProofRow
    {
        public ProofRow(DateTimeOffset at, ProofSource source, string text, bool seen = false)
        {
            At = at;
            Source = source;
            Text = text;
            Seen = seen;
        }

        public DateTimeOffset At { get; }
        public ProofSource Source { get; }
        public string Text { get; }

        /// <summary>
        /// The time is when THIS APP saw the state (the 1 Hz poll), not when it happened: the platform does not date the transitions of a
        /// command between queued and finished. A drawer prints it as "seen hh:mm:ss", never as the time of the event.
        /// </summary>
        public bool Seen { get; }

        /// <summary>The label a drawer prints beside the row.</summary>
        public string SourceLabel => Source == ProofSource.Platform ? "platform" : Source == ProofSource.Device ? "device" : "presenter";
    }

    /// <summary>Where a drawer reads its rows from; <see cref="Version"/> moves whenever a row is added or the log is cleared.</summary>
    public interface IProofRows
    {
        int Version { get; }

        /// <summary>Adds the newest <paramref name="max"/> rows of a device, oldest first, to <paramref name="into"/>.</summary>
        void Rows(string deviceId, List<ProofRow> into, int max);
    }

    /// <summary>The lines the platform's detection rules draw (sitepulse.go), which a proof row names when a sample crosses one.</summary>
    public static class RuleLines
    {
        public const double LowFuelPct = 15.0;
        public const double TyreLowKpa = 600.0;

        /// <summary>The line a measurement is watched against (the rule fires on a value BELOW it); false for a key no rule watches.</summary>
        public static bool TryBelow(string key, out double line)
        {
            switch (key)
            {
                case MeasurementKeys.FuelPct: line = LowFuelPct; return true;
                case MeasurementKeys.TyrePressureKpa: line = TyreLowKpa; return true;
                default: line = 0; return false;
            }
        }
    }

    /// <summary>
    /// The proof drawer's evidence, per device: only things that HAPPENED, each with the time it happened and who says so. A row is added
    /// when the platform said something (a sample on the far side of a rule's line, an alarm changing state, a command changing state),
    /// when the device's own timeline said something, or when the presenter's hand moved an input. Nothing is inferred: a command state
    /// the observer never saw has no row, an alarm that was never reported cleared has no CLEARED row, and a device's "SUCCESS" is never
    /// turned into a platform's SUCCESSFUL. A snapshot's measurement is a fact about the past, not an event, and adds no row. Main thread.
    /// </summary>
    public sealed class ProofLog : IProofRows
    {
        public const int MaxPerDevice = 80;
        const int MaxText = 84;

        /// <summary>The device timeline's kinds the drawer shows. The rest (driving back to its track, back at work, queue arrivals) are the machine's own business.</summary>
        static readonly HashSet<string> ShownKinds = new HashSet<string>(StringComparer.Ordinal)
        {
            "received", "accepted", "refused", "superseded", "refuelling", "outcome", "stalled", "presenter",
        };

        // where a row of one command's chain stands, whatever clock each row was dated by: queued, sent, anything else short of
        // finished, the device receiving it, the device accepting (or refusing, or being superseded), the device's outcome, the platform's
        // finishing state. The order of ONE command's rows is this, never a comparison of their times.
        const int StageNone = 0, StageQueued = 10, StageSent = 20, StageOther = 25, StageReceived = 30, StageAnswered = 40, StageOutcome = 50, StageTerminal = 60;

        sealed class Entry
        {
            public ProofRow Row;
            public long Seq;
            public int Stage;

            /// <summary>A platform command row: the command's token and when the platform queued it.</summary>
            public string CommandToken;
            public DateTimeOffset Queued;

            /// <summary>A device row about a command: the tail of the command token the machine printed (null when its text had none).</summary>
            public string Tail;
            public string Kind;

            // computed on every rebuild
            public string Group;
            public DateTimeOffset Pos;
        }

        sealed class Lane
        {
            public readonly List<Entry> Entries = new List<Entry>();
            public readonly HashSet<string> Seen = new HashSet<string>(StringComparer.Ordinal);
            public readonly Dictionary<string, bool> Armed = new Dictionary<string, bool>(StringComparer.Ordinal);
            public readonly Dictionary<string, (string state, DateTimeOffset at)> AlarmLast = new Dictionary<string, (string, DateTimeOffset)>(StringComparer.Ordinal);
            public readonly List<Entry> Sorted = new List<Entry>();
            public long NextSeq;
        }

        readonly Dictionary<string, Lane> lanes = new Dictionary<string, Lane>(StringComparer.Ordinal);

        public int Version { get; private set; }

        public void Reset()
        {
            lanes.Clear();
            Version++;
        }

        Lane Of(string device)
        {
            if (!lanes.TryGetValue(device, out var l)) lanes[device] = l = new Lane();
            return l;
        }

        static string Num(double v, string format = "0.##") => v.ToString(format, CultureInfo.InvariantCulture);

        static string Clip(string s) => s.Length <= MaxText ? s : s.Substring(0, MaxText - 1) + "…";

        void Add(Lane lane, Entry e)
        {
            e.Seq = lane.NextSeq++;
            lane.Entries.Add(e);
            if (lane.Entries.Count > MaxPerDevice)
            {
                // the oldest row makes room; the presenter's hand is the one thing never evicted
                Rebuild(lane);
                Entry drop = null;
                foreach (var x in lane.Sorted)
                    if (x.Row.Source != ProofSource.Presenter) { drop = x; break; }
                lane.Entries.Remove(drop ?? lane.Sorted[0]);
            }

            Rebuild(lane);
            Version++;
        }

        static Entry Plain(DateTimeOffset at, ProofSource source, string text) => new Entry { Row = new ProofRow(at, source, text), Stage = StageNone };

        /// <summary>The tail of a command token a machine's "received" text carries: "goto-refuel (…abc)" is "abc".</summary>
        static string TailOf(string text)
        {
            if (string.IsNullOrEmpty(text) || text[text.Length - 1] != ')') return null;
            var open = text.LastIndexOf('(');
            if (open < 0) return null;
            var tail = text.Substring(open + 1, text.Length - open - 2).TrimStart('…');
            return tail.Length >= 3 ? tail : null;
        }

        /// <summary>
        /// Puts a lane's rows in order. Rows of no command go by the time they happened. The rows of ONE command stay together and in causal
        /// order (<see cref="StageQueued"/> to <see cref="StageTerminal"/>): the handshake (queued, sent, received, accepted) stands where the
        /// command was queued, and the finish (the device's outcome, then the platform's finishing state) where the device says it finished.
        /// A time seen by a poll, or taken from the device's clock, is only ever a clamp: a row cannot stand above its own command's queueing.
        /// </summary>
        static void Rebuild(Lane lane)
        {
            var anchors = new Dictionary<string, DateTimeOffset>(StringComparer.Ordinal);
            foreach (var e in lane.Entries)
                if (e.CommandToken != null && !anchors.ContainsKey(e.CommandToken)) anchors[e.CommandToken] = e.Queued;

            // which command a device row is about: a "received" names it by its token's tail, and what the machine says next is about the same one
            string current = null;
            foreach (var e in lane.Entries)
            {
                if (e.Kind == null) continue;
                var tail = e.Kind == "received" ? e.Tail : (e.Tail ?? current);
                if (e.Kind == "received") current = tail;
                if (e.Stage == StageNone || tail == null) { e.Group = null; continue; }
                string found = null;
                foreach (var t in anchors.Keys)
                    if (t.EndsWith(tail, StringComparison.Ordinal) && (found == null || anchors[t] > anchors[found])) found = t;
                e.Group = found;
            }

            var outcomeAt = new Dictionary<string, DateTimeOffset>(StringComparer.Ordinal);
            foreach (var e in lane.Entries)
                if (e.Kind != null && e.Group != null && e.Stage == StageOutcome)
                {
                    var p = e.Row.At > anchors[e.Group] ? e.Row.At : anchors[e.Group];
                    if (!outcomeAt.TryGetValue(e.Group, out var held) || p > held) outcomeAt[e.Group] = p;
                }

            foreach (var e in lane.Entries)
            {
                if (e.CommandToken != null) e.Group = e.CommandToken;
                if (e.Group == null) { e.Pos = e.Row.At; continue; }
                var anchor = anchors[e.Group];
                if (e.Stage == StageOutcome) e.Pos = e.Row.At > anchor ? e.Row.At : anchor;
                else if (e.Stage == StageTerminal) e.Pos = outcomeAt.TryGetValue(e.Group, out var o) ? o : (e.Row.At > anchor ? e.Row.At : anchor);
                else e.Pos = anchor;
            }

            lane.Sorted.Clear();
            lane.Sorted.AddRange(lane.Entries);
            lane.Sorted.Sort((a, b) =>
            {
                var c = a.Pos.UtcTicks.CompareTo(b.Pos.UtcTicks);
                if (c != 0) return c;
                c = a.Stage.CompareTo(b.Stage);
                return c != 0 ? c : a.Seq.CompareTo(b.Seq);
            });
        }

        /// <summary>
        /// A measurement the platform reported. Its first sample below a rule's line (after one at or above it) is a row; a snapshot's
        /// value is not an event and adds nothing, and does not re-arm the line.
        /// </summary>
        public void SampleObserved(string device, string key, double value, DateTimeOffset occurredAt, bool fromSnapshot)
        {
            if (device == null || fromSnapshot || !RuleLines.TryBelow(key, out var line)) return;
            var lane = Of(device);
            var armed = !lane.Armed.TryGetValue(key, out var a) || a;
            if (value >= line)
            {
                lane.Armed[key] = true;
                return;
            }

            if (!armed) return;
            lane.Armed[key] = false;
            var unit = MeasurementKeys.Unit(key);
            Add(lane, Plain(occurredAt, ProofSource.Platform, $"sample {key} {Num(value, "0.0#")} {unit} — below the rule line ({Num(line)} {unit})"));
        }

        /// <summary>
        /// An alarm event the platform reported. The platform keeps ONE alarm row per device and key and flips it between ACTIVE and CLEARED
        /// in place, so its token is the same every cycle: a row is one TRANSITION (a state, at the time it happened), however often the same
        /// transition is told (the startup snapshot, then the stream). A repeat of the state it is already in (an acknowledgement, a new
        /// severity) is not a new transition.
        /// </summary>
        public void AlarmObserved(string device, string token, string key, string state, string severity, DateTimeOffset occurredAt)
        {
            if (device == null || token == null || key == null || state == null) return;
            var lane = Of(device);
            if (!lane.Seen.Add("a|" + token + "|" + state + "|" + occurredAt.UtcTicks.ToString(CultureInfo.InvariantCulture))) return;
            // a state it is already in, as of a time no earlier than the last one, is the same transition told again (or an acknowledgement)
            if (lane.AlarmLast.TryGetValue(token, out var last))
            {
                if (occurredAt >= last.at)
                {
                    if (last.state == state) return;
                    lane.AlarmLast[token] = (state, occurredAt);
                }
            }
            else lane.AlarmLast[token] = (state, occurredAt);

            Add(lane, Plain(occurredAt, ProofSource.Platform, "alarm " + key + " " + state + (string.IsNullOrEmpty(severity) ? "" : " · " + severity)));
        }

        /// <summary>
        /// A command's state as the observer last saw it: one row for each state of each command it SAW. QUEUED is dated when the platform
        /// queued it; every other state is dated when this app SAW it (the platform does not date the transitions between), and says so
        /// (<see cref="ProofRow.Seen"/>). The rows of one command are ordered causally, not by those times.
        /// </summary>
        public void CommandObserved(string device, string token, string name, string status, DateTimeOffset queuedAt, DateTimeOffset observedAt)
        {
            if (device == null || token == null || name == null || status == null) return;
            var lane = Of(device);
            if (!lane.Seen.Add("c|" + token + "|" + status)) return;
            var queued = status == "QUEUED";
            var stage = queued ? StageQueued : status == "SENT" ? StageSent : CommandStatus.Parse(status).IsTerminal ? StageTerminal : StageOther;
            Add(lane, new Entry
            {
                Row = new ProofRow(queued ? queuedAt : observedAt, ProofSource.Platform, "command " + name + " " + status, !queued),
                Stage = stage,
                CommandToken = token,
                Queued = queuedAt,
            });
        }

        /// <summary>A row of the machine's own timeline (its account of what it did). Kinds the drawer does not show are ignored.</summary>
        public void DeviceRow(string device, DateTimeOffset at, string kind, string text)
        {
            if (device == null || kind == null || !ShownKinds.Contains(kind)) return;
            var source = kind == "presenter" ? ProofSource.Presenter : ProofSource.Device;
            var stage = kind == "received" ? StageReceived
                : kind == "accepted" || kind == "refused" || kind == "superseded" ? StageAnswered
                : kind == "outcome" ? StageOutcome : StageNone;
            var e = Plain(at, source, Clip(kind + " · " + (text ?? "")));
            e.Kind = kind;
            e.Stage = stage;
            e.Tail = kind == "received" ? TailOf(text) : null;
            Add(Of(device), e);
        }

        /// <summary>
        /// The newest <paramref name="max"/> rows, oldest first. The presenter's rows are pinned: when the chain is longer than the drawer
        /// the other rows give way, never the disclosure that the presenter's hand moved an input.
        /// </summary>
        public void Rows(string deviceId, List<ProofRow> into, int max)
        {
            if (deviceId == null || !lanes.TryGetValue(deviceId, out var lane)) return;
            var all = lane.Sorted;
            if (all.Count <= max)
            {
                foreach (var e in all) into.Add(e.Row);
                return;
            }

            var keep = new bool[all.Count];
            var budget = max;
            for (var i = all.Count - 1; i >= 0 && budget > 0; i--)
                if (all[i].Row.Source == ProofSource.Presenter) { keep[i] = true; budget--; }
            for (var i = all.Count - 1; i >= 0 && budget > 0; i--)
                if (!keep[i]) { keep[i] = true; budget--; }
            for (var i = 0; i < all.Count; i++)
                if (keep[i]) into.Add(all[i].Row);
        }
    }
}
