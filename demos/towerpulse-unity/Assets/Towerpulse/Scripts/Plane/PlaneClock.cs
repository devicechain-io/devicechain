// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Towerpulse.Model;

namespace DeviceChain.Towerpulse.Plane
{
    /// <summary>
    /// Wall time to MODEL time, and nothing else. Event occurred-time is always the wall clock (the platform bounds future skew, and its DETECT windows,
    /// absence timers and command expiry all read real time), so an accelerated run only speeds the physical model and must be labelled. The default is
    /// real time. The model starts at <see cref="StartSimS"/> and is fast-forwarded there before the first reading.
    /// </summary>
    public sealed class PlaneClock
    {
        public PlaneClock(double startSimS, double speed = 1.0)
        {
            if (!(speed > 0) || speed > 60) throw new ArgumentOutOfRangeException(nameof(speed), "speed must be in (0, 60]");
            StartSimS = startSimS; Speed = speed;
        }

        public double StartSimS { get; }
        public double Speed { get; }
        public bool Accelerated { get { return Speed > 1.0000001; } }

        public double SimAt(double wallSeconds) { return StartSimS + (wallSeconds < 0 ? 0 : wallSeconds) * Speed; }

        public string Label
        {
            get
            {
                var what = Accelerated ? "ACCELERATED MODEL x" + Speed.ToString("0.#", System.Globalization.CultureInfo.InvariantCulture) : "real time";
                return what + " from " + TimeClock.Format(StartSimS);
            }
        }

        public static PlaneClock FromHour(double hourOfDay, double speed = 1.0) { return new PlaneClock(hourOfDay * 3600.0, speed); }
    }
}
