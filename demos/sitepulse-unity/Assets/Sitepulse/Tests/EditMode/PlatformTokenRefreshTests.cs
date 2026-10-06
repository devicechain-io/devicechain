// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;
using UnityEngine.TestTools;

namespace DeviceChain.Sitepulse.Tests
{
    // A refresh yields to the Unity context, so these run as coroutines and let the Editor tick
    // instead of blocking on the task.
    public class PlatformTokenRefreshTests
    {
        long now;
        int fetches;
        Func<string> serve;

        TokenBroker Broker(long exp)
        {
            fetches = 0;
            return new TokenBroker(new RunnerConfig { Token = PlatformTestData.JwtExp(exp) },
                _ =>
                {
                    fetches++;
                    return Task.FromResult(Parsed<RunnerConfig>.Success(new RunnerConfig { Token = serve() }));
                },
                () => DateTimeOffset.FromUnixTimeSeconds(now));
        }

        static IEnumerator Settle(int frames = 10)
        {
            for (var i = 0; i < frames; i++) yield return null;
        }

        [UnityTest]
        public IEnumerator ANewerTokenReplacesTheOldOne()
        {
            now = 10_000 - 100;                                   // inside the 120 s margin
            serve = () => PlatformTestData.JwtExp(10_900);
            var b = Broker(10_000);
            b.Tick();
            yield return Settle();
            Assert.AreEqual(1, fetches);
            Assert.AreEqual(DateTimeOffset.FromUnixTimeSeconds(10_900), b.ExpiresAt);
            Assert.AreEqual(TokenState.Fresh, b.State);
        }

        [UnityTest]
        public IEnumerator TheSameTokenBackDoesNotRefetchEveryFrame()
        {
            // the runner renews a minute before expiry, later than the broker asks
            now = 10_000 - 100;
            var held = PlatformTestData.JwtExp(10_000);
            serve = () => held;
            var b = Broker(10_000);
            for (var i = 0; i < 30; i++) { b.Tick(); yield return null; }
            Assert.AreEqual(1, fetches, "one ask, then wait for the retry interval");
            Assert.AreEqual(TokenState.Fresh, b.State);

            now += 16;                                            // past the retry interval
            serve = () => PlatformTestData.JwtExp(10_900);
            b.Tick();
            yield return Settle();
            Assert.AreEqual(2, fetches);
            Assert.AreEqual(DateTimeOffset.FromUnixTimeSeconds(10_900), b.ExpiresAt);
        }

        [UnityTest]
        public IEnumerator AfterA401TheSameTokenStaysExpired()
        {
            now = 5_000;
            var held = PlatformTestData.JwtExp(10_000);
            serve = () => held;
            var b = Broker(10_000);
            var pending = b.NotifyUnauthorized(held);
            while (!pending.IsCompleted) yield return null;
            Assert.AreEqual(1, fetches);
            Assert.AreEqual(TokenState.Expired, b.State);
            StringAssert.Contains("refused", b.Reason);
        }
    }
}
