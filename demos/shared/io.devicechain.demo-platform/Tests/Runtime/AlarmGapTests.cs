// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Demos.Platform;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    /// <summary>
    /// The alarm stream is silent when nothing happens, which is also what a stream that stopped delivering looks like. The
    /// detector is the independent check: the platform's own list of active alarms against what the observer holds.
    /// </summary>
    public sealed class AlarmGapDetectorTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 29, 9, 0, 0, TimeSpan.Zero);
        static readonly TimeSpan Grace = TimeSpan.FromSeconds(6);

        static string[] L(params string[] t) => t;

        [Test]
        public void AnAlarmThePlatformListsAndTheObserverNeverHeardIsAGapOnceTheGraceHasPassed()
        {
            var d = new AlarmGapDetector(Grace);
            Assert.IsFalse(d.Check(L("a1"), L(), T0).Any, "first sight: the stream may be a moment behind");
            Assert.IsFalse(d.Check(L("a1"), L(), T0.AddSeconds(5)).Any);
            var gap = d.Check(L("a1"), L(), T0.AddSeconds(6));
            Assert.IsTrue(gap.Any);
            CollectionAssert.AreEqual(L("a1"), gap.Missed);
            CollectionAssert.IsEmpty(gap.EndedUnseen);
        }

        [Test]
        public void AnAlarmTheStreamDeliveredInTimeIsNotAGap()
        {
            var d = new AlarmGapDetector(Grace);
            d.Check(L("a1"), L(), T0);
            Assert.IsFalse(d.Check(L("a1"), L("a1"), T0.AddSeconds(3)).Any);
            Assert.IsFalse(d.Check(L("a1"), L("a1"), T0.AddSeconds(30)).Any);
        }

        [Test]
        public void AnAlarmHeldAsActiveThatThePlatformNoLongerListsIsAGapToo()
        {
            var d = new AlarmGapDetector(Grace);
            d.Check(L(), L("a1"), T0);
            var gap = d.Check(L(), L("a1"), T0.AddSeconds(6));
            CollectionAssert.AreEqual(L("a1"), gap.EndedUnseen);
            CollectionAssert.IsEmpty(gap.Missed);
        }

        [Test]
        public void ADisagreementThatResolvesItselfLeavesNoTraceForTheNextOne()
        {
            var d = new AlarmGapDetector(Grace);
            d.Check(L("a1"), L(), T0);
            d.Check(L("a1"), L("a1"), T0.AddSeconds(4));
            // the same token missing again later starts a fresh clock rather than inheriting the old one
            Assert.IsFalse(d.Check(L("a1"), L(), T0.AddSeconds(100)).Any);
            Assert.IsTrue(d.Check(L("a1"), L(), T0.AddSeconds(106)).Any);
        }

        [Test]
        public void AGapIsReportedOnceAndTheClockStartsOverAfterIt()
        {
            var d = new AlarmGapDetector(Grace);
            d.Check(L("a1"), L(), T0);
            Assert.IsTrue(d.Check(L("a1"), L(), T0.AddSeconds(6)).Any);
            Assert.IsFalse(d.Check(L("a1"), L(), T0.AddSeconds(7)).Any, "the repair is in flight; it is not reported every poll");
            Assert.IsTrue(d.Check(L("a1"), L(), T0.AddSeconds(13)).Any, "still missing after another grace: reported again");
        }

        [Test]
        public void SeveralAlarmsAreTrackedIndependently()
        {
            var d = new AlarmGapDetector(Grace);
            d.Check(L("a1"), L(), T0);
            d.Check(L("a1", "a2"), L(), T0.AddSeconds(4));
            var gap = d.Check(L("a1", "a2"), L(), T0.AddSeconds(7));
            CollectionAssert.AreEqual(L("a1"), gap.Missed);
            gap = d.Check(L("a1", "a2"), L(), T0.AddSeconds(11));
            CollectionAssert.AreEqual(L("a2"), gap.Missed);
        }

        [Test]
        public void ANegativeGraceIsRefused()
        {
            Assert.Throws<ArgumentOutOfRangeException>(() => new AlarmGapDetector(TimeSpan.FromSeconds(-1)));
        }
    }
}
