// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// A browser build has no command line, so its page address carries the few options a preview needs:
    /// <c>?hp-offline&amp;hp-start-hour=17.3&amp;hp-speed=4</c> becomes <c>-hp-offline -hp-start-hour 17.3 -hp-speed 4</c>. Only the options in
    /// <see cref="Flags"/> and <see cref="Valued"/> exist; any other <c>hp-</c> key is refused, never ignored, and keys that are not
    /// <c>hp-</c> ones (a host page's own) are left alone. Nothing here opens a file or names a runner: a page address is not trusted with those.
    /// </summary>
    public static class WebOptions
    {
        static readonly string[] Flags = { "hp-offline", "hp-portrait", "hp-frame-stats" };
        static readonly string[] Valued = { "hp-start-hour", "hp-speed", "hp-quit-after-sim" };

        /// <summary>The options a page address carries, as command-line arguments. <paramref name="problem"/> is set, and nothing is returned, when one is unknown or malformed.</summary>
        public static string[] FromUrl(string url, out string problem)
        {
            problem = null;
            var args = new List<string>();
            if (string.IsNullOrEmpty(url)) return args.ToArray();
            var at = url.IndexOf('?');
            if (at < 0) return args.ToArray();
            var query = url.Substring(at + 1);
            var hash = query.IndexOf('#');
            if (hash >= 0) query = query.Substring(0, hash);
            foreach (var pair in query.Split(new[] { '&' }, StringSplitOptions.RemoveEmptyEntries))
            {
                var eq = pair.IndexOf('=');
                string key, value;
                try
                {
                    key = Uri.UnescapeDataString(eq < 0 ? pair : pair.Substring(0, eq));
                    value = eq < 0 ? null : Uri.UnescapeDataString(pair.Substring(eq + 1));
                }
                catch (UriFormatException)
                {
                    problem = "the page address has a malformed option: " + pair;
                    return new string[0];
                }

                if (!key.StartsWith("hp-", StringComparison.Ordinal)) continue;
                if (Array.IndexOf(Flags, key) >= 0)
                {
                    if (!string.IsNullOrEmpty(value) && value != "1") { problem = key + " takes no value in a page address (or 1)"; return new string[0]; }
                    args.Add("-" + key);
                }
                else if (Array.IndexOf(Valued, key) >= 0)
                {
                    if (string.IsNullOrEmpty(value) || value.StartsWith("-", StringComparison.Ordinal)) { problem = key + " needs a value in the page address"; return new string[0]; }
                    args.Add("-" + key); args.Add(value);
                }
                else
                {
                    problem = "the page address has an option this build does not have: " + key;
                    return new string[0];
                }
            }

            return args.ToArray();
        }
    }
}
