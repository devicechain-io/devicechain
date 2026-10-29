// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>
    /// Who is the witness for a fact on a card. <see cref="Device"/> is what the simulated device plane itself sent or received; <see cref="Platform"/> is
    /// what the platform answered when asked (its own record); <see cref="Preview"/> is a scripted stand-in for the platform when no platform is
    /// connected, and is always drawn as such.
    /// </summary>
    public enum FactSource { Device, Platform, Preview }

    public static class FactLabels
    {
        public static string Tag(FactSource s)
        {
            switch (s)
            {
                case FactSource.Platform: return "platform";
                case FactSource.Preview: return "preview";
                default: return "device (simulated)";
            }
        }
    }

    /// <summary>The three steps of a command the card shows. Delivered is NOT done: SENT says it left the platform, only SUCCESSFUL says the job happened.</summary>
    public enum CardStage { Queued = 0, Delivered = 1, Done = 2 }

    public enum CardOutcome { InFlight, Successful, Failed }

    /// <summary>
    /// One command, keyed by the platform's command id, which every card that mentions it shows unchanged. Facts arrive from two witnesses in any
    /// order and are folded in monotonically: a stage once reached is never lost, a late QUEUED never un-sends a command, and the first terminal word stands.
    /// </summary>
    public sealed class CommandCard
    {
        readonly bool[] has = new bool[3];
        readonly FactSource[] source = new FactSource[3];
        readonly double[] at = { double.NaN, double.NaN, double.NaN };

        public CommandCard(string id, string device, string name)
        {
            Id = id ?? ""; Device = device ?? ""; Name = name ?? "";
        }

        public string Id { get; }
        public string Device { get; }
        public string Name { get; }
        public CardOutcome Outcome { get; private set; } = CardOutcome.InFlight;
        public string FailReason { get; private set; } = "";

        /// <summary>True once the command has been seen leaving for the device (or the device has seen it). Says nothing about the job.</summary>
        public bool Delivered { get { return has[(int)CardStage.Delivered]; } }

        /// <summary>True only once a terminal SUCCESS was reported. A delivered command is not done.</summary>
        public bool Done { get { return Outcome == CardOutcome.Successful; } }

        public bool Terminal { get { return Outcome != CardOutcome.InFlight; } }

        public bool Has(CardStage s) { return has[(int)s]; }
        public FactSource SourceOf(CardStage s) { return source[(int)s]; }
        public double AtS(CardStage s) { return at[(int)s]; }

        /// <summary>The furthest stage reached (Queued before anything is known).</summary>
        public CardStage Stage
        {
            get
            {
                if (has[(int)CardStage.Done]) return CardStage.Done;
                if (has[(int)CardStage.Delivered]) return CardStage.Delivered;
                return CardStage.Queued;
            }
        }

        void Reach(CardStage s, FactSource src, double when)
        {
            int i = (int)s;
            if (has[i]) return;
            has[i] = true; source[i] = src; at[i] = when;
        }

        /// <summary>The platform's status word for the command, as it spells it. Words that are not understood change nothing.</summary>
        public void ApplyPlatformStatus(string raw, double when, FactSource src = FactSource.Platform)
        {
            switch (raw)
            {
                case "QUEUED":
                case "HELD":
                case "PARKED":
                    Reach(CardStage.Queued, src, when);
                    break;
                case "SENT":
                    Reach(CardStage.Queued, src, when);
                    Reach(CardStage.Delivered, src, when);
                    break;
                case "SUCCESSFUL":
                    Reach(CardStage.Queued, src, when);
                    Reach(CardStage.Delivered, src, when);
                    Finish(CardOutcome.Successful, "", src, when);
                    break;
                case "FAILED":
                case "TIMEOUT":
                case "EXPIRED":
                case "CANCELLED":
                    Finish(CardOutcome.Failed, raw, src, when);
                    break;
            }
        }

        /// <summary>The device plane received the command.</summary>
        public void DeviceReceived(double when)
        {
            Reach(CardStage.Delivered, FactSource.Device, when);
        }

        /// <summary>The device plane answered the command.</summary>
        public void DeviceAnswered(bool success, string reason, double when)
        {
            Reach(CardStage.Delivered, FactSource.Device, when);
            Finish(success ? CardOutcome.Successful : CardOutcome.Failed, success ? "" : (string.IsNullOrEmpty(reason) ? "FAILED" : reason), FactSource.Device, when);
        }

        void Finish(CardOutcome outcome, string reason, FactSource src, double when)
        {
            if (Terminal) return;
            Outcome = outcome;
            FailReason = reason ?? "";
            Reach(CardStage.Done, src, when);
        }

        public static string StageLabel(CardStage s)
        {
            switch (s)
            {
                case CardStage.Queued: return "QUEUED";
                case CardStage.Delivered: return "SENT";
                default: return "SUCCESSFUL";
            }
        }

        /// <summary>The plain-words gloss under each stage, so a viewer reads "delivered" and "done" as two different claims.</summary>
        public static string StageGloss(CardStage s)
        {
            switch (s)
            {
                case CardStage.Queued: return "waiting";
                case CardStage.Delivered: return "delivered";
                default: return "job done";
            }
        }
    }

    /// <summary>The cards by command id, oldest first, fed from the device ledger and from whatever the platform says.</summary>
    public sealed class CommandBoard
    {
        readonly List<CommandCard> order = new List<CommandCard>();
        readonly Dictionary<string, CommandCard> byId = new Dictionary<string, CommandCard>(StringComparer.Ordinal);
        int ledgerSeen;

        public IReadOnlyList<CommandCard> Cards => order;
        public int Version { get; private set; }

        public CommandCard Get(string id)
        {
            return id != null && byId.TryGetValue(id, out var c) ? c : null;
        }

        public CommandCard Latest(string deviceExternalId = null)
        {
            for (int i = order.Count - 1; i >= 0; i--)
                if (deviceExternalId == null || order[i].Device == deviceExternalId) return order[i];
            return null;
        }

        CommandCard Ensure(string id, string device, string name)
        {
            if (byId.TryGetValue(id, out var c)) return c;
            c = new CommandCard(id, device, name);
            byId[id] = c;
            order.Add(c);
            Version++;
            return c;
        }

        /// <summary>A platform status for a command (what the platform's own record says). The card is created by the first word about it.</summary>
        public CommandCard ApplyPlatform(string id, string device, string name, string rawStatus, double when, FactSource src = FactSource.Platform)
        {
            if (string.IsNullOrEmpty(id)) return null;
            var c = Ensure(id, device, name);
            c.ApplyPlatformStatus(rawStatus, when, src);
            Version++;
            return c;
        }

        /// <summary>Folds in everything new in the device ledger. Cheap to call every frame: it re-reads only records, never the model.</summary>
        public void Sync(CommandLedger ledger, double nowSimS)
        {
            if (ledger == null) return;
            var rec = ledger.Records;
            // the ledger caps its length, so a record can fall off the front: read all, apply what is new
            for (int i = 0; i < rec.Count; i++)
            {
                var r = rec[i];
                if (r.Token.Length == 0) continue;
                var c = Ensure(r.Token, r.DeviceExternalId, r.Name);
                bool before = c.Delivered;
                c.DeviceReceived(r.ReceivedSimS);
                if (r.Answered) c.DeviceAnswered(r.Success, r.Reason, r.AnsweredSimS);
                if (!before) Version++;
            }
            ledgerSeen = rec.Count;
        }

        public int LedgerSeen { get { return ledgerSeen; } }
    }

    /// <summary>One alarm the story shows: what raised it, how bad, whether it is still active, and who says so.</summary>
    public sealed class AlarmFact
    {
        public string Key;
        public string Device;
        public string Severity;
        public bool Active;
        public FactSource Source;
        public double RaisedAtS;
        public double ClearedAtS = double.NaN;
    }

    public sealed class AlarmBoard
    {
        readonly List<AlarmFact> facts = new List<AlarmFact>();
        public IReadOnlyList<AlarmFact> Facts => facts;
        public int Version { get; private set; }

        public AlarmFact Find(string device, string key)
        {
            for (int i = facts.Count - 1; i >= 0; i--) if (facts[i].Device == device && facts[i].Key == key) return facts[i];
            return null;
        }

        /// <summary>Records an alarm becoming active (or active again after it cleared); a repeat of one already active changes nothing.</summary>
        public AlarmFact Raise(string device, string key, string severity, FactSource src, double when)
        {
            var f = Find(device, key);
            if (f != null && f.Active) return f;
            f = new AlarmFact { Key = key, Device = device, Severity = severity, Active = true, Source = src, RaisedAtS = when };
            facts.Add(f);
            Version++;
            return f;
        }

        public void Clear(string device, string key, double when)
        {
            var f = Find(device, key);
            if (f == null || !f.Active) return;
            f.Active = false; f.ClearedAtS = when;
            Version++;
        }

        /// <summary>The newest active alarm, or the newest of any when none is active; null when there are none.</summary>
        public AlarmFact Headline()
        {
            for (int i = facts.Count - 1; i >= 0; i--) if (facts[i].Active) return facts[i];
            return facts.Count > 0 ? facts[facts.Count - 1] : null;
        }
    }
}
