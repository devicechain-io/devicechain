// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class DirectorTestsStatus
    {
        static string With(string extra) => Kit.Status().Replace(",\"beat\":\"quiet\"}", ",\"beat\":\"quiet\"," + extra + "}");
        const string Digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

        [Test]
        public void DefectsDigestBackfillAndDrainsParse()
        {
            var s = ScenarioStatus.Parse(With("\"defects\":[\"truck-stays-home\"],\"layoutDigest\":\"" + Digest + "\"," +
                "\"backfill\":[{\"device\":\"hp-charger-east-1\",\"day\":0,\"buffered\":2,\"first\":\"2026-11-19T12:10:00Z\",\"last\":\"2026-11-19T12:11:00Z\",\"times\":[\"2026-11-19T12:10:00.000000001Z\",\"2026-11-19T12:11:00Z\"],\"powerKw\":[1,2],\"values\":[]}]," +
                "\"s5\":{\"restoreDrains\":[{\"day\":0,\"offered\":9,\"accepted\":6,\"refused429\":3,\"backpressured\":0,\"ambiguous\":0,\"deadLettered\":0,\"ordinaryShed\":0,\"firstRefusalAt\":\"2026-11-19T12:12:00Z\"}]}"));
            Assert.That(s.Defects, Is.EqualTo(new[] { "truck-stays-home" }));
            Assert.That(s.LayoutDigest, Is.EqualTo(Digest));
            Assert.That(s.Backfill[0].Times, Is.EqualTo(new[] { "2026-11-19T12:10:00.000000001Z", "2026-11-19T12:11:00Z" }));
            Assert.That(s.Backfill[0].Buffered, Is.EqualTo(2));
            Assert.That(s.RestoreDrains[0].Refused429, Is.EqualTo(3));
            Assert.That(s.RestoreDrains[0].CaughtUpAt, Is.Null);
        }

        [Test]
        public void AbsentFieldsAreAbsentNotDefaulted()
        {
            var s = ScenarioStatus.Parse(Kit.Status());
            Assert.That(s.Defects, Is.Empty);
            Assert.That(s.LayoutDigest, Is.Null, "a runner that reports no digest must not look like a matching one");
            Assert.That(s.Backfill, Is.Empty);
        }

        [TestCase("\"layoutDigest\":\"ABC\"")]
        [TestCase("\"layoutDigest\":\"" + Digest + "0\"")]
        [TestCase("\"defects\":[\"\"]")]
        [TestCase("\"backfill\":[{\"device\":\"x\",\"day\":0,\"buffered\":1,\"first\":\"2026-11-19T12:10:00Z\",\"last\":\"2026-11-19T12:10:00Z\"}]")]
        public void MalformedFieldsFailLoudly(string extra) =>
            Assert.That(() => ScenarioStatus.Parse(With(extra)), Throws.TypeOf<FormatException>());
    }
}
