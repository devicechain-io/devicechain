// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;
using DeviceChain.Demos.Core;

namespace DeviceChain.Demos.Platform
{
    /// <summary>
    /// The Platform code's only way to log. Every line passes the <see cref="Redactor"/>. Each line is
    /// tagged with <see cref="Prefix"/>, which a demo sets once to its own tag (<c>[sitepulse]</c>) so
    /// its player log reads the same whichever assembly wrote the line.
    /// </summary>
    public static class PlatformLog
    {
        /// <summary>The tag at the start of every line. A demo sets it before its first line is written.</summary>
        public static string Prefix { get; set; } = "[demo]";

        public static void Info(string line) => Debug.Log(Prefix + " " + Redactor.Redact(line));
        public static void Warn(string line) => Debug.LogWarning(Prefix + " " + Redactor.Redact(line));
        public static void Error(string line) => Debug.LogError(Prefix + " " + Redactor.Redact(line));
    }
}
