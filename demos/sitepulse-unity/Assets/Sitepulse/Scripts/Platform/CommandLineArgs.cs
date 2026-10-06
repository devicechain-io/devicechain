// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>The player's command line, read for the few flags Sitepulse defines.</summary>
    public static class CommandLineArgs
    {
        /// <summary>
        /// The value after <paramref name="flag"/>, or null when the flag is absent. A flag with no
        /// value (last argument, or followed by another flag) is an error, never an empty string.
        /// </summary>
        public static string Get(IReadOnlyList<string> args, string flag, out string error)
        {
            error = null;
            if (args == null) return null;
            for (var i = 0; i < args.Count; i++)
            {
                if (args[i] != flag) continue;
                if (i + 1 >= args.Count || string.IsNullOrEmpty(args[i + 1]) || args[i + 1].StartsWith("-"))
                {
                    error = $"{flag} needs a value";
                    return null;
                }

                return args[i + 1];
            }

            return null;
        }
    }
}
