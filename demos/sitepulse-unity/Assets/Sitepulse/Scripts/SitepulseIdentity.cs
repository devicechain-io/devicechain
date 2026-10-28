// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Demos.Platform;
using UnityEngine;

namespace DeviceChain.Sitepulse
{
    /// <summary>
    /// Tells the shared platform code which demo it is running in, before anything logs or binds a device: its log
    /// lines read <c>[sitepulse] ...</c> and the access-token credential it creates for each device is named
    /// <c>{deviceToken}-sitepulse</c>, as they always were. Registered for the player (before the first scene) and for
    /// the Editor (on every domain reload, which is when the EditMode tests start).
    /// </summary>
    public static class SitepulseIdentity
    {
        public const string Name = "sitepulse";

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.SubsystemRegistration)]
        static void RegisterForPlayer() => DemoIdentity.Configure(Name);

#if UNITY_EDITOR
        [UnityEditor.InitializeOnLoadMethod]
        static void RegisterForEditor() => DemoIdentity.Configure(Name);
#endif
    }
}
