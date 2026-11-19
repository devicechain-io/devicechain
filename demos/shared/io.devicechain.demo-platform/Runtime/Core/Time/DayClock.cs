// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// The demos' one day clock. A sim time is seconds since midnight of day 0; the hour of day, the day index and the "d0 13:45:00" label all
    /// derive from it. Towerpulse's scripted day (TowerDay) introduced the convention and its TimeClock now forwards here, so Harborpulse's sky and
    /// gauges read the very same clock rather than a second one.
    /// </summary>
    public static class DayClock
    {
        public const double Day = 86400.0;

        /// <summary>Seconds into the day, 0..86400.</summary>
        public static double TimeOfDay(double simS) { return simS - Math.Floor(simS / Day) * Day; }

        public static int DayIndex(double simS) { return (int)Math.Floor(simS / Day); }

        /// <summary>The hour of day, 0..24.</summary>
        public static double Hours(double simS) { return TimeOfDay(simS) / 3600.0; }

        public static double At(int day, int hour, int minute) { return day * Day + hour * 3600.0 + minute * 60.0; }

        public static string Format(double simS)
        {
            double tod = TimeOfDay(simS);
            int h = (int)(tod / 3600), m = (int)((tod - h * 3600) / 60), s = (int)(tod - h * 3600 - m * 60);
            return "d" + DayIndex(simS) + " " + h.ToString("00") + ":" + m.ToString("00") + ":" + s.ToString("00");
        }

        /// <summary>"HH:MM" of the hour of day, as the Go simulator's ScenarioClock.SimTime renders it.</summary>
        public static string FormatHourMinute(double simS)
        {
            int minutes = (int)(TimeOfDay(simS) / 60.0) % (24 * 60);
            return (minutes / 60).ToString("00") + ":" + (minutes % 60).ToString("00");
        }
    }

    /// <summary>
    /// A compressed day: a runner whose whole sim day takes <see cref="DayLengthSeconds"/> of wall time and opened at <see cref="StartHour"/>. It is
    /// the mapping the Go simulator's ScenarioClock states on <c>/status</c> (sim hour = startHour + 24 x elapsed / dayLength, and a beat's wall time
    /// is startedAt + ((day x 24 + hour) - startHour) / 24 x dayLength), written once so a scene and a verifier cannot disagree about it.
    /// </summary>
    public readonly struct CompressedDay
    {
        public CompressedDay(double startHour, double dayLengthSeconds)
        {
            if (double.IsNaN(startHour) || startHour < 0 || startHour >= 24) throw new ArgumentOutOfRangeException(nameof(startHour), startHour, "must be an hour of day in [0, 24)");
            if (double.IsNaN(dayLengthSeconds) || double.IsInfinity(dayLengthSeconds) || dayLengthSeconds <= 0)
                throw new ArgumentOutOfRangeException(nameof(dayLengthSeconds), dayLengthSeconds, "must be a positive number of seconds");
            StartHour = startHour;
            DayLengthSeconds = dayLengthSeconds;
        }

        public double StartHour { get; }
        public double DayLengthSeconds { get; }

        /// <summary>The sim time (<see cref="DayClock"/> seconds) at a wall time since the run's first tick.</summary>
        public double SimSecondsAt(double elapsedWallSeconds) => StartHour * 3600.0 + DayClock.Day * elapsedWallSeconds / DayLengthSeconds;

        /// <summary>The wall seconds since the first tick at which the run reaches a sim time; the inverse of <see cref="SimSecondsAt"/>.</summary>
        public double ElapsedWallSecondsAt(double simS) => (simS - StartHour * 3600.0) * DayLengthSeconds / DayClock.Day;

        /// <summary>The sim time a runner reports as (day, hour of day).</summary>
        public static double SimSecondsOf(int day, double hourOfDay) => day * DayClock.Day + hourOfDay * 3600.0;
    }
}
