// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.App
{
    public enum FreshState { Idle, Listing, Confirm, Cancelling }

    /// <summary>
    /// The presenter's hands, as logic without a keyboard: pick a machine, prepare its next low-fuel cycle,
    /// resume its work, and start a fresh run (list the tenant's unfinished commands, ask, cancel). Everything
    /// here changes inputs or the platform's QUEUE of old commands; nothing creates a command or an alarm,
    /// and nothing raises a tank. Each local action writes a <c>presenter</c> row to the machine's timeline.
    /// </summary>
    public sealed class PresenterControls
    {
        public const string HelpLine = "[ ] machine · T timeline · P prepare low fuel · G resume work · F fresh run · R readiness · L local sim";

        readonly TaskDirector director;
        readonly Func<string, MachineModel> modelOf;
        readonly Func<QueryFn> commandQuery;
        readonly Func<DateTimeOffset> clock;
        int selected;
        bool manual;
        string lastFollowed;
        FreshRunPlan plan;

        public PresenterControls(TaskDirector director, Func<string, MachineModel> modelOf, Func<QueryFn> commandQuery, Func<DateTimeOffset> clock = null)
        {
            this.director = director ?? throw new ArgumentNullException(nameof(director));
            this.modelOf = modelOf ?? throw new ArgumentNullException(nameof(modelOf));
            this.commandQuery = commandQuery;
            this.clock = clock ?? (() => DateTimeOffset.UtcNow);
        }

        public FreshState Fresh { get; private set; }

        /// <summary>The last thing the presenter's actions said, and when.</summary>
        public string Message { get; private set; }
        public DateTimeOffset MessageAt { get; private set; }
        public int Version { get; private set; }

        public string Selected => director.Machines.Count == 0 ? null : director.Machines[Math.Max(0, Math.Min(selected, director.Machines.Count - 1))];

        /// <summary>Raised with what a presenter action said, as it says it (a recording listens here).</summary>
        public event Action<string> Said;

        void Say(string text)
        {
            Message = text;
            Said?.Invoke(text);
            MessageAt = clock();
            Version++;
            PlatformLog.Info("presenter · " + text);
        }

        /// <summary>Selects the previous (-1) or next (+1) machine and holds the selection until another machine is commanded.</summary>
        public void Select(int delta)
        {
            var n = director.Machines.Count;
            if (n == 0) return;
            selected = ((selected + delta) % n + n) % n;
            manual = true;
            Version++;
        }

        /// <summary>Follows the machine most recently commanded, unless the presenter picked one since.</summary>
        public void FollowLatest(string latestCommanded)
        {
            if (latestCommanded == null || latestCommanded == lastFollowed) return;
            lastFollowed = latestCommanded;
            manual = false;
            var i = IndexOf(latestCommanded);
            if (i >= 0) selected = i;
            Version++;
        }

        int IndexOf(string id)
        {
            for (var i = 0; i < director.Machines.Count; i++)
                if (director.Machines[i] == id) return i;
            return -1;
        }

        public bool IsManual => manual;

        /// <summary>Prepares the selected machine's next low-fuel cycle (inputs only: it can lower the tank, never raise it).</summary>
        public string PrepareLowFuel() => PrepareLowFuel(Selected);

        /// <summary>The same for a named machine (the acceptance run's probe); null when there is no such machine.</summary>
        public string PrepareLowFuel(string id)
        {
            if (id == null || IndexOf(id) < 0) return null;
            var said = PresenterActions.PrepareLowFuel(id, modelOf(id), director.Timeline);
            Say(id + ": " + said);
            return said;
        }

        /// <summary>Sends the selected machine back to its routine track, if it was parked.</summary>
        public string Resume()
        {
            var id = Selected;
            if (id == null) return null;
            var said = director[id].Resume();
            Say(id + ": " + (said ?? "resume work"));
            return said;
        }

        // ---------------------------------------------------------------- fresh run

        /// <summary>Starts a fresh run: lists the tenant's unfinished commands, then waits for <see cref="ConfirmFresh"/> or <see cref="DeclineFresh"/>.</summary>
        public async Task BeginFresh(CancellationToken ct)
        {
            if (Fresh == FreshState.Listing || Fresh == FreshState.Cancelling) return;
            var query = commandQuery?.Invoke();
            if (query == null)
            {
                Say("fresh run: the command plane is not available");
                return;
            }

            Fresh = FreshState.Listing;
            Say("fresh run: listing the tenant's unfinished commands");
            try
            {
                plan = await FreshRun.ListAsync(query, clock, ct);
            }
            catch (OperationCanceledException)
            {
                Fresh = FreshState.Idle;
                throw;
            }
            catch (Exception e)
            {
                Fresh = FreshState.Idle;
                Say("fresh run: could not list commands: " + Redactor.Redact(e.GetType().Name + ": " + e.Message));
                return;
            }

            if (plan.Total == 0)
            {
                Fresh = FreshState.Idle;
                Say("fresh run: " + plan.Describe());
                return;
            }

            Fresh = FreshState.Confirm;
            Say("fresh run: " + plan.Describe() + ". Y cancels what can be cancelled; N or Esc keeps them");
        }

        public void DeclineFresh()
        {
            if (Fresh != FreshState.Confirm) return;
            Fresh = FreshState.Idle;
            plan = null;
            Say("fresh run: nothing cancelled");
        }

        /// <summary>The presenter confirmed: cancels what the platform can still stop.</summary>
        public async Task ConfirmFresh(CancellationToken ct)
        {
            if (Fresh != FreshState.Confirm || plan == null) return;
            var query = commandQuery?.Invoke();
            var p = plan;
            plan = null;
            Fresh = FreshState.Cancelling;
            Say($"fresh run: cancelling {p.Cancellable.Count} command(s)");
            try
            {
                var r = await FreshRun.CancelAsync(query, p, ct);
                Say("fresh run: " + r.Describe() + (p.AtDevice.Count > 0 ? $"; {p.AtDevice.Count} already at a device will still run" : ""));
            }
            catch (OperationCanceledException)
            {
                throw;
            }
            catch (Exception e)
            {
                Say("fresh run: cancelling failed: " + Redactor.Redact(e.GetType().Name + ": " + e.Message));
            }
            finally
            {
                Fresh = FreshState.Idle;
            }
        }

        // ---------------------------------------------------------------- the panel

        /// <summary>The timeline panel's text for the selected machine, or null when there is nothing to say.</summary>
        public string PanelText(DateTimeOffset now, int rows = 10)
        {
            var id = Selected;
            if (id == null) return null;
            var sb = new StringBuilder();
            var text = director.Timeline.Text(id, rows);
            var fresh = Message != null && (now - MessageAt).TotalSeconds < 20 || Fresh == FreshState.Confirm;
            if (text.Length == 0 && !fresh) return null;
            sb.Append("Timeline (the device's own account, not platform state)\n");
            sb.Append(id).Append(" · ").Append(director[id].Status()).Append(manual ? " · selected" : "").Append('\n');
            if (text.Length > 0) sb.Append('\n').Append(text);
            if (fresh && Message != null) sb.Append("\n\n").Append(Message);
            return sb.ToString();
        }
    }
}
