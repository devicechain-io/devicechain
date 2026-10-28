// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Towerpulse.Model
{
    public enum CommandKind { BoostCooling, SetSetpoint, SetAhuMode, RestartChiller }

    /// <summary>
    /// Where a command stands. <see cref="Active"/> is the only non-final state: the device has accepted the command and is working on it. The platform
    /// reports SUCCESSFUL on <see cref="Completed"/> and a failure on the other three finals; completion is the device's report that the thing HAPPENED,
    /// not that the command arrived.
    /// </summary>
    public enum CommandState { Active, Completed, Expired, Failed }

    public sealed class CommandTicket
    {
        public int Id { get; internal set; }
        public CommandKind Kind { get; internal set; }
        public string Target { get; internal set; }
        public CommandState State { get; internal set; }
        public double IssuedAtS { get; internal set; }
        /// <summary>Sim time the ticket reached a final state; NaN while it is Active.</summary>
        public double EndedAtS { get; internal set; } = double.NaN;
        /// <summary>Why a ticket Failed or Expired, empty otherwise.</summary>
        public string Reason { get; internal set; } = "";
        /// <summary>Sim seconds an Active boost may run before it Expires; 0 for kinds without a window.</summary>
        public double WindowS { get; internal set; }

        public bool IsFinal { get { return State != CommandState.Active; } }
    }

    public enum AhuMode { Normal, Economy, Boost }

    public sealed class ModelEvent
    {
        public double TimeS { get; internal set; }
        public string Kind { get; internal set; }
        public string Subject { get; internal set; }
        public string Detail { get; internal set; }
        public override string ToString() { return TimeClock.Format(TimeS) + " " + Kind + " " + Subject + " " + Detail; }
    }

    public static class TimeClock
    {
        public const double Day = 86400.0;

        /// <summary>Seconds into the day, 0..86400.</summary>
        public static double TimeOfDay(double simS) { return simS - Math.Floor(simS / Day) * Day; }
        public static int DayIndex(double simS) { return (int)Math.Floor(simS / Day); }
        /// <summary>Day 0 is a Monday; 5 and 6 are the weekend.</summary>
        public static bool IsWeekday(double simS) { int d = DayIndex(simS) % 7; if (d < 0) d += 7; return d < 5; }
        public static double Hours(double simS) { return TimeOfDay(simS) / 3600.0; }
        public static double At(int day, int hour, int minute) { return day * Day + hour * 3600.0 + minute * 60.0; }

        public static string Format(double simS)
        {
            double tod = TimeOfDay(simS);
            int h = (int)(tod / 3600), m = (int)((tod - h * 3600) / 60), s = (int)(tod - h * 3600 - m * 60);
            return "d" + DayIndex(simS) + " " + h.ToString("00") + ":" + m.ToString("00") + ":" + s.ToString("00");
        }
    }
}
