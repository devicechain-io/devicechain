// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using DeviceChain.Demos.Platform;
using NUnit.Framework;

namespace DeviceChain.Demos.Tests
{
    // A device plane dials the broker and must refuse an unverified one; a watcher never dials it, so the same runner config is usable to it.
    public class RunnerConfigUseTests
    {
        static string Token()
        {
            var exp = DateTimeOffset.UtcNow.AddMinutes(15).ToUnixTimeSeconds();
            string B64(string s) => Convert.ToBase64String(Encoding.UTF8.GetBytes(s)).TrimEnd('=').Replace('+', '-').Replace('/', '_');
            return B64("{\"alg\":\"none\"}") + "." + B64("{\"exp\":" + exp + "}") + ".sig";
        }

        static string Config(bool insecure, bool withBroker) =>
            "{\"tenant\":\"sim-hp\",\"instanceId\":\"dc\",\"apiOrigin\":\"http://localhost:18080\",\"wsUrl\":\"ws://localhost:18080/api/event-management/graphql\"," +
            (withBroker ? "\"mqttBroker\":\"ssl://localhost:1883\"," : "") + "\"mqttTLSInsecure\":" + (insecure ? "true" : "false") + ",\"token\":\"" + Token() + "\"}";

        [Test]
        public void ADevicePlaneRefusesAnInsecureBroker()
        {
            var r = RunnerConfigParser.Parse(Config(true, true), null);
            Assert.That(r.Ok, Is.False);
            Assert.That(r.Error, Does.Contain("mqttTLSInsecure"));
        }

        [Test]
        public void AWatcherAcceptsAnInsecureBrokerItNeverDials()
        {
            var r = RunnerConfigParser.Parse(Config(true, true), null, RunnerUse.ObserveOnly);
            Assert.That(r.Ok, Is.True, r.Error);
            Assert.That(r.Value.MqttTlsInsecure, Is.True, "the runner's trust decision is still recorded");
        }

        [Test]
        public void OnlyAWatcherMayGoWithoutABroker()
        {
            Assert.That(RunnerConfigParser.Parse(Config(false, false), null).Ok, Is.False);
            Assert.That(RunnerConfigParser.Parse(Config(false, false), null, RunnerUse.ObserveOnly).Ok, Is.True);
        }

        [Test]
        public void AWatcherStillRefusesWhatItDoesUse()
        {
            var cfg = Config(true, true).Replace("ws://localhost:18080", "ftp://localhost:18080");
            Assert.That(RunnerConfigParser.Parse(cfg, null, RunnerUse.ObserveOnly).Ok, Is.False, "a wsUrl it cannot open is refused however the broker is");
            var wrongTenant = RunnerConfigParser.Parse(Config(true, true), new LiveSettings { ExpectTenant = "sim-other", Source = "test" }, RunnerUse.ObserveOnly);
            Assert.That(wrongTenant.Ok, Is.False);
        }
    }
}
