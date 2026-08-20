// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;

namespace DeviceChain.Sdk.Mqtt;

/// <summary>
/// The device plane's naming rules: the client id a device must present, and the topics it
/// publishes and subscribes on. Mirrors the platform's own definitions
/// (<c>core/messaging/mqtt_clientid.go</c>, <c>core/messaging/messaging.go</c>,
/// <c>core/streams/streams.go</c>).
/// </summary>
/// <remarks>
/// These are composed here rather than accepted as caller-supplied strings because the broker
/// enforces them and the failures are opaque from the client's side — a wrong client id is
/// refused at the auth callout, and a wrong topic is either refused by the minted grant or
/// silently dead-lettered downstream.
/// </remarks>
public static class DevicePlane
{
    /// <summary>
    /// Joins the fields of a device's MQTT client id.
    /// </summary>
    /// <remarks>
    /// It is ":" because the token grammar excludes it, which is what makes the split
    /// unambiguous — the platform's reasoning, carried across verbatim. A hyphen would not do:
    /// "acme-1:dev" and "acme:1-dev" are distinct, but "acme-1-dev" could be read either way.
    /// It is also what the CONNECT username already uses to carry a tenant.
    /// </remarks>
    private const string ClientIdSeparator = ":";

    /// <summary>The maximum length of an entity token, matching the platform's storage column.</summary>
    private const int MaxTokenLength = 128;

    /// <summary>
    /// Builds the MQTT client id a device authenticating as <paramref name="deviceToken"/> in
    /// <paramref name="tenant"/> connects with.
    /// </summary>
    /// <remarks>
    /// <para>
    /// 🔴 THE CLIENT ID IS NOT FREE-FORM AND THE AUTH CALLOUT REFUSES ANYTHING ELSE. An MQTT
    /// client id is a SESSION KEY: a new connection presenting an existing id TAKES OVER that
    /// session, disconnecting the incumbent. Deriving it from an already-authenticated identity
    /// is what stops one device evicting another's session — so this is composed from the
    /// shared rule, never written as a literal at a call site.
    /// </para>
    /// <para>
    /// The instance id is a field because MQTT sessions are keyed per ACCOUNT, not per instance:
    /// on a shared broker two instances share one account, so an id without it would make the
    /// same device token in two instances a single session.
    /// </para>
    /// <para>
    /// A device wanting more than one session appends <paramref name="discriminator"/>, which the
    /// callout admits after the same separator. That exists because the tidy-looking alternative
    /// — one id per device, exactly — makes a device's second connection evict its own first,
    /// which is what firmware does the moment it publishes on one connection and subscribes on
    /// another. The broker punishes the retry by jailing the pair as flappers, so the failure
    /// presents as an unexplained connect loop.
    /// </para>
    /// </remarks>
    /// <exception cref="ArgumentException">Any field is not token-grammar-safe.</exception>
    public static string DeviceClientId(string instanceId, string tenant, string deviceToken, string? discriminator = null)
    {
        RequireToken(instanceId, nameof(instanceId));
        RequireToken(tenant, nameof(tenant));
        RequireToken(deviceToken, nameof(deviceToken));

        var id = instanceId + ClientIdSeparator + tenant + ClientIdSeparator + deviceToken;
        if (string.IsNullOrEmpty(discriminator))
        {
            return id;
        }

        // The discriminator is deliberately unconstrained by the token grammar — it is chosen
        // inside a namespace the broker has already authenticated the device into, so it can
        // only ever collide with that same device's own sessions. It must not carry the NATS
        // metacharacters that would stop the id being a single subject token, though.
        foreach (var c in discriminator!)
        {
            if (c == '.' || c == '*' || c == '>' || char.IsWhiteSpace(c))
            {
                throw new ArgumentException(
                    $"an MQTT client id discriminator must not contain '.', '*', '>' or whitespace, got \"{discriminator}\"",
                    nameof(discriminator));
            }
        }

        return id + ClientIdSeparator + discriminator;
    }

    /// <summary>
    /// The CONNECT username for a device credential: <c>{tenant}:{credentialId}</c>. The password
    /// is EMPTY, which is what selects access-token credential mode at the callout.
    /// </summary>
    public static string ConnectUsername(string tenant, string credentialId)
    {
        RequireToken(tenant, nameof(tenant));
        if (string.IsNullOrEmpty(credentialId))
        {
            throw new ArgumentException("a credential id is required", nameof(credentialId));
        }

        return tenant + ":" + credentialId;
    }

    /// <summary>The topic a device publishes its own telemetry to.</summary>
    /// <remarks>
    /// The broker grant is minted from exactly this subject, so a device is confined to it — a
    /// publish anywhere else is refused rather than misrouted.
    /// </remarks>
    public static string EventsTopic(string instanceId, string tenant, string deviceToken)
    {
        RequireToken(instanceId, nameof(instanceId));
        RequireToken(tenant, nameof(tenant));
        RequireToken(deviceToken, nameof(deviceToken));
        return $"{instanceId}/{tenant}/devices/{deviceToken}/events";
    }

    /// <summary>The topic a device subscribes to for its own commands.</summary>
    public static string CommandsTopic(string instanceId, string tenant, string deviceToken)
    {
        RequireToken(instanceId, nameof(instanceId));
        RequireToken(tenant, nameof(tenant));
        RequireToken(deviceToken, nameof(deviceToken));
        return $"{instanceId}/{tenant}/device-commands/{deviceToken}";
    }

    /// <summary>The topic a device publishes its own command responses to.</summary>
    /// <remarks>
    /// <para>
    /// Device-scoped, symmetrically with <see cref="CommandsTopic"/>. The response still carries
    /// the command token, which is what correlates it back to the persisted command — but the
    /// device token in the topic is what says WHO answered, and the platform refuses a response
    /// for a command belonging to a different device.
    /// </para>
    /// <para>
    /// This topic was tenant-scoped before. A device's broker grant permitted publishing to it,
    /// so any device could report an outcome for any command in its tenant and the platform had
    /// no way to tell that the wrong device had answered. Sending a response on the old topic
    /// now fails the broker's authorization check rather than silently settling someone else's
    /// command.
    /// </para>
    /// </remarks>
    public static string CommandResponsesTopic(string instanceId, string tenant, string deviceToken)
    {
        RequireToken(instanceId, nameof(instanceId));
        RequireToken(tenant, nameof(tenant));
        RequireToken(deviceToken, nameof(deviceToken));
        return $"{instanceId}/{tenant}/command-responses/{deviceToken}";
    }

    /// <summary>
    /// Reports whether a token satisfies the platform's global security grammar: letters, digits,
    /// hyphen and underscore, starting with a letter or digit, at most 128 characters.
    /// </summary>
    /// <remarks>
    /// Hand-written rather than a <c>Regex</c> on purpose. It is a handful of comparisons, and it
    /// keeps the SDK's no-reflection guarantee unambiguous — <c>RegexOptions.Compiled</c> emits IL
    /// at runtime, which IL2CPP cannot do, and leaving a regex uncompiled to avoid that is a
    /// subtlety a future edit can quietly undo.
    /// </remarks>
    public static bool IsValidToken(string? token)
    {
        if (string.IsNullOrEmpty(token) || token!.Length > MaxTokenLength)
        {
            return false;
        }

        var first = token[0];
        if (!IsAsciiLetterOrDigit(first))
        {
            return false;
        }

        for (var i = 1; i < token.Length; i++)
        {
            var c = token[i];
            if (!IsAsciiLetterOrDigit(c) && c != '-' && c != '_')
            {
                return false;
            }
        }

        return true;
    }

    private static bool IsAsciiLetterOrDigit(char c) =>
        (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9');

    private static void RequireToken(string value, string parameterName)
    {
        if (!IsValidToken(value))
        {
            throw new ArgumentException(
                $"refusing to build a device-plane name from an invalid {parameterName}: " +
                "must be letters, digits, hyphens and underscores, starting with a letter or digit " +
                $"(got \"{value}\")",
                parameterName);
        }
    }
}
