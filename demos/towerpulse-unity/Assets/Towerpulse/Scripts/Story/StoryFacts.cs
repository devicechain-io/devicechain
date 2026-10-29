// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.Story
{
    public enum StoryFactKind { Alarm, Command, LedgerReceived, LedgerAnswered }

    /// <summary>
    /// One thing the story layer was told, with the model time it was told at. The boards (<see cref="CommandBoard"/>, <see cref="AlarmBoard"/>, the device
    /// ledger) are a pure function of a sequence of these, which is what lets a recording stand in for the plane: the live app builds a fact for everything
    /// the platform's observer and the command ledger report, applies it with <see cref="StoryFacts.Apply"/> and records it; a replay reads the same facts
    /// back and applies them with the same call. Nothing else changes a board.
    /// </summary>
    public sealed class StoryFact
    {
        public StoryFactKind Kind;
        /// <summary>The model time (seconds on the story clock) the fact reached the story layer.</summary>
        public double SimS;
        public FactSource Source = FactSource.Platform;
        public string Device = "";
        /// <summary>The alarm key (Alarm) or the command name (Command, Ledger*).</summary>
        public string Key = "";
        public string Token = "";
        /// <summary>The platform's raw command status (Command).</summary>
        public string Status = "";
        public string Severity = "";
        public bool Active;
        public bool Success;
        public string Reason = "";
        /// <summary>The command's parameters (LedgerReceived), so a replay can actuate the model the way the live device did.</summary>
        public readonly Dictionary<string, double> Numbers = new Dictionary<string, double>();
        public readonly Dictionary<string, string> Strings = new Dictionary<string, string>();

        public static StoryFact Alarm(string device, string key, string severity, bool active, FactSource src, double simS)
        {
            return new StoryFact { Kind = StoryFactKind.Alarm, Device = device ?? "", Key = key ?? "", Severity = severity ?? "", Active = active, Source = src, SimS = simS };
        }

        public static StoryFact Command(string token, string device, string name, string status, FactSource src, double simS)
        {
            return new StoryFact { Kind = StoryFactKind.Command, Token = token ?? "", Device = device ?? "", Key = name ?? "", Status = status ?? "", Source = src, SimS = simS };
        }
    }

    public static class StoryFacts
    {
        /// <summary>Folds one fact into the boards. The single place a recorded and a live fact become the same thing.</summary>
        public static void Apply(StoryFact f, CommandBoard commands, AlarmBoard alarms, CommandLedger ledger)
        {
            switch (f.Kind)
            {
                case StoryFactKind.Alarm:
                    if (f.Active) alarms.Raise(f.Device, f.Key, f.Severity, f.Source, f.SimS);
                    else alarms.Clear(f.Device, f.Key, f.SimS);
                    break;
                case StoryFactKind.Command:
                    commands.ApplyPlatform(f.Token, f.Device, f.Key, f.Status, f.SimS, f.Source);
                    break;
                case StoryFactKind.LedgerReceived:
                    ledger.Received(f.Token, f.Device, f.Key, f.SimS, ArgsOf(f));
                    break;
                case StoryFactKind.LedgerAnswered:
                    ledger.Answered(f.Token, f.Success, f.Reason, f.SimS);
                    break;
            }
        }

        public static StoryFact FromReceived(CommandRecord r)
        {
            var f = new StoryFact { Kind = StoryFactKind.LedgerReceived, Source = FactSource.Device, Device = r.DeviceExternalId, Key = r.Name, Token = r.Token, SimS = r.ReceivedSimS };
            if (r.Args != null)
            {
                foreach (var kv in r.Args.Numbers) f.Numbers[kv.Key] = kv.Value;
                foreach (var kv in r.Args.Strings) f.Strings[kv.Key] = kv.Value;
            }
            return f;
        }

        public static StoryFact FromAnswered(CommandRecord r)
        {
            return new StoryFact { Kind = StoryFactKind.LedgerAnswered, Source = FactSource.Device, Device = r.DeviceExternalId, Key = r.Name, Token = r.Token, Success = r.Success, Reason = r.Reason, SimS = r.AnsweredSimS };
        }

        public static CommandArgs ArgsOf(StoryFact f)
        {
            var a = new CommandArgs();
            foreach (var kv in f.Numbers) a.Numbers[kv.Key] = kv.Value;
            foreach (var kv in f.Strings) a.Strings[kv.Key] = kv.Value;
            return a;
        }

        static string N(double v) { return double.IsNaN(v) ? "-" : v.ToString("0.###", CultureInfo.InvariantCulture); }

        /// <summary>
        /// The story layer's state as text, in a fixed order and with fixed number formatting, so two runs can be compared with one string compare.
        /// Everything the visuals read from the boards is in it: every card's stages with their sources and times, every alarm fact, the ledger.
        /// </summary>
        public static string Digest(CommandBoard commands, AlarmBoard alarms, CommandLedger ledger)
        {
            var sb = new StringBuilder();
            foreach (var c in commands.Cards)
            {
                sb.Append("card ").Append(c.Id).Append(' ').Append(c.Device).Append(' ').Append(c.Name).Append(" outcome=").Append(c.Outcome).Append(" reason=").Append(c.FailReason);
                foreach (CardStage s in Enum.GetValues(typeof(CardStage)))
                {
                    sb.Append(' ').Append(s).Append('=');
                    if (!c.Has(s)) sb.Append('-');
                    else sb.Append(c.SourceOf(s)).Append('@').Append(N(c.AtS(s)));
                }
                sb.Append('\n');
            }
            foreach (var a in alarms.Facts)
            {
                sb.Append("alarm ").Append(a.Device).Append(' ').Append(a.Key).Append(' ').Append(a.Severity).Append(" active=").Append(a.Active).Append(' ').Append(a.Source)
                    .Append(" raised=").Append(N(a.RaisedAtS)).Append(" cleared=").Append(N(a.ClearedAtS)).Append('\n');
            }
            foreach (var r in ledger.Records)
            {
                sb.Append("ledger ").Append(r.Token).Append(' ').Append(r.DeviceExternalId).Append(' ').Append(r.Name).Append(" recv=").Append(N(r.ReceivedSimS))
                    .Append(" answered=").Append(r.Answered).Append(" ok=").Append(r.Success).Append(" reason=").Append(r.Reason).Append('\n');
            }
            return sb.ToString();
        }
    }
}
