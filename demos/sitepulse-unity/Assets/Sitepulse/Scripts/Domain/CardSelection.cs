// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>What the card policy is told about one device this frame (no scene, no UI).</summary>
    public readonly struct CardInput
    {
        public CardInput(string id, bool visible, float share, int alarmRank, bool commandInFlight)
        {
            Id = id;
            Visible = visible;
            Share = share;
            AlarmRank = alarmRank;
            CommandInFlight = commandInFlight;
        }

        public string Id { get; }

        /// <summary>The device can be pointed at: on screen, its anchor inside the frame, not behind the ground.</summary>
        public bool Visible { get; }

        /// <summary>Its height on screen as a share of the largest machine in view; 1 for a device that is not judged by size (the plant).</summary>
        public float Share { get; }

        /// <summary>0 for no active alarm, else <see cref="CardSelection.AlarmRank"/> of its severity.</summary>
        public int AlarmRank { get; }

        /// <summary>The platform's command for it is not finished, or the device's own task is still running.</summary>
        public bool CommandInFlight { get; }
    }

    /// <summary>
    /// Which devices carry a card, decided from values alone. A device shows a card when it has an active alarm,
    /// is under an active command (a command that has just finished keeps its card for <see cref="CommandLingerSeconds"/>
    /// so its result is seen), or is the one selected device. At most <see cref="MaxCards"/> show; the selected one
    /// comes first, then alarms (the worse severity first), then commands. Equal priorities keep whichever was already
    /// shown, so two equal cards never swap places. A device that is too small on screen gets none, with hysteresis:
    /// it must reach <see cref="MinShare"/> to appear and stays until it falls below <see cref="MinShare"/> times
    /// <see cref="ShareKeep"/>, so a card does not flicker at the threshold. The selected device ignores the size rule.
    /// </summary>
    public sealed class CardSelection
    {
        public const double CommandLingerSeconds = 5.0;
        public const float ShareKeep = 0.8f;

        const int SelectedBase = 3000, AlarmBase = 2000, CommandBase = 1000;

        readonly Dictionary<string, double> inFlightAt = new Dictionary<string, double>(StringComparer.Ordinal);
        readonly HashSet<string> shown = new HashSet<string>(StringComparer.Ordinal);
        readonly List<(int priority, bool was, string id)> scratch = new List<(int, bool, string)>();

        public int MaxCards { get; set; } = 4;
        public float MinShare { get; set; } = 0.35f;

        /// <summary>The devices chosen last time, best first.</summary>
        public IReadOnlyCollection<string> Shown => shown;

        public void Reset()
        {
            inFlightAt.Clear();
            shown.Clear();
        }

        /// <summary>The platform's severity as a rank: a worse alarm outranks a lesser one. Unknown or absent severity is the lowest.</summary>
        public static int AlarmRank(string severity)
        {
            switch ((severity ?? "").ToUpperInvariant())
            {
                case "CRITICAL": return 5;
                case "MAJOR": return 4;
                case "MINOR": return 3;
                case "WARNING": return 2;
                default: return 1;
            }
        }

        /// <summary>Whether a command card is still wanted: in flight now, or finished less than the linger ago.</summary>
        public bool CommandActive(string id, double now) => inFlightAt.TryGetValue(id, out var at) && now - at <= CommandLingerSeconds;

        /// <param name="now">Seconds on any steady clock.</param>
        /// <param name="selected">The one selected device, or null.</param>
        /// <param name="chosen">Receives the ids, best first.</param>
        public void Choose(double now, IReadOnlyList<CardInput> inputs, string selected, List<string> chosen)
        {
            chosen.Clear();
            scratch.Clear();
            foreach (var d in inputs)
                if (d.CommandInFlight) inFlightAt[d.Id] = now;
            foreach (var d in inputs)
            {
                if (!d.Visible) continue;
                bool isSelected = selected != null && string.Equals(d.Id, selected, StringComparison.Ordinal);
                int priority;
                if (isSelected) priority = SelectedBase;
                else if (d.AlarmRank > 0) priority = AlarmBase + d.AlarmRank;
                else if (CommandActive(d.Id, now)) priority = CommandBase;
                else continue;
                bool was = shown.Contains(d.Id);
                if (!isSelected && d.Share < (was ? MinShare * ShareKeep : MinShare)) continue;
                scratch.Add((priority, was, d.Id));
            }

            scratch.Sort((a, b) =>
            {
                int c = b.priority.CompareTo(a.priority);
                if (c != 0) return c;
                c = b.was.CompareTo(a.was);
                return c != 0 ? c : string.CompareOrdinal(a.id, b.id);
            });
            shown.Clear();
            for (int i = 0; i < scratch.Count && i < MaxCards; i++)
            {
                chosen.Add(scratch[i].id);
                shown.Add(scratch[i].id);
            }
        }
    }
}
