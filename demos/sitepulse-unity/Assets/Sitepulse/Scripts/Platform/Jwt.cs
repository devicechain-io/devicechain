// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using System.Text.Json;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Reads the <c>exp</c> claim of a JWT WITHOUT verifying it. This is scheduling only: when to
    /// ask the runner for a fresh token. Nothing trusts the claim; the platform verifies the token
    /// on every request.
    /// </summary>
    public static class Jwt
    {
        public static bool TryGetExpiry(string jwt, out DateTimeOffset expiry)
        {
            expiry = default;
            if (string.IsNullOrEmpty(jwt)) return false;
            var parts = jwt.Split('.');
            if (parts.Length != 3) return false;
            try
            {
                var b64 = parts[1].Replace('-', '+').Replace('_', '/');
                b64 = b64.PadRight(b64.Length + (4 - b64.Length % 4) % 4, '=');
                using var doc = JsonDocument.Parse(Encoding.UTF8.GetString(Convert.FromBase64String(b64)));
                if (doc.RootElement.ValueKind != JsonValueKind.Object) return false;
                if (!doc.RootElement.TryGetProperty("exp", out var exp) || exp.ValueKind != JsonValueKind.Number) return false;
                if (!exp.TryGetInt64(out var seconds))
                {
                    if (!exp.TryGetDouble(out var d) || double.IsNaN(d) || double.IsInfinity(d)) return false;
                    seconds = (long)d;
                }

                expiry = DateTimeOffset.FromUnixTimeSeconds(seconds);
                return true;
            }
            catch (Exception e) when (e is FormatException || e is JsonException || e is ArgumentException)
            {
                return false;
            }
        }
    }
}
