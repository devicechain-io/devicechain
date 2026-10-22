// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A declaration-only stand-in for the one UnityEditor API the demo tests call. Never shipped, never
// run; see the other stubs for why they exist.
namespace UnityEditor
{
    public static class AssetDatabase
    {
        public static T LoadAssetAtPath<T>(string assetPath) where T : UnityEngine.Object => default;
    }
}
