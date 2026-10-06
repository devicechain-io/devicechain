// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>Where the scene's values come from. Fixed at startup, never toggled.</summary>
    public enum SitepulseMode
    {
        /// <summary>The scripted preview; every value is illustrative. No network.</summary>
        Choreographed,

        /// <summary>Bound to a DeviceChain instance; values are observed from it.</summary>
        Live,

        /// <summary>A recorded live run. Not in this build.</summary>
        Replay,
    }

    /// <summary>
    /// Mode selection and the always-on badge. The mode is a startup argument because switching it
    /// means a different composition root: a hot toggle is how an illustrative value ends up on a
    /// card that says it is live. An unrecognised value is an error, never a default.
    /// </summary>
    public static class SitepulseModes
    {
        public const string Flag = "-sitepulse-mode";

        public static Parsed<SitepulseMode> Parse(string value)
        {
            switch ((value ?? "").Trim().ToLowerInvariant())
            {
                case "live": return Parsed<SitepulseMode>.Success(SitepulseMode.Live);
                case "choreographed": return Parsed<SitepulseMode>.Success(SitepulseMode.Choreographed);
                case "replay": return Parsed<SitepulseMode>.Success(SitepulseMode.Replay);
                default:
                    return Parsed<SitepulseMode>.Fail(
                        $"{Flag} \"{value}\" is not a mode; use live, choreographed or replay");
            }
        }

        /// <summary>The mode on the command line. <paramref name="present"/> is false when the flag is absent.</summary>
        public static Parsed<SitepulseMode> FromCommandLine(IReadOnlyList<string> args, out bool present)
        {
            var value = CommandLineArgs.Get(args, Flag, out var error);
            present = value != null || error != null;
            if (error != null) return Parsed<SitepulseMode>.Fail(error);
            if (value == null) return Parsed<SitepulseMode>.Success(SitepulseMode.Choreographed);
            return Parse(value);
        }

        /// <summary>
        /// The badge text. Live names its source only once the runner's config says which instance
        /// that is; before then it claims nothing it has not seen.
        /// </summary>
        public static string Badge(SitepulseMode mode, string tenant = null, string instance = null)
        {
            switch (mode)
            {
                case SitepulseMode.Live:
                    return string.IsNullOrEmpty(tenant) || string.IsNullOrEmpty(instance)
                        ? "LIVE · connecting to DeviceChain"
                        : $"LIVE · observed from DeviceChain · {tenant}@{instance}";
                case SitepulseMode.Choreographed:
                    return "CHOREOGRAPHED · illustrative values";
                case SitepulseMode.Replay:
                    return "REPLAY · not in this build";
                default:
                    throw new ArgumentOutOfRangeException(nameof(mode), mode, null);
            }
        }
    }
}
