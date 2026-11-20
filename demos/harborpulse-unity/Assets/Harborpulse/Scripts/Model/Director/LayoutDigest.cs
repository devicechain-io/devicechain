// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.Linq;
using System.Security.Cryptography;
using System.Text;

namespace DeviceChain.Harborpulse.Model.Director
{
    public static class LayoutDigest
    {
        public static string Compute(byte[] committedBytes)
        {
            var lf = Encoding.UTF8.GetString(committedBytes).Replace("\r\n", "\n");
            using (var sha = SHA256.Create())
                return string.Concat(sha.ComputeHash(Encoding.UTF8.GetBytes(lf)).Select(b => b.ToString("x2", CultureInfo.InvariantCulture)));
        }
        public static bool Matches(string local, string runner, out string reason)
        {
            if (string.IsNullOrEmpty(runner)) { reason = "runner reports no layout digest"; return false; }
            var match = string.Equals(local, runner, StringComparison.Ordinal);
            reason = match ? "" : "runner layout digest differs"; return match;
        }
        public static string Override(string local, string control)
        {
            if (control == null) return local;
            if (control.Length != 64 || control.Any(c => !Uri.IsHexDigit(c))) throw new FormatException("-hp-layout-digest-override requires 64 hex digits");
            return control.ToLowerInvariant();
        }
    }
}
