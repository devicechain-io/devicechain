// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Model;

namespace DeviceChain.Towerpulse.Plane
{
    /// <summary>
    /// The campus as the device plane sees it: the scripted day, the 31 devices, the commands. The host calls <see cref="Advance"/> with wall seconds and
    /// then takes each device's <see cref="Readings"/>; everything here is single threaded and engine free. The platform closes the loop (rules and
    /// commands), never this class.
    /// </summary>
    public sealed class TowerPlane
    {
        public TowerPlane(ulong seed, PlaneClock clock)
        {
            Clock = clock ?? throw new ArgumentNullException(nameof(clock));
            Seed = seed;
            Build();
        }

        public ulong Seed { get; }
        public PlaneClock Clock { get; }
        public TowerDay Day { get; private set; }
        public BuildingModel Model { get { return Day.Model; } }
        public IReadOnlyList<PlaneDevice> Devices { get; private set; }
        public CommandEngine Commands { get; private set; }

        void Build()
        {
            Day = new TowerDay(Seed);
            Devices = DeviceMap.All(Day.Model.Layout);
            Commands = new CommandEngine(Day.Model);
            Day.AdvanceTo(Clock.StartSimS);
        }

        /// <summary>
        /// A real rewind: a new model, new tickets and a new timeline from the pinned seed, fast-forwarded to the clock's start. Commands still waiting are
        /// failed first. Time spent stopped is not carried in: the next <see cref="Advance"/> counts wall seconds from the new start.
        /// </summary>
        public void Reset()
        {
            Commands.FailAll("the take was reset");
            Build();
        }

        public PlaneDevice Find(string externalId)
        {
            foreach (var d in Devices) if (d.ExternalId == externalId) return d;
            return null;
        }

        /// <summary>Runs the day to the model time for <paramref name="wallSeconds"/> since the start, then answers what the step settled.</summary>
        public void Advance(double wallSeconds)
        {
            Day.AdvanceTo(Clock.SimAt(wallSeconds));
            Commands.Settle();
        }
    }
}
