// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// Who the running demo is, for the few places the shared platform code has to say so: the prefix
    /// on a log line and the suffix on the access-token credential it creates for each device. A demo
    /// calls <see cref="Configure"/> once, before anything logs or binds, and every line it writes
    /// from then on carries its own name. Until it does the name is <see cref="DefaultName"/>.
    /// </summary>
    public static class DemoIdentity
    {
        public const string DefaultName = "demo";

        static volatile string name = DefaultName;

        /// <summary>The demo's short name: lower case, no spaces, as it appears in log lines and credential names.</summary>
        public static string Name => name;

        /// <summary>The prefix of a log line, with its trailing space.</summary>
        public static string LogPrefix => "[" + name + "] ";

        /// <summary>The suffix of the per-device credential this demo creates on the platform.</summary>
        public static string CredentialSuffix => "-" + name;

        public static void Configure(string demoName)
        {
            if (string.IsNullOrWhiteSpace(demoName)) throw new ArgumentException("a demo needs a name", nameof(demoName));
            foreach (var c in demoName)
                if (!(char.IsLower(c) || char.IsDigit(c) || c == '-'))
                    throw new ArgumentException("a demo name is lower case letters, digits and '-': " + demoName, nameof(demoName));
            name = demoName;
        }
    }
}
