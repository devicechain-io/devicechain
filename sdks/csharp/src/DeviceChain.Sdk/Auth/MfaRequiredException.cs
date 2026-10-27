// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;

namespace DeviceChain.Sdk.Auth;

/// <summary>
/// Thrown by <see cref="AuthSession.LoginAsync"/> when the identity has a second factor: the
/// password was right, <b>no token was issued</b>, and the sign-in completes with
/// <see cref="AuthSession.VerifyMfaAsync"/> and a one-time code (or a recovery code).
/// <para>
/// It is a <see cref="GraphQlRequestException"/>, so code that already catches that type
/// (the server has always answered an enrolled sign-in with a GraphQL error) keeps catching it.
/// Do not call <see cref="AuthSession.LoginAsync"/> again to "retry": a second call mints a second
/// challenge and gets the same answer.
/// </para>
/// </summary>
public sealed class MfaRequiredException : GraphQlRequestException
{
    /// <summary>The code the server puts on this answer.</summary>
    public const string ErrorCode = "MFA_CHALLENGE";

    /// <summary>The handle <see cref="AuthSession.VerifyMfaAsync"/> takes. Single-use and short-lived.</summary>
    public string Challenge { get; }

    /// <summary>When the challenge stops being accepted, or null if the server sent none.</summary>
    public DateTimeOffset? ExpiresAt { get; }

    /// <summary>What the server accepts as the code: <c>TOTP</c> and <c>RECOVERY_CODE</c>.</summary>
    public IReadOnlyList<string> Methods { get; }

    internal MfaRequiredException(string message, int status, IReadOnlyList<GraphQlError> errors,
        string challenge, DateTimeOffset? expiresAt, IReadOnlyList<string> methods)
        : base(message, status, errors)
    {
        Challenge = challenge;
        ExpiresAt = expiresAt;
        Methods = methods;
    }

    // Recognizes the server's MFA_CHALLENGE answer: exactly one error, carrying the code and a
    // non-empty challenge. Anything else stays the plain GraphQlRequestException it always was, so a
    // malformed answer can never become a challenge with nothing to verify.
    internal static bool TryFrom(GraphQlRequestException ex, out MfaRequiredException? mfa)
    {
        mfa = null;
        if (ex.Errors.Count != 1 || ex.Errors[0].Code != ErrorCode || ex.Errors[0].ExtensionsJson is not { } json)
        {
            return false;
        }
        try
        {
            using var doc = JsonDocument.Parse(json);
            JsonElement root = doc.RootElement;
            if (root.ValueKind != JsonValueKind.Object
                || !root.TryGetProperty("challenge", out JsonElement c) || c.ValueKind != JsonValueKind.String
                || string.IsNullOrEmpty(c.GetString()))
            {
                return false;
            }
            DateTimeOffset? expires = null;
            if (root.TryGetProperty("expiresAt", out JsonElement e) && e.ValueKind == JsonValueKind.String
                && DateTimeOffset.TryParse(e.GetString(), CultureInfo.InvariantCulture,
                    DateTimeStyles.AssumeUniversal | DateTimeStyles.AdjustToUniversal, out DateTimeOffset parsed))
            {
                expires = parsed;
            }
            var methods = new List<string>();
            if (root.TryGetProperty("methods", out JsonElement m) && m.ValueKind == JsonValueKind.Array)
            {
                foreach (JsonElement item in m.EnumerateArray())
                {
                    if (item.ValueKind == JsonValueKind.String && item.GetString() is { } s)
                    {
                        methods.Add(s);
                    }
                }
            }
            mfa = new MfaRequiredException(
                "A second factor is required to finish signing in. Call VerifyMfaAsync with the challenge and a one-time code.",
                ex.Status, ex.Errors, c.GetString()!, expires, methods);
            return true;
        }
        catch (JsonException)
        {
            return false;
        }
    }
}
