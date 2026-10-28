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
using DeviceChain.Demos.Platform;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class PlatformCredentialTests
    {
        const string Id = "0123456789abcdef0123456789abcdef";

        [Test]
        public void ARowForTheDeviceIsAccepted()
        {
            Assert.IsNull(CredentialResolver.Check(Row("sp-hauler-01-sitepulse", "sp-hauler-01"), "sp-hauler-01"));
        }

        [Test]
        public void ARowForAnotherDeviceIsRefused()
        {
            var why = CredentialResolver.Check(Row("sp-hauler-01-sitepulse", "sp-hauler-02"), "sp-hauler-01");
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
        public void TheExistenceAndIssuedShapesParse()
        {
            var existing = CredentialResolver.ParseExisting(CredBatch(Cred("a-sitepulse", "a"), Cred("b-sitepulse", "b", enabled: false)));
            Assert.AreEqual(2, existing.Count);
            Assert.AreEqual("a", existing[0].DeviceToken);
            Assert.IsFalse(existing[1].Enabled);

            var issued = CredentialResolver.ParseIssued(Issued("createDeviceCredential", "a-sitepulse", "a", "tok"), "createDeviceCredential");
            Assert.AreEqual("tok", issued.CredentialId);
            Assert.Throws<System.FormatException>(() => CredentialResolver.ParseIssued("{}", "createDeviceCredential"));
        }

        static IReadOnlyList<BindResult> Bound(params string[] deviceTokens) =>
            deviceTokens.Select((t, i) => new BindResult { ExternalId = "SP-" + i, DeviceToken = t, Outcome = BindOutcome.Bound }).ToList();

        // the platform answers with whatever id the test says; the call it was asked to make is recorded
        static CredentialOutcome Resolve(string device, List<CredentialRow> existing, System.Func<string, CredentialRow> answer,
            DeviceCredentials into = null, List<(bool exists, string token)> calls = null) =>
            CredentialResolver.Resolve(Bound(device), existing, (d, exists, token) =>
            {
                calls?.Add((exists, token));
                return Task.FromResult(answer(token));
            }, into ?? new DeviceCredentials(), () => Id).GetAwaiter().GetResult().Single();

        static CredentialRow Echo(string token, string device = "d") => Row(device + "-sitepulse", device, id: token);

        [Test]
        public void AnAbsentCredentialIsCreatedAndTheEchoedTokenIsHeld()
        {
            var creds = new DeviceCredentials();
            var calls = new List<(bool, string)>();
            var o = Resolve("d", new List<CredentialRow>(), t => Echo(t), creds, calls);
            Assert.IsTrue(o.Ok);
            Assert.AreEqual(CredentialPath.Created, o.Path);
            Assert.AreEqual((false, Id), calls.Single());
            Assert.IsTrue(creds.TryGet("d", out var held));
            Assert.AreEqual(Id, held);
        }

        [Test]
        public void AnExistingCredentialIsRotatedNotRecreated()
        {
            var creds = new DeviceCredentials();
            var calls = new List<(bool, string)>();
            var o = Resolve("d", new List<CredentialRow> { Row("d-sitepulse", "d", id: CredentialResolver.FingerprintPrefix + "x") }, t => Echo(t), creds, calls);
            Assert.IsTrue(o.Ok);
            Assert.AreEqual(CredentialPath.Rotated, o.Path);
            Assert.AreEqual((true, Id), calls.Single());
            Assert.IsTrue(creds.TryGet("d", out _));
        }

        [Test]
        public void ADisabledExistingCredentialIsStillRotatedBecauseTheUpdateEnablesIt()
        {
            var o = Resolve("d", new List<CredentialRow> { Row("d-sitepulse", "d", enabled: false) }, t => Echo(t));
            Assert.IsTrue(o.Ok);
        }

        [Test]
        public void AFingerprintInPlaceOfTheTokenIsRefusedLoudly()
        {
            var creds = new DeviceCredentials();
            UnityEngine.TestTools.LogAssert.Expect(UnityEngine.LogType.Error, "[sitepulse] SP-0: the platform returned a fingerprint, not the token it was given, for d-sitepulse");
            var o = Resolve("d", new List<CredentialRow>(), _ => Echo("dctk1$abc"), creds);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, creds.Count);
            StringAssert.Contains("fingerprint", o.Reason);
        }

        [Test]
        public void AnotherTokenThanTheOneSentIsRefused()
        {
            var creds = new DeviceCredentials();
            UnityEngine.TestTools.LogAssert.Expect(UnityEngine.LogType.Error, "[sitepulse] SP-0: the platform did not return the token it was given for d-sitepulse");
            var o = Resolve("d", new List<CredentialRow>(), _ => Echo("someone-elses-token"), creds);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, creds.Count);
        }

        [Test]
        public void ARowForAnotherDeviceOrTypeIsNeverRewritten()
        {
            var calls = new List<(bool, string)>();
            UnityEngine.TestTools.LogAssert.Expect(UnityEngine.LogType.Error, "[sitepulse] SP-0: credential d-sitepulse belongs to device other, not d; not rewriting it");
            var o = Resolve("d", new List<CredentialRow> { Row("d-sitepulse", "other") }, t => Echo(t), null, calls);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, calls.Count);

            UnityEngine.TestTools.LogAssert.Expect(UnityEngine.LogType.Error, "[sitepulse] SP-0: credential d-sitepulse is MQTT_BASIC, not ACCESS_TOKEN; not rewriting it");
            o = Resolve("d", new List<CredentialRow> { Row("d-sitepulse", "d", type: "MQTT_BASIC") }, t => Echo(t), null, calls);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, calls.Count);
        }

        [Test]
        public void ARefusedMutationFailsThatDeviceWithTheReason()
        {
            var creds = new DeviceCredentials();
            UnityEngine.TestTools.LogAssert.Expect(UnityEngine.LogType.Error, "[sitepulse] SP-0: the platform refused to create d-sitepulse: FORBIDDEN");
            var o = Resolve("d", new List<CredentialRow>(), _ => throw new System.InvalidOperationException("FORBIDDEN"), creds);
            Assert.IsFalse(o.Ok);
            Assert.AreEqual(0, creds.Count);
            StringAssert.Contains("FORBIDDEN", o.Reason);
        }

        [Test]
        public void MintedTokensAreLongDistinctAndRedacted()
        {
            var a = CredentialResolver.MintToken();
            var b = CredentialResolver.MintToken();
            Assert.AreNotEqual(a, b);
            Assert.AreEqual(64, a.Length);
            Assert.IsTrue(a.All(c => (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')), a);
            Assert.IsFalse(a.StartsWith(CredentialResolver.FingerprintPrefix));
            // the shape is chosen so the redactor catches a token wherever it is printed
            StringAssert.DoesNotContain(a, Redactor.Redact("device d connected with " + a));
            StringAssert.DoesNotContain(a, Redactor.RedactForRecording("device d connected with " + a));
        }

        [Test]
        public void VariablesAreWrittenAsJson()
        {
            var ids = VarsJson.StringList("tokens", new[] { "a-sitepulse", "b\"q" });
            using var doc = System.Text.Json.JsonDocument.Parse(ids);
            Assert.AreEqual(2, doc.RootElement.GetProperty("tokens").GetArrayLength());
            Assert.AreEqual("b\"q", doc.RootElement.GetProperty("tokens")[1].GetString());

            using var c = System.Text.Json.JsonDocument.Parse(VarsJson.CredentialCreate("d-sitepulse", "d", "tok"));
            var r = c.RootElement.GetProperty("r");
            Assert.AreEqual("d-sitepulse", r.GetProperty("token").GetString());
            Assert.AreEqual("d", r.GetProperty("deviceToken").GetString());
            Assert.AreEqual("ACCESS_TOKEN", r.GetProperty("credentialType").GetString());
            Assert.AreEqual("tok", r.GetProperty("credentialId").GetString());
            Assert.IsTrue(r.GetProperty("enabled").GetBoolean());

            using var u = System.Text.Json.JsonDocument.Parse(VarsJson.CredentialRotate("d-sitepulse", "tok2"));
            Assert.AreEqual("d-sitepulse", u.RootElement.GetProperty("t").GetString());
            Assert.AreEqual("tok2", u.RootElement.GetProperty("r").GetProperty("credentialId").GetString());
            Assert.IsTrue(u.RootElement.GetProperty("r").GetProperty("enabled").GetBoolean());
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
