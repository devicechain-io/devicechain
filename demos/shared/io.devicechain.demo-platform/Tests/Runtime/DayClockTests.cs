// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Demos.Platform;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    public class DayClockTests
    {
        [Test]
        public void TheDayClockSplitsASimTimeIntoDayAndHour()
        {
            var t = DayClock.At(2, 19, 45);
            Assert.That(DayClock.DayIndex(t), Is.EqualTo(2));
            Assert.That(DayClock.Hours(t), Is.EqualTo(19.75).Within(1e-9));
            Assert.That(DayClock.Format(t), Is.EqualTo("d2 19:45:00"));
            Assert.That(DayClock.FormatHourMinute(t), Is.EqualTo("19:45"));
            Assert.That(DayClock.Hours(-1800), Is.EqualTo(23.5).Within(1e-9), "before day 0 is the previous day's evening, not a negative hour");
            Assert.That(DayClock.DayIndex(-1800), Is.EqualTo(-1));
        }

        // The Go simulator's ScenarioClock: hour = startHour + 24 x elapsed / dayLength, day = floor of the same over 24.
        [Test]
        public void ACompressedDayIsTheSimulatorsMapping()
        {
            var day = new CompressedDay(6, 1440);
            Assert.That(DayClock.Hours(day.SimSecondsAt(0)), Is.EqualTo(6).Within(1e-9));
            Assert.That(DayClock.Hours(day.SimSecondsAt(720)), Is.EqualTo(18).Within(1e-9), "half a 24-minute day after 06:00 is 18:00");
            Assert.That(DayClock.DayIndex(day.SimSecondsAt(1080)), Is.EqualTo(1), "three quarters of a day after 06:00 is past midnight");
            Assert.That(DayClock.Hours(day.SimSecondsAt(1080)), Is.EqualTo(0).Within(1e-9));
            Assert.That(day.ElapsedWallSecondsAt(day.SimSecondsAt(1234.5)), Is.EqualTo(1234.5).Within(1e-9));
            Assert.That(CompressedDay.SimSecondsOf(1, 2.5), Is.EqualTo(DayClock.Day + 9000));
        }

        [Test]
        public void ACompressedDayRefusesANonsenseClock()
        {
            Assert.Throws<ArgumentOutOfRangeException>(() => new CompressedDay(6, 0));
            Assert.Throws<ArgumentOutOfRangeException>(() => new CompressedDay(6, double.NaN));
            Assert.Throws<ArgumentOutOfRangeException>(() => new CompressedDay(24, 1440));
        }
    }
}
