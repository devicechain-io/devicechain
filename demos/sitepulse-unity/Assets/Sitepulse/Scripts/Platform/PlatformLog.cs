// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Sitepulse.Domain;
using UnityEngine;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>The Platform code's only way to log. Every line passes the <see cref="Redactor"/>.</summary>
    public static class PlatformLog
    {
        public static void Info(string line) => Debug.Log("[sitepulse] " + Redactor.Redact(line));
        public static void Warn(string line) => Debug.LogWarning("[sitepulse] " + Redactor.Redact(line));
        public static void Error(string line) => Debug.LogError("[sitepulse] " + Redactor.Redact(line));
    }
}
