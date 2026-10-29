// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Model;

namespace DeviceChain.Towerpulse.Plane
{
    /// <summary>A command's parameters, already read off the wire: numbers and strings by name. <see cref="Malformed"/> is a payload that was not an object.</summary>
    public sealed class CommandArgs
    {
        public readonly Dictionary<string, double> Numbers = new Dictionary<string, double>();
        public readonly Dictionary<string, string> Strings = new Dictionary<string, string>();
        /// <summary>Parameters of any other type (bool, array, object, null).</summary>
        public readonly List<string> Other = new List<string>();
        public bool Malformed;

        public int Count { get { return Numbers.Count + Strings.Count + Other.Count; } }
    }

    public readonly struct CommandResult
    {
        public CommandResult(bool success, string reason) { Success = success; Reason = reason ?? ""; }
        public bool Success { get; }
        public string Reason { get; }
    }

    /// <summary>
    /// The device side of the command contract. A command is answered only when the thing it asked for is true of the model: boost-cooling when the zone is at
    /// its setpoint (or failed when its window runs out), restart when the chiller is running and its water is cold again; set-setpoint and set-mode the moment
    /// they are applied. Not engine, not network: the host feeds it commands and calls <see cref="Settle"/> after each model step.
    ///
    /// Physical dedup is separate from reply identity. A command token actuates once; every further delivery of the same token (the SDK session already
    /// replays a remembered outcome under the new dispatch nonce, this is the second line) is answered with the one outcome, so a redelivery while pending
    /// is answered when the first is, and a replay after completion is answered at once, never actuating twice. Each delivery gets its own answer
    /// callback, which is how the reply can carry that delivery's own dispatch nonce.
    /// </summary>
    public sealed class CommandEngine
    {
        sealed class Entry
        {
            public CommandTicket Ticket;
            public bool Done;
            public CommandResult Result;
            public readonly List<Action<CommandResult>> Waiters = new List<Action<CommandResult>>();
        }

        public const int MemoryLimit = 512;

        readonly BuildingModel model;
        readonly Dictionary<string, Entry> byToken = new Dictionary<string, Entry>(StringComparer.Ordinal);
        readonly Queue<string> order = new Queue<string>();
        readonly List<Entry> pending = new List<Entry>();

        public CommandEngine(BuildingModel model) { this.model = model ?? throw new ArgumentNullException(nameof(model)); }

        public int PendingCount { get { return pending.Count; } }

        /// <summary>How many times a command actually reached the model (a redelivery does not count).</summary>
        public int Actuations { get; private set; }

        /// <summary>Checks a command against the device's profile. Null means it is valid.</summary>
        public static string Validate(PlaneKind kind, string name, CommandArgs a)
        {
            if (string.IsNullOrEmpty(name)) return "the command has no name";
            if (a == null) a = new CommandArgs();
            switch (kind)
            {
                case PlaneKind.Vav:
                    if (name == Wire.BoostCooling) return a.Malformed ? NeedsObject(name) : One(a, Wire.MinutesParam, Wire.BoostMinutesMin, Wire.BoostMinutesMax, true);
                    if (name == Wire.SetSetpoint) return a.Malformed ? NeedsObject(name) : One(a, Wire.CelsiusParam, Wire.SetpointMin, Wire.SetpointMax, false);
                    break;
                case PlaneKind.Ahu:
                    if (name == Wire.SetMode)
                    {
                        if (a.Malformed) return NeedsObject(name);
                        if (a.Count != 1 || !a.Strings.TryGetValue(Wire.ModeParam, out var m)) return "set-mode needs exactly a string \"mode\"";
                        if (m != "normal" && m != "economy" && m != "boost") return "mode must be normal, economy or boost";
                        return null;
                    }
                    break;
                case PlaneKind.Chiller:
                    if (name == Wire.Restart) return a.Count == 0 ? null : "restart takes no parameters";
                    break;
            }
            return "unknown command \"" + Clip(name) + "\" for this device";
        }

        static string NeedsObject(string name) { return name + " needs a payload object"; }

        static string One(CommandArgs a, string name, double lo, double hi, bool whole)
        {
            if (a.Count != 1 || !a.Numbers.TryGetValue(name, out var v)) return "needs exactly a number \"" + name + "\"";
            if (double.IsNaN(v) || double.IsInfinity(v) || v < lo || v > hi) return name + " must be in [" + lo + ", " + hi + "]";
            if (whole && v != Math.Floor(v)) return name + " must be a whole number";
            return null;
        }

        static string Clip(string s) { return s.Length <= 40 ? s : s.Substring(0, 40); }

        /// <summary>
        /// Applies a valid command and takes its answer. Instant kinds answer now; boost and restart answer from <see cref="Settle"/>. An invalid or
        /// rejected command answers now with a failure that says why.
        /// </summary>
        public void Submit(PlaneDevice device, string commandToken, string name, CommandArgs args, Action<CommandResult> answer)
        {
            if (!string.IsNullOrEmpty(commandToken) && byToken.TryGetValue(commandToken, out var seen))
            {
                if (seen.Done) answer(seen.Result); else seen.Waiters.Add(answer);
                return;
            }

            var entry = new Entry();
            entry.Waiters.Add(answer);
            if (!string.IsNullOrEmpty(commandToken)) Remember(commandToken, entry);

            var bad = Validate(device.Kind, name, args);
            if (bad != null) { Finish(entry, new CommandResult(false, bad)); return; }
            CommandTicket tk;
            Actuations++;
            switch (name)
            {
                case Wire.BoostCooling: tk = model.BoostCooling(device.LayoutId, args.Numbers[Wire.MinutesParam]); break;
                case Wire.SetSetpoint: tk = model.SetSetpoint(device.LayoutId, args.Numbers[Wire.CelsiusParam]); break;
                case Wire.SetMode: tk = model.SetAhuMode(device.LayoutId, args.Strings[Wire.ModeParam]); break;
                default: tk = model.RestartChiller(device.LayoutId); break;
            }
            entry.Ticket = tk;
            if (tk.IsFinal) { Finish(entry, Result(tk)); return; }
            pending.Add(entry);
        }

        void Remember(string token, Entry e)
        {
            byToken[token] = e;
            order.Enqueue(token);
            while (order.Count > MemoryLimit) byToken.Remove(order.Dequeue());
        }

        static void Finish(Entry e, CommandResult r)
        {
            e.Done = true; e.Result = r;
            var w = e.Waiters.ToArray();
            e.Waiters.Clear();
            foreach (var a in w) a(r);
        }

        /// <summary>Answers every pending command whose ticket has reached a final state. Call after the model advances.</summary>
        public int Settle()
        {
            int n = 0;
            for (int i = pending.Count - 1; i >= 0; i--)
            {
                var p = pending[i];
                if (!p.Ticket.IsFinal) continue;
                pending.RemoveAt(i);
                Finish(p, Result(p.Ticket));
                n++;
            }
            return n;
        }

        /// <summary>Fails everything still waiting (the plane is shutting down): silence would leave the command at SENT until it expired.</summary>
        public void FailAll(string reason)
        {
            var all = pending.ToArray();
            pending.Clear();
            foreach (var p in all) Finish(p, new CommandResult(false, reason));
        }

        static CommandResult Result(CommandTicket tk)
        {
            switch (tk.State)
            {
                case CommandState.Completed: return new CommandResult(true, "");
                case CommandState.Expired: return new CommandResult(false, "expired: " + tk.Reason);
                default: return new CommandResult(false, tk.Reason);
            }
        }
    }
}
