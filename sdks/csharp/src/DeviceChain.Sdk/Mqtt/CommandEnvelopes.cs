// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Text.Json;
using System.Text.Json.Serialization;

namespace DeviceChain.Sdk.Mqtt;

/// <summary>
/// The JSON command-delivery publishes on a device's command topic. Mirrors
/// <c>command-delivery/processor.deliveryEnvelope</c>.
/// </summary>
/// <remarks>
/// Mirrored as a literal shape because the SDK speaks only the wire, exactly as the Go simulator's
/// receiver does. There is no shared schema artifact between the two sides; the field names below
/// ARE the contract.
/// </remarks>
public sealed class CommandDeliveryEnvelope
{
    /// <summary>The command's own token — what correlates a response back to the persisted command.</summary>
    [JsonPropertyName("token")]
    public string? Token { get; set; }

    /// <summary>The device the command is addressed to.</summary>
    [JsonPropertyName("deviceToken")]
    public string? DeviceToken { get; set; }

    /// <summary>The command name, as declared on the device profile.</summary>
    [JsonPropertyName("name")]
    public string? Name { get; set; }

    /// <summary>
    /// The command's parameters, carried as raw JSON so the SDK neither imposes a shape nor needs
    /// reflection to read one.
    /// </summary>
    [JsonPropertyName("payload")]
    public JsonElement? Payload { get; set; }

    /// <summary>
    /// Names the dispatch this frame is. The device echoes it in its answer, and the platform
    /// refuses an answer that carries none.
    /// </summary>
    /// <remarks>
    /// It is opaque: nothing on the device reads it, compares it, or should store it beyond the
    /// answer it is echoed into. It is the platform's evidence that this device received THIS
    /// dispatch — the same command can be published more than once, and only the nonce tells the
    /// two apart, so an answer echoing the wrong one settles nothing.
    /// </remarks>
    [JsonPropertyName("dispatchNonce")]
    public string? DispatchNonce { get; set; }
}

/// <summary>
/// The JSON a device publishes to report a command's outcome. Mirrors
/// <c>command-delivery/processor.responseEnvelope</c>; this is what drives the durable command
/// from SENT to SUCCESSFUL.
/// </summary>
public sealed class CommandResponseEnvelope
{
    /// <summary>The token of the command being answered.</summary>
    [JsonPropertyName("commandToken")]
    public string? CommandToken { get; set; }

    /// <summary>Whether the device carried the command out.</summary>
    [JsonPropertyName("success")]
    public bool Success { get; set; }

    /// <summary>An optional result payload.</summary>
    [JsonPropertyName("payload")]
    public string? Payload { get; set; }

    /// <summary>An optional failure reason, set when <see cref="Success"/> is false.</summary>
    [JsonPropertyName("error")]
    public string? Error { get; set; }

    /// <summary>
    /// The dispatch nonce echoed from the delivery envelope this answer is for. REQUIRED: the
    /// platform refuses a response that names no dispatch.
    /// </summary>
    /// <remarks>
    /// 🔴 IT IS THE NONCE OF THE FRAME BEING ANSWERED, NOT OF THE FRAME THAT RAN THE HANDLER, and
    /// the two differ exactly when it matters. A command whose publish reported an error is
    /// returned to the platform's queue and dispatched again under a NEW nonce; a device that
    /// already ran it must answer that redelivery with its remembered outcome — running the
    /// handler twice would move a machine twice — but under the nonce it has just been sent, or
    /// the answer names a dispatch the platform has moved off and settles nothing.
    /// </remarks>
    [JsonPropertyName("dispatchNonce")]
    public string? DispatchNonce { get; set; }
}

/// <summary>One command as handed to a device's handler.</summary>
public sealed class DeviceCommand
{
    /// <summary>Creates a command.</summary>
    public DeviceCommand(string token, string name, JsonElement? payload)
    {
        Token = token;
        Name = name;
        Payload = payload;
    }

    /// <summary>The command's token.</summary>
    public string Token { get; }

    /// <summary>The command name.</summary>
    public string Name { get; }

    /// <summary>The command's parameters as raw JSON, if any.</summary>
    public JsonElement? Payload { get; }
}

/// <summary>
/// What the device did with a command. This is the value that becomes the response envelope.
/// </summary>
/// <remarks>
/// 🔴 IT IS RETURNED BY THE HANDLER, NOT INFERRED FROM DELIVERY, AND THAT IS THE WHOLE POINT.
/// Acknowledging on receipt would drive a command to SUCCESSFUL the moment it arrived — reporting
/// that a machine acted when only the network did. A handler that cannot carry the command out
/// returns <see cref="Failed"/>, and the command is answered as failed rather than not answered:
/// silence is indistinguishable from a device that is gone, and would leave the command sitting at
/// SENT until it expired.
/// </remarks>
public sealed class CommandOutcome
{
    private CommandOutcome(bool success, string? payload, string? error)
    {
        Success = success;
        Payload = payload;
        Error = error;
    }

    /// <summary>Whether the device carried the command out.</summary>
    public bool Success { get; }

    /// <summary>An optional result payload.</summary>
    public string? Payload { get; }

    /// <summary>The failure reason, when unsuccessful.</summary>
    public string? Error { get; }

    /// <summary>The device accepted and carried out the command.</summary>
    public static CommandOutcome Succeeded(string? payload = null) => new(true, payload, null);

    /// <summary>The device could not carry out the command.</summary>
    public static CommandOutcome Failed(string error) => new(false, null, error);
}
