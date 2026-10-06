// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Platform
{
    public enum BindOutcome { Bound, Missing, Ambiguous, WrongType, ProfileMismatch }

    /// <summary>
    /// What the platform said about one scene device. <see cref="BindOutcome.Bound"/> carries the
    /// device's token and its profile's identity; everything else carries a reason a person can
    /// read. A bound device can still have <see cref="UnmappedAreas"/>: areas its goto-area command
    /// accepts that the scene has no geometry for, which the command path refuses at run time.
    /// </summary>
    public sealed class BindResult
    {
        public string ExternalId { get; set; }
        public BindOutcome Outcome { get; set; }
        public string DeviceToken { get; set; }
        public string TypeToken { get; set; }
        public string ProfileToken { get; set; }
        public int ActiveVersion { get; set; }

        /// <summary>How many devices carry this external ID (Ambiguous: more than one).</summary>
        public int Count { get; set; }

        public string Detail { get; set; }
        public IReadOnlyList<string> UnmappedAreas { get; set; } = Array.Empty<string>();

        public bool IsBound => Outcome == BindOutcome.Bound;

        /// <summary>The line the readiness panel shows for a device that is not bound.</summary>
        public string Describe(string tenant)
        {
            switch (Outcome)
            {
                case BindOutcome.Bound: return $"{ExternalId} · {DeviceToken}";
                case BindOutcome.Missing: return $"{ExternalId} · not on the platform · no device with this external ID in tenant {tenant}";
                case BindOutcome.Ambiguous: return $"{ExternalId} · ambiguous · {Count} devices share this external ID in tenant {tenant}";
                case BindOutcome.WrongType: return $"{ExternalId} · wrong device type · {Detail}";
                case BindOutcome.ProfileMismatch: return $"{ExternalId} · profile mismatch · {Detail}";
                default: throw new ArgumentOutOfRangeException();
            }
        }
    }
}
