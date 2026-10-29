// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>
    /// Feeds the story layer from a recording instead of the plane. Each <see cref="Step"/> applies every recorded fact whose model time has been reached,
    /// in recorded order, through the same <see cref="StoryFacts.Apply"/> the live app uses. A command the recorded device received is also submitted to
    /// the replayed model, so the room cools after the boost the way it did on the day; the model's own answer is ignored, because the recorded answer is
    /// the one that happened.
    /// </summary>
    public sealed class RecordingReplay
    {
        readonly TowerPlane plane;
        readonly IReadOnlyList<StoryFact> facts;
        readonly CommandBoard commands;
        readonly AlarmBoard alarms;
        readonly CommandLedger ledger;
        int next;

        public RecordingReplay(TowerPlane plane, IReadOnlyList<StoryFact> facts, CommandBoard commands, AlarmBoard alarms, CommandLedger ledger)
        {
            this.plane = plane ?? throw new ArgumentNullException(nameof(plane));
            this.facts = facts ?? throw new ArgumentNullException(nameof(facts));
            this.commands = commands ?? throw new ArgumentNullException(nameof(commands));
            this.alarms = alarms ?? throw new ArgumentNullException(nameof(alarms));
            this.ledger = ledger ?? throw new ArgumentNullException(nameof(ledger));
            for (int i = 1; i < facts.Count; i++)
                if (facts[i].SimS < facts[i - 1].SimS) throw new ArgumentException("the recorded facts are not in model-time order at index " + i, nameof(facts));
        }

        public int Applied => next;
        public bool Finished => next >= facts.Count;

        public int Step()
        {
            double now = plane.Model.TimeS;
            int n = 0;
            while (next < facts.Count && facts[next].SimS <= now)
            {
                var f = facts[next++];
                StoryFacts.Apply(f, commands, alarms, ledger);
                // the live app folds the ledger into the cards every frame, so a fact is always folded before the next one arrives; do the same per fact,
                // so the cards come out the same however large a step the caller takes
                commands.Sync(ledger, f.SimS);
                if (f.Kind == StoryFactKind.LedgerReceived)
                {
                    var device = plane.Find(f.Device);
                    if (device != null) plane.Commands.Submit(device, f.Token, f.Key, StoryFacts.ArgsOf(f), _ => { });
                }
                n++;
            }
            commands.Sync(ledger, now);
            return n;
        }
    }
}
