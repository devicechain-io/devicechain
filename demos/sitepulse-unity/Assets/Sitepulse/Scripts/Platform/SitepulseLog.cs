// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Demos.Platform;
using UnityEngine;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Sitepulse's tag on its player log. The shared platform code writes through <see cref="PlatformLog"/>
    /// with whatever prefix it is given; this gives it <c>[sitepulse]</c>, so a line reads the same whichever
    /// assembly wrote it.
    /// </summary>
    public static class SitepulseLog
    {
        public const string Tag = "[sitepulse]";

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.SubsystemRegistration)]
        static void Install() => PlatformLog.Prefix = Tag;
    }
}
