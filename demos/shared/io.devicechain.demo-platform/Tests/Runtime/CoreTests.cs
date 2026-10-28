// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.RegularExpressions;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.DevicePlane;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    public sealed class RedactorTests
    {
        const string Credential = "0123456789abcdef0123456789abcdef";
        const string Jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl";

        [Test]
        public void ALogLineShowsOnlyTheLastFourOfACredential()
        {
            var text = Redactor.Redact("token " + Credential + " ok");
            Assert.AreEqual("token cred:…cdef ok", text);
            StringAssert.DoesNotContain(Credential, text);
        }

        [Test]
        public void AJwtIsReplacedByAFingerprintOfItself()
        {
            var text = Redactor.Redact("Bearer " + Jwt);
            StringAssert.StartsWith("Bearer jwt:", text);
            StringAssert.DoesNotContain("eyJ", text);
            Assert.AreEqual(text, Redactor.Redact("Bearer " + Jwt), "the same token redacts to the same fingerprint");
        }

        [Test]
        public void ARecordingKeepsTwoCredentialsThatShareTheirLastFourApart()
        {
            var other = "ffffffffffffffffffffffffffffcdef";
            var a = Redactor.RedactForRecording(Credential);
            var b = Redactor.RedactForRecording(other);
            Assert.IsTrue(Regex.IsMatch(a, "^hex:[0-9a-f]{12}$"), a);
            Assert.AreNotEqual(a, b, "the log form would not tell these apart; the recording form must");
            Assert.AreEqual(a, Redactor.RedactForRecording(a), "the output is too short to be redacted again");
        }

        [Test]
        public void ShortHexAndPlainTextAreLeftAlone()
        {
            const string short31 = "0123456789abcdef0123456789abcde";
            Assert.AreEqual("deadbeef " + short31, Redactor.Redact("deadbeef " + short31));
            Assert.IsNull(Redactor.Redact(null));
            Assert.AreEqual("", Redactor.RedactForRecording(""));
        }
    }

    public sealed class CommandStatusTests
    {
        [TestCase("QUEUED", CommandState.Queued)]
        [TestCase("SENT", CommandState.Sent)]
        [TestCase("SUCCESSFUL", CommandState.Successful)]
        [TestCase("TIMEOUT", CommandState.Timeout)]
        [TestCase("CANCELLED", CommandState.Cancelled)]
        public void ThePlatformsSpellingParsesToItsState(string raw, CommandState state)
        {
            var s = CommandStatus.Parse(raw);
            Assert.AreEqual(state, s.State);
            Assert.AreEqual(raw, s.Label);
        }

        [Test]
        public void AnythingElseIsUnknownAndKeepsItsText()
        {
            var s = CommandStatus.Parse("EXPLODED");
            Assert.AreEqual(CommandState.Unknown, s.State);
            Assert.AreEqual("EXPLODED", s.Label);
            Assert.IsFalse(s.IsTerminal);
            Assert.AreEqual("UNKNOWN", CommandStatus.Parse(null).Label);
            Assert.Throws<ArgumentException>(() => CommandStatus.Of(CommandState.Unknown));
        }

        [Test]
        public void OnlyTheFiveFinalStatesAreTerminal()
        {
            foreach (var raw in new[] { "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED" })
                Assert.IsTrue(CommandStatus.Parse(raw).IsTerminal, raw);
            foreach (var raw in new[] { "QUEUED", "HELD", "SENT", "PARKED" })
                Assert.IsFalse(CommandStatus.Parse(raw).IsTerminal, raw);
        }
    }

    public sealed class DemoIdentityTests
    {
        string before;

        [SetUp]
        public void Remember() => before = DemoIdentity.Name;

        // the identity is process-wide: a test that changes it puts it back for whatever runs next
        [TearDown]
        public void Restore() => DemoIdentity.Configure(before);

        [Test]
        public void AConfiguredNameIsInTheLogPrefixAndTheCredentialSuffix()
        {
            DemoIdentity.Configure("tower-7");
            Assert.AreEqual("[tower-7] ", DemoIdentity.LogPrefix);
            Assert.AreEqual("-tower-7", DemoIdentity.CredentialSuffix);
        }

        [TestCase("")]
        [TestCase(" ")]
        [TestCase("Upper")]
        [TestCase("has space")]
        [TestCase("dot.ted")]
        public void ANameThatCouldNotBeALogTagOrACredentialSuffixIsRefused(string name)
        {
            Assert.Throws<ArgumentException>(() => DemoIdentity.Configure(name));
            Assert.AreEqual(before, DemoIdentity.Name, "a refused name changes nothing");
        }
    }

    public sealed class OutboundRingTests
    {
        static Sample Measurement(long sequence) =>
            Sample.Measurement(sequence, new DateTimeOffset(2026, 10, 1, 0, 0, 0, TimeSpan.Zero).AddSeconds(sequence), new Dictionary<string, double> { ["x"] = sequence });

        [Test]
        public void AFullRingDropsItsOldestAndCountsTheLoss()
        {
            var ring = new OutboundRing();
            for (var i = 1; i <= OutboundRing.Capacity + 3; i++) ring.Enqueue(Measurement(i));
            Assert.AreEqual(OutboundRing.Capacity, ring.Count);
            Assert.AreEqual(3, ring.Dropped);
            Assert.IsTrue(ring.TryPeek(out var oldest));
            Assert.AreEqual(4, oldest.Sequence, "the three oldest went");
        }

        [Test]
        public void ASampleStaysUntilItIsAcknowledged()
        {
            var ring = new OutboundRing();
            ring.Enqueue(Measurement(1));
            ring.Enqueue(Measurement(2));
            Assert.IsTrue(ring.TryPeek(out var first));
            Assert.AreEqual(1, first.Sequence);
            Assert.IsTrue(ring.TryPeek(out first), "peeking removes nothing");
            Assert.IsTrue(ring.Remove(1));
            Assert.IsFalse(ring.Remove(1), "an acknowledged sample is gone");
            Assert.IsTrue(ring.TryPeek(out var next));
            Assert.AreEqual(2, next.Sequence);
        }

        [Test]
        public void ALocationSampleNeverCarriesANegativeSpeed()
        {
            var s = Sample.Location(1, new DateTimeOffset(2026, 10, 1, 0, 0, 0, TimeSpan.Zero), new GeoPoint(39.0, -117.0), 1200.0, -4.0, 90.0);
            Assert.AreEqual(SampleKind.Location, s.Kind);
            Assert.AreEqual(0.0, s.SpeedMps);
            Assert.AreEqual(39.0, s.Latitude);
        }
    }
}
