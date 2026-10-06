// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>The tenant's unfinished commands, split by whether the platform can still call them off.</summary>
    public sealed class FreshRunPlan
    {
        public FreshRunPlan(IReadOnlyList<CommandItem> cancellable, IReadOnlyList<CommandItem> atDevice, bool truncated)
        {
            Cancellable = cancellable;
            AtDevice = atDevice;
            Truncated = truncated;
        }

        /// <summary>QUEUED, HELD or PARKED: the platform still has them, and cancelling stops them being delivered.</summary>
        public IReadOnlyList<CommandItem> Cancellable { get; }

        /// <summary>SENT: already at a device. Cancelling would stop no actuation, so the platform leaves them be.</summary>
        public IReadOnlyList<CommandItem> AtDevice { get; }

        /// <summary>More unfinished commands exist than the pages read.</summary>
        public bool Truncated { get; }

        public int Total => Cancellable.Count + AtDevice.Count;

        public string Describe()
        {
            if (Total == 0) return "no unfinished commands";
            return $"{Cancellable.Count} can be cancelled, {AtDevice.Count} already sent to a device (those will still run){(Truncated ? ", and more beyond the pages read" : "")}";
        }
    }

    /// <summary>What cancelling did.</summary>
    public sealed class FreshRunResult
    {
        public int Cancelled { get; set; }

        /// <summary>The platform answered with a state other than CANCELLED (the command had just been sent, or had finished).</summary>
        public int Unchanged { get; set; }

        public int Failed { get; set; }
        public string FirstError { get; set; }

        public string Describe() =>
            $"cancelled {Cancelled}" + (Unchanged > 0 ? $", {Unchanged} had already moved on" : "") + (Failed > 0 ? $", {Failed} could not be cancelled ({FirstError})" : "");
    }

    /// <summary>
    /// The presenter's Fresh run: list the tenant's unfinished commands (so commands left from an earlier
    /// run cannot drive a machine the moment it reconnects) and, when the presenter confirms, cancel the
    /// ones the platform can still stop through command-delivery's <c>cancelCommand</c>. It is an operator
    /// act, offered and never automatic, and it says plainly what it could not stop (a SENT command is at
    /// its device). The queries run over the operator's token; the pure parts are testable without a network.
    /// </summary>
    public static class FreshRun
    {
        public const int PageSize = 100, MaxPages = 5;

        public const string CancelMutation = "mutation Cancel($t: String!) { cancelCommand(token: $t) { token status } }";

        public static string ListVariables(int page)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                w.WriteStartObject("c");
                w.WriteNumber("pageNumber", page);
                w.WriteNumber("pageSize", PageSize);
                w.WriteStartArray("statuses");
                foreach (var s in new[] { "QUEUED", "HELD", "SENT", "PARKED" }) w.WriteStringValue(s);
                w.WriteEndArray();
                w.WriteEndObject();
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        public static string CancelVariables(string token)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                w.WriteString("t", token);
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        /// <summary>Splits listed commands by whether they can be cancelled. Terminal and unknown states are neither.</summary>
        public static FreshRunPlan Plan(IEnumerable<CommandItem> listed, bool truncated)
        {
            var cancellable = new List<CommandItem>();
            var atDevice = new List<CommandItem>();
            foreach (var c in listed)
            {
                switch (CommandStatus.Parse(c.Command.Status).State)
                {
                    case CommandState.Queued:
                    case CommandState.Held:
                    case CommandState.Parked:
                        cancellable.Add(c);
                        break;
                    case CommandState.Sent:
                        atDevice.Add(c);
                        break;
                }
            }

            return new FreshRunPlan(cancellable, atDevice, truncated);
        }

        /// <summary>Lists the unfinished commands, up to <see cref="MaxPages"/> pages.</summary>
        public static async Task<FreshRunPlan> ListAsync(QueryFn commands, Func<DateTimeOffset> clock, CancellationToken ct)
        {
            var all = new List<CommandItem>();
            var truncated = false;
            for (var page = 1; page <= MaxPages; page++)
            {
                var data = await commands(ObserverQueries.CommandsQuery(false), ListVariables(page), ct).ConfigureAwait(false);
                var rows = ObserverQueries.ParseCommands(data, clock());
                all.AddRange(rows);
                if (rows.Count < PageSize) break;
                if (page == MaxPages)
                {
                    // a full last page is not "more": it is only more when one row follows it
                    var next = await commands(ObserverQueries.CommandsQuery(false), ListVariables(page + 1), ct).ConfigureAwait(false);
                    truncated = ObserverQueries.ParseCommands(next, clock()).Count > 0;
                }
            }

            return Plan(all, truncated);
        }

        /// <summary>Cancels what the platform can still stop, one command at a time (each answer is read).</summary>
        public static async Task<FreshRunResult> CancelAsync(QueryFn commands, FreshRunPlan plan, CancellationToken ct)
        {
            var result = new FreshRunResult();
            foreach (var c in plan.Cancellable)
            {
                ct.ThrowIfCancellationRequested();
                try
                {
                    var data = await commands(CancelMutation, CancelVariables(c.Command.Token), ct).ConfigureAwait(false);
                    using var doc = JsonDocument.Parse(data);
                    string status = null;
                    if (doc.RootElement.TryGetProperty("cancelCommand", out var row) && row.TryGetProperty("status", out var s) && s.ValueKind == JsonValueKind.String)
                        status = s.GetString();
                    if (CommandStatus.Parse(status).State == CommandState.Cancelled) result.Cancelled++;
                    else result.Unchanged++;
                }
                catch (OperationCanceledException) { throw; }
                catch (Exception e)
                {
                    result.Failed++;
                    if (result.FirstError == null) result.FirstError = Redactor.Redact(e.GetType().Name + ": " + e.Message);
                }
            }

            return result;
        }
    }
}
