// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;
using static DeviceChain.Sitepulse.Tests.PlatformTestData;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class PlatformCredentialTests
    {
        const string Id = "0123456789abcdef0123456789abcdef";

        [Test]
        public void ARowForTheDeviceIsAccepted()
        {
            Assert.IsNull(CredentialResolver.Check(Row("sp-hauler-01-cred", "sp-hauler-01"), "sp-hauler-01"));
        }

        [Test]
        public void ARowForAnotherDeviceIsRefused()
        {
            var why = CredentialResolver.Check(Row("sp-hauler-01-cred", "sp-hauler-02"), "sp-hauler-01");
            StringAssert.Contains("belongs to device sp-hauler-02, not sp-hauler-01", why);
        }

        [Test]
        public void ADisabledRowIsRefused()
        {
            StringAssert.Contains("disabled", CredentialResolver.Check(Row("c", "d", enabled: false), "d"));
        }

        [Test]
        public void AnotherCredentialTypeIsRefused()
        {
            StringAssert.Contains("MQTT_BASIC, not ACCESS_TOKEN", CredentialResolver.Check(Row("c", "d", type: "MQTT_BASIC"), "d"));
        }

        [Test]
        public void ARowWithNoIdIsRefused()
        {
            StringAssert.Contains("no id", CredentialResolver.Check(Row("c", "d", id: ""), "d"));
        }

        [Test]
        public void ARowNeverPrintsItsId()
        {
            StringAssert.DoesNotContain(Id, Row("c", "d").ToString());
        }

        [Test]
        public void TheBatchAndSearchShapesParse()
        {
            var batch = CredentialResolver.ParseBatch(CredBatch(Cred("a-cred", "a"), Cred("b-cred", "b", enabled: false)));
            Assert.AreEqual(2, batch.Count);
            Assert.AreEqual("a", batch[0].DeviceToken);
            Assert.IsFalse(batch[1].Enabled);
            Assert.AreEqual(Id, batch[0].CredentialId);

            var search = CredentialResolver.ParseSearch(CredSearch(Cred("a-cred", "a")));
            Assert.AreEqual(1, search.Count);
            Assert.AreEqual("ACCESS_TOKEN", search[0].CredentialType);
        }

        static IReadOnlyList<BindResult> Bound(params string[] deviceTokens) =>
            deviceTokens.Select((t, i) => new BindResult { ExternalId = "SP-" + i, DeviceToken = t, Outcome = BindOutcome.Bound }).ToList();

        static System.Func<string, Task<List<CredentialRow>>> Search(params CredentialRow[] rows) =>
            _ => Task.FromResult(rows.ToList());

        static CredentialOutcome Resolve(string device, List<CredentialRow> batch, System.Func<string, Task<List<CredentialRow>>> search, DeviceCredentials into = null) =>
            CredentialResolver.Resolve(Bound(device), batch, search, into ?? new DeviceCredentials()).GetAwaiter().GetResult().Single();

        [Test]
        public void TheBatchedRowAnswersWithoutTheFallback()
        {
            var creds = new DeviceCredentials();
            var o = Resolve("sp-hauler-01", new List<CredentialRow> { Row("sp-hauler-01-cred", "sp-hauler-01") },
                _ => throw new System.InvalidOperationException("the fallback must not run"), creds);
            Assert.IsTrue(o.Ok);
            Assert.AreEqual(CredentialPath.Batched, o.Path);
            Assert.IsTrue(creds.TryGet("sp-hauler-01", out var held));
            Assert.AreEqual(Id, held);
        }

        [Test]
        public void AMissingRowFallsBackToTheSearchAndSaysSo()
        {
            var creds = new DeviceCredentials();
            var o = Resolve("sp-hauler-01", new List<CredentialRow>(), Search(Row("custom-token", "sp-hauler-01", id: "ffffffffffffffffffffffffffffffff")), creds);
            Assert.IsTrue(o.Ok);
            Assert.AreEqual(CredentialPath.Fallback, o.Path);
            Assert.IsTrue(creds.TryGet("sp-hauler-01", out var held));
            Assert.AreEqual("ffffffffffffffffffffffffffffffff", held);
        }

        [Test]
        public void ARejectedBatchRowFallsBackToo()
        {
            var o = Resolve("d", new List<CredentialRow> { Row("d-cred", "d", enabled: false) }, Search(Row("other", "d")));
            Assert.IsTrue(o.Ok);
            Assert.AreEqual(CredentialPath.Fallback, o.Path);
        }

        [Test]
        public void AMismatchedBatchRowIsNeverUsedAndTheReasonSurvivesAnEmptyFallback()
        {
            var creds = new DeviceCredentials();
            var o = Resolve("sp-hauler-01", new List<CredentialRow> { Row("sp-hauler-01-cred", "sp-hauler-02") }, Search(), creds);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, creds.Count);
            StringAssert.Contains("belongs to device sp-hauler-02", o.Reason);
        }

        [Test]
        public void TheFallbackRefusesToChooseBetweenTwoCredentials()
        {
            var creds = new DeviceCredentials();
            var o = Resolve("d", new List<CredentialRow>(), Search(Row("a", "d"), Row("b", "d")), creds);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, creds.Count);
            StringAssert.Contains("refusing to choose", o.Reason);
        }

        [Test]
        public void TheFallbackAppliesTheSameCrossCheck()
        {
            var o = Resolve("d", new List<CredentialRow>(), Search(Row("a", "other"), Row("b", "d", type: "MQTT_BASIC"), Row("c", "d", enabled: false)));
            Assert.IsFalse(o.Ok);
            StringAssert.Contains("no usable ACCESS_TOKEN credential", o.Reason);
        }

        [Test]
        public void VariablesAreWrittenAsJson()
        {
            var ids = VarsJson.StringList("tokens", new[] { "a-cred", "b\"q" });
            using var doc = System.Text.Json.JsonDocument.Parse(ids);
            Assert.AreEqual(2, doc.RootElement.GetProperty("tokens").GetArrayLength());
            Assert.AreEqual("b\"q", doc.RootElement.GetProperty("tokens")[1].GetString());

            using var s = System.Text.Json.JsonDocument.Parse(VarsJson.CredentialSearch("sp-hauler-01"));
            var c = s.RootElement.GetProperty("c");
            Assert.AreEqual("sp-hauler-01", c.GetProperty("device").GetString());
            Assert.AreEqual("ACCESS_TOKEN", c.GetProperty("credentialType").GetString());
            Assert.IsTrue(c.GetProperty("enabled").GetBoolean());
        }
    }

    public sealed class RedactorTests
    {
        const string Hex = "0123456789abcdef0123456789abcdef";

        static string Fingerprint(string jwt)
        {
            using var sha = SHA256.Create();
            var h = sha.ComputeHash(Encoding.UTF8.GetBytes(jwt));
            return string.Concat(h.Take(4).Select(b => b.ToString("x2")));
        }

        [Test]
        public void ACredentialShowsOnlyItsLastFour()
        {
            Assert.AreEqual("device sp-hauler-01 cred:…cdef connected", Redactor.Redact($"device sp-hauler-01 {Hex} connected"));
        }

        [Test]
        public void AnUppercaseCredentialIsRedactedToo()
        {
            Assert.AreEqual("cred:…CDEF", Redactor.Redact(Hex.ToUpperInvariant()));
        }

        [Test]
        public void AJwtBecomesAFingerprintOfItself()
        {
            var jwt = JwtExp(4102444800);
            StringAssert.StartsWith("eyJ", jwt);
            var line = Redactor.Redact($"Authorization: Bearer {jwt} sent");
            Assert.AreEqual($"Authorization: Bearer jwt:{Fingerprint(jwt)} sent", line);
            Assert.AreEqual(8, Fingerprint(jwt).Length);
        }

        [Test]
        public void ALineWithBothIsRedactedForBoth()
        {
            var jwt = JwtExp(4102444800);
            var line = Redactor.Redact($"op {jwt} dev {Hex}");
            StringAssert.DoesNotContain(jwt, line);
            StringAssert.DoesNotContain(Hex, line);
            StringAssert.Contains("jwt:" + Fingerprint(jwt), line);
            StringAssert.Contains("cred:…cdef", line);
        }

        [Test]
        public void ShortHexAndOrdinaryTextAreLeftAlone()
        {
            const string text = "SP-HL-0001 sp-hauler-01 deadbeef 0123456789abcdef0123456789abcde";
            Assert.AreEqual(text, Redactor.Redact(text));
            Assert.IsNull(Redactor.Redact(null));
            Assert.AreEqual("", Redactor.Redact(""));
        }

        static string JwtExp(long exp) => PlatformTestData.JwtExp(exp);
    }
}
