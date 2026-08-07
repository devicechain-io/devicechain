// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using Xunit;

namespace DeviceChain.Sdk.Tests;

/// <summary>
/// Fails the build if the real-broker tests were SKIPPED in an environment that promised to
/// provide a broker.
/// </summary>
/// <remarks>
/// <para>
/// 🔴 THIS EXISTS BECAUSE A SKIP READS AS A PASS. <see cref="MqttRealBrokerTests"/> skips itself
/// when <c>DC_MQTT_BROKER</c> is unset, which is right for a developer with no broker installed
/// and catastrophic in CI: if the step that starts nats-server silently stopped working — a moved
/// binary, a changed port, a broker that died on startup — every real-broker test would skip and
/// the job would stay green while testing nothing. "No failures" would have quietly come to mean
/// "never ran".
/// </para>
/// <para>
/// So CI sets <c>DC_REQUIRE_MQTT_BROKER=1</c> alongside the broker itself, and this test turns a
/// missing broker into a failure there while leaving a local run free to skip. The negative
/// control is trivial to perform and worth performing: unset <c>DC_MQTT_BROKER</c> in the job and
/// watch this fail.
/// </para>
/// </remarks>
public class MqttRealBrokerGuardTests
{
    [Fact]
    public void TheRealBrokerTestsMustNotBeSkippedWhereABrokerWasPromised()
    {
        var required = Environment.GetEnvironmentVariable("DC_REQUIRE_MQTT_BROKER");
        if (string.IsNullOrEmpty(required) || required == "0")
        {
            // No promise was made — a local run without a broker is legitimate.
            return;
        }

        var broker = Environment.GetEnvironmentVariable("DC_MQTT_BROKER");
        Assert.False(
            string.IsNullOrEmpty(broker),
            "DC_REQUIRE_MQTT_BROKER is set but DC_MQTT_BROKER is not, so every real-broker test " +
            "would silently skip and this job would pass while exercising no broker at all. " +
            "Fix the step that starts nats-server rather than relaxing this check.");
    }
}
