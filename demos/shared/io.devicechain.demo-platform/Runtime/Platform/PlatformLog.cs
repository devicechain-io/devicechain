// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using UnityEngine;

namespace DeviceChain.Demos.Platform
{
    /// <summary>The Platform code's only way to log. Every line passes the <see cref="Redactor"/>.</summary>
    public static class PlatformLog
    {
        public static void Info(string line) => Debug.Log(DemoIdentity.LogPrefix + Redactor.Redact(line));
        public static void Warn(string line) => Debug.LogWarning(DemoIdentity.LogPrefix + Redactor.Redact(line));
        public static void Error(string line) => Debug.LogError(DemoIdentity.LogPrefix + Redactor.Redact(line));
    }
}
