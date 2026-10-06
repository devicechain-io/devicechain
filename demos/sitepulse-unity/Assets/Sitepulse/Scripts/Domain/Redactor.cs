// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>
    /// Every log line from the Platform code passes through here. A device credential is a 32-hex
    /// string and the operator token a JWT; neither may reach a log, a recording or the screen.
    /// A hex run of 32 or more is shown as its last four characters (enough to tell two apart in a
    /// log, useless to a thief) and a JWT as a short hash of itself. Longer-than-32 runs are
    /// redacted too: it errs towards hiding a hash rather than leaking a secret that has something
    /// stuck to it.
    /// </summary>
    public static class Redactor
    {
        // header starts eyJ ('{"'), three base64url segments; the signature may be empty
        static readonly Regex JwtShape = new Regex(@"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*", RegexOptions.CultureInvariant);
        static readonly Regex HexRun = new Regex(@"[0-9A-Fa-f]{32,}", RegexOptions.CultureInvariant);

        public static string Redact(string text)
        {
            if (string.IsNullOrEmpty(text)) return text;
            // JWTs first: a base64url segment can contain a hex-looking run
            text = JwtShape.Replace(text, m => "jwt:" + Fingerprint(m.Value));
            return HexRun.Replace(text, m => "cred:…" + m.Value.Substring(m.Value.Length - 4));
        }

        static string Fingerprint(string jwt)
        {
            using var sha = SHA256.Create();
            var hash = sha.ComputeHash(Encoding.UTF8.GetBytes(jwt));
            var sb = new StringBuilder(8);
            for (var i = 0; i < 4; i++) sb.Append(hash[i].ToString("x2"));
            return sb.ToString();
        }
    }
}
