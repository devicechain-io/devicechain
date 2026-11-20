// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Demos.Platform;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    public class ObserverLocationTests
    {
        static readonly DateTimeOffset At = new DateTimeOffset(2026, 11, 19, 12, 0, 0, TimeSpan.Zero);
        static string Row(string coordinates) => "{\"latestLocations\":[{\"deviceToken\":\"truck\",\"occurredTime\":\"2026-11-19T12:00:00Z\"" + coordinates + "}]}";
        [Test]
        public void CommandPollCoverageRequiresSuccessAndResetsAfterFailureOrGap()
        {
            var health = new ObserverStatus();
            Assert.That(health.CommandsHealthySince, Is.Null);
            health.SetPoll("commands", true, null, At);
            Assert.That(health.CommandsHealthySince, Is.EqualTo(At));
            health.SetPoll("commands", true, null, At.AddSeconds(1));
            Assert.That(health.CommandsLastOk, Is.EqualTo(At.AddSeconds(1)));
            health.SetPoll("commands", false, "offline", At.AddSeconds(2));
            Assert.That(health.CommandsHealthySince, Is.Null);
            health.SetPoll("commands", true, null, At.AddSeconds(3));
            Assert.That(health.CommandsHealthySince, Is.EqualTo(At.AddSeconds(3)));
            health.SetPoll("commands", true, null, At.AddSeconds(7));
            Assert.That(health.CommandsHealthySince, Is.EqualTo(At.AddSeconds(7)));
        }

        [Test]
        public void CoordinatesAreSelectedAndParsedWithoutLosingZero()
        {
            Assert.That(ObserverQueries.LocationsQuery, Does.Contain("latitude longitude"));
            var rows = ObserverQueries.ParseLocations(Row(",\"latitude\":0,\"longitude\":-12.2"), At);
            Assert.That(rows.Count, Is.EqualTo(1));
            Assert.That(rows[0].Location.Latitude, Is.EqualTo(0));
            Assert.That(rows[0].Location.Longitude, Is.EqualTo(-12.2));
        }
        [TestCase("")]
        [TestCase(",\"latitude\":null,\"longitude\":null")]
        public void MissingFixCoordinatesAreNull(string fields)
        {
            var row = ObserverQueries.ParseLocations(Row(fields), At)[0].Location;
            Assert.That(row.Latitude, Is.Null); Assert.That(row.Longitude, Is.Null);
        }
        [TestCase(",\"latitude\":\"49.8\"")]
        [TestCase(",\"longitude\":false")]
        [TestCase(",\"latitude\":{}")]
        [TestCase(",\"longitude\":[]")]
        public void WrongTypesRefuseTheRow(string fields) => Assert.That(ObserverQueries.ParseLocations(Row(fields), At), Is.Empty);
    }
}
