// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using DeviceChain.Sdk.Subscriptions;
using UnityEngine;

namespace DeviceChain.Sdk.Unity
{
    /// <summary>
    /// Drives <see cref="ManualPumpDriver"/>s from the player loop — the Unity half of ADR-035
    /// slice 4.3. WebGL under IL2CPP has no thread pool, so a subscription's read loop cannot run
    /// on one; instead it advances one turn per <c>Update</c>, on the main thread.
    ///
    /// <see cref="DeviceChainRuntime.CreateClient(System.Uri)"/> attaches this automatically on the
    /// WebGL player, because a host that forgets to pump sees subscriptions that connect, ack, and
    /// then silently deliver nothing — the exact failure this slice exists to remove. A host that
    /// wants to own the cadence instead can construct <c>DeviceChainClient</c> with its own
    /// <see cref="ManualPumpDriver"/> and call <c>Pump()</c> wherever it likes.
    /// </summary>
    [DisallowMultipleComponent]
    public sealed class DeviceChainPump : MonoBehaviour
    {
        private static DeviceChainPump _instance;

        private readonly List<ManualPumpDriver> _drivers = new List<ManualPumpDriver>();

        /// <summary>
        /// Starts pumping <paramref name="driver"/> every frame, creating the host object on first
        /// use. Unity main thread only (it touches the scene). Attaching twice is a no-op.
        /// </summary>
        public static void Attach(ManualPumpDriver driver)
        {
            if (driver == null)
            {
                return;
            }
            if (_instance == null)
            {
                var go = new GameObject("DeviceChainPump") { hideFlags = HideFlags.HideAndDontSave };
                DontDestroyOnLoad(go); // subscriptions outlive a scene load; the loop must too
                _instance = go.AddComponent<DeviceChainPump>();
            }
            if (!_instance._drivers.Contains(driver))
            {
                _instance._drivers.Add(driver);
            }
        }

        /// <summary>Stops pumping <paramref name="driver"/>. Safe to call for one never attached.</summary>
        public static void Detach(ManualPumpDriver driver)
        {
            if (driver != null && _instance != null)
            {
                _instance._drivers.Remove(driver);
            }
        }

        private void Update()
        {
            // Indexed, and re-reading Count each turn: a pumped continuation can attach or detach a
            // driver, and an enumerator over the live list would throw when it did.
            for (int i = 0; i < _drivers.Count; i++)
            {
                _drivers[i].Pump();
            }
        }

        private void OnDestroy()
        {
            if (ReferenceEquals(_instance, this))
            {
                _instance = null;
            }
        }
    }
}
