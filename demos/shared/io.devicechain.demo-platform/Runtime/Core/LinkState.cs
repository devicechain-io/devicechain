// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

namespace DeviceChain.Demos.Core
{
    /// <summary>A session's state, as the device plane names it (the SDK's own enum stays behind the SDK-backed link).</summary>
    public enum LinkState { Starting, Ready, Reconnecting, Blind, Stopped }
}
