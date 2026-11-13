// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Commands;

/// <summary>One command handed to a device by an HTTP pull.</summary>
public sealed class PulledCommand
{
    /// <summary>The command's token: what the device quotes when it acknowledges and responds.</summary>
    [JsonPropertyName("token")]
    public string Token { get; set; } = "";

    /// <summary>The command key, one of the commands the device's profile declares.</summary>
    [JsonPropertyName("name")]
    public string Name { get; set; } = "";

    /// <summary>The command's parameters, or null when it was issued without any.</summary>
    [JsonPropertyName("payload")]
    public JsonElement? Payload { get; set; }

    /// <summary>
    /// <c>ONEWAY</c> when the acknowledgement completes the command and the device must not respond;
    /// null for an ordinary command the device also answers.
    /// </summary>
    [JsonPropertyName("deliveryMode")]
    public string? DeliveryMode { get; set; }

    /// <summary>
    /// How many times this command has been handed out, counting this one. Above 1 means an earlier
    /// pull of it was never acknowledged, so a device that cannot tell whether it already acted should
    /// treat the command as possibly repeated.
    /// </summary>
    [JsonPropertyName("attempt")]
    public int Attempt { get; set; }

    /// <summary>When the command expires (RFC 3339), or null when it does not.</summary>
    [JsonPropertyName("expiresAt")]
    public string? ExpiresAt { get; set; }
}

/// <summary>The body of a successful pull: <c>{"commands":[...]}</c>.</summary>
public sealed class PulledCommandList
{
    /// <summary>The commands this pull claimed, oldest first.</summary>
    [JsonPropertyName("commands")]
    public List<PulledCommand> Commands { get; set; } = new();
}

/// <summary>The body a device posts to report what it did with a pulled command.</summary>
public sealed class PulledCommandResponse
{
    /// <summary>True when the device carried the command out.</summary>
    [JsonPropertyName("success")]
    public bool Success { get; set; }

    /// <summary>The result data, or null for none.</summary>
    [JsonPropertyName("payload")]
    public JsonElement? Payload { get; set; }

    /// <summary>Why the command failed; read only when <see cref="Success"/> is false.</summary>
    [JsonPropertyName("error")]
    public string? Error { get; set; }
}

/// <summary>
/// Receives commands on the HTTP pull channel: for a device whose type has the <c>HTTP_PULL</c>
/// command channel and so holds no connection the platform can publish to. A device pulls, then
/// acknowledges each command <i>before</i> it acts on it, then reports what it did.
/// </summary>
/// <remarks>
/// <para>
/// The requests go to the device-plane HTTP listener (event-sources' ingest port), authenticated by
/// the device's access token in an <c>Authorization: Bearer</c> header; the token is never put in the
/// URL. A command whose acknowledgement does not arrive is handed out again by a later pull, up to
/// its attempt limit, and then fails as never delivered, so acknowledge promptly and idempotently
/// (the platform accepts a repeated acknowledgement).
/// </para>
/// </remarks>
public sealed class HttpCommandPuller
{
    /// <summary>The longest wait the platform holds a pull open for, in seconds.</summary>
    public const int MaxWaitSeconds = 30;

    /// <summary>The wait a pull uses when none is given, in seconds.</summary>
    public const int DefaultWaitSeconds = 25;

    private readonly IHttpTransport _transport;
    private readonly Uri _ingressOrigin;
    private readonly string _instanceId;
    private readonly string _tenant;
    private readonly string _deviceToken;
    private readonly string _accessToken;

    /// <param name="transport">The HTTP transport (default <see cref="HttpClientTransport"/>; Unity WebGL injects its own).</param>
    /// <param name="ingressOrigin">The device-plane ingress origin: the same listener that serves <c>POST /{instanceId}/{tenant}/events</c>.</param>
    /// <param name="instanceId">The instance segment.</param>
    /// <param name="tenant">The tenant segment.</param>
    /// <param name="deviceToken">The device pulling.</param>
    /// <param name="accessToken">The device's access token.</param>
    public HttpCommandPuller(IHttpTransport transport, Uri ingressOrigin, string instanceId, string tenant,
        string deviceToken, string accessToken)
    {
        _transport = transport ?? throw new ArgumentNullException(nameof(transport));
        _ingressOrigin = ingressOrigin ?? throw new ArgumentNullException(nameof(ingressOrigin));
        if (string.IsNullOrEmpty(instanceId)) throw new ArgumentException("instance id required", nameof(instanceId));
        if (string.IsNullOrEmpty(tenant)) throw new ArgumentException("tenant required", nameof(tenant));
        if (string.IsNullOrEmpty(deviceToken)) throw new ArgumentException("device token required", nameof(deviceToken));
        if (string.IsNullOrEmpty(accessToken)) throw new ArgumentException("access token required", nameof(accessToken));
        _instanceId = instanceId;
        _tenant = tenant;
        _deviceToken = deviceToken;
        _accessToken = accessToken;
    }

    /// <summary>
    /// Claims the commands waiting for the device, holding the request open up to
    /// <paramref name="waitSeconds"/> (0 to <see cref="MaxWaitSeconds"/>) when there are none. Returns an
    /// empty list when none arrived. Throws <see cref="GraphQlRequestException"/> (the SDK's transport
    /// error) on any other answer: 401 (token not accepted), 403 (the token is another device's, or the
    /// device's type has not opted in), 429 (too many requests held open, retry after
    /// <see cref="GraphQlRequestException.RetryAfter"/>) or 503 (retry).
    /// </summary>
    public async Task<IReadOnlyList<PulledCommand>> PullAsync(int waitSeconds = DefaultWaitSeconds,
        CancellationToken cancellationToken = default)
    {
        if (waitSeconds < 0 || waitSeconds > MaxWaitSeconds)
        {
            throw new ArgumentOutOfRangeException(nameof(waitSeconds), waitSeconds,
                $"wait must be between 0 and {MaxWaitSeconds} seconds");
        }
        string path = $"{CommandsPath()}?wait={waitSeconds.ToString(CultureInfo.InvariantCulture)}";
        HttpTransportResponse response = await SendAsync("GET", path, null, cancellationToken).ConfigureAwait(false);
        if (response.Status == 204) return Array.Empty<PulledCommand>();
        if (response.Status != 200) throw Refused(path, response);
        PulledCommandList? list = JsonSerializer.Deserialize(response.Body, SdkJson.Default.PulledCommandList);
        return list is null ? Array.Empty<PulledCommand>() : list.Commands;
    }

    /// <summary>
    /// Tells the platform the command reached the device. Send it <i>before</i> acting on the command.
    /// A repeated acknowledgement is accepted. Throws <see cref="GraphQlRequestException"/> on any
    /// answer but 204: 404 (not this device's command), 409 (the command is no longer waiting for an
    /// acknowledgement, for instance because it already failed as not delivered).
    /// </summary>
    public async Task AcknowledgeAsync(string commandToken, CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(commandToken)) throw new ArgumentException("command token required", nameof(commandToken));
        string path = $"{CommandsPath()}/{Uri.EscapeDataString(commandToken)}/ack";
        HttpTransportResponse response = await SendAsync("POST", path, null, cancellationToken).ConfigureAwait(false);
        if (response.Status != 204) throw Refused(path, response);
    }

    /// <summary>
    /// Reports what the device did with a command, settling it as successful or failed. Not for a
    /// <c>ONEWAY</c> command, which its acknowledgement already completed. Throws
    /// <see cref="GraphQlRequestException"/> on any answer but 204.
    /// </summary>
    /// <param name="commandToken">The command's token.</param>
    /// <param name="success">True when the device carried the command out.</param>
    /// <param name="payload">The result data, or null for none.</param>
    /// <param name="error">Why the command failed; read only when <paramref name="success"/> is false.</param>
    /// <param name="cancellationToken">Cancels the send.</param>
    public async Task RespondAsync(string commandToken, bool success, JsonElement? payload = null,
        string? error = null, CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(commandToken)) throw new ArgumentException("command token required", nameof(commandToken));
        string path = $"{CommandsPath()}/{Uri.EscapeDataString(commandToken)}/response";
        byte[] body = JsonSerializer.SerializeToUtf8Bytes(
            new PulledCommandResponse { Success = success, Payload = payload, Error = error },
            SdkJson.Default.PulledCommandResponse);
        HttpTransportResponse response = await SendAsync("POST", path, body, cancellationToken).ConfigureAwait(false);
        if (response.Status != 204) throw Refused(path, response);
    }

    private string CommandsPath() =>
        $"/{Uri.EscapeDataString(_instanceId)}/{Uri.EscapeDataString(_tenant)}/devices/{Uri.EscapeDataString(_deviceToken)}/commands";

    private async Task<HttpTransportResponse> SendAsync(string method, string path, byte[]? body,
        CancellationToken cancellationToken)
    {
        var request = new HttpTransportRequest
        {
            Uri = new Uri(_ingressOrigin, path),
            Method = method,
            Body = body,
            ContentType = body is null ? null : "application/json",
            BearerToken = _accessToken,
        };
        try
        {
            return await _transport.SendAsync(request, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception ex) when (ex is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
        {
            throw new GraphQlRequestException(ex.Message, 0);
        }
    }

    private static GraphQlRequestException Refused(string path, HttpTransportResponse response)
    {
        string detail = response.Body.Length == 0 ? "" : Encoding.UTF8.GetString(response.Body);
        return new GraphQlRequestException($"command channel {path} returned {response.Status}: {detail.Trim()}",
            response.Status, retryAfter: response.RetryAfter);
    }
}
