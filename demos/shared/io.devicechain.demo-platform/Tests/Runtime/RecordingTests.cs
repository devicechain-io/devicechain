// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text.Json;
using DeviceChain.Demos.Platform.Recording;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    public sealed class RecordLineTests
    {
        static readonly DateTimeOffset At = new DateTimeOffset(2026, 10, 1, 12, 0, 0, TimeSpan.Zero);

        static JsonElement Parse(string json)
        {
            using (var doc = JsonDocument.Parse(json)) return doc.RootElement.Clone();
        }

        [Test]
        public void AMeasurementLineReadsBackAsItWasWritten()
        {
            var line = ObservedLine.Of(ObservedKinds.Measurement);
            line.T = 1.2346;
            line.Utc = At;
            line.Device = "dev-1";
            line.Name = "fuel_pct";
            line.Value = 41.5;
            line.OccurredAt = At.AddSeconds(-1);
            line.ObservedAt = At;

            var back = ObservedLine.Read(Parse(line.ToJson()));
            Assert.AreEqual(ObservedKinds.Measurement, back.K);
            Assert.AreEqual("dev-1", back.Device);
            Assert.AreEqual("fuel_pct", back.Name);
            Assert.AreEqual(41.5, back.Value);
            Assert.AreEqual(1.235, back.T, 1e-9, "times are kept to the millisecond");
            Assert.AreEqual(At.AddSeconds(-1), back.OccurredAt);
        }

        [Test]
        public void ACredentialInALineNeverReachesTheFile()
        {
            var line = ObservedLine.Of(ObservedKinds.Status);
            line.Utc = At;
            line.Name = "stream";
            line.State = "live";
            line.Reason = "token 0123456789abcdef0123456789abcdef refused";

            var json = line.ToJson();
            StringAssert.DoesNotContain("0123456789abcdef0123456789abcdef", json);
            StringAssert.Contains("hex:", json);
        }

        [Test]
        public void ALineWithNoTimeIsRefusedNotDefaulted()
        {
            var ex = Assert.Throws<RecordingFormatException>(() => ObservedLine.Read(Parse("{\"utc\":\"2026-10-01T12:00:00.0000000Z\",\"k\":\"status\"}")));
            StringAssert.Contains("\"t\"", ex.Message);
        }

        [Test]
        public void ATimeThatIsNotATimeIsRefused()
        {
            Assert.Throws<RecordingFormatException>(() => JsonIo.ParseTime("yesterday-ish", "line.utc"));
        }

        [Test]
        public void TheFormatHasOneVersionAndNamesItsFiles()
        {
            Assert.AreEqual(1, RecordingFormat.Version);
            Assert.AreEqual("sim.bin", RecordingFiles.SimBin);
        }
    }
}
