// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Towerpulse.Model
{
    /// <summary>
    /// The scripted day behind the 90-second story, on a deterministic clock. The script owns only what happens TO the building (a hot day, a full
    /// meeting room, a chiller that trips); what the platform does about it is not here, which is the point: leave the platform out and the room stays hot.
    ///
    ///   Beat 1  people arrive                         08:00 - 09:30, staggered per zone by the seed
    ///   Beat 2  F4-East: 14 people, sun on the glass  14:00 meeting, hot a few minutes later; the rule's boost cools it; the meeting ends 14:45
    ///   Beat 3  the annex chiller trips               16:30, silent until a restart command
    /// </summary>
    public sealed class TowerDay
    {
        public static readonly double ArrivalWindowStartS = TimeClock.At(0, 8, 0);
        public static readonly double ArrivalWindowEndS = TimeClock.At(0, 9, 30);
        public static readonly double MeetingStartS = TimeClock.At(0, 14, 0);
        public static readonly double MeetingEndS = TimeClock.At(0, 14, 45);
        public static readonly double ChillerTripS = TimeClock.At(0, 16, 30);
        public const int MeetingPeople = 14;
        /// <summary>The story day is a hot one.</summary>
        public const double HotDayBiasC = 4.0;

        public BuildingModel Model { get; private set; }
        public ulong Seed { get; private set; }
        public bool ChillerTripped { get; private set; }

        public TowerDay(ulong seed)
        {
            Seed = seed;
            Model = new BuildingModel(seed, TimeClock.At(0, 6, 0));
            Model.OutdoorBiasC = HotDayBiasC;
            Model.HoldOccupancy(BuildingLayout.HotZone, MeetingStartS, MeetingEndS, MeetingPeople);
        }

        public double TimeS { get { return Model.TimeS; } }

        /// <summary>Advance the model to a sim time, firing the scripted fault when the clock crosses it.</summary>
        public void AdvanceTo(double simS)
        {
            if (!ChillerTripped && simS >= ChillerTripS)
            {
                Model.AdvanceTo(ChillerTripS);
                Model.TripChiller(BuildingLayout.AnnexChiller);
                ChillerTripped = true;
            }
            Model.AdvanceTo(simS);
        }

        /// <summary>Advance by show time (seconds into the video) through <see cref="ShowClock"/>.</summary>
        public void AdvanceToShow(double showS) { AdvanceTo(ShowClock.ToSim(showS)); }
    }

    /// <summary>
    /// Show time to sim time. A working day cannot be filmed in 90 s at one speed, so the clock is piecewise linear: it crawls through the beats and
    /// races through the gaps. The table is data, so the shot file can retime a beat without touching the model. Never goes backwards.
    /// </summary>
    public static class ShowClock
    {
        // (show seconds, sim hours of day 0)
        static readonly double[] ShowS = { 0, 10, 12, 35, 40, 55, 90 };
        static readonly double[] SimH = { 8.0, 9.5, 13.75, 15.0, 16.25, 17.5, 18.5 };

        public static double ToSim(double showS)
        {
            if (showS <= ShowS[0]) return SimH[0] * 3600.0;
            for (int i = 1; i < ShowS.Length; i++)
            {
                if (showS <= ShowS[i])
                {
                    double f = (showS - ShowS[i - 1]) / (ShowS[i] - ShowS[i - 1]);
                    return (SimH[i - 1] + f * (SimH[i] - SimH[i - 1])) * 3600.0;
                }
            }
            return SimH[SimH.Length - 1] * 3600.0 + (showS - ShowS[ShowS.Length - 1]) * 60.0;
        }
    }
}
