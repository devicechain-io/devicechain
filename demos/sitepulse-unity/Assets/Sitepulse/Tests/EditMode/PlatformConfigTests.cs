// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class PlatformConfigTests
    {
        static Parsed<RunnerConfig> Parse(Action<Dictionary<string, object>> edit = null, LiveSettings expect = null) =>
            RunnerConfigParser.Parse(PlatformTestData.ConfigJson(edit), expect);

        [Test]
        public void ValidConfigParses()
        {
            var r = Parse();
            Assert.IsTrue(r.Ok, r.Error);
            Assert.AreEqual("sim-sitepulse", r.Value.Tenant);
            Assert.AreEqual("sitepulse", r.Value.InstanceId);
            Assert.IsNull(r.Value.MqttTlsInsecure);
        }

        [TestCase("apiOrigin")]
        [TestCase("wsUrl")]
        [TestCase("mqttBroker")]
        [TestCase("instanceId")]
        [TestCase("tenant")]
        [TestCase("token")]
        public void EachRequiredFieldIsRequired(string field)
        {
            var r = Parse(d => d.Remove(field));
            Assert.IsFalse(r.Ok);
            StringAssert.Contains($"missing \"{field}\"", r.Error);
        }

        [TestCase("apiOrigin")]
        [TestCase("mqttBroker")]
        [TestCase("tenant")]
        public void ABlankFieldIsRefused(string field)
        {
            var r = Parse(d => d[field] = "  ");
            Assert.IsFalse(r.Ok);
            StringAssert.Contains($"\"{field}\" is blank", r.Error);
        }

        [Test]
        public void InsecureBrokerFlagAbsentOrFalseIsAccepted()
        {
            Assert.IsTrue(Parse().Ok);
            var r = Parse(d => d["mqttTLSInsecure"] = false);
            Assert.IsTrue(r.Ok, r.Error);
            Assert.AreEqual(false, r.Value.MqttTlsInsecure);
        }

        [Test]
        public void InsecureBrokerTrueIsRefused()
        {
            var r = Parse(d => d["mqttTLSInsecure"] = true);
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("mqttTLSInsecure", r.Error);
            StringAssert.Contains("pins", r.Error);
        }

        [Test]
        public void InsecureBrokerFlagMustBeABoolean()
        {
            var r = Parse(d => d["mqttTLSInsecure"] = "false");
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("must be a boolean", r.Error);
        }

        [Test]
        public void ATokenWithoutAReadableExpIsRefused()
        {
            var r = Parse(d => d["token"] = "not-a-jwt");
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("\"token\"", r.Error);
        }

        [Test]
        public void ExpectedInstanceAndTenantMustMatch()
        {
            var ok = Parse(expect: new LiveSettings { ExpectInstance = "sitepulse", ExpectTenant = "sim-sitepulse", Source = "live.json" });
            Assert.IsTrue(ok.Ok, ok.Error);

            var wrongInstance = Parse(expect: new LiveSettings { ExpectInstance = "other", Source = "live.json" });
            Assert.IsFalse(wrongInstance.Ok);
            StringAssert.Contains("expectInstance is \"other\" but the runner serves instance \"sitepulse\"", wrongInstance.Error);
            StringAssert.Contains("live.json", wrongInstance.Error);

            var wrongTenant = Parse(expect: new LiveSettings { ExpectTenant = "acme", Source = "live.json" });
            Assert.IsFalse(wrongTenant.Ok);
            StringAssert.Contains("expectTenant", wrongTenant.Error);
        }

        [Test]
        public void SeveralProblemsAreAllReported()
        {
            var r = Parse(d => { d.Remove("tenant"); d["mqttTLSInsecure"] = true; });
            StringAssert.Contains("missing \"tenant\"", r.Error);
            StringAssert.Contains("mqttTLSInsecure", r.Error);
        }

        [TestCase("")]
        [TestCase("not json")]
        [TestCase("[1,2]")]
        public void NonObjectBodiesAreRefused(string body)
        {
            Assert.IsFalse(RunnerConfigParser.Parse(body, null).Ok);
        }

        [Test]
        public void TheConfigNeverPrintsItsToken()
        {
            var r = Parse();
            Assert.IsFalse(r.Value.ToString().Contains(r.Value.Token));
            StringAssert.DoesNotContain("eyJ", r.Value.ToString());
        }

        // ---- live.json

        [Test]
        public void LiveJsonParses()
        {
            var r = LiveSettingsLoader.ParseJson("{\"runnerUrl\":\"http://localhost:8090\",\"caPemPath\":\"c:/ca.pem\",\"expectTenant\":\"sim-sitepulse\"}", "live.json");
            Assert.IsTrue(r.Ok, r.Error);
            Assert.AreEqual("http://localhost:8090", r.Value.RunnerUrl);
            Assert.AreEqual("sim-sitepulse", r.Value.ExpectTenant);
            Assert.IsNull(r.Value.ExpectInstance);
        }

        [TestCase("{\"caPemPath\":\"a\"}", "missing \"runnerUrl\"")]
        [TestCase("{\"runnerUrl\":\"http://x\"}", "missing \"caPemPath\"")]
        [TestCase("{\"runnerUrl\":\"http://x\",\"caPemPath\":\"a\",\"expectTenat\":\"t\"}", "unknown field \"expectTenat\"")]
        [TestCase("{\"runnerUrl\":5,\"caPemPath\":\"a\"}", "\"runnerUrl\" must be a string")]
        [TestCase("{broken", "not valid JSON")]
        [TestCase("[]", "must be a JSON object")]
        public void LiveJsonErrorsNameTheFileAndTheField(string json, string expected)
        {
            var r = LiveSettingsLoader.ParseJson(json, "C:/x/live.json");
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("C:/x/live.json", r.Error);
            StringAssert.Contains(expected, r.Error);
        }

        static Parsed<LiveSettings> Resolve(string[] args, Func<string, bool> exists, string fileText = null) =>
            LiveSettingsLoader.Resolve(args, "C:/data/live.json", exists, _ => fileText);

        [Test]
        public void CommandLineBeatsTheFile()
        {
            var r = Resolve(new[] { "-dc-runner", "http://localhost:8090", "-dc-ca", "c:/ca.pem" }, _ => true);
            Assert.IsTrue(r.Ok, r.Error);
            Assert.AreEqual("command line", r.Value.Source);
        }

        [Test]
        public void OneOfTheTwoCommandLineFlagsIsAnErrorNamingTheOther()
        {
            var r = Resolve(new[] { "-dc-runner", "http://localhost:8090" }, _ => true);
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("-dc-ca", r.Error);
            r = Resolve(new[] { "-dc-ca", "c:/ca.pem" }, _ => true);
            StringAssert.Contains("-dc-runner", r.Error);
        }

        [Test]
        public void AFlagWithNoValueIsAnError()
        {
            var r = Resolve(new[] { "-dc-runner" }, _ => true);
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("-dc-runner needs a value", r.Error);
        }

        [Test]
        public void AMissingFileNamesItsPath()
        {
            var r = Resolve(new string[0], _ => false);
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("C:/data/live.json", r.Error);
            StringAssert.Contains("live.example.json", r.Error);
        }

        [Test]
        public void TheFileIsUsedWhenThereIsNoCommandLine()
        {
            var r = Resolve(new string[0], _ => true, "{\"runnerUrl\":\"http://localhost:8090\",\"caPemPath\":\"c:/ca.pem\"}");
            Assert.IsTrue(r.Ok, r.Error);
            Assert.AreEqual("C:/data/live.json", r.Value.Source);
        }

        [Test]
        public void AMissingCaFileAndABadUrlAreReported()
        {
            var r = Resolve(new string[0], p => p.EndsWith("live.json"), "{\"runnerUrl\":\"ftp://x\",\"caPemPath\":\"c:/nope.pem\"}");
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("not an http(s) URL", r.Error);
            StringAssert.Contains("caPemPath \"c:/nope.pem\" does not exist", r.Error);
        }

        // ---- mode

        [TestCase("live", SitepulseMode.Live)]
        [TestCase("choreographed", SitepulseMode.Choreographed)]
        [TestCase("replay", SitepulseMode.Replay)]
        [TestCase("LIVE", SitepulseMode.Live)]
        public void KnownModesParse(string text, SitepulseMode mode)
        {
            var r = SitepulseModes.Parse(text);
            Assert.IsTrue(r.Ok);
            Assert.AreEqual(mode, r.Value);
        }

        [TestCase("")]
        [TestCase("liv")]
        [TestCase("record")]
        public void AnUnknownModeIsAnErrorNotADefault(string text)
        {
            var r = SitepulseModes.Parse(text);
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("is not a mode", r.Error);
        }

        [Test]
        public void ModeFromTheCommandLine()
        {
            var absent = SitepulseModes.FromCommandLine(new[] { "game.exe" }, out var present);
            Assert.IsTrue(absent.Ok);
            Assert.IsFalse(present);
            Assert.AreEqual(SitepulseMode.Choreographed, absent.Value);

            var live = SitepulseModes.FromCommandLine(new[] { "game.exe", "-sitepulse-mode", "live" }, out present);
            Assert.IsTrue(present);
            Assert.AreEqual(SitepulseMode.Live, live.Value);

            var bad = SitepulseModes.FromCommandLine(new[] { "game.exe", "-sitepulse-mode", "bogus" }, out present);
            Assert.IsTrue(present);
            Assert.IsFalse(bad.Ok);

            var noValue = SitepulseModes.FromCommandLine(new[] { "game.exe", "-sitepulse-mode" }, out present);
            Assert.IsTrue(present);
            Assert.IsFalse(noValue.Ok);
        }

        [Test]
        public void TheBadgeSaysWhatEachModeIs()
        {
            Assert.AreEqual("LIVE · observed from DeviceChain · sim-sitepulse@sitepulse", SitepulseModes.Badge(SitepulseMode.Live, "sim-sitepulse", "sitepulse"));
            Assert.AreEqual("CHOREOGRAPHED · illustrative values", SitepulseModes.Badge(SitepulseMode.Choreographed));
            StringAssert.StartsWith("REPLAY", SitepulseModes.Badge(SitepulseMode.Replay));
            // before the runner has said which instance, Live claims no source
            StringAssert.DoesNotContain("observed from", SitepulseModes.Badge(SitepulseMode.Live));
        }

        // ---- JWT

        [Test]
        public void JwtExpiryIsRead()
        {
            Assert.IsTrue(Jwt.TryGetExpiry(PlatformTestData.JwtExp(1790000000), out var exp));
            Assert.AreEqual(DateTimeOffset.FromUnixTimeSeconds(1790000000), exp);
            Assert.IsTrue(Jwt.TryGetExpiry(PlatformTestData.JwtWith("{\"exp\":1790000000.5}"), out exp));
        }

        [TestCase(null)]
        [TestCase("")]
        [TestCase("abc")]
        [TestCase("a.b")]
        [TestCase("a.b.c.d")]
        [TestCase("eyJhbGciOiJub25lIn0.!!!.sig")]
        public void MalformedJwtsAreRefused(string jwt)
        {
            Assert.IsFalse(Jwt.TryGetExpiry(jwt, out _));
        }

        [TestCase("{\"sub\":\"x\"}")]
        [TestCase("{\"exp\":\"soon\"}")]
        [TestCase("[1]")]
        [TestCase("not json")]
        public void AJwtWithoutANumericExpIsRefused(string payload)
        {
            Assert.IsFalse(Jwt.TryGetExpiry(PlatformTestData.JwtWith(payload), out _));
        }

        // ---- broker (only the paths that finish without a refresh: a refresh yields to the Unity context)

        static TokenBroker Broker(long exp, long now) =>
            new TokenBroker(new RunnerConfig { Token = PlatformTestData.JwtExp(exp) },
                _ => throw new InvalidOperationException("no refresh expected"),
                () => DateTimeOffset.FromUnixTimeSeconds(now));

        [Test]
        public void ABrokerRefusesAMalformedToken()
        {
            Assert.Throws<ArgumentException>(() => new TokenBroker(new RunnerConfig { Token = "x" }, _ => null));
        }

        [Test]
        public void ABrokerServesAFreshTokenWithoutRefreshing()
        {
            var b = Broker(exp: 10_000, now: 10_000 - 121);
            var token = b.Get(default).AsTask().GetAwaiter().GetResult();
            Assert.AreEqual(PlatformTestData.JwtExp(10_000), token);
            Assert.AreEqual(TokenState.Fresh, b.State);
            Assert.AreEqual(DateTimeOffset.FromUnixTimeSeconds(10_000), b.ExpiresAt);
        }

        [Test]
        public void ANotifyForAnAlreadyReplacedTokenDoesNothing()
        {
            var b = Broker(exp: 10_000, now: 1_000);
            Assert.IsTrue(b.NotifyUnauthorized("an older token").IsCompleted);
            Assert.AreEqual(TokenState.Fresh, b.State);
        }
    }
}
