// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Towerpulse.Plane
{
    /// <summary>One command as the DEVICE side saw it: when it arrived, and what the device answered. Nothing the platform said is in here.</summary>
    public sealed class CommandRecord
    {
        /// <summary>The platform's command token: the one id every card shows for this command.</summary>
        public string Token;
        public string DeviceExternalId;
        public string Name;
        /// <summary>Model time the command reached the device.</summary>
        public double ReceivedSimS;
        public bool Answered;
        public bool Success;
        public string Reason = "";
        public double AnsweredSimS = double.NaN;
    }

    /// <summary>
    /// What the plane received and sent back for each command, in arrival order. Append only; the visuals read it with a cursor. It records only
    /// what crossed the device's own wire, so a card built from it can say "the device received this" and "the device answered this", and nothing about
    /// the queue the platform kept before delivery.
    /// </summary>
    public sealed class CommandLedger
    {
        public const int Limit = 256;

        readonly List<CommandRecord> records = new List<CommandRecord>();
        readonly Dictionary<string, CommandRecord> byToken = new Dictionary<string, CommandRecord>(StringComparer.Ordinal);

        public IReadOnlyList<CommandRecord> Records => records;

        /// <summary>Bumps on every change, so a reader can tell cheaply whether to look again.</summary>
        public int Version { get; private set; }

        public CommandRecord Received(string token, string deviceExternalId, string name, double simS)
        {
            if (!string.IsNullOrEmpty(token) && byToken.TryGetValue(token, out var seen)) return seen; // a redelivery is the same command
            var r = new CommandRecord { Token = token ?? "", DeviceExternalId = deviceExternalId ?? "", Name = name ?? "", ReceivedSimS = simS };
            records.Add(r);
            if (r.Token.Length > 0) byToken[r.Token] = r;
            if (records.Count > Limit)
            {
                var old = records[0];
                records.RemoveAt(0);
                if (old.Token.Length > 0) byToken.Remove(old.Token);
            }
            Version++;
            return r;
        }

        public void Answered(string token, bool success, string reason, double simS)
        {
            if (string.IsNullOrEmpty(token) || !byToken.TryGetValue(token, out var r) || r.Answered) return;
            r.Answered = true; r.Success = success; r.Reason = reason ?? ""; r.AnsweredSimS = simS;
            Version++;
        }

        public bool TryGet(string token, out CommandRecord r)
        {
            if (string.IsNullOrEmpty(token)) { r = null; return false; }
            return byToken.TryGetValue(token, out r);
        }
    }
}
